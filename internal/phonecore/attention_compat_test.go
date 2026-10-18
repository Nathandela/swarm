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

func TestParallelNativeAttentionSurvivesPhoneMailboxAndRestart(t *testing.T) {
	store := &memStore{}
	seedPaired(t, store)
	phone, err := Resume(Config{State: store, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatal(err)
	}
	group := status.Derive(status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionPermission, Attention: true})
	roster := schema.JournalRecord{Cursor: 10, SessionID: "m1/native", Type: "session_state", Agent: "opencode", Group: group}
	if _, err := phone.Router().AcceptCommit(sealFrameFrom(t, testContentKey(), machineSender, 7, 1, marshalEvent(t, roster)), 101); err != nil {
		t.Fatal(err)
	}
	const item = `{"v":1,"item_id":"native-attention","ts":"2026-10-18T10:00:00Z","kind":"session_status","process":"running","turn":"active","interaction":"permission","attention":true,"group":"needs_input"}`
	driveInteraction(t, phone.Router(), 2, 11, roster.SessionID, item)
	restarted, err := Resume(Config{State: store, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Core{phone, restarted} {
		row, ok := current.Router().Sessions().Get(roster.SessionID)
		if !ok || row.Group != status.GroupNeedsInput {
			t.Fatal("parallel attention disappeared from phone roster")
		}
		items := current.Router().Items().Session(roster.SessionID)
		if len(items) != 1 {
			t.Fatalf("missing native status marker: %d", len(items))
		}
		var body struct {
			Turn      string `json:"turn"`
			Attention bool   `json:"attention"`
		}
		if json.Unmarshal(items[0].Body, &body) != nil || !body.Attention || body.Turn != "active" {
			t.Fatalf("phone changed actual busy/attention facts: %+v", body)
		}
	}
}
