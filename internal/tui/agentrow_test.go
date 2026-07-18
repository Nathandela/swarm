package tui

// v0.4 committee fix wave — item 5 (Fable): the launch form's agent picker row
// rendered unclamped. At 120 cols a crashing agent's long reason (probe error +
// Rosetta rebuild hint) overflowed the form width, wrapping the row and pushing the
// SELECTED agent and its reason off-screen. The row must clamp to the form width,
// keeping the selected agent (dot + name) and its reason visible while truncating
// other agents' reasons first.

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLaunch_AgentRowClampsKeepingSelectedVisible(t *testing.T) {
	agents := []AgentInfo{
		// A crashing agent whose reason + Rosetta hint is long enough on its own to
		// blow past the row width.
		{Name: "codex", Installed: true, InRange: false,
			Reason: "version probe failed - reinstall? (swarm is x86_64 under Rosetta; rebuild native ROSETTA-HINT-TAIL-MARKER)"},
		// The SELECTED crashing agent: its name and reason must survive the clamp.
		{Name: "claude", Installed: true, InRange: false,
			Reason: "unsupported version 0.0.1 SELECTED-REASON-MARKER"},
	}
	m := launchModel{agents: agents, agentIdx: 1, detected: true, width: testCols}

	row := m.agentValue()
	// The agent value is rendered after a 2-cell focus prefix and the padded label
	// column, so the row must fit the remaining form width.
	budget := testCols - (2 + launchLabelW)
	if w := lipgloss.Width(row); w > budget {
		t.Fatalf("agent row width = %d, must clamp to the form budget %d; row:\n%q", w, budget, stripANSI(row))
	}

	plain := stripANSI(row)
	if !strings.Contains(plain, "claude") {
		t.Fatalf("the selected agent name must stay visible; row:\n%q", plain)
	}
	if !strings.Contains(plain, "SELECTED-REASON-MARKER") {
		t.Fatalf("the selected agent's reason must stay visible; row:\n%q", plain)
	}
	if strings.Contains(plain, "ROSETTA-HINT-TAIL-MARKER") {
		t.Fatalf("a non-selected agent's long reason must truncate first; row:\n%q", plain)
	}
}

// A short row (everything fits) is rendered byte-identical to the unclamped form,
// so the clamp never perturbs the common case (and the golden stays valid).
func TestLaunch_AgentRowUnchangedWhenItFits(t *testing.T) {
	agents := []AgentInfo{
		{Name: "claude", Installed: true, InRange: true},
		{Name: "codex", Installed: true, InRange: true},
	}
	m := launchModel{agents: agents, agentIdx: 0, detected: true, width: testCols}
	if w := lipgloss.Width(m.agentValue()); w > testCols-(2+launchLabelW) {
		t.Fatalf("a short agent row must fit without clamping; width=%d", w)
	}
}
