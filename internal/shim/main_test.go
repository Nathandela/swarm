// Package shim is the Epic 4 shim engine: the per-session process that owns the
// PTY, execs the agent in its own process group from an argv array + captured
// env, serves the per-session UDS (G2 message set), pipes PTY bytes into the VT
// emulator + transcript, and — surviving the daemon indefinitely — always
// drains the PTY, then on agent exit writes the final snapshot + exit side-file
// (G3). This is the security-critical heart of ADR-001.
//
// These are FAILING-FIRST white-box tests (package shim). They exercise the
// frozen production API a separate implementer will build:
//
//	type Config struct {
//	    SessionID     string
//	    Argv          []string      // argv[0] = program; exec'd directly, never via a shell
//	    Cwd           string
//	    Env           []string      // pre-filtered by caller
//	    SocketPath    string        // per-session UDS
//	    SessionDir    string        // side-files: final-snapshot.bin, exit.json, transcript.log
//	    Cols, Rows    int
//	    TranscriptCfg transcript.Config
//	    GraceTimeout  time.Duration // TERM->KILL grace
//	    Metrics       *Metrics      // optional, test-observable counters
//	}
//	type Metrics struct{ FramesDropped atomic.Int64 }
//	type ExitInfo struct {  // decoded exit.json
//	    ExitCode   int       `json:"exit_code"`
//	    ExitSignal string    `json:"exit_signal"`
//	    FinishedAt time.Time `json:"finished_at"`
//	}
//	const ( SnapshotFile="final-snapshot.bin"; ExitFile="exit.json"; TranscriptFile="transcript.log" )
//	func Run(cfg Config) (agentExit int, err error)  // blocks until agent exit
//
// DRIVER STRATEGY (orchestrator brief):
//   - The re-exec helper agent (TestHelperProcess-style, gated by an env var and
//     intercepted in TestMain) is the workhorse: it runs arbitrary Go as the
//     agent under the shim's PTY, so it can print its own argv/env/cwd/pgid,
//     answer terminal queries, spawn a same-group child, or stream on demand —
//     giving exact assertions no scripted binary could.
//   - swarm-fake-agent (built once in TestMain) drives the plain lifecycle
//     cases (clean exit codes) where a scripted agent suffices.
//
// Every integration test carries a deadline; nothing may hang. Tests only ever
// open ONE shim connection at a time (the daemon is the sole client — v1 shim
// pin); sequential reconnects are used where a fresh view is needed.
package shim

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
	"github.com/charmbracelet/x/term"
)

// ---------------------------------------------------------------------------
// Re-exec helper agent
// ---------------------------------------------------------------------------

// helperEnvVar gates the re-exec: when set in a process's environment, TestMain
// runs that helper mode as the agent instead of the test suite. Because the
// shim spawns the agent with cfg.Env exactly, a test activates a helper by
// putting helperEnvVar=<mode> in cfg.Env.
const helperEnvVar = "SWARM_SHIM_TEST_HELPER"

// Helper modes. Each is a distinct agent behavior under the shim's PTY.
const (
	modeInfo          = "info"             // print argv/env/cwd/pid/pgid, exit 0 (E4.1a-d)
	modeDSR           = "dsr"              // emit DSR, read reply, print DSR_OK/DSR_TIMEOUT (emulator-replies carry-forward)
	modeWinsize       = "winsize"          // print PTY winsize at start + on each SIGWINCH, block (E4.2 resize)
	modeStreamBlock   = "stream-block"     // phase1, block on stdin, phase2, exit (E4.3/S10 deterministic boundary)
	modeStreamActive  = "stream-active"    // stream a contiguous integer sequence continuously until killed (E4.3/S10 under load)
	modeTermStubborn  = "term-stubborn"    // ignore TERM, spawn same-group child, print both PIDs, block (E4.4/S5)
	modeChildStubborn = "child-stubborn"   // ignore TERM, print PID, block (child of term-stubborn)
	modeTermCooperate = "term-cooperative" // default TERM disposition (dies on TERM), print PID, block (E4.4/S5)
	modeIdle          = "idle"             // print IDLING, block (kill/exit side-file cases)
	modeBurstExit     = "burst-exit"       // print many lines + BURST_DONE, exit 0 (no-consumer drain, E4.6/S1)
	modeFloodIdle     = "flood-idle"       // flood output continuously until killed (wedged-consumer, E4.6/S9)
)

