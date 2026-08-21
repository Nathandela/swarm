package daemon

// WAVE R8 / SLICE S4 -- THE VIEW EPOCH, THE REVISION, AND THE RESET MARKER.
// Failing-first (TDD RED, GG-5).
//
// THE DEFECT AMENDMENT T4-a IS ABOUT, AND IT IS NOT HYPOTHETICAL. `RenderTerminal` is PER
// INVOCATION: it builds a fresh `vt.Emulator` on every call (terminalrender.go:88) and seeds
// it from whatever initial snapshot the stream happens to carry. The gateway's watcher
// RE-RUNS it after every transport hiccup -- `internal/remotegw/terminal_watcher.go:149-165`
// loops `RunTerminal` with a backoff until its ctx is cancelled, forever, silently. So the
// second run's snapshots are a NEW screen with a counter that starts again.
//
// Give those snapshots a bare monotonic revision and the phone's only sane rule -- "drop
// anything not strictly greater than what I hold" -- discards every snapshot of the second
// run. There is no error, no reconnect banner, nothing to report: the user is looking at a
// plausible screen that stopped being true minutes ago. On a session they may be about to
// type into, that is the worst failure this wave can ship, because it is invisible on both
// sides.
//
// So: a VIEW EPOCH minted per render-loop start, a REVISION strictly increasing within it,
// `reset=true` on the FIRST SNAPSHOT OF EVERY EPOCH ON EVERY PATH -- explicitly including the
// path where the initial snapshot fails to decode and today pushes nothing at all
// (terminalrender.go:140-147) -- and exactly one producer per session.
//
// THE SEAM (undefined symbols -> compile-fail RED):
//
//	type TerminalView struct {
//	    Session, SessionInstance string
//	    ViewEpoch, Revision      uint64
//	    Reset                    bool
//	    Lines                    []string
//	    Cols, Rows               int
//	    RenderedAt               time.Time
//	}
//	func RenderTerminalView(ctx context.Context, session, instance string, stream TerminalStream,
//	        stillAllowed func() bool, push func(TerminalView))
//
// `RenderTerminal` and `TerminalRender` ARE deleted, as of the CLOSING round. They survived
// the first three rounds as the legacy projection of this loop; once the peek handler drove
// this function directly and built both wire bodies from one view, the projection had no
// production caller and B94's whole-repo reachability gate failed on it. The legacy
// `TerminalSnapshot` BODY is still on the wire byte for byte -- what went away is a second
// Go-side shape of it. `terminalrender_test.go`'s render corpus now drives THIS function.

import (
	"context"
	"testing"
	"time"
)

// viewCollector accumulates pushed views. Same shape as `collector`, over the new type.
type viewCollector struct {
	mu  chan struct{}
	got []TerminalView
}

func newViewCollector() *viewCollector {
	c := &viewCollector{mu: make(chan struct{}, 1)}
	c.mu <- struct{}{}
	return c
}

func (c *viewCollector) push(v TerminalView) {
	<-c.mu
	c.got = append(c.got, v)
	c.mu <- struct{}{}
}

func (c *viewCollector) views() []TerminalView {
	<-c.mu
	defer func() { c.mu <- struct{}{} }()
	return append([]TerminalView(nil), c.got...)
}

// r8RunView drives one render-loop run to completion over a closed frame channel, which is
// exactly the shape of a run that ends because the transport dropped.
func r8RunView(t *testing.T, session, instance string, snap []byte, feed [][]byte) []TerminalView {
	t.Helper()
	frames := make(chan []byte, len(feed)+1)
	for _, f := range feed {
		frames <- f
	}
	close(frames)
	c := newViewCollector()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	RenderTerminalView(ctx, session, instance, &stubTerminalStream{snap: snap, frames: frames}, alwaysAllowed, c.push)
	return c.views()
}

