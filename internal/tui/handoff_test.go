package tui

// FAILING-FIRST tests for ADR-010 Phase 4 PIECE 2 (D3 + A2): the TUI trigger. A
// keybinding on the SELECTED roster row injects the '/swarm-handoff ' slash-command
// text (trailing space, NO submit) into that session via the SAME owner-tier
// send_input op the `swarm send` CLI verb uses — "two triggers, one code path"
// (A2) made literal. The human completes the target CLI/model and presses Enter
// inside the session; the TUI never submits on the agent's behalf.
//
// FROZEN additions to the Client interface (tui.go:31), matching
// protocol.Client.SendInput's own signature exactly (internal/protocol/client.go):
//
//	type Client interface {
//	    ... // existing methods unchanged
//	    SendInput(id string, req protocol.SendInputReq) error
//	}
//
// Key: 'h' on the general view, unattached to any existing binding (j/k/down/up,
// enter, n, r, e, ctrl+x, esc — see general.go's updateGeneral and keymap_test.go).
// Gate (D3): fires ONLY when the selected row's Group is needs_input or
// ready_for_review — the session is at a prompt. On any other group (working,
// completed) it sends NOTHING and instead sets the existing transient-banner
// mechanism (generalModel.setBanner, the same surface Enter-on-an-ended-row and a
// failed launch/resume already use) to the exact message:
//
//	"session is busy — try when it is at a prompt"
//
// The injected request is exactly:
//
//	protocol.SendInputReq{Text: "/swarm-handoff ", Submit: false}
//
// against the SELECTED session's id (protocol.SessionView.ID, the namespaced
// <endpoint>/<local> form every other verb/row keys off).
//
// The general status bar (generalStatus in tui.go) gains the key in its help
// surface the same way its neighbors (n new, e rename, ctrl+x kill) are shown.
//
// RED today: Client has no SendInput method and 'h' has no binding, so this file
// fails to compile / the keymap tests fail against the unmodified general.go.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// keyH is the handoff-trigger key.
var keyH = keyRune('h')

// handoffBusyMessage is the exact footer-style refusal (D3) shown when the
// selected session is not at a prompt.
const handoffBusyMessage = "session is busy — try when it is at a prompt"

// wantHandoffReq is the exact steering request the trigger must send: the slash
// command text, UNSUBMITTED (the human completes and submits it inside the
// session).
func wantHandoffText(t *testing.T, got sendInputCall, wantID string) {
	t.Helper()
	if got.id != wantID {
		t.Errorf("SendInput targeted %q, want the selected session %q", got.id, wantID)
	}
	if got.req.Text != "/swarm-handoff " {
		t.Errorf("SendInput text = %q, want the literal slash command with a trailing space", got.req.Text)
	}
	if got.req.Submit {
		t.Error("SendInput must not submit (Submit: false) — the human completes and presses Enter")
	}
	if got.req.Key != "" {
		t.Errorf("SendInput must carry text, not a key; got Key=%q", got.req.Key)
	}
}

// TestKeymap_HandoffInjectsOnNeedsInputSession — the primary case: a session at a
// permission prompt (needs_input) is a session "at a prompt" per D3.
func TestKeymap_HandoffInjectsOnNeedsInputSession(t *testing.T) {
	f := newFakeClient(sNeedsInput("endpoint/s1", "codex", "~/Code/x", "Permission: run db migration?", time.Minute))
	m := newModel(t, f, detectMixed())

	_, cmd := m.Update(keyH)
	execCmd(cmd)

	calls := f.sendInputCalls()
	if len(calls) != 1 {
		t.Fatalf("SendInput called %d times, want exactly 1: %+v", len(calls), calls)
	}
	wantHandoffText(t, calls[0], "endpoint/s1")
}

// TestKeymap_HandoffInjectsOnReadyForReviewSession — the second "at a prompt"
// group D3 names.
func TestKeymap_HandoffInjectsOnReadyForReviewSession(t *testing.T) {
	f := newFakeClient(sReview("endpoint/s1", "claude", "~/Code/x", "Turn finished, review the diff", time.Minute))
	m := newModel(t, f, detectMixed())

	_, cmd := m.Update(keyH)
	execCmd(cmd)

	calls := f.sendInputCalls()
	if len(calls) != 1 {
		t.Fatalf("SendInput called %d times, want exactly 1: %+v", len(calls), calls)
	}
	wantHandoffText(t, calls[0], "endpoint/s1")
}

