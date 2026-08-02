package skeleton

// ADR-007 B34, machine half (FAILING FIRST): the daemon's pairing rendezvous honours the
// SPKI pin in <stateDir>/remote/relay.json.
//
// This is the THIRD machine dial path and the one most likely to be left behind, because
// it is the only one that is not obviously "the machine talking to the relay": it is
// reached through loadPairingConfig -> NewRendezvous, a closure built at daemon assembly.
// Leaving it unpinned would mean a machine that verifies its relay for the gateway and
// the CLI, and does not verify it for the one exchange that establishes a pairing.
//
// The fence therefore starts at loadPairingConfig -- the production assembly serve.go
// calls -- rather than at relayRendezvousFactory, so it also proves the pin survives the
// loader.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// b34TLSFrontedRelay puts the real relay behind a TLS terminator, the topology
// docs/operations/relay-runbook.md describes.
func b34TLSFrontedRelay(t *testing.T) (wssURL string, cert *x509.Certificate) {
	t.Helper()
	srv := startPairingRelay(t)
	target, err := url.Parse(strings.Replace(srv.URL(), "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1), front.Certificate()
}

func b34SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// b34PairingConfig provisions a machine and returns what the daemon assembly builds.
func b34PairingConfig(t *testing.T, cfg relaycfg.Config) *pairingConfig {
	t.Helper()
	stateDir := t.TempDir()
	writeTestIdentity(t, stateDir, "b34.local")
	if err := relaycfg.Save(stateDir, cfg); err != nil {
		t.Fatalf("save relay.json: %v", err)
	}
	pc, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if pc == nil || pc.NewRendezvous == nil {
		t.Fatalf("loadPairingConfig built no rendezvous seam for a configured relay")
	}
	return pc
}

// TestPBOPS5_TheDaemonRendezvousHonoursTheConfiguredPin proves the matching pin joins
// before asserting the wrong one is refused, so the refusal cannot be a rig that never
// worked.
func TestPBOPS5_TheDaemonRendezvousHonoursTheConfiguredPin(t *testing.T) {
	wss, cert := b34TLSFrontedRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var id [16]byte
	copy(id[:], "b34-rendezvous--")

	// ---- control: the matching pin opens a rendezvous transport ------------
	pc := b34PairingConfig(t, relaycfg.Config{RelayURL: wss, SPKIPin: b34SPKIPin(cert)})
	rt, err := pc.NewRendezvous(ctx, id)
	if err != nil {
		t.Fatalf("the MATCHING pin was refused, so this rig cannot demonstrate anything: %v", err)
	}
	if rt == nil {
		t.Fatal("NewRendezvous returned no transport and no error")
	}

	// ---- the fence: a valid pin for a different key ------------------------
	wrongSum := sha256.Sum256([]byte("some other relay's public key"))
	pc = b34PairingConfig(t, relaycfg.Config{
		RelayURL: wss, SPKIPin: base64.StdEncoding.EncodeToString(wrongSum[:]),
	})
	rt, err = pc.NewRendezvous(ctx, id)
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("NewRendezvous against a relay that does not match the configured pin "+
			"returned %v, want relay.ErrPinMismatch", err)
	}
	if rt != nil {
		t.Fatal("NewRendezvous returned a transport alongside a pin failure")
	}
}

// TestPBOPS5_AnUnpinnedMachineKeepsTheLoopbackRelay is the "optional in the file" half at
// the path S19 depends on: a machine with no pin configured still reaches the plain
// ws://127.0.0.1 relay the exit demonstration spawns.
func TestPBOPS5_AnUnpinnedMachineKeepsTheLoopbackRelay(t *testing.T) {
	srv := startPairingRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pc := b34PairingConfig(t, relaycfg.Config{RelayURL: srv.URL()})
	var id [16]byte
	copy(id[:], "b34-loopback----")
	if _, err := pc.NewRendezvous(ctx, id); err != nil {
		t.Fatalf("an unpinned machine could not reach its own loopback relay: %v", err)
	}
}
