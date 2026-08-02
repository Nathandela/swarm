// FAILING-FIRST (TDD RED, GG-5) tests for slice S19's first production hole: the phone has
// no production source for the MACHINE ENDPOINT ID, so crypto.Command.Canonical -- which
// refuses an empty Machine -- rejects every signed verb the phone will ever author.
//
// WHY THE PAIRING PAYLOAD IS WHERE THE ID BELONGS. The phone's other three machine
// coordinates (Noise static, grant-signing pub, relay-auth pub) are all pinned HERE, in
// msg2, because a pairing is the one authenticated moment at which the phone learns who the
// machine is. The endpoint id is the same class of fact -- it names the machine the phone's
// commands are addressed to -- and it is needed BEFORE the gateway exists: PB-LIFE-3 starts
// the sidecar only after pairing, so an id delivered on the gateway's reconcile record would
// arrive after the phone had already been unable to author anything, and would leave a paired
// phone with a machine it cannot name for as long as the sidecar is down.
//
// THE CONTRACT these tests freeze, mirroring the 2026-07-20 MachineSignPub amendment
// (machinesignpub_test.go) field-for-field:
//   - MachinePayload gains MachineEndpointID string, carried as a length-prefixed field
//     BEFORE the trailing epoch id so the epoch-trailer contract is undisturbed.
//   - encode/decodeMachinePayload round-trip it losslessly and disturb no neighbour.
//   - After a full successful pair the DEVICE outcome carries the machine's value, so the
//     phone has something to write into durable state.
//
// No production code is written here and no existing test is edited.
package pairing

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s19EndpointID is a recognizable stand-in for the daemon's federation endpoint id
// (skeleton.endpointID's "ep-" + 8 hex chars). Distinct from every other literal in the
// harness so a field-swap or a mis-ordered append/read is unmistakable.
const s19EndpointID = "ep-5a56c05a"

// TestS19_MachinePayload_EndpointIDRoundTrip pins the encode/decode contract: a payload
// carrying a non-empty MachineEndpointID survives encode-then-decode, AND every
// pre-existing field -- including the trailing epoch id, which a mis-ordered append would
// consume -- is still preserved.
func TestS19_MachinePayload_EndpointIDRoundTrip(t *testing.T) {
	want := MachinePayload{
		Hostname:            "s19-machine.local",
		MachineRoutingID:    []byte("machine-routing-id-0001"),
		MachineRelayAuthPub: []byte("machine-relay-auth-pub-ed25519!!"), // 32B, distinct
		RecipientPub:        []byte("machine-recipient-x25519-pub-32b"), // 31B, distinct
		MachineSignPub:      fillMachineSignPub(),                       // 32B, distinct sentinel
		MachineEndpointID:   s19EndpointID,
		EpochID:             11,
	}

	got, err := decodeMachinePayload(encodeMachinePayload(want))
	if err != nil {
		t.Fatalf("decodeMachinePayload after encode: %v", err)
	}

	if got.MachineEndpointID != want.MachineEndpointID {
		t.Errorf("MachineEndpointID = %q, want %q (new field lost in round-trip)",
			got.MachineEndpointID, want.MachineEndpointID)
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
	if !bytes.Equal(got.MachineSignPub, want.MachineSignPub) {
		t.Errorf("MachineSignPub = %x, want %x", got.MachineSignPub, want.MachineSignPub)
	}
	if got.EpochID != want.EpochID {
		t.Errorf("EpochID = %d, want %d (trailing epoch trailer disturbed by the new field)",
			got.EpochID, want.EpochID)
	}
}

// TestS19_Pairing_ConveysMachineEndpointID drives a full happy-path pair with the machine
// advertising a known endpoint id, and asserts the DEVICE received that exact value.
//
// The round-trip test above cannot stand in for this one: it exercises the codec directly,
// while nothing about it says the codec is what the handshake carries. A payload field the
// encoder writes and Machine.Pair never populates round-trips perfectly and reaches no phone.
func TestS19_Pairing_ConveysMachineEndpointID(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x3C)
	secret := fill32(0x7F)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	mp.Payload.MachineEndpointID = s19EndpointID
	dp := newDeviceParams(dID, secret, rid)

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil || do == nil {
		t.Fatal("nil outcome on a completed pairing")
	}

	if do.Machine.MachineEndpointID != s19EndpointID {
		t.Errorf("device received MachineEndpointID = %q, want %q. The endpoint id is the one "+
			"coordinate every signed command names (crypto.Command.Canonical refuses an empty "+
			"Machine), so a phone that completes this handshake without it can author nothing",
			do.Machine.MachineEndpointID, s19EndpointID)
	}
}
