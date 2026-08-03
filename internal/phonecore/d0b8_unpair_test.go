// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-d0b8: "Replace this computer bricks
// pairing: nothing clears the pinned machine."
//
// WHAT HAPPENED. ADR-007 B133 made revoke/unpair the purge's trigger and agents-tracker-64rf
// put the control on the settings screen, where pressing it deregisters the device, rotates the
// epoch, severs the gateway and destroys BOTH key tiers. What it does not do is leave any
// durable trace that the registration is over: `dropAllKeyMaterial` clears the keys, the push
// token and the decrypted caches, and `State.Machine` -- the coordinate the phone's presentation
// gate reads through `StateSummary` -- is written back untouched by `persistState`. So a revoked
// phone comes back up in the four-tab scaffold, the settings screen still says it is paired, and
// the pairing entry point is on the one screen the gate will not show it. The phone is
// unpairable short of clearing the app's data, which is strictly worse than the defect 64rf fixed.
//
// WHY THE FIX IS A NEW COORDINATE AND NOT `Machine = ""`. Two reasons, both in this package.
//
//	THE FILTER. `OpenStore`'s machine id is the load-time filter AND the initialiser, and its
//	  own doc records why: a blob stamped with a machine that is not the caller's loads EMPTY,
//	  so persisting an empty one made the store write a blob it would itself discard on the
//	  next process start -- pairing, epoch, sealed content key, relay cursor and send-seq
//	  ceilings, silently, on the first Android process death. That is S9, and the initialiser
//	  exists so a store can no longer do it. Clearing the field from a caller puts it back by
//	  a different door; `TestD0B8_TheUnpairDoesNotDisarmTheMachineIdFilter` is what holds that.
//	THE SIGNATURE. `State.Machine` is what every mutating verb signs over, and
//	  `crypto.Command.Canonical` refuses an empty one -- so a cleared name is not merely a lost
//	  label, it is a phone that cannot author anything even after it pairs again (the guard at
//	  mobile/pairing.go's `pin` keeps a known name for exactly that reason).
//
// AND NOT DERIVED FROM KEY MATERIAL EITHER, which is the third approach and the one that looks
// cheapest. `hasSealedContentKey` answers false for a paired phone whose sealed blob belongs to
// a PREVIOUS epoch -- which is where `pin` leaves a re-pairing that lands in a new epoch (it
// zeroes `State.Keys` and adopts the new epoch id, and the content key arrives later, in the
// machine's grant). A gate derived from key material shows the pairing screen to a phone that
// has just finished pairing.
//
// SO THE FACT IS EXPLICIT AND DURABLE. It is set by the ONE act that ends a registration, it
// survives process death, a writer that has not noticed it cannot undo it, and pairing again
// clears it. Those four are the cases below.

package phonecore

import (
	"testing"
)

// TestD0B8_ARevokeRecordsTheUnpairDurably is the requirement: after the purge the phone's own
// durable state says the registration is over, and it still says so in the next process.
//
// THE SECOND HALF IS THE WHOLE POINT. Android SIGKILLs the app as routine behaviour, so a flag
// held in memory comes back clear and the handset re-presents itself as paired on the very next
// launch -- which is the shape of the brick this test exists for, not a refinement of it.
func TestD0B8_ARevokeRecordsTheUnpairDurably(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if core.State().Disowned {
		t.Fatal("the fixture is already disowned, so this test would pass over a purge that did nothing")
	}
	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	if !core.State().Disowned {
		t.Error("agents-tracker-d0b8: the purge left no record that the registration is over, so " +
			"every reader of this state still answers that the phone is paired -- including the " +
			"presentation gate, which then shows the app instead of the one screen that could pair again")
	}
	// From the BYTES, because a Load answers from the in-memory copy the purge just wrote.
	if !readStateFile(t, dir).Disowned {
		t.Error("agents-tracker-d0b8: the unpair is in memory only. Android SIGKILLs this app as " +
			"routine behaviour, so a phone the owner revoked comes back paired on the next launch")
	}
	if reopened := s14aR2Resume(t, dir, wake, content).State(); !reopened.Disowned {
		t.Error("agents-tracker-d0b8: a fresh process over the revoked directory reports the phone " +
			"as still owned by its machine")
	}
}

