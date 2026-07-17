package shim

// Epic 4 review-fix round (audit-003). NEW tests only — the frozen designer
// files are not touched. Each test here fails against the pre-fix engine and
// passes once the corresponding fix lands:
//
//	F1  group-empty escalation + bounded PTY-EOF wait (S5/S-4/E4.4)
//	F2  reply writer must never block the PTY drain (S9)
//	F3  transcript Flush wrapped in a finalization timeout (S9)
//	F4  G3 persistence integrity (no exit.json without a snapshot; err surfaced)
//	F6  empty/nil Argv -> clean setup error, not an index panic
//	F7  socket file gone after Run returns

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// ---------------------------------------------------------------------------
// F1 — a leader that spawns a same-group child, used to prove group
// containment when the leader goes away but a descendant lingers on the PTY.
// The frozen helpers cannot express "cooperative/natural leader + stubborn
// child", so this test group builds its own tiny agent.
// ---------------------------------------------------------------------------

// f1agentSrc is a self-contained agent: a leader that spawns a TERM-ignoring
// child in its own process group (the child inherits the PTY slave and holds
// it open), with the leader's own disposition chosen by F1_LEADER.
const f1agentSrc = `package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if os.Getenv("F1_ROLE") == "child" {
		signal.Ignore(syscall.SIGTERM)
		fmt.Printf("CHILD_PID\t%d\n", os.Getpid())
		if os.Getenv("F1_CHILD_NOPTY") == "1" {
			// Release every PTY fd, then park: the child ignores TERM but does
			// NOT hold the PTY, so the leader's reap yields PTY EOF at once and
			// only a synchronous group KILL (not the drain-EOF wait) can contain
			// this child.
			os.Stdin.Close()
			os.Stdout.Close()
			os.Stderr.Close()
			time.Sleep(time.Hour)
			return
		}
		io.Copy(io.Discard, os.Stdin) // hold the PTY slave; only a group KILL ends this
		return
	}
	exe, _ := os.Executable()
	child := exec.Command(exe)
	child.Env = append(os.Environ(), "F1_ROLE=child")
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	// No SysProcAttr: the child inherits the leader's process group, so a
	// group signal reaches it too (S5 containment).
	_ = child.Start()
	fmt.Printf("PARENT_PID\t%d\n", os.Getpid())
	if os.Getenv("F1_LEADER") == "exit" {
		ms, _ := strconv.Atoi(os.Getenv("F1_EXIT_MS"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
		os.Exit(0) // natural exit while the child still holds the PTY
	}
	io.Copy(io.Discard, os.Stdin) // cooperative: default TERM disposition ends this
}
`

// f1WaitPID reads a "<label>\t<pid>" line the agent printed, tolerant of when
// the client attached: it scans both the live TDataOut stream (where a tab
// survives) and any attach snapshot's grid (where the emulator expands the tab
// to spaces), so it never depends on the attach racing the agent's first
// output.
func f1WaitPID(t *testing.T, c *shimClient, label string, timeout time.Duration) int {
	t.Helper()
	re := regexp.MustCompile(label + `\s+(\d+)`)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := re.FindSubmatch(c.dataOut()); m != nil {
			pid, _ := strconv.Atoi(string(m[1]))
			return pid
		}
		for _, f := range c.frames() {
			if f.typ != wire.TSnapshot {
				continue
			}
			if s, err := vt.DecodeSnapshot(f.payload); err == nil {
				if m := re.FindStringSubmatch(gridText(s)); m != nil {
					pid, _ := strconv.Atoi(m[1])
					return pid
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("PID %s not seen within %s", label, timeout)
	return 0
}

// buildF1Agent compiles f1agentSrc into t.TempDir and returns the binary path.
func buildF1Agent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(f1agentSrc), 0o600); err != nil {
		t.Fatalf("write f1 agent source: %v", err)
	}
	bin := filepath.Join(dir, "f1agent")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build f1 agent: %v", err)
	}
	return bin
}

