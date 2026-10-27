package shim

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/wire"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

// These tests are private/unexecuted preparation, not a historical semantic RED.
const claudeChatTestUUID = "8e4c6620-bfdd-43c3-93de-e69a2f97a01f"

func TestClaudeChatStartBufferedSignalCancelsBeforeStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-an-executable")
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		signals := make(chan os.Signal, 1)
		signals <- sig
		cmd := exec.Command(missing)
		if err := startClaudeChatPrimary(cmd, signals); !errors.Is(err, context.Canceled) || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("buffered signal missed final cancellation gate: %v", err)
		}
		if cmd.Process != nil || cmd.ProcessState != nil || len(signals) != 0 {
			t.Fatal("canceled gate attempted Start/Wait or failed to consume signal")
		}
	}
	// Same command reaches its actual Start error without a buffered signal;
	// context.Canceled above is not inferred from missing PID telemetry.
	cmd := exec.Command(missing)
	if err := startClaudeChatPrimary(cmd, nil); !errors.Is(err, os.ErrNotExist) || errors.Is(err, context.Canceled) {
		t.Fatalf("uncanceled Start canary: %v", err)
	}
	if cmd.Process != nil || cmd.ProcessState != nil {
		t.Fatal("failed OS Start acquired a Wait")
	}
}

func TestClaudeChatStartPreservesPendingControlReplay(t *testing.T) {
	// Control Stop may already be pending while the OS signal gate is clear.
	// The existing setter must replay it onto only the newly owned group.
	srv := &server{}
	srv.onSignal(shimwire.SigKill)
	cmd := exec.Command("/bin/sh", "-c", "exec sleep 12")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := startClaudeChatPrimary(cmd, nil); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	if cmd.Process == nil || cmd.ProcessState != nil {
		t.Fatal("successful Start lost caller Wait ownership")
	}
	srv.setAgentPgid(cmd.Process.Pid)
	err := cmd.Wait()
	waited = true
	code, sig := interpretExit(err)
	if sig != "SIGKILL" || code != 128+int(syscall.SIGKILL) {
		t.Fatalf("pending control was not replayed: code=%d signal=%s", code, sig)
	}
}

