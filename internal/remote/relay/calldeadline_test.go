package relay

// FAILING-FIRST (TDD RED, GG-5) for the round-7 blocker: relay.Client has NO timeout of any
// kind, so a relay that completes the websocket handshake and then ANSWERS NOTHING parks its
// caller for as long as the socket stays up.
//
// THE ADVERSARY DOES NOTHING. The relay is the declared adversary, and this is the cheapest
// move it has: accept the connection, accept every frame, reply to none. roundtrip holds c.mu
// across write-then-read, so the first parked caller also blocks every other caller on that
// connection -- one silent socket, and the whole outbound plane stops with no error, no state
// change and nothing to retry.
//
// IT IS ALSO REACHABLE BENIGNLY. A half-open TCP after a WiFi -> cellular handoff presents to
// the client exactly as this proxy does: writes are accepted by the local stack, reads never
// return, and the connection is never observed to die.
//
// THE GATEWAY ALREADY FIXED THIS ON ITS OWN SIDE and named it Blocker 2 --
// remotegw/relaysink.go's defaultAppendTimeout, whose comment is the argument in full: "an
// UNBOUNDED append against a hung relay would pin that lock forever and wedge every producer
// AND Err()". The client the gateway bounds is this one, and it was never bounded here, so
// every OTHER caller of it -- the phone above all -- inherited the unbounded call.
//
// This file contains NO implementation.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// silentRelayBound is how long a test waits for a call that must be bounded. It is
// deliberately well above the bound itself and well below "forever": a call still parked here
// is parked because nothing bounds it, not because the bound is generous.
//
// It is a LITERAL rather than a multiple of DefaultCallTimeout, deliberately: this file is the
// RED, and it has to compile and fail against a tree in which the fix does not exist. That is
// also why it moved -- §6.0 binds the non-wait request timeout at 10 s, so a 10 s wait here
// would be a coin flip against the very implementation it is meant to accept.
const silentRelayBound = 25 * time.Second

// silentRelay is a websocket proxy in front of the REAL relay that can be told to stop
// answering. Silenced, it keeps the connection up and keeps consuming everything the client
// sends -- it simply never writes a reply back. Nothing about the client's socket changes,
// which is the point: there is no drop for the client to observe.
type silentRelay struct {
	srv      *httptest.Server
	upstream string
	silent   atomic.Bool
}

func newSilentRelay(t *testing.T, upstream string) *silentRelay {
	t.Helper()
	sr := &silentRelay{upstream: upstream}
	sr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, sr.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(MaxFrame + 64)

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				// SILENCE IS A DROPPED REPLY, NOT A CLOSED SOCKET. The upstream frame is read
				// (so the real relay is never backpressured into noticing) and thrown away.
				if sr.silent.Load() {
					continue
				}
				if err := down.Write(ctx, mt, data); err != nil {
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
				if err := up.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(sr.srv.Close)
	return sr
}

func (s *silentRelay) URL() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") }

// Silence stops every reply from reaching the client, permanently.
func (s *silentRelay) Silence() { s.silent.Store(true) }

// silentRelayFixture pairs a machine that dials THROUGH the proxy -- so its calls are the
// ones that must stay bounded -- with a device it may append to.
func silentRelayFixture(t *testing.T) (*Client, string, *silentRelay) {
	t.Helper()
	srv, _, _, _ := startTestRelay(t, nil)
	proxy := newSilentRelay(t, srv.URL())

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, proxy.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub),
		consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	return machine, RoutingID(dPub), proxy
}

