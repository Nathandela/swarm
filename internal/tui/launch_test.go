package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"
)

// openLaunch drives the router from the general view into the launch form (`n`).
// Detection is async (delivered as a detectMsg off the hot path), so the helper
// seeds it before opening so the form renders against the detected agents rather
// than the transient "checking..." state. The directory field is prefilled with
// the client cwd and focused; Tab moves through fields in L-1 order (directory,
// agent, options..., prompt, worktree); Enter submits the form from any field.
func openLaunch(t *testing.T, f *fakeClient) tea.Model {
	t.Helper()
	m := newModel(t, f, detectMixed())
	m = send(m, detectMsg{agents: detectMixed()()})
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

	// The directory field is prefilled with the client cwd (ADR-006 field-test
	// revision); clear it so this test's own path is what submits.
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
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
	// Clear the cwd prefill (ADR-006) so dir is the exact submitted path.
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
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

// v0.3 / form-nav — UP/DOWN move the field focus like Tab (down = next field, up =
// previous), with the same wrap semantics; Tab still works. detectMixed carries one
// option, so the L-1 field order is directory(0), agent(1), Model(2), prompt(3),
// worktree(4).
func TestLaunch_ArrowsNavigateFields(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	if f := launchOf(m).focus; f != 0 {
		t.Fatalf("form opens on the directory field, got focus %d", f)
	}
	m = send(m, keyDown) // 0 -> 1 (agent)
	if f := launchOf(m).focus; f != 1 {
		t.Fatalf("KeyDown from directory should focus agent (1), got %d", f)
	}
	m = send(m, keyDown) // 1 -> 2 (Model)
	m = send(m, keyUp)   // 2 -> 1 (agent)
	if f := launchOf(m).focus; f != 1 {
		t.Fatalf("KeyUp should move to the previous field (agent=1), got %d", f)
	}
	// Wrap: Up from the first field lands on the last (worktree = fieldCount-1).
	m = send(m, keyUp) // 1 -> 0
	m = send(m, keyUp) // 0 -> last (wrap)
	last := launchOf(m).fieldCount() - 1
	if f := launchOf(m).focus; f != last {
		t.Fatalf("KeyUp from the first field should wrap to the last (%d), got %d", last, f)
	}
	m = send(m, keyDown) // last -> 0 (wrap)
	if f := launchOf(m).focus; f != 0 {
		t.Fatalf("KeyDown from the last field should wrap to the first (0), got %d", f)
	}
	// The footer advertises the up/down affordance alongside tab.
	if v := view(m); !strings.Contains(v, "↑↓") {
		t.Fatalf("footer must advertise up/down field navigation:\n%s", v)
	}
}

// detectBrokenCodex: claude usable, codex FOUND but unusable carrying a derived
// reason (the field-test case: a codex whose npm wrapper crashes so the version
// probe yields nothing). Exercises L-2 design (a): the arrows can LAND on the
// unusable agent, and submitting on it is refused inline WITH the reason.
func detectBrokenCodex() DetectFunc {
	return func() []AgentInfo {
		return []AgentInfo{
			{Name: "claude", Installed: true, InRange: true, Options: claudeSchema()},
			{Name: "codex", Installed: true, InRange: false, Reason: "version probe failed - reinstall?"},
		}
	}
}

// v0.3 / L-2 (design a) — an unusable agent is reachable by the arrows (never a
// silent no-op), renders its reason in the picker, and submitting on it is refused
// inline WITH that reason so the user learns why it cannot launch.
func TestLaunch_UnusableAgentSelectableAndRefusedWithReason(t *testing.T) {
	f := newFakeClient()
	m := newModel(t, f, detectBrokenCodex())
	m = send(m, detectMsg{agents: detectBrokenCodex()()})
	m = send(m, keyRune('n'))

	// The reason is visible in the picker regardless of which field is focused.
	if v := view(m); !strings.Contains(v, "version probe failed - reinstall?") {
		t.Fatalf("picker must show the unusable agent's reason:\n%s", v)
	}

	// Focus the agent field and cycle right onto the unusable codex; the arrow must
	// land on it rather than skip back to the only usable agent.
	m = send(m, keyDown)  // directory -> agent
	m = send(m, keyRight) // claude -> codex (unusable)
	if got := launchOf(m).currentAgentName(); got != "codex" {
		t.Fatalf("right arrow must land on the unusable codex, got %q", got)
	}

	// Submitting on the unusable agent is refused inline, quoting the reason, with no
	// launch attempted.
	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)
	if reqs := f.launchReqs(); len(reqs) != 0 {
		t.Fatalf("submit on an unusable agent must not launch, got %v", reqs)
	}
	v := view(m2)
	if !strings.Contains(v, "no installed, supported agent") {
		t.Fatalf("refusal must keep the guard message:\n%s", v)
	}
	if !strings.Contains(v, "version probe failed - reinstall?") {
		t.Fatalf("refusal must quote the agent's reason:\n%s", v)
	}
}

// E7.5 — GOLDEN: the launch form matches the approved ui-preview look. Regenerate
// with `go test ./internal/tui/ -update`.

func TestGoldenLaunchForm(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "") // deterministic: golden captures the no-auth baseline
	tm := startTM(t, New(newFakeClient(), detectMixed()))
	waitContains(t, tm, "new")                  // general footer painted first
	tm.Send(detectMsg{agents: detectMixed()()}) // deliver async detection so agents render (not "checking...")
	tm.Send(keyRune('n'))
	waitContains(t, tm, "new session")
	quitTM(t, tm)

	// The directory field is prefilled with the client cwd (machine-specific), so
	// normalize it to keep the golden portable across dev/CI checkouts.
	got := finalView(t, tm)
	if wd, err := os.Getwd(); err == nil && wd != "" {
		got = strings.ReplaceAll(got, wd, "<cwd>")
	}
	teatest.RequireEqualOutput(t, []byte(got))
}
