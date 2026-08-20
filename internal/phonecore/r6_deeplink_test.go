package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's Mirror M3 deep-link resolution: a
// notification names `(session, item_id)`, and the tap must land on THAT item even after
// the machine restarted and reconciliation re-delivered the transcript at different
// cursors -- item identity is the item_id (IS-ENV-2 folds by it, never by position), so
// the resolver looks items up by id and answers an honest miss for an id the retained
// window no longer holds. Bead: agents-tracker-hggx.7.
//
// RED is undefined-only: ItemStore.Resolve does not exist.

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func r6DeepLinkRecord(cursor uint64, itemID, text string) schema.JournalRecord {
	return schema.JournalRecord{Cursor: cursor, SessionID: "m/s1", Type: "interaction",
		Item: json.RawMessage(`{"v":1,"item_id":"` + itemID + `","ts":"2026-08-16T10:00:00Z",` +
			`"kind":"user_message","text":"` + text + `"}`)}
}

// TestR6DeepLink_ResolveFindsAnItemByIdNotByCursor: the happy path, and the property the
// happy path rests on -- the answer carries the item's CURRENT cursor, which is what the
// screen scrolls to.
func TestR6DeepLink_ResolveFindsAnItemByIdNotByCursor(t *testing.T) {
	s := NewItemStore()
	s.Apply(r6DeepLinkRecord(10, "01JAAA", "first"))
	s.Apply(r6DeepLinkRecord(11, "01JBBB", "second"))

	it, ok := s.Resolve("m/s1", "01JBBB")
	if !ok {
		t.Fatal("Resolve missed an item the store holds")
	}
	if it.ItemID != "01JBBB" || it.Cursor != 11 {
		t.Errorf("Resolve = item %q at cursor %d, want 01JBBB at 11", it.ItemID, it.Cursor)
	}
}

// TestR6DeepLink_ResolveSurvivesAReconciliationThatMovedTheCursors is the M3 row's named
// scenario: the daemon restarted, reconstruction re-journalled the transcript, and the
// repair re-delivered the SAME items at HIGHER cursors. The notification the user is
// tapping was minted BEFORE the restart, so only the item_id in it is stable -- and the
// resolver must land on the item at its NEW position.
func TestR6DeepLink_ResolveSurvivesAReconciliationThatMovedTheCursors(t *testing.T) {
	s := NewItemStore()
	s.Apply(r6DeepLinkRecord(10, "01JAAA", "first"))
	s.Apply(r6DeepLinkRecord(11, "01JBBB", "second"))
	// The post-restart re-delivery: same ids, new cursors (the fold's own LastCursor
	// guard accepts records that ADVANCE).
	s.Apply(r6DeepLinkRecord(100, "01JAAA", "first"))
	s.Apply(r6DeepLinkRecord(101, "01JBBB", "second"))

	it, ok := s.Resolve("m/s1", "01JAAA")
	if !ok {
		t.Fatal("Resolve missed an item that survived reconciliation")
	}
	if it.ItemID != "01JAAA" {
		t.Fatalf("Resolve = %q, want 01JAAA", it.ItemID)
	}
	if len(s.Session("m/s1")) != 2 {
		t.Errorf("the re-delivery split items instead of folding by id: %d items, want 2 (IS-ENV-2)",
			len(s.Session("m/s1")))
	}
}

// TestR6DeepLink_AnUnretainedIdMissesHonestly: an id below the retention floor (or from
// some other session) answers ok=false -- NEVER the nearest item, and never another
// session's item under the same id namespace. A deep-link that silently lands somewhere
// else is worse than one that says "no longer here".
func TestR6DeepLink_AnUnretainedIdMissesHonestly(t *testing.T) {
	s := NewItemStore()
	s.Apply(r6DeepLinkRecord(10, "01JAAA", "first"))

	if _, ok := s.Resolve("m/s1", "01JGONE"); ok {
		t.Error("Resolve claimed an item the store does not hold")
	}
	if _, ok := s.Resolve("m/OTHER", "01JAAA"); ok {
		t.Error("Resolve crossed sessions: the id is unique WITHIN a session (IS-ENV §2) and " +
			"a notification's session half is load-bearing")
	}
}
