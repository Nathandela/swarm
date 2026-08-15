package swarmmobile

// The webpki punch list's two remaining Go-side findings, reviewed against main:
//
//   - MEDIUM W4.5 surfacing: adoptReconcile used to discard applyRelayTLSPolicy's own
//     return value outright (`_ = a.applyRelayTLSPolicy(...)`). W4 step 5 requires a failed
//     migration to "surface webpki_unavailable with the operator-facing cause", and nothing
//     did. reportWebPKIUnavailable (mobile/relay.go) is the fix, in exactly the
//     pull-surface-plus-deduped-event shape reportSkew already gives PB-TIME-1's
//     ClockVerdict.
//   - MEDIUM relay_trust_unavailable: a handset that migrated to webpki and then starts
//     with no RelayTrust installed (PhoneRuntime.installRelayTrust swallows every
//     exception it can raise, by design) resolved to relay.ErrPinRequired and was
//     classified identically to a genuinely untrusted relay -- connRelayUntrusted, "not the
//     relay your machine published" -- which W8 forbids by name for an app fault.
//     relayTrustUnavailable (mobile/relaytrust.go) tells the two apart.
//
// Both are exercised directly against the small surface each fix actually added, rather
// than through the full adoptReconcile -> Core.Reconcile -> Core.LastProfile pipeline: that
// needs a sealed reconcile record and phonecore's own internal test fixtures
// (newRollbackFixture, sealFrameFrom), which are unexported from that package. adoptReconcile's
// own wiring is the two-line call `a.reportWebPKIUnavailable(a.applyRelayTLSPolicy(...))`,
// verifiable by inspection; what these tests prove is the surface and the classifier
// themselves, each driven by a REAL error the production code they wrap actually produces
// (applyRelayTLSPolicy's own wrapped probe failure; relay.ErrPinRequired, the real sentinel
// handsetSecurity's pinning-only floor returns).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// w4dot5DrainEvents pops every event currently queued on the dispatcher, in order, and
// clears the queue. Internal test file (package swarmmobile): reads the dispatcher's
// unexported queue directly rather than standing up a real EventListener and its delivery
// goroutine, the same shortcut the rest of this package's white-box tests take.
func w4dot5DrainEvents(d *dispatcher) []*Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]*Event(nil), d.queue...)
	d.queue = nil
	return out
}

// TestADR016W4dot5_FailedProbeSurfacesWebPKIUnavailableWithTheCause is W4 step 5's own
// words, end to end from a real applyRelayTLSPolicy failure through to the surface:
// reportWebPKIUnavailable must record the classed cause on both the pull surface and the
// event.
func TestADR016W4dot5_FailedProbeSurfacesWebPKIUnavailableWithTheCause(t *testing.T) {
	pin := []byte("32-byte-sha256-digest-of-spki!!")
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "pinned_spki", pin)

	profile := schema.RemoteProfileV1{RelayTLSPolicy: "webpki", RelayHost: "swarm-relay.example.com"}
	probeErr := errors.New("relay_name_mismatch")
	probe := func(context.Context, string) error { return probeErr }

	err := a.applyRelayTLSPolicy(context.Background(), profile, probe)
	if err == nil {
		t.Fatalf("applyRelayTLSPolicy returned nil after a failed probe; test setup is wrong")
	}
	a.reportWebPKIUnavailable(err)

	a.mu.Lock()
	got := a.webpkiUnavailable
	a.mu.Unlock()
	if got == "" || !strings.Contains(got, "relay_name_mismatch") {
		t.Fatalf("webpkiUnavailable = %q, want it to carry the probe's own cause", got)
	}

	events := w4dot5DrainEvents(a.events)
	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1", len(events))
	}
	if events[0].Kind != "connection" || events[0].State != "webpki_unavailable" {
		t.Fatalf("event = %+v, want Kind=connection State=webpki_unavailable", events[0])
	}
	if !strings.Contains(events[0].Message, "relay_name_mismatch") {
		t.Fatalf("event.Message = %q, want it to carry the probe's own cause", events[0].Message)
	}
}

// TestADR016W4dot5_ASuccessfulReconcileClearsAPriorWebPKIUnavailableVerdict is reportSkew's
// own "emits on both transitions" rule, applied here: a screen that latched the failure
// event must not go on telling the user the migration is broken once it recovers.
func TestADR016W4dot5_ASuccessfulReconcileClearsAPriorWebPKIUnavailableVerdict(t *testing.T) {
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	a.reportWebPKIUnavailable(errors.New("stale failure"))
	w4dot5DrainEvents(a.events) // the failure event itself is not this test's assertion

	a.reportWebPKIUnavailable(nil)

	a.mu.Lock()
	got := a.webpkiUnavailable
	a.mu.Unlock()
	if got != "" {
		t.Fatalf("webpkiUnavailable = %q after a successful reconcile, want cleared", got)
	}
	events := w4dot5DrainEvents(a.events)
	if len(events) != 1 || events[0].State != "available" {
		t.Fatalf("events = %+v, want exactly one clearing event", events)
	}
}

