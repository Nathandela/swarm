package skeleton

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// remoteIdentityFile is the machine identity `swarm remote init` persists (see
// cmd/swarm/remote.go) and loadPairingConfig reads back: <stateDir>/remote/machine.key.
// The CLI and the daemon assembly must agree on this path.
const remoteIdentityFile = "machine.key"

// remoteRelayFile is the relay config `swarm remote init` persists and loadPairingConfig
// reads back: <stateDir>/remote/relay.json, {"relay_url":"...","relay_spki_pin":"..."}.
// The CLI writer and this reader must agree on this filename + shape.
const remoteRelayFile = "relay.json"

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
	relayURL, pin, err := loadRelayConfig(stateDir)
	if err != nil {
		return nil, err
	}
	// The relay SPKI pin travels to the phone in msg2 and NOWHERE ELSE (ADR-007 B33/B34):
	// the pairing QR has no room for it -- MaxRelayURLLen = 39 already leaves one byte of
	// slack in the v6-L symbol -- and a handset whose platform trust-root source is
	// TrustRootsPinned refuses every dial without one. It is OPTIONAL here: a machine with
	// no pin configured still pairs, and the pin's absence is decided at the phone's DIAL
	// site, not here (see mobile).
	cfg.RelaySPKIPin = pin
	if relayURL != "" {
		// PB-PAIR-7: the URL survives onto the config as well as into the rendezvous
		// closure, so BeginPairing can put it in the QR verbatim. It used to be read only
		// to build the closure and then discarded, leaving the scanning phone with no
		// endpoint to dial.
		cfg.RelayURL = relayURL
		cfg.NewRendezvous = relayRendezvousFactory(relayURL)
	}

	return cfg, nil
}

// loadRelayConfig reads <stateDir>/remote/relay.json
// ({"relay_url":"...","relay_spki_pin":"<base64 sha256 of the relay SPKI>"}). It returns
// "" (no relay configured) when the file is ABSENT — the fail-closed default that
// leaves NewRendezvous nil — and a non-nil error when the file is present but
// unreadable, unparseable, or carries an empty relay_url.
//
// The pin is OPTIONAL and is not validated for length here. It is an opaque value this
// daemon only forwards: the phone is the party that must decide what an absent or wrong
// pin means, at its dial site, and a machine-side length check would only turn a
// misconfiguration into a daemon that will not start.
func loadRelayConfig(stateDir string) (url string, spkiPin []byte, err error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "remote", remoteRelayFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil
		}
		return "", nil, err
	}
	var rc struct {
		RelayURL     string `json:"relay_url"`
		RelaySPKIPin []byte `json:"relay_spki_pin"`
	}
	if err := json.Unmarshal(b, &rc); err != nil {
		return "", nil, fmt.Errorf("parse relay.json: %w", err)
	}
	if rc.RelayURL == "" {
		return "", nil, fmt.Errorf("relay.json present but relay_url is empty")
	}
	return rc.RelayURL, rc.RelaySPKIPin, nil
}
