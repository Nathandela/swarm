// FAILING-FIRST (TDD RED, GG-5) tests for the enrollment keystone
// (agents-tracker-qo4): turning an affirmatively-confirmed pairing outcome into
// the two durable artifacts the runnable stack needs -- a device.Registry record
// the daemon authorizes commands against (R-POL.9 / R-DEV.1), and a sealed,
// signed EpochGrant that delivers the epoch content key to the phone (F3/A15).
//
// THE CONTRACT these tests freeze (undefined package/symbols -> compile-fail RED):
//   - package enroll, func Enroll(out *pairing.MachineOutcome, cap device.Capability,
//     machineGrantPriv ed25519.PrivateKey, epochID uint32, grantSeq uint64,
//     keys crypto.EpochKeys, now time.Time) (Result, error)
//   - Result{ Record device.Record; Grant *crypto.EpochGrant }
//   - The Record is admissible: its DeviceID is DeviceIDFor(the device command-sign
//     pub), its pinned keys/capability/epoch come from the outcome, and registry.Add
//     accepts it (validateRecord passes).
//   - The Grant opens on the device side (crypto.OpenEpochGrant with the device
//     KeyStore + the machine grant-signing pub) to exactly the keys Enroll sealed.
//   - Enroll fails closed on a malformed outcome (missing command-sign or recipient
//     key), never producing a half-built record or an unsealable grant.
package enroll

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// deviceKeys builds a device KeyStore and the pairing.MachineOutcome a machine
// would hold after pairing that device (the outcome pins the device's public
// identity keys the machine learned over the authenticated Noise channel).
func deviceOutcome(t *testing.T) (crypto.KeyStore, *pairing.MachineOutcome) {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	return ks, &pairing.MachineOutcome{
		DeviceStatic: ks.NoiseStaticPublic(),
		Device: pairing.DevicePayload{
			DeviceName:           "Test iPhone",
			DeviceRoutingID:      []byte("device-routing-id-0001"),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
	}
}

func TestEnroll_ProducesAdmissibleRecordAndOpenableGrant(t *testing.T) {
	ks, out := deviceOutcome(t)
	machinePub, machinePriv, _ := ed25519.GenerateKey(nil)
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)

	res, err := Enroll(out, device.CapFull, machinePriv, 7, 1, keys, now)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// The record must be admissible by the very registry the daemon authorizes
	// against, and must authorize the tier we granted.
	reg, err := device.Open(t.TempDir())
	if err != nil {
		t.Fatalf("registry open: %v", err)
	}
	if err := reg.Add(res.Record); err != nil {
		t.Fatalf("registry rejected the enrolled record: %v", err)
	}
	wantID := device.DeviceIDFor(ks.CommandSigningPublic())
	if res.Record.DeviceID != wantID {
		t.Errorf("Record.DeviceID = %q, want %q (id must derive from the command-signing key)", res.Record.DeviceID, wantID)
	}
	if res.Record.GrantedEpoch != 7 {
		t.Errorf("Record.GrantedEpoch = %d, want 7", res.Record.GrantedEpoch)
	}
	if !res.Record.PairedAt.Equal(now) {
		t.Errorf("Record.PairedAt = %v, want %v", res.Record.PairedAt, now)
	}
	if !reg.Authorized(wantID, device.ActionControl) {
		t.Error("enrolled CapFull device is not authorized to control")
	}

	// The grant must open on the device side to exactly the keys we sealed.
	gotEpoch, gotSeq, gotKeys, err := crypto.OpenEpochGrant(ks, machinePub, res.Grant)
	if err != nil {
		t.Fatalf("device could not open the enrolled grant: %v", err)
	}
	if gotEpoch != 7 || gotSeq != 1 {
		t.Errorf("grant coords = (%d,%d), want (7,1)", gotEpoch, gotSeq)
	}
	if !bytes.Equal(gotKeys.ContentKey[:], keys.ContentKey[:]) {
		t.Error("opened ContentKey does not match the sealed epoch content key")
	}
	if !bytes.Equal(gotKeys.WakeKey[:], keys.WakeKey[:]) {
		t.Error("opened WakeKey does not match the sealed epoch wake key")
	}
}

func TestEnroll_FailsClosedOnMalformedOutcome(t *testing.T) {
	_, machinePriv, _ := ed25519.GenerateKey(nil)
	keys, _ := crypto.NewEpochKeys()
	now := time.Unix(1_700_000_000, 0)

	if _, err := Enroll(nil, device.CapFull, machinePriv, 1, 1, keys, now); err == nil {
		t.Error("Enroll(nil outcome) = nil error, want fail-closed")
	}

	// A device outcome missing its command-signing key can never yield a verifiable
	// command signature -> must be refused, not admitted with an unusable id.
	_, out := deviceOutcome(t)
	out.Device.DeviceCommandSignPub = nil
	if _, err := Enroll(out, device.CapFull, machinePriv, 1, 1, keys, now); err == nil {
		t.Error("Enroll(outcome without command-sign key) = nil error, want fail-closed")
	}

	// A device outcome missing its recipient key leaves the grant with no seal
	// target -> must be refused.
	_, out2 := deviceOutcome(t)
	out2.Device.RecipientPub = nil
	if _, err := Enroll(out2, device.CapFull, machinePriv, 1, 1, keys, now); err == nil {
		t.Error("Enroll(outcome without recipient key) = nil error, want fail-closed")
	}
}
