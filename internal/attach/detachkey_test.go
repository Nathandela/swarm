package attach

import (
	"strings"
	"testing"
)

// ADR-006 / bead agents-tracker-bkn — the default detach key becomes Ctrl+q (0x11):
// layout-friendly across US/Swiss/QWERTZ/AZERTY (the old Ctrl+\ / 0x1c needs a
// Shift+Alt/AltGr chord and is near-untypeable there). The Config.DetachKey seam
// keeps it configurable; delivery is clean because raw mode clears IXON, so 0x11
// (XON) never triggers flow control.
func TestDefaultDetachKeyIsCtrlQ(t *testing.T) {
	if DefaultDetachKey != 0x11 {
		t.Fatalf("DefaultDetachKey = %#x, want 0x11 (Ctrl+q, ADR-006)", DefaultDetachKey)
	}
	if got := keyLabel(DefaultDetachKey); got != "Ctrl+Q" {
		t.Fatalf("keyLabel(DefaultDetachKey) = %q, want \"Ctrl+Q\"", got)
	}
}

// item 9 — keyLabel renders DEL (0x7f) as "DEL", not a bogus "Ctrl+<char>" (0x7f
// has no sensible Ctrl+letter form: 0x7f|0x40 is 0x7f itself).
func TestKeyLabel_DELRendersAsDEL(t *testing.T) {
	if got := keyLabel(0x7f); got != "DEL" {
		t.Fatalf("keyLabel(0x7f) = %q, want \"DEL\"", got)
	}
}

// The reserved-row hint names the detach key so returning is discoverable (A-5);
// after ADR-006 v0.3 it reads "ctrl+q returns to swarm" (the key label lowercased in
// the hint), never the old "Ctrl+\". Repointed from the v0.2 top-row chromeLine, which
// the reserved-row design replaced.
func TestChromeHintNamesCtrlQ(t *testing.T) {
	hint := hintText("claude", DefaultDetachKey, 0, false)
	if !strings.Contains(hint, "ctrl+q returns to swarm") {
		t.Fatalf("hint must name ctrl+q as the return key; got %q", hint)
	}
	if strings.Contains(hint, `Ctrl+\`) || strings.Contains(hint, `ctrl+\`) {
		t.Fatalf("hint must not still name the old Ctrl+\\ key; got %q", hint)
	}
}

// D4's solo-read rule is SUPERSEDED by ADR-019: field evidence showed that bytes
// sharing a read with the keypress are the steady state of an attach, not a rare
// flood — agent CLIs enable mouse tracking (CSI ?1003h) and focus reporting
// (CSI ?1004h), which the passthrough forwards to the user's terminal, so the
// terminal streams reports into stdin for the whole attach. The rule is now
// boundary-aware, and the pins live in detachscan_test.go:
//
//   TestDetachKey_RecognizedBehindTerminalReports  — the key counts behind a report
//   TestDetachKey_PrecedingBytesForwardedKeyIsNot  — those bytes are still input
//   TestDetachKey_InsideBracketedPasteIsData       — the paste gate D4 was protecting
//   TestDetachKey_InsideEscapeSequenceIsNotAKeypress — the GROUND gate
//
// The companion pin for the plain solo press stays in passthrough_test.go
// (TestPassthrough_DetachKeyDetachesAndIsNotForwarded).
