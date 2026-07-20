package device

// Failing-first (TDD RED, GG-5) tests for the R-DEV.1 device-registry slice's
// capability model (R-POL.6, plan .claude/tmp/remote-control-implementation-plan.md
// :697-701). These tests are the acceptance criteria for the three-tier per-device
// capability policy that the daemon later enforces per remote command (R-POL.9).
//
// Every test below fails to COMPILE today because the package `device` and all of
// its symbols (Capability, the Cap* constants, Action, the Action* constants,
// Capability.Allows, Capability.MarshalText/UnmarshalText) do not yet exist. A
// separate implementer makes them pass; no test here is edited to go green.
//
// The security-critical contract these tests pin:
//   - The capability tiers form a strict ladder: read_only < read_approve < full.
//   - Enforcement is FAIL-CLOSED: an unknown/zero-invalid Capability value grants
//     NOTHING, and an unknown capability STRING fails to decode rather than
//     silently defaulting to any tier (never to CapFull).

import (
	"encoding/json"
	"testing"
)

// TestCapability_AllowsTruthTable pins the full 3x3 tier-vs-action matrix that is
// the heart of R-POL.6, plus the fail-closed behavior of an out-of-range value.
//
// CapReadOnly    -> Read only.
// CapReadApprove -> Read + Approve (NOT Control).
// CapFull        -> Read + Approve + Control.
// An unknown/zero-invalid Capability -> NOTHING (fail-closed).
func TestCapability_AllowsTruthTable(t *testing.T) {
	cases := []struct {
		name       string
		cap        Capability
		read       bool
		approve    bool
		control    bool
	}{
		{"read_only", CapReadOnly, true, false, false},
		{"read_approve", CapReadApprove, true, true, false},
		{"full", CapFull, true, true, true},
		// A zero/unknown value must be treated as no grant at all. 0xFF is a value
		// no valid constant can occupy; it stands in for a corrupted/forged tier.
		{"unknown_high", Capability(0xFF), false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cap.Allows(ActionRead); got != tc.read {
				t.Errorf("%s.Allows(ActionRead) = %v, want %v", tc.name, got, tc.read)
			}
			if got := tc.cap.Allows(ActionApprove); got != tc.approve {
				t.Errorf("%s.Allows(ActionApprove) = %v, want %v", tc.name, got, tc.approve)
			}
			if got := tc.cap.Allows(ActionControl); got != tc.control {
				t.Errorf("%s.Allows(ActionControl) = %v, want %v", tc.name, got, tc.control)
			}
		})
	}
}

// TestCapability_TextRoundTrip pins the stable snake_case JSON encoding for the
// three valid tiers: each marshals to its documented wire string and unmarshals
// back to the identical Capability value. The on-disk registry (TestRegistry_Persist)
// depends on this being byte-stable, so it is asserted directly here too.
func TestCapability_TextRoundTrip(t *testing.T) {
	cases := []struct {
		cap  Capability
		wire string
	}{
		{CapReadOnly, "read_only"},
		{CapReadApprove, "read_approve"},
		{CapFull, "full"},
	}
	for _, tc := range cases {
		t.Run(tc.wire, func(t *testing.T) {
			b, err := json.Marshal(tc.cap)
			if err != nil {
				t.Fatalf("json.Marshal(%v) error: %v", tc.cap, err)
			}
			// Marshalled form must be the exact quoted snake_case string.
			if got, want := string(b), `"`+tc.wire+`"`; got != want {
				t.Fatalf("json.Marshal(%v) = %s, want %s", tc.cap, got, want)
			}
			var back Capability
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatalf("json.Unmarshal(%s) error: %v", b, err)
			}
			if back != tc.cap {
				t.Fatalf("round-trip: got %v, want %v", back, tc.cap)
			}
		})
	}
}

// TestCapability_UnmarshalUnknownFailsClosed is the fail-closed heart of R-POL.6:
// an unrecognized capability string MUST error, never silently decode to a tier —
// and above all never to CapFull. A forged/typo'd policy string must not escalate.
func TestCapability_UnmarshalUnknownFailsClosed(t *testing.T) {
	for _, bad := range []string{`"bogus"`, `"READ_ONLY"`, `"readonly"`, `"admin"`, `""`} {
		t.Run(bad, func(t *testing.T) {
			var c Capability = CapFull // seed with a non-zero value to expose silent no-writes
			err := json.Unmarshal([]byte(bad), &c)
			if err == nil {
				t.Fatalf("json.Unmarshal(%s) = nil error; want a decode error (fail-closed)", bad)
			}
		})
	}
}
