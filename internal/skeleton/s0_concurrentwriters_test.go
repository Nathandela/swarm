package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the SECOND test the audit committee owes Slice 0:
// docs/specifications/chat-surface-plan.md §12, "Concurrent owner Enter, phone send, and two
// distinct phone sends: no concatenation, no duplicate submit, no misordered turn". Bead:
// agents-tracker-bzfe. Evidence: docs/verification/chat-surface.md, "Owed before this is
// called complete".
//
// WHAT THIS ADDS OVER s0_writerserialise_test.go. That file races TWO PHONE SENDS, which are
// both writes the daemon itself makes. This one puts the OWNER'S OWN HAND on the same PTY at
// the same instant, which is the state ruling R1 makes ordinary: "a live session is typeable
// from phone and terminal simultaneously, always". The owner's keystrokes do not travel
// through composerSend, take no daemon lock, and are answerable only by the shim -- so the
// serialization claim is being tested at its one true chokepoint (ptyWriter.mu) rather than
// at a daemon-side gate that the owner's hand bypasses.
//
// THE CONTRACT, and it holds under EVERY interleaving:
//
//	WHOLE. Every line the session's stdin submits is exactly one of the three messages.
//	Never a concatenation ("half a thoughtalpha"), never an empty submit, never a fragment.
//	NEVER TWICE. An accepted send submits its message exactly once.
//	ALL OR NOTHING. A send that answered OK is on the wire; a send refused input_busy wrote
//	NOTHING, so its words appear nowhere.
//	THE OWNER IS NEVER REFUSED. Their Enter is a raw keystroke on their own terminal and no
//	part of this mechanism may turn it away.
//
// "NO MISORDERED TURN" IS THE WHOLE-LINE CLAIM, stated for the keystroke sink. Claude has no
// turn-scoped entry point -- ComposerKeys types into the CLI's own composer -- so what the
// conversation's order MEANS here is the order of submitted lines, and a message that arrives
// split across two of them has been misordered no matter which line it lands on.

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/submitframe"
)

const stdinAuditBarrier = "__swarm_stdin_audit_barrier__"

// newAuditedKeystrokeRig gives this timing-sensitive test a unique observation
// of what the fake process actually read. Terminal frames remain useful for
// liveness, but are not an input oracle: PTY ECHO can paint bytes before the
// canonical line discipline has delivered them to the child.
func newAuditedKeystrokeRig(t *testing.T) (*r7ComposerRig, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "fake-agent-stdin.bin")
	return newKeystrokeRigWithFakeOptions(t, "--stdin-log", logPath), logPath
}

// finishStdinAudit writes a final owner line only after every concurrent writer
// has returned. Seeing the fake process report that barrier is a deterministic
// liveness fence: every earlier PTY write has then been consumed and copied to
// the audit file. No timing settle or PTY-echo interpretation is part of the
// integrity verdict.
func finishStdinAudit(t *testing.T, r *r7ComposerRig, logPath string) []string {
	t.Helper()
	if err := r.att.Input([]byte(stdinAuditBarrier + "\r")); err != nil {
		t.Fatalf("write stdin-audit barrier: %v", err)
	}
	if ok, drained := awaitFrames(r.att, "got: "+stdinAuditBarrier, 20*time.Second); !ok {
		t.Fatalf("fake agent never consumed the stdin-audit barrier; drained %q", drained)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake-agent stdin audit: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("fake-agent stdin audit is not a sequence of complete lines: %q", raw)
	}
	parts := bytes.Split(raw, []byte{'\n'})
	parts = parts[:len(parts)-1] // the byte after the final newline is empty
	lines := make([]string, len(parts))
	for i, part := range parts {
		lines[i] = string(part) // byte-exact: deliberately no TrimSpace/CR removal
	}
	if len(lines) == 0 || lines[len(lines)-1] != stdinAuditBarrier {
		t.Fatalf("fake-agent stdin audit ended in %q, want barrier %q; raw=%q",
			lines, stdinAuditBarrier, raw)
	}
	return lines[:len(lines)-1]
}

