// ADR-007 B37 (FAILING FIRST): the handset's own dial path refuses a cleartext relay,
// and refuses it BEFORE it sends its relay-auth public key.
//
// This is the disclosure step of B37's chain. mobile/relay.go's App.dial used to call
// relay.Dial, which applies no transport-security policy at all, and relay's
// authenticate() opens with
//
//	auth_init {"relay_auth_pub": <the phone's FULL Ed25519 public key>}
//
// so a passive observer of a ws:// hop reads the victim's public key, registers a
// throwaway identity, and calls authorize_device + device_revoke naming a handset that
// has never paired -- which B27's first-use clause permits, because a never-paired
// identity has authorized nobody. The result is a permanent, unauthenticated denial of
// service by an adversary who is not the relay operator.
//
// THE FENCE IS ON THE PRODUCTION PATH ON PURPOSE. internal/remote/transport/tls_test.go
// was green throughout the window in which this hole was open, because it guards
// relay.DialSecure and nothing production ran reached it. So this test drives the real
// swarmmobile.App -- NewApp + Start, exactly what PhoneRuntime does -- and asserts on
// what arrives at a listener, not on which helper was called.
package conformance_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// dialTap is a websocket proxy in front of the real relay. It forwards every frame
// verbatim and decides nothing: it counts accepted connections and keeps every frame the
// phone wrote toward the relay, so "the handshake never happened" is an observation
// rather than an inference. It works at the websocket layer for the same reason
// countingRelay does -- client-to-server frames are masked on the wire, so a raw TCP tap
// cannot see an op name even when one is there.
//
// It is addressable two ways, and the pair is the whole experiment: the SAME listener,
// the SAME relay behind it, named once as a loopback IP literal and once as a hostname.
// The transport policy resolves nothing, so "localhost" is not a loopback literal and is
// refused -- while 127.0.0.1 is admitted inside a test binary, which proves the refusal is
// the policy and not a tap that never worked.
type dialTap struct {
	srv      *httptest.Server
	upstream string

	mu    sync.Mutex
	conns int
	sent  bytes.Buffer
}

func newDialTap(t *testing.T, upstreamWS string) *dialTap {
	t.Helper()
	tap := &dialTap{upstream: upstreamWS}
	tap.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tap.mu.Lock()
		tap.conns++
		tap.mu.Unlock()

		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, tap.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

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

// LiteralURL addresses the tap as a loopback IP literal -- the form the policy admits
// inside a test binary. httptest listens on 127.0.0.1.
func (d *dialTap) LiteralURL() string { return "ws" + strings.TrimPrefix(d.srv.URL, "http") }

// NamedURL addresses the SAME listener by hostname. A name is never a loopback literal:
// resolution is not part of the carve-out, so a name cannot be pointed somewhere else.
func (d *dialTap) NamedURL() string {
	u := d.LiteralURL()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(u, "ws://"))
	if err != nil {
		return ""
	}
	return "ws://localhost:" + port
}

func (d *dialTap) observed() (conns int, sent []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns, append([]byte(nil), d.sent.Bytes()...)
}

// TestPBNET2_TheHandsetRefusesACleartextRelayBeforeSendingItsPublicKey is B37 steps 1-3
// closed at step 1.
//
// The control half runs FIRST and against the same tap, so the negative half cannot pass
// on a broken rig: if the tap could never carry a handshake, the assertion that no
// handshake reached it would be vacuous.
func TestPBNET2_TheHandsetRefusesACleartextRelayBeforeSendingItsPublicKey(t *testing.T) {
	h := newHarness(t)
	tap := newDialTap(t, h.RelayURL)

	// ---- control: the tap carries a real handshake -------------------------
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = tap.LiteralURL()
	h.App = h.openApp()
	eventually(t, "the phone never came online through the tap addressed as 127.0.0.1", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})
	conns, sent := tap.observed()
	if conns == 0 || !bytes.Contains(sent, []byte("auth_init")) {
		t.Fatalf("the tap did not observe the relay-auth handshake it must be able to observe "+
			"(%d connections, %d bytes); the negative half below would be vacuous", conns, len(sent))
	}
	baseConns, baseAuth := conns, bytes.Count(sent, []byte("auth_init"))

	// ---- the fence: the same relay, named rather than a loopback literal ----
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = tap.NamedURL()
	h.App = h.openApp()

	// Long enough for App.run to have made several dial attempts at its 250ms backoff.
	time.Sleep(2 * time.Second)

	conns, sent = tap.observed()
	if conns != baseConns {
		t.Errorf("the handset opened %d connection(s) to a cleartext relay after the policy "+
			"should have refused it; a refusal that costs a connection has already told the "+
			"observer this handset holds a swarm identity", conns-baseConns)
	}
	if n := bytes.Count(sent, []byte("auth_init")) - baseAuth; n != 0 {
		t.Errorf("the handset sent %d auth_init frame(s) in cleartext; auth_init carries the FULL "+
			"relay-auth public key, which is B37 step 2 -- the disclosure that lets any passive "+
			"observer revoke a never-paired identity", n)
	}
	if st, err := h.App.ConnectionState(); err == nil && st == "online" {
		t.Errorf("ConnectionState = %q against a cleartext relay: the handset came online over "+
			"a connection the transport policy must refuse", st)
	}
}
