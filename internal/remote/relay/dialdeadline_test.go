package relay

// FAILING-FIRST (TDD RED, GG-5) for the round-7 blocker at the ONE hop nothing bounds: the
// DIAL. This file contains NO implementation.
//
// THE DEFECT. dialConn applies no deadline of its own, and ALL THREE shipped callers hand it a
// cancellation-only context:
//
//	mobile/app.go        ctx, cancel := context.WithCancel(context.Background())  -> a.dial
//	cmd/swarm-remote     signal.NotifyContext(context.Background(), SIGINT, ...)  -> run
//	internal/skeleton    the pair_start handler's ctx (protocol/server.go's WithCancel of
//	                     Background) -> relayRendezvousFactory -> DialRawSecure
//
// None carries a deadline, so a relay that accepts the TCP connection and then STALLS --
// in the TLS handshake, before the HTTP response, halfway through the websocket upgrade --
// parks its caller for as long as it cares to hold the socket. The adversary does nothing:
// it accepts and goes quiet, which costs it one file descriptor.
//
// WHAT IT COSTS, on each side, differently:
//   - THE PHONE NEVER ENTERS BACKOFF. App.run's reconnect schedule (PB-NET-4) runs between
//     dial attempts, so a dial that never returns is a dial that never fails, and the retry
//     that would grow, jitter and eventually reach a working relay is never scheduled. The
//     handset shows "connecting" forever. mobile/conformance/pbnet4_stalleddial_test.go is
//     that half, over the real facade.
//   - THE GATEWAY IS STUCK FOR THE LIFE OF THE PROCESS. cmd/swarm-remote/main.go starts by
//     dialling, so a stalled dial is a sidecar that never starts and never says why.
//   - THE PAIRING RENDEZVOUS BURNS THE OWNER CONNECTION'S PAIRING SLOT, and it is the worst of
//     the three. The dial runs BEFORE pairing.go builds pairCtx, so ADR-007 B64's window is not
//     yet in force; the slot is already claimed and only `result` or BeginPairing's error return
//     frees it, a parked dial reaches neither, and there is no pair_cancel op.
//     internal/skeleton/pbnet7_stalleddial_test.go is that half, at the daemon.
//
// THIS IS THE THIRD INSTANCE OF ONE SHAPE, and each fix bounded only what the last probe
// happened to touch: B94 bounded the non-wait exchanges (DefaultCallTimeout), B115 bounded
// MailboxWait at the gateway, and the dial -- which happens BEFORE either, and without which
// neither bound is ever reached -- was never bounded at all.
//
// WHY THE BOUND BELONGS HERE AND NOT AT THE CALLERS. Three callers exist and all three got it
// wrong independently -- and only two of them were known when this file was written. That is the
// argument rather than a prediction about it: a per-caller fix would have gone to the set we
// happened to know, and that set was wrong. Every dial in the package funnels through dialConn,
// so that is the abstraction boundary and the only place a bound is quantified over dials rather
// than over the callers somebody remembered.
//
// THE RED IS BEHAVIOURAL, NOT A COMPILE FAILURE. Everything below compiles against the tree as
// it stands and fails by PARKING, which is the defect itself. The pin on the bound's numeric
// VALUE necessarily names a constant that does not exist yet, so it lands with the
// implementation rather than here.

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// dialStallBound is how long a test waits for a dial that must be bounded. It is far above
// every bound this file asserts and far below "forever": a dial still parked here is parked
// because nothing bounds it, not because the bound is generous.
//
// It is a LITERAL and it deliberately transcribes NO production constant. A test whose
// tolerance is derived from the value under test passes whatever that value becomes (ADR-007
// B113); this one fails a widened constant as readily as a missing one.
const dialStallBound = 60 * time.Second

// stallStage names the point in the dial handshake at which the peer stops.
//
// The stages are the whole handshake, in order, because a bound that covers only the socket
// leaves every later stall live -- and the later ones are the cheaper attack, since the peer
// has by then proved it is willing to talk.
type stallStage string

const (
	// stallTLSHandshake accepts the TCP connection and never sends a ServerHello.
	stallTLSHandshake stallStage = "TLS handshake"
	// stallHTTPResponse completes TCP, takes the upgrade request, and never answers it.
	stallHTTPResponse stallStage = "HTTP response"
	// stallUpgrade answers with a PARTIAL 101 and then goes quiet, so the client is parked
	// mid-header on a peer that has already agreed to switch protocols. No existing fixture
	// covers this: silentRelay (calldeadline_test.go) stalls AFTER the handshake completes.
	stallUpgrade stallStage = "websocket upgrade"
	// stallRelayAuth completes the whole websocket handshake and then answers no frame, so
	// the dial is parked in the relay-auth exchange.
	stallRelayAuth stallStage = "relay-auth exchange"
)

