package pairing

// PB-SAS-4 -- THE ACKNOWLEDGEMENT'S KEYING ON THE CHANNEL BINDING IS ASSERTED BY NOTHING.
//
// PB-SAS-4 is declared closed on one sentence in acceptAck's doc comment: "A machine that
// commits only on a frame no party without this binding can produce makes the comparison those
// operators performed reach the accept/decline exchange too." That is the whole argument, and
// round 6 found it unfenced -- BOTH mutations that destroy it leave the entire tree green:
//
//   - recvAck stops comparing, so any frame that merely DECRYPTS is an acknowledgement;
//   - acceptAck stops reading sess.ChannelBinding() and keys on a constant, so the digest
//     attests the ack label and the decision byte and nothing about WHICH ceremony produced it.
//
// Neither is exploitable today: crypto.NoiseSession splits its transport keys by direction, so
// a relay-reflected frame fails the AEAD before either check is reached. That is precisely why
// the gap is worth fencing rather than shrugging at -- the property is currently held by a
// DIFFERENT mechanism in a FROZEN package, and PB-SAS-4 does not name that mechanism. If the
// transport ever stops carrying it, nothing here would notice.
//
// The two arms below are deliberately split by MECHANISM rather than by scenario, because the
// two mutations break different halves and a single end-to-end ceremony test would catch only
// the first. Arm 1 asserts acceptAck CONSUMES the binding; arm 2 asserts recvAck ENFORCES it.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// pbSAS4Session drives one complete PSK handshake and returns both ends, each carrying the
// channel binding that handshake produced. Two calls with different secret/rid tags produce
// two sessions whose bindings differ, which is the discriminator both arms below rest on.
func pbSAS4Session(t *testing.T, tag byte) (device, machine *crypto.NoiseSession) {
	t.Helper()
	mID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	dID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("device identity: %v", err)
	}
	secret, rid := fill32(tag), fill16(tag)
	newSess := func(initiator bool, id *crypto.Identity) *crypto.NoiseSession {
		t.Helper()
		s, err := crypto.NewNoise(crypto.NoiseConfig{
			Initiator: initiator, Static: id.NoiseStatic(), AllowUnpinnedPeer: true,
			PSK: secret[:], Prologue: crypto.PairPrologue(rid[:]),
		})
		if err != nil {
			t.Fatalf("new noise: %v", err)
		}
		return s
	}
	dev, mach := newSess(true, dID), newSess(false, mID)
	if err := driveLiveXX(dev, mach); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if len(dev.ChannelBinding()) == 0 || !bytes.Equal(dev.ChannelBinding(), mach.ChannelBinding()) {
		t.Fatalf("the two ends did not converge on a channel binding; the fixture is broken, not the code")
	}
	return dev, mach
}

// TestPBSAS4_TheAcknowledgementIsKeyedOnTheChannelBinding is arm 1: acceptAck must actually
// READ the session's channel binding.
//
// This is the arm that catches a constant-keyed acceptAck. It cannot be written as a ceremony
// test: if both ends compute the same wrong constant they still agree, the pairing still
// succeeds, and every end-to-end assertion in this package stays green -- which is exactly what
// round 6 measured. The property is only visible by comparing acks ACROSS two ceremonies.
func TestPBSAS4_TheAcknowledgementIsKeyedOnTheChannelBinding(t *testing.T) {
	_, machA := pbSAS4Session(t, 0x5A)
	_, machB := pbSAS4Session(t, 0xA5)

	if bytes.Equal(machA.ChannelBinding(), machB.ChannelBinding()) {
		t.Fatal("two independent handshakes produced the SAME channel binding; the fixture cannot discriminate")
	}

	ackA, ackB := acceptAck(machA, decisionAccept), acceptAck(machB, decisionAccept)
	if bytes.Equal(ackA, ackB) {
		t.Errorf("acceptAck returned the same %d bytes for two ceremonies with DIFFERENT channel bindings "+
			"(%x vs %x): the acknowledgement is not keyed on the binding at all, so it attests the label and "+
			"the decision byte and nothing about WHICH handshake produced it. PB-SAS-4 is closed on the claim "+
			"that no party without this binding can produce the frame; a constant-keyed ack is producible by "+
			"any party that knows the label", len(ackA), machA.ChannelBinding()[:8], machB.ChannelBinding()[:8])
	}

	// And the decision byte must still separate an accept from a decline, or the frame would
	// acknowledge "a decision" rather than "the acceptance".
	if bytes.Equal(acceptAck(machA, decisionAccept), acceptAck(machA, decisionDecline)) {
		t.Error("acceptAck ignores the decision byte: an acknowledgement of a DECLINE would satisfy the machine's wait for an acknowledgement of its ACCEPTANCE")
	}
}

// TestPBSAS4_RecvAckRefusesAnAckOverAnotherBinding is arm 2: recvAck must ENFORCE the keying.
//
// The frame handed to recvAck is sealed by the real peer of the real session, so it decrypts
// cleanly -- it is a well-formed frame from the right party on the right transport. The ONLY
// thing wrong with it is that its digest is over a different ceremony's binding. A recvAck that
// skipped the comparison would accept it, which is the mutation that stayed green tree-wide.
func TestPBSAS4_RecvAckRefusesAnAckOverAnotherBinding(t *testing.T) {
	dev, mach := pbSAS4Session(t, 0x11)
	_, foreign := pbSAS4Session(t, 0x22)

	mEnd, dEnd := newRendezvousPipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The device seals an acknowledgement over the FOREIGN session's binding and sends it
	// down the genuine transport.
	frame, err := dev.Encrypt(acceptAck(foreign, decisionAccept))
	if err != nil {
		t.Fatalf("device encrypt: %v", err)
	}
	go func() { _ = dEnd.Send(ctx, frame) }()

	if err := recvAck(ctx, mach, mEnd); err == nil {
		t.Error("recvAck ACCEPTED an acknowledgement computed over another ceremony's channel binding; " +
			"the frame decrypted, so the only thing that could refuse it is the binding comparison, and it did not. " +
			"The machine would pin a device on a frame that attests nothing about the handshake the two operators compared")
	}

	// The control: the SAME transport and the SAME session must accept the RIGHT ack, or the
	// arm above would pass against a recvAck that refuses everything.
	frame, err = dev.Encrypt(acceptAck(mach, decisionAccept))
	if err != nil {
		t.Fatalf("device encrypt (control): %v", err)
	}
	go func() { _ = dEnd.Send(ctx, frame) }()
	if err := recvAck(ctx, mach, mEnd); err != nil {
		t.Errorf("recvAck refused the genuine acknowledgement for this session: %v; the arm above proves nothing if nothing is accepted", err)
	}
}
