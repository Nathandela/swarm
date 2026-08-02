// Package relaycfg owns <stateDir>/remote/relay.json: the relay a machine is provisioned
// against, and the optional certificate pin it verifies that relay with.
//
// IT IS ONE PARSER BECAUSE IT USED TO BE THREE. cmd/swarm (the CLI's owner connection),
// cmd/swarm-remote (the gateway sidecar) and internal/skeleton (the daemon's pairing
// rendezvous) each read this file with their own anonymous struct and their own copy of
// the JSON key, and two of them carried comments observing that the writer and the reader
// had to agree. That arrangement is survivable for one field. It is not survivable for a
// SECURITY field: a pin added to two of the three readers produces a machine that
// verifies its relay on two dial paths and does not on the third, and nothing at runtime
// distinguishes it from a machine that verifies on all three (ADR-007 B34).
//
// The absent-file question is deliberately NOT answered here, because the three callers
// answer it differently and each is right: the daemon treats an absent file as "pairing
// unconfigured" and leaves the rendezvous seam nil, the gateway treats it as fatal
// because it has nothing to do without a relay, and the CLI treats it as "this machine
// holds no relay state". Load reports what it found and lets each decide.
package relaycfg

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// Dir is the subdirectory of the state dir that holds a machine's remote provisioning.
const Dir = "remote"

// FileName is the provisioning file `swarm remote init` writes and all three machine
// dial paths read.
const FileName = "relay.json"

// Config is the content of relay.json.
type Config struct {
	// RelayURL is the relay both this machine and the phone dial. It is ONE field
	// serving both, because it is also what `swarm remote pair` puts in the QR verbatim
	// (PB-PAIR-7), so a machine reachable only over loopback cannot pair a handset.
	RelayURL string `json:"relay_url"`
	// SPKIPin is base64 of SHA-256 over the relay certificate's SubjectPublicKeyInfo --
	// exactly what docs/operations/relay-runbook.md section 3 produces:
	//
	//	openssl x509 -in relay.crt -pubkey -noout |
	//	  openssl pkey -pubin -outform der |
	//	  openssl dgst -sha256 -binary | openssl base64
	//
	// It is OPTIONAL: a machine with no pin keeps the behaviour it has today, which is
	// what local development against a loopback relay depends on. Once set it is
	// MANDATORY IN EFFECT -- see Security.
	SPKIPin string `json:"relay_spki_pin,omitempty"`
}

// path is the file's location under a state directory.
func path(stateDir string) string { return filepath.Join(stateDir, Dir, FileName) }

// Load reads relay.json. found reports whether the file exists at all; an absent file is
// not an error, because two of the three callers treat it as an ordinary unprovisioned
// state. A file that exists but cannot be read or parsed IS an error, so a corrupt
// provisioning fails closed rather than silently reverting to unconfigured.
func Load(stateDir string) (cfg Config, found bool, err error) {
	b, err := os.ReadFile(path(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read %s: %w", FileName, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, true, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return cfg, true, nil
}

// Save writes relay.json at 0600, creating <stateDir>/remote if needed.
func Save(stateDir string, cfg Config) error {
	dir := filepath.Join(stateDir, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path(stateDir), b, 0o600)
}

// Security is the transport policy this machine's dials run under: relay.MachineSecurity
// -- verified TLS, cleartext refused except to a loopback IP literal, the decision
// re-asked on every redirect hop -- plus the configured pin.
//
// A MALFORMED PIN IS REFUSED HERE, before any dial and before any caller can decide to
// carry on without one, and it is refused as relay.ErrPinMalformed for B33's reason: a
// truncated pin that only surfaced as a handshake failure would read as "the relay is
// down", and one silently zero-padded to length would weaken the check into something
// that still looks like a pin.
//
// A CONFIGURED PIN ALSO WITHDRAWS THE LOOPBACK CLEARTEXT CARVE-OUT, which relay.Security
// enforces. An operator who configured a pin has stated they want a verified peer, and
// cleartext cannot present one; admitting the dial anyway would leave a configured
// security control doing nothing, which is the exact defect class B34 records.
func (c Config) Security() (relay.Security, error) {
	pin, err := c.Pin()
	if err != nil {
		return relay.Security{}, err
	}
	sec := relay.MachineSecurity()
	sec.PinnedSPKISHA256 = pin
	return sec, nil
}

// Pin decodes the configured SPKI pin, or returns nil when none is configured.
//
// IT IS THE ONLY DECODER OF THIS FIELD, and that is the invariant rather than a preference.
// internal/skeleton grew a second base64 decode of Config.SPKIPin -- with no length check --
// while every existing fence stayed green, and the pin it would have forwarded to a handset is
// one no dial on this machine would have accepted. Two decoders are two opinions about what
// "malformed" means, and the disagreement surfaces as a pin that is carried and never
// consulted (ADR-007 B34). internal/remote/transport's TestPBOPS5_OnlyRelayCfgDecodesThePin
// keeps it that way.
//
// ABSENT is the empty string, which is what omitempty writes and what a Config literal
// carries. Anything else is a pin the operator SET, so a value that is present but unusable --
// whitespace, a truncated digest, hex instead of base64 -- fails closed rather than silently
// reverting to an unpinned machine.
func (c Config) Pin() ([]byte, error) {
	if c.SPKIPin == "" {
		return nil, nil
	}
	pin, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.SPKIPin))
	if err != nil || len(pin) != sha256.Size {
		return nil, fmt.Errorf("%s: relay_spki_pin is not base64 of a 32-byte "+
			"SHA-256 digest (see the relay runbook, section 3): %w", FileName, relay.ErrPinMalformed)
	}
	return pin, nil
}
