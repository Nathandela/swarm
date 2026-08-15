package main

// `swarm relay doctor` (playbook 4.1/6.5, R2 hggx.3.2) FAILING-FIRST tests
// (TDD RED, GG-5). runRelay/runRelayDoctor do not exist yet: this file does
// not compile until they do.
//
// The relay under test is a REAL relay.Server, built only from the relay
// package's exported surface (relay.New/WithOperatorSecret/Start) -- cmd/swarm
// is an external consumer of that package, so its own test fixture uses
// nothing internal/test-only.
//
// R2 review pass (H-1/H-2/coverage-gap): the tests above this line all dial a
// loopback plain-ws:// relay, so the doctor's TLS branch -- the step ADR-016:197
// names as an obligation -- had never executed. startDoctorTestRelayTLS fronts
// the same real relay with a genuine TLS listener (self-signed cert), the same
// shape internal/remote/relay's own transportsec_harness_test.go uses for its
// package, rebuilt here from cmd/swarm's exported-surface-only fixtures.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// startDoctorTestRelayTLS fronts startDoctorTestRelay's real, loopback relay
// with a TLS terminator holding a self-signed certificate (httptest.NewTLSServer
// mints one automatically) and returns the wss:// URL plus that certificate.
func startDoctorTestRelayTLS(t *testing.T, secret []byte) (wssURL string, cert *x509.Certificate) {
	t.Helper()
	srv := startDoctorTestRelay(t, secret)
	target, err := url.Parse(strings.Replace(srv.URL(), "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewUnstartedServer(proxy)
	// These tests deliberately trigger TLS handshake failures (the unpinned
	// case) and drop the connection right after a bare handshake (the pinned
	// case, which never issues an HTTP request over it) -- the server's default
	// ErrorLog would otherwise print both to stderr as if something were wrong.
	front.Config.ErrorLog = log.New(io.Discard, "", 0)
	front.StartTLS()
	t.Cleanup(front.Close)
	return strings.Replace(front.URL, "https://", "wss://", 1), front.Certificate()
}

// doctorStepStatus finds step name's line in a doctor CLI's stdout (the
// fixed-width "%-20s %-4s %s\n" format runRelayDoctor prints) and returns its
// status column, failing the test if the step never printed a line at all.
// A bare substring check on the whole output ("ok" appears in status columns
// AND ordinary detail text) does not actually establish which step passed --
// R2 review LOW.
func doctorStepStatus(t *testing.T, out, name string) string {
	t.Helper()
	prefix := fmt.Sprintf("%-20s ", name)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, prefix))
		if len(fields) == 0 {
			t.Fatalf("doctor output line for step %q has no status field: %q", name, line)
		}
		return fields[0]
	}
	t.Fatalf("doctor output has no line for step %q; got:\n%s", name, out)
	return ""
}

// startDoctorTestRelay boots a real, loopback, plain-ws:// relay for the
// doctor CLI to dial, optionally with an operator secret installed. It runs
// the relay's default REAL clock, deliberately: MintDiagnosticCapability (the
// CLI side) stamps a capability with the real wall clock, exactly as
// production does, so the relay verifying it must read the same clock family
// -- capability TTL/single-use timing is the relay package's own suite's job
// (internal/remote/relay/diag_test.go), not this CLI-wiring one's.
func startDoctorTestRelay(t *testing.T, secret []byte) *relay.Server {
	t.Helper()
	return startDoctorTestRelayWithOpts(t, secret)
}

