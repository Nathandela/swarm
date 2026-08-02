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
// THE DEFECT. run() calls relay.DialSecure ONCE and hands the client to remotegw.Service
// for the life of the process. There is no second dial anywhere in the gateway, and
// nothing observes the first one dying: Service.Run's journal loop reconnects to the
// DAEMON, and the command loop retries the same dead relay client forever. The process
// therefore never exits, so neither launchd's KeepAlive{SuccessfulExit:false} nor
// systemd's Restart=on-failure ever runs -- a supervision policy written against EXIT
// cannot restart a zombie.
//
// The rig is the real relay behind a websocket proxy that can SEVER every live connection
// on demand, which is a desktop WiFi blip reproduced exactly: the gateway's socket dies
// while the relay, the phone's mailbox and every durable coordinate stay untouched.
//
// Nothing here names a symbol that does not exist yet -- the fence is on run(), the
// sidecar's own entry point, exactly as B34's and B37's are.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// cutTap is a websocket proxy in front of the real relay. It counts DIALS (one per
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

		up, _, err := websocket.Dial(ctx, tap.upstream, nil)
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

// startCutTapRelay stands up the real in-process relay behind a severable tap.
func startCutTapRelay(ctx context.Context, t *testing.T, killAfter time.Duration) *cutTap {
	t.Helper()
	rcfg := relay.DefaultConfig()
	rcfg.Listen = "127.0.0.1:0"
	rcfg.TLSMode = "off"
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return newCutTap(t, srv.URL(), killAfter)
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
// allowed to establish a WORKING generation first -- proved by the command loop's
// mailbox_wait reaching the relay through the tap -- so a redial cannot be confused with
// a dial that never succeeded. Then the link is cut with the context still alive.
func TestPBNET4_TheGatewayRedialsTheRelayAfterTheLinkDrops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tap := startCutTapRelay(ctx, t, 0)
	stderr := captureStderr(t)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	out := make(chan error, 1)
	go func() { out <- run(runCtx, gatewayParamsFor(t, tap.url())) }()

	// The premise: a LIVE generation. mailbox_wait is the command loop's own frame, so it
	// crossing the tap proves the sidecar is authenticated and serving on this connection.
	if !waitUntil(30*time.Second, func() bool { return tap.wrote("mailbox_wait") }) {
		t.Fatalf("the gateway never reached a working relay generation (%d dials); the fence "+
			"below would be vacuous", tap.dialCount())
	}
	before := tap.dialCount()

	tap.cut() // the desktop's WiFi blips

	if !waitUntil(45*time.Second, func() bool { return tap.dialCount() > before }) {
		t.Fatalf("the gateway did NOT redial the relay after its only connection was cut "+
			"(still %d dial(s), context still alive): remote control is over until a human "+
			"restarts the sidecar, and the process never exits so no supervision policy will "+
			"(PB-NET-4, ADR-007 section 6.0 -- backoff and jitter on BOTH hops)", tap.dialCount())
	}
	if !waitUntil(30*time.Second, func() bool { return tap.wrote("auth_init") && tap.dialCount() > before }) {
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
	tap := startCutTapRelay(ctx, t, 500*time.Millisecond)

	runCtx, runCancel := context.WithTimeout(ctx, 20*time.Second)
	defer runCancel()
	out := make(chan error, 1)
	go func() { out <- run(runCtx, gatewayParamsFor(t, tap.url())) }()
	select {
	case <-out:
	case <-time.After(60 * time.Second):
		t.Fatal("run() did not return within 60s of its context expiring")
	}

	// A correct schedule dials at roughly 0, 1.0, 2.5, 5.0, 9.5 and 18.0 seconds: six.
	// A fixed 500ms retry -- or a backoff reset by every successful dial -- produces
	// twenty or more in the same window.
	const window = 20 * time.Second
	const hammering = 12
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
