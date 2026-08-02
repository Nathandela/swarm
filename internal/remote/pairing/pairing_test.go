// R-PAIR.1 (secret never on wire), R-PAIR.3 (XXpsk0 orchestration + payload
// carriage), R-PAIR.4 (SAS integration), R-PAIR.7 (mutual pinning + routing).
package pairing

import (
	"bytes"
	"context"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestPairing_XXpsk0Completes drives both ends (device initiator / machine
// responder) through the XXpsk0 handshake — the secret as PSK, the pairing
// prologue — to a completed pairing over the fake rendezvous, and confirms both
// ends reach a mutually-authenticated SAS.
func TestPairing_XXpsk0Completes(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x11)
	secret := fill32(0x5A)

	rec := &confirmRecorder{}
	mp := newMachineParams(mID, secret, rid, rec.fn(true, nil))
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil || do == nil {
		t.Fatal("nil outcome on a completed pairing")
	}
	if do.SAS != mo.SAS {
		t.Errorf("SAS differ across a clean handshake: machine %v device %v", mo.SAS, do.SAS)
	}
	for i, e := range mo.SAS {
		if e == "" {
			t.Errorf("SAS[%d] is empty; the 64-entry table must map every index", i)
		}
	}
}

// TestPairing_PayloadFields pins the exact field carriage of R-PAIR.3 (amended by
// D.0-A14 to also carry each side's sealed-box RecipientPub): msg2 carries the
// machine payload the device learns; msg3 carries the device payload the machine
// learns.
func TestPairing_PayloadFields(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x12)
	secret := fill32(0x5B)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil || do == nil {
		t.Fatal("nil outcome on a completed pairing")
	}
	// msg2: the device learned the machine's fields.
	assertMachinePayload(t, do.Machine, mp.Payload)
	// msg3: the machine learned the device's fields.
	assertDevicePayload(t, mo.Device, dp.Payload)
}

// TestPairing_SecretNeverOnWire asserts the 32-byte pairing secret (the XXpsk0
// PSK) never appears verbatim in any byte either end put on the wire, nor in the
// rendezvous id label — it is the OOB camera channel, never transmitted.
func TestPairing_SecretNeverOnWire(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x13)
	secret := fill32(0x5A) // sentinel: a verbatim leak is unmistakable

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, _, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil {
		t.Fatal("nil outcome on a completed pairing")
	}

	for _, end := range []*fakeRendezvous{mEnd, dEnd} {
		for i, frame := range end.sentBytes() {
			if bytes.Contains(frame, secret[:]) {
				t.Fatalf("pairing secret appeared verbatim in wire frame %d", i)
			}
		}
		for _, id := range end.createdIDs() {
			if bytes.Contains([]byte(id), secret[:]) {
				t.Fatal("pairing secret appeared verbatim in a rendezvous id")
			}
		}
	}
}

// TestSAS_MatchOnCleanHandshake pins R-PAIR.4 at the orchestration level: on a
// clean handshake the SAS the machine shows its operator equals the SAS the
// device computes, so an out-of-band comparison succeeds.
func TestSAS_MatchOnCleanHandshake(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x14)
	secret := fill32(0x5C)

	rec := &confirmRecorder{}
	mp := newMachineParams(mID, secret, rid, rec.fn(true, nil))
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	_, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if do == nil {
		t.Fatal("nil device outcome on a completed pairing")
	}
	machSAS, name, called := rec.snapshot()
	if called == 0 {
		t.Fatal("machine confirm was never invoked; the SAS must be shown to the operator")
	}
	if name != dp.Payload.DeviceName {
		t.Errorf("confirm shown device name %q, want %q", name, dp.Payload.DeviceName)
	}
	if machSAS != do.SAS {
		t.Errorf("machine-shown SAS %v != device SAS %v on a clean handshake", machSAS, do.SAS)
	}
}

