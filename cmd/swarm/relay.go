package main

// `swarm relay doctor <wss-url>` (playbook 4.1/6.5): proves DNS resolution,
// TCP+TLS (reporting the policy actually applied), WebSocket upgrade,
// protocol version compatibility, an ephemeral authenticated mailbox
// round-trip, and the relay's own storage health against a self-hosted relay
// -- everything an operator needs before pointing `swarm remote init
// --relay-url` at it.
//
// The mailbox round-trip and storage steps need an operator-minted
// diagnostic capability (relay.MintDiagnosticCapability), which needs the
// SAME operator secret file the relay was booted with (relay_config's
// operator_secret_file / EnsureOperatorSecret). This CLI only reads that
// file locally and mints the capability itself -- there is no network call
// that hands one out, so the public protocol gains no privileged
// unauthenticated endpoint (playbook 6.5).
//
// R2 review MEDIUM: playbook 6.5 requires the doctor to print an actionable
// STORAGE result too -- the mailbox round-trip alone cannot: its diagnostic
// route is deliberately per-connection memory (diag.go), so it never touches
// the relay's bbolt store and would report ok even against a relay whose
// disk is full. The storage step rides the SAME diag_open capability and
// asks diag_status, which runs the relay's own store.healthCheck()/
// diskFreeBytes checks -- the identical checks /readyz reports -- and
// returns the verdict over the ordinary public wss:// connection, since a
// remote operator running this CLI typically has no admin_listen access.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

const relayUsage = `usage: swarm relay <command>

  swarm relay doctor   diagnose a self-hosted relay deployment
`

const relayDoctorUsage = `usage: swarm relay doctor [flags] <wss-url>

  proves DNS resolution, TCP+TLS (reporting the policy actually applied),
  WebSocket upgrade, protocol version compatibility, an ephemeral
  authenticated mailbox round-trip, and storage health (playbook 4.1/6.5).
  See docs/operations/relay-runbook.md §12 for a walkthrough.

  --relay-pin <pin>              base64 SHA-256 SPKI pin (see the relay
                                  runbook); omit to dial under system trust
                                  roots
  --operator-secret-file <path>  the relay's operator secret file, needed for
                                  the mailbox round-trip and storage steps
  --timeout <duration>           per-step network timeout (default 10s)
`

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
	pin := fs.String("relay-pin", "", "base64 SHA-256 SPKI pin (see the relay runbook)")
	secretFile := fs.String("operator-secret-file", "", "path to the relay's operator secret file")
	timeout := fs.Duration("timeout", 10*time.Second, "per-step network timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		_, _ = fmt.Fprint(stderr, relayDoctorUsage)
		return 2
	}
	rawURL := rest[0]

	sec, err := (relaycfg.Config{RelayURL: rawURL, SPKIPin: *pin}).Security()
	if err != nil {
		// R2 review LOW: relaycfg.Config.Pin's error text names relay.json --
		// correct for the three real config readers that field actually comes
		// from, but --relay-pin here never touches that file. Blaming it would
		// send an operator chasing the wrong config after a truncated or
		// CRLF-mangled `relay.pin.b64` (docs/operations/relay-runbook.md §12).
		if errors.Is(err, relay.ErrPinMalformed) {
			_, _ = fmt.Fprint(stderr, "relay doctor: --relay-pin is not base64 of a "+
				"32-byte SHA-256 digest (see docs/operations/relay-runbook.md section 3)\n")
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "relay doctor: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*(*timeout))
	defer cancel()
	steps := runRelayDoctorChecks(ctx, rawURL, sec, *secretFile, *timeout)

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

// Doctor step statuses. R2 review MEDIUM (doc-vs-behavior): a THIRD, neutral
// status -- distinct from ok/fail -- for a step that was deliberately not
// attempted (docs/operations/relay-runbook.md section 12's documented
// network-only workflow: omit --operator-secret-file). Only statusFail turns
// the exit code nonzero; a skip does not, matching what the runbook promises.
const (
	statusOK   = "ok"
	statusFail = "fail"
	statusSkip = "skip"
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
func runRelayDoctorChecks(ctx context.Context, rawURL string, sec relay.Security, operatorSecretFile string, timeout time.Duration) []doctorStep {
	u, err := url.Parse(rawURL)
	if err != nil {
		return []doctorStep{{"url", statusFail, fmt.Sprintf("%q is not a valid URL: %v", rawURL, err)}}
	}
	// R2 review LOW: a bare hostname (the likeliest operator typo -- forgetting
	// wss://) parses with an empty Scheme and Hostname, and every step below
	// then fails on an opaque "lookup :" / "unsupported url scheme \"\"" with
	// no mention of the actual fix. Catching it here, before any step runs,
	// names the fix once instead of five confusing ways.
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return []doctorStep{{"url", statusFail, fmt.Sprintf(
			"%q has no ws:// or wss:// scheme; did you mean wss://%s ? (see docs/operations/relay-runbook.md section 3)",
			rawURL, rawURL)}}
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
		return doctorCheckTCPTLS(c, u, sec, rawURL)
	}))
	ws, proto := bounded2(ctx, timeout, func(c context.Context) (doctorStep, doctorStep) {
		return doctorCheckWSAndProtocol(c, rawURL, sec)
	})
	steps = append(steps, ws, proto)
	mailbox, storage := bounded2(ctx, timeout, func(c context.Context) (doctorStep, doctorStep) {
		return doctorCheckMailboxAndStorage(c, rawURL, sec, operatorSecretFile)
	})
	steps = append(steps, mailbox, storage)
	return steps
}

