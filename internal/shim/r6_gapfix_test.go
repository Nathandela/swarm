package shim

// R6 REVIEW FIX-PACK regression tests: each function here pins one BLOCKER a
// probe-confirmed review found in the original HookSpool implementation, as a
// permanent test rather than a throwaway script. See hookspool.go's own header
// comment for the design these tests hold in place.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// BLOCKER 1: Compact must never erase a proven gap. The original implementation
// rebuilt the compacted file from PARSED records alone, which silently dropped an
// unparseable torn tail -- a restart's first drain would then compact through the
// fold cursor and report a clean spool, never emitting structured_gap.
func TestHookSpool_CompactNeverErasesAProvenGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	if _, err := s.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if _, err := s.Append([]byte(`{"n":2}`)); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	brokenSeq := truncateLastRecord(t, path, s, []byte(`{"n":3,"padding":"enough bytes to halve cleanly"}`))
	_ = s.Close()

	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()

	before, gapAtBefore, hasGapBefore, err := s2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom before compact: %v", err)
	}
	if !hasGapBefore || gapAtBefore != brokenSeq {
		t.Fatalf("ReadFrom before compact: hasGap=%v gapAt=%d, want (true, %d)", hasGapBefore, gapAtBefore, brokenSeq)
	}

	// The daemon folding what it already applied (records 1-2, below the tear) must
	// not make the tear disappear.
	if err := s2.Compact(2); err != nil {
		t.Fatalf("Compact(2): %v", err)
	}

	after, gapAtAfter, hasGapAfter, err := s2.ReadFrom(2)
	if err != nil {
		t.Fatalf("ReadFrom after compact: %v", err)
	}
	if !hasGapAfter {
		t.Fatalf("Compact(2) erased the proven gap: ReadFrom(2) after compact reports hasGap=false (before: hasGap=%v gapAt=%d)", hasGapBefore, gapAtBefore)
	}
	if gapAtAfter != brokenSeq {
		t.Fatalf("gap boundary changed across compaction: before=%d after=%d, want both %d", gapAtBefore, gapAtAfter, brokenSeq)
	}
	if len(after) != 0 {
		t.Fatalf("ReadFrom(2) after compact returned %d record(s) past a torn tail, want 0", len(after))
	}
	_ = before
}

// BLOCKER 2: an Append after a torn record must never fabricate a merged record body
// by writing past the tear. It must refuse outright.
func TestHookSpool_AppendAfterATearRefusesRatherThanFabricates(t *testing.T) {
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	if _, err := s.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	truncateLastRecord(t, path, s, []byte(`{"n":2,"padding":"enough bytes to halve cleanly here too"}`))
	_ = s.Close()

	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("reopen a torn spool: %v", err)
	}
	defer func() { _ = s2.Close() }()

	if _, err := s2.Append([]byte(`{"n":"NEW-AFTER-TEAR"}`)); !errors.Is(err, ErrHookSpoolTorn) {
		t.Fatalf("Append after a torn record returned err=%v, want ErrHookSpoolTorn", err)
	}

	// The refusal must be PERMANENT for this instance, not one-shot.
	if _, err := s2.Append([]byte(`{"n":"STILL-REFUSED"}`)); !errors.Is(err, ErrHookSpoolTorn) {
		t.Fatalf("second Append after a torn record returned err=%v, want ErrHookSpoolTorn (refusal must be permanent)", err)
	}

	// The file on disk must hold exactly what it held before either refused Append --
	// no fabricated bytes, nothing silently merged.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spool file: %v", err)
	}
	if bytes.Contains(raw, []byte("NEW-AFTER-TEAR")) || bytes.Contains(raw, []byte("STILL-REFUSED")) {
		t.Fatalf("a refused Append wrote bytes into the spool file: %q", raw)
	}
}

