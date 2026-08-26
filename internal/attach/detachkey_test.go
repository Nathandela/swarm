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

// The reserved-row hint names the detach key so returning is discoverable (A-5);
// after ADR-006 v0.3 it reads "ctrl+q returns to swarm" (the key label lowercased in
// the hint), never the old "Ctrl+\". Repointed from the v0.2 top-row chromeLine, which
// the reserved-row design replaced.
func TestChromeHintNamesCtrlQ(t *testing.T) {
	hint := hintText("claude", DefaultDetachKey, 0)
	if !strings.Contains(hint, "ctrl+q returns to swarm") {
		t.Fatalf("hint must name ctrl+q as the return key; got %q", hint)
	}
	if strings.Contains(hint, `Ctrl+\`) || strings.Contains(hint, `ctrl+\`) {
		t.Fatalf("hint must not still name the old Ctrl+\\ key; got %q", hint)
	}
}

// ADR-006 amendment 2026-08-26: a keypress is recognized independently of the
// arbitrary read boundary that carried it. Bytes before the key are forwarded;
// the detach key and bytes after it are not.
func TestDetachKey_WithinMultiByteReadDetaches(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	burst := []byte{'p', DefaultDetachKey, 'q'}
	term.feed(burst)

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if got := sess.inputBytes(); !bytes.Equal(got, []byte{'p'}) {
		t.Fatalf("forwarded input = %q, want only bytes before detach", got)
	}
}

func TestDetachKey_KittyCSIUDetachesAndIsNotForwarded(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte("\x1b[113;5u")) // Ctrl+Q under Kitty keyboard protocol.
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if got := sess.inputBytes(); len(got) != 0 {
		t.Fatalf("Kitty Ctrl+Q was forwarded: %q", got)
	}
}

func TestDetachKey_KittyCSIUExplicitPressEventDetaches(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte("\x1b[113;5:1u"))
	if res := waitResult(t, ch); res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}

func TestDetachKey_KittyCSIUAcrossFragmentedReadsDetaches(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	for _, fragment := range [][]byte{[]byte("\x1b"), []byte("[113"), []byte(";5"), []byte("u")} {
		term.feed(fragment)
	}
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if got := sess.inputBytes(); len(got) != 0 {
		t.Fatalf("fragmented Kitty Ctrl+Q was forwarded: %q", got)
	}
}

func TestDetachKey_BracketedPasteForwardsLegacyAndKittyFormsByteExactly(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	paste := append([]byte("\x1b[200~before"), DefaultDetachKey)
	paste = append(paste, []byte("\x1b[113;5uafter\x1b[201~")...)
	for _, fragment := range [][]byte{paste[:2], paste[2:11], paste[11 : len(paste)-3], paste[len(paste)-3:]} {
		term.feed(fragment)
	}
	eventually(t, func() bool { return bytes.Equal(sess.inputBytes(), paste) })
	if sess.detachCalls != 0 {
		t.Fatalf("detach sequence inside bracketed paste detached; calls = %d", sess.detachCalls)
	}

	sess.endSession()
	if res := waitResult(t, ch); res.reason != ReasonSessionEnd {
		t.Fatalf("reason = %v, want ReasonSessionEnd", res.reason)
	}
}

func TestDetachKey_KittyReleaseIsForwardedNotDetach(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	release := []byte("\x1b[113;5:3u")
	term.feed(release)
	eventually(t, func() bool { return bytes.Equal(sess.inputBytes(), release) })

	sess.endSession()
	if res := waitResult(t, ch); res.reason != ReasonSessionEnd {
		t.Fatalf("reason = %v, want ReasonSessionEnd", res.reason)
	}
}

func TestDetachKey_ConfiguredControlKeyMatchesKittyCSIU(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess, DetachKey: 0x1d}) // Ctrl+]

	term.feed([]byte("\x1b[93;5u"))
	if res := waitResult(t, ch); res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}

func TestDetachInputFilter_NonDetachCSIIsByteExactAcrossFragments(t *testing.T) {
	var got []byte
	filter := newDetachInputFilter(DefaultDetachKey, func(p []byte) {
		got = append(got, p...)
	}, func() { t.Fatal("ordinary cursor key detached") })

	if filter.Feed([]byte("\x1b")) || filter.Feed([]byte("[")) || filter.Feed([]byte("A")) {
		t.Fatal("ordinary cursor key detached")
	}
	if want := []byte("\x1b[A"); !bytes.Equal(got, want) {
		t.Fatalf("forwarded input = %q, want byte-exact %q", got, want)
	}
}
