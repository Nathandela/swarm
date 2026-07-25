package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for slice S10's MACHINE side:
//
//   PB-KEY-3  epoch-grant recovery: a lost grant must have a defined, recoverable end.
//   PB-KEY-4  rotation while the phone is backgrounded/offline, WITHOUT unpairing it.
//   PB-SYNC-5 the closed actionClass switch, and the decision that the resync is unsigned.
//
// Reused (same package): assemble / assembleWithMachineIdentity, writeTestIdentity,
// validDeviceRecord.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// s10RotatedMachine assembles a daemon whose machine identity has been rotated to epoch 2,
// with ONE device still registered at epoch 1 and holding a grant sidecar -- exactly the
// state a rotation the phone slept through leaves behind. It returns the daemon and the
// device record.
//
// Note what is NOT possible to reach here: the ONLY rotation path in the tree is
// coreAPI.rotateEpoch, and its only caller is RevokeDevice, which rotates and then REMOVES
// the device in the same transaction. A rotation that keeps a device paired therefore has
// no producer at all, which is why PB-KEY-4's scenario is constructed rather than driven.
// That absence is itself the finding: a re-grant is the only thing that can produce it.
func s10RotatedMachine(t *testing.T) (*Daemon, device.Record) {
	t.Helper()
	rec := validDeviceRecord(t) // GrantedEpoch defaults to 1
	sk, err := assembleWithMachineIdentity(t, func(stateDir string) {
		remoteDir := filepath.Join(stateDir, "remote")
		if err := os.MkdirAll(remoteDir, 0o700); err != nil {
			t.Fatalf("mkdir remote: %v", err)
		}
		id, gerr := machineid.Generate("s10-regrant-host")
		if gerr != nil {
			t.Fatalf("machineid.Generate: %v", gerr)
		}
		if rerr := id.RotateEpoch(); rerr != nil { // epoch 1 -> 2, the phone slept through it
			t.Fatalf("RotateEpoch: %v", rerr)
		}
		if serr := id.Save(filepath.Join(remoteDir, "machine.key")); serr != nil {
			t.Fatalf("Save identity: %v", serr)
		}
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := grant.Save(sk.api.registryDir(), rec.DeviceID, &crypto.EpochGrant{EpochID: 1, GrantSeq: 1}); err != nil {
		t.Fatalf("seed grant sidecar: %v", err)
	}
	return sk, rec
}

// s10SameEpochMachine is s10RotatedMachine's counterpart with the epoch STANDING STILL: the
// machine is at epoch 1, the device is registered at epoch 1, and its sidecar carries the
// grant coordinates it was already handed, (1, 1).
//
// It is the PRIMARY re-grant case, not a variant. PB-KEY-3's terminal state is a bootstrap
// frame that was lost -- purged past the relay's retention cap, or dropped -- with nothing
// having rotated, so the re-grant reuses the live epoch and ONLY the grant seq can carry the
// strict increase crypto.GrantReceiver.Accept demands. It is also where PB-KEY-7 lands:
// dropKeyMaterial preserves GrantEpoch/GrantSeq across a lock purge deliberately, so after a
// purge the gateway's ordinary re-delivery of the SAME sidecar is refused by the phone as a
// replay, and only a seq-advancing re-grant recovers the handset.
//
// The rotated fixture cannot fence any of that: its epoch has already moved, so an
// epoch-only comparison is satisfied whatever the seq does.
func s10SameEpochMachine(t *testing.T) (*Daemon, device.Record) {
	t.Helper()
	rec := validDeviceRecord(t) // GrantedEpoch defaults to 1, matching the un-rotated identity
	sk, err := assembleWithMachineIdentity(t, func(stateDir string) {
		remoteDir := filepath.Join(stateDir, "remote")
		if err := os.MkdirAll(remoteDir, 0o700); err != nil {
			t.Fatalf("mkdir remote: %v", err)
		}
		id, gerr := machineid.Generate("s10-regrant-sameepoch-host")
		if gerr != nil {
			t.Fatalf("machineid.Generate: %v", gerr)
		}
		if serr := id.Save(filepath.Join(remoteDir, "machine.key")); serr != nil {
			t.Fatalf("Save identity: %v", serr)
		}
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := grant.Save(sk.api.registryDir(), rec.DeviceID, &crypto.EpochGrant{EpochID: 1, GrantSeq: 1}); err != nil {
		t.Fatalf("seed grant sidecar: %v", err)
	}
	if got := s10MachineEpoch(t, sk.api.stateDir); got != 1 {
		t.Fatalf("precondition: the fixture machine is at epoch %d, not 1; the epoch must NOT have "+
			"moved or the seq assertion below is satisfied by the epoch alone", got)
	}
	return sk, rec
}

// s10MachineEpoch reads the current machine epoch the reconcile compares against.
func s10MachineEpoch(t *testing.T, stateDir string) uint32 {
	t.Helper()
	epoch, ok := currentMachineEpoch(stateDir)
	if !ok {
		t.Fatalf("the machine identity at %s is unreadable; the reconcile would leave every device untouched", stateDir)
	}
	return epoch
}

// ---------------------------------------------------------------------------
// PB-KEY-4.
// ---------------------------------------------------------------------------

// TestS10_ARotationTheDeviceSleptThroughUnpairsItOnTheNextRestart is the NON-VACUITY
// CONTROL for everything below, and it PASSES TODAY: it documents the mechanism rather than
// finding it. Without it, the re-grant test could pass because the reconcile never removes
// anything, and would prove nothing about GrantedEpoch.
func TestS10_ARotationTheDeviceSleptThroughUnpairsItOnTheNextRestart(t *testing.T) {
	sk, rec := s10RotatedMachine(t)

	// The daemon restarts. reconcilePairedDevices runs before anything is served.
	if err := reconcilePairedDevices(sk.api.devices, sk.api.stateDir); err != nil {
		t.Fatalf("reconcilePairedDevices: %v", err)
	}
	if _, ok := sk.api.devices.Get(rec.DeviceID); ok {
		t.Fatalf("a device whose GrantedEpoch (%d) does not match the machine epoch (%d) survived "+
			"the startup reconcile. This control exists so the re-grant assertion below cannot "+
			"pass by the reconcile being inert", rec.GrantedEpoch, s10MachineEpoch(t, sk.api.stateDir))
	}
}

// TestS10_ARegrantConvergesTheDeviceOntoTheCurrentEpoch is PB-KEY-4's acceptance criterion,
// in its own words: rotate while offline, reconnect, converge, RESTART THE DAEMON, and
// assert the device is still paired.
func TestS10_ARegrantConvergesTheDeviceOntoTheCurrentEpoch(t *testing.T) {
	sk, rec := s10RotatedMachine(t)
	curEpoch := s10MachineEpoch(t, sk.api.stateDir)

	if err := sk.api.RegrantDevice(rec.DeviceID); err != nil {
		t.Fatalf("RegrantDevice: %v.\n\nThere is no re-grant verb anywhere in the tree today: "+
			"the only rotation path is coreAPI.rotateEpoch, whose only caller is RevokeDevice, "+
			"which removes the device in the same transaction. So a phone that slept through a "+
			"rotation has no way back that does not involve physical access to the machine", err)
	}

	got, ok := sk.api.devices.Get(rec.DeviceID)
	if !ok {
		t.Fatalf("the device is gone after a re-grant")
	}
	if got.GrantedEpoch != curEpoch {
		t.Errorf("after the re-grant the device record's GrantedEpoch is %d, want the current "+
			"machine epoch %d. reconcilePairedDevices removes any device whose GrantedEpoch does "+
			"not match on EVERY daemon start, so a re-grant that converges the KEY but not the "+
			"RECORD silently unpairs the only device on the next restart -- the phone simply "+
			"stops working and nothing says why", got.GrantedEpoch, curEpoch)
	}

	g, err := grant.Load(sk.api.registryDir(), rec.DeviceID)
	if err != nil {
		t.Fatalf("load the re-granted sidecar: %v", err)
	}
	if g == nil {
		t.Fatalf("the re-grant left no sidecar. The gateway delivers the bootstrap frame by " +
			"loading exactly this file (cmd/swarm-remote/deliver.go), so a re-grant with no " +
			"sidecar delivers nothing -- and reconcilePairedDevices then removes the device for " +
			"having no sidecar at all")
	}
	if g.EpochID != curEpoch {
		t.Errorf("the re-granted sidecar carries epoch %d, want %d. A grant sealed under the dead "+
			"epoch delivers a key the machine no longer seals anything with", g.EpochID, curEpoch)
	}

	// THE DAEMON RESTARTS. This is the assertion PB-KEY-4 actually names.
	if err := reconcilePairedDevices(sk.api.devices, sk.api.stateDir); err != nil {
		t.Fatalf("reconcilePairedDevices after the re-grant: %v", err)
	}
	if _, ok := sk.api.devices.Get(rec.DeviceID); !ok {
		t.Errorf("the device is UNPAIRED after the daemon restart that followed a successful " +
			"re-grant. Converging the key without the record is not convergence")
	}
}

// TestS10_ARegrantAdvancesTheGrantSeq: the phone runs every grant through a
// crypto.GrantReceiver seeded from its durable watermark, which enforces strict
// (epoch_id, grant_seq) monotonicity. A re-grant that reuses the previous coordinates is
// refused by the phone as a REPLAY -- so the unblock silently does not unblock.
//
// IT RUNS OVER BOTH FIXTURES, and the same-epoch one is what gives the assertion teeth.
// Against the rotated fixture alone the epoch has already moved, so the comparison is
// satisfied by the epoch whatever the seq does: making Identity.NextGrantSeq return the
// counter unchanged left the whole repository green. Only a re-grant under a STANDING epoch
// -- the lost-bootstrap case PB-KEY-3 actually describes, and the post-purge case PB-KEY-7
// leaves behind -- forces the seq to carry the increase on its own.
func TestS10_ARegrantAdvancesTheGrantSeq(t *testing.T) {
	for _, tc := range []struct {
		name    string
		machine func(*testing.T) (*Daemon, device.Record)
	}{
		{"same epoch (a lost bootstrap; only the seq can carry the increase)", s10SameEpochMachine},
		{"rotated epoch (the phone slept through a rotation)", s10RotatedMachine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk, rec := tc.machine(t)
			before, err := grant.Load(sk.api.registryDir(), rec.DeviceID)
			if err != nil || before == nil {
				t.Fatalf("precondition: seeded sidecar unreadable (%v)", err)
			}

			if err := sk.api.RegrantDevice(rec.DeviceID); err != nil {
				t.Fatalf("RegrantDevice: %v", err)
			}
			after, err := grant.Load(sk.api.registryDir(), rec.DeviceID)
			if err != nil || after == nil {
				t.Fatalf("load the re-granted sidecar: %v", err)
			}
			if after.EpochID < before.EpochID || (after.EpochID == before.EpochID && after.GrantSeq <= before.GrantSeq) {
				t.Errorf("the re-grant's coordinates (epoch %d, seq %d) do not strictly exceed the "+
					"previous grant's (epoch %d, seq %d). crypto.GrantReceiver.Accept rejects anything "+
					"that does not, and the phone seeds it from durable state -- so the re-granted key "+
					"is refused as ErrGrantReplay and the device stays exactly as broken as it was",
					after.EpochID, after.GrantSeq, before.EpochID, before.GrantSeq)
			}
		})
	}
}

// TestS10_ARepeatedRegrantKeepsAdvancingTheGrantSeq is the same property under REPETITION,
// which is the shape the support path really takes: the owner runs `swarm remote regrant`,
// the phone is locked or offline when the gateway session that carried it ended, and they
// run it again. A verb that advances once and then plateaus -- because the floor is only
// consulted against the sidecar, or because the counter is re-derived rather than allocated
// -- makes every attempt after the first a replay the phone silently refuses, with the CLI
// reporting success each time.
func TestS10_ARepeatedRegrantKeepsAdvancingTheGrantSeq(t *testing.T) {
	sk, rec := s10SameEpochMachine(t)

	prev := uint64(0)
	for i := 0; i < 3; i++ {
		if err := sk.api.RegrantDevice(rec.DeviceID); err != nil {
			t.Fatalf("RegrantDevice #%d: %v", i+1, err)
		}
		g, err := grant.Load(sk.api.registryDir(), rec.DeviceID)
		if err != nil || g == nil {
			t.Fatalf("load the re-granted sidecar #%d: %v", i+1, err)
		}
		if g.EpochID != 1 {
			t.Fatalf("re-grant #%d moved the epoch to %d; a re-grant must not rotate", i+1, g.EpochID)
		}
		if g.GrantSeq <= prev {
			t.Fatalf("re-grant #%d minted seq %d, which does not exceed the previous %d. The second "+
				"and every later re-grant is refused by the phone as ErrGrantReplay while the CLI "+
				"reports success", i+1, g.GrantSeq, prev)
		}
		prev = g.GrantSeq
	}
}

// TestS10_ARegrantOfAnUnknownDeviceIsRefused is the fail-closed direction: minting and
// persisting a grant sidecar for an id the registry does not hold would write a deliverable
// key for a device that was revoked, and reconcilePairedDevices would not clean it up
// (it walks the REGISTRY, not the sidecar directory).
//
// VACUOUSLY PASSING AGAINST THE RED SCAFFOLD, and labelled so no evidence line counts it as
// earned: the scaffold refuses EVERYTHING, so "a refusal happened" is satisfied by a verb
// that does nothing at all. It becomes real the moment RegrantDevice has a success path --
// which is exactly when the assertion starts to matter -- and it is written now so the
// implementer cannot land the success path without the guard.
func TestS10_ARegrantOfAnUnknownDeviceIsRefused(t *testing.T) {
	sk, _ := s10RotatedMachine(t)

	if err := sk.api.RegrantDevice("0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Errorf("RegrantDevice succeeded for a device the registry does not hold")
	}
	if _, err := os.Stat(grant.Path(sk.api.registryDir(),
		"0000000000000000000000000000000000000000000000000000000000000000")); !os.IsNotExist(err) {
		t.Errorf("a refused re-grant still wrote a sidecar (stat err = %v). A deliverable key for "+
			"an unregistered device is a key nothing will ever clean up: the startup reconcile "+
			"walks the registry, not the sidecar directory", err)
	}
}

// ---------------------------------------------------------------------------
// PB-SYNC-5: the closed switch, and the decision.
// ---------------------------------------------------------------------------

// TestS10_EveryForwardedActionIsMappedInActionClass is PB-SYNC-5 made mechanical. actionClass
// is a CLOSED switch that fails closed on an unknown action, and remotegw.opForAction is the
// gateway's list of actions it FORWARDS to the daemon's device authenticator. An action in
// the second and not the first is a half-landed change that refuses every correctly-signed
// command with "unknown action" -- a silent brick, which is exactly the trap the requirement
// warns a new resync verb would walk into.
//
// The gateway's list is restated here rather than imported: internal/skeleton must not
// depend on internal/remotegw, and a literal that has to be edited in lockstep is the point
// -- adding a forwarded action without a capability class becomes a failing test rather than
// a refusal in production.
//
// LEGITIMATELY PASSING TODAY, and labelled so no evidence line counts it as earned: the four
// mappings are all present. It is a standing REGRESSION FENCE, and there is no equivalent in
// the suite -- nothing today would notice a fifth forwarded action landing unmapped.
func TestS10_EveryForwardedActionIsMappedInActionClass(t *testing.T) {
	forwarded := []string{
		protocol.ActionKill,
		protocol.ActionDelete,
		protocol.ActionLaunch,
		protocol.ActionPushPrefs,
	}
	for _, action := range forwarded {
		if _, ok := actionClass(action); !ok {
			t.Errorf("remotegw forwards %q to the daemon's device authenticator, but actionClass "+
				"does not map it. The switch fails closed, so EVERY correctly-signed %q is refused "+
				"as an unknown action -- with a valid signature, a valid capability and nothing "+
				"anywhere saying the mapping is missing", action, action)
		}
	}
}

// TestS10_TheResyncActionIsDeliberatelyUnsigned records PB-SYNC-5's DECISION as a fence.
//
// THE DECISION: the journal resync is an UNSIGNED read command routed by the gateway, like
// terminal_watch and terminal_unwatch, and it is therefore absent from actionClass.
//
// THE REASONING, which is why this is a fence and not a comment: the only fitting existing
// class is ActionControl, which would make a READ REPAIR require the control tier; device
// capability is pinned at enrollment (pairing.go) and never read from the wire, so an
// observe-tier device could never resync its own view; and mapping it to ActionRead instead
// buys nothing, because Capability.Allows admits ActionRead at every tier -- the class could
// not refuse anything (ADR-007 B20 already recorded that for push_prefs). Sealing under the
// epoch content key is already proof the asker is the paired device.
//
// The fence fails if a later change makes the resync signed WITHOUT mapping it (every
// resync silently refused), and equally if it maps it without making it signed (a capability
// gate on a verb that never reaches the authorizer, which reads as protection and is none).
//
// LEGITIMATELY PASSING TODAY, and labelled so no evidence line counts it as earned: it
// records a DECISION, and the decision is already consistent with the tree.
func TestS10_TheResyncActionIsDeliberatelyUnsigned(t *testing.T) {
	if _, ok := actionClass(protocol.ActionJournalResync); ok {
		t.Errorf("actionClass maps %q. Either the resync is now device-signed -- in which case "+
			"the capability tier it lands in must be DECIDED and stated, and an observe-tier "+
			"device must still be able to repair its own view -- or the mapping is decoration on "+
			"a verb the authorizer never sees", protocol.ActionJournalResync)
	}
	// The two existing unsigned reads are absent for the same reason; if one of them ever
	// appears, the shape of the decision has changed and this fence must be revisited with it.
	for _, action := range []string{protocol.ActionTerminalWatch, protocol.ActionTerminalUnwatch} {
		if _, ok := actionClass(action); ok {
			t.Errorf("actionClass now maps the unsigned read %q; the resync's decision was made by "+
				"analogy to it, so the analogy needs restating", action)
		}
	}
}