// bounded2 is bounded's shape for a step that produces two results from one
// bounded connection (WebSocket upgrade + protocol version share a dial).
func bounded2(ctx context.Context, timeout time.Duration, fn func(context.Context) (doctorStep, doctorStep)) (doctorStep, doctorStep) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(c)
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
func doctorCheckTCPTLS(ctx context.Context, u *url.URL, sec relay.Security, rawURL string) doctorStep {
	const name = "TCP+TLS"
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

// doctorCheckWSAndProtocol dials the websocket upgrade under sec and
// negotiates the protocol version on the same connection, since a failed
// upgrade leaves nothing to negotiate over.
func doctorCheckWSAndProtocol(ctx context.Context, rawURL string, sec relay.Security) (ws, proto doctorStep) {
	const wsName, protoName = "WebSocket upgrade", "Protocol version"
	conn, err := relay.DialRawSecure(ctx, rawURL, sec)
	if err != nil {
		return doctorStep{wsName, statusFail, fmt.Sprintf("%v", err)},
			doctorStep{protoName, statusFail, "skipped: no connection"}
	}
	defer func() { _ = conn.Close() }()
	ws = doctorStep{wsName, statusOK, "101 Switching Protocols"}

	version, _, err := conn.Hello(ctx, relay.ProtocolVersion, []string{"mailbox"})
	if err != nil {
		return ws, doctorStep{protoName, statusFail, fmt.Sprintf("hello: %v", err)}
	}
	if version != relay.ProtocolVersion {
		return ws, doctorStep{protoName, statusFail, fmt.Sprintf(
			"relay negotiated version %d; this CLI speaks %d", version, relay.ProtocolVersion)}
	}
	return ws, doctorStep{protoName, statusOK, fmt.Sprintf("negotiated version %d", version)}
}

// doctorCheckMailboxAndStorage is the R2 doctor capability (playbook 6.5): an
// ephemeral relay-auth identity authenticates, opens a diagnostic route with
// an operator-minted capability, then proves two INDEPENDENT things over
// it -- storage health (diag_status) and a mailbox round-trip of
// locally-encrypted random bytes -- before deleting the route. They are
// independent because the round-trip's diagnostic route is per-connection
// memory (diag.go) and never touches the relay's bbolt store: it can succeed
// against a relay whose disk is full (R2 review MEDIUM), which is exactly
// what the separate Storage step exists to catch.
func doctorCheckMailboxAndStorage(ctx context.Context, rawURL string, sec relay.Security, operatorSecretFile string) (mailbox, storage doctorStep) {
	const mbName, stName = "Mailbox round-trip", "Storage"
	// R2 review MEDIUM (doc-vs-behavior): omitting --operator-secret-file is
	// the documented, legitimate network-only workflow
	// (docs/operations/relay-runbook.md section 12) -- these two steps report
	// "skip", not "fail", and a skip does not turn the exit code nonzero. A
	// flag that WAS given but turns out broken (unreadable file, empty file,
	// wrong secret, unreachable relay) is a real failure below, never a skip:
	// the operator asked for the check and it did not run.
	if operatorSecretFile == "" {
		reason := "no --operator-secret-file given; pass the relay's " +
			"operator secret file to mint a diagnostic capability (docs/operations/relay-runbook.md §12)"
		return doctorStep{mbName, statusSkip, reason}, doctorStep{stName, statusSkip, "skipped: " + reason}
	}
	fail := func(reason string) (doctorStep, doctorStep) {
		return doctorStep{mbName, statusFail, reason}, doctorStep{stName, statusFail, "skipped: " + reason}
	}
	secretDoc, err := os.ReadFile(operatorSecretFile)
	if err != nil {
		return fail(fmt.Sprintf("read --operator-secret-file: %v", err))
	}

	// Generated BEFORE minting: the capability is bound to this identity's
	// routing id (R2 review LOW, design -- relay.MintDiagnosticCapability's
	// rid parameter), so it verifies only when THIS keypair goes on to
	// authenticate below.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fail(fmt.Sprintf("generate ephemeral identity: %v", err))
	}
	capability, err := relay.MintDiagnosticCapability(bytes.TrimSpace(secretDoc), time.Now(), relay.RoutingID(pub))
	if err != nil {
		if errors.Is(err, relay.ErrDiagnosticsDisabled) {
			// R2 review LOW (misattribution): an empty/whitespace-only local
			// file must not blame the RELAY's configuration -- the relay is
			// very likely fine; the same treatment the --relay-pin fix above
			// already gives a locally malformed pin.
			return fail(fmt.Sprintf(
				"--operator-secret-file %q is empty (this is a problem with the LOCAL file, not the relay's configuration)",
				operatorSecretFile))
		}
		return fail(fmt.Sprintf("mint diagnostic capability: %v", err))
	}

	auth := relay.ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(challenge []byte) ([]byte, error) { return ed25519.Sign(priv, challenge), nil },
	}
	cl, err := relay.DialSecure(ctx, rawURL, auth, sec)
	if err != nil {
		return fail(fmt.Sprintf("authenticated dial: %v", err))
	}
	defer func() { _ = cl.Close() }()

	if err := cl.DiagOpen(ctx, capability); err != nil {
		reason := fmt.Sprintf(
			"diag_open: %v (check --operator-secret-file matches the relay's operator_secret_file)", err)
		return doctorStep{mbName, statusFail, reason}, doctorStep{stName, statusFail, "skipped: diag_open did not succeed"}
	}

	if status, err := cl.DiagStatus(ctx); err != nil {
		storage = doctorStep{stName, statusFail, fmt.Sprintf("diag_status: %v", err)}
	} else {
		storage = doctorStorageStep(status)
	}
	mailbox = doctorMailboxRoundTrip(ctx, cl)
	return mailbox, storage
}

