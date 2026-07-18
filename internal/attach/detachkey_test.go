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

// The one-line chrome names the detach key so it is discoverable (A-5); after the
// change it must read "Ctrl+Q", never the old "Ctrl+\".
func TestChromeLineNamesCtrlQ(t *testing.T) {
	line := string(chromeLine("claude", DefaultDetachKey))
	if !strings.Contains(line, "Ctrl+Q to detach") {
		t.Fatalf("chrome line must name Ctrl+Q as the detach key; got %q", line)
	}
	if strings.Contains(line, `Ctrl+\`) {
		t.Fatalf("chrome line must not still name the old Ctrl+\\ key; got %q", line)
	}
}
