package tui

// Field-test fixes (agents-tracker-3it P0 perf, agents-tracker-0cx P1 form UX).
//
// These tests pin the new launch-form contract:
//   - detection is async + cached: opening the form must NEVER block on the prober,
//     and shows "checking..." until the first detectMsg lands (P0);
//   - the directory is prefilled with the client cwd; the model string option is
//     editable (type/backspace/paste) and cycles curated suggestions; Space toggles
//     any bool option; paste routes into the focused text field with newlines
//     stripped; an auth indicator surfaces an inherited ANTHROPIC_API_KEY (P1).

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// P0 — opening the launch form must not call the prober on the Update hot path. A
// prober that blocks forever must not delay the form; detection is async (Init
// Cmd) and cached, so `n` opens INSTANTLY and shows "checking..." until results
// land.
func TestLaunch_FormOpenNeverBlocksOnProber(t *testing.T) {
	proberCalled := make(chan struct{}, 1)
	release := make(chan struct{})
	slow := func() []AgentInfo {
		select {
		case proberCalled <- struct{}{}:
		default:
		}
		<-release // never returns until the test releases it
		return nil
	}
	defer close(release)

	m := New(newFakeClient(), slow)
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	done := make(chan tea.Model, 1)
	go func() { done <- send(m, keyRune('n')) }()

	select {
	case m2 := <-done:
		v := view(m2)
		if !strings.Contains(v, "new session") {
			t.Fatalf("form did not open:\n%s", v)
		}
		if !strings.Contains(v, "checking") {
			t.Fatalf("cold detection must render 'checking...', got:\n%s", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening the launch form blocked on the prober (detection must be async)")
	}

	select {
	case <-proberCalled:
		t.Fatal("prober was invoked synchronously on the form-open path")
	default:
	}
}

// P0 — a detectMsg arriving while the form is open updates agent availability live
// (checking -> the detected agents) without discarding the form.
func TestLaunch_DetectionPopulatesFormLive(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, keyRune('n')) // cold cache: no detection delivered yet
	if v := view(m); !strings.Contains(v, "checking") {
		t.Fatalf("expected 'checking...' before detection lands, got:\n%s", v)
	}
	// Stamp the live probe with the form-open generation so the generation gate
	// (item 6) accepts it rather than treating it as stale.
	m = send(m, detectMsg{gen: m.(rootModel).detectGen, agents: detectMixed()()})
	v := view(m)
	if strings.Contains(v, "checking") {
		t.Fatalf("detection landed; 'checking...' must be gone:\n%s", v)
	}
	for _, name := range []string{"claude", "codex", "gemini"} {
		if !strings.Contains(v, name) {
			t.Fatalf("live-refreshed form missing agent %q:\n%s", name, v)
		}
	}
}

// P1 — the directory field is prefilled with the client's working directory
// (os.Getwd at form creation), so a bare Enter launches in the current directory.
func TestLaunch_DirectoryPrefilledWithCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot resolve cwd: %v", err)
	}
	m := openLaunch(t, newFakeClient())
	if got := launchOf(m).cwd; got != wd {
		t.Fatalf("directory prefill = %q, want the client cwd %q", got, wd)
	}
	if !strings.Contains(view(m), wd) {
		t.Fatalf("prefilled cwd %q must render in the form:\n%s", wd, view(m))
	}
}

// P1 — the model string option is editable: typed runes append, Backspace deletes,
// and a pasted value lands (newlines stripped for the single-line field).
func TestLaunch_ModelStringOptionEditable(t *testing.T) {
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, detectMsg{agents: detectEditable()()})
	m = send(m, keyRune('n'))
	m = send(m, keyTab) // directory -> name
	m = send(m, keyTab) // name -> agent
	m = send(m, keyTab) // agent -> model (string)

	m = sendType(m, "opus")
	if got := launchOf(m).options["model"]; got != "opus" {
		t.Fatalf("typed model = %q, want opus", got)
	}
	m = send(m, keyBackspace)
	if got := launchOf(m).options["model"]; got != "opu" {
		t.Fatalf("after backspace model = %q, want opu", got)
	}
	m = send(m, tea.PasteMsg{Content: "s-4\n"})
	if got := launchOf(m).options["model"]; got != "opus-4" {
		t.Fatalf("after paste model = %q, want opus-4 (newline stripped)", got)
	}
}

