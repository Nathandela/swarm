package main

// PB-NET-8 OWNS THIS FILE'S SUBJECT as of 2026-07-31. The tests were written against
// PB-NET-4, whose text says "automatic reconnect" without naming a hop and whose fences are
// the PHONE's; the machine hop had no row at all, which is why its recovery mechanism was
// absent for the whole project (ADR-007 B120/F1). The row now exists and cites this file, so
// the id is named here: a fence nobody can find by grepping the requirement is a fence the
// next audit re-derives from scratch.
//
// PB-NET-4 / ADR-007 section 6.0 ("client reconnect backoff + jitter on BOTH hops"),
// MACHINE half, FAILING FIRST.
//
// THE DEFECT. run() once dialed a relay connection and handed it to remotegw.Service
// for the life of the process. There is no second dial anywhere in the gateway, and
// nothing observes the first one dying: Service.Run's journal loop reconnects to the
// DAEMON, and the command loop retries the same dead relay client forever. The process
// therefore never exits, so neither launchd's KeepAlive{SuccessfulExit:false} nor
// systemd's Restart=on-failure ever runs -- a supervision policy written against EXIT
// cannot restart a zombie.
//
// The rig is a native-v2 relay harness behind a websocket proxy that can SEVER every
// live connection on demand, which is a desktop WiFi blip reproduced exactly: the
// gateway's socket dies while every durable coordinate stays untouched.
//
// Nothing here names a symbol that does not exist yet -- the fence is on run(), the
// sidecar's own entry point, exactly as B34's and B37's are.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

// cutTap is a websocket proxy in front of the relay harness. It counts DIALS (one per
// upgrade request, so a redial is visible even when it fails during the handshake),
// records what the gateway wrote, and can sever every live connection -- either on demand
// (cut) or automatically, killAfter into each connection's life.
//
// It works at the websocket layer rather than over raw TCP for gwTap's reason: client ->
// server frames are masked, so an op name is not literally present on the wire.
type cutTap struct {
	srv       *httptest.Server
	upstream  string
	killAfter time.Duration // 0 => connections live until cut() or the test ends

	mu    sync.Mutex
	dials int
	sent  bytes.Buffer
	live  []context.CancelFunc
}

func newCutTap(t *testing.T, upstreamWS string, killAfter time.Duration) *cutTap {
	t.Helper()
	tap := &cutTap{upstream: upstreamWS, killAfter: killAfter}
	tap.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tap.mu.Lock()
		tap.dials++
		tap.mu.Unlock()

		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tap.mu.Lock()
		tap.live = append(tap.live, cancel)
		tap.mu.Unlock()
		if tap.killAfter > 0 {
			timer := time.AfterFunc(tap.killAfter, cancel)
			defer timer.Stop()
		}

		up, _, err := websocket.Dial(ctx, tap.upstream+r.URL.RequestURI(), nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		// The severance has to reach the SOCKETS: cancelling the copy contexts alone
		// leaves both peers holding an open connection that simply goes quiet, which is a
		// different failure (a half-open link) from the one under test.
		go func() {
			<-ctx.Done()
			_ = down.CloseNow()
			_ = up.CloseNow()
		}()

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil || down.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				tap.mu.Lock()
				tap.sent.Write(data)
				tap.mu.Unlock()
				if up.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(tap.srv.Close)
	return tap
}

func (c *cutTap) url() string { return "ws" + strings.TrimPrefix(c.srv.URL, "http") }

func (c *cutTap) dialCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dials
}

// wrote reports whether the gateway has sent the named op on ANY connection so far.
func (c *cutTap) wrote(op string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Contains(c.sent.Bytes(), []byte(op))
}

func (c *cutTap) writeCount(op string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Count(c.sent.Bytes(), []byte(op))
}

// cut severs every live connection: the blip.
func (c *cutTap) cut() {
	c.mu.Lock()
	live := c.live
	c.live = nil
	c.mu.Unlock()
	for _, cancel := range live {
		cancel()
	}
}

