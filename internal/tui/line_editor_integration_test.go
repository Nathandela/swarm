package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/adapter"
)

// These tests split the contract in two on purpose. The focused unit tests pin
// every key shape once on lineEditor; the table-driven router test then proves
// that every TUI-owned text field actually delegates to that editor. This keeps
// the forms from growing subtly different copies of the same key switch.
func TestLineEditor_FastEditingContract(t *testing.T) {
	const text = "aé🐝bc"
	end := utf8.RuneCountInString(text)
	tests := []struct {
		name        string
		cursor      int
		key         tea.KeyPressMsg
		wantText    string
		wantCursor  int
		wantChanged bool
	}{
		{name: "super left", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper}, wantText: text, wantCursor: 0},
		{name: "meta left", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModMeta}, wantText: text, wantCursor: 0},
		{name: "home", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyHome}, wantText: text, wantCursor: 0},
		{name: "ctrl a", cursor: 3, key: tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, wantText: text, wantCursor: 0},
		{name: "super right", cursor: 2, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper}, wantText: text, wantCursor: end},
		{name: "meta right", cursor: 2, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta}, wantText: text, wantCursor: end},
		{name: "end", cursor: 2, key: tea.KeyPressMsg{Code: tea.KeyEnd}, wantText: text, wantCursor: end},
		{name: "ctrl e", cursor: 2, key: tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, wantText: text, wantCursor: end},
		{name: "super backspace preserves unicode suffix", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}, wantText: "bc", wantCursor: 0, wantChanged: true},
		{name: "meta backspace preserves unicode suffix", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModMeta}, wantText: "bc", wantCursor: 0, wantChanged: true},
		{name: "ctrl u preserves unicode suffix", cursor: 3, key: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, wantText: "bc", wantCursor: 0, wantChanged: true},
		{name: "plain left moves one rune", cursor: 3, key: keyLeft, wantText: text, wantCursor: 2},
		{name: "plain right moves one rune", cursor: 3, key: keyRight, wantText: text, wantCursor: 4},
		{name: "plain backspace deletes one unicode rune", cursor: 3, key: keyBackspace, wantText: "aébc", wantCursor: 2, wantChanged: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLineEditor(text)
			e.cursor = tt.cursor
			result := e.update(tt.key)
			if !result.handled {
				t.Fatalf("%s was not handled", tt.key.Keystroke())
			}
			if result.changed != tt.wantChanged {
				t.Fatalf("changed = %t, want %t", result.changed, tt.wantChanged)
			}
			if e.text != tt.wantText || e.cursor != tt.wantCursor {
				t.Fatalf("text/cursor = %q/%d, want %q/%d", e.text, e.cursor, tt.wantText, tt.wantCursor)
			}
			if !utf8.ValidString(e.text) {
				t.Fatalf("edit split UTF-8: %q", e.text)
			}
		})
	}
}

func TestLineEditor_InsertionAndPasteUseMovedRuneCursor(t *testing.T) {
	e := newLineEditor("ac")
	e.update(keyLeft)
	if result := e.update(tea.KeyPressMsg{Text: "漢字"}); !result.handled || !result.changed {
		t.Fatalf("printable IME text result = %+v, want handled content change", result)
	}
	if e.text != "a漢字c" || e.cursor != 3 {
		t.Fatalf("middle insertion = %q at %d, want a漢字c at 3", e.text, e.cursor)
	}
	if changed := e.paste("🐝\r\n"); !changed {
		t.Fatal("non-empty single-line paste was not reported as a content change")
	}
	if e.text != "a漢字🐝c" || e.cursor != 4 {
		t.Fatalf("middle paste = %q at %d, want a漢字🐝c at 4", e.text, e.cursor)
	}
	if !utf8.ValidString(e.text) || strings.ContainsAny(e.text, "\r\n") {
		t.Fatalf("paste produced an invalid single-line UTF-8 value: %q", e.text)
	}
}

