package skeleton

import (
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// remoteIdentityFile is the machine identity `swarm remote init` persists (see
// cmd/swarm/remote.go) and loadPairingConfig reads back: <stateDir>/remote/machine.key.
// The CLI and the daemon assembly must agree on this path.
const remoteIdentityFile = "machine.key"

// loadPairingConfig reads the machine's pairing identity and maps it onto a
// *pairingConfig for the daemon assembly (serve.go). TRI-STATE, fail-closed on
// corruption but not on absence:
//   - identity file MISSING     -> (nil, nil): pairing is simply unprovisioned
//     (`swarm remote init` has not run yet), and BeginPairing already fails
//     closed on a nil pairingConfig.
//   - identity file present, OK -> (*pairingConfig, nil).
//   - identity file CORRUPT     -> (nil, non-nil error): assembly must abort
//     rather than start with pairing silently broken (machine key custody).
//
// NewRendezvous is left nil here; the real relay adapter is wired in a later slice.
func loadPairingConfig(stateDir string) (*pairingConfig, error) {
	path := filepath.Join(stateDir, "remote", remoteIdentityFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	id, err := machineid.Load(path)
	if err != nil {
		return nil, err
	}

	return &pairingConfig{
		Static:       id.NoiseStatic(),
		RecipientPub: id.RecipientPublic(),
		SignPub:      id.GrantSignPublic(),
		SignPriv:     id.GrantSignPrivate(),
		EpochID:      id.EpochID(),
		GrantSeq:     id.GrantSeq(),
		EpochKeys:    id.EpochKeys(),
		Hostname:     id.Hostname(),
		RoutingID:    id.RoutingID(),
		RelayAuthPub: id.RelayAuthPublic(),
	}, nil
}
