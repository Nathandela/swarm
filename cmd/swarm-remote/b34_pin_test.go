package main

// ADR-007 B34, machine half (FAILING FIRST): the gateway sidecar honours the SPKI pin
// configured in <stateDir>/remote/relay.json.
//
// The pin is the half of B34 that B37 could not close: refusing cleartext needs no
// channel, verifying the peer does. The machine has a channel -- relay.json, which all
// three machine dial paths already read -- so there is no size budget in the way and no
// reason for the gateway to be the unpinned one.
//
// THE FENCE IS ON run(), NOT ON DialSecure, for the reason that made B34 exist at all: a
// pin proved against the secure helper is a pin that may or may not be on the path the
// sidecar takes. The relay behind the TLS front is the real relay, so a dial that passes
// the pin goes on to complete the real relay-auth handshake.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// startTLSFrontedRelay stands up the real relay behind a TLS terminator, which is the
// deployment docs/operations/relay-runbook.md describes: the relay itself speaks ws://,
// and a front terminates TLS for everyone who reaches it over a network.
func startTLSFrontedRelay(ctx context.Context, t *testing.T) (wssURL string, cert *x509.Certificate) {
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

	target, err := url.Parse(strings.Replace(srv.URL(), "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1), front.Certificate()
}

// spkiPinOf is the runbook's section-3 value, computed in Go: base64 of SHA-256 over the
// certificate's SubjectPublicKeyInfo.
func spkiPinOf(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// otherPin is a syntactically valid pin for a different key, so a refusal is the PIN
// deciding rather than the value being unusable.
func otherPin() string {
	sum := sha256.Sum256([]byte("some other relay's public key"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// TestPBOPS5_TheGatewayHonoursTheConfiguredPin runs the matching pin first, so a refusal
// under the wrong pin cannot be blamed on a rig that could never connect.
func TestPBOPS5_TheGatewayHonoursTheConfiguredPin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wss, cert := startTLSFrontedRelay(ctx, t)

	// ---- control: the MATCHING pin reaches the relay-auth handshake --------
	// run() fails after the dial -- there is no daemon behind this sidecar -- so the
	// assertion is that it got PAST the transport policy, not that it ran.
	matching, err := relaycfg.Config{RelayURL: wss, SPKIPin: spkiPinOf(cert)}.Security()
	if err != nil {
		t.Fatalf("Security with the matching pin: %v", err)
	}
	ctlCtx, ctlCancel := context.WithTimeout(ctx, 5*time.Second)
	defer ctlCancel()
	err = run(ctlCtx, gatewayParams{RelayURL: wss, RelayAuth: gwAuth(t), RelaySecurity: matching})
	if errors.Is(err, relay.ErrPinMismatch) || errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("the MATCHING pin was refused, so this rig cannot demonstrate anything: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "dial relay") &&
		strings.Contains(err.Error(), "certificate") {
		t.Fatalf("the matching pin did not replace chain verification: %v", err)
	}

	// ---- the fence: a valid pin for a DIFFERENT key ------------------------
	wrong, err := relaycfg.Config{RelayURL: wss, SPKIPin: otherPin()}.Security()
	if err != nil {
		t.Fatalf("Security with the wrong pin: %v", err)
	}
	fenceCtx, fenceCancel := context.WithTimeout(ctx, 10*time.Second)
	defer fenceCancel()
	err = run(fenceCtx, gatewayParams{RelayURL: wss, RelayAuth: gwAuth(t), RelaySecurity: wrong})
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("run() against a relay that does not match the configured pin returned %v, "+
			"want relay.ErrPinMismatch; a gateway that ignores its own relay.json pin is the "+
			"unpinned dial path B34 recorded", err)
	}
}

// TestPBOPS5_TheGatewayResolvesItsPinFromRelayJSON closes the other half of the wiring:
// the policy run() dials under must come from the FILE, not from a value a test handed
// in. resolveGatewayParams is the sidecar's own assembly, and this asserts the pin
// survives it.
func TestPBOPS5_TheGatewayResolvesItsPinFromRelayJSON(t *testing.T) {
	_, cert := startTLSFrontedRelay(t.Context(), t)
	pin := spkiPinOf(cert)

	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	addPairedDevice(t, stateDir)
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL: "wss://relay.example.com:8443", OperatorNamespace: "owner", SPKIPin: pin,
	}); err != nil {
		t.Fatalf("Save relay.json: %v", err)
	}

	p, err := resolveGatewayParams(stateDir, filepath.Join(stateDir, "d.sock"))
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	want, err := relaycfg.Config{RelayURL: "wss://relay.example.com:8443", SPKIPin: pin}.Security()
	if err != nil {
		t.Fatalf("Security: %v", err)
	}
	if string(p.RelaySecurity.PinnedSPKISHA256) != string(want.PinnedSPKISHA256) {
		t.Fatalf("resolveGatewayParams dropped the configured pin: got %x, want %x",
			p.RelaySecurity.PinnedSPKISHA256, want.PinnedSPKISHA256)
	}
}
