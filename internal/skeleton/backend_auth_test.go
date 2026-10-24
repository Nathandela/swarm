package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/coder/websocket"
)

func TestBackendAuthRefreshUsesRequestConnectionAndDoesNotBlockReader(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan json.RawMessage, 2)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer ws.CloseNow()
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":"refresh-1","method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized","previousAccountId":"old"}}`))
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"method":"thread/started","params":{"thread":{"id":"public"}}}`))
		_, raw, e := ws.Read(r.Context())
		if e == nil {
			replies <- raw
		}
	})}
	go server.Serve(ln)
	defer server.Close()
	release := make(chan struct{})
	defer close(release)
	var called atomic.Int32
	auth := newBackendAuthCoordinator(func(ctx context.Context, refresh bool) (backendAuthTokens, error) {
		called.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return backendAuthTokens{}, ctx.Err()
		}
		return backendAuthTokens{AccessToken: "SECRET", AccountID: "new", PlanType: "pro"}, nil
	}, func() bool { return true })
	notes := make(chan string, 1)
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{OnFrame: func(method string, raw json.RawMessage) {
		if !auth.intercept(method, raw) {
			notes <- method
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth.bind(conn)
	select {
	case <-notes:
	case <-time.After(time.Second):
		t.Fatal("refresh blocked backend reader")
	}
	release <- struct{}{}
	select {
	case raw := <-replies:
		var fr struct {
			ID     string `json:"id"`
			Result struct {
				Token string `json:"accessToken"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &fr) != nil || fr.ID != "refresh-1" || fr.Result.Token != "SECRET" {
			t.Fatalf("wrong callback response: %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("no auth response")
	}
	if called.Load() != 1 {
		t.Fatal("unexpected refresh count")
	}
}

func TestBackendAuthRejectsSecretsAndObsoleteResults(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		stale        bool
	}{
		{"malformed", `{"reason":"unauthorized","previousAccountId":"secret","accessToken":"SECRET"}`, false},
		{"obsolete", `{"reason":"unauthorized","previousAccountId":null}`, true},
		{"write_boundary_obsolete", `{"reason":"unauthorized","previousAccountId":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := backendAuthTestSocket(t)
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			replies := make(chan []byte, 1)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
				if e != nil {
					return
				}
				defer ws.CloseNow()
				_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":3,"method":"account/chatgptAuthTokens/refresh","params":`+tc.params+`}`))
				_, raw, e := ws.Read(r.Context())
				if e == nil {
					replies <- raw
				}
			})}
			go server.Serve(ln)
			defer server.Close()
			var called atomic.Int32
			auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
				called.Add(1)
				var checks atomic.Int32
				return backendAuthTokens{AccessToken: "SECRET", AccountID: "PII", current: func() bool {
					if tc.name == "write_boundary_obsolete" {
						return checks.Add(1) == 1
					}
					return false
				}}, nil
			}, func() bool { return true })
			var pumped atomic.Bool
			conn, err := appserver.Dial(context.Background(), sock, appserver.Options{OnFrame: func(method string, raw json.RawMessage) {
				if !auth.intercept(method, raw) {
					pumped.Store(true)
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			auth.bind(conn)
			select {
			case raw := <-replies:
				var fr struct {
					ID    int `json:"id"`
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if json.Unmarshal(raw, &fr) != nil || fr.ID != 3 || fr.Error.Message == "" || bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("PII")) {
					t.Fatalf("unsafe auth rejection: %s", raw)
				}
			case <-time.After(time.Second):
				t.Fatal("missing auth rejection")
			}
			if pumped.Load() {
				t.Fatal("auth reached generic pump")
			}
			if !tc.stale && called.Load() != 0 {
				t.Fatal("invalid request acquired credentials")
			}
		})
	}
	raw, err := json.Marshal(backendAuthTokens{AccessToken: "SECRET", AccountID: "PII"})
	if err != nil || string(raw) != "{}" {
		t.Fatalf("credentials serialized: %s", raw)
	}
}

func backendAuthTestSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swauth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

func TestBackendAuthBootstrapAcknowledgesPrivateNativeLogin(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	request := make(chan []byte, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer ws.CloseNow()
		_, raw, e := ws.Read(r.Context())
		if e != nil {
			return
		}
		request <- raw
		var fr struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(raw, &fr)
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":`+string(fr.ID)+`,"result":{"type":"chatgptAuthTokens"}}`))
		_, _, _ = ws.Read(r.Context())
	})}
	go server.Serve(ln)
	defer server.Close()
	auth := newBackendAuthCoordinator(func(_ context.Context, refresh bool) (backendAuthTokens, error) {
		if refresh {
			t.Error("bootstrap forced refresh")
		}
		return backendAuthTokens{AccessToken: "SECRET", AccountID: "account", current: func() bool { return true }}, nil
	}, func() bool { return true })
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth.bind(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = auth.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	raw := <-request
	var fr struct {
		Method string            `json:"method"`
		Params map[string]string `json:"params"`
	}
	if json.Unmarshal(raw, &fr) != nil || fr.Method != "account/login/start" || fr.Params["accessToken"] != "SECRET" || fr.Params["type"] != "chatgptAuthTokens" {
		t.Fatal("incorrect native auth bootstrap")
	}
}

func TestBackendAuthExactParams(t *testing.T) {
	for _, raw := range []string{`{"reason":"unauthorized","Reason":"unauthorized"}`, `{"reason":"unauthorized","reason":"unauthorized"}`, `{"reason":"unauthorized","previousAccountId":4}`, `null`, `{"reason":"other"}`} {
		if validBackendAuthParams(json.RawMessage(raw)) {
			t.Fatalf("accepted malformed auth params: %s", raw)
		}
	}
}

func TestBackendAuthOverflowClosesFeedAndCancelsRefresh(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer ws.CloseNow()
		for n := 0; n < 20; n++ {
			_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":`+strconv.Itoa(n)+`,"method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized"}}`))
			if n == 0 {
				select {
				case <-started:
				case <-r.Context().Done():
					return
				}
			}
		}
		_, _, _ = ws.Read(r.Context())
	})}
	go server.Serve(ln)
	defer server.Close()
	cancelled := make(chan struct{}, 1)
	auth := newBackendAuthCoordinator(func(ctx context.Context, _ bool) (backendAuthTokens, error) {
		started <- struct{}{}
		<-ctx.Done()
		select {
		case cancelled <- struct{}{}:
		default:
		}
		return backendAuthTokens{}, ctx.Err()
	}, func() bool { return true })
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{OnFrame: func(method string, raw json.RawMessage) { auth.intercept(method, raw) }})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth.bind(conn)
	select {
	case <-conn.Done():
	case <-time.After(time.Second):
		t.Fatal("overflow left auth feed live")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("lost connection did not cancel auth work")
	}
}