func TestLineEditor_UnsupportedModifiedEditingKeysAreHandledNoOps(t *testing.T) {
	tests := []tea.KeyPressMsg{
		{Code: tea.KeyLeft, Mod: tea.ModSuper | tea.ModShift},
		{Code: tea.KeyLeft, Mod: tea.ModMeta | tea.ModShift},
		{Code: tea.KeyLeft, Mod: tea.ModAlt},
		{Code: tea.KeyLeft, Mod: tea.ModCtrl},
		{Code: tea.KeyRight, Mod: tea.ModSuper | tea.ModShift},
		{Code: tea.KeyRight, Mod: tea.ModMeta | tea.ModShift},
		{Code: tea.KeyRight, Mod: tea.ModAlt},
		{Code: tea.KeyRight, Mod: tea.ModCtrl},
		{Code: tea.KeyBackspace, Mod: tea.ModSuper | tea.ModShift},
		{Code: tea.KeyBackspace, Mod: tea.ModAlt},
		{Code: tea.KeyBackspace, Mod: tea.ModCtrl},
		{Code: tea.KeyHome, Mod: tea.ModMeta},
		{Code: tea.KeyEnd, Mod: tea.ModSuper},
		{Code: 'a', Mod: tea.ModCtrl | tea.ModShift},
		{Code: 'e', Mod: tea.ModCtrl | tea.ModShift},
		{Code: 'u', Mod: tea.ModCtrl | tea.ModShift},
	}

	for _, key := range tests {
		t.Run(key.Keystroke(), func(t *testing.T) {
			e := newLineEditor("aé🐝bc")
			e.cursor = 3
			result := e.update(key)
			if !result.handled || result.changed {
				t.Fatalf("unsupported %s result = %+v, want handled no-op", key.Keystroke(), result)
			}
			if e.text != "aé🐝bc" || e.cursor != 3 {
				t.Fatalf("unsupported %s changed text/cursor to %q/%d", key.Keystroke(), e.text, e.cursor)
			}
		})
	}
}

type routedTextEditor struct {
	name  string
	open  func(*testing.T, string) tea.Model
	value func(tea.Model) string
}

// One compound edit is routed through every writable single-line field. It
// covers a moved-cursor typed insertion, bracketed paste, both Command transport
// shapes, Unicode-safe delete-to-start, and the Ctrl+U fallback. The unit tests
// above carry the exhaustive key matrix; these assertions prove the forms did
// not leave any append-only field behind.
func TestEveryRoutedTextFieldUsesTheLineEditor(t *testing.T) {
	for _, field := range routedTextEditors() {
		t.Run(field.name, func(t *testing.T) {
			m := field.open(t, "c")
			m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
			m = send(m, keyRune('a'))
			m = send(m, tea.PasteMsg{Content: "b\r\n"})
			if got := field.value(m); got != "abc" {
				t.Fatalf("typing/paste at moved cursor = %q, want abc", got)
			}

			m = send(m, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper})
			m = sendType(m, "é🐝")
			if got := field.value(m); got != "é🐝abc" {
				t.Fatalf("Super+Left insertion = %q, want é🐝abc", got)
			}
			m = send(m, tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper})
			if got := field.value(m); got != "abc" {
				t.Fatalf("Super+Backspace = %q, want preserved suffix abc", got)
			}

			m = send(m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta})
			m = send(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
			if got := field.value(m); got != "" {
				t.Fatalf("Meta+Right then Ctrl+U = %q, want empty", got)
			}
		})
	}
}

