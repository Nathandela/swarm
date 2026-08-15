package swarmmobile

import "github.com/Nathandela/swarm/internal/remote/relay"

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
