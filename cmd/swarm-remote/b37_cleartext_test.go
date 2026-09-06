package main

// ADR-007 B37 (FAILING FIRST): the gateway sidecar's own dial refuses a cleartext relay,
// and refuses it before it sends the machine's relay-auth public key.
//
// run() is the sidecar's whole body and used to dial with relay.Dial, which applies no
// transport-security policy. The machine's key is disclosed by the AUTH_INIT frame
// the handset's is, so the same chain runs against the MACHINE identity: an observer of a
// ws:// hop learns it, and B27's first-use clause lets any registered identity revoke a
// target that has authorized nobody.
//
// The fence drives run() -- not a helper it calls -- and asserts on frames at a listener,
// because the defect this closes is precisely a policy that existed, was tested, and sat
// on a path production never took.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func namedLoopbackRelayURL(t *testing.T, literal string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(literal, "ws://"))
	if err != nil {
		t.Fatalf("split loopback relay URL: %v", err)
	}
	return "ws://localhost:" + port
}

// gwAuth is a machine relay-auth identity, the one resolveGatewayParams loads from
// <stateDir>/remote in production.
func gwAuth(t *testing.T) relayv2.Auth {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("relay-auth key: %v", err)
	}
	return relayv2.Auth{
		PublicKey: pub,
		Sign:      func(ch []byte) ([]byte, error) { return ed25519.Sign(priv, ch), nil },
	}
}

// gatewayParamsFor builds the sidecar's params the way resolveGatewayParams does: the
// transport policy is RESOLVED FROM THE PROVISIONING, not chosen by the caller. Handing
// run() a hand-built relay.Security would let this fence pass against a sidecar that
// dials under whatever it is given, which is the shape of the defect it exists to catch.
func gatewayParamsFor(t *testing.T, relayURL string) gatewayParams {
	t.Helper()
	sec, err := relaycfg.Config{RelayURL: relayURL}.Security()
	if err != nil {
		t.Fatalf("resolve the machine transport policy: %v", err)
	}
	auth := gwAuth(t)
	phonePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayParams{
		RelayAuth: auth, Inbound: mustMemoryInbound(t),
		RelayV2Profile: relayv2.Profile{RelayURL: relayURL, MachineRID: relayv2.RoutingID(auth.PublicKey), OperatorNamespace: "owner", Security: sec},
		PhoneTarget:    relayv2.RoutingID(phonePub), DeviceRelayAuthPub: phonePub, DeviceConsentSig: []byte("test-consent"),
	}
}

func mustMemoryInbound(t *testing.T) remotegw.InboundState {
	t.Helper()
	state, err := remotegw.OpenInboundState("", "")
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// TestPBNET2_TheGatewayRefusesACleartextRelayBeforeSendingItsPublicKey drives run()
// against a cleartext relay it must refuse, having first proved through the same tap that
// a permitted URL does reach the handshake.
func TestPBNET2_TheGatewayRefusesACleartextRelayBeforeSendingItsPublicKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tap, _ := startCutTapRelay(t, 0)

	// ---- control: a loopback IP literal reaches the relay-auth handshake ----
	// run() fails afterwards -- there is no daemon behind this sidecar -- and that is
	// deliberate: the assertion is what crossed the wire, not that the gateway ran.
	ctlCtx, ctlCancel := context.WithTimeout(ctx, 5*time.Second)
	defer ctlCancel()
	err := run(ctlCtx, gatewayParamsFor(t, tap.url()))
	if errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("the loopback relay a developer runs, and the one S19 spawns the real "+
			"sidecar against, was refused: %v", err)
	}
	conns := tap.dialCount()
	if conns == 0 || !tap.wrote("AUTH_INIT") {
		t.Fatalf("the tap did not observe the relay-auth handshake it must be able to observe "+
			"(%d connections); the negative half below would be vacuous", conns)
	}
	baseConns, baseAuth := conns, tap.writeCount("AUTH_INIT")

	// ---- the fence: same listener, same relay, addressed by name -----------
	// A refusal is decided from the URL, so it needs no time at all. The bound is here so
	// a regression reports in seconds instead of running the sidecar to the suite's
	// deadline.
	fenceCtx, fenceCancel := context.WithTimeout(ctx, 10*time.Second)
	defer fenceCancel()
	err = run(fenceCtx, gatewayParamsFor(t, namedLoopbackRelayURL(t, tap.url())))
	if !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("run() against a cleartext relay returned %v, want relay.ErrCleartextRefused", err)
	}

	conns = tap.dialCount()
	if conns != baseConns {
		t.Errorf("the gateway opened %d connection(s) to a cleartext relay before refusing it; "+
			"the refusal must be decided from the URL, so it costs no connection", conns-baseConns)
	}
	if n := tap.writeCount("AUTH_INIT") - baseAuth; n != 0 {
		t.Errorf("the gateway sent %d AUTH_INIT frame(s) in cleartext; AUTH_INIT carries the "+
			"machine's FULL relay-auth public key (ADR-007 B37 step 2)", n)
	}
}
