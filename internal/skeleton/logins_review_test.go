package skeleton

import (
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"testing"
	"time"
)

func TestAstraFailedContinuationRemainsClosedToNewTurns(t *testing.T) {
	f := newLoginFixture(t)
	f.m.d.authw.ready = func(persist.Meta) bool { return false }
	if _, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 16; n++ {
		f.m.step()
		for id, row := range f.rows {
			if row.ResumedFrom != "" {
				row.CreatedAt = time.Now().Add(-3 * time.Minute)
				f.rows[id] = row
			}
		}
	}
	r, _ := f.m.store.Snapshot()
	if len(r.Switches) != 0 {
		t.Fatalf("did not reach completed failure: %+v", r.Switches)
	}
	for _, id := range []string{"child-1", "child-2"} {
		if f.rows[id].ResumeError == "" {
			t.Fatal("missing failure outcome")
		}
		if e := f.m.inputGate(id); e == nil {
			t.Fatalf("failed never-ready native continuation %s accepted a new turn after journal completion", id)
		}
	}
}

func TestProviderLoginActivateSelectedIsNoOp(t *testing.T) {
	f := newLoginFixture(t)
	before, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.BeginSwitch("codex", f.first.ID, "already selected"); err != nil {
		t.Fatal(err)
	}
	after, err := f.m.store.Snapshot()
	if err != nil || after.Revision != before.Revision || len(after.Switches) != 0 {
		t.Fatalf("same-login activation changed provider: %+v %v", after, err)
	}
}