func TestClaudeChatRunBufferedSignalClosesPreStartResources(t *testing.T) {
	cfg := claudeChatFixtureConfig(t)
	cfg.ClaudeChatFixture = filepath.Join(t.TempDir(), "missing-start-canary")
	cfg.Argv[0] = cfg.ClaudeChatFixture
	// Linux supplements the portable Start-object/return-path proof with exact
	// pipe inode accounting, including all six ends observed at the final gate.
	pipeIDs := func() map[[2]uint64]int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		out := make(map[[2]uint64]int)
		for _, entry := range entries {
			info, err := os.Stat(filepath.Join("/proc/self/fd", entry.Name()))
			if os.IsNotExist(err) {
				continue
			} // ReadDir's own already-closed descriptor
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeNamedPipe == 0 {
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("unknown pipe identity")
			}
			out[[2]uint64{uint64(stat.Dev), uint64(stat.Ino)}]++
		}
		return out
	}
	var before, opened map[[2]uint64]int
	if runtime.GOOS == "linux" {
		before = pipeIDs()
	}
	old := testHookAfterSignalArm
	t.Cleanup(func() { testHookAfterSignalArm = old })
	gateReached := false
	testHookAfterSignalArm = func() {
		gateReached = true
		if runtime.GOOS == "linux" {
			opened = pipeIDs()
			for id := range before {
				delete(opened, id)
			}
			if len(opened) != 3 {
				t.Fatalf("final gate did not own three pipe pairs: %d", len(opened))
			}
			for _, ends := range opened {
				if ends != 2 {
					t.Fatal("pipe pair missing an owned end")
				}
			}
		}
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM // already buffered; no relay can consume it first
	_, err := runClaudeChatPrimary(cfg, signals)
	if !gateReached || !errors.Is(err, context.Canceled) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong pre-Start return path: gate=%v err=%v", gateReached, err)
	}
	if runtime.GOOS == "linux" {
		after := pipeIDs()
		for id := range opened {
			if after[id] != 0 {
				t.Fatal("canceled constructor retained an owned pipe")
			}
		}
	}
	if conn, err := net.DialTimeout("unix", cfg.SocketPath, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("canceled constructor left control listener")
	}
	for _, name := range []string{claudeChatCaptureFile, ExitFile, BackendFile, SnapshotFile} {
		if _, err := os.Stat(filepath.Join(cfg.SessionDir, name)); !os.IsNotExist(err) {
			t.Fatalf("canceled constructor published started evidence: %s", name)
		}
	}
	spool, err := OpenHookSpoolProducer(filepath.Join(cfg.SessionDir, HookSpoolFile), 8<<20)
	if err != nil {
		t.Fatal("canceled constructor retained spool ownership", err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
}

func claudeChatFixtureConfig(t *testing.T) Config {
	t.Helper()
	cfg := fakeAgentConfig(t, "exit 0\n")
	cfg.RequiredRuntime = persist.RequiredRuntimeClaudeChat
	cfg.ConversationID = claudeChatTestUUID
	cfg.ClaudeChatFixture = fakeAgentBin
	cfg.Argv = []string{fakeAgentBin, "--claude-primary-fixture", "--session-id=" + cfg.ConversationID}
	cfg.SessionID = "chat-fixture"
	cfg.GraceTimeout = time.Second
	cfg.LoginStateRoot = t.TempDir()
	cfg.SessionDir = filepath.Join(cfg.LoginStateRoot, cfg.SessionID)
	store, err := persist.NewStore(cfg.LoginStateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(persist.Meta{SchemaVersion: 6, ID: cfg.SessionID, AgentType: "fake", Cwd: cfg.Cwd, RequiredRuntime: cfg.RequiredRuntime, ConversationID: cfg.ConversationID, Status: status.Status{Process: status.ProcessRunning}}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestClaudeChatFixtureConfigRefusesManagedAndMixedModes(t *testing.T) {
	good := claudeChatFixtureConfig(t)
	if err := ValidateClaudeChatConfig(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"missing-mode":    func(c *Config) { c.RequiredRuntime = "" },
		"missing-fixture": func(c *Config) { c.ClaudeChatFixture = "" },
		"wrong-uuid":      func(c *Config) { c.ConversationID = "not-a-uuid" },
		"native":          func(c *Config) { c.NativeProvider = "claude" },
		"image":           func(c *Config) { c.CLIIdentity = &persist.CLIIdentity{} },
		"backend":         func(c *Config) { c.Backend = &BackendConfig{} },
		"embargo":         func(c *Config) { c.InputEmbargo = "held" },
		"auto-message":    func(c *Config) { c.Argv = append(append([]string{}, c.Argv...), "hello") },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := good
			mutate(&cfg)
			if ValidateClaudeChatConfig(cfg) == nil {
				t.Fatal("mixed fixture admitted")
			}
		})
	}
	if err := ValidateClaudeChatConfig(Config{}); err != nil {
		t.Fatal("ordinary config changed", err)
	}
}

func TestClaudeChatFixtureReservationBindsModeAndBirth(t *testing.T) {
	for _, field := range []string{"conversation_id", "required_runtime", "agent_type", "schema_version", "cwd", "shim_pid", "shim_start_time"} {
		t.Run(field, func(t *testing.T) {
			cfg := claudeChatFixtureConfig(t)
			start, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if err := validateClaudeChatReservation(cfg, start); err != nil {
				t.Fatal("valid zero-publication fixture refused", err)
			}
			path := filepath.Join(cfg.SessionDir, "meta.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if json.Unmarshal(raw, &fields) != nil {
				t.Fatal("bad test metadata")
			}
			switch field {
			case "conversation_id":
				fields[field] = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			case "required_runtime":
				fields[field] = ""
			case "agent_type":
				fields[field] = "claude"
			case "schema_version":
				fields[field] = 5
			case "cwd":
				fields[field] = t.TempDir()
			case "shim_pid":
				fields[field] = os.Getpid() + 1
				fields["shim_start_time"] = start
			case "shim_start_time":
				fields["shim_pid"] = os.Getpid()
				fields[field] = start + 1
			}
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if validateClaudeChatReservation(cfg, start) == nil {
				t.Fatal("mismatched stored reservation admitted")
			}
		})
	}
	cfg := claudeChatFixtureConfig(t)
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(cfg.LoginStateRoot)
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.Load(cfg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	m.ShimPID, m.ShimStartTime = os.Getpid(), start
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	if err := validateClaudeChatReservation(cfg, start); err != nil {
		t.Fatal("published matching birth refused", err)
	}
}

// Open first, then hold the actual Append fsync. Cleanup releases and joins
// before restoring the global seam, even after a Fatal in the caller.
func claudeChatBlockedCapture(t *testing.T) (*claudeChatCapture, <-chan struct{}, func()) {
	t.Helper()
	spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	old := hookSpoolSync
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce, finishOnce sync.Once
	hookSpoolSync = func(f *os.File) error { enteredOnce.Do(func() { close(entered) }); <-release; return old(f) }
	c := newClaudeChatCapture(spool, &server{}, claudeChatPrimaryIdentity{})
	finish := func() {
		finishOnce.Do(func() {
			releaseOnce.Do(func() { close(release) })
			close(c.queue)
			<-c.done
			hookSpoolSync = old
			_ = spool.Close()
		})
	}
	t.Cleanup(finish)
	return c, entered, finish
}

func TestClaudeChatCaptureQueueIncludesProcessingFrameAndEncodedBytes(t *testing.T) {
	t.Run("wrapper-boundary", func(t *testing.T) {
		identity := claudeChatPrimaryIdentity{SessionID: "fixture", RequiredRuntime: "claude-chat", NativeSessionID: claudeChatTestUUID, ShimPID: 1, ShimStartTime: 2, PrimaryPID: 3, PrimaryStartTime: 4}
		empty, err := encodeClaudeChatEnvelope(identity, []byte{}, 1, 42)
		if err != nil {
			t.Fatal(err)
		}
		n := 3 * ((wire.MaxFrame - 1 - len(empty)) / 4)
		body, err := encodeClaudeChatEnvelope(identity, bytes.Repeat([]byte("x"), n), 1, 42)
		if err != nil || len(body) > wire.MaxFrame-1 || wire.MaxFrame-1-len(body) >= 4 {
			t.Fatal("largest fitting wrapper refused or unbounded", err)
		}
		if _, err := encodeClaudeChatEnvelope(identity, bytes.Repeat([]byte("x"), n+1), 1, 42); err == nil {
			t.Fatal("one extra raw byte's encoded wrapper exceeded wire cap")
		}
	})
	t.Run("frames", func(t *testing.T) {
		c, entered, finish := claudeChatBlockedCapture(t)
		if !c.admit([]byte("first\n")) {
			t.Fatal("first refused")
		}
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("Append did not enter fsync")
		}
		for i := 1; i < claudeChatFrames; i++ {
			if !c.admit([]byte("x\n")) {
				t.Fatalf("frame %d refused", i)
			}
		}
		if c.admit([]byte("overflow\n")) {
			t.Fatal("processing frame excluded from budget")
		}
		c.mu.Lock()
		frames, lost := c.frames, c.lost
		c.mu.Unlock()
		if frames != 64 || !lost {
			t.Fatalf("frames=%d loss=%v", frames, lost)
		}
		finish()
	})
	t.Run("encoded", func(t *testing.T) {
		c, entered, finish := claudeChatBlockedCapture(t)
		raw := bytes.Repeat([]byte("x"), 400<<10)
		if !c.admit(raw) {
			t.Fatal("first refused")
		}
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("Append did not enter fsync")
		}
		if c.admit(raw) {
			t.Fatal("counted raw bytes instead of encoded processing bytes")
		}
		c.mu.Lock()
		size, frames := c.bytes, c.frames
		c.mu.Unlock()
		if size <= len(raw) || size > claudeChatBytes || frames != 1 {
			t.Fatalf("encoded=%d frames=%d", size, frames)
		}
		finish()
	})
}

type claudeChatDataError struct{ sent bool }

func (r *claudeChatDataError) Read(p []byte) (int, error) {
	if r.sent {
		return 0, syscall.EIO
	}
	r.sent = true
	return copy(p, "partial"), syscall.EIO
}

func TestClaudeChatCaptureEOFReadFailureAndWrapperLimits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		r         io.Reader
		lost, eof bool
	}{
		{"LF-only-policy", bytes.NewBufferString("line\r\n"), false, true},
		{"EOF-tail", bytes.NewBufferString("tail"), true, true},
		{"read-error", &claudeChatDataError{}, true, false},
		{"raw-over", bytes.NewReader(bytes.Repeat([]byte("x"), claudeChatBytes+2)), true, true},
		{"wrapper-over", bytes.NewReader(bytes.Repeat([]byte("x"), claudeChatBytes)), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = spool.Close() }()
			c := newClaudeChatCapture(spool, &server{}, claudeChatPrimaryIdentity{})
			c.readStdout(tc.r)
			close(c.queue)
			<-c.done
			c.mu.Lock()
			lost, eof := c.lost, c.eof[0]
			c.mu.Unlock()
			if lost != tc.lost || eof != tc.eof {
				t.Fatalf("loss=%v eof=%v", lost, eof)
			}
			recs, _, _, err := spool.ReadFrom(0)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "EOF-tail" || tc.name == "read-error" || tc.name == "LF-only-policy" {
				if len(recs) != 1 {
					t.Fatalf("data before EOF/error discarded: %d", len(recs))
				}
				var env claudeChatEnvelope
				if json.Unmarshal(recs[0].Body, &env) != nil || env.Origin != "claude_primary" {
					t.Fatal("lost origin envelope")
				}
				if tc.name == "LF-only-policy" && string(env.Raw) != "line\r" {
					t.Fatal("did not exclude exactly one LF")
				}
			}
		})
	}
}

