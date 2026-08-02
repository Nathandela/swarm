// ADR-007 B45 and residual 1.9 (FAILING FIRST): the pinning-only refusal, EXECUTED.
//
// The branch these tests reach had never run. relay.Security.tlsConfig switches on
// runtime.GOOS, the suite runs on a desktop, and the only test naming ErrPinRequired asserts
// its MESSAGE TEXT. So the fail-closed behaviour that residual 1.9's entire resolution rests
// on -- "an unpinned handset refuses rather than dialling unverified" -- was reasoned from
// source and never measured, which is the defect class this phase has spent itself finding.
//
// relay.WithTrustRootSource makes the branch reachable inside a test binary and nowhere else.
package relay_test

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestPBNET2_APinningOnlyPlatformRefusesAnUnpinnedDial is residual 1.9's resolution, executed.
// The refusal is decided before any packet, so the dead port never matters.
func TestPBNET2_APinningOnlyPlatformRefusesAnUnpinnedDial(t *testing.T) {
	wss, _ := startTLSRelay(t)
	pub, priv := newRelayAuthKey(t)

	sec := relay.WithTrustRootSource(relay.Security{}, relay.TrustRootsPinned)
	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), sec)
	if err == nil {
		_ = c.Close()
		t.Fatalf("a pinning-only platform dialed a relay with NO pin configured. That is the " +
			"unverified fallback residual 1.9 exists to forbid")
	}
	if !errors.Is(err, relay.ErrPinRequired) {
		t.Fatalf("unpinned dial on a pinning-only platform: got %v, want ErrPinRequired", err)
	}
}

// TestPBNET2_APinningOnlyPlatformAcceptsThePinnedRelay is the other half: the refusal above is
// the ABSENCE of a pin and not a platform that can never connect. Without this, an
// implementation that refused every dial on Android would pass the test above and ship a
// handset that reaches nothing.
func TestPBNET2_APinningOnlyPlatformAcceptsThePinnedRelay(t *testing.T) {
	wss, der := startTLSRelay(t)
	pub, priv := newRelayAuthKey(t)

	sec := relay.WithTrustRootSource(relay.Security{PinnedCert: der}, relay.TrustRootsPinned)
	c, err := relay.DialSecure(testCtx(t), wss, authFor(pub, priv), sec)
	if err != nil {
		t.Fatalf("a pinning-only platform refused the relay whose certificate it pinned: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.RoutingID() != relay.RoutingID(pub) {
		t.Fatalf("routing id mismatch after a pinned dial on a pinning-only platform")
	}
}

// TestB45_ThePairingPolicyCanBootstrapOnAPinningOnlyPlatform is the deadlock B45 resolves,
// demonstrated rather than argued: the SAME platform and the SAME unpinned state that the
// first test proves is refused, is admitted for the pairing dial -- which is the only way the
// pin it would have checked can ever reach the phone.
func TestB45_ThePairingPolicyCanBootstrapOnAPinningOnlyPlatform(t *testing.T) {
	wss, _ := startTLSRelay(t)

	sec := relay.WithTrustRootSource(relay.PairingSecurity(), relay.TrustRootsPinned)
	conn, err := relay.DialRawSecure(testCtx(t), wss, sec)
	if err != nil {
		t.Fatalf("the pairing dial was refused on a pinning-only platform: %v.\n"+
			"That is ADR-007 B45's deadlock: the dial that FETCHES the pin cannot itself be "+
			"pinned, so refusing it means the pin never arrives and the handset can never pair", err)
	}
	_ = conn.Close()
}

// TestB45_ThePairingPolicyStillRefusesCleartext asserts what B45 did NOT relax. The ruling is
// about certificate VERIFICATION on one dial; the cleartext ban is decided from the URL and is
// untouched, so a QR naming a routable ws:// relay is refused exactly as before.
func TestB45_ThePairingPolicyStillRefusesCleartext(t *testing.T) {
	for _, target := range []string{
		"ws://relay.example.com:8080/",
		"ws://10.0.0.7:8080/",
		"ws://localhost:8080/",
	} {
		if _, err := relay.DialRawSecure(testCtx(t), target, relay.PairingSecurity()); !errors.Is(err, relay.ErrCleartextRefused) {
			t.Errorf("%s: pairing policy returned %v, want ErrCleartextRefused", target, err)
		}
	}
}

// TestB45_APinOutranksTheUnverifiedFlag pins the precedence that keeps the ruling narrow: a
// policy that has something better to check must check it, so the unverified flag can only
// ever relax the DEFAULT and never an explicit pin. Without this, composing the two would
// silently downgrade a pinned dial.
func TestB45_APinOutranksTheUnverifiedFlag(t *testing.T) {
	wss, _ := startTLSRelay(t)
	other := selfSignedDER(t)

	sec := relay.PairingSecurity()
	sec.PinnedCert = other
	if _, err := relay.DialRawSecure(testCtx(t), wss, sec); !errors.Is(err, relay.ErrPinMismatch) {
		t.Fatalf("a pin composed with the unverified flag was ignored: got %v, want ErrPinMismatch", err)
	}
}

// trustRootReleaseProgram is a NON-test main package. It asks for the override in the
// strongest possible way and reports whether the release build honoured it.
const trustRootReleaseProgram = `package main

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
	// A desktop is TrustRootsSystem. If the override were honoured here, this unpinned
	// dial would be refused with ErrPinRequired instead of failing at the dead port.
	sec := relay.WithTrustRootSource(relay.Security{}, relay.TrustRootsPinned)
	_, err := relay.DialSecure(ctx, "wss://127.0.0.1:1/", relay.ClientAuth{}, sec)
	if errors.Is(err, relay.ErrPinRequired) {
		fmt.Print("HONOURED")
		return
	}
	fmt.Print("INERT")
}
`

// TestPBNET2_TheTrustRootOverrideIsInertInAReleaseBuild is the seam's own fence. The override
// exists so a never-executed branch can be executed; it must not become a way for a shipped
// binary to claim a platform it is not on.
func TestPBNET2_TheTrustRootOverrideIsInertInAReleaseBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	// The same call inside this test binary IS honoured, or the tests above prove nothing.
	sec := relay.WithTrustRootSource(relay.Security{}, relay.TrustRootsPinned)
	if _, err := relay.DialSecure(testCtx(t), "wss://127.0.0.1:1/", relay.ClientAuth{}, sec); !errors.Is(err, relay.ErrPinRequired) {
		t.Fatalf("the override is inert inside a test binary, so the pinning-only tests above "+
			"are not exercising the branch they claim: %v", err)
	}

	if got := runReleaseProbe(t, buildReleaseProbe(t, trustRootReleaseProgram)); got != "INERT" {
		t.Fatalf("a release build honoured the trust-root override (%s); it could then claim a "+
			"platform it is not on, which is the opposite of what this seam is for", got)
	}
}
