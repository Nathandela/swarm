// PB-NET-2 (FAILING FIRST): transport security policy.
//
//	TLS verified by default; a pinned self-signed cert is an explicit opt-in for
//	self-hosted relays; cleartext refused EXCEPT a narrowly-scoped loopback
//	carve-out for the in-process test relay -- which cannot be enabled in a
//	release build. The Go client's trust-root source on Android must be STATED,
//	because x509.SystemCertPool is not usable there as it is on desktop (opus H3),
//	and PB-SEC-5 establishes that Android's networkSecurityConfig does not govern
//	crypto/tls inside a native .so, so this is the SOLE control for the relay
//	transport.
//
// The carve-out exists because the relay server is ws://-only today
// (internal/remote/relay/server.go:228 sets "ws://"+addr), so an unconditional
// cleartext ban makes PB-NET-1 and PB-E2E-1 unsatisfiable (fable F6).
package relay_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestTLS_DefaultVerificationFailsClosedOnUntrustedCert asserts the DEFAULT policy
// is real verification: a self-signed relay certificate that no system root vouches
// for is refused, and no Client is returned. "Fails closed" means an error, never a
// silent downgrade to an unverified connection.
func TestTLS_DefaultVerificationFailsClosedOnUntrustedCert(t *testing.T) {
	wss, _ := startTLSRelay(t)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), relay.Security{})
	if err == nil {
		_ = c.Close()
		t.Fatalf("DialSecure accepted an untrusted self-signed relay certificate under the default policy")
	}
	if c != nil {
		t.Fatalf("DialSecure returned a non-nil Client alongside an error: %v", err)
	}
}

// TestTLS_PinnedCertIsAnExplicitOptIn asserts the self-hosted-relay path: with the
// operator's certificate explicitly pinned, the connection completes all the way
// through the relay-auth handshake against the REAL relay behind the terminator.
// Pinning is opt-in per connection, never a global relaxation.
func TestTLS_PinnedCertIsAnExplicitOptIn(t *testing.T) {
	wss, der := startTLSRelay(t)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), relay.Security{PinnedCert: der})
	if err != nil {
		t.Fatalf("DialSecure with the matching pin: %v", err)
	}
	defer func() { _ = c.Close() }()
	if got, want := c.RoutingID(), relay.RoutingID(pub); got != want {
		t.Fatalf("routing id: got %q, want %q", got, want)
	}
}

// TestTLS_PinAcceptsOnlyThePinnedCert asserts pinning is a WHITELIST of one: a
// different, equally self-signed certificate is refused. Without this a "pinning"
// implementation that merely disables verification would pass the test above.
func TestTLS_PinAcceptsOnlyThePinnedCert(t *testing.T) {
	wss, _ := startTLSRelay(t)
	other := selfSignedDER(t)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), relay.Security{PinnedCert: other})
	if err == nil {
		_ = c.Close()
		t.Fatalf("DialSecure accepted a certificate that does not match the pin")
	}
	if !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("wrong-pin error: got %v, want ErrPinMismatch", err)
	}
}

// TestTLS_RedirectToCleartextIsRefused asserts the policy is decided on EVERY hop,
// not only on the URL the caller handed in. coder/websocket installs a CheckRedirect
// that FOLLOWS a 3xx and rewrites ws->http / wss->https (dial.go:90-101), so a relay
// -- or anyone who takes one over -- can answer the upgrade with "302 -> ws://" and
// carry the rest of the session in cleartext. Payloads stay sealed, so this is a
// routing-metadata break rather than a content break, and routing metadata is exactly
// what PB-NET-2's cleartext ban protects.
//
// The dial below uses the strongest policy available (an explicit pin, no cleartext
// opt-in), so an admitted downgrade cannot be blamed on a lax caller.
func TestTLS_RedirectToCleartextIsRefused(t *testing.T) {
	_, plain := startRelay(t, nil)
	tap := newWireTap(t, plain)

	// A TLS front that answers the websocket upgrade with a redirect to the cleartext
	// relay, which is what a compromised relay would do.
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(tap.URL(), "ws://", "http://", 1)+"/", http.StatusFound)
	}))
	t.Cleanup(front.Close)
	wss := strings.Replace(front.URL, "https://", "wss://", 1)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), relay.Security{PinnedCert: front.Certificate().Raw})
	if err == nil {
		_ = c.Close()
		t.Fatalf("DialSecure(wss://) followed a 302 to cleartext and returned a Client: the session runs unencrypted")
	}
	if !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("redirect-to-cleartext error: got %v, want ErrCleartextRefused", err)
	}
	if on := string(tap.Sent()); strings.Contains(on, "auth_init") {
		t.Fatalf("the relay-auth handshake ran in CLEARTEXT after the redirect (%d bytes observed on the plain hop)", len(on))
	}
}

// TestCleartext_RefusedWithoutTheExplicitOptIn asserts ws:// is refused by default
// even on loopback: the carve-out is opt-in, not ambient.
func TestCleartext_RefusedWithoutTheExplicitOptIn(t *testing.T) {
	_, ws := startRelay(t, nil)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), ws, authFor(pub, priv), relay.Security{})
	if err == nil {
		_ = c.Close()
		t.Fatalf("DialSecure accepted cleartext ws:// without AllowLoopbackCleartext")
	}
	if !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("cleartext error: got %v, want ErrCleartextRefused", err)
	}
}

