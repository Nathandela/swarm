package tui

import (
	"strings"
	"testing"
)

// detectNoneUsable: codex is out of range, gemini is not installed — no agent is
// usable. firstUsable falls back to index 0, so a naive submit would compose a
// LaunchReq against an unusable agent.
func detectNoneUsable() DetectFunc {
	return func() []AgentInfo {
		return []AgentInfo{
			{Name: "codex", Installed: true, InRange: false, Reason: "upgrade codex to >= 1.2.0"},
			{Name: "gemini", Installed: false, InRange: false, Reason: "install: npm i -g @google/gemini-cli"},
		}
	}
}

// F5 / L-2 — submitting the launch form when no agent is usable is refused inline
// with no Client.Launch call (the client must never compose a request against a
// not-installed / out-of-range agent, even with a valid directory).
func TestLaunch_SubmitRefusedWhenNoUsableAgent(t *testing.T) {
	f := newFakeClient()
	m := newModel(t, f, detectNoneUsable())

	// Seed the async detection result (detection is off the hot path since the
	// v0.2 perf fix) so the form sees "agents present but none usable" rather
	// than the cold not-yet-detected state.
	m = send(m, detectMsg{agents: detectNoneUsable()()})
	m = send(m, keyRune('n')) // open the launch form
	if v := view(m); !strings.Contains(v, "new session") {
		t.Fatalf("expected the launch form after `n`, got:\n%s", v)
	}

	// Clear the cwd prefill (ADR-006) so this test's path is the one submitted.
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
	m = sendType(m, t.TempDir()) // a real, existing directory — cwd check passes
	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)

	if reqs := f.launchReqs(); len(reqs) != 0 {
		t.Fatalf("submit with no usable agent must not launch, got %v", reqs)
	}
	if v := view(m2); !strings.Contains(v, "no installed, supported agent") {
		t.Fatalf("expected an inline 'no usable agent' error, got:\n%s", v)
	}
}
