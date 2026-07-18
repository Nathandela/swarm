package tui

// v0.3 ended-session UX (bd agents-tracker-d06). Failing-first suite for the four
// fixes:
//   1. Enter on an ended/lost row surfaces an actionable banner and never attempts
//      the (daemon-refused) attach.
//   2. A successful delete optimistically drops the row and acknowledges it; delete
//      and kill errors are surfaced instead of silently discarded.
//   3. An attach that returns an error banners the reason instead of silently
//      returning to the board.
//   4. A daemon/client build-version skew shows a persistent one-line notice.

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// buildVersionClient wraps the shared fake to also report a daemon build version —
// the optional surface New probes for the version-skew notice — without touching
// the frozen tui.Client interface or the shared fakeClient.
type buildVersionClient struct {
	*fakeClient
	daemonVersion string
}

func (c buildVersionClient) BuildVersion() string { return c.daemonVersion }

// ---------------------------------------------------------------------------
// Item 1 — Enter on an ended/lost row banners instead of the silent no-op.
// ---------------------------------------------------------------------------

// The daemon refuses any attach to a non-running session (internal/daemon
// attach.go), so Enter on an ended row must surface an actionable banner rather
// than dial a doomed attach whose error is swallowed (the field-test silent no-op).
func TestEnter_OnEndedRowBannersInsteadOfAttaching(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/done", "claude", "~/Code/x", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	m, _ = m.Update(keyEnter)

	v := view(m)
	if !strings.Contains(v, "session has ended") {
		t.Fatalf("Enter on an ended row must show a 'session has ended' banner; view:\n%s", v)
	}
	// It must stay on the board (the placeholder attach screen would hide the groups).
	if !strings.Contains(v, "COMPLETED") {
		t.Fatalf("Enter on an ended row must stay on the board, not the attach screen; view:\n%s", v)
	}
}

// Enter on an ended row must NEVER invoke the attach runner (never attempt attach).
func TestEnter_OnEndedRowDoesNotInvokeRunner(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/done", "codex", "~/Code/y", "exited 0", 5*time.Minute))
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	m, _ = m.Update(keyEnter)

	if len(r.recorded()) != 0 {
		t.Fatalf("Enter on an ended row must never attempt attach; runner called %d times", len(r.recorded()))
	}
	if !strings.Contains(view(m), "session has ended") {
		t.Fatalf("Enter on an ended row must surface the banner; view:\n%s", view(m))
	}
}

// A LOST row (shim gone, no clean exit) is likewise unattachable and banners.
func TestEnter_OnLostRowBanners(t *testing.T) {
	lost := mkSession("endpoint/lost", "gemini", "~/Code/z", status.GroupCompleted,
		status.Status{Process: status.ProcessLost, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		"connection lost", 2*time.Minute)
	f := newFakeClient(lost)
	m := newModel(t, f, detectMixed())

	m, _ = m.Update(keyEnter)

	if !strings.Contains(view(m), "session has ended") {
		t.Fatalf("Enter on a lost row must show the banner; view:\n%s", view(m))
	}
}

// ---------------------------------------------------------------------------
// Item 2 — delete acknowledgment (optimistic removal) + delete/kill error banners.
// ---------------------------------------------------------------------------

// A successful delete removes the row from the local model immediately and shows a
// brief acknowledgment banner, rather than waiting for the eventual daemon event
// (the field-test "nothing happens - looks stale").
func TestDelete_SuccessRemovesRowAndAcknowledges(t *testing.T) {
	f := newFakeClient(
		sWorking("endpoint/live", "claude", "~/Code/a", "building", time.Minute),
		sCompleted("endpoint/gone", "gemini", "~/Code/b", "exit 0", time.Hour),
	)
	m := newModel(t, f, detectMixed())

	m = send(m, keyDown)  // select the completed row (endpoint/gone)
	m = send(m, keyCtrlX) // open the delete confirm
	_, cmd := m.Update(keyRune('y'))
	if cmd == nil {
		t.Fatal("confirming a delete issued no command")
	}
	m = send(m, cmd()) // feed the delete reply back

	v := view(m)
	if strings.Contains(v, "exit 0") {
		t.Fatalf("a successful delete must optimistically remove the row; view:\n%s", v)
	}
	if !strings.Contains(v, "session deleted") {
		t.Fatalf("a successful delete must acknowledge with a banner; view:\n%s", v)
	}
	if !strings.Contains(v, "building") {
		t.Fatalf("delete must only remove its target, not other rows; view:\n%s", v)
	}
}

// A failed delete surfaces its error and leaves the row in place.
func TestDelete_ErrorSurfacesToBanner(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/gone", "gemini", "~/Code/b", "exit 0", time.Hour))
	m := newModel(t, f, detectMixed())

	m = send(m, deleteDoneMsg{id: "endpoint/gone", err: errors.New("daemon: session busy")})

	v := view(m)
	if !strings.Contains(v, "delete failed") || !strings.Contains(v, "session busy") {
		t.Fatalf("a failed delete must surface its error on the banner; view:\n%s", v)
	}
	if !strings.Contains(v, "exit 0") {
		t.Fatalf("a failed delete must NOT remove the row; view:\n%s", v)
	}
}

// A failed kill surfaces its error.
func TestKill_ErrorSurfacesToBanner(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/run", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, killDoneMsg{id: "endpoint/run", err: errors.New("daemon: no such session")})

	if v := view(m); !strings.Contains(v, "kill failed") || !strings.Contains(v, "no such session") {
		t.Fatalf("a failed kill must surface its error on the banner; view:\n%s", v)
	}
}

// A successful kill does NOT remove the row: the session transitions to completed
// and stays on the board (the daemon event moves its group).
func TestKill_SuccessKeepsRow(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/run", "codex", "~/Code/x", "compiling", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, killDoneMsg{id: "endpoint/run", err: nil})

	if !strings.Contains(view(m), "compiling") {
		t.Fatalf("a successful kill must not remove the row; view:\n%s", view(m))
	}
}

// ---------------------------------------------------------------------------
// Item 3 — an attach failure banners instead of silently returning.
// ---------------------------------------------------------------------------

func TestAttach_FailureSurfacesToBanner(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, attachDoneMsg{err: errors.New("daemon: session \"s1\" is not running")})

	v := view(m)
	if !strings.Contains(v, "attach failed") || !strings.Contains(v, "is not running") {
		t.Fatalf("a failed attach must surface its error instead of silently returning; view:\n%s", v)
	}
	if !strings.Contains(v, "WORKING") {
		t.Fatalf("after a failed attach the board must be shown; view:\n%s", v)
	}
}

