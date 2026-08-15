package swarmmobile

// ADR-016 W4/W9 migration ladder (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5):
// "A pinned client migrates only on advertise + prove + commit; failure retains the pin
// and offers a repair path, and never disables validation."
//
// applyRelayTLSPolicy is the phone-side state machine over ONE reconcile's published
// schema.RemoteProfileV1, and its four cases are the ladder's rungs:
//
//   - RelayTLSPolicy == ""            : no advertisement at all -- an old machine build, or
//     a reconcile this ADR's own R1 sibling fields have not reached yet. NO-OP: this is
//     BOTH "old machine (no profile) leaves the phone exactly as it is" (W9) AND "downgrade
//     ... does not un-migrate silently" (W9) -- the same mechanism covers a
//     never-migrated phone and an already-migrated one, because in neither case may an
//     ABSENT claim be read as an authenticated one.
//   - RelayTLSPolicy == "pinned_spki" : B54's reverse direction -- adopted VERBATIM,
//     unconditionally, no probe needed (reverting to the expert policy proves nothing).
//   - RelayTLSPolicy == "webpki", host mismatch : refused as a re-pairing question
//     (W4 step 2, "stale_profile"), never a TLS migration.
//   - RelayTLSPolicy == "webpki", host matches   : PROVE via the injected probe, then
//     COMMIT only on success; failure retains the phone's current policy untouched.
//
// THE PROBE IS INJECTED rather than built from a live relay.DialSecure call inside this
// function, deliberately: proving "the probe itself enforces webpki correctly" is W2's own
// contract (internal/remote/relay/r2_adr016_w2_test.go, and TrustRootsPlatformDelegate's
// real dial-time behaviour). This file's contract is the STATE MACHINE around that result
// -- advertise, check destination, commit-or-retain -- which is what the Conformance
// table's "Downgrade attempt" and "N / N-1" rows are actually about. The production
// caller wires probe to a real webpki dial (W2's TrustRootsPlatformDelegate / Go's
// VerifyHostname) on a connection separate from the live pinned one (W4 step 3).

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func w9App(t *testing.T, relayURL string) *App {
	t.Helper()
	core, err := phonecore.Resume(phonecore.Config{})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	a := &App{core: core, events: newDispatcher(), relayURL: relayURL}
	t.Cleanup(a.events.close)
	return a
}

func w9Seed(t *testing.T, a *App, policy string, pin []byte) {
	t.Helper()
	if err := a.core.Mutate(func(st *phonecore.State) {
		st.Machine = "m1"
		st.RelayTLSPolicy = policy
		st.RelaySPKIPin = pin
	}); err != nil {
		t.Fatalf("seed Mutate: %v", err)
	}
}

func neverCalledProbe(t *testing.T) func(context.Context, string) error {
	return func(context.Context, string) error {
		t.Fatalf("the probe was called; this case must not attempt to prove anything")
		return nil
	}
}

// TestADR016W9_NoAdvertisementLeavesAPinnedPhoneUnchanged: "old machine (no profile)
// leaves the phone exactly as it is."
func TestADR016W9_NoAdvertisementLeavesAPinnedPhoneUnchanged(t *testing.T) {
	pin := []byte("32-byte-sha256-digest-of-spki!!")
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "pinned_spki", pin)

	if err := a.applyRelayTLSPolicy(context.Background(), schema.RemoteProfileV1{}, neverCalledProbe(t)); err != nil {
		t.Fatalf("applyRelayTLSPolicy with no advertisement: %v", err)
	}
	got := a.core.State()
	if got.RelayTLSPolicy != "pinned_spki" || string(got.RelaySPKIPin) != string(pin) {
		t.Fatalf("state changed on an absent advertisement: %+v", got)
	}
}

// TestADR016W9_NoAdvertisementDoesNotUnmigrateAWebPKIPhone: "downgrade (webpki machine
// profile withdrawn) does not un-migrate silently" -- the SAME no-op rule, from the other
// starting state.
func TestADR016W9_NoAdvertisementDoesNotUnmigrateAWebPKIPhone(t *testing.T) {
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "webpki", nil)

	if err := a.applyRelayTLSPolicy(context.Background(), schema.RemoteProfileV1{}, neverCalledProbe(t)); err != nil {
		t.Fatalf("applyRelayTLSPolicy with no advertisement: %v", err)
	}
	if got := a.core.State().RelayTLSPolicy; got != "webpki" {
		t.Fatalf("an absent advertisement changed RelayTLSPolicy to %q; W9 forbids reading absence "+
			"as an authenticated pinned_spki claim", got)
	}
}

