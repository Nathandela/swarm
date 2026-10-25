package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// This is retained reservation metadata, not a claimed authenticated account or
// native process. Zero PID deliberately supplies no retirement authority.
func retainedClaudeDaemonFixture(t *testing.T, edit func(*Config)) (*Daemon, persist.Meta) {
	t.Helper()
	cfg := daemonConfig(t)
	root, err := filepath.EvalSymlinks(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = root
	cfg.SocketPath = filepath.Join(root, "d.sock")
	cfg.LockPath = filepath.Join(root, "d.lock")
	cfg.LogPath = filepath.Join(root, "d.log")
	store, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// This metadata-only fixture has no account or grant. Set only the
	// existing registry schema floor; operational tests use public Select instead.
	registryPath := filepath.Join(root, "logins", "registry.json")
	registryRaw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registryFields map[string]json.RawMessage
	if err := json.Unmarshal(registryRaw, &registryFields); err != nil {
		t.Fatal(err)
	}
	registryFields["schema_version"], _ = json.Marshal(logins.ClaudeProfileCustodySchemaVersion)
	registryRaw, _ = json.Marshal(registryFields)
	if err := os.WriteFile(registryPath, registryRaw, 0600); err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	home, err := store.ClaudeProfileHome(identity)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{SchemaVersion: persist.ClaudeSchemaVersion, ID: "retained-owned", AgentType: logins.ProviderClaude, Cwd: cwd, CreatedAt: time.Now().UTC(), Status: status.Status{Process: status.ProcessLost, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, LoginRuntime: &logins.Runtime{Provider: logins.ProviderClaude, Epoch: 1, Identity: identity, Home: home}, CLIIdentity: &persist.CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}}
	dir := filepath.Join(root, m.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&cfg)
	}
	return openDaemon(t, cfg), m
}

func TestClaudeCustodyNormalDeleteRetiresBeforeDestructiveHooks(t *testing.T) {
	ended, predeleted := 0, 0
	d, m := retainedClaudeDaemonFixture(t, func(cfg *Config) {
		cfg.OnSessionEnd = func(string) { ended++ }
		cfg.PreDelete = func(persist.Meta) error { predeleted++; return nil }
	})
	if err := d.Delete(m.ID); err == nil {
		t.Fatal("unresolved reservation deleted")
	}
	if ended != 0 || predeleted != 0 {
		t.Fatalf("cleanup preceded retirement: OnSessionEnd=%d PreDelete=%d", ended, predeleted)
	}
	if _, ok := d.Get(m.ID); !ok {
		t.Fatal("unresolved reservation lost registry row")
	}
	if _, err := os.Stat(filepath.Join(d.cfg.StateDir, m.ID, "payload")); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCustodyRollbackRetainsUnresolvedReservationAndWorkspace(t *testing.T) {
	predeleted := 0
	d, m := retainedClaudeDaemonFixture(t, func(cfg *Config) { cfg.PreDelete = func(persist.Meta) error { predeleted++; return nil } })
	d.rollbackReserved(m.ID, m, true)
	if predeleted != 0 {
		t.Fatal("rollback removed workspace before retirement")
	}
	if _, ok := d.Get(m.ID); !ok {
		t.Fatal("rollback hid retained unresolved custody")
	}
	if _, err := os.Stat(filepath.Join(d.cfg.StateDir, m.ID, "payload")); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCustodyLostZeroPIDReplayDoesNotRedrive(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	const op = "owned-replay"
	if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
		t.Fatal(err)
	}
	redrive, _, _, _, err := d.resolveReplay(op, "fresh")
	if redrive || !errors.Is(err, ErrLaunchOutcomeUnknown) {
		t.Fatalf("lost owned reservation redrove: redrive=%v err=%v", redrive, err)
	}
	rec, _ := d.idem.Get(op)
	if rec.SessionID != m.ID {
		t.Fatal("replay repointed exact reservation")
	}
}

func TestClaudeCustodyStaleLaunchSweepRetainsUnresolvedOperation(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	const op = "owned-stale"
	if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
		t.Fatal(err)
	}
	d.resolveStaleLaunches()
	rec, _ := d.idem.Get(op)
	if rec.Phase != idempotency.PhasePrepared {
		t.Fatalf("unresolved owned launch became replayable: %s", rec.Phase)
	}
}

func TestClaudeCustodyDaemonSavePublishesCanonicalOwnedSchema(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	m.Name = "ordinary metadata update"
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	got, ok := d.Get(m.ID)
	if !ok || got.SchemaVersion != persist.ClaudeSchemaVersion {
		t.Fatalf("daemon published downgraded owned metadata: %d", got.SchemaVersion)
	}
	disk, err := d.store.Load(m.ID)
	if err != nil || disk.SchemaVersion != got.SchemaVersion {
		t.Fatalf("disk/memory schema mismatch: disk=%d memory=%d err=%v", disk.SchemaVersion, got.SchemaVersion, err)
	}
}

