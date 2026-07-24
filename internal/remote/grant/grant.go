// Package grant persists and transports the initial sealed EpochGrant that bootstraps
// a freshly paired device's E2EE session (ADR-007 amendment 2026-07-24, decision C5).
//
// enroll.Enroll mints a crypto.EpochGrant sealed to the device's RECIPIENT key and
// signed by the machine's grant-signing key, but BeginPairing used to discard it, so a
// real (non-in-process) phone could never recover the epoch ContentKey. Delivery now
// follows the out-of-band topology: the daemon PERSISTS the sealed grant addressable by
// device id (opaque at rest -- only the phone's recipient private key opens it), and the
// gateway -- the process already holding an authenticated relay client for the device --
// appends it to the device mailbox as a tagged plaintext BOOTSTRAP frame.
//
// The bootstrap frame is DISTINCT from phonecore's ContentKey-sealed router "epoch_grant"
// rotation frame: this frame is recipient-sealed (NOT ContentKey-sealed) because it is
// what DELIVERS the ContentKey -- a chicken-and-egg the router cannot resolve. The phone
// scans its mailbox for this tag, opens the grant with AcceptGrant BEFORE it builds the
// ContentKey-keyed router, and dedups by grant seq (delivery is at-least-once).
package grant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// BootstrapKind tags the plaintext mailbox bootstrap frame so the phone finds it among
// mailbox items WITHOUT a ContentKey. The phone matches on this exact value.
const BootstrapKind = "epoch_grant_bootstrap"

// grantsSubdir is the sidecar directory under the device registry dir; one file per
// device id keeps the frozen device.Record untouched.
const grantsSubdir = "grants"

// Bootstrap is the tagged plaintext frame the gateway appends to the device mailbox. It
// carries the recipient-sealed, machine-signed grant (opaque to the relay); the phone
// opens Grant with its recipient private key to derive the epoch keys.
type Bootstrap struct {
	Kind  string             `json:"kind"`  // always BootstrapKind
	Grant *crypto.EpochGrant `json:"grant"` // recipient-sealed, machine-signed
}

// MarshalBootstrap wraps a sealed grant in the tagged bootstrap frame the gateway
// appends raw (NOT ContentKey-sealed) to the device mailbox.
func MarshalBootstrap(g *crypto.EpochGrant) ([]byte, error) {
	return json.Marshal(Bootstrap{Kind: BootstrapKind, Grant: g})
}

// ParseBootstrap decodes a mailbox item as a bootstrap frame, returning ok=false when
// the item is not a well-formed bootstrap frame (so a phone skips ContentKey-sealed
// items while scanning for its bootstrap).
func ParseBootstrap(env []byte) (*crypto.EpochGrant, bool) {
	var b Bootstrap
	if err := json.Unmarshal(env, &b); err != nil || b.Kind != BootstrapKind || b.Grant == nil {
		return nil, false
	}
	return b.Grant, true
}

// Path is the sidecar file for deviceID: <registryDir>/grants/<deviceID>.json, next to
// the device registry (registryDir is <stateDir>/devices). deviceID is the canonical
// hex SHA-256 of the command-signing key, so it is always a safe filename.
func Path(registryDir, deviceID string) string {
	return filepath.Join(registryDir, grantsSubdir, deviceID+".json")
}

// Save persists the sealed grant for deviceID atomically (temp+Sync+rename, 0600),
// mirroring the device registry's process-crash durability model. The caller treats a
// write error as a failed enrollment (fail-closed: never claim paired-without-grant).
func Save(registryDir, deviceID string, g *crypto.EpochGrant) error {
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	dir := filepath.Join(registryDir, grantsSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "grant.tmp*") // os.CreateTemp creates 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename; cleans up on any error
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, Path(registryDir, deviceID))
}

// Load reads the sealed grant persisted for deviceID. An ABSENT sidecar yields
// (nil, nil) -- a gateway assembled for a pre-grant pairing has nothing to bootstrap and
// must not fail closed; a present-but-corrupt sidecar is a non-nil error (fail-closed,
// like a corrupt registry).
func Load(registryDir, deviceID string) (*crypto.EpochGrant, error) {
	data, err := os.ReadFile(Path(registryDir, deviceID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g crypto.EpochGrant
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}