// drainStream returns the RAW bytes as well as the fake CLI's parsed `got:` reports, settling
// after `want` reports. Callers that must prove something is ABSENT from the terminal stream can
// inspect the stream rather than relying only on the summary.
func drainStream(att *protocol.Attachment, want int, settle, within time.Duration) (string, []string) {
	var buf []byte
	deadline := time.After(within)
	var quiet <-chan time.Time
	for {
		seen := len(submittedLines(string(buf)))
		if seen >= want && quiet == nil {
			quiet = time.After(settle)
		}
		select {
		case f, ok := <-att.Frames():
			if !ok {
				return string(buf), submittedLines(string(buf))
			}
			buf = append(buf, f...)
			if len(submittedLines(string(buf))) > seen {
				quiet = nil // a fresh report restarts the settle window
			}
		case <-quiet:
			return string(buf), submittedLines(string(buf))
		case <-deadline:
			return string(buf), submittedLines(string(buf))
		}
	}
}

// countOf reports how many times want appears in lines.
func countOf(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

// assertEveryLineIsAWholeMessage is the shared verdict: the session's stdin holds nothing but
// whole messages, each accepted one exactly once and each refused one not at all.
func assertEveryLineIsAWholeMessage(t *testing.T, lines []string, landed map[string]bool) {
	t.Helper()
	for _, l := range lines {
		if _, ok := landed[l]; !ok {
			t.Fatalf("the session's stdin submitted %q, which is nobody's message.\n"+
				"The lines were %q and the messages in flight were %v.\n"+
				"A LINE THAT IS NOT ONE OF THE MESSAGES IS TWO MESSAGES RUN TOGETHER OR ONE CUT "+
				"IN HALF: the text and the carriage return of a message must cross the PTY's only "+
				"serialized writer under ONE hold of its lock, and the owner's own keystrokes take "+
				"that same lock or they can land inside somebody else's message.", l, lines, landed)
		}
	}
	for text, want := range landed {
		got := countOf(lines, text)
		switch {
		case want && got != 1:
			t.Fatalf("%q was submitted %d times; it answered OK, so it belongs on the wire exactly "+
				"once. The lines were %q.\nTwice is a duplicate submit; never is a message the "+
				"sender was told had been delivered.", text, got, lines)
		case !want && got != 0:
			t.Fatalf("%q was submitted %d times after being REFUSED. The lines were %q.\n"+
				"A refusal promises nothing was written -- \"nothing was typed\" is the sentence the "+
				"refusal itself carries -- and a refusal that still left bytes behind is worse than "+
				"the merge it was trying to prevent.", text, got, lines)
		}
	}
}

// TestSlice0_OwnerCompleteInputFrameAndTwoPhoneSendsEachLandAsOneWholeMessage is the
// committee's case with the owner submitting a COMPLETE line and its return in one Input call
// while both phone messages are already in flight.
//
// This is the arm with no acceptable refusal. A write that ends in a return leaves the input
// line clean, so at no instant is there a draft for the quiescence guard to refuse on -- and
// all three messages must therefore land, whole, each exactly once.
func TestSlice0_OwnerCompleteInputFrameAndTwoPhoneSendsEachLandAsOneWholeMessage(t *testing.T) {
	r, stdinLog := newAuditedKeystrokeRig(t)

	const ownerLine = "the owner's own question"
	landed := map[string]bool{ownerLine: true}

	var wg sync.WaitGroup
	var codes [2]protocol.ErrorCode
	var errs [2]error
	texts := [2]string{"alpha from the phone", "bravo from the phone"}
	ops := [2]string{"devA:01JS0CONCURRENTALPHA000", "devA:01JS0CONCURRENTBRAVO000"}

	start := make(chan struct{})
	for i := range texts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], errs[i] = r.sendBare("", texts[i], ops[i])
		}(i)
	}
	wg.Add(1)
	var ownerErr error
	go func() {
		defer wg.Done()
		<-start
		// ONE INPUT FRAME carries both the text and its return. Unlike the next test, this
		// owner does not park a previously typed draft before the concurrent sends begin.
		ownerErr = r.att.Input([]byte(ownerLine + "\r"))
	}()
	close(start)
	wg.Wait()

	if ownerErr != nil {
		t.Fatalf("the owner's complete input frame failed: %v. The terminal's keystrokes are not this "+
			"mechanism's to refuse", ownerErr)
	}
	for i := range texts {
		if errs[i] != nil || codes[i] != "" {
			t.Fatalf("phone send %q was refused: code %q err %v.\n"+
				"Every write in this test ends in a return, so the input line is clean at every "+
				"instant a send could observe it. A refusal here means the quiescence guard is "+
				"counting bytes that ran their own line -- or its own message's -- as a draft "+
				"somebody is holding.", texts[i], codes[i], errs[i])
		}
		landed[texts[i]] = true
	}

	lines := finishStdinAudit(t, r, stdinLog)
	if len(lines) != 3 {
		t.Fatalf("the session's stdin submitted %d lines %q, want exactly 3.\n"+
			"Three messages went in. Fewer lines means two were run together; more means one was "+
			"split and its orphaned carriage return submitted an empty line -- the exact pair of "+
			"symptoms Slice 0 exists to remove.", len(lines), lines)
	}
	assertEveryLineIsAWholeMessage(t, lines, landed)
}

