package shim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/status"
)

func claudeClaimFixture(t *testing.T) (Config, *managedNativeScope, persist.Meta) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(state, "synthetic-cli")
	if err := os.WriteFile(image, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIContentFingerprint(image)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	rt := &logins.Runtime{Provider: logins.ProviderClaude, Epoch: 1, Identity: identity, Home: filepath.Join(state, "logins", "provider", "claude", "profiles", identity)}
	cli := &persist.CLIIdentity{Path: image, Version: "2.1.310", Fingerprint: fingerprint}
	cfg := Config{NativeProvider: logins.ProviderClaude, LoginStateRoot: state, SessionID: "owned", SessionDir: filepath.Join(state, "owned"), Cwd: "/project", Argv: []string{image}, LoginRuntime: rt, CLIIdentity: cli}
	if err := os.Mkdir(cfg.SessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	scope := &managedNativeScope{root: root, info: NativeProcessInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("b", 32), ShimPID: os.Getpid(), ShimStartTime: started, Provider: logins.ProviderClaude, Runtime: *rt, SessionID: cfg.SessionID, Cwd: cfg.Cwd, CLIFingerprint: fingerprint}}
	meta := persist.Meta{SchemaVersion: persist.ClaudeSchemaVersion, ID: cfg.SessionID, AgentType: logins.ProviderClaude, Cwd: cfg.Cwd, LoginRuntime: rt, CLIIdentity: cli, Status: status.Status{Process: status.ProcessRunning}}
	return cfg, scope, meta
}

func TestClaudeOwnedClaimRequiresExactPrivateUnretiredReservation(t *testing.T) {
	for _, kind := range []string{"valid", "cwd", "runtime", "cli", "terminal", "session", "foreign-shim", "retired", "duplicate", "case-alias", "symlink-meta", "winner-native", "winner-stopped"} {
		t.Run(kind, func(t *testing.T) {
			cfg, scope, meta := claudeClaimFixture(t)
			switch kind {
			case "cwd":
				meta.Cwd = "/foreign"
			case "runtime":
				bad := *meta.LoginRuntime
				bad.Epoch++
				meta.LoginRuntime = &bad
			case "cli":
				bad := *meta.CLIIdentity
				bad.Version = "0.0.0"
				meta.CLIIdentity = &bad
			case "terminal":
				meta.Status.Process = status.ProcessLost
			case "session":
				meta.ID = "foreign"
			case "foreign-shim":
				meta.ShimPID = os.Getpid() + 1
				meta.ShimStartTime = 1
			case "retired":
				meta.ClaudeRetirement = &logins.ClaudeReservationRetirement{Version: 1}
			}
			raw, _ := json.Marshal(meta)
			if kind == "duplicate" {
				raw = append(raw[:len(raw)-1], []byte(`,"schema_version":5}`)...)
			}
			if kind == "case-alias" {
				raw = []byte(strings.Replace(string(raw), `"schema_version"`, `"Schema_Version"`, 1))
			}
			metaPath := filepath.Join(cfg.SessionDir, "meta.json")
			if kind == "symlink-meta" {
				target := filepath.Join(cfg.LoginStateRoot, "foreign-meta")
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, metaPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(metaPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			winner := []byte("retained winner bytes")
			var winnerName string
			switch kind {
			case "winner-native":
				winnerName = NativeProcessFile
			case "winner-stopped":
				winnerName = NativeStoppedFile
			}
			if winnerName != "" {
				if err := os.WriteFile(filepath.Join(cfg.SessionDir, winnerName), winner, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := scope.claimClaudeReservation(cfg)
			if kind == "valid" {
				if err != nil || !scope.ownPreclaimDurable || scope.info.PID != 0 || scope.preclaim != scope.info {
					t.Fatalf("exact preclaim refused: %v", err)
				}
			} else {
				if err == nil || scope.ownPreclaimDurable {
					t.Fatal("invalid reservation obtained publication authority")
				}
				if err := scope.finish(); err != nil {
					t.Fatal(err)
				}
				if winnerName != "" {
					got, err := os.ReadFile(filepath.Join(cfg.SessionDir, winnerName))
					if err != nil || string(got) != string(winner) {
						t.Fatalf("refused preclaim changed winner: %v", err)
					}
				}
			}
		})
	}
}

func TestClaudeOwnedClaimCannotBeReusedOrReplaceAnotherGeneration(t *testing.T) {
	cfg, scope, meta := claudeClaimFixture(t)
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := scope.claimClaudeReservation(cfg); err != nil {
		t.Fatal(err)
	}
	winner, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeProcessFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.claimClaudeReservation(cfg); err == nil {
		t.Fatal("same claim reused")
	}
	root, err := os.OpenRoot(cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	other := &managedNativeScope{root: root, info: scope.info}
	other.info.Generation = strings.Repeat("c", 32)
	if err := other.claimClaudeReservation(cfg); err == nil || other.ownPreclaimDurable {
		t.Fatal("second generation replaced winner")
	}
	if err := other.finish(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeProcessFile))
	if err != nil || string(got) != string(winner) {
		t.Fatal("duplicate preclaim changed winner bytes")
	}
}

func TestClaudeOwnedClaimUncertainDirectorySyncNeverGrantsFinishAuthority(t *testing.T) {
	cfg, scope, meta := claudeClaimFixture(t)
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	original := nativeRecordSync
	t.Cleanup(func() { nativeRecordSync = original })
	nativeRecordSync = func(*os.Root) error { return syscall.ENOSPC }
	if err := scope.claimClaudeReservation(cfg); !errors.Is(err, syscall.ENOSPC) || scope.ownPreclaimDurable {
		t.Fatalf("uncertain sync granted authority: %v", err)
	}
	visible, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeProcessFile))
	if err != nil {
		t.Fatal(err)
	}
	nativeRecordSync = original
	if err := scope.finish(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeProcessFile))
	if err != nil || string(got) != string(visible) {
		t.Fatal("uncertain publication was replaced on finish")
	}
	if _, err := os.Lstat(filepath.Join(cfg.SessionDir, NativeStoppedFile)); !os.IsNotExist(err) {
		t.Fatal("uncertain preclaim published stop authority")
	}
}

func TestClaudeOwnedClaimFinishPreservesNativeSchemaFour(t *testing.T) {
	cfg, scope, meta := claudeClaimFixture(t)
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := scope.claimClaudeReservation(cfg); err != nil {
		t.Fatal(err)
	}
	if err := scope.finish(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, NativeStoppedFile))
	if err != nil {
		t.Fatal(err)
	}
	var stopped NativeStoppedInfo
	if err := json.Unmarshal(raw, &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.SchemaVersion != ClaudeNativeWriterSchemaVersion || stopped.Native != scope.preclaim {
		t.Fatal("owned finish downgraded or changed exact native wire scope")
	}
}

func TestClaudeOwnedRecordPreservesPublicationErrno(t *testing.T) {
	cfg, scope, meta := claudeClaimFixture(t)
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := scope.claimClaudeReservation(cfg); err != nil {
		t.Fatal(err)
	}
	original := nativeRecordSync
	t.Cleanup(func() { nativeRecordSync = original })
	nativeRecordSync = func(*os.Root) error { return syscall.ENOSPC }
	if err := scope.record(os.Getpid(), nil); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("native publication lost errno: %v", err)
	}
	if scope.recorded {
		t.Fatal("failed native publication became durable")
	}
}
