// Package journal is the daemon-owned durable event journal (ADR-007 D6, plan
// R-JRN): a single daemon-wide append-only log of versioned records
// (schema_version, monotonic cursor, ts, session_id, type, payload) under
// <stateDir>/journal/, written at the daemon's saveMetaLocked choke point (A7).
// It survives a daemon crash/upgrade (D-5): every append is fsync'd before its
// cursor is acked (R-JRN.5). Resume is "snapshot as of cursor N + events after N",
// atomic under one lock (R-JRN.4); a read below the retained floor returns a
// full-resync signal (R-JRN.6). Retention rotates segments to bound disk.
package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// SchemaVersion is the record schema this build writes. A future version is
// rejected loudly on decode; an older version is migrated forward (R-JRN.1).
const SchemaVersion = 1

// RecordType is the kind of journalled transition.
type RecordType string

const (
	TypeGroupTransition RecordType = "group_transition"
	TypeLaunched        RecordType = "launched"
	TypeExited          RecordType = "exited"
	TypeLost            RecordType = "lost"
	TypeDeleted         RecordType = "deleted"
	TypePresence        RecordType = "presence"
	// TypeInteraction is one interaction item -- a message, tool run, file change,
	// approval, plan revision or status marker of the phone's chat transcript (ADR-009,
	// ADR-010, docs/specifications/interaction-schema.md IS-LAYER-1). The item object IS
	// the record's Payload, and the item's own `kind` discriminator lives only in there:
	// nothing that spec defines may become a record field, let alone a header field the
	// seq bucket keys on (IS-LAYER-2, PB-SYNC-1). Ordering is the cursor Append assigns and
	// nothing else -- an item carries no private sequence number (IS-LAYER-3).
	//
	// Adding the type did NOT bump SchemaVersion, and interaction_test.go pins why: a new
	// TYPE is a new value of an existing string field, so the on-disk field set is
	// unchanged, and a reader that does not know the type skips the record and advances its
	// cursor (IS-COMPAT-1/-3) instead of rejecting the whole journal the way a version bump
	// would make it.
	TypeInteraction RecordType = "interaction"
	// TypeRoster is a SYNTHETIC snapshot record: it is emitted ONLY inside
	// Resume.Roster (the atomic snapshot half of R-JRN.4) to describe a session that
	// is live as-of the read cursor. It is NEVER appended to the journal — no
	// producer writes it and no reader will find it in the event stream. The daemon
	// (which owns the meta store) populates these; the journal package itself never
	// manufactures a roster record.
	TypeRoster RecordType = "roster"
	// TypeStructuredGap is the daemon-authored capability-degrade event of ADR-017 T2
	// rule 2: an unrecoverable shim/daemon spool or cursor gap emits an exact boundary,
	// disables structured_chat for the session instance, and forbids a fabricated
	// completion. Its body is daemon.StructuredGapEvent, riding this record family exactly
	// as InteractionItem rides TypeInteraction -- no new mailbox frame kind.
	TypeStructuredGap RecordType = "structured_gap"
	// TypeCapabilityTransition is an additive, cursor-ordered publication of the
	// daemon's complete capability record after a durable runtime transition. It is
	// separate from TypeRoster: roster records are snapshot-only, while a phone that
	// is already subscribed must observe a degrade or a proven recovery without a
	// reconnect. The payload is protocol.SessionCapabilities; journal deliberately
	// owns only the string discriminator to avoid an import cycle.
	TypeCapabilityTransition RecordType = "capability_transition"
)

