package pairing

// ADR-007 B46/B52 -- THE CONSENT IS RELEASED BEFORE THE OPERATOR IS ASKED.
//
// The phone operator does the one thing the design asks of them: they compare the SAS,
// see it does not match the desktop, and reject. RunDevice fails closed and pins nothing.
// But msg3 -- carrying the phone's STANDING relay consent -- went out six lines earlier,
// so the interceptor walks away with Sign_phone(ConsentMessage(attackerRID)): enough to
// authorize itself over the phone's route and then permanently ban the phone, whose
// relay-auth key is minted once per install. Recovery is a reinstall.
//
// The fix is NOT a reordering, and TestB52_NoSharedSASExistsBeforeMsg3 below is why. The
// signature therefore leaves msg3 for a fourth, authenticated frame the device sends only
// after its gate, and msg3 carries ConsentDeferred in its place: the machine's pre-confirm
// refusal keeps its position and its meaning, distinguishing a build that WILL grant a
// route once its operator confirms from one that grants none. The marker conveys no
// authority, which is what keeps this from being the same argument one level down.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestB52_NoSharedSASExistsBeforeMsg3 is the measurement that rules out every
// "just reorder RunDevice" fix, kept executable because it is exactly what a future
// simplifier will otherwise re-derive by breaking it.
//
// Two facts, and the second is the one that bites:
//
//  1. After msg1+msg2 NEITHER end has a channel binding. crypto.NoiseSession captures it
//     only in establish(), which XXpsk0 reaches at msg3, and internal/remote/crypto is
//     frozen. There is no early SAS to gate on.
//
//  2. The device's binding appears when it WRITES msg3 -- i.e. it is fixed by, and
//     commits to, the payload the device has just chosen. So the SAS attests the very
//     bytes it would have to gate: nothing carried in msg3 can be decided after the SAS
//     is shown.
//
// AND THE NEAR-MISS, which looks strictly better and is not: the device could write msg3
// LOCALLY (fact 2 gives it a binding), gate on the SAS, and only then send -- releasing
// nothing early and needing no fourth frame. The machine's binding is still empty at that
// instant, because msg3 has not arrived, so the phone operator would be comparing against
// a desktop that is still blocked on Recv. A one-sided SAS is not a comparison; it is the
// user confirming a number against nothing, which is worse than no gate because it looks
// like one.
func TestB52_NoSharedSASExistsBeforeMsg3(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

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

	msg1, err := dev.WriteMessage(nil)
	if err != nil {
		t.Fatalf("write msg1: %v", err)
	}
	if _, err := mach.ReadMessage(msg1); err != nil {
		t.Fatalf("read msg1: %v", err)
	}
	msg2, err := mach.WriteMessage(encodeMachinePayload(MachinePayload{Hostname: "m", OperatorNamespace: "owner"}))
	if err != nil {
		t.Fatalf("write msg2: %v", err)
	}
	if _, err := dev.ReadMessage(msg2); err != nil {
		t.Fatalf("read msg2: %v", err)
	}

	// (1) No SAS exists on either side after msg2.
	if len(dev.ChannelBinding()) != 0 || len(mach.ChannelBinding()) != 0 {
		t.Fatalf("a channel binding existed after msg2 (device %d bytes, machine %d bytes); "+
			"if that is ever true, the gate belongs there and this whole design is unnecessary",
			len(dev.ChannelBinding()), len(mach.ChannelBinding()))
	}

	// (2) The device's binding is created BY writing msg3, so it commits to msg3's payload.
	msg3, err := dev.WriteMessage(encodeDevicePayload(DevicePayload{DeviceName: "d", ConsentDeferred: true}))
	if err != nil {
		t.Fatalf("write msg3: %v", err)
	}
	if len(dev.ChannelBinding()) == 0 {
		t.Fatal("the device has no binding even after writing msg3")
	}
	// The near-miss: at this instant the device COULD show a SAS, and the machine could not.
	if len(mach.ChannelBinding()) != 0 {
		t.Fatal("the machine had a binding before msg3 reached it; the local-write-then-gate " +
			"design would then be viable and this test is the wrong shape")
	}
	if _, err := mach.ReadMessage(msg3); err != nil {
		t.Fatalf("read msg3: %v", err)
	}
	if !bytes.Equal(dev.ChannelBinding(), mach.ChannelBinding()) {
		t.Fatal("the two ends disagree on the channel binding after msg3")
	}
}

