package protocol

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"strings"
	"testing"
)

type nativeHistoryBackend struct {
	*r6HistoryBackend
	query schema.InteractionHistoryReq
}

func (b *nativeHistoryBackend) NativeInteractionHistory(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, ErrorCode, error) {
	b.mu.Lock()
	b.query = req
	b.mu.Unlock()
	return schema.NativeHistoryPage{Version: 2, Scope: strings.Repeat("a", 64), Items: []json.RawMessage{json.RawMessage(`{"v":2,"kind":"message_snapshot","provenance":"imported"}`)}, NextCursor: "native-next"}, "", nil
}
func TestNativeHistoryUsesIndependentOptionalReadSeam(t *testing.T) {
	b := &nativeHistoryBackend{r6HistoryBackend: newR6HistoryBackend()}
	sock := tmpSock(t)
	server, err := Serve(b, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client, err := Dial(sock, []string{CapJournal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	req := schema.InteractionHistoryReq{Session: client.EndpointID() + "/sess1", Native: true, NativeCursor: "native-before", Limit: 20}
	reply, err := client.request(Control{Op: OpInteractionHistory, EndpointID: client.EndpointID(), SessionID: req.Session, History: &req})
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if reply.NativeHistory == nil || reply.NativeHistory.NextCursor != "native-next" || len(reply.Journal) != 0 || b.query != req || len(b.histQ) != 0 {
		t.Fatalf("native page fell through journal history: %+v", reply)
	}
}
