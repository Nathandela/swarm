package skeleton

// T2.1.a (agents-tracker-vyd, R2.1.3) — captureConversationID's transcript read
// must be growth-gated: re-read the tail ONLY when the file's size has changed
// since the last scan (grown, or shrunk/rotated), not on every poll. The session
// is launched with the "reference" adapter (registry.New("reference") needs no
// live process, so the AgentType is set on an otherwise-real fake-agent session —
// same trick capture_c1_e2e_test.go's Agent:"reference" launch relies on, minus
// the real reference-cli binary). Its transcript file is then mutated directly on
// disk (the agent is parked in a long idle, so nothing else touches the file),
// which gives byte-exact control over growth/shrink timing that waiting on a real
// script's scheduled output cannot. readTail is swapped for a call-counting
// wrapper around the real reader so "did a re-read happen" is observed directly.

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
)

// convScanHarness assembles a real core with one long-idling "reference" session
// and a read-counting readTail seam, so a test can drive captureConversationID
// directly and assert exactly how many times the transcript was actually read.
func convScanHarness(t *testing.T) (d *Daemon, id string, transcriptPath string, reads func() int32) {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("/tmp", "swskconv")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 8,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	m, err := core.Launch(daemon.LaunchSpec{
		// AgentType is spoofed as "reference" so registry.New resolves the real
		// refadapter (which scans for "conv-id=" in the transcript tail) while the
		// process itself is the ordinary fake agent — no real reference-cli needed.
		AgentType: "reference",
		Argv:      []string{fakeAgentBin, mustScript(t, "print booting\nidle 60s\n")},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("core Launch: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})

	tp := filepath.Join(dir, m.ID, "transcript.log")
	waitForFile(t, tp)

	var n int32
	orig := readTail
	readTail = func(path string, max int64) []byte {
		atomic.AddInt32(&n, 1)
		return orig(path, max)
	}
	t.Cleanup(func() { readTail = orig })

	d = &Daemon{core: core, stateDir: dir}
	return d, m.ID, tp, func() int32 { return atomic.LoadInt32(&n) }
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("transcript file %s never appeared with content", path)
}

// TestCaptureConversationID_UnchangedSizeSkipsReread: back-to-back calls against
// an unchanged transcript must re-read exactly once (the first scan), not twice
// (fails today: captureConversationID unconditionally reads every call).
func TestCaptureConversationID_UnchangedSizeSkipsReread(t *testing.T) {
	d, id, _, reads := convScanHarness(t)

	d.captureConversationID(id)
	first := reads()
	if first == 0 {
		t.Fatal("first scan performed no read at all")
	}

	d.captureConversationID(id)
	if got := reads(); got != first {
		t.Fatalf("unchanged transcript size triggered a re-read: %d reads -> %d reads, want no change", first, got)
	}
}

// TestCaptureConversationID_GrowthTriggersRereadAndCapturesLateID: growth after an
// initial no-marker scan must trigger a re-read, and a marker that only appears
// LATE (after the first, failed scan) must still be captured — the tap must not
// permanently give up on a session that has not printed its id yet.
func TestCaptureConversationID_GrowthTriggersRereadAndCapturesLateID(t *testing.T) {
	d, id, tp, reads := convScanHarness(t)

	d.captureConversationID(id) // first scan: no marker yet
	first := reads()
	if m, _ := d.core.Get(id); m.ConversationID != "" {
		t.Fatalf("captured a conversation id before one was ever printed: %q", m.ConversationID)
	}

	appendToFile(t, tp, "conv-id=late-arriving-id\n")

	d.captureConversationID(id) // growth: must re-read and this time extract
	if got := reads(); got <= first {
		t.Fatalf("file growth did not trigger a re-read: %d reads -> %d reads", first, got)
	}
	m, ok := d.core.Get(id)
	if !ok || m.ConversationID != "late-arriving-id" {
		t.Fatalf("ConversationID = %q (ok=%v), want %q captured from the grown tail", m.ConversationID, ok, "late-arriving-id")
	}

	// Once captured, the id is write-once: further growth is a no-op and short-
	// circuits before ever reaching the read-gating logic.
	afterCapture := reads()
	appendToFile(t, tp, "conv-id=should-be-ignored\n")
	d.captureConversationID(id)
	if got := reads(); got != afterCapture {
		t.Fatalf("captureConversationID re-read after the id was already captured: %d reads -> %d reads", afterCapture, got)
	}
	if m, _ := d.core.Get(id); m.ConversationID != "late-arriving-id" {
		t.Fatalf("ConversationID changed after write-once capture: %q", m.ConversationID)
	}
}

// TestCaptureConversationID_ShrinkTriggersRescan: a shrunk file (rotation) must
// force a rescan even though the size went DOWN, not just up.
func TestCaptureConversationID_ShrinkTriggersRescan(t *testing.T) {
	d, id, tp, reads := convScanHarness(t)

	d.captureConversationID(id) // establishes a non-zero last-scanned size
	before := reads()

	// Simulate rotation: truncate to a smaller file carrying the marker.
	if err := os.WriteFile(tp, []byte("conv-id=post-rotation-id\n"), 0o600); err != nil {
		t.Fatalf("truncate transcript: %v", err)
	}

	d.captureConversationID(id)
	if got := reads(); got <= before {
		t.Fatalf("shrunk transcript did not trigger a rescan: %d reads -> %d reads", before, got)
	}
	if m, _ := d.core.Get(id); m.ConversationID != "post-rotation-id" {
		t.Fatalf("ConversationID = %q, want the post-rotation marker captured", m.ConversationID)
	}
}

// TestCaptureConversationID_DiskErrorToleratedThenRecovers: a transcript that is
// temporarily unreadable must not panic or wedge future scans — once the file is
// back, a normal scan (including capture) proceeds.
func TestCaptureConversationID_DiskErrorToleratedThenRecovers(t *testing.T) {
	d, id, tp, _ := convScanHarness(t)

	if err := os.Remove(tp); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}

	d.captureConversationID(id) // must not panic despite the missing file
	if m, _ := d.core.Get(id); m.ConversationID != "" {
		t.Fatalf("captured a conversation id off a missing transcript: %q", m.ConversationID)
	}

	if err := os.WriteFile(tp, []byte("conv-id=recovered-id\n"), 0o600); err != nil {
		t.Fatalf("recreate transcript: %v", err)
	}
	d.captureConversationID(id)
	if m, _ := d.core.Get(id); m.ConversationID != "recovered-id" {
		t.Fatalf("ConversationID = %q after recovery, want the id captured once the file returned", m.ConversationID)
	}
}

func appendToFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}
