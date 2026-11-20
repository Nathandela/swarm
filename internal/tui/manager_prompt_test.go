package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/voice"
)

func TestManagerPromptOptionsPersistOnSave(t *testing.T) {
	dir := t.TempDir()
	m := rootModel{managerSettingsDir: dir, screen: screenOptions}
	m.loadManagerPrompt()
	m.options.focus = optionsFocusManagerPrompt
	if view := m.options.view(80); !strings.Contains(view, "manager prompt") || !strings.Contains(view, "visible to the model") {
		t.Fatalf("manager setting missing: %s", view)
	}
	m.pasteManagerPrompt("Check output.\nThen summarize.")
	next, _ := m.updateOptions(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next.(rootModel).options.managerPromptErr != "" {
		t.Fatal(next.(rootModel).options.managerPromptErr)
	}
	restarted := rootModel{managerSettingsDir: dir}
	restarted.loadManagerPrompt()
	if got := restarted.options.managerPrompt.text; got != "Check output. Then summarize." {
		t.Fatalf("reopened prompt = %q", got)
	}
	restarted.options.managerPrompt.set("unsaved")
	if got, err := voice.LoadManagerStartingPrompt(dir); got != "Check output. Then summarize." || err != nil {
		t.Fatalf("editing changed saved prompt = %q, %v", got, err)
	}
}
