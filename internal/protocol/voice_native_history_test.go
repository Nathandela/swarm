package protocol

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestOwnerNativeHistoryPageClientUsesNativePageAndCursor(t *testing.T) {
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
	page, err := client.NativeHistoryPage(req)
	if err != nil || page.NextCursor != "native-next" {
		t.Fatalf("native page: %+v %v", page, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.query != req || len(b.histQ) != 0 {
		t.Fatal("owner native page drifted into legacy journal history")
	}
	req.Native = false
	if _, err := client.NativeHistoryPage(req); err == nil {
		t.Fatal("legacy mode accepted as native history")
	}
}
