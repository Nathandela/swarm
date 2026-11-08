package voice

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

type Store struct {
	mu           sync.Mutex
	cached       State
	header       storeHeader
	transaction  int
	db           *bolt.DB
	generation   uint64
	beforeCommit func() error // transaction fault injection; production leaves nil
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, ErrInvalid
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		_ = f.Close()
		return nil, ErrInvalid
	}
	err = f.Chmod(0700)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	// This independent marker distinguishes a new installation from lost authority.
	markerPath := filepath.Join(dir, "voice.initialized")
	markerExists := false
	marker, markerErr := os.OpenFile(markerPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if markerErr == nil {
		markerInfo, statErr := marker.Stat()
		if statErr != nil || !privateRegular(markerInfo) || markerInfo.Mode().Perm() != 0600 {
			_ = marker.Close()
			return nil, ErrCorrupt
		}
		_ = marker.Close()
		markerExists = true
	} else if !os.IsNotExist(markerErr) {
		return nil, ErrCorrupt
	}
	if _, statErr := os.Lstat(filepath.Join(dir, "voice.db")); markerExists && os.IsNotExist(statErr) {
		return nil, ErrCorrupt
	}
	created := false
	db, err := bolt.Open(filepath.Join(dir, "voice.db"), 0600, &bolt.Options{Timeout: 100 * time.Millisecond, OpenFile: func(path string, flag int, mode os.FileMode) (*os.File, error) {
		return openStoreFile(path, flag, mode, &created)
	}})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("controller"))
		var state State
		if b == nil {
			if !created {
				return ErrCorrupt
			}
			// An existing nonempty database with missing authority must never become a new namespace.
			c := tx.Cursor()
			if k, _ := c.First(); k != nil {
				return ErrCorrupt
			}
			var err error
			_, err = tx.CreateBucket([]byte("controller"))
			if err != nil {
				return err
			}
			state = emptyState()
		} else {
			var err error
			state, err = readState(tx)
			if err != nil {
				return err
			}
		}
		if state.Generation == ^uint64(0) {
			return ErrCapacity
		}
		state.Generation++
		s.generation = state.Generation
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if len(raw) > MaxStoreBytes {
			return ErrCapacity
		}
		return writeStateRecord(tx, raw)
	})
	if err == nil && !markerExists {
		marker, createErr := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if createErr == nil {
			_, createErr = marker.WriteString("voice-store-v1\n")
			if createErr == nil {
				createErr = marker.Sync()
			}
			_ = marker.Close()
		}
		err = createErr
	}
	if err == nil {
		for _, p := range []string{dir, filepath.Dir(filepath.Clean(dir))} {
			f, e := os.Open(p)
			if e != nil {
				err = e
				break
			}
			e = f.Sync()
			_ = f.Close()
			if e != nil {
				err = e
				break
			}
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = db.View(func(tx *bolt.Tx) error { return s.load(tx) }); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func openStoreFile(path string, flag int, mode os.FileMode, created *bool) (*os.File, error) {
	entry, err := os.Lstat(path)
	if err == nil {
		if !privateRegular(entry) {
			return nil, ErrInvalid
		}
		flag &^= os.O_CREATE
	} else if os.IsNotExist(err) {
		flag |= os.O_EXCL
		*created = true
	} else {
		return nil, err
	}
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !privateRegular(opened) || (entry != nil && !os.SameFile(entry, opened)) {
		_ = f.Close()
		return nil, ErrInvalid
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
func privateRegular(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid()) && st.Nlink == 1 && info.Mode().IsRegular()
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}
func emptyState() State {
	return State{Version: 1, Conversations: map[string]Conversation{}, Utterances: map[string]Utterance{}, Admissions: map[string]Admission{}, Actions: map[string]Action{}, Requests: map[string]Request{}, ClosedTurns: map[string]bool{}, CancelledTasks: map[string]bool{}, Permits: map[string]SpeechPermit{}, Publications: map[string]Publication{}, Journal: map[string]JournalCursor{}, JournalSnapshots: map[string]JournalSnapshot{}, Events: map[string]Event{}, Wakes: map[string]Wake{}}
}

// The root checksum covers the keyed record set, including deletion or substitution.
// LogicalBytes retains the exact full-State JSON limit, independent of bbolt pages.
type storeHeader struct {
	Version             int               `json:"version"`
	Generation          uint64            `json:"generation"`
	CurrentConversation string            `json:"current_conversation"`
	LogicalBytes        int               `json:"logical_bytes"`
	Records             int               `json:"records"`
	RecordHash          [sha256.Size]byte `json:"record_hash"`
}
type stateChanges struct {
	tables map[string]map[string]any
	counts map[string]int
}

// Copy only a table being changed; failed commits leave the verified cache intact.
func putRecord[T any](st *State, table string, records *map[string]T, k string, value T) {
	touchTable(st, table, records)
	(*records)[k] = value
	st.changes.tables[table][k] = value
}
func deleteRecord[T any](st *State, table string, records *map[string]T, k string) {
	touchTable(st, table, records)
	delete(*records, k)
	st.changes.tables[table][k] = nil
}
func touchTable[T any](st *State, table string, records *map[string]T) {
	if st.changes.tables[table] == nil {
		st.changes.tables[table] = make(map[string]any)
		st.changes.counts[table] = len(*records)
		*records = maps.Clone(*records)
	}
}
func recordKey(table, k string) []byte { return []byte(table + "\x00" + k) }
func xorRecord(sum *[sha256.Size]byte, k, raw []byte) {
	h := sha256.New()
	_, _ = h.Write(k)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(raw)
	for i, v := range h.Sum(nil) {
		sum[i] ^= v
	}
}
func readHeader(tx *bolt.Tx) (storeHeader, error) {
	var header storeHeader
	b := tx.Bucket([]byte("controller"))
	if b == nil {
		return header, ErrCorrupt
	}
	raw := b.Get([]byte("state"))
	if string(b.Get([]byte("digest"))) != hashBytes(raw) || json.Unmarshal(raw, &header) != nil || header.Version != 2 || header.Generation == 0 || header.LogicalBytes > MaxStoreBytes || header.Records > MaxRecords {
		return header, ErrCorrupt
	}
	return header, nil
}
func readState(tx *bolt.Tx) (State, error) {
	header, err := readHeader(tx)
	if err != nil {
		return State{}, err
	}
	st := emptyState()
	st.Generation, st.CurrentConversation = header.Generation, header.CurrentConversation
	records := tx.Bucket([]byte("controller")).Bucket([]byte("records"))
	if records == nil {
		return State{}, ErrCorrupt
	}
	var sum [sha256.Size]byte
	err = records.ForEach(func(k, raw []byte) error {
		table, id, ok := strings.Cut(string(k), "\x00")
		if !ok || raw == nil {
			return ErrCorrupt
		}
		xorRecord(&sum, k, raw)
		switch table {
		case "conversations":
			return decodeRecord(st.Conversations, id, raw)
		case "utterances":
			return decodeRecord(st.Utterances, id, raw)
		case "admissions":
			return decodeRecord(st.Admissions, id, raw)
		case "actions":
			return decodeRecord(st.Actions, id, raw)
		case "closed_turns":
			return decodeRecord(st.ClosedTurns, id, raw)
		case "cancelled_tasks":
			return decodeRecord(st.CancelledTasks, id, raw)
		case "requests":
			return decodeRecord(st.Requests, id, raw)
		case "permits":
			return decodeRecord(st.Permits, id, raw)
		case "publications":
			return decodeRecord(st.Publications, id, raw)
		case "journal":
			return decodeRecord(st.Journal, id, raw)
		case "journal_snapshots":
			return decodeRecord(st.JournalSnapshots, id, raw)
		case "events":
			return decodeRecord(st.Events, id, raw)
		case "wakes":
			return decodeRecord(st.Wakes, id, raw)
		default:
			return ErrCorrupt
		}
	})
	if err != nil {
		return State{}, err
	}
	raw, err := json.Marshal(st)
	if err != nil || len(raw) != header.LogicalBytes || countRecords(st) != header.Records || sum != header.RecordHash {
		return State{}, ErrCorrupt
	}
	return st, nil
}
func decodeRecord[T any](records map[string]T, k string, raw []byte) error {
	var value T
	if json.Unmarshal(raw, &value) != nil {
		return ErrCorrupt
	}
	records[k] = value
	return nil
}
func writeHeader(tx *bolt.Tx, header storeHeader) error {
	raw, err := json.Marshal(header)
	if err != nil {
		return err
	}
	b := tx.Bucket([]byte("controller"))
	if err = b.Put([]byte("state"), raw); err != nil {
		return err
	}
	return b.Put([]byte("digest"), []byte(hashBytes(raw)))
}

// Full replacement is used only at startup and by storage fixtures.
func writeStateRecord(tx *bolt.Tx, raw []byte) error {
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	b := tx.Bucket([]byte("controller"))
	if b.Bucket([]byte("records")) != nil {
		if err := b.DeleteBucket([]byte("records")); err != nil {
			return err
		}
	}
	records, err := b.CreateBucket([]byte("records"))
	if err != nil {
		return err
	}
	header := storeHeader{Version: 2, Generation: st.Generation, CurrentConversation: st.CurrentConversation, LogicalBytes: len(raw), Records: countRecords(st)}
	if err = writeRecords(records, "conversations", st.Conversations, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "utterances", st.Utterances, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "admissions", st.Admissions, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "actions", st.Actions, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "closed_turns", st.ClosedTurns, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "cancelled_tasks", st.CancelledTasks, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "requests", st.Requests, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "permits", st.Permits, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "publications", st.Publications, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "journal", st.Journal, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "journal_snapshots", st.JournalSnapshots, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "events", st.Events, &header.RecordHash); err != nil {
		return err
	}
	if err = writeRecords(records, "wakes", st.Wakes, &header.RecordHash); err != nil {
		return err
	}
	return writeHeader(tx, header)
}
func writeRecords[T any](b *bolt.Bucket, table string, records map[string]T, sum *[sha256.Size]byte) error {
	for k, value := range records {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		rk := recordKey(table, k)
		xorRecord(sum, rk, raw)
		if err = b.Put(rk, raw); err != nil {
			return err
		}
	}
	return nil
}
func countRecords(st State) int {
	return len(st.Conversations) + len(st.Utterances) + len(st.Admissions) + len(st.Actions) + len(st.Requests) + len(st.ClosedTurns) + len(st.CancelledTasks) + len(st.Permits) + len(st.Publications) + len(st.Journal) + len(st.JournalSnapshots) + len(st.Events) + len(st.Wakes)
}
func (s *Store) load(tx *bolt.Tx) error {
	if tx.ID() == s.transaction && s.cached.Generation != 0 {
		return nil
	}
	st, err := readState(tx)
	if err != nil {
		return err
	}
	header, err := readHeader(tx)
	if err != nil {
		return err
	}
	s.cached, s.header, s.transaction = st, header, tx.ID()
	return nil
}
func stateBaseBytes(st State) int {
	base := emptyState()
	base.Generation, base.CurrentConversation = st.Generation, st.CurrentConversation
	raw, _ := json.Marshal(base)
	return len(raw)
}
func entryBytes(k string, raw []byte) int {
	encoded, _ := json.Marshal(k)
	return len(encoded) + 1 + len(raw)
}

// Map writes in fn must use putRecord/deleteRecord to stage persistence and isolate rollback.
func (s *Store) update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st State
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := s.load(tx); err != nil {
			return err
		}
		if s.cached.Generation != s.generation {
			return ErrStale
		}
		st = s.cached
		st.changes = &stateChanges{tables: make(map[string]map[string]any), counts: make(map[string]int)}
		return fn(&st)
	})
	if err != nil {
		return err
	}
	if countRecords(st) > MaxRecords {
		return ErrCapacity
	}
	if len(st.changes.tables) == 0 && st.Generation == s.cached.Generation && st.CurrentConversation == s.cached.CurrentConversation {
		return nil
	}
	header := s.header
	header.Generation, header.CurrentConversation = st.Generation, st.CurrentConversation
	header.LogicalBytes += stateBaseBytes(st) - stateBaseBytes(s.cached)
	header.Records = countRecords(st)
	changed := st.Generation != s.cached.Generation || st.CurrentConversation != s.cached.CurrentConversation
	err = s.db.Update(func(tx *bolt.Tx) error {
		if tx.ID() != s.transaction+1 {
			return ErrStale
		}
		records := tx.Bucket([]byte("controller")).Bucket([]byte("records"))
		for table, changes := range st.changes.tables {
			oldCount := st.changes.counts[table]
			newCount := oldCount
			for k, value := range changes {
				rk := recordKey(table, k)
				old := records.Get(rk)
				var raw []byte
				if value != nil {
					var err error
					raw, err = json.Marshal(value)
					if err != nil {
						return err
					}
				}
				if bytes.Equal(old, raw) {
					continue
				}
				changed = true
				if old != nil {
					header.LogicalBytes -= entryBytes(k, old)
					xorRecord(&header.RecordHash, rk, old)
					newCount--
				}
				if raw != nil {
					header.LogicalBytes += entryBytes(k, raw)
					xorRecord(&header.RecordHash, rk, raw)
					newCount++
					if err := records.Put(rk, raw); err != nil {
						return err
					}
				} else if err := records.Delete(rk); err != nil {
					return err
				}
			}
			header.LogicalBytes += max(newCount-1, 0) - max(oldCount-1, 0)
		}
		if header.LogicalBytes > MaxStoreBytes {
			return ErrCapacity
		}
		if !changed {
			return errNoStoreChange
		}
		if err := writeHeader(tx, header); err != nil {
			return err
		}
		if s.beforeCommit != nil {
			return s.beforeCommit()
		}
		return nil
	})
	if err == errNoStoreChange {
		return nil
	}
	if err == bolt.ErrKeyTooLarge {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	st.changes = nil
	s.cached, s.header = st, header
	s.transaction++
	return nil
}

var errNoStoreChange = fmt.Errorf("voice: unchanged transaction")

func (s *Store) Snapshot() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st State
	err := s.db.View(func(tx *bolt.Tx) error {
		if err := s.load(tx); err != nil {
			return err
		}
		st = cloneState(s.cached)
		return nil
	})
	return st, err
}
func cloneState(st State) State {
	st.Conversations = maps.Clone(st.Conversations)
	st.Utterances = maps.Clone(st.Utterances)
	st.Admissions = maps.Clone(st.Admissions)
	st.Actions = maps.Clone(st.Actions)
	st.ClosedTurns = maps.Clone(st.ClosedTurns)
	st.CancelledTasks = maps.Clone(st.CancelledTasks)
	st.Requests = maps.Clone(st.Requests)
	st.Permits = maps.Clone(st.Permits)
	st.Publications = maps.Clone(st.Publications)
	st.Journal = maps.Clone(st.Journal)
	st.JournalSnapshots = maps.Clone(st.JournalSnapshots)
	st.Events = maps.Clone(st.Events)
	st.Wakes = maps.Clone(st.Wakes)
	for k, v := range st.Actions {
		st.Actions[k] = cloneAction(v)
	}
	for k, v := range st.Requests {
		st.Requests[k] = cloneRequest(v)
	}
	for k, v := range st.Events {
		v.Metadata = bytes.Clone(v.Metadata)
		st.Events[k] = v
	}
	for k, v := range st.JournalSnapshots {
		v.Metadata = bytes.Clone(v.Metadata)
		st.JournalSnapshots[k] = v
	}
	for k, v := range st.Wakes {
		st.Wakes[k] = cloneWake(v)
	}
	return st
}
func cloneAction(a Action) Action {
	a.Request = bytes.Clone(a.Request)
	a.Receipt = bytes.Clone(a.Receipt)
	return a
}
func cloneRequest(r Request) Request {
	r.Payload = bytes.Clone(r.Payload)
	r.Answer = bytes.Clone(r.Answer)
	return r
}
func cloneWake(w Wake) Wake { w.Request = bytes.Clone(w.Request); return w }
func key(conversation, id string) string {
	raw, _ := json.Marshal([]string{conversation, id})
	return string(raw)
}
func admissionKey(conversation string, seq uint64) string {
	return key(conversation, strconv.FormatUint(seq, 10))
}

