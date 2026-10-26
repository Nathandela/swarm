package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	typeCursorReservation   RecordType = "cursor_reservation"
	liveCursorBlock                    = 64
	maxMigrationRecordBytes            = 1 << 20
	// shortcut: a session's cache stays off after 4096 unfinished evictions; revisit if native concurrency reaches this limit.
	maxSuppressedItems = 4096
)

type livePin struct {
	item    string
	expires time.Time
}

type liveItem struct {
	ID            string    `json:"item_id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	InteractionID string    `json:"interaction_id"`
	Expires       time.Time `json:"expires_at"`
	Process       string    `json:"process"`
}

// liveWriteAtomic commits both the replacement inode and its directory entry.
func liveWriteAtomic(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".live-journal-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncLiveDir(dir)
}

var writeLiveSegment = liveWriteAtomic

var syncLiveDir = func(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

var writeLiveReservation = func(j *Journal, ceiling uint64) error {
	line := append(EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: ceiling, Type: typeCursorReservation, TS: j.clock().UTC()}), '\n')
	if err := j.writeDurableLineLocked(line); err != nil {
		return err
	}
	j.active.bytes += int64(len(line))
	return nil
}

func (j *Journal) reserveCursorLocked() error {
	if j.cursor < j.reserved {
		return nil
	}
	if j.cursor > math.MaxUint64-liveCursorBlock {
		return errors.New("journal: cursor space exhausted")
	}
	ceiling := j.cursor + liveCursorBlock
	if j.active.bytes >= j.opts.MaxBytes {
		if err := j.rotateLocked(); err != nil {
			return err
		}
	}
	if err := writeLiveReservation(j, ceiling); err != nil {
		return err
	}
	j.reserved = ceiling
	return nil
}

// openLive streams legacy segments before replacing them; no whole-file allocation
// or interaction payload survives migration, and the ceiling commits before removal.
func (j *Journal) openLive() (*Journal, error) {
	if j.opts.MaxBytes <= 0 || j.opts.MaxFiles <= 0 || j.opts.MaxTotalBytes <= 0 {
		return nil, errors.New("journal: live mode requires bounded control retention")
	}
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	var maxSeq uint64
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".live-journal-") && !entry.IsDir() {
			if err := os.Remove(filepath.Join(j.dir, entry.Name())); err != nil {
				return nil, err
			}
			continue
		}
		if seq, ok := parseSegName(entry.Name()); ok && !entry.IsDir() {
			paths = append(paths, filepath.Join(j.dir, entry.Name()))
			maxSeq = max(maxSeq, seq)
		}
	}
	sort.Strings(paths)
	if maxSeq == math.MaxUint64 {
		return nil, errors.New("journal: segment sequence exhausted")
	}
	var controlBytes int64
	var lastControl uint64
	for _, path := range paths {
		err := streamSegment(path, func(r Record) error {
			j.cursor = max(j.cursor, r.Cursor)
			if r.Type == TypeInteraction || r.Type == typeCursorReservation || r.Cursor <= lastControl {
				return nil
			}
			lastControl = r.Cursor
			n := int64(len(EncodeRecord(r)) + 1)
			if n > j.opts.MaxTotalBytes {
				return errors.New("journal: control record exceeds retention budget")
			}
			j.records = append(j.records, r)
			controlBytes += n
			for controlBytes > j.opts.MaxTotalBytes {
				controlBytes -= int64(len(EncodeRecord(j.records[0])) + 1)
				j.records[0] = Record{}
				j.records = j.records[1:]
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	j.reserved, j.lostThrough = j.cursor, j.cursor
	var checkpoint []byte
	if j.reserved > lastControl {
		checkpoint = append(EncodeRecord(Record{SchemaVersion: SchemaVersion, Cursor: j.reserved, Type: typeCursorReservation, TS: j.clock().UTC()}), '\n')
	}
	if int64(len(checkpoint)) > j.opts.MaxTotalBytes {
		return nil, errors.New("journal: control budget cannot hold cursor reservation")
	}
	for controlBytes+int64(len(checkpoint)) > j.opts.MaxTotalBytes {
		controlBytes -= int64(len(EncodeRecord(j.records[0])) + 1)
		j.records[0] = Record{}
		j.records = j.records[1:]
	}
	var data bytes.Buffer
	var segments []*segment
	nextSeq := maxSeq + 1
	var lastCursor uint64
	flush := func() error {
		if nextSeq == 0 {
			return errors.New("journal: segment sequence exhausted")
		}
		name := segName(nextSeq)
		if err := writeLiveSegment(j.dir, name, data.Bytes()); err != nil {
			return err
		}
		segments = append(segments, &segment{seq: nextSeq, path: filepath.Join(j.dir, name), bytes: int64(data.Len()), lastCursor: lastCursor})
		nextSeq++
		data.Reset()
		lastCursor = 0
		return nil
	}
	for _, record := range j.records {
		line := append(EncodeRecord(record), '\n')
		if data.Len() > 0 && int64(data.Len()+len(line)) > j.opts.MaxBytes {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		data.Write(line)
		lastCursor = record.Cursor
	}
	// Old readers also observe the reserved ceiling, so rollback cannot reuse cursors.
	if data.Len() > 0 && int64(data.Len()+len(checkpoint)) > j.opts.MaxBytes {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	data.Write(checkpoint)
	if data.Len() > 0 || len(segments) == 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	if err := syncLiveDir(j.dir); err != nil {
		return nil, err
	}
	seg := segments[len(segments)-1]
	j.activeF, err = os.OpenFile(seg.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	j.active, j.segments, j.nextSeq = seg, segments, nextSeq
	j.enforceLiveRetentionLocked()
	if j.storageErr != nil {
		_ = j.activeF.Close()
		return nil, j.storageErr
	}
	return j, nil
}

func streamSegment(path string, visit func(Record) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if len(line)+len(chunk) > maxMigrationRecordBytes {
			return fmt.Errorf("journal: record exceeds migration limit in %s", filepath.Base(path))
		}
		line = append(line, chunk...)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(bytes.TrimSpace(line)) > 0 {
			r, decodeErr := DecodeRecord(line)
			if decodeErr != nil {
				if errors.Is(readErr, io.EOF) && !json.Valid(line) {
					return nil
				}
				return fmt.Errorf("journal: invalid migration record in %s: %w", filepath.Base(path), decodeErr)
			}
			if err := visit(r); err != nil {
				return err
			}
		}
		line = line[:0]
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func liveIdentity(r Record) (string, liveItem) {
	var item liveItem
	_ = json.Unmarshal(r.Payload, &item)
	return r.SessionID + "\x00" + item.ID, item
}

func (j *Journal) noteLiveItemLocked(r Record) {
	key, item := liveIdentity(r)
	if j.pins == nil {
		j.pins = make(map[string]livePin)
	}
	if j.suppressed == nil {
		j.suppressed = make(map[string]struct{})
	}
	if j.cacheDisabled == nil {
		j.cacheDisabled = make(map[string]bool)
	}
	if item.Kind == "approval_request" && item.Status == "in_progress" && item.ID != "" && item.Expires.After(j.clock()) {
		delete(j.suppressed, key)
		// A re-announcement replaces its card; retaining every version would grow a pinned item.
		kept := j.records[:0]
		for _, prior := range j.records {
			priorKey, _ := liveIdentity(prior)
			if prior.Type == TypeInteraction && priorKey == key && prior.Cursor != r.Cursor {
				j.liveBytes -= int64(len(EncodeRecord(prior)) + 1)
				j.lostThrough = max(j.lostThrough, prior.Cursor)
			} else {
				kept = append(kept, prior)
			}
		}
		clear(j.records[len(kept):])
		j.records = kept
		j.pins[r.SessionID] = livePin{item: item.ID, expires: item.Expires}
	}
	if pin, ok := j.pins[r.SessionID]; ok && item.Kind == "approval_resolved" && item.InteractionID == pin.item {
		delete(j.pins, r.SessionID)
	}
	if item.Kind == "approval_resolved" {
		resolved := r.SessionID + "\x00" + item.InteractionID
		delete(j.suppressed, resolved)
	}
	_, hidden := j.suppressed[key]
	if hidden || (j.cacheDisabled[r.SessionID] && item.Kind != "approval_request" && item.Kind != "approval_resolved") {
		j.records[len(j.records)-1] = Record{}
		j.records = j.records[:len(j.records)-1]
		j.liveBytes -= int64(len(EncodeRecord(r)) + 1)
		j.lostThrough = max(j.lostThrough, r.Cursor)
		if item.Status != "" && item.Status != "in_progress" {
			delete(j.suppressed, key)
		}
	}
	// The terminal marker follows the final item sweep; earlier clearing admits orphan tails.
	if item.Kind == "session_status" && (item.Process == "exited" || item.Process == "lost") {
		j.forgetLiveSessionLocked(r.SessionID)
	}
}

func (j *Journal) trimLiveLocked() {
	for session, pin := range j.pins {
		if !pin.expires.After(j.clock()) {
			delete(j.pins, session)
		}
	}
	for j.liveBytes > j.opts.LiveBytes {
		key := ""
		latest := liveItem{}
		for _, record := range j.records {
			if record.Type != TypeInteraction {
				continue
			}
			_, candidate := liveIdentity(record)
			if pin, ok := j.pins[record.SessionID]; ok && pin.item == candidate.ID {
				continue
			}
			key, latest = liveIdentity(record)
			break
		}
		if key == "" {
			break
		}
		kept := j.records[:0]
		for _, record := range j.records {
			if record.Type != TypeInteraction {
				kept = append(kept, record)
				continue
			}
			candidate, item := liveIdentity(record)
			if candidate == key {
				latest = item
				j.liveBytes -= int64(len(EncodeRecord(record)) + 1)
				j.lostThrough = max(j.lostThrough, record.Cursor)
			} else {
				kept = append(kept, record)
			}
		}
		clear(j.records[len(kept):])
		j.records = kept
		if latest.Status == "in_progress" && latest.Kind != "approval_request" {
			if len(j.suppressed) >= maxSuppressedItems {
				j.disableLiveCacheLocked(strings.SplitN(key, "\x00", 2)[0])
			} else {
				j.suppressed[key] = struct{}{}
			}
		}
	}
}

func (j *Journal) forgetLiveSessionLocked(session string) {
	delete(j.pins, session)
	delete(j.cacheDisabled, session)
	for key := range j.suppressed {
		if strings.HasPrefix(key, session+"\x00") {
			delete(j.suppressed, key)
		}
	}
}

func (j *Journal) disableLiveCacheLocked(session string) {
	j.cacheDisabled[session] = true
	for key := range j.suppressed {
		if strings.HasPrefix(key, session+"\x00") {
			delete(j.suppressed, key)
		}
	}
	kept := j.records[:0]
	for _, r := range j.records {
		if r.Type != TypeInteraction || r.SessionID != session {
			kept = append(kept, r)
			continue
		}
		_, item := liveIdentity(r)
		if item.Kind != "approval_request" && item.Kind != "approval_resolved" {
			j.liveBytes -= int64(len(EncodeRecord(r)) + 1)
			j.lostThrough = max(j.lostThrough, r.Cursor)
		} else {
			kept = append(kept, r)
		}
	}
	clear(j.records[len(kept):])
	j.records = kept
}

func (j *Journal) enforceLiveRetentionLocked() {
	var total int64
	for _, seg := range j.segments {
		total += seg.bytes
	}
	var through uint64
	for len(j.segments) > 1 && (len(j.segments) > j.opts.MaxFiles || total > j.opts.MaxTotalBytes) {
		old := j.segments[0]
		if err := os.Remove(old.path); err != nil {
			_ = j.failStorageLocked(fmt.Errorf("retire control segment: %w", err))
			return
		}
		total -= old.bytes
		through = max(through, old.lastCursor)
		j.segments = j.segments[1:]
	}
	if through == 0 {
		return
	}
	j.lostThrough = max(j.lostThrough, through)
	kept := j.records[:0]
	for _, record := range j.records {
		if record.Type == TypeInteraction || record.Cursor > through {
			kept = append(kept, record)
		}
	}
	clear(j.records[len(kept):])
	j.records = kept
}