// TestB52_APhoneThatRefusesTheSASEnrollsNothingAnywhere is the reviewer's probe as an
// in-tree fence, at the only boundary that matters: what the OTHER END ends up holding.
//
// It reads two ways at once, and both are defects at HEAD. If the machine is an
// INTERCEPTOR, its "operator" always allows because it is not trying to complete a
// pairing, it is trying to reach the consent -- and it reaches it. If the machine is the
// owner's REAL machine whose desktop operator allowed, nothing after msg3 tells it the
// phone refused, so it returns an outcome, enroll.Enroll runs, AddSole commits, and
// PB-STATE-10's single-device slot is spent on a pairing the user refused. One assertion
// closes both: an operator refusal on the phone yields NO machine outcome.
func TestB52_APhoneThatRefusesTheSASEnrollsNothingAnywhere(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	consentAsked := false
	dp.Consent = func(MachinePayload) ([]byte, error) {
		consentAsked = true
		return bytes.Repeat([]byte{0x5C}, ed25519.SignatureSize), nil
	}
	errSASMismatch := errors.New("operator rejected the SAS")
	dp.DeviceSAS = func(context.Context, [6]string) error { return errSASMismatch }

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)

	if !errors.Is(dErr, errSASMismatch) || do != nil {
		t.Fatalf("device: outcome=%v err=%v, want the SAS rejection to fail the pairing closed", do, dErr)
	}
	if consentAsked {
		t.Error("the phone signed its standing relay consent for a pairing its own operator rejected")
	}
	if mo != nil {
		t.Fatalf("the machine produced an outcome for a pairing the phone's operator REFUSED "+
			"(consent=%d bytes, err=%v).\n"+
			"  As the owner's machine that enrolls the device and spends the single-device slot on a "+
			"pairing the user declined. As an interceptor it holds a signature that is enough for "+
			"authorize_device(phone) and then device_revoke(phone): the phone is banned by a pairing "+
			"it refused, only the banner lifts a ban, and the banner is the interceptor.",
			len(mo.Device.ConsentSig), mErr)
	}
	if !errors.Is(mErr, ErrNoConsent) {
		t.Fatalf("machine err = %v, want ErrNoConsent", mErr)
	}
}

// TestB52_ARefusalIsAnAnsweredAbortNotSilence. A machine parked on a receive, waiting for
// a consent the phone decided never to send, reports a timeout for a question that was
// already settled -- the desktop operator sits at a prompt for a refusal that is minutes
// old. So a refusal is SENT, in the same frame shape, carrying nothing.
func TestB52_ARefusalIsAnAnsweredAbortNotSilence(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = func(context.Context, [6]string) error { return errors.New("rejected") }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mEnd, dEnd := newRendezvousPipe()

	type res struct {
		out *MachineOutcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := NewMachine(mp).Pair(ctx, mEnd)
		done <- res{out, err}
	}()
	if _, err := RunDevice(ctx, dp, dEnd); err == nil {
		t.Fatal("RunDevice returned nil for a rejected SAS")
	}

	select {
	case r := <-done:
		if r.out != nil || !errors.Is(r.err, ErrNoConsent) {
			t.Fatalf("machine: outcome=%v err=%v, want a fail-closed ErrNoConsent", r.out, r.err)
		}
	case <-ctx.Done():
		t.Fatal("the machine is still waiting for a consent the phone declined to give; the " +
			"refusal must be an ANSWER, not silence")
	}

	// The device is not left hanging either: the machine's decline reaches it.
	if got := len(mEnd.completedIDs()); got == 0 {
		t.Error("the machine never burned the rendezvous on a refused pairing")
	}
}

