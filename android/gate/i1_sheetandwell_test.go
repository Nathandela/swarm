package gate

// Slice I1 exit, the phone half: the approval sheet says what the item carries, and the
// plain-text terminal well is deleted.
//
// Both halves are ONE file because they are one decision. `docs/adr/ADR-009-structured-chat-
// interaction.md` (3) dates the well's deletion to I1's exit -- "a fallback surface that outlives
// its replacement stops being a fallback and becomes the design" -- and (1) is what replaces it:
// the phone's only session surface is the structured transcript, plus the approval card and the
// prompt card. A run that deleted the grid and left the sheet unable to state a question would
// have removed a surface and shipped nothing in its place.
//
// WHY THESE ARE SOURCE SCANS AND NOT ROBOLECTRIC ASSERTIONS. The Kotlin suite asserts what a
// rendered tree contains; it cannot assert what the app NO LONGER CONTAINS anywhere, which is
// exactly what a deletion obligation is. `PeekPanelViewTest` was green about a screen ADR-009
// retires, and it would have stayed green forever had the screen merely stopped being composed.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// HALF 1 -- the approval sheet, over the approval_request item.
// ---------------------------------------------------------------------------

const (
	i1SheetPanel = "dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt"
	i1SheetView  = "dev/swarm/phone/ui/screens/ApprovalSheetView.kt"
	i1SheetItem  = "dev/swarm/phone/ui/ApprovalItem.kt"
)

// i1RetiredGaps are the three refusals `ApprovalSheetPanel.kt` recorded because the wire carried
// nothing to fill them, quoted from the file as it stood before this slice.
//
// THEY ARE ASSERTED ABSENT RATHER THAN THE REPLACEMENTS ASSERTED PRESENT, and both directions are
// checked below for that reason. A doc comment that still says "THERE IS NO APPROVE VERB" over a
// model that now carries decisions is worse than either state alone: the next reader believes the
// comment, which is the failure mode ADR-009's own Notes section names ("so the next agent to read
// `PeekPanel.kt` and find it deleted meets a ruling instead of a gap").
var i1RetiredGaps = []string{
	"THERE IS NO APPROVE VERB",
	"Nothing on this wire carries the literal",
	"`swarmmobile.Session.Need` is",
}

// TestADR009_TheApprovalSheetReadsTheApprovalRequestItem is HALF 1's fence.
//
// `interaction-schema.md` §3.5 puts three things on the item that the sheet refused to invent: a
// `summary` for the headline, an `action` (§7) so the card reads like a tool card, and
// `decisions[]` whose `label` the card puts on its buttons (IS-APR-3). The panel is the model that
// reads them, so the check is over the model's own shape.
func TestADR009_TheApprovalSheetReadsTheApprovalRequestItem(t *testing.T) {
	src := i1Source(t, i1SheetPanel)
	code := kotlinCodeOnly(src)

	for _, gap := range i1RetiredGaps {
		if strings.Contains(src, gap) {
			t.Errorf("ADR-009: %s still records %q. The wire now carries the prompt, the action "+
				"and the decisions (interaction-schema.md §3.5), so that paragraph documents a "+
				"gap this slice filled -- and a stale refusal reads to the next agent as a rule.",
				i1SheetPanel, gap)
		}
	}

	// The model's own shape. `actions` is the list the buttons are labelled from; a sheet that
	// carried none is the state O6 shipped, where "the two CTAs the maquette draws are the part of
	// this frame that waits for a protocol decision".
	for _, member := range []string{"val actions:", "val question:", "val command:"} {
		if !strings.Contains(code, member) {
			t.Errorf("ADR-009: %s declares no `%s`. The sheet's three contents are the item's "+
				"summary, its action's literal and its decisions' labels.", i1SheetPanel, member)
		}
	}

	// And it must no longer read the ROSTER for the question. `Session.Need` is the journal record
	// type that last touched the session -- a token like `needs_input` -- which is what the sheet
	// rendered for want of a sentence.
	if regexp.MustCompile(`question\s*=\s*row\.need`).MatchString(code) {
		t.Errorf("ADR-009: %s still takes its question from `row.need`, the verbatim journal "+
			"record type. §3.5's `summary` is the adapter's own one-line headline and is what a "+
			"blocking question reads as.", i1SheetPanel)
	}
}

// TestADR009_TheItemIsDecodedOutsideTheScreen fences where the wire is read.
//
// `mobile/types.go` binds the item body as a RAW JSON STRING (gomobile binds no map type), so
// something has to decode it. PB-DS-9 gives a screen copy and arrangement, and `ui/` is where
// every other wire-to-screen model already lives (`SessionScreens.kt`, `TriageInbox.kt`) -- so the
// decode belongs beside them and not inside the composition.
func TestADR009_TheItemIsDecodedOutsideTheScreen(t *testing.T) {
	src := i1Source(t, i1SheetItem)

	// §3.5's own field names, as they appear on the wire.
	for _, field := range []string{"summary", "action", "decisions", "label", "mode", "prompt_card", "prompt_lines"} {
		if !strings.Contains(src, field) {
			t.Errorf("ADR-009: %s names no `%s`. §3.5 is the item's shape and the decode is where "+
				"this app meets it.", i1SheetItem, field)
		}
	}

	// IS-APR-4's phone-side twin of `internal/skeleton/interaction_chain_e2e_test.go`'s fence.
	// The verdict is MACHINE-SIDE: the daemon classifies §3.6's allowed/denied from it, the wire
	// carries {id, label} and nothing else, and "no phone surface switches on polarity". A decoder
	// that reached for a verdict would be reading a field that is not there and rendering a
	// polarity the daemon never resolved from.
	if regexp.MustCompile(`(?i)\bverdict\b`).MatchString(kotlinCodeOnly(src)) {
		t.Errorf("IS-APR-4: %s reads a `verdict`. It is machine-side and is NOT a field on the "+
			"item -- the card labels its buttons from `decisions[].label` and no phone surface "+
			"switches on polarity.", i1SheetItem)
	}
}

// TestADR009_TheSheetPaintsNoPolarityItCannotKnow is the same rule one layer up, over the pixels.
//
// `.a2-ok` and `.a2-no` ARE a polarity claim -- a green Allow and a red Deny -- and the phone has
// no field to make it from. `.a2-more` is the one CTA variant that asserts nothing, which is also
// what `kit/ApprovalSheet.kt` already rules about width ("A sheet whose Allow is wider than its
// Deny has decided for the user, and this is the one surface in the app where it must not").
func TestADR009_TheSheetPaintsNoPolarityItCannotKnow(t *testing.T) {
	code := kotlinCodeOnly(i1Source(t, i1SheetView))
	for _, painted := range []string{"CtaKind.APPROVE", "CtaKind.DENY", "denyChip("} {
		if strings.Contains(code, painted) {
			t.Errorf("IS-APR-4: %s paints `%s` on a decision. The verdict is machine-side, so the "+
				"phone cannot know which decision grants and which refuses; `.a2-ok` or `.a2-no` "+
				"on a label read from `decisions[].label` asserts a polarity this side has not "+
				"been told.", i1SheetView, painted)
		}
	}
}

// ---------------------------------------------------------------------------
// HALF 2 -- the terminal well, deleted.
// ---------------------------------------------------------------------------

// i1WellSymbols are the hosting path ADR-009 (3) names by file and by field: "`PhoneSurface.kt`'s
// `peekHost` / `PeekPanel` path and the screens under it".
var i1WellSymbols = []string{
	"peekHost",
	"peekPanelView",
	"PeekPanelScreen",
	"PeekPanel",
	"TerminalPeek",
}

