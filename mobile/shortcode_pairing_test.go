package swarmmobile

// ADR-007 B140 at the phone's edge: a typed ten-character code plus the remembered relay URL
// must construct EXACTLY the payload the QR would have carried -- same derivation, same
// golden arithmetic as internal/remote/pairing/shortcode_test.go -- because everything
// downstream (origin confirm, the B45 dial, RunDevice, SAS) consumes a pairing.QRPayload and
// must not know which spelling produced it.

import (
	"encoding/hex"
	"strings"
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

// TestPayloadFromShortCode_RefusesARelayThatIsNotAWebSocketAddress is the hardening
// agents-tracker-3fkm's first-run prompt makes necessary.
//
// UNTIL NOW THE RELAY URL AT THIS SEAM CAME FROM A QR THIS APP HAD ALREADY DECODED, so the only
// question worth asking was whether it was there at all. The first-run prompt changes who writes
// it: a person types it on a phone keyboard, once, off a terminal on the other side of the room.
// A typo therefore arrives HERE rather than being impossible -- and every refusal below, left
// unchecked, is a phone that holds a ceremony, shows the user their own typo as the destination
// to confirm, and fails at the dial with a transport error nobody can act on.
//
// THE SHAPE IS NAMED IN THE MESSAGE, which is the difference between a refusal and a dead end:
// this string is routed onto the pairing screen and it is the only thing the typist has to work
// from.
func TestPayloadFromShortCode_RefusesARelayThatIsNotAWebSocketAddress(t *testing.T) {
	refused := []struct{ url, why string }{
		{"relay.example:8443", "a bare host:port is what a person copies off a terminal by eye"},
		{"https://relay.example", "an https URL is the plausible wrong guess: it is the scheme " +
			"every other address a user types has"},
		{"http://relay.example", "the same guess without TLS"},
		{"wss://", "a scheme and no host names nothing to dial"},
		{"wss:///pair", "a path with no authority is the same defect wearing a path"},
		{"not a url at all", "prose"},
	}
	for _, c := range refused {
		err := func() error {
			_, err := payloadFromShortCode("K73-M2QF-9TD", c.url)
			return err
		}()
		if err == nil {
			t.Errorf("payloadFromShortCode accepted %q as a relay address (%s)", c.url, c.why)
			continue
		}
		if !strings.Contains(err.Error(), "wss://") {
			t.Errorf("the refusal of %q does not name the shape an address must have (%q). This "+
				"message IS the pairing screen's instruction to the person who typed it",
				c.url, err.Error())
		}
	}
}

// TestPayloadFromShortCode_AcceptsEveryWebSocketSpellingAPersonMightType is the other direction,
// and it is the one that keeps the check above from being a fence nobody can satisfy.
//
// `ws://` IS ACCEPTED and that is not an oversight: PB-OPS-1's demonstration is a phone reaching
// a laptop over the LAN, where there is no certificate to be had. The destination is displayed
// and confirmed either way (PB-PAIR-6), and refusing the unencrypted scheme here would refuse
// the local-network case the confirm step exists to LABEL rather than forbid.
func TestPayloadFromShortCode_AcceptsEveryWebSocketSpellingAPersonMightType(t *testing.T) {
	for _, url := range []string{
		"wss://relay.example",
		"wss://relay.example:8443",
		"wss://relay.example:8443/pair",
		"ws://192.168.1.20:7443",
		"  wss://relay.example:8443  ",
	} {
		if _, err := payloadFromShortCode("K73-M2QF-9TD", url); err != nil {
			t.Errorf("payloadFromShortCode refused %q, which is an address a machine really "+
				"prints: %v", url, err)
		}
	}
}