// f1Config builds a Config running the F1 agent with the given leader env.
func f1Config(t *testing.T, grace time.Duration, env ...string) Config {
	t.Helper()
	return Config{
		SessionID:     "f1",
		Argv:          []string{buildF1Agent(t)},
		Cwd:           t.TempDir(),
		Env:           env,
		SocketPath:    newSocketPath(t),
		SessionDir:    t.TempDir(),
		Cols:          80,
		Rows:          24,
		TranscriptCfg: transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3},
		GraceTimeout:  grace,
		Metrics:       &Metrics{},
	}
}

// F1 (escalation not cancelled on leader reap) — a cooperative leader dies on
// TERM, but its TERM-ignoring child survives and holds the PTY. The escalation
// must STILL SIGKILL the group at grace (rather than cancel on the leader's
// reap), and the bounded EOF wait must let Run finalize. Pre-fix: the child
// survives forever and Run hangs on PTY EOF.
func TestF1_TermIgnoringChildKilledAfterLeaderCooperates(t *testing.T) {
	grace := 800 * time.Millisecond
	cfg := f1Config(t, grace) // F1_LEADER unset => cooperative leader
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	parentPID := f1WaitPID(t, c, "PARENT_PID", 5*time.Second)
	childPID := f1WaitPID(t, c, "CHILD_PID", 5*time.Second)
	if parentPID == childPID {
		t.Fatalf("parent and child share pid %d", parentPID)
	}

	sent := time.Now()
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigTerm})
	r := waitRun(t, ch, 10*time.Second)
	if elapsed := time.Since(sent); elapsed > grace+4*time.Second {
		t.Errorf("Run took %s after TERM, want bounded (~grace) — escalation/EOF wait did not converge", elapsed)
	}
	_ = r
	if !processGone(childPID, 3*time.Second) {
		t.Errorf("TERM-ignoring child %d survived — escalation was cancelled on the leader's reap (S5 violated)", childPID)
	}
	if !processGone(parentPID, 3*time.Second) {
		t.Errorf("leader %d still alive", parentPID)
	}
	// Finalization still produced the side-files.
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, ExitFile)); err != nil {
		t.Errorf("exit.json missing after group-contained TERM: %v", err)
	}
}

// F1 (bounded EOF wait on natural exit) — the leader exits 0 on its own while a
// stubborn child still holds the PTY slave open. Run must not block forever
// waiting for EOF: it bounds the wait, SIGKILLs the group, and finalizes with
// the side-files written and the leader's exit code reported. Pre-fix: Run
// hangs on the unconditional PTY-EOF wait.
func TestF1_NaturalExitWithLingeringChildStillFinalizes(t *testing.T) {
	grace := 800 * time.Millisecond
	cfg := f1Config(t, grace, "F1_LEADER=exit", "F1_EXIT_MS=400")
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	childPID := f1WaitPID(t, c, "CHILD_PID", 5*time.Second)

	r := waitRun(t, ch, 10*time.Second) // must return; pre-fix this hangs
	if r.err != nil {
		t.Errorf("Run err = %v, want nil", r.err)
	}
	if r.exit != 0 {
		t.Errorf("Run agentExit = %d, want 0 (leader exited 0 naturally)", r.exit)
	}
	if !processGone(childPID, 3*time.Second) {
		t.Errorf("lingering child %d survived — the group was not killed after the bounded EOF wait", childPID)
	}
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitCode != 0 || ei.ExitSignal != "" {
		t.Errorf("exit.json = {code:%d sig:%q}, want {0 \"\"}", ei.ExitCode, ei.ExitSignal)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, SnapshotFile)); err != nil {
		t.Errorf("final snapshot missing: %v", err)
	}
}

