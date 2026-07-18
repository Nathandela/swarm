package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// openCodexForm opens the launch form against detectCodexSchema and focuses the
// agent field, ready for left/right cycling between claude (1 option) and the
// unusable codex (2 options).
func openCodexForm(t *testing.T) tea.Model {
	t.Helper()
	m := newModel(t, newFakeClient(), detectCodexSchema())
	m = send(m, detectMsg{agents: detectCodexSchema()()})
	m = send(m, keyRune('n'))
	if v := view(m); !strings.Contains(v, "new session") {
		t.Fatalf("expected the launch form after `n`, got:\n%s", v)
	}
	m = send(m, keyDown) // directory -> agent
	return m
}

// v0.4 / bead 41b — cycling the agent picker FULLY swaps the option schema: after
// landing on codex (whose schema adds a "Sandbox mode" row) and cycling back to
// claude, the codex-only row is gone and only claude's own options remain. Guards
// against stale/merged option rows leaking across agents.
func TestLaunch_CyclingAgentFullySwapsSchema(t *testing.T) {
	m := openCodexForm(t)

	m = send(m, keyRight) // claude -> codex; codex's Sandbox mode row loads
	if got := launchOf(m).currentAgentName(); got != "codex" {
		t.Fatalf("right arrow must land on codex, got %q", got)
	}
	if v := view(m); !strings.Contains(v, "Sandbox mode") {
		t.Fatalf("codex's schema must render its Sandbox mode row:\n%s", v)
	}

	m = send(m, keyLeft) // codex -> claude; the schema must fully swap back
	if got := launchOf(m).currentAgentName(); got != "claude" {
		t.Fatalf("left arrow must land back on claude, got %q", got)
	}
	v := view(m)
	if strings.Contains(v, "Sandbox mode") {
		t.Fatalf("codex's Sandbox mode row must be GONE once claude is selected again:\n%s", v)
	}
	if !strings.Contains(v, "Model") {
		t.Fatalf("claude's own Model row must be present:\n%s", v)
	}
	if got := len(launchOf(m).optSpecs); got != 1 {
		t.Fatalf("claude carries exactly one option, got %d optSpecs (stale schema leak)", got)
	}
}

// v0.4 / bead 41b — a label as wide as the label column still keeps a separating
// space before its value. Codex's "Sandbox mode" is exactly launchLabelW (12)
// chars, which padRight leaves flush; the field-test glitch rendered it jammed as
// "Sandbox modeworkspace-write".
func TestLaunch_OptionLabelKeepsSeparatorSpace(t *testing.T) {
	m := openCodexForm(t)
	m = send(m, keyRight) // select codex so its Sandbox mode row renders

	v := view(m)
	if strings.Contains(v, "Sandbox modeworkspace-write") {
		t.Fatalf("12-char label must not jam against its value:\n%s", v)
	}
	if !strings.Contains(v, "Sandbox mode ") { // label followed by at least one space
		t.Fatalf("the 'Sandbox mode' label must keep a separating space before its value:\n%s", v)
	}
}

// v0.4 / bead 41b — the SELECTED agent always renders the filled selection dot,
// even when that agent is unusable. The field-test glitch showed the selected
// (unusable) codex with an empty circle, indistinguishable from an unselected
// usable agent, so the cursor position was invisible.
func TestLaunch_SelectedAgentRendersFilledDot(t *testing.T) {
	m := openCodexForm(t)
	m = send(m, keyRight) // claude -> codex (unusable, now selected)

	if got := launchOf(m).currentAgentName(); got != "codex" {
		t.Fatalf("codex must be selected, got %q", got)
	}
	v := view(m)
	if !strings.Contains(v, "● codex") {
		t.Fatalf("the selected agent must render the filled dot, even when unusable:\n%s", v)
	}
	if strings.Contains(v, "○ codex") {
		t.Fatalf("the selected codex must not render the empty (unselected) circle:\n%s", v)
	}
}

// v0.4 / bead 3sr (keymap-affordance half) — bool rows render their toggle
// affordance inline ("space" next to the checkbox), so the affordance is
// discoverable on the row itself and not only in the focused-field hint bar.
func TestLaunch_BoolRowsShowSpaceAffordanceInline(t *testing.T) {
	// The worktree row is a bool present in every form.
	m := openLaunch(t, newFakeClient())
	if v := view(m); !strings.Contains(v, "[ ] space") {
		t.Fatalf("the worktree bool row must show its 'space' affordance inline:\n%s", v)
	}

	// A declarative bool option (claude's Skip permission prompts) likewise.
	m2 := newModel(t, newFakeClient(), detectEditable())
	m2 = send(m2, detectMsg{agents: detectEditable()()})
	m2 = send(m2, keyRune('n'))
	if v := view(m2); !strings.Contains(v, "[ ] space") {
		t.Fatalf("a bool option row must show its 'space' affordance inline:\n%s", v)
	}
}
