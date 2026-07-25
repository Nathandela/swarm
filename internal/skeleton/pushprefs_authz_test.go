package skeleton

// FAILING-FIRST (TDD RED) daemon-side authorization tests for PB-PUSH-8's new signed
// action, push_prefs.
//
// PB-PUSH-8 requires a device -> machine push-preference verb in the SIGNED ACTION SET,
// which drags in PB-SYNC-5's problem: actionClass (deviceauth.go) is a closed
// fail-closed switch, so an action with no case is refused as "unknown action" no matter
// how valid its signature is. These pin the mapping, the tier, and -- critically -- that
// the DAEMON actually serves the op, because a gateway-side unit test with a fake
// forwarder is green whether or not the real daemon has ever heard of it.
//
// RED is undefined-only: protocol.ActionPushPrefs and protocol.OpPushPrefs do not exist
// yet, so this file does not compile until they do.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestPBPUSH8_PushPrefsIsAKnownActionInTheReadClass pins the capability-tier decision.
//
// The tier is READ, and the reasoning is recorded here because the choice is not
// obvious. A push preference mutates machine-side state, which argues for the control
// class -- but it cannot start, stop, or type into anything, and its entire effect is
// "does the machine wake THIS device". A control-class mapping would mean a read-only
// paired phone receives notifications it has no way to silence, which is worse than the
// privilege it would withhold.
//
// The honest consequence, stated rather than dressed up: because Capability.Allows
// admits ActionRead at every tier, the CAPABILITY gate can never refuse this verb. The
// gate that can and does refuse it is the SIGNATURE, exercised below.
func TestPBPUSH8_PushPrefsIsAKnownActionInTheReadClass(t *testing.T) {
	class, ok := actionClass(protocol.ActionPushPrefs)
	if !ok {
		t.Fatalf("actionClass(push_prefs) ok=false: an action with no case is refused as unknown, so no valid signature can ever set a preference")
	}
	if class != device.ActionRead {
		t.Fatalf("actionClass(push_prefs) = %v, want device.ActionRead", class)
	}

	// Contrast: the switch is still genuinely closed. If this passed, the mapping above
	// would prove nothing (a switch that accepts everything is not a gate).
	if _, ok := actionClass("push_prefs_but_not_really"); ok {
		t.Fatal("actionClass accepted an unknown action: the fail-closed default is gone")
	}
}

// TestPBPUSH8_PushPrefsIsPermittedAtEveryPairedTier is the positive half: a phone at the
// LOWEST capability tier can silence its own notifications. It fails before the mapping
// exists (unknown action), and it would fail again if a later change moved push_prefs to
// the control class.
func TestPBPUSH8_PushPrefsIsPermittedAtEveryPairedTier(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)
	exp := now.Add(time.Minute)

	for _, cap := range []device.Capability{device.CapReadOnly, device.CapReadApprove, device.CapFull} {
		reg, ks, _, id := authFixture(t, cap)
		cmd := signWith(t, ks, id, protocol.ActionPushPrefs, "machine1", protocol.LaunchSessionSentinel, "op-prefs-1", exp)
		if err := authorizeCommand(reg, now, cmd); err != nil {
			t.Fatalf("capability %v was refused push_prefs: %v", cap, err)
		}
	}
}

// TestPBPUSH8_ForgedOrExpiredPushPrefsIsRefused exercises the gate that CAN fail. This
// is the real guard on the verb, and it must be shown failing rather than assumed.
func TestPBPUSH8_ForgedOrExpiredPushPrefsIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_100, 0)

	// POSITIVE CONTROL. Before push_prefs has an actionClass case, EVERY push_prefs is
	// refused as an unknown action -- so the two negative assertions below would pass
	// against a build in which the verb does not work at all.
	regCtl, ksCtl, _, idCtl := authFixture(t, device.CapFull)
	good := signWith(t, ksCtl, idCtl, protocol.ActionPushPrefs, "machine1", protocol.LaunchSessionSentinel, "op-control", now.Add(time.Minute))
	if err := authorizeCommand(regCtl, now, good); err != nil {
		t.Fatalf("control: a genuine in-date push_prefs was refused (%v); the refusals below prove nothing", err)
	}

	reg, _, other, id := authFixture(t, device.CapFull)

	// A signature from a key the registry does not pin for this device.
	forged := signWith(t, other, id, protocol.ActionPushPrefs, "machine1", protocol.LaunchSessionSentinel, "op-forged", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, forged); err == nil {
		t.Fatal("a push_prefs signed by an unpinned key was authorized")
	}

	// A genuine signature that has expired.
	regOK, ks, _, idOK := authFixture(t, device.CapFull)
	stale := signWith(t, ks, idOK, protocol.ActionPushPrefs, "machine1", protocol.LaunchSessionSentinel, "op-stale", now.Add(-time.Second))
	if err := authorizeCommand(regOK, now, stale); err == nil {
		t.Fatal("an expired push_prefs was authorized")
	}
}

// TestPBPUSH8_DaemonServesThePushPrefsOp is the class-(v) guard for this verb. Every
// gateway-side test in internal/remotegw drives a FAKE forwarder, so all of them stay
// green while the real daemon answers "unsupported op" and no preference the user sets
// ever reaches the machine. This drives the op through the real assembled daemon over
// its real remote socket.
//
// It asserts only that the op is SERVED and AUTHORIZED, which is all the daemon does
// here: the durable record and the delivery decision belong to the gateway (PB-PUSH-10
// puts them where delivery is decided), so the daemon has no preference state to check.
func TestPBPUSH8_DaemonServesThePushPrefsOp(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapReadOnly)

	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionPushPrefs,
		Machine:     sk.api.endpointID,
		Session:     protocol.LaunchSessionSentinel,
		OperationID: "op-prefs-e2e-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}

	gw := remotegw.New(rsock, nil)
	reply, err := gw.ForwardCommand(protocol.OpPushPrefs, protocol.LaunchSessionSentinel, cmd, nil)
	if err != nil {
		t.Fatalf("gateway forward: %v", err)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("the daemon refused a paired phone's push_prefs: %q / %q", reply.Error, reply.ErrorCode)
	}

	// And an unpaired phone's identical push_prefs is refused, so the op is authorized
	// rather than merely accepted.
	other, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("other keystore: %v", err)
	}
	bad, err := phonecore.SignCommand(other, phonecore.CommandInput{
		Action:      protocol.ActionPushPrefs,
		Machine:     sk.api.endpointID,
		Session:     protocol.LaunchSessionSentinel,
		OperationID: "op-prefs-e2e-2",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("other sign: %v", err)
	}
	reply, err = gw.ForwardCommand(protocol.OpPushPrefs, protocol.LaunchSessionSentinel, bad, nil)
	if err != nil {
		t.Fatalf("gateway forward (unpaired): %v", err)
	}
	if reply.Op != protocol.OpError || reply.ErrorCode != protocol.CodeNotAuthorized {
		t.Fatalf("unpaired phone's push_prefs = op %q code %q, want error/not_authorized", reply.Op, reply.ErrorCode)
	}
}