// R1 (re-review) — after a fast COOPERATIVE exit, a TERM-ignoring descendant
// that does NOT hold the PTY must be contained by the synchronous final KILL
// during finalization, not by a grace-timer that outlives Run. Grace is set
// long: a leaked escalation timer would kill the child only at t+grace (after
// Run has returned, at a possibly-reused pgid). The fix reaps the child before
// Run returns and joins the worker, so no goroutine can signal the pgid after.
func TestR1_SurvivorKilledSynchronouslyNotByLeakedTimer(t *testing.T) {
	grace := 10 * time.Second // long: a leaked grace-timer would fire well after Run returns
	cfg := f1Config(t, grace, "F1_LEADER=cooperative", "F1_CHILD_NOPTY=1")
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	childPID := f1WaitPID(t, c, "CHILD_PID", 5*time.Second)

	sent := time.Now()
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigTerm})
	r := waitRun(t, ch, 15*time.Second)
	if r.err != nil {
		t.Errorf("Run err = %v, want nil", r.err)
	}
	// Finalization must complete well before the grace timer would fire:
	// containment came from the synchronous KILL, not from waiting out (or
	// leaking) the timer.
	if elapsed := time.Since(sent); elapsed >= grace {
		t.Errorf("Run took %s (>= grace %s) — finalization relied on the escalation timer, not a synchronous kill", elapsed, grace)
	}
	// The child must ALREADY be dead just after Run returns. Pre-fix it survives
	// until the leaked timer fires at t+grace.
	if !processGone(childPID, 1*time.Second) {
		t.Errorf("TERM-ignoring child %d still alive just after Run returned — left to a leaked escalation timer instead of reaped synchronously (R1)", childPID)
	}
}

// ---------------------------------------------------------------------------
// F2 — reply writer must never block the drain.
// ---------------------------------------------------------------------------

// dsrFloodScript emits n cursor-position queries (each followed by a visible
// marker) then a QDONE marker. A fakeagent never reads its stdin, so the
// emulator's replies pile up on the PTY input with no reader.
func dsrFloodScript(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "print \x1b[6nQ%05d\n", i)
	}
	b.WriteString("print QDONE\n")
	b.WriteString("exit 0\n")
	return b.String()
}

// F2 / S9 — an agent floods terminal queries without reading stdin. The
// emulator's query replies must be routed through a bounded, dropping queue so
// the reply write never blocks the vt drain (and thus the PTY drain): the grid
// keeps advancing and the agent runs to completion. Pre-fix: the reply write
// blocks on the full PTY, wedges Feed under hub.mu, stalls the drain, and Run
// hangs.
func TestF2_QueryFloodDoesNotWedgeDrain(t *testing.T) {
	cfg := fakeAgentConfig(t, dsrFloodScript(2000))
	r := waitRun(t, runShimAsync(cfg), 25*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	// The drain kept advancing all the way to the final marker.
	tr := readTranscript(t, cfg.SessionDir)
	if !strings.Contains(tr, "QDONE") {
		t.Errorf("transcript missing QDONE — the query flood wedged the drain (S9):\n%s", lastNonEmptyLine(tr))
	}
	snap := decodeFinalSnapshot(t, cfg.SessionDir)
	if !strings.Contains(gridText(snap), "QDONE") {
		t.Errorf("final grid missing QDONE — grid did not advance under the reply flood:\n%s", gridText(snap))
	}
}

// ---------------------------------------------------------------------------
// F3 — transcript Flush must be timeout-protected at finalization.
// ---------------------------------------------------------------------------

// F3 / S9 — a wedged transcript sink (a FIFO no one drains) must not hang
// finalization: the exit-time Flush is bounded by the same timeout mechanism as
// Close, so Run finalizes and writes its side-files. Pre-fix: the un-timed
// Flush blocks forever and Run never returns.
func TestF3_WedgedTranscriptFlushTimesOut(t *testing.T) {
	orig := finalizeStepTimeout
	finalizeStepTimeout = 300 * time.Millisecond
	defer func() { finalizeStepTimeout = orig }()

	cfg := helperConfig(t, modeFloodIdle, nil, nil)
	fifo := filepath.Join(cfg.SessionDir, TranscriptFile)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo transcript: %v", err)
	}
	// Hold the read end open so the shim's write-open unblocks, but never read,
	// so the pipe fills and the transcript's sink.Write wedges. The reader's
	// O_RDONLY open and the shim's O_WRONLY open rendezvous — starting the shim
	// after launching this goroutine is enough; waiting on the open here would
	// deadlock (it blocks until the shim opens the write end).
	holderStop := make(chan struct{})
	go func() {
		rf, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			return
		}
		<-holderStop
		rf.Close()
	}()
	defer close(holderStop)

	ch := runShimAsync(cfg)
	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	c.waitOutput("F", 5*time.Second)   // flood underway
	time.Sleep(500 * time.Millisecond) // let the FIFO fill and the transcript drain wedge
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})

	r := waitRun(t, ch, 10*time.Second) // must finalize despite the wedged transcript
	_ = r
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, ExitFile)); err != nil {
		t.Errorf("exit.json missing after wedged-transcript finalization (Flush hung the shim): %v", err)
	}
}

