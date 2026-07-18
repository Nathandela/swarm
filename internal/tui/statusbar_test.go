package tui

import (
	"strings"
	"testing"
	"time"
)

// ADR-006 / bead agents-tracker-qes — the board is a full-screen alternate-screen
// app so it no longer renders inline in the terminal scrollback. In
// charm.land/bubbletea/v2 the alternate screen is requested through the View (there
// is no WithAltScreen program option), so the router's View must set it.
func TestView_UsesAltScreen(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute)), detectMixed())
	if !m.View().AltScreen {
		t.Fatal("the board must run in the alternate screen (ADR-006 full-screen board)")
	}
}

// The status bar is anchored on the bottom row: the composed view is exactly the
// window height, and its last line carries the current screen's context keys
// (promoted from the old inline footer).
func TestStatusBar_AnchoredOnGeneral(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute)), detectMixed())
	lines := strings.Split(view(m), "\n")
	if len(lines) != testRows {
		t.Fatalf("full-screen view must be %d rows tall, got %d", testRows, len(lines))
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "new") || !strings.Contains(last, "attach") {
		t.Fatalf("bottom status bar must carry the general context keys, got last line %q", last)
	}
}

// The launch form carries the same anchored bar, showing its contextual field hint.
func TestStatusBar_AnchoredOnLaunch(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, keyRune('n'))
	lines := strings.Split(view(m), "\n")
	if len(lines) != testRows {
		t.Fatalf("launch form must be %d rows tall, got %d", testRows, len(lines))
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "type or paste") {
		t.Fatalf("launch bar must show the field hint, got last line %q", last)
	}
}

// The kill/delete confirm is a board sub-state and gets its own context keys on the
// same anchored bar.
func TestStatusBar_ConfirmContext(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute)), detectMixed())
	m = send(m, keyCtrlX)
	lines := strings.Split(view(m), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "confirm") || !strings.Contains(last, "cancel") {
		t.Fatalf("confirm status bar must show y confirm / n cancel, got last line %q", last)
	}
}
