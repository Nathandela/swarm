package main

// `swarm relay doctor` proves the configured machine's relay-v2 path: DNS,
// TCP+TLS under relay.json's exact pin policy, the v2 edge marker, then a
// machine-authenticated pairing rendezvous exchanging locally AES-GCM-sealed
// bytes in both directions. It deliberately creates no phone member or
// retirement record, and finishes the rendezvous even when a later step fails.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

const relayUsage = `usage: swarm relay <command>

  swarm relay doctor   diagnose this machine's configured relay-v2 path
`

const relayDoctorUsage = `usage: swarm relay doctor [flags]

  proves DNS resolution, TCP+TLS, authenticated relay-v2 control, and an
  encrypted ephemeral pairing-rendezvous round-trip using this machine's
  configured relay identity and relay.json.

  --timeout <duration>           per-step network timeout (default 10s)
`

const relayV2Marker = "swarm relay v2"

// runRelay is the `swarm relay` role: it dispatches to a relay-operator verb.
func runRelay(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, relayUsage)
		return 2
	}
	switch args[0] {
	case "doctor":
		return runRelayDoctor(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "relay: unknown relay command %q\n", args[0])
		return 2
	}
}

// runRelayDoctor parses flags, runs every diagnostic step, and prints one
// ok/fail line per step. It exits 0 only when every step succeeded.
func runRelayDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("relay doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 10*time.Second, "per-step network timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 0 {
		_, _ = fmt.Fprint(stderr, relayDoctorUsage)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*(*timeout))
	defer cancel()
	steps := runRelayDoctorChecks(ctx, remoteStateDir(), *timeout)

	allOK := true
	for _, st := range steps {
		if st.status == statusFail {
			allOK = false
		}
		_, _ = fmt.Fprintf(stdout, "%-20s %-4s %s\n", st.name, st.status, st.detail)
	}
	if !allOK {
		return 1
	}
	return 0
}

const (
	statusOK   = "ok"
	statusFail = "fail"
)

// doctorStep is one diagnostic check's outcome, with an actionable remedy in
// detail whenever status is not statusOK.
type doctorStep struct {
	name   string
	status string
	detail string
}

// runRelayDoctorChecks runs every step independently (a DNS failure does not
// prevent the TCP+TLS step from attempting and reporting its own result) and
// returns them in the playbook's order.
func runRelayDoctorChecks(ctx context.Context, stateDir string, timeout time.Duration) []doctorStep {
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil {
		return []doctorStep{{"Configuration", statusFail, "load configured relay state: " + err.Error()}}
	}
	if !found || cfg.RelayURL == "" {
		return []doctorStep{{"Configuration", statusFail, "relay.json is absent or has no relay URL; run swarm remote init first"}}
	}
	sec, err := cfg.Security()
	if err != nil {
		return []doctorStep{{"Configuration", statusFail, fmt.Sprintf("relay.json TLS policy: %v", err)}}
	}
	id, err := machineid.Load(filepath.Join(stateDir, "remote", remoteIdentityFile))
	if err != nil {
		return []doctorStep{{"Configuration", statusFail, fmt.Sprintf("load machine relay identity: %v", err)}}
	}
	u, err := url.Parse(cfg.RelayURL)
	if err != nil {
		return []doctorStep{{"Configuration", statusFail, fmt.Sprintf("%q is not a valid relay URL: %v", cfg.RelayURL, err)}}
	}
	// R2 review LOW: a bare hostname (the likeliest operator typo -- forgetting
	// wss://) parses with an empty Scheme and Hostname, and every step below
	// then fails on an opaque "lookup :" / "unsupported url scheme \"\"" with
	// no mention of the actual fix. Catching it here, before any step runs,
	// names the fix once instead of five confusing ways.
	if u.Scheme != "ws" && u.Scheme != "wss" && u.Scheme != "http" && u.Scheme != "https" {
		return []doctorStep{{"Configuration", statusFail, fmt.Sprintf("relay URL %q has no ws://, wss://, http://, or https:// scheme", cfg.RelayURL)}}
	}

	bounded := func(fn func(context.Context) doctorStep) doctorStep {
		c, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return fn(c)
	}

	var steps []doctorStep
	steps = append(steps, bounded(func(c context.Context) doctorStep {
		return doctorCheckDNS(c, u.Hostname())
	}))
	steps = append(steps, bounded(func(c context.Context) doctorStep {
		return doctorCheckTCPTLS(c, cfg.RelayURL, sec)
	}))
	steps = append(steps, bounded(func(c context.Context) doctorStep {
		return doctorCheckRelayV2Edge(c, cfg.RelayURL, sec)
	}))
	steps = append(steps, bounded(func(c context.Context) doctorStep {
		return doctorCheckRelayV2(c, cfg, sec, id)
	}))
	return steps
}

