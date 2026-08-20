package appserver_test

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.2's TRANSPORT: the daemon-owned JSON-RPC client
// that speaks to `codex app-server --listen unix://PATH`. Bead: agents-tracker-hggx.8.
// ADR-013 §R7.2d ("the JSON-RPC client is DAEMON-owned, in a new core package
// internal/appserver ... it holds the connection, the request-id correlation, the
// server-request table and nothing else; it imports neither internal/adapter nor
// internal/daemon").
//
// THE SINGLE MOST IMPORTANT INTEGRATION DETAIL IN R7, and it cost the R1 gate ten minutes of
// its timebox to find (r1-codex-gate.md:32-37):
//
//	The `--listen unix://PATH` endpoint is a WEBSOCKET endpoint, not a raw JSON-lines socket.
//	A client must perform an HTTP/1.1 upgrade (GET /rpc, Upgrade: websocket,
//	Sec-WebSocket-Version: 13) over the UDS and then exchange JSON-RPC messages as WebSocket
//	TEXT frames. Writing bare newline-delimited JSON to the socket produces TOTAL SILENCE:
//	no response, no error, no server log.
//
// The recorded upgrade bytes are r1-codex-fixtures/ws-handshake.txt, captured off the REAL
// TUI's connection through a logging MITM on the UDS. The failure mode is fenced here as its
// own test, because "no response, no error, no server log" is the single most expensive shape a
// bug can take: nothing in the system says anything is wrong.
//
// THE CONTRACT these tests freeze:
//
//	const RPCPath = "/rpc"
//	type Options struct {
//	    DialTimeout time.Duration
//	    OnNotify    func(method string, params json.RawMessage)
//	    OnRequest   func(id json.RawMessage, method string, params json.RawMessage)
//	    OnClose     func(error)
//	}
//	func Dial(ctx context.Context, socketPath string, opt Options) (*Client, error)
//	func (c *Client) Call(ctx context.Context, method string, params, out any) error
//	func (c *Client) Notify(ctx context.Context, method string, params any) error
//	func (c *Client) Respond(ctx context.Context, id json.RawMessage, result any) error
//	func (c *Client) Close() error
//	type RPCError struct{ Code int; Message string }

import (
	"context"
	"encoding/json"
	"errors"
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

// ---------------------------------------------------------------------------
// A fake app-server: a WebSocket endpoint at /rpc over a UDS, exactly as
// ws-handshake.txt records the real one.
// ---------------------------------------------------------------------------

type fakeServer struct {
	t    *testing.T
	sock string

	mu       sync.Mutex
	upgrades int                       // how many successful /rpc upgrades happened
	handler  func(*fakeConn, rpcFrame) // per-message behavior
	onOpen   func(*fakeConn)           // fires once per upgraded connection
	ln       net.Listener
	srv      *http.Server
}

type fakeConn struct {
	ctx context.Context
	ws  *websocket.Conn
}

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	EmittedAtMs int64 `json:"emittedAtMs,omitempty"`
}

func (c *fakeConn) send(t *testing.T, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal server frame: %v", err)
	}
	if err := c.ws.Write(c.ctx, websocket.MessageText, body); err != nil {
		t.Logf("server write: %v", err)
	}
}

// newFakeServer binds a UDS and serves the WebSocket upgrade on GET /rpc only.
func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	dir := r7ShortDir(t)
	f := &fakeServer{t: t, sock: filepath.Join(dir, "codex.sock")}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatalf("bind fake app-server: %v", err)
	}
	f.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		f.mu.Lock()
		f.upgrades++
		onOpen, handler := f.onOpen, f.handler
		f.mu.Unlock()
		ctx := context.Background()
		c := &fakeConn{ctx: ctx, ws: ws}
		if onOpen != nil {
			go onOpen(c)
		}
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var fr rpcFrame
			if json.Unmarshal(data, &fr) != nil {
				continue
			}
			if handler != nil {
				handler(c, fr)
			}
		}
	})
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = f.srv.Close()
		_ = os.Remove(f.sock)
	})
	return f
}