// newStallingPeer starts a peer that accepts and then stops at stage, and returns the URL to
// dial it at. Connections are held open until the test ends: nothing is closed, refused or
// reset, so there is no event for the client to observe and nothing for the OS to time out.
func newStallingPeer(t *testing.T, stage stallStage) string {
	t.Helper()

	if stage == stallRelayAuth {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer func() { _ = ws.CloseNow() }()
			// Read every frame the client sends and reply to none. The socket stays up and
			// the client's writes keep succeeding, so nothing about it looks wrong.
			for {
				if _, _, err := ws.Read(r.Context()); err != nil {
					return
				}
			}
		}))
		t.Cleanup(srv.Close)
		return "ws" + strings.TrimPrefix(srv.URL, "http")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
			go stallConn(c, stage)
		}
	}()

	scheme := "ws://"
	if stage == stallTLSHandshake {
		scheme = "wss://"
	}
	return scheme + ln.Addr().String()
}

// stallConn drives one accepted connection to its stage and then stops reading and writing.
func stallConn(c net.Conn, stage stallStage) {
	switch stage {
	case stallTLSHandshake, stallHTTPResponse:
		// Nothing is read and nothing is written. The client's ClientHello (wss) or its
		// upgrade request (ws) lands in the socket buffer and is never answered.
		return
	case stallUpgrade:
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimRight(line, "\r\n") == "" {
				break // end of the request head
			}
		}
		// A PARTIAL 101: the peer has agreed to switch protocols and then stops mid-header,
		// so the client is blocked reading a response head that will never be terminated.
		_, _ = c.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"))
	}
}

// securityFor is the transport policy a dial to url takes: the loopback cleartext carve-out
// for ws://, and ordinary platform-root verification for wss:// (which is never reached --
// the peer stalls before it presents a certificate).
func securityFor(url string) Security {
	if strings.HasPrefix(url, "wss://") {
		return Security{}
	}
	return Security{AllowLoopbackCleartext: true}
}

// TestDialDeadline_EveryStageOfTheDialIsBoundedWithoutACallerDeadline is the fence.
//
// THE CONTEXT IS context.Background(), verbatim what all three shipped callers supply: mobile's
// is a WithCancel of it, the gateway's is a signal.NotifyContext of it, the daemon's pair_start
// handler builds a WithCancel of it, and not one adds a deadline. A test that passed a deadline of its own would prove only that context deadlines
// work, which nobody doubted; the defect is that NOBODY DECLARES ONE.
//
// EACH STAGE IS ITS OWN SUBTEST because they fail in different code: the TLS handshake and the
// HTTP response are inside net/http's round trip, the upgrade is inside coder/websocket's
// header parse, and the relay-auth exchange is roundtrip's. A bound applied to one of them is
// a bound that leaves the others parked, which is precisely how this defect survived two
// previous fixes to its siblings.
//
// NO TIGHT WALL-CLOCK DURATION IS ASSERTED. The only timing claim is that the dial RETURNS
// inside a bound generous enough that a loaded host cannot fail it, and the substantive claim
// is on the ERROR: a dial that returns "connection refused" from some unrelated cause would
// satisfy "it returned" while proving nothing, so the error must report a DEADLINE.
func TestDialDeadline_EveryStageOfTheDialIsBoundedWithoutACallerDeadline(t *testing.T) {
	stages := []stallStage{stallTLSHandshake, stallHTTPResponse, stallUpgrade, stallRelayAuth}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			t.Parallel()
			url := newStallingPeer(t, stage)
			pub, priv := newRelayAuthKey(t)

			type result struct {
				cl  *Client
				err error
			}
			done := make(chan result, 1)
			start := time.Now()
			go func() {
				cl, err := DialSecure(context.Background(), url, authFor(pub, priv), securityFor(url))
				done <- result{cl, err}
			}()

			var got result
			select {
			case got = <-done:
			case <-time.After(dialStallBound):
				t.Fatalf("DialSecure is STILL PARKED after %v against a peer that stalls at the "+
					"%s, dialed with context.Background() -- verbatim the context all three "+
					"shipped callers supply (mobile/app.go's WithCancel, cmd/swarm-remote's "+
					"NotifyContext, the daemon pair_start handler's WithCancel). Nothing bounds "+
					"the dial, so the phone never reaches App.run's reconnect schedule, the "+
					"gateway never starts, and the pairing slot is never released",
					dialStallBound, stage)
			}
			elapsed := time.Since(start)

			if got.err == nil {
				_ = got.cl.Close()
				t.Fatalf("DialSecure SUCCEEDED after %v against a peer that stalls at the %s",
					elapsed, stage)
			}
			if !errors.Is(got.err, context.DeadlineExceeded) {
				t.Fatalf("DialSecure against a peer stalling at the %s returned %v after %v, "+
					"which is not a deadline. The dial has to END on a bound this side "+
					"declared -- an error from some other cause would leave the stall itself "+
					"unbounded and this assertion satisfied by accident",
					stage, got.err, elapsed)
			}
			t.Logf("stall at the %s: DialSecure returned after %v with %v", stage, elapsed, got.err)
		})
	}
}