// TestSAS_MismatchOnTamper pins R-PAIR.4's active-MITM defense at the pairing
// level. An attacker holding the photographed secret runs a machine leg toward
// the real device and a device leg toward the real machine (two divergent
// transcripts). The SAS the phone shows (device outcome) and the SAS the desktop
// shows (real machine's confirm) come from DIFFERENT channel bindings, so they
// differ and the operator's out-of-band comparison detects the interception.
func TestSAS_MismatchOnTamper(t *testing.T) {
	mID, _ := crypto.GenerateIdentity() // real machine
	dID, _ := crypto.GenerateIdentity() // real device
	aID, _ := crypto.GenerateIdentity() // active MITM (holds the photographed secret)
	rid := fill16(0x22)
	secret := fill32(0x33)

	// Leg 1: real device (initiator) <-> MITM-as-machine (responder).
	devEnd, atkMachEnd := newRendezvousPipe()
	// Leg 2: MITM-as-device (initiator) <-> real machine (responder).
	atkDevEnd, machEnd := newRendezvousPipe()

	machRec := &confirmRecorder{} // captures the SAS the real desktop shows
	atkRec := &confirmRecorder{}  // MITM auto-confirms its own leg toward the device

	realMachine := NewMachine(newMachineParams(mID, secret, rid, machRec.fn(true, nil)))
	atkMachine := NewMachine(newMachineParams(aID, secret, rid, atkRec.fn(true, nil)))

	ctx := context.Background()
	var (
		devOut *DeviceOutcome
		devErr error
	)
	done := make(chan struct{})
	go func() { devOut, devErr = RunDevice(ctx, newDeviceParams(dID, secret, rid), devEnd); done <- struct{}{} }()
	go func() { _, _ = atkMachine.Pair(ctx, atkMachEnd); done <- struct{}{} }()
	go func() { _, _ = RunDevice(ctx, newDeviceParams(aID, secret, rid), atkDevEnd); done <- struct{}{} }()
	go func() { _, _ = realMachine.Pair(ctx, machEnd); done <- struct{}{} }()
	for i := 0; i < 4; i++ {
		<-done
	}

	if devErr != nil {
		t.Fatalf("real device leg: %v", devErr)
	}
	if devOut == nil {
		t.Fatal("real device produced no outcome")
	}
	machSAS, _, called := machRec.snapshot()
	if called == 0 {
		t.Fatal("real machine never showed a SAS to its operator")
	}
	if devOut.SAS == machSAS {
		t.Errorf("SAS matched across a MITM (device %v, machine %v); interception would be undetectable", devOut.SAS, machSAS)
	}
}

// TestPairing_PinsAndStoresRouting pins R-PAIR.7: both ends pin the peer's Noise
// static and record its routing payload (routing id, relay-auth pub, recipient
// pub; the device also records the initial epoch id).
func TestPairing_PinsAndStoresRouting(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x15)
	secret := fill32(0x5D)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	mo, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil || do == nil {
		t.Fatal("nil outcome on a completed pairing")
	}
	if !bytes.Equal(mo.DeviceStatic, dID.NoiseStaticPublic()) {
		t.Errorf("machine pinned device static %x, want %x", mo.DeviceStatic, dID.NoiseStaticPublic())
	}
	if !bytes.Equal(do.MachineStatic, mID.NoiseStaticPublic()) {
		t.Errorf("device pinned machine static %x, want %x", do.MachineStatic, mID.NoiseStaticPublic())
	}
	assertDevicePayload(t, mo.Device, dp.Payload)
	assertMachinePayload(t, do.Machine, mp.Payload)
	if do.Machine.EpochID != mp.Payload.EpochID {
		t.Errorf("device recorded epoch %d, want %d", do.Machine.EpochID, mp.Payload.EpochID)
	}
}

// TestPairing_ThenLiveHandshakeUsesPin pins R-PAIR.7's downstream property: the
// machine static the device pinned at pairing authenticates a subsequent LIVE
// Noise XX handshake (crypto's live path), and a machine presenting any other
// static is rejected by the pin.
func TestPairing_ThenLiveHandshakeUsesPin(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x16)
	secret := fill32(0x5E)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(mp)
	_, mErr, do, dErr := drivePair(t, m, dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if do == nil {
		t.Fatal("nil device outcome on a completed pairing")
	}

	live := crypto.LivePrologue([]byte("machine-route"), []byte("device-route"))

	// Honest live handshake: the device pins the machine static from pairing.
	devLive, err := crypto.NewNoise(crypto.NoiseConfig{
		Initiator: true, Static: dID.NoiseStatic(), PeerStatic: do.MachineStatic, Prologue: live,
	})
	if err != nil {
		t.Fatalf("device live NewNoise: %v", err)
	}
	machLive, err := crypto.NewNoise(crypto.NoiseConfig{
		Initiator: false, Static: mID.NoiseStatic(), PeerStatic: dID.NoiseStaticPublic(), Prologue: live,
	})
	if err != nil {
		t.Fatalf("machine live NewNoise: %v", err)
	}
	if err := driveLiveXX(devLive, machLive); err != nil {
		t.Fatalf("live handshake with the pinned static failed: %v", err)
	}

	// An imposter machine (different static) is rejected by the pin.
	imposter, _ := crypto.GenerateIdentity()
	devLive2, err := crypto.NewNoise(crypto.NoiseConfig{
		Initiator: true, Static: dID.NoiseStatic(), PeerStatic: do.MachineStatic, Prologue: live,
	})
	if err != nil {
		t.Fatalf("device live NewNoise (2): %v", err)
	}
	impLive, err := crypto.NewNoise(crypto.NoiseConfig{
		Initiator: false, Static: imposter.NoiseStatic(), PeerStatic: dID.NoiseStaticPublic(), Prologue: live,
	})
	if err != nil {
		t.Fatalf("imposter live NewNoise: %v", err)
	}
	if err := driveLiveXX(devLive2, impLive); err == nil {
		t.Fatal("live handshake accepted a static other than the one pinned at pairing")
	}
}
