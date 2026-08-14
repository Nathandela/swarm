package tui

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// F2 — the elapsed-time repaint timer runs ONLY on the general view (N-3: no idle
// repaints on the launch/attach screens) and restarts when the general view is
// re-entered. The cadence is one second, not the former 200 ms.

func TestRepaint_TimerInactiveOffGeneralAndRestartsOnReturn(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	// General view: a repaint tick re-arms the timer.
	m2, cmd := m.Update(repaintMsg{})
	if cmd == nil {
		t.Fatal("repaint on the general view must re-arm the timer")
	}
	m = m2

	// Launch form: a repaint must NOT re-arm — the timer lapses off the general view.
	m = send(m, keyRune('n'))
	m2, cmd = m.Update(repaintMsg{})
	if cmd != nil {
		t.Fatal("repaint off the general view (launch form) must not re-arm the timer")
	}
	m = m2 // the timer has now lapsed (ticking = false)

	// Esc back to the general view restarts the lapsed timer.
	_, cmd = m.Update(keyEsc)
	if cmd == nil {
		t.Fatal("returning to the general view must restart the lapsed repaint timer")
	}
}

// F2 — the repaint cadence is one second (slower than the former 200 ms full
// repaint, per N-3). Guards against a regression back to the busy interval.
func TestRepaint_IntervalIsOneSecond(t *testing.T) {
	if repaintInterval != time.Second {
		t.Fatalf("repaintInterval = %s, want 1s (N-3: slower than the former 200ms)", repaintInterval)
	}
}

func TestWorkingAnimationInactiveOffGeneralAndRestartsOnReturn(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())
	if !m.(rootModel).animatingWorking {
		t.Fatal("a general board with a Working row must start with one animation tick in flight")
	}

	m = send(m, keyRune('n'))
	m2, cmd := m.Update(workingAnimationMsg{})
	if cmd != nil {
		t.Fatal("a Working animation tick off the general board must not re-arm")
	}
	m = m2
	if m.(rootModel).animatingWorking {
		t.Fatal("the animation guard must record that the off-board tick lapsed")
	}

	m2, cmd = m.Update(keyEsc)
	if cmd == nil {
		t.Fatal("returning to a board with a Working row must restart the animation")
	}
	if !m2.(rootModel).animatingWorking {
		t.Fatal("returning to the board must mark exactly one animation tick in flight")
	}
}

func TestWorkingAnimationLapsesDuringAttachAndRestartsAfterDetach(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := New(f, detectMixed(), WithAttachRunner(func(protocol.SessionView, bool) error { return nil }))
	m, _ = m.Update(keyEnter)
	if got := m.(rootModel).screen; got != screenAttach {
		t.Fatalf("screen during passthrough = %v, want attach so animation cannot repaint the handed-off terminal", got)
	}

	m2, cmd := m.Update(workingAnimationMsg{})
	if cmd != nil || m2.(rootModel).animatingWorking {
		t.Fatal("the Working animation tick must lapse while attach owns the terminal")
	}

	m2, cmd = m2.Update(attachDoneMsg{})
	if cmd == nil || !m2.(rootModel).animatingWorking {
		t.Fatal("returning from attach to a board with a Working row must restart one animation tick")
	}
}

func TestWorkingAnimationStartsAndStopsWithWorkingRows(t *testing.T) {
	s := sReview("endpoint/s1", "codex", "~/Code/x", "ready", time.Minute)
	m := newModel(t, newFakeClient(s), detectMixed())
	if m.(rootModel).animatingWorking {
		t.Fatal("a board without Working rows must not run the animation timer")
	}

	working := sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute)
	m2, cmd := m.Update(eventMsg{ev: protocol.Event{Session: working}})
	if cmd == nil || !m2.(rootModel).animatingWorking {
		t.Fatal("an event introducing a Working row must start the animation timer")
	}
	m = m2

	m = send(m, eventMsg{ev: protocol.Event{Session: s}})
	m2, cmd = m.Update(workingAnimationMsg{})
	if cmd != nil {
		t.Fatal("the in-flight animation tick must lapse after the final Working row leaves")
	}
	if m2.(rootModel).animatingWorking {
		t.Fatal("the animation guard must clear after the final Working row leaves")
	}
}

func TestWorkingAnimationStopsAfterConnectionLoss(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute)), detectMixed())
	rm := m.(rootModel)
	m = send(m, connectionLostMsg{from: rm.events})

	before := m.(rootModel).general.spinnerFrame
	m2, cmd := m.Update(workingAnimationMsg{})
	if cmd != nil {
		t.Fatal("connection loss must prevent the Working animation from re-arming")
	}
	after := m2.(rootModel)
	if after.animatingWorking || after.general.spinnerFrame != before {
		t.Fatalf("connection-lost animation state = active:%v frame:%d, want inactive frame:%d", after.animatingWorking, after.general.spinnerFrame, before)
	}
}

func TestWorkingAnimationArmsAtMostOneTick(t *testing.T) {
	rm := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute)), detectMixed()).(rootModel)
	rm.animatingWorking = false
	if cmd := rm.armWorkingAnimation(); cmd == nil {
		t.Fatal("the first arm must schedule an animation tick")
	}
	if cmd := rm.armWorkingAnimation(); cmd != nil {
		t.Fatal("a second arm while a tick is in flight must be a no-op")
	}
}
