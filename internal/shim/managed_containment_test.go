//go:build linux

package shim

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/transcript"
)

// All subreaper mutation and all synthetic native descendants live in a
// dedicated re-exec process; the test runner never adopts unrelated children.
func init() {
	if len(os.Args) < 3 {
		return
	}
	mode, path := os.Args[1], os.Args[2]
	switch mode {
	case "fixture-managed-shim":
		var cfg Config
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &cfg) != nil {
			os.Exit(90)
		}
		_, err = Run(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	case "fixture-managed-provider", "fixture-managed-backend":
		child := exec.Command(os.Args[0], "fixture-managed-fork", path)
		child.Env = nil
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if child.Run() != nil {
			os.Exit(92)
		}
		if mode == "fixture-managed-backend" {
			ln, err := net.Listen("unix", os.Args[3])
			if err != nil {
				os.Exit(93)
			}
			defer func() { _ = ln.Close() }()
			for {
				c, err := ln.Accept()
				if err != nil {
					os.Exit(94)
				}
				_ = c.Close()
			}
		}
		for {
			if _, err := os.Stat(filepath.Join(path, "finish")); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "fixture-managed-fork":
		child := exec.Command(os.Args[0], "fixture-managed-writer", path)
		child.Env = nil
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if child.Start() != nil {
			os.Exit(95)
		}
		os.Exit(0)
	case "fixture-managed-writer":
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
		start, _ := procstart.StartTime(os.Getpid())
		raw, _ := json.Marshal(processcontain.Identity{PID: os.Getpid(), StartTime: start})
		if os.WriteFile(filepath.Join(path, "writer.json"), raw, 0o600) != nil {
			os.Exit(96)
		}
		for {
			_ = os.WriteFile(filepath.Join(path, "writes"), []byte(time.Now().String()), 0o600)
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func managedFixture(t *testing.T, withBackend bool) (Config, *exec.Cmd, <-chan error, []processcontain.Identity) {
	t.Helper()
	base, err := os.MkdirTemp("", "sw-managed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	candidate, err := store.CreateCandidate(accounts.ProviderCodex, accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"containment-fixture","access_token":"synthetic-bearer","refresh_token":"synthetic-refresh"}}`)
	if err := os.WriteFile(filepath.Join(candidate.ProfilePath, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
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
	home, cwd, dir, native := filepath.Join(base, "home"), filepath.Join(base, "project"), filepath.Join(state, "session"), filepath.Join(base, "native")
	for _, p := range []string{home, cwd, dir, native} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exe := selfExe(t)
	argv := []string{exe, "fixture-managed-provider", native}
	projection, err := accountconfig.Prepare(state, accounts.ProviderCodex, candidate.ProfilePath, cwd, []string{"HOME=" + home}, argv, "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.CurrentBinding(account.ID, projection.Generation)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(exe)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{SessionID: "session", Argv: argv, Cwd: cwd, Env: projection.HarmlessEnv, SocketPath: filepath.Join(dir, "s"), SessionDir: dir, AccountBinding: &binding, AccountStateRoot: state, AccountProjectionRef: projection.Ref, CLIIdentity: &persist.CLIIdentity{Path: exe, Version: "0.160.0", Fingerprint: fingerprint}, Cols: 80, Rows: 24, GraceTimeout: 25 * time.Millisecond, TranscriptCfg: transcript.Config{MaxBytes: 1 << 20, MaxFiles: 1}}
	paths := []string{native}
	if withBackend {
		backend := filepath.Join(base, "backend")
		if err := os.Mkdir(backend, 0o700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(dir, "backend.sock")
		cfg.Backend = &BackendConfig{Program: exe, Args: []string{"fixture-managed-backend", backend, socket}, Env: projection.HarmlessEnv, SocketPath: socket, GoAheadTimeout: 20 * time.Millisecond, ReadyTimeout: 5 * time.Second}
		paths = append(paths, backend)
	}
	raw, _ = json.Marshal(cfg)
	config := filepath.Join(base, "config.json")
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "fixture-managed-shim", config)
	// Clear inherited SWARM_ controls and all account selectors; config supplies
	// only private synthetic profiles. No native/auth/model binary is invoked.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(key, "SWARM_") && key != "CODEX_HOME" && key != "CLAUDE_CONFIG_DIR" {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	children := []processcontain.Identity{}
	t.Cleanup(func() {
		for _, child := range children {
			_ = processcontain.SignalIdentity(child, syscall.SIGKILL)
		}
		_ = cmd.Process.Kill()
		var info NativeProcessInfo
		if raw, err := os.ReadFile(filepath.Join(dir, NativeProcessFile)); err == nil && json.Unmarshal(raw, &info) == nil {
			_ = processcontain.SignalIdentity(processcontain.Identity{PID: info.PID, StartTime: info.StartTime}, syscall.SIGKILL)
			_ = processcontain.SignalIdentity(processcontain.Identity{PID: info.BackendPID, StartTime: info.BackendStartTime}, syscall.SIGKILL)
		}
	})
	for _, path := range paths {
		deadline := time.Now().Add(10 * time.Second)
		for {
			var child processcontain.Identity
			raw, err := os.ReadFile(filepath.Join(path, "writer.json"))
			if err == nil && json.Unmarshal(raw, &child) == nil && child.Alive() {
				children = append(children, child)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("escaped fixture writer did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, NativeProcessFile)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed native identity not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cfg, cmd, done, children
}

func TestManagedShimContainsDetachedWritersBeforeDurableProof(t *testing.T) {
	for _, backend := range []bool{false, true} {
		for _, term := range []bool{false, true} {
			t.Run(fmt.Sprintf("backend=%v/term=%v", backend, term), func(t *testing.T) {
				cfg, cmd, done, children := managedFixture(t, backend)
				if term {
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(filepath.Join(cfg.AccountStateRoot, "..", "native", "finish"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(8 * time.Second):
					t.Fatal("managed shim did not finish")
				}
				var info NativeProcessInfo
				raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeProcessFile))
				if err != nil || json.Unmarshal(raw, &info) != nil {
					t.Fatal("missing native identity")
				}
				var proof NativeStoppedInfo
				raw, err = os.ReadFile(filepath.Join(cfg.SessionDir, NativeStoppedFile))
				if err != nil || json.Unmarshal(raw, &proof) != nil {
					t.Fatal("missing durable death proof")
				}
				if proof.SchemaVersion != ManagedWriterSchemaVersion || !proof.WritersStopped || proof.Native != info || info.Binding != *cfg.AccountBinding || len(info.Generation) != 32 {
					t.Fatal("proof does not match exact native writer incarnation")
				}
				for _, child := range children {
					if _, err := procstart.StartTime(child.PID); !os.IsNotExist(err) {
						t.Fatal("escaped writer was not reaped before proof")
					}
				}
			})
		}
	}
}

func TestManagedShimCrashNeverPublishesWriterDeathProof(t *testing.T) {
	cfg, cmd, done, _ := managedFixture(t, true)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture shim kill did not finish")
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionDir, NativeStoppedFile)); !os.IsNotExist(err) {
		t.Fatal("crashed shim published a writer-death proof")
	}
}
