package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for the audit committee's round-4 finding F6 (Opus):
// App.run's teardown at the end of every transport generation used the GRACEFUL
// relay Close, whose contract waits up to five seconds for the peer's close frame.
// The teardown is an ABANDONMENT -- the generation is over, for backgrounding or for a
// dead link -- and relay.Conn.CloseNow is documented for exactly this caller ("a caller
// that is ABANDONING the exchange rather than finishing it").
//
// WHERE THE FIVE SECONDS IS ACTUALLY PAID, measured rather than assumed: the graceful
// close is CHEAP while the connection's pump is parked in a read (cancelling the read
// hard-closes the socket through the websocket library's own timeout loop), and cheap
// when the socket is already dead. It costs the full five seconds precisely when the
// pump has EXITED and the peer is SILENT -- waitCloseHandshake then reads the socket
// itself and nothing ever answers. That state is not exotic: it is what a malformed
// frame leaves behind (the pump forwards the decode error and returns) on a link that
// has gone dark -- and the reconnect that should be redialling instead spends five
// seconds saying goodbye to a peer that provably is not listening. The first test
// drives exactly that shape and bounds the redial. The second bounds the lifecycle
// path Opus F6 names: background (Stop) then immediate foreground (Start), where Stop
// joins the generation whose teardown runs the close -- on the facade's serial command
// lane.
//
// The graceful Close survives where an orderly goodbye is the point: the pairing
// probe's finished exchange, the machines manager and push gateway on App.Close
// (process exit), and the gateway sidecar's own shutdown.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// wedgeProxy fronts the real relay for the phone, forwarding verbatim -- until Wedge is
// called on the CURRENT connection: it then delivers one malformed binary message to the
// phone (relay.ReadFrame fails, so the phone's pump forwards the decode error and exits)
// and goes silent in both directions with the TCP connections still open. That is the
// dead-pump-plus-silent-peer state in which, and only in which, the graceful close pays
// its full five-second handshake wait. Connections accepted after the wedge forward
// verbatim again, and their accept times are recorded: the gap between the wedge and the
// next accept IS the phone's teardown-plus-backoff resume cost.
type wedgeProxy struct {
	srv      *httptest.Server
	upstream string

	mu      sync.Mutex
	wedged  chan struct{} // closed by Wedge; the current connection's cue
	accepts []time.Time
}

func newWedgeProxy(t *testing.T, upstream string) *wedgeProxy {
	t.Helper()
	p := &wedgeProxy{upstream: upstream, wedged: make(chan struct{})}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.accepts = append(p.accepts, time.Now())
		wedge := p.wedged
		p.mu.Unlock()

		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		up, _, err := websocket.Dial(ctx, p.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		var wmu sync.Mutex
		write := func(c *websocket.Conn, mt websocket.MessageType, b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			return c.Write(ctx, mt, b)
		}

		done := make(chan struct{}, 2)
		forward := func(src, dst *websocket.Conn) {
			defer func() { done <- struct{}{} }()
			for {
				// The wedge wins over a concurrently-read frame: after it, NOTHING
				// crosses in either direction and neither socket is read again, so
				// the library on this side never answers the phone's close frame.
				select {
				case <-wedge:
					return
				default:
				}
				mt, data, err := src.Read(ctx)
				if err != nil {
					return
				}
				select {
				case <-wedge:
					return
				default:
				}
				if err := write(dst, mt, data); err != nil {
					return
				}
			}
		}
		go forward(up, down)
		go forward(down, up)

		select {
		case <-wedge:
			// One malformed frame: the phone's pump forwards the decode failure and
			// exits, leaving no reader parked on the socket. Then silence, sockets open.
			_ = write(down, websocket.MessageBinary, []byte{0xff, 0xfe, 0xfd})
			<-ctx.Done()
		case <-ctx.Done():
		}
		<-done
		<-done
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *wedgeProxy) URL() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// Wedge cues the malformed frame + silence on the currently-proxied connection and
// returns the moment of the cue. Later connections forward verbatim.
func (p *wedgeProxy) Wedge() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.wedged)
	p.wedged = make(chan struct{}) // connections accepted after the cue get a fresh, open cue
	return time.Now()
}