// TestSlice0_AnOwnerDraftAndItsEnterNeverSplitAPhoneSend is the same three writers with the
// owner typing the way a person actually does: the thought first, the return a moment later,
// with both phone sends racing the gap between them.
//
// HERE A REFUSAL IS A CORRECT ANSWER, and which sends get one is genuinely undetermined --
// that is the interleaving, not a weakness in the test. What is determined is the shape of
// the outcome: a send is delivered WHOLE or it wrote nothing, the owner's line is exactly the
// words the owner typed, and no line on that PTY is two messages wearing one carriage return.
func TestSlice0_AnOwnerDraftAndItsEnterNeverSplitAPhoneSend(t *testing.T) {
	r, stdinLog := newAuditedKeystrokeRig(t)

	const draft = "half a thought"
	landed := map[string]bool{draft: true}

	var wg sync.WaitGroup
	var codes [2]protocol.ErrorCode
	var errs [2]error
	texts := [2]string{"alpha from the phone", "bravo from the phone"}
	ops := [2]string{"devA:01JS0DRAFTRACEALPHA000", "devA:01JS0DRAFTRACEBRAVO000"}

	// The draft is parked FIRST, so the phone sends race a line that is already dirty and the
	// owner's Enter races them back. Nobody waits for anybody.
	if err := r.att.Input([]byte(draft)); err != nil {
		t.Fatalf("owner types a draft: %v", err)
	}
	if ok, drained := awaitFrames(r.att, draft, 10*time.Second); !ok {
		t.Fatalf("the draft never reached the session's terminal; drained %q", drained)
	}

	start := make(chan struct{})
	for i := range texts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], errs[i] = r.sendBare("", texts[i], ops[i])
		}(i)
	}
	wg.Add(1)
	var ownerErr error
	go func() {
		defer wg.Done()
		<-start
		// The owner finishes their line inside the window the sends are contending for.
		time.Sleep(submitframe.Gap / 3)
		ownerErr = r.att.Input([]byte("\r"))
	}()
	close(start)
	wg.Wait()

	if ownerErr != nil {
		t.Fatalf("the owner's own Enter failed: %v", ownerErr)
	}
	for i := range texts {
		switch {
		case errs[i] == nil && codes[i] == "":
			landed[texts[i]] = true
		case codes[i] == protocol.CodeInputBusy:
			landed[texts[i]] = false
		default:
			t.Fatalf("phone send %q answered code %q err %v. The only two honest answers to a "+
				"contended line are DELIVERED and input_busy; anything else leaves the sender "+
				"unable to tell whether their words are on the wire", texts[i], codes[i], errs[i])
		}
	}

	lines := finishStdinAudit(t, r, stdinLog)
	assertEveryLineIsAWholeMessage(t, lines, landed)
	if got := countOf(lines, draft); got != 1 {
		t.Fatalf("the owner's own line was submitted %d times as %q; the stdin lines were %q.\n"+
			"THE OWNER'S WORDS ARE THEIRS. A phone message that landed between their draft and "+
			"their return would submit a sentence nobody wrote, and their return would then run "+
			"whatever was left.", got, draft, lines)
	}
}
