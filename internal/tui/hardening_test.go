package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// v0.3.0 committee hardening. FAILING-FIRST.
//
//   - item 6: after a client-side delete removes a row, a late buffered status event
//     for the SAME id must not resurrect it (the client tombstone that complements
//     the daemon's server-side tombstone).
//   - item 7: composeBoard must never emit more lines than the terminal height, even
//     when the skew notice is present at heights 1 and 2 — the notice is dropped
//     first, then the body, keeping the status bar as the last-resort row.

// item 6 — a successful delete tombstones the id so a late buffered event (one that
// was already queued on the subscribe stream before the delete) cannot re-append the
// removed row.
func TestDelete_TombstonePreventsLateEventReappear(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/gone", "gemini", "~/Code/b", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	// The delete succeeds and drops the row.
	m = send(m, deleteDoneMsg{id: "endpoint/gone", err: nil})
	if strings.Contains(view(m), "exit 0") {
		t.Fatalf("a successful delete must remove the row; view:\n%s", view(m))
	}

	// A late buffered status-change event for the SAME id arrives (it was queued
	// before the delete landed). It must NOT bring the row back.
	late := sCompleted("endpoint/gone", "gemini", "~/Code/b", "exit 0", time.Hour)
	m = send(m, eventMsg{ev: protocol.Event{Session: late}})
	if strings.Contains(view(m), "exit 0") {
		t.Fatalf("a late buffered event must not resurrect a client-deleted row (client tombstone); view:\n%s", view(m))
	}
}

// item 6 — the tombstone is scoped to the deleted id: a different session's event
// still applies normally (the tombstone does not swallow unrelated updates).
func TestDelete_TombstoneDoesNotBlockOtherSessions(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/gone", "gemini", "~/Code/b", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	m = send(m, deleteDoneMsg{id: "endpoint/gone", err: nil})
	other := sWorking("endpoint/other", "claude", "~/Code/x", "compiling", time.Minute)
	m = send(m, eventMsg{ev: protocol.Event{Session: other}})
	if !strings.Contains(view(m), "compiling") {
		t.Fatalf("a tombstone must not block an unrelated session's event; view:\n%s", view(m))
	}
}

// item 7 — the composed board never exceeds the terminal height, with or without the
// version-skew notice, at heights 1, 2, and 3.
func TestComposeBoard_NeverExceedsHeight(t *testing.T) {
	body := "line1\nline2\nline3\nline4\nline5"
	for _, h := range []int{1, 2, 3} {
		plain := rootModel{width: 40, height: h}
		if got := lineCount(plain.composeBoard(body, "status keys")); got != h {
			t.Fatalf("height %d without notice: %d lines, want exactly %d", h, got, h)
		}
		withNotice := rootModel{width: 40, height: h, daemonVersion: "0.1.0", clientVersion: "0.2.0"}
		if got := lineCount(withNotice.composeBoard(body, "status keys")); got != h {
			t.Fatalf("height %d WITH notice: %d lines, want exactly %d", h, got, h)
		}
	}
}

// item 7 — at height 1 the status bar is the last-resort row: the skew notice is
// dropped so the single row carries the context keys, not the notice.
func TestComposeBoard_StatusBarWinsAtHeightOne(t *testing.T) {
	m := rootModel{width: 40, height: 1, daemonVersion: "0.1.0", clientVersion: "0.2.0"}
	out := stripANSI(m.composeBoard("body", "status keys here"))
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("height 1 must be exactly one row; got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "status keys here") {
		t.Fatalf("at height 1 the status bar must survive over the notice; got %q", lines[0])
	}
	if strings.Contains(lines[0], "differs from swarm") {
		t.Fatalf("at height 1 the skew notice must be dropped, not shown; got %q", lines[0])
	}
}

func lineCount(s string) int { return len(strings.Split(s, "\n")) }