// TestB52_ADeviceThatSaysNothingAfterMsg3FailsClosed is the other half of the same
// property: an abort that is SENT is answered promptly, and an abort that is merely
// silence must still never be mistaken for a grant. The adversary is a build that
// declares ConsentDeferred and then walks away.
func TestB52_ADeviceThatSaysNothingAfterMsg3FailsClosed(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	confirms := 0
	mp := newMachineParams(mID, secret, rid, func(context.Context, [6]string, string) (bool, error) {
		confirms++
		return true, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mEnd, dEnd := newRendezvousPipe()

	type res struct {
		out *MachineOutcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := NewMachine(mp).Pair(ctx, mEnd)
		done <- res{out, err}
	}()

	sess := handRolledDeviceThroughMsg3(t, ctx, dID, secret, rid, dEnd, DevicePayload{
		DeviceName:           "Silent iPhone",
		DeviceRoutingID:      []byte("device-routing-id-0001"),
		DeviceRelayAuthPub:   dID.RecipientPublic(),
		RecipientPub:         dID.RecipientPublic(),
		DeviceCommandSignPub: dID.RecipientPublic(),
		ConsentDeferred:      true,
	})
	_ = sess // the device says nothing further, which is the adversary.

	select {
	case r := <-done:
		if r.out != nil {
			t.Fatalf("the machine enrolled a device that never granted it a route (err=%v)", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the machine neither accepted nor refused a device that fell silent after msg3")
	}
}

// TestB52_AConsentInsideMsg3IsRefusedBeforeTheConfirm keeps the pre-B52 shape from being
// reachable. A device that puts its signature in msg3 has released it before its operator
// saw anything, which is the whole defect; the machine cannot repair that phone, so it
// refuses the pairing rather than completing one whose credential is already harvestable.
//
// The device leg is hand-rolled, because RunDevice no longer produces such a msg3 -- the
// adversary is exactly what RunDevice is not: an older build, or a hostile one. This is
// also the fence on the marker's POSITION: the refusal must come before the confirm, so
// the operator's attention is never spent on a pairing that cannot work.
func TestB52_AConsentInsideMsg3IsRefusedBeforeTheConfirm(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	confirms := 0
	mp := newMachineParams(mID, secret, rid, func(context.Context, [6]string, string) (bool, error) {
		confirms++
		return true, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mEnd, dEnd := newRendezvousPipe()

	type res struct {
		out *MachineOutcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := NewMachine(mp).Pair(ctx, mEnd)
		done <- res{out, err}
	}()

	handRolledDeviceThroughMsg3(t, ctx, dID, secret, rid, dEnd, DevicePayload{
		DeviceName:           "Pre-B52 iPhone",
		DeviceRoutingID:      []byte("device-routing-id-0001"),
		DeviceRelayAuthPub:   dID.RecipientPublic(),
		RecipientPub:         dID.RecipientPublic(),
		DeviceCommandSignPub: dID.RecipientPublic(),
		ConsentSig:           bytes.Repeat([]byte{0x5C}, ed25519.SignatureSize),
	})

	select {
	case r := <-done:
		if r.out != nil || !errors.Is(r.err, ErrNoConsent) {
			t.Fatalf("machine: outcome=%v err=%v, want a fail-closed ErrNoConsent for a msg3 that "+
				"carries the consent inline", r.out, r.err)
		}
	case <-ctx.Done():
		t.Fatal("the machine neither accepted nor refused a msg3 carrying an inline consent")
	}
	if confirms != 0 {
		t.Fatalf("the operator was prompted %d times for a pairing whose credential was already "+
			"released; the refusal must come BEFORE the confirm", confirms)
	}
}

// handRolledDeviceThroughMsg3 drives a real XXpsk0 initiator to the end of msg3 with a
// caller-chosen payload, so a test can be an adversarial device build rather than
// RunDevice. It returns the established session for tests that send further frames.
func handRolledDeviceThroughMsg3(t *testing.T, ctx context.Context, dID *crypto.Identity,
	secret [32]byte, rid [16]byte, dEnd RendezvousTransport, payload DevicePayload) *crypto.NoiseSession {
	t.Helper()
	sess, err := crypto.NewNoise(crypto.NoiseConfig{
		Initiator:         true,
		Static:            dID.NoiseStatic(),
		AllowUnpinnedPeer: true,
		PSK:               secret[:],
		Prologue:          crypto.PairPrologue(rid[:]),
	})
	if err != nil {
		t.Fatalf("device noise: %v", err)
	}
	msg1, err := sess.WriteMessage(nil)
	if err != nil {
		t.Fatalf("write msg1: %v", err)
	}
	if err := dEnd.Send(ctx, msg1); err != nil {
		t.Fatalf("send msg1: %v", err)
	}
	msg2, err := dEnd.Recv(ctx)
	if err != nil {
		t.Fatalf("recv msg2: %v", err)
	}
	if _, err := sess.ReadMessage(msg2); err != nil {
		t.Fatalf("read msg2: %v", err)
	}
	msg3, err := sess.WriteMessage(encodeDevicePayload(payload))
	if err != nil {
		t.Fatalf("write msg3: %v", err)
	}
	if err := dEnd.Send(ctx, msg3); err != nil {
		t.Fatalf("send msg3: %v", err)
	}
	return sess
}
