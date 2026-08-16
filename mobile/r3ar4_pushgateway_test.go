package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) for Wave R3 ROUND 4, the ZERO-PRODUCTION-CALLERS finding:
// the ADR-015 gateway client, the installation identity and the wake-drop diagnostics
// existed and were driven only by tests. The orchestrator's scope ruling wires what needs no
// owner-gated external:
//
//   - the app lifecycle must actually CALL registration (Kotlin's two token entry points),
//     which needs a bound verb and a real gateway URL on Config -- the same config surface
//     the relay URL crosses on;
//   - Play Integrity attestation stays PARKED (owner console setup), so the honest
//     production behaviour is a NAMED, LOUD refusal that enrolls nothing -- never a fake
//     verdict token, and never a silent success.
//
// These tests pin the two ends of that: an unconfigured build says so and does nothing, and
// a configured build refuses with the parked reason and writes no durable identity.

import (
	"crypto/sha256"
	"strings"
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// r3ar4Custody is the two-tier KEK seam, deterministic per tier.
type r3ar4Custody struct{}

func (r3ar4Custody) WakeKEK() ([]byte, error)    { return r3ar4KEK("wake"), nil }
func (r3ar4Custody) ContentKEK() ([]byte, error) { return r3ar4KEK("content"), nil }

func r3ar4KEK(tier string) []byte {
	sum := sha256.Sum256([]byte("r3ar4-kek-" + tier))
	return sum[:]
}

func r3ar4App(t *testing.T, gatewayURL string) *swarmmobile.App {
	t.Helper()
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir:       t.TempDir(),
		PushGatewayURL: gatewayURL,
	}, r3ar4Custody{})
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// TestR3AR4_EnsurePushRegistration_SaysSoWhenNoGatewayIsConfigured: a build with no gateway
// endpoint is honestly foreground-only. It must not panic, must not enroll, and must say
// which decision is missing -- the same "graceful AND loud" shape PB-PUSH-5 asks of the
// absent Firebase project.
func TestR3AR4_EnsurePushRegistration_SaysSoWhenNoGatewayIsConfigured(t *testing.T) {
	app := r3ar4App(t, "")
	err := app.EnsurePushRegistration("fcm-token-alpha")
	if err == nil {
		t.Fatal("an unconfigured push gateway reported a successful registration")
	}
	if !strings.Contains(err.Error(), "push gateway") {
		t.Errorf("err = %v, want a message naming the missing push gateway configuration", err)
	}
}

// TestR3AR4_EnsurePushRegistration_RefusesLoudlyWhileAttestationIsParked: with a gateway URL
// configured, the verb runs for real and stops at the ONE external this repository cannot
// provide -- the Play Integrity verdict, whose setup is owner-console work (parked). The
// refusal must NAME that, and it must leave no half-enrolled durable identity (PG-AUTH-13's
// own rule for a refused attestation).
func TestR3AR4_EnsurePushRegistration_RefusesLoudlyWhileAttestationIsParked(t *testing.T) {
	app := r3ar4App(t, "https://push.invalid")
	err := app.EnsurePushRegistration("fcm-token-alpha")
	if err == nil {
		t.Fatal("registration succeeded with no Play Integrity attestation available")
	}
	if !strings.Contains(err.Error(), "attestation") {
		t.Errorf("err = %v, want a message naming the parked attestation seam", err)
	}
	// That nothing durable is enrolled by a refused attestation is PG-AUTH-13's own rule and
	// is pinned where it is owned, against the REAL gateway
	// (phonecore's TestR3A_Register_ARefusedAttestationEnrollsNothing). Restating it through
	// a bound accessor would add a verb no screen asks for -- the defect class the bound-verb
	// ledger exists to catch -- to re-check a property one layer down already holds.
}

// TestR3AR4_WakeDropCounts_AreReachableFromTheApp is the operator-reachable half of the
// skew-diagnosability finding: a machine whose clock runs ahead has 100% of its wakes
// refused, and the app must be able to say that rather than only report a total.
func TestR3AR4_WakeDropCounts_AreReachableFromTheApp(t *testing.T) {
	app := r3ar4App(t, "")
	counts, err := app.WakeDropCounts()
	if err != nil {
		t.Fatalf("App.WakeDropCounts: %v", err)
	}
	if counts == nil {
		t.Fatal("App.WakeDropCounts returned nil")
	}
	if counts.Total != 0 || counts.PeerClockAhead != 0 {
		t.Errorf("a fresh phone reports Total=%d PeerClockAhead=%d, want zeroes",
			counts.Total, counts.PeerClockAhead)
	}
}
