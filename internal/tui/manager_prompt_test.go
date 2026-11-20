package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/voice"
)

func TestManagerPromptOptionsPersistOnSave(t *testing.T) {
	dir := t.TempDir()
	m := New(newFakeClient(), detectMixed(), WithManagerSettingsDir(dir))
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 8})
	m = send(m, keyRune('o'))
	for range 3 {
		m = send(m, keyDown)
	}
	if view := m.View().Content; !strings.Contains(view, "manager prompt") || !strings.Contains(view, "visible to the model") {
		t.Fatalf("manager setting missing: %s", view)
	}
	m = send(m, tea.PasteMsg{Content: "Check output.\nThen summarize."})
	m = send(m, keyEnter)
	if m.(rootModel).options.managerPromptErr != "" {
		t.Fatal(m.(rootModel).options.managerPromptErr)
	}
	restarted := send(New(newFakeClient(), detectMixed(), WithManagerSettingsDir(dir)), keyRune('o')).(rootModel)
	if got := restarted.options.managerPrompt.text; got != "Check output. Then summarize." {
		t.Fatalf("reopened prompt = %q", got)
	}
	restarted.options.managerPrompt.set("unsaved")
	if got, err := voice.LoadManagerStartingPrompt(dir); got != "Check output. Then summarize." || err != nil {
		t.Fatalf("editing changed saved prompt = %q, %v", got, err)
	}
}
