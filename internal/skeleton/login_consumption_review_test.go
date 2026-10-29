package skeleton

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/protocol"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFinalSolCanceledReloginDoesNotReplayCompletedEnrollment(t *testing.T) {
	f := newLoginFixture(t)
	a := f.m.d.api
	c := loginworker.Config{SchemaVersion: 1, StateRoot: f.m.store.StateRoot(), JobID: "0123456789abcdef0123456789abcdef", Generation: 1, Provider: logins.ProviderCodex, Label: f.first.Label, LoginID: f.first.ID, Deadline: time.Now().Add(time.Minute)}
	home := loginworker.NativeHome(c.StateRoot, c.JobID)
	if e := os.MkdirAll(home, 0700); e != nil {
		t.Fatal(e)
	}
	write := func(name string, v any) {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(filepath.Dir(home), name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(loginworker.ConfigFile, c)
	write(loginworker.ProgressFile, loginworker.Progress{SchemaVersion: 1, JobID: c.JobID, Generation: 1, Phase: loginworker.PhaseReady, WritersStopped: true, NativeWritersStopped: true})
	credential := testLoginCredential("first")
	if e := os.WriteFile(filepath.Join(home, "auth.json"), credential.Auth, 0600); e != nil {
		t.Fatal(e)
	}
	r, e := f.m.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	// The daemon crashes after durable staging but before the completion receipt.
	if _, e = f.m.BeginReloginJob(r.Revision, c.JobID, c.LoginID, credential, "enrollment complete"); e != nil {
		t.Fatal(e)
	}
	r, e = f.m.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	// Owner cancels the untouched switch before the background job watcher retries.
	if _, e = a.Logins(protocol.LoginsReq{Action: "cancel_switch", ExpectedRevision: r.Revision, Provider: c.Provider}); e != nil {
		t.Fatal(e)
	}
	r, e = f.m.store.Snapshot()
	if e != nil || len(r.Switches) != 0 {
		t.Fatalf("cancel failed: %v %+v", e, r.Switches)
	}
	if done, err := loginworker.Completed(c); err != nil || !done {
		t.Fatalf("cancel did not preserve durable completion receipt: %v %v", done, err)
	}
	completeLoginFixture(t, a, c)
	if cleaned, err := loginworker.Cleaned(c); err != nil || !cleaned {
		t.Fatalf("canceled enrollment was not cleaned up: %v %v", cleaned, err)
	}
	r, e = f.m.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Switches) != 0 {
		t.Fatalf("background replay undid owner cancellation: %+v", r.Switches)
	}
}

func TestFinalSolRemovedEnrollmentDoesNotReappear(t *testing.T) {
	a, c, credential := readyLoginJob(t)
	old := filepath.Dir(loginworker.NativeHome(c.StateRoot, c.JobID))
	c.JobID = "9753124680abcdef9753124680abcdef"
	next := filepath.Dir(loginworker.NativeHome(c.StateRoot, c.JobID))
	if e := os.Rename(old, next); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c)
	if e := os.WriteFile(filepath.Join(next, loginworker.ConfigFile), raw, 0600); e != nil {
		t.Fatal(e)
	}
	progress := loginworker.Progress{SchemaVersion: 1, JobID: c.JobID, Generation: 1, Phase: loginworker.PhaseReady, WritersStopped: true, NativeWritersStopped: true}
	raw, _ = json.Marshal(progress)
	if e := os.WriteFile(filepath.Join(next, loginworker.ProgressFile), raw, 0600); e != nil {
		t.Fatal(e)
	}
	r, e := a.logins.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	// The daemon crashes after Import but before completion receipt persistence.
	r, _, e = a.logins.store.Import(r.Revision, logins.Login{ID: c.JobID, Provider: c.Provider, Label: c.Label, LastLoginJobID: c.JobID}, credential)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Logins(protocol.LoginsReq{Action: "remove", ExpectedRevision: r.Revision, LoginID: c.JobID}); e != nil {
		t.Fatal(e)
	}
	completeLoginFixture(t, a, c)
	r, e = a.logins.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Logins) != 0 {
		t.Fatalf("background completion resurrected owner-removed login: %+v", r.Logins)
	}
}
