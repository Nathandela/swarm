package opencodebridge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateClientAndAsset(t *testing.T) {
	if len(PluginSource) == 0 || !strings.Contains(string(PluginSource), "export default") {
		t.Fatal("missing embedded native plugin")
	}
	socket := filepath.Join(t.TempDir(), "b.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	const token = "synthetic-private-bridge-token"
	streaming := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("private bridge token missing")
		}
		switch r.URL.Path {
		case "/ready":
			fmt.Fprint(w, `{"version":1,"rootID":"ses_abcdefghij","bound":true,"ready":true,"nativeCreatedAtMS":123}`)
		case "/snapshot":
			if r.URL.Query().Get("before") != "opaque+/?=" || r.URL.Query().Get("limit") != "2" {
				t.Error("opaque cursor was changed")
			}
			fmt.Fprint(w, `{"messages":[],"nextCursor":"opaque-next","statuses":{},"permissions":[],"questions":[],"children":[]}`)
		case "/dispatch":
			w.WriteHeader(422)
			fmt.Fprintf(w, `{"reason":%q}`, token)
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"swarm.bridge.connected\",\"properties\":{}}\n\n")
			w.(http.Flusher).Flush()
			close(streaming)
			<-r.Context().Done()
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := NewClient(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ready, err := client.Ready(context.Background())
	if err != nil || !ready.Bound || ready.RootID != "ses_abcdefghij" || ready.NativeCreatedAtMS != 123 {
		t.Fatalf("ready: %+v %v", ready, err)
	}
	page, err := client.Snapshot(context.Background(), 2, "opaque+/?=")
	if err != nil || page.NextCursor != "opaque-next" || page.Messages == nil {
		t.Fatalf("page: %+v %v", page, err)
	}
	_, err = client.Dispatch(context.Background(), DispatchRequest{})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("unsafe refusal: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- client.Events(context.Background(), func(event Event) error {
			if event.Type != "swarm.bridge.connected" {
				t.Error("wrong event")
			}
			return nil
		})
	}()
	select {
	case <-streaming:
	case <-time.After(time.Second):
		t.Fatal("events did not start")
	}
	client.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stream loss was silent")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active events")
	}
}
