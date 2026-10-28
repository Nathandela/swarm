package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/charmbracelet/x/term"
)

const claudeStdioChildArg = "fixture-claude-owned-stdio"

type claudeStdioChildReport struct {
	PID, PGID int
	Argv      []string
	Cwd       string
	Input     string
	TTY       [3]bool
}

// Re-exec the copied test ELF without TestMain's unrelated fake-agent build.
func init() {
	if len(os.Args) != 2 || os.Args[1] != claudeStdioChildArg || os.Getenv("SWARM_TEST_CLAUDE_STDIO_CHILD") != "1" {
		return
	}
	input, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		os.Exit(91)
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(92)
	}
	report := claudeStdioChildReport{
		PID: os.Getpid(), PGID: syscall.Getpgrp(), Argv: os.Args, Cwd: cwd, Input: input,
		TTY: [3]bool{term.IsTerminal(os.Stdin.Fd()), term.IsTerminal(os.Stdout.Fd()), term.IsTerminal(os.Stderr.Fd())},
	}
	if json.NewEncoder(os.Stdout).Encode(report) != nil {
		os.Exit(93)
	}
	if _, err := fmt.Fprintln(os.Stderr, "CLAUDE_STDIO_STDERR"); err != nil {
		os.Exit(94)
	}
	park()
	os.Exit(0)
}

type claudeStdioStarter interface {
	startStdio(*managedNativeScope, Config, *exec.Cmd, *server, <-chan os.Signal) error
}

// The first RED is API absence; later omission checks exercise the real fence.
func requireClaudeStdioStarter(t *testing.T, birth *claudeBirth) claudeStdioStarter {
	t.Helper()
	starter, ok := any(birth).(claudeStdioStarter)
	if !ok {
		t.Fatal("managed Claude birth has no dormant stdio entry point")
	}
	return starter
}

type claudeStdioFixture struct {
	cfg                         Config
	scope                       *managedNativeScope
	birth                       *claudeBirth
	cmd                         *exec.Cmd
	srv                         *server
	input, output, diagnostic   *os.File
	childIn, childOut, childErr *os.File
	ends                        []*os.File
	waited                      bool
	signalsFinished             bool
}

func newClaudeStdioFixture(t *testing.T) *claudeStdioFixture {
	t.Helper()
	cfg, scope, birth, _ := claudeStartFixture(t)
	cfg.Argv[1] = claudeStdioChildArg
	f := &claudeStdioFixture{
		cfg: cfg, scope: scope, birth: birth,
		srv: newServer(nil, "", nil, nil, nil, 0, 100*time.Millisecond, &Metrics{}),
	}
	t.Cleanup(func() {
		if f.cmd != nil && f.cmd.Process != nil && !f.waited {
			_ = f.cmd.Process.Kill()
			_ = f.cmd.Wait()
			f.waited = true
		}
		f.finishSignals()
		for _, end := range f.ends {
			_ = end.Close()
		}
	})
	pipe := func() (*os.File, *os.File) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		f.ends = append(f.ends, r, w)
		return r, w
	}
	inR, inW := pipe()
	outR, outW := pipe()
	errR, errW := pipe()
	f.input, f.output, f.diagnostic = inW, outR, errR
	f.childIn, f.childOut, f.childErr = inR, outW, errW
	f.cmd = f.command()
	return f
}

func (f *claudeStdioFixture) command() *exec.Cmd {
	return &exec.Cmd{
		Path: f.cfg.Argv[0], Args: append([]string(nil), f.cfg.Argv...), Dir: f.cfg.Cwd,
		Env:   []string{"SWARM_TEST_CLAUDE_STDIO_CHILD=1", "PATH=/usr/bin:/bin"},
		Stdin: f.childIn, Stdout: f.childOut, Stderr: f.childErr,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
}

func (f *claudeStdioFixture) finishSignals() {
	if !f.signalsFinished {
		f.srv.finishEscalation()
		f.signalsFinished = true
	}
}

func (f *claudeStdioFixture) wait(t *testing.T) error {
	t.Helper()
	if f.waited || f.cmd.Process == nil || f.cmd.ProcessState != nil {
		t.Fatal("stdio child was absent or already waited before caller containment")
	}
	f.waited = true
	done := make(chan error, 1)
	go func() { done <- f.cmd.Wait() }()
	select {
	case err := <-done:
		f.finishSignals()
		return err
	case <-time.After(3 * time.Second):
		_ = f.cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("stdio child Wait did not join after emergency containment")
		}
		t.Fatal("registered stdio child did not stop within the containment bound")
		return nil
	}
}

func (f *claudeStdioFixture) stoppedProof(t *testing.T) NativeStoppedInfo {
	t.Helper()
	if err := f.scope.finish(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.cfg.SessionDir, NativeStoppedFile))
	var proof NativeStoppedInfo
	if err != nil || json.Unmarshal(raw, &proof) != nil || !proof.WritersStopped || proof.Native != f.scope.info {
		t.Fatalf("stdio scope lost exact stopped proof: %v", err)
	}
	return proof
}

