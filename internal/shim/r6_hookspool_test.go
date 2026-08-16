package shim

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5) tests for the shim-owned
// hook spool: playbook §6.1's structured-capture survival boundary --
//
//	"Claude hooks post to a per-session shim-owned socket; the shim durably sequences and
//	 spools each accepted raw event before acknowledging it, and the daemon drains the
//	 spool idempotently. ... Daemon unavailability neither fails a provider hook nor loses
//	 an accepted item. ... The spool is owner-only, bounded, crash-atomic, and compacted
//	 only after the daemon durably folds an acknowledged sequence."
//
// and ADR-013's sacred rule: the PTY is untouched by any of this -- the hook socket is a
// SECOND per-session listener beside the existing control socket (server.go), sharing its
// ownership discipline (listen()'s 0600-from-bind pattern) but touching neither the PTY nor
// the emulator/transcript pipeline.
//
// THIS FILE tests HookSpool as a standalone, socket-independent durability primitive: open,
// append (fsync before the call returns), read back, compact, and the bounded-refusal and
// gap-detection edges. A "shim crash" is simulated the way this repository's other durable-
// state tests do it (idempotency_compaction_test.go, kill_idempotency_test.go): seed the
// durable log through its own API, then open a FRESH instance over the same file and assert
// what a restarted process would see -- not a timed process kill, which cannot be made
// deterministic. r6_hooksocket_test.go covers the live-socket half (fsync-before-ack over
// the wire, the drain wire protocol); r6_gap_test.go covers corruption/truncation.
//
// THE SEAMS THIS FILE PINS (undefined symbols -> compile-fail RED):
//
//	const HookSpoolFile = "hooks.spool"          // sibling of SnapshotFile/ExitFile/
//	                                              // TranscriptFile inside SessionDir
//	const hookSpoolDefaultMaxBytes = 8 << 20     // Config.HookSpoolMaxBytes == 0 default
//
//	type HookRecord struct {
//	    Seq  uint64
//	    Body []byte    // the exact bytes Append was given, verbatim
//	}
//
//	type HookSpool struct{ ... }  // unexported fields; opened over one file
//	func OpenHookSpool(path string, maxBytes int) (*HookSpool, error)
//	    // maxBytes<=0 means hookSpoolDefaultMaxBytes. Creates the file (and its parent dir,
//	    // which callers already own at 0700) at 0600 if absent; reopens it unchanged
//	    // otherwise -- OpenHookSpool is what a restarted shim calls, and existing records
//	    // (and the persisted compaction base) must survive that reopen untouched.
//	func (s *HookSpool) Append(body []byte) (seq uint64, err error)
//	    // fsyncs the record to disk BEFORE returning. err is ErrHookSpoolFull once the file
//	    // has grown to maxBytes; a full spool is refused OUTRIGHT -- nothing already
//	    // accepted is ever evicted to make room (playbook 6.1's "loses an accepted item"
//	    // prohibition, applied to the spool's own bound as well as to daemon downtime).
//	func (s *HookSpool) ReadFrom(after uint64) (recs []HookRecord, gapAt uint64, hasGap bool, err error)
//	    // every record with Seq>after, IN ORDER, stopping at the first corrupt/truncated
//	    // one. hasGap==true names the boundary (gapAt = the first sequence that could not be
//	    // read); recs never includes it or anything past it. A clean tail is hasGap=false,
//	    // gapAt=0.
//	func (s *HookSpool) Compact(foldSeq uint64) error
//	    // durably drops every record with Seq<=foldSeq via the SAME temp+fsync+rename+
//	    // fsyncDir pattern writeFileAtomic/persistSideFiles already use elsewhere in this
//	    // package, so a crash mid-compaction leaves the OLD file (rename is atomic) -- never
//	    // a half-written one.
//	func (s *HookSpool) Close() error
//
//	var ErrHookSpoolFull = errors.New("shim: hook spool at its size bound")
//
//	var testHookAfterSpoolFsync func()
//	    // test-only seam (nil in production), the hook-socket peer of
//	    // testHookAfterPTYResize/testHookAfterSignalArm: runs synchronously inside Append,
//	    // AFTER the fsync commits and BEFORE Append returns. Because the hook-socket POST
//	    // handler (r6_hooksocket_test.go) writes its ack byte only after Append returns nil,
//	    // this is the exact "accepted, not yet acked" window playbook 6.1 names.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// openTestSpool opens a fresh HookSpool in a t.TempDir() at the standard sibling name, with
// cleanup.
func openTestSpool(t *testing.T, maxBytes int) (*HookSpool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, maxBytes)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// ---------------------------------------------------------------------------
// (1) fsync-before-ack durability: Append commits before it returns, and a
// restarted (reopened) spool sees every committed record, in order, verbatim.
// ---------------------------------------------------------------------------