// TestKeymap_HandoffRefusedOnWorkingSession — the gate: a session mid-turn is not
// at a prompt, so nothing is sent and the busy message is surfaced instead of
// silently queueing (D3).
func TestKeymap_HandoffRefusedOnWorkingSession(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m2, cmd := m.Update(keyH)
	execCmd(cmd)

	if calls := f.sendInputCalls(); len(calls) != 0 {
		t.Fatalf("a working session must send nothing, got %+v", calls)
	}
	if v := view(m2); !strings.Contains(v, handoffBusyMessage) {
		t.Fatalf("a busy session must surface %q, got:\n%s", handoffBusyMessage, v)
	}
}

// TestKeymap_HandoffRefusedOnCompletedSession — a completed row is likewise not at
// a live prompt.
func TestKeymap_HandoffRefusedOnCompletedSession(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/s1", "gemini", "~/Code/x", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	m2, cmd := m.Update(keyH)
	execCmd(cmd)

	if calls := f.sendInputCalls(); len(calls) != 0 {
		t.Fatalf("a completed session must send nothing, got %+v", calls)
	}
	if v := view(m2); !strings.Contains(v, handoffBusyMessage) {
		t.Fatalf("a non-prompt session must surface %q, got:\n%s", handoffBusyMessage, v)
	}
}

// TestKeymap_HandoffTargetsTheSelectedRow — with more than one eligible session on
// the board, the trigger must act on the SELECTED row, not the first one (the
// selection-by-identity discipline every other single-row action in general.go
// already follows: rename, kill/delete confirm, resume).
func TestKeymap_HandoffTargetsTheSelectedRow(t *testing.T) {
	f := newFakeClient(
		sNeedsInput("endpoint/first", "codex", "~/Code/x", "Permission: a?", time.Minute),
		sNeedsInput("endpoint/second", "claude", "~/Code/y", "Permission: b?", 2*time.Minute),
	)
	m := newModel(t, f, detectMixed())

	m = send(m, keyDown) // move selection off the first row onto the second
	_, cmd := m.Update(keyH)
	execCmd(cmd)

	calls := f.sendInputCalls()
	if len(calls) != 1 {
		t.Fatalf("SendInput called %d times, want exactly 1: %+v", len(calls), calls)
	}
	wantHandoffText(t, calls[0], "endpoint/second")
}

// handoffKeyHelp is how the footer must tie the KEY to the action, matching how
// its neighbors render ("n new", "e rename", "ctrl+x kill" — tui.go's
// generalStatus): the key, a space, the word — not the bare word "handoff"
// floating unattached to any key (2026-08-07 RED-review contract fix 6).
const handoffKeyHelp = "h handoff"

// TestStatusBar_HelpListsHandoffKey — the help surface teaches the new key the
// same way its neighbors (n new, e rename, ctrl+x kill) are shown (D3).
func TestStatusBar_HelpListsHandoffKey(t *testing.T) {
	m := newModel(t, newFakeClient(sNeedsInput("endpoint/s1", "codex", "~/Code/x", "Permission: run db migration?", time.Minute)), detectMixed())
	lines := strings.Split(view(m), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, handoffKeyHelp) {
		t.Fatalf("bottom status bar must tie key h to handoff like its neighbors (%q), got last line %q", handoffKeyHelp, last)
	}
}

// TestKeymap_HandoffSendInputErrorBanners exercises the fake's sendInputErr field
// (dead until this fix): a daemon-refused SendInput must banner the failure
// rather than being silently swallowed or crashing the model, mirroring
// TestKill_ErrorSurfacesToBanner (sessionux_test.go) and TestRename_
// SkewErrorBanners (rename_test.go) (contract fix 7).
func TestKeymap_HandoffSendInputErrorBanners(t *testing.T) {
	f := newFakeClient(sNeedsInput("endpoint/s1", "codex", "~/Code/x", "Permission: run db migration?", time.Minute))
	f.sendInputErr = errors.New("daemon: no such session")
	m := newModel(t, f, detectMixed())

	m2, cmd := m.Update(keyH)
	if cmd == nil {
		t.Fatal("Update(h) on a needs_input row returned a nil command; the handoff trigger is not wired yet")
	}
	m = send(m2, cmd())

	if v := view(m); !strings.Contains(v, "handoff failed") || !strings.Contains(v, "no such session") {
		t.Fatalf("a failed SendInput must surface its error on the banner; view:\n%s", v)
	}
}

// TestKeymap_HandoffOnEmptyRosterIsNoOp — no selected row means nothing to
// target: the same guard every other single-row action in general.go already
// applies (selected() returning ok=false) must also cover 'h' (contract fix 7).
func TestKeymap_HandoffOnEmptyRosterIsNoOp(t *testing.T) {
	f := newFakeClient() // empty roster
	m := newModel(t, f, detectMixed())

	_, cmd := m.Update(keyH)
	execCmd(cmd)

	if calls := f.sendInputCalls(); len(calls) != 0 {
		t.Fatalf("an empty roster must send nothing, got %+v", calls)
	}
}
