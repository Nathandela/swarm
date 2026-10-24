package shim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/shimwire"
)

// Managed-native custody belongs to a dedicated process, never the test runner.
func init() {
	if len(os.Args) < 3 {
		return
	}
	switch os.Args[1] {
	case "fixture-external-auth-agent":
		if os.WriteFile(os.Args[2], []byte("started"), 0600) != nil {
			os.Exit(93)
		}
		os.Exit(0)
	case "fixture-external-auth-shim":
		raw, err := os.ReadFile(os.Args[2])
		var cfg Config
		if err != nil || json.Unmarshal(raw, &cfg) != nil {
			os.Exit(94)
		}
		if len(os.Args) > 3 {
			testHookBeforeBackendSpawn = func(srv *server) {
				var args []string
				switch os.Args[3] {
				case "success":
					args = []string{"--remote", "unix://" + cfg.Backend.SocketPath}
				case "wrongremote":
					args = []string{"--remote", "unix:///wrong"}
				}
				srv.noteGoAhead(shimwire.Control{Type: shimwire.TypeBackendAttach, AgentArgs: args})
			}
		}
		_, err = Run(cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(95)
		}
		os.Exit(0)
	}
}

func TestExternalAuthBackendNeverStartsStandalone(t *testing.T) {
	for _, mode := range []string{"no_backend", "not_ready", "readiness_timeout", "record_failure", "timeout", "empty_attach", "wrongremote", "success"} {
		t.Run(mode, func(t *testing.T) {
			cfg := r7BackendCfg(t, r7BackendBind, nil)
			cfg.GraceTimeout = 25 * time.Millisecond
			cfg.Backend.ReadyTimeout = 3 * time.Second
			cfg.Backend.GoAheadTimeout = 40 * time.Millisecond
			cfg.LoginRuntime, cfg.LoginStateRoot = externalAuthRuntimeFixture(t)
			cfg.NativeProvider = logins.ProviderCodex
			marker := filepath.Join(cfg.SessionDir, "agent-started")
			cfg.Argv = []string{selfExe(t), "fixture-external-auth-agent", marker}
			home := t.TempDir()
			cfg.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			cfg.Backend.Env = append(cfg.Backend.Env, "HOME="+home)
			switch mode {
			case "no_backend":
				cfg.Backend = nil
			case "not_ready":
				cfg.Backend.Program = "/does/not/exist"
			case "readiness_timeout":
				cfg.Backend.ReadyTimeout = 40 * time.Millisecond
				cfg.Backend.Env[0] = r7BackendEnvVar + "=" + r7BackendNeverBinds
			case "record_failure":
				if err := os.Mkdir(filepath.Join(cfg.SessionDir, BackendFile), 0700); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(cfg)
			config := filepath.Join(cfg.SessionDir, "config.json")
			if err := os.WriteFile(config, raw, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"fixture-external-auth-shim", config}
			if mode == "empty_attach" || mode == "wrongremote" || mode == "success" {
				args = append(args, mode)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, selfExe(t), args...).CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("shim did not finish: %s", output)
			}
			_, started := os.Stat(marker)
			if mode == "success" {
				if err != nil || started != nil {
					t.Fatalf("authenticated join did not start agent: %v %s", err, output)
				}
			} else {
				if err == nil || !os.IsNotExist(started) {
					t.Fatalf("external-auth mode started standalone: err=%v marker=%v output=%s", err, started, output)
				}
			}
		})
	}
}

func externalAuthRuntimeFixture(t *testing.T) (*logins.Runtime, string) {
	t.Helper()
	state := t.TempDir()
	s, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "fixture-user", "chatgpt_account_id": "fixture-account"}})
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": "fixture-account", "access_token": jwt, "id_token": jwt, "refresh_token": "synthetic-refresh"}})
	r, _ := s.Snapshot()
	_, login, err := s.Add(r.Revision, logins.ProviderCodex, "fixture", logins.Credential{Auth: auth})
	if err != nil {
		t.Fatal(err)
	}
	r, _ = s.Snapshot()
	op, err := s.BeginSwitch(r.Revision, logins.ProviderCodex, login.ID, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	finish := func(migrate bool) {
		t.Helper()
		for _, phase := range []string{"stopping", "stopped"} {
			op.Phase = phase
			if err = s.SaveSwitch(op); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.CaptureOutgoing(op.ID); err != nil {
			t.Fatal(err)
		}
		if migrate {
			err = s.MigrateCodexOwnership(op.ID)
		} else {
			err = s.InstallIncoming(op.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.CommitSwitch(op.ID); err != nil {
			t.Fatal(err)
		}
		if err = s.CompleteSwitch(op.ID); err != nil {
			t.Fatal(err)
		}
	}
	finish(false)
	r, _ = s.Snapshot()
	op, err = s.BeginCodexOwnershipMigration(r.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	finish(true)
	rt, err := s.Runtime(logins.ProviderCodex)
	if err != nil || !rt.ExternalAuth {
		t.Fatalf("external fixture unavailable: %v", err)
	}
	return &rt, state
}
