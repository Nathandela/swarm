package attach

import (
	"bytes"
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

// D4 RULED (agents-tracker-rs8) — detach recognition stays solo-byte: only a
// read that yields the detach key ALONE (n==1) detaches. The input pump has no
// bracketed-paste state machine, so a read that carries the detach key's byte
// amid other bytes (a paste burst, or flood) is forwarded through as ordinary
// input, never treated as a detach. Companion pin:
// TestPassthrough_DetachKeyDetachesAndIsNotForwarded (passthrough_test.go)
// pins the solo-byte case that DOES detach.
func TestDetachKey_WithinMultiByteReadIsForwardedNotDetach(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	// One read carrying the detach key alongside other bytes, fed as a single
	// write so it lands in one Read call with n>1.
	burst := []byte{'p', DefaultDetachKey, 'q'}
	term.feed(burst)

	eventually(t, func() bool { return bytes.Equal(sess.inputBytes(), burst) })
	if sess.detachCalls != 0 {
		t.Fatalf("detach key within a multi-byte read must not detach (D4); Session.Detach called %d times", sess.detachCalls)
	}

	sess.endSession()
	res := waitResult(t, ch)
	if res.reason != ReasonSessionEnd {
		t.Fatalf("reason = %v, want ReasonSessionEnd (the multi-byte read must not have detached earlier)", res.reason)
	}
}
