package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for the SECOND half of ADR-007 B42's fast-clock finding: the
// destruction was reported as health.
//
// internal/phonecore/b42_staleage_test.go pins the receive path -- an age-refused frame is not
// acked, and correcting the clock recovers it. This is the seam a USER meets. The transport is
// up throughout, so nothing in the connection state machine has anything to say, and
// App.ConnectionState answered "online" -- which ConnectionBanner renders as "Connected to your
// machine." with no banner at all, while every frame the machine sends is being thrown away.
//
// The relay does not need a wrong phone clock to produce this. It schedules delivery: holding
// a page for eleven minutes and then releasing it puts every released frame past PB-TIME-2's
// ten-minute bound. That is what this test does, with the machine's own real sealer, and it is
// the shape ADR-007 D9 says to assume of the relay.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestB42_ThePhoneDoesNotReadOnlineWhileDiscardingItsInboundPlane.
//
// "online" is the ONE quiet state on this surface -- ConnectionBanner.of makes it the only one
// with no visible banner and no remedy -- so it is the one state a phone that cannot receive
// anything must not be in. The requirement is not that some particular replacement state is
// chosen; it is that a phone destroying its inbound plane does not report itself healthy.
func TestB42_ThePhoneDoesNotReadOnlineWhileDiscardingItsInboundPlane(t *testing.T) {
	h := newHarness(t)
	eventually(t, "the phone never came online", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})

	// The relay withheld this page for eleven minutes and is releasing it now. The frame is
	// the machine's own, sealed by the real gateway sealer under the real epoch content key:
	// only its authenticated stamp is old, which is exactly what a withheld page looks like.
	env, err := remotegw.SealControlReply(h.Keys.ContentKey, h.EpochID, 1, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-withheld",
	})
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	withheld, err := crypto.ParseEnvelope(env)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	withheld.Header.IssuedAt = time.Now().Add(-11 * time.Minute).UnixMilli()
	restamped, err := crypto.SealMailbox(h.Keys.ContentKey, withheld.Header, replyPlaintext(t, h))
	if err != nil {
		t.Fatalf("re-seal at the withheld stamp: %v", err)
	}
	if _, err := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, restamped.Marshal()); err != nil {
		t.Fatalf("append the withheld frame: %v", err)
	}

	eventually(t, "the phone kept reading \"online\" while every frame reaching it was being "+
		"discarded past PB-TIME-2's bound.\n"+
		"That is the half of this defect that makes it silent: the websocket is up, so the "+
		"connection state machine has nothing to say, and the user is told the machine is "+
		"connected while its output never arrives. Content destruction reported as health is "+
		"worse than an outage, because nobody looks for it.", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st != "online"
	})
}

// replyPlaintext is the plaintext SealControlReply produces, rebuilt so the re-stamped envelope
// carries a real command_reply rather than opaque bytes -- the frame must be one the phone
// WOULD have taken, or the test measures an unrecognised kind instead of the age bound.
func replyPlaintext(t *testing.T, h *harness) []byte {
	t.Helper()
	env, err := remotegw.SealControlReply(h.Keys.ContentKey, h.EpochID, 1, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-withheld",
	})
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	parsed, err := crypto.ParseEnvelope(env)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	pt, err := crypto.OpenMailbox(h.Keys.ContentKey, parsed)
	if err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	return pt
}
