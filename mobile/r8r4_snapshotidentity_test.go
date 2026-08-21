package swarmmobile

// WAVE R8 CLOSING ROUND -- THE STALENESS FIELDS SURVIVE THE FACADE (finding 12).
//
// HOW THIS WAS FOUND, because the provenance is the point. The closing round wired
// `rendered_at` and `session_instance` end to end -- daemon render loop, `TerminalViewV1` on
// the wire, `phonecore.Snapshot`, `swarmmobile.Snapshot`, and `TerminalFallbackScreen.grid()`,
// which reads `snap.renderedAtMillis` and derives BOTH the age it prints AND `watchLapsed`,
// the predicate that decides whether a lapsed watch is re-established. Every link had a fence
// EXCEPT the last Go one: `App.Peek` builds the bound `Snapshot` the phone actually reads, and
// nothing asserted it copied the two new fields across.
//
// It was measured, not reasoned about: with `Peek` reverted to the shape that predates the
// closing round, `go test ./mobile/...` was GREEN. The Kotlin gate
// (`android/gate/r8r4_staleness_test.go`) asserts the SCREEN READS the field; no test asserted
// the FACADE WRITES it. That is finding 7's defect class one package over -- a property with
// two fences, either of which can pass while the property is false -- and the consequence is
// not cosmetic: a dropped `RenderedAtMillis` is a zero, `ageOf` reads zero as UNKNOWN, and
// `watchLapsed(0)` is false FOREVER. Both halves of the closing round's finding 6 -- the honest
// staleness indicator AND the re-watch after a machine-side reap -- would be silently dead,
// with every other test in the wave still green.
//
// THE FENCE IS BEHAVIOURAL AND NOT A SOURCE SCAN. `Peek` is driven over a real
// `phonecore` core and a real snapshot cache, and the assertion is on the value the facade
// hands the binding.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
)

// TestR8R4Peek_TheMachinesRenderTimeAndIncarnationReachTheBoundSnapshot is finding 12.
func TestR8R4Peek_TheMachinesRenderTimeAndIncarnationReachTheBoundSnapshot(t *testing.T) {
	a := transcriptApp(t)

	rendered := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Millisecond)
	a.core.Router().Snapshots().Apply(phonecore.Snapshot{
		Session:         "m1/sess1",
		Lines:           []string{"a screen the machine rendered ninety seconds ago"},
		Cols:            80,
		Rows:            24,
		SessionInstance: "inst-1",
		ViewEpoch:       7,
		Revision:        3,
		RenderedAt:      rendered,
	})

	snap, err := a.Peek("m1/sess1")
	if err != nil {
		t.Fatalf("App.Peek: %v", err)
	}

	if snap.RenderedAtMillis != rendered.UnixMilli() {
		t.Errorf("ADR-017 T4-b: App.Peek handed the phone RenderedAtMillis=%d, want %d (the "+
			"machine's own render time).\nThe fallback screen derives BOTH the age it prints and "+
			"`watchLapsed` from this field. A zero is read as UNKNOWN, so `watchLapsed(0)` is false "+
			"forever: the honest staleness indicator AND the re-watch after a machine-side reap are "+
			"both dead, and every other fence in this wave stays green while they are.",
			snap.RenderedAtMillis, rendered.UnixMilli())
	}
	if snap.SessionInstance != "inst-1" {
		t.Errorf("ADR-017 T8-a: App.Peek handed the phone SessionInstance=%q, want %q. Without the "+
			"incarnation the phone cannot tell the session it is watching from the one that REPLACED "+
			"it under the same id, which is the correctness property of the READ half that T4-a and "+
			"T8-a exist for.", snap.SessionInstance, "inst-1")
	}
}

// TestR8R4Peek_AnUnversionedSnapshotStaysZeroRatherThanBeingInvented is the other direction,
// and it is what stops the fence above being satisfied by a fabricated timestamp.
//
// A machine that predates the closing round sends no `rendered_at`. Zero must survive as zero
// -- "this machine does not version its views" -- because `ageOf` reads zero as UNKNOWN and
// prints it as unknown. Stamping `time.Now()` here would make every such machine's screen read
// as freshly rendered, which is the exact lie T4-b names.
func TestR8R4Peek_AnUnversionedSnapshotStaysZeroRatherThanBeingInvented(t *testing.T) {
	a := transcriptApp(t)

	a.core.Router().Snapshots().Apply(phonecore.Snapshot{
		Session: "m1/legacy",
		Lines:   []string{"a screen from a machine that predates the closing round"},
		Cols:    80,
		Rows:    24,
	})

	snap, err := a.Peek("m1/legacy")
	if err != nil {
		t.Fatalf("App.Peek: %v", err)
	}
	if snap.RenderedAtMillis != 0 {
		t.Errorf("ADR-017 T4-b: an UNVERSIONED snapshot came back with RenderedAtMillis=%d. Zero "+
			"means the machine sent no render time and must survive as zero; substituting the "+
			"phone's own clock reports an arbitrarily old screen as rendered just now.",
			snap.RenderedAtMillis)
	}
	if snap.SessionInstance != "" {
		t.Errorf("an unversioned snapshot came back naming incarnation %q", snap.SessionInstance)
	}
}