// BLOCKER 3a: a reader whose cursor sits behind a compaction it never observed must
// see a reported gap over the records it lost, not a silent empty result.
func TestHookSpool_ReadFromDetectsAHoleBelowARetreatedCursor(t *testing.T) {
	s, _ := openTestSpool(t, 0)
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`), []byte(`{"n":4}`), []byte(`{"n":5}`)} {
		if _, err := s.Append(b); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Compact all the way through seq 5 -- as if some OTHER drain cycle already
	// folded everything -- while a reader's own cursor is still back at 2.
	if err := s.Compact(5); err != nil {
		t.Fatalf("Compact(5): %v", err)
	}

	recs, gapAt, hasGap, err := s.ReadFrom(2)
	if err != nil {
		t.Fatalf("ReadFrom(2): %v", err)
	}
	if !hasGap {
		t.Fatalf("ReadFrom(2) after Compact(5) reports no gap -- records 3-5 vanished from under a reader that never observed them")
	}
	if gapAt != 3 {
		t.Fatalf("gap boundary = %d, want 3 (the first sequence the cursor-2 reader never saw)", gapAt)
	}
	if len(recs) != 0 {
		t.Fatalf("ReadFrom(2) after Compact(5) returned %d record(s), want 0 (everything through 5 was folded away)", len(recs))
	}
}

// BLOCKER 3b: a spool file destroyed and recreated from scratch (sequencing reset)
// must never let a stale cursor read as "nothing to drain, forever" -- it must report
// a gap the moment the cursor is asked for again.
func TestHookSpool_ReadFromDetectsASequenceSpaceReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HookSpoolFile)
	s1, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`), []byte(`{"n":4}`), []byte(`{"n":5}`)} {
		if _, err := s1.Append(b); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	_ = s1.Close()

	// Simulate the spool file being destroyed and recreated from scratch (disk
	// repair, a wiped tmpfs session dir, ...): remove it and open a FRESH one at the
	// same path, with no floor sidecar either.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove spool file: %v", err)
	}
	if err := os.Remove(hookFloorPath(path)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove floor sidecar: %v", err)
	}
	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("reopen after destroy-and-recreate: %v", err)
	}
	defer func() { _ = s2.Close() }()

	// A reader whose persisted cursor still says 5 (from before the reset) must see
	// an honest gap, not a permanently-empty "nothing new" result.
	recs, gapAt, hasGap, err := s2.ReadFrom(5)
	if err != nil {
		t.Fatalf("ReadFrom(5): %v", err)
	}
	if !hasGap {
		t.Fatalf("ReadFrom(5) over a spool whose sequence space was reset under it reports no gap -- the caller would poll forever and never learn its cursor is unrecoverable")
	}
	if gapAt != 6 {
		t.Fatalf("gap boundary = %d, want 6 (the first sequence the reset reader can no longer trust)", gapAt)
	}
	if len(recs) != 0 {
		t.Fatalf("ReadFrom(5) after a sequence-space reset returned %d record(s), want 0", len(recs))
	}
}

// MEDIUM (dead code / evidence): testHookAfterSpoolFsync is the RED contract's named
// seam for the exact "accepted, not yet acked" crash window playbook 6.1 calls out --
// it fires inside Append, after the fsync commits and before Append returns (strictly
// earlier than hooksocket.go's servePost gets a chance to write its ack byte). This
// test installs it and proves that window resolves to "already visible to an
// independent reader of the same file", not merely that a client which hangs up early
// still sees it (r6_hooksocket_test.go's own TestHookSocket_RecordSurvivesEvenWhenThe
// ClientNeverSeesTheAck proves a client-side severance; this proves the SERVER-side
// window the seam actually names).
//
// R6 REVIEW FIX-PACK ROUND 1 (MEDIUM 5) -- RENAMED to what it actually proves. Its old
// name (…WindowIsAlreadyDurable) overclaimed: a second in-process handle reads through
// the SAME page cache, so this test stayed green with s.f.Sync() deleted outright.
// VISIBILITY is what it pins; DURABILITY -- the fsync call itself -- is pinned by
// TestHookSpool_AppendFsyncsExactlyOncePerAcceptedRecord (r6_fixpack_test.go).
func TestHookSpool_AcceptedNotYetAckedWindow_RecordIsAlreadyVisibleToASecondHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	defer func() { _ = s.Close() }()

	var sawDurable bool
	testHookAfterSpoolFsync = func() {
		// A FRESH, independent spool instance over the SAME file, standing in for the
		// daemon (or a restarted shim) observing the file at the exact instant a crash
		// here would land -- after Append's fsync, before it (and therefore servePost's
		// ack) has returned.
		s2, err := OpenHookSpool(path, 0)
		if err != nil {
			t.Errorf("open a second instance inside the after-fsync window: %v", err)
			return
		}
		defer func() { _ = s2.Close() }()
		recs, _, hasGap, err := s2.ReadFrom(0)
		if err != nil || hasGap || len(recs) != 1 {
			t.Errorf("record not visible to an independent handle inside the accepted-not-yet-acked window: recs=%+v hasGap=%v err=%v", recs, hasGap, err)
			return
		}
		sawDurable = true
	}
	defer func() { testHookAfterSpoolFsync = nil }()

	if _, err := s.Append([]byte(`{"event":"Stop"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !sawDurable {
		t.Fatalf("testHookAfterSpoolFsync never ran, or never observed the record")
	}
}

// Compaction persists its fold floor durably: a restart after a FULL compaction (the
// spool emptied entirely, no content left to recover a high-water mark from) must
// still remember the true last-appended sequence, both for Append's own monotonic
// numbering and for ReadFrom's stale-cursor detection.
func TestHookSpool_FullCompactionSurvivesRestartWithoutResettingSequencing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HookSpoolFile)
	s1, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	var lastSeq uint64
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`)} {
		seq, err := s1.Append(b)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		lastSeq = seq
	}
	if err := s1.Compact(lastSeq); err != nil { // fold EVERYTHING away
		t.Fatalf("Compact(%d): %v", lastSeq, err)
	}
	_ = s1.Close()

	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("reopen after a full compaction: %v", err)
	}
	defer func() { _ = s2.Close() }()

	seq, err := s2.Append([]byte(`{"n":4}`))
	if err != nil {
		t.Fatalf("Append after reopening a fully-compacted spool: %v", err)
	}
	if seq != lastSeq+1 {
		t.Fatalf("Append after reopening a fully-compacted spool assigned seq %d, want %d (sequencing must not reset to 1)", seq, lastSeq+1)
	}

	// A reader whose cursor predates the full compaction (e.g. cursor=1, folded away
	// long ago) must still be told honestly that it cannot resume from there.
	_, gapAt, hasGap, err := s2.ReadFrom(1)
	if err != nil {
		t.Fatalf("ReadFrom(1): %v", err)
	}
	if !hasGap {
		t.Fatalf("ReadFrom(1) after a restart post-full-compaction reports no gap -- the pre-compaction cursor should be recognized as behind the retained window")
	}
	if gapAt != 2 {
		t.Fatalf("gap boundary = %d, want 2", gapAt)
	}
}