func TestLaunchAndHandoffRenderCursorAtFastNavigationBoundary(t *testing.T) {
	tests := []struct {
		name  string
		label string
		open  func(*testing.T, string) tea.Model
	}{
		{name: "launch directory", label: "directory", open: openLaunchDirectoryEditor},
		{name: "launch name", label: "name", open: openLaunchNameEditor},
		{name: "launch prompt", label: "prompt", open: openLaunchPromptEditor},
		{name: "launch string option", label: "Model", open: openLaunchStringOptionEditor},
		{name: "handoff model", label: "model", open: openHandoffModelEditor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.open(t, "é🐝")
			m = send(m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
			if row := lineContaining(view(m), tt.label); !strings.Contains(row, "█é🐝") {
				t.Fatalf("Ctrl+A cursor is not rendered at the start of %s: %q", tt.name, row)
			}
			m = send(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
			if row := lineContaining(view(m), tt.label); !strings.Contains(row, "é🐝█") {
				t.Fatalf("Ctrl+E cursor is not rendered at the end of %s: %q", tt.name, row)
			}
		})
	}
}

func TestLaunchDirectoryFastEditingRefreshesCompletionWithoutStealingPlainArrows(t *testing.T) {
	dir := dirFixture(t)
	want := dir + "/bet"
	m := openLaunchDirectoryEditor(t, want)
	if ghost := launchOf(m).dirGhost; ghost != "a/" {
		t.Fatalf("precondition: ghost = %q, want a/", ghost)
	}

	// Exact Command navigation belongs to the editor, not the completion picker.
	m = send(m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper})
	if got := launchOf(m).cwd.text; got != want {
		t.Fatalf("Super+Right accepted completion: cwd = %q, want %q", got, want)
	}

	rm := m.(rootModel)
	rm.launch.errMsg = "stale validation"
	m = send(rm, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModMeta})
	if got := launchOf(m).errMsg; got != "stale validation" {
		t.Fatalf("cursor-only move cleared validation error: %q", got)
	}
	m = send(m, keyRune('x'))
	if got := launchOf(m).cwd.text; got != "x"+want {
		t.Fatalf("Meta+Left did not move the directory cursor: cwd = %q, want %q", got, "x"+want)
	}
	if launchOf(m).errMsg != "" || launchOf(m).dirGhost != "" {
		t.Fatalf("directory content edit did not clear validation/recompute completion: err=%q ghost=%q", launchOf(m).errMsg, launchOf(m).dirGhost)
	}
	m = send(m, tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper})
	if got := launchOf(m).cwd.text; got != want || launchOf(m).dirGhost != "a/" {
		t.Fatalf("prefix delete did not restore text/completion: cwd=%q ghost=%q", got, launchOf(m).dirGhost)
	}

	// The pre-existing exact unmodified arrow remains the completion action.
	m = send(m, keyRight)
	if got := launchOf(m).cwd.text; got != dir+"/beta/" {
		t.Fatalf("plain Right no longer accepts completion: cwd = %q", got)
	}
}

func TestSuggestionEditorsReserveOnlyExactUnmodifiedArrowsForSuggestions(t *testing.T) {
	tests := []struct {
		name       string
		open       func(*testing.T, string) tea.Model
		value      func(tea.Model) string
		wantCycled string
	}{
		{
			name:       "launch string option",
			open:       openLaunchStringOptionEditor,
			value:      func(m tea.Model) string { return launchOf(m).options["model"].text },
			wantCycled: "sonnet",
		},
		{
			name:       "handoff model",
			open:       openHandoffModelEditor,
			value:      func(m tea.Model) string { return m.(rootModel).handoff.model.text },
			wantCycled: "gpt-5.6",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.open(t, "custom")
			m = send(m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper | tea.ModShift})
			if got := tt.value(m); got != "custom" {
				t.Fatalf("unsupported modified Right entered suggestions: value = %q", got)
			}
			m = send(m, keyRight)
			if got := tt.value(m); got != tt.wantCycled {
				t.Fatalf("plain Right suggestion = %q, want %q", got, tt.wantCycled)
			}
			m = send(m, keyRune('X'))
			if got := tt.value(m); got != tt.wantCycled+"X" {
				t.Fatalf("typing after suggestion replacement = %q, want cursor reset to end", got)
			}
		})
	}
}