// Record is one versioned journal entry. Cursor is a monotonic uint64 assigned by
// Append and never reused for the journal's whole lifetime.
type Record struct {
	SchemaVersion int          `json:"schema_version"`
	Cursor        uint64       `json:"cursor"`
	TS            time.Time    `json:"ts"`
	SessionID     string       `json:"session_id"`
	Type          RecordType   `json:"type"`
	Group         status.Group `json:"group,omitempty"` // set on group_transition
	// Agent is the session's agent identity (persist.Meta.AgentType), so the phone can label
	// a session with the CLI running it. The daemon sets it on the roster snapshot and on all
	// four journalworthy transitions (internal/daemon/journal.go); the `deleted` record
	// (lifecycle.go) deliberately carries none, because it REMOVES the session from the
	// phone's model and an agent name on a tombstone would describe nothing.
	//
	// It is omitempty and its ABSENCE IS MEANINGFUL: a record with no agent carries no
	// agent, and readers must never read "" as an agent by that name.
	//
	// Adding it did NOT bump SchemaVersion, and internal/journal/agentfield_test.go pins
	// why in both directions: this build reads pre-Agent lines (an absent key decodes to
	// ""), and a build predating the field reads records carrying one (encoding/json
	// ignores the unknown key). A bump would instead make every older daemon reject every
	// record this build writes, costing them the whole journal to gain nothing.
	Agent string `json:"agent,omitempty"`
	// Name is the session's USER-GIVEN label (persist.Meta.Name), so the phone can head a row
	// with what a person called the session instead of the 16-char random local id. It is set
	// at exactly the sites Agent is -- the roster snapshot and all four journalworthy
	// transitions -- and the `deleted` record deliberately carries none, for Agent's reason: a
	// tombstone REMOVES the session, and a label on it would describe nothing.
	//
	// It is omitempty and its ABSENCE IS MEANINGFUL, again as Agent's: most sessions are never
	// labelled, so an empty name means the user gave none. The phone falls back to the id
	// (mobile/app.go's session()); nothing anywhere may substitute a plausible label for a
	// missing one.
	//
	// Adding it did NOT bump SchemaVersion, and internal/journal/namefield_test.go pins why in
	// both directions, exactly as agentfield_test.go did for Agent.
	Name string `json:"name,omitempty"`
	// StateSince is the MACHINE's own stamp of when the session entered its CURRENT state --
	// persist.Meta.EffectiveGroupEnteredAt(), with that method's own fallbacks for records
	// written before GroupEnteredAt existed -- set at exactly the sites Agent and Name are
	// (the roster snapshot and the four journalworthy transitions), so the phone can age a
	// row from a clock it did not invent (phone-refit-playbook W7.1). It is NOT LastActivity:
	// that is written only at launch and exit, so "Waiting on you · 3h" would have meant
	// "launched 3h ago" and twins launched together would age identically. Zero means the
	// meta carried no instant at all, and it is omitzero so a record without one serialises
	// as it did before the field existed. It is distinct from TS, which is when THIS RECORD
	// was appended.
	StateSince time.Time       `json:"state_since,omitzero"`
	Payload    json.RawMessage `json:"payload,omitempty"` // opaque per-type extra
}

// Resume is the atomic result of ReadFrom: the snapshot roster as of the read
// cursor, the events after the caller's cursor, and a full-resync signal when the
// caller's cursor fell below the retained floor (dropped records were not silently
// omitted, R-JRN.6).
type Resume struct {
	Cursor     uint64
	Roster     []Record
	Events     []Record
	FullResync bool
}

// Options tune retention (rotation bounds) and the injected clock.
type Options struct {
	MaxBytes int64            // rotate the active segment past this size (0 = unbounded)
	MaxFiles int              // keep at most this many segments (0 = unbounded)
	Clock    func() time.Time // record timestamp source (defaults to time.Now)
}

// EncodeRecord serializes a record to one JSON line (no trailing newline).
func EncodeRecord(r Record) []byte {
	b, _ := json.Marshal(r) // Record is always marshalable
	return b
}

// DecodeRecord parses one JSON line. A future schema version is rejected loudly
// (never silently dropped); an older version is migrated forward; malformed JSON
// is an error (R-JRN.1).
func DecodeRecord(line []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(line, &r); err != nil {
		return Record{}, err
	}
	if r.SchemaVersion > SchemaVersion {
		return Record{}, fmt.Errorf("journal: record schema_version %d is newer than supported %d", r.SchemaVersion, SchemaVersion)
	}
	if r.SchemaVersion < SchemaVersion {
		r.SchemaVersion = SchemaVersion // forward migration (v0->v1 shares the field set)
	}
	return r, nil
}

