package swarmmobile

// WAVE R8 / ROUND 2 -- THE TERMINAL-VIEW BOUNDS, WITH A PRODUCTION CALLER (round-2 MODERATE 10).
//
// `RemoteProfileV1.TerminalViewBounds()` resolved the ceiling the phone renders under, and
// NOTHING CALLED IT: the three `terminal_view_max_*` profile fields were never written (that
// was blocker 3) and never read. So ADR-017 T5-a's rule -- "any zero bound clamps to a
// conservative built-in, never unlimited" -- was asserted about a function no production path
// reached, and round 1's D-ZERO mutation covered `OffersTerminalView` (which the router does
// use) while leaving the bounds resolver untouched. B94's ledger did not catch it.
//
// `App.Peek` is the production caller: it is where the phone's COPY of a machine-rendered
// screen is made, which is the one place a ceiling can be applied before a view allocates
// against it.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestR8Bounds_AZeroProfileClampsToTheBuiltInRatherThanToUnlimited is T5-a at the resolver.
func TestR8Bounds_AZeroProfileClampsToTheBuiltInRatherThanToUnlimited(t *testing.T) {
	zero := schema.RemoteProfileV1{}.TerminalViewBounds()
	if zero.MaxRows <= 0 || zero.MaxLineBytes <= 0 || zero.MaxRateHz <= 0 {
		t.Fatalf("a ZERO profile resolved bounds %+v. Zero means CLAMP TO THE BUILT-IN, never "+
			"unlimited (T5-a) -- and a zero profile is literally what every machine deployed "+
			"before this wave sends.", zero)
	}
}

// TestR8Bounds_AMachineMayLowerTheCeilingAndMayNotRaiseIt is clampBound's rule, which is the
// whole reason the resolver exists rather than the profile field being read directly.
func TestR8Bounds_AMachineMayLowerTheCeilingAndMayNotRaiseIt(t *testing.T) {
	builtin := schema.RemoteProfileV1{}.TerminalViewBounds()

	lower := schema.RemoteProfileV1{TerminalViewMaxRows: 10}.TerminalViewBounds()
	if lower.MaxRows != 10 {
		t.Errorf("a machine declaring 10 rows resolved %d; a LOWER declaration is believed",
			lower.MaxRows)
	}

	higher := schema.RemoteProfileV1{TerminalViewMaxRows: 1 << 20}.TerminalViewBounds()
	if higher.MaxRows != builtin.MaxRows {
		t.Errorf("a machine declaring %d rows resolved %d, want the built-in %d. A compromised or "+
			"skewed machine does not get to grant itself an unbounded render on the handset.",
			1<<20, higher.MaxRows, builtin.MaxRows)
	}
}

// TestR8Bounds_TheClampAppliesToTheCopyThePhoneHandsAView drives the applier over the exact
// shapes a hostile or broken machine produces.
func TestR8Bounds_TheClampAppliesToTheCopyThePhoneHandsAView(t *testing.T) {
	b := schema.TerminalViewBounds{MaxRows: 3, MaxLineBytes: 4}

	rows := []string{"aaaaaaaaaa", "bb", "cc", "dd", "ee"}
	got := clampSnapshotLines(rows, b)
	if len(got) != 3 {
		t.Fatalf("clampSnapshotLines kept %d rows under a 3-row ceiling", len(got))
	}
	if got[0] != "aaaa" {
		t.Fatalf("clampSnapshotLines kept %q under a 4-byte line ceiling, want %q", got[0], "aaaa")
	}

	// A RUNE IS NEVER SPLIT. Truncating mid-rune would let the clamp manufacture invalid UTF-8
	// out of input that was valid -- a corruption the phone introduced, on a path whose whole
	// job is to be the safe copy.
	multi := clampSnapshotLines([]string{strings.Repeat("é", 10)}, schema.TerminalViewBounds{MaxRows: 1, MaxLineBytes: 5})
	if !utf8.ValidString(multi[0]) {
		t.Fatalf("the byte clamp split a rune: %q is not valid UTF-8", multi[0])
	}
	if len(multi[0]) > 5 {
		t.Fatalf("the byte clamp kept %d bytes under a 5-byte ceiling", len(multi[0]))
	}

	// AND IT IS NOT A FLOOR: a short grid is handed through unchanged.
	short := clampSnapshotLines([]string{"ab"}, b)
	if len(short) != 1 || short[0] != "ab" {
		t.Fatalf("clampSnapshotLines altered a grid already inside the bounds: %q", short)
	}
}

// TestR8Bounds_PeekResolvesAndAppliesTheBounds is the WIRING fence, and it is written over
// the body of `Peek` rather than over the file: a whole-file grep for `clampSnapshotLines`
// would be satisfied by that function's own declaration, which is the exact defect class the
// wave's rule 4 names.
func TestR8Bounds_PeekResolvesAndAppliesTheBounds(t *testing.T) {
	body := funcBody(readMobileSource(t, "app.go"), "func (a *App) Peek(")
	for _, want := range []string{"TerminalViewBounds()", "clampSnapshotLines("} {
		if !strings.Contains(body, want) {
			t.Errorf("App.Peek's body does not contain %q, so the resolved TerminalView ceiling is "+
				"never applied to the copy the phone hands a view. A bound with no production "+
				"caller is a rule asserted about a function nothing calls.", want)
		}
	}
	// The vacuity guard for the extractor itself: a body that did not extract would contain
	// none of the things Peek certainly does.
	if !strings.Contains(body, "Snapshots().Get(") {
		t.Fatalf("the body extractor did not find App.Peek; every assertion above would be vacuous")
	}
}