// TestDialDeadline_TheWholeDialFitsTheRelaysOwnPreAuthWindow corroborates the NUMBER, which is
// section 6.0's own "Non-wait request timeout | 10 s" read onto the connect phase (the upgrade
// is one non-wait request/reply). Nothing new is minted, which is what ADR-007 B99 requires;
// this pins that the reading COMPOSES:
//
//	A whole dial is the connect phase plus two non-wait requests -- auth_init and auth_resp,
//	each bounded at the same 10 s by DefaultCallTimeout.
//
//	The relay bounds the SAME window from its own side: preAuthDeadline (server.go) is the
//	"CUMULATIVE time-to-authenticate, anchored at accept time", capped at
//	Config.HandshakeTimeout -- 30 s, one of the Phase A constants section 6.0's preamble names
//	as the values its table is chosen to be consistent with.
//
// The two meet exactly. Under the window an honest slow link fails a dial the request budget
// permits; over it the client waits past the instant an honest peer stopped listening.
//
// THIS IS NOT A B113 CONSTANT-TRANSCRIPTION. It asserts a RELATION between three constants
// rather than repeating a literal, so widening the dial bound to three hours fails here as
// well as in the behavioural fences above -- and narrowing HandshakeTimeout without revisiting
// the dial fails too, which is the coupling the derivation actually claims.
func TestDialDeadline_TheWholeDialFitsTheRelaysOwnPreAuthWindow(t *testing.T) {
	authExchanges := 2 * DefaultCallTimeout
	window := DefaultConfig().HandshakeTimeout

	if DefaultDialTimeout+authExchanges != window {
		t.Fatalf("the whole dial is bounded at %v (connect %v + two auth exchanges at %v each), "+
			"but the relay's own pre-auth window is %v.\n"+
			"Under it, an honest slow link fails a dial section 6.0's own request budget "+
			"permits; over it, the client waits past the instant an honest peer stopped "+
			"listening",
			DefaultDialTimeout+authExchanges, DefaultDialTimeout, DefaultCallTimeout, window)
	}
}

// TestDialDeadline_EveryExportedDialInheritsTheBound is the QUANTIFIER, which is the half
// B115 left open at the sibling defect: "fixing the one instance does not close a quantifier".
//
// Three callers exist today and all three got this wrong independently, so a bound that lives at
// the callers is a bound the next caller does not inherit -- and the third was found only after
// two had been fixed. This asserts the property over the DIAL
// SURFACE instead: every exported constructor in the package, driven against the same stalling
// peer with context.Background(), must return. That is a behavioural fence, not a scan of call
// sites -- ADR-007 B112 found a call-site scan passing while the very defect its error message
// described was present in the code it was pinning.
//
// The stall used here is a CONNECT-phase one, because that is the stage all four share: the raw
// constructors return as soon as the socket is up and never reach the relay-auth exchange.
func TestDialDeadline_EveryExportedDialInheritsTheBound(t *testing.T) {
	pub, priv := newRelayAuthKey(t)
	dials := []struct {
		name string
		fn   func(ctx context.Context, url string) error
	}{
		{"DialRaw", func(ctx context.Context, url string) error {
			c, err := DialRaw(ctx, url)
			closeIfOpen(c, err)
			return err
		}},
		{"DialRawSecure", func(ctx context.Context, url string) error {
			c, err := DialRawSecure(ctx, url, securityFor(url))
			closeIfOpen(c, err)
			return err
		}},
		{"Dial", func(ctx context.Context, url string) error {
			c, err := Dial(ctx, url, authFor(pub, priv))
			if err == nil {
				_ = c.Close()
			}
			return err
		}},
		{"DialSecure", func(ctx context.Context, url string) error {
			c, err := DialSecure(ctx, url, authFor(pub, priv), securityFor(url))
			if err == nil {
				_ = c.Close()
			}
			return err
		}},
	}

	for _, d := range dials {
		t.Run(d.name, func(t *testing.T) {
			t.Parallel()
			url := newStallingPeer(t, stallHTTPResponse)
			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- d.fn(context.Background(), url) }()

			var err error
			select {
			case err = <-done:
			case <-time.After(dialStallBound):
				t.Fatalf("%s is STILL PARKED after %v against a peer that accepts the "+
					"connection and never answers. The bound has to live where every dial "+
					"passes, or the next dial path added to this package is unbounded again",
					d.name, dialStallBound)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s returned %v after %v, which is not a deadline",
					d.name, err, time.Since(start))
			}
		})
	}
}

// closeIfOpen releases a raw connection a dial unexpectedly returned.
func closeIfOpen(c *Conn, err error) {
	if err == nil && c != nil {
		_ = c.Close()
	}
}
