package pairing

// ADR-007 B38 -- THE PAIRING CEREMONY IS WHERE CONSENT IS GIVEN, SO IT IS WHERE CONSENT
// MUST BE SIGNED, and B33/B34's relay pin has no channel but this one either.
//
// The relay cannot witness the QR/SAS ceremony. Everything it knows about who consented to
// what has to arrive over a verb any party can call, so the ceremony's outcome has to be
// carried: msg3 gains the device's relay-auth signature over the routing id of the machine
// it authenticated in msg2. These tests pin the three properties that make that carriage
// worth anything, and the fourth that shares the same message.
//
//	1. the consent SURVIVES the wire (a field silently dropped is a fence that never fires)
//	2. it is signed over the machine the device AUTHENTICATED, not one it was handed
//	3. its absence fails the pairing CLOSED on BOTH sides
//	4. the relay SPKI pin (B33/B34) survives msg2 -- it has no other channel to the phone
//
// Property 2 is the one worth being explicit about. The consent's whole value is that only
// the named grantee can use it, and the grantee is named by a routing id DERIVED from
// MachineRelayAuthPub. A device that signed a consent built before the handshake would be
// consenting to whatever machine it happened to be configured for, which on a photographed
// QR is not the machine on the other end.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestB38_ConsentAndRelayPinSurviveTheWire is the round-trip fence for both fields added to
// the handshake payloads. It is separate from the handshake tests because a field lost in
// the codec fails here in microseconds, with a message that names the field, instead of as
// an authorization refusal three packages away.
func TestB38_ConsentAndRelayPinSurviveTheWire(t *testing.T) {
	wantPin := bytes.Repeat([]byte{0xD4}, 32)
	mach := MachinePayload{
		Hostname:            "test-machine.local",
		MachineRoutingID:    []byte("machine-routing-id-0001"),
		MachineRelayAuthPub: bytes.Repeat([]byte{0xC3}, 32),
		RecipientPub:        bytes.Repeat([]byte{0xA1}, 32),
		MachineSignPub:      bytes.Repeat([]byte{0xB2}, 32),
		MachineEndpointID:   "endpoint-1",
		RelaySPKIPin:        wantPin,
		EpochID:             9,
	}
	gotMach, err := decodeMachinePayload(encodeMachinePayload(mach))
	if err != nil {
		t.Fatalf("decode machine payload: %v", err)
	}
	if !bytes.Equal(gotMach.RelaySPKIPin, wantPin) {
		t.Fatalf("MachinePayload.RelaySPKIPin = %x, want %x.\n"+
			"  msg2 is the ONLY channel that can carry the relay's SPKI pin to a handset: the QR\n"+
			"  cannot (MaxRelayURLLen = 39 leaves one byte of slack in the v6-L symbol) and every\n"+
			"  later frame rides the connection the pin exists to protect. Dropped here, a\n"+
			"  pinning-only platform refuses every dial and cannot say why (ADR-007 B33/B34).",
			gotMach.RelaySPKIPin, wantPin)
	}
	if gotMach.EpochID != mach.EpochID {
		t.Fatalf("EpochID = %d, want %d: the new field was appended AFTER the epoch trailer",
			gotMach.EpochID, mach.EpochID)
	}

	wantConsent := bytes.Repeat([]byte{0x5C}, ed25519.SignatureSize)
	dev := DevicePayload{
		DeviceName:           "Test iPhone",
		DeviceRoutingID:      []byte("device-routing-id-0001"),
		DeviceRelayAuthPub:   bytes.Repeat([]byte{0xE5}, 32),
		RecipientPub:         bytes.Repeat([]byte{0xA2}, 32),
		DeviceCommandSignPub: bytes.Repeat([]byte{0xB3}, 32),
		ConsentSig:           wantConsent,
	}
	gotDev, err := decodeDevicePayload(encodeDevicePayload(dev))
	if err != nil {
		t.Fatalf("decode device payload: %v", err)
	}
	if !bytes.Equal(gotDev.ConsentSig, wantConsent) {
		t.Fatalf("DevicePayload.ConsentSig = %x, want %x.\n"+
			"  Without it on the wire the machine has nothing to present at authorize_device, and\n"+
			"  the relay is back to inferring consent from a public key anyone can read (B37/B38).",
			gotDev.ConsentSig, wantConsent)
	}
}