// A clean attach return (no error) does NOT banner.
func TestAttach_CleanReturnDoesNotBanner(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, attachDoneMsg{err: nil})

	if v := view(m); strings.Contains(v, "attach failed") {
		t.Fatalf("a clean attach return must not banner; view:\n%s", v)
	}
}

// ---------------------------------------------------------------------------
// Item 4 — version-skew notice.
// ---------------------------------------------------------------------------

func TestSkewNotice_ShownOnMismatch(t *testing.T) {
	got := skewNotice("0.1.0", "0.2.0")
	if !strings.Contains(got, "daemon 0.1.0") || !strings.Contains(got, "swarm 0.2.0") {
		t.Fatalf("skewNotice(mismatch) must name both versions; got %q", got)
	}
	if !strings.Contains(got, "swarm daemon restart") {
		t.Fatalf("skewNotice(mismatch) must nudge the restart; got %q", got)
	}
}

func TestSkewNotice_SuppressedWhenEqual(t *testing.T) {
	if got := skewNotice("0.2.0", "0.2.0"); got != "" {
		t.Fatalf("skewNotice(equal) = %q; want empty", got)
	}
}

func TestSkewNotice_SuppressedWhenEitherDev(t *testing.T) {
	if got := skewNotice("dev", "0.2.0"); got != "" {
		t.Fatalf("skewNotice(dev daemon) = %q; want empty (dev builds always mismatch)", got)
	}
	if got := skewNotice("0.1.0", "dev"); got != "" {
		t.Fatalf("skewNotice(dev client) = %q; want empty", got)
	}
}

func TestSkewNotice_SuppressedWhenNoDaemonVersion(t *testing.T) {
	if got := skewNotice("", "0.2.0"); got != "" {
		t.Fatalf("skewNotice(no daemon version) = %q; want empty", got)
	}
}

// New captures the daemon build version from the client hello (the optional
// BuildVersion surface).
func TestNew_CapturesDaemonBuildVersion(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	c := buildVersionClient{fakeClient: f, daemonVersion: "0.1.0"}

	m := New(c, detectMixed())

	if got := m.(rootModel).daemonVersion; got != "0.1.0" {
		t.Fatalf("New must capture the daemon build version from the hello; got %q", got)
	}
}

// The board shows the persistent notice when the daemon and client builds differ.
func TestBoard_ShowsSkewNotice(t *testing.T) {
	gm := newGeneralModel([]protocol.SessionView{sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)})
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, daemonVersion: "0.1.0", clientVersion: "0.2.0"}

	v := stripANSI(m.View().Content)
	if !strings.Contains(v, "daemon 0.1.0 differs from swarm 0.2.0") {
		t.Fatalf("the board must show a persistent version-skew notice; view:\n%s", v)
	}
	if !strings.Contains(v, "swarm daemon restart") {
		t.Fatalf("the skew notice must nudge 'swarm daemon restart'; view:\n%s", v)
	}
}

// A terminal too short for the notice row + status bar must not panic (the notice
// reserves a second bottom row, so a height of 1 would otherwise slice negative).
func TestComposeBoard_SkewNoticeShortTerminalDoesNotPanic(t *testing.T) {
	m := rootModel{width: 30, height: 1, daemonVersion: "0.1.0", clientVersion: "0.2.0"}
	_ = m.composeBoard("body", m.generalStatus()) // must not panic
}

// No notice when the builds match.
func TestBoard_SkewNoticeSuppressedWhenMatched(t *testing.T) {
	gm := newGeneralModel([]protocol.SessionView{sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)})
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, daemonVersion: "0.2.0", clientVersion: "0.2.0"}

	if v := stripANSI(m.View().Content); strings.Contains(v, "differs from swarm") {
		t.Fatalf("no skew notice when the builds match; view:\n%s", v)
	}
}
