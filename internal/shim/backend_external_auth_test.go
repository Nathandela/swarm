package shim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
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
			cfg.SessionID = "external"
			cfg.SessionDir = filepath.Join(cfg.LoginStateRoot, cfg.SessionID)
			if err := os.Mkdir(cfg.SessionDir, 0700); err != nil {
				t.Fatal(err)
			}
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
			reservation, err := persist.NewStore(cfg.LoginStateRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err = reservation.Save(persist.Meta{ID: cfg.SessionID, AgentType: logins.ProviderCodex, LoginRuntime: cfg.LoginRuntime}); err != nil {
				t.Fatal(err)
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

func externalAuthRuntimeFixture(t *testing.T, bootstrap ...bool) (*logins.Runtime, string) {
	withBootstrap := len(bootstrap) > 0 && bootstrap[0]
	t.Helper()
	state := t.TempDir()
	s, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
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
			if phase == "stopped" {
				for i := range op.Sessions {
					op.Sessions[i].Phase = "stopped"
				}
			}
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
		if migrate && withBootstrap {
			r, _ := s.Snapshot()
			r, h, e := s.TransferCodexMigration(r.Revision, op.ID, op.ToID, nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "birth-source", strings.Repeat("e", 32)); e != nil {
				t.Fatal(e)
			}
		} else if err = s.CompleteSwitch(op.ID); err != nil {
			t.Fatal(err)
		}
	}
	finish(false)
	r, _ = s.Snapshot()
	var rows []logins.Restart
	if withBootstrap {
		rows = []logins.Restart{{SourceID: "birth-source", ConversationID: "birth-thread"}}
	}
	op, err = s.BeginCodexOwnershipMigration(r.Revision, rows)
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

func TestExternalBirthInvalidReservationPreservesPriorCustodyProof(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.LoginRuntime, cfg.LoginStateRoot = externalAuthRuntimeFixture(t)
	cfg.NativeProvider = logins.ProviderCodex
	cfg.Argv = []string{selfExe(t), "fixture-external-auth-agent", filepath.Join(cfg.SessionDir, "unexpected-agent")}
	cfg.Backend.Program = "/does/not/exist"
	for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
		if err := os.WriteFile(filepath.Join(cfg.SessionDir, name), []byte("prior-exact-custody"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(cfg)
	config := filepath.Join(cfg.SessionDir, "config.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, selfExe(t), "fixture-external-auth-shim", config).CombinedOutput(); err == nil {
		t.Fatalf("invalid reservation started: %s", output)
	}
	for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
		raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, name))
		if err != nil || string(raw) != "prior-exact-custody" {
			t.Fatal("invalid birth destroyed prior native custody evidence")
		}
	}
}

func TestExternalBirthRechecksCodexDotenvBeforeLaunch(t *testing.T) {
	rt, state := externalAuthRuntimeFixture(t)
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.LoginRuntime, cfg.LoginStateRoot, cfg.NativeProvider = rt, state, logins.ProviderCodex
	cfg.Argv = []string{selfExe(t), "fixture-external-auth-agent", filepath.Join(t.TempDir(), "not-started")}
	if err := resolveLoginEnvironment(&cfg); err != nil {
		t.Fatalf("clean birth refused: %v", err)
	}
	for _, key := range []string{"OPENAI_FEDERATION_RULE_ID", "OPENAI_API_KEY", "OPENAI_ORGANIZATION", "OPENAI_PROJECT"} {
		if err := os.WriteFile(filepath.Join(rt.Home, ".env"), []byte("export "+key+"=synthetic-private-value\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := resolveLoginEnvironment(&cfg); err == nil {
			t.Fatalf("final shim resolution accepted late dotenv selector %s", key)
		}
	}
}

func TestExternalBirthHistoricalRuntimeResolvesAfterRetarget(t *testing.T) {
	rt, state := externalAuthRuntimeFixture(t)
	cfg := r7BackendCfg(t, r7BackendBind, nil)
	cfg.LoginRuntime, cfg.LoginStateRoot, cfg.NativeProvider = rt, state, logins.ProviderCodex
	cfg.SessionID = "external"
	cfg.SessionDir = filepath.Join(state, cfg.SessionID)
	if err := os.Mkdir(cfg.SessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Argv = []string{selfExe(t), "fixture-external-auth-agent", filepath.Join(cfg.SessionDir, "agent-started")}
	reservation, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = reservation.Save(persist.Meta{ID: cfg.SessionID, AgentType: logins.ProviderCodex, LoginRuntime: rt}); err != nil {
		t.Fatal(err)
	}
	s, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "next-user", "chatgpt_account_id": "next-account"}})
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": "next-account", "access_token": jwt, "id_token": jwt, "refresh_token": "synthetic-refresh"}})
	r, _ := s.Snapshot()
	r, next, err := s.Add(r.Revision, logins.ProviderCodex, "next", logins.Credential{Auth: auth})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.BeginCodexHandover(r.Revision, next.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.ValidateRuntime(*rt); err == nil {
		t.Fatal("historical runtime unexpectedly remained current")
	}
	if err = validateCodexExternalReservation(cfg); err != nil {
		t.Fatal(err)
	}
	if err = resolveLoginEnvironment(&cfg); err != nil {
		t.Fatalf("token-free birth could not follow current backend auth after retarget: %v", err)
	}
	if *cfg.LoginRuntime != *rt {
		t.Fatal("birth rewrote historical runtime metadata")
	}
}

func TestExternalBirthReservationRejectionPreservesCustody(t *testing.T) {
	for _, scenario := range []string{"bound_replay", "schema_alias", "duplicate", "runtime_alias", "wrong_cli", "wrong_source", "wrong_nonce", "wrong_conversation", "symlink_meta", "public_meta"} {
		t.Run(scenario, func(t *testing.T) {
			rt, state := externalAuthRuntimeFixture(t, true)
			cfg := r7BackendCfg(t, r7BackendBind, nil)
			cfg.LoginRuntime, cfg.LoginStateRoot, cfg.NativeProvider = rt, state, logins.ProviderCodex
			cfg.SessionID = "external"
			cfg.SessionDir = filepath.Join(state, cfg.SessionID)
			cfg.InputEmbargo = strings.Repeat("e", 32)
			if err := os.Mkdir(cfg.SessionDir, 0700); err != nil {
				t.Fatal(err)
			}
			cfg.Argv = []string{selfExe(t), "fixture-external-auth-agent", filepath.Join(cfg.SessionDir, "unexpected-agent")}
			cfg.Backend.Program = "/does/not/exist"
			meta := persist.Meta{ID: cfg.SessionID, AgentType: logins.ProviderCodex, LoginRuntime: rt, InputEmbargo: cfg.InputEmbargo, ResumedFrom: "birth-source", ConversationID: "birth-thread"}
			switch scenario {
			case "wrong_cli":
				meta.CLIIdentity = &persist.CLIIdentity{Path: selfExe(t), Version: "0.163.0", Fingerprint: "fixture"}
			case "wrong_source":
				meta.ResumedFrom = "other-source"
			case "wrong_nonce":
				meta.InputEmbargo = strings.Repeat("f", 32)
				cfg.InputEmbargo = meta.InputEmbargo
			case "wrong_conversation":
				meta.ConversationID = "other-thread"
			case "bound_replay":
				s, e := logins.Open(state)
				if e != nil {
					t.Fatal(e)
				}
				r, _ := s.Snapshot()
				h := r.Handovers[logins.ProviderCodex]
				n := logins.NativeMember{SessionID: cfg.SessionID, NativeIncarnation: "101:201", NativePID: 101, NativeStartTime: 201, ConversationID: meta.ConversationID}
				_, _, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, meta.ResumedFrom, cfg.InputEmbargo, n)
				_ = s.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			reservation, e := persist.NewStore(state)
			if e != nil {
				t.Fatal(e)
			}
			if e = reservation.Save(meta); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(cfg.SessionDir, "meta.json")
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			switch scenario {
			case "schema_alias":
				raw = []byte(strings.Replace(string(raw), fmt.Sprintf(`"schema_version":%d`, persist.LoginSchemaVersion), fmt.Sprintf(`"schema_version":1,"Schema_Version":%d`, persist.LoginSchemaVersion), 1))
			case "duplicate":
				raw = []byte(strings.Replace(string(raw), `"id":"external"`, `"id":"external","id":"external"`, 1))
			case "runtime_alias":
				raw = []byte(strings.Replace(string(raw), `"login_runtime":`, `"Login_Runtime":`, 1))
			}
			if e = os.WriteFile(path, raw, 0600); e != nil {
				t.Fatal(e)
			}
			if scenario == "symlink_meta" {
				target := filepath.Join(cfg.SessionDir, "meta-original")
				if e = os.Rename(path, target); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(target, path); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "public_meta" {
				if e = os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			}
			for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
				if e = os.WriteFile(filepath.Join(cfg.SessionDir, name), []byte("prior-exact-custody"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			raw, _ = json.Marshal(cfg)
			config := filepath.Join(cfg.SessionDir, "config.json")
			if e = os.WriteFile(config, raw, 0600); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if output, e := exec.CommandContext(ctx, selfExe(t), "fixture-external-auth-shim", config).CombinedOutput(); e == nil {
				t.Fatalf("rejected birth started: %s", output)
			}
			for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
				raw, e = os.ReadFile(filepath.Join(cfg.SessionDir, name))
				if e != nil || string(raw) != "prior-exact-custody" {
					t.Fatal("rejected birth destroyed original custody proof")
				}
			}
		})
	}
}
