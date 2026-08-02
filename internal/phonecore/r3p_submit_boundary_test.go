package phonecore

// Bead agents-tracker-r3p -- FAILING-FIRST (TDD RED, GG-5) tests for the paste-submit
// defect spike S-A measured against the real CLIs (docs/verification/spike-SA.md finding
// #1): a PTY write that carries text AND the carriage return that submits it is read by
// Claude Code's TUI as a multi-line PASTE. The CR is inserted into the input box as a
// literal newline instead of submitting, the prompt sits there unsent, and the next turn's
// text is appended to the SAME unsent draft. It fails silently -- nothing on either side
// reports an error.
//
// WHY THE FIX IS HERE AND NOT AT THE CALL SITE. PhoneSurface.kt's "Send line" hands
// App.SendInput one buffer, `line + "\r"`, and that is only the most visible way to produce
// the bad frame. The one that no call site can avoid is this coalescer's own window: while
// the user is typing, the buffer holds the tail of the burst, so a submit arriving inside
// that window is APPENDED to those held bytes and the next drain emits "ello world\r" as a
// single frame. Splitting the call in Kotlin would not prevent it. The coalescer is the
// phone's only path from keystrokes to frames and already owns every other "what goes in
// one frame" rule (PB-INPUT-6: flush before a paste, flush before a resize), so the submit
// boundary belongs beside them.
//
// THE RULE these tests freeze: a frame is a maximal RUN of submit bytes ("\r", "\n") or a
// maximal run of non-submit bytes, capped at MaxInputPayload as before. Never a mixture.
//
//   - A RUN, not one byte per frame, and TestR3PCoalescer_HeldEnterStaysUnderTheRelayQuota
//     is what forbids the other shape: one submit byte per 125 ms window drains slower than
//     a 30 Hz autorepeat fills, so the buffer would grow without bound and the submits would
//     arrive minutes late -- the queue ADR-007 D7 exists to make structurally impossible.
//   - Insert is UNTOUCHED. A paste and an IME commit are one unit by PB-INPUT-6, and a
//     multi-line paste is a genuine paste: splitting it at its newlines would turn one paste
//     into N submits, which is the same defect pointed the other way.
//
// SEPARATE FRAMES ARE NECESSARY AND NOT SUFFICIENT. The CLI heuristic keys on co-arrival in
// one read tick at the PTY, which is on the MACHINE side, and the relay is store-and-forward
// -- the 125 ms this coalescer puts between two frames is compressed to microseconds by the
// gateway's batched inbound poll. What guarantees the gap at the PTY is the gateway's own
// spacing of submit-only frames (internal/remotegw, TestR3PLeaseConn_*). This file pins the
// half that only the phone can do: preserving the BOUNDARY, which the machine cannot
// recover once it is destroyed, and cannot re-derive itself because a sealed input frame
// carries no keystroke-vs-paste marker.
//
// This file contains NO implementation.

import (
	"bytes"
	"testing"
	"time"
)

// r3pMixedFrame returns the index of the first frame that carries a submit byte together
// with anything else, or -1. That mixture IS the defect: it is what arrives at the PTY as
// one write and trips the CLI's paste heuristic.
func r3pMixedFrame(frames []InputFrame) int {
	for i, f := range frames {
		if f.T != "data" || len(f.Data) == 0 {
			continue
		}
		submit := bytes.ContainsAny(f.Data, "\r\n")
		other := bytes.ContainsFunc(f.Data, func(r rune) bool { return r != '\r' && r != '\n' })
		if submit && other {
			return i
		}
	}
	return -1
}

// r3pRender is a frame's payload with the submit bytes made visible, so a failure names
// what was actually in the frame rather than printing a bare CR.
func r3pRender(f InputFrame) string {
	var out []byte
	for _, b := range f.Data {
		switch b {
		case '\r':
			out = append(out, `\r`...)
		case '\n':
			out = append(out, `\n`...)
		default:
			out = append(out, b)
		}
	}
	return string(out)
}

