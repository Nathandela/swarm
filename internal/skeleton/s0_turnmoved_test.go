package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the THIRD test the audit committee owes Slice 0:
// docs/specifications/chat-surface-plan.md §12, "Turn closure or start between expected_turn
// validation and delivery, for both sinks". Bead: agents-tracker-bzfe. Evidence:
// docs/verification/chat-surface.md, "Owed before this is called complete".
//
// THE GAP, AND IT IS A LIVE ONE. composerSend verifies expected_turn against the daemon's own
// turn state under itemMu, RELEASES the lock, and only then delivers (chat.go: the unlock at
// the end of the precondition block, the deliverComposerText call on the line after it). The
// verified fact is therefore stale by the time it is acted on.
//
// HOW BIG THE GAP IS, SAID HONESTLY, because this header used to overstate it and so did the
// comment on deliverComposerText. A turn only moves under itemMu, so the unguarded interval is
// exactly the one composerSend spends outside that lock: an unlock, a hook load, a nil test, a
// call. Both files used to describe the gap as holding "a tap subscribe and a shimwire round
// trip" on one arm and "a whole JSON-RPC round trip" on the other. Those are the DELIVERY.
// They run AFTER the re-check, they are the window that stays open, and naming them as the
// window that closed made a few instructions read as three orders of magnitude more.
//
// A SHORT WINDOW IS NOT A CLOSED ONE, which is why the tests below still exist: a goroutine
// can be descheduled anywhere inside it, and a send that resumes after the turn it was written
// against has closed is misapplied whatever the odds were. The tests DRIVE that interleaving
// through the seam rather than wait for it.
//
// WHAT MOVES IN THAT GAP IS NOT EXOTIC. The turn opens on a user_message and closes on a
// terminal agent_message (IS-ENV-1) -- so the owner asking a question at the terminal, or the
// agent simply finishing, moves it. Both are asynchronous to this path and neither is rare on
// a surface whose whole purpose is a conversation two people are having at once.
//
// WHAT SHOULD HAPPEN, AND WHY IT IS THE SAME ANSWER THE PRECONDITION ALREADY GIVES.
// IS-LIFE-5: "a tap that lands after the turn moved on is REFUSED, NEVER MISAPPLIED." The
// harm is semantic, not mechanical -- a phone message is written against a conversation the
// reader had in front of them, and "yes" delivered into the turn that REPLACED the one it
// answered is a message nobody meant. The refusal is protocol.CodeStaleTurn, which the phone
// already draws as bubble.stale: "Not sent -- the conversation moved on. Read the latest turn
// and send again." It is a precondition, not a lockout: the same words sent against the
// current turn go in.
//
// THE SEAM. testHookComposerCheckedNotYetDelivered parks the test INSIDE the gap. Racing it
// with sleeps would be dishonest in both directions -- a sleep long enough to be reliable
// lands after the write has begun, and a sleep short enough to land before it is a coin toss.
// The hook runs on the CALLER's goroutine, so every turn test below is ordinary sequential
// test code with no goroutine of its own. The one exception is the seam's own race test, which
// is about the variable rather than the turn and needs two goroutines to say anything.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// armSeam and disarmSeam are the only two writes any test makes to the package's delivery-gap
// hook. They exist as a pair so the synchronisation lives in ONE place: every test below
// installs the seam and every test clears it, and a hook a daemon goroutine reads while a test
// writes it is a data race whatever the test meant by it.
func armSeam(fn func(string)) { testHookComposerCheckedNotYetDelivered.Store(&fn) }
func disarmSeam()             { testHookComposerCheckedNotYetDelivered.Store(nil) }

// inTheDeliveryGap installs a one-shot seam that runs `move` after composerSend has verified
// expected_turn and before it has delivered anything, then removes it.
func inTheDeliveryGap(t *testing.T, move func()) {
	t.Helper()
	fired := false
	armSeam(func(string) {
		if fired {
			return
		}
		fired = true
		move()
	})
	t.Cleanup(func() {
		disarmSeam()
		if !fired {
			t.Error("the delivery gap was never entered, so this test proved nothing: " +
				"composerSend returned before it reached the seam")
		}
	})
}

// TestSlice0_TheSeamIsSafeToClearWhileASendIsInFlight is about the seam itself and not about
// the turn.
//
// THE HOOK IS PACKAGE STATE A TEST WRITES AND A DAEMON GOROUTINE READS, and until this test
// there was nothing between the two. Every delivery-gap test above clears it from t.Cleanup,
// cleanups run LIFO, and the rig each test builds registers its teardown FIRST -- so the hook
// is nilled while the daemon is still running, and any send still inside composerSend at that
// instant reads the variable the cleanup is writing. Low probability and entirely real: -race
// reports it, and a gate that reports a race on its own scaffolding teaches the next reader to
// ignore -race output.
//
// This drives the same collision on purpose rather than waiting for teardown to find it: real
// sends on one goroutine, the arm/clear a cleanup performs on the other, no synchronisation
// between them beyond whatever the hook itself provides. Under -race it either reports or it
// does not; nothing here asserts on a turn.
func TestSlice0_TheSeamIsSafeToClearWhileASendIsInFlight(t *testing.T) {
	r := newR7ComposerRig(t, true)
	daemonTurn := r7OpenNativeTurn(t, r)
	t.Cleanup(disarmSeam)

	sends := make(chan struct{})
	go func() {
		defer close(sends)
		for i := 0; i < 64; i++ {
			// The outcome is deliberately unread: this test is about the seam variable,
			// not the turn, and a send refused for any reason has still read the hook.
			_, _ = r.sendBare(daemonTurn, "a message", fmt.Sprintf("devA:01JS0SEAMRACE%010d", i))
		}
	}()
	for {
		select {
		case <-sends:
			return
		default:
		}
		armSeam(func(string) {})
		disarmSeam()
	}
}