// TestCleartext_LoopbackCarveOutIsNarrow asserts the carve-out is scoped to
// loopback: the same opt-in against a routable host is still refused, so the flag
// can never turn into "cleartext to the internet".
func TestCleartext_LoopbackCarveOutIsNarrow(t *testing.T) {
	pub, priv := newRelayAuthKey(t)
	sec := relay.Security{AllowLoopbackCleartext: true}

	for _, target := range []string{
		"ws://relay.example.com:8080/",
		"ws://10.0.0.7:8080/",
		"ws://[2001:db8::1]:8080/",
	} {
		c, err := relay.DialSecure(testCtx(t), target, authFor(pub, priv), sec)
		if err == nil {
			_ = c.Close()
			t.Fatalf("%s: AllowLoopbackCleartext admitted a NON-loopback cleartext relay", target)
		}
		if !errors.Is(err, relay.ErrCleartextRefused) {
			t.Fatalf("%s: got %v, want ErrCleartextRefused", target, err)
		}
	}
}

// TestCleartext_LoopbackCarveOutWorksForTheInProcessRelay asserts the carve-out
// actually carves: the in-process ws:// relay that PB-NET-1 and PB-E2E-1 depend on
// is reachable when the opt-in is set and the host is loopback.
func TestCleartext_LoopbackCarveOutWorksForTheInProcessRelay(t *testing.T) {
	_, ws := startRelay(t, nil)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), ws, authFor(pub, priv), relay.Security{AllowLoopbackCleartext: true})
	if err != nil {
		t.Fatalf("loopback cleartext carve-out refused the in-process relay: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.RoutingID() != relay.RoutingID(pub) {
		t.Fatalf("routing id mismatch after cleartext loopback dial")
	}
}

// releaseCheckProgram is a NON-test main package. It asks for the carve-out in the
// strongest possible way -- explicit opt-in, loopback literal host -- and reports
// whether the policy still refused. The dial target is a dead port so the answer
// can only be the policy decision, which pins a second property: the cleartext
// check happens BEFORE any network attempt.
const releaseCheckProgram = `package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sec := relay.Security{AllowLoopbackCleartext: true}
	_, err := relay.DialSecure(ctx, "ws://127.0.0.1:1/", relay.ClientAuth{}, sec)
	if errors.Is(err, relay.ErrCleartextRefused) {
		fmt.Print("REFUSED")
		return
	}
	fmt.Printf("ADMITTED err=%v", err)
}
`

// TestCleartext_CarveOutCannotBeEnabledInAReleaseBuild is the requirement's teeth.
// It COMPILES AND RUNS a plain (non-test) binary -- exactly what `go build ./...`
// ships -- and asserts the loopback carve-out is inert there, while the identical
// call inside this test binary is admitted.
//
// The assertion is deliberately mechanism-agnostic: it does not care whether the
// implementation keys the carve-out on testing.Testing(), a build tag, or anything
// else, only that a normally-built binary CANNOT turn it on. Note that a build tag
// alone cannot satisfy this, since the tag would have to be absent from the release
// build and present in `go test ./...`, which passes no tags.
func TestCleartext_CarveOutCannotBeEnabledInAReleaseBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	// Same call, inside a test binary: the carve-out is live, so the failure must be
	// the dead port, not the policy.
	sec := relay.Security{AllowLoopbackCleartext: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := relay.DialSecure(ctx, "ws://127.0.0.1:1/", relay.ClientAuth{}, sec); errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("the carve-out is inert inside a test binary, so the in-process relay tests cannot work: %v", err)
	}

	bin := buildReleaseProbe(t, releaseCheckProgram)

	if got := runReleaseProbe(t, bin); got != "REFUSED" {
		t.Fatalf("a release build admitted the loopback cleartext carve-out: %s", got)
	}
}

// TestTrustRootSourceIsStatedPerPlatform asserts the Android trust-root question is
// ANSWERED in code rather than left to x509.SystemCertPool by default. On Android
// the system pool is not the desktop pool: the client must either carry an embedded
// CA bundle or refuse to verify at all and require a pin.
func TestTrustRootSourceIsStatedPerPlatform(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if got := relay.TrustRootSourceFor(goos); got != relay.TrustRootsSystem {
			t.Errorf("TrustRootSourceFor(%q) = %q, want %q", goos, got, relay.TrustRootsSystem)
		}
	}

	android := relay.TrustRootSourceFor("android")
	switch android {
	case relay.TrustRootsSystem:
		t.Fatalf("android trust roots are declared as %q: x509.SystemCertPool is not usable on Android as it is on desktop (opus H3)", android)
	case relay.TrustRootsEmbedded:
		if len(relay.EmbeddedTrustRoots()) == 0 {
			t.Fatalf("android declares an embedded trust bundle but EmbeddedTrustRoots() is empty")
		}
	case relay.TrustRootsPinned:
		if len(relay.EmbeddedTrustRoots()) != 0 {
			t.Fatalf("android declares pinning-only trust but still ships an embedded bundle; the source must be unambiguous")
		}
	default:
		t.Fatalf("android trust-root source %q is not one of the stated sources", android)
	}
}

// TestRelayClientCompilesForAndroid guards the declaration above against the case
// where the android answer exists only in a comment: the package the handset links
// must actually build for the handset's platform.
func TestRelayClientCompilesForAndroid(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	cmd := exec.Command("go", "build", "./internal/remote/relay")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("relay client does not build for android/arm64: %v\n%s", err, out)
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
