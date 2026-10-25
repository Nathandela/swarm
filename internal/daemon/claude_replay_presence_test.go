package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
)

func TestClaudeCustodyReplayRetainsStateOmittedByScan(t *testing.T) {
	for _, kind := range []string{"malformed", "future", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read mode000 metadata")
			}
			d, m := retainedClaudeDaemonFixture(t, nil)
			const op = "omitted-roster"
			if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
				t.Fatal(err)
			}
			started, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			native := logins.NativeProcessInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("e", 32), ShimPID: os.Getpid(), ShimStartTime: started, Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime, SessionID: m.ID, Cwd: m.Cwd, CLIFingerprint: m.CLIIdentity.Fingerprint}
			proof, _ := json.Marshal(native)
			proofPath := filepath.Join(d.cfg.StateDir, m.ID, logins.NativeProcessFile)
			if err := os.WriteFile(proofPath, proof, 0600); err != nil {
				t.Fatal(err)
			}
			metaPath := filepath.Join(d.cfg.StateDir, m.ID, "meta.json")
			switch kind {
			case "malformed":
				if err := os.WriteFile(metaPath, []byte("not json"), 0600); err != nil {
					t.Fatal(err)
				}
			case "future":
				m.SchemaVersion = persist.ClaudeSchemaVersion + 1
				raw, _ := json.Marshal(m)
				if err := os.WriteFile(metaPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Chmod(metaPath, 0); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Chmod(metaPath, 0600) }()
			}
			cfg := d.cfg
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openDaemon(t, cfg)
			if _, ok := reopened.Get(m.ID); ok {
				t.Fatal("fixture metadata was not omitted by actual Scan")
			}
			rec, _ := reopened.idem.Get(op)
			if rec.Phase != idempotency.PhasePrepared {
				t.Error("restart failed an operation whose session directory remains")
			}
			redrive, _, _, _, err := reopened.resolveReplay(op, "fresh")
			if redrive || !errors.Is(err, ErrLaunchOutcomeUnknown) {
				t.Errorf("omitted roster redrove retained native state: %v %v", redrive, err)
			}
			rec, _ = reopened.idem.Get(op)
			if rec.SessionID != m.ID {
				t.Error("replay repointed retained operation")
			}
			got, err := os.ReadFile(proofPath)
			if err != nil || string(got) != string(proof) {
				t.Fatal("omitted native proof was changed")
			}
		})
	}
}

func TestClaudeCustodyReplayRetainsUncertainSessionLstat(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	const op = "uncertain-lstat"
	if _, _, err := d.idem.Prepare(op, "launch", m.ID); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	delete(d.sessions, m.ID)
	d.mu.Unlock()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(parent, m.ID)); err == nil || os.IsNotExist(err) {
		t.Fatal("fixture did not produce uncertain lstat")
	}
	originalState := d.cfg.StateDir
	d.cfg.StateDir = parent
	t.Cleanup(func() { d.cfg.StateDir = originalState })
	d.resolveStaleLaunches()
	rec, _ := d.idem.Get(op)
	if rec.Phase != idempotency.PhasePrepared {
		t.Error("uncertain lstat failed retained operation")
	}
	redrive, _, _, _, err := d.resolveReplay(op, "fresh")
	if redrive || !errors.Is(err, ErrLaunchOutcomeUnknown) {
		t.Fatalf("uncertain lstat redrove: %v %v", redrive, err)
	}
}

func TestClaudeCustodyReplayKeepsPriorBehaviorOnlyForAbsentDirectory(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const op = "truly-absent"
	if _, _, err := d.idem.Prepare(op, "launch", "absent"); err != nil {
		t.Fatal(err)
	}
	d.resolveStaleLaunches()
	rec, _ := d.idem.Get(op)
	if rec.Phase != idempotency.PhaseFailed {
		t.Fatal("true absent directory no longer resolves stale launch")
	}
	redrive, _, _, _, err := d.resolveReplay(op, "fresh")
	if err != nil || !redrive {
		t.Fatalf("true absent directory no longer redrives: %v", err)
	}
}
