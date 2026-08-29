package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// lineEditor is the shared state and key contract for every TUI-owned
// single-line text field. cursor is a rune index, never a byte offset, so moving
// through or deleting Unicode text cannot split its UTF-8 encoding.
//
// The editor is deliberately a value. Bubble Tea copies the root model on each
// update; retaining pointers into an older model copy would let an update mutate
// state owned by a previous frame.
type lineEditor struct {
	text   string
	cursor int
}

type lineEditResult struct {
	handled bool
	changed bool // content changed; cursor-only movement is false
}

const lineEditFastHint = "⌘←/→ home/end · ⌘⌫/ctrl+u clear-left"

func newLineEditor(text string) lineEditor {
	e := lineEditor{}
	e.set(text)
	return e
}

// set replaces the value from an external picker/default/completion and leaves
// subsequent typing at its end.
func (e *lineEditor) set(text string) {
	e.text = text
	e.cursor = len([]rune(text))
}

// update applies one keypress. Exact fast-edit gestures precede their plain-key
// counterparts. Unsupported modified navigation/deletion keys are consumed as
// no-ops so a form adapter cannot accidentally reinterpret them as a picker or a
// one-rune edit.
func (e *lineEditor) update(k tea.KeyPressMsg) lineEditResult {
	e.clampCursor()

	switch {
	case k.Code == tea.KeyLeft && isCommandArrowModifier(k.Mod),
		k.Code == tea.KeyHome && k.Mod == 0,
		k.Code == 'a' && k.Mod == tea.ModCtrl:
		e.cursor = 0
		return lineEditResult{handled: true}

	case k.Code == tea.KeyRight && isCommandArrowModifier(k.Mod),
		k.Code == tea.KeyEnd && k.Mod == 0,
		k.Code == 'e' && k.Mod == tea.ModCtrl:
		e.cursor = len([]rune(e.text))
		return lineEditResult{handled: true}

	case k.Code == tea.KeyBackspace && (k.Mod == tea.ModSuper || k.Mod == tea.ModMeta),
		k.Code == 'u' && k.Mod == tea.ModCtrl:
		return lineEditResult{handled: true, changed: e.deleteToStart()}

	case k.Code == tea.KeyLeft && k.Mod == 0:
		if e.cursor > 0 {
			e.cursor--
		}
		return lineEditResult{handled: true}

	case k.Code == tea.KeyRight && k.Mod == 0:
		if e.cursor < len([]rune(e.text)) {
			e.cursor++
		}
		return lineEditResult{handled: true}

	case k.Code == tea.KeyBackspace && k.Mod == 0:
		return lineEditResult{handled: true, changed: e.deleteBeforeCursor()}

	// These codes belong to the editing-key family, but only the exact modifier
	// shapes above are supported. Consume every other shape as a safe no-op.
	case k.Code == tea.KeyLeft || k.Code == tea.KeyRight ||
		k.Code == tea.KeyBackspace || k.Code == tea.KeyHome || k.Code == tea.KeyEnd:
		return lineEditResult{handled: true}
	case (k.Code == 'a' || k.Code == 'e' || k.Code == 'u') && k.Mod&tea.ModCtrl != 0:
		return lineEditResult{handled: true}

	case k.Text != "":
		e.insert(k.Text)
		return lineEditResult{handled: true, changed: true}
	}
	return lineEditResult{}
}

// paste inserts bracketed-paste content at the cursor after removing CR/LF.
// Every field using this editor is intentionally single-line.
func (e *lineEditor) paste(text string) bool {
	text = strings.NewReplacer("\r", "", "\n", "").Replace(text)
	if text == "" {
		return false
	}
	e.insert(text)
	return true
}

func (e *lineEditor) insert(text string) {
	if text == "" {
		return
	}
	e.clampCursor()
	runes := []rune(e.text)
	inserted := []rune(text)
	out := make([]rune, 0, len(runes)+len(inserted))
	out = append(out, runes[:e.cursor]...)
	out = append(out, inserted...)
	out = append(out, runes[e.cursor:]...)
	e.text = string(out)
	e.cursor += len(inserted)
}

func (e *lineEditor) deleteBeforeCursor() bool {
	e.clampCursor()
	if e.cursor == 0 {
		return false
	}
	runes := []rune(e.text)
	i := e.cursor - 1
	e.text = string(append(runes[:i], runes[e.cursor:]...))
	e.cursor = i
	return true
}

func (e *lineEditor) deleteToStart() bool {
	e.clampCursor()
	if e.cursor == 0 {
		return false
	}
	runes := []rune(e.text)
	e.text = string(runes[e.cursor:])
	e.cursor = 0
	return true
}

func (e *lineEditor) clampCursor() {
	n := len([]rune(e.text))
	if e.cursor < 0 {
		e.cursor = 0
	}
	if e.cursor > n {
		e.cursor = n
	}
}

// cursorView renders the full value with the insertion cursor at its rune
// position. Width-constrained callers can use editViewport instead.
func (e lineEditor) cursorView() string {
	runes := []rune(e.text)
	cursor := e.cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	return string(runes[:cursor]) + "█" + string(runes[cursor:])
}

func isCommandArrowModifier(mod tea.KeyMod) bool {
	return mod == tea.ModSuper || mod == tea.ModMeta
}
