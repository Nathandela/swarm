package claude

// The folder-trust LAUNCH GATE (ADR-025, bead swarm-1mq).
//
// A claude started in a directory it has never been trusted in draws its folder-trust
// dialog before anything else -- before the hooks fire, before a positional prompt is
// submitted -- and trust is NOT inherited from a trusted parent directory (measured live
// on 2.1.258: a fresh child of a long-trusted directory still asked). The dialog changed
// between the two recorded versions:
//
//	2.1.231   ❯ 1. Yes, I trust this folder      2.1.258   ❯ No, exit
//	             2. No, exit                                  Yes, I trust this folder
//
// so the bare Enter that accepted on 2.1.231 EXITS the CLI with status 1 on 2.1.258. The
// answer is therefore read off the grid rather than assumed: the row carrying the selection
// marker decides whether a cursor move precedes the confirm. Everything the recognizer
// anchors on -- the title under the box rule, the two option labels, the marker -- is
// written contiguously by the CLI (testdata/permdialog/README.md), so the column-jump
// artifact that garbles other rows cannot reach it.

import (
	"strings"

	"github.com/Nathandela/swarm/internal/vt"
)

// The dialog's own words, verbatim on both recorded versions.
const (
	trustTitle  = "Accessing workspace:"
	trustAccept = "Yes, I trust this folder"
	trustExit   = "No, exit"
)

// The keys: normal-mode cursor keys (CSI A / CSI B) and a bare carriage return.
const (
	keyUp    = "\x1b[A"
	keyDown  = "\x1b[B"
	keyEnter = "\r"
)

// LaunchGateKeys makes this adapter an adapter.LaunchGateAnswerer: it answers the
// folder-trust dialog on snap by selecting "Yes, I trust this folder" and confirming. It is
// pure and total; any grid that is not positively that dialog yields ("", false).
func (claudeAdapter) LaunchGateKeys(snap *vt.Snap) (string, bool) {
	if snap == nil || len(snap.Lines) == 0 {
		return "", false
	}
	yes, no, marked := -1, -1, -1
	for y := range snap.Lines {
		label, isMarked := gateOption(rowText(snap.Lines[y]))
		switch label {
		case trustAccept:
			if yes >= 0 {
				return "", false // two Yes rows: prose quoting the dialog, not the dialog
			}
			yes = y
		case trustExit:
			if no >= 0 {
				return "", false
			}
			no = y
		default:
			continue
		}
		if isMarked {
			if marked >= 0 {
				return "", false // two marked rows is no selection state at all
			}
			marked = y
		}
	}
	// Both options, adjacent, exactly one marked, under the dialog's own title.
	if yes < 0 || no < 0 || marked < 0 || (yes-no != 1 && no-yes != 1) {
		return "", false
	}
	if !trustTitleAbove(snap, min(yes, no)) {
		return "", false
	}
	switch {
	case marked == yes:
		return keyEnter, true
	case yes > no:
		return keyDown + keyEnter, true
	default:
		return keyUp + keyEnter, true
	}
}

// gateOption reads one option row: its label with the selection marker and any "N. "
// numbering stripped, and whether the marker was on it. A row that is no option yields its
// trimmed text, which matches no label.
func gateOption(text string) (label string, marked bool) {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, selectionMarker) {
		marked = true
		t = strings.TrimSpace(strings.TrimPrefix(t, selectionMarker))
	}
	if len(t) >= 3 && t[0] >= '1' && t[0] <= '9' && t[1] == '.' && t[2] == ' ' {
		t = strings.TrimSpace(t[3:])
	}
	return t, marked
}

// trustTitleAbove walks up from the option rows to the dialog box's own top rule and
// requires the trust dialog's title directly beneath it -- the same anchoring as
// variantAbove, so a quoted dialog scrolled up in the transcript cannot pass.
func trustTitleAbove(snap *vt.Snap, firstOption int) bool {
	for y := firstOption - 1; y >= 0; y-- {
		if !isBoxRule(rowText(snap.Lines[y])) {
			continue
		}
		return strings.TrimSpace(rowText(snap.Lines[y+1])) == trustTitle
	}
	return false
}