// Marshal RawMessage directly to preserve number spelling while using the same
// compaction and HTML escaping that embeds it in durable records.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > MaxControlFrameBytes || !json.Valid(raw) {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(raw)
	if err != nil || len(canonical) > MaxControlFrameBytes {
		return nil, ErrInvalid
	}
	return canonical, nil
}

func hashBytes(raw []byte) string              { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func hashValue(v any) string                   { raw, _ := json.Marshal(v); return hashBytes(raw) }
func identity(b ManagerBinding) ManagerBinding { b.Generation = 0; return b }
func validBinding(b ManagerBinding) bool {
	return b.ConversationID != "" && (b.Provider == "codex" || b.Provider == "claude") && b.SessionID != "" && b.SessionInstance != "" && b.NativeSessionID != "" && b.NativeGeneration != "" && b.LoginEpoch != ""
}
func checkBinding(st *State, b ManagerBinding) error {
	c, ok := st.Conversations[b.ConversationID]
	if !ok || st.CurrentConversation != b.ConversationID || b.Generation != st.Generation || identity(c.Binding) != identity(b) {
		return ErrStale
	}
	return nil
}
func activeAdmission(st *State, b ManagerBinding, seq uint64) (Admission, error) {
	c := st.Conversations[b.ConversationID]
	if seq == 0 {
		return Admission{}, ErrInvalid
	}
	if seq <= c.RetiredThrough {
		return Admission{}, ErrExpired
	}
	a, ok := st.Admissions[admissionKey(b.ConversationID, seq)]
	if !ok {
		return a, ErrNotFound
	}
	if !a.SettledAt.IsZero() {
		return a, ErrExpired
	}
	return a, nil
}
func allocateAdmission(st *State, b ManagerBinding, source string) (Admission, error) {
	c := st.Conversations[b.ConversationID]
	if c.LastAdmission == ^uint64(0) {
		return Admission{}, ErrCapacity
	}
	c.LastAdmission++
	a := Admission{ConversationID: b.ConversationID, Sequence: c.LastAdmission, SourceID: source}
	putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
	putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
	return a, nil
}

func (s *Store) BindManager(b ManagerBinding) (ManagerBinding, error) {
	var result ManagerBinding
	err := s.update(func(st *State) error {
		if !validBinding(b) {
			return ErrInvalid
		}
		c, exists := st.Conversations[b.ConversationID]
		if exists && identity(c.Binding) != identity(b) {
			return ErrConflict
		}
		if st.CurrentConversation != "" && st.CurrentConversation != b.ConversationID {
			for k, r := range st.Requests {
				if r.ConversationID == st.CurrentConversation && r.State == "pending" {
					r.State = "expired"
					putRecord(st, "requests", &st.Requests, k, r)
				}
			}
			for k, p := range st.Permits {
				if p.ConversationID == st.CurrentConversation {
					p.Revoked = true
					putRecord(st, "permits", &st.Permits, k, p)
				}
			}
		}
		b.Generation = st.Generation
		c.Binding = identity(b)
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		st.CurrentConversation = b.ConversationID
		result = b
		return nil
	})
	return result, err
}

func (s *Store) BeginUtterance(b ManagerBinding, u Utterance) (Utterance, error) {
	var result Utterance
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if u.ID == "" || u.DeviceID == "" || u.VoiceSessionID == "" || u.MediaEpoch == 0 || u.Text != "" || u.TextHash != "" || u.Admission != 0 || (u.ConversationID != "" && u.ConversationID != b.ConversationID) || (u.Phase != "" && u.Phase != "capturing") {
			return ErrInvalid
		}
		u.ConversationID = b.ConversationID
		u.Phase = "capturing"
		k := key(b.ConversationID, u.ID)
		if old, ok := st.Utterances[k]; ok {
			if old.DeviceID != u.DeviceID || old.VoiceSessionID != u.VoiceSessionID || old.MediaEpoch != u.MediaEpoch {
				return ErrConflict
			}
			result = old
			return nil
		}
		for _, old := range st.Utterances {
			if old.ID == u.ID && old.ConversationID != b.ConversationID {
				return ErrConflict
			}
			if old.VoiceSessionID == u.VoiceSessionID && old.DeviceID == u.DeviceID && (old.Phase == "capturing" || old.Phase == "finalizing") {
				return ErrInvalid
			}
		}
		putRecord(st, "utterances", &st.Utterances, k, u)
		result = u
		return nil
	})
	return result, err
}

