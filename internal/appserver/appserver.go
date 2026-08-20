// Package appserver is the DAEMON-OWNED JSON-RPC client for `codex app-server
// --listen unix://PATH` (Wave R7, Mirror M4.2/M4.3; ADR-013 §R7.2d).
//
// It holds the connection, the request-id correlation and the server-request delivery, and
// NOTHING ELSE. It imports neither internal/adapter nor internal/daemon: the adapter shapes
// content and owns no fd, the daemon owns lifecycle, and this package owns one socket.
//
// THE SINGLE MOST IMPORTANT INTEGRATION DETAIL IN R7, and it cost the R1 feasibility gate ten
// minutes of its timebox to find (docs/verification/r1-codex-gate.md:32-37):
//
//	The `--listen unix://PATH` endpoint is a WEBSOCKET endpoint, not a raw JSON-lines
//	socket. A client must perform an HTTP/1.1 upgrade (GET /rpc, Upgrade: websocket,
//	Sec-WebSocket-Version: 13) over the UDS and then exchange JSON-RPC messages as
//	WebSocket TEXT frames. Writing bare newline-delimited JSON to the socket produces
//	TOTAL SILENCE: no response, no error, no server log.
//
// `codex app-server proxy --sock` is NOT the bridge to that endpoint (gate correction 2).
// The recorded upgrade bytes are r1-codex-fixtures/ws-handshake.txt, captured off the real
// TUI's own connection through a logging MITM on the UDS.
//
// TWO CORRELATION RULES THE RECORDED CORPUS FORCES, and a naive client gets both wrong:
//
//   - "the next frame is my answer" is FALSE on the very first exchange of every session --
//     frame-samples.json shows a `remoteControl/status/changed` notification arriving between
//     `initialize` and its reply -- so replies are matched BY ID.
//   - an approval arrives as an UNSOLICITED SERVER REQUEST correlated to nothing the client
//     sent, so a client that only routes frames carrying an id it is WAITING for drops every
//     approval on the floor.
package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// RPCPath is the app-server's WebSocket endpoint. RECORDED: ws-handshake.txt's first line is
// `GET /rpc HTTP/1.1`.
const RPCPath = "/rpc"

// defaultDialTimeout bounds the connect + upgrade when Options names none. A dial with no
// bound turns "no response, no error, no server log" into a daemon goroutine parked forever
// while the phone shows an empty transcript.
const defaultDialTimeout = 10 * time.Second

// maxFrameBytes bounds one inbound WebSocket message. A turn's aggregatedOutput can be large;
// an unbounded read is an allocation a peer controls.
const maxFrameBytes = 8 << 20

// ErrClosed is returned to every caller parked on a connection that went away.
var ErrClosed = errors.New("appserver: connection closed")

// RPCError is a JSON-RPC error frame, surfaced TYPED rather than flattened to a string.
// Two of the three errors the gate recorded are load-bearing SEMANTICS rather than failures --
// `no active turn to interrupt` is benign (§R7.5) and `no rollout found for thread id` is the
// join race (§R7.2e) -- and a caller that could not tell either from a transport fault would
// report a working Stop as a failure.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("appserver: rpc error %d: %s", e.Code, e.Message)
}

// Options configures a Dial.
type Options struct {
	// DialTimeout bounds the connect and the HTTP upgrade (0 => defaultDialTimeout).
	DialTimeout time.Duration
	// OnNotify receives every server->client NOTIFICATION (a frame with a method and no id).
	// It runs on the read loop's goroutine, so it must not block for long.
	OnNotify func(method string, params json.RawMessage)
	// OnRequest receives every server->client REQUEST (a frame with BOTH a method and an id).
	// The client must answer it with Respond, carrying the SAME id, on THIS connection.
	OnRequest func(id json.RawMessage, method string, params json.RawMessage)
	// OnClose fires exactly once when the read loop ends, with the reason.
	OnClose func(error)
}

// Client is one connection to one app-server.
type Client struct {
	ws   *websocket.Conn
	opt  Options
	done chan struct{}

	writeMu sync.Mutex // one writer at a time: a WebSocket message must not interleave

	mu      sync.Mutex
	nextID  int64
	waiters map[string]chan rpcReply
	closed  bool
	closeCb sync.Once
}

// rpcReply is one correlated answer handed back to a parked Call.
type rpcReply struct {
	result json.RawMessage
	err    error
}

// wireFrame is the JSON-RPC envelope in both directions. `id` stays RAW because the server's
// ids are its own (the recorded approval carries `"id": 0`) and a reply must echo the exact
// bytes it received.
type wireFrame struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Dial connects to the app-server's UDS and performs the recorded WebSocket upgrade. It
// returns only once the connection is usable, and it FAILS rather than hanging when the peer
// accepts and then says nothing -- which is exactly what a wrong-protocol write produces.
func Dial(ctx context.Context, socketPath string, opt Options) (*Client, error) {
	timeout := opt.DialTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The HTTP client dials the UDS; the URL's host is a placeholder the server ignores
	// (RECORDED: `Host: localhost`).
	httpc := &http.Client{
		Transport: recordedHeaderCase{inner: &http.Transport{
			DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(c, "unix", socketPath)
			},
		}},
	}
	ws, _, err := websocket.Dial(dialCtx, "ws://localhost"+RPCPath, &websocket.DialOptions{
		HTTPClient: httpc,
	})
	if err != nil {
		return nil, fmt.Errorf("appserver: upgrade %s%s: %w", socketPath, RPCPath, err)
	}
	ws.SetReadLimit(maxFrameBytes)

	c := &Client{
		ws:      ws,
		opt:     opt,
		done:    make(chan struct{}),
		waiters: map[string]chan rpcReply{},
	}
	go c.readLoop()
	return c, nil
}