// TestR3PCoalescer_SubmitNeverSharesAFrameWithTheTextItSubmits is the shape the phone
// actually ships: PhoneSurface.kt's "Send line" appends one CR to the composed line and
// hands the whole thing to SendInput in a single call. Today that is one frame, and it is
// the first thing every beta tester will hit.
func TestR3PCoalescer_SubmitNeverSharesAFrameWithTheTextItSubmits(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	line := []byte("git status\r")
	frames := c.Type(s11Session, line)

	if i := r3pMixedFrame(frames); i >= 0 {
		t.Fatalf("frame %d is %q -- text and its submit in ONE frame reach the PTY as one write, which Claude Code reads as a multi-line paste: the CR is inserted as a literal newline and the prompt is never submitted (spike-SA finding #1)", i, r3pRender(frames[i]))
	}
	if got := string(s11DataBytes(t, frames, s11Session)); got != "git status" {
		t.Fatalf("leading-edge frames carried %q, want %q -- the text goes out at once and only the submit waits", got, "git status")
	}

	// The submit still leaves, one window later, and it leaves ALONE.
	clk.advance(InputFrameInterval)
	due := c.Due()
	if i := r3pMixedFrame(due); i >= 0 {
		t.Fatalf("the submit frame is %q, want the submit run by itself", r3pRender(due[i]))
	}
	if got := string(s11DataBytes(t, due, s11Session)); got != "\r" {
		t.Fatalf("after the window the coalescer yielded %q, want %q -- a submit that never leaves is a prompt the user typed and nothing ran", got, `\r`)
	}

	// Nothing was lost or reordered on the way: PB-INPUT-6's first clause still holds.
	all := append(append([]InputFrame(nil), frames...), due...)
	if got := s11DataBytes(t, all, s11Session); !bytes.Equal(got, line) {
		t.Fatalf("splitting the submit lost or reordered bytes: got %q, want %q", got, line)
	}
}

// TestR3PCoalescer_ASubmitInsideAnOpenWindowIsSplitOffTheHeldBytes is the case no call site
// can fix by splitting its own call. The user is mid-burst, so the coalescer is HOLDING the
// tail of the line when the submit arrives; appending it to those held bytes rebuilds the
// bad frame from two perfectly separate SendInput calls.
func TestR3PCoalescer_ASubmitInsideAnOpenWindowIsSplitOffTheHeldBytes(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	c.Type(s11Session, []byte("d")) // leading edge, emitted immediately
	clk.advance(InputFrameInterval / 4)
	if held := c.Type(s11Session, []byte("eploy prod")); len(held) != 0 {
		t.Fatalf("premise broken: the burst tail was emitted (%d frames) instead of held, so this test proves nothing about the window", len(held))
	}
	// A SEPARATE call, inside the same window -- the shape a Kotlin-side fix would produce.
	if held := c.Type(s11Session, []byte("\r")); r3pMixedFrame(held) >= 0 {
		t.Fatalf("the submit was emitted merged with the held bytes: %q", r3pRender(held[r3pMixedFrame(held)]))
	}

	clk.advance(InputFrameInterval)
	due := c.Due()
	if i := r3pMixedFrame(due); i >= 0 {
		t.Fatalf("frame %d is %q -- the coalescer merged the submit into the keystrokes it was holding, so a phone-side call split cannot help: the merge happens after it", i, r3pRender(due[i]))
	}
	if got := string(s11DataBytes(t, due, s11Session)); got != "eploy prod" {
		t.Fatalf("the drained window carried %q, want %q", got, "eploy prod")
	}

	clk.advance(InputFrameInterval)
	if got := string(s11DataBytes(t, c.Due(), s11Session)); got != "\r" {
		t.Fatalf("the submit came out as %q, want %q", got, `\r`)
	}
}

// TestR3PCoalescer_AForcedFlushSplitsTheSubmitToo covers the three callers that drain with
// force -- Flush (release, backgrounding, lease horizon), Resize and Insert's flush-first --
// with the one mechanism they share. A boundary flush that concatenated the buffer into a
// single frame would rebuild the defect at exactly the moment the user pressed Enter and
// then released control.
func TestR3PCoalescer_AForcedFlushSplitsTheSubmitToo(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	c.Type(s11Session, []byte("e")) // leading edge
	clk.advance(InputFrameInterval / 4)
	c.Type(s11Session, []byte("cho hi\r")) // held, submit included

	flushed := c.Flush()
	if i := r3pMixedFrame(flushed); i >= 0 {
		t.Fatalf("the forced flush emitted %q as one frame -- forcing the window must not also collapse the submit boundary", r3pRender(flushed[i]))
	}
	if got := s11Kinds(flushed); got != "data,data" {
		t.Fatalf("Flush emitted %q, want two data frames (the text, then the submit)", got)
	}
	if got, want := string(flushed[0].Data), "cho hi"; got != want {
		t.Fatalf("first flushed frame = %q, want %q", got, want)
	}
	if got, want := string(flushed[1].Data), "\r"; got != want {
		t.Fatalf("second flushed frame = %q, want %q", r3pRender(flushed[1]), `\r`)
	}
	if c.Buffered(s11Session) != 0 {
		t.Fatalf("Buffered = %d after a forced flush, want 0 -- splitting must not leave the submit behind", c.Buffered(s11Session))
	}
}

