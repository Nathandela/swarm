package tui

// Epic 8 PART A — the TUI-side carry-forwards the walking skeleton lands:
//
//   - the attach sub-model becomes a real raw passthrough: on Enter the router
//     invokes an injected AttachRunner (production dials Client.Attach and drives
//     internal/attach after releasing the tea terminal; tests inject a fake). A
//     completed/lost row attaches READ-ONLY (G3).
//   - New's eager List is bounded so a wedged daemon cannot stall first paint (the
//     bounded-dial carry-forward flagged in tui.go's New).
//   - the V-5 notification banner moves INTO View().Content (under the alt-screen
//     tea.Printf above the program is invisible — the Epic 7 note on general.go).
//   - the kill/delete confirm prompt renders on the confirmID row, not the selected
//     row, so once daemon-side removal exists a target removed mid-confirm cannot
//     paint the prompt onto a neighbor (bd agents-tracker-ddp carry-forward).
//
// FROZEN tui additions the implementer must land (undefined-RED until then):
//
//	type AttachRunner func(session protocol.SessionView, readOnly bool) error
//	type Option func(*rootModel)
//	func WithAttachRunner(r AttachRunner) Option
//	func New(c Client, detect DetectFunc, opts ...Option) tea.Model  // variadic added; 2-arg calls unaffected

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// ---------------------------------------------------------------------------
// Attach-runner wiring (Enter -> raw passthrough), via the injected seam.
// ---------------------------------------------------------------------------

type recordingRunner struct {
	mu        sync.Mutex
	calls     []attachCall
	returnErr error
}

type attachCall struct {
	session  protocol.SessionView
	readOnly bool
}

func (r *recordingRunner) run(s protocol.SessionView, readOnly bool) error {
	r.mu.Lock()
	r.calls = append(r.calls, attachCall{s, readOnly})
	r.mu.Unlock()
	return r.returnErr
}

func (r *recordingRunner) recorded() []attachCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]attachCall(nil), r.calls...)
}

// runAttachCmd applies Enter and then runs whatever command the router returned,
// feeding its completion message back so the model settles after the (fake)
// attach. It returns the settled model.
func runAttachCmd(m tea.Model) tea.Model {
	m2, cmd := m.Update(keyEnter)
	if cmd == nil {
		return m2
	}
	if msg := cmd(); msg != nil {
		m2, _ = m2.Update(msg)
	}
	return m2
}

// E8.1/E8.7 — Enter on a running row invokes the attach runner with that session,
// not read-only, and the model returns to the general board afterward.
func TestAttach_EnterRunsPassthroughForRunningRow(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	m = runAttachCmd(m)

	calls := r.recorded()
	if len(calls) != 1 {
		t.Fatalf("attach runner called %d times, want 1", len(calls))
	}
	if calls[0].session.ID != "endpoint/s1" {
		t.Fatalf("runner session = %q, want endpoint/s1", calls[0].session.ID)
	}
	if calls[0].readOnly {
		t.Fatal("a running session must attach read-write, not read-only")
	}
	if !strings.Contains(view(m), "building") {
		t.Fatal("after detach the general board must be shown again")
	}
}

// E8.4/G3 (superseded by v0.3 d06 item 1) — the daemon refuses any attach to a
// non-running session (internal/daemon attach.go: "session %q is not running"), so
// the former read-only-attach-on-completed path was a dead, silent no-op in
// production (the v0.3 field-test finding); the system spec never promised read-only
// viewing of an ended session. Enter on an ended/lost row now surfaces an actionable
// banner and NEVER invokes the runner. (Running-row read-write attach stays covered
// by TestAttach_EnterRunsPassthroughForRunningRow.) The read-only-view idea is
// preserved as a feature: bd agents-tracker-x5z (read-only view of an ended session's
// final screen from the board).
func TestAttach_EnterOnEndedRowBannersNoAttach(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/done", "codex", "~/Code/y", "exited 0", 5*time.Minute))
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	// Do not use runAttachCmd here: Enter now returns the banner tick (a tea.Tick
	// that would block), not the runner command. Assert the synchronous state.
	m, _ = m.Update(keyEnter)

	if len(r.recorded()) != 0 {
		t.Fatalf("Enter on an ended row must never attempt attach; runner called %d times", len(r.recorded()))
	}
	if !strings.Contains(view(m), "session has ended") {
		t.Fatalf("Enter on an ended row must surface an actionable banner:\n%s", view(m))
	}
}

// E8 — Enter on an empty board is a no-op: no runner call, no panic.
func TestAttach_EnterOnEmptyBoardDoesNothing(t *testing.T) {
	f := newFakeClient() // no sessions
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	_ = runAttachCmd(m)

	if len(r.recorded()) != 0 {
		t.Fatal("Enter on an empty board must not invoke the attach runner")
	}
}

