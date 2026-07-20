// FAILING-FIRST (TDD RED, GG-5) tests for the enrollment-keystone amendment
// (agents-tracker-qo4): the machine's Ed25519 grant-signing public key is pinned
// on the DEVICE at pairing so the phone can later verify sealed epoch grants
// (crypto.OpenEpochGrant needs the machine's Ed25519 pub). This mirrors the
// 2026-07-20 DeviceCommandSignPub amendment on the msg3 device payload, but for
// the msg2 machine payload.
//
// THE DEFECT these tests pin: MachinePayload carries the machine's two X25519
// keys (Noise-static via the handshake, RecipientPub for sealed boxes) and the
// epoch id, but NOT the machine's Ed25519 grant-signing public key. Without it a
// paired phone has no key to verify an EpochGrant signature against, so the whole
// async content-key delivery path (SealEpochGrant/OpenEpochGrant) cannot be
// bootstrapped from a pairing.
//
// THE CONTRACT these tests freeze (why they fail to COMPILE today, a valid RED
// per plan section B -- MachineSignPub is an undefined field):
//   - MachinePayload gains a MachineSignPub []byte field (the machine's Ed25519
//     grant-signing public key, 32 bytes), carried as a length-prefixed field
//     BEFORE the trailing epoch id so the epoch trailer contract is undisturbed.
//   - encode/decodeMachinePayload round-trip it losslessly without disturbing the
//     existing fields (a mis-ordered append/read would swap or garble a neighbour).
//   - After a full successful pair, the DEVICE outcome carries the value the
//     machine supplied: DeviceOutcome.Machine.MachineSignPub equals
//     MachineParams.Payload.MachineSignPub (the phone received it to pin).
//
// No production code is written here and no existing test is edited.
package pairing

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// A fixed, recognizable 32-byte sentinel standing in for the machine's Ed25519
// grant-signing public key. Distinct from every other key literal in the harness
// so a field-swap or mis-ordered append/read is caught.
func fillMachineSignPub() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0x3A
	}
	return b
}

// TestMachinePayload_SignPubRoundTrip pins the encode/decode contract: a
// MachinePayload carrying a non-empty MachineSignPub survives encode-then-decode
// losslessly, AND every pre-existing field (incl. the trailing epoch id) is still
// preserved -- guarding against a mis-ordered field append/read.
func TestMachinePayload_SignPubRoundTrip(t *testing.T) {
	want := MachinePayload{
		Hostname:            "test-machine.local",
		MachineRoutingID:    []byte("machine-routing-id-0001"),
		MachineRelayAuthPub: []byte("machine-relay-auth-pub-ed25519!!"), // 32B, distinct
		RecipientPub:        []byte("machine-recipient-x25519-pub-32b"),  // 31B, distinct
		MachineSignPub:      fillMachineSignPub(),                        // 32B, distinct sentinel
		EpochID:             9,
	}

	got, err := decodeMachinePayload(encodeMachinePayload(want))
	if err != nil {
		t.Fatalf("decodeMachinePayload after encode: %v", err)
	}

	if !bytes.Equal(got.MachineSignPub, want.MachineSignPub) {
		t.Errorf("MachineSignPub = %x, want %x (new field lost in round-trip)", got.MachineSignPub, want.MachineSignPub)
	}
	if got.Hostname != want.Hostname {
		t.Errorf("Hostname = %q, want %q", got.Hostname, want.Hostname)
	}
	if !bytes.Equal(got.MachineRoutingID, want.MachineRoutingID) {
		t.Errorf("MachineRoutingID = %x, want %x", got.MachineRoutingID, want.MachineRoutingID)
	}
	if !bytes.Equal(got.MachineRelayAuthPub, want.MachineRelayAuthPub) {
		t.Errorf("MachineRelayAuthPub = %x, want %x", got.MachineRelayAuthPub, want.MachineRelayAuthPub)
	}
	if !bytes.Equal(got.RecipientPub, want.RecipientPub) {
		t.Errorf("RecipientPub = %x, want %x", got.RecipientPub, want.RecipientPub)
	}
	if got.EpochID != want.EpochID {
		t.Errorf("EpochID = %d, want %d (trailing epoch trailer disturbed by the new field)", got.EpochID, want.EpochID)
	}
}

// TestPairing_ConveysMachineSignPub drives a full happy-path pair with the machine
// advertising a known MachineSignPub, and asserts the device received that exact
// key for pinning: DeviceOutcome.Machine.MachineSignPub equals the value the
// machine put in its msg2 payload (so the phone can verify epoch grants).
func TestPairing_ConveysMachineSignPub(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x2B)
	secret := fill32(0x6E)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	signPub := fillMachineSignPub()
	mp.Payload.MachineSignPub = signPub
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

	if !bytes.Equal(do.Machine.MachineSignPub, signPub) {
		t.Errorf("device received MachineSignPub = %x, want %x (machine grant-signing key must be pinned at pairing so the phone can verify epoch grants)",
			do.Machine.MachineSignPub, signPub)
	}
}