func (f *fakeServer) upgradeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upgrades
}

func (f *fakeServer) setHandler(h func(*fakeConn, rpcFrame)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

// ---------------------------------------------------------------------------
// The upgrade, and the recorded silence
// ---------------------------------------------------------------------------

// TestR7AppServer_DialPerformsTheRecordedWebSocketUpgradeOverTheUDS is the positive.
func TestR7AppServer_DialPerformsTheRecordedWebSocketUpgradeOverTheUDS(t *testing.T) {
	f := newFakeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := appserver.Dial(ctx, f.sock, appserver.Options{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// EVENTUALLY ONE, and the bound is the fix for a flake this round's gate caught: the server
	// increments its counter AFTER websocket.Accept has already written the 101, so Dial can
	// return before the handler's next instruction runs. The assertion is exactly as strong --
	// a client that skips the upgrade leaves the count at 0 forever, which is the failure this
	// test exists for -- it is simply no longer a race against the server's own goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for f.upgradeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.upgradeCount() != 1 {
		t.Fatalf("the server saw %d upgrades on /rpc, want 1. Writing bare newline-delimited JSON "+
			"to this socket produces TOTAL SILENCE -- no response, no error, no server log "+
			"(r1-codex-gate.md:32-37) -- so a client that skips the upgrade fails in the one way "+
			"nothing in the system reports", f.upgradeCount())
	}
	if appserver.RPCPath != "/rpc" {
		t.Errorf("RPCPath = %q, want \"/rpc\" (RECORDED: ws-handshake.txt's first line is "+
			"`GET /rpc HTTP/1.1`)", appserver.RPCPath)
	}
}

// TestR7AppServer_TheHandshakeCarriesTheRecordedHeaders asserts on the bytes the real TUI's
// connection put on the wire, captured through a MITM on the UDS
// (r1-codex-fixtures/ws-handshake.txt).
func TestR7AppServer_TheHandshakeCarriesTheRecordedHeaders(t *testing.T) {
	dir := r7ShortDir(t)
	sock := filepath.Join(dir, "codex.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		got <- string(buf[:n])
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = appserver.Dial(ctx, sock, appserver.Options{DialTimeout: 2 * time.Second})

	select {
	case req := <-got:
		for _, want := range []string{
			"GET /rpc HTTP/1.1",
			"Upgrade: websocket",
			"Sec-WebSocket-Version: 13",
			"Sec-WebSocket-Key:",
		} {
			if !strings.Contains(req, want) {
				t.Errorf("the dial's first bytes do not contain %q; RECORDED handshake:\n%s\n"+
					"got:\n%s", want, r7RecordedHandshake(t), req)
			}
		}
		if strings.HasPrefix(strings.TrimSpace(req), "{") {
			t.Fatal("the client wrote BARE JSON as its first bytes. That is the recorded failure " +
				"mode: the server answers nothing at all, forever")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client wrote nothing to the socket within 5s")
	}
}

// r7RecordedHandshake returns the RECORDED upgrade bytes for a failure message.
func r7RecordedHandshake(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../docs/verification/r1-codex-fixtures/ws-handshake.txt")
	if err != nil {
		return "(ws-handshake.txt unreadable: " + err.Error() + ")"
	}
	return string(data)
}

// TestR7AppServer_AServerThatNeverUpgradesFailsTheDIALRatherThanHangingForever is the silence
// fence from the client's side. A UDS that accepts and then says nothing is exactly what a
// wrong-protocol write produces, and a client with no dial bound would sit on it for the life
// of the daemon while the phone showed an empty transcript and no error anywhere.
func TestR7AppServer_AServerThatNeverUpgradesFailsTheDIALRatherThanHangingForever(t *testing.T) {
	dir := r7ShortDir(t)
	sock := filepath.Join(dir, "codex.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and say nothing, forever. This is the recorded silence.
			_ = conn
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := appserver.Dial(context.Background(), sock, appserver.Options{DialTimeout: 500 * time.Millisecond})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Dial SUCCEEDED against a server that never sent 101 Switching Protocols")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Dial hung against a silent server. The whole class of bug the R1 gate paid for is " +
			"\"no response, no error, no server log\"; a dial with no bound turns that into a " +
			"daemon goroutine parked forever with a phone showing an empty transcript")
	}
}

// TestR7AppServer_DialingAPathNothingServesFailsPromptly is the launch-time arm: §R7.7 case 1
// (never connected) has to be REACHED, and it is reached by a dial that returns.
func TestR7AppServer_DialingAPathNothingServesFailsPromptly(t *testing.T) {
	sock := filepath.Join(r7ShortDir(t), "codex.sock")
	start := time.Now()
	if _, err := appserver.Dial(context.Background(), sock, appserver.Options{DialTimeout: time.Second}); err == nil {
		t.Fatal("Dial succeeded against a path nothing serves")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Dial against a dead path took %s", elapsed)
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC correlation
// ---------------------------------------------------------------------------

// TestR7AppServer_CallCorrelatesRepliesByIdEvenWhenTheyArriveOutOfOrder is the property a
// naive "read the next frame" client silently gets wrong. The RECORDED corpus shows a
// notification (`remoteControl/status/changed`) arriving BETWEEN a request and its reply
// (frame-samples.json, t=1786760503.216 send initialize -> t=...503.324 recv a notification),
// so "the next frame is my answer" is false on the very first exchange of every session.
func TestR7AppServer_CallCorrelatesRepliesByIdEvenWhenTheyArriveOutOfOrder(t *testing.T) {
	f := newFakeServer(t)
	f.setHandler(func(c *fakeConn, fr rpcFrame) {
		if fr.Method == "" || len(fr.ID) == 0 {
			return
		}
		// A notification first, then the two replies in REVERSE order.
		c.send(t, map[string]any{
			"method": "remoteControl/status/changed",
			"params": map[string]any{"status": "disabled"},
		})
		if fr.Method == "thread/start" {
			go func() {
				time.Sleep(150 * time.Millisecond)
				c.send(t, map[string]any{"id": json.RawMessage(fr.ID), "result": map[string]any{"marker": "start"}})
			}()
			return
		}
		c.send(t, map[string]any{"id": json.RawMessage(fr.ID), "result": map[string]any{"marker": "other"}})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	type reply struct {
		Marker string `json:"marker"`
	}
	var slow, fast reply
	var wg sync.WaitGroup
	wg.Add(2)
	var slowErr, fastErr error
	go func() { defer wg.Done(); slowErr = c.Call(ctx, "thread/start", map[string]any{}, &slow) }()
	time.Sleep(20 * time.Millisecond)
	go func() { defer wg.Done(); fastErr = c.Call(ctx, "thread/loaded/list", map[string]any{}, &fast) }()
	wg.Wait()

	if slowErr != nil || fastErr != nil {
		t.Fatalf("Call errors: slow=%v fast=%v", slowErr, fastErr)
	}
	if slow.Marker != "start" {
		t.Errorf("thread/start got %q, want \"start\": the client matched a reply to the wrong "+
			"request, which on the real server means a turn id belonging to another call", slow.Marker)
	}
	if fast.Marker != "other" {
		t.Errorf("thread/loaded/list got %q, want \"other\"", fast.Marker)
	}
}

// TestR7AppServer_ARecordedErrorFrameSurfacesAsATypedRPCError drives the three REAL error
// frames the gate encountered (errors-observed.json), because two of them are load-bearing
// semantics rather than failures: `no active turn to interrupt` is BENIGN (§R7.5) and
// `no rollout found for thread id` is the join race (§R7.2e). A client that flattened them to
// a string would make both undistinguishable from a transport fault.
func TestR7AppServer_ARecordedErrorFrameSurfacesAsATypedRPCError(t *testing.T) {
	recorded := r7RecordedErrors(t)
	f := newFakeServer(t)
	f.setHandler(func(c *fakeConn, fr rpcFrame) {
		if len(fr.ID) == 0 {
			return
		}
		c.send(t, map[string]any{
			"id": json.RawMessage(fr.ID),
			"error": map[string]any{
				"code":    recorded.code,
				"message": recorded.message,
			},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	err = c.Call(ctx, "turn/interrupt", map[string]any{}, nil)
	if err == nil {
		t.Fatal("a JSON-RPC error frame did not surface as an error")
	}
	var rpcErr *appserver.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("Call returned %T (%v), want an *appserver.RPCError; the caller must be able to "+
			"tell `no active turn to interrupt` (BENIGN, §R7.5) from a transport fault", err, err)
	}
	if rpcErr.Code != recorded.code {
		t.Errorf("RPCError.Code = %d, want the RECORDED %d", rpcErr.Code, recorded.code)
	}
	if rpcErr.Message != recorded.message {
		t.Errorf("RPCError.Message = %q, want the RECORDED %q", rpcErr.Message, recorded.message)
	}
}

// r7RecordedErrors returns the `no active turn to interrupt` frame from errors-observed.json.
func r7RecordedErrors(t *testing.T) struct {
	code    int
	message string
} {
	t.Helper()
	data, err := os.ReadFile("../../docs/verification/r1-codex-fixtures/errors-observed.json")
	if err != nil {
		t.Fatalf("read errors-observed.json: %v", err)
	}
	var doc struct {
		TurnInterruptOnCompletedTurn struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turnInterruptOnCompletedTurn"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode errors-observed.json: %v", err)
	}
	e := doc.TurnInterruptOnCompletedTurn.Error
	if e.Code == 0 || e.Message == "" {
		t.Fatal("errors-observed.json carries no turnInterruptOnCompletedTurn error; this test drives " +
			"a RECORDED frame and may not invent one")
	}
	return struct {
		code    int
		message string
	}{e.Code, e.Message}
}

// ---------------------------------------------------------------------------
// The server-initiated request, arriving UNSOLICITED
// ---------------------------------------------------------------------------

// TestR7AppServer_AnUnsolicitedServerRequestReachesOnRequestAndIsAnsweredOnTheSameConnection
// is the whole of M4.3's transport. The approval arrives as a JSON-RPC SERVER-REQUEST on the
// daemon's own connection, correlated to nothing the daemon sent (RECORDED:
// approval-request.json, delivered to the observer at r1-codex-gate.md:125-131), and the reply
// must go out on THAT connection with THAT id, because JSON-RPC ids are per-connection.
func TestR7AppServer_AnUnsolicitedServerRequestReachesOnRequestAndIsAnsweredOnTheSameConnection(t *testing.T) {
	approval := r7ApprovalFixture(t)

	f := newFakeServer(t)
	answered := make(chan rpcFrame, 1)
	f.onOpen = func(c *fakeConn) {
		time.Sleep(50 * time.Millisecond)
		c.send(t, json.RawMessage(approval))
	}
	f.setHandler(func(c *fakeConn, fr rpcFrame) {
		if fr.Method == "" && len(fr.ID) > 0 {
			answered <- fr
		}
	})

	type seen struct {
		id     json.RawMessage
		method string
		params json.RawMessage
	}
	got := make(chan seen, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{
		OnRequest: func(id json.RawMessage, method string, params json.RawMessage) {
			got <- seen{id: id, method: method, params: params}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	var req seen
	select {
	case req = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the unsolicited server request never reached OnRequest. It correlates to nothing " +
			"the daemon sent, so a client that only routes frames carrying an id it is WAITING for " +
			"drops every approval on the floor and the phone never sees a card")
	}

	if req.method != "item/fileChange/requestApproval" {
		t.Fatalf("OnRequest saw method %q, want the RECORDED item/fileChange/requestApproval", req.method)
	}
	var params struct {
		ItemID string `json:"itemId"`
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal(req.params, &params); err != nil {
		t.Fatalf("decode the request params: %v", err)
	}
	if params.ItemID != "exec-29bcdedd-84f6-423c-931d-0f0433cc3328" {
		t.Errorf("params.itemId = %q, want the RECORDED value; the pending request is matched by "+
			"itemId, which is what ties the JSON-RPC id to the approval card", params.ItemID)
	}

	if err := c.Respond(ctx, req.id, json.RawMessage(`{"decision":"accept"}`)); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	select {
	case reply := <-answered:
		if string(reply.ID) != string(req.id) {
			t.Errorf("the reply carries id %s, want the id the server sent (%s). JSON-RPC ids are "+
				"PER-CONNECTION, so a reply with any other id answers nothing", reply.ID, req.id)
		}
		var body struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(reply.Result, &body) != nil || body.Decision != "accept" {
			t.Errorf("the reply result was %s, want {\"decision\":\"accept\"} (RECORDED at "+
				"r1-codex-gate.md:128)", reply.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Respond sent nothing back to the server")
	}
}

// TestR7AppServer_ServerRequestResolvedIsDeliveredAsANotification is what lets the surface
// that did NOT answer retire its card. First-answer-wins is SERVER-SIDE, so this broadcast is
// the daemon's only honest evidence that the request is over (RECORDED: frame-samples.json's
// serverRequest/resolved, and r1-codex-gate.md:129-131).
func TestR7AppServer_ServerRequestResolvedIsDeliveredAsANotification(t *testing.T) {
	f := newFakeServer(t)
	f.onOpen = func(c *fakeConn) {
		time.Sleep(50 * time.Millisecond)
		c.send(t, map[string]any{
			"method":      "serverRequest/resolved",
			"params":      map[string]any{"threadId": "01a00335-9a50-79e2-8253-e08861d67c4d", "requestId": 0},
			"emittedAtMs": 1786760261774,
		})
	}

	got := make(chan string, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{
		OnNotify: func(method string, _ json.RawMessage) { got <- method },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-got:
			if m == "serverRequest/resolved" {
				return
			}
		case <-deadline:
			t.Fatal("serverRequest/resolved never reached OnNotify; without it the phone's card and " +
				"the TUI's dialog both stay up after the OTHER surface answered")
		}
	}
}

// TestR7AppServer_CloseUnblocksEveryWaiterRatherThanLeakingThem is the crash arm. §R7.6 has
// the app-server dying mid-session as a real event; a Call parked on a dead connection is a
// daemon goroutine and a phone op that never resolves.
func TestR7AppServer_CloseUnblocksEveryWaiterRatherThanLeakingThem(t *testing.T) {
	f := newFakeServer(t)
	f.setHandler(func(*fakeConn, rpcFrame) {}) // never answers

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{
		OnClose: func(err error) { closed <- err },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	callDone := make(chan error, 1)
	go func() { callDone <- c.Call(ctx, "turn/start", map[string]any{}, nil) }()
	time.Sleep(100 * time.Millisecond)
	_ = c.Close()

	select {
	case err := <-callDone:
		if err == nil {
			t.Error("a Call parked on a connection that was closed returned nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close left a Call parked forever; that is a leaked daemon goroutine and a phone op " +
			"that never resolves")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("OnClose never fired; §R7.6 needs the daemon to LEARN that the backend died")
	}
}

// r7ApprovalFixture returns the RECORDED server-request frame verbatim.
func r7ApprovalFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../docs/verification/r1-codex-fixtures/approval-request.json")
	if err != nil {
		t.Fatalf("read approval-request.json: %v", err)
	}
	return data
}

// r7ShortDir is a scratch dir under /tmp rather than t.TempDir(). macOS's $TMPDIR plus a
// test-name-derived subdirectory blows past the 104-byte sun_path limit and every bind here
// fails with EINVAL; the repo's established workaround for a UDS-binding test is a short
// os.MkdirTemp prefix (internal/hookclient, internal/remotegw, internal/skeleton all do this).
func r7ShortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swas")
	if err != nil {
		t.Fatalf("short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