// TestR8View_FirstSnapshotOfAnEpochCarriesReset is T4-a's marker rule, over BOTH paths.
//
// The decode-failure row is the one the amendment calls out by line number, and it is why
// "reset on the first emission" must be LOOP STATE rather than a property of whichever
// function happened to push. `renderInitial` returns nil and pushes NOTHING when the initial
// snapshot cannot be decoded, so on that path the epoch's first snapshot arrives from
// `renderEmulator`, which has no idea it is first.
func TestR8View_FirstSnapshotOfAnEpochCarriesReset(t *testing.T) {
	cases := []struct {
		name string
		snap []byte
		why  string
	}{
		{
			name: "decodable_initial_snapshot",
			snap: snapBytes(t, 40, 10, []byte("READY")),
			why:  "the ordinary path: renderInitial pushes the seeded screen first",
		},
		{
			name: "undecodable_initial_snapshot",
			snap: []byte("{not a snapshot"),
			why: "terminalrender.go:140-147 pushes NOTHING on a decode failure, so the epoch's first " +
				"snapshot comes from renderEmulator, which cannot know it is first unless the loop " +
				"tracks it as state",
		},
		{
			name: "empty_initial_snapshot",
			snap: nil,
			why:  "an empty stream is the same path as a corrupt one and must not be a different rule",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			views := r8RunView(t, "sess1", "inst-a", tc.snap, [][]byte{[]byte("LIVE")})
			if len(views) == 0 {
				t.Fatalf("ADR-017 T4-a: the render loop emitted NOTHING (%s). An epoch with no snapshot "+
					"leaves the phone with nothing to reset to.", tc.why)
			}
			if !views[0].Reset {
				t.Errorf("ADR-017 T4-a: the FIRST snapshot of an epoch does not carry reset=true (%s). "+
					"Without it the phone folds a fresh screen into a stale one and renders a composite "+
					"that never existed on the machine.", tc.why)
			}
			for i, v := range views[1:] {
				if v.Reset {
					t.Errorf("snapshot %d of the same epoch also carries reset=true; reset is the marker "+
						"for the FIRST snapshot of an epoch, and a stream of resets makes the phone "+
						"discard its own state on every frame", i+1)
				}
			}
		})
	}
}

// TestR8View_RevisionIsStrictlyIncreasingWithinAnEpoch is the half a bare counter already
// gives, pinned so the epoch work cannot cost it.
func TestR8View_RevisionIsStrictlyIncreasingWithinAnEpoch(t *testing.T) {
	views := r8RunView(t, "sess1", "inst-a", snapBytes(t, 40, 10, []byte("A")),
		[][]byte{[]byte("B"), []byte("C"), []byte("D")})
	if len(views) < 2 {
		t.Fatalf("want at least two snapshots to compare revisions, got %d", len(views))
	}
	for i := 1; i < len(views); i++ {
		if views[i].ViewEpoch != views[0].ViewEpoch {
			t.Errorf("snapshot %d changed epoch mid-run (%d -> %d); the epoch is minted per render-loop "+
				"START and changes only on a re-seed or an instance change",
				i, views[0].ViewEpoch, views[i].ViewEpoch)
		}
		if views[i].Revision <= views[i-1].Revision {
			t.Errorf("revision went %d -> %d at snapshot %d; within one epoch the phone accepts only a "+
				"STRICTLY greater revision, so a repeat or a decrease is a snapshot the user never sees",
				views[i-1].Revision, views[i].Revision, i)
		}
	}
}

