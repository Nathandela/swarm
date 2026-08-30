package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's daemon-side composer_send application --
// Mirror M2.4, IS-LIFE-5, ADR-009 (5)/(8), playbook §8.1 step 3 ("carry expected_turn
// ... write text plus submit framing; and correlate the exact subsequent UserPromptSubmit
// item back to the phone operation"). Bead: agents-tracker-hggx.7. Undefined symbols ->
// compile-fail RED is expected and valid (spool-red.txt precedent in this same package).
//
// THE CONTRACT these tests freeze, on the coreAPI seam the protocol Server holds
// (mirroring ApproveInteraction exactly):
//
//	func (a *coreAPI) ComposerSend(machine, operationID string, req protocol.ComposerSendReq) (protocol.ErrorCode, error)
//
//   - ORDERING: expected_turn is signed render context, not a destructive-target
//     precondition. Accepted messages enter the session FIFO and are delivered against
//     the daemon/provider's current state, so a newer or closed rendered turn does not
//     make ordinary conversation text stale. Stop remains strictly turn-scoped.
//   - APPLICATION: an accepted send writes the text into the session's PTY through the
//     daemon's own input path with submit framing (the r3p submit-boundary discipline:
//     the CR that runs the message never shares a write with it), observable here as the
//     fake CLI reporting the exact text on its stdin.
//   - ATTRIBUTION (M2.4 "source: phone via injection-time correlation", 8.1 step 3):
//     the daemon remembers the accepted injection, and the NEXT captured UserPromptSubmit
//     user_message whose text matches is journalled with source "phone" and the phone
//     op's operation_id -- even though the ADAPTER reported source owner, because the
//     adapter cannot know and the daemon can (ADR-010 §3: the daemon is the sole producer
//     of what goes on the wire). An owner-typed prompt with no matching pending injection
//     stays source "owner": attribution is honest in both directions, never guessed.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// r6OpenTurn captures one user_message and returns the turn id it opened, read back from
// the journalled item itself (the daemon's answer, not a reimplementation of it).
func r6OpenTurn(t *testing.T, sk *Daemon, local, text string, already int) string {
	t.Helper()
	sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: text, Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})
	items := awaitItems(t, sk, local, already+1)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i]["kind"] == adapter.KindUserMessage && items[i]["text"] == text {
			turn, _ := items[i]["turn_id"].(string)
			if turn == "" {
				t.Fatalf("the user_message %q opened no turn: %v", text, items[i])
			}
			return turn
		}
	}
	t.Fatalf("the user_message %q never reached the journal", text)
	return ""
}

// r6UserMessage finds the journalled user_message with the given text.
func r6UserMessage(t *testing.T, sk *Daemon, local, text string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, it := range interactionItems(t, sk, local) {
			if it["kind"] == adapter.KindUserMessage && it["text"] == text {
				return it
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no user_message %q reached the journal", text)
	return nil
}

// TestR6ComposerApply_AnIdleSessionAcceptsTheEmptyExpectedTurnAndTypesTheText is the
// success path at rest: no turn is open, the phone rendered no turn, the send goes in.
func TestR6ComposerApply_AnIdleSessionAcceptsTheEmptyExpectedTurnAndTypesTheText(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-idle"))
	machine := r.sk.api.endpointID

	code, err := r.sk.api.ComposerSend(machine, "devA:01JIDLE", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: "", Text: "ship it",
	})
	if err != nil || code != "" {
		t.Fatalf("idle composer send refused: code %q err %v; the composer is gated on online "+
			"only (M2.4) and an idle session is exactly where a send STARTS a turn", code, err)
	}
	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported its stdin after an ACCEPTED send; drained %q", drained)
	}
	if !strings.Contains(drained, "ship it") {
		t.Errorf("the session's stdin reported %q, want the sent text \"ship it\": an accepted "+
			"send that typed something else submitted a message nobody wrote", drained)
	}
}

// TestR6ComposerApply_ATurnAdvancingBetweenRenderAndTapQueuesTheMessageInDialogOrder
// pins the conversational contract: the phone rendered turn A, the owner opened turn B,
// and the phone's message still joins the current dialog rather than becoming stale.
func TestR6ComposerApply_ATurnAdvancingBetweenRenderAndTapQueuesTheMessageInDialogOrder(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-race"))
	machine := r.sk.api.endpointID
	already := len(interactionItems(t, r.sk, r.local))

	turnA := r6OpenTurn(t, r.sk, r.local, "first question", already)
	turnB := r6OpenTurn(t, r.sk, r.local, "second question", already+1)
	if turnA == turnB {
		t.Fatalf("two user_messages share one turn id %q; IS-ENV-1 opens a NEW turn per user_message", turnA)
	}

	code, err := r.sk.api.ComposerSend(machine, "devA:01JRACE", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: turnA, Text: "reply to the first question",
	})
	if err != nil || code != "" {
		t.Fatalf("send carrying superseded render context refused: code %q err %v", code, err)
	}

	// A send carrying the current rendered id follows it in the same FIFO.
	code, err = r.sk.api.ComposerSend(machine, "devA:01JRACE2", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: turnB, Text: "reply to the second question",
	})
	if err != nil || code != "" {
		t.Fatalf("send against the current turn refused: code %q err %v", code, err)
	}
	lines := awaitSubmittedLines(r.att, 2, 20*time.Second)
	if len(lines) < 2 || lines[0] != "reply to the first question" || lines[1] != "reply to the second question" {
		t.Fatalf("queued messages reached the session as %q, want dialog order [first second]", lines)
	}
}