// startDoctorTestRelayWithOpts is startDoctorTestRelay with room for further
// relay.Options -- the R2 review MEDIUM fix's storage-step tests use it to
// inject relay.WithDiskFreeFunc without duplicating this fixture.
func startDoctorTestRelayWithOpts(t *testing.T, secret []byte, extra ...relay.Option) *relay.Server {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	var opts []relay.Option
	if len(secret) > 0 {
		opts = append(opts, relay.WithOperatorSecret(secret))
	}
	opts = append(opts, extra...)
	srv, err := relay.New(cfg, opts...)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// TestRelayDoctor_AllStepsOkWithOperatorSecret is the whole-command happy
// path: every one of the five proven steps (playbook 4.1) reports ok, and the
// process exits 0.
func TestRelayDoctor_AllStepsOkWithOperatorSecret(t *testing.T) {
	secret := []byte("fixture-operator-secret-thirty-two-bytes-plus")
	srv := startDoctorTestRelay(t, secret)
	secretPath := filepath.Join(t.TempDir(), "operator.secret")
	if err := os.WriteFile(secretPath, secret, 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--operator-secret-file", secretPath, srv.URL()}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runRelay doctor exit code = %d, want 0. stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"DNS resolution", "TCP+TLS", "WebSocket upgrade", "Protocol version", "Mailbox round-trip", "Storage"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing step %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fail") {
		t.Errorf("doctor output reports a failing step on the happy path:\n%s", out)
	}
}

// TestRelayDoctor_MailboxAndStorageStepsSkipWithoutOperatorSecretFile is the
// R2 review MEDIUM (doc-vs-behavior) fix: the runbook promises that omitting
// --operator-secret-file "runs every step except the mailbox round-trip and
// storage checks -- useful when you only have network access to the relay,
// not its host." That is a legitimate, exit-0 workflow, not a failing run --
// the two identity-gated steps report "skip", never "fail", and a skip must
// not turn the exit code nonzero. The first four steps still run and report
// ok against a relay that DOES have diagnostics configured -- only the CLI
// invocation omitted the flag.
func TestRelayDoctor_MailboxAndStorageStepsSkipWithoutOperatorSecretFile(t *testing.T) {
	srv := startDoctorTestRelay(t, []byte("fixture-operator-secret-thirty-two-bytes-plus"))

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", srv.URL()}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runRelay doctor with no --operator-secret-file exited %d, want 0 (a skip, not a failure). stdout=%s", code, stdout.String())
	}
	out := stdout.String()
	for _, name := range []string{"DNS resolution", "TCP+TLS", "WebSocket upgrade", "Protocol version"} {
		if status := doctorStepStatus(t, out, name); status != "ok" {
			t.Errorf("step %q status = %q, want ok; got:\n%s", name, status, out)
		}
	}
	for _, name := range []string{"Mailbox round-trip", "Storage"} {
		if status := doctorStepStatus(t, out, name); status != "skip" {
			t.Errorf("step %q status = %q without --operator-secret-file, want skip; got:\n%s", name, status, out)
		}
	}
	if !strings.Contains(out, "operator-secret-file") {
		t.Errorf("the skipped mailbox step must name the fix (--operator-secret-file); got:\n%s", out)
	}
	if strings.Contains(out, "fail") {
		t.Errorf("no step should report fail on a bare skip; got:\n%s", out)
	}
}

// TestRelayDoctor_MailboxStepFailsWhenOperatorSecretFileIsUnreadable is the
// skip test's other half: "the operator OMITTED the flag" (skip, above) is
// distinct from "the operator PASSED the flag and it is broken" -- a real
// failure, not a skip, and it must still turn the exit code nonzero.
func TestRelayDoctor_MailboxStepFailsWhenOperatorSecretFileIsUnreadable(t *testing.T) {
	srv := startDoctorTestRelay(t, []byte("fixture-operator-secret-thirty-two-bytes-plus"))
	missing := filepath.Join(t.TempDir(), "does-not-exist.secret")

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--operator-secret-file", missing, srv.URL()}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runRelay doctor with an unreadable --operator-secret-file exited 0, want nonzero. stdout=%s", stdout.String())
	}
	out := stdout.String()
	for _, name := range []string{"Mailbox round-trip", "Storage"} {
		if status := doctorStepStatus(t, out, name); status != "fail" {
			t.Errorf("step %q status = %q with an unreadable --operator-secret-file, want fail (not skip); got:\n%s", name, status, out)
		}
	}
}

// TestRelayDoctor_MailboxStepNamesTheLocalFileWhenOperatorSecretFileIsEmpty is
// the R2 review LOW (misattribution) fix: an empty/whitespace-only
// --operator-secret-file must not blame the RELAY's configuration (the
// generic "diagnostics disabled; no operator secret configured" relay.go
// prints verbatim) for a purely local, client-side file problem -- the same
// treatment the --relay-pin LOW fix already gives a malformed pin.
func TestRelayDoctor_MailboxStepNamesTheLocalFileWhenOperatorSecretFileIsEmpty(t *testing.T) {
	srv := startDoctorTestRelay(t, []byte("fixture-operator-secret-thirty-two-bytes-plus"))
	empty := filepath.Join(t.TempDir(), "empty.secret")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty secret file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--operator-secret-file", empty, srv.URL()}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runRelay doctor with an empty --operator-secret-file exited 0, want nonzero. stdout=%s", stdout.String())
	}
	out := stdout.String()
	if strings.Contains(out, "no operator secret configured") {
		t.Errorf("an empty LOCAL --operator-secret-file must not blame the relay's own configuration; got:\n%s", out)
	}
	if !strings.Contains(out, empty) && !strings.Contains(out, "--operator-secret-file") {
		t.Errorf("the failure must name the local file/flag that is actually empty; got:\n%s", out)
	}
}

