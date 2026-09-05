package relayv2

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/coder/websocket"
)

func TestResponseRequiresTypedFieldsAndCanonicalIncarnation(t *testing.T) {
	for _, body := range []string{
		`{"v":2,"type":"APPENDED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184","generation":"1","cursor":"1"}`,
		`{"v":2,"type":"APPENDED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184","generation":"1","cursor":"1","deduped":null}`,
		`{"v":2,"v":2,"type":"REVOKED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184"}`,
		`{"v":2,"type":"ERROR","request_id":"r","code":"` + strings.Repeat("x", 1000) + `"}`,
	} {
		if _, err := decodeFrame([]byte(body)); err == nil {
			t.Fatalf("accepted incomplete/unbounded response: %.80s", body)
		}
	}
	if validIncarnation("AAAAAAAAAAAAAAAAAAAAAB") {
		t.Fatal("accepted non-canonical 16-byte incarnation")
	}
	if !validIncarnation(base64.RawURLEncoding.EncodeToString(make([]byte, 16))) {
		t.Fatal("rejected canonical 16-byte incarnation")
	}
}

func TestSecurityRejectsRoutableCleartext(t *testing.T) {
	if _, _, err := secureHTTPClient(relay.Security{}, "ws://relay.example/v2/ws"); err == nil {
		t.Fatal("zero-value production security accepted routable cleartext")
	}
	if _, _, err := secureHTTPClient(relay.Security{AllowLoopbackCleartext: true}, "ws://127.0.0.1:8790/v2/ws"); err != nil {
		t.Fatalf("explicit test-only loopback policy rejected: %v", err)
	}
}

func TestSlowConsumerCannotBlockResponsePump(t *testing.T) {
	wrote := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		payload := base64.RawURLEncoding.EncodeToString(make([]byte, maxQueuedEventBytes/2))
		for i := 0; i < 3; i++ {
			body := fmt.Sprintf(`{"v":2,"type":"DELIVER","request_id":"delivery-%d","peer_rid":"88564c8ede170d2ed321e21e61354184","generation":"1","incarnation":"AAAAAAAAAAAAAAAAAAAAAA","cursor":"%d","msg_id":"m%d","ciphertext":"%s"}`, i, i+1, i, payload)
			if err := ws.Write(context.Background(), websocket.MessageText, []byte(body)); err != nil {
				return
			}
		}
		close(wrote)
		<-r.Context().Done()
	}))
	defer server.Close()
	c, err := dialRaw(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("server did not write test deliveries")
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("event byte overflow blocked the reader instead of closing")
	}
}

func TestClientBounds(t *testing.T) {
	if defaultCallTimeout <= 0 || defaultDialTimeout <= 0 || maxPendingRequests <= 0 || maxPendingRequests > 64 {
		t.Fatal("client request/dial bounds are missing or excessive")
	}
}
