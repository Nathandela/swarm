package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
// agents-tracker-hggx.6), facade half of BLOCKER 3: the launch screen's TIER_FORBIDS
// availability state had NO data source -- the exported surface carried no
// tier/capability accessor at all, so the resolver's `tier` parameter could never be
// filled with a machine fact. The honest source is the machine itself: the
// launch_presets reply now stamps the SIGNING device's registry-pinned capability
// (device_capability), and the facade exposes exactly what the machine last
// published:
//
//   App.LaunchCapability() (string, error) -- "full"/"read_only"/"read_approve" as
//   adopted from the last launch_presets reply, and "" while the machine has not
//   answered one yet (the screens' first-run state; ADR-007 B135: no Go-constant
//   defaults dressed up as wire facts).
//
// This file must fail to compile (App.LaunchCapability undefined) until the round-2
// GREEN slice adds the accessor.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestR5Round2_LaunchCapabilityIsAWireFactNeverInvented: empty until a
// launch_presets reply carries it; then exactly the machine's word; replaced (not
// merged) by the next reply.
func TestR5Round2_LaunchCapabilityIsAWireFactNeverInvented(t *testing.T) {
	a := approveApp(t)

	tier, err := a.LaunchCapability()
	if err != nil {
		t.Fatalf("LaunchCapability: %v", err)
	}
	if tier != "" {
		t.Fatalf("LaunchCapability before any launch_presets reply = %q, want \"\": the phone "+
			"cannot know its tier until the machine says it (capability is pinned machine-side "+
			"at enrollment and read from no other reply)", tier)
	}

	a.adoptPresets(schema.Control{
		Op:               schema.ActionLaunchPresets,
		DeviceCapability: "read_only",
		Presets:          []schema.LaunchPresetView{{ID: "preset-api", Revision: "rev-1"}},
	})
	tier, err = a.LaunchCapability()
	if err != nil {
		t.Fatalf("LaunchCapability: %v", err)
	}
	if tier != "read_only" {
		t.Errorf("LaunchCapability after adoption = %q, want the machine's read_only", tier)
	}

	// The next reply REPLACES: a machine that re-tiers the device is believed.
	a.adoptPresets(schema.Control{Op: schema.ActionLaunchPresets, DeviceCapability: "full"})
	if tier, _ = a.LaunchCapability(); tier != "full" {
		t.Errorf("LaunchCapability after the second reply = %q, want full", tier)
	}

	// An error reply adopts nothing -- a refusal must not blank or rewrite the tier.
	a.adoptPresets(schema.Control{Op: schema.ActionLaunchPresets, ErrorCode: schema.CodeKillSwitch})
	if tier, _ = a.LaunchCapability(); tier != "full" {
		t.Errorf("LaunchCapability after an error reply = %q, want the prior full untouched", tier)
	}
}