type claudeChatErrorWriter struct{}

func (claudeChatErrorWriter) Write([]byte) (int, error) { return 0, syscall.ENOSPC }

func TestClaudeChatStderrIsBoundedDiagnosticsNotPrimary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input io.Reader
		sink  io.Writer
		lost  bool
	}{
		{"ordinary", bytes.NewBufferString("diagnostic\n"), io.Discard, false},
		{"overflow", bytes.NewReader(bytes.Repeat([]byte("x"), claudeChatBytes+1)), io.Discard, true},
		{"read-error", &claudeChatDataError{}, io.Discard, true},
		{"write-error", bytes.NewBufferString("diagnostic\n"), claudeChatErrorWriter{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = spool.Close() }()
			c := newClaudeChatCapture(spool, &server{}, claudeChatPrimaryIdentity{})
			c.readStderr(tc.input, tc.sink)
			close(c.queue)
			<-c.done
			if c.lost != tc.lost {
				t.Fatalf("loss=%v", c.lost)
			}
			recs, _, _, err := spool.ReadFrom(0)
			if err != nil || len(recs) != 0 {
				t.Fatal("stderr became primary provenance")
			}
			if tc.name != "read-error" && !c.eof[1] {
				t.Fatal("real diagnostic EOF missing")
			}
		})
	}
}