// assertNoBackendCall proves the RPC never went out.
func assertNoBackendCall(t *testing.T, b *r7FakeBackend, method string) {
	t.Helper()
	for _, c := range b.recorded() {
		if c.Method == method {
			t.Fatalf("%s was dispatched to the app-server after the turn had already moved; the "+
				"backend saw %v.\nA refusal that has already sent the message refuses nothing: the "+
				"phone is told the conversation moved on while the agent reads the words anyway.",
				method, methodsOf(b))
		}
	}
}

// r6CloseTurn ends the open turn the way IS-ENV-1 says it ends: a terminal agent_message.
func r6CloseTurn(t *testing.T, sk *Daemon, local string) {
	t.Helper()
	sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted, Text: "the answer",
	}), adapter.HookPayload{Event: "Stop"})
	awaitTrue(t, 10*time.Second, "the terminal agent_message never closed the daemon's turn", func() bool {
		sk.itemMu.Lock()
		defer sk.itemMu.Unlock()
		return sk.turnIDs[local] == ""
	})
}

// ---------------------------------------------------------------------------
// The keystroke sink (Claude): the message is typed into the CLI's own composer
// ---------------------------------------------------------------------------

// TestSlice0_KeystrokeSink_ATurnOpeningInTheDeliveryGapRefusesStaleAndTypesNothing is the
// TURN START half. The phone read an idle session and sent against no turn; the owner asked
// their own question at the terminal while the send was on its way to the PTY.
func TestSlice0_KeystrokeSink_ATurnOpeningInTheDeliveryGapRefusesStaleAndTypesNothing(t *testing.T) {
	r := newKeystrokeRig(t)

	inTheDeliveryGap(t, func() {
		already := len(interactionItems(t, r.sk, r.local))
		r6OpenTurn(t, r.sk, r.local, "the owner's own question", already)
	})

	code, err := r.send(t, "", "reply to whatever is open", "devA:01JS0GAPKEYSTART000000")
	if code != protocol.CodeStaleTurn {
		t.Fatalf("a send whose turn OPENED between the check and the write = code %q err %v, want %q.\n"+
			"The phone wrote its message against an idle conversation and the conversation stopped "+
			"being idle before the bytes left. IS-LIFE-5 refuses; it never misapplies. The phone "+
			"draws that refusal as bubble.stale and offers the words back for a resend.",
			code, err, protocol.CodeStaleTurn)
	}
	r.assertPTYUntouched(t)
}

// TestSlice0_KeystrokeSink_ATurnClosingInTheDeliveryGapRefusesStaleAndTypesNothing is the
// TURN CLOSURE half: the agent finished answering while the phone's reply to that same
// answer was in flight.
func TestSlice0_KeystrokeSink_ATurnClosingInTheDeliveryGapRefusesStaleAndTypesNothing(t *testing.T) {
	r := newKeystrokeRig(t)
	already := len(interactionItems(t, r.sk, r.local))
	turnA := r6OpenTurn(t, r.sk, r.local, "the only question", already)

	inTheDeliveryGap(t, func() { r6CloseTurn(t, r.sk, r.local) })

	code, err := r.send(t, turnA, "and one more thing", "devA:01JS0GAPKEYSCLOSE00000")
	if code != protocol.CodeStaleTurn {
		t.Fatalf("a send whose turn CLOSED between the check and the write = code %q err %v, want %q.\n"+
			"The turn the reader was answering ended before the answer arrived. Typing it anyway "+
			"starts a NEW turn with a message written as a continuation of the old one.",
			code, err, protocol.CodeStaleTurn)
	}
	r.assertPTYUntouched(t)
}