// ---------------------------------------------------------------------------
// New() bounded-dial: a wedged daemon must not stall first paint.
// ---------------------------------------------------------------------------

// blockingClient is a Client whose List blocks forever (a hung daemon).
type blockingClient struct{ release chan struct{} }

func (b *blockingClient) List() ([]protocol.SessionView, error) {
	<-b.release // never released in this test
	return nil, nil
}
func (b *blockingClient) Launch(protocol.LaunchReq) (string, string, error) { return "", "", nil }
func (b *blockingClient) Kill(string) error                                 { return nil }
func (b *blockingClient) Delete(string) error                               { return nil }
func (b *blockingClient) Subscribe() (<-chan protocol.Event, error)         { return nil, nil }

// E8 (bounded-dial carry-forward) — New must bound its eager List so a hung daemon
// cannot stall the first paint. New returns promptly with an (empty) usable board.
func TestNew_BoundedListDoesNotHangOnWedgedDaemon(t *testing.T) {
	b := &blockingClient{release: make(chan struct{})}
	defer close(b.release)

	done := make(chan tea.Model, 1)
	go func() { done <- New(b, detectMixed()) }()

	select {
	case <-done: // New returned within the bound — correct
	case <-time.After(3 * time.Second):
		t.Fatal("New must bound the eager List (a wedged daemon cannot stall first paint)")
	}
}

// ---------------------------------------------------------------------------
// V-5 banner renders inside the frame (alt-screen makes tea.Printf invisible).
// ---------------------------------------------------------------------------

// E8 (in-view banner carry-forward) — a transition into needs_input surfaces the
// banner inside View().Content, so it is visible under the alt-screen.
func TestBanner_RendersInViewContentUnderAltScreen(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", 2*time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, eventMsg{ev: protocol.Event{
		Session: sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission: run tests?", 0),
	}})

	if !strings.Contains(view(m), "claude needs input") {
		t.Fatalf("V-5 banner must render inside View().Content under the alt-screen (Epic 8):\n%s", view(m))
	}
}

// ---------------------------------------------------------------------------
// Confirm prompt renders by identity (confirmID), not by selection index.
// ---------------------------------------------------------------------------

// E8 (confirm-render-by-id carry-forward, bd agents-tracker-ddp) — the kill/delete
// confirm prompt renders on the confirmID row even when a DIFFERENT row is
// selected, so a mid-confirm regroup/removal cannot paint the prompt onto a
// neighbor.
func TestConfirm_PromptRendersOnConfirmIDRowNotSelectedRow(t *testing.T) {
	alpha := sWorking("endpoint/alpha", "alpha", "~/Code/a", "building", time.Minute)
	beta := sWorking("endpoint/beta", "beta", "~/Code/b", "compiling", time.Minute)

	gm := generalModel{
		sessions:    []protocol.SessionView{alpha, beta},
		sel:         1, // beta is selected
		confirm:     true,
		confirmID:   "endpoint/alpha", // but the confirm targets alpha
		confirmKill: true,
		width:       testCols,
	}

	out := stripANSI(gm.view())
	alphaLine := lineContaining(out, "alpha")
	betaLine := lineContaining(out, "beta")

	if !strings.Contains(alphaLine, "kill?") {
		t.Fatalf("confirm prompt must render on the confirmID (alpha) row:\n%s", out)
	}
	if strings.Contains(betaLine, "kill?") || strings.Contains(betaLine, "delete?") {
		t.Fatalf("confirm prompt must NOT render on the selected-but-untargeted (beta) row:\n%s", out)
	}
}

// E8 (confirm-render-by-id carry-forward) — when the confirm target has been
// removed (confirmID matches no row), NO row shows the prompt (it does not fall
// through onto whatever neighbor the selection clamped to).
func TestConfirm_RemovedTargetShowsNoPromptOnNeighbor(t *testing.T) {
	beta := sCompleted("endpoint/beta", "beta", "~/Code/b", "exited 0", time.Minute)

	gm := generalModel{
		sessions:  []protocol.SessionView{beta}, // alpha already removed
		sel:       0,                            // clamped onto beta
		confirm:   true,
		confirmID: "endpoint/alpha", // the removed target
		width:     testCols,
	}

	out := stripANSI(gm.view())
	betaLine := lineContaining(out, "beta")
	if strings.Contains(betaLine, "delete?") || strings.Contains(betaLine, "kill?") {
		t.Fatalf("a removed confirm target must not paint its prompt onto a neighbor:\n%s", out)
	}
}

// keep status import used even if a future edit drops the only reference.
var _ = status.GroupCompleted
