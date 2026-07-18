package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// Failing-first suite for the v0.2 committee audit fixes in the TUI:
//   - item 6: detection generation stamp (drop a stale probe that lands after a
//     newer one — the Init-vs-form-open ordering race);
//   - item 7: focus clamp in refreshAgents (a re-detection must not re-index the
//     field under the user's cursor);
//   - item 9: composeBoard truncates the status bar to the terminal width so it can
//     never wrap and break the fixed-height board contract.

// item 6 — an older detection probe that resolves AFTER a newer one must be dropped,
// not applied, so it cannot restore stale agent availability.
func TestDetect_StaleGenerationDropped(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	// The first dispatch (Init-equivalent) is the current generation; opening the form
	// stamps the next generation for the form-open refresh.
	genOld := m.(rootModel).detectGen
	m = send(m, keyRune('n'))
	genNew := m.(rootModel).detectGen
	if genNew == genOld {
		t.Fatalf("opening the form must dispatch a newer detection generation; got %d then %d", genOld, genNew)
	}

	fresh := []AgentInfo{{Name: "fresh", Installed: true, InRange: true}}
	stale := []AgentInfo{{Name: "stale", Installed: true, InRange: true}}
	// Deliver the newer generation's result, then the older (superseded) one.
	m = send(m, detectMsg{gen: genNew, agents: fresh})
	m = send(m, detectMsg{gen: genOld, agents: stale})

	got := m.(rootModel).agents
	if len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("a stale (older-generation) detectMsg must be dropped; agents=%+v", got)
	}
}

// item 7 — a re-detection that grows the option schema must NOT re-index the field
// under the user's cursor. Cold-open the form, focus the prompt, then let detection
// land with options: focus must stay semantically on prompt so a typed rune lands
// there rather than on a newly-inserted option field.
func TestRefreshAgents_PreservesPromptFocusAcrossReindex(t *testing.T) {
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, keyRune('n')) // open cold: no options yet (fieldCount = 4)
	m = send(m, keyTab)       // directory -> agent
	m = send(m, keyTab)       // agent -> prompt (promptIndex = 2 with no options)
	if !launchOf(m).isPrompt() {
		t.Fatalf("precondition: focus must be on prompt before detection lands; focus=%d", launchOf(m).focus)
	}

	gen := m.(rootModel).detectGen
	m = send(m, detectMsg{gen: gen, agents: detectEditable()()}) // grows optSpecs beneath the focus
	if !launchOf(m).isPrompt() {
		t.Fatalf("focus must remain semantically on prompt after the re-index; focus=%d", launchOf(m).focus)
	}
	m = send(m, keyRune('Z'))
	if got := launchOf(m).prompt; got != "Z" {
		t.Fatalf("a typed rune must land in the prompt after the re-index; prompt=%q", got)
	}
}

// item 7 — when focus was on an option field whose list changes beneath it, focus
// clamps to the directory field (index 0) rather than pointing at a stale index.
func TestRefreshAgents_OptionFocusClampsToDirectory(t *testing.T) {
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, detectMsg{agents: detectEditable()()}) // land options first (gen 0, before any form-open)
	m = send(m, keyRune('n'))
	m = send(m, keyTab) // directory -> agent
	m = send(m, keyTab) // agent -> first option (model)
	if _, ok := launchOf(m).optionFocus(); !ok {
		t.Fatalf("precondition: focus must be on an option field; focus=%d", launchOf(m).focus)
	}

	gen := m.(rootModel).detectGen
	m = send(m, detectMsg{gen: gen, agents: detectEditable()()})
	if !launchOf(m).isDir() {
		t.Fatalf("option focus must clamp to the directory field after a re-index; focus=%d", launchOf(m).focus)
	}
}

// item 9 — the status bar is clamped to the terminal width so a long context line can
// never wrap onto a second row and break the fixed-height board contract.
func TestComposeBoard_StatusBarTruncatedToWidth(t *testing.T) {
	m := rootModel{width: 20, height: 5}
	out := m.composeBoard("body line", strings.Repeat("x", 100))

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("composed board must be exactly height (5) rows, got %d", len(lines))
	}
	last := stripANSI(lines[len(lines)-1])
	if w := lipgloss.Width(last); w > 20 {
		t.Fatalf("status bar must be clamped to the terminal width (20); got width %d: %q", w, last)
	}
}