// doctorCheckDNS resolves host, the first proven step (playbook 4.1).
func doctorCheckDNS(ctx context.Context, host string) doctorStep {
	const name = "DNS resolution"
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		// R2 review LOW: err is a *net.DNSError whose own Error() already begins
		// "lookup <host>: ...", so wrapping it again in "lookup %s: %v" produced
		// a doubled, confusing "lookup X: lookup X: ..." prefix.
		return doctorStep{name, statusFail, fmt.Sprintf("%v (check the DNS record for this host)", err)}
	}
	detail := host
	for i, a := range addrs {
		if i == 0 {
			detail += " -> "
		} else {
			detail += ", "
		}
		detail += a
	}
	return doctorStep{name, statusOK, detail}
}

// doctorCheckTCPTLS proves the TCP connect and, under an encrypted scheme,
// the TLS handshake under the EXACT policy a real machine/phone dial would
// apply -- and reports which policy that was.
func doctorCheckTCPTLS(ctx context.Context, rawURL string, sec relay.Security) doctorStep {
	const name = "TCP+TLS"
	u, err := url.Parse(rawURL)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("invalid relay URL: %v", err)}
	}
	tlsCfg, err := sec.Resolve(rawURL)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("transport policy: %v", err)}
	}
	policy := doctorPolicyName(sec, tlsCfg)

	hostport, err := doctorHostPort(u)
	if err != nil {
		return doctorStep{name, statusFail, err.Error()}
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("connect to %s: %v (check the host is reachable and the port is open)", hostport, err)}
	}
	defer func() { _ = conn.Close() }()

	if tlsCfg == nil {
		return doctorStep{name, statusOK, fmt.Sprintf("policy: %s (no TLS on this hop)", policy)}
	}
	// R2 review H-1: security.go's tlsConfig() never sets ServerName -- the real
	// dial path only works because http.Transport fills it in from the request
	// URL (net/http, not this hand-rolled tls.Client). Without it, Go's TLS
	// client refuses to even attempt the handshake ("either ServerName or
	// InsecureSkipVerify must be specified"), so certificate validation -- the
	// very thing ADR-016 makes this step report on -- would never run. Cloned
	// rather than mutated in place: never reach for InsecureSkipVerify here, or
	// a false FAIL becomes a false OK.
	if tlsCfg.ServerName == "" {
		clone := tlsCfg.Clone()
		clone.ServerName = u.Hostname()
		tlsCfg = clone
	}
	tlsConn := tls.Client(conn, tlsCfg)
	defer func() { _ = tlsConn.Close() }()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("TLS handshake under policy %q: %v", policy, err)}
	}
	cs := tlsConn.ConnectionState()
	detail := fmt.Sprintf("policy: %s", policy)
	if len(cs.PeerCertificates) > 0 {
		leaf := cs.PeerCertificates[0]
		detail += fmt.Sprintf("; issuer=%q not-after=%s", leaf.Issuer.CommonName, leaf.NotAfter.Format(time.RFC3339))
	}
	return doctorStep{name, statusOK, detail}
}

// doctorHostPort returns host:port for a TCP dial against u, deriving the
// scheme's default port when the URL omits one (R2 review H-2) -- exactly the
// portless form docs/operations/relay-vps-deploy.md's own examples use
// (`--relay-url wss://relay.example.com`). Without this, a portless URL
// reaches net.Dialer.DialContext as a bare hostname and fails with "missing
// port in address" before TLS is ever attempted.
func doctorHostPort(u *url.URL) (string, error) {
	if u.Port() != "" {
		return u.Host, nil
	}
	var port string
	switch u.Scheme {
	case "wss", "https":
		port = "443"
	case "ws", "http":
		port = "80"
	default:
		return "", fmt.Errorf("relay doctor: unsupported url scheme %q", u.Scheme)
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// doctorPolicyName names the transport-security policy this doctor run
// actually applied, so an operator sees the SAME fact ADR-016 requires the
// doctor to print rather than an inferred one.
func doctorPolicyName(sec relay.Security, tlsCfg *tls.Config) string {
	if tlsCfg == nil {
		return "cleartext (loopback exemption)"
	}
	if len(sec.PinnedSPKISHA256) > 0 || len(sec.PinnedCert) > 0 {
		return "pinned certificate (operator-configured)"
	}
	return "system trust roots"
}

// doctorCheckRelayV2Edge only proves that this endpoint advertises the v2
// service. It is deliberately not a readiness check: authenticated control and
// the rendezvous exchange below prove the usable relay path.
func doctorCheckRelayV2Edge(ctx context.Context, rawURL string, sec relay.Security) doctorStep {
	const name = "Relay-v2 edge"
	u, err := url.Parse(rawURL)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("invalid relay URL: %v", err)}
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return doctorStep{name, statusFail, fmt.Sprintf("unsupported relay URL scheme %q", u.Scheme)}
	}
	u.Path, u.RawQuery, u.Fragment = "/", "", ""
	tlsCfg, err := sec.Resolve(rawURL)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("transport policy: %v", err)}
	}
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("build request: %v", err)}
	}
	response, err := client.Do(request)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("GET /: %v", err)}
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(relayV2Marker)+1)))
	if err != nil || response.StatusCode != http.StatusOK || string(body) != relayV2Marker {
		return doctorStep{name, statusFail, "GET / did not return the swarm relay v2 marker"}
	}
	return doctorStep{name, statusOK, "GET / returned swarm relay v2"}
}

