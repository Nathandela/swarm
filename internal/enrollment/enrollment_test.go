//go:build linux

package enrollment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/coder/websocket"
)

const fixtureSecret = "fixture-private-device-code"
const fixtureTerminal = "fixture-private-native-terminal"
const secureStorageChild = "SWARM_TEST_CLAUDE_SECURE_STORAGE_CHILD"
const secureStorageAmbientDir = "SWARM_TEST_CLAUDE_AMBIENT_DIR"
const secureStorageExpectedDir = "SWARM_TEST_CLAUDE_EXPECTED_DIR"

// A real detached same-binary supervisor/runner uses a fake native CLI and a
// real WebSocket-over-UDS server. No provider authentication/network occurs.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "internal" && os.Args[2] == "account-enroll" {
		if err := RunConfig(context.Background(), os.Args[3]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "fixture-grandchild" {
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
		start, _ := procstart.StartTime(os.Getpid())
		raw, _ := json.Marshal(ProcessIdentity{PID: os.Getpid(), StartTime: start})
		_ = os.WriteFile("fixture-descendant.json", raw, 0o600)
		for {
			time.Sleep(time.Second)
		}
	}
	if os.Getenv("CODEX_HOME") != "" || os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			if os.Getenv("CODEX_HOME") != "" {
				fmt.Println("codex-cli 0.160.0")
			} else {
				fmt.Println("2.1.288 (Claude Code)")
			}
			os.Exit(0)
		}
		for _, arg := range os.Args {
			if arg == "app-server" {
				fakeCodex()
				os.Exit(0)
			}
		}
		if len(os.Args) > 2 && os.Args[1] == "auth" {
			if os.Args[2] == "status" {
				fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max","email":"fixture@example.test"}`)
				os.Exit(0)
			}
			if os.Args[2] == "login" {
				fmt.Println(fixtureTerminal)
				var input string
				_, _ = fmt.Scanln(&input)
				if input == "finish" {
					fakeClaudeCredentials()
					os.Exit(0)
				}
				os.Exit(1)
			}
		}
	}
	os.Exit(m.Run())
}

func fixtureMode() string { raw, _ := os.ReadFile("fixture-mode"); return string(raw) }