// TestSlice0_KeystrokeSink_AStillCurrentTurnIsDeliveredThroughTheSameGap is the control that
// keeps the two refusals above from being satisfied by a composer that simply stopped working.
// The seam fires and does NOT move the turn; the message goes in, whole.
func TestSlice0_KeystrokeSink_AStillCurrentTurnIsDeliveredThroughTheSameGap(t *testing.T) {
	r := newKeystrokeRig(t)
	already := len(interactionItems(t, r.sk, r.local))
	turnA := r6OpenTurn(t, r.sk, r.local, "the only question", already)

	inTheDeliveryGap(t, func() {}) // the gap is entered; nothing moves in it

	const text = "the reader's answer"
	code, err := r.send(t, turnA, text, "devA:01JS0GAPKEYSOK00000000")
	if err != nil || code != "" {
		t.Fatalf("a send against the STILL-CURRENT turn was refused: code %q err %v. The re-check "+
			"is a precondition on a turn that moved, not a second chance to refuse a good send",
			code, err)
	}
	lines := awaitSubmittedLines(r.att, 1, 20*time.Second)
	if len(lines) != 1 || lines[0] != text {
		t.Fatalf("the session's stdin saw %q, want [%q]", lines, text)
	}
}

// ---------------------------------------------------------------------------
// The backend sink (Codex): the message is an app-server RPC
// ---------------------------------------------------------------------------

// TestSlice0_BackendSink_ATurnOpeningInTheDeliveryGapRefusesStaleAndSendsNothing is the turn
// START half on the arm that never touches a PTY -- and the harm here is one chat.go names in
// its own words: an idle-time check dispatches turn/start, and "dispatching turn/start
// mid-turn instead would QUEUE A SECOND TURN, so the owner's question and the phone's would
// arrive as two separate conversations".
func TestSlice0_BackendSink_ATurnOpeningInTheDeliveryGapRefusesStaleAndSendsNothing(t *testing.T) {
	r := newR7ComposerRig(t, true)

	inTheDeliveryGap(t, func() { r7OpenNativeTurn(t, r) })

	code, err := r.send(t, "", "reply to whatever is open", "devA:01JS0GAPBACKSTART00000")
	if code != protocol.CodeStaleTurn {
		t.Fatalf("a send whose turn OPENED between the check and the RPC = code %q err %v, want %q.\n"+
			"The dispatch was chosen from a fact that had already expired: turn/start against a "+
			"thread whose turn is now running queues a SECOND turn, and the owner's question and "+
			"the phone's become two conversations.", code, err, protocol.CodeStaleTurn)
	}
	assertNoBackendCall(t, r.backend, "turn/start")
	r.assertPTYUntouched(t)
}

// TestSlice0_BackendSink_ATurnClosingInTheDeliveryGapRefusesStaleAndSendsNothing is the turn
// CLOSURE half. A steer names the CLI's own expectedTurnId, so this one is rejected by the
// app-server's own precondition when it arrives -- but "rejected by the far end" reaches the
// phone as an unclassified failure, not as the refusal that tells the reader what to do next.
func TestSlice0_BackendSink_ATurnClosingInTheDeliveryGapRefusesStaleAndSendsNothing(t *testing.T) {
	r := newR7ComposerRig(t, true)
	daemonTurn := r7OpenNativeTurn(t, r)

	inTheDeliveryGap(t, func() {
		r.sk.ingestBackendFrame(r.local, []byte(r7RecordedTurnCompletedFrame), time.Now().UnixMilli())
		r.sk.flushBackendFrames(r.local)
		awaitTrue(t, 10*time.Second, "the recorded turn/completed never closed the daemon's turn", func() bool {
			r.sk.itemMu.Lock()
			defer r.sk.itemMu.Unlock()
			return r.sk.turnIDs[r.local] == ""
		})
	})

	code, err := r.send(t, daemonTurn, "and one more thing", "devA:01JS0GAPBACKCLOSE00000")
	if code != protocol.CodeStaleTurn {
		t.Fatalf("a send whose turn CLOSED between the check and the RPC = code %q err %v, want %q.\n"+
			"An error the far end returns is not a remedy the reader can act on; stale_turn is, and "+
			"the daemon already knew the turn was over before it sent anything.",
			code, err, protocol.CodeStaleTurn)
	}
	assertNoBackendCall(t, r.backend, "turn/steer")
	r.assertPTYUntouched(t)
}

// TestSlice0_BackendSink_AStillCurrentTurnIsSteeredThroughTheSameGap is the backend arm's
// control: the seam fires, nothing moves, and the steer goes out naming the CLI's own turn.
func TestSlice0_BackendSink_AStillCurrentTurnIsSteeredThroughTheSameGap(t *testing.T) {
	r := newR7ComposerRig(t, true)
	daemonTurn := r7OpenNativeTurn(t, r)

	inTheDeliveryGap(t, func() {})

	code, err := r.send(t, daemonTurn, "actually, stop", "devA:01JS0GAPBACKOK00000000")
	if err != nil || code != "" {
		t.Fatalf("a mid-turn send against the STILL-CURRENT turn was refused: code %q err %v", code, err)
	}
	params := r7CallParams(t, r.backend, "turn/steer")
	if got, _ := params["expectedTurnId"].(string); got != r7NativeTurnID {
		t.Errorf("turn/steer expectedTurnId = %q, want the CLI's OWN turn id %q", got, r7NativeTurnID)
	}
	if body := fmt.Sprintf("%v", params["input"]); !strings.Contains(body, "actually, stop") {
		t.Errorf("the steer's input was %s, which does not hold the message that was sent", body)
	}
}
