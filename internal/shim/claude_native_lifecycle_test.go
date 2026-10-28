package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/status"
)

type claudePreparedChatLifetime interface {
	runClaudeChatStdio(Config, *managedNativeScope, <-chan os.Signal) (int, error)
}

const claudeLifecycleChildArg = "fixture-claude-chat-lifecycle"
const claudeLifecycleDescendantArg = "fixture-claude-chat-descendant"

// Copied synthetic test ELF, not fixed-pin Claude image qualification.
func init() {
	if len(os.Args) != 2 || os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_CHILD") != "1" {
		return
	}
	switch os.Args[1] {
	case claudeLifecycleDescendantArg:
		started, err := procstart.StartTime(os.Getpid())
		if err != nil {
			os.Exit(97)
		}
		ready, _ := json.Marshal(processcontain.Identity{PID: os.Getpid(), StartTime: started})
		if os.WriteFile(os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_DIR")+"/descendant.ready", ready, 0600) != nil {
			os.Exit(98)
		}
		// Independent of /dev/null stdin; the enclosing helper has its own deadline.
		time.Sleep(12 * time.Second)
		_ = os.WriteFile(os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_DIR")+"/descendant.expired", []byte("natural expiry"), 0600)
		os.Exit(99)
	case claudeLifecycleChildArg:
		mode := os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_MODE")
		if mode == "descendant" {
			image, err := os.Executable()
			if err != nil {
				os.Exit(90)
			}
			child := exec.Command(image, claudeLifecycleDescendantArg)
			child.Env = os.Environ()
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if child.Start() != nil {
				os.Exit(91)
			}
			start, err := procstart.StartTime(child.Process.Pid)
			if err != nil {
				os.Exit(92)
			}
			raw, _ := json.Marshal(processcontain.Identity{PID: child.Process.Pid, StartTime: start})
			if os.WriteFile(os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_DIR")+"/descendant.json", raw, 0600) != nil {
				os.Exit(93)
			}
			deadline := time.Now().Add(time.Second)
			for {
				ready, readErr := os.ReadFile(os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_DIR") + "/descendant.ready")
				var observed processcontain.Identity
				if readErr == nil && json.Unmarshal(ready, &observed) == nil && observed.PID == child.Process.Pid && observed.StartTime == start {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(89)
				}
				time.Sleep(time.Millisecond)
			}
		}
		if _, err := fmt.Fprintln(os.Stdout, `{"type":"synthetic-owned-stdio","marker":"OWNED_PRIMARY"}`); err != nil {
			os.Exit(94)
		}
		if mode == "stderr" {
			block := strings.Repeat("d", 32<<10)
			for i := 0; i < 35; i++ {
				if _, err := fmt.Fprint(os.Stderr, block); err != nil {
					os.Exit(95)
				}
			}
			fmt.Fprint(os.Stderr, "LAST_DIAGNOSTIC")
		}
		if mode == "publication" || mode == "capture" || strings.HasPrefix(mode, "early-hook") {
			park()
			os.Exit(96)
		}
		os.Exit(23)
	}
}

func TestClaudeChatManagedLifecyclePreparedScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed stdio mechanics require Linux")
	}
	if _, ok := any((*claudeBirth)(nil)).(claudePreparedChatLifetime); !ok {
		t.Fatal("APIabsence: prepared managed Claude lifetime is not implemented")
	}
	for _, kind := range []string{"normal", "stderr", "descendant", "publication", "capture", "early-hook", "early-hook-oversize", "invalid-runtime"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, selfExe(t), "-test.run=^TestClaudeChatManagedLifecycleHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "SWARM_TEST_CLAUDE_LIFECYCLE_HELPER=1", "SWARM_TEST_CLAUDE_LIFECYCLE_CASE="+kind)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("dedicated owned lifecycle helper: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "OWNED_LIFECYCLE_PROOF "+kind) {
				t.Fatalf("helper lacks positive completion witness: %s", out)
			}
		})
	}
}

