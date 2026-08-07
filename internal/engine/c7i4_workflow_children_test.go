package engine

// agents-tracker-c7i4 (spike-SE F1/F2/F3): a claude orchestrating BACKGROUND
// children fires Stop when its MAIN turn ends, while the workflow keeps running.
// Stop maps to turn=idle, so the session flipped to ready_for_review — a "done"
// banner and a phone push, 76-91 times a day per session.
//
// SubagentStart and SubagentStop tightly bracket every child (F1), so the engine
// can count outstanding children and hold the turn active across a Stop that only
// ended the main loop. Three properties this replay pins, all against the captured
// hook ORDER of docs/verification/fixtures/spike-se/workflow-background.json:
//
//   - F2: a Stop with a child outstanding holds WORKING.
//   - the floor: the capture carries THREE SubagentStops for TWO SubagentStarts
//     (a resume re-fires SubagentStart, and stops can outnumber starts), so an
//     unfloored counter goes negative and the SECOND Stop — which does have a
//     child outstanding — would read done again. That second Stop is the floor
//     proof.
//   - F3: the auto-continuation enters as UserPromptSubmit MID-workflow, so it
//     must never reset the child accounting.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
)

// replayCapturedHooks posts a capture's hook payloads to a fresh session in the
// recorded order, advancing the injected clock by each recorded inter-hook gap,
// and returns the engine, its emit recorder and the status standing after every
// hook (the last emitted status, i.e. what a subscriber would be showing then).
func replayCapturedHooks(t *testing.T, hooks []adapter.HookPayload) (*Engine, *fakeClock, *emitRecorder, []status.Status) {
	t.Helper()
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

	standing := make([]status.Status, len(hooks))
	for i, h := range hooks {
		if i > 0 {
			clk.advance(time.Duration(h.ReceivedAtMs-hooks[i-1].ReceivedAtMs) * time.Millisecond)
		}
		cb := Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: h.Event, Payload: hookFields(t, h.Raw)}
		if err := e.HandleCallback(cb); err != nil {
			t.Fatalf("hook %d (%s): %v", i, h.Event, err)
		}
		if got, ok := rec.last(); ok {
			standing[i] = got.s
		}
	}
	return e, clk, rec, standing
}

// ascending reports whether the given hook positions are strictly increasing.
func ascending(idx ...int) bool {
	for i := 1; i < len(idx); i++ {
		if idx[i-1] >= idx[i] {
			return false
		}
	}
	return true
}

// eventIndexes returns the positions of every hook with the given event name.
func eventIndexes(hooks []adapter.HookPayload, event string) []int {
	var out []int
	for i, h := range hooks {
		if h.Event == event {
			out = append(out, i)
		}
	}
	return out
}

