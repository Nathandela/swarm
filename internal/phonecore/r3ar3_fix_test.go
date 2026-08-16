// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, ROUND 3: the review
// findings against the round-2 GREEN (docs/verification/r3-green/android-green-round2.txt).
// Each test states one finding the round-3 review PROVED with a probe, as a permanent
// assertion:
//
//   - BLOCKING: WakeDrops never reached disk in the production topology. The FCM receipt
//     is a process that wakes, drops and dies; a refused wake by definition triggers no
//     adopt/accept/revoke that would persist the container, so "dropped and COUNTED" held
//     only within one process lifetime and the durable counter was 0 forever in the exact
//     scenario it exists to measure. The fix BOUNDS the write cost instead of dropping the
//     requirement: the first drop of a process persists the container once; later drops
//     coalesce onto the next real state change.
//   - LOW: re-adopting an address with a FRESH key inherited the old high-water. Captures
//     made under the old key cannot open under the new one, so the retained coordinate
//     bought no replay protection and silently killed a re-pair that reuses an address.
//     The coordinate is kept when the key is unchanged (the r3ar2 tombstone contract) and
//     reset when the key changes.
package phonecore

import (
	"errors"
	"testing"
	"time"
)

// TestR3AR3_WakeDrops_ARefusedWakeSurvivesProcessDeath: the durable half of "an
// unverifiable wake is dropped and counted, never acted on". On Android the FCM receipt
// is a process that wakes, refuses and dies, so a counter that only reaches disk with the
// next adopt/accept/revoke is a counter that is durably zero in the exact topology it
// exists to measure. The cost stays bounded: the FIRST drop of a process persists, later
// drops in the same process coalesce onto the next real state change.
func TestR3AR3_WakeDrops_ARefusedWakeSurvivesProcessDeath(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xE1)

	// Process 1: one forged wake, then death. This is the FCM topology verbatim.
	forged := r3aSeal(t, key, addr, 1, time.Now())
	forged[70] ^= 0x01 // flip one AEAD tag bit: authentication fails
	if err := core.AcceptWakeV1(forged); err == nil {
		t.Fatal("a tag-flipped envelope was accepted")
	}
	if got := core.WakeDrops(); got != 1 {
		t.Fatalf("in-memory WakeDrops = %d, want 1", got)
	}

	restarted := phone.resume(t)
	if got := restarted.WakeDrops(); got != 1 {
		t.Fatalf("WakeDrops after process death = %d, want 1 (the refused wake was counted "+
			"only in memory; the wake-drop-die process is the scenario the counter exists for)", got)
	}

	// Process 2: three refusals in ONE process. The first persists (the per-process
	// bound); the remaining two are in-memory-first and reach disk with the next real
	// state change, which is the write-amplification bound the round-2 fix was after.
	for i := 0; i < 3; i++ {
		if err := restarted.AcceptWakeV1(forged); err == nil {
			t.Fatal("a tag-flipped envelope was accepted")
		}
	}
	if got := restarted.WakeDrops(); got != 4 {
		t.Fatalf("in-memory WakeDrops = %d, want 4", got)
	}
	again := phone.resume(t)
	// EXACT, not a floor (round-4 LOW finding: a bound hid the gap it was measuring). This
	// process refused three times and its FIRST refusal persisted -- the remaining two are
	// inside the wakeDropPersistEvery budget -- so the durable counter is the one drop from
	// process 1 plus that one write.
	if got := again.WakeDrops(); got != 2 {
		t.Fatalf("WakeDrops after the second process death = %d, want exactly 2 (the first drop "+
			"of every process must reach disk, and the two behind it are inside the write budget)", got)
	}

	// An accepted wake persists the whole container, Drops included: the coalesced
	// remainder rides along with the next real state change.
	if err := again.AcceptWakeV1(r3aSeal(t, key, addr, 5, time.Now())); err != nil {
		t.Fatalf("genuine seq 5: %v", err)
	}
	drops := again.WakeDrops()
	final := phone.resume(t)
	if got := final.WakeDrops(); got != drops {
		t.Fatalf("WakeDrops after an accepted wake persisted the container = %d, want %d "+
			"(the accepted wake must carry the coalesced drops to disk)", got, drops)
	}
}

// TestR3AR3_AdoptPushBinding_AFreshKeyReAdoptResetsTheCoordinate: the per-address
// high-water is a replay guard for envelopes sealed under ONE key. A re-adoption with the
// SAME key must keep it (the r3ar2 tombstone contract: a forget/re-pair of the same
// pairing cannot rewind the window). A re-adoption with a DIFFERENT key must reset it:
// captures under the old key cannot open under the new one, so the retained coordinate
// protects nothing and silently refuses every wake of the new pairing up to the old
// high-water, with no diagnostic.
func TestR3AR3_AdoptPushBinding_AFreshKeyReAdoptResetsTheCoordinate(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, oldKey := r3aBinding(t, core, 0xE2)

	// The first pairing runs its coordinate up to 500, then the phone forgets it.
	if err := core.AcceptWakeV1(r3aSeal(t, oldKey, addr, 500, time.Now())); err != nil {
		t.Fatalf("seq 500 under the first key: %v", err)
	}
	if err := core.DropPushBinding(addr); err != nil {
		t.Fatalf("DropPushBinding: %v", err)
	}

	// A re-pair reuses the address with a FRESH phone-minted key.
	newKey, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	if err := core.AdoptPushBinding(addr, newKey); err != nil {
		t.Fatalf("AdoptPushBinding with a fresh key: %v", err)
	}

	// The new pairing's seq 1 must land: the old coordinate guarded envelopes under a key
	// that no longer exists, and refusing here kills the re-pair with no diagnostic.
	if err := core.AcceptWakeV1(r3aSeal(t, newKey, addr, 1, time.Now())); err != nil {
		t.Fatalf("the new pairing's seq 1 after a fresh-key re-adopt: %v (the stale "+
			"coordinate outlived the key it guarded)", err)
	}

	// A capture from the OLD pairing still opens nothing: wrong key, refused and counted.
	if err := core.AcceptWakeV1(r3aSeal(t, oldKey, addr, 501, time.Now())); err == nil {
		t.Fatal("an old-key capture was accepted after the key change")
	}

	// The reset is DURABLE, and the new pairing's own window still holds: seq 1 is now a
	// replay, seq 2 lands.
	restarted := phone.resume(t)
	if err := restarted.AcceptWakeV1(r3aSeal(t, newKey, addr, 1, time.Now())); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("seq 1 replayed after process death: got %v, want ErrWakeReplay", err)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, newKey, addr, 2, time.Now())); err != nil {
		t.Fatalf("seq 2 under the new key: %v", err)
	}

	// And the SAME-key contract is untouched (r3ar2): drop, re-adopt the same key, and
	// the coordinate held.
	if err := restarted.DropPushBinding(addr); err != nil {
		t.Fatalf("second DropPushBinding: %v", err)
	}
	if err := restarted.AdoptPushBinding(addr, newKey); err != nil {
		t.Fatalf("same-key re-adopt: %v", err)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, newKey, addr, 2, time.Now())); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("seq 2 after a same-key re-adopt: got %v, want ErrWakeReplay (the same-key "+
			"contract must keep the coordinate)", err)
	}
}
