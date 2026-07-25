package relay

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"testing"
)

// Transport-security sentinels (PB-NET-2). Both are decided BEFORE any packet is
// sent, so a refusal never costs a connection attempt and never leaks a dial to
// an unverified peer.
var (
	// ErrCleartextRefused rejects a ws:// relay URL. E2EE does not depend on TLS,
	// but cleartext exposes routing metadata, so it is refused outside the
	// loopback carve-out below.
	ErrCleartextRefused = errors.New("relay: cleartext ws:// refused; use wss://")
	// ErrPinMismatch rejects a relay certificate that is not the pinned one.
	ErrPinMismatch = errors.New("relay: server certificate does not match the pin")
	// ErrPinRequired rejects a verified dial on a platform whose trust-root source
	// is TrustRootsPinned: there is no usable root store, so the caller must pin.
	ErrPinRequired = errors.New("relay: platform has no usable trust roots; Security.PinnedCert is required")
)

// TrustRootSource names where a platform's relay TLS verification gets its
// roots. It is STATED per platform rather than left to x509.SystemCertPool,
// because that pool is not the desktop pool on every platform we ship to
// (opus H3), and PB-SEC-5 establishes that Android's networkSecurityConfig does
// not govern crypto/tls inside a native .so -- this is the sole control for the
// relay transport there.
type TrustRootSource string

const (
	// TrustRootsSystem verifies against the platform root store.
	TrustRootsSystem TrustRootSource = "system"
	// TrustRootsEmbedded verifies against a CA bundle compiled into the binary.
	TrustRootsEmbedded TrustRootSource = "embedded"
	// TrustRootsPinned does not verify against any root store: the caller must
	// supply Security.PinnedCert or the dial is refused.
	TrustRootsPinned TrustRootSource = "pinned"
)

// TrustRootSourceFor returns the trust-root source for a GOOS.
//
// Android is TrustRootsPinned. Not because Go ignores the platform store -- it
// does read it: GOOS=android implies the linux build tag, and
// crypto/x509/root_linux.go lists /system/etc/security/cacerts. The problem is
// that the store it reads is the wrong one and an incomplete one:
//
//   - Android 14 moved the system CA store into the Conscrypt APEX
//     (/apex/com.android.conscrypt/cacerts), which is not in Go's search path, so
//     the pool is stale or empty on a modern handset;
//   - user-installed and enterprise CAs are never picked up at all.
//
// Shipping an embedded CA bundle instead means shipping a trust store that rots
// between releases. Refusing to verify against any root store -- and requiring
// the operator's certificate to be pinned -- is the unambiguous answer, so
// EmbeddedTrustRoots is deliberately empty.
func TrustRootSourceFor(goos string) TrustRootSource {
	if goos == "android" {
		return TrustRootsPinned
	}
	return TrustRootsSystem
}

// EmbeddedTrustRoots returns the compiled-in CA bundle, in PEM form. It is empty
// because no platform we ship to declares TrustRootsEmbedded.
func EmbeddedTrustRoots() []byte { return nil }

// Security is the per-connection transport-security policy for DialSecure.
// The zero value is the default policy: TLS verified against the platform's
// stated trust-root source, cleartext refused.
type Security struct {
	// PinnedCert is a DER-encoded certificate that the relay must present. It is
	// an explicit, per-connection opt-in for a self-hosted relay with a
	// self-signed certificate: pinning is a whitelist of exactly one certificate,
	// never a global relaxation of verification.
	PinnedCert []byte
	// AllowLoopbackCleartext admits a ws:// relay whose host is a loopback IP
	// literal. It exists because the relay server is ws://-only today, so an
	// unconditional cleartext ban makes the in-process integration tests
	// unsatisfiable. It is honoured ONLY inside a test binary: in a release build
	// no URL reaches cleartext through this policy, whatever this field says --
	// neither the URL the caller supplies nor any URL a redirect points at.
	AllowLoopbackCleartext bool
}

// resolve decides whether rawURL may be dialed under this policy and, if so,
// with what TLS configuration (nil means "plain ws://" or "platform defaults").
// Every refusal happens here, and it is re-asked for every hop: the first refusal
// happens before a socket is opened, and a redirect is answered with the same
// question rather than followed blindly (see checkRedirect).
func (s Security) resolve(rawURL string) (*tls.Config, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("relay: bad url: %w", err)
	}
	switch u.Scheme {
	case "ws", "http":
		if !s.AllowLoopbackCleartext || !testing.Testing() || !isLoopbackLiteral(u.Hostname()) {
			return nil, ErrCleartextRefused
		}
		return nil, nil
	case "wss", "https":
		return s.tlsConfig()
	default:
		return nil, fmt.Errorf("relay: unsupported url scheme %q", u.Scheme)
	}
}

// tlsConfig builds the verification policy for an encrypted dial.
func (s Security) tlsConfig() (*tls.Config, error) {
	if len(s.PinnedCert) > 0 {
		pinned := append([]byte(nil), s.PinnedCert...)
		// Verification is replaced, not disabled: the presented chain must contain
		// exactly the pinned certificate, so an equally self-signed impostor is
		// refused where a bare InsecureSkipVerify would accept it.
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec // replaced by the pin check below
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				for _, raw := range rawCerts {
					if bytes.Equal(raw, pinned) {
						return nil
					}
				}
				return ErrPinMismatch
			},
		}, nil
	}
	switch TrustRootSourceFor(runtime.GOOS) {
	case TrustRootsEmbedded:
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(EmbeddedTrustRoots()) {
			return nil, errors.New("relay: embedded trust roots are unusable")
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, nil
	case TrustRootsPinned:
		return nil, ErrPinRequired
	default:
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
}

// isLoopbackLiteral reports whether host is a loopback IP literal. A name is
// never accepted: resolution is not part of the carve-out, so "localhost" cannot
// be pointed somewhere else.
func isLoopbackLiteral(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// httpClient returns the websocket dial client for a resolved TLS configuration.
// A client is built even when cfg is nil (the loopback cleartext carve-out): the
// default client would carry no redirect policy, and coder/websocket's own
// CheckRedirect FOLLOWS every hop after rewriting ws->http and wss->https
// (dial.go:90-101), which is how a wss:// dial ends up in cleartext.
func (s Security) httpClient(cfg *tls.Config) *http.Client {
	c := &http.Client{CheckRedirect: s.checkRedirect}
	if cfg != nil {
		c.Transport = &http.Transport{TLSClientConfig: cfg}
	}
	return c
}

// checkRedirect re-runs the policy on the hop a redirect points at, so a relay
// cannot answer a wss:// upgrade with "302 -> ws://" and serve the rest of the
// session in cleartext. The payloads stay sealed either way, but the routing
// metadata a cleartext hop exposes is precisely what PB-NET-2 bans.
//
// The pin needs no equivalent hop check: VerifyPeerCertificate lives on the
// Transport, so it is applied to every TLS hop this client makes.
func (s Security) checkRedirect(req *http.Request, _ []*http.Request) error {
	_, err := s.resolve(req.URL.String())
	return err
}