func TestC7I4_CapturedWorkflowHoldsWorkingAcrossStop(t *testing.T) {
	fx, err := fixtureio.LoadFixture(workflowBackgroundFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	hooks := fx.HookPayloads

	// The captured order is itself the evidence, so assert it before replaying it.
	starts := eventIndexes(hooks, "SubagentStart")
	stops := eventIndexes(hooks, "SubagentStop")
	mainStops := eventIndexes(hooks, "Stop")
	if len(starts) != 2 || len(stops) != 3 || len(mainStops) != 2 {
		t.Fatalf("capture has %d SubagentStart / %d SubagentStop / %d Stop; expected 2/3/2 (spike-SE F1)",
			len(starts), len(stops), len(mainStops))
	}
	if !ascending(starts[0], mainStops[0], stops[0]) {
		t.Fatalf("captured order is not SubagentStart(%d) -> Stop(%d) -> SubagentStop(%d); the false-done window is not in this fixture",
			starts[0], mainStops[0], stops[0])
	}
	if !ascending(stops[1], starts[1], mainStops[1], stops[2]) {
		t.Fatalf("captured order around the second turn is not SubagentStop(%d) -> SubagentStart(%d) -> Stop(%d) -> SubagentStop(%d)",
			stops[1], starts[1], mainStops[1], stops[2])
	}

	e, clk, rec, standing := replayCapturedHooks(t, hooks)

	// F2: both main-turn Stops land with a child outstanding, so both must hold
	// WORKING. The second one is also the floor proof: three stops for two starts
	// drive an unfloored counter to -1, and the SubagentStart before this Stop
	// would only bring it back to 0.
	for n, i := range mainStops {
		if g := status.Derive(standing[i]); g != status.GroupWorking {
			t.Errorf("Stop #%d (hook %d) with a background child outstanding derived %v (%+v); want working",
				n+1, i, g, standing[i])
		}
	}

	// F3: the auto-continuation wake arrives as UserPromptSubmit; the workflow is
	// running again, so the session is working.
	last := eventIndexes(hooks, "UserPromptSubmit")
	lastPrompt := last[len(last)-1]
	if standing[lastPrompt].Turn != status.TurnActive {
		t.Errorf("the auto-continuation UserPromptSubmit (hook %d) left turn=%s; want active",
			lastPrompt, standing[lastPrompt].Turn)
	}

	// The capture's dedicated permission event must still read needs_input: an
	// outstanding child never masks a wait on the human.
	perm := eventIndexes(hooks, "PermissionRequest")
	if g := status.Derive(standing[perm[0]]); g != status.GroupNeedsInput {
		t.Errorf("PermissionRequest (hook %d) derived %v; want needs_input", perm[0], g)
	}

	// Drainage: every child the capture started has stopped, so a Stop posted after
	// the capture ends real work and must read done — the mask is not a one-way
	// latch.
	clk.advance(time.Second)
	if err := e.HandleCallback(Callback{
		SessionID: "s1", Token: "tok1", Sequence: uint64(len(hooks) + 1), Event: "Stop",
	}); err != nil {
		t.Fatalf("trailing Stop: %v", err)
	}
	got, _ := rec.last()
	if g := status.Derive(got.s); g != status.GroupReadyForReview {
		t.Fatalf("a Stop after every child stopped derived %v (%+v); want ready_for_review (the counter drained)", g, got.s)
	}
}

// The born-green guard: the spike-SD stream (UserPromptSubmit -> Stop, no
// subagent anywhere) still ends ready_for_review. The children mask must not
// leak into an ordinary turn.
func TestC7I4_PlainTurnWithoutChildrenStillEndsReady(t *testing.T) {
	fx, err := fixtureio.LoadFixture(claudeBusyStreamFixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	hooks := fx.HookPayloads
	if len(eventIndexes(hooks, "SubagentStart")) != 0 || len(eventIndexes(hooks, "SubagentStop")) != 0 {
		t.Fatalf("the spike-SD capture carries subagent hooks; it is meant to be the no-children control")
	}
	_, _, _, standing := replayCapturedHooks(t, hooks)
	stops := eventIndexes(hooks, "Stop")
	if g := status.Derive(standing[stops[0]]); g != status.GroupReadyForReview {
		t.Fatalf("Stop with no children derived %v (%+v); want ready_for_review", g, standing[stops[0]])
	}
}

// F3, isolated: the auto-continuation fires UserPromptSubmit while the workflow
// runs, so it must not clear the outstanding-children accounting — the Stop that
// follows it still has a child and still holds working.
func TestC7I4_UserPromptSubmitDoesNotResetChildAccounting(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

	for seq, event := range []string{"SubagentStart", "UserPromptSubmit", "Stop"} {
		clk.advance(time.Second)
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(seq + 1), Event: event}); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	got, _ := rec.last()
	if g := status.Derive(got.s); g != status.GroupWorking {
		t.Fatalf("Stop after SubagentStart + the auto-continuation UserPromptSubmit derived %v (%+v); want working", g, got.s)
	}
}

// A wait on the human outranks outstanding children: PermissionRequest's idle is
// never masked, so needs_input still shows while the workflow runs.
func TestC7I4_PermissionRequestIsNeverMaskedByChildren(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

	for seq, event := range []string{"SubagentStart", "PermissionRequest"} {
		clk.advance(time.Second)
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(seq + 1), Event: event}); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	got, _ := rec.last()
	if g := status.Derive(got.s); g != status.GroupNeedsInput {
		t.Fatalf("PermissionRequest with a child outstanding derived %v (%+v); want needs_input", g, got.s)
	}
}

// The adapter's declared mapping, at the deriveDims seam: SubagentStart is a real
// activity signal, SubagentStop names NO turn at all (it reports that a child
// ENDED, which is never evidence the session is working).
func TestC7I4_SubagentBracketDims(t *testing.T) {
	src := claudeSignalSources(t)
	start := deriveDims(src, "SubagentStart", nil)
	if start[PayloadKeyTurn] != string(status.TurnActive) || start[PayloadKeyInteraction] != string(status.InteractionNone) {
		t.Errorf("deriveDims(SubagentStart) = %v; want turn=active, interaction=none", start)
	}
	stop := deriveDims(src, "SubagentStop", nil)
	if _, ok := stop[PayloadKeyTurn]; ok {
		t.Errorf("deriveDims(SubagentStop) = %v; want NO turn dimension", stop)
	}
	if stop[PayloadKeyInteraction] != string(status.InteractionNone) {
		t.Errorf("deriveDims(SubagentStop) = %v; want interaction=none", stop)
	}
}