func TestClaudeOwnedStdioStartHeldImagePipesAndExclusiveClaim(t *testing.T) {
	f := newClaudeStdioFixture(t)
	starter := requireClaudeStdioStarter(t, f.birth)
	replacement := f.cfg.Argv[0] + ".replacement"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 98\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, f.cfg.Argv[0]); err != nil {
		t.Fatal(err)
	}
	if err := starter.startStdio(f.scope, f.cfg, f.cmd, f.srv, nil); err != nil {
		t.Fatal(err)
	}
	if f.cmd.Process == nil || f.cmd.ProcessState != nil || !f.scope.recorded {
		t.Fatal("stdio birth failed to publish its live unwaited child")
	}
	deadline := time.Now().Add(3 * time.Second)
	if err := f.input.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(f.input, "exact stdio input\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.output.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	var report claudeStdioChildReport
	if err := json.NewDecoder(f.output).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.PID != f.cmd.Process.Pid || report.PGID != report.PID || report.Cwd != f.cfg.Cwd || !reflect.DeepEqual(report.Argv, f.cfg.Argv) || report.Input != "exact stdio input\n" || report.TTY != [3]bool{} {
		t.Fatalf("stdio child changed argv/CWD/input or acquired a terminal: %+v", report)
	}
	if err := f.diagnostic.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(f.diagnostic).ReadString('\n')
	if err != nil || line != "CLAUDE_STDIO_STDERR\n" {
		t.Fatalf("stderr was not a separate native pipe: %q %v", line, err)
	}
	for _, name := range []string{"exe", "fd/3"} {
		actual, err := os.Stat(filepath.Join("/proc", strconv.Itoa(report.PID), name))
		if err != nil || !os.SameFile(actual, f.birth.anchor) {
			t.Fatalf("stdio child did not retain the admitted image at %s: %v", name, err)
		}
	}
	f.srv.pgidMu.Lock()
	registered := f.srv.pgid
	f.srv.pgidMu.Unlock()
	if registered != report.PID || f.scope.info.PID != report.PID || f.scope.info.BackendPID != 0 {
		t.Fatal("stdio birth did not register exactly its one primary")
	}
	again := f.command()
	if err := starter.startStdio(f.scope, f.cfg, again, f.srv, nil); err == nil || again.Process != nil {
		if again.Process != nil {
			_ = again.Process.Kill()
			_ = again.Wait()
		}
		t.Fatal("same owned birth admitted a second stdio process")
	}
	f.srv.onSignal(shimwire.SigTerm)
	if err := f.wait(t); err == nil {
		t.Fatal("parked stdio child exited without the caller's containment signal")
	}
	f.stoppedProof(t)
}

func TestClaudeOwnedStdioStartRechecksSelectedProfileAfterCustody(t *testing.T) {
	f := newClaudeStdioFixture(t)
	starter := requireClaudeStdioStarter(t, f.birth)
	r, err := f.birth.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.birth.store.SelectClaudeProfile(r.Revision, r.Providers["claude"].ActiveID); err != nil {
		t.Fatal(err)
	}
	if err := starter.startStdio(f.scope, f.cfg, f.cmd, f.srv, nil); err == nil || f.cmd.Process != nil || f.scope.startAttempted || !f.scope.ownPreclaimDurable {
		t.Fatal("stale selected profile crossed Start or lost cleanup custody")
	}
	if proof := f.stoppedProof(t); proof.Native != f.scope.preclaim || proof.Native.PID != 0 {
		t.Fatal("refused selected profile lost its exact zero-PID scope")
	}
}

func TestClaudeOwnedStdioStartPendingStopNeverStarts(t *testing.T) {
	for _, kind := range []string{"buffered-term", "server-stop"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeStdioFixture(t)
			starter := requireClaudeStdioStarter(t, f.birth)
			signals := make(chan os.Signal, 1)
			if kind == "buffered-term" {
				signals <- syscall.SIGTERM
			} else {
				f.srv.onSignal(shimwire.SigKill)
			}
			err := starter.startStdio(f.scope, f.cfg, f.cmd, f.srv, signals)
			if !errors.Is(err, context.Canceled) || f.cmd.Process != nil || f.scope.startAttempted {
				t.Fatalf("pending Stop allowed a late native Start: %v", err)
			}
			if proof := f.stoppedProof(t); proof.Native.PID != 0 {
				t.Fatal("pre-Start Stop created native custody")
			}
		})
	}
}

