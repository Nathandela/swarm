package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// F1 — selection and the kill/delete confirm must resolve by SESSION IDENTITY,
// not by a flat index. apply() re-groups on every event regardless of screen or
// pending confirm, so a concurrent status event can shift a different session
// under the selection index between Ctrl+X and its resolve. Resolving by the
// captured id keeps the destructive action pinned to the intended session.

// F1(a) — with a kill-confirm open on X, an event that inserts a new session in an
// earlier group shifts X's flat index; resolving must still kill X, not the row
// that slid under the old index.
func TestConfirm_ResolvesByIdentityNotIndex(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/X", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX) // open the kill-confirm on X (only row, index 0)

	// A concurrent needs_input event for a NEW session sorts into an earlier group,
	// pushing X from flat index 0 to 1. (Y is running, so an index-based resolve
	// would kill Y.)
	m = send(m, eventMsg{ev: protocol.Event{Session: sNeedsInput("endpoint/Y", "claude", "~/Code/y", "perm?", 0)}})

	_, cmd := m.Update(keyRune('y'))
	execCmd(cmd)

	if got := f.killedIDs(); len(got) != 1 || got[0] != "endpoint/X" {
		t.Fatalf("confirm must kill the captured target X, got killed=%v", got)
	}
	if len(f.deletedIDs()) != 0 {
		t.Fatalf("no delete expected, got %v", f.deletedIDs())
	}
}

// F1(b) — if the confirm's target disappears while the confirm is open, resolving
// is a no-op, even when a DECOY session now occupies the target's old index (an
// index-based resolve would kill/delete the decoy).
func TestConfirm_NoOpWhenTargetVanishes(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/X", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX) // confirm open on X (index 0)

	// X vanishes and a decoy Y takes index 0. The model never removes sessions
	// itself today, so rewrite the board directly (white-box) to model a future
	// daemon-side delete arriving mid-confirm.
	rm := m.(rootModel)
	rm.general.sessions = []protocol.SessionView{
		sWorking("endpoint/Y", "claude", "~/Code/y", "still going", time.Minute),
	}

	_, cmd := rm.Update(keyRune('y'))
	execCmd(cmd)

	if len(f.killedIDs()) != 0 || len(f.deletedIDs()) != 0 {
		t.Fatalf("resolving a confirm whose target vanished must be a no-op (not act on the decoy); killed=%v deleted=%v", f.killedIDs(), f.deletedIDs())
	}
}

// F1 — if the target flips kill<->delete state (e.g. a running session exits)
// while the confirm is open, resolving is a no-op rather than silently turning a
// kill into a delete.
func TestConfirm_NoOpWhenTargetStateFlips(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/X", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyCtrlX) // kill-confirm on the running X

	// X exits while the confirm is open: the captured intent was "kill", but the
	// row is now a delete target.
	m = send(m, eventMsg{ev: protocol.Event{Session: sCompleted("endpoint/X", "codex", "~/Code/x", "exit 0", 0)}})

	_, cmd := m.Update(keyRune('y'))
	execCmd(cmd)

	if len(f.killedIDs()) != 0 || len(f.deletedIDs()) != 0 {
		t.Fatalf("resolving must no-op when the target flipped kill<->delete; killed=%v deleted=%v", f.killedIDs(), f.deletedIDs())
	}
}

// F1(c) — the visible selection follows the session by identity across an apply()
// that reorders the flat list: an inserted earlier-group row must not drag the
// highlight onto a different session.
func TestSelection_FollowsIdentityAcrossReorder(t *testing.T) {
	f := newFakeClient(
		sWorking("endpoint/A", "codex", "~/Code/a", "AAA", time.Minute),
		sWorking("endpoint/B", "claude", "~/Code/b", "BBB", time.Minute),
	)
	m := newModel(t, f, detectMixed())

	m = send(m, keyDown) // select the second row (B)
	if got := selectedSummary(t, m, []string{"AAA", "BBB"}); got != "BBB" {
		t.Fatalf("precondition: expected BBB selected, got %q", got)
	}

	// A new needs_input session sorts before both working rows, shifting B's index
	// from 1 to 2. Index-based selection would land on A; identity keeps it on B.
	m = send(m, eventMsg{ev: protocol.Event{Session: sNeedsInput("endpoint/Z", "gemini", "~/Code/z", "ZZZ", 0)}})

	if got := selectedSummary(t, m, []string{"AAA", "BBB", "ZZZ"}); got != "BBB" {
		t.Fatalf("selection must stay on B by identity across the reorder, got %q", got)
	}
}

// F1 — Enter attaches to the session under the selection captured at the keypress
// (by identity), even after a reorder has moved that session's index.
func TestAttach_RoutesToSelectedIdentity(t *testing.T) {
	f := newFakeClient(
		sWorking("endpoint/A", "codex", "~/Code/a", "AAA", time.Minute),
		sWorking("endpoint/B", "claude", "~/Code/b", "BBB", time.Minute),
	)
	m := newModel(t, f, detectMixed())

	m = send(m, keyDown) // select B
	// Reorder so B is no longer at index 1.
	m = send(m, eventMsg{ev: protocol.Event{Session: sNeedsInput("endpoint/Z", "gemini", "~/Code/z", "ZZZ", 0)}})

	m = send(m, keyEnter) // attach to the selected session (B)
	v := view(m)
	if !strings.Contains(v, "~/Code/b") || !strings.Contains(v, "claude") {
		t.Fatalf("attach must target the selected session B (claude, ~/Code/b):\n%s", v)
	}
}