func (s *Store) SetUtterancePhase(b ManagerBinding, id, phase string) (Utterance, error) {
	var result Utterance
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		u, ok := st.Utterances[k]
		if !ok {
			return ErrNotFound
		}
		if phase == u.Phase {
			result = u
			return nil
		}
		switch phase {
		case "finalizing":
			if u.Phase != "capturing" {
				return ErrInvalid
			}
		case "failed":
			if u.Phase != "capturing" && u.Phase != "finalizing" {
				return ErrInvalid
			}
		case "uncertain":
			if u.Phase != "committed" && u.Phase != "admitted" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		u.Phase = phase
		putRecord(st, "utterances", &st.Utterances, k, u)
		result = u
		return nil
	})
	return result, err
}

func (s *Store) FinalizeUtterance(b ManagerBinding, id, text string) (Utterance, error) {
	var result Utterance
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if text == "" || !utf8.ValidString(text) || len(text) > MaxUtteranceBytes {
			return ErrInvalid
		}
		k := key(b.ConversationID, id)
		u, ok := st.Utterances[k]
		if !ok {
			return ErrNotFound
		}
		h := hashBytes([]byte(text))
		if u.TextHash != "" {
			if u.TextHash != h {
				return ErrConflict
			}
			result = u
			return nil
		}
		if u.Phase != "capturing" && u.Phase != "finalizing" {
			return ErrInvalid
		}
		u.Text = text
		u.TextHash = h
		u.Phase = "committed"
		putRecord(st, "utterances", &st.Utterances, k, u)
		result = u
		return nil
	})
	return result, err
}
func (s *Store) AdmitUtterance(b ManagerBinding, id string) (Admission, error) {
	var result Admission
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		u, ok := st.Utterances[k]
		if !ok {
			return ErrNotFound
		}
		if u.Admission != 0 {
			if u.Admission <= st.Conversations[b.ConversationID].RetiredThrough {
				return ErrExpired
			}
			result = st.Admissions[admissionKey(b.ConversationID, u.Admission)]
			return nil
		}
		if u.Phase != "committed" {
			return ErrInvalid
		}
		a, err := allocateAdmission(st, b, "utterance:"+id)
		if err != nil {
			return err
		}
		u.Admission = a.Sequence
		u.Phase = "admitted"
		putRecord(st, "utterances", &st.Utterances, k, u)
		result = a
		return nil
	})
	return result, err
}
func (s *Store) BindNativeInput(b ManagerBinding, seq uint64, nativeInput string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if nativeInput == "" {
			return ErrInvalid
		}
		a, err := activeAdmission(st, b, seq)
		if err != nil {
			return err
		}
		if a.NativeInputID != "" && a.NativeInputID != nativeInput {
			return ErrConflict
		}
		for _, other := range st.Admissions {
			if other.ConversationID == b.ConversationID && other.Sequence != seq && other.NativeInputID == nativeInput {
				return ErrConflict
			}
		}
		a.NativeInputID = nativeInput
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, seq), a)
		return nil
	})
}
func (s *Store) PrepareAction(b ManagerBinding, a Action) (Action, error) {
	var result Action
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if a.ID == "" || a.NativeInputID == "" || a.Target == "" || a.OperationID == "" || len(a.Request) > 128<<10 || !json.Valid(a.Request) || (a.ConversationID != "" && a.ConversationID != b.ConversationID) {
			return ErrInvalid
		}
		var err error
		a.Request, err = canonicalJSON(a.Request)
		if err != nil {
			return err
		}
		a.ConversationID = b.ConversationID
		a.RequestHash = hashBytes(a.Request)
		a.State = "prepared"
		a.Receipt = nil
		a.SettledAt = time.Time{}
		k := key(b.ConversationID, a.ID)
		if old, ok := st.Actions[k]; ok {
			candidate := old
			candidate.State = "prepared"
			candidate.Receipt = nil
			candidate.SettledAt = time.Time{}
			if hashValue(candidate) != hashValue(a) {
				return ErrConflict
			}
			result = old
			return nil
		}
		input, err := activeAdmission(st, b, a.Admission)
		if err != nil {
			return err
		}
		if input.NativeInputID != a.NativeInputID {
			return ErrStale
		}
		for _, old := range st.Actions {
			if old.OperationID == a.OperationID {
				return ErrConflict
			}
		}
		for _, old := range st.Wakes {
			if old.OperationID == a.OperationID {
				return ErrConflict
			}
		}
		putRecord(st, "actions", &st.Actions, k, a)
		result = a
		return nil
	})
	return cloneAction(result), err
}
func (s *Store) SettleAction(b ManagerBinding, id string, receipt json.RawMessage) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if !json.Valid(receipt) || len(receipt) > 128<<10 {
			return ErrInvalid
		}
		var err error
		receipt, err = canonicalJSON(receipt)
		if err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		a, ok := st.Actions[k]
		if !ok {
			return ErrNotFound
		}
		if a.State == "settled" {
			if string(a.Receipt) != string(receipt) {
				return ErrConflict
			}
			return nil
		}
		a.State = "settled"
		a.Receipt = append(json.RawMessage(nil), receipt...)
		a.SettledAt = time.Now().UTC()
		putRecord(st, "actions", &st.Actions, k, a)
		return nil
	})
}
func settleAdmission(st *State, b ManagerBinding, seq uint64) error {
	k := admissionKey(b.ConversationID, seq)
	a, ok := st.Admissions[k]
	if !ok {
		return ErrNotFound
	}
	if a.NativeInputID == "" {
		return ErrInvalid
	}
	for _, action := range st.Actions {
		if action.ConversationID == b.ConversationID && action.Admission == seq && action.State != "settled" {
			return ErrInvalid
		}
	}
	if a.SettledAt.IsZero() {
		a.SettledAt = time.Now().UTC()
		putRecord(st, "admissions", &st.Admissions, k, a)
	}
	c := st.Conversations[b.ConversationID]
	for c.RetiredThrough < c.LastAdmission {
		next, ok := st.Admissions[admissionKey(b.ConversationID, c.RetiredThrough+1)]
		if !ok || next.SettledAt.IsZero() {
			break
		}
		c.RetiredThrough++
	}
	putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
	return nil
}
func (s *Store) SettleAdmission(b ManagerBinding, seq uint64) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		return settleAdmission(st, b, seq)
	})
}

