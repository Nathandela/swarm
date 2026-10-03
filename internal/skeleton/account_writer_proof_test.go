package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func accountWriterProofFixture(t *testing.T) (*accountRotationManager, persist.Meta, shim.NativeProcessInfo) {
	t.Helper()
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(root, bindings[0])
	source.Status.Process = status.ProcessExited
	source.ShimPID, source.ShimStartTime = 1<<30+1, 1
	m, _ := rotationTestManager(t, store, root, source)
	native := shim.NativeProcessInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Generation: strings.Repeat("a", 32), PID: 1 << 30, PGID: 1 << 30, StartTime: 1, ShimPID: source.ShimPID, ShimStartTime: source.ShimStartTime, Binding: *source.AccountBinding}
	if err := os.Mkdir(filepath.Join(root, source.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAccountProofFixture(t, filepath.Join(root, source.ID, shim.NativeProcessFile), native)
	return m, source, native
}

func writeAccountProofFixture(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountStoppedWriterRequiresExactContainedProof(t *testing.T) {
	for _, kind := range []string{"missing", "legacy", "unconfirmed", "generation", "binding", "incident", "shim-live", "native-live", "backend-live", "unsafe", "partial-zero", "zero-valid", "valid"} {
		t.Run(kind, func(t *testing.T) {
			m, source, native := accountWriterProofFixture(t)
			proof := shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: native, WritersStopped: true}
			start, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "legacy":
				native.SchemaVersion = 1
				proof.Native = native
			case "unconfirmed":
				proof.WritersStopped = false
			case "generation":
				proof.Native.Generation = strings.Repeat("b", 32)
			case "binding":
				proof.Native.Binding.Identity = strings.Repeat("b", 64)
			case "incident":
				native.IncidentID = "foreign-incident"
				proof.Native = native
			case "shim-live":
				source.ShimPID, source.ShimStartTime = os.Getpid(), start
				native.ShimPID, native.ShimStartTime = source.ShimPID, source.ShimStartTime
				proof.Native = native
			case "native-live":
				native.PID, native.PGID, native.StartTime = os.Getpid(), os.Getpid(), start
				proof.Native = native
			case "backend-live":
				native.BackendPID, native.BackendStartTime = os.Getpid(), start
				proof.Native = native
			case "partial-zero":
				native.PID = 0
				proof.Native = native
			case "zero-valid":
				native.PID, native.PGID, native.StartTime = 0, 0, 0
				proof.Native = native
			}
			dir := filepath.Join(m.w.stateDir, source.ID)
			writeAccountProofFixture(t, filepath.Join(dir, shim.NativeProcessFile), native)
			if kind != "missing" {
				writeAccountProofFixture(t, filepath.Join(dir, shim.NativeStoppedFile), proof)
			}
			if kind == "unsafe" {
				path := filepath.Join(dir, shim.NativeStoppedFile)
				if err := os.Rename(path, path+".outside"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".outside", path); err != nil {
					t.Fatal(err)
				}
			}
			err = m.verifyStopped(source)
			if kind == "valid" || kind == "zero-valid" {
				if err != nil {
					t.Fatal("exact contained dead writers refused", err)
				}
			} else if !errors.Is(err, accounts.ErrInUse) {
				t.Fatal("missing, stale, unsafe or live writer custody permitted transfer", err)
			}
		})
	}
}

func TestAccountStoppedWriterIncludesRetainedCheckCustody(t *testing.T) {
	m, source, native := accountWriterProofFixture(t)
	writeAccountProofFixture(t, filepath.Join(m.w.stateDir, source.ID, shim.NativeStoppedFile), shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: native, WritersStopped: true})
	generation := strings.Repeat("c", 32)
	dir := filepath.Join(m.w.stateDir, "accounts", "checks", generation)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A check using another configuration still shares the credential writer.
	binding := *source.AccountBinding
	binding.ConfigurationGeneration++
	ref := accountcheck.Ref{SchemaVersion: accountcheck.SchemaVersion, Generation: generation, Binding: binding, Mode: accountcheck.ModeAuthStatus, Worker: processcontain.Identity{PID: 1<<30 + 2, StartTime: 1}}
	writeAccountProofFixture(t, filepath.Join(dir, accountcheck.WorkerFile), ref)
	if err := m.verifyStopped(source); !errors.Is(err, accounts.ErrInUse) {
		t.Fatal("dead probe without contained proof permitted transfer", err)
	}
	writeAccountProofFixture(t, filepath.Join(dir, accountcheck.StoppedFile), accountcheck.Stopped{SchemaVersion: accountcheck.SchemaVersion, Ref: ref, WritersStopped: true})
	if err := m.verifyStopped(source); err != nil {
		t.Fatal("exact stopped check custody refused", err)
	}
}

func TestAccountClaimedRecoveryReloadRequiresStoppedWriterProofBeforeHistory(t *testing.T) {
	m, source, native := accountWriterProofFixture(t)
	m.stopProof = m.verifyStopped
	binding := *source.AccountBinding
	accountTestRollout(t, m.store, binding, source.Cwd, "opaque native body\n")
	registry, err := m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var destination accounts.Binding
	for id := range registry.Accounts {
		if id != binding.AccountID {
			destination, err = m.store.CurrentBinding(id, 1)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := m.preflight(source, destination, accountHistoryOwnership{}, "contained-recovery")
	if err != nil {
		t.Fatal(err)
	}
	rec := accountRotationRecord{Incident: accounts.NewIncident("contained-recovery", source.AgentType, "exact-model", 2), OriginalSource: source.ID, SourceID: source.ID, SourceBinding: binding, Destination: &destination, Manifest: &manifest, ConversationID: source.ConversationID, State: accountClaimed}
	m.w.state.Killed[source.ID] = true
	if err := m.persist(rec); err != nil {
		t.Fatal(err)
	}
	called := 0
	m.preflight = func(persist.Meta, accounts.Binding, accountHistoryOwnership, string) (accountHistoryManifest, error) {
		called++
		return manifest, nil
	}
	loaded, err := loadAuthWatchStateChecked(m.w.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	m.w.state = loaded
	m.stepRecord(loaded.AccountRotations[source.ID])
	if called != 0 || !m.w.state.Killed[source.ID] || m.w.state.AccountRotations[source.ID].LastError != "native-writer-death-unconfirmed" {
		t.Fatal("restart invented death from missing PID or lost issued stop authority")
	}
	writeAccountProofFixture(t, filepath.Join(m.w.stateDir, source.ID, shim.NativeStoppedFile), shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: native, WritersStopped: true})
	// Existing claimed authority can reconcile once the exact shim proof arrives.
	if err := m.persist(rec); err != nil {
		t.Fatal(err)
	}
	m.stepRecord(rec)
	if called != 1 || m.w.state.AccountRotations[source.ID].State != accountStopped {
		t.Fatal("contained positive control did not reach history preflight")
	}
}
