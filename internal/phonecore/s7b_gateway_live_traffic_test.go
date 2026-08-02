package phonecore

// FAILING-FIRST (TDD RED, GG-5) test for PB-GW-2's SECOND acceptance criterion, the one
// the requirement calls "what makes this test honest": with the bounded-age check enabled,
// legitimate phone-sealed traffic is unaffected.
//
// It lives here rather than in internal/remotegw because it must use the REAL producer.
// remotegw's own tests hand-roll their sealed frames (mailbox_route_test.go:94-105 notes
// why: remotegw must not import phonecore), so a test written there would prove only that
// the gateway accepts frames the gateway's test helper made. The whole PB-GW-6 trap was
// that the real producer and the test fixtures disagreed about IssuedAt. phonecore's tests
// already call into remotegw for exactly this reason (input_test.go:57), and PB-GW-6's own
// evidence is next door in issuedat_test.go.
//
// The bound is asserted on remotegw.OpenMailboxFrame -- the production choke point
// CommandBridge.handle reads through (command_loop.go:277) -- not on a re-implementation of
// crypto's age arithmetic. issuedat_test.go had to re-implement it because PB-GW-2's toggle
// did not exist yet; that is what this slice supplies.

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestS7bLiveTraffic_RealPhoneSealsPassTheEnabledBound is the anti-brick assertion. All
// five phone -> machine seal functions are covered because PB-GW-2's bound applies to the
// whole inbound stream: one unstamped producer is enough to make the toggle refuse that
// class of frame forever, and a phone that cannot send take_control cannot type at all.
//
// Each frame gets its own receiver: phoneSeals returns a map, so iteration order is
// randomised, and a shared receiver would report ErrStaleSeq on whichever frame happened
// to come second. This test is about the age axis; the seq axis is pinned in remotegw.
func TestS7bLiveTraffic_RealPhoneSealsPassTheEnabledBound(t *testing.T) {
	key := testContentKey()
	var seq Sequencer

	// GUARD: prove the bound is actually live on this path before claiming traffic
	// survives it. Without this, a gateway that never enabled the check would pass the
	// loop below trivially -- the vacuous-check half of the defect PB-GW-2 exists to
	// avoid, mirrored from its brick half.
	stale, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  7,
		Seq:      9000,
		IssuedAt: time.Now().Add(-remotegw.InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"t":"data","s":"m/s1","data":"bHM="}`))
	if err != nil {
		t.Fatalf("seal the stale control frame: %v", err)
	}
	if _, err := remotegw.OpenMailboxFrame(crypto.NewMailboxReceiver(), key, stale.Marshal()); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("a frame %v older than the %v bound was not refused (err = %v); the gateway's bounded-age check is not enabled, so every assertion below is vacuous", time.Minute, remotegw.InboundMaxAge, err)
	}

	for name, raw := range phoneSeals(t, key, 7, &seq) {
		if _, err := remotegw.OpenMailboxFrame(crypto.NewMailboxReceiver(), key, raw); err != nil {
			t.Errorf("%s: a freshly sealed frame was refused by the gateway with %v; enabling PB-GW-2's bound rejects live traffic of this class (the PB-GW-6 brick)", name, err)
		}
	}
}

// TestS7bLiveTraffic_TheWindowIsTenRealMinutes measures the bound against the real
// producer's own stamp instead of a hand-set one, by moving the gateway's clock rather
// than the phone's. It pins that a phone-sealed frame survives a nine-minute delivery
// delay through the untrusted relay and is refused after eleven -- so the number in §6.0
// is the number a real frame experiences, not one that a fixture happens to satisfy.
func TestS7bLiveTraffic_TheWindowIsTenRealMinutes(t *testing.T) {
	key := testContentKey()
	var seq Sequencer
	sealedAt := time.Now()

	for name, raw := range phoneSeals(t, key, 7, &seq) {
		inside := sealedAt.Add(remotegw.InboundMaxAge - time.Minute)
		if _, err := remotegw.OpenMailboxFrameAt(crypto.NewMailboxReceiver(), key, raw, inside); err != nil {
			t.Errorf("%s: refused with %v after a %v delay, inside the %v bound", name, err, remotegw.InboundMaxAge-time.Minute, remotegw.InboundMaxAge)
		}
		outside := sealedAt.Add(remotegw.InboundMaxAge + time.Minute)
		if _, err := remotegw.OpenMailboxFrameAt(crypto.NewMailboxReceiver(), key, raw, outside); !errors.Is(err, crypto.ErrStaleAge) {
			t.Errorf("%s: err = %v after a %v delay, outside the %v bound; want crypto.ErrStaleAge", name, err, remotegw.InboundMaxAge+time.Minute, remotegw.InboundMaxAge)
		}
	}
}
