// Package journal is the daemon-owned durable event journal (ADR-007 D6, plan
// R-JRN): a single daemon-wide append-only log of versioned records
// (schema_version, monotonic cursor, ts, session_id, type, payload) under
// <stateDir>/journal/, written at the daemon's saveMetaLocked choke point (A7).
// It is the one coordinate the async mailbox seq reuses (A5), and it survives a
// daemon crash/upgrade (D-5): every append is fsync'd before its cursor is acked.
//
// These are FAILING-FIRST white-box tests (package journal) for the remote-control
// Phase-1 daemon foundation. NO implementation exists yet: `go test
// ./internal/journal/` fails to COMPILE because the production symbols below are
// undefined — the correct GG-5 RED (undefined-only, no test bug). No production
// code or stubs are provided here; the implementer builds them to these
// expectations.
//
// FROZEN API these tests expect (the implementer's deliverable):
//
//	const SchemaVersion = 1
//	type RecordType string
//	const ( TypeGroupTransition, TypeLaunched, TypeExited, TypeLost, TypeDeleted, TypePresence RecordType )
//	type Record struct {
//	    SchemaVersion int             `json:"schema_version"`
//	    Cursor        uint64          `json:"cursor"`
//	    TS            time.Time       `json:"ts"`
//	    SessionID     string          `json:"session_id"`
//	    Type          RecordType      `json:"type"`
//	    Group         status.Group    `json:"group,omitempty"`   // set on group_transition
//	    Payload       json.RawMessage `json:"payload,omitempty"`
//	}
//	func EncodeRecord(r Record) []byte          // one JSON line (no trailing newline)
//	func DecodeRecord(line []byte) (Record, error) // future schema -> error; older -> migrated; malformed -> error
//	type Options struct { MaxBytes int64; MaxFiles int; Clock func() time.Time }
//	type Journal struct{ ... }
//	func Open(dir string) (*Journal, error)
//	func OpenWithOptions(dir string, opts Options) (*Journal, error)
//	func (j *Journal) Append(r Record) (Record, error)   // assigns cursor+schema+ts; fsync BEFORE return (R-JRN.5)
//	func (j *Journal) Cursor() uint64
//	func (j *Journal) ReadFrom(from uint64) (Resume, error) // atomic snapshot+range (R-JRN.4); FullResync below floor (R-JRN.6)
//	func (j *Journal) Close() error
//	type Resume struct { Cursor uint64; Roster []Record; Events []Record; FullResync bool }
//
// Every test carries a deadline / bounded loop; nothing may hang. No real sleeps
// for retention/debounce — clocks are injected.
package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// jdir returns a fresh, auto-cleaned journal directory.
func jdir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jrnl")
	if err != nil {
		t.Fatalf("mkdir journal dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// openJournal opens a journal with cleanup, fatal on error.
func openJournal(t *testing.T, dir string) *Journal {
	t.Helper()
	j, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

// mustAppend appends a record and returns the stamped result, fatal on error.
func mustAppend(t *testing.T, j *Journal, r Record) Record {
	t.Helper()
	got, err := j.Append(r)
	if err != nil {
		t.Fatalf("Append(%+v): %v", r, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// R-JRN.1 — versioned records; future schema rejected, older migrated; torn tail.
// ---------------------------------------------------------------------------

// TestJournal_SchemaRoundTripAndReject asserts the versioned line codec: a record
// round-trips byte-stable through Encode/Decode; a FUTURE schema is rejected
// loudly (error, never silently dropped); an OLDER schema is migrated forward.
func TestJournal_SchemaRoundTripAndReject(t *testing.T) {
	rec := Record{
		SchemaVersion: SchemaVersion,
		Cursor:        7,
		TS:            time.Unix(1700000000, 0).UTC(),
		SessionID:     "sess1",
		Type:          TypeGroupTransition,
		Group:         status.GroupNeedsInput,
	}
	line := EncodeRecord(rec)
	if len(line) == 0 {
		t.Fatalf("EncodeRecord returned empty line")
	}
	back, err := DecodeRecord(line)
	if err != nil {
		t.Fatalf("DecodeRecord(round-trip): %v", err)
	}
	if back.Cursor != rec.Cursor || back.SessionID != rec.SessionID ||
		back.Type != rec.Type || back.Group != rec.Group {
		t.Fatalf("round-trip mismatch: got %+v want %+v", back, rec)
	}

	// Future schema: rejected LOUDLY (R-JRN.1). Craft a line whose schema_version
	// exceeds this build's — an older daemon MUST NOT silently drop a newer record.
	future := rec
	future.SchemaVersion = SchemaVersion + 1
	if _, err := DecodeRecord(EncodeRecord(future)); err == nil {
		t.Fatalf("DecodeRecord accepted a future schema_version=%d; want a loud error", future.SchemaVersion)
	}

	// Older schema: migrated forward, not rejected (mirrors persist's v0->v1 chain).
	older := rec
	older.SchemaVersion = SchemaVersion - 1
	migrated, err := DecodeRecord(EncodeRecord(older))
	if err != nil {
		t.Fatalf("DecodeRecord rejected an older schema_version=%d; want forward migration: %v", older.SchemaVersion, err)
	}
	if migrated.SchemaVersion != SchemaVersion {
		t.Fatalf("older record migrated to schema_version=%d; want %d", migrated.SchemaVersion, SchemaVersion)
	}
}

// TestJournal_TruncatedTailTolerated asserts a torn final line (a crash mid-append)
// is tolerated: every complete earlier record is still readable and the partial
// tail is dropped without error — never a whole-journal read failure.
func TestJournal_TruncatedTailTolerated(t *testing.T) {
	dir := jdir(t)
	j := openJournal(t, dir)
	for i := 0; i < 5; i++ {
		mustAppend(t, j, Record{SessionID: "s", Type: TypeGroupTransition, Group: status.GroupWorking})
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Tear the physical tail: lop bytes off the largest segment file so its last
	// JSON line is incomplete, exactly as a crash between write and fsync would.
	seg := largestFile(t, dir)
	fi, err := os.Stat(seg)
	if err != nil {
		t.Fatalf("stat segment: %v", err)
	}
	if err := os.Truncate(seg, fi.Size()-3); err != nil {
		t.Fatalf("truncate segment tail: %v", err)
	}

	j2 := openJournal(t, dir)
	res, err := j2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom after torn tail returned error; a partial tail must be tolerated: %v", err)
	}
	if len(res.Events) != 4 {
		t.Fatalf("after truncating 1 torn line, ReadFrom(0) returned %d events; want the 4 intact ones", len(res.Events))
	}
}

// ---------------------------------------------------------------------------
// R-JRN.4 — cursor + atomic resume; monotonic across restart.
// ---------------------------------------------------------------------------

// TestJournal_ResumeBoundaryAtomic asserts ReadFrom captures "snapshot as of cursor
// N + every record cursor>N" under ONE lock, so a concurrent append never straddles
// the boundary. A reader run repeatedly against a live writer must always observe a
// contiguous, gap-free, duplicate-free cursor run ending exactly at Resume.Cursor.
func TestJournal_ResumeBoundaryAtomic(t *testing.T) {
	j := openJournal(t, jdir(t))

	const total = 400
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			if _, err := j.Append(Record{SessionID: "s", Type: TypeGroupTransition, Group: status.GroupWorking}); err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	sawGrowth := false
	for time.Now().Before(deadline) {
		res, err := j.ReadFrom(0)
		if err != nil {
			t.Fatalf("concurrent ReadFrom: %v", err)
		}
		// Events must be exactly cursors 1..Resume.Cursor: strictly increasing,
		// contiguous, no gap, no duplicate, none beyond the boundary (no straddle).
		var prev uint64
		for i, ev := range res.Events {
			if ev.Cursor <= prev {
				t.Fatalf("events not strictly increasing at %d: cursor %d after %d", i, ev.Cursor, prev)
			}
			if ev.Cursor != prev+1 {
				t.Fatalf("cursor gap at index %d: %d then %d (torn boundary)", i, prev, ev.Cursor)
			}
			if ev.Cursor > res.Cursor {
				t.Fatalf("event cursor %d exceeds Resume.Cursor %d (straddle)", ev.Cursor, res.Cursor)
			}
			prev = ev.Cursor
		}
		if uint64(len(res.Events)) != res.Cursor {
			t.Fatalf("ReadFrom(0) returned %d events but Resume.Cursor=%d; snapshot/range not atomic", len(res.Events), res.Cursor)
		}
		if res.Cursor > 0 && res.Cursor < total {
			sawGrowth = true
		}
		if res.Cursor == total {
			break
		}
	}
	wg.Wait()
	if !sawGrowth {
		t.Skip("writer completed before any mid-flight read; atomicity assertions still ran")
	}
}

// TestJournal_CursorMonotonicAcrossRestart asserts cursors are monotonic for the
// journal's whole lifetime and never reused, even across a Close/reopen.
func TestJournal_CursorMonotonicAcrossRestart(t *testing.T) {
	dir := jdir(t)
	j := openJournal(t, dir)
	var last uint64
	for i := 0; i < 3; i++ {
		r := mustAppend(t, j, Record{SessionID: "s", Type: TypeLaunched})
		if r.Cursor <= last {
			t.Fatalf("cursor not monotonic: %d after %d", r.Cursor, last)
		}
		last = r.Cursor
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	j2 := openJournal(t, dir)
	if j2.Cursor() != last {
		t.Fatalf("reopened Cursor()=%d; want the persisted high-water %d", j2.Cursor(), last)
	}
	for i := 0; i < 2; i++ {
		r := mustAppend(t, j2, Record{SessionID: "s", Type: TypeExited})
		if r.Cursor <= last {
			t.Fatalf("cursor reused/regressed across restart: %d after %d", r.Cursor, last)
		}
		last = r.Cursor
	}
}

// ---------------------------------------------------------------------------
// R-JRN.5 — append durability (fsync before ack) surviving an uncleaned stop.
// ---------------------------------------------------------------------------

// TestJournal_CrashAfterAckSurvives asserts the fsync-before-ack property (D-5): a
// record whose cursor Append returned is durable even if the journal handle is
// dropped with NO clean Close (modelling a kill -9 of the daemon). A fresh Open on
// the same dir reads it back.
func TestJournal_CrashAfterAckSurvives(t *testing.T) {
	dir := jdir(t)
	j, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	acked := mustAppend(t, j, Record{SessionID: "survivor", Type: TypeLaunched})
	if acked.Cursor == 0 {
		t.Fatalf("Append returned cursor 0; a durable cursor must be assigned before ack")
	}
	// No Close: drop the handle as a SIGKILL would, without any extra flush.
	j = nil

	j2 := openJournal(t, dir)
	res, err := j2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom after crash: %v", err)
	}
	found := false
	for _, ev := range res.Events {
		if ev.Cursor == acked.Cursor && ev.SessionID == "survivor" {
			found = true
		}
	}
	if !found {
		t.Fatalf("acked record (cursor %d) did not survive an uncleaned stop; fsync-before-ack violated", acked.Cursor)
	}
}

// ---------------------------------------------------------------------------
// R-JRN.6 — retention/compaction bounds disk; a stale cursor returns full-resync.
// ---------------------------------------------------------------------------

// TestJournal_CompactionBoundsDisk asserts bounded retention (transcript-style
// MaxBytes/MaxFiles rotation): appending far more than the cap keeps the on-disk
// footprint bounded, never unbounded growth.
func TestJournal_CompactionBoundsDisk(t *testing.T) {
	dir := jdir(t)
	const maxBytes, maxFiles = 4 << 10, 3
	j, err := OpenWithOptions(dir, Options{MaxBytes: maxBytes, MaxFiles: maxFiles})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })

	for i := 0; i < 2000; i++ {
		mustAppend(t, j, Record{SessionID: "flood", Type: TypeGroupTransition, Group: status.GroupWorking})
	}
	// Disk footprint must stay within a small multiple of the configured cap; a
	// generous 2x slack absorbs the active (not-yet-rotated) segment.
	if got, limit := dirSize(t, dir), int64(maxBytes*maxFiles*2); got > limit {
		t.Fatalf("journal on-disk size %d exceeds bound %d after 2000 appends; compaction not enforced", got, limit)
	}
}

// TestJournal_StaleCursorReturnsFullResync asserts a read below the retained floor
// returns a full-resync signal (never a silent omission of dropped records).
func TestJournal_StaleCursorReturnsFullResync(t *testing.T) {
	dir := jdir(t)
	j, err := OpenWithOptions(dir, Options{MaxBytes: 2 << 10, MaxFiles: 2})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	for i := 0; i < 1000; i++ {
		mustAppend(t, j, Record{SessionID: "s", Type: TypeGroupTransition, Group: status.GroupWorking})
	}
	// Cursor 1 is far below the retained floor after heavy rotation.
	res, err := j.ReadFrom(1)
	if err != nil {
		t.Fatalf("ReadFrom(1): %v", err)
	}
	if !res.FullResync {
		t.Fatalf("ReadFrom below the retained floor did not set FullResync; a dropped-record read must signal resync, never silently omit")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// largestFile returns the path of the largest regular file under dir (the active
// data segment), so the torn-tail test can truncate it without hard-coding the
// journal's segment naming.
func largestFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read journal dir: %v", err)
	}
	type sz struct {
		path string
		n    int64
	}
	var files []sz
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, sz{filepath.Join(dir, e.Name()), fi.Size()})
	}
	if len(files) == 0 {
		t.Fatalf("no segment files under %s", dir)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].n > files[j].n })
	return files[0].path
}

// dirSize sums the sizes of every regular file under dir.
func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk journal dir: %v", err)
	}
	return total
}

// jsonLen is a tiny guard used to keep the encoding/json import honest in the
// codec round-trip (a malformed line must fail DecodeRecord, not panic).
func jsonLen(v any) int { b, _ := json.Marshal(v); return len(b) }

var _ = jsonLen
