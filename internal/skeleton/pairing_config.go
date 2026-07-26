package skeleton

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
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
// NewRendezvous is wired ONLY when a relay URL is configured (relay.json present); its
// absence leaves NewRendezvous nil, so pairing-without-a-relay stays cleanly
// unsupported and BeginPairing fails closed on the nil seam (never panics).
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

	cfg := &pairingConfig{
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
		// The phone's name for this machine (S19), from the SAME function serve.go derives
		// the daemon's served endpoint id with, over the SAME state directory. Deriving it
		// here rather than accepting it from the assembly is what keeps the two from
		// drifting: a phone that signs over a different id has every command refused, and
		// the refusal names a signature failure rather than a mismatched address.
		EndpointID: endpointID(stateDir),
	}

	// A configured relay URL wires the live rendezvous seam; its absence is fail-closed
	// (nil NewRendezvous). Present-but-malformed is fail-closed as an error, consistent
	// with corrupt-identity handling: assembly aborts rather than starting with pairing
	// silently half-configured.
	relayCfg, err := loadRelayConfig(stateDir)
	if err != nil {
		return nil, err
	}
	// The relay SPKI pin travels to the phone in msg2 and NOWHERE ELSE (ADR-007 B33/B34):
	// the pairing QR has no room for it. OPTIONAL here -- a machine with no pin configured
	// still pairs, and what an absent pin MEANS is decided at the phone's dial site.
	if relayCfg.SPKIPin != "" {
		// Decoded HERE rather than forwarded as text: relaycfg.Security() already rejects a
		// malformed pin at assembly, so a phone can only ever receive one this machine itself
		// would dial under. Forwarding the base64 would let the two ends disagree about what
		// "malformed" means, which is how a pin ends up carried and never consulted.
		pin, perr := base64.StdEncoding.DecodeString(strings.TrimSpace(relayCfg.SPKIPin))
		if perr != nil {
			return nil, fmt.Errorf("relay.json: relay_spki_pin is not base64: %w", perr)
		}
		cfg.RelaySPKIPin = pin
	}
	if relayCfg.RelayURL != "" {
		// The transport policy is resolved HERE rather than inside the closure, so a
		// malformed pin aborts assembly like a corrupt identity does. Resolved at dial
		// time instead, it would surface as a pairing that fails when the owner runs it
		// and reports a relay problem (ADR-007 B34, and B33's reasoning about a pin whose
		// only symptom is a handshake failure).
		sec, serr := relayCfg.Security()
		if serr != nil {
			return nil, serr
		}
		// PB-PAIR-7: the URL survives onto the config as well as into the rendezvous
		// closure, so BeginPairing can put it in the QR verbatim. It used to be read only
		// to build the closure and then discarded, leaving the scanning phone with no
		// endpoint to dial.
		cfg.RelayURL = relayCfg.RelayURL
		cfg.NewRendezvous = relayRendezvousFactory(relayCfg.RelayURL, sec)
	}

	return cfg, nil
}

// loadRelayConfig reads the machine's relay provisioning through the one parser that owns
// the file (relaycfg). It returns the ZERO config — no relay configured, the fail-closed
// default that leaves NewRendezvous nil — when the file is ABSENT, and a non-nil error
// when it is present but unreadable, unparseable, or carries an empty relay_url.
func loadRelayConfig(stateDir string) (relaycfg.Config, error) {
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil {
		return relaycfg.Config{}, err
	}
	if !found {
		return relaycfg.Config{}, nil
	}
	if cfg.RelayURL == "" {
		return relaycfg.Config{}, fmt.Errorf("relay.json present but relay_url is empty")
	}
	return cfg, nil
}
