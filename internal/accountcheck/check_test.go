//go:build linux

package accountcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "internal" && os.Args[2] == "account-check" {
		if RunWorker(context.Background(), os.Args[3], os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "fixture-check-writer" {
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
		start, _ := procstart.StartTime(os.Getpid())
		raw, _ := json.Marshal(processcontain.Identity{PID: os.Getpid(), StartTime: start})
		_ = os.WriteFile(filepath.Join(os.Args[2], "writer.json"), raw, 0o600)
		for {
			_ = os.WriteFile(filepath.Join(os.Args[2], "writes"), []byte(time.Now().String()), 0o600)
			time.Sleep(10 * time.Millisecond)
		}
	}
	if base := os.Getenv("CHECK_FIXTURE_DIR"); base != "" {
		child := exec.Command(os.Args[0], "fixture-check-writer", base)
		child.Env = nil
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if child.Start() != nil {
			os.Exit(91)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(base, "writer.json")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(92)
			}
			time.Sleep(10 * time.Millisecond)
		}
		mode, _ := os.ReadFile(filepath.Join(base, "mode"))
		if string(mode) == "crash" {
			_ = syscall.Kill(os.Getppid(), syscall.SIGKILL)
		}
		if string(mode) == "pending" {
			for {
				time.Sleep(time.Second)
			}
		}
		if len(os.Args) > 1 && os.Args[1] == "auth" {
			fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`)
		} else {
			fmt.Println(`{"type":"result","subtype":"success","is_error":false,"modelUsage":{"claude-synthetic":{}}}`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func checkFixture(t *testing.T, mode, checkMode string) (string, Config, string) {
	t.Helper()
	base := t.TempDir()
	state, home, cwd, fixture := filepath.Join(base, "state"), filepath.Join(base, "home"), filepath.Join(base, "scratch"), filepath.Join(base, "fixture")
	for _, path := range []string{state, home, cwd, fixture} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	candidate, err := store.CreateCandidate(accounts.ProviderClaude, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		".claude.json":      `{"oauthAccount":{"accountUuid":"check-fixture","organizationUuid":"check-fixture-org"}}`,
		".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-bearer","refreshToken":"synthetic-refresh","subscriptionType":"max","scopes":["user:inference"]}}`,
	} {
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err = store.VerifyCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	_, account, err := store.Admit(registry.Revision, candidate, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateRoot: state, Binding: binding, CLI: persist.CLIIdentity{Path: exe, Version: "2.1.288", Fingerprint: fingerprint}, Mode: checkMode, Model: "claude-synthetic", Cwd: cwd, Env: []string{"HOME=" + home, "CHECK_FIXTURE_DIR=" + fixture}, Deadline: time.Now().Add(20 * time.Second)}
	t.Cleanup(func() {
		var child processcontain.Identity
		raw, _ := os.ReadFile(filepath.Join(fixture, "writer.json"))
		if json.Unmarshal(raw, &child) == nil {
			_ = processcontain.SignalIdentity(child, syscall.SIGKILL)
		}
	})
	return exe, cfg, fixture
}

func fixtureWritersStopped(t *testing.T, stateRoot string, binding accounts.Binding) bool {
	t.Helper()
	root, err := openChecks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	return writersStopped(root, binding)
}

func fixtureWriter(t *testing.T, fixture string) processcontain.Identity {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var child processcontain.Identity
		raw, err := os.ReadFile(filepath.Join(fixture, "writer.json"))
		if err == nil && json.Unmarshal(raw, &child) == nil {
			return child
		}
		if time.Now().After(deadline) {
			t.Fatal("detached fixture writer not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOwnedCheckContainsDetachedWriterBeforeSuccess(t *testing.T) {
	for _, mode := range []string{ModeAuthStatus, ModeAvailability} {
		t.Run(mode, func(t *testing.T) {
			exe, cfg, fixture := checkFixture(t, "success", mode)
			var ref Ref
			raw, err := Run(context.Background(), exe, cfg, func(got Ref) error { ref = got; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "success") && mode == ModeAvailability {
				t.Fatal("missing bounded native result")
			}
			child := fixtureWriter(t, fixture)
			if _, err := procstart.StartTime(child.PID); !os.IsNotExist(err) {
				t.Fatal("proof/result published before detached writer was reaped")
			}
			if !CustodyStopped(cfg.StateRoot, ref.Worker, cfg.Binding) || !fixtureWritersStopped(t, cfg.StateRoot, cfg.Binding) {
				t.Fatal("exact clean custody proof unavailable")
			}
			wrong := ref.Worker
			wrong.StartTime++
			if CustodyStopped(cfg.StateRoot, wrong, cfg.Binding) {
				t.Fatal("stale incarnation accepted")
			}
		})
	}
}

func TestOwnedCheckCancellationContainsDetachedWriter(t *testing.T) {
	exe, cfg, fixture := checkFixture(t, "pending", ModeAvailability)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var ref Ref
	go func() { _, err := Run(ctx, exe, cfg, func(got Ref) error { ref = got; return nil }); done <- err }()
	child := fixtureWriter(t, fixture)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("owned check cancellation did not finish")
	}
	if child.Alive() || !CustodyStopped(cfg.StateRoot, ref.Worker, cfg.Binding) {
		t.Fatal("canceled check did not contain its detached writer")
	}
}

func TestOwnedAuthCheckCrashKeepsCustodyUnknownAndBlocksNextExec(t *testing.T) {
	exe, cfg, _ := checkFixture(t, "crash", ModeAuthStatus)
	var ref Ref
	_, err := Run(context.Background(), exe, cfg, func(got Ref) error { ref = got; return nil })
	if !errors.Is(err, ErrCustodyUnknown) || CustodyStopped(cfg.StateRoot, ref.Worker, cfg.Binding) || fixtureWritersStopped(t, cfg.StateRoot, cfg.Binding) {
		t.Fatal("crashed auth check inferred writer death")
	}
	called := false
	if _, err := Run(context.Background(), exe, cfg, func(Ref) error { called = true; return nil }); !errors.Is(err, ErrCustodyUnknown) || called {
		t.Fatal("unknown old auth writer admitted a second native exec")
	}
}

func TestOwnedCheckRejectedAdmissionHasCleanNoExecProof(t *testing.T) {
	exe, cfg, fixture := checkFixture(t, "success", ModeAvailability)
	rejected := errors.New("synthetic admission rejection")
	var ref Ref
	_, err := Run(context.Background(), exe, cfg, func(got Ref) error { ref = got; return rejected })
	if !errors.Is(err, rejected) || !CustodyStopped(cfg.StateRoot, ref.Worker, cfg.Binding) {
		t.Fatalf("rejected admission retained unknown custody: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture, "writer.json")); !os.IsNotExist(err) {
		t.Fatal("rejected admission executed native")
	}
}

func TestOwnedCheckCancelBeforeAdmissionHasCleanNoExecProof(t *testing.T) {
	exe, cfg, fixture := checkFixture(t, "success", ModeAvailability)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ref Ref
	_, err := Run(ctx, exe, cfg, func(got Ref) error { ref = got; cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) || !CustodyStopped(cfg.StateRoot, ref.Worker, cfg.Binding) {
		t.Fatalf("pre-exec cancellation retained unknown custody: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture, "writer.json")); !os.IsNotExist(err) {
		t.Fatal("canceled admission executed native")
	}
}

func TestOwnedCheckRejectsAliasedOrNonemptyAdmissionLock(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "nonempty"} {
		t.Run(kind, func(t *testing.T) {
			exe, cfg, fixture := checkFixture(t, "success", ModeAuthStatus)
			root, err := openChecks(cfg.StateRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			key := sha256.Sum256([]byte(cfg.Binding.Provider + ":" + cfg.Binding.AccountID + ":" + fmt.Sprint(cfg.Binding.CredentialGeneration)))
			path := filepath.Join(cfg.StateRoot, "accounts", "checks", ".lock-"+hex.EncodeToString(key[:]))
			target := filepath.Join(fixture, "lock-target")
			if err := os.WriteFile(target, []byte("synthetic sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "hardlink":
				err = os.Link(target, path)
			case "nonempty":
				err = os.WriteFile(path, []byte("invalid lock bytes"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Run(context.Background(), exe, cfg, nil); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsafe lock accepted: %v", err)
			}
			if fixtureWritersStopped(t, cfg.StateRoot, cfg.Binding) {
				t.Fatal("unsafe lock ignored by writer inventory")
			}
			if _, err := os.Stat(filepath.Join(fixture, "writer.json")); !os.IsNotExist(err) {
				t.Fatal("unsafe lock reached native exec")
			}
		})
	}
}
