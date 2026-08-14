package relay

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sync"
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
	// It names PinnedSPKISHA256 FIRST because this error is the only instruction
	// most operators will ever read about how to configure a pin, and the other
	// form stops working at the relay's next certificate renewal (PB-OPS-5).
	ErrPinRequired = errors.New("relay: platform has no usable trust roots; Security.PinnedSPKISHA256 (renewal-safe) or Security.PinnedCert is required")
	// ErrPinMalformed rejects a pin that is not a SHA-256 digest. It is decided
	// before the dial rather than inside VerifyPeerCertificate: a truncated pin
	// that only ever surfaced as a handshake failure would read as "the relay is
	// down", and one silently zero-padded to length would weaken the check.
	ErrPinMalformed = errors.New("relay: Security.PinnedSPKISHA256 must be a 32-byte SHA-256 digest")
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
	// supply Security.PinnedSPKISHA256 or Security.PinnedCert, or the dial is
	// refused.
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
	//
	// IT DOES NOT SURVIVE A CERTIFICATE RENEWAL, and on Android that is fatal
	// rather than inconvenient: TrustRootSourceFor makes the handset pinning-only,
	// so there is no root-store path to fall back to. A reissue changes the leaf
	// DER even when the key is unchanged, so a relay renewing on the Let's Encrypt
	// cadence takes the handset offline every 60-90 days. Prefer
	// PinnedSPKISHA256 unless the certificate is long-lived and self-signed.
	PinnedCert []byte
	// PinnedSPKISHA256 is SHA-256 over the presented certificate's
	// SubjectPublicKeyInfo -- the renewal-safe pin (PB-OPS-5), and the value
	//
	//	openssl x509 -in relay.crt -pubkey -noout |
	//	  openssl pkey -pubin -outform der | openssl dgst -sha256 -binary
	//
	// produces. A reissue that REUSES THE KEY presents the same SPKI, so the pin
	// keeps matching across renewals at the same security level: the digest still
	// admits exactly one public key, and an impostor holding a different key is
	// refused exactly as it is under PinnedCert.
	//
	// KEY REUSE IS PART OF THE PIN, not an implementation detail of the operator's
	// ACME client. certbot rotates the keypair on every renewal unless it is told
	// not to (--reuse-key, or reuse_key = True in the renewal config), and a
	// rotated key is a new SPKI, which breaks this pin on exactly the cadence it
	// was adopted to survive. docs/operations/relay-runbook.md carries the step;
	// TestPBOPS5_SPKIPinIsBrokenByARenewalThatROTATESTheKey carries the proof.
	//
	// Either pin alone satisfies a TrustRootsPinned platform; both may be set, in
	// which case either matching admits the peer.
	PinnedSPKISHA256 []byte
	// AllowLoopbackCleartext admits a ws:// relay whose host is a loopback IP
	// literal. It exists because the relay server is ws://-only today, so an
	// unconditional cleartext ban makes the in-process integration tests
	// unsatisfiable. It is honoured ONLY inside a test binary: in a release build
	// no URL reaches cleartext through this policy, whatever this field says --
	// neither the URL the caller supplies nor any URL a redirect points at.
	AllowLoopbackCleartext bool
	// loopbackInRelease is the same carve-out WITHOUT the test-binary condition, and
	// it is unexported because that is what keeps it honest: only MachineSecurity
	// sets it, so no caller anywhere can turn cleartext on for a URL of its choosing,
	// and AllowLoopbackCleartext keeps its stronger property intact.
	//
	// See MachineSecurity for why a machine-side process needs it.
	loopbackInRelease bool
	// unverifiedTLS accepts ANY certificate on an encrypted dial. It is unexported and
	// set by PairingSecurity alone, which is the whole of its scope: exactly one dial in
	// the product may use it, and a caller cannot widen that by assembling a Security of
	// its own. A configured pin still wins over it (tlsConfig), so it can only ever be a
	// relaxation of the DEFAULT, never of an explicit one.
	unverifiedTLS bool
	// observer records the SPKI the peer presented on an unverified dial (ADR-007 B48).
	// It is unexported and populated only by DialRawSecure, so a Security value cannot be
	// assembled to spy on a dial it does not own, and a nil observer records nothing.
	observer *spkiObserver
	// trustRoots overrides the platform's stated trust-root source, and is honoured ONLY
	// inside a test binary -- see WithTrustRootSource, which is the only thing that sets
	// it. It exists because the branch it reaches was unreachable in every test that
	// could run: tlsConfig switches on runtime.GOOS, the suite runs on a desktop, and the
	// pinning-only refusal that residual 1.9's whole resolution rests on had therefore
	// never been executed by anything.
	trustRoots TrustRootSource
}

