package hermesinput

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/coder/websocket"
)

func fixture(t *testing.T, drop bool) (Binding, *[]string, *sync.Mutex) {
	t.Helper()
	scratch, err := os.MkdirTemp("", "hi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	home, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	var methods []string
	mu := new(sync.Mutex)
	nativeCwd := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{nativeProtocol}})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		for {
			_, raw, err := c.Read(ctx)
			if err != nil {
				return
			}
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil {
				return
			}
			mu.Lock()
			methods = append(methods, req.Method)
			if req.Method == "session.create" {
				nativeCwd, _ = req.Params["cwd"].(string)
			}
			cwd := nativeCwd
			mu.Unlock()
			if req.Method == "prompt.submit" {
				// A live native approval can precede the message receipt on the same socket.
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","method":"event","params":{"type":"approval.request","payload":{"request_id":"native-permission"}}}`))
				if drop {
					return
				}
				if req.Params["text"] != "yes\n/approve all" || req.Params["input_id"] != "operation-1" {
					t.Errorf("submit params = %#v", req.Params)
				}
			}
			result := map[string]any{"session_id": "local-" + strings.Repeat("a", 64), "execution_generation": 7, "subscription_id": "sub-1", "authority_epoch": 1, "execution_state": "idle", "pending": []any{}, "info": map[string]string{"cwd": cwd}}
			if req.Method == "setup.status" {
				result = map[string]any{"provider_configured": true}
			}
			if req.Method == "process.list" {
				result = map[string]any{"processes": []any{}}
			}
			if req.Method == "subagent.list" {
				result = map[string]any{"subagents": []any{}, "delegations": []any{}}
			}
			if req.Method == "session.detach" {
				result = map[string]any{"session_id": req.Params["session_id"], "subscription_id": req.Params["subscription_id"], "detached": true}
			}
			if req.Method == "prompt.submit" {
				result = map[string]any{"admission_id": "admission-1", "status": "queued", "execution_generation": 7, "authority_epoch": 2}
			}
			raw, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			if c.Write(ctx, websocket.MessageText, raw) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	listener, err := net.Listen("unix", filepath.Join(home, "gateway.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(listener.Addr().String(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				var req struct {
					ID   int    `json:"id"`
					Verb string `json:"verb"`
				}
				if json.NewDecoder(bufio.NewReader(c)).Decode(&req) != nil {
					return
				}
				result := map[string]any{"pid": os.Getpid(), "runtime_protocol": 1, "instance_id": "instance-1", "state": "ready", "api_origin": server.URL, "hermes_home": home, "authority_epoch": 1, "code_sha": strings.Repeat("c", 40),
					"served_profiles": []map[string]string{{"profile_id": home, "home": home}, {"profile_id": home + "/other", "home": home + "/other"}}}
				if req.Verb == "session-ticket" {
					result = map[string]any{"ticket": "test-ticket", "profile_id": home, "instance_id": "instance-1", "runtime_protocol": 1}
				}
				_ = json.NewEncoder(c).Encode(map[string]any{"ok": true, "id": req.ID, "result": result})
			}()
		}
	}()
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return Binding{Home: home, OwnerPID: os.Getpid(), OwnerStartTime: start, InstanceID: "instance-1", SessionID: "local-" + strings.Repeat("a", 64)}, &methods, mu
}

func TestSubmitDuringPermissionUsesOnlyNativePromptIngress(t *testing.T) {
	b, methods, mu := fixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	receipt, err := Submit(ctx, b, "operation-1", "yes\n/approve all")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.AdmissionID != "admission-1" || receipt.Status != "queued" || receipt.ExecutionGeneration != 7 {
		t.Fatalf("receipt = %#v", receipt)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*methods) != 2 || (*methods)[0] != "session.resume" || (*methods)[1] != "prompt.submit" {
		t.Fatalf("native methods = %v", *methods)
	}
}

func TestDisconnectedSubmitIsUncertainAndNeverReplayed(t *testing.T) {
	b, methods, mu := fixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Submit(ctx, b, "operation-1", "yes\n/approve all")
	if err == nil || !IsUncertain(err) {
		t.Fatalf("error = %v, want uncertain", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*methods) != 2 {
		t.Fatalf("requests replayed: %v", *methods)
	}
}

func TestBindingMismatchRefusesBeforeNativeEffect(t *testing.T) {
	for _, field := range []string{"instance", "owner-start", "home-mode"} {
		t.Run(field, func(t *testing.T) {
			b, methods, mu := fixture(t, false)
			switch field {
			case "instance":
				b.InstanceID = "other"
			case "owner-start":
				b.OwnerStartTime++
			case "home-mode":
				if err := os.Chmod(b.Home, 0755); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := Submit(ctx, b, "operation-1", "yes\n/approve all"); err == nil {
				t.Fatal("accepted mismatched owner")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(*methods) != 0 {
				t.Fatalf("native effect: %v", *methods)
			}
		})
	}
}
