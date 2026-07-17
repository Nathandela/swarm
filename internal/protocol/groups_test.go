package protocol

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// E6.9 — status groups are computed DAEMON-SIDE via the shared status package;
// clients never re-derive. The mechanism pinned here: SessionView carries a
// precomputed Group field, filled by the server via status.Derive, that the
// client simply displays.

// TestGroups_SessionViewCarriesPrecomputedGroup asserts SessionView exposes a
// Group of type status.Group on the wire (a precomputed field, not something the
// client must compute).
func TestGroups_SessionViewCarriesPrecomputedGroup(t *testing.T) {
	f, ok := reflect.TypeOf(SessionView{}).FieldByName("Group")
	if !ok {
		t.Fatalf("SessionView has no Group field; the client would have to re-derive (violates E6.9)")
	}
	if f.Type != reflect.TypeOf(status.Group("")) {
		t.Fatalf("SessionView.Group type = %s, want status.Group", f.Type)
	}
	if tag := f.Tag.Get("json"); tag == "" || tag == "-" {
		t.Fatalf("SessionView.Group json tag = %q; the group must travel on the wire", tag)
	}
}

// TestGroups_ServerComputesGroupForEveryDimensionCombo asserts List returns a
// Group equal to status.Derive of the raw dimensions, across every derivation
// case — proving the server computed it and the client can trust the field.
func TestGroups_ServerComputesGroupForEveryDimensionCombo(t *testing.T) {
	cases := []persist.Meta{
		{ID: "n1", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission}},
		{ID: "n2", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPrompt}},
		{ID: "w1", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}},
		{ID: "w2", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}},
		{ID: "r1", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}},
		{ID: "c1", Status: status.Status{Process: status.ProcessExited}},
		{ID: "c2", Status: status.Status{Process: status.ProcessLost}},
	}
	stub := newStubDaemon()
	stub.setMetas(cases...)
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)

	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byLocal := map[string]SessionView{}
	for _, v := range views {
		_, local, _ := ParseID(v.ID)
		byLocal[local] = v
	}
	for _, m := range cases {
		v, ok := byLocal[m.ID]
		if !ok {
			t.Fatalf("session %q missing from list", m.ID)
		}
		want := status.Derive(m.Status)
		if v.Group != want {
			t.Errorf("session %q group = %q, want status.Derive => %q (server must compute it)", m.ID, v.Group, want)
		}
		if v.Status != m.Status {
			t.Errorf("session %q status dims = %+v, want %+v (raw dims travel alongside the group)", m.ID, v.Status, m.Status)
		}
	}
}

// TestGroups_EventsCarryServerComputedGroup asserts the subscribe path also
// carries the precomputed group, so a live status change needs no client-side
// derivation either.
func TestGroups_EventsCarryServerComputedGroup(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(statusMeta("sess1", status.TurnActive, status.InteractionNone))
	sock := serveStub(t, stub)
	c := dialClient(t, sock, []string{"subscribe"})
	ch, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// running + idle + none => ready_for_review.
	stub.pushStatus(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	ev, ok := recvEvent(t, ch, oneSecond)
	if !ok {
		t.Fatalf("no event delivered")
	}
	if ev.Session.Group != status.GroupReadyForReview {
		t.Errorf("event group = %q, want %q", ev.Session.Group, status.GroupReadyForReview)
	}
}
