package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave G item G.4, the half that lives where the writing
// actually happens: docs/specifications/chat-surface-plan.md §9, "Nothing is written into the
// terminal's own output ... A test proves the attribution chrome writes zero bytes into the
// PTY". Bead: agents-tracker-tbpm.9. Evidence: docs/verification/chat-surface.md, Wave G.
//
// THE RULE. ADR-017 T10 -- "The PTY is sacred ... the shim-owned PTY hosts the vendor's real
// TUI, byte-exact, always" (ADR-013 decision 1) -- and the plan names the two harms it is
// protecting against: PREFIXING changes the prompt the agent receives, and DISPLAY BYTES
// corrupt the CLI's own screen. Wave G's attribution -- `phone` on the board row, source
// "phone" plus the operation id on the journalled item -- is therefore allowed to be a fact
// the daemon HOLDS and a word the owner's own terminal DRAWS, and is allowed to be neither a
// prefix on the message nor a mark on the session's screen.
//
// WHY IT NEEDS A TEST RATHER THAN A COMMENT. The daemon is the party that both attributes the
// message and writes it: composerSend records the injection and then hands the very same text
// to the sink, so the two are one function apart, and the shape of the mistake -- "just
// prepend who it came from" -- is one line of plausible code away at every future edit. The
// assertion below is EQUALITY WITH THE SENT TEXT on the agent's own stdin, not containment:
// containment is exactly what a prefix satisfies.
//
// The other half -- that the terminal-side chrome writes nothing toward the session -- is
// proved on the seam that could carry it, in internal/attach/g4_ptybytesacred_test.go. That the
// attribution IS drawn where it belongs is already pinned elsewhere and is not restated here:
// internal/tui/remotecontrol_test.go's TestRoster_RemoteControlledRowIsMarked puts the marker on
// the board row. The complete claim is the pair -- shown on the owner's own surfaces, written
// into the session never.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/protocol"
)

// attributionWords are the words Wave G's chrome is made of: the board marker
// (internal/tui, phoneSentMarker -- "phone sent 09:41"), the phrasing G.3 reserves for the
// attach row, and the source token the journal stamps. None of them is a byte the session may
// see. "phone" alone is listed rather than the whole marker on purpose: it is a prefix of
// every wording this row has ever had, so a leak is caught whatever the copy becomes.
var attributionWords = []string{"phone", "operation_id", "devA:"}

// TestG4_TheAttributionChromeWritesZeroBytesIntoThePTY is the whole of G.4's daemon-side
// claim in one run: a phone message is delivered, the attribution turns ON everywhere it is
// supposed to -- the roster marks the session and the journalled item names the phone and the
// operation that sent it -- and the session's own terminal stream is byte-identical to what it
// would have been had nobody attributed anything.
func TestG4_TheAttributionChromeWritesZeroBytesIntoThePTY(t *testing.T) {
	r := newKeystrokeRig(t)

	const text = "ship it"
	const op = "devA:01JG4BYTESACRED0000000"
	code, err := r.sendBare("", text, op)
	if err != nil || code != "" {
		t.Fatalf("the send was refused: code %q err %v", code, err)
	}

	// THE ATTRIBUTION IS ON. Without this the assertions below would pass on a build that
	// simply never attributes anything, which is not the claim being made.
	//
	// phoneRecentlyActive is the fact itself; serve.go's `driven` is what turns it into the
	// board's marker (`RemoteControlled` on every SessionView), and it is wired only where a
	// remote listener exists -- which this in-process rig has none of, because it drives the
	// coreAPI seam directly rather than through a paired device.
	if !r.sk.phoneRecentlyActive(r.local) {
		t.Fatal("the daemon recorded no phone activity for a delivered message, so this test " +
			"proved only that a build with no attribution writes no attribution")
	}

	// The CLI echoes the injected prompt back through its own hook, and the daemon -- the only
	// party that watched the injection -- stamps the RECORD with who sent it.
	r.sk.captureInteractions(r.local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: text, Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})
	it := r6UserMessage(t, r.sk, r.local, text)
	if got, _ := it["source"].(string); got != adapter.SourcePhone {
		t.Fatalf("the echoed prompt's source = %q, want %q: with no attribution recorded there is "+
			"nothing for this test to prove is absent from the PTY", got, adapter.SourcePhone)
	}
	if got, _ := it["operation_id"].(string); got != op {
		t.Fatalf("the echoed prompt's operation_id = %q, want %q", got, op)
	}

	raw, lines := drainStream(r.att, 1, 750*time.Millisecond, 25*time.Second)

	// 1. THE PROMPT THE AGENT RECEIVES IS THE MESSAGE, and nothing more. Equality, because a
	//    prefix is precisely what containment would let through.
	if len(lines) != 1 || lines[0] != text {
		t.Fatalf("the session's stdin saw %q, want exactly [%q].\n"+
			"THE AGENT READS WHAT IS ON THAT LINE AS THE USER'S WORDS. A marker, a name or a "+
			"source prefixed here is not chrome -- it is a different question being asked of the "+
			"model, in words the person who sent it never wrote.", lines, text)
	}

	// 2. THE SESSION'S SCREEN NEVER LEARNS ANY OF IT. The drained stream is everything the
	//    session's terminal emitted across the send, echo included.
	for _, word := range attributionWords {
		if strings.Contains(raw, word) {
			t.Fatalf("the session's terminal stream carried the attribution word %q.\n"+
				"Drained: %q.\nDisplay bytes written into a CLI's own screen corrupt it: the "+
				"emulator that owns those cells is the agent's, the grid is its output, and swarm "+
				"does not get to draw on it. The board row is where this fact is shown.", word, raw)
		}
	}
}