// stream-block phase sizing (E4.3). Kept small; the grid is 24 rows, so the
// last phase1 lines (incl. PHASE1_DONE) are what a snapshot can show.
const (
	phase1Lines = 40
	phase2Lines = 40
)

// runHelperAgent executes one helper mode as the agent, then exits the process.
// It never returns.
func runHelperAgent(mode string) {
	switch mode {
	case modeInfo:
		helperInfo()
	case modeDSR:
		helperDSR()
	case modeWinsize:
		helperWinsize()
	case modeStreamBlock:
		helperStreamBlock()
	case modeStreamActive:
		helperStreamActive()
	case modeTermStubborn:
		helperTermStubborn()
	case modeChildStubborn:
		helperChildStubborn()
	case modeTermCooperate:
		helperTermCooperative()
	case modeIdle:
		helperIdle()
	case modeBurstExit:
		helperBurstExit()
	case modeFloodIdle:
		helperFloodIdle()
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(99)
	}
	os.Exit(0) // helpers that fall through end successfully
}

// park keeps a helper process alive until the shim ends it (by signal, or by
// closing the PTY). A bare select{} trips the Go runtime's deadlock detector
// once the shim drains the helper's final output — with every goroutine asleep
// the runtime aborts with "all goroutines are asleep - deadlock!" (exit 2),
// killing the agent before any signal arrives. A blocking stdin read keeps a
// live M and also models a real agent CLI, which blocks reading its stdin.
func park() {
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func helperInfo() {
	for _, a := range os.Args {
		fmt.Printf("ARGV\t%s\n", a)
	}
	wd, _ := os.Getwd()
	fmt.Printf("CWD\t%s\n", wd)
	fmt.Printf("PID\t%d\n", os.Getpid())
	fmt.Printf("PGID\t%d\n", syscall.Getpgrp())
	for _, kv := range os.Environ() {
		fmt.Printf("ENV\t%s\n", kv)
	}
	fmt.Printf("INFO_DONE\n")
}

// helperDSR proves the shim pipes emulator query replies back into the PTY
// master: it puts the tty in raw mode (a CPR reply carries no newline, so a
// canonical read would block), asks for the cursor position, and waits for the
// report on stdin.
func helperDSR() {
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		fmt.Printf("DSR_RAWFAIL\n")
		os.Exit(0)
	}
	got := make(chan bool, 1)
	go func() {
		buf := make([]byte, 0, 64)
		tmp := make([]byte, 32)
		for {
			n, err := os.Stdin.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				// CPR is ESC [ rows ; cols R — terminator 'R' is enough.
				for _, b := range tmp[:n] {
					if b == 'R' {
						got <- true
						return
					}
				}
			}
			if err != nil {
				got <- false
				return
			}
		}
	}()
	fmt.Printf("\x1b[6n") // DSR: report cursor position
	select {
	case ok := <-got:
		if ok {
			fmt.Printf("\nDSR_OK\n")
		} else {
			fmt.Printf("\nDSR_TIMEOUT\n")
		}
	case <-time.After(2 * time.Second):
		fmt.Printf("\nDSR_TIMEOUT\n")
	}
}

// helperWinsize reports the PTY window size the kernel gives the slave: once at
// start (proving the shim set the initial size from cfg.Cols/Rows) and again on
// every SIGWINCH (proving a resize op reached pty.Setsize on the master).
func helperWinsize() {
	printSize := func() {
		w, h, err := term.GetSize(os.Stdin.Fd())
		if err != nil {
			fmt.Printf("WINSIZE_ERR\t%v\n", err)
			return
		}
		fmt.Printf("WINSIZE\t%dx%d\n", h, w) // rows x cols
	}
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, syscall.SIGWINCH)
	printSize()
	fmt.Printf("WINSIZE_READY\n")
	for range ch {
		printSize()
	}
}

