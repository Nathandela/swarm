package remotegw

// FAILING-FIRST (TDD RED, GG-5) for Wave R5's GATEWAY sliver (bead
// agents-tracker-hggx.6): the command routing for the finished launch vocabulary.
// Companion to internal/protocol/r5_sessionlaunch_test.go (the daemon-side handlers)
// and r1_refusalops_test.go here (which already pinned that session_launch FORWARDS,
// Op == Action, never gateway-locally refused). This file adds only what R5 changes
// at this hop:
//
//   - protocol.ActionLaunchPresets / protocol.OpLaunchPresets ("launch_presets", the
//     playbook :414 op): opForAction gains the arm, forwarded like every semantic op
//     -- only the daemon holds the device registry and the preset custody.
//   - schema.RemoteCommand carries the session_launch body (SessionLaunch
//     *schema.SessionLaunchReq), and opForAction refuses a session_launch WITHOUT its
//     body at the gateway, exactly by launch's and approve's own precedent: a
//     stripped body forwarded as a zero request would reach the daemon as a launch
//     naming no preset, and the user would be told their preset is invalid by a frame
//     that merely lost its payload.
//
// The gateway remains a blind conduit for the body's CONTENT: the refusal decisions
// (kill switch, tier, unknown/stale preset, roots, options) are all machine-side,
// behind requireRemoteAuthz -- nothing here inspects preset ids.
//
// This file must fail to compile (undefined: protocol.ActionLaunchPresets /
// rc.SessionLaunch) until the GREEN slice adds the vocabulary.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestR5Route_LaunchPresetsForwardsToItsOwnOp: the read of the machine-authored
// preset list rides the signed-command plane to the daemon, Op == Action like every
// mapped sibling.
func TestR5Route_LaunchPresetsForwardsToItsOwnOp(t *testing.T) {
	op, err := opForAction(protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{Action: protocol.ActionLaunchPresets},
	})
	if err != nil {
		t.Fatalf("opForAction(launch_presets) = error %v; want it forwarded -- the preset custody "+
			"lives daemon-side and a gateway-local refusal would invent policy at the wrong hop", err)
	}
	if op != protocol.OpLaunchPresets {
		t.Errorf("opForAction(launch_presets) = %q, want %q", op, protocol.OpLaunchPresets)
	}
	if protocol.OpLaunchPresets != "launch_presets" {
		t.Errorf("OpLaunchPresets = %q, want the stable wire spelling launch_presets", protocol.OpLaunchPresets)
	}
}

// TestR5Route_SessionLaunchWithoutItsBodyIsRefusedAtTheGateway: launch's own rule,
// inherited by the preset op. A session_launch whose SessionLaunch body was stripped
// in transit must NOT be forwarded as a bodyless frame.
func TestR5Route_SessionLaunchWithoutItsBodyIsRefusedAtTheGateway(t *testing.T) {
	_, err := opForAction(protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{Action: protocol.ActionSessionLaunch},
		SessionLaunch:     nil,
	})
	if err == nil {
		t.Fatal("opForAction(session_launch, no body) = nil error; want the gateway to refuse a " +
			"stripped body rather than forward a launch naming no preset (the launch/approve precedent)")
	}
}

// TestR5Route_SessionLaunchBodyRidesTheMappingUnchanged: with the body present the
// mapping still forwards to its own op, and the RemoteCommand handed onward is the
// SAME value -- the gateway adds nothing, drops nothing, and rewrites nothing in the
// preset body (blind-conduit rule; the content binding is the phone's signature).
func TestR5Route_SessionLaunchBodyRidesTheMappingUnchanged(t *testing.T) {
	rc := protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{Action: protocol.ActionSessionLaunch},
		SessionLaunch: &schema.SessionLaunchReq{
			PresetID: "preset-api", PresetRevision: "rev-1", InitialPrompt: "fix it",
			Cols: 80, Rows: 24,
		},
	}
	op, err := opForAction(rc)
	if err != nil {
		t.Fatalf("opForAction(session_launch with body) = error %v; want forwarded", err)
	}
	if op != protocol.OpSessionLaunch {
		t.Errorf("opForAction(session_launch) = %q, want %q", op, protocol.OpSessionLaunch)
	}
	if rc.SessionLaunch.PresetID != "preset-api" || rc.SessionLaunch.PresetRevision != "rev-1" ||
		rc.SessionLaunch.InitialPrompt != "fix it" {
		t.Errorf("session_launch body mutated by the mapping: %+v", rc.SessionLaunch)
	}
}

// TestR5Route_UnknownActionRefusalStillHolds: adding the new arms must not loosen the
// fail-closed default -- an action string neither mapped nor known still refuses (the
// nx444/m03 contract, re-asserted beside the new arms so a sweep of this file alone
// proves the default survived the edit).
func TestR5Route_UnknownActionRefusalStillHolds(t *testing.T) {
	if _, err := opForAction(protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{Action: "launch_presets_v2"},
	}); err == nil {
		t.Fatal("opForAction(unknown action) = nil error; want the generic unsupported-action refusal")
	}
}
