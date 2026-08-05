package swarmmobile

// ADR-007 B140 at the phone's edge: a typed ten-character code plus the remembered relay URL
// must construct EXACTLY the payload the QR would have carried -- same derivation, same
// golden arithmetic as internal/remote/pairing/shortcode_test.go -- because everything
// downstream (origin confirm, the B45 dial, RunDevice, SAS) consumes a pairing.QRPayload and
// must not know which spelling produced it.

import (
	"encoding/hex"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

func TestPayloadFromShortCode_ConstructsTheQRsPayload(t *testing.T) {
	got, err := payloadFromShortCode("k73 m2qf 9td", "wss://relay.example:8443")
	if err != nil {
		t.Fatalf("payloadFromShortCode: %v", err)
	}
	wantID, wantPSK, err := pairing.DeriveShortCode("K73-M2QF-9TD")
	if err != nil {
		t.Fatal(err)
	}
	if got.RendezvousID != wantID {
		t.Fatalf("rendezvous id %s, want %s: this phone would claim a mailbox the machine "+
			"never opened", hex.EncodeToString(got.RendezvousID[:]), hex.EncodeToString(wantID[:]))
	}
	if got.PairingSecret != wantPSK {
		t.Fatal("pairing secret disagrees with the shared derivation: the handshake's PSK " +
			"would never match the machine's")
	}
	if got.RelayURL != "wss://relay.example:8443" {
		t.Fatalf("relay URL %q was not carried into the payload verbatim", got.RelayURL)
	}
	if got.MachineStaticPub != nil {
		t.Fatal("a short code cannot pin a machine static; the payload must say so honestly " +
			"(nil), exactly as the v1 QR does")
	}
}

func TestPayloadFromShortCode_RefusalsAreDistinctAndRouted(t *testing.T) {
	if _, err := payloadFromShortCode("not a code", "wss://relay.example:8443"); err == nil {
		t.Fatal("a malformed code was accepted; the screen has nothing to tell the typist")
	}
	if _, err := payloadFromShortCode("K73-M2QF-9TD", ""); err == nil {
		t.Fatal("an empty relay URL was accepted: the phone would hold a ceremony and no " +
			"address to dial, the exact state PB-PAIR-7 exists to prevent")
	}
	if _, err := payloadFromShortCode("K73-M2QF-9TD", "   "); err == nil {
		t.Fatal("a blank relay URL was accepted")
	}
}
