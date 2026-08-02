package conformance_test

// Slice S16 GREEN scaffolding: the one step PB-PAIR-6 inserts into every pairing.
//
// BeginPairing no longer joins anything -- it decodes the QR, reports the destination, and
// stops. ConfirmOrigin is what dials. Every pairing written before that change therefore has
// to grow the step, and it is ONE call rather than four copies so a later reader can see that
// the shipped suites were adapted mechanically and nothing about what they assert moved.
//
// NO ASSERTION IN ANY CALLER CHANGED. This helper adds the user's yes and nothing else: it
// reads the origin the phone would display and hands back exactly that string, which is the
// same comparison the confirm sheet performs.

import (
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s16PassOriginGate confirms the destination a freshly-begun pairing is showing.
//
// It is tolerant of a handle that is already past the gate, so a caller that was written
// against the pre-PB-PAIR-6 flow -- or one that reaches this after the handshake has already
// started -- is left alone rather than failed.
func s16PassOriginGate(t *testing.T, p *swarmmobile.Pairing) {
	t.Helper()
	state, err := p.State()
	if err != nil {
		t.Fatalf("Pairing.State: %v", err)
	}
	if state != "confirm_destination" {
		return
	}
	origin, err := p.Origin()
	if err != nil {
		t.Fatalf("Pairing.Origin: %v", err)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatalf("PB-PAIR-6: confirming the destination the phone itself reported was refused: %v", err)
	}
}
