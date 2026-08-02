package phonecore

import (
	"crypto/ed25519"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// AcceptGrant is the phone side of the enrollment keystone: after a device-side
// pairing pins the machine's Ed25519 grant-signing public key
// (pairing.DeviceOutcome.Machine.MachineSignPub), the phone accepts the sealed
// initial EpochGrant the machine delivered over the relay and obtains the epoch's
// wake/content keys.
//
// It verifies the grant against the pinned machine grant-signing pub and opens it
// with the device KeyStore (crypto.OpenEpochGrant), so a relay that cannot forge
// the machine's signature cannot inject a key. It fails closed on a grant signed
// by any key other than the pinned one. This first accept does not enforce
// (epoch, seq) monotonicity; the phone threads subsequent grants through a
// crypto.GrantReceiver seeded from this result for replay safety (F3).
func AcceptGrant(ks crypto.KeyStore, machineSignPub []byte, g *crypto.EpochGrant) (epochID uint32, grantSeq uint64, keys crypto.EpochKeys, err error) {
	return crypto.OpenEpochGrant(ks, ed25519.PublicKey(machineSignPub), g)
}