// ---------------------------------------------------------------------------
// F4 — G3 persistence integrity.
// ---------------------------------------------------------------------------

// F4 / G3 — when the final-snapshot write fails, the shim must NOT write
// exit.json (its presence must imply a complete snapshot) and must surface the
// failure via Run's returned err, while still reporting the agent's exit code.
// The snapshot write is obstructed by placing a directory where the file must
// go, so the atomic rename fails.
func TestF4_SnapshotWriteFailureLeavesNoExitJSON(t *testing.T) {
	cfg := fakeAgentConfig(t, "print hi\nexit 0\n")
	if err := os.Mkdir(filepath.Join(cfg.SessionDir, SnapshotFile), 0o755); err != nil {
		t.Fatalf("obstruct snapshot path: %v", err)
	}
	r := waitRun(t, runShimAsync(cfg), 15*time.Second)
	if r.err == nil {
		t.Errorf("Run err = nil, want a persistence error when the snapshot write fails")
	}
	if r.exit != 0 {
		t.Errorf("Run agentExit = %d, want 0 (the agent's exit code is still reported)", r.exit)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, ExitFile)); !os.IsNotExist(err) {
		t.Errorf("exit.json exists despite a failed snapshot write — G3 integrity violated (stat err=%v)", err)
	}
}

// F4 (happy path) — a clean run persists both side-files and returns nil,
// proving the added parent-dir fsync does not break the normal path.
func TestF4_HappyPathPersistsAndReturnsNil(t *testing.T) {
	cfg := fakeAgentConfig(t, "print ok\nexit 0\n")
	r := waitRun(t, runShimAsync(cfg), 15*time.Second)
	if r.err != nil {
		t.Fatalf("Run err = %v, want nil on the happy path", r.err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, SnapshotFile)); err != nil {
		t.Errorf("snapshot missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, ExitFile)); err != nil {
		t.Errorf("exit.json missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// F6 — empty/nil Argv -> clean setup error, not a panic.
// ---------------------------------------------------------------------------

func TestF6_EmptyArgvIsCleanSetupError(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	cfg.Argv = nil
	exit, err := Run(cfg)
	if err == nil {
		t.Errorf("Run err = nil, want a setup error for empty Argv")
	}
	if exit != 0 {
		t.Errorf("Run agentExit = %d, want 0 for a setup failure", exit)
	}

	cfg2 := helperConfig(t, modeIdle, nil, nil)
	cfg2.Argv = []string{}
	if _, err := Run(cfg2); err == nil {
		t.Errorf("Run err = nil for empty (non-nil) Argv, want a setup error")
	}
}

// ---------------------------------------------------------------------------
// F7 — socket file removed after Run returns (contract regression guard).
// ---------------------------------------------------------------------------

func TestF7_SocketFileGoneAfterRun(t *testing.T) {
	cfg := fakeAgentConfig(t, "print bye\nexit 0\n")
	r := waitRun(t, runShimAsync(cfg), 15*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if _, err := os.Stat(cfg.SocketPath); !os.IsNotExist(err) {
		t.Errorf("socket %s still present after Run returned (stat err=%v)", cfg.SocketPath, err)
	}
}