// acceptsSince counts connections accepted after t, returning the first such accept time.
func (p *wedgeProxy) acceptsSince(t time.Time) (int, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var first time.Time
	n := 0
	for _, at := range p.accepts {
		if at.After(t) {
			if n == 0 {
				first = at
			}
			n++
		}
	}
	return n, first
}

// redialBudget separates the two worlds by a wide margin: the abandoning teardown plus
// the first reconnect backoff (500 ms +/-20%) lands the redial in well under 1.5 s, while
// the graceful close against this silent peer waits its five seconds BEFORE the backoff
// even starts (>5.5 s total, deterministically). 3.5 s tolerates a heavily loaded host on
// the fast path and still cannot be reached through the handshake wait.
const redialBudget = 3500 * time.Millisecond

// TestCommitteeR4_RedialDoesNotWaitForTheDeadConnectionsCloseHandshake: a link that dies
// in the dead-pump shape (one malformed frame, then silence) must be redialled on the
// reconnect schedule, not five seconds late -- the teardown of a connection the phone is
// abandoning must be CloseNow (Opus round-4 F6).
func TestCommitteeR4_RedialDoesNotWaitForTheDeadConnectionsCloseHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	proxyHolder := &struct{ p *wedgeProxy }{}
	app, _ := committeeRig(t, ctx, func(realURL string) string {
		proxyHolder.p = newWedgeProxy(t, realURL)
		return proxyHolder.p.URL()
	})
	proxy := proxyHolder.p
	online := func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "online"
	}
	r9Eventually(t, "the phone never came online through the proxy", online)

	wedgedAt := proxy.Wedge()
	r9Eventually(t, "the phone never redialled after the link died in the dead-pump shape", func() bool {
		n, _ := proxy.acceptsSince(wedgedAt)
		return n > 0
	})
	_, first := proxy.acceptsSince(wedgedAt)
	if gap := first.Sub(wedgedAt); gap >= redialBudget {
		t.Fatalf("the redial arrived %v after the link died; the teardown of the dead "+
			"connection paid the graceful close's handshake wait (~5 s against a silent "+
			"peer) before the reconnect backoff could even start. An abandoned connection "+
			"must be severed with CloseNow (Opus round-4 F6)", gap)
	}
	r9Eventually(t, "the phone never came back online after the redial", online)
}

// stopBudget bounds the background half of the lifecycle: Stop joins the transport
// generation, so whatever the teardown waits for is time spent on the facade's serial
// command lane. The CloseNow teardown does no network wait at all (goroutine wind-down
// only), so 2.5 s is generous headroom under load and still half the close-handshake
// wait it exists to keep off this path.
const stopBudget = 2500 * time.Millisecond

// TestCommitteeR4_BackgroundForegroundResumeDoesNotPayTheCloseHandshake drives the
// lifecycle Opus F6 names: online, background (Stop), immediate foreground (Start),
// online again -- with the background half bounded.
func TestCommitteeR4_BackgroundForegroundResumeDoesNotPayTheCloseHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	app, _ := committeeRig(t, ctx, func(realURL string) string { return realURL })
	online := func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "online"
	}
	r9Eventually(t, "the phone never came online against the in-process relay", online)

	start := time.Now()
	if err := app.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	stopped := time.Since(start)

	if err := app.Start(); err != nil {
		t.Fatalf("App.Start after background: %v", err)
	}
	r9Eventually(t, "the phone never came back online after a background -> foreground cycle", online)

	if stopped >= stopBudget {
		t.Fatalf("Stop() took %v; the background teardown waited on the connection it was "+
			"abandoning, on the serial command lane (Opus round-4 F6)", stopped)
	}
}
