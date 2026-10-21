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
	for _, kind := range []string{"healthy", "pending", "missing-proof", "unknown-check", "orphan-custody", "malformed-meta", "half-open", "enrollment"} {
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
			case "enrollment":
				m.w.state.CLI = map[string]cliRefreshRecord{"legacy-enrollment": {Enrollment: &accountEnrollmentPlan{Binding: *source.AccountBinding}, State: cliRefreshPending}}
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

func TestAccountRetirementPrescanAllowsDrainedWriters(t *testing.T) {
	m, _, source, _ := retirementFixture(t)
	if _, err := m.retirementReferences(m.d.accounts, *source.AccountBinding, retirementGeneration(t, m, source)); err != nil {
		t.Fatalf("drained source unexpectedly held: %v", err)
	}
}

func TestAccountRetirementUnknownVersionHolds(t *testing.T) {
	m, f, source, _ := retirementFixture(t)
	source.CLIIdentity = nil
	f.add(source)
	m.stepRetirements()
	if g := retirementGeneration(t, m, source); g.CredentialErasing || g.CredentialErased {
		t.Fatal("unknown native version authorized inventory")
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