// Compact removes seven-day settled receipts only behind the durable retirement floor.
// Utterance ID tombstones stay bounded and prevent a compacted capture being re-executed.
func (s *Store) Compact(b ManagerBinding, now time.Time) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		cutoff := now.Add(-7 * 24 * time.Hour)
		floor := st.Conversations[b.ConversationID].RetiredThrough
		for k, a := range st.Actions {
			if a.ConversationID == b.ConversationID && a.Admission <= floor && a.State == "settled" && !a.SettledAt.After(cutoff) {
				deleteRecord(st, "actions", &st.Actions, k)
			}
		}
		for k, a := range st.Admissions {
			if a.ConversationID == b.ConversationID && a.Sequence <= floor && !a.SettledAt.IsZero() && !a.SettledAt.After(cutoff) {
				deleteRecord(st, "admissions", &st.Admissions, k)
			}
		}
		for k, u := range st.Utterances {
			if u.ConversationID == b.ConversationID && u.Admission != 0 && u.Admission <= floor {
				u.Text = ""
				putRecord(st, "utterances", &st.Utterances, k, u)
			}
		}
		return nil
	})
}

func (s *Store) PutRequest(b ManagerBinding, r Request) (Request, error) {
	var result Request
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if r.ID == "" || (r.Kind != "manager" && r.Kind != "native") || !json.Valid(r.Payload) || len(r.Payload) > 128<<10 || r.ExpiresAt.IsZero() || (r.ConversationID != "" && r.ConversationID != b.ConversationID) {
			return ErrInvalid
		}
		if r.Kind == "native" && (r.NativeInputID == "" || r.NativeTurnID == "") {
			return ErrInvalid
		}
		if r.ExpiresAt.After(time.Now().Add(24 * time.Hour)) {
			return ErrInvalid
		}
		var err error
		r.Payload, err = canonicalJSON(r.Payload)
		if err != nil {
			return err
		}
		r.ConversationID = b.ConversationID
		r.State = "pending"
		r.Hash = ""
		r.AnswerID = ""
		r.Answer = nil
		r.Hash = hashValue(r)
		k := key(b.ConversationID, r.ID)
		if old, ok := st.Requests[k]; ok {
			if old.Hash != r.Hash {
				return ErrConflict
			}
			result = old
			return nil
		}
		if (r.Kind == "native" && st.ClosedTurns[key(b.ConversationID, r.NativeTurnID)]) || (r.TaskID != "" && st.CancelledTasks[key(b.ConversationID, r.TaskID)]) {
			return ErrExpired
		}
		putRecord(st, "requests", &st.Requests, k, r)
		result = r
		return nil
	})
	return cloneRequest(result), err
}
func (s *Store) AnswerRequest(b ManagerBinding, id, expectedHash, answerID string, answer json.RawMessage, now time.Time) (Request, error) {
	var result Request
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if answerID == "" || !json.Valid(answer) || len(answer) > 128<<10 || now.IsZero() {
			return ErrInvalid
		}
		var err error
		answer, err = canonicalJSON(answer)
		if err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		r, ok := st.Requests[k]
		if !ok {
			return ErrNotFound
		}
		if r.Hash != expectedHash {
			return ErrConflict
		}
		if r.State == "answered" {
			if r.AnswerID != answerID || string(r.Answer) != string(answer) {
				return ErrConflict
			}
			result = r
			return nil
		}
		if r.State != "pending" || !now.Before(r.ExpiresAt) {
			return ErrExpired
		}
		r.State = "answered"
		r.AnswerID = answerID
		r.Answer = append(json.RawMessage(nil), answer...)
		putRecord(st, "requests", &st.Requests, k, r)
		result = r
		return nil
	})
	return cloneRequest(result), err
}
func (s *Store) CancelTaskRequests(b ManagerBinding, task string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if task == "" {
			return ErrInvalid
		}
		putRecord(st, "cancelled_tasks", &st.CancelledTasks, key(b.ConversationID, task), true)
		for k, r := range st.Requests {
			if r.ConversationID == b.ConversationID && r.TaskID == task && r.State == "pending" {
				r.State = "expired"
				putRecord(st, "requests", &st.Requests, k, r)
			}
		}
		return nil
	})
}
func (s *Store) EndNativeTurn(b ManagerBinding, turn string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if turn == "" {
			return ErrInvalid
		}
		putRecord(st, "closed_turns", &st.ClosedTurns, key(b.ConversationID, turn), true)
		for k, r := range st.Requests {
			if r.ConversationID == b.ConversationID && r.Kind == "native" && r.NativeTurnID == turn && r.State == "pending" {
				r.State = "expired"
				putRecord(st, "requests", &st.Requests, k, r)
			}
		}
		k := key(b.ConversationID, turn)
		p := st.Permits[k]
		p.ConversationID = b.ConversationID
		p.NativeTurnID = turn
		p.Revoked = true
		putRecord(st, "permits", &st.Permits, k, p)
		return nil
	})
}
func (s *Store) GrantSpeech(b ManagerBinding, seq uint64, input, turn string, epoch uint64) (SpeechPermit, error) {
	var result SpeechPermit
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if input == "" || turn == "" || epoch == 0 {
			return ErrInvalid
		}
		a, err := activeAdmission(st, b, seq)
		if err != nil {
			return err
		}
		if a.NativeInputID != input {
			return ErrStale
		}
		p := SpeechPermit{ConversationID: b.ConversationID, Admission: seq, NativeInputID: input, NativeTurnID: turn, Epoch: epoch}
		k := key(b.ConversationID, turn)
		if old, ok := st.Permits[k]; ok {
			if old.Revoked {
				return ErrExpired
			}
			if old != p {
				return ErrConflict
			}
			result = old
			return nil
		}
		putRecord(st, "permits", &st.Permits, k, p)
		result = p
		return nil
	})
	return result, err
}
func (s *Store) RevokeSpeech(b ManagerBinding, turn string) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if turn == "" {
			return ErrInvalid
		}
		k := key(b.ConversationID, turn)
		p := st.Permits[k]
		p.ConversationID = b.ConversationID
		p.NativeTurnID = turn
		p.Revoked = true
		putRecord(st, "permits", &st.Permits, k, p)
		return nil
	})
}
func (s *Store) PublishSpeech(b ManagerBinding, permit SpeechPermit, p Publication) (Publication, error) {
	var result Publication
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		stored, ok := st.Permits[key(b.ConversationID, permit.NativeTurnID)]
		if !ok {
			return ErrStale
		}
		revoked := stored.Revoked
		stored.Revoked = permit.Revoked
		if stored != permit || permit.ConversationID != b.ConversationID || p.NativeTurnID != permit.NativeTurnID {
			return ErrStale
		}
		if p.ID == "" || p.Sequence == 0 || p.Text == "" || !utf8.ValidString(p.Text) || len(p.Text) > MaxUtteranceBytes || (p.ConversationID != "" && p.ConversationID != b.ConversationID) {
			return ErrInvalid
		}
		p.ConversationID = b.ConversationID
		p.TextHash = hashBytes([]byte(p.Text))
		p.Speak = !revoked
		k := key(b.ConversationID, p.ID)
		if old, ok := st.Publications[k]; ok {
			old.Speak = p.Speak
			if old != p {
				return ErrConflict
			}
			result = old
			return nil
		}
		for _, old := range st.Publications {
			if old.ConversationID == b.ConversationID && old.NativeTurnID == p.NativeTurnID && (old.Sequence == p.Sequence || old.Final || old.Sequence > p.Sequence) {
				return ErrConflict
			}
		}
		putRecord(st, "publications", &st.Publications, k, p)
		result = p
		return nil
	})
	return result, err
}