func helperStreamBlock() {
	for i := 0; i < phase1Lines; i++ {
		fmt.Printf("P1L%04d\n", i)
	}
	fmt.Printf("PHASE1_DONE\n")
	// Block until the trigger byte(s) arrive from stdin (canonical read is fine;
	// the client sends a newline). The trigger's echo is harmless noise: the
	// continuity assertions key off PHASE1_/PHASE2_ markers, which the echo
	// never matches.
	buf := make([]byte, 16)
	_, _ = os.Stdin.Read(buf)
	for i := 0; i < phase2Lines; i++ {
		fmt.Printf("P2L%04d\n", i)
	}
	fmt.Printf("PHASE2_DONE\n")
}

func helperStreamActive() {
	// Emit a dense, contiguous integer sequence forever (until killed), paced so a
	// tight-loop consumer always keeps up and the shim's bounded queue never
	// drops — the precondition for the strict-contiguity check in the active-load
	// S10 test. Running until killed (rather than a fixed count) guarantees the
	// stream is still live throughout the client's observation window.
	for i := 0; ; i++ {
		fmt.Printf("N%d\n", i)
		time.Sleep(150 * time.Microsecond)
	}
}

func helperTermStubborn() {
	signal.Ignore(syscall.SIGTERM)
	exe, _ := os.Executable()
	cmd := exec.Command(exe)
	cmd.Env = replaceEnv(os.Environ(), helperEnvVar, modeChildStubborn)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// No SysProcAttr: the child inherits this process's group so a group-kill
	// reaches it too (S5 containment).
	if err := cmd.Start(); err != nil {
		fmt.Printf("CHILD_START_ERR\t%v\n", err)
	}
	fmt.Printf("PARENT_PID\t%d\n", os.Getpid())
	park() // ignore TERM; only a group KILL ends this
}

func helperChildStubborn() {
	signal.Ignore(syscall.SIGTERM)
	fmt.Printf("CHILD_PID\t%d\n", os.Getpid())
	park()
}

func helperTermCooperative() {
	// Default SIGTERM disposition: the Go runtime terminates the process on
	// SIGTERM when it is not caught, so this agent dies from TERM within grace.
	fmt.Printf("PARENT_PID\t%d\n", os.Getpid())
	park()
}

func helperIdle() {
	fmt.Printf("IDLING\n")
	park()
}

func helperBurstExit() {
	for i := 0; i < 500; i++ {
		fmt.Printf("B%04d\n", i)
	}
	fmt.Printf("BURST_DONE\n")
}

func helperFloodIdle() {
	// Flood continuously until the shim kills us. A wedged consumer cannot keep
	// up, so the shim's bounded per-conn queue (subQueueCap frames) overflows
	// once ~subQueueCap read-chunks accrue behind the blocked socket writer, and
	// frames are dropped (S9); meanwhile the emulator grid keeps advancing
	// (authoritative, S1 shim half). No completion marker: forcing enough total
	// bytes through the emulator to "finish" a flood is far slower than reaching
	// the drop condition, especially under -race, so the test polls the drop
	// counter and the grid rather than waiting for the flood to end. The tight
	// loop is not a busy spin: it blocks on PTY writes once the kernel buffer
	// fills, throttled to the drain's speed.
	for i := 0; ; i++ {
		fmt.Printf("F%08d\n", i)
	}
}

// replaceEnv returns env with key's assignment set to val (replacing any
// existing occurrence), so a re-exec'd child runs a different helper mode than
// its parent.
func replaceEnv(env []string, key, val string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			out = append(out, key+"="+val)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, key+"="+val)
	}
	return out
}

// ---------------------------------------------------------------------------
// TestMain: intercept helper re-exec, else build the fake agent once
// ---------------------------------------------------------------------------

// fakeAgentBin is the path to swarm-fake-agent, built once in TestMain.
var fakeAgentBin string

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnvVar); mode != "" {
		runHelperAgent(mode) // never returns
	}

	dir, err := os.MkdirTemp("", "swarm-shim-fake")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	fakeAgentBin = filepath.Join(dir, "swarm-fake-agent")
	build := exec.Command("go", "build", "-o", fakeAgentBin, "github.com/Nathandela/swarm/cmd/swarm-fake-agent")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build fake agent:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Config + Run harness