// Only this dedicated helper installs subreaper custody. The parent test process
// never adopts unrelated test children, and the core receives prepared objects.
func TestClaudeChatManagedLifecycleHelper(t *testing.T) {
	if os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_HELPER") != "1" {
		return
	}
	kind := os.Getenv("SWARM_TEST_CLAUDE_LIFECYCLE_CASE")
	cfg, scope, birth, _ := claudeBirthFixture(t)
	lifetime, ok := any(birth).(claudePreparedChatLifetime)
	if !ok {
		t.Fatal("APIabsence: prepared managed lifetime")
	}
	cfg.RequiredRuntime = persist.RequiredRuntimeClaudeChat
	cfg.ConversationID = claudeChatTestUUID
	if kind == "invalid-runtime" {
		// An omitted guard may enable subreaper custody; keep this destructive
		// refusal control in the already dedicated helper, never the parent.
		original := *cfg.LoginRuntime
		for _, invalid := range []string{"common-home", "foreign-home", "malformed-identity", "zero-epoch"} {
			t.Run(invalid, func(t *testing.T) {
				copy := original
				switch invalid {
				case "common-home":
					copy.Home = filepath.Join(cfg.LoginStateRoot, "logins", "provider", "claude")
				case "foreign-home":
					copy.Home = filepath.Join(cfg.LoginStateRoot, "foreign-profile")
				case "malformed-identity":
					copy.Identity = "invalid"
				case "zero-epoch":
					copy.Epoch = 0
				}
				cfg.LoginRuntime = &copy
				native, stopped := []byte("retained foreign native bytes"), []byte("retained foreign stopped bytes")
				if err := os.WriteFile(filepath.Join(cfg.SessionDir, NativeProcessFile), native, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.SessionDir, NativeStoppedFile), stopped, 0600); err != nil {
					t.Fatal(err)
				}
				oldSync, calls := nativeRecordSync, 0
				nativeRecordSync = func(root *os.Root) error { calls++; return oldSync(root) }
				defer func() { nativeRecordSync = oldSync }()
				code, err := runClaudeChatManaged(cfg, nil)
				if code != 0 || !errors.Is(err, errLoginResolution) {
					t.Fatalf("wrong early runtime refusal: %d %v", code, err)
				}
				for name, expected := range map[string][]byte{NativeProcessFile: native, NativeStoppedFile: stopped} {
					got, err := os.ReadFile(filepath.Join(cfg.SessionDir, name))
					if err != nil || !bytes.Equal(got, expected) {
						t.Fatalf("invalid runtime changed retained %s: %v", name, err)
					}
				}
				if calls != 0 || scope.startAttempted || scope.info.PID != 0 {
					t.Fatal("invalid profile reached scope publication or Start")
				}
			})
		}
		fmt.Println("OWNED_LIFECYCLE_PROOF", kind)
		return
	}
	cfg.Argv = []string{cfg.Argv[0], claudeLifecycleChildArg}
	cfg.SocketPath = filepath.Join(cfg.SessionDir, "control.sock")
	cfg.HookSocketPath = filepath.Join(cfg.SessionDir, "hooks.sock")
	cfg.HookToken = "synthetic-owner-drain"
	cfg.GraceTimeout = 100 * time.Millisecond
	cfg.Env = []string{"PATH=/usr/bin:/bin", "SWARM_TEST_CLAUDE_LIFECYCLE_CHILD=1", "SWARM_TEST_CLAUDE_LIFECYCLE_MODE=" + kind, "SWARM_TEST_CLAUDE_LIFECYCLE_DIR=" + cfg.SessionDir}
	meta := persist.Meta{SchemaVersion: persist.ClaudeChatSchemaVersion, ID: cfg.SessionID, AgentType: "claude", Cwd: cfg.Cwd, LoginRuntime: cfg.LoginRuntime, CLIIdentity: cfg.CLIIdentity, ConversationID: cfg.ConversationID, RequiredRuntime: cfg.RequiredRuntime}
	// Preserve the actual running-state shape from the established fixture.
	meta.Status = status.Status{Process: status.ProcessRunning}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := birth.store.ClaimClaudeProfileCustody(*cfg.LoginRuntime, cfg.Cwd, func() error { return scope.claimClaudeReservation(cfg) }); err != nil {
		t.Fatal(err)
	}
	if err := resolveLoginEnvironment(&cfg); err != nil {
		t.Fatal(err)
	}
	if err := processcontain.EnableSubreaper(); err != nil {
		t.Fatal(err)
	}
	scope.subreaper = true
	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			if err := scope.finish(); err != nil {
				t.Error(err)
			}
		})
	}
	defer finish()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	type outcome struct {
		code int
		err  error
	}
	var got outcome
	if kind == "publication" || kind == "capture" || strings.HasPrefix(kind, "early-hook") {
		oldNative, oldHook := nativeRecordSync, hookSpoolSync
		entered, release := make(chan struct{}), make(chan struct{})
		var enteredOnce, releaseOnce sync.Once
		hold := func() { enteredOnce.Do(func() { close(entered) }); <-release }
		if kind == "capture" {
			hookSpoolSync = func(*os.File) error { hold(); return syscall.ENOSPC }
		} else {
			nativeRecordSync = func(root *os.Root) error {
				hold()
				if strings.HasPrefix(kind, "early-hook") {
					return oldNative(root)
				}
				return syscall.ENOSPC
			}
		}
		done := make(chan outcome, 1)
		joined := false
		go func() { code, err := lifetime.runClaudeChatStdio(cfg, scope, sigCh); done <- outcome{code, err} }()
		defer func() {
			releaseOnce.Do(func() { close(release) })
			if !joined {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("lifetime did not join after held IO release")
				}
			}
			nativeRecordSync = oldNative
			hookSpoolSync = oldHook
		}()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("managed lifetime did not reach held IO")
		}
		// The record/capture barrier follows assignment of this actual child birth.
		pid, start := scope.info.PID, scope.info.StartTime
		if pid <= 0 || start <= 0 {
			t.Fatal("held IO lacks actual child identity")
		}
		if strings.HasPrefix(kind, "early-hook") {
			body := []byte(`{"event":"Stop"}`)
			if kind == "early-hook" {
				hookSpoolSync = func(*os.File) error { return syscall.ENOSPC }
			} else {
				body = append([]byte(`{"data":"`), bytes.Repeat([]byte("x"), hookPostMaxBytes)...)
				body = append(body, []byte(`"}`)...)
			}
			if claudeFailedHookACK(t, cfg.HookSocketPath, body) {
				t.Fatal("pre-capture failed hook was ACKed")
			}
			hookSpoolSync = oldHook
		} else {
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
		}
		// During publication only, a zombie witnesses stopped execution while Start
		// still owns the call. During capture IO, require disappearance: the lifetime
		// must actually reap its direct child before capture persistence is released.
		deadline := time.Now().Add(2 * time.Second)
		for {
			current, err := procstart.StartTime(pid)
			if err != nil || current != start {
				break
			}
			if kind != "capture" {
				statRaw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if err == nil {
					end := strings.LastIndexByte(string(statRaw), ')')
					if end >= 0 && strings.HasPrefix(string(statRaw[end+1:]), " Z ") {
						break
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("containment/Wait waited for held persistence")
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-done:
			joined = true
			t.Fatal("lifetime returned before held IO released")
		default:
		}
		releaseOnce.Do(func() { close(release) })
		select {
		case got = <-done:
			joined = true
		case <-time.After(3 * time.Second):
			t.Fatal("IO failure retained child/reader/handler")
		}
		if kind == "capture" || strings.HasPrefix(kind, "early-hook") {
			if !errors.Is(got.err, errClaudeChatCapture) {
				t.Fatalf("capture/early-hook loss disappeared after publication: %v", got.err)
			}
		} else {
			if !errors.Is(got.err, syscall.ENOSPC) {
				t.Fatalf("publication error disappeared: %v", got.err)
			}
		}
		nativeRecordSync = oldNative
		hookSpoolSync = oldHook
	} else {
		coreStarted := time.Now()
		got.code, got.err = lifetime.runClaudeChatStdio(cfg, scope, sigCh)
		if kind == "descendant" {
			if time.Since(coreStarted) >= 3*time.Second {
				t.Fatal("core waited toward descendant natural expiry")
			}
			if _, err := os.Lstat(filepath.Join(cfg.SessionDir, "descendant.expired")); !os.IsNotExist(err) {
				t.Fatal("descendant natural expiry cannot qualify containment")
			}
		}
		if got.err != nil || got.code != 23 {
			t.Fatalf("owned exit/capture: %d %v", got.code, got.err)
		}
	}
	// No owner input/readiness capability may accidentally be advertised in A1.
	if _, err := os.Stat(cfg.SocketPath); err == nil {
		t.Fatal("dormant native lifetime published legacy control socket")
	}
	if scope.info.PID <= 0 || scope.info.BackendPID != 0 {
		t.Fatal("not exactly one recorded primary/zero backend")
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(scope.info.PID, &status, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatal("direct child was not reaped by the lifetime's Wait")
	}
	var descendant processcontain.Identity
	if kind == "descendant" {
		raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, "descendant.json"))
		if err != nil || json.Unmarshal(raw, &descendant) != nil || descendant.PID <= 0 {
			t.Fatalf("no ready descendant birth witness: %v", err)
		}
		if start, err := procstart.StartTime(descendant.PID); err == nil && start == descendant.StartTime {
			statRaw, err := os.ReadFile("/proc/" + strconv.Itoa(descendant.PID) + "/stat")
			end := strings.LastIndexByte(string(statRaw), ')')
			if err != nil || end < 0 || !strings.HasPrefix(string(statRaw[end+1:]), " Z ") {
				t.Fatal("escaped writer still executing BEFORE test-owned scope.finish")
			}
		}
	}
	finish()
	raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeStoppedFile))
	var proof NativeStoppedInfo
	if err != nil || json.Unmarshal(raw, &proof) != nil || !proof.WritersStopped || proof.Native != scope.info {
		t.Fatalf("positive managed stopped proof: %v", err)
	}
	if kind == "descendant" {
		if start, err := procstart.StartTime(descendant.PID); err == nil && start == descendant.StartTime {
			t.Fatal("descendant birth remained after managed stopped proof")
		}
	}
	other, err := OpenHookSpoolProducer(filepath.Join(cfg.SessionDir, HookSpoolFile), 0)
	if err != nil {
		t.Fatalf("lifetime retained shared producer: %v", err)
	}
	_ = other.Close()
	fmt.Println("OWNED_LIFECYCLE_PROOF", kind)
}