func (s *Store) AcceptEvent(b ManagerBinding, e Event, w Wake) (Wake, error) {
	var result Wake
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if e.ID == "" || e.Cursor.Binding == "" || e.Cursor.Sequence == 0 || !json.Valid(e.Metadata) || len(e.Metadata) > 128<<10 || e.WakeID == "" || e.WakeID != w.ID || w.OperationID == "" || !json.Valid(w.Request) || len(w.Request) > 128<<10 || (e.ConversationID != "" && e.ConversationID != b.ConversationID) || (w.ConversationID != "" && w.ConversationID != b.ConversationID) {
			return ErrInvalid
		}
		var err error
		e.Metadata, err = canonicalJSON(e.Metadata)
		if err != nil {
			return err
		}
		w.Request, err = canonicalJSON(w.Request)
		if err != nil {
			return err
		}
		e.ConversationID = b.ConversationID
		w.ConversationID = b.ConversationID
		w.EventID = e.ID
		w.State = "prepared"
		w.Admission = 0
		w.NativeInputID = ""
		w.RequestHash = hashBytes(w.Request)
		cursor, exists := st.Journal[b.ConversationID]
		if exists && cursor.Binding != e.Cursor.Binding {
			return ErrStale
		}
		ek := key(b.ConversationID, e.ID)
		wk := key(b.ConversationID, w.ID)
		if old, ok := st.Events[ek]; ok {
			if hashValue(old) != hashValue(e) {
				return ErrConflict
			}
			prior, ok := st.Wakes[wk]
			if !ok {
				return ErrCorrupt
			}
			candidate := prior
			candidate.State = "prepared"
			candidate.Admission = 0
			candidate.NativeInputID = ""
			if hashValue(candidate) != hashValue(w) {
				return ErrConflict
			}
			result = prior
			return nil
		}
		if e.ExaminedThrough != (JournalCursor{}) {
			if e.ExaminedThrough.Binding != e.Cursor.Binding || e.ExaminedThrough.Sequence < cursor.Sequence || e.ExaminedThrough.Sequence != e.Cursor.Sequence-1 {
				return fmt.Errorf("%w: invalid examined journal checkpoint", ErrStale)
			}
		} else if e.Cursor.Sequence != cursor.Sequence+1 {
			return fmt.Errorf("%w: daemon journal gap requires reconciliation", ErrStale)
		}
		if _, exists := st.Wakes[wk]; exists {
			return ErrConflict
		}
		for _, old := range st.Wakes {
			if old.OperationID == w.OperationID {
				return ErrConflict
			}
		}
		for _, old := range st.Actions {
			if old.OperationID == w.OperationID {
				return ErrConflict
			}
		}
		putRecord(st, "events", &st.Events, ek, e)
		putRecord(st, "wakes", &st.Wakes, wk, w)
		putRecord(st, "journal", &st.Journal, b.ConversationID, e.Cursor)
		result = w
		return nil
	})
	return cloneWake(result), err
}
func (s *Store) AdvanceWake(b ManagerBinding, id, state, nativeInput string) (Wake, error) {
	var result Wake
	err := s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		k := key(b.ConversationID, id)
		w, ok := st.Wakes[k]
		if !ok {
			return ErrNotFound
		}
		if state == w.State {
			if nativeInput != "" && nativeInput != w.NativeInputID {
				return ErrConflict
			}
			result = w
			return nil
		}
		switch state {
		case "admitted":
			if w.State != "prepared" || nativeInput != "" {
				return ErrInvalid
			}
			a, err := allocateAdmission(st, b, "wake:"+id)
			if err != nil {
				return err
			}
			w.Admission = a.Sequence
		case "native_input":
			if w.State != "admitted" || nativeInput == "" {
				return ErrInvalid
			}
			for _, a := range st.Admissions {
				if a.ConversationID == b.ConversationID && a.NativeInputID == nativeInput && a.Sequence != w.Admission {
					return ErrConflict
				}
			}
			a, err := activeAdmission(st, b, w.Admission)
			if err != nil {
				return err
			}
			if a.NativeInputID != "" && a.NativeInputID != nativeInput {
				return ErrConflict
			}
			a.NativeInputID = nativeInput
			putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, w.Admission), a)
			w.NativeInputID = nativeInput
		case "settled":
			if w.State != "native_input" || nativeInput != w.NativeInputID {
				return ErrInvalid
			}
			if err := settleAdmission(st, b, w.Admission); err != nil {
				return err
			}
		default:
			return ErrInvalid
		}
		w.State = state
		putRecord(st, "wakes", &st.Wakes, k, w)
		result = w
		return nil
	})
	return cloneWake(result), err
}

// ReconcileJournal accepts a source-verified snapshot, never a device stream cursor.
// Reconciliation records observations without settling any native input or effect.
func (s *Store) ReconcileJournal(b ManagerBinding, cursor JournalCursor, snapshot JournalSnapshot) error {
	return s.update(func(st *State) error {
		if err := checkBinding(st, b); err != nil {
			return err
		}
		if cursor.Binding == "" || !json.Valid(snapshot.Metadata) || len(snapshot.Metadata) > 128<<10 || (snapshot.Cursor != (JournalCursor{}) && snapshot.Cursor != cursor) {
			return ErrInvalid
		}
		var err error
		snapshot.Metadata, err = canonicalJSON(snapshot.Metadata)
		if err != nil {
			return err
		}
		previous, exists := st.Journal[b.ConversationID]
		if snapshot.Previous != previous {
			return ErrStale
		}
		if exists && previous.Binding == cursor.Binding && cursor.Sequence < previous.Sequence {
			return ErrStale
		}
		snapshot.Cursor = cursor
		putRecord(st, "journal_snapshots", &st.JournalSnapshots, b.ConversationID, snapshot)
		putRecord(st, "journal", &st.Journal, b.ConversationID, cursor)
		return nil
	})
}
