package shim

// R6 REVIEW FIX-PACK ROUND 1 (FAILING-FIRST, TDD RED, GG-5). Each function here pins one
// finding the rejecting reviewer raised against the delivered R6 spool slice and PROVED
// with a probe. They are written before the fix, and their failing run is captured in
// docs/verification/r6-red/fixpack-red.txt.
//
// THE SEAMS THIS FILE PINS (undefined symbols -> compile-fail RED):
//
//	// hookspool.go, MEDIUM 5: the fsync itself must be observable, not merely true by
//	// inspection. Deleting s.f.Sync() from Append left every existing durability test
//	// green, because a second in-process handle reads through the page cache.
//	var hookSpoolSync = func(f *os.File) error { return f.Sync() }
//
//	// hookspool.go, MEDIUM 7: ReadFrom must never hand back a record on the FAR side of
//	// a reported boundary. Its two gap branches disagreed: the torn-tail branch returns
//	// records strictly BELOW gapAt, the fold-hole branch returned records strictly ABOVE
//	// it, with a comment inviting the caller to apply them (ADR-017: never silently
//	// bridged). One rule now: every returned record is strictly below gapAt.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// countingSpoolSync swaps hookSpoolSync for a counter for the duration of a test and
// returns a reader for the count. The original is restored on cleanup.
func countingSpoolSync(t *testing.T) func() int {
	t.Helper()
	orig := hookSpoolSync
	var n int
	hookSpoolSync = func(f *os.File) error {
		n++
		return orig(f)
	}
	t.Cleanup(func() { hookSpoolSync = orig })
	return func() int { return n }
}

// ---------------------------------------------------------------------------
// MEDIUM 5: fsync-before-ack, asserted as a CALL rather than inferred from a read
// that the page cache would satisfy either way.
// ---------------------------------------------------------------------------

func TestHookSpool_AppendFsyncsExactlyOncePerAcceptedRecord(t *testing.T) {
	syncs := countingSpoolSync(t)

	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	defer func() { _ = s.Close() }()

	for i := 0; i < 3; i++ {
		if _, err := s.Append([]byte(`{"event":"Stop"}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if got := syncs(); got != 3 {
		t.Fatalf("Append fsynced %d time(s) for 3 accepted records, want exactly 3 -- a durably ACCEPTED record must be committed before Append returns (playbook 6.1)", got)
	}
}

func TestHookSpool_ARefusedAppendNeverClaimsADurableCommit(t *testing.T) {
	syncs := countingSpoolSync(t)

	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, hookRecordHeaderLen+8) // room for exactly one small record
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.Append([]byte(`{"a":1}`)); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	before := syncs()
	if _, err := s.Append([]byte(`{"a":2}`)); err == nil {
		t.Fatalf("Append past the size bound returned nil error, want ErrHookSpoolFull")
	}
	if got := syncs(); got != before {
		t.Fatalf("a REFUSED Append fsynced (%d -> %d): nothing was accepted, so nothing may be committed", before, got)
	}
}

// The socket-level half: the ack byte is the client's ONLY durability signal, so no ack
// may ever be written before the record's own fsync has returned. The counter is read at
// the instant the client observes the ack -- servePost runs Append and the ack write on
// one goroutine, so a count of 0 there would mean the ack outran (or replaced) the commit.
func TestHookSocket_AckIsNeverWrittenBeforeTheAppendFsync(t *testing.T) {
	syncs := countingSpoolSync(t)

	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"Stop","sequence":1}`), 3*time.Second) {
		t.Fatalf("hook post was never acked")
	}
	if got := syncs(); got < 1 {
		t.Fatalf("the shim acked a post after %d fsync(s): the ack is the client's only durability signal and must never precede the commit", got)
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

// ---------------------------------------------------------------------------
// MEDIUM 7: nothing past a boundary, ever -- from ReadFrom itself, not from the one
// consumer that happens to break at the boundary today.
// ---------------------------------------------------------------------------

func TestHookSpool_ReadFromNeverReturnsARecordAtOrPastTheGapBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Append([]byte(`{"n":0}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Fold through 3 out-of-band: a reader still sitting at cursor 1 now has a hole
	// (2,3 are gone) with records 4 and 5 retained ABOVE it.
	if err := s.Compact(3); err != nil {
		t.Fatalf("Compact(3): %v", err)
	}
	defer func() { _ = s.Close() }()

	recs, gapAt, hasGap, err := s.ReadFrom(1)
	if err != nil {
		t.Fatalf("ReadFrom(1): %v", err)
	}
	if !hasGap || gapAt != 2 {
		t.Fatalf("ReadFrom(1) = (hasGap=%v, gapAt=%d), want (true, 2)", hasGap, gapAt)
	}
	for _, r := range recs {
		if r.Seq >= gapAt {
			t.Fatalf("ReadFrom returned record seq=%d at/past the reported boundary %d -- ADR-017 forbids handing back the far side of a hole as if it were applicable (recs=%+v)", r.Seq, gapAt, recs)
		}
	}
}

// ---------------------------------------------------------------------------
// LOW 10: permission coverage. The spool file and its compacted replacement are
// already pinned 0600; the fold-floor sidecar and the hook socket itself were not.
// ---------------------------------------------------------------------------

func TestHookSpool_FoldFloorSidecarIsOwnerOnly0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact(1): %v", err)
	}
	fi, err := os.Stat(hookFloorPath(path))
	if err != nil {
		t.Fatalf("stat fold floor sidecar: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("fold floor sidecar mode = %v, want 0600 -- it names how far the spool has been folded, and the spool itself is owner-only", fi.Mode().Perm())
	}
}

func TestHookSocket_SocketFileIsOwnerOnly0600(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)
	// Dialing proves the listener is bound before the mode is read.
	conn := dialHookSocket(t, cfg.HookSocketPath)
	_ = conn.Close()

	fi, err := os.Lstat(cfg.HookSocketPath)
	if err != nil {
		t.Fatalf("lstat hook socket: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("hook socket mode = %v, want 0600 -- same-user filesystem permissions are the PRIMARY control on this socket", fi.Mode().Perm())
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}