// segment is one on-disk log file; segments are ascending by seq (== cursor order).
type segment struct {
	seq   uint64
	path  string
	count int   // records written to this segment (for retention record-drop)
	bytes int64 // on-disk size (for rotation)
}

// Journal is the daemon-wide append-only log. Retained records are mirrored in
// memory (bounded by retention) so ReadFrom is atomic under one lock.
type Journal struct {
	dir   string
	opts  Options
	clock func() time.Time

	mu       sync.Mutex
	cursor   uint64     // high-water mark, monotonic for the journal's lifetime
	records  []Record   // retained records, ascending cursor
	segments []*segment // ascending seq; the last is the active segment
	active   *segment
	activeF  *os.File
	nextSeq  uint64
	closed   bool
	subs     map[chan Record]struct{} // live subscribers (fan-out), guarded by mu
}

// subscriberBuffer bounds each live subscriber's per-channel buffer. Append does a
// NON-BLOCKING send (drop-on-full) so a wedged subscriber never stalls journaling
// (Append is on the daemon's write-critical path under writeMu); the downstream
// protocol layer evicts a wedged subscriber and the phone resyncs from its cursor
// (R-JRN.6).
const subscriberBuffer = 256

// Open opens (or creates) the journal at dir with default (unbounded) retention.
func Open(dir string) (*Journal, error) { return OpenWithOptions(dir, Options{}) }

// OpenWithOptions opens the journal, loading every retained record (tolerating a
// torn final line from a crash mid-append) and resuming the high-water cursor. A
// fresh active segment is always started so new appends never extend a possibly
// torn segment.
func OpenWithOptions(dir string, opts Options) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j := &Journal{dir: dir, opts: opts, clock: opts.Clock}
	if j.clock == nil {
		j.clock = time.Now
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type found struct {
		seq  uint64
		path string
	}
	var segs []found
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if seq, ok := parseSegName(e.Name()); ok {
			segs = append(segs, found{seq, filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(segs, func(a, b int) bool { return segs[a].seq < segs[b].seq })

	var maxSeq uint64
	for _, f := range segs {
		recs := loadSegment(f.path)
		seg := &segment{seq: f.seq, path: f.path, count: len(recs)}
		if fi, err := os.Stat(f.path); err == nil {
			seg.bytes = fi.Size()
		}
		for _, r := range recs {
			j.records = append(j.records, r)
			if r.Cursor > j.cursor {
				j.cursor = r.Cursor
			}
		}
		j.segments = append(j.segments, seg)
		if f.seq > maxSeq {
			maxSeq = f.seq
		}
	}
	j.nextSeq = maxSeq + 1
	if err := j.openNewSegmentLocked(); err != nil {
		return nil, err
	}
	return j, nil
}

// Append assigns the next cursor, stamps schema+ts, writes the record as one JSON
// line, and fsyncs BEFORE returning the stamped record (fsync-before-ack, R-JRN.5).
func (j *Journal) Append(r Record) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Record{}, errors.New("journal: closed")
	}

	j.cursor++
	r.Cursor = j.cursor
	r.SchemaVersion = SchemaVersion
	r.TS = j.clock().UTC()

	line := append(EncodeRecord(r), '\n')
	if j.opts.MaxBytes > 0 && j.active.bytes > 0 && j.active.bytes+int64(len(line)) > j.opts.MaxBytes {
		if err := j.rotateLocked(); err != nil {
			return Record{}, err
		}
	}
	if _, err := j.activeF.Write(line); err != nil {
		return Record{}, err
	}
	if err := j.activeF.Sync(); err != nil { // durable before the cursor is acked
		return Record{}, err
	}
	j.active.bytes += int64(len(line))
	j.active.count++
	j.records = append(j.records, r)
	j.enforceRetentionLocked()
	// Live fan-out: deliver the newly-appended record to every subscriber. The send
	// is NON-BLOCKING (drop-on-full) so a slow/wedged subscriber never blocks the
	// write-critical path; the protocol layer evicts it and the phone resyncs.
	for ch := range j.subs {
		select {
		case ch <- r:
		default:
		}
	}
	return r, nil
}

// Subscribe registers a live subscriber and returns its record feed plus a cancel
// func. Every Record appended after subscription is delivered to the feed (subject
// to the non-blocking drop-on-full discipline in Append). The cancel func
// unsubscribes and closes the feed; it is idempotent and race-free (serialized with
// Append and Close under the journal lock).
func (j *Journal) Subscribe() (<-chan Record, func()) {
	ch := make(chan Record, subscriberBuffer)
	j.mu.Lock()
	if j.subs == nil {
		j.subs = make(map[chan Record]struct{})
	}
	if j.closed {
		// Already shut down: hand back an immediately-closed feed so the reader sees
		// EOF and no goroutine leaks.
		j.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	j.subs[ch] = struct{}{}
	j.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			j.mu.Lock()
			if _, ok := j.subs[ch]; ok {
				delete(j.subs, ch)
				close(ch)
			}
			j.mu.Unlock()
		})
	}
	return ch, cancel
}

