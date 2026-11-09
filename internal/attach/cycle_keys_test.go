package attach

// FAILING-FIRST for ADR-006 Amendment "session cycling keys": while attached, Ctrl+P and
// Ctrl+N leave the current session for the previous or next one without the board in
// between. The passthrough recognizes them exactly as it recognizes the detach key --
// legacy C0 byte and Kitty CSI-u, across read boundaries, never inside a bracketed paste
// -- and reports which one ended the attach, so the caller can attach the neighbour.
// They are opt-in: a Config without them forwards 0x10 and 0x0e to the agent unchanged.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const (
	ctrlP = 0x10
	ctrlN = 0x0e
)

func TestCycleKeys_LegacyBytesEndTheAttachWithTheirReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  byte
		want Reason
	}{
		{"ctrl+n", ctrlN, ReasonNext},
		{"ctrl+p", ctrlP, ReasonPrev},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession([]byte("S"))
			ch := runInBackground(Config{Term: term, Session: sess, PrevKey: ctrlP, NextKey: ctrlN})

			term.feed([]byte{'a', tc.key, 'b'})
			res := waitResult(t, ch)
			if res.reason != tc.want {
				t.Fatalf("reason = %v, want %v", res.reason, tc.want)
			}
			if got := sess.inputBytes(); !bytes.Equal(got, []byte{'a'}) {
				t.Fatalf("forwarded input = %q, want only the bytes before the key", got)
			}
		})
	}
}

func TestCycleKeys_KittyCSIUEndTheAttachAndAreNotForwarded(t *testing.T) {
	for _, tc := range []struct {
		name string
		seq  string
		want Reason
	}{
		{"ctrl+n", "\x1b[110;5u", ReasonNext},
		{"ctrl+p", "\x1b[112;5u", ReasonPrev},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession([]byte("S"))
			ch := runInBackground(Config{Term: term, Session: sess, PrevKey: ctrlP, NextKey: ctrlN})

			term.feed([]byte(tc.seq))
			res := waitResult(t, ch)
			if res.reason != tc.want {
				t.Fatalf("reason = %v, want %v", res.reason, tc.want)
			}
			if got := sess.inputBytes(); len(got) != 0 {
				t.Fatalf("the Kitty key was forwarded: %q", got)
			}
		})
	}
}

func TestCycleKeys_DetachKeyStillDetaches(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess, PrevKey: ctrlP, NextKey: ctrlN})

	term.feed([]byte{DefaultDetachKey})
	if res := waitResult(t, ch); res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}

func TestCycleKeys_InsideBracketedPasteAreData(t *testing.T) {
	f, got := newRecordingFilter(t)
	paste := []byte("\x1b[200~x\x10y\x0ez\x1b[201~")
	if f.Feed(paste) {
		t.Fatal("a pasted Ctrl+P/Ctrl+N byte ended the attach")
	}
	if !bytes.Equal(got(), paste) {
		t.Fatalf("forwarded = %q, want the paste unchanged %q", got(), paste)
	}
}

func TestCycleKeys_SplitKittySequenceIsRecognizedAcrossReads(t *testing.T) {
	f, got := newRecordingFilter(t)
	if f.Feed([]byte("\x1b[11")) {
		t.Fatal("a partial sequence ended the attach")
	}
	if !f.Feed([]byte("0;5u")) {
		t.Fatal("Kitty Ctrl+N split across two reads was not recognized")
	}
	if f.fired != ctrlN {
		t.Fatalf("fired = %#x, want Ctrl+N", f.fired)
	}
	if len(got()) != 0 {
		t.Fatalf("forwarded = %q, want nothing", got())
	}
}

// Without the keys configured the passthrough is unchanged: the agent receives them.
func TestCycleKeys_OffByDefaultForwardsTheBytes(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte{ctrlP, ctrlN})
	waitInput(t, sess, []byte{ctrlP, ctrlN})
	term.feed([]byte{DefaultDetachKey})
	if res := waitResult(t, ch); res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}

func TestCycleKeys_HintNamesThem(t *testing.T) {
	hint := hintTextNav("claude", DefaultDetachKey, navHint(ctrlP, ctrlN), 0)
	if !strings.Contains(hint, "ctrl+q returns to swarm") {
		t.Fatalf("hint lost the return key; got %q", hint)
	}
	if !strings.Contains(hint, "ctrl+p/ctrl+n switch") {
		t.Fatalf("hint must name the cycling keys; got %q", hint)
	}
	if navHint(0, 0) != "" {
		t.Fatalf("navHint(0, 0) = %q, want empty", navHint(0, 0))
	}
}

func newRecordingFilter(t *testing.T) (*detachInputFilter, func() []byte) {
	t.Helper()
	var buf bytes.Buffer
	f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { buf.Write(p) }, func() {})
	f.cycle = []byte{ctrlP, ctrlN}
	return f, func() []byte { return buf.Bytes() }
}

func waitInput(t *testing.T, sess *fakeSession, want []byte) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Equal(sess.inputBytes(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("forwarded input = %q, want %q", sess.inputBytes(), want)
}