func TestClaudeChatCaptureAppendFailureIsSticky(t *testing.T) {
	spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	old := hookSpoolSync
	hookSpoolSync = func(*os.File) error { return syscall.ENOSPC }
	defer func() { hookSpoolSync = old }()
	c := newClaudeChatCapture(spool, &server{}, claudeChatPrimaryIdentity{})
	c.admit([]byte("frame\n"))
	close(c.queue)
	<-c.done
	c.eof = [2]bool{true, true}
	if _, err := c.result(); !errors.Is(err, errClaudeChatCapture) {
		t.Fatal("storage failure became success", err)
	}
}

func TestClaudeChatCaptureRetirementJoinsEnteredSignal(t *testing.T) {
	srv := &server{}
	c := &claudeChatCapture{server: srv}
	// This blocks actual onSignal inside killGroups, after fail has entered its
	// signal lifetime lock. No real PID is configured or signalled in this unit.
	srv.pgidMu.Lock()
	locked := true
	failed, retired := make(chan struct{}), make(chan struct{})
	retireStarted := false
	go func() { defer close(failed); c.fail() }()
	t.Cleanup(func() {
		if locked {
			srv.pgidMu.Unlock()
		}
		<-failed
		if retireStarted {
			<-retired
		}
	})
	deadline := time.Now().Add(time.Second)
	for c.signalMu.TryLock() {
		c.signalMu.Unlock()
		if !time.Now().Before(deadline) {
			t.Fatal("fail did not enter signal authority")
		}
		time.Sleep(time.Millisecond)
	}
	retireStarted = true
	go func() { defer close(retired); c.retireSignals() }()
	select {
	case <-retired:
		t.Fatal("retirement passed an entered onSignal")
	case <-time.After(20 * time.Millisecond):
	}
	srv.pgidMu.Unlock()
	locked = false
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("retirement did not join signal")
	}
	<-failed
	if !c.lost || !c.signalsRetired {
		t.Fatal("retirement erased loss or failed to retire")
	}
}

