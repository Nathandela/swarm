package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// selectedSummary returns the summary carried by the currently-selected row. The
// selected row is marked with a leading "▌" glyph (a text marker, so selection is
// observable without a TTY). rowSummaries lists the row summaries in display order.
func selectedSummary(t *testing.T, m tea.Model, rowSummaries []string) string {
	t.Helper()
	sel := lineContaining(view(m), "▌")
	if sel == "" {
		t.Fatalf("no selected row marker (▌) in:\n%s", view(m))
	}
	for _, s := range rowSummaries {
		if strings.Contains(sel, s) {
			return s
		}
	}
	t.Fatalf("selected row matched none of the known summaries; row:\n%s", sel)
	return ""
}

// E7.4 / V-3 — ↑/↓ and j/k move a flat global selection across groups, and the
// selection WRAPS (down past the last row -> first; up past the first -> last).

func TestKeymap_SelectionWrapsAcrossGroups(t *testing.T) {
	m := newModel(t, fullBoard(), detectMixed())
	rows := []string{
		"Permission: run db migration?",  // r0 needs_input
		"Writing adapter fixture tests",  // r1 working
		"Turn finished, review the diff", // r2 review
		"exit 0",                         // r3 completed
	}

	if got := selectedSummary(t, m, rows); got != rows[0] {
		t.Fatalf("initial selection = %q, want first row", got)
	}

	// Arrow keys, walking down and wrapping.
	for i, want := range []string{rows[1], rows[2], rows[3], rows[0]} {
		m = send(m, keyDown)
		if got := selectedSummary(t, m, rows); got != want {
			t.Fatalf("after %d downs: selection = %q, want %q", i+1, got, want)
		}
	}
	// Up from the first row wraps to the last.
	m = send(m, keyUp)
	if got := selectedSummary(t, m, rows); got != rows[3] {
		t.Fatalf("up from first row should wrap to last, got %q", got)
	}

	// j/k mirror down/up.
	m = send(m, keyRune('j')) // wraps last -> first
	if got := selectedSummary(t, m, rows); got != rows[0] {
		t.Fatalf("j from last should wrap to first, got %q", got)
	}
	m = send(m, keyRune('k')) // wraps first -> last
	if got := selectedSummary(t, m, rows); got != rows[3] {
		t.Fatalf("k from first should wrap to last, got %q", got)
	}
}

// E7.4 / V-3 — Esc from the general view quits the program.

func TestKeymap_EscQuitsFromGeneral(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute)), detectMixed())
	_, cmd := m.Update(keyEsc)
	if !cmdQuits(cmd) {
		t.Fatal("Esc in the general view should quit (tea.Quit)")
	}
}

// E7.4 / R-3 — Ctrl+X on a RUNNING session prompts a KILL confirm (the prompt
// states "kill"); confirming with `y` calls Client.Kill (not Delete).

func TestKeymap_CtrlXKillsRunningSession(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX)
	v := view(m)
	// "kill?" is the confirm-specific token (the footer's "ctrl+x kill" has no "?").
	if !strings.Contains(v, "kill?") {
		t.Fatalf("confirm prompt must state 'kill?' for a running session:\n%s", v)
	}
	if strings.Contains(v, "delete?") {
		t.Fatalf("running-session confirm must not offer to delete:\n%s", v)
	}

	_, cmd := m.Update(keyRune('y'))
	execCmd(cmd)
	if got := f.killedIDs(); len(got) != 1 || got[0] != "endpoint/s1" {
		t.Fatalf("expected Kill(endpoint/s1), got %v", got)
	}
	if len(f.deletedIDs()) != 0 {
		t.Fatalf("Delete must not be called for a running session, got %v", f.deletedIDs())
	}
}

// E7.4 / R-3 — Ctrl+X on a COMPLETED session prompts a DELETE confirm (the prompt
// states "delete"); confirming calls Client.Delete (not Kill).

func TestKeymap_CtrlXDeletesCompletedSession(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/s1", "gemini", "~/Code/x", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX)
	v := view(m)
	if !strings.Contains(v, "delete?") {
		t.Fatalf("confirm prompt must state 'delete?' for a completed session:\n%s", v)
	}
	// "kill?" is the running-session confirm token; the footer's "ctrl+x kill" has no "?".
	if strings.Contains(v, "kill?") {
		t.Fatalf("completed-session confirm must not offer to kill:\n%s", v)
	}

	_, cmd := m.Update(keyRune('y'))
	execCmd(cmd)
	if got := f.deletedIDs(); len(got) != 1 || got[0] != "endpoint/s1" {
		t.Fatalf("expected Delete(endpoint/s1), got %v", got)
	}
	if len(f.killedIDs()) != 0 {
		t.Fatalf("Kill must not be called for a completed session, got %v", f.killedIDs())
	}
}

// E7.4 — a second Ctrl+X resolves the pending confirm (same as `y`).

func TestKeymap_SecondCtrlXResolvesConfirm(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX)        // opens confirm
	_, cmd := m.Update(keyCtrlX) // second Ctrl+X confirms
	execCmd(cmd)
	if got := f.killedIDs(); len(got) != 1 || got[0] != "endpoint/s1" {
		t.Fatalf("second Ctrl+X should resolve the kill confirm, got %v", got)
	}
}

// E7.4 — `n` at the confirm cancels it: no Kill/Delete, and the prompt clears.

func TestKeymap_ConfirmCancelledByN(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX)
	m = send(m, keyRune('n')) // cancel
	if len(f.killedIDs()) != 0 || len(f.deletedIDs()) != 0 {
		t.Fatalf("cancelled confirm must not kill/delete; killed=%v deleted=%v", f.killedIDs(), f.deletedIDs())
	}
	if v := view(m); strings.Contains(v, "kill?") {
		t.Fatalf("confirm prompt should be dismissed after `n`:\n%s", v)
	}
}