// TestB38_TheDeviceConsentsToTheMachineItAuthenticated is property 2, and it is the one a
// plausible-looking implementation gets wrong: signing the consent from the DeviceParams the
// caller assembled before the handshake, rather than from the msg2 the Noise channel just
// authenticated.
//
// The distinction is the whole security property. The consent names its grantee by a routing
// id derived from MachineRelayAuthPub, so a consent built before the handshake names whatever
// machine the device happened to be configured for -- which, on a photographed QR, is not the
// machine on the other end of the wire.
func TestB38_TheDeviceConsentsToTheMachineItAuthenticated(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	// A machine payload nothing outside the handshake could have guessed.
	mp.Payload.MachineRelayAuthPub = bytes.Repeat([]byte{0x77}, 32)

	dp := newDeviceParams(dID, secret, rid)
	var sawPub []byte
	dp.Consent = func(m MachinePayload) ([]byte, error) {
		sawPub = append([]byte(nil), m.MachineRelayAuthPub...)
		return append([]byte("consent-for:"), m.MachineRelayAuthPub...), nil
	}

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, _, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil || dErr != nil {
		t.Fatalf("pairing failed: machine=%v device=%v", mErr, dErr)
	}
	if !bytes.Equal(sawPub, mp.Payload.MachineRelayAuthPub) {
		t.Fatalf("the consent callback was handed MachineRelayAuthPub %x, want the msg2 value %x.\n"+
			"  A consent signed over anything but the AUTHENTICATED machine payload names a grantee\n"+
			"  the device never spoke to, which is the same as no consent at all.",
			sawPub, mp.Payload.MachineRelayAuthPub)
	}
	want := append([]byte("consent-for:"), mp.Payload.MachineRelayAuthPub...)
	if !bytes.Equal(mo.Device.ConsentSig, want) {
		t.Fatalf("MachineOutcome.Device.ConsentSig = %x, want %x: the machine did not receive the "+
			"consent the device signed", mo.Device.ConsentSig, want)
	}
}

func TestAuditRound2_ConsentMustNotBeReleasedBeforeTheSASGate(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)
	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	consentReleased := false
	dp.Consent = func(MachinePayload) ([]byte, error) {
		consentReleased = true
		return bytes.Repeat([]byte{0x5C}, ed25519.SignatureSize), nil
	}
	sasSawConsent := false
	dp.DeviceSAS = func(context.Context, [6]string) error {
		sasSawConsent = consentReleased
		return nil
	}

	mEnd, dEnd := newRendezvousPipe()
	_, mErr, _, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil || dErr != nil {
		t.Fatalf("pairing failed: machine=%v device=%v", mErr, dErr)
	}
	if sasSawConsent {
		t.Fatal("the phone released its standing relay consent before the user even saw the SAS")
	}
}

// TestB38_ADeviceWithNoConsentCallbackFailsClosed is half of property 3: a device that
// cannot produce a consent must not complete a pairing. Completing would leave the phone
// paired to a machine that can never deliver it the epoch grant -- a pairing that looks
// successful and does nothing.
//
// The refusal lands AFTER msg2, because that is the first moment the device knows what it
// would be consenting to, so the test drives a real machine leg to get there. The machine
// leg is then bounded rather than joined: its msg3 never arrives, which is the correct
// consequence of the device refusing and not a property this test is about.
func TestB38_ADeviceWithNoConsentCallbackFailsClosed(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)
	dp.Consent = nil

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mEnd, dEnd := newRendezvousPipe()
	go func() { _, _ = NewMachine(mp).Pair(ctx, mEnd) }()

	if _, err := RunDevice(ctx, dp, dEnd); !errors.Is(err, ErrNoConsent) {
		t.Fatalf("RunDevice with no consent callback = %v, want ErrNoConsent.\n"+
			"  A pairing that completes here leaves the phone pinned to a machine whose very first "+
			"act -- delivering the epoch grant -- the relay will refuse.", err)
	}
}

// TestB38_AMachineRefusesAMsg3WithNoConsentBeforeTheConfirm is the other half of property 3,
// and the ordering assertion is the point: the refusal must land BEFORE the operator is
// prompted. A confirm spent on a pairing that cannot work consumes the owner's attention on
// a device that will be refused a moment later at enroll.Enroll, and PB-STATE-10's recovery
// path is already the wall this product makes people hit.
//
// The device leg is HAND-ROLLED rather than RunDevice, because RunDevice fails closed on its
// own side and never sends such a msg3. The adversary here is exactly the thing RunDevice is
// not: an older build, or a hostile one, that completes the handshake and grants no route.
func TestB38_AMachineRefusesAMsg3WithNoConsentBeforeTheConfirm(t *testing.T) {
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

	// The hostile device: a real XXpsk0 initiator with the QR secret, whose msg3 carries a
	// well-formed DevicePayload and an EMPTY ConsentSig.
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
	msg3, err := sess.WriteMessage(encodeDevicePayload(DevicePayload{
		DeviceName:           "Hostile iPhone",
		DeviceRoutingID:      []byte("device-routing-id-0001"),
		DeviceRelayAuthPub:   dID.RecipientPublic(),
		RecipientPub:         dID.RecipientPublic(),
		DeviceCommandSignPub: dID.RecipientPublic(),
	}))
	if err != nil {
		t.Fatalf("write msg3: %v", err)
	}
	if err := dEnd.Send(ctx, msg3); err != nil {
		t.Fatalf("send msg3: %v", err)
	}

	select {
	case r := <-done:
		if r.out != nil {
			t.Fatalf("the machine produced an outcome for a device that granted no relay route; "+
				"the pairing would enroll a device the machine can never reach (err=%v)", r.err)
		}
		if !errors.Is(r.err, ErrNoConsent) {
			t.Fatalf("machine err = %v, want ErrNoConsent", r.err)
		}
	case <-ctx.Done():
		t.Fatal("the machine neither accepted nor refused a msg3 with no consent; it must fail " +
			"CLOSED, and it must decline so the waiting device is not left on rt.Recv until its " +
			"own deadline")
	}
	if confirms != 0 {
		t.Fatalf("the operator was prompted %d times for a pairing that cannot work; the refusal "+
			"must come BEFORE the confirm", confirms)
	}
}
