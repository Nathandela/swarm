package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func retainedCheckFixture(t *testing.T, state string, binding accounts.Binding) accountcheck.Ref {
	t.Helper()
	path := filepath.Join(state, "accounts", "checks")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(binding.Provider + ":" + binding.AccountID + ":" + fmt.Sprint(binding.CredentialGeneration)))
	if err := os.WriteFile(filepath.Join(path, ".lock-"+hex.EncodeToString(key[:])), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ref := accountcheck.Ref{SchemaVersion: accountcheck.SchemaVersion, Generation: "0123456789abcdef0123456789abcdef", Worker: processcontain.Identity{PID: os.Getpid(), StartTime: start + 1}, Binding: binding, Mode: accountcheck.ModeAuthStatus}
	path = filepath.Join(path, ref.Generation)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{accountcheck.WorkerFile: ref, accountcheck.StoppedFile: accountcheck.Stopped{SchemaVersion: accountcheck.SchemaVersion, Ref: ref, WritersStopped: true}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ref
}

func TestAccountCheckCollectionUsesDurableAndMemoryPermitUnion(t *testing.T) {
	store, state, bindings := accountTestStore(t, 1)
	m, _ := rotationTestManager(t, store, state, accountTestSource(t, state, bindings[0]))
	ref := retainedCheckFixture(t, state, bindings[0])
	permit := accounts.HalfOpenPermit{OperationID: "check-operation", OwnerRequested: true, Model: "exact-model", Deadline: time.Now().Add(time.Minute), Spent: true, Stamp: accounts.ProbeStamp{Binding: bindings[0]}, WorkerPID: ref.Worker.PID, WorkerPGID: ref.Worker.PID, WorkerStartTime: ref.Worker.StartTime}
	m.w.state.AccountSchemaVersion = accounts.RecoverySchemaVersion
	m.w.state.AccountHalfOpen[bindings[0].AccountID] = permit
	if err := m.w.saveState(); err != nil {
		t.Fatal(err)
	}
	delete(m.w.state.AccountHalfOpen, bindings[0].AccountID)
	m.w.writeState = func(string, []byte) (bool, error) { return false, errors.New("synthetic pre-rename release failure") }
	if err := m.w.saveState(); err == nil {
		t.Fatal("release fault not exercised")
	}
	m.collectChecks()
	path := filepath.Join(state, "accounts", "checks", ref.Generation, accountcheck.StoppedFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("durable-only permit lost stopped proof", err)
	}
	// Fresh owner must still retain the persisted permit, even when the prior
	// actor optimistically removed it in memory before its write failed.
	fresh, _ := rotationTestManager(t, store, state, accountTestSource(t, state, bindings[0]))
	fresh.collectChecks()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("restart lost durable-only permit", err)
	}
	m.w.writeState = nil
	if err := m.w.saveState(); err != nil {
		t.Fatal(err)
	}
	// Conversely, new RAM-only admission must protect the whole generation
	// before a worker identity has been committed to the journal.
	permit.WorkerPID = 0
	permit.WorkerPGID = 0
	permit.WorkerStartTime = 0
	m.w.state.AccountHalfOpen[bindings[0].AccountID] = permit
	m.checkCollector.Close()
	m.collectChecks()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("zero-worker RAM reference lost custody", err)
	}
	delete(m.w.state.AccountHalfOpen, bindings[0].AccountID)
	m.checkCollector.Close()
	m.collectChecks()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("durably released check retained", err)
	}
	m.checkCollector.Close()
	fresh.checkCollector.Close()
}

func TestAccountCheckCollectionRefusesUnreadableJournal(t *testing.T) {
	store, state, bindings := accountTestStore(t, 1)
	m, _ := rotationTestManager(t, store, state, accountTestSource(t, state, bindings[0]))
	ref := retainedCheckFixture(t, state, bindings[0])
	if err := os.WriteFile(filepath.Join(state, authWatchStateFile), []byte(`{"account_schema_version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.collectChecks()
	if _, err := os.Stat(filepath.Join(state, "accounts", "checks", ref.Generation, accountcheck.StoppedFile)); err != nil {
		t.Fatal("unreadable reference authority collected", err)
	}
	if m.checkCollector != nil {
		t.Fatal("collector started without checked durable references")
	}
}