func TestHandoffModelClearsValidationOnlyOnContentChange(t *testing.T) {
	m := openHandoffModelEditor(t, "custom")
	rm := m.(rootModel)
	rm.handoff.errMsg = "stale validation"
	m = send(rm, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper})
	if got := m.(rootModel).handoff.errMsg; got != "stale validation" {
		t.Fatalf("cursor-only move cleared validation error: %q", got)
	}
	m = send(m, keyRune('x'))
	if got := m.(rootModel).handoff.model.text; got != "xcustom" {
		t.Fatalf("Super+Left did not move handoff cursor: model = %q", got)
	}
	if got := m.(rootModel).handoff.errMsg; got != "" {
		t.Fatalf("model content edit kept stale validation error: %q", got)
	}
}

func TestTextFieldCursorStaysVisibleAtNarrowViewportBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		label string
		open  func(*testing.T, string) tea.Model
	}{
		{name: "launch name", label: "name", open: openLaunchNameEditor},
		{name: "handoff model", label: "model", open: openHandoffModelEditor},
	}
	const width = 26
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.open(t, "prefix-é🐝-a-very-long-suffix")
			m = send(m, tea.WindowSizeMsg{Width: width, Height: testRows})
			for _, key := range []tea.KeyPressMsg{
				{Code: tea.KeyHome},
				{Code: tea.KeyEnd},
			} {
				m = send(m, key)
				row := lineContaining(view(m), tt.label)
				if !strings.Contains(row, "█") {
					t.Fatalf("%s cursor disappeared after %s: %q", tt.name, key.Keystroke(), row)
				}
				if got := lipgloss.Width(row); got > width {
					t.Fatalf("%s row width = %d, want <= %d: %q", tt.name, got, width, row)
				}
				if !utf8.ValidString(row) {
					t.Fatalf("%s viewport split UTF-8: %q", tt.name, row)
				}
			}
		})
	}
}

func TestEmptyHandoffModelKeepsLogicalCursorVisibleAtNarrowWidth(t *testing.T) {
	agents := []AgentInfo{{
		Name:      "codex",
		Installed: true,
		InRange:   true,
		Options: []adapter.OptionSpec{{
			Key: "model", Label: "Model", Type: "string",
		}},
	}}
	detect := func() []AgentInfo { return agents }
	m := newModel(t, newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", 0)), detect)
	m = send(m, detectMsg{agents: agents})
	m = send(m, keyH)
	m = send(m, keyTab) // target -> empty editable model
	const width = 26
	m = send(m, tea.WindowSizeMsg{Width: width, Height: testRows})

	rm := m.(rootModel)
	if rm.handoff.model.text != "" || rm.handoff.model.cursor != 0 {
		t.Fatalf("empty model state = %q at %d, want empty at logical cursor 0", rm.handoff.model.text, rm.handoff.model.cursor)
	}
	row := lineContaining(view(m), "model")
	if !strings.Contains(row, "█") {
		t.Fatalf("empty handoff model hid its logical cursor at narrow width: %q", row)
	}
	if got := lipgloss.Width(row); got > width {
		t.Fatalf("empty handoff model row width = %d, want <= %d: %q", got, width, row)
	}
}

func TestLaunchLongDynamicOptionLabelKeepsCursorVisibleAtNarrowWidth(t *testing.T) {
	const (
		label = "Toolsets (comma-separated)"
		width = 26
	)
	agents := []AgentInfo{{
		Name:      "hermes",
		Installed: true,
		InRange:   true,
		Options: []adapter.OptionSpec{{
			Key: "toolsets", Label: label, Type: "string",
		}},
	}}
	detect := func() []AgentInfo { return agents }
	m := newModel(t, newFakeClient(), detect)
	m = send(m, detectMsg{agents: agents})
	m = send(m, keyRune('n'))
	for n := 0; n < launchOf(m).fieldCount(); n++ {
		if _, ok := launchOf(m).focusedOptionOfType("string"); ok {
			break
		}
		m = send(m, keyDown)
	}
	if _, ok := launchOf(m).focusedOptionOfType("string"); !ok {
		t.Fatalf("failed to focus %q option", label)
	}
	m = sendType(m, "terminal,web,browser,vision")
	m = send(m, tea.WindowSizeMsg{Width: width, Height: testRows})

	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyHome}, {Code: tea.KeyEnd}} {
		m = send(m, key)
		row := lineContaining(view(m), "Toolsets")
		if !strings.Contains(row, "█") {
			t.Fatalf("dynamic-label cursor disappeared after %s: %q", key.Keystroke(), row)
		}
		if got := lipgloss.Width(row); got > width {
			t.Fatalf("dynamic-label row width = %d, want <= %d: %q", got, width, row)
		}
		if !utf8.ValidString(row) {
			t.Fatalf("dynamic-label viewport split UTF-8: %q", row)
		}
	}
}