func TestClaudeChatRetirementJoinsControlAndBlocksDelayedAppendSignal(t *testing.T) {
	spool, err := OpenHookSpool(filepath.Join(t.TempDir(), HookSpoolFile), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	old := hookSpoolSync
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce, queueOnce, stopOnce, unlockOnce, finalOnce sync.Once
	hookSpoolSync = func(*os.File) error { enterOnce.Do(func() { close(entered) }); <-release; return syscall.ENOSPC }
	listener, err := listen(newSocketPath(t))
	if err != nil {
		hookSpoolSync = old
		_ = spool.Close()
		t.Fatal(err)
	}
	srv := newServer(listener, listener.Addr().String(), nil, nil, nil, 0, time.Hour, &Metrics{})
	srv.claudeChat = true
	accepted := make(chan struct{})
	go func() { defer close(accepted); srv.acceptLoop() }()
	stopControl := func() { stopOnce.Do(func() { srv.shutdown(shimwire.Control{}); <-accepted }) }
	finalContainment := func() { finalOnce.Do(func() { srv.finishEscalation() }) }
	capture := newClaudeChatCapture(spool, srv, claudeChatPrimaryIdentity{})
	srv.escMu.Lock()
	unlock := func() { unlockOnce.Do(func() { srv.escMu.Unlock() }) }
	t.Cleanup(func() {
		unlock()
		stopControl()
		capture.retireSignals()
		finalContainment()
		releaseOnce.Do(func() { close(release) })
		queueOnce.Do(func() { close(capture.queue) })
		<-capture.done
		hookSpoolSync = old
		_ = spool.Close()
	})
	if !capture.admit([]byte("frame")) {
		t.Fatal("frame refused")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Append did not block")
	}
	client := dialShim(t, listener.Addr().String())
	client.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	_ = client.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := wire.ReadFrame(client.conn); err != nil {
		t.Fatal(err)
	}
	client.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigTerm})
	deadline := time.Now().Add(time.Second)
	for {
		srv.pgidMu.Lock()
		signal := srv.pendingSig
		srv.pgidMu.Unlock()
		if signal == syscall.SIGTERM {
			break
		} // actual handler entered; escMu holds it
		if !time.Now().Before(deadline) {
			t.Fatal("control signal did not enter")
		}
		time.Sleep(time.Millisecond)
	}
	controlEOF := make(chan struct{})
	go func() { defer close(controlEOF); _, _, _ = wire.ReadFrame(client.conn) }()
	t.Cleanup(func() { unlock(); stopControl(); <-controlEOF })
	stopped := make(chan struct{})
	go func() { defer close(stopped); stopControl() }()
	t.Cleanup(func() { unlock(); stopControl(); <-stopped })
	select {
	case <-controlEOF:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not close control transport")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown skipped entered control signal")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("control handler did not join")
	}
	capture.retireSignals()
	finalContainment() // joins the TERM worker, then the final synchronous KILL
	if capture.lost {
		t.Fatal("Append failed before the retirement boundary")
	}
	// pendingSig is an actual onSignal/killGroups attempt witness. Reset only
	// AFTER every producer has joined/retired; no real PGID is used or adopted.
	srv.pgidMu.Lock()
	srv.pendingSig = 0
	srv.pgidMu.Unlock()
	lateControl := make(chan struct{})
	go func() {
		defer close(lateControl)
		_ = client.conn.SetWriteDeadline(time.Now().Add(time.Second))
		raw, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
		_ = wire.WriteFrame(client.conn, wire.TControl, raw)
	}()
	releaseOnce.Do(func() { close(release) }) // first loss occurs AFTER final containment
	queueOnce.Do(func() { close(capture.queue) })
	<-capture.done
	<-lateControl
	srv.pgidMu.Lock()
	lateSignal := srv.pendingSig
	srv.pgidMu.Unlock()
	if lateSignal != 0 {
		t.Fatal("retired Append/control actor attempted a later signal")
	}
	if !capture.lost {
		t.Fatal("retirement swallowed Append failure")
	}
	if _, err := capture.result(); !errors.Is(err, errClaudeChatCapture) {
		t.Fatal("loss became success")
	}
}