// TestADR016W9_SuccessfulProbeCommitsWebPKIAndRetainsThePin is the ladder's happy path:
// advertise (webpki, matching host) + prove (probe succeeds) + commit. W4.4: the commit
// does not clear the pin -- it is retained, not consulted (W3's own scoping, tested
// separately).
func TestADR016W9_SuccessfulProbeCommitsWebPKIAndRetainsThePin(t *testing.T) {
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "pinned_spki", nil)

	profile := schema.RemoteProfileV1{
		RelayTLSPolicy: "webpki",
		RelayHost:      "swarm-relay.example.com",
		RelaySPKIPin:   []byte("compat-pin-published-during-the-window!"),
	}
	probed := false
	probe := func(ctx context.Context, host string) error {
		probed = true
		if host != "swarm-relay.example.com" {
			t.Errorf("probe host = %q, want %q", host, "swarm-relay.example.com")
		}
		return nil
	}
	if err := a.applyRelayTLSPolicy(context.Background(), profile, probe); err != nil {
		t.Fatalf("applyRelayTLSPolicy: %v", err)
	}
	if !probed {
		t.Fatalf("the probe was never called; the ladder must PROVE before it commits")
	}
	got := a.core.State()
	if got.RelayTLSPolicy != "webpki" {
		t.Fatalf("RelayTLSPolicy = %q after a successful probe, want %q", got.RelayTLSPolicy, "webpki")
	}
	if string(got.RelaySPKIPin) != string(profile.RelaySPKIPin) {
		t.Fatalf("RelaySPKIPin = %x after commit, want %x (B54 verbatim, W4.4 retained)",
			got.RelaySPKIPin, profile.RelaySPKIPin)
	}
}

// TestADR016W9_FailedProbeRetainsThePinAndSurfacesTheFailure is W4 step 5: "Any failure
// leaves the phone on pinned_spki ... keeps the working connection up ... never disables
// verification." A phone that was already on pinned_spki must simply STAY there, with an
// error the caller can turn into the webpki_unavailable repair state.
func TestADR016W9_FailedProbeRetainsThePinAndSurfacesTheFailure(t *testing.T) {
	pin := []byte("32-byte-sha256-digest-of-spki!!")
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "pinned_spki", pin)

	profile := schema.RemoteProfileV1{RelayTLSPolicy: "webpki", RelayHost: "swarm-relay.example.com"}
	probeErr := errors.New("relay_name_mismatch")
	probe := func(context.Context, string) error { return probeErr }

	err := a.applyRelayTLSPolicy(context.Background(), profile, probe)
	if err == nil {
		t.Fatalf("applyRelayTLSPolicy returned nil after a failed probe")
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("applyRelayTLSPolicy error = %v, want it to wrap the probe's own error so the "+
			"repair state can name the cause (W4 step 5)", err)
	}
	got := a.core.State()
	if got.RelayTLSPolicy != "pinned_spki" {
		t.Fatalf("RelayTLSPolicy = %q after a FAILED probe, want %q retained (never disable "+
			"validation on failure)", got.RelayTLSPolicy, "pinned_spki")
	}
	if string(got.RelaySPKIPin) != string(pin) {
		t.Fatalf("RelaySPKIPin = %x after a failed probe, want the original %x still in force",
			got.RelaySPKIPin, pin)
	}
}

// TestADR016W9_HostMismatchIsRefusedAsStaleProfileWithoutProbing is W4 step 2: "A profile
// that changes the destination is not a TLS migration; it is a re-pairing question and is
// refused as stale_profile." The probe must not even run: proving a DIFFERENT host tells
// the phone nothing about whether ITS relay is trustworthy.
func TestADR016W9_HostMismatchIsRefusedAsStaleProfileWithoutProbing(t *testing.T) {
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "pinned_spki", nil)

	profile := schema.RemoteProfileV1{RelayTLSPolicy: "webpki", RelayHost: "a-different-relay.example.com"}
	err := a.applyRelayTLSPolicy(context.Background(), profile, neverCalledProbe(t))
	if err == nil {
		t.Fatalf("applyRelayTLSPolicy admitted a profile naming a different relay host")
	}
	if got := a.core.State().RelayTLSPolicy; got != "pinned_spki" {
		t.Fatalf("RelayTLSPolicy = %q after a stale-profile refusal, want unchanged", got)
	}
}

