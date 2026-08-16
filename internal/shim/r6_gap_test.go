package shim

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5) tests for playbook §6.1's
// gap-honesty rule: "Disk-full or corrupt-spool [behavior] must let the agent continue
// locally, record the earliest provable gap, and show the surviving transcript read-only" --
// and ADR-013's sacred rule that none of this ever touches the PTY: "nothing in this program
// reads the terminal for structure and nothing in it alters how the terminal works."
//
// This file corrupts a HookSpool's on-disk file directly (truncating a record mid-write, the
// deterministic stand-in for a torn write across a crash -- the same "seed the durable state
// then exercise it" style r6_hookspool_test.go and this repo's other durable-state tests
// use) and pins two things: HookSpool.ReadFrom stops at the exact boundary sequence rather
// than skipping past or silently losing the corrupt tail, and a corrupted hook spool never
// reaches the PTY -- the agent keeps running, its output keeps flowing, and the control
// socket keeps serving, unaffected.
//
// No new symbols beyond r6_hookspool_test.go's HookSpool and r6_hooksocket_test.go's drain
// wire (HookDrainTag/HookDrainRequest/HookDrainResponse) are introduced here.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// truncateLastRecord appends one more record via s, then chops the file at a point strictly
// inside that record's on-disk bytes -- format-agnostic (it only relies on file-size deltas,
// never on HookSpool's internal encoding), and deterministic: whatever the encoding, cutting
// a nonzero record's bytes in half always yields an incomplete final record.
func truncateLastRecord(t *testing.T, path string, s *HookSpool, body []byte) (seq uint64) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before append: %v", err)
	}
	seq, err = s.Append(body)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after append: %v", err)
	}
	grew := after.Size() - before.Size()
	if grew <= 1 {
		t.Fatalf("appending one record only grew the file by %d byte(s); cannot truncate it in half", grew)
	}
	cut := before.Size() + grew/2
	if err := os.Truncate(path, cut); err != nil {
		t.Fatalf("truncate spool file to %d: %v", cut, err)
	}
	return seq
}

// ---------------------------------------------------------------------------
// (4) gap honesty: HookSpool.ReadFrom stops at the earliest provable gap.
// ---------------------------------------------------------------------------

func TestHookSpool_ReadFromStopsAtATruncatedRecord(t *testing.T) {
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
		t.Fatalf("reopen truncated spool: %v", err)
	}
	defer func() { _ = s2.Close() }()

	recs, gapAt, hasGap, err := s2.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0) over a truncated spool returned an error rather than a reported gap: %v", err)
	}
	if !hasGap {
		t.Fatalf("ReadFrom(0) reports no gap over a spool whose last record was truncated mid-write")
	}
	if gapAt != brokenSeq {
		t.Fatalf("gap boundary = %d, want %d (the exact sequence of the torn record — not the last valid one, not a rounded-up guess)", gapAt, brokenSeq)
	}
	if len(recs) != 2 || recs[0].Seq != 1 || recs[1].Seq != 2 {
		t.Fatalf("records before the gap = %+v, want [seq=1, seq=2] — every record fsynced BEFORE the tear must still be honored", recs)
	}
}