// startCutTapRelay stands up a native-v2 relay harness behind a severable tap.
func startCutTapRelay(t *testing.T, killAfter time.Duration) (*cutTap, <-chan struct{}) {
	t.Helper()
	subscribed := make(chan struct{}, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		read := func() map[string]any {
			_, body, err := ws.Read(r.Context())
			if err != nil {
				return nil
			}
			var frame map[string]any
			if json.Unmarshal(body, &frame) != nil {
				return nil
			}
			return frame
		}
		write := func(frame map[string]any) bool {
			body, _ := json.Marshal(frame)
			return ws.Write(r.Context(), websocket.MessageText, body) == nil
		}
		init := read()
		if init == nil {
			return
		}
		machineRID := r.URL.Query().Get("machine_rid")
		home := relayv2.HomeID("owner", machineRID)
		if !write(map[string]any{
			"v": 2, "type": "CHALLENGE", "request_id": init["request_id"], "home": home,
			"nonce":      base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
			"expires_at": fmt.Sprint(time.Now().Add(30 * time.Second).UnixMilli()),
		}) {
			return
		}
		prove := read()
		if prove == nil || !write(map[string]any{
			"v": 2, "type": "AUTHENTICATED", "request_id": prove["request_id"],
			"rid": machineRID, "role": "machine", "purpose": init["purpose"], "home": home,
		}) {
			return
		}
		if init["purpose"] == "control" {
			authorize := read()
			if authorize == nil {
				return
			}
			phonePub, _ := base64.RawURLEncoding.DecodeString(fmt.Sprint(authorize["phone_pub"]))
			if !write(map[string]any{
				"v": 2, "type": "AUTHORIZED", "request_id": authorize["request_id"],
				"phone_rid": relayv2.RoutingID(phonePub), "generation": "1",
			}) {
				return
			}
			// Keep the server side alive until the client consumes AUTHORIZED and closes
			// its one-shot control connection. CloseNow here can discard the response
			// before the proxy has forwarded it.
			_, _, _ = ws.Read(r.Context())
			return
		}
		subscribe := read()
		if subscribe == nil {
			return
		}
		if !write(map[string]any{
			"v": 2, "type": "SUBSCRIBED", "request_id": subscribe["request_id"],
			"peer_rid": subscribe["peer_rid"], "generation": subscribe["generation"],
			"incarnation": "AAAAAAAAAAAAAAAAAAAAAA", "after": subscribe["after"],
		}) {
			return
		}
		subscribed <- struct{}{}
		_, _, _ = ws.Read(r.Context())
	}))
	t.Cleanup(srv.Close)
	return newCutTap(t, "ws"+strings.TrimPrefix(srv.URL, "http"), killAfter), subscribed
}

