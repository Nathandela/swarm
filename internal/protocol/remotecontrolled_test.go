package protocol

// agents-tracker-nx44.7 -- the roster badge's FIRST hop: SessionView carries
// RemoteControlled, stamped from the source the assembly registers with
// SetRemoteControlledFunc (production: the remote-tier Server's IsControlled), on
// BOTH roster surfaces -- the OpList snapshot and the OpEvent stream that
// distribute fans out.
//
// The field is omitempty on purpose: an uncontrolled row must serialize EXACTLY as
// it did before the field existed, so the released 0.8.0 gateway -- which relays
// these frames verbatim and is untouched by this change -- sees no new bytes.
//
// RED today: SessionView has no RemoteControlled field and Server has no
// SetRemoteControlledFunc, so this file does not compile.

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// rcMeta is one running session meta for the stamp assertions.
func rcMeta(id string) persist.Meta {
	return persist.Meta{
		ID: id, AgentType: "claude", Cwd: "/tmp",
		Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
	}
}

// TestRemoteControlled_StampedOnListAndEvents pins that both roster surfaces carry
// the flag, that it tracks the registered source per SESSION (not per roster), and
// that an unregistered source leaves every row false.
func TestRemoteControlled_StampedOnListAndEvents(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(rcMeta("s1"), rcMeta("s2"))
	sock, srv := serveStubServer(t, stub)

	var live atomic.Bool
	srv.SetRemoteControlledFunc(func(local string) bool { return live.Load() && local == "s1" })

	c := dialClient(t, sock, []string{"subscribe"})

	byLocal := func() map[string]SessionView {
		t.Helper()
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		out := map[string]SessionView{}
		for _, v := range views {
			_, local, ok := ParseID(v.ID)
			if !ok {
				t.Fatalf("roster row id %q is not namespaced", v.ID)
			}
			out[local] = v
		}
		return out
	}

	rows := byLocal()
	if rows["s1"].RemoteControlled || rows["s2"].RemoteControlled {
		t.Errorf("no remote controller yet, but list stamped remote_controlled: s1=%v s2=%v",
			rows["s1"].RemoteControlled, rows["s2"].RemoteControlled)
	}

	live.Store(true)
	rows = byLocal()
	if !rows["s1"].RemoteControlled {
		t.Errorf("s1 has a remote controller lease but its list row is not stamped")
	}
	if rows["s2"].RemoteControlled {
		t.Errorf("s2 has no remote controller lease but its list row is stamped")
	}

	events, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	stub.pushStatus(rcMeta("s1"))
	select {
	case ev := <-events:
		if !ev.Session.RemoteControlled {
			t.Errorf("the event stream dropped the remote-control flag for s1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event for s1 within 2s")
	}
	stub.pushStatus(rcMeta("s2"))
	select {
	case ev := <-events:
		if ev.Session.RemoteControlled {
			t.Errorf("s2 has no remote controller lease but its event row is stamped")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event for s2 within 2s")
	}
}

// TestRemoteControlled_NoSourceLeavesRowsFalse: a Server with no registered source
// (no remote listener configured) must never claim a session is remote-controlled.
func TestRemoteControlled_NoSourceLeavesRowsFalse(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(rcMeta("s1"))
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)
	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, v := range views {
		if v.RemoteControlled {
			t.Errorf("row %q stamped remote_controlled with no registered source", v.ID)
		}
	}
}

// TestRemoteControlled_AbsentFromTheWireWhenFalse is the wire-compat assertion: the
// field is omitempty, so an uncontrolled row's JSON is byte-identical to what a
// pre-nx44.7 daemon emitted. The 0.8.0 gateway relays these frames untouched.
func TestRemoteControlled_AbsentFromTheWireWhenFalse(t *testing.T) {
	b, err := json.Marshal(SessionView{ID: "e/s1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "remote_controlled") {
		t.Errorf("an uncontrolled row put remote_controlled on the wire: %s", b)
	}
	b, err = json.Marshal(SessionView{ID: "e/s1", RemoteControlled: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"remote_controlled":true`) {
		t.Errorf("a controlled row must carry remote_controlled:true; got %s", b)
	}
}