// PairingSecurity is the policy the HANDSET's pairing rendezvous dials under, and the only
// place in the product that accepts an unverified certificate (ADR-007 B45).
//
// WHY IT HAS TO EXIST. The relay pin reaches a phone through pairing.MachinePayload, so the
// pairing dial is the dial that FETCHES the pin and cannot itself be pinned. On a
// pinning-only platform -- every Android handset -- an unpinned wss:// dial is not merely
// unverified, it is REFUSED with ErrPinRequired before a packet. Under the default policy the
// pairing dial is therefore refused, the pin never arrives, and the phone can never pair over
// wss:// at all. That deadlock is B45.
//
// WHY IT IS SOUND. The relay's certificate never protected this exchange in the first place.
// The payload is a Noise handshake whose peer the operator authenticates by comparing a
// six-symbol SAS against the machine's own screen, and nothing is pinned until that comparison
// is confirmed. TLS on this hop buys metadata confidentiality, not content authenticity.
//
// WHAT IT COSTS, named rather than buried: a hostile terminator on this one hop learns which
// routing ids are pairing and when. It learns nothing it could use -- the handshake is sealed
// against it and a substituted payload fails the SAS -- but that metadata is precisely what
// PB-NET-2's cleartext ban protects, and this policy gives it up for one dial.
//
// WHAT IT DOES NOT RELAX. Cleartext is refused exactly as everywhere else, decided from the
// URL before any socket: a ws:// pairing URL is still refused outside the loopback carve-out.
// And a Security carrying a pin ignores this flag entirely, so it can never downgrade a dial
// that had something better to check.
func PairingSecurity() Security {
	return Security{AllowLoopbackCleartext: true, unverifiedTLS: true}
}

// WithTrustRootSource returns sec with its trust-root source overridden.
//
// IT IS HONOURED ONLY INSIDE A TEST BINARY, and the field it sets is unexported, so a release
// build cannot select a trust-root source it is not on however this is called. That is the
// same shape as AllowLoopbackCleartext and for the same reason.
//
// It exists because the pinning-only refusal had never been executed. TrustRootSourceFor is a
// pure function of GOOS and tlsConfig consults runtime.GOOS, so on the desktop the whole suite
// runs on, the ErrPinRequired arm is unreachable -- the only test naming that error asserts its
// message text. A fail-closed branch that has never run is the defect class this phase exists
// to find, so the branch is made reachable rather than reasoned about.
func WithTrustRootSource(sec Security, src TrustRootSource) Security {
	sec.trustRoots = src
	return sec
}

// trustRootSource is the source this policy verifies against: the platform's, unless a test
// binary has overridden it.
func (s Security) trustRootSource() TrustRootSource {
	if s.trustRoots != "" && testing.Testing() {
		return s.trustRoots
	}
	return TrustRootSourceFor(runtime.GOOS)
}

// MachineSecurity is the transport policy every machine-side dial takes: the gateway
// sidecar, the CLI's short-lived owner connection, and the daemon's pairing rendezvous.
//
// It is Security's default policy -- TLS verified against the platform trust roots,
// cleartext refused, the decision re-asked on every redirect hop -- with ONE exception:
// a ws:// URL whose host is a loopback IP LITERAL is admitted, in a release build as
// well as a test binary.
//
// WHY THE EXCEPTION IS SAFE, and why it is not the flag ADR-007 B37 forbids. B37's chain
// begins with a PASSIVE ON-PATH OBSERVER of a cleartext hop; a connection to 127.0.0.1
// never leaves the host, so there is no path to sit on. The exception cannot be widened
// into one either: it is decided from the URL, a NAME is never accepted (isLoopbackLiteral
// parses an IP and does not resolve), and the same decision is re-run on every redirect,
// so a loopback relay cannot answer the upgrade with "302 -> ws://elsewhere". An operator
// who set this on a real deployment would gain the ability to speak cleartext to their own
// machine and nothing else.
//
// WHY IT HAS TO EXIST. The relay server is ws://-only (server.go sets "ws://"+addr), the
// gateway sidecar is a release binary, and the S19 exit demonstration builds and spawns
// that binary against a real ws://127.0.0.1 relay. Without the exception, local
// development and the exit demonstration both require a TLS terminator and a pin that has
// no channel yet -- and the alternative, an environment variable a deployment could set,
// would be exactly the general kill switch B37 rules out.
func MachineSecurity() Security { return Security{loopbackInRelease: true} }

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
		// A CONFIGURED PIN WITHDRAWS THE CARVE-OUT. The caller has stated which peer it
		// will accept, and cleartext presents no peer at all, so admitting the dial
		// would leave a configured security control doing nothing -- indistinguishable
		// at runtime from a machine that verifies its relay. A malformed one is refused
		// here too, rather than only on the branch that happens to build a TLS config.
		if _, err := s.pin(); err != nil {
			return nil, err
		}
		if s.pinned() {
			return nil, ErrCleartextRefused
		}
		// The host condition is asked FIRST and separately: it is the one that carries
		// the security argument (a loopback hop has no on-path position), and neither
		// opt-in can relax it.
		if !isLoopbackLiteral(u.Hostname()) {
			return nil, ErrCleartextRefused
		}
		if !s.loopbackInRelease && (!s.AllowLoopbackCleartext || !testing.Testing()) {
			return nil, ErrCleartextRefused
		}
		return nil, nil
	case "wss", "https":
		return s.tlsConfig()
	default:
		return nil, fmt.Errorf("relay: unsupported url scheme %q", u.Scheme)
	}
}