func TestHookSpool_AppendAssignsMonotonicSequenceStartingAtOne(t *testing.T) {
	s, _ := openTestSpool(t, 0)
	seq1, err := s.Append([]byte(`{"event":"UserPromptSubmit"}`))
	if err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("first Append returned seq %d, want 1", seq1)
	}
	seq2, err := s.Append([]byte(`{"event":"Stop"}`))
	if err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("second Append returned seq %d, want 2 (monotonic, no gaps on a healthy spool)", seq2)
	}
}

func TestHookSpool_SurvivesReopenAfterAppend_TheCoreR6Guarantee(t *testing.T) {
	// "the event is in the spool on restart": Append durably fsyncs before returning, so a
	// FRESH HookSpool instance over the same file -- standing in for a restarted shim
	// process, the deterministic equivalent of a process kill this repo's other durable-
	// state tests already use -- must see every record that Append acknowledged, with
	// their bodies byte-exact and their sequences intact.
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s1, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool (first instance): %v", err)
	}
	bodies := [][]byte{
		[]byte(`{"event":"UserPromptSubmit","sequence":1}`),
		[]byte(`{"event":"PreToolUse","sequence":2}`),
		[]byte(`{"event":"Stop","sequence":3}`),
	}
	for i, b := range bodies {
		if _, err := s1.Append(b); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// No clean Close: Append's own fsync is the ONLY durability guarantee under test. A
	// process that crashed the instant after the last Append returned never runs any
	// deferred Close either.

	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool (post-restart instance): %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	recs, gapAt, hasGap, err := s2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) after restart: %v", err)
	}
	if hasGap {
		t.Fatalf("restarted spool reports a gap at seq %d over records that were durably fsynced before the crash", gapAt)
	}
	if len(recs) != len(bodies) {
		t.Fatalf("restarted spool holds %d record(s), want %d — an accepted item was lost across restart", len(recs), len(bodies))
	}
	for i, want := range bodies {
		if recs[i].Seq != uint64(i+1) {
			t.Errorf("record %d has seq %d, want %d", i, recs[i].Seq, i+1)
		}
		if !bytes.Equal(recs[i].Body, want) {
			t.Errorf("record %d body = %s, want %s (verbatim bytes)", i, recs[i].Body, want)
		}
	}
}