func TestPasteRespectsModalOwnershipAndLaunchConfirmationInvariant(t *testing.T) {
	t.Run("pairing modal owns paste over launch", func(t *testing.T) {
		m := openLaunchNameEditor(t, "name")
		rm := m.(rootModel)
		rm.pairing = &pairingModal{sas: sasFixture, deviceName: "phone"}
		m = send(rm, tea.PasteMsg{Content: "-leak"})
		if got := launchOf(m).name.text; got != "name" {
			t.Fatalf("paste leaked through pairing modal: %q", got)
		}
	})

	t.Run("handoff disclosure confirmation owns paste", func(t *testing.T) {
		m := openHandoffModelEditor(t, "custom")
		rm := m.(rootModel)
		rm.handoff.confirmTarget = rm.handoff.targetName()
		rm.handoff.confirmModel = rm.handoff.model.text
		m = send(rm, tea.PasteMsg{Content: "-leak"})
		if got := m.(rootModel).handoff.model.text; got != "custom" {
			t.Fatalf("paste leaked through handoff confirmation: %q", got)
		}
	})

	t.Run("paste in any launch field invalidates directory creation", func(t *testing.T) {
		m := openLaunchNameEditor(t, "name")
		rm := m.(rootModel)
		rm.launch.createCwd = "/armed/path"
		m = send(rm, tea.PasteMsg{Content: "-edited"})
		if got := launchOf(m).name.text; got != "name-edited" {
			t.Fatalf("launch paste = %q, want name-edited", got)
		}
		if got := launchOf(m).createCwd; got != "" {
			t.Fatalf("launch paste retained armed directory %q", got)
		}
	})
}

func TestAsyncDetectionRefreshPreservesTextCursorForSameField(t *testing.T) {
	t.Run("launch option", func(t *testing.T) {
		m := openLaunchStringOptionEditor(t, "custom")
		m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
		rm := m.(rootModel)
		m = send(rm, detectMsg{gen: rm.detectGen, agents: detectEditable()()})
		// Existing refresh semantics deliberately clamp an option focus to the
		// directory because the option schema may have shifted. Revisit the same
		// surviving option to verify its editor state was preserved.
		for n := 0; n < launchOf(m).fieldCount(); n++ {
			if _, ok := launchOf(m).focusedOptionOfType("string"); ok {
				break
			}
			m = send(m, keyDown)
		}
		m = send(m, keyRune('X'))
		if got := launchOf(m).options["model"].text; got != "Xcustom" {
			t.Fatalf("launch refresh moved option cursor: %q", got)
		}
	})

	t.Run("handoff model", func(t *testing.T) {
		m := openHandoffModelEditor(t, "custom")
		m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
		rm := m.(rootModel)
		m = send(rm, detectMsg{gen: rm.detectGen, agents: handoffAgents()()})
		m = send(m, keyRune('X'))
		if got := m.(rootModel).handoff.model.text; got != "Xcustom" {
			t.Fatalf("handoff refresh moved model cursor: %q", got)
		}
	})
}