// P1 — left/right cycle the adapter's curated suggestions on an editable string
// option (empty -> first with right, wrapping).
func TestLaunch_ModelSuggestionsCycle(t *testing.T) {
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, detectMsg{agents: detectEditable()()})
	m = send(m, keyRune('n'))
	m = send(m, keyTab) // directory -> name
	m = send(m, keyTab) // name -> agent
	m = send(m, keyTab) // agent -> model

	// Suggest = [sonnet, opus, haiku]; from empty, right -> sonnet, right -> opus.
	m = send(m, keyRight)
	if got := launchOf(m).options["model"]; got != "sonnet" {
		t.Fatalf("first suggestion = %q, want sonnet", got)
	}
	m = send(m, keyRight)
	if got := launchOf(m).options["model"]; got != "opus" {
		t.Fatalf("second suggestion = %q, want opus", got)
	}
	// Left from sonnet wraps to the last suggestion.
	m = send(m, keyLeft) // opus -> sonnet
	m = send(m, keyLeft) // sonnet -> haiku (wrap)
	if got := launchOf(m).options["model"]; got != "haiku" {
		t.Fatalf("left-wrap suggestion = %q, want haiku", got)
	}
}

// P1 — Space toggles ANY bool option (generalizing the worktree special case),
// rendered as [x]/[ ].
func TestLaunch_SpaceTogglesBoolOption(t *testing.T) {
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, detectMsg{agents: detectEditable()()})
	m = send(m, keyRune('n'))
	m = send(m, keyTab) // directory -> name
	m = send(m, keyTab) // name -> agent
	m = send(m, keyTab) // agent -> model
	m = send(m, keyTab) // model -> skip (bool)

	if got := launchOf(m).options["dangerously-skip-permissions"]; got != "false" {
		t.Fatalf("default skip = %q, want false", got)
	}
	m = send(m, keyRune(' '))
	if got := launchOf(m).options["dangerously-skip-permissions"]; got != "true" {
		t.Fatalf("after space skip = %q, want true", got)
	}
	if v := view(m); !strings.Contains(v, "[x]") {
		t.Fatalf("a toggled bool option must render [x]:\n%s", v)
	}
	m = send(m, keyRune(' '))
	if got := launchOf(m).options["dangerously-skip-permissions"]; got != "false" {
		t.Fatalf("second space skip = %q, want false", got)
	}
}

// P1 — a PasteMsg routes into the focused directory field with \r and \n stripped
// (single-line field).
func TestLaunch_PasteIntoDirectoryStripsNewlines(t *testing.T) {
	m := openLaunch(t, newFakeClient()) // focus starts on directory (prefilled)
	before := launchOf(m).cwd
	m = send(m, tea.PasteMsg{Content: "/tmp/x\r\ny"})
	got := launchOf(m).cwd
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("pasted newlines must be stripped, got %q", got)
	}
	if got != before+"/tmp/xy" {
		t.Fatalf("paste = %q, want %q", got, before+"/tmp/xy")
	}
}

// P1 — the auth indicator renders exactly when the client env carries
// ANTHROPIC_API_KEY and the selected agent is claude; the wording is neutral and
// purely informational (no advice to unset).
func TestLaunch_AuthIndicatorWhenAPIKeySet(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	m := openLaunch(t, newFakeClient()) // default agent is claude
	v := view(m)
	if !strings.Contains(v, "ANTHROPIC_API_KEY from env") {
		t.Fatalf("auth indicator missing when the key is set:\n%s", v)
	}
	if strings.Contains(v, "unset") {
		t.Fatalf("auth indicator must be neutral/informational (no 'unset' advice):\n%s", v)
	}
}

func TestLaunch_NoAuthIndicatorWhenKeyUnset(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	m := openLaunch(t, newFakeClient())
	if v := view(m); strings.Contains(v, "ANTHROPIC_API_KEY") {
		t.Fatalf("auth indicator must be absent when the key is unset:\n%s", v)
	}
}

// P1 — the form carries a per-field contextual hint footer that changes with the
// focused field type.
func TestLaunch_HintFooterFollowsFocus(t *testing.T) {
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, detectMsg{agents: detectEditable()()})
	m = send(m, keyRune('n')) // focus: directory (text)
	if v := view(m); !strings.Contains(v, "type or paste") {
		t.Fatalf("directory hint missing 'type or paste':\n%s", v)
	}
	m = send(m, keyTab) // name (text)
	if v := view(m); !strings.Contains(v, "type or paste") {
		t.Fatalf("name hint missing 'type or paste':\n%s", v)
	}
	m = send(m, keyTab) // agent
	if v := view(m); !strings.Contains(v, "arrows change") {
		t.Fatalf("agent hint missing 'arrows change':\n%s", v)
	}
	m = send(m, keyTab) // model (text)
	m = send(m, keyTab) // skip (bool)
	if v := view(m); !strings.Contains(v, "space toggle") {
		t.Fatalf("bool hint missing 'space toggle':\n%s", v)
	}
}
