package engine

// agents-tracker-707 FIX E: a Stop is a TURN BOUNDARY, not just another
// sequence-numbered write.
//
// applyTyped ordered typed signals by sequence alone, and sequences do not carry
// causal order: they are handed out by racing `swarm hook` processes contending
// on a flock (internal/hookclient.nextSequence). A SubagentStop, or the
// PostToolUse of a tool that finished around the same instant, can therefore take
// a HIGHER sequence than the turn's Stop and flip a settled idle back to active —
// permanently, because no further hook is coming to correct it and the grid tap
// only governs once the typed signal goes stale.
//
// The rule under test: within postStopGrace of an idle-setting Stop, a
// TRAILING-EDGE hook (SubagentStop, PostToolUse — events that report work
// ENDING) may not reopen the turn. A LEADING-EDGE hook (UserPromptSubmit,
// PreToolUse — events that report work STARTING) always wins: that is a real new
// turn. Past the window, a trailing-edge hook is honored as before: work that
// really is still running (a background task) belongs in the working group.
//
// Time is injected (Config.Now via fakeClock), so nothing here depends on wall
// clock. The sources are the REAL claude adapter's, so these tests fail if the
// event names the engine's boundary rule keys on drift from the adapter's table.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// postStopFixture registers a session on the real claude signal sources and
// drives it to the settled state the bug starts from: a turn ran and Stop closed
// it. It returns the engine, the clock and the recorder, with the next free
// sequence number.
func postStopFixture(t *testing.T) (*Engine, *fakeClock, *emitRecorder, uint64) {
	t.Helper()
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "UserPromptSubmit"}); err != nil {
		t.Fatalf("UserPromptSubmit: %v", err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("setup: UserPromptSubmit left turn=%s, want active", got.s.Turn)
	}
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "Stop"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnIdle {
		t.Fatalf("setup: Stop left turn=%s, want idle", got.s.Turn)
	}
	return e, clk, rec, 3
}

// A trailing-edge hook that wins the sequence race must not reopen the turn.
func TestBead707_TrailingHookAfterStopKeepsIdle(t *testing.T) {
	for _, event := range []string{"SubagentStop", "PostToolUse"} {
		t.Run(event, func(t *testing.T) {
			e, clk, rec, seq := postStopFixture(t)

			// The straggler: a HIGHER sequence than the Stop, arriving just after it.
			clk.advance(100 * time.Millisecond)
			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: seq, Event: event}); err != nil {
				t.Fatalf("%s: %v", event, err)
			}
			got, _ := rec.last()
			if got.s.Turn != status.TurnIdle {
				t.Fatalf("%s(seq %d) 100ms after Stop flipped turn to %s; want idle held (the Stop is the turn boundary)", event, seq, got.s.Turn)
			}
			if g := status.Derive(got.s); g != status.GroupReadyForReview {
				t.Fatalf("%s(seq %d) after Stop derived %v; want ready_for_review", event, seq, g)
			}
		})
	}
}

// The guard: a leading-edge hook is a genuine new turn and must always win, at
// any distance from the Stop.
func TestBead707_LeadingHookAfterStopStartsANewTurn(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "PreToolUse"} {
		t.Run(event, func(t *testing.T) {
			e, clk, rec, seq := postStopFixture(t)

			clk.advance(100 * time.Millisecond)
			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: seq, Event: event}); err != nil {
				t.Fatalf("%s: %v", event, err)
			}
			got, _ := rec.last()
			if got.s.Turn != status.TurnActive {
				t.Fatalf("%s(seq %d) 100ms after Stop left turn=%s; want active (a new turn always wins)", event, seq, got.s.Turn)
			}
			if g := status.Derive(got.s); g != status.GroupWorking {
				t.Fatalf("%s(seq %d) after Stop derived %v; want working", event, seq, g)
			}
		})
	}
}

// Past the window a trailing-edge hook behaves exactly as before: this is no
// longer a straggler racing the turn boundary but work that genuinely started
// after it — a background task completing — and the session IS busy again.
func TestBead707_TrailingHookPastGraceReactivates(t *testing.T) {
	for _, event := range []string{"SubagentStop", "PostToolUse"} {
		t.Run(event, func(t *testing.T) {
			e, clk, rec, seq := postStopFixture(t)

			clk.advance(10 * time.Second) // far past the boundary grace window
			if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: seq, Event: event}); err != nil {
				t.Fatalf("%s: %v", event, err)
			}
			if got, _ := rec.last(); got.s.Turn != status.TurnActive {
				t.Fatalf("%s(seq %d) 10s after Stop left turn=%s; want active (a genuinely new background task)", event, seq, got.s.Turn)
			}
		})
	}
}

// The window is armed by a Stop, not by idleness in general: a trailing-edge hook
// arriving after a PermissionRequest (also turn=idle) must still read active.
// This is the auto-approved tool path the live corpus shows ("Allowed by
// PermissionRequest hook", docs/verification/fixtures/spike-sc/c1-delay30s):
// PermissionRequest, then PostToolUse milliseconds later, with the turn still
// genuinely running.
func TestBead707_PermissionRequestDoesNotArmTheBoundary(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))

	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "PermissionRequest"}); err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	clk.advance(50 * time.Millisecond)
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 2, Event: "PostToolUse"}); err != nil {
		t.Fatalf("PostToolUse: %v", err)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive {
		t.Fatalf("PostToolUse 50ms after an auto-approved PermissionRequest left turn=%s; want active", got.s.Turn)
	}
}