// TestR6ComposerApply_AClosedRenderedTurnStillQueuesAConversationFollowup covers the other
// transition IS-ENV-1 defines: the turn closed before the tap, so the queued message starts
// the next conversational turn instead of asking the user to resend the same words.
func TestR6ComposerApply_AClosedRenderedTurnStillQueuesAConversationFollowup(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-closed"))
	machine := r.sk.api.endpointID
	already := len(interactionItems(t, r.sk, r.local))

	turnA := r6OpenTurn(t, r.sk, r.local, "the only question", already)
	r.sk.captureInteractions(r.local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted, Text: "the answer",
	}), adapter.HookPayload{Event: "Stop"})

	code, err := r.sk.api.ComposerSend(machine, "devA:01JCLOSED", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: turnA, Text: "follow-up against a finished turn",
	})
	if err != nil || code != "" {
		t.Fatalf("follow-up carrying a closed rendered turn refused: code %q err %v", code, err)
	}

	code, err = r.sk.api.ComposerSend(machine, "devA:01JAFTER", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: "", Text: "fresh follow-up",
	})
	if err != nil || code != "" {
		t.Fatalf("idle send after the turn closed refused: code %q err %v", code, err)
	}
	lines := awaitSubmittedLines(r.att, 2, 20*time.Second)
	if len(lines) < 2 || lines[0] != "follow-up against a finished turn" || lines[1] != "fresh follow-up" {
		t.Fatalf("post-close messages reached the session as %q, want both in FIFO order", lines)
	}
}

// TestR6ComposerApply_TheEchoedPromptIsStampedSourcePhoneWithTheOperationID is the
// injection-time correlation: the CLI echoes the injected prompt back through its own
// UserPromptSubmit hook, the ADAPTER honestly reports owner (it cannot know), and the
// daemon -- the only party that watched the injection -- stamps the item source "phone"
// and carries the phone op's id, so the transcript attributes the message to the hand
// that wrote it (8.1 step 3, IS-LIFE-5's carrier reasoning).
func TestR6ComposerApply_TheEchoedPromptIsStampedSourcePhoneWithTheOperationID(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-attrib"))
	machine := r.sk.api.endpointID

	code, err := r.sk.api.ComposerSend(machine, "devA:01JATTRIB", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: "", Text: "phone says hi",
	})
	if err != nil || code != "" {
		t.Fatalf("send refused: code %q err %v", code, err)
	}

	// The hook echo: the adapter shapes the prompt as it always has, source owner.
	r.sk.captureInteractions(r.local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: "phone says hi", Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})

	it := r6UserMessage(t, r.sk, r.local, "phone says hi")
	if got, _ := it["source"].(string); got != adapter.SourcePhone {
		t.Errorf("the echoed prompt's source = %q, want %q: the daemon watched the injection "+
			"and is the only party that can attribute it", got, adapter.SourcePhone)
	}
	if got, _ := it["operation_id"].(string); got != "devA:01JATTRIB" {
		t.Errorf("the echoed prompt's operation_id = %q, want devA:01JATTRIB: without the "+
			"correlation the phone cannot resolve its own send against the item it became", got)
	}
}

// TestR6ComposerApply_AnOwnerTypedPromptStaysSourceOwner is attribution's other
// direction, and it is what makes the first direction honest rather than optimistic: a
// prompt that does NOT match a pending injection keeps the adapter's owner attribution,
// and a non-matching prompt does not consume the pending correlation.
func TestR6ComposerApply_AnOwnerTypedPromptStaysSourceOwner(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-composer-owner"))
	machine := r.sk.api.endpointID

	code, err := r.sk.api.ComposerSend(machine, "devA:01JPEND", protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: "", Text: "the phone's pending text",
	})
	if err != nil || code != "" {
		t.Fatalf("send refused: code %q err %v", code, err)
	}

	// The owner races the echo with their own, DIFFERENT prompt at the terminal.
	r.sk.captureInteractions(r.local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: "the owner's own words", Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})

	it := r6UserMessage(t, r.sk, r.local, "the owner's own words")
	if got, _ := it["source"].(string); got != adapter.SourceOwner {
		t.Errorf("the owner's prompt was attributed %q, want owner: attribution is a fact the "+
			"daemon observed, never a guess that the next prompt must be the phone's", got)
	}
	if _, has := it["operation_id"]; has {
		t.Errorf("the owner's prompt carries a phone operation_id: %v", it)
	}

	// The pending correlation survives for the echo it actually matches.
	r.sk.captureInteractions(r.local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: "the phone's pending text", Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})
	it = r6UserMessage(t, r.sk, r.local, "the phone's pending text")
	if got, _ := it["source"].(string); got != adapter.SourcePhone {
		t.Errorf("the matching echo's source = %q, want phone: the owner's interleaved prompt "+
			"must not have consumed the correlation", got)
	}
}

// TestR6ComposerApply_AnUnknownSessionRefusesAndNothingIsWritten: the precondition is
// checked against a session this daemon actually runs; a stranger's id is a refusal with
// a code, never a silent no-op and never a write into some other session.
func TestR6ComposerApply_AnUnknownSessionRefusesAndNothingIsWritten(t *testing.T) {
	sk := assemble(t)
	code, err := sk.api.ComposerSend(sk.api.endpointID, "devA:01JNOWHERE", protocol.ComposerSendReq{
		Session: protocol.NamespacedID(sk.api.endpointID, "no-such-session"), Text: "hello?",
	})
	if err == nil || code == "" {
		t.Fatalf("composer send into an unknown session answered code %q err %v; want a coded "+
			"refusal -- OK here is a sent message no agent ever received", code, err)
	}
}