func fixtureDescendant() {
	executable, _ := os.Executable()
	cmd := exec.Command(executable, "fixture-grandchild")
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

func fakeCodexCredentials() {
	_ = os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"fixture-account","access_token":"fixture-bearer","refresh_token":"fixture-refresh"}}`), 0o600)
}

func fakeClaudeCredentials() {
	profile := os.Getenv("CLAUDE_CONFIG_DIR")
	_ = os.WriteFile(filepath.Join(profile, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"fixture-account","organizationUuid":"fixture-org"}}`), 0o600)
	_ = os.WriteFile(filepath.Join(profile, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"fixture-bearer","refreshToken":"fixture-refresh","subscriptionType":"max","scopes":["user:inference"]}}`), 0o600)
}

func fakeCodex() {
	var socket string
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "unix://") {
			socket = strings.TrimPrefix(arg, "unix://")
		}
	}
	physical := socket
	if strings.HasPrefix(fixtureMode(), "alias-") {
		dir := filepath.Join("/tmp", fmt.Sprintf("codex-daemon-%d", os.Getuid()))
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			os.Exit(2)
		}
		physical = filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(socket))))
	}
	ln, err := net.Listen("unix", physical)
	if err != nil {
		os.Exit(2)
	}
	if os.Chmod(physical, 0o600) != nil {
		os.Exit(2)
	}
	if physical != socket && os.Symlink(physical, socket) != nil {
		os.Exit(2)
	}
	handler := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(rw, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		authenticated := false
		write := func(value any) {
			raw, _ := json.Marshal(value)
			_ = conn.Write(context.Background(), websocket.MessageText, raw)
		}
		for {
			_, raw, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var rpc struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(raw, &rpc) != nil {
				return
			}
			var result any
			switch rpc.Method {
			case "initialized":
				continue
			case "initialize":
				result = map[string]any{"userAgent": "codex-cli 0.160.0"}
			case "account/read":
				if authenticated {
					result = map[string]any{"account": map[string]any{"type": "chatgpt", "email": "fixture@example.test", "planType": "plus"}}
				} else {
					result = map[string]any{"account": nil}
				}
			case "account/login/start":
				mode := fixtureMode()
				if mode == "pending" || mode == "late-cancel" {
					fixtureDescendant()
				}
				if mode == "early-null" || mode == "alias-null" || mode == "mismatch" {
					fakeCodexCredentials()
					authenticated = true
					var id any
					if mode == "mismatch" {
						id = "wrong-attempt"
					}
					write(map[string]any{"method": "account/login/completed", "params": map[string]any{"loginId": id, "success": true}})
				}
				result = map[string]any{"type": "chatgptDeviceCode", "loginId": "fixture-login", "verificationUrl": "https://auth.example.test/" + fixtureSecret, "userCode": fixtureSecret}
			case "account/login/cancel":
				fakeCodexCredentials()
				write(map[string]any{"method": "account/login/completed", "params": map[string]any{"success": true}})
				write(map[string]any{"id": rpc.ID, "error": map[string]any{"code": -32000, "message": "notFound"}})
				continue
			default:
				result = nil
			}
			write(map[string]any{"id": rpc.ID, "result": result})
		}
	})
	_ = http.Serve(ln, handler)
}

func testConfig(t *testing.T, provider, mode string) Config {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "enr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := store.CreateCandidate(provider, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The repository's tool environment uses a group-writable build umask.
	// Make this disposable native-fixture executable owner-only for the same
	// fail-closed launch permission check used by real enrollment.
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{SchemaVersion: SchemaVersion, StateRoot: state, JobID: "11223344556677889900aabbccddeeff", CandidateID: candidate.ID, CandidateProfileGeneration: candidate.ProfileGeneration, Generation: 1, Provider: provider, NativePath: executable, Deadline: time.Now().Add(20 * time.Second)}
	if provider == "codex" {
		cfg.Method = MethodDeviceCode
		cfg.NativeVersion = "0.160.0"
	} else {
		cfg.Method = MethodNativeLogin
		cfg.NativeVersion = "2.1.288"
	}
	files, err := openJob(state, cfg.JobID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.root.WriteFile("fixture-mode", []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	files.Close()
	return cfg
}

func startFixture(t *testing.T, cfg Config) Ref {
	t.Helper()
	executable, _ := os.Executable()
	ref, err := Start(context.Background(), executable, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ref.Worker.Alive() {
			_ = FenceCancel(ref.StateRoot, ref.JobID, ref.Generation)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = Cancel(ctx, ref)
			cancel()
			_ = signalIdentity(ref.Worker, syscall.SIGKILL)
		}
	})
	return ref
}

func waitProgress(t *testing.T, ref Ref, predicate func(Progress) bool) Progress {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, err := ReadProgress(ref.StateRoot, ref.JobID)
		if err == nil && predicate(p) {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	p, _ := ReadProgress(ref.StateRoot, ref.JobID)
	t.Fatalf("worker progress did not converge: phase=%s error=%s stopped=%v", p.Phase, p.ErrorCode, p.WritersStopped)
	return Progress{}
}

func assertNoJobSecrets(t *testing.T, cfg Config) {
	t.Helper()
	_ = filepath.WalkDir(filepath.Join(cfg.StateRoot, "accounts", "jobs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, _ := entry.Info()
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, _ := os.ReadFile(path)
		for _, secret := range []string{fixtureSecret, fixtureTerminal, "fixture-bearer", "fixture-refresh"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("private native output reached a durable job artifact")
			}
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("job artifact permissions: %o", info.Mode().Perm())
		}
		return nil
	})
}

func TestDetachedCodexEarlyNullCompletion(t *testing.T) {
	cfg := testConfig(t, "codex", "early-null")
	ref := startFixture(t, cfg)
	p := waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseReady || p.Worker != ref.Worker || p.Runner.Alive() {
		t.Fatalf("unexpected terminal state: phase=%s code=%s", p.Phase, p.ErrorCode)
	}
	if p.Email != "fixture@example.test" || p.Plan != "plus" {
		t.Fatal("completed device sign-in lost its verified display identity")
	}
	assertNoJobSecrets(t, cfg)
	store, err := accounts.Open(cfg.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	r, err := store.Snapshot()
	if err != nil || len(r.Accounts) != 0 {
		t.Fatal("worker modified registry admission")
	}
}

func TestDetachedCodexNativeSocketAlias(t *testing.T) {
	cfg := testConfig(t, "codex", "alias-null")
	ref := startFixture(t, cfg)
	socket := filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID, "native.sock")
	target := filepath.Join("/tmp", fmt.Sprintf("codex-daemon-%d", os.Getuid()), fmt.Sprintf("%x", sha256.Sum256([]byte(socket))))
	t.Cleanup(func() {
		if p, err := ReadProgress(cfg.StateRoot, cfg.JobID); err == nil && p.WritersStopped && p.NativeWritersStopped {
			_ = os.Remove(target)
		}
	})
	p := waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseReady || p.Worker != ref.Worker || p.Runner.Alive() {
		t.Fatalf("native socket alias did not complete enrollment: phase=%s code=%s", p.Phase, p.ErrorCode)
	}
	assertNoJobSecrets(t, cfg)
}

func TestCodexSocketPathRejectsUnsafeAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.sock")
	dir := filepath.Join("/tmp", fmt.Sprintf("codex-daemon-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	expected := filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(path))))
	for _, target := range []string{filepath.Join(t.TempDir(), "other.sock"), filepath.Join(dir, strings.Repeat("0", 64)), expected} {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := codexSocketPath(path); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe or dangling socket alias accepted: %v", err)
		}
		_ = os.Remove(path)
	}
	if err := os.WriteFile(expected, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(expected) })
	if err := os.Symlink(expected, path); err != nil {
		t.Fatal(err)
	}
	if _, err := codexSocketPath(path); !errors.Is(err, ErrUnsafe) {
		t.Fatal("non-socket alias target accepted")
	}
}

func TestDetachedCodexRejectsConflictingCompletion(t *testing.T) {
	cfg := testConfig(t, "codex", "mismatch")
	ref := startFixture(t, cfg)
	p := waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseFailed {
		t.Fatal("conflicting completion accepted")
	}
	assertNoJobSecrets(t, cfg)
}

func TestCancelFenceWinsLateSuccessAndContainsEscapedChild(t *testing.T) {
	cfg := testConfig(t, "codex", "late-cancel")
	ref := startFixture(t, cfg)
	waitProgress(t, ref, func(p Progress) bool { return p.Phase == PhaseAuthenticating })
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	status, err := Status(ctx, ref)
	if err != nil || status.Device == nil || status.Device.UserCode != fixtureSecret {
		t.Fatal("live device presentation unavailable")
	}
	child := waitFixtureDescendant(t, cfg)
	if _, err := Cancel(ctx, ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("unrecorded cancellation accepted")
	}
	if err := FenceCancel(cfg.StateRoot, cfg.JobID, cfg.Generation); err != nil {
		t.Fatal(err)
	}
	p, err := Cancel(ctx, ref)
	if err != nil || p.Phase != PhaseCanceled || !p.WritersStopped {
		t.Fatalf("cancel did not fence ready: phase=%s code=%s err=%v", p.Phase, p.ErrorCode, err)
	}
	if child.Alive() {
		t.Fatal("escaped native descendant survived cancellation")
	}
	assertNoJobSecrets(t, cfg)
}

func waitFixtureDescendant(t *testing.T, cfg Config) ProcessIdentity {
	t.Helper()
	path := filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID, "fixture-descendant.json")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		var child ProcessIdentity
		if err == nil && json.Unmarshal(raw, &child) == nil && child.Alive() {
			return child
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture descendant did not start")
	return ProcessIdentity{}
}

func TestRunnerCrashStopsNativeWriters(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	ref := startFixture(t, cfg)
	p := waitProgress(t, ref, func(p Progress) bool { return p.Phase == PhaseAuthenticating })
	child := waitFixtureDescendant(t, cfg)
	if err := signalIdentity(p.Runner, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	p = waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseFailed || child.Alive() {
		t.Fatal("runner crash retained native writer")
	}
	assertNoJobSecrets(t, cfg)
}

func TestSupervisorCrashStopsWritersAndCancellationCanReconcile(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	ref := startFixture(t, cfg)
	p := waitProgress(t, ref, func(p Progress) bool { return p.Phase == PhaseAuthenticating })
	child := waitFixtureDescendant(t, cfg)
	if err := signalIdentity(ref.Worker, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := FenceCancel(cfg.StateRoot, cfg.JobID, cfg.Generation); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	result, err := Cancel(ctx, ref)
	if err != nil || !result.WritersStopped || result.Phase != PhaseCanceled || p.Runner.Alive() || child.Alive() {
		t.Fatalf("supervisor crash retained a writer: err=%v", err)
	}
	assertNoJobSecrets(t, cfg)
}

func TestDeadlineContainsWriters(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	cfg.Deadline = time.Now().Add(3 * time.Second)
	ref := startFixture(t, cfg)
	waitProgress(t, ref, func(p Progress) bool { return p.Phase == PhaseAuthenticating })
	child := waitFixtureDescendant(t, cfg)
	p := waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseFailed || child.Alive() {
		t.Fatal("deadline retained native writer")
	}
	assertNoJobSecrets(t, cfg)
}

func TestClaudePrivatePTYAttachAndReady(t *testing.T) {
	cfg := testConfig(t, "claude", "pty")
	ref := startFixture(t, cfg)
	waitProgress(t, ref, func(p Progress) bool { return p.Phase == PhaseAuthenticating })
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	status, err := Status(ctx, ref)
	if err != nil || status.LoginSocket != ref.SocketPath() {
		t.Fatal("login terminal socket unavailable")
	}
	session, err := AttachSocket(ctx, status.LoginSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Detach() }()
	if _, err := vt.DecodeSnapshot(session.Snapshot()); err != nil {
		t.Fatal("invalid private terminal snapshot")
	}
	if err := session.Resize(90, 30); err != nil {
		t.Fatal(err)
	}
	if err := session.Input([]byte("finish\n")); err != nil {
		t.Fatal(err)
	}
	p := waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseReady {
		t.Fatalf("Claude candidate not ready: %s", p.ErrorCode)
	}
	if p.Email != "fixture@example.test" || p.Plan != "max" {
		t.Fatal("completed Claude sign-in lost its verified display identity")
	}
	assertNoJobSecrets(t, cfg)
}

func TestCredentialEnvironmentScrubPreservesHome(t *testing.T) {
	cfg := Config{StateRoot: "/private", CandidateProfileGeneration: "fixture", Provider: "claude"}
	env := NativeEnvironment(cfg, []string{"HOME=/ordinary", "PATH=/usr/bin", "CLAUDE_CODE_OAUTH_TOKEN=private", "ANTHROPIC_API_KEY=private", "OPENAI_API_KEY=private", "CODEX_HOME=/ambient", "CLAUDE_CONFIG_DIR=/ambient", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/ambient-secure", runnerEnv + "=bad", "HTTP_PROXY=private"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "private\n") || strings.Contains(joined, "TOKEN=") || strings.Contains(joined, "API_KEY=") || strings.Contains(joined, "=/ambient") || !strings.Contains(joined, "HOME=/ordinary") || !strings.Contains(joined, "CLAUDE_CONFIG_DIR=/private/accounts/profiles/fixture") || !strings.Contains(joined, "CLAUDE_SECURESTORAGE_CONFIG_DIR=/private/accounts/profiles/fixture") {
		t.Fatal("credential environment not isolated")
	}
}

func TestClaudeSecureStorageSelectorChildSentinel(t *testing.T) {
	if os.Getenv(secureStorageChild) == "1" {
		ambient := os.Getenv(secureStorageAmbientDir)
		selected := os.Getenv("CLAUDE_SECURESTORAGE_CONFIG_DIR")
		expected := os.Getenv(secureStorageExpectedDir)
		if selected == "" || selected != expected || selected == ambient || os.Getenv("CLAUDE_CONFIG_DIR") != expected {
			t.Fatalf("Claude profile selectors are not both isolated: config=%q secure-storage=%q", os.Getenv("CLAUDE_CONFIG_DIR"), selected)
		}
		if os.Getenv("HOME") != "/ordinary" {
			t.Fatalf("ordinary HOME was not preserved: %q", os.Getenv("HOME"))
		}
		if _, err := os.Stat(filepath.Join(selected, "ambient-sentinel")); !os.IsNotExist(err) {
			t.Fatalf("ambient sentinel appeared through the selected profile: %v", err)
		}
		if err := os.MkdirAll(selected, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(selected, "child-write-sentinel"), []byte("private-profile"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	ambient := filepath.Join(root, "ambient-secure-storage")
	if err := os.MkdirAll(ambient, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "ambient-sentinel"), []byte("ambient-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateRoot: filepath.Join(root, "private"), CandidateProfileGeneration: "candidate-1", Provider: "claude"}
	profile := filepath.Join(cfg.StateRoot, "accounts", "profiles", cfg.CandidateProfileGeneration)
	env := NativeEnvironment(cfg, []string{"HOME=/ordinary", "PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/ambient", "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + ambient})
	env = append(env,
		secureStorageChild+"=1",
		secureStorageAmbientDir+"="+ambient,
		secureStorageExpectedDir+"="+profile,
	)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestClaudeSecureStorageSelectorChildSentinel$")
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("secure-storage child failed: %v\n%s", err, output)
	}
	if raw, err := os.ReadFile(filepath.Join(ambient, "ambient-sentinel")); err != nil || string(raw) != "ambient-only" {
		t.Fatalf("ambient sentinel changed: %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(ambient, "child-write-sentinel")); !os.IsNotExist(err) {
		t.Fatalf("child wrote through the ambient secure-storage selector: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(profile, "child-write-sentinel")); err != nil || string(raw) != "private-profile" {
		t.Fatalf("private profile did not receive the child write: %q, %v", raw, err)
	}
}

func TestUnsafeConfigStartsNoWorker(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	cfg.CandidateProfileGeneration = "../escape"
	executable, _ := os.Executable()
	if _, err := Start(context.Background(), executable, cfg); err == nil {
		t.Fatal("unsafe profile selector accepted")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID, ConfigFile)); !os.IsNotExist(err) {
		t.Fatal("unsafe config persisted")
	}
}

func TestValidateNativePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode os.FileMode
		want error
	}{{0o700, nil}, {0o755, nil}, {0o775, ErrUnsafe}, {0o757, ErrUnsafe}, {0o644, ErrUnsafe}} {
		t.Run(fmt.Sprintf("mode_%o", tc.mode), func(t *testing.T) {
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := ValidateNativePath(path); !errors.Is(err, tc.want) {
				t.Fatalf("native mode %o: got %v, want %v", tc.mode, err, tc.want)
			}
		})
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := path + "-alias"
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{alias, filepath.Dir(path), path + "-missing", "relative-native"} {
		if err := ValidateNativePath(unsafe); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe native path accepted: %s", unsafe)
		}
	}
}

func TestGroupWritableNativeStartsNoWorker(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	cfg.NativePath = filepath.Join(t.TempDir(), "codex.js")
	if err := os.WriteFile(cfg.NativePath, []byte("#!/bin/sh\nexit 0\n"), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.NativePath, 0o775); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	if _, err := Start(context.Background(), executable, cfg); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("group-writable native launch: got %v, want unsafe", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID, ConfigFile)); !os.IsNotExist(err) {
		t.Fatal("unsafe native launch persisted worker configuration")
	}
}

func TestStartCanceledContextHasNoSideEffects(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executable, _ := os.Executable()
	if _, err := Start(ctx, executable, cfg); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled caller started a worker")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateRoot, "accounts", "jobs", cfg.JobID, ConfigFile)); !os.IsNotExist(err) {
		t.Fatal("canceled caller persisted worker configuration")
	}
}

func TestCanceledBeforeExecStartsNoNativeChild(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	if err := FenceCancel(cfg.StateRoot, cfg.JobID, cfg.Generation); err != nil {
		t.Fatal(err)
	}
	ref := startFixture(t, cfg)
	p := waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped })
	if p.Phase != PhaseCanceled || p.Runner.PID != 0 || len(p.Children) != 0 {
		t.Fatal("canceled generation started a native writer")
	}
	assertNoJobSecrets(t, cfg)
}

func TestStaleWorkerIdentityCannotCancelOrReadDevice(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	ref := startFixture(t, cfg)
	waitProgress(t, ref, func(p Progress) bool { return p.Phase == PhaseAuthenticating })
	stale := ref
	stale.Worker.StartTime++
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	status, err := Status(ctx, stale)
	if !errors.Is(err, ErrStale) || status.Device != nil {
		t.Fatal("stale worker identity exposed live authentication")
	}
	if err := signalIdentity(stale.Worker, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !ref.Worker.Alive() {
		t.Fatal("stale process identity killed a different generation")
	}
	if err := FenceCancel(cfg.StateRoot, cfg.JobID, cfg.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := Cancel(ctx, stale); !errors.Is(err, ErrStale) {
		t.Fatal("stale process identity canceled a different generation")
	}
	if _, err := Cancel(ctx, ref); err != nil {
		t.Fatal(err)
	}
}

func TestAbandonedTemporaryFilesCannotBlockProgressOrCancel(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	files, err := openJob(cfg.StateRoot, cfg.JobID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	for _, name := range []string{ProgressFile, CancelFile} {
		// A killed writer can leave any prior temp inode behind. Neither the
		// old fixed filename nor a prior random filename may block new writes.
		for _, abandoned := range []string{"." + name + ".tmp", "." + name + ".11223344556677889900aabbccddeeff.tmp"} {
			if err := files.root.WriteFile(abandoned, []byte(`{"generation":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	progress := Progress{SchemaVersion: SchemaVersion, JobID: cfg.JobID, Generation: cfg.Generation, Phase: PhaseFailed, WritersStopped: true, UpdatedAt: time.Now().UTC()}
	if err := files.write(ProgressFile, progress); err != nil {
		t.Fatal(err)
	}
	if read, err := ReadProgress(cfg.StateRoot, cfg.JobID); err != nil || read.Phase != PhaseFailed || !read.WritersStopped {
		t.Fatal("abandoned temporary progress prevented terminal proof")
	}
	if err := FenceCancel(cfg.StateRoot, cfg.JobID, cfg.Generation); err != nil || !files.canceled(cfg.Generation) {
		t.Fatal("abandoned temporary cancellation prevented durable cancel")
	}
}

func TestCancellationBeforeRunnerPublishesCompleteDeathProof(t *testing.T) {
	cfg := testConfig(t, "codex", "pending")
	if err := FenceCancel(cfg.StateRoot, cfg.JobID, cfg.Generation); err != nil {
		t.Fatal(err)
	}
	ref := startFixture(t, cfg)
	waitProgress(t, ref, func(p Progress) bool { return p.WritersStopped && !p.Worker.Alive() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := Cancel(ctx, ref)
	if err != nil || p.Phase != PhaseCanceled || p.Runner.PID != 0 || len(p.Children) != 0 || !p.WritersStopped || !p.NativeWritersStopped {
		t.Fatal("pre-native cancellation omitted the complete proof needed by canonical authority")
	}
}
