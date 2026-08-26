package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Slice 0 of the conversation surface:
// docs/specifications/chat-surface-plan.md §3. Bead: agents-tracker-bzfe.
//
// THIS IS A LIVE DEFECT, NOT NEW WORK, and it is reachable at HEAD before any chat-surface
// code. injectComposerText (chat.go) writes the text, sleeps submitframe.Gap, then writes the
// CR -- and the write itself takes NO LOCK: tapSub.Input is a bare `return s.t.up.Input(p)`
// (sessiontap.go:366-370), and composerSend has already released itemMu (chat.go:184) before
// deliverComposerText is called (:186). Two things therefore land inside one message:
//
//	ANOTHER PHONE SEND. Both pass the same expected_turn -- the turn id only advances when the
//	CLI echoes -- so nothing refuses either of them, and the PTY receives
//	text_A, text_B, CR, CR: ONE SUBMITTED CONCATENATION AND ONE EMPTY SUBMIT.
//
//	THE OWNER'S OWN DRAFT. This is the B13 gap that chat.go:337-345 discloses in its own
//	words: "the phone's text is APPENDED to it and the CR submits the CONCATENATION: a message
//	nobody wrote".
//
// The first case is the one the redesign makes routine: a chat surface is exactly where a
// person fires three short messages in a row, and the composer is about to stop being greyed.
//
// THE FIX THESE TESTS DESCRIBE is deliberately weaker than ADR-017:175's
// expected_input_revision, and that is the point. It never characterises the CLI's input
// region -- the guess chat.go:345-357 rightly refuses to make. The shim owns the PTY's single
// serialised writer (internal/shim/server.go, ptyWriter, a mutex taken on every write), so it
// can count bytes written since the last forwarded submit and answer one question absolutely:
// HAS ANYBODY WRITTEN TO THIS PTY SINCE THE LAST SUBMIT. Text and CR then go out under one
// hold of that same lock, or the send is refused having written nothing.
//
// It errs safe by construction: typed-then-deleted-back-to-empty still refuses.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/submitframe"
)

// newKeystrokeRig is the Claude arm: a session whose adapter PROVES the keystroke composer
// seam, so composer_send resolves to the PTY sink. It is the only arm that can merge, and
// the only ComposerKeys implementor in the tree is Claude
// (internal/adapter/claude/interaction.go).
func newKeystrokeRig(t *testing.T) *r7ComposerRig {
	t.Helper()
	r := newR7ComposerRig(t, false)
	r.sk.adapterFor = func(string) (adapter.Adapter, bool) {
		return &r7KeystrokeAdapter{Adapter: newPlainAdapter().Adapter}, true
	}
	// Pin the capability record a Claude-shaped session carries. Without it the daemon
	// derives one lazily and requireStructuredComposer can answer differently for two sends
	// a fraction of a second apart -- which would make this file's subject unreachable
	// behind a refusal that has nothing to do with it.
	r.sk.registerSessionCapabilities(r.local, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	return r
}

// sendBare drives composerSend WITHOUT touching *testing.T, so it is safe to call from a
// goroutine (t.Fatalf from a non-test goroutine is undefined behaviour).
func (r *r7ComposerRig) sendBare(expectedTurn, text, opID string) (protocol.ErrorCode, error) {
	return r.sk.api.ComposerSend(r.sk.api.endpointID, opID, protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: expectedTurn, Text: text,
	})
}

// awaitSubmittedLines drains the owner's attachment until the fake CLI has reported `want`
// stdin lines, and returns them in order with the "got: " prefix stripped. The fake agent's
// `ask` step reads ONE line and echoes `got: <line>` (internal/fakeagent/fakeagent.go:106-117),
// so one echoed line is exactly one SUBMIT the PTY saw.
func awaitSubmittedLines(att *protocol.Attachment, want int, within time.Duration) []string {
	var buf []byte
	timeout := time.After(within)
	for {
		if lines := submittedLines(string(buf)); len(lines) >= want {
			return lines
		}
		select {
		case f, ok := <-att.Frames():
			if !ok {
				return submittedLines(string(buf))
			}
			buf = append(buf, f...)
		case <-timeout:
			return submittedLines(string(buf))
		}
	}
}

