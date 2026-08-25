package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// v0.5 rename — the board's leftmost column is the session NAME; the agent
// (claude/codex) is its OWN separate column. 'e' on the selected row opens an
// inline single-line edit of the name; type/backspace/paste edit at a movable
// rune-aware cursor, Enter commits a rename op end-to-end, Esc cancels.

// A named row shows BOTH the name and the agent, as separate columns in that order.
func TestBoard_NameAndAgentAreSeparateColumns(t *testing.T) {
	s := sWorking("endpoint/s1", "codex", "~/Code/xyz", "building", time.Minute)
	s.Name = "backend-refactor"
	m := newModel(t, newFakeClient(s), detectMixed())

	row := lineContaining(view(m), "building")
	if row == "" {
		t.Fatalf("no row carried the summary:\n%s", view(m))
	}
	name := strings.Index(row, "backend-refactor")
	agent := strings.Index(row, "codex")
	cwd := strings.Index(row, "~/Code/xyz")
	if name < 0 || agent < 0 || cwd < 0 {
		t.Fatalf("row must show name, agent, and cwd columns:\n%s", row)
	}
	if name >= agent || agent >= cwd {
		t.Fatalf("columns out of order: want name(%d) < agent(%d) < cwd(%d):\n%s", name, agent, cwd, row)
	}
}

// An unnamed row still shows its agent (the separate column identifies it) —
// pressing 'e' can then give it a name.
func TestBoard_UnnamedRowStillShowsAgentColumn(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute)), detectMixed())
	row := lineContaining(view(m), "building")
	if !strings.Contains(row, "codex") {
		t.Fatalf("an unnamed row must still be identified by its agent column:\n%s", row)
	}
}

// 'e' opens the inline editor seeded with the current name; typing appends.
func TestRename_InlineEditTypes(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "old"
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('e'))
	m = sendType(m, "-name")

	row := lineContaining(view(m), "building")
	if !strings.Contains(row, "old-name") {
		t.Fatalf("inline editor must show the edited buffer 'old-name':\n%s", row)
	}
}

// Backspace deletes the last rune of the edit buffer.
func TestRename_InlineEditBackspace(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "abc"
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('e'))
	m = send(m, keyBackspace)

	row := lineContaining(view(m), "building")
	if !strings.Contains(row, "ab") || strings.Contains(row, "abc") {
		t.Fatalf("backspace must drop the last rune (abc -> ab):\n%s", row)
	}
}

// Left and Right move the insertion cursor so a missing character can be repaired
// in place instead of rewriting the suffix.
func TestRename_ArrowKeysEditInTheMiddle(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "ac"
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('e')) // cursor after "ac"
	m = send(m, keyLeft)
	if row := lineContaining(view(m), "building"); !strings.Contains(row, "a█c") {
		t.Fatalf("Left must move the visible cursor between a and c:\n%s", row)
	}
	m = send(m, keyLeft)
	m = send(m, keyRight)
	m = send(m, keyRune('b'))

	rm := m.(rootModel)
	if rm.general.editBuf != "abc" || rm.general.editCursor != 2 {
		t.Fatalf("middle insertion = %q at %d; want abc at 2", rm.general.editBuf, rm.general.editCursor)
	}
	if status := rm.generalStatus(); !strings.Contains(status, "←→") {
		t.Fatalf("rename status must advertise plain cursor navigation, got %q", status)
	}
}

// Cursor movement and deletion are rune-aware: one Backspace removes one Unicode
// code point, never one byte of its UTF-8 encoding.
func TestRename_CursorEditingIsRuneAware(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "aéc"
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('e'))
	m = send(m, keyLeft)      // a é | c
	m = send(m, keyBackspace) // a | c
	rm := m.(rootModel)
	if rm.general.editBuf != "ac" || rm.general.editCursor != 1 {
		t.Fatalf("Unicode backspace = %q at %d; want ac at 1", rm.general.editBuf, rm.general.editCursor)
	}
}

func TestRename_PasteInsertsAtCursor(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "ac"
	m := newModel(t, newFakeClient(s), detectMixed())

	m = send(m, keyRune('e'))
	m = send(m, keyLeft)
	m = send(m, tea.PasteMsg{Content: "b\n"})
	rm := m.(rootModel)
	if rm.general.editBuf != "abc" || rm.general.editCursor != 2 {
		t.Fatalf("paste-at-cursor = %q at %d; want abc at 2", rm.general.editBuf, rm.general.editCursor)
	}
}

// Enter commits the edited name via Client.Rename and shows it optimistically.
func TestRename_EnterCommits(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "old"
	f := newFakeClient(s)
	m := newModel(t, f, detectMixed())

	m = send(m, keyRune('e'))
	m = sendType(m, "X") // buffer: "oldX"
	m2, cmd := m.Update(keyEnter)
	m = m2
	execCmd(cmd) // fire renameCmd -> fake.Rename

	got := f.renamedCalls()
	if len(got) != 1 || got[0].name != "oldX" {
		t.Fatalf("Enter must commit a rename to 'oldX'; got %+v", got)
	}
	if got[0].id != "endpoint/s1" {
		t.Fatalf("rename id = %q, want the namespaced session id", got[0].id)
	}
	// Optimistic: applying the success message shows the new name on the board.
	m = send(m, renameDoneMsg{id: "endpoint/s1", name: "oldX"})
	if row := lineContaining(view(m), "building"); !strings.Contains(row, "oldX") {
		t.Fatalf("committed name must show on the board:\n%s", row)
	}
}

// Paste appends into the editor (newlines stripped); Esc cancels — no rename is sent
// and the original name is restored.
func TestRename_PasteThenEscCancels(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "old"
	f := newFakeClient(s)
	m := newModel(t, f, detectMixed())

	m = send(m, keyRune('e'))
	m = send(m, tea.PasteMsg{Content: "pasted\nname"}) // newline stripped
	if row := lineContaining(view(m), "building"); !strings.Contains(row, "oldpastedname") {
		t.Fatalf("paste must append into the editor with newlines stripped:\n%s", row)
	}

	m = send(m, keyEsc)
	if got := f.renamedCalls(); len(got) != 0 {
		t.Fatalf("Esc must cancel without renaming; got %+v", got)
	}
	row := lineContaining(view(m), "building")
	if !strings.Contains(row, "old") || strings.Contains(row, "oldpastedname") {
		t.Fatalf("Esc must restore the original name:\n%s", row)
	}
}

// Skew-safe: an older daemon without the rename op returns an error; the TUI banners
// the refusal and never crashes.
func TestRename_SkewErrorBanners(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "old"
	f := newFakeClient(s)
	f.renameErr = errors.New(`unknown op "rename"`)
	m := newModel(t, f, detectMixed())

	m = send(m, keyRune('e'))
	m = sendType(m, "Y")
	m2, cmd := m.Update(keyEnter)
	m = m2
	m = send(m, cmd()) // renameCmd resolves to a renameDoneMsg carrying the error

	if !strings.Contains(view(m), "rename failed") {
		t.Fatalf("a rename refusal must banner 'rename failed'; view:\n%s", view(m))
	}
}