// TestADR016W9_MachineRollbackToPinnedSPKIIsAdoptedVerbatimWithoutAProbe is W4's reverse
// direction: "a machine that reverts to pinned_spki republishes the policy and the pin
// set, and the phone adopts it under B54's verbatim rule." No probe is needed to go back
// to the STRONGER policy.
func TestADR016W9_MachineRollbackToPinnedSPKIIsAdoptedVerbatimWithoutAProbe(t *testing.T) {
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "webpki", []byte("stale-compat-pin"))

	newPin := []byte("32-byte-sha256-digest-of-spki!!")
	profile := schema.RemoteProfileV1{RelayTLSPolicy: "pinned_spki", RelayHost: "swarm-relay.example.com", RelaySPKIPin: newPin}
	if err := a.applyRelayTLSPolicy(context.Background(), profile, neverCalledProbe(t)); err != nil {
		t.Fatalf("applyRelayTLSPolicy (rollback): %v", err)
	}
	got := a.core.State()
	if got.RelayTLSPolicy != "pinned_spki" {
		t.Fatalf("RelayTLSPolicy = %q after a machine rollback, want %q", got.RelayTLSPolicy, "pinned_spki")
	}
	if string(got.RelaySPKIPin) != string(newPin) {
		t.Fatalf("RelaySPKIPin = %x after rollback, want the newly-published %x (B54 verbatim)",
			got.RelaySPKIPin, newPin)
	}
}

// TestADR016W9_APinnedSPKIAdvertisementWithNoPinIsRefusedNotAdopted is the reviewer-named
// fence: W1's own rule makes "pinned_spki, no pin" impossible from a legitimate machine
// (--relay-pin is MANDATORY under pinned_spki), so a phone that adopted it anyway would wipe
// a WORKING pin on the strength of a claim its own policy cannot satisfy -- ErrPinRequired
// forever on Android, ADR-016 W9's own brick, reached with no probe and no guard.
func TestADR016W9_APinnedSPKIAdvertisementWithNoPinIsRefusedNotAdopted(t *testing.T) {
	pin := []byte("32-byte-sha256-digest-of-spki!!")
	a := w9App(t, "wss://swarm-relay.example.com:8443")
	w9Seed(t, a, "pinned_spki", pin)

	profile := schema.RemoteProfileV1{RelayTLSPolicy: "pinned_spki", RelayHost: "swarm-relay.example.com"}
	err := a.applyRelayTLSPolicy(context.Background(), profile, neverCalledProbe(t))
	if err == nil {
		t.Fatalf("applyRelayTLSPolicy admitted a pinned_spki advertisement with no pin")
	}
	got := a.core.State()
	if got.RelayTLSPolicy != "pinned_spki" || string(got.RelaySPKIPin) != string(pin) {
		t.Fatalf("state after refusal = policy %q pin %x; want the working pin UNCHANGED (%q, %x)",
			got.RelayTLSPolicy, got.RelaySPKIPin, "pinned_spki", pin)
	}
}

// TestADR016W9_AnUnparsableRelayURLRefusesTheMigrationRatherThanSkippingTheHostCheck is W4
// step 2 applied to the phone's OWN destination: relayURLHost returning "" for a relay URL
// this phone cannot parse must not silently disable the host check and prove whatever host
// the profile names -- it must refuse, the same as a genuine mismatch does.
func TestADR016W9_AnUnparsableRelayURLRefusesTheMigrationRatherThanSkippingTheHostCheck(t *testing.T) {
	a := w9App(t, "://not-a-url")
	w9Seed(t, a, "pinned_spki", nil)

	profile := schema.RemoteProfileV1{RelayTLSPolicy: "webpki", RelayHost: "swarm-relay.example.com"}
	err := a.applyRelayTLSPolicy(context.Background(), profile, neverCalledProbe(t))
	if err == nil {
		t.Fatalf("applyRelayTLSPolicy proved a host this phone could not name its own destination against")
	}
	if got := a.core.State().RelayTLSPolicy; got != "pinned_spki" {
		t.Fatalf("RelayTLSPolicy = %q after the refusal, want unchanged", got)
	}
}
