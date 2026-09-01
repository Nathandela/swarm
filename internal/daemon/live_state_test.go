package daemon

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/status"
)

func TestLiveState_LifecycleRecordsCarryCompleteDerivedGroup(t *testing.T) {
	running := metaWith("s1", "claude", status.ProcessRunning, status.InteractionNone)
	cases := []struct {
		name       string
		prevExists bool
		prev       status.Status
		next       status.Status
		wantType   journal.RecordType
	}{
		{"launched", false, status.Status{}, running.Status, journal.TypeLaunched},
		{"exited", true, running.Status, status.Status{Process: status.ProcessExited}, journal.TypeExited},
		{"lost", true, running.Status, status.Status{Process: status.ProcessLost}, journal.TypeLost},
		{"group", true, running.Status, status.Status{Process: status.ProcessRunning, Interaction: status.InteractionPermission}, journal.TypeGroupTransition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev, next := running, running
			prev.Status, next.Status = tc.prev, tc.next
			rec, ok := journalRecordFor(prev, tc.prevExists, next)
			if !ok || rec.Type != tc.wantType {
				t.Fatalf("record = (%q,%v), want (%q,true)", rec.Type, ok, tc.wantType)
			}
			if want := status.Derive(next.Status); rec.Group != want {
				t.Fatalf("%s Group = %q, want complete derived group %q", rec.Type, rec.Group, want)
			}
		})
	}
}

func TestLiveState_RecordSessionStatePublishesCurrentCompleteRow(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := namedMeta("s1", "claude", "renamed", status.ProcessRunning, status.InteractionPermission)
	m.ShimPID = 41
	d.putMem(m)
	payload := json.RawMessage(`{"provider":"claude","session_instance":"i","structured_chat":true}`)
	matched, err := d.RecordSessionStateForIncarnation(m.ID, m.ShimPID, m.ShimStartTime, func() (json.RawMessage, error) { return payload, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("current incarnation was refused")
	}
	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(res.Events))
	}
	got := res.Events[0]
	if got.Type != journal.TypeSessionState || got.Group != status.GroupNeedsInput || got.Agent != "claude" || got.Name != "renamed" || string(got.Payload) != string(payload) {
		t.Fatalf("session state = %#v, want current complete row and exact payload", got)
	}
}

func TestLiveState_RecordSessionStateCannotResurrectTombstone(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := metaWith("s1", "claude", status.ProcessRunning, status.InteractionNone)
	d.putMem(m)
	d.tombstoneID(m.ID)
	before := d.journal.Cursor()
	matched, err := d.RecordSessionStateForIncarnation(m.ID, m.ShimPID, m.ShimStartTime, func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("tombstoned incarnation matched")
	}
	if after := d.journal.Cursor(); after != before {
		t.Fatalf("tombstoned state advanced journal %d -> %d", before, after)
	}
}

func TestLiveState_StaleIncarnationNeverAuthorsOrPublishes(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := metaWith("s1", "claude", status.ProcessRunning, status.InteractionNone)
	m.ShimPID = 202
	d.putMem(m)
	called := false
	before := d.journal.Cursor()
	matched, err := d.RecordSessionStateForIncarnation(m.ID, 101, m.ShimStartTime, func() (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"stale":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched || called {
		t.Fatalf("stale incarnation matched=%v author-called=%v", matched, called)
	}
	if after := d.journal.Cursor(); after != before {
		t.Fatalf("stale incarnation advanced journal %d -> %d", before, after)
	}
}

func TestLiveState_ReusedPIDWithDifferentStartNeverAuthorsOrPublishes(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := metaWith("s-reused", "claude", status.ProcessRunning, status.InteractionNone)
	m.ShimPID = 202
	m.ShimStartTime = 9002
	d.putMem(m)
	called := false
	before := d.journal.Cursor()
	matched, err := d.RecordSessionStateForIncarnation(m.ID, m.ShimPID, 9001, func() (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"stale":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched || called {
		t.Fatalf("reused-pid old start matched=%v author-called=%v", matched, called)
	}
	if after := d.journal.Cursor(); after != before {
		t.Fatalf("reused-pid stale incarnation advanced journal %d -> %d", before, after)
	}
}

func TestLiveState_SameGroupRenameEmitsCompleteSessionState(t *testing.T) {
	prev := namedMeta("s1", "claude", "old", status.ProcessRunning, status.InteractionNone)
	next := prev
	next.Name = "new"
	rec, ok := journalRecordFor(prev, true, next)
	if !ok {
		t.Fatal("same-group rename emitted no authoritative state delta")
	}
	if rec.Type != journal.TypeSessionState {
		t.Fatalf("type = %q, want %q", rec.Type, journal.TypeSessionState)
	}
	if rec.Group != status.Derive(next.Status) || rec.Agent != "claude" || rec.Name != "new" {
		t.Fatalf("session state = %#v, want complete group/agent/name", rec)
	}
}

func TestLiveState_SameGroupActivityTickDoesNotEmit(t *testing.T) {
	prev := metaWith("s1", "claude", status.ProcessRunning, status.InteractionNone)
	next := prev
	next.LastActivity = next.LastActivity.Add(1)
	if rec, ok := journalRecordFor(prev, true, next); ok {
		t.Fatalf("same-group activity tick emitted %#v", rec)
	}
}