// ---------------------------------------------------------------------------

// selfExe is the absolute path to this test binary; the re-exec helper agent
// runs it as argv[0].
func selfExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// helperEnv builds an agent environment that activates helper mode plus any
// extra KEY=VALUE entries. It deliberately does NOT inherit the test process's
// environment, so env differential assertions are exact.
func helperEnv(mode string, extra ...string) []string {
	return append([]string{helperEnvVar + "=" + mode}, extra...)
}

// newSocketPath returns a short per-session UDS path (UNIX socket paths are
// capped near ~104 bytes, so t.TempDir's long names are avoided).
func newSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("mktemp socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "s")
	if len(p) > 100 {
		t.Fatalf("socket path too long (%d bytes): %s", len(p), p)
	}
	return p
}

// helperConfig builds a Config that runs the re-exec helper in the given mode.
func helperConfig(t *testing.T, mode string, argvExtra []string, envExtra []string) Config {
	t.Helper()
	sessionDir := t.TempDir()
	argv := append([]string{selfExe(t)}, argvExtra...)
	return Config{
		SessionID:     "test-session",
		Argv:          argv,
		Cwd:           t.TempDir(),
		Env:           helperEnv(mode, envExtra...),
		SocketPath:    newSocketPath(t),
		SessionDir:    sessionDir,
		Cols:          80,
		Rows:          24,
		TranscriptCfg: transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3},
		GraceTimeout:  5 * time.Second,
		Metrics:       &Metrics{},
	}
}

// fakeAgentConfig builds a Config that runs swarm-fake-agent against a script.
func fakeAgentConfig(t *testing.T, script string) Config {
	t.Helper()
	sessionDir := t.TempDir()
	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return Config{
		SessionID:     "test-session",
		Argv:          []string{fakeAgentBin, scriptPath},
		Cwd:           t.TempDir(),
		Env:           []string{"PATH=" + os.Getenv("PATH")},
		SocketPath:    newSocketPath(t),
		SessionDir:    sessionDir,
		Cols:          80,
		Rows:          24,
		TranscriptCfg: transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3},
		GraceTimeout:  5 * time.Second,
		Metrics:       &Metrics{},
	}
}

type runResult struct {
	exit int
	err  error
}

// runShimAsync starts Run in a goroutine and returns a channel that yields its
// result once the agent exits.
func runShimAsync(cfg Config) <-chan runResult {
	ch := make(chan runResult, 1)
	go func() {
		exit, err := Run(cfg)
		ch <- runResult{exit, err}
	}()
	return ch
}

// waitRun waits for Run to return, failing the test on timeout (no hangs).
func waitRun(t *testing.T, ch <-chan runResult, timeout time.Duration) runResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(timeout):
		t.Fatalf("shim.Run did not return within %s", timeout)
		return runResult{}
	}
}

// ---------------------------------------------------------------------------
// Side-file readers
// ---------------------------------------------------------------------------

func readTranscript(t *testing.T, sessionDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sessionDir, TranscriptFile))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return string(b)
}

func decodeFinalSnapshot(t *testing.T, sessionDir string) *vt.Snap {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sessionDir, SnapshotFile))
	if err != nil {
		t.Fatalf("read final snapshot: %v", err)
	}
	s, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode final snapshot: %v", err)
	}
	return s
}

func readExitInfo(t *testing.T, sessionDir string) ExitInfo {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sessionDir, ExitFile))
	if err != nil {
		t.Fatalf("read exit.json: %v", err)
	}
	var ei ExitInfo
	if err := json.Unmarshal(b, &ei); err != nil {
		t.Fatalf("decode exit.json: %v (%s)", err, b)
	}
	return ei
}