// TestRelayDoctor_FailsCleanlyOnAnUnreachableRelay proves the tool never
// panics or hangs on a dead relay, and names the connect failure.
func TestRelayDoctor_FailsCleanlyOnAnUnreachableRelay(t *testing.T) {
	// A loopback listener bound and then closed: the port is very likely
	// refused rather than reachable, without depending on network access.
	ln := mustCloseNewListener(t)

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--timeout", "500ms", "ws://" + ln}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runRelay doctor against an unreachable relay exited 0, want nonzero. stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "fail") {
		t.Errorf("expected at least one failing step reported; got:\n%s", stdout.String())
	}
}

// TestRelayDoctor_RequiresExactlyOneURLArgument is the usage-error path.
func TestRelayDoctor_RequiresExactlyOneURLArgument(t *testing.T) {
	for _, args := range [][]string{
		{"doctor"},
		{"doctor", "wss://a.example.com:443", "wss://b.example.com:443"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runRelay(args, &stdout, &stderr); code != 2 {
			t.Errorf("runRelay %v exit code = %d, want 2 (usage error)", args, code)
		}
	}
}

// TestDoctorHostPort_DerivesDefaultPortWhenURLOmitsOne is the R2 review H-2
// fix: docs/operations/relay-vps-deploy.md's own examples (§11, §14b) tell
// operators to configure the PORTLESS form (`--relay-url wss://relay.example.com`),
// whose url.URL.Host carries no port at all -- net.Dialer.DialContext then fails
// with "missing port in address" before TLS is ever attempted.
func TestDoctorHostPort_DerivesDefaultPortWhenURLOmitsOne(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"wss://relay.example.com", "relay.example.com:443"},
		{"ws://relay.example.com", "relay.example.com:80"},
		{"wss://relay.example.com:8443", "relay.example.com:8443"},
		{"ws://127.0.0.1:9000", "127.0.0.1:9000"},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		got, err := doctorHostPort(u)
		if err != nil {
			t.Fatalf("doctorHostPort(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Errorf("doctorHostPort(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestDoctorCheckTCPTLS_UnpinnedSystemTrustRootsReachesCertificateValidation is
// the R2 review H-1 fix, and the coverage gap behind it: against a REAL TLS
// listener (self-signed cert), the unpinned/system-trust-roots policy -- what
// ADR-016's Web-PKI migration steers operators toward -- must reach actual
// certificate-chain validation rather than aborting on a bare tls.Config that
// names neither ServerName nor InsecureSkipVerify.
func TestDoctorCheckTCPTLS_UnpinnedSystemTrustRootsReachesCertificateValidation(t *testing.T) {
	wssURL, _ := startDoctorTestRelayTLS(t, nil)
	u, err := url.Parse(wssURL)
	if err != nil {
		t.Fatalf("parse %q: %v", wssURL, err)
	}

	step := doctorCheckTCPTLS(context.Background(), u, relay.Security{}, wssURL)
	if step.status == statusOK {
		t.Fatalf("doctorCheckTCPTLS against a self-signed cert under system trust roots reported ok, want a certificate-validation failure; detail=%q", step.detail)
	}
	if strings.Contains(step.detail, "ServerName or InsecureSkipVerify") {
		t.Fatalf("doctorCheckTCPTLS failed on a tls.Config precondition (ServerName never set) instead of reaching certificate validation: %q", step.detail)
	}
}

// TestDoctorCheckTCPTLS_PinnedPolicySucceedsAgainstSelfSignedCert proves the
// H-1 fix does not regress the pinned path: a matching SPKI pin against the
// SAME self-signed listener must still report ok.
func TestDoctorCheckTCPTLS_PinnedPolicySucceedsAgainstSelfSignedCert(t *testing.T) {
	wssURL, cert := startDoctorTestRelayTLS(t, nil)
	u, err := url.Parse(wssURL)
	if err != nil {
		t.Fatalf("parse %q: %v", wssURL, err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)

	step := doctorCheckTCPTLS(context.Background(), u, relay.Security{PinnedSPKISHA256: sum[:]}, wssURL)
	if step.status != statusOK {
		t.Fatalf("doctorCheckTCPTLS under a matching SPKI pin: got fail, detail=%q", step.detail)
	}
}

// TestRelayDoctor_AllStepsOkOverARealWSSListener is the missing coverage the
// review's BLOCKING ROOT CAUSE finding names: every earlier test in this file
// dials a loopback plain-ws:// relay, so the TLS branch had never executed
// through the full CLI. This runs all five steps -- including the mailbox
// round-trip, which needs the WebSocket-upgrade dial that also goes through
// doctorCheckTCPTLS's Security policy -- over a real wss:// listener.
func TestRelayDoctor_AllStepsOkOverARealWSSListener(t *testing.T) {
	secret := []byte("fixture-operator-secret-thirty-two-bytes-plus")
	wssURL, cert := startDoctorTestRelayTLS(t, secret)
	secretPath := filepath.Join(t.TempDir(), "operator.secret")
	if err := os.WriteFile(secretPath, secret, 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	pin := base64.StdEncoding.EncodeToString(sum[:])

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--relay-pin", pin, "--operator-secret-file", secretPath, wssURL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runRelay doctor over a real wss:// listener exit code = %d, want 0. stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, name := range []string{"DNS resolution", "TCP+TLS", "WebSocket upgrade", "Protocol version", "Mailbox round-trip", "Storage"} {
		if status := doctorStepStatus(t, out, name); status != "ok" {
			t.Errorf("step %q status = %q, want ok over a real wss:// listener; got:\n%s", name, status, out)
		}
	}
}

// TestRelayDoctor_StorageStepFailsWhenDiskIsLow is the R2 review MEDIUM fix's
// end-to-end proof: the failure scenario the review named directly (a relay
// whose disk is full passes every step because the round-trip route is
// per-connection memory, never bbolt) must now fail on the NEW Storage step
// while the Mailbox round-trip -- which never touches disk -- still succeeds.
func TestRelayDoctor_StorageStepFailsWhenDiskIsLow(t *testing.T) {
	secret := []byte("fixture-operator-secret-thirty-two-bytes-plus")
	lowDisk := func() (uint64, error) { return 1, nil } // 1 byte free, far below the default 1 GiB alarm
	srv := startDoctorTestRelayWithOpts(t, secret, relay.WithDiskFreeFunc(lowDisk))
	secretPath := filepath.Join(t.TempDir(), "operator.secret")
	if err := os.WriteFile(secretPath, secret, 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--operator-secret-file", secretPath, srv.URL()}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runRelay doctor against a low-disk relay exited 0, want nonzero. stdout=%s", stdout.String())
	}
	out := stdout.String()
	if status := doctorStepStatus(t, out, "Storage"); status != "fail" {
		t.Errorf("Storage step status = %q against a low-disk relay, want fail; got:\n%s", status, out)
	}
	if status := doctorStepStatus(t, out, "Mailbox round-trip"); status != "ok" {
		t.Errorf("Mailbox round-trip status = %q, want ok: the diag route is in-memory and must succeed independent of disk health; got:\n%s", status, out)
	}
}

// TestDoctorCheckDNS_FailureIsNotDoubled is the R2 review LOW fix: a
// net.DNSError's own Error() already begins "lookup <host>: ...", so wrapping
// it again in a "lookup %s: %v" format produced a doubled, confusing prefix.
func TestDoctorCheckDNS_FailureIsNotDoubled(t *testing.T) {
	host := "this-host-should-not-resolve.invalid" // RFC 2606 reserved TLD
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	step := doctorCheckDNS(ctx, host)
	if step.status == statusOK {
		t.Skipf("environment unexpectedly resolved %q; cannot exercise the failure path", host)
	}
	if strings.Count(step.detail, "lookup "+host) > 1 {
		t.Errorf("DNS failure detail has a doubled prefix: %q", step.detail)
	}
}

// TestRelayDoctor_SchemelessURLNamesTheFix is the R2 review LOW fix's other
// half: a bare hostname (the likeliest operator typo -- forgetting wss://)
// must not fall through to url.Hostname()=="" and an opaque double "lookup :"
// DNS failure; it must name the missing scheme.
func TestRelayDoctor_SchemelessURLNamesTheFix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "relay.example.com"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runRelay doctor with a schemeless url exited 0, want nonzero")
	}
	out := stdout.String()
	if !strings.Contains(out, "wss://") {
		t.Errorf("a schemeless url's failure must suggest adding wss://; got:\n%s", out)
	}
	if strings.Contains(out, "lookup : lookup") {
		t.Errorf("schemeless url fell through to the empty-host DNS failure instead of naming the missing scheme; got:\n%s", out)
	}
}

// TestRelayDoctor_MalformedRelayPinNamesTheFlagNotAFile is the R2 review LOW
// fix: relaycfg.Config.Pin's error text names relay.json -- correct for the
// three real config readers, but the doctor's --relay-pin never touches that
// file, so blaming it points an operator at the wrong thing entirely.
func TestRelayDoctor_MalformedRelayPinNamesTheFlagNotAFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRelay([]string{"doctor", "--relay-pin", "not valid base64!!", "wss://relay.example.com"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runRelay doctor with a malformed --relay-pin exit code = %d, want 1. stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	errOut := stderr.String()
	if strings.Contains(errOut, "relay.json") {
		t.Errorf("malformed --relay-pin error must not blame relay.json (the operator never used that file); got: %s", errOut)
	}
	if !strings.Contains(errOut, "--relay-pin") {
		t.Errorf("malformed --relay-pin error must name the flag that was actually wrong; got: %s", errOut)
	}
}

// mustCloseNewListener returns a loopback host:port that was briefly bound
// and is now closed, so a dial against it fails fast and locally.
func mustCloseNewListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}