// TestG4_TheShippedClaudeAdapterTypesTheMessageAndNothingElse pins the same rule one layer
// down, at the seam a future prefix would most plausibly be added to -- and it pins it on the
// SHIPPED adapter, reached through the production resolver, not on a test double that returns
// the text unchanged because a test wrote it that way. The only ComposerKeys implementor in
// the tree is Claude, and until now nothing anywhere asserted what it returns.
func TestG4_TheShippedClaudeAdapterTypesTheMessageAndNothingElse(t *testing.T) {
	ad, ok := registry.New("claude")
	if !ok {
		t.Fatal("the production registry has no claude adapter; the keystroke sink has no implementor")
	}
	kc, ok := adapter.AsKeystrokeComposer(ad)
	if !ok {
		t.Fatal("the shipped claude adapter proves no keystroke composer seam, so a phone message " +
			"to a claude session has no sink at all")
	}
	for _, text := range []string{"ship it", "yes", "", "a message with  spacing  kept", "半分の考え"} {
		if got := string(kc.ComposerKeys(text)); got != text {
			t.Errorf("ComposerKeys(%q) = %q. The keystrokes ARE the message: anything added here "+
				"is added to the prompt the model answers, in words the sender never wrote, and "+
				"ADR-017 T10 keeps this PTY byte-exact", text, got)
		}
	}
}

// TestG4_AClaudeShapedSessionResolvesToThatKeystrokeSink is the other half of the pairing: the
// sink the daemon picks for a session with no backend IS the adapter asserted above, so the
// two facts compose into "what a claude session receives is the message".
func TestG4_AClaudeShapedSessionResolvesToThatKeystrokeSink(t *testing.T) {
	r := newKeystrokeRig(t)
	sink, code, err := r.sk.resolveMessageSink(r.local, r.session)
	if err != nil || code != "" {
		t.Fatalf("the keystroke sink did not resolve: code %q err %v", code, err)
	}
	if sink.backend != nil || sink.keystrok == nil {
		t.Fatal("a session with no live backend resolved to something other than the keystroke " +
			"seam; the arm that writes into a PTY is the one this rule is about")
	}
}

// TestG4_ARefusedSendLeavesTheSessionsTerminalUntouched is the rule's other direction, and it
// is the one a "helpful" build breaks first: a message that was refused must not leave a note
// behind explaining itself. Refused is a state the PHONE draws.
func TestG4_ARefusedSendLeavesTheSessionsTerminalUntouched(t *testing.T) {
	r := newKeystrokeRig(t)

	const draft = "half a thought"
	if err := r.att.Input([]byte(draft)); err != nil {
		t.Fatalf("owner types a draft: %v", err)
	}
	if ok, drained := awaitFrames(r.att, draft, 10*time.Second); !ok {
		t.Fatalf("the draft never reached the session's terminal; drained %q", drained)
	}

	code, err := r.sendBare("", "ship it", "devA:01JG4REFUSEDQUIET000000")
	if code != protocol.CodeInputBusy {
		t.Fatalf("phone send against a dirty input line = code %q err %v, want %q", code, err, protocol.CodeInputBusy)
	}

	if err := r.att.Input([]byte("\r")); err != nil {
		t.Fatalf("owner submits their own draft: %v", err)
	}
	raw, lines := drainStream(r.att, 1, 750*time.Millisecond, 25*time.Second)
	if len(lines) != 1 || lines[0] != draft {
		t.Fatalf("the session's stdin saw %q, want exactly [%q]", lines, draft)
	}
	for _, word := range append(attributionWords, "refused", "not sent", "ship it") {
		if strings.Contains(raw, word) {
			t.Fatalf("the session's terminal stream carried %q after a REFUSED send.\n"+
				"Drained: %q.\nA refusal is news for the sender, drawn on the phone as "+
				"bubble.refused. Explaining it on the agent's screen writes bytes nobody asked "+
				"for into a stream the agent owns.", word, raw)
		}
	}
}
