package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAccountReservationFailurePreservesDurableTriedAccounts(t *testing.T) {
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(root, bindings[0])
	accountTestRollout(t, store, bindings[0], root, "opaque history\n")
	m, _ := rotationTestManager(t, store, root, source)
	if err := m.reportFailure(m.w, source.ID, "quota", "", "reserve-failure"); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(m.w.state.AccountRotations[source.ID])
	m.w.writeState = func(string, []byte) (bool, error) { return false, errors.New("before rename") }
	m.step()
	after, _ := json.Marshal(m.w.state.AccountRotations[source.ID])
	if string(before) != string(after) {
		t.Fatal("failed reservation changed authoritative memory")
	}
	loaded, err := loadAuthWatchStateChecked(root)
	if err != nil || len(loaded.AccountRotations[source.ID].Incident.TriedAccounts) != 1 {
		t.Fatal("failed reservation changed durable tried accounts", err)
	}
	m.w.writeState = nil
	m.step()
	if m.w.state.AccountRotations[source.ID].State != accountReserved {
		t.Fatal("failed reservation consumed the candidate and prevented retry")
	}
}

func TestAccountCommitVisibilityStagesHistoryAndLeaseTogether(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprint(visible), func(t *testing.T) {
			store, root, bindings := accountTestStore(t, 2)
			source := accountTestSource(root, bindings[0])
			accountTestRollout(t, store, bindings[0], root, "opaque history\n")
			m, _ := rotationTestManager(t, store, root, source)
			if err := m.reportFailure(m.w, source.ID, "quota", "", "commit-failure"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 8 && m.w.state.AccountRotations[source.ID].State != accountLaunched; i++ {
				m.step()
			}
			before, _ := json.Marshal(m.w.state)
			releases := 0
			m.release = func(string, string) error { releases++; return nil }
			m.w.writeState = func(path string, raw []byte) (bool, error) {
				if visible {
					return writeAuthWatchStateWithOps(path, raw, authWatchStateWriteOps{syncDir: func(string) error { return errors.New("after rename") }})
				}
				return false, errors.New("before rename")
			}
			m.step()
			if releases != 0 {
				t.Fatal("uncertain commit authorized input release")
			}
			loaded, err := loadAuthWatchStateChecked(root)
			if err != nil {
				t.Fatal("commit failure made the journal unreadable", err)
			}
			if visible {
				if loaded.AccountRotations[source.ID].State != accountCommitted || len(loaded.AccountHistoryOwnership) != 1 || len(m.w.state.AccountHistoryOwnership) != 1 {
					t.Fatal("visible commit lost its retained ownership obligation")
				}
			} else {
				after, _ := json.Marshal(m.w.state)
				if string(before) != string(after) || len(loaded.AccountHistoryOwnership) != 0 {
					t.Fatal("failed commit changed history ownership or trial lease")
				}
			}
			m.w.writeState = nil
			m.step()
			if releases != 1 || m.w.state.AccountRotations[source.ID].State != accountCommitted {
				t.Fatal("durable retry failed to reconcile the commit")
			}
		})
	}
}

func TestAccountRecoveryNeverAdoptsCanonicalOwnerResume(t *testing.T) {
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(root, bindings[0])
	accountTestRollout(t, store, bindings[0], root, "opaque history\n")
	m, fake := rotationTestManager(t, store, root, source)
	if err := m.reportFailure(m.w, source.ID, "quota", "", "owner-resume"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && m.w.state.AccountRotations[source.ID].State != accountPrepared; i++ {
		m.step()
	}
	owner := source
	owner.ID, owner.ResumedFrom = "owner-manual-resume", source.ID
	owner.InputEmbargo = ""
	owner.Status.Process = status.ProcessRunning
	m.w.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		fake.add(owner)
		result, ok := runningConversation(m.w.list(), source)
		if !ok || result.ID != owner.ID {
			t.Fatal("canonical dedup did not return the owner resume")
		}
		return result, nil
	}
	for i := 0; i < 4; i++ {
		m.step()
	}
	rec := m.w.state.AccountRotations[source.ID]
	if rec.CandidateID != "" || rec.LastError != "successor-launch-not-owned" || rec.Incident.SpawnCount != 1 {
		t.Fatal("unowned launch was adopted or spent spawn budget was restored")
	}
	for _, id := range fake.killed {
		if id == owner.ID {
			t.Fatal("recovery killed the owner's canonical resume")
		}
	}
	loaded, err := loadAuthWatchStateChecked(root)
	if err != nil || loaded.AccountRotations[source.ID].CandidateID != "" {
		t.Fatal("unowned result entered the durable recovery lineage", err)
	}
}

func TestAccountModelCapacityFailurePreservesReadableJournal(t *testing.T) {
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(root, bindings[0])
	m, _ := rotationTestManager(t, store, root, source)
	m.w.state.AccountSchemaVersion = 1
	for i := 0; i < 4096; i++ {
		m.w.state.AccountModels[fmt.Sprintf("model-session-%d", i)] = accountModelRecord{Binding: bindings[0], Model: "exact-model"}
	}
	if err := m.w.saveState(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, authWatchStateFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.noteModel(source, "new-exact-model"); err == nil {
		t.Fatal("model inventory exceeded the readable journal bound")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) || len(m.w.state.AccountModels) != 4096 {
		t.Fatal("capacity failure changed the last valid document or memory")
	}
	if _, err := loadAuthWatchStateChecked(root); err != nil {
		t.Fatal("capacity failure froze the journal on restart", err)
	}
}
