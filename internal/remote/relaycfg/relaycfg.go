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
	"net/url"
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

// The two relay TLS policies ADR-016 W1 names on the wire. A policy is its own value in
// every durable and wire artifact that carries the pin -- never derived from the pin's
// presence or absence.
const (
	// PolicyWebPKI verifies the relay's certificate chain against the platform's trust
	// roots and matches the hostname. It is the default (Omitted, it is webpki).
	PolicyWebPKI = "webpki"
	// PolicyPinnedSPKI replaces chain and name verification with the SPKI pin set: the
	// expert, opt-in policy.
	PolicyPinnedSPKI = "pinned_spki"
)

// Config is the content of relay.json.
type Config struct {
	// RelayURL is the relay both this machine and the phone dial. It is ONE field
	// serving both, because it is also what `swarm remote pair` puts in the QR verbatim
	// (PB-PAIR-7), so a machine reachable only over loopback cannot pair a handset.
	RelayURL string `json:"relay_url"`
	// PushGatewayURL is the public bare HTTPS origin both peers are provisioned to use
	// for negotiated push bindings. It carries no capability or address; those authorities
	// exist only in the authenticated pairing transcript and sole device registry row.
	PushGatewayURL string `json:"push_gateway_url,omitempty"`
	// TLSPolicy is ADR-016 W1's named relay TLS policy (PolicyWebPKI or PolicyPinnedSPKI),
	// authored by `swarm remote init --relay-tls-policy` and published to the phone
	// verbatim (pairing.MachinePayload.RelayTLSPolicy, RemoteProfileV1.RelayTLSPolicy).
	//
	// IT IS INDEPENDENT OF SPKIPin AND NEVER DERIVED FROM IT: a pin's presence never
	// implies pinned_spki and a pin's absence never implies webpki -- W9's migration
	// ladder needs a webpki machine that ALSO publishes a compatibility pin, which is
	// inexpressible under a derivation rule. Empty is the legacy shape: a relay.json
	// written before this field existed, decoded here as "no policy stated" rather than
	// silently promoted to pinned_spki because a pin happens to be present.
	//
	// IT ALSO SCOPES THE MACHINE'S OWN DIAL, per W3's single rule stated once and applying
	// everywhere Security.PinnedSPKISHA256 gets populated from a (policy, pin) pair, not
	// only on the phone: "a pin is consulted if and only if the effective relay TLS policy
	// is pinned_spki... not by anything." See Security() below. W2's "Desktop is unchanged"
	// is a narrower claim than that -- it is about the machine's TRUST-ROOT SOURCE
	// (TrustRootsSystem, never the platform delegate), not about whether a compatibility
	// pin is consulted, and does not license skipping this scoping machine-side.
	TLSPolicy string `json:"relay_tls_policy,omitempty"`
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
	if err := ValidatePushGatewayURL(cfg.PushGatewayURL); err != nil {
		return Config{}, true, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return cfg, true, nil
}

// Save writes relay.json at 0600, creating <stateDir>/remote if needed.
func Save(stateDir string, cfg Config) error {
	if err := ValidatePushGatewayURL(cfg.PushGatewayURL); err != nil {
		return err
	}
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

// ValidatePushGatewayURL accepts an absent endpoint (foreground/legacy mode) or one bare
// HTTPS origin. Paths, credentials, query/fragment and whitespace are refused before the
// endpoint can be advertised in a pairing QR.
func ValidatePushGatewayURL(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("push_gateway_url has surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("push_gateway_url must be an https bare origin")
	}
	return nil
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
//
// ADR-016 W3's SINGLE RULE APPLIES HERE TOO: a pin is consulted if and only if the
// effective policy is pinned_spki, "not by anything" -- and this machine's own dial is one
// of the things. TLSPolicy == "" is the legacy shape (a relay.json predating this field, or
// one an operator configured before ever typing --relay-tls-policy) and is read as
// pinned_spki here, the same inference cmd/swarm's own CLI applies to a bare --relay-pin:
// it is what a machine that pinned before this ADR shipped is already doing, and reading an
// absent field as an authenticated webpki claim would silently un-pin it. Only an EXPLICIT
// webpki policy withdraws consultation -- exactly the shape W9's compatibility window needs:
// a machine on `webpki --relay-pin-compat <spki>` verifies its OWN dial against the
// platform's trust roots, and the compatibility pin is carried for un-migrated PHONES only,
// never consulted by the machine that published it.
func (c Config) Security() (relay.Security, error) {
	pin, err := c.Pin()
	if err != nil {
		return relay.Security{}, err
	}
	sec := relay.MachineSecurity()
	if c.TLSPolicy != PolicyWebPKI {
		sec.PinnedSPKISHA256 = pin
	}
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

// HasPin reports whether a pin is configured at all, without touching its base64 form --
// the predicate callers outside this package are allowed (TestPBOPS5 fences direct
// SPKIPin reads to this package so there is exactly one decoder and one presence rule).
func (c Config) HasPin() bool { return c.SPKIPin != "" }

// PinBase64 re-encodes the VALIDATED pin for carry-forward into a fresh Config (W9's
// compatibility pin surviving a flagless re-run). Returning the decoded pin's canonical
// encoding, rather than the raw field, means a malformed stored value fails here instead
// of being copied forward verbatim.
func (c Config) PinBase64() (string, error) {
	pin, err := c.Pin()
	if err != nil {
		return "", err
	}
	if pin == nil {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString(pin), nil
}
