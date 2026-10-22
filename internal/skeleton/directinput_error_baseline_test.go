package skeleton

// A turn that FAILS leaves the session at idle/error: codex reports a usage limit or a
// "model is at capacity" refusal as turn/completed{status:"failed"} (attention.go), and its
// composer is ready for the next message. Every input path records a direct-input baseline
// before it writes a byte (forwardInput, send_input), and a baseline the record refuses drops
// the input. Refusing idle/error therefore cut the owner off from the one session that most
// needs a reply, until it was killed and resumed.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var failedTurn = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionError}

func TestDirectInputAfterAFailedTurnIsAccepted(t *testing.T) {
	dir := t.TempDir()
	const local = "s1"
	d := newDirectInputStateTestDaemon(t, dir, local)
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputDraft, failedTurn); err != nil {
		t.Fatalf("typing after a failed turn was refused: %v", err)
	}
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputSubmitted, failedTurn); err != nil {
		t.Fatalf("submitting after a failed turn was refused: %v", err)
	}
	if !d.directInputUnresolved(local) {
		t.Fatal("the submitted input after a failed turn is not reported unresolved")
	}
}

// The error baseline survives a daemon restart as itself, not as the fail-closed generic draft
// a malformed record becomes, so the submit still clears on the provider's next progress.
func TestDirectInputErrorBaselineRestoresAndClearsOnProgress(t *testing.T) {
	dir := t.TempDir()
	const local = "s1"
	d := newDirectInputStateTestDaemon(t, dir, local)
	if err := d.markDirectInputUnresolvedAt(local, protocol.DirectInputSubmitted, failedTurn); err != nil {
		t.Fatalf("mark direct input: %v", err)
	}
	d = newDirectInputStateTestDaemon(t, dir, local)
	want := directInputRecord{
		Version: directInputStateVersion, Class: protocol.DirectInputSubmitted,
		BaselineTurn: status.TurnIdle, BaselineInteraction: status.InteractionError,
	}
	if got := d.directInput.unresolved[local]; got != want {
		t.Fatalf("restored record = %+v, want %+v", got, want)
	}

	d.noteDirectInputStatus(local, failedTurn)
	if !d.directInputUnresolved(local) {
		t.Fatal("the submit cleared itself without later provider progress")
	}
	d.noteDirectInputStatus(local, status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})
	if d.directInputUnresolved(local) {
		t.Fatal("the provider started the submitted turn and the marker did not clear")
	}
}