func TestExternalTextReplacementResetsCursorToRuneEnd(t *testing.T) {
	t.Run("directory completion", func(t *testing.T) {
		dir := dirFixture(t)
		m := openLaunchDirectoryEditor(t, dir+"/bet") // ghost: "a/"
		m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
		m = send(m, keyRight) // completion replaces the value with ".../beta/"
		m = send(m, keyRune('X'))
		if got := launchOf(m).cwd.text; got != dir+"/beta/X" {
			t.Fatalf("typing after directory replacement = %q, want cursor at replacement end", got)
		}
	})

	resetAgents := []AgentInfo{
		{Name: "alpha", Installed: true, InRange: true, Options: []adapter.OptionSpec{{Key: "model", Label: "Model", Type: "string", Default: "alpha-model"}}},
		{Name: "beta", Installed: true, InRange: true, Options: []adapter.OptionSpec{{Key: "model", Label: "Model", Type: "string", Default: "beta-model"}}},
	}
	detectReset := func() []AgentInfo { return resetAgents }

	t.Run("launch agent schema", func(t *testing.T) {
		m := newModel(t, newFakeClient(), detectReset)
		m = send(m, detectMsg{agents: resetAgents})
		m = send(m, keyRune('n'))
		m = send(m, keyDown) // name
		m = send(m, keyDown) // tag
		m = send(m, keyDown) // agent
		m = send(m, keyDown) // model
		m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
		m = send(m, keyUp)    // model -> agent
		m = send(m, keyRight) // alpha -> beta, reload schema/defaults
		m = send(m, keyDown)  // agent -> model
		m = send(m, keyRune('X'))
		if got := launchOf(m).options["model"].text; got != "beta-modelX" {
			t.Fatalf("typing after launch-agent replacement = %q, want beta-modelX", got)
		}
	})

	t.Run("handoff target", func(t *testing.T) {
		m := newModel(t, newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", 0)), detectReset)
		m = send(m, detectMsg{agents: resetAgents})
		m = send(m, keyH)
		m = send(m, keyTab) // target -> model
		m = send(m, tea.KeyPressMsg{Code: tea.KeyHome})
		m = send(m, keyUp)    // model -> target
		m = send(m, keyRight) // alpha -> beta, load beta default
		m = send(m, keyDown)  // target -> model
		m = send(m, keyRune('X'))
		if got := m.(rootModel).handoff.model.text; got != "beta-modelX" {
			t.Fatalf("typing after handoff-target replacement = %q, want beta-modelX", got)
		}
	})
}

func TestLaunchTextHintsAdvertiseFastEditingWithoutDroppingFormActions(t *testing.T) {
	tests := []struct {
		name string
		open func(*testing.T, string) tea.Model
	}{
		{name: "directory", open: openLaunchDirectoryEditor},
		{name: "name", open: openLaunchNameEditor},
		{name: "prompt", open: openLaunchPromptEditor},
		{name: "string option", open: openLaunchStringOptionEditor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.open(t, "value")
			lines := strings.Split(view(m), "\n")
			status := lines[len(lines)-1]
			for _, want := range []string{"⌘←/→", "⌘⌫/ctrl+u", "↑↓", "enter launch", "esc cancel"} {
				if !strings.Contains(status, want) {
					t.Errorf("launch %s status %q missing %q", tt.name, status, want)
				}
			}
		})
	}
}

