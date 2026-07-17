package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"
)

// openLaunch drives the router from the general view into the launch form (`n`).
// Per the launch-form pins: the cwd field starts EMPTY and focused; Tab moves
// through fields in L-1 order (directory, agent, options..., prompt, worktree);
// Enter submits the form from any field.
func openLaunch(t *testing.T, f *fakeClient) tea.Model {
	t.Helper()
	m := newModel(t, f, detectMixed())
	m = send(m, keyRune('n'))
	if v := view(m); !strings.Contains(v, "new session") {
		t.Fatalf("expected the launch form after `n`, got:\n%s", v)
	}
	return m
}

// E7.5 / L-1 — the form renders every collected field plus the declarative option
// schema (label + default).

func TestLaunch_FormRendersFields(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	v := view(m)
	for _, label := range []string{"directory", "agent", "prompt", "worktree"} {
		if !strings.Contains(v, label) {
			t.Errorf("launch form missing %q field label:\n%s", label, v)
		}
	}
	// Declarative option from claudeSchema: label "Model", default "opus".
	if !strings.Contains(v, "Model") || !strings.Contains(v, "opus") {
		t.Errorf("launch form must render options from the declarative schema (Model/opus):\n%s", v)
	}
}

// E7.5 / L-2 — the agent picker lists detected agents; a not-installed agent AND
// an out-of-range agent are greyed and carry an install/upgrade hint.

func TestLaunch_AgentPickerGreysUnavailable(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	v := view(m)

	for _, name := range []string{"claude", "codex", "gemini"} {
		if !strings.Contains(v, name) {
			t.Errorf("agent picker missing %q:\n%s", name, v)
		}
	}
	if !strings.Contains(v, "upgrade codex to >= 1.2.0") { // codex: installed but out of range
		t.Errorf("out-of-range agent must show an upgrade hint:\n%s", v)
	}
	if !strings.Contains(v, "install: npm i -g @google/gemini-cli") { // gemini: not installed
		t.Errorf("not-installed agent must show an install hint:\n%s", v)
	}
}

// E7.5 / L-3 — a nonexistent working directory is refused with an inline error and
// no launch is attempted.

func TestLaunch_InvalidCwdRefused(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	m = sendType(m, "/no/such/dir/really/missing")
	m = send(m, keyEnter) // submit from the cwd field

	if v := view(m); !strings.Contains(v, "exist") { // "... does not exist"
		t.Fatalf("expected an inline cwd error mentioning existence:\n%s", v)
	}
	if reqs := f.launchReqs(); len(reqs) != 0 {
		t.Fatalf("invalid cwd must not launch, got %v", reqs)
	}
}

// E7.5 / L-1 — the cwd field expands `~` to the home directory; a valid form
// submits with the EXPANDED absolute path and the default agent.

func TestLaunch_TildeExpansionAndSubmit(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	m = sendType(m, "~") // expands to $HOME, which exists
	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("valid form should launch exactly once, got %d", len(reqs))
	}
	if reqs[0].Cwd != homeDir(t) {
		t.Fatalf("cwd `~` should expand to home %q, got %q", homeDir(t), reqs[0].Cwd)
	}
	if reqs[0].Agent != "claude" { // default = first installed+in-range agent
		t.Fatalf("expected default agent claude, got %q", reqs[0].Agent)
	}
}

// E7.5 — submitting a valid form calls Client.Launch with the fully composed
// LaunchReq: agent, expanded cwd, schema-default options, AND the initial prompt.

func TestLaunch_SubmitComposesLaunchReq(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	dir := t.TempDir() // a real, existing directory
	m = sendType(m, dir)
	// Field order: directory -> agent -> (Model option) -> prompt. Tab three times.
	m = send(m, keyTab) // agent (leave default claude)
	m = send(m, keyTab) // Model option (leave default opus)
	m = send(m, keyTab) // prompt
	m = sendType(m, "Fix the failing CI")

	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Agent != "claude" {
		t.Errorf("Agent = %q, want claude", got.Agent)
	}
	if got.Cwd != dir {
		t.Errorf("Cwd = %q, want %q", got.Cwd, dir)
	}
	if got.Options["model"] != "opus" {
		t.Errorf("Options[model] = %q, want opus (schema default)", got.Options["model"])
	}
	if got.InitialPrompt != "Fix the failing CI" {
		t.Errorf("InitialPrompt = %q, want %q", got.InitialPrompt, "Fix the failing CI")
	}
}

// E7.5 — GOLDEN: the launch form matches the approved ui-preview look. Regenerate
// with `go test ./internal/tui/ -update`.

func TestGoldenLaunchForm(t *testing.T) {
	tm := startTM(t, New(newFakeClient(), detectMixed()))
	waitContains(t, tm, "new") // general footer painted first
	tm.Send(keyRune('n'))
	waitContains(t, tm, "new session")
	tm.Quit()

	teatest.RequireEqualOutput(t, []byte(finalView(t, tm)))
}
