package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the fact a sent bubble settles on.
// Plan: docs/specifications/chat-surface-plan.md §7 D.7. Bead: agents-tracker-tbpm.4.
//
// OWNER RULING R6: a message drawn on the phone stays PENDING until the agent's own
// transcript echoes it back, and only then settles.
//
// WHY IT NEEDS A FACT AND NOT A TIMER. The audit committee's worst finding about the
// redesign was that a settled bubble is a delivery claim the wire cannot back:
// composer_send is acknowledged when the daemon wrote BYTES INTO A PTY, not when the CLI
// accepted them, and on the keystroke path there is no acknowledgement from the CLI ever.
// If the agent was in a permission prompt or a slash menu when the text landed, it went
// somewhere else entirely. Today's flat "Sent" label is weak enough to survive that; a
// chat bubble is a much stronger claim over identical evidence.
//
// THE FACT ALREADY EXISTS ON THE MACHINE AND STOPS AT THE FOLD. When the CLI echoes an
// injected prompt through its own hook, the daemon -- the only party that watched the
// injection -- stamps that item `source: phone` AND `operation_id: <the phone's own op>`
// (skeleton/chat.go, stampComposerEchoLocked). The phone's fold carries `source` across
// and DROPS the operation id, so a phone holding two sends in flight can see that one of
// them was echoed but not WHICH. Carrying it is the whole change.

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestEchoID_TheFoldCarriesTheOperationIdOfAnEchoedSend. Without it the phone can only
// match on text, which is exactly the correlation the daemon itself refuses to rely on:
// chat.go records a PROBED mis-attribution where an owner-typed "yes" was stamped as the
// phone's because a phone send of "yes" was pending. A bubble that settled on text would
// inherit that defect on the surface where the user is watching.
func TestEchoID_TheFoldCarriesTheOperationIdOfAnEchoedSend(t *testing.T) {
	const opID = "devA:01JECHOSETTLE0000000000"
	s := NewItemStore()

	body, err := json.Marshal(map[string]any{
		"v":            1,
		"item_id":      "01JITEM0000000000000000001",
		"kind":         "user_message",
		"text":         "check the relay logs too",
		"source":       "phone",
		"operation_id": opID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !s.Apply(schema.JournalRecord{
		Type: RecordTypeInteraction, SessionID: "s1", Cursor: 1, Item: body,
	}) {
		t.Fatalf("the record was not applied")
	}

	items := s.Session("s1")
	if len(items) != 1 {
		t.Fatalf("Session = %d items, want 1", len(items))
	}
	if got := items[0].OperationID; got != opID {
		t.Fatalf("Item.OperationID = %q, want %q.\n"+
			"The daemon stamps this id onto the echoed prompt precisely so the phone can tell "+
			"WHICH of its sends the agent received. Dropping it at the fold leaves the surface "+
			"matching on text, which is the correlation the daemon has a probed "+
			"mis-attribution against.", got, opID)
	}
	if items[0].Source != "phone" {
		t.Fatalf("Item.Source = %q, want \"phone\"", items[0].Source)
	}
}

// TestEchoID_AnOwnerPromptCarriesNone. The absence is as load-bearing as the presence: a
// prompt typed at the machine matches no pending send, so the phone must not settle a
// bubble on it.
func TestEchoID_AnOwnerPromptCarriesNone(t *testing.T) {
	s := NewItemStore()
	body, err := json.Marshal(map[string]any{
		"v":       1,
		"item_id": "01JITEM0000000000000000002",
		"kind":    "user_message",
		"text":    "check the relay logs too",
		"source":  "owner",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !s.Apply(schema.JournalRecord{
		Type: RecordTypeInteraction, SessionID: "s1", Cursor: 1, Item: body,
	}) {
		t.Fatalf("the record was not applied")
	}
	items := s.Session("s1")
	if len(items) != 1 {
		t.Fatalf("Session = %d items, want 1", len(items))
	}
	if got := items[0].OperationID; got != "" {
		t.Fatalf("an owner-typed prompt carries OperationID %q; the phone would settle a bubble "+
			"on somebody else's message", got)
	}
}
