package protocol

import (
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestDiscussionProjectionListAndEvents(t *testing.T) {
	stub := newStubDaemon()
	a := persist.Meta{ID: "a", AgentType: "codex", CreatedAt: time.Unix(1, 0), Status: status.Status{Process: status.ProcessExited}}
	b := a
	b.ID, b.ResumedFrom, b.CreatedAt = "b", "a", time.Unix(2, 0)
	stub.setMetas(a, b)
	c := dialClient(t, serveStub(t, stub), nil)
	views, err := c.List()
	if err != nil || len(views) != 2 {
		t.Fatalf("list: %v, %v", views, err)
	}
	if views[0].ID != NamespacedID(c.EndpointID(), "a") || views[0].SupersededBy != NamespacedID(c.EndpointID(), "b") {
		t.Fatalf("raw list lost historical identity: %+v", views)
	}
	views = VisibleDiscussions(views)
	if len(views) != 1 {
		t.Fatalf("visible list: %+v", views)
	}
	wantHidden := []string{NamespacedID(c.EndpointID(), "a")}
	if views[0].ID != NamespacedID(c.EndpointID(), "b") || !reflect.DeepEqual(views[0].Supersedes, wantHidden) {
		t.Fatalf("list projection: %+v", views)
	}
	events, err := c.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	stub.events <- a
	select {
	case ev := <-events:
		if ev.Session.ID != NamespacedID(c.EndpointID(), "a") || ev.Session.SupersededBy != NamespacedID(c.EndpointID(), "b") {
			t.Fatalf("late event lost identity/projection: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no source event")
	}
	stub.setMetas(b) // old recovery deleted the source metadata
	stale := a
	stale.Status.Process = status.ProcessRunning
	stub.events <- stale
	select {
	case ev := <-events:
		if ev.Session.SupersededBy != NamespacedID(c.EndpointID(), "b") {
			t.Fatalf("deleted source running event resurrected: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no stale source event")
	}
	stub.events <- b
	select {
	case ev := <-events:
		if !reflect.DeepEqual(ev.Session.Supersedes, wantHidden) {
			t.Fatalf("missing ancestor: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no replacement event")
	}
	a.RosterHidden = true
	stub.setMetas(a) // representative deleted, only archived history remains
	stub.events <- stale
	select {
	case ev := <-events:
		if !ev.Session.RosterHidden {
			t.Fatalf("buffered event resurrected archived history: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no archived source event")
	}
	views, err = c.List()
	if err != nil || len(views) != 1 || len(VisibleDiscussions(views)) != 0 {
		t.Fatalf("archived raw/visible roster: %+v, %v", views, err)
	}
	if len(stub.List()) != 1 {
		t.Fatal("projection mutated source metadata")
	}
}