// TestR3PCoalescer_APasteKeepsItsNewlinesInOneUnit is the guard on the fix's most likely
// wrong shape, and it is GREEN before the fix on purpose: it is what must still be true
// afterwards. A paste and an IME commit are ONE unit (PB-INPUT-6). A multi-line paste is a
// genuine paste, and the CLI is right to read it as one -- splitting it at its newlines
// would run each line as a separate command, which is the same silent-wrong-thing this bead
// is about, pointed the other way.
func TestR3PCoalescer_APasteKeepsItsNewlinesInOneUnit(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	paste := []byte("first line\nsecond line\nthird line\n")
	frames := c.Insert(s11Session, paste)

	if len(frames) != 1 {
		t.Fatalf("a %d-byte multi-line paste emitted %d frames, want 1 -- Insert is PB-INPUT-6's atomic unit, and splitting it at its newlines turns one paste into %d submits", len(paste), len(frames), len(frames))
	}
	if !bytes.Equal(frames[0].Data, paste) {
		t.Fatalf("paste frame = %q, want the paste verbatim", r3pRender(frames[0]))
	}

	// ... and it is still flushed BEHIND buffered keystrokes, with the submit boundary
	// applied to those keystrokes and not to the paste.
	c2 := NewInputCoalescer(clk.now)
	c2.Type(s11Session, []byte("#")) // leading edge
	clk.advance(InputFrameInterval / 4)
	c2.Type(s11Session, []byte(" go\r")) // held: text, then a submit
	out := c2.Insert(s11Session, paste)
	if got := s11Kinds(out); got != "data,data,data" {
		t.Fatalf("Insert after a held submit emitted %q, want three data frames (text, submit, paste)", got)
	}
	if got, want := string(out[2].Data), string(paste); got != want {
		t.Fatalf("the paste frame is %q, want the paste verbatim -- the flush-first must not chop it", r3pRender(out[2]))
	}
	if i := r3pMixedFrame(out[:2]); i >= 0 {
		t.Fatalf("the flushed keystrokes still mix text and submit in frame %d: %q", i, r3pRender(out[i]))
	}
}

// TestR3PCoalescer_HeldEnterStaysUnderTheRelayQuota is why a submit RUN is one frame rather
// than one frame per byte. A held Enter is a 30 Hz stream of submits; a coalescer that
// emitted one byte per 125 ms window would drain at 8 bytes/s against 30 bytes/s arriving,
// so the buffer grows without bound, the submits land minutes after the key was pressed, and
// the eventual boundary flush dumps the whole backlog into one instant -- over the relay's
// MailboxAppendPerMin in a single burst. Backlog is the queue ADR-007 D7 forbids, not a
// pacing detail.
func TestR3PCoalescer_HeldEnterStaysUnderTheRelayQuota(t *testing.T) {
	const (
		hz                  = 30
		seconds             = 60
		mailboxAppendPerMin = 600 // relay/config.go, written as a literal per PB-BIND-0
	)

	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	var typed []byte
	var frames []InputFrame
	var emitted []time.Time
	record := func(fs []InputFrame) {
		for _, f := range fs {
			frames = append(frames, f)
			emitted = append(emitted, clk.now())
		}
	}

	tick := time.Second / hz
	for i := 0; i < hz*seconds; i++ {
		typed = append(typed, '\r')
		record(c.Type(s11Session, []byte{'\r'}))
		clk.advance(tick)
		record(c.Due())
	}
	record(c.Flush())

	if got := s11DataBytes(t, frames, s11Session); !bytes.Equal(got, typed) {
		t.Fatalf("held Enter lost or duplicated submits: sent %d, typed %d", len(got), len(typed))
	}
	if worst := s11WorstWindow(emitted, time.Minute); worst > mailboxAppendPerMin {
		t.Fatalf("worst 60s window issued %d appends, over the relay's MailboxAppendPerMin of %d -- one frame per submit BYTE drains slower than autorepeat arrives, so the backlog lands in one burst and the lease dies with codeQuotaExceeded", worst, mailboxAppendPerMin)
	}
	if buffered := c.Buffered(s11Session); buffered != 0 {
		t.Fatalf("Buffered = %d after the flush, want 0", buffered)
	}
	if budget := int(float64(seconds) * float64(time.Second) / float64(InputFrameInterval)); len(frames) > budget+1 {
		t.Fatalf("emitted %d frames over %ds, over §6.0's %d (8/s sustained, +1 for the leading edge)", len(frames), seconds, budget)
	}
}
