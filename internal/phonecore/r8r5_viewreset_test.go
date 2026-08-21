package phonecore

// WAVE R8 / CLOSING ROUND 2 -- THE RESET MARKER IS ON THE WIRE AND NOTHING READ IT.
//
// THE FINDING, stated as what the user sees. `viewEpochSeq` (internal/daemon/terminalview.go)
// is a bare process-global counter minted per render-loop start, so it RESTARTS AT 1 IN EVERY
// DAEMON PROCESS -- and sessions surviving a daemon crash, restart or upgrade is a designed
// property of this system, not an edge case. `SnapshotCache.Apply` compared ONLY (epoch,
// revision), so after a restart the phone holding {epoch 1, revision 40} discarded the new
// daemon's {epoch 1, revision 1} and the 39 revisions after it: the user reads a plausible,
// frozen, PRE-RESTART terminal, with nothing on either side saying so.
//
// That is the SAME failure class T4-a names -- "a plausible screen that stopped being true" --
// and the closing round fenced only its session-replacement variant, which is the variant where
// the epoch happens not to collide.
//
// THE FIX IS THE MARKER THE PROTOCOL ALREADY CARRIES. `reset` is documented in
// docs/specifications/protocol.md:275 as "true on the FIRST snapshot of every epoch, on every
// path" and ADR-017 T4-a calls it what "tells the phone to discard prior state rather than merge
// a new screen into an old one". The daemon has always sent it (r8r4_viewwire_test.go asserts it
// over the real gateway) and the phone has always decoded it (snapshot.go's snapshotFrame). Only
// the ordering rule ignored it. Reading it makes the epoch's process-locality harmless: a
// colliding epoch is still a hard reset, because the machine SAYS it is one.

import (
	"testing"
	"time"
)

// TestR8R5Snapshot_ADaemonRestartsResetFrameLands is the finding's own scenario, on the real
// cache: same epoch number, LOWER revision, and the machine's reset marker.
func TestR8R5Snapshot_ADaemonRestartsResetFrameLands(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 1, Revision: 40,
		Lines: []string{"OLD screen from before the restart"},
	})
	// THE RESTART. A fresh daemon process mints epoch 1 again (the counter is per process) and
	// its first emission is revision 1 with reset set.
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 1, Revision: 1, Reset: true,
		Lines: []string{"NEW screen the restarted daemon rendered"},
	})

	got, ok := c.Get("m/s1")
	if !ok {
		t.Fatalf("the cache holds nothing for m/s1")
	}
	if got.Revision != 1 || len(got.Lines) != 1 || got.Lines[0] != "NEW screen the restarted daemon rendered" {
		t.Errorf("ADR-017 T4-a: the machine's RESET frame was dropped as a stale revision, so the "+
			"phone still holds epoch=%d rev=%d lines=%v after a daemon restart. `reset` is the "+
			"machine saying `discard prior state`; the epoch counter is process-local and a "+
			"restarted daemon re-mints epoch 1 under a phone holding revision N, which is exactly "+
			"the frozen screen T4-a exists to prevent -- and every revision after this one is "+
			"dropped too, because they are all lower than the 40 the phone is still holding.",
			got.ViewEpoch, got.Revision, got.Lines)
	}
}

// TestR8R5Snapshot_TheRevisionsAfterAResetAreAdoptedToo is the half that makes the fix a fix
// rather than a single frame slipping through: after the reset lands, the cache must be
// following the NEW run's revision line, not the old one's high-water mark.
func TestR8R5Snapshot_TheRevisionsAfterAResetAreAdoptedToo(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{Session: "m/s1", ViewEpoch: 1, Revision: 40, Lines: []string{"pre-restart"}})
	c.Apply(Snapshot{Session: "m/s1", ViewEpoch: 1, Revision: 1, Reset: true, Lines: []string{"post-restart 1"}})
	c.Apply(Snapshot{Session: "m/s1", ViewEpoch: 1, Revision: 2, Lines: []string{"post-restart 2"}})

	got, _ := c.Get("m/s1")
	if got.Revision != 2 || got.Lines[0] != "post-restart 2" {
		t.Errorf("the phone holds rev=%d lines=%v after the restarted daemon's SECOND emission; a "+
			"reset that lands while the old high-water mark survives leaves the screen frozen one "+
			"frame later instead of never.", got.Revision, got.Lines)
	}
}

// TestR8R5Snapshot_AResetIsNotAWayToReplayAnOldScreen is the vacuity guard, and it is the
// property that keeps the reorder rule from being repealed: within a live epoch a LATE frame
// still loses to the newer one it arrived behind. Only a frame the machine MARKED as the first
// of a run may go backwards.
func TestR8R5Snapshot_AResetIsNotAWayToReplayAnOldScreen(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{Session: "m/s1", ViewEpoch: 7, Revision: 2, Lines: []string{"newer"}})
	c.Apply(Snapshot{Session: "m/s1", ViewEpoch: 7, Revision: 1, Lines: []string{"older"}})

	got, _ := c.Get("m/s1")
	if got.Revision != 2 || got.Lines[0] != "newer" {
		t.Errorf("ADR-017 T4-a's reorder rule was repealed by the reset fix: a revision-1 frame with "+
			"NO reset marker overwrote revision 2 of the same epoch. got %v", got.Lines)
	}
}

// TestR8R5Snapshot_TheMachinesBlankCarriesNoGeometryAndNoRenderTime pins the THREE facts the
// phone's fallback screen reads to tell "the machine stopped rendering this" from "the terminal
// is idle": no lines, no geometry, and no render time.
//
// It is the phone-side end of internal/remotegw's
// TestR8R5_AReapedWatchLeavesThePhoneAFrameItCanTellFromALiveOne, which proves those are the
// values the REAL gateway sends when the REAL watcher reaps a watch; and the rule that reads
// them is TerminalFallbackBinding.watchLapsed(grid), unit-tested in
// android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TerminalFallbackWatchTest.kt. The three
// meet here because no single test can span the gomobile boundary.
func TestR8R5Snapshot_TheMachinesBlankCarriesNoGeometryAndNoRenderTime(t *testing.T) {
	c := NewSnapshotCache()
	c.Apply(Snapshot{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 7, Revision: 40,
		Cols: 80, Rows: 24, RenderedAt: time.Now().UTC(),
		Lines: []string{"the grid the user is looking at"},
	})
	c.Apply(Snapshot{Session: "m/s1"}) // exactly what Gateway.BlankTerminal publishes

	got, _ := c.Get("m/s1")
	if len(got.Lines) != 0 || got.Cols != 0 || got.Rows != 0 || !got.RenderedAt.IsZero() {
		t.Errorf("the blank the machine sends on reap did not fully replace the held grid: "+
			"lines=%v cols=%d rows=%d rendered_at=%v. The phone tells a blanked screen from a live "+
			"one by its GEOMETRY, because a zero rendered_at is indistinguishable from a machine "+
			"that predates the closing round and a zero age reads as `nothing to say`.",
			got.Lines, got.Cols, got.Rows, got.RenderedAt)
	}
}