// gridText renders a decoded snapshot to plain text (each row's run text,
// trailing spaces trimmed, rows joined by newlines) — the same oracle the vt
// suite uses, so grid assertions read naturally.
func gridText(s *vt.Snap) string {
	var b strings.Builder
	for _, l := range s.Lines {
		var row strings.Builder
		for _, r := range l.Runs {
			row.WriteString(r.Text)
		}
		b.WriteString(strings.TrimRight(row.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Shim protocol client (drives the per-session UDS with wire + shimwire)
// ---------------------------------------------------------------------------

type frameRec struct {
	typ     wire.Type
	payload []byte
}

// shimClient speaks the G2 protocol to a shim over its UDS. The reader
// goroutine is started explicitly (startReader): a "wedged" client dials and
// writes but never reads, to exercise the bounded-queue drop path (S9).
type shimClient struct {
	t    *testing.T
	conn net.Conn

	mu      sync.Mutex
	log     []frameRec // every frame received, in order
	readErr error
}

// dialShim connects to the shim's socket, retrying until it appears (the shim
// binds asynchronously after Run starts).
func dialShim(t *testing.T, socketPath string) *shimClient {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			c := &shimClient{t: t, conn: conn}
			t.Cleanup(func() { conn.Close() })
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial shim socket %s: %v", socketPath, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *shimClient) startReader() {
	go func() {
		for {
			typ, payload, err := wire.ReadFrame(c.conn)
			if err != nil {
				c.mu.Lock()
				c.readErr = err
				c.mu.Unlock()
				return
			}
			c.mu.Lock()
			c.log = append(c.log, frameRec{typ, payload})
			c.mu.Unlock()
		}
	}()
}

// writeControl sends a shimwire.Control inside a TControl frame.
func (c *shimClient) writeControl(ctrl shimwire.Control) {
	c.t.Helper()
	b, err := shimwire.Encode(ctrl)
	if err != nil {
		c.t.Fatalf("encode control: %v", err)
	}
	if err := wire.WriteFrame(c.conn, wire.TControl, b); err != nil {
		c.t.Fatalf("write control frame: %v", err)
	}
}

// writeDataIn sends raw bytes as a TDataIn frame (PTY input).
func (c *shimClient) writeDataIn(b []byte) {
	c.t.Helper()
	if err := wire.WriteFrame(c.conn, wire.TDataIn, b); err != nil {
		c.t.Fatalf("write data-in frame: %v", err)
	}
}

// hello performs the version handshake and returns the shim's hello reply.
func (c *shimClient) hello(wireVersion int) shimwire.Control {
	c.t.Helper()
	c.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: wireVersion})
	return c.waitControl(shimwire.TypeHello, 3*time.Second)
}

func (c *shimClient) attach() {
	c.writeControl(shimwire.Control{Type: shimwire.TypeAttach})
}

func (c *shimClient) frames() []frameRec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]frameRec(nil), c.log...)
}

// dataOut concatenates every TDataOut payload received so far, in order.
func (c *shimClient) dataOut() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []byte
	for _, f := range c.log {
		if f.typ == wire.TDataOut {
			out = append(out, f.payload...)
		}
	}
	return out
}

// waitOutput blocks until the accumulated TDataOut contains sub, returning the
// full accumulated bytes, or fails on timeout.
func (c *shimClient) waitOutput(sub string, timeout time.Duration) []byte {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := c.dataOut()
		if strings.Contains(string(out), sub) {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("output %q not seen within %s; got:\n%s", sub, timeout, c.dataOut())
	return nil
}

// waitControl blocks until a TControl frame decoding to the given type arrives,
// returning it, or fails on timeout.
func (c *shimClient) waitControl(ctrlType string, timeout time.Duration) shimwire.Control {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, f := range c.frames() {
			if f.typ != wire.TControl {
				continue
			}
			ctrl, err := shimwire.Decode(f.payload)
			if err == nil && ctrl.Type == ctrlType {
				return ctrl
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("control frame %q not seen within %s", ctrlType, timeout)
	return shimwire.Control{}
}

// firstSnapshot returns the first TSnapshot payload, waiting up to timeout.
func (c *shimClient) firstSnapshot(timeout time.Duration) []byte {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, f := range c.frames() {
			if f.typ == wire.TSnapshot {
				return f.payload
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("no snapshot frame within %s", timeout)
	return nil
}
