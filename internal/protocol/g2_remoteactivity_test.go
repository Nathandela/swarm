package protocol

// FAILING-FIRST (TDD RED, GG-5) for Wave G item G.2's wire hop:
// docs/specifications/chat-surface-plan.md §9, "`phone control` becomes `phone sent HH:mm`".
// Bead: agents-tracker-tbpm.9.
//
// WHY A NEW FIELD RATHER THAN A NEW READING OF THE OLD ONE. The board row has to state an
// EVENT -- what the phone did, and when -- because a bare noun in the marker column, beside
// `supervisor pending`, reads as a CONDITION, and "a phone is on this session" is exactly the
// presence claim plan G.5 rules out. An event needs an instant, and RemoteControlled is a
// bool: there is no reading of it that carries a time. The daemon has held the instant since
// Wave G (skeleton.phoneActivityAt, documented as an instant "so the terminal can draw it")
// and had no way to hand it over.
//
// SAMPLED AT STAMP TIME, exactly like RemoteControlled and SupervisionPending, from a source
// the assembly registers -- so this Server holds no clock, no horizon and no state of its own.
// The horizon is applied by the source, which is the daemon that also answers anyControlled;
// applying it here would let the row and ADR-010 Amendment 3 C3's gate disagree about one
// record.
//
// A POINTER, not a value: encoding/json does not omit a zero time.Time, so a value field
// would stamp "0001-01-01T00:00:00Z" onto every row that has never seen a phone -- new bytes
// on every frame of every roster, relayed verbatim by a released gateway that was promised
// none.

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestG2RemoteActivity_StampedOnListAndEvents pins that both roster surfaces carry the
// instant, that it tracks the registered source per SESSION, and that an unregistered source
// leaves every row empty.
func TestG2RemoteActivity_StampedOnListAndEvents(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(rcMeta("s1"), rcMeta("s2"))
	sock, srv := serveStubServer(t, stub)

	sent := time.Date(2026, 8, 26, 9, 41, 0, 0, time.UTC)
	var live atomic.Bool
	srv.SetRemoteActivityFunc(func(local string) time.Time {
		if live.Load() && local == "s1" {
			return sent
		}
		return time.Time{}
	})

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
	if rows["s1"].RemoteActivityAt != nil || rows["s2"].RemoteActivityAt != nil {
		t.Errorf("no phone has messaged either session, but the list stamped an instant: s1=%v s2=%v",
			rows["s1"].RemoteActivityAt, rows["s2"].RemoteActivityAt)
	}

	live.Store(true)
	rows = byLocal()
	if got := rows["s1"].RemoteActivityAt; got == nil || !got.Equal(sent) {
		t.Errorf("s1's list row carries %v, want the source's instant %v: the row is where the "+
			"terminal reads what to say, and it can say nothing a row does not carry", got, sent)
	}
	if rows["s2"].RemoteActivityAt != nil {
		t.Errorf("s2 has had no phone message but its list row is stamped %v", rows["s2"].RemoteActivityAt)
	}

	events, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	stub.pushStatus(rcMeta("s1"))
	select {
	case ev := <-events:
		if got := ev.Session.RemoteActivityAt; got == nil || !got.Equal(sent) {
			t.Errorf("the event stream carries %v for s1, want %v. The board redraws from events, "+
				"so a marker that only survives a full List would appear late or not at all", got, sent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event for s1 within 2s")
	}
	stub.pushStatus(rcMeta("s2"))
	select {
	case ev := <-events:
		if ev.Session.RemoteActivityAt != nil {
			t.Errorf("s2 has had no phone message but its event row is stamped %v", ev.Session.RemoteActivityAt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event for s2 within 2s")
	}
}

// TestG2RemoteActivity_NoSourceLeavesRowsEmpty: a Server with no registered source (no remote
// listener configured) must never claim a phone messaged anything.
func TestG2RemoteActivity_NoSourceLeavesRowsEmpty(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(rcMeta("s1"))
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)
	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, v := range views {
		if v.RemoteActivityAt != nil {
			t.Errorf("row %q stamped remote_activity_at %v with no registered source", v.ID, v.RemoteActivityAt)
		}
	}
}

// TestG2RemoteActivity_AbsentFromTheWireWhenThereIsNoInstant is the wire-compat assertion, and
// it is the reason the field is a POINTER. A zero time.Time is not empty to encoding/json: a
// value field would put "0001-01-01T00:00:00Z" on every row of every roster frame, which the
// released 0.8.0 gateway relays verbatim.
func TestG2RemoteActivity_AbsentFromTheWireWhenThereIsNoInstant(t *testing.T) {
	b, err := json.Marshal(SessionView{ID: "e/s1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "remote_activity_at") {
		t.Errorf("a row no phone has messaged put remote_activity_at on the wire: %s", b)
	}
	// And the guard that keeps it that way lives in stampView, so it is asserted there rather
	// than left to every future call site to remember: a zero instant becomes a NIL pointer,
	// never a pointer to a zero time.
	if v := stampView("e", persist.Meta{ID: "s1"}, status.GroupWorking, false, false, time.Time{}); v.RemoteActivityAt != nil {
		t.Errorf("stampView turned the zero instant into %v; a pointer to a zero time serializes "+
			"as \"0001-01-01T00:00:00Z\" and is exactly what the pointer exists to avoid", *v.RemoteActivityAt)
	}
	sentAt := time.Date(2026, 8, 26, 9, 41, 0, 0, time.UTC)
	if v := stampView("e", persist.Meta{ID: "s1"}, status.GroupWorking, false, false, sentAt); v.RemoteActivityAt == nil || !v.RemoteActivityAt.Equal(sentAt) {
		t.Errorf("stampView dropped a real instant: %v", v.RemoteActivityAt)
	}

	b, err = json.Marshal(SessionView{ID: "e/s1", RemoteActivityAt: &sentAt})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"remote_activity_at":"2026-08-26T09:41:00Z"`) {
		t.Errorf("a messaged row must carry its instant; got %s", b)
	}
}