func routedTextEditors() []routedTextEditor {
	return []routedTextEditor{
		{name: "inline rename", open: openRenameEditor, value: generalEditValue},
		{name: "inline tag", open: openTagEditor, value: generalEditValue},
		{name: "launch directory", open: openLaunchDirectoryEditor, value: func(m tea.Model) string { return launchOf(m).cwd.text }},
		{name: "launch name", open: openLaunchNameEditor, value: func(m tea.Model) string { return launchOf(m).name.text }},
		{name: "launch prompt", open: openLaunchPromptEditor, value: func(m tea.Model) string { return launchOf(m).prompt.text }},
		{name: "launch string option", open: openLaunchStringOptionEditor, value: func(m tea.Model) string { return launchOf(m).options["model"].text }},
		{name: "handoff model", open: openHandoffModelEditor, value: func(m tea.Model) string { return m.(rootModel).handoff.model.text }},
	}
}

func openRenameEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	return renameEditorAt(t, seed, utf8.RuneCountInString(seed))
}

func openTagEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	s := sWorking("endpoint/s1", "codex", "~/Code/x", "building", 0)
	s.Tag = seed
	m := send(newModel(t, newFakeClient(s), detectMixed()), keyRune('t'))
	rm := m.(rootModel)
	if !rm.general.editing || !rm.general.editTag {
		t.Fatal("failed to enter inline tag editor")
	}
	return m
}

func generalEditValue(m tea.Model) string { return m.(rootModel).general.edit.text }

func openLaunchDirectoryEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	m := openLaunch(t, newFakeClient())
	return replaceFocusedText(t, m, func(m tea.Model) string { return launchOf(m).cwd.text }, seed)
}

func openLaunchNameEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	m := openLaunch(t, newFakeClient())
	m = send(m, keyDown)
	if !launchOf(m).isName() {
		t.Fatalf("failed to focus launch name; focus=%d", launchOf(m).focus)
	}
	return replaceFocusedText(t, m, func(m tea.Model) string { return launchOf(m).name.text }, seed)
}

func openLaunchPromptEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	m := openLaunch(t, newFakeClient())
	for n := 0; !launchOf(m).isPrompt() && n < launchOf(m).fieldCount(); n++ {
		m = send(m, keyDown)
	}
	if !launchOf(m).isPrompt() {
		t.Fatalf("failed to focus launch prompt; focus=%d", launchOf(m).focus)
	}
	return replaceFocusedText(t, m, func(m tea.Model) string { return launchOf(m).prompt.text }, seed)
}

func openLaunchStringOptionEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	m := newModel(t, newFakeClient(), detectEditable())
	m = send(m, detectMsg{agents: detectEditable()()})
	m = send(m, keyRune('n'))
	for n := 0; n < launchOf(m).fieldCount(); n++ {
		if _, ok := launchOf(m).focusedOptionOfType("string"); ok {
			return replaceFocusedText(t, m, func(m tea.Model) string { return launchOf(m).options["model"].text }, seed)
		}
		m = send(m, keyDown)
	}
	t.Fatalf("failed to focus launch string option; focus=%d", launchOf(m).focus)
	return nil
}

func openHandoffModelEditor(t *testing.T, seed string) tea.Model {
	t.Helper()
	f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", 0))
	m := newModel(t, f, handoffAgents())
	m = send(m, detectMsg{agents: handoffAgents()()})
	m = send(m, keyH)
	m = send(m, keyRight) // claude -> editable codex target
	m = send(m, keyTab)   // target -> model
	rm := m.(rootModel)
	if rm.screen != screenHandoff || rm.handoff.focus != 1 || rm.handoff.targetName() != "codex" {
		t.Fatalf("failed to focus editable handoff model: screen=%v focus=%d target=%q", rm.screen, rm.handoff.focus, rm.handoff.targetName())
	}
	return replaceFocusedText(t, m, func(m tea.Model) string { return m.(rootModel).handoff.model.text }, seed)
}

func replaceFocusedText(t *testing.T, m tea.Model, value func(tea.Model) string, seed string) tea.Model {
	t.Helper()
	for n := 0; value(m) != "" && n < 1024; n++ {
		m = send(m, keyBackspace)
	}
	if got := value(m); got != "" {
		t.Fatalf("could not clear focused text field; remaining value %q", got)
	}
	return sendType(m, seed)
}
