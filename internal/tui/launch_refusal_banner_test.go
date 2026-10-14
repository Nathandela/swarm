package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestBanner_LaunchRefusalNamesTheAccountAndRemedy pins A4's surfacing half: a
// refusal that names several accounts is longer than one board row, and clipping it
// to the width hid exactly the accounts and remedies the owner needs to act on.
func TestBanner_LaunchRefusalNamesTheAccountAndRemedy(t *testing.T) {
	m := New(newFakeClient(), detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	refusal := `launch: accounts: no codex account can take this discussion: "Work" configuration-writer-active - end discussion "fix-login"; ` +
		`"Personal" configuration-writer-active; "Spare" signed-out - sign in again; or turn codex rotation off`
	m = send(m, launchResultMsg{err: errors.New(refusal)})

	shown := strings.Join(strings.Fields(view(m)), " ")
	for _, want := range []string{`"Work" configuration-writer-active - end discussion "fix-login"`, `"Spare" signed-out - sign in again`, "or turn codex rotation off"} {
		if !strings.Contains(shown, want) {
			t.Errorf("banner lacks %q at %d columns; view:\n%s", want, testCols, view(m))
		}
	}
}
