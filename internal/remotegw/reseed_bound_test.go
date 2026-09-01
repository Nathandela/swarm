package remotegw

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestBoundJournalReseedPreservesRosterFinalCursorAndUnresolvedApprovals(t *testing.T) {
	const pendingID = "approval-old"
	pending := protocol.JournalRecord{
		Cursor: 1, SessionID: "machine/s1", Type: "interaction",
		Item: json.RawMessage(`{"v":1,"item_id":"` + pendingID + `","kind":"approval_request","summary":"allow?"}`),
	}
	resolved := protocol.JournalRecord{
		Cursor: 2, SessionID: "machine/s2", Type: "interaction",
		Item: json.RawMessage(`{"v":1,"item_id":"resolved-event","kind":"approval_request"}`),
	}
	resolution := protocol.JournalRecord{
		Cursor: 3, SessionID: "machine/s2", Type: "interaction",
		Item: json.RawMessage(`{"v":1,"item_id":"resolution","kind":"approval_resolved","interaction_id":"resolved-event"}`),
	}
	events := []protocol.JournalRecord{pending, resolved, resolution}
	largeItem := json.RawMessage(`{"v":1,"item_id":"tail","kind":"agent_message","text":"` + strings.Repeat("x", 16_000) + `"}`)
	for cursor := uint64(4); cursor <= 100; cursor++ {
		rec := protocol.JournalRecord{Cursor: cursor, SessionID: "machine/s1", Type: "interaction", Item: largeItem}
		events = append(events, rec)
	}
	rs := protocol.JournalReseed{
		Roster: []protocol.JournalRecord{{SessionID: "machine/s1", Type: "roster"}},
		Events: events, Cursor: 100,
	}

	got, err := boundJournalReseed(rs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cursor != rs.Cursor || len(got.Roster) != 1 || got.Roster[0].SessionID != "machine/s1" {
		t.Fatalf("bounded reseed lost authority: cursor=%d roster=%#v", got.Cursor, got.Roster)
	}
	if len(got.Events) >= len(events) {
		t.Fatalf("bounded reseed kept %d/%d events", len(got.Events), len(events))
	}
	seenPending := false
	seenNewest := false
	seenResolvedRequest := false
	for _, rec := range got.Events {
		if rec.Cursor == pending.Cursor {
			seenPending = true
		}
		if rec.Cursor == 100 {
			seenNewest = true
		}
		if rec.Cursor == resolved.Cursor {
			seenResolvedRequest = true
		}
	}
	if !seenPending || !seenNewest {
		t.Fatalf("bounded events lost pending approval/newest tail: pending=%t newest=%t", seenPending, seenNewest)
	}
	if seenResolvedRequest {
		t.Fatal("resolved old approval was treated as mandatory outside the newest tail")
	}
	body, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: got})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxJournalReseedPlaintextBytes {
		t.Fatalf("bounded reseed = %d bytes, limit %d", len(body), maxJournalReseedPlaintextBytes)
	}
}
