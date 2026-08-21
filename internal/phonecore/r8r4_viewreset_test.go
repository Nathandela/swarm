package phonecore

// WAVE R8 / CLOSING ROUND -- THE PHONE'S HALF OF T4-a (closing review, finding 5).
//
// The epoch and the instance are now on the wire. This is the rule that makes them mean
// something on the phone, and it is stated as ADR-017 T4-a states it:
//
//	differing epoch  = HARD RESET, discard prior state
//	same epoch       = strictly greater revision only
//
// WHY "LATEST WINS" IS NOT THAT RULE, and why the cache could not simply keep doing what it
// did. Latest-wins is right for a replacement and WRONG for a reorder: two frames of one
// epoch that arrive out of order leave the phone showing the older screen, permanently, with
// nothing saying so. And the rule cannot be "highest revision wins" either, because the epoch
// restarts the revision at 1 -- which is the frozen-screen defect T4-a exists to name: a phone
// holding revision N discards every frame of the next render loop and reads a plausible screen
// that stopped being true minutes ago.

import (
	"testing"
	"time"
)

// TestR8R4Snapshot_AReorderedFrameOfTheSameEpochDoesNotOverwriteANewerOne is the "strictly
// greater revision only" half.
func TestR8R4Snapshot_AReorderedFrameOfTheSameEpochDoesNotOverwriteANewerOne(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 7, Revision: 2,
		Lines: []string{"newer"},
	})
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 7, Revision: 1,
		Lines: []string{"older"},
	})
	got, ok := c.Get("m/s1")
	if !ok {
		t.Fatalf("the cache holds nothing for m/s1")
	}
	if got.Revision != 2 || got.Lines[0] != "newer" {
		t.Errorf("ADR-017 T4-a: a revision-1 frame overwrote the revision-2 frame of the SAME "+
			"epoch. Within one epoch the revision is strictly increasing and the phone's rule is "+
			"`strictly greater only`; latest-wins leaves the user reading the older screen with "+
			"nothing saying so.\ngot revision %d lines %v", got.Revision, got.Lines)
	}
}

// TestR8R4Snapshot_ANewEpochIsAHardResetEvenAtALowerRevision is the half the naive fix breaks,
// and it is the one the ruling is actually about: a session REPLACED under the same id.
func TestR8R4Snapshot_ANewEpochIsAHardResetEvenAtALowerRevision(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 7, Revision: 40,
		Lines: []string{"the incarnation the user was reading"},
	})
	// The session is REPLACED: a new render loop, a new epoch, a new incarnation, and a
	// revision that starts again at 1.
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-2", ViewEpoch: 8, Revision: 1, Reset: true,
		Lines: []string{"the incarnation that replaced it"},
	})
	got, _ := c.Get("m/s1")
	if got.ViewEpoch != 8 || got.SessionInstance != "inst-2" || got.Lines[0] != "the incarnation that replaced it" {
		t.Errorf("ADR-017 T4-a/T8-a: the phone still holds epoch %d instance %q lines %v after the "+
			"session was replaced under the same id. A rule written as `highest revision wins` "+
			"discards every frame of the new render loop and leaves a plausible screen that stopped "+
			"being true -- which is the exact failure the epoch exists to make legible.",
			got.ViewEpoch, got.SessionInstance, got.Lines)
	}
}

// TestR8R4Snapshot_ABlankFromTheMachineAlwaysLands is the reap path, and it must not be
// mistaken for a stale frame. `BlankTerminal` publishes an EMPTY view with no epoch at all --
// the machine saying "I am no longer rendering this" to a phone that never asked it to stop --
// and a phone that dropped it as unversioned would keep showing a dead grid it calls fresh.
func TestR8R4Snapshot_ABlankFromTheMachineAlwaysLands(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 7, Revision: 40,
		Lines: []string{"the grid the user is looking at"},
	})
	c.Apply(Snapshot{Session: "m/s1"}) // the blank: no epoch, no revision, no lines
	got, _ := c.Get("m/s1")
	if len(got.Lines) != 0 {
		t.Errorf("ADR-017 T4-b: the machine's blanking frame did not land; the phone still holds "+
			"%v and is still labelling it live.", got.Lines)
	}
}

// TestR8R4Snapshot_TheMachinesRenderTimeSurvivesTheCache is F6's precondition: a screen can
// only say it is stale if it knows when it was rendered, and ARRIVAL time is not that -- a
// replayed backlog arrives all at once and a held relay delivers old content at a new instant.
func TestR8R4Snapshot_TheMachinesRenderTimeSurvivesTheCache(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	c := NewSnapshotCache()
	c.Apply(Snapshot{Session: "m/s1", ViewEpoch: 1, Revision: 1, RenderedAt: at, Lines: []string{"x"}})
	got, _ := c.Get("m/s1")
	if !got.RenderedAt.Equal(at) {
		t.Errorf("the machine's rendered_at did not survive the cache: got %v want %v. Without it "+
			"the fallback screen cannot distinguish a frozen screen from an idle one, which is the "+
			"worst failure mode this surface has.", got.RenderedAt, at)
	}
}