// doctorStorageStep turns a diag_open reply's storage snapshot into an
// actionable step: the relay's own store.healthCheck()/free-disk verdict
// (relay's checkStorage, health.go), the same one /readyz reports.
func doctorStorageStep(status relay.DiagStatus) doctorStep {
	const name = "Storage"
	if !status.StoreOK {
		return doctorStep{name, statusFail, fmt.Sprintf(
			"persistence store not writable: %s (check the relay host's disk and the bbolt file's permissions)",
			status.StoreError)}
	}
	if status.DiskCheckEnabled && !status.DiskOK {
		if status.DiskError != "" {
			return doctorStep{name, statusFail, fmt.Sprintf("disk free-space check failed: %s", status.DiskError)}
		}
		return doctorStep{name, statusFail, fmt.Sprintf(
			"low disk space: %d bytes free, want >= %d (see quotas.disk_free_min_bytes in the relay runbook)",
			status.DiskFreeBytes, status.DiskFreeMinBytes)}
	}
	if !status.DiskCheckEnabled {
		return doctorStep{name, statusOK, "store writable; disk-space alarm disabled (quotas.disk_free_min_bytes <= 0)"}
	}
	return doctorStep{name, statusOK, fmt.Sprintf(
		"store writable; %d bytes free (>= %d)", status.DiskFreeBytes, status.DiskFreeMinBytes)}
}

// doctorMailboxRoundTrip proves the ALREADY-OPEN diagnostic route works:
// round-trips locally-encrypted random bytes through it and deletes the
// route, proving the relay only ever handled opaque ciphertext.
func doctorMailboxRoundTrip(ctx context.Context, cl *relay.Client) doctorStep {
	const name = "Mailbox round-trip"
	plaintext := make([]byte, 32)
	if _, err := rand.Read(plaintext); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("generate random payload: %v", err)}
	}
	ciphertext, key, err := doctorSeal(plaintext)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("encrypt diagnostic payload: %v", err)}
	}
	if err := cl.DiagAppend(ctx, ciphertext); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("diag_append: %v", err)}
	}
	items, err := cl.DiagRead(ctx)
	if err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("diag_read: %v", err)}
	}
	if len(items) != 1 {
		return doctorStep{name, statusFail, fmt.Sprintf("diag_read returned %d item(s), want exactly 1", len(items))}
	}
	got, err := doctorOpen(items[0].Envelope, key)
	if err != nil || !bytes.Equal(got, plaintext) {
		return doctorStep{name, statusFail, "the round-tripped bytes did not decrypt to the sent payload"}
	}
	if err := cl.DiagClose(ctx); err != nil {
		return doctorStep{name, statusFail, fmt.Sprintf("diag_close: %v", err)}
	}
	return doctorStep{name, statusOK, fmt.Sprintf(
		"%d bytes round-tripped through an ephemeral, single-use diagnostic route", len(plaintext))}
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
