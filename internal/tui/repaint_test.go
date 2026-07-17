package tui

import (
	"testing"
	"time"
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
