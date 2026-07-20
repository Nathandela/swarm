// FAILING-FIRST (TDD RED, GG-5) test for the phone side of the enrollment
// keystone (agents-tracker-qo4): after a device-side pairing pins the machine's
// grant-signing public key, the phone accepts the sealed initial EpochGrant the
// machine delivered and obtains the epoch content key it will decrypt journal
// envelopes and seal commands under -- WITHOUT any manually-provisioned key.
//
// THE CONTRACT this test freezes (undefined symbol -> compile-fail RED):
//   - func AcceptGrant(ks crypto.KeyStore, machineSignPub []byte, g *crypto.EpochGrant)
//     (epochID uint32, grantSeq uint64, keys crypto.EpochKeys, err error)
//   - It verifies the grant against the pinned machine grant-signing pub, opens it
//     with the device KeyStore, and returns the epoch keys; it fails closed on a
//     grant signed by the wrong machine key (no keys leak to an unverified grant).
package phonecore

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func TestAcceptGrant_OpensSealedEpochGrant(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	machinePub, machinePriv, _ := ed25519.GenerateKey(nil)
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	grant, err := crypto.SealEpochGrant(machinePriv, ks.RecipientPublic(), 4, 2, keys)
	if err != nil {
		t.Fatalf("seal grant: %v", err)
	}

	epochID, grantSeq, got, err := AcceptGrant(ks, machinePub, grant)
	if err != nil {
		t.Fatalf("AcceptGrant: %v", err)
	}
	if epochID != 4 || grantSeq != 2 {
		t.Errorf("coords = (%d,%d), want (4,2)", epochID, grantSeq)
	}
	if !bytes.Equal(got.ContentKey[:], keys.ContentKey[:]) {
		t.Error("accepted ContentKey does not match the machine's sealed epoch key")
	}
}

func TestAcceptGrant_FailsClosedOnWrongMachineKey(t *testing.T) {
	ks, _ := crypto.NewFileKeyStore(t.TempDir())
	_, machinePriv, _ := ed25519.GenerateKey(nil)
	wrongPub, _, _ := ed25519.GenerateKey(nil) // a DIFFERENT machine's pin
	keys, _ := crypto.NewEpochKeys()
	grant, _ := crypto.SealEpochGrant(machinePriv, ks.RecipientPublic(), 1, 1, keys)

	if _, _, _, err := AcceptGrant(ks, wrongPub, grant); err == nil {
		t.Error("AcceptGrant with the wrong machine pin = nil error, want fail-closed (grant signature must not verify)")
	}
}