// TestADR016W4dot5_RepeatedIdenticalFailuresDoNotSpamEvents is the reason reportSkew dedupes
// in the first place, applied here: a machine stuck on a failing probe reconciles
// repeatedly, and only the FIRST occurrence of a given cause may raise an event.
func TestADR016W4dot5_RepeatedIdenticalFailuresDoNotSpamEvents(t *testing.T) {
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	cause := errors.New("relay_name_mismatch")
	a.reportWebPKIUnavailable(cause)
	w4dot5DrainEvents(a.events)

	a.reportWebPKIUnavailable(cause) // the SAME cause again

	if events := w4dot5DrainEvents(a.events); len(events) != 0 {
		t.Fatalf("got %d events for an unchanged verdict, want 0 (dedup, same reason reportSkew dedupes)", len(events))
	}
}

// TestADR016W8_RelayTrustUnavailableStaysDistinctFromRelayUntrusted is the webpki punch
// list's relay_trust_unavailable finding: relay.ErrPinRequired reaches handsetSecurity's
// pinning-only floor for TWO different reasons that must not collapse into one state.
func TestADR016W8_RelayTrustUnavailableStaysDistinctFromRelayUntrusted(t *testing.T) {
	// Case 1: webpki policy, no platform delegate installed at all -- PhoneRuntime never
	// called SetRelayTrust (its own installRelayTrust swallowed whatever stopped it). This
	// is an APP FAULT: the phone has nothing wrong with its pairing, nothing wrong with the
	// relay, and no security question to raise.
	webpkiApp := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, webpkiApp, "webpki", nil)
	if !webpkiApp.relayTrustUnavailable(relay.ErrPinRequired) {
		t.Error("a webpki phone with no platform delegate installed must classify " +
			"ErrPinRequired as relay_trust_unavailable, not relay_untrusted")
	}

	// Case 2: pinned_spki policy, genuinely no pin configured -- handsetSecurity's own
	// documented residual, and a REAL security-adjacent state ("not the relay your machine
	// published"). This must keep classifying as relay_untrusted.
	pinnedApp := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, pinnedApp, "pinned_spki", nil)
	if pinnedApp.relayTrustUnavailable(relay.ErrPinRequired) {
		t.Error("a pinned_spki phone with genuinely no pin must NOT classify as " +
			"relay_trust_unavailable -- that is a real relay_untrusted verdict")
	}

	// Case 3: a platform delegate IS installed. ErrPinRequired should not be reachable via
	// this path in production once a delegate exists (WithPlatformVerifier selects a
	// different trust-root source entirely), but the classifier must not over-fire if it
	// somehow is -- over-firing would hide a genuine relay_untrusted verdict behind the
	// app-fault wording.
	delegateApp := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, delegateApp, "webpki", nil)
	if err := delegateApp.SetRelayTrust(alwaysTrustingRelayTrust{}); err != nil {
		t.Fatalf("SetRelayTrust: %v", err)
	}
	if delegateApp.relayTrustUnavailable(relay.ErrPinRequired) {
		t.Error("a phone with a platform delegate installed must not classify " +
			"ErrPinRequired as relay_trust_unavailable")
	}

	// Case 4: the Kotlin-stamped token itself, as it reaches Go wrapped inside whatever the
	// TLS handshake failure became -- classifies regardless of this phone's own policy or
	// delegate state, because the token is Kotlin's own authoritative verdict.
	tokenErr := fmt.Errorf("tls: failed to verify certificate: %s: no default trust manager", RelayTrustUnavailable)
	if !pinnedApp.relayTrustUnavailable(tokenErr) {
		t.Error("an error carrying the RelayTrustUnavailable token must classify as " +
			"relay_trust_unavailable regardless of this phone's own policy")
	}
}

// TestADR016W8_UntrustedTokenWinsOverAnAttackerEmbeddedUnavailableToken is the
// re-verification pass's LOW finding: RelayTrust.kt stamps its verdict as a PREFIX
// (RelayTrustUntrusted or RelayTrustUnavailable) but wraps peer-controlled text alongside
// it -- a path-validation failure's message carries the presented certificate's own DN, and
// the SANs are interpolated verbatim (RelayTrust.kt). relayTrustUnavailable used to match
// RelayTrustUnavailable anywhere in the wrapped string, so a hostile relay could embed that
// literal token inside its own certificate's DN or a SAN and flip a genuine
// RelayTrustUntrusted verdict into this app-fault classification, substituting W8's
// security-accusation wording for a wording that hides one. The untrusted token must win
// when both appear in the same wrapped error.
func TestADR016W8_UntrustedTokenWinsOverAnAttackerEmbeddedUnavailableToken(t *testing.T) {
	pinnedApp := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, pinnedApp, "pinned_spki", nil)

	// Kotlin's OWN verdict is RelayTrustUntrusted (a real security rejection), stamped as
	// the prefix; the certificate's attacker-controlled DN, wrapped alongside it, happens to
	// contain the RelayTrustUnavailable token's literal text.
	hostileErr := fmt.Errorf("%s: PKIX path validation failed: presented certificate DN="+
		"CN=%s", RelayTrustUntrusted, RelayTrustUnavailable)
	if pinnedApp.relayTrustUnavailable(hostileErr) {
		t.Error("a hostile relay embedding the RelayTrustUnavailable token inside its own " +
			"certificate must not flip a genuine RelayTrustUntrusted verdict into the " +
			"relay_trust_unavailable app-fault classification")
	}
}
