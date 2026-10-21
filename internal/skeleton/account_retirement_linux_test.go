//go:build linux

package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAccountRetirementRescansPostFenceReservationAndRetries(t *testing.T) {
	m, f, source, _ := retirementFixture(t)
	profile, err := m.store.HistoryProfilePath(*source.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	pending := source
	pending.ID = "prefence-resolved-launch"
	pending.Status.Process = status.ProcessRunning
	pending.ShimPID, pending.ShimStartTime = 0, 0
	// The actual core reserves memory before finalization/credential resolution.
	// Simulate that already resolved admission becoming visible between scans.
	inject := true
	m.w.list = func() []persist.Meta {
		rows := f.list()
		r, err := m.store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if inject && r.Accounts[source.AccountBinding.AccountID].Generations[1].CredentialErasing {
			rows = append(rows, pending)
		}
		return rows
	}
	if _, err := m.retirementReferences(m.d.accounts, *source.AccountBinding, retirementGeneration(t, m, source)); err != nil {
		t.Fatalf("prescan unexpectedly held: %v", err)
	}
	m.stepRetirements()
	g := retirementGeneration(t, m, source)
	if !g.CredentialErasing || g.CredentialErased {
		t.Fatal("did not retain exact fence after post-fence reference")
	}
	if _, err := os.Stat(filepath.Join(profile, "auth.json")); err != nil {
		t.Fatal("erased after new reservation appeared")
	}
	if _, err := m.store.ResolveEnvironment(*source.AccountBinding, nil); !errors.Is(err, accounts.ErrIneligible) {
		t.Fatal("post-fence resolution admitted writer")
	}
	inject = false
	m.stepRetirements()
	g = retirementGeneration(t, m, source)
	if !g.CredentialErased || g.CredentialErasing {
		t.Fatal("fenced generation could not retry through history-only path")
	}
	view, err := m.d.accounts.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, account := range view.Accounts {
		if account.ID == source.AccountBinding.AccountID {
			found = true
			if !account.CredentialsErased || !account.Retiring {
				t.Fatal("retirement DTO does not reflect actual erasure")
			}
		}
	}
	if !found {
		t.Fatal("retained account disappeared")
	}
}

func TestAccountRetirementEndedHistoryRemains(t *testing.T) {
	m, f, source, _ := retirementFixture(t)
	profile, err := m.store.HistoryProfilePath(*source.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	rel := accountTestRollout(t, m.store, *source.AccountBinding, source.Cwd, "synthetic retained history\n")
	source.CLIIdentity = &persist.CLIIdentity{Path: "/synthetic-codex", Version: "0.160.0", Fingerprint: "synthetic"}
	f.add(source)
	if _, err := m.retirementReferences(m.d.accounts, *source.AccountBinding, retirementGeneration(t, m, source)); err != nil {
		t.Fatalf("valid prescan unexpectedly held: %v", err)
	}
	// Persisted supported identity independently provides the same writer version.
	m.stepRetirements()
	if g := retirementGeneration(t, m, source); !g.CredentialErased {
		t.Fatal("drained history reference prevented retirement")
	}
	if _, err := os.Stat(filepath.Join(profile, rel)); err != nil {
		t.Fatal("ended history erased")
	}
	if _, err := m.store.ProfilePath(*source.AccountBinding); !errors.Is(err, accounts.ErrIneligible) {
		t.Fatal("erased source remained executable")
	}
}

func TestAccountOwnerMoveFromErasedSourceUsesCanonicalFinalizer(t *testing.T) {
	x := feasibleAccountTest(t, 2)
	source := x.source
	source.Status.Process = status.ProcessExited
	source.ShimPID, source.ShimStartTime = 1<<30+2, 1
	x.f.add(source)
	native := shim.NativeProcessInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Generation: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ShimPID: source.ShimPID, ShimStartTime: source.ShimStartTime, Binding: *source.AccountBinding}
	if err := os.Mkdir(filepath.Join(x.root, source.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAccountProofFixture(t, filepath.Join(x.root, source.ID, shim.NativeProcessFile), native)
	writeAccountProofFixture(t, filepath.Join(x.root, source.ID, shim.NativeStoppedFile), shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: native, WritersStopped: true})
	x.m.stopProof = x.m.verifyStopped
	r, err := x.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, err = x.m.store.SetLifecycle(r.Revision, source.AccountBinding.AccountID, accounts.LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	r, err = x.m.store.BeginCredentialErasure(r.Revision, source.AccountBinding.AccountID, 1, "0.160.0")
	if err != nil {
		t.Fatal(err)
	}
	r, err = x.m.store.EraseCredentials(r.Revision, source.AccountBinding.AccountID, 1, accounts.ErasureProof{WritersStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	var destination accounts.Binding
	for id := range r.Accounts {
		if id != source.AccountBinding.AccountID {
			destination, err = x.m.store.CurrentBinding(id, source.AccountBinding.ConfigurationGeneration)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	result := make(chan error, 1)
	go func() { result <- x.m.RequestMove(source.ID, destination) }()
	op := <-x.m.w.managedOps
	op.done <- op.apply(x.m.w)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		x.m.step()
	}
	rec := x.m.w.state.AccountRotations[source.ID]
	if len(x.f.killed) != 0 || len(x.prepared) != 1 || len(x.f.launched) != 1 || rec.State != accountCommitted {
		t.Fatalf("erased source did not reach actual managed finalizer: state=%s err=%s prepared=%d launched=%d", rec.State, rec.LastError, len(x.prepared), len(x.f.launched))
	}
	prepared := x.prepared[0]
	if prepared.AccountBinding == nil || !sameCredentialGeneration(*prepared.AccountBinding, destination) || prepared.AccountProjectionRef != source.AccountProjectionRef || prepared.AccountNativeModel != "gpt-first-model" {
		t.Fatal("canonical move lost frozen destination/projection/model")
	}
	profile, err := x.m.store.HistoryProfilePath(destination)
	if err != nil {
		t.Fatal(err)
	}
	rel := accountTestRollout(t, x.m.store, *source.AccountBinding, source.AgentCwd, `{"type":"turn_context","payload":{"model":"gpt-first-model"}}`+"\n")
	if _, err := os.Stat(filepath.Join(profile, rel)); err != nil {
		t.Fatal("canonical destination history not published")
	}
	if _, err := x.m.store.ProfilePath(*source.AccountBinding); !errors.Is(err, accounts.ErrIneligible) {
		t.Fatal("source credentials resurrected during move")
	}
}

func TestAccountRetirementConsumesTerminalEnrollmentAfterErasure(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "historical-pruned"}[historical], func(t *testing.T) {
			m, _, source, _ := retirementFixture(t)
			job, _ := retiredEnrollmentFixture(t, m, source)
			if !historical {
				if err := m.d.accounts.saveJob(job); err != nil {
					t.Fatal(err)
				}
			}
			m.stepRetirements()
			if !retirementGeneration(t, m, source).CredentialErased {
				t.Fatal("exact enrollment proof prevented drained erasure")
			}
			if _, err := os.Stat(filepath.Join(m.w.stateDir, "accounts", "jobs", job.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("consumed terminal metadata retained")
			}
			if retained := m.d.accounts.jobs.Jobs[job.ID]; !retained.CustodyConsumed || retained.State != "admitted" {
				t.Fatal("directory removed before durable consumed authority")
			}
			raw, err := m.d.accounts.readJobs()
			if err != nil {
				t.Fatal(err)
			}
			var disk accountJobs
			if json.Unmarshal(raw, &disk) != nil || !disk.Jobs[job.ID].CustodyConsumed {
				t.Fatal("consumption did not survive restart")
			}
		})
	}
}