func TestHookSpool_ReadFromAfterExcludesAlreadyDrainedRecords(t *testing.T) {
	s, _ := openTestSpool(t, 0)
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`)} {
		if _, err := s.Append(b); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	recs, _, hasGap, err := s.ReadFrom(1) // already drained through seq 1
	if err != nil {
		t.Fatalf("ReadFrom(1): %v", err)
	}
	if hasGap {
		t.Fatalf("ReadFrom(1) reports a gap on a healthy spool")
	}
	if len(recs) != 2 || recs[0].Seq != 2 || recs[1].Seq != 3 {
		t.Fatalf("ReadFrom(1) = %+v, want records [seq=2, seq=3]", recs)
	}
}

// ---------------------------------------------------------------------------
// (5) bounds: the spool at its size cap refuses new events rather than
// evicting anything already accepted.
// ---------------------------------------------------------------------------

func TestHookSpool_AtCapacityRefusesRatherThanEvicts(t *testing.T) {
	const tiny = 256 // small enough that a handful of records fill it
	s, path := openTestSpool(t, tiny)

	var lastGood uint64
	var filled int
	for i := 0; i < 1000; i++ {
		seq, err := s.Append(bytes.Repeat([]byte{'x'}, 32))
		if err != nil {
			if !errors.Is(err, ErrHookSpoolFull) {
				t.Fatalf("Append at capacity returned %v, want ErrHookSpoolFull", err)
			}
			filled = i
			break
		}
		lastGood = seq
	}
	if lastGood == 0 {
		t.Fatalf("the spool never refused a single record under a %d-byte cap — the bound is not enforced", tiny)
	}
	if filled == 0 {
		t.Fatalf("the spool accepted 1000 32-byte records under a %d-byte cap without ever refusing — the bound is not enforced", tiny)
	}

	// The refusal must be HONEST, not silently lossy: every record accepted before the
	// cap was hit is still there, in full, after the refusal.
	recs, hasGapAt, hasGap, err := s.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) after a refusal: %v", err)
	}
	if hasGap {
		t.Fatalf("a bounded-refusal spool reports a gap at %d; a refused record must never corrupt what was already accepted", hasGapAt)
	}
	if uint64(len(recs)) != lastGood {
		t.Fatalf("spool holds %d record(s) after a refusal, want the %d accepted before the cap — a refusal must never evict an accepted item", len(recs), lastGood)
	}

	// A subsequent Compact (the daemon folding everything already drained) frees room, and
	// the spool accepts again — the refusal is about the BOUND, not a permanent wedge.
	if err := s.Compact(lastGood); err != nil {
		t.Fatalf("Compact(%d): %v", lastGood, err)
	}
	if _, err := s.Append([]byte("room now")); err != nil {
		t.Fatalf("Append after Compact freed room: %v, want it to succeed", err)
	}
	_ = path
}

// ---------------------------------------------------------------------------
// Compaction: crash-atomic, and never removes an unfolded record.
// ---------------------------------------------------------------------------

func TestHookSpool_CompactDropsOnlyRecordsAtOrBelowTheFoldCursor(t *testing.T) {
	s, path := openTestSpool(t, 0)
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`), []byte(`{"n":4}`)} {
		if _, err := s.Append(b); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Compact(2); err != nil {
		t.Fatalf("Compact(2): %v", err)
	}
	recs, _, hasGap, err := s.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) after Compact(2): %v", err)
	}
	if hasGap {
		t.Fatalf("ReadFrom reports a gap immediately after a clean Compact")
	}
	if len(recs) != 2 || recs[0].Seq != 3 || recs[1].Seq != 4 {
		t.Fatalf("post-compaction records = %+v, want [seq=3, seq=4] (records 1-2 folded away, 3-4 kept)", recs)
	}

	// Survives a restart: compaction is durable, not merely an in-memory trim.
	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("reopen after Compact: %v", err)
	}
	defer func() { _ = s2.Close() }()
	recs2, _, hasGap2, err := s2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) on reopened, compacted spool: %v", err)
	}
	if hasGap2 || len(recs2) != 2 || recs2[0].Seq != 3 {
		t.Fatalf("compaction did not survive reopen: recs=%+v hasGap=%v", recs2, hasGap2)
	}
}

func TestHookSpool_CompactRefusesToDropAnUnacknowledgedRecord(t *testing.T) {
	// "compacted only after the daemon durably folds an acknowledged sequence" -- Compact's
	// argument IS that acknowledgement, so asking it to fold past what has actually been
	// appended must not silently accept a cursor from the future.
	s, _ := openTestSpool(t, 0)
	if _, err := s.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Compact(99); err == nil {
		t.Fatalf("Compact(99) over a spool whose highest sequence is 1 returned nil error, want a refusal — a fold cursor ahead of everything ever appended must never be honored silently")
	}
	recs, _, hasGap, err := s.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) after a refused Compact: %v", err)
	}
	if hasGap || len(recs) != 1 {
		t.Fatalf("a refused Compact corrupted the spool: recs=%+v hasGap=%v", recs, hasGap)
	}
}

// ---------------------------------------------------------------------------
// (6) ownership: the spool file is owner-only (0600), matching listen()'s
// socket discipline and shim.go's writeFileAtomic side-files.
// ---------------------------------------------------------------------------

func TestHookSpool_FileIsOwnerOnly0600(t *testing.T) {
	s, path := openTestSpool(t, 0)
	if _, err := s.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat spool file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("hook spool file mode = %o, want 0600 (owner-only, ADR-004's threat model)", perm)
	}
}

func TestHookSpool_CompactedFileStaysOwnerOnly0600(t *testing.T) {
	// Compact rewrites the file via temp+rename; the REPLACEMENT must carry the same 0600
	// mode as the original, not whatever CreateTemp/umask would otherwise leave it at.
	s, path := openTestSpool(t, 0)
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`)} {
		if _, err := s.Append(b); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact(1): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat compacted spool file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("post-compaction spool file mode = %o, want 0600", perm)
	}
}
