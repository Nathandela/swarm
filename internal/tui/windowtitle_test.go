package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// agents-tracker-gcf6 — the TUI names the terminal tab: every rendered View
// carries WindowTitle "swarm", so the renderer titles the tab on first paint and
// re-asserts it when the terminal is restored after an attach.
func TestWindowTitleIsSwarm(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := New(f, detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	if got := m.View().WindowTitle; got != "swarm" {
		t.Fatalf("WindowTitle = %q, want %q", got, "swarm")
	}
}