// captureStderr redirects the process's stderr for the duration of one test and returns
// what was written to it. The sidecar reports through os.Stderr because that is what a
// systemd/launchd unit captures; a test that wants to read the report has to read the
// same channel an operator does.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w

	var mu sync.Mutex
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf.Write(chunk[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	var once sync.Once
	stop := func() string {
		once.Do(func() {
			os.Stderr = saved
			_ = w.Close()
			<-drained
			_ = r.Close()
		})
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// waitUntil polls cond until it holds or the budget elapses. Budgets here are generous by
// design: what is being asserted is that something happens AT ALL, never how fast.
func waitUntil(budget time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// TestPBNET4_TheGatewayRedialsTheRelayAfterTheLinkDrops is the fence. The gateway is
// allowed to establish a WORKING generation first -- proved by SUBSCRIBE reaching the
// relay through the tap -- so a redial cannot be confused with
// a dial that never succeeded. Then the link is cut with the context still alive.
func TestPBNET4_TheGatewayRedialsTheRelayAfterTheLinkDrops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tap, subscribed := startCutTapRelay(t, 0)
	stderr := captureStderr(t)
	p := gatewayParamsFor(t, tap.url())

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	out := make(chan error, 1)
	go func() { out <- run(runCtx, p) }()

	// The premise: a LIVE generation. The server must have accepted SUBSCRIBE and the
	// client must have durably adopted its incarnation before the link is cut. That places
	// the failure after startup, where MachineMailbox.Done drives the reconnect.
	select {
	case <-subscribed:
	case <-time.After(30 * time.Second):
		t.Fatalf("the gateway never reached a working relay generation (%d dials); the fence "+
			"below would be vacuous", tap.dialCount())
	}
	if !waitUntil(30*time.Second, func() bool {
		return p.Inbound.Load().Incarnation == "AAAAAAAAAAAAAAAAAAAAAA"
	}) {
		t.Fatal("the gateway did not durably adopt the accepted subscription")
	}
	select {
	case err := <-out:
		t.Fatalf("the accepted generation ended before the cut: %v", err)
	default:
	}
	before := tap.dialCount()
	beforeAuth := tap.writeCount("AUTH_INIT")

	tap.cut() // the desktop's WiFi blips

	if !waitUntil(45*time.Second, func() bool { return tap.dialCount() > before }) {
		t.Fatalf("the gateway did NOT redial the relay after its only connection was cut "+
			"(still %d dial(s), context still alive): remote control is over until a human "+
			"restarts the sidecar, and the process never exits so no supervision policy will "+
			"(PB-NET-4, ADR-007 section 6.0 -- backoff and jitter on BOTH hops)", tap.dialCount())
	}
	if !waitUntil(30*time.Second, func() bool { return tap.writeCount("AUTH_INIT") > beforeAuth }) {
		t.Fatal("the redial never re-authenticated")
	}

	// PB-NET-4's other half, and ADR-007 B114's: the three components that STORE a degraded
	// state (CommandBridge.Err, RelaySink.Err, PushNotifier.Err) have no reader anywhere in
	// the tree, so an operator learns nothing today. A link loss the sidecar recovers from
	// is still a link loss an operator must be able to see in the unit's log.
	runCancel()
	select {
	case <-out:
	case <-time.After(30 * time.Second):
		t.Fatal("run() did not return within 30s of cancel")
	}
	if got := stderr(); !strings.Contains(got, "relay") {
		t.Fatalf("the sidecar reported NOTHING on stderr about losing and redialling its relay "+
			"connection; the unit's log is the only channel an operator has. stderr was: %q", got)
	}
}

// TestPBNET4_TheGatewayBacksOffBetweenRedialsRatherThanHammering pins the SHAPE of the
// retry, not its speed. Every connection is severed shortly after it is established, so
// no generation ever makes progress -- and section 6.0's schedule (500ms, doubling, 30s
// ceiling) must therefore keep growing.
//
// The bound is deliberately loose, at more than twice the count a correct schedule
// produces in the window, because this host fails tight wall-clock assertions under load.
// What it catches is the shape that is NOT a backoff: a reset fired by a successful DIAL
// rather than by evidence of progress turns a link that connects and immediately dies
// into a fixed-rate redial the adversary drives for free.
func TestPBNET4_TheGatewayBacksOffBetweenRedialsRatherThanHammering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tap, _ := startCutTapRelay(t, 500*time.Millisecond)

	runCtx, runCancel := context.WithTimeout(ctx, 20*time.Second)
	defer runCancel()
	out := make(chan error, 1)
	go func() { out <- run(runCtx, gatewayParamsFor(t, tap.url())) }()
	select {
	case <-out:
	case <-time.After(60 * time.Second):
		t.Fatal("run() did not return within 60s of its context expiring")
	}

	// A correct schedule starts generations at roughly 0, 1.0, 2.5, 5.0, 9.5 and 18.0
	// seconds: about twelve sockets, because native v2 uses control plus stream. A fixed
	// 500ms retry -- or a backoff reset by every successful stream dial -- produces forty
	// or more sockets in the same window.
	const window = 20 * time.Second
	const hammering = 24 // native v2 uses one control and one stream socket per generation
	switch n := tap.dialCount(); {
	case n < 2:
		t.Fatalf("the gateway dialled %d time(s) in %v while its connection was severed every "+
			"500ms: it never redials at all (PB-NET-4, ADR-007 section 6.0)", n, window)
	case n > hammering:
		t.Fatalf("the gateway dialled %d times in %v, more than the %d a section 6.0 backoff "+
			"(500ms initial, factor 2, 30s ceiling) can produce: the retry has no growth, or "+
			"its backoff is reset by a successful DIAL rather than by evidence of progress -- "+
			"which is a fixed-rate redial against a relay that connects and answers nothing",
			n, window, hammering)
	}
}
