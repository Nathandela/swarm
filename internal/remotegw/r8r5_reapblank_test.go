package remotegw

// WAVE R8 / CLOSING ROUND 2 -- THE BLANK AND THE LAPSE DETECTOR, MEASURED TOGETHER
// (closing review, finding 6, second pass).
//
// THE DEFECT THIS FENCE ANSWERS. Round 3 gave the reaper a blank ("the machine says it stopped
// rendering this") and round 4 gave the phone a lapse detector ("my screen aged past the
// horizon, so re-watch instead of renewing into nothing"). Nothing drove the two together, and
// they DISAGREED: the blank carries a ZERO rendered_at, the phone reads zero as UNKNOWN rather
// than as old, and a detector written over the age alone therefore answers "not lapsed" for the
// one frame that proves the watch is over. The user sat on a permanently blank terminal.
//
// WHAT IS FENCED HERE, and it is the machine's half stated in the phone's terms: the frame the
// REAL gateway publishes when the REAL watcher reaps a watch is distinguishable from a live one
// by its GEOMETRY -- a live view has columns and rows, the blank has none. The phone's rule reads
// exactly that (TerminalFallbackBinding.watchLapsed(grid) / TerminalGrid.machineStoppedRendering),
// and it cannot read the age, because zero means "this machine sent no render time" and stamping
// a render time on a blank would make the blank look FRESH -- which is the same disagreement with
// the sign flipped.
//
// THE SEAM IS DELIBERATE AND NAMED. No single test spans the gomobile boundary, so the property
// is held by three tests that meet at one set of values: this one (the machine really sends
// them, over a real ServeRemote + a real Gateway + the real TerminalWatcher), phonecore's
// TestR8R5Snapshot_TheMachinesBlankCarriesNoGeometryAndNoRenderTime (they survive the phone's
// cache), and Kotlin's TerminalFallbackWatchTest (the rule that reads them answers LAPSED).

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestR8R5_AReapedWatchLeavesThePhoneAFrameItCanTellFromALiveOne drives the production
// composition: TerminalWatcher over the REAL *Gateway (its runner AND its blanker), peeking a
// real remote-tier daemon.
func TestR8R5_AReapedWatchLeavesThePhoneAFrameItCanTellFromALiveOne(t *testing.T) {
	sock, backend := serveR8Remote(t)
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true,
	})
	backend.setStream("sess1", &r8r4Stream{snap: r8r4Grid("the screen the user is reading"), frames: make(chan []byte)})

	sink := &r8r4Sink{}
	gw := New(sock, sink)
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	const horizon = 30 * time.Second
	w := newTerminalWatcher(gw, time.Millisecond, horizon, clk.now)
	t.Cleanup(func() { _ = w.Close() }) // joins every peek goroutine (standing constraint 9)

	session := r8Endpoint + "/sess1"
	w.Watch(session)

	live, ok := waitForLiveView(t, sink)
	if !ok {
		t.Fatalf("no rendered view reached the sink within 5s (got %d frames)", len(sink.all()))
	}
	// NON-VACUITY: the discriminator only means something if a LIVE view really carries the
	// geometry a blank lacks. A live view with zero rows would make the phone's rule fire on
	// every frame, which is a re-watch storm rather than a fix.
	if live.Cols <= 0 || live.Rows <= 0 {
		t.Fatalf("the LIVE view carries cols=%d rows=%d; the phone tells the machine's blank from a "+
			"live screen by its geometry, and a live screen with none makes that rule fire always.",
			live.Cols, live.Rows)
	}

	// THE HORIZON PASSES with the screen still up: the phone renewed nothing (a descheduled UI
	// thread, a slow relay), so the machine reaps.
	clk.advance(horizon + time.Second)
	w.Reap()

	all := sink.all()
	last := all[len(all)-1]
	if last.Session != session {
		t.Fatalf("the last frame the phone received is for %q, want the reaped session %q", last.Session, session)
	}
	if len(last.Lines) != 0 || last.Cols != 0 || last.Rows != 0 || !last.RenderedAt.IsZero() {
		t.Errorf("ADR-017 T4-b: the frame a REAPED watch leaves on the phone is %+v.\n"+
			"The phone must be able to tell it from a live screen WITHOUT reading its age: zero "+
			"rendered_at means `this machine sent no render time` (a pre-closing-round machine), and "+
			"a zero age reads as `nothing to say`, so a lapse detector written over the age alone "+
			"answers FALSE forever on exactly this frame -- the machine reaps, the blank lands, and "+
			"the user sits on a blank terminal that never re-watches. The geometry is what carries "+
			"the difference: no cols, no rows, no lines.", last)
	}
}

// waitForLiveView polls the sink for the first view carrying rendered rows.
func waitForLiveView(t *testing.T, sink *r8r4Sink) (protocol.TerminalViewV1, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := sink.firstNonEmpty(); ok {
			return v, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return protocol.TerminalViewV1{}, false
}