// R6 REVIEW FIX-PACK, MEDIUM: Compact must never treat a copied-verbatim tail's
// FULL length as clean. A tear this instance has not yet discovered (external
// corruption landing after Open, past what Append's own shrink-detector can catch)
// found only by Compact's own tail scan must (a) latch gapAt so this instance stops
// accepting further writes and (b) leave s.size at the tail's true clean end, not
// its raw length -- the fabricated-merged-record hole this package's header comment
// says was removed.
func TestHookSpool_CompactDiscoversATearInItsOwnTailScanAndRefusesFurtherAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	defer func() { _ = s.Close() }()
	for _, b := range [][]byte{[]byte(`{"n":1}`), []byte(`{"n":2}`), []byte(`{"n":3}`)} {
		if _, err := s.Append(b); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// External corruption: bytes land on the file WITHOUT going through this live
	// instance -- too short to even be a record header. This instance has not
	// discovered any tear going in: Open ran before the corruption existed, and
	// Append's own shrink-detector only catches a file getting SMALLER than its
	// known-clean size, never larger.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open for external corruption: %v", err)
	}
	if _, err := f.Write([]byte("garbage")); err != nil {
		t.Fatalf("write external corruption: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Compact(2) folds through record 2 -- entirely below and unrelated to the
	// corruption at the tail -- but copies the tail (record 3 plus the garbage)
	// verbatim, and its own scan of that copy is where the tear must be found.
	if err := s.Compact(2); err != nil {
		t.Fatalf("Compact(2): %v", err)
	}
	if s.gapAt == 0 {
		t.Fatalf("Compact did not latch gapAt after its own tail scan found unparseable bytes")
	}

	// The fabricated-merge hole: an Append after Compact must REFUSE, never land
	// past the copied-verbatim garbage where a later parse could misread the
	// garbage's leading bytes as a header whose declared length swallows this very
	// Append's bytes as its "body".
	if _, err := s.Append([]byte(`{"n":"AFTER-COMPACT-DISCOVERED-TEAR"}`)); !errors.Is(err, ErrHookSpoolTorn) {
		t.Fatalf("Append after Compact discovered a tear in its own tail scan returned err=%v, want ErrHookSpoolTorn", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spool file: %v", err)
	}
	if bytes.Contains(raw, []byte("AFTER-COMPACT-DISCOVERED-TEAR")) {
		t.Fatalf("a refused Append wrote bytes into the spool file: %q", raw)
	}
}
