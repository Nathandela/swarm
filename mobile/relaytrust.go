package swarmmobile

import (
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// RelayTrust is ADR-016 W2's reverse-bound trust delegate, in exactly the shape
// mobile/keycustody.go's KeyCustody established: Go calls it, Kotlin implements it.
//
//	RelayTrust interface {
//	    VerifyRelayChain(host string, pemChain []byte) error
//	}
//
// THE DIRECTION IS THE SAME AS KeyCustody'S; THE PARAMETER RULE IS THE OPPOSITE, and W2
// states why: "a server certificate chain is public by construction". KeyCustody's B8
// rule -- "single crossing, inbound only" -- bans KEY MATERIAL crossing outbound
// (keycustody.go), and nothing here is key material: it is the certificate chain a relay
// presented on an open TLS handshake, which a network observer already saw in the clear.
// So unlike KeyCustody, this interface's whole point is a []byte PARAMETER carrying that
// chain OUTBOUND to Kotlin's X509TrustManagerExtensions -- the platform's own verifier,
// which reads the Conscrypt APEX store Go cannot see (security.go's own reasoning for
// TrustRootsPinned).
//
// The chain travels as PEM, leaf first, because gomobile cannot bind [][]byte and
// CertificateFactory.generateCertificates consumes PEM directly.
//
// Go still checks the name and validity window itself, independent of this verifier's
// answer (relay.PlatformVerifier's own doc): a verifier that approves everything still
// cannot admit a certificate whose SAN does not cover the configured host. This interface
// is asked about chain trust only.
type RelayTrust interface {
	VerifyRelayChain(host string, pemChain []byte) error
}

// The two RelayTrust verdict tokens, in KeyCustody's own convention (keycustody.go:66-94):
// strings that survive gomobile's error flattening, stamped by the Kotlin implementation
// and read back here so a refusal reaches the user as the right one of W8's distinct
// states rather than an opaque bug report.
const (
	// RelayTrustUntrusted marks a REAL security verdict: the platform verifier rejected
	// the presented chain (an untrusted issuer). Maps to W8's relay_untrusted.
	RelayTrustUntrusted = "swarm-relaytrust/untrusted"
	// RelayTrustUnavailable marks a CONFIGURATION fault, never a security accusation: no
	// platform verifier answered at all. Maps to W8's relay_trust_unavailable.
	RelayTrustUnavailable = "swarm-relaytrust/unavailable"
)

// withPlatformTrust installs a.relayTrust as sec's platform delegate (relay.
// WithPlatformVerifier), if SetRelayTrust ever installed one. It is the ONE place both the
// pairing dial (mobile/pairing.go) and the session dial (handsetSecurity) reach for this,
// so Android's RelayTrust wiring is symmetric across the two rather than reimplemented per
// caller. A nil a.relayTrust is a no-op: sec comes back exactly as given, and every dial
// keeps resolving relay.TrustRootSourceFor(runtime.GOOS) exactly as before this ADR --
// which is every platform this is never called on.
func (a *App) withPlatformTrust(sec relay.Security) relay.Security {
	a.mu.Lock()
	rt := a.relayTrust
	a.mu.Unlock()
	if rt == nil {
		return sec
	}
	return relay.WithPlatformVerifier(sec, rt)
}

// relayTrustUnavailable reports whether err is ADR-016 W8's "no platform verifier
// answered" fault -- an APP fault, never a security accusation -- reached two ways that are
// the SAME fault under the Conformance table's own naming ("No platform verifier |
// ErrPinRequired | relay_trust_unavailable | distinct copy from a security verdict"):
//
//   - Kotlin's RelayTrust WAS installed and consulted, but itself could not reach the
//     platform verifier: RelayTrustUnavailable is stamped into the returned error's
//     message, which survives gomobile's flattening (the same token convention KeyCustody
//     uses), and reaches here wrapped inside whatever the TLS handshake failure became.
//   - PhoneRuntime never installed a RelayTrust at all -- installRelayTrust swallows every
//     exception it can raise by design, so SetRelayTrust is simply never called and
//     a.relayTrust stays nil. A webpki handset then resolves through TrustRootsPinned with
//     no pin (handsetSecurity/effectiveStatePin withhold the pin under webpki by
//     construction), and the dial fails PRE-HANDSHAKE with relay.ErrPinRequired -- with no
//     token to read, because nothing ran that could have stamped one.
//
// THE POLICY CHECK ON THE SECOND BRANCH IS WHAT KEEPS THIS FROM OVER-FIRING: a genuinely
// pinned_spki phone that legitimately holds no pin also reaches relay.ErrPinRequired
// (handsetSecurity's own documented residual), and that IS "not the relay your machine
// published" -- a real security-adjacent state, not an app fault -- so it must keep
// classifying as connRelayUntrusted, which is why RelayTLSPolicy is consulted rather than
// treating every bare ErrPinRequired as this fault.
func (a *App) relayTrustUnavailable(err error) bool {
	if err == nil {
		return false
	}
	// The re-verification round's LOW finding: RelayTrust.kt stamps its verdict as a
	// PREFIX but wraps peer-controlled text alongside it -- a path-validation failure's
	// message carries the presented certificate's own DN, and the SANs are interpolated
	// verbatim. A hostile relay can therefore embed RelayTrustUnavailable's literal string
	// inside its own certificate on a genuinely UNTRUSTED chain. The untrusted token is
	// checked first and wins when both appear, so that substitution cannot turn a real
	// security verdict into this app-fault classification.
	msg := err.Error()
	if strings.Contains(msg, RelayTrustUntrusted) {
		return false
	}
	if strings.Contains(msg, RelayTrustUnavailable) {
		return true
	}
	if !errors.Is(err, relay.ErrPinRequired) {
		return false
	}
	a.mu.Lock()
	noDelegate := a.relayTrust == nil
	a.mu.Unlock()
	return noDelegate && a.core.State().RelayTLSPolicy == "webpki"
}
