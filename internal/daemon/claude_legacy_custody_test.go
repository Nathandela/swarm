package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/status"
)

func legacyClaudeDaemonFixture(t *testing.T, floor int, edit func(*Config)) (*Daemon, persist.Meta) {
	t.Helper()
	d, m := retainedClaudeDaemonFixture(t, edit)
	m.SchemaVersion = persist.LoginSchemaVersion
	m.CLIIdentity = nil
	rt := *m.LoginRuntime
	rt.Home = filepath.Join(d.cfg.StateDir, "logins", "provider", "claude", "native")
	m.LoginRuntime = &rt
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(d.cfg.StateDir, "logins", "registry.json")
	raw, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["schema_version"], _ = json.Marshal(floor)
	raw, _ = json.Marshal(fields)
	if err := os.WriteFile(registry, raw, 0600); err != nil {
		t.Fatal(err)
	}
	d.putMem(m)
	return d, m
}

func TestClaudeLegacyOperationalCustodyRetainsUnknownLaunch(t *testing.T) {
	for _, floor := range []int{1, 9, logins.ClaudeProfileCustodySchemaVersion} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) { testClaudeLegacyRetainsUnknownLaunch(t, floor) })
	}
}

func testClaudeLegacyRetainsUnknownLaunch(t *testing.T, floor int) {
	for _, kind := range []string{"drop", "rollback", "delete", "replay", "sweep"} {
		t.Run(kind, func(t *testing.T) {
			callbacks := 0
			d, m := legacyClaudeDaemonFixture(t, floor, func(cfg *Config) {
				cfg.PreDelete = func(persist.Meta) error { callbacks++; return nil }
				cfg.OnSessionEnd = func(string) { callbacks++ }
			})
			const op = "legacy-operational"
			if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "delete":
				if err := d.Delete(m.ID); err == nil {
					t.Error("unknown legacy delete succeeded")
				}
			case "drop":
				d.dropReserved(m.ID)
			case "rollback":
				d.rollbackReserved(m.ID, m, true)
			case "replay":
				redrive, _, _, _, err := d.resolveReplay(op, "fresh")
				if redrive || !errors.Is(err, ErrLaunchOutcomeUnknown) {
					t.Errorf("operational legacy redrove: %v %v", redrive, err)
				}
			case "sweep":
				d.resolveStaleLaunches()
			}
			if callbacks != 0 {
				t.Fatal("legacy workspace cleanup preceded positive retirement")
			}
			if _, ok := d.Get(m.ID); !ok {
				t.Fatal("unknown legacy custody disappeared from roster")
			}
			rec, _ := d.idem.Get(op)
			if rec.SessionID != m.ID || rec.Phase != idempotency.PhasePrepared {
				t.Fatal("unknown legacy operation advanced")
			}
			if _, err := os.Stat(filepath.Join(d.cfg.StateDir, m.ID, "payload")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func claudeLegacyRecordedShim(t *testing.T) (int, int64) {
	t.Helper()
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	started, err := procstart.StartTime(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return child.Process.Pid, started
}

func TestClaudeLegacyModeNineKeepsHealthyReplayBehavior(t *testing.T) {
	for _, kind := range []string{"replay", "sweep"} {
		t.Run(kind, func(t *testing.T) {
			callbacks := 0
			d, m := legacyClaudeDaemonFixture(t, 9, func(cfg *Config) { cfg.PreDelete = func(persist.Meta) error { callbacks++; return nil } })
			if kind == "replay" || kind == "sweep" {
				m.ShimPID, m.ShimStartTime = claudeLegacyRecordedShim(t)
				m.Status.Process = status.ProcessRunning
				d.putMem(m)
			}
			const op = "legacy-nine"
			if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "replay":
				redrive, _, _, cached, err := d.resolveReplay(op, "fresh")
				if err != nil || redrive || cached.ID != m.ID {
					t.Fatalf("mode9 healthy replay changed: %v", err)
				}
			case "sweep":
				d.resolveStaleLaunches()
				rec, _ := d.idem.Get(op)
				if rec.Phase != idempotency.PhasePrepared {
					t.Fatal("mode9 healthy sweep changed")
				}
			}
		})
	}
}

func TestClaudeLegacyModeNineRetainsUnresolvedReplayBeforeSelection(t *testing.T) {
	for _, kind := range []string{"zero-shim-running", "zero-shim-lost", "positive-shim-lost"} {
		t.Run(kind, func(t *testing.T) {
			d, m := legacyClaudeDaemonFixture(t, 9, nil)
			switch kind {
			case "zero-shim-running":
				m.Status.Process = status.ProcessRunning
			case "positive-shim-lost":
				m.ShimPID, m.ShimStartTime = claudeLegacyRecordedShim(t)
			}
			d.putMem(m)
			assertClaudeUnresolvedReplayRetained(t, d, m)
		})
	}
}

func assertClaudeUnresolvedReplayRetained(t *testing.T, d *Daemon, m persist.Meta) {
	t.Helper()
	const op = "legacy-floor-transition"
	if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
		t.Fatal(err)
	}
	d.resolveStaleLaunches()
	rec, _ := d.idem.Get(op)
	if rec.Phase != idempotency.PhasePrepared || rec.SessionID != m.ID {
		t.Error("unresolved reservation advanced before replay")
	}
	redrive, stale, _, _, err := d.resolveReplay(op, "fresh")
	if redrive || stale != "" || !errors.Is(err, ErrLaunchOutcomeUnknown) {
		t.Errorf("unresolved reservation redrove: %v %q %v", redrive, stale, err)
	}
	rec, _ = d.idem.Get(op)
	if rec.Phase != idempotency.PhasePrepared || rec.SessionID != m.ID {
		t.Error("unresolved replay changed original operation")
	}
	if _, ok := d.Get(m.ID); !ok {
		t.Fatal("unresolved replay lost original reservation")
	}
}

func TestClaudeLegacyReplayRetainsAcrossActualOwnedSelection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("synthetic Claude Add must never write Darwin Keychain")
	}
	callbacks := 0
	d, m := legacyClaudeDaemonFixture(t, 9, func(cfg *Config) {
		cfg.PreDelete = func(persist.Meta) error { callbacks++; return nil }
		cfg.OnSessionEnd = func(string) { callbacks++ }
	})
	assertCleanupRetained := func() {
		t.Helper()
		if err := d.Delete(m.ID); err == nil {
			t.Error("unproven common reservation passed normal Delete")
		}
		if err := d.store.Delete(m.ID); err == nil {
			t.Error("unproven common reservation passed lower Delete")
		}
		if callbacks != 0 {
			t.Fatal("unproven common cleanup invoked destructive hook")
		}
		if got, err := os.ReadFile(filepath.Join(d.cfg.StateDir, m.ID, "payload")); err != nil || string(got) != "retained" {
			t.Fatal("unproven cleanup erased original payload")
		}
	}
	assertCleanupRetained()
	assertClaudeUnresolvedReplayRetained(t, d, m)
	s, err := logins.OpenExisting(d.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	r, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	// This test needs only actual durable selection, not a native/helper birth.
	grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-transition-access", "refreshToken": "synthetic-transition-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:profile", "user:inference"}}})
	r, login, err := s.Add(r.Revision, logins.ProviderClaude, "transition", logins.Credential{Auth: grant, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"transition","organizationUuid":"synthetic-org"},"userID":"transition"}`)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SelectClaudeProfile(r.Revision, login.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != logins.ClaudeProfileCustodySchemaVersion {
		t.Fatal("actual selection did not commit operational floor")
	}
	assertCleanupRetained()
	assertClaudeUnresolvedReplayRetained(t, d, m)
}

func TestClaudeLegacyCustodyQueryFailureNeverMeansUnowned(t *testing.T) {
	for _, kind := range []string{"drop", "rollback", "replay", "sweep"} {
		t.Run(kind, func(t *testing.T) {
			callbacks := 0
			d, m := legacyClaudeDaemonFixture(t, logins.ClaudeProfileCustodySchemaVersion, func(cfg *Config) { cfg.PreDelete = func(persist.Meta) error { callbacks++; return nil } })
			const op = "legacy-query-error"
			if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d.cfg.StateDir, "logins", "registry.json"), []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "drop":
				d.dropReserved(m.ID)
			case "rollback":
				d.rollbackReserved(m.ID, m, true)
			case "replay":
				redrive, _, _, _, err := d.resolveReplay(op, "fresh")
				if redrive || err == nil {
					t.Error("query error allowed replay")
				}
			case "sweep":
				d.resolveStaleLaunches()
			}
			if callbacks != 0 {
				t.Fatal("query error allowed workspace cleanup")
			}
			if _, ok := d.Get(m.ID); !ok {
				t.Fatal("query error dropped legacy custody")
			}
			rec, _ := d.idem.Get(op)
			if rec.SessionID != m.ID || rec.Phase != idempotency.PhasePrepared {
				t.Fatal("query error advanced legacy operation")
			}
		})
	}
}

func TestClaudeLegacyOriginalNoForkRetiresBeforeCleanup(t *testing.T) {
	for _, floor := range []int{1, 9, logins.ClaudeProfileCustodySchemaVersion} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) { testClaudeLegacyOriginalNoForkRetiresBeforeCleanup(t, floor) })
	}
}

func testClaudeLegacyOriginalNoForkRetiresBeforeCleanup(t *testing.T, floor int) {
	var freshID string
	calls := 0
	want := errors.New("original common launch refused before fork")
	d, m := legacyClaudeDaemonFixture(t, floor, func(cfg *Config) {
		cfg.PreLaunch = func(id string, _ LaunchSpec) (string, error) { freshID = id; return "", nil }
		cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) { return spec, want }
		cfg.PreDelete = func(retired persist.Meta) error {
			calls++
			check, err := logins.OpenReadOnly(cfg.StateDir)
			if err != nil {
				return err
			}
			r, err := check.Snapshot()
			_ = check.Close()
			if err != nil {
				return err
			}
			if r.SchemaVersion != logins.ClaudeProfileCustodySchemaVersion {
				t.Fatal("workspace cleanup preceded durable operational floor")
			}
			if retired.ID != freshID || retired.SchemaVersion != persist.ClaudeSchemaVersion || retired.CLIIdentity != nil || retired.ClaudeRetirement == nil || retired.ClaudeRetirement.Source != logins.ClaudeRetirementNoFork || logins.IsClaudeProfileRuntime(cfg.StateDir, *retired.LoginRuntime) {
				t.Fatal("legacy original cleanup lacked exact terminal common marker")
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
				t.Fatal("legacy cleanup preceded durable marker")
			}
			return nil
		}
	})
	_, err := d.Launch(LaunchSpec{AgentType: logins.ProviderClaude, Argv: []string{"/synthetic/old-claude"}, Cwd: m.Cwd, LoginRuntime: m.LoginRuntime, Cols: 80, Rows: 24})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("legacy nofork cleanup: calls=%d err=%v", calls, err)
	}
	if _, ok := d.Get(freshID); ok {
		t.Fatal("legacy nofork cleanup retained fresh row")
	}
}

func TestClaudeLegacyStoppedPartialDeleteReopensWithCanonicalMarker(t *testing.T) {
	calls := 0
	d, m := legacyClaudeStoppedDaemonFixture(t, logins.ClaudeProfileCustodySchemaVersion, func(cfg *Config) {
		cfg.PreDelete = func(retired persist.Meta) error {
			calls++
			if retired.SchemaVersion != persist.ClaudeSchemaVersion || retired.ClaudeRetirement == nil || retired.ClaudeRetirement.Native.SchemaVersion != logins.NativeWriterSchemaVersion || retired.CLIIdentity != nil {
				t.Fatal("legacy native3 cleanup lost canonical marker/optional CLI")
			}
			return nil
		}
	})
	original := deleteSessionDirFn
	t.Cleanup(func() { deleteSessionDirFn = original })
	deleteSessionDirFn = func(current *Daemon, id string) error {
		for _, name := range []string{logins.NativeProcessFile, logins.NativeStoppedFile} {
			if err := os.Remove(filepath.Join(current.cfg.StateDir, id, name)); err != nil {
				return err
			}
		}
		return syscall.ENOSPC
	}
	if err := d.Delete(m.ID); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("partial deletion: %v", err)
	}
	retained, ok := d.Get(m.ID)
	if !ok || retained.SchemaVersion != persist.ClaudeSchemaVersion || retained.ClaudeRetirement == nil {
		t.Fatal("partial cleanup lost canonical live marker")
	}
	retained.Name = "metadata update after native proof removal"
	if err := d.saveMeta(retained); err != nil {
		t.Fatal(err)
	}
	retained, _ = d.Get(m.ID)
	if retained.SchemaVersion != persist.ClaudeSchemaVersion {
		t.Fatal("ordinary daemon save downgraded terminal common row")
	}
	deleteSessionDirFn = original
	cfg := d.cfg
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openDaemon(t, cfg)
	got, ok := reopened.Get(m.ID)
	if !ok || got.ClaudeRetirement == nil || *got.ClaudeRetirement != *retained.ClaudeRetirement {
		t.Fatal("reopen lost terminal common marker")
	}
	if err := reopened.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("cleanup retry calls=%d", calls)
	}
	if _, err := os.Lstat(filepath.Join(cfg.StateDir, m.ID)); !os.IsNotExist(err) {
		t.Fatal("exact legacy partial cleanup retry retained directory")
	}
}

func legacyClaudeStoppedDaemonFixture(t *testing.T, floor int, edit func(*Config)) (*Daemon, persist.Meta) {
	t.Helper()
	d, m := legacyClaudeDaemonFixture(t, floor, edit)
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	started, err := procstart.StartTime(child.Process.Pid)
	_ = child.Process.Kill()
	_ = child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	m.ShimPID, m.ShimStartTime = child.Process.Pid, started
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	d.putMem(m)
	native := logins.NativeProcessInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Generation: strings.Repeat("f", 32), Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime, ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime}
	stopped := logins.NativeStoppedInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
	for name, value := range map[string]any{logins.NativeProcessFile: native, logins.NativeStoppedFile: stopped} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return d, m
}

func TestClaudeLegacyStoppedProofCannotBypassCorruptRegistry(t *testing.T) {
	for _, floor := range []int{1, 9, logins.ClaudeProfileCustodySchemaVersion} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) {
			callbacks := 0
			d, m := legacyClaudeStoppedDaemonFixture(t, floor, func(cfg *Config) {
				cfg.PreDelete = func(persist.Meta) error { callbacks++; return nil }
				cfg.OnSessionEnd = func(string) { callbacks++ }
			})
			if err := os.WriteFile(filepath.Join(d.cfg.StateDir, "logins", "registry.json"), []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			// Writer-stop proof is independent from authorization to mutate login state.
			if err := logins.VerifyClaudeNativeWritersStopped(d.cfg.StateDir, m.ID); err != nil {
				t.Fatalf("exact native3 stop fixture: %v", err)
			}
			before, err := os.ReadFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Delete(m.ID); err == nil {
				t.Error("corrupt registry authorized normal cleanup")
			}
			if err := d.store.Delete(m.ID); err == nil {
				t.Error("corrupt registry authorized lower cleanup")
			}
			if callbacks != 0 {
				t.Fatal("corrupt registry reached destructive hooks")
			}
			if got, err := os.ReadFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json")); err != nil || string(got) != string(before) {
				t.Fatal("corrupt registry changed exact reservation")
			}
			if got, err := os.ReadFile(filepath.Join(d.cfg.StateDir, m.ID, "payload")); err != nil || string(got) != "retained" {
				t.Fatal("corrupt registry erased payload")
			}
		})
	}
}
