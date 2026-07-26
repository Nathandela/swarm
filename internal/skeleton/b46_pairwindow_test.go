package skeleton

// ADR-007 B46 part 2: the announced pairing window is longer than the slot it names.
//
// defaultPairTTL was 3 minutes against the relay's authoritative RendezvousTTL of 60
// seconds, and its own comment called expiry "advisory... the daemon's real gate is the
// mandatory SAS confirm, not a wall clock". The wall clock the comment dismissed is the
// relay's, not the daemon's, and it is not advisory at all: past it the slot is gone. So
// `swarm remote pair` printed an expiry two minutes after the rendezvous had died, with
// the QR still on screen -- which is the window B47b's re-creatable id is reached through.
//
// The fence: the expiry this daemon ANNOUNCES never outlives the slot the relay keeps.

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestB46_TheAnnouncedPairingWindowNeverOutlivesTheRelaySlot(t *testing.T) {
	slot := relay.DefaultConfig().RendezvousTTL

	for _, tc := range []struct {
		name string
		ttl  int
	}{
		{"no TTL requested, so the daemon's default applies", 0},
		{"a caller asks for longer than the relay will hold", int(10 * time.Minute / time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk := assemble(t)
			injectPairing(t, sk)

			// The handshake goroutine BeginPairing spawns has no peer here; cancelling is
			// what unblocks it, and this test is about the view returned synchronously.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			view, err := sk.api.BeginPairing(ctx,
				protocol.PairStartReq{Capability: "full", TTLSeconds: tc.ttl},
				func([]string, string) (bool, error) { return false, nil },
				func(protocol.PairResult) {})
			// Taken AFTER the call: the rendezvous was minted at or before this instant, so
			// an expiry within one slot of here is within one slot of the mint. Reading the
			// clock before the call instead would charge the call's own duration to the
			// window and fail a correct implementation by microseconds.
			after := time.Now()
			if err != nil {
				t.Fatalf("BeginPairing: %v", err)
			}
			if view.ExpiresAt == nil {
				t.Fatal("BeginPairing announced no expiry at all")
			}
			if window := view.ExpiresAt.Sub(after); window > slot {
				t.Fatalf("announced pairing window %s outlives the relay slot %s.\n"+
					"  The QR stays on the owner's screen for %s after the rendezvous is gone, "+
					"which is exactly the interval an expired id can be re-created in.",
					window, slot, window-slot)
			}
		})
	}
}
