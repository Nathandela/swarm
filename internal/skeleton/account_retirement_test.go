package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fmt"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"time"
)

func retirementFixture(t *testing.T) (*accountRotationManager, *authFake, persist.Meta, shim.NativeProcessInfo) {
	t.Helper()
	m, source, native := accountWriterProofFixture(t)
	proof := shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: native, WritersStopped: true}
	writeAccountProofFixture(t, filepath.Join(m.w.stateDir, source.ID, shim.NativeStoppedFile), proof)
	persistence, err := persist.NewStore(m.w.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Save(source); err != nil {
		t.Fatal(err)
	}
	jobsRoot, err := os.OpenRoot(filepath.Join(m.w.stateDir, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobsRoot.Close() })
	anchor, err := os.Lstat(filepath.Join(m.w.stateDir, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &accountManager{store: m.store, stateRoot: m.w.stateDir, jobsRoot: jobsRoot, jobsAnchor: anchor, jobs: accountJobs{SchemaVersion: accountJobsSchema, Jobs: make(map[string]accountJob)}, list: m.w.list}
	m.d = &Daemon{accounts: manager}
	fake := newAuthFake(source.AuthIdentity)
	fake.add(source)
	m.w.list, m.w.get = fake.list, fake.get
	m.stopProof = m.verifyStopped
	r, err := m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.SetLifecycle(r.Revision, source.AccountBinding.AccountID, accounts.LifecycleRetiring); err != nil {
		t.Fatal(err)
	}
	metas, err := m.retirementMetas()
	if err != nil {
		t.Fatalf("initial meta scan: %v", err)
	}
	for _, row := range metas {
		if row.AccountBinding != nil {
			if err := verifyAccountWritersStopped(m.w.stateDir, row); err != nil {
				t.Fatalf("initial proof %s: %v", row.ID, err)
			}
		}
	}
	if _, err := manager.retirementEnrollment(*source.AccountBinding, retirementGeneration(t, m, source)); err != nil {
		t.Fatalf("initial enrollment: %v", err)
	}
	return m, fake, source, native
}

func retirementGeneration(t *testing.T, m *accountRotationManager, source persist.Meta) accounts.Generation {
	t.Helper()
	r, err := m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return r.Accounts[source.AccountBinding.AccountID].Generations[source.AccountBinding.CredentialGeneration]
}

func TestAccountRetirementPrescanRetainsHealthyPendingUnknownWriters(t *testing.T) {
	for _, kind := range []string{"healthy", "pending", "missing-proof", "unknown-check", "orphan-custody", "malformed-meta", "half-open"} {
		t.Run(kind, func(t *testing.T) {
			m, f, source, _ := retirementFixture(t)
			profile, err := m.store.HistoryProfilePath(*source.AccountBinding)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "healthy":
				source.Status.Process = status.ProcessRunning
				f.add(source)
			case "pending":
				pending := source
				pending.ID = "reserved-launch"
				pending.Status.Process = status.ProcessRunning
				pending.ShimPID, pending.ShimStartTime = 0, 0
				f.add(pending)
			case "missing-proof":
				if err := os.Remove(filepath.Join(m.w.stateDir, source.ID, shim.NativeStoppedFile)); err != nil {
					t.Fatal(err)
				}
			case "unknown-check":
				if err := os.MkdirAll(filepath.Join(m.w.stateDir, "accounts", "checks", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "orphan-custody":
				if err := os.Remove(filepath.Join(m.w.stateDir, source.ID, "meta.json")); err != nil {
					t.Fatal(err)
				}
			case "malformed-meta":
				if err := os.WriteFile(filepath.Join(m.w.stateDir, source.ID, "meta.json"), []byte(`{"unreadable":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "half-open":
				m.w.state.AccountHalfOpen[source.AccountBinding.AccountID] = accounts.HalfOpenPermit{Stamp: accounts.ProbeStamp{Binding: *source.AccountBinding}}
			}
			m.stepRetirements()
			g := retirementGeneration(t, m, source)
			if g.CredentialErasing || g.CredentialErased {
				t.Fatalf("prescan fenced a retained writer: %s", kind)
			}
			if _, err := os.Stat(filepath.Join(profile, "auth.json")); err != nil {
				t.Fatal("credentials deleted before drained proof")
			}
		})
	}
}

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

func TestAccountRetirementEndedHistoryRemainsAndVersionUnknownHolds(t *testing.T) {
	m, f, source, _ := retirementFixture(t)
	profile, err := m.store.HistoryProfilePath(*source.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	rel := accountTestRollout(t, m.store, *source.AccountBinding, source.Cwd, "synthetic retained history\n")
	source.CLIIdentity = nil
	f.add(source)
	m.stepRetirements()
	if g := retirementGeneration(t, m, source); g.CredentialErasing || g.CredentialErased {
		t.Fatal("unknown native version authorized inventory")
	}
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

func retiredEnrollmentFixture(t *testing.T, m *accountRotationManager, source persist.Meta) (accountJob, enrollment.Config) {
	t.Helper()
	generation := retirementGeneration(t, m, source)
	profile, err := m.store.HistoryProfilePath(*source.AccountBinding)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(profile, ".swarm-candidate.json"))
	if err != nil {
		t.Fatal(err)
	}
	var candidate accounts.Candidate
	if json.Unmarshal(raw, &candidate) != nil {
		t.Fatal("candidate metadata unavailable")
	}
	job := accountJob{ID: candidate.ID, Generation: 1, Provider: source.AgentType, Method: enrollment.MethodDeviceCode, State: "admitted", Candidate: candidate, AdmittedAccountID: source.AccountBinding.AccountID, Worker: processcontain.Identity{PID: 1<<30 + 3, StartTime: 1}, Deadline: time.Now().Add(time.Minute)}
	cfg := enrollment.Config{SchemaVersion: enrollment.SchemaVersion, StateRoot: m.w.stateDir, JobID: job.ID, CandidateID: job.ID, CandidateProfileGeneration: generation.ProfileGeneration, Generation: job.Generation, Provider: job.Provider, Method: job.Method, NativePath: "/synthetic-never-executed", NativeVersion: "0.160.0", Deadline: job.Deadline}
	dir := filepath.Join(m.w.stateDir, "accounts", "jobs", job.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAccountProofFixture(t, filepath.Join(dir, enrollment.ConfigFile), cfg)
	writeAccountProofFixture(t, filepath.Join(dir, enrollment.ProgressFile), enrollment.Progress{SchemaVersion: enrollment.SchemaVersion, JobID: job.ID, Generation: job.Generation, Phase: enrollment.PhaseReady, Worker: job.Worker, WritersStopped: true, NativeWritersStopped: true})
	return job, cfg
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

func TestAccountRetirementUnknownEnrollmentRetainsProfileAndProof(t *testing.T) {
	m, _, source, _ := retirementFixture(t)
	job, _ := retiredEnrollmentFixture(t, m, source)
	if err := m.d.accounts.saveJob(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.w.stateDir, "accounts", "jobs", job.ID, enrollment.ProgressFile)); err != nil {
		t.Fatal(err)
	}
	m.stepRetirements()
	if g := retirementGeneration(t, m, source); g.CredentialErased || g.CredentialErasing {
		t.Fatal("missing enrollment proof allowed credential fence")
	}
	if _, err := os.Stat(filepath.Join(m.w.stateDir, "accounts", "jobs", job.ID, enrollment.ConfigFile)); err != nil {
		t.Fatal("unknown custody directory erased")
	}
}

func TestAccountEnrollmentPruneRetainsPartiallyConsumedDirectory(t *testing.T) {
	m, _, source, _ := retirementFixture(t)
	manager := m.d.accounts
	job, _ := retiredEnrollmentFixture(t, m, source)
	job.CustodyConsumed = true
	if err := manager.saveJob(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.w.stateDir, "accounts", "jobs", job.ID, enrollment.ConfigFile)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 129; i++ {
		id := fmt.Sprintf("%032x", i)
		other := job
		other.ID = id
		other.Candidate.ID = id
		other.Candidate.ProfileGeneration = id
		other.State = "cancelled"
		other.Worker = processcontain.Identity{}
		manager.jobs.Jobs[id] = other
	}
	if err := manager.pruneTerminalJobs(); err != nil {
		t.Fatal(err)
	}
	if _, found := manager.jobs.Jobs[job.ID]; !found {
		t.Fatal("prune lost only remaining incomplete cleanup intent")
	}
	manager.consumeTerminalEnrollment()
	if _, err := os.Stat(filepath.Join(m.w.stateDir, "accounts", "jobs", job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("durable intent could not finish partial metadata cleanup")
	}
}