// recordedHeaderCase restores the WebSocket handshake header names to the EXACT spelling
// r1-codex-fixtures/ws-handshake.txt captured off the real TUI's connection --
// `Sec-WebSocket-Key`, not Go's canonical `Sec-Websocket-Key`.
//
// RFC 7230 makes header names case-insensitive, so in principle this is cosmetic. In
// practice the recorded bytes are the ONE upgrade shape known to work against this server,
// and the failure mode when an upgrade is not accepted is the recorded silence -- no
// response, no error, no server log. Matching the capture costs four map renames and removes
// a dependency on the server's parser being as case-insensitive as the RFC.
//
// http.Header.writeSubset writes the map's keys verbatim, and coder/websocket verifies
// Sec-WebSocket-Accept against a LOCAL copy of the key rather than re-reading the header, so
// the rename is invisible to both ends of the library.
type recordedHeaderCase struct{ inner http.RoundTripper }

// recordedNames maps Go's canonical form to the recorded form.
var recordedNames = map[string]string{
	"Sec-Websocket-Key":        "Sec-WebSocket-Key",
	"Sec-Websocket-Version":    "Sec-WebSocket-Version",
	"Sec-Websocket-Protocol":   "Sec-WebSocket-Protocol",
	"Sec-Websocket-Extensions": "Sec-WebSocket-Extensions",
}

func (r recordedHeaderCase) RoundTrip(req *http.Request) (*http.Response, error) {
	for canonical, recorded := range recordedNames {
		if v, ok := req.Header[canonical]; ok {
			delete(req.Header, canonical)
			req.Header[recorded] = v
		}
	}
	return r.inner.RoundTrip(req)
}

// readLoop dispatches every inbound frame until the connection ends. It is the ONLY reader.
func (c *Client) readLoop() {
	var reason error
	for {
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			reason = err
			break
		}
		if typ != websocket.MessageText {
			continue // the protocol is TEXT frames; anything else is not ours
		}
		var fr wireFrame
		if json.Unmarshal(data, &fr) != nil {
			continue // a frame this revision cannot parse is dropped, never fatal
		}
		c.dispatch(fr)
	}
	c.finish(reason)
}

// dispatch routes ONE decoded frame. The three shapes are distinguished exactly as the wire
// distinguishes them: method+id is a server REQUEST, method alone is a NOTIFICATION, and id
// alone is the answer to something we sent.
func (c *Client) dispatch(fr wireFrame) {
	switch {
	case fr.Method != "" && len(fr.ID) > 0:
		if c.opt.OnRequest != nil {
			c.opt.OnRequest(append(json.RawMessage(nil), fr.ID...), fr.Method, fr.Params)
		}
	case fr.Method != "":
		if c.opt.OnNotify != nil {
			c.opt.OnNotify(fr.Method, fr.Params)
		}
	case len(fr.ID) > 0:
		c.deliver(string(fr.ID), rpcReply{result: fr.Result, err: errFor(fr.Error)})
	}
}

// errFor converts a JSON-RPC error member into a typed error, or nil.
func errFor(e *RPCError) error {
	if e == nil {
		return nil
	}
	return e
}

// deliver hands a reply to the Call parked on that id, if any.
func (c *Client) deliver(id string, r rpcReply) {
	c.mu.Lock()
	ch, ok := c.waiters[id]
	delete(c.waiters, id)
	c.mu.Unlock()
	if ok {
		ch <- r
	}
}

// finish releases EVERY parked Call and fires OnClose once. A Call left parked on a dead
// connection is a leaked daemon goroutine and a phone op that never resolves (§R7.6).
func (c *Client) finish(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	waiters := c.waiters
	c.waiters = map[string]chan rpcReply{}
	c.mu.Unlock()

	if reason == nil {
		reason = ErrClosed
	}
	for _, ch := range waiters {
		ch <- rpcReply{err: reason}
	}
	close(c.done)
	c.closeCb.Do(func() {
		if c.opt.OnClose != nil {
			c.opt.OnClose(reason)
		}
	})
}

// Call sends a request and blocks until its reply arrives, the context ends, or the
// connection closes. out, when non-nil, receives the result member.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("appserver: marshal %s params: %w", method, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	key := fmt.Sprintf("%d", id)
	ch := make(chan rpcReply, 1)
	c.waiters[key] = ch
	c.mu.Unlock()

	if err := c.write(ctx, wireFrame{
		JSONRPC: "2.0", ID: json.RawMessage(key), Method: method, Params: body,
	}); err != nil {
		c.mu.Lock()
		delete(c.waiters, key)
		c.mu.Unlock()
		return err
	}

	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out == nil || len(r.result) == 0 {
			return nil
		}
		return json.Unmarshal(r.result, out)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.waiters, key)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
}

// Notify sends a request that expects no reply (`initialized`).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("appserver: marshal %s params: %w", method, err)
	}
	return c.write(ctx, wireFrame{JSONRPC: "2.0", Method: method, Params: body})
}

// Respond answers a server-initiated request. The id MUST be the bytes that connection
// received: JSON-RPC ids are PER-CONNECTION, so a reply carrying any other id answers
// nothing and the agent stays blocked.
func (c *Client) Respond(ctx context.Context, id json.RawMessage, result any) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("appserver: marshal response: %w", err)
	}
	return c.write(ctx, wireFrame{JSONRPC: "2.0", ID: id, Result: body})
}

// write serializes one frame as a WebSocket TEXT message. One writer at a time.
func (c *Client) write(ctx context.Context, fr wireFrame) error {
	data, err := json.Marshal(fr)
	if err != nil {
		return err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// Close tears the connection down and unblocks every waiter.
func (c *Client) Close() error {
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	c.finish(ErrClosed)
	return err
}

// Done is closed once the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }
