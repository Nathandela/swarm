package claude

// The permission-dialog RECOGNIZER (Mirror M1.1, bead agents-tracker-dwwv.2.1).
//
// M1.2 applies a phone approval by injecting the dialog's own keys into the PTY the
// daemon owns, gated on the live grid still showing that dialog (mirror-program.md
// section 3, step 2). This is that gate's reader: a pure function of the rendered
// grid, no state, no I/O -- so it stays inside the adapter's T-5 boundary (contract
// + vt only) while being callable from the daemon/skeleton layer, which is where the
// tap's snapshots arrive.
//
// IT IS THE STRICTER READER OF A SCREEN THE ENGINE ALREADY CLASSIFIES, NOT A SECOND
// ONE. The engine's claude grid signature (internal/engine/heuristic.go, ADR-007)
// answers one question -- is this session blocked on a human -- and answers it for
// EVERY modal claude dialog. This answers the narrower question the injection needs:
// which dialog exactly, and which keys does it obey. Everything it calls a dialog the
// engine also calls interaction=permission (pinned by
// TestRecognizedDialog_IsPermissionToTheStatusEngineToo); the engine keeps its own
// broader hints untouched.
//
// WHAT IT ANCHORS ON, AND WHY ONLY THAT. Claude paints a row as words separated by
// `CSI n G` column jumps and never clears the cells it jumps over, trusting its own
// model of what they already hold; where the emulator's cells differ, a stale
// character shows through (visible in the recorded fixtures' option-2 row, and
// documented in testdata/permdialog/README.md). Only rows the CLI writes CONTIGUOUSLY
// and then clears to end-of-line survive that intact -- the box's title, `1. Yes` and
// `3. No`. Those are the anchors. The question row ("Do you want to proceed?") is
// written with jumps, so it is deliberately NOT one, and neither is option 2.
//
// A grid it does not POSITIVELY match is a refusal (ok=false), never a guess: a
// wrong match types keystrokes into whatever has focus, while a missed match only
// declines the phone's tap and leaves the terminal's own dialog untouched.

import (
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

// Recognized dialog variants. The variant names the KEY MAP that applies; it is not
// decoration, so a dialog whose title is not one of these is not recognized at all.
const (
	VariantBash = "bash"
	VariantEdit = "edit"
)

// dialogTitles maps a dialog box's title row, verbatim as claude 2.1.231 renders it,
// to the variant it identifies. Adding a row here REQUIRES a recorded fixture: the
// key map is a per-variant, per-version observation, not an extrapolation.
var dialogTitles = map[string]string{
	"Bash command": VariantBash,
	"Edit file":    VariantEdit,
}

// variantForAction maps the PENDING REQUEST's own interaction-schema.md §7 action
// type to the dialog variant that request is answered on. It is the join that makes
// the injection gate prove "the dialog on screen is THIS request's" rather than only
// "some answerable dialog is on screen" (M1.8).
//
// IT IS FAIL-CLOSED. An action with no row here -- `read`, `search`, `fetch`,
// IS-TOOL-2's `other`, or none at all -- matches no variant and is refused, which is
// the same posture the recognizer takes towards a title it has no fixture for: this
// package answers the screens it has RECORDED and declines the rest.
//
// `write` shares VariantEdit with `edit` on the reading that both tools propose the
// same act on the same file. No fixture records a Write dialog's title, so if claude
// titles that box anything but "Edit file" the recognizer refuses it one step
// earlier and this row never fires -- the pairing can only ever be as permissive as
// dialogTitles already is.
var variantForAction = map[string]string{
	"execute": VariantBash,
	"edit":    VariantEdit,
	"write":   VariantEdit,
}

// The option labels that carry the decision. Both are matched EXACTLY: the folder-trust
// dialog's "Yes, I trust this folder" / "No, exit" are a different question with a
// different consequence, and must not be answered with an approval's key map.
const (
	optionAllow = "Yes"
	optionDeny  = "No"
)

// selectionMarker is the U+276F glyph on the currently highlighted option row. It is
// stripped before an option is read rather than required, because the digits are
// ABSOLUTE: a live run answered "3" while option 1 was highlighted and the request
// was denied (docs/verification/mirror-m1.md), so the key map does not depend on
// where the terminal's user happens to have left the selection.
const selectionMarker = "❯"

// dialogRegionRows bounds the option scan to the bottom of the screen, matching the
// engine's own region convention (gridRegionRows). The dialog is bottom-anchored; a
// numbered list further up is transcript text, not a live decision point.
const dialogRegionRows = 12

// boxRule is the glyph claude draws the dialog box's top rule with (U+2500). The
// diff inside an Edit dialog is fenced with U+254C instead, so the two never confuse.
const boxRule = '─'

// PermissionDialog is a recognized dialog and the keys it obeys. AllowKeys and
// DenyKeys are written to the PTY VERBATIM and are complete answers on their own:
// each recorded key selects its option AND submits it, with no Enter after it.
type PermissionDialog struct {
	Variant   string // VariantBash | VariantEdit
	AllowKeys string // keystrokes that approve the request
	DenyKeys  string // keystrokes that refuse it
}

// ApprovalKeys makes this adapter an adapter.ApprovalApplier (Mirror M1.2): it answers the
// dialog currently on snap with the keys that carry the given verdict's polarity, PROVIDED
// that dialog is the one the pending request raised.
//
// It is the ONLY place the normalized verdict meets the recorded key map, and the join is
// deliberately narrow. The verdict is what approvalFrom classified the CLI's own decision id
// as at capture; the keys are what M1.1 recorded off the live dialog. Everything else -- allow
// vs deny, which grid is a dialog at all -- is already decided by the two halves.
//
// A verdict outside allow|deny is REFUSED rather than folded into allow. There are exactly two
// answerable options on the recorded dialog and `other` is IS-TOOL-2's posture for a decision
// the adapter could place neither way, so answering it with the allow key would type a grant
// nobody gave.
//
// THE ACTION BIND (M1.8) is the second refusal and closes the CHAINED-DIALOG race. `swarm hook`
// posts and exits without waiting for the daemon to shape its item, so a dialog is on the glass
// before its own card exists: the owner answers dialog A at the terminal, claude raises B, and
// A is still pending daemon-side because the session never left `permission` for anything to
// observe. A phone approve for A arriving in that window passed the tuple check and the grid
// gate alike, and typed A's verdict into B. Requiring the recognized variant to be the one this
// request's action names removes the CROSS-TOOL case; Bash-after-Bash stays ambiguous, because
// the recognizer reads a variant and not a command, and that residue is stated rather than
// papered over.
func (claudeAdapter) ApprovalKeys(snap *vt.Snap, verdict, action string) (string, bool) {
	dlg, ok := RecognizePermissionDialog(snap)
	if !ok {
		return "", false
	}
	if want, ok := variantForAction[action]; !ok || want != dlg.Variant {
		return "", false
	}
	switch verdict {
	case adapter.VerdictAllow:
		return dlg.AllowKeys, true
	case adapter.VerdictDeny:
		return dlg.DenyKeys, true
	}
	return "", false
}

// RecognizePermissionDialog reports whether snap shows a claude tool-approval dialog
// this package has a recorded key map for, and returns that key map. It is pure and
// total: a nil, empty or unfamiliar grid yields (zero, false).
func RecognizePermissionDialog(snap *vt.Snap) (PermissionDialog, bool) {
	if snap == nil || len(snap.Lines) == 0 {
		return PermissionDialog{}, false
	}
	last, ok := lastContentRow(snap)
	if !ok {
		return PermissionDialog{}, false
	}
	top := last - dialogRegionRows + 1
	if top < 0 {
		top = 0
	}

	allowRow, denyRow := -1, -1
	var allowKey, denyKey string
	for y := top; y <= last; y++ {
		digit, label, ok := optionRow(rowText(snap.Lines[y]))
		if !ok {
			continue
		}
		switch label {
		case optionAllow:
			if allowRow < 0 {
				allowRow, allowKey = y, digit
			}
		case optionDeny:
			denyRow, denyKey = y, digit
		}
	}
	// Both decisions must be on screen, in the order the dialog lists them. A screen
	// offering only one of them is not a dialog this can answer.
	if allowRow < 0 || denyRow <= allowRow {
		return PermissionDialog{}, false
	}

	variant, ok := variantAbove(snap, allowRow)
	if !ok {
		return PermissionDialog{}, false
	}
	return PermissionDialog{Variant: variant, AllowKeys: allowKey, DenyKeys: denyKey}, true
}

// variantAbove walks up from the option rows to the dialog box's own top rule and
// reads the title row directly beneath it. Anchoring the title to the box rather than
// searching the screen for it keeps an earlier dialog's title, scrolled up in the
// transcript, from naming the variant of the dialog currently on screen.
func variantAbove(snap *vt.Snap, allowRow int) (string, bool) {
	for y := allowRow - 1; y >= 0; y-- {
		if !isBoxRule(rowText(snap.Lines[y])) {
			continue
		}
		variant, ok := dialogTitles[strings.TrimSpace(rowText(snap.Lines[y+1]))]
		return variant, ok
	}
	return "", false
}

// optionRow reads one numbered option: its digit and its label, with the selection
// marker stripped. ok is false for any row that is not "<digit>. <label>".
func optionRow(text string) (digit, label string, ok bool) {
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), selectionMarker))
	if len(t) < 3 || t[0] < '1' || t[0] > '9' || t[1] != '.' || t[2] != ' ' {
		return "", "", false
	}
	return t[:1], strings.TrimSpace(t[2:]), true
}

// isBoxRule reports whether a row is one unbroken run of the box-rule glyph.
func isBoxRule(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, r := range t {
		if r != boxRule {
			return false
		}
	}
	return true
}

// rowText concatenates one grid row's runs back into its text.
func rowText(line vt.Line) string {
	var b strings.Builder
	for _, r := range line.Runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// lastContentRow returns the index of the lowest row carrying any visible content.
func lastContentRow(snap *vt.Snap) (int, bool) {
	for y := len(snap.Lines) - 1; y >= 0; y-- {
		if strings.TrimSpace(rowText(snap.Lines[y])) != "" {
			return y, true
		}
	}
	return 0, false
}