// submittedLines pulls every `got: ...` report out of a drained frame buffer.
func submittedLines(drained string) []string {
	var out []string
	for _, raw := range strings.Split(drained, "got:")[1:] {
		line := raw
		if j := strings.IndexAny(line, "\r\n"); j >= 0 {
			line = line[:j]
		}
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// TestSlice0_TwoPhoneSendsAreNotMergedIntoOneSubmit is the defect the committee ranked above
// the whole redesign, and it needs no concurrency luck to reproduce: the second send is
// issued INSIDE the first one's submitframe.Gap, which is the window injectComposerText holds
// open by design between the text and the CR.
//
// At HEAD the PTY sees `alpha`, `bravo`, CR, CR -- so the CLI reports "alphabravo" and then an
// empty line. Two messages went in; one nobody wrote came out, and the other vanished.
func TestSlice0_TwoPhoneSendsAreNotMergedIntoOneSubmit(t *testing.T) {
	r := newKeystrokeRig(t)

	var wg sync.WaitGroup
	var firstCode protocol.ErrorCode
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		firstCode, firstErr = r.sendBare("", "alpha", "devA:01JSLICE0ALPHA0000000000")
	}()

	// Land the second send inside the first's text-to-CR window. The gap is 150 ms
	// (internal/submitframe), so a third of it is comfortably inside and does not depend on
	// scheduling luck in either direction.
	time.Sleep(submitframe.Gap / 3)
	secondCode, secondErr := r.sendBare("", "bravo", "devA:01JSLICE0BRAVO0000000000")
	wg.Wait()

	if firstErr != nil || firstCode != "" {
		t.Fatalf("the first send was refused: code %q err %v", firstCode, firstErr)
	}
	if secondErr != nil || secondCode != "" {
		t.Fatalf("the second send was refused: code %q err %v. A send that lands while another "+
			"is mid-submit must WAIT for the writer, not be turned away", secondCode, secondErr)
	}

	lines := awaitSubmittedLines(r.att, 2, 20*time.Second)
	want := []string{"alpha", "bravo"}
	if len(lines) < 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("the session's stdin saw %q, want %q.\n"+
			"TWO SENDS WERE MERGED INTO ONE SUBMIT. tapSub.Input takes no lock, so the second "+
			"message's text lands between the first's text and its CR, and the carriage return "+
			"submits the concatenation -- a message nobody wrote -- while the second CR submits "+
			"an empty line. The shim owns the only serialised writer to this PTY; the text and "+
			"the CR of one message must cross it under ONE hold of that lock.", lines, want)
	}
}

// TestSlice0_AnOwnerDraftIsNeverMergedWithAPhoneSend is B13, stated as a test rather than as a
// comment. chat.go:337-345 has disclosed it in writing since Wave R6; the owner's ruling that
// the phone and the terminal are both live at all times is what turns a disclosed gap into the
// ordinary case.
//
// The contract: the send is REFUSED, the draft is untouched, and the words the user typed on
// the phone are still theirs to retry. Refusing is the posture the same file already takes
// twenty lines up for the other half of this message ("refused, never truncated").
func TestSlice0_AnOwnerDraftIsNeverMergedWithAPhoneSend(t *testing.T) {
	r := newKeystrokeRig(t)

	// The owner is half-way through a line at the terminal. No newline: this is a DRAFT.
	const draft = "half a thought"
	if err := r.att.Input([]byte(draft)); err != nil {
		t.Fatalf("owner types a draft: %v", err)
	}
	if ok, drained := awaitFrames(r.att, draft, 10*time.Second); !ok {
		t.Fatalf("the draft never reached the session's terminal; drained %q", drained)
	}

	code, err := r.sendBare("", "ship it", "devA:01JSLICE0DRAFT0000000000")
	if code != protocol.CodeInputBusy {
		t.Fatalf("phone send against a dirty input line = code %q err %v, want %q.\n"+
			"THE SEND WAS ACCEPTED AND THE PTY NOW HOLDS THE OWNER'S DRAFT JOINED TO THE PHONE'S "+
			"WORDS. B13 is disclosed at chat.go:337-345 and this is the state R1 makes ordinary. "+
			"The shim knows bytes have been written since the last submit; that is the whole of "+
			"the fact needed to refuse.", code, err, protocol.CodeInputBusy)
	}

	// The draft must survive BYTE-EXACT. The owner presses return themselves and the CLI
	// reports what was actually on the line.
	if err := r.att.Input([]byte("\n")); err != nil {
		t.Fatalf("owner submits their own draft: %v", err)
	}
	lines := awaitSubmittedLines(r.att, 1, 20*time.Second)
	if len(lines) < 1 || lines[0] != draft {
		t.Fatalf("the session's stdin saw %q, want [%q]. A refused send must write NOTHING: the "+
			"owner's line is theirs, and a refusal that still left bytes behind would be worse "+
			"than the merge it was trying to prevent", lines, draft)
	}
}