// TestD0B8_TheUnpairDoesNotDisarmTheMachineIdFilter is the constraint the fix is shaped around:
// the pinned machine SURVIVES, so the blob still passes `OpenStore`'s load-time filter.
//
// `s14aR2Resume` opens every store here with a non-empty machine id, so the filter is ARMED in
// this test rather than notionally present: a fix that cleared `State.Machine` would leave a blob
// whose machine no longer matches the caller's, and the reopened store below would answer with
// the empty state that discard produces -- no epoch, no watermark, and no record of the unpair
// either. That is S9 reopened by the remedy for a different bug.
func TestD0B8_TheUnpairDoesNotDisarmTheMachineIdFilter(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)
	pinned := core.State().Machine
	if pinned == "" {
		t.Fatal("the fixture pins no machine, so this test cannot tell a surviving id from a cleared one")
	}
	watermark := core.State().GrantSeq
	if watermark == 0 {
		t.Fatal("the fixture records no grant watermark, so a discarded blob would look identical to a kept one")
	}

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	reopened := s14aR2Resume(t, dir, wake, content).State()
	if reopened.Machine != pinned {
		t.Errorf("agents-tracker-d0b8: the unpair changed the pinned machine from %q to %q. That is the "+
			"coordinate OpenStore filters the blob on and the coordinate every mutating verb signs over "+
			"-- moving it discards the whole state on the next launch (S9) and leaves a re-paired phone "+
			"unable to author anything", pinned, reopened.Machine)
	}
	if reopened.GrantSeq != watermark {
		t.Errorf("agents-tracker-d0b8: the grant watermark came back %d rather than %d, so the blob was "+
			"discarded at load rather than read -- a replayed grant is now accepted", reopened.GrantSeq, watermark)
	}
}

// TestD0B8_AWriterThatHasNotNoticedTheRevokeCannotUndoIt.
//
// THE RACE IS THE ORDINARY CASE, not a corner. The revoke arrives from an Android lifecycle
// callback while the relay drain, the op queue and the send path all hold State snapshots taken
// before it and Save them as they finish -- `Save` adopts everything but the replay guards AS
// GIVEN, so a snapshot carrying the pre-revoke value would put the phone back in the app shell
// with no keys, which is the brick with an extra step. Custody already knows the shape of this
// hazard: it re-applies the purge to any writer arriving with an older purge stamp, and the
// unpair is part of what that purge established.
func TestD0B8_AWriterThatHasNotNoticedTheRevokeCannotUndoIt(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	// The snapshot a goroutine already holds when the owner presses the control.
	stale := core.State()

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	// It finishes its work and writes, knowing nothing about the revoke.
	if err := core.Save(stale); err != nil {
		t.Fatalf("the pre-revoke writer's Save: %v", err)
	}

	if !core.State().Disowned {
		t.Error("agents-tracker-d0b8: a writer holding a snapshot from before the revoke cleared the " +
			"unpair. The revoke arrives from a lifecycle callback while the drain and the op queue are " +
			"mid-write, so this is the ordinary sequence rather than a race to be argued away")
	}
	if !readStateFile(t, dir).Disowned {
		t.Error("agents-tracker-d0b8: the pre-revoke writer's Save reached disk with the unpair cleared, " +
			"so the next launch comes up paired")
	}
}

// TestD0B8_PairingAgainClearsTheUnpair is the other direction, and it is what keeps the fix from
// being a brick of its own: a phone that can record "disowned" and never clear it has replaced an
// unpairable phone with an unpairable-and-unpairable-again one.
//
// It is written at the seam a pairing actually uses -- `mobile.App.pin` mutates the state under
// the core lock and Saves -- rather than through a second Store verb. The flag is not a replay
// guard and is deliberately not merged monotonically: the purge stamp is what protects it from a
// STALE writer, and a caller holding a CURRENT snapshot is by definition one that has seen the
// revoke and is entitled to end it.
func TestD0B8_PairingAgainClearsTheUnpair(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)
	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	// What a pairing does: a fresh read, the new coordinates written, one Save.
	if err := core.Mutate(func(st *State) { st.Disowned = false }); err != nil {
		t.Fatalf("the re-pairing's Mutate: %v", err)
	}

	if core.State().Disowned {
		t.Error("agents-tracker-d0b8: pairing again cannot clear the unpair, so the phone is now stuck " +
			"on the pairing screen it just completed")
	}
	if reopened := s14aR2Resume(t, dir, wake, content).State(); reopened.Disowned {
		t.Error("agents-tracker-d0b8: the cleared unpair did not reach disk, so the re-paired phone is " +
			"offered the pairing screen again on its next launch")
	}
}