func TestClaudeCustodyDropReservedRetainsUnresolvedRow(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	d.dropReserved(m.ID)
	if _, ok := d.Get(m.ID); !ok {
		t.Fatal("dropReserved hid an unresolved owned reservation after lower Delete refused")
	}
	if _, err := os.Stat(filepath.Join(d.cfg.StateDir, m.ID, "payload")); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCustodyOriginalBeforeForkRollbackRetiresBeforeWorkspaceCleanup(t *testing.T) {
	workspace := t.TempDir()
	sentinel := filepath.Join(workspace, "created-by-this-launch")
	var freshID string
	cleaned := 0
	want := errors.New("synthetic refusal before fork")
	d, m := retainedClaudeDaemonFixture(t, func(cfg *Config) {
		cfg.PreLaunch = func(id string, _ LaunchSpec) (string, error) {
			freshID = id
			return workspace, os.WriteFile(sentinel, []byte("owned"), 0600)
		}
		cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) { return spec, want }
		cfg.PreDelete = func(retired persist.Meta) error {
			if retired.ID != freshID || retired.SchemaVersion != persist.ClaudeSchemaVersion || retired.ClaudeRetirement == nil || retired.ClaudeRetirement.Source != logins.ClaudeRetirementNoFork {
				t.Fatal("original cleanup has no exact durable no-fork retirement")
			}
			raw, err := os.ReadFile(filepath.Join(cfg.StateDir, retired.ID, "meta.json"))
			if err != nil {
				return err
			}
			var disk persist.Meta
			if err := json.Unmarshal(raw, &disk); err != nil {
				return err
			}
			if disk.ClaudeRetirement == nil || *disk.ClaudeRetirement != *retired.ClaudeRetirement {
				t.Fatal("cleanup preceded durable stamp")
			}
			lock, err := os.OpenFile(filepath.Join(cfg.StateDir, "logins", ".registry-lock"), os.O_RDWR, 0)
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				return err
			}
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			cleaned++
			return os.Remove(sentinel)
		}
	})
	before := len(d.List())
	_, err := d.Launch(LaunchSpec{AgentType: logins.ProviderClaude, Argv: []string{"/synthetic/claude"}, Cwd: m.Cwd, LoginRuntime: m.LoginRuntime, CLIIdentity: m.CLIIdentity, ClientEnv: []string{"PATH=/usr/bin:/bin"}, Cols: 80, Rows: 24})
	if !errors.Is(err, want) {
		t.Fatalf("wrong launch refusal: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("original no-fork cleanup calls=%d", cleaned)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatal("original before-fork workspace was retained")
	}
	if len(d.List()) != before {
		t.Fatal("retired original launch leaked reservation")
	}
	if _, err := os.Lstat(filepath.Join(d.cfg.StateDir, freshID)); !os.IsNotExist(err) {
		t.Fatal("retired original directory remains")
	}
}

func TestClaudeCustodyRollbackCleanupFailureRetainsStampForDeleteRetry(t *testing.T) {
	var freshID string
	cleanupCalls := 0
	want := errors.New("synthetic original before-fork refusal")
	d, m := retainedClaudeDaemonFixture(t, func(cfg *Config) {
		cfg.PreLaunch = func(id string, _ LaunchSpec) (string, error) { freshID = id; return "", nil }
		cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) { return spec, want }
		cfg.PreDelete = func(retired persist.Meta) error {
			if retired.ClaudeRetirement == nil {
				t.Fatal("cleanup without retained retirement")
			}
			cleanupCalls++
			if cleanupCalls == 1 {
				return errors.New("synthetic workspace cleanup failure")
			}
			return nil
		}
	})
	_, err := d.Launch(LaunchSpec{AgentType: logins.ProviderClaude, Argv: []string{"/synthetic/claude"}, Cwd: m.Cwd, LoginRuntime: m.LoginRuntime, CLIIdentity: m.CLIIdentity, ClientEnv: []string{"PATH=/usr/bin:/bin"}, Cols: 80, Rows: 24})
	if !errors.Is(err, want) {
		t.Fatalf("launch refusal: %v", err)
	}
	retained, ok := d.Get(freshID)
	if !ok || retained.ClaudeRetirement == nil {
		t.Fatal("failed rollback cleanup lost retained marker/row")
	}
	if err := d.Delete(freshID); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls != 2 {
		t.Fatalf("cleanup retry calls=%d", cleanupCalls)
	}
	if _, ok := d.Get(freshID); ok {
		t.Fatal("cleanup retry retained deleted row")
	}
}

func TestClaudeCustodyRetirementSnapshotsOwnTheirComparableValue(t *testing.T) {
	for _, kind := range []string{"retire-result", "get", "list"} {
		t.Run(kind, func(t *testing.T) {
			d, m := retainedClaudeDaemonFixture(t, nil)
			// Test owns this synthetic reservation and has never forked a shim for it.
			retired, err := d.retireClaudeReservation(m.ID, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			want := *retired.ClaudeRetirement
			switch kind {
			case "retire-result":
				retired.ClaudeRetirement.Version++
			case "get":
				snapshot, _ := d.Get(m.ID)
				snapshot.ClaudeRetirement.Version++
			case "list":
				snapshot := d.List()[0]
				snapshot.ClaudeRetirement.Version++
			}
			got, _ := d.Get(m.ID)
			if got.ClaudeRetirement == nil || *got.ClaudeRetirement != want {
				t.Fatal("caller mutated retained live authorization through a metadata snapshot")
			}
		})
	}
}
