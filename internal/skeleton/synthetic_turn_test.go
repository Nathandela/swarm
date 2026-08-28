package skeleton

// Phone refit W2.4, round-1 review ruling (bead agents-tracker-d45a.2): Claude's own envelopes
// -- a teammate message, a task notification, a slash command's stdout -- fire UserPromptSubmit,
// and a user_message is the ONLY turn-opening signal Claude gives (turnIDLocked; the adapter
// sets TurnRef nowhere). W2.4 as first shipped shaped them as nothing, so a session whose work
// starts from an envelope opened NO turn: its tool items carried turn_id "", the phone drew it
// idle, and Stop was refused (chat.go, the stale_turn / empty expected_turn preconditions).
//
// KEEP THE TURN, DROP THE BUBBLE: the adapter shapes the envelope as a SourceSynthetic
// user_message; the daemon runs the turn-open logic on it and neither persists nor publishes
// it, so nothing reaches the wire or the phone.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestSyntheticPrompt_OpensATurnAndPublishesNoMessage(t *testing.T) {
	sk := assemble(t)
	const session = "s-synthetic"

	// The SHIPPED adapter, fed the CLI's own envelope.
	raw, err := json.Marshal(map[string]string{
		"hook_event_name": "UserPromptSubmit",
		"prompt":          `<teammate-message teammate_id="team-lead" summary="assign">start on task 1</teammate-message>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	sk.captureInteractions(session, claude.New(), adapter.HookPayload{Event: "UserPromptSubmit", Raw: raw})

	sk.itemMu.Lock()
	sk.initInteractionsLocked()
	turn := sk.turnIDs[session]
	sk.itemMu.Unlock()
	if turn == "" {
		t.Fatal("a synthetic prompt opened no turn: every tool item that follows carries turn_id \"\", " +
			"the phone draws the session idle, and Stop is refused for want of a turn to name")
	}

	// The work that follows carries that turn ...
	sk.captureInteractions(session, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusInProgress, Ref: "tool-1", Tool: "Bash",
		Action: adapter.ToolAction{Type: "execute", Command: "go test ./..."},
	}), adapter.HookPayload{Event: "PreToolUse"})
	got := awaitItems(t, sk, session, 1)
	if k := itemString(t, got[0], "kind"); k != adapter.KindToolRun {
		t.Fatalf("the first published item is a %q; the envelope reached the wire as a message", k)
	}
	if id := itemString(t, got[0], "turn_id"); id != turn {
		t.Fatalf("the tool item carries turn_id %q, want the turn the envelope opened, %q", id, turn)
	}

	// ... and the envelope itself never lands, even after the append floor's window has passed.
	time.Sleep(3 * remotegw.DefaultAppendWindow)
	for _, item := range interactionItems(t, sk, session) {
		if k := itemString(t, item, "kind"); k == adapter.KindUserMessage {
			t.Fatalf("a user_message was published for the CLI's own envelope: %v", item)
		}
	}
}
