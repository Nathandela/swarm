// FAILING-FIRST (TDD RED, GG-5) tests for the ADR-007 amendment (2026-07-20,
// "Pairing conveys the device command-signing public key"; review finding
// agents-tracker-vam).
//
// THE DEFECT these tests pin: the device's Ed25519 command-signing public key
// (R-CRY.16, D4/A1) is the key the machine must pin at pairing so it can later
// verify daemon-bound command signatures (R-POL.9). Today the pairing handshake
// never carries it: pairing.DevicePayload (pairing.go) has only four fields
// (DeviceName, DeviceRoutingID, DeviceRelayAuthPub, RecipientPub), and
// encode/decodeDevicePayload round-trip only those four. So the machine finishes
// a pairing with NO device command-signing key to pin, leaving R-POL.9
// signature verification with nothing to check against — the pinning gap the
// amendment closes.
//
// THE CONTRACT these tests freeze (why they fail to COMPILE today, a valid RED
// per plan section B — DeviceCommandSignPub is an undefined field):
//   - DevicePayload gains a fifth field DeviceCommandSignPub []byte (the device's
//     Ed25519 command-signing public key, 32 bytes).
//   - encode/decodeDevicePayload round-trip the new field losslessly, without
//     disturbing the existing four (a mis-ordered append/read would swap fields).
//   - After a full successful pair, the machine's outcome carries the value the
//     device supplied: MachineOutcome.Device.DeviceCommandSignPub equals
//     DeviceParams.Payload.DeviceCommandSignPub (the machine received it to pin).
//
// No production code is written here and no existing test is edited; the
// implementer threads the field through to turn this RED green.
package pairing

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// A fixed, recognizable 32-byte sentinel standing in for the device's Ed25519
// command-signing public key (R-CRY.16; Ed25519 public keys are 32 bytes). It is
// deliberately DISTINCT from the harness's DeviceRelayAuthPub literal and from
// any RecipientPublic() so a field-swap or mis-ordered append/read is caught.
func fillCmdSignPub() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 0xC7
	}
	return b
}

// TestDevicePayload_CmdSignPubRoundTrip pins the encode/decode contract: a
// DevicePayload carrying a non-empty DeviceCommandSignPub (distinct from
// DeviceRelayAuthPub and RecipientPub) survives encode-then-decode losslessly,
// AND the four pre-existing fields are still preserved — guarding against a
// mis-ordered field append/read that would swap the new key with a neighbour.
func TestDevicePayload_CmdSignPubRoundTrip(t *testing.T) {
	want := DevicePayload{
		DeviceName:           "Test iPhone",
		DeviceRoutingID:      []byte("device-routing-id-0001"),
		DeviceRelayAuthPub:   []byte("device-relay-auth-pub-ed25519!!!"), // 32B, distinct
		RecipientPub:         []byte("device-recipient-x25519-pub-32by"), // 32B, distinct
		DeviceCommandSignPub: fillCmdSignPub(),                           // 32B, distinct sentinel
	}

	got, err := decodeDevicePayload(encodeDevicePayload(want))
	if err != nil {
		t.Fatalf("decodeDevicePayload after encode: %v", err)
	}

	if !bytes.Equal(got.DeviceCommandSignPub, want.DeviceCommandSignPub) {
		t.Errorf("DeviceCommandSignPub = %x, want %x (new field lost in round-trip)",
			got.DeviceCommandSignPub, want.DeviceCommandSignPub)
	}
	// The pre-existing four fields must be untouched: a mis-ordered append/read of
	// the new field would surface here as a swapped/garbled neighbour.
	if got.DeviceName != want.DeviceName {
		t.Errorf("DeviceName = %q, want %q", got.DeviceName, want.DeviceName)
	}
	if !bytes.Equal(got.DeviceRoutingID, want.DeviceRoutingID) {
		t.Errorf("DeviceRoutingID = %x, want %x", got.DeviceRoutingID, want.DeviceRoutingID)
	}
	if !bytes.Equal(got.DeviceRelayAuthPub, want.DeviceRelayAuthPub) {
		t.Errorf("DeviceRelayAuthPub = %x, want %x", got.DeviceRelayAuthPub, want.DeviceRelayAuthPub)
	}
	if !bytes.Equal(got.RecipientPub, want.RecipientPub) {
		t.Errorf("RecipientPub = %x, want %x", got.RecipientPub, want.RecipientPub)
	}
}

// TestPairing_ConveysDeviceCommandSignPub drives a full happy-path pair (device
// initiator / machine responder) over the in-memory rendezvous harness, with the
// device supplying a known DeviceCommandSignPub. It asserts the machine received
// that exact key for pinning: MachineOutcome.Device.DeviceCommandSignPub equals
// the value the device put in its msg3 payload (R-CRY.16 pinned at pairing so
// R-POL.9 daemon signature verification has a key to check against).
func TestPairing_ConveysDeviceCommandSignPub(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x17)
	secret := fill32(0x5F)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	dp := newDeviceParams(dID, secret, rid)
	// The device advertises its command-signing public key for the machine to pin.
	cmdSignPub := fillCmdSignPub()
	dp.Payload.DeviceCommandSignPub = cmdSignPub

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

	if !bytes.Equal(mo.Device.DeviceCommandSignPub, cmdSignPub) {
		t.Errorf("machine received DeviceCommandSignPub = %x, want %x (device command-signing key must be pinned at pairing, R-CRY.16 / R-POL.9)",
			mo.Device.DeviceCommandSignPub, cmdSignPub)
	}
}
