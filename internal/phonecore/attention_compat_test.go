package phonecore

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAttentionErrorSurvivesPhoneMailboxAndRestart(t *testing.T) {
	store := &memStore{}
	seedPaired(t, store)
	phone, err := Resume(Config{State: store, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatal(err)
	}
	roster := schema.JournalRecord{Cursor: 10, SessionID: "m1/s-alpha", Type: "session_state", Agent: "codex", Group: status.GroupNeedsInput}
	if _, err := phone.Router().AcceptCommit(sealFrameFrom(t, testContentKey(), machineSender, 7, 1, marshalEvent(t, roster)), 101); err != nil {
		t.Fatal(err)
	}
	const item = `{"v":1,"item_id":"failure-status","ts":"2026-08-07T10:00:00Z","kind":"session_status","process":"running","turn":"idle","interaction":"error","group":"needs_input"}`
	driveInteraction(t, phone.Router(), 2, 11, roster.SessionID, item)
	restarted, err := Resume(Config{State: store, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Core{phone, restarted} {
		row, ok := current.Router().Sessions().Get(roster.SessionID)
		if !ok || row.Group != status.GroupNeedsInput {
			t.Fatalf("phone lost existing Needs input group: %+v, present=%v", row, ok)
		}
		items := current.Router().Items().Session(roster.SessionID)
		if len(items) != 1 || items[0].Kind != KindSessionStatus {
			t.Fatalf("phone dropped version-one status item: %+v", items)
		}
		var body struct {
			Version     int    `json:"v"`
			Interaction string `json:"interaction"`
		}
		if err := json.Unmarshal(items[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Version != 1 || body.Interaction != "error" {
			t.Fatalf("phone changed additive error vocabulary: %+v", body)
		}
	}
}
