package protocol

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestOwnerRetainedHistoryClientKeepsWholeDeltasAndExplicitFloor(t *testing.T) {
	backend := newR6HistoryBackend()
	backend.floor = true
	backend.history = []schema.JournalRecord{{Cursor: 1, SessionID: "sess1", Type: "interaction", Item: json.RawMessage(`{"item_id":"a","kind":"agent_message","text":"Final "}`)}, {Cursor: 2, SessionID: "sess1", Type: "interaction", Item: json.RawMessage(`{"item_id":"a","kind":"agent_message","text":"result","status":"completed"}`)}}
	socket := tmpSock(t)
	server, err := Serve(backend, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client, err := Dial(socket, []string{CapJournal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	req := schema.InteractionHistoryReq{Session: client.EndpointID() + "/sess1", BeforeItem: "next", Limit: 1}
	page, err := client.RetainedHistoryPage(req)
	if err != nil || !page.Floor || len(page.Records) != 2 {
		t.Fatal("whole retained item was cut to the requested record count", err)
	}
	req.Native = true
	if _, err = client.RetainedHistoryPage(req); err == nil {
		t.Fatal("native mode was accepted as retained output")
	}
}
