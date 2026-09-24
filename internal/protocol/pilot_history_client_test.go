package protocol

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestPilotInteractionHistoryClientReadsNamedDiscussion(t *testing.T) {
	b := newR6HistoryBackend()
	b.history = []schema.JournalRecord{{Cursor: 7, SessionID: "sess1", Type: "interaction", Item: json.RawMessage(`{"kind":"agent_message","text":"still probing"}`)}}
	sock := tmpSock(t)
	srv, err := Serve(b, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	c, err := Dial(sock, []string{CapJournal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	id := c.EndpointID() + "/sess1"
	recs, err := c.InteractionHistory(id, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || string(recs[0].Item) != string(b.history[0].Item) {
		t.Fatalf("history = %+v, want the worker's exact record", recs)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.histQ) != 1 || b.histQ[0] != (r6HistoryQuery{session: id, limit: 12}) {
		t.Fatalf("history query = %+v, want explicit session and bounded newest page", b.histQ)
	}
}
