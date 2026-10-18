package opencodebridge

import (
	"context"
	"encoding/json"
	"errors"
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
	const token = "syntheticprivatebridgecredentialtoken"
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

func TestRequestReceiptClient(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "requests.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dispatch" {
			var fields map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
				t.Error(err)
			}
			for _, field := range []string{"messageID", "text", "reply"} {
				if _, ok := fields[field]; ok {
					t.Errorf("irrelevant prompt/permission field %s", field)
				}
			}
			if string(fields["answers"]) != `[["A"],[]]` || string(fields["requestID"]) != `"que_target"` {
				t.Errorf("wrong answer body: %v", fields)
			}
		} else if r.URL.Path != "/receipt" || r.URL.Query().Get("operationID") != "opaque+/?=" {
			t.Errorf("wrong receipt request: %s", r.URL)
		}
		fmt.Fprint(w, `{"status":"admitted","operationID":"opaque+/?=","kind":"question_reply","requestID":"que_target","targetID":"ses_child"}`)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client, err := NewClient(socket, "syntheticprivatebridgecredentialtoken")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	receipt, err := client.Dispatch(context.Background(), DispatchRequest{Kind: "question_reply", RequestID: "que_target", TargetID: "ses_child", Answers: [][]string{{"A"}, {}}})
	if err != nil || receipt.Kind != "question_reply" || receipt.RequestID != "que_target" || receipt.TargetID != "ses_child" {
		t.Fatalf("request receipt: %+v %v", receipt, err)
	}
	saved, err := client.OperationReceipt(context.Background(), "opaque+/?=")
	if err != nil || saved == nil || saved.Status != "admitted" {
		t.Fatalf("saved receipt: %+v %v", saved, err)
	}
}

func TestExactNativeMessageRead(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "message.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	const existing = "msg_0123456789abcdef"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message" {
			t.Errorf("wrong route %s", r.URL)
		}
		if r.URL.Query().Get("messageID") != existing {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"reason":"native_message_missing"}`)
			return
		}
		fmt.Fprint(w, `{"info":{"id":"msg_0123456789abcdef","role":"user"},"parts":[],"nativeExtension":{"preserved":true}}`)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client, err := NewClient(socket, "syntheticprivatebridgecredentialtoken")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	row, err := client.Message(context.Background(), existing)
	if err != nil || !strings.Contains(string(row), `"nativeExtension":{"preserved":true}`) {
		t.Fatalf("raw message: %s %v", row, err)
	}
	_, err = client.Message(context.Background(), "msg_aaaaaaaaaaaaaaaa")
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Code != 404 || refusal.Reason != "native_message_missing" {
		t.Fatalf("native absence: %v", err)
	}
}

func TestInitialChoiceFields(t *testing.T) {
	raw, err := json.Marshal(InitialRequest{ExpectedModel: "fixture/namespace/model", ExpectedAgent: "build"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["expectedModel"]) != `"fixture/namespace/model"` || string(fields["expectedAgent"]) != `"build"` {
		t.Fatalf("launch choices missing: %s", raw)
	}
	raw, err = json.Marshal(InitialRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "expectedModel") || strings.Contains(string(raw), "expectedAgent") {
		t.Fatalf("blank defaults were sent: %s", raw)
	}
}