// i1WatchCalls are (2)'s half: the machine→phone append budget is spent by the journal alone, and
// "no phone surface issues a watch". `TerminalSnapshot` and `terminal_watch` stay on the wire
// unchanged -- what is deleted is this side asking for them.
var i1WatchCalls = []string{
	`\.\s*terminalWatch\s*\(`,
	`\.\s*terminalUnwatch\s*\(`,
	`\.\s*terminalPeek\s*\(`,
}

// TestADR009_TheTerminalWellIsDeletedAtI1Exit is HALF 2's fence.
func TestADR009_TheTerminalWellIsDeletedAtI1Exit(t *testing.T) {
	sources := s24ProductionKotlin(t)

	for name := range sources {
		if strings.HasPrefix(filepath.Base(name), "PeekPanel") {
			t.Errorf("ADR-009 (3): %s is still in the app. The peek screen is deleted with the "+
				"well, not merely left uncomposed -- a screen nothing reaches is how the grid "+
				"comes back.", name)
		}
	}

	var faults []string
	for name, src := range sources {
		code := kotlinCodeOnly(src)
		for _, symbol := range i1WellSymbols {
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`).MatchString(code) {
				faults = append(faults, name+": names `"+symbol+"`")
			}
		}
		for _, call := range i1WatchCalls {
			if regexp.MustCompile(call).MatchString(code) {
				faults = append(faults, name+": calls "+strings.NewReplacer(`\`, "", `.`, "", `s*`, "", `(`, "").Replace(call))
			}
		}
	}
	sort.Strings(faults)
	for _, fault := range faults {
		t.Errorf("ADR-009: %s. (1) leaves no raw grid anywhere in the app and (2) leaves no phone "+
			"surface issuing a terminal_watch; the well's hosting path goes with the well.", fault)
	}
}

// TestADR009_NoScreenRendersTheTerminalVariantOfTheWell is the narrower half, and it is the one
// that survives the component.
//
// `monoWell` STAYS: it is "one component for every mono block in the app", and the approval
// sheet's command literal is one -- the workpackage's own instruction is to keep the component and
// delete the well SCREEN. What must not survive is `terminal = true`, which is the escape-filtered
// VT snapshot's ink: the grid's presence in a screen is what ADR-009 (1) forbids, whatever view
// prints it.
func TestADR009_NoScreenRendersTheTerminalVariantOfTheWell(t *testing.T) {
	terminalWell := regexp.MustCompile(`monoWell\s*\([^)]*terminal\s*=\s*true`)
	for name, src := range s24ProductionKotlin(t) {
		if terminalWell.MatchString(kotlinCodeOnly(src)) {
			t.Errorf("ADR-009 (1): %s prints the daemon-rendered grid in the terminal well. There "+
				"is no terminal emulation and no raw grid anywhere in the app; the transcript is "+
				"the session surface.", name)
		}
	}
}

// TestPBDS6_EveryClaimedScreenExists is the s24 map's missing direction.
//
// `s24ScreenComponents` is checked by iterating the screens that EXIST, so an entry for a file
// that has been deleted is silently skipped -- the same shape of vacuous pass the map's own
// comment records about `SessionDetailView.kt` ("the screen passed because nothing was asked of
// it, which reads identical to passing"), one level up. This slice deletes a claimed screen and
// adds another, so the direction that catches a stale claim is worth having before the next one.
func TestPBDS6_EveryClaimedScreenExists(t *testing.T) {
	screens := s24ScreenSources(t)
	for name := range s24ScreenComponents {
		if _, ok := screens[name]; !ok {
			t.Errorf("PB-DS-6: `s24ScreenComponents` claims a composition for %s, which is not a "+
				"screen in this app. Every factory the entry requires is checked over a file that "+
				"does not exist, which is a green run about nothing.", name)
		}
	}
}

// i1Source reads one production Kotlin file by its repo-relative Kotlin path, failing when it is
// absent -- which for this slice is the assertion rather than an accident.
func i1Source(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(s24KotlinRoot(t), filepath.FromSlash(name))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ADR-009: cannot read %s: %v", name, err)
	}
	return string(b)
}