func TestClaudeChatRunStartFailureHasNoCaptureOrTerminal(t *testing.T) {
	cfg := claudeChatFixtureConfig(t)
	cfg.ClaudeChatFixture = filepath.Join(t.TempDir(), "absent")
	cfg.Argv[0] = cfg.ClaudeChatFixture
	if _, err := Run(cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost Start failure: %v", err)
	}
	for _, name := range []string{claudeChatCaptureFile, ExitFile, SnapshotFile, BackendFile} {
		if _, err := os.Stat(filepath.Join(cfg.SessionDir, name)); !os.IsNotExist(err) {
			t.Fatalf("failed Start published %s", name)
		}
	}
}

func TestClaudeChatControlServerRefusesEveryRawPlane(t *testing.T) {
	listener, err := listen(newSocketPath(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(listener, listener.Addr().String(), nil, nil, nil, 0, time.Second, &Metrics{})
	srv.claudeChat = true // fixed before accept goroutine starts
	done := make(chan struct{})
	go func() { defer close(done); srv.acceptLoop() }()
	t.Cleanup(func() { srv.shutdown(shimwire.Control{}); <-done })
	for _, typ := range []string{shimwire.TypeAttach, shimwire.TypeSnapshotReq, shimwire.TypeResize, shimwire.TypeSubmit, shimwire.TypeControlInput, shimwire.TypePasteInput, shimwire.TypeBackendAttach, shimwire.TypeOpenCodeDispatch, shimwire.TypeInputEmbargoRelease, shimwire.TypeOpenCodeEmbargoRelease, "data"} {
		t.Run(typ, func(t *testing.T) {
			c := dialShim(t, listener.Addr().String())
			c.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
			_ = c.conn.SetReadDeadline(time.Now().Add(time.Second))
			_, body, err := wire.ReadFrame(c.conn)
			if err != nil {
				t.Fatal(err)
			}
			hello, err := shimwire.Decode(body)
			if err != nil || hello.SnapshotOnly || hello.ControlInput || hello.PasteInput || hello.SubmitTransaction {
				t.Fatal("raw capabilities advertised")
			}
			if typ == "data" {
				c.writeDataIn([]byte("forbidden"))
			} else {
				c.writeControl(shimwire.Control{Type: typ, Text: "forbidden", Cols: 80, Rows: 24})
			}
			if _, _, err := wire.ReadFrame(c.conn); !errors.Is(err, io.EOF) {
				t.Fatalf("raw plane did not refuse: %v", err)
			}
		})
	}
}

func TestClaudeChatRunStopDoesNotWaitForCaptureSync(t *testing.T) {
	for name, failAfterRetirement := range map[string]bool{"success": false, "Append-fails-after-retirement": true} {
		t.Run(name, func(t *testing.T) {
			cfg := claudeChatFixtureConfig(t)
			old := hookSpoolSync
			entered, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			hookSpoolSync = func(f *os.File) error {
				once.Do(func() { close(entered) })
				<-release
				if failAfterRetirement {
					return syscall.ENOSPC
				}
				return old(f)
			}
			run := runShimAsync(cfg)
			var c *shimClient
			joined := false
			t.Cleanup(func() {
				if !joined {
					conn, err := net.DialTimeout("unix", cfg.SocketPath, time.Second)
					if err == nil {
						_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
						for _, ctrl := range []shimwire.Control{{Type: shimwire.TypeHello, WireVersion: shimwire.Version}, {Type: shimwire.TypeSignal, Sig: shimwire.SigKill}} {
							b, _ := shimwire.Encode(ctrl)
							_ = wire.WriteFrame(conn, wire.TControl, b)
						}
						_ = conn.Close()
					}
				}
				releaseOnce.Do(func() { close(release) })
				if !joined {
					select {
					case <-run:
						joined = true
					case <-time.After(13 * time.Second):
						t.Error("Run cleanup exceeded finite fixture lifetime")
					}
				}
				if joined {
					hookSpoolSync = old
				} // never restore while its writer could still run
				if c != nil {
					_ = c.conn.Close()
				}
			})
			c = dialShim(t, cfg.SocketPath)
			c.startReader()
			c.hello(shimwire.Version)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("capture did not enter fsync")
			}
			// The pending record is only visibility evidence, never durability credit.
			spool, err := os.ReadFile(filepath.Join(cfg.SessionDir, HookSpoolFile))
			if err != nil {
				t.Fatal(err)
			}
			if len(spool) <= hookRecordHeaderLen {
				t.Fatal("no pending frame")
			}
			var envelope claudeChatEnvelope
			size := int(binary.BigEndian.Uint32(spool[8:12]))
			if len(spool) != hookRecordHeaderLen+size || json.Unmarshal(spool[hookRecordHeaderLen:], &envelope) != nil {
				t.Fatal("bad pending envelope")
			}
			var init struct {
				PID  int `json:"fixture_pid"`
				PGID int `json:"fixture_pgid"`
			}
			if json.Unmarshal(envelope.Raw, &init) != nil || init.PID <= 0 || init.PID != init.PGID || envelope.Version != 1 || envelope.Origin != "claude_primary" || envelope.NativeSessionID != cfg.ConversationID || envelope.Primary.SessionID != cfg.SessionID || envelope.Primary.RequiredRuntime != cfg.RequiredRuntime || envelope.Primary.NativeSessionID != cfg.ConversationID || envelope.Primary.ShimPID != os.Getpid() || envelope.Primary.ShimStartTime <= 0 || envelope.Primary.PrimaryPID != init.PID || envelope.Primary.PrimaryStartTime <= 0 || envelope.ReadMonotonicNS <= 0 || envelope.StreamSequence != 1 {
				t.Fatal("missing/mismatched positive fixture identity")
			}
			start, err := procstart.StartTime(init.PID)
			if err != nil || start <= 0 {
				t.Fatal("fixture birth not positively observed")
			}
			pgid, err := syscall.Getpgid(init.PID)
			if err != nil || pgid != init.PGID {
				t.Fatal("fixture not positively alive in owned group")
			}
			c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
			if !processGone(init.PID, 2*time.Second) {
				t.Fatal("Stop waited for blocked capture fsync")
			}
			select {
			case <-run:
				joined = true
				t.Fatal("Run returned before capture joined")
			default:
			}
			// Socket retirement happens while fsync is still held. No explicit signal
			// ACK exists: the existing daemon treats this EOF as delivery/termination.
			deadline := time.Now().Add(5 * time.Second)
			for {
				c.mu.Lock()
				readErr := c.readErr
				c.mu.Unlock()
				if readErr != nil {
					break
				}
				if !time.Now().Before(deadline) {
					t.Fatal("control handlers remained live during capture persistence join")
				}
				time.Sleep(time.Millisecond)
			}
			lateDone := make(chan struct{})
			go func() {
				defer close(lateDone)
				_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
				raw, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
				_ = wire.WriteFrame(c.conn, wire.TControl, raw)
			}()
			<-lateDone
			if late, err := net.DialTimeout("unix", cfg.SocketPath, time.Second); err == nil {
				_ = late.Close()
				t.Fatal("late control could reenter retired server")
			}
			releaseOnce.Do(func() { close(release) })
			result := waitRun(t, run, 5*time.Second)
			joined = true
			if failAfterRetirement {
				if !errors.Is(result.err, errClaudeChatCapture) {
					t.Fatal("delayed Append failure lost", result.err)
				}
			} else if result.err != nil {
				t.Fatal(result.err)
			}
			var receipt struct {
				StdoutEOF bool `json:"stdout_eof"`
				StderrEOF bool `json:"stderr_eof"`
				Loss      bool `json:"loss"`
			}
			b, err := os.ReadFile(filepath.Join(cfg.SessionDir, claudeChatCaptureFile))
			if err != nil || json.Unmarshal(b, &receipt) != nil || !receipt.StdoutEOF || !receipt.StderrEOF || receipt.Loss != failAfterRetirement {
				t.Fatalf("capture EOF/loss mismatch: %+v err=%v", receipt, err)
			}
			for _, name := range []string{SnapshotFile, TranscriptFile, IdentityTailFile, BackendFile} {
				if _, err := os.Stat(filepath.Join(cfg.SessionDir, name)); !os.IsNotExist(err) {
					t.Fatalf("terminal/backend artifact %s exists or unreadable", name)
				}
			}
		})
	}
}

func TestClaudeChatRunContainsInheritedPipesBeforeReaderJoin(t *testing.T) {
	cfg := claudeChatFixtureConfig(t)
	root := t.TempDir()
	pidPath, gatePath := filepath.Join(root, "child-pid"), filepath.Join(root, "release")
	script := filepath.Join(root, "fixture")
	text := "#!/bin/sh\nsleep 12 &\nprintf '%s' \"$!\" > '" + pidPath + "'\ni=0; while [ ! -f '" + gatePath + "' ] && [ $i -lt 1200 ]; do sleep .01; i=$((i+1)); done\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\"}'\nexit 0\n"
	if err := os.WriteFile(script, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.ClaudeChatFixture = script
	cfg.Argv[0] = script
	ch := runShimAsync(cfg)
	joined := false
	t.Cleanup(func() {
		_ = os.WriteFile(gatePath, nil, 0600)
		if !joined {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Error("inherited-pipe Run did not join")
			}
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for {
		var err error
		raw, err = os.ReadFile(pidPath)
		if err == nil && len(raw) != 0 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("child was not published")
		}
		time.Sleep(time.Millisecond)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 {
		t.Fatal("missing inherited-pipe child")
	}
	start, err := procstart.StartTime(pid)
	if err != nil || start <= 0 {
		t.Fatal("child not positively alive before containment")
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 0 || pgid == syscall.Getpgrp() {
		t.Fatal("child not in separate owned group")
	}
	if err := os.WriteFile(gatePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, ch, 5*time.Second)
	joined = true
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !processGone(pid, time.Second) {
		t.Fatal("inherited-pipe child survived Run")
	}
	raw, err = os.ReadFile(filepath.Join(cfg.SessionDir, claudeChatCaptureFile))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		StdoutEOF bool `json:"stdout_eof"`
		StderrEOF bool `json:"stderr_eof"`
		Loss      bool `json:"loss"`
	}
	if json.Unmarshal(raw, &receipt) != nil || !receipt.StdoutEOF || !receipt.StderrEOF || receipt.Loss {
		t.Fatal("reader join substituted local pipe close for real EOF")
	}
}