func TestHookSpool_ReadFromAfterAGapCursorStillReportsTheSameGap(t *testing.T) {
	// A drain that already applied records 1..N before discovering the gap at N+1 must see
	// the identical gap on a retry from cursor N -- the boundary is a property of the spool,
	// not of where the reader started.
	path := filepath.Join(t.TempDir(), HookSpoolFile)
	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	if _, err := s.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	brokenSeq := truncateLastRecord(t, path, s, []byte(`{"n":2,"padding":"enough bytes to halve cleanly here too"}`))
	_ = s.Close()

	s2, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	recs, gapAt, hasGap, err := s2.ReadFrom(1) // already drained through seq 1
	if err != nil {
		t.Fatalf("ReadFrom(1): %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("ReadFrom(1) over a spool whose only record past 1 is torn returned %d record(s), want 0", len(recs))
	}
	if !hasGap || gapAt != brokenSeq {
		t.Fatalf("ReadFrom(1) gap = (hasGap=%v, at=%d), want (true, %d)", hasGap, gapAt, brokenSeq)
	}
}

// ---------------------------------------------------------------------------
// (4) gap honesty over the wire: a drain request against a corrupted spool reports the
// gap rather than hanging, erroring the connection, or silently truncating in place.
// ---------------------------------------------------------------------------

func TestHookSocket_DrainOverACorruptedSpoolReportsTheGapBoundary(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"UserPromptSubmit"}`), 3*time.Second) {
		t.Fatalf("post 1 not acked")
	}
	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"PreToolUse"}`), 3*time.Second) {
		t.Fatalf("post 2 not acked")
	}

	// Stop the shim to corrupt its spool file safely (no concurrent writer), then restart a
	// second instance over the same session dir -- exactly TestHookSocket_
	// RestartOverTheSameSessionDirSeesEveryAckedPost's shape, but with a torn tail seeded in
	// between.
	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)

	spoolPath := filepath.Join(cfg.SessionDir, HookSpoolFile)
	s, err := OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool to seed a tear: %v", err)
	}
	brokenSeq := truncateLastRecord(t, spoolPath, s, []byte(`{"event":"PostToolUse","padding":"enough to halve cleanly"}`))
	_ = s.Close()

	cfg2 := helperConfig(t, modeIdle, nil, nil)
	cfg2.SessionDir = cfg.SessionDir
	cfg2.HookSocketPath = newSocketPath(t)
	ch2 := runShimAsync(cfg2)

	resp := drainOnce(t, cfg2.HookSocketPath, 0, 0)
	if !resp.Gap {
		t.Fatalf("drain over a spool with a torn tail reports no gap")
	}
	if resp.GapBoundary != brokenSeq {
		t.Fatalf("drain gap boundary = %d, want %d", resp.GapBoundary, brokenSeq)
	}
	if len(resp.Records) != 2 || resp.Records[0].Seq != 1 || resp.Records[1].Seq != 2 {
		t.Fatalf("drain records before the gap = %+v, want [seq=1, seq=2]", resp.Records)
	}

	ctl2 := dialShim(t, cfg2.SocketPath)
	ctl2.startReader()
	ctl2.hello(shimwire.Version)
	killShim(ctl2)
	waitRun(t, ch2, 10*time.Second)
}

// ---------------------------------------------------------------------------
// (4) the PTY is sacred: a corrupted hook spool never touches it. The agent keeps running,
// its output keeps flowing, and the control socket keeps serving attach/signal normally --
// ADR-013's "nothing in this program reads the terminal for structure and nothing in it
// alters how the terminal works", now proven under a FAILING structured-capture channel.
// ---------------------------------------------------------------------------

func TestHookSocket_PTYSurvivesACorruptedSpool(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"UserPromptSubmit"}`), 3*time.Second) {
		t.Fatalf("post 1 not acked")
	}

	// Corrupt the spool file WHILE the shim is still running and driving the agent's PTY --
	// the failure must stay confined to the hook plane.
	spoolPath := filepath.Join(cfg.SessionDir, HookSpoolFile)
	info, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat live spool file: %v", err)
	}
	if info.Size() < 2 {
		t.Fatalf("live spool file too small (%d bytes) to truncate meaningfully", info.Size())
	}
	if err := os.Truncate(spoolPath, info.Size()/2); err != nil {
		t.Fatalf("truncate live spool file: %v", err)
	}

	// The PTY/control plane must be entirely unaffected: attach still works and shows the
	// agent's real output. Poll (waitObserved), rather than grabbing whatever the FIRST
	// snapshot frame happens to hold: a single firstSnapshot races the agent's own
	// startup (the frame can legitimately arrive before "IDLING" ever reaches the grid),
	// which is exactly what made this assertion flaky under -race.
	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	c.waitObserved("IDLING", 5*time.Second)

	// A drain against the now-corrupted spool must report the gap, not hang or crash the
	// listener (a wedged/crashed hook socket would leave killShim's control-socket signal as
	// the only proof of life, which is exactly what this asserts next).
	resp := drainOnce(t, cfg.HookSocketPath, 0, 0)
	if !resp.Gap {
		t.Fatalf("drain over the live-corrupted spool reports no gap")
	}

	killShim(c)
	waitRun(t, ch, 10*time.Second)
}
