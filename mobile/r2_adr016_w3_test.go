package swarmmobile

// ADR-016 W3 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "a pin is consulted
// if and only if the effective relay TLS policy is pinned_spki... Under webpki, a
// published pin is stored and not consulted: not by DeviceVerifyFunc on the pairing dial,
// not by tlsConfig on the session dial, not by anything."
//
// THE MECHANISM, per the Blast radius invariant list: "implemented by never populating
// Security.PinnedSPKISHA256 from a webpki profile -- not by teaching tlsConfig to ignore a
// pin it was handed." checkRelayPin's own signature and fence
// (TestB48_CheckRelayPin, mobile/b48_relaypin_test.go:14) are UNTOUCHED by this file --
// Conformance names it explicitly as a fence that must stay green. The scoping therefore
// lives in the CALLER: effectiveRelayPin decides what byte slice checkRelayPin ever sees,
// and handsetSecurity decides what byte slice ever reaches Security.PinnedSPKISHA256.
//
// Internal test file (package swarmmobile) because handsetSecurity is unexported, the
// same access pattern nx444_freshness_test.go and relayunreachable_test.go already use.

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestADR016W3_EffectiveRelayPinIsScopedToPinnedSPKI is the pairing-dial half:
// effectiveRelayPin(m) must return m.RelaySPKIPin verbatim when the machine's advertised
// policy is pinned_spki (or unset -- a legacy machine payload, today's behaviour exactly),
// and MUST return nil when the policy is webpki, however the pin field itself reads --
// "a deliberately wrong published pin changes no outcome" under webpki (Conformance table).
func TestADR016W3_EffectiveRelayPinIsScopedToPinnedSPKI(t *testing.T) {
	pin := bytes.Repeat([]byte{0xAB}, 32)

	for _, tc := range []struct {
		name   string
		policy string
		want   []byte
	}{
		{"pinned_spki: the pin is consulted", "pinned_spki", pin},
		{"legacy (no policy field): today's behaviour, the pin is consulted", "", pin},
		{"webpki: the pin is stored and NEVER consulted", "webpki", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pairing.MachinePayload{RelayTLSPolicy: tc.policy, RelaySPKIPin: pin}
			got := effectiveRelayPin(m)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("effectiveRelayPin(policy=%q) = %x, want %x", tc.policy, got, tc.want)
			}
		})
	}
}

// TestADR016W3_AWrongPinChangesNoOutcomeUnderWebPKI is the Conformance table's own case,
// stated directly rather than only through the scoping helper: feeding effectiveRelayPin's
// result into checkRelayPin (the untouched fence) with a DELIBERATELY WRONG presented
// certificate must still pass under webpki, and must still refuse under pinned_spki --
// proving the comparison was SCOPED and not merely made unreachable by accident.
func TestADR016W3_AWrongPinChangesNoOutcomeUnderWebPKI(t *testing.T) {
	machinePin := bytes.Repeat([]byte{0xAB}, 32)
	presented := bytes.Repeat([]byte{0xCD}, 32) // deliberately wrong

	webpki := pairing.MachinePayload{RelayTLSPolicy: "webpki", RelaySPKIPin: machinePin}
	if err := checkRelayPin(effectiveRelayPin(webpki), presented); err != nil {
		t.Fatalf("checkRelayPin under webpki with a wrong presented cert = %v, want nil "+
			"(the pin is not consulted at all under webpki)", err)
	}

	pinnedSPKI := pairing.MachinePayload{RelayTLSPolicy: "pinned_spki", RelaySPKIPin: machinePin}
	if err := checkRelayPin(effectiveRelayPin(pinnedSPKI), presented); err == nil {
		t.Fatalf("checkRelayPin under pinned_spki with a wrong presented cert = nil, want a refusal " +
			"(the comparison must still be the whole defense under this policy)")
	}
}

// TestADR016W3_HandsetSecurityConsultsThePinOnlyUnderPinnedSPKI is the SESSION-dial half:
// handsetSecurity() must never populate Security.PinnedSPKISHA256 from State.RelaySPKIPin
// when State.RelayTLSPolicy is webpki, exactly the Blast-radius mechanism sentence:
// "never populating Security.PinnedSPKISHA256 from a webpki profile".
func TestADR016W3_HandsetSecurityConsultsThePinOnlyUnderPinnedSPKI(t *testing.T) {
	pin := bytes.Repeat([]byte{0xAB}, 32)

	for _, tc := range []struct {
		name       string
		policy     string
		wantPinned bool
	}{
		{"pinned_spki", "pinned_spki", true},
		{"legacy (no policy stored)", "", true},
		{"webpki", "webpki", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, err := phonecore.Resume(phonecore.Config{})
			if err != nil {
				t.Fatalf("phonecore.Resume: %v", err)
			}
			a := &App{core: core, events: newDispatcher()}
			t.Cleanup(a.events.close)
			if err := a.core.Mutate(func(st *phonecore.State) {
				st.RelayTLSPolicy = tc.policy
				st.RelaySPKIPin = pin
			}); err != nil {
				t.Fatalf("Mutate: %v", err)
			}

			sec := a.handsetSecurity()
			gotPinned := len(sec.PinnedSPKISHA256) > 0
			if gotPinned != tc.wantPinned {
				t.Fatalf("handsetSecurity() under policy %q pinned=%v, want pinned=%v (PinnedSPKISHA256=%x)",
					tc.policy, gotPinned, tc.wantPinned, sec.PinnedSPKISHA256)
			}
			if tc.wantPinned && !bytes.Equal(sec.PinnedSPKISHA256, pin) {
				t.Fatalf("PinnedSPKISHA256 = %x, want %x", sec.PinnedSPKISHA256, pin)
			}
		})
	}
}
