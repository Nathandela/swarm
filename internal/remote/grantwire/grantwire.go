// Package grantwire is the WIRE half of the epoch-grant bootstrap: the tagged plaintext
// frame the gateway appends to a device mailbox, and its parser.
//
// It is split out of internal/remote/grant because the two halves run on DIFFERENT
// machines. The sidecar half (Save/Load/Delete/Path) is the daemon's registry file I/O and
// belongs only to the machine; this half is what the PHONE must be able to read, and
// phonecore's bound dependency closure (PB-BIND-0) is an allowlist of code shipped to a
// handset an adversary may hold. Keeping them in one package would have put the machine's
// registry-sidecar file I/O on that handset for no reason. internal/remote/grant re-exports
// what is here, so every machine-side caller keeps ONE import.
//
// The bootstrap frame is DISTINCT from phonecore's ContentKey-sealed router "epoch_grant"
// rotation frame: this frame is recipient-sealed (NOT ContentKey-sealed) because it is what
// DELIVERS the ContentKey -- a chicken-and-egg the router cannot resolve. The phone finds it
// on the ordinary inbound path by this tag, opens it with its recipient private key, and
// dedups by (epoch_id, grant_seq) through a crypto.GrantReceiver seeded from durable state
// (delivery is at-least-once).
package grantwire

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// BootstrapKind tags the plaintext mailbox bootstrap frame so the phone finds it among
// mailbox items WITHOUT a ContentKey. The phone matches on this exact value.
const BootstrapKind = "epoch_grant_bootstrap"

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
