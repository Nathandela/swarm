package shim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/creack/pty"
)

// Synthetic ELF mechanics only. The production constructor always uses the
// fixed-pin OpenClaudeNativeImage; this private fixture never qualifies a native
// release and is never reachable from Config or an optional admission callback.
func claudeStartFixture(t *testing.T) (Config, *managedNativeScope, *claudeBirth, logins.Login) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("private synthetic Claude file authority and fd exec are Linux-only")
	}
	cfg, scope, _ := claudeClaimFixture(t)
	// Account setup precedes any retained session reservation. Keep the same
	// private scope inode outside state until its exact metadata can be published.
	preparedParent := t.TempDir()
	if err := os.Chmod(preparedParent, 0700); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(preparedParent, "prepared-scope")
	if err := os.Rename(cfg.SessionDir, prepared); err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(selfExe(t))
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(cfg.Argv[0], os.O_WRONLY|os.O_TRUNC, 0700)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, source)
	_ = source.Close()
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("synthetic image copy: %v %v", copyErr, closeErr)
	}
	fingerprint, err := persist.CLIContentFingerprint(cfg.Argv[0])
	if err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(cfg.LoginStateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	expires := time.UnixMilli(time.Now().Add(time.Hour).UnixMilli()).UTC()
	grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expires.UnixMilli(), "scopes": []string{"user:profile", "user:inference"}}})
	r, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, login, err := store.Add(r.Revision, logins.ProviderClaude, "synthetic native mechanics", logins.Credential{Auth: grant, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"synthetic-birth","organizationUuid":"synthetic-org"},"userID":"synthetic-birth"}`)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.ProvisionClaudeProfile(r.Revision, login.ID, []string{cwd})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := store.BeginClaudeAuth(r.Revision, login.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(filepath.Join(cfg.LoginStateRoot, "logins", "saved", login.ID, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	generation := sha256.Sum256(canonical)
	started := time.Now().Add(-time.Second).UTC()
	// Exact test-owned settled receipt; not native authentication evidence.
	receipt, _ := json.Marshal(map[string]any{"version": 1, "ref": ref, "selection": r.Providers[logins.ProviderClaude], "deadline": started.Add(45 * time.Second), "attempt": 1, "phase": "ready", "started_at": started, "generation": hex.EncodeToString(generation[:]), "worker": processcontain.Identity{PID: 999999998, StartTime: 1}, "native": processcontain.Identity{PID: 999999999, StartTime: 1}, "stop_proof": "native-tree", "expires_at": expires})
	if err := os.WriteFile(filepath.Join(cfg.LoginStateRoot, "logins", "claude-auth", login.ID+".json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	r, err = store.SelectClaudeProfile(r.Revision, login.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateClaudeAuthReceipt(ref, r.Providers[logins.ProviderClaude].Epoch); err != nil {
		t.Fatal(err)
	}
	home, err := store.ClaudeProfileHome(login.Identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LoginRuntime = &logins.Runtime{Provider: logins.ProviderClaude, Epoch: r.Providers[logins.ProviderClaude].Epoch, Identity: login.Identity, Home: home}
	cfg.Cwd = cwd
	cfg.CLIIdentity.Fingerprint = fingerprint
	cfg.Argv = append(cfg.Argv, "synthetic-owned-argv")
	scope.info.Runtime = *cfg.LoginRuntime
	scope.info.Cwd = cwd
	scope.info.CLIFingerprint = fingerprint
	if err := os.Rename(prepared, cfg.SessionDir); err != nil {
		t.Fatal(err)
	}
	meta := persist.Meta{SchemaVersion: persist.ClaudeSchemaVersion, ID: cfg.SessionID, AgentType: logins.ProviderClaude, Cwd: cwd, LoginRuntime: cfg.LoginRuntime, CLIIdentity: cfg.CLIIdentity, Status: status.Status{Process: status.ProcessRunning}}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	image, err := os.Open(cfg.Argv[0])
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := image.Stat()
	if err != nil {
		_ = image.Close()
		t.Fatal(err)
	}
	birth := &claudeBirth{store: store, image: image, anchor: anchor}
	t.Cleanup(func() { _ = image.Close() })
	return cfg, scope, birth, login
}

func claudeSyntheticCmd(cfg Config) *exec.Cmd {
	return &exec.Cmd{Path: cfg.Argv[0], Args: append([]string(nil), cfg.Argv...), Dir: cfg.Cwd, Env: []string{helperEnvVar + "=" + modeIdle, "PATH=/usr/bin:/bin"}}
}

func TestClaudeOwnedStartSyntheticHeldImageAndExclusiveClaim(t *testing.T) {
	cfg, scope, birth, _ := claudeStartFixture(t)
	replacement := cfg.Argv[0] + ".replacement"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 98\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, cfg.Argv[0]); err != nil {
		t.Fatal(err)
	}
	cmd := claudeSyntheticCmd(cfg)
	ptmx, err := birth.start(scope, cfg, cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if cmd.Process != nil {
		defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	}
	if err != nil || ptmx == nil || cmd.Process == nil {
		t.Fatalf("held image start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	actual, err := os.Stat(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "exe"))
	if err != nil || !os.SameFile(actual, birth.anchor) {
		t.Fatalf("native did not execute retained image: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "cmdline"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if !reflect.DeepEqual(args, cfg.Argv) {
		t.Fatal("held image changed original argv0 or flags")
	}
	fds, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "fd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) > 128 {
		t.Fatal("unexpected synthetic child descriptor inventory")
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "fd", fd.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if target == cfg.LoginStateRoot || target == cfg.SessionDir || target == filepath.Join(cfg.LoginStateRoot, "logins") || target == filepath.Join(cfg.LoginStateRoot, "logins", ".registry-lock") {
			t.Fatal("registry or private scope descriptor leaked through exec")
		}
	}
	again := claudeSyntheticCmd(cfg)
	if _, err := birth.start(scope, cfg, again, &pty.Winsize{}); err == nil || again.Process != nil {
		t.Fatal("same birth claim started a second process")
	}
}

func TestClaudeOwnedStartSyntheticNewSelectionRefusesBeforeClaim(t *testing.T) {
	cfg, scope, birth, login := claudeStartFixture(t)
	r, err := birth.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := birth.store.SelectClaudeProfile(r.Revision, login.ID); err != nil {
		t.Fatal(err)
	}
	cmd := claudeSyntheticCmd(cfg)
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	}()
	if _, err := birth.start(scope, cfg, cmd, &pty.Winsize{}); err == nil || cmd.Process != nil || scope.ownPreclaimDurable {
		t.Fatal("stale selected epoch crossed claim/start")
	}
	if _, err := os.Lstat(filepath.Join(cfg.SessionDir, NativeProcessFile)); !os.IsNotExist(err) {
		t.Fatal("refused selection published native scope")
	}
}

func TestClaudeOwnedStartSyntheticPublicationFailureReturnsUnwaitedChild(t *testing.T) {
	cfg, scope, birth, _ := claudeStartFixture(t)
	original := nativeRecordSync
	t.Cleanup(func() { nativeRecordSync = original })
	writes := 0
	nativeRecordSync = func(root *os.Root) error {
		writes++
		if writes == 2 {
			return syscall.ENOSPC
		}
		return original(root)
	}
	cmd := claudeSyntheticCmd(cfg)
	ptmx, err := birth.start(scope, cfg, cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if cmd.Process == nil {
		t.Fatalf("post-start fixture did not start: %v", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	}()
	if ptmx != nil {
		defer func() { _ = ptmx.Close() }()
	}
	if !errors.Is(err, syscall.ENOSPC) || ptmx == nil || cmd.ProcessState != nil || !scope.ownPreclaimDurable || scope.recorded {
		t.Fatalf("post-start publication lost child custody: %v", err)
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	waited = true
	nativeRecordSync = original
	if err := scope.finish(); err != nil {
		t.Fatal(err)
	}
}