func TestClaudeOwnedStdioStartPublicationFailureReturnsLiveUnwaitedChild(t *testing.T) {
	f := newClaudeStdioFixture(t)
	starter := requireClaudeStdioStarter(t, f.birth)
	original := nativeRecordSync
	t.Cleanup(func() { nativeRecordSync = original })
	nativeRecordSync = func(*os.Root) error { return syscall.ENOSPC }
	err := starter.startStdio(f.scope, f.cfg, f.cmd, f.srv, nil)
	if !errors.Is(err, syscall.ENOSPC) || f.cmd.Process == nil || f.cmd.ProcessState != nil || !f.scope.startAttempted || f.scope.recorded || !f.scope.ownPreclaimDurable {
		t.Fatalf("publication failure lost its live unwaited stdio child: %v", err)
	}
	again := f.command()
	if err := starter.startStdio(f.scope, f.cfg, again, f.srv, nil); err == nil || again.Process != nil {
		if again.Process != nil {
			_ = again.Process.Kill()
			_ = again.Wait()
		}
		t.Fatal("uncertain native publication admitted another process")
	}
	f.srv.onSignal(shimwire.SigTerm)
	if err := f.wait(t); err == nil {
		t.Fatal("publication failure waited for or lost the owned child")
	}
	nativeRecordSync = original
	f.stoppedProof(t)
}

func TestClaudeOwnedStdioStartRegistersSignalsBeforePublicationSync(t *testing.T) {
	f := newClaudeStdioFixture(t)
	starter := requireClaudeStdioStarter(t, f.birth)
	original := nativeRecordSync
	entered, release, started := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	joined := false
	var signalled chan struct{}
	t.Cleanup(func() { nativeRecordSync = original })
	nativeRecordSync = func(*os.Root) error {
		close(entered)
		<-release
		return syscall.ENOSPC
	}
	go func() { started <- starter.startStdio(f.scope, f.cfg, f.cmd, f.srv, nil) }()
	defer func() {
		unblock()
		if !joined {
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Error("stdio birth did not join after publication sync was released")
			}
		}
		if signalled != nil {
			select {
			case <-signalled:
			case <-time.After(3 * time.Second):
				t.Error("stdio signal did not join after publication sync was released")
			}
		}
	}()
	select {
	case <-entered:
	case err := <-started:
		joined = true
		t.Fatalf("stdio birth did not reach native publication sync: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("stdio birth stalled before native publication sync")
	}
	if f.cmd.Process == nil || f.cmd.ProcessState != nil {
		t.Fatal("publication sync did not retain an owned unwaited child")
	}
	signalled = make(chan struct{})
	go func() { f.srv.onSignal(shimwire.SigTerm); close(signalled) }()
	select {
	case <-signalled:
	case <-time.After(time.Second):
		t.Fatal("native publication fsync held the child signal mutex")
	}
	if err := f.wait(t); err == nil {
		t.Fatal("publication fsync hid the child from containment")
	}
	select {
	case err := <-started:
		joined = true
		t.Fatalf("birth returned while the actual publication sync remained held: %v", err)
	default:
	}
	unblock()
	err := <-started
	joined = true
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Stop swallowed native publication failure: %v", err)
	}
	nativeRecordSync = original
	f.stoppedProof(t)
}

func TestClaudeOwnedStdioStartRefusesInvalidSetupBeforeAttempt(t *testing.T) {
	for _, kind := range []string{"missing-server", "missing-stdin", "copying-stdout", "missing-stderr", "missing-group", "foreign-group", "controlling-terminal"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeStdioFixture(t)
			starter := requireClaudeStdioStarter(t, f.birth)
			srv := f.srv
			switch kind {
			case "missing-server":
				srv = nil
			case "missing-stdin":
				f.cmd.Stdin = nil
			case "copying-stdout":
				f.cmd.Stdout = io.Discard
			case "missing-stderr":
				f.cmd.Stderr = nil
			case "missing-group":
				f.cmd.SysProcAttr = nil
			case "foreign-group":
				f.cmd.SysProcAttr.Pgid = os.Getpid()
			case "controlling-terminal":
				f.cmd.SysProcAttr.Setsid = true
				f.cmd.SysProcAttr.Setctty = true
			}
			if err := starter.startStdio(f.scope, f.cfg, f.cmd, srv, nil); err == nil || f.cmd.Process != nil || f.scope.startAttempted {
				t.Fatal("invalid stdio setup consumed or crossed native Start")
			}
			f.stoppedProof(t)
		})
	}
}

func TestClaudeOwnedStdioPrerequisiteKeepsNativeChatAdmissionOff(t *testing.T) {
	f := newClaudeStdioFixture(t)
	f.cfg.RequiredRuntime = persist.RequiredRuntimeClaudeChat
	if err := ValidateClaudeChatConfig(f.cfg); err == nil {
		t.Fatal("dormant stdio birth enabled native chat admission")
	}
}

func TestClaudeOwnedStdioUnsupportedPlatformKeepsConstructorRefusal(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the existing managed constructor's non-Linux refusal is checked on that platform")
	}
	cfg, scope, _ := claudeClaimFixture(t)
	if birth, err := prepareClaudeBirth(cfg, scope); err == nil || birth != nil {
		if birth != nil {
			birth.close()
		}
		t.Fatal("stdio prerequisite relaxed non-Linux managed native admission")
	}
}