// TestR8View_ARerunMintsANewEpoch is the mutation fence D-EPOCH names: mint the counter in
// `renderEmulator` instead of at loop start and this test shows the frozen-screen defect.
//
// The two runs below are exactly what the watcher does across a transport hiccup: same
// session, same instance, a brand-new emulator seeded from a brand-new initial snapshot.
func TestR8View_ARerunMintsANewEpoch(t *testing.T) {
	first := r8RunView(t, "sess1", "inst-a", snapBytes(t, 40, 10, []byte("FIRST")), [][]byte{[]byte("X")})
	second := r8RunView(t, "sess1", "inst-a", snapBytes(t, 40, 10, []byte("SECOND")), [][]byte{[]byte("Y")})
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("both runs must emit; got %d and %d", len(first), len(second))
	}
	if first[0].ViewEpoch == second[0].ViewEpoch {
		t.Errorf("ADR-017 T4-a: a re-run of the render loop reused epoch %d. The watcher re-runs "+
			"RunTerminal after EVERY transport hiccup (terminal_watcher.go:149-165) with a fresh "+
			"emulator; a reused epoch plus a restarted revision makes the phone's 'drop anything not "+
			"greater' rule discard the entire second run, and the user reads a frozen screen with no "+
			"error on either side.", first[0].ViewEpoch)
	}
	if !second[0].Reset {
		t.Errorf("the first snapshot of the SECOND epoch must carry reset=true: it is a different screen")
	}
}

// TestR8View_EveryViewCarriesItsSessionInstance is T8-a at the snapshot.
//
// Without it the phone cannot tell a re-seed of the same incarnation from a REPLACEMENT, and
// T8's "session replacement / instance change" severance trigger has nothing to fire on.
func TestR8View_EveryViewCarriesItsSessionInstance(t *testing.T) {
	views := r8RunView(t, "sess1", "inst-a", snapBytes(t, 40, 10, []byte("A")), [][]byte{[]byte("B")})
	if len(views) == 0 {
		t.Fatalf("no views")
	}
	for i, v := range views {
		if v.SessionInstance != "inst-a" {
			t.Errorf("view %d carries session_instance %q, want %q. A snapshot that names only the "+
				"session id cannot tell the phone that the session it is watching was REPLACED, so a "+
				"new PTY arrives as a continuation of the old screen.", i, v.SessionInstance, "inst-a")
		}
		if v.Session != "sess1" {
			t.Errorf("view %d carries session %q, want sess1", i, v.Session)
		}
		if v.RenderedAt.IsZero() {
			t.Errorf("view %d carries no RenderedAt. ADR-017 T4-b's staleness indicator is derived from "+
				"the snapshot's OWN age; arrival time renders a quiet machine as an idle terminal.", i)
		}
	}
}

// TestR8View_LivenessIsPolledEveryTickAndBeforeEveryEmission is T4-b + T6-e over the
// predicate the loop already polls.
//
// `stillAllowed` exists and is polled every tick (terminalrender.go:121-127) -- that half is
// green and this test keeps it green. What is new is that the SAME predicate must gate the
// emission itself, so a watch that dies between the tick and the push does not ship one last
// screen, and that the predicate carries the watch lease and the capability alongside the
// kill switch rather than the kill switch alone.
func TestR8View_LivenessIsPolledEveryTickAndBeforeEveryEmission(t *testing.T) {
	frames := make(chan []byte, 4)
	frames <- []byte("BEFORE")
	c := newViewCollector()
	allowed := make(chan bool, 1)
	allowed <- true
	live := true
	stillAllowed := func() bool {
		select {
		case v := <-allowed:
			live = v
		default:
		}
		return live
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RenderTerminalView(ctx, "sess1", "inst-a", &stubTerminalStream{snap: snapBytes(t, 40, 10, nil), frames: frames}, stillAllowed, c.push)
	}()

	// Withdraw the permission with NO further phone action and NO ctx cancel: the loop must
	// return on its own tick.
	time.Sleep(50 * time.Millisecond)
	allowed <- false
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("ADR-017 T4-b/T6-e: the render loop did not return within 2s of its liveness predicate " +
			"going false. A watch whose lease expired, whose capability was withdrawn or whose session " +
			"was replaced must stop rendering, sealing and appending on the loop's OWN clock -- the " +
			"phone may be gone, which is the case the horizon exists for.")
	}
	close(frames)
}
