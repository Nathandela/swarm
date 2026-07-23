package skeleton

// FAILING-FIRST (TDD RED) skeleton authorization tests for the take_control op — slice
// A5-a. take_control is a CONTROL-class remote op: reaching a session's keystroke stream
// is at least as privileged as kill/launch, so it must map onto device.ActionControl and
// be permitted ONLY for a full-capability device. These pin the daemon-side capability
// decision with the REAL device.Registry + real crypto (via authFixture/signWith from
// deviceauth_test.go), so a genuine signature from an insufficient tier is refused by the
// cryptography-backed authorizer, not a stub.
//
// The contract these pin (green only once take_control is a known control-class action):
//   - actionClass(protocol.ActionTakeControl) == device.ActionControl (a known action);
//   - a read+approve device with a VALID take_control signature is refused on capability
//     grounds (Capability.Allows(ActionControl) is false for CapReadApprove);
//   - a full-capability device with a VALID take_control signature IS authorized — this
//     positive assertion is what fails until actionClass maps take_control to
//     ActionControl (before the mapping, actionClass returns unknown-action and even a
//     CapFull take_control is refused).
//
// RED is undefined-only: protocol.ActionTakeControl does not exist yet, so this file
// fails to compile until the implementer defines it (internal/protocol/remote.go) and
// adds the take_control case to actionClass (internal/skeleton/deviceauth.go). No
// assertion runs until protocol.ActionTakeControl exists.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// TestSkeleton_TakeControlMapsToControlClass: take_control is a recognised action and
// maps to the ActionControl capability class (never fail-closed unknown, never a read).
// Fails today because actionClass has no take_control case (returns 0, false).
func TestSkeleton_TakeControlMapsToControlClass(t *testing.T) {
	class, ok := actionClass(protocol.ActionTakeControl)
	if !ok {
		t.Fatalf("actionClass(take_control) ok=false; want a KNOWN control-class action (never fail-closed unknown)")
	}
	if class != device.ActionControl {
		t.Fatalf("actionClass(take_control) = %v; want device.ActionControl", class)
	}
}

// TestSkeleton_TakeControlRefusedForLowCapability: a read+approve device (CapReadApprove)
// presenting a GENUINELY-signed take_control is refused — the signature is authentic but
// the tier does not permit a control-class op (Capability.Allows(ActionControl) == false).
// A full-capability device (CapFull) presenting the same take_control IS authorized. The
// positive CapFull assertion pins take_control as a control-class op and RED-fails until
// actionClass maps take_control to device.ActionControl (mirrors
// TestPolicy_ReadApproveDeviceCannotLaunch's negative+positive tier boundary).
func TestSkeleton_TakeControlRefusedForLowCapability(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	exp := now.Add(time.Minute)

	// read+approve: valid signature, insufficient tier for a control-class op -> refused.
	regRA, ksRA, _, idRA := authFixture(t, device.CapReadApprove)
	tc := signWith(t, ksRA, idRA, protocol.ActionTakeControl, "machine1", "machine1/sess1", "op-1", exp)
	if err := authorizeCommand(regRA, now, tc); err == nil {
		t.Fatalf("read+approve device was allowed to take_control; want a capability rejection (control-class)")
	}

	// full capability: same valid take_control signature -> authorized. Fails until
	// take_control is a known control-class action in actionClass.
	regF, ksF, _, idF := authFixture(t, device.CapFull)
	okCmd := signWith(t, ksF, idF, protocol.ActionTakeControl, "machine1", "machine1/sess1", "op-2", exp)
	if err := authorizeCommand(regF, now, okCmd); err != nil {
		t.Fatalf("full-capability device was refused take_control: %v (take_control must map to ActionControl)", err)
	}
}
