package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/coder/websocket"
)

// resumeExcludeTurnsServer is a real WebSocket JSON-RPC app-server over a UDS. It models the
// production failure that motivated this fence: thread/resume includes the complete turn history
// unless excludeTurns is true, and a long-running thread can therefore exceed the client's
// deliberately bounded 8 MiB inbound frame limit before the live subscription is established.
type resumeExcludeTurnsServer struct {
	t      *testing.T
	sock   string
	server *http.Server

	mu       sync.Mutex
	requests []resumeExcludeTurnsRequest
	valid    int
}

type resumeExcludeTurnsRequest struct {
	ThreadID     string `json:"threadId"`
	ExcludeTurns *bool  `json:"excludeTurns"`
}

func newResumeExcludeTurnsServer(t *testing.T, threadID string) *resumeExcludeTurnsServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sw-resume-exclude-turns")
	if err != nil {
		t.Fatalf("make short app-server socket directory: %v", err)
	}
	s := &resumeExcludeTurnsServer{t: t, sock: filepath.Join(dir, "codex.sock")}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		t.Fatalf("listen on fake app-server socket: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(appserver.RPCPath, func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			s.handle(ws, data, threadID)
		}
	})
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = s.server.Serve(ln) }()
	t.Cleanup(func() {
		_ = s.server.Close()
		_ = os.RemoveAll(dir)
	})
	return s
}

func (s *resumeExcludeTurnsServer) handle(ws *websocket.Conn, data []byte, threadID string) {
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &frame); err != nil || frame.Method != "thread/resume" {
		return
	}
	var request resumeExcludeTurnsRequest
	if err := json.Unmarshal(frame.Params, &request); err != nil {
		s.t.Errorf("decode thread/resume params: %v", err)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, request)
	valid := request.ExcludeTurns != nil && *request.ExcludeTurns
	if valid {
		s.valid++
	}
	validCall := s.valid
	s.mu.Unlock()

	ctx := context.Background()
	if !valid {
		// A response just over the client limit is what an established Codex thread with a large
		// turn history produces when excludeTurns is omitted. The client must keep its 8 MiB
		// safety bound; the resume request must avoid asking for this history instead.
		oversized := strings.Repeat("x", (8<<20)+1)
		s.reply(ws, frame.ID, fmt.Sprintf(`{"thread":{"id":%q,"turns":%q}}`, threadID, oversized))
		return
	}
	if request.ThreadID != threadID {
		s.replyError(ws, frame.ID, -32602, "wrong thread id")
		return
	}
	if validCall == 1 {
		// Preserve the recorded pre-first-turn race so the production retry path makes the
		// second request; both attempts must use the history-free shape.
		s.replyError(ws, frame.ID, -32600, "no rollout found for thread id "+threadID)
		return
	}
	s.reply(ws, frame.ID, fmt.Sprintf(`{"thread":{"id":%q}}`, threadID))
	_ = ws.Write(ctx, websocket.MessageText, []byte(
		`{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"delta":"still live"}}`,
	))
}

func (s *resumeExcludeTurnsServer) reply(ws *websocket.Conn, id json.RawMessage, result string) {
	_ = ws.Write(context.Background(), websocket.MessageText, []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result,
	)))
}

func (s *resumeExcludeTurnsServer) replyError(ws *websocket.Conn, id json.RawMessage, code int, message string) {
	body, _ := json.Marshal(message)
	_ = ws.Write(context.Background(), websocket.MessageText, []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%s}}`, id, code, body,
	)))
}

func (s *resumeExcludeTurnsServer) observed() []resumeExcludeTurnsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]resumeExcludeTurnsRequest(nil), s.requests...)
}

// TestResumeThreadExcludesHistoricalTurnsOnTheInitialAndRetryCalls protects the remote live
// bridge for old, large Codex conversations. Loading history during thread/resume can exceed the
// app-server client's intentional 8 MiB frame cap and close the socket before it subscribes.
// History already comes from Swarm's journal; resume needs only the future notification stream.
func TestResumeThreadExcludesHistoricalTurnsOnTheInitialAndRetryCalls(t *testing.T) {
	const threadID = "01995d55-6606-7d33-8e63-f8b7e0b77062"
	server := newResumeExcludeTurnsServer(t, threadID)
	notified := make(chan string, 1)
	client, err := appserver.Dial(context.Background(), server.sock, appserver.Options{
		DialTimeout: time.Second,
		OnNotify: func(method string, _ json.RawMessage) {
			notified <- method
		},
	})
	if err != nil {
		t.Fatalf("dial fake app-server: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	sk := assemble(t)
	subscribed, err := sk.resumeThreadOnce(client, threadID)
	if subscribed || !isMissingRollout(err) {
		t.Fatalf("initial history-free resume should reach the recorded rollout retry: subscribed=%v err=%v", subscribed, err)
	}

	// Exercise the real retry caller, not a mock's second direct invocation.
	sk.backend.mu.Lock()
	if sk.backend.live == nil {
		sk.backend.live = map[string]*sessionBackend{}
	}
	sk.backend.live["large-resume"] = &sessionBackend{threadID: threadID, conn: client}
	sk.backend.mu.Unlock()
	sk.subscribeSessionThread("large-resume", client, threadID)
	if !sk.backendSubscribed("large-resume") {
		t.Fatal("retry did not establish the live thread subscription")
	}

	requests := server.observed()
	if len(requests) != 2 {
		t.Fatalf("thread/resume requests = %d, want initial + one retry; params=%+v", len(requests), requests)
	}
	for i, request := range requests {
		if request.ThreadID != threadID {
			t.Errorf("thread/resume request %d threadId = %q, want exact %q", i+1, request.ThreadID, threadID)
		}
		if request.ExcludeTurns == nil || !*request.ExcludeTurns {
			t.Errorf("thread/resume request %d excludeTurns = %v, want true; without it a large history exceeds the 8 MiB frame limit", i+1, request.ExcludeTurns)
		}
	}

	select {
	case method := <-notified:
		if method != "item/agentMessage/delta" {
			t.Fatalf("notification after resume = %q, want live item delta", method)
		}
	case <-time.After(time.Second):
		t.Fatal("history-free resume returned but no later live notification reached the client")
	}
}