// doctorCheckRelayV2 proves the configured machine can authenticate, create a
// one-shot rendezvous, and exchange opaque encrypted frames. Pairing state is
// always finished by the machine control connection; it never creates a member
// or a retirement record.
func doctorCheckRelayV2(ctx context.Context, cfg relaycfg.Config, sec relay.Security, id *machineid.Identity) doctorStep {
	const name = "Relay-v2 rendezvous"
	machineRID := relayv2.RoutingID(id.RelayAuthPublic())
	profile := relayv2.Profile{RelayURL: cfg.RelayURL, MachineRID: machineRID, OperatorNamespace: cfg.OperatorNamespace, Security: sec}
	control, err := relayv2.Dial(ctx, profile, relayv2.Auth{PublicKey: id.RelayAuthPublic(), Sign: func(message []byte) ([]byte, error) {
		return id.RelayAuthSign(message), nil
	}, Role: relayv2.RoleMachine, Purpose: relayv2.PurposeControl})
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("machine-control authentication: %v", err)}
	}
	defer control.Close()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("generate ceremony: %v", err)}
	}
	ceremony := hex.EncodeToString(raw[:])
	machine := relayv2.NewMachinePairTransport(control)
	if err := machine.Create(ctx, ceremony); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("pair create: %v", err)}
	}
	finished := false
	defer func() {
		if !finished {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = machine.Complete(cleanup, ceremony)
		}
	}()
	claimant, err := relayv2.DialPair(ctx, relayv2.Profile{RelayURL: cfg.RelayURL, Security: sec}, ceremony)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("pair claimant dial: %v", err)}
	}
	defer claimant.Close()
	if err := claimant.Claim(ctx, ceremony); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("pair claim: %v", err)}
	}
	if err := doctorPairExchange(ctx, machine, claimant); err != nil {
		return doctorStep{name, statusFail, err.Error()}
	}
	if err := machine.Complete(ctx, ceremony); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("pair finish: %v", err)}
	}
	finished = true
	select {
	case <-control.Done():
		return doctorStep{name, statusFail, "machine-control connection was superseded during the probe"}
	default:
	}
	return doctorStep{name, statusOK, "machine control authenticated; encrypted pairing frames exchanged and retired"}
}

func doctorPairExchange(ctx context.Context, machine, claimant *relayv2.PairTransport) error {
	for _, direction := range []struct {
		from, to *relayv2.PairTransport
		name     string
	}{{machine, claimant, "machine-to-claimant"}, {claimant, machine, "claimant-to-machine"}} {
		plain := make([]byte, 32)
		if _, err := rand.Read(plain); err != nil {
			return fmt.Errorf("generate %s payload: %w", direction.name, err)
		}
		ciphertext, key, err := doctorSeal(plain)
		if err != nil {
			return fmt.Errorf("encrypt %s payload: %w", direction.name, err)
		}
		if err := direction.from.Send(ctx, ciphertext); err != nil {
			return fmt.Errorf("send %s payload: %w", direction.name, err)
		}
		got, err := direction.to.Recv(ctx)
		if err != nil {
			return fmt.Errorf("receive %s payload: %w", direction.name, err)
		}
		opened, err := doctorOpen(got, key)
		if err != nil || !bytes.Equal(opened, plain) {
			return fmt.Errorf("%s payload did not decrypt exactly", direction.name)
		}
	}
	return nil
}

// doctorSeal AES-256-GCM-encrypts plaintext under a freshly generated,
// process-local key that never leaves this CLI: the relay handles only the
// returned opaque ciphertext, exactly as it does for real session content.
func doctorSeal(plaintext []byte) (ciphertext, key []byte, err error) {
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), key, nil
}

// doctorOpen is doctorSeal's inverse.
func doctorOpen(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext shorter than one nonce")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}