// pinned reports whether this policy names a peer it will accept.
func (s Security) pinned() bool {
	return len(s.PinnedCert) > 0 || len(s.PinnedSPKISHA256) > 0
}

// pin validates the SPKI pin and returns it. A pin that is present but is not a SHA-256
// digest is ErrPinMalformed, decided before the dial on every scheme.
func (s Security) pin() ([]byte, error) {
	if len(s.PinnedSPKISHA256) > 0 && len(s.PinnedSPKISHA256) != sha256.Size {
		return nil, ErrPinMalformed
	}
	return s.PinnedSPKISHA256, nil
}

// tlsConfig builds the verification policy for an encrypted dial.
func (s Security) tlsConfig() (*tls.Config, error) {
	if _, err := s.pin(); err != nil {
		return nil, err
	}
	// A PIN OUTRANKS THE UNVERIFIED FLAG, so PairingSecurity can only ever relax the
	// DEFAULT policy and never an explicit one. It is ordered before the flag rather than
	// after it so that a future caller composing the two gets the stronger answer.
	if !s.pinned() && s.unverifiedTLS {
		// ADR-007 B45, and the ONE dial that reaches this: the peer is authenticated by the
		// Noise handshake and the SAS the operator compares, not by this certificate.
		//
		// UNVERIFIED IS NOT UNOBSERVED (ADR-007 B48). The certificate cannot be CHECKED here
		// -- the pin that would check it is what this dial exists to fetch -- but it can be
		// RECORDED, and msg2 then carries the machine's own RelaySPKIPin to compare it
		// against. A network attacker terminating this TLS cannot make the two agree,
		// because the real machine authored the pin and the attacker cannot reach msg2's
		// contents. See Conn.PeerSPKI.
		obs := s.observer
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec // B45: the pairing peer is authenticated by Noise + SAS, not by TLS
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				obs.record(rawCerts)
				return nil // recording only: this dial verifies nothing, by construction
			},
		}, nil
	}
	if s.pinned() {
		pinnedDER := append([]byte(nil), s.PinnedCert...)
		pinnedSPKI := append([]byte(nil), s.PinnedSPKISHA256...)
		// Verification is replaced, not disabled: the presented chain must contain
		// exactly the pinned certificate or exactly the pinned public key, so an
		// equally self-signed impostor is refused where a bare InsecureSkipVerify
		// would accept it.
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec // replaced by the pin check below
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				for _, raw := range rawCerts {
					if len(pinnedDER) > 0 && bytes.Equal(raw, pinnedDER) {
						return nil
					}
					if len(pinnedSPKI) == 0 {
						continue
					}
					// Parsed per certificate rather than once: the SPKI is a FIELD of
					// the certificate, so it cannot be recovered from the raw DER
					// without decoding it. An undecodable certificate is skipped, not
					// accepted -- a peer that cannot be parsed has not matched a pin.
					cert, err := x509.ParseCertificate(raw)
					if err != nil {
						continue
					}
					sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
					if bytes.Equal(sum[:], pinnedSPKI) {
						return nil
					}
				}
				return ErrPinMismatch
			},
		}, nil
	}
	switch s.trustRootSource() {
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

// spkiObserver records the SHA-256 SubjectPublicKeyInfo digest of the leaf certificate a
// peer presented on ONE dial, so an unverified pairing dial can be compared against the
// pin msg2 delivers (ADR-007 B48). It records the first certificate in the presented
// chain -- the leaf -- because that is the key the peer proved possession of, and it is
// the same value relaycfg pins and MachinePayload.RelaySPKIPin carries.
type spkiObserver struct {
	mu   sync.Mutex
	spki []byte
}

func (o *spkiObserver) record(rawCerts [][]byte) {
	if o == nil || len(rawCerts) == 0 {
		return
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return // an undecodable certificate yields no observation, never a wrong one
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.spki = sum[:]
}

func (o *spkiObserver) get() []byte {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.spki...)
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
