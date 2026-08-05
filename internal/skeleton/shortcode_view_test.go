package skeleton

// ADR-007 B140 at the daemon seam: the short code the PairView carries must be a SECOND
// SPELLING of the very ceremony BeginPairing opened -- not a parallel secret, not a display
// string minted beside the real one. The assertion is arithmetical: derive the code and land
// on the rendezvous id the daemon announced and the secret the QR encodes. A code that
// derives to anything else is two devices that can never meet, printed side by side.

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

func TestBeginPairing_ShortCodeSpellsTheSameCeremony(t *testing.T) {
	sk := assemble(t)
	injectPairing(t, sk)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	view, err := sk.api.BeginPairing(ctx, protocol.PairStartReq{Capability: "full"},
		func([]string, string) (bool, error) { return false, context.Canceled },
		func(protocol.PairResult) {})
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}

	if view.ShortCode == "" {
		t.Fatal("PairView carries no short code: the CLI has nothing typeable to print " +
			"(ADR-007 B140), and the manual path stays 133 characters")
	}

	id, psk, err := pairing.DeriveShortCode(view.ShortCode)
	if err != nil {
		t.Fatalf("the minted short code %q does not parse by the phone's own derivation: %v",
			view.ShortCode, err)
	}
	if got := hex.EncodeToString(id[:]); got != view.RendezvousID {
		t.Fatalf("the short code derives rendezvous id %s but the daemon opened %s: "+
			"a phone typing this code claims a mailbox nobody is listening on", got, view.RendezvousID)
	}

	qp, err := pairing.DecodeQR(view.QR)
	if err != nil {
		t.Fatalf("DecodeQR(view.QR): %v", err)
	}
	if qp.RendezvousID != id {
		t.Fatal("the QR and the short code name different rendezvous ids: two spellings, two ceremonies")
	}
	if qp.PairingSecret != psk {
		t.Fatal("the QR and the short code derive different pairing secrets: the scanned phone " +
			"and the typing phone would run different handshakes against the same machine")
	}
}