// TestCallDeadline_ASilentRelayCannotParkACallForever is the blocker itself, at the call the
// phone makes on every keystroke, every kill and every take_control.
//
// context.Background() is not an oversight in the test: it is what EVERY shipped phone call
// site passes (mobile/commands.go, mobile/relay.go, mobile/app.go), because the caller has no
// deadline of its own to offer and reasonably expects the transport to have one.
func TestCallDeadline_ASilentRelayCannotParkACallForever(t *testing.T) {
	machine, devRID, proxy := silentRelayFixture(t)

	// PREMISE: the append works while the relay answers, so a hang afterwards is caused by
	// the silence and not by the fixture.
	if _, err := machine.MailboxAppend(context.Background(), devRID, []byte("premise")); err != nil {
		t.Fatalf("MailboxAppend before the relay went silent: %v", err)
	}

	proxy.Silence()

	done := make(chan error, 1)
	go func() {
		_, err := machine.MailboxAppend(context.Background(), devRID, []byte("into the silence"))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("MailboxAppend returned nil against a relay that answered nothing; the caller " +
				"was told the frame was stored when no reply was ever received")
		}
		// The deadline must be REPORTED as one. An unrecognisable error is the same wedge with
		// extra steps: the phone's classifier routes by Go identity, and an identity nothing
		// matches lands in the class whose remedy is "report a bug".
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("MailboxAppend against a silent relay = %v; want an error that reports a "+
				"deadline (errors.Is(err, context.DeadlineExceeded)), so a caller can tell a "+
				"relay that will not answer from one that refused", err)
		}
	case <-time.After(silentRelayBound):
		t.Fatalf("MailboxAppend(context.Background(), ...) was STILL PARKED after %v against a "+
			"relay that completed the websocket handshake and then answered nothing.\n"+
			"relay.Client carries no timeout of any kind: roundtrip uses only the caller's "+
			"context and readFrame blocks until the socket dies. Every shipped phone call site "+
			"passes context.Background(), so the relay -- the declared adversary -- wedges the "+
			"phone's whole outbound plane by doing NOTHING, and a half-open TCP after a network "+
			"handoff does it by accident.\n"+
			"The gateway bounds this same client at remotegw/relaysink.go's defaultAppendTimeout "+
			"and calls it Blocker 2; the client itself was never bounded.", silentRelayBound)
	}
}

// TestCallDeadline_ASilentRelayCannotWedgeEveryOtherCaller is the half that makes this a
// PLANE-wide outage rather than one slow call.
//
// roundtrip holds c.mu across write-then-read, so a caller parked on a silent relay holds the
// exchange lock while it waits. Every other caller on that connection -- on the phone that is
// the inbound drain, the keystrokes, take_control and kill -- then blocks on the mutex, having
// never been given a deadline of their own to expire. Bounding only the FIRST caller would
// leave the second one queued behind it for as long as the queue is deep, so the bound has to
// be on the CALL, from the moment it is issued, not on the exchange once it holds the lock.
func TestCallDeadline_ASilentRelayCannotWedgeEveryOtherCaller(t *testing.T) {
	machine, devRID, proxy := silentRelayFixture(t)
	proxy.Silence()

	// The first caller parks holding the exchange lock.
	parked := make(chan struct{})
	go func() {
		close(parked)
		_, _ = machine.MailboxAppend(context.Background(), devRID, []byte("first"))
	}()
	<-parked
	time.Sleep(200 * time.Millisecond) // let it reach the read

	// A SECOND, unrelated caller. It must not inherit the first one's wait.
	second := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := machine.Presence(context.Background(), machine.RoutingID())
		second <- err
	}()

	select {
	case err := <-second:
		if err == nil {
			t.Fatal("Presence returned nil against a relay that answered nothing")
		}
		if took := time.Since(started); took > silentRelayBound {
			t.Errorf("the second caller took %v; it must be bounded from when IT was issued, "+
				"not from when the caller ahead of it gave up", took)
		}
	case <-time.After(silentRelayBound):
		t.Fatalf("a second, unrelated call was still parked after %v behind ONE caller stuck on a "+
			"silent relay.\n"+
			"c.mu is held across write-then-read, so one unbounded exchange stops every producer "+
			"on the connection: on the phone that is the inbound drain AND every keystroke, "+
			"take_control and kill, all with no error and no state change.", silentRelayBound)
	}
}