// Cursor returns the current high-water cursor.
func (j *Journal) Cursor() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursor
}

// ReadFrom returns every retained record with cursor > from, atomically with the
// boundary cursor (R-JRN.4). If from fell below the retained floor (records were
// compacted away), FullResync is set so the caller resyncs rather than seeing a
// silent gap (R-JRN.6).
func (j *Journal) ReadFrom(from uint64) (Resume, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	res := Resume{Cursor: j.cursor}
	if floor := j.floorLocked(); from > 0 && floor > 0 && from+1 < floor {
		res.FullResync = true
	}
	for _, r := range j.records {
		if r.Cursor > from {
			res.Events = append(res.Events, r)
		}
	}
	return res, nil
}

// Close closes the active segment file. Every append was already fsync'd, so a
// dropped handle (no Close) loses nothing.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	// Release every live subscriber: closing its feed signals EOF so its reader exits
	// (no goroutine leak). A subsequent cancel is a no-op (guarded on map membership),
	// so there is no double close.
	for ch := range j.subs {
		close(ch)
		delete(j.subs, ch)
	}
	if j.activeF != nil {
		return j.activeF.Close()
	}
	return nil
}

// floorLocked is the smallest retained cursor, or 0 when nothing is retained.
func (j *Journal) floorLocked() uint64 {
	if len(j.records) == 0 {
		return 0
	}
	return j.records[0].Cursor
}

// rotateLocked seals the active segment and opens a fresh one.
func (j *Journal) rotateLocked() error {
	if err := j.activeF.Close(); err != nil {
		return err
	}
	return j.openNewSegmentLocked()
}

// openNewSegmentLocked creates and opens (append-only, 0600) a new active segment.
func (j *Journal) openNewSegmentLocked() error {
	seg := &segment{seq: j.nextSeq, path: filepath.Join(j.dir, segName(j.nextSeq))}
	f, err := os.OpenFile(seg.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	j.nextSeq++
	j.active = seg
	j.activeF = f
	j.segments = append(j.segments, seg)
	return nil
}

// enforceRetentionLocked deletes the oldest segments (and drops their records from
// the in-memory mirror) until at most MaxFiles remain, bounding disk (R-JRN.6).
func (j *Journal) enforceRetentionLocked() {
	if j.opts.MaxFiles <= 0 {
		return
	}
	for len(j.segments) > j.opts.MaxFiles {
		old := j.segments[0]
		_ = os.Remove(old.path)
		if old.count > 0 && old.count <= len(j.records) {
			j.records = j.records[old.count:]
		}
		j.segments = j.segments[1:]
	}
}

// loadSegment reads every complete record from a segment file, tolerating a torn
// final line (a crash mid-append): an undecodable line is dropped, never failing
// the whole read.
func loadSegment(path string) []Record {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Record
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		r, err := DecodeRecord(line)
		if err != nil {
			continue // torn/corrupt line: tolerate (S8 isolation spirit)
		}
		out = append(out, r)
	}
	return out
}

// segName / parseSegName map a segment sequence number to its file name.
func segName(seq uint64) string { return fmt.Sprintf("seg-%020d.log", seq) }

func parseSegName(name string) (uint64, bool) {
	if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".log") {
		return 0, false
	}
	n, err := strconv.ParseUint(name[len("seg-"):len(name)-len(".log")], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
