package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var keyH = keyRune('h')

func sPrompt(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView {
	return mkSession(id, agent, cwd, status.GroupNeedsInput,
		status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPrompt},
		summary, ago)
}

func handoffAgents() DetectFunc {
	return func() []AgentInfo {
		return []AgentInfo{
			{Name: "claude", Installed: true, InRange: true, Options: []adapter.OptionSpec{
				{Key: "model", Label: "Model", Type: "choice", Choices: []string{"opus", "sonnet"}, Default: "opus"},
				{Key: "dangerously-skip-permissions", Label: "Skip permissions", Type: "bool", Default: "false"},
			}},
			{Name: "codex", Installed: true, InRange: true, Options: []adapter.OptionSpec{
				{Key: "model", Label: "Model", Type: "string", Suggest: []string{"gpt-5.6", "gpt-5.5"}, Default: "gpt-5.6"},
				{Key: "sandbox", Label: "Sandbox", Type: "choice", Choices: []string{"read-only", "danger-full-access"}},
			}},
		}
	}
}

func TestHandoff_OpenFormForOrdinaryPromptAndReadyReview(t *testing.T) {
	for _, source := range []protocol.SessionView{
		sPrompt("endpoint/prompt", "codex", "/repo", "What next?", time.Minute),
		sReview("endpoint/review", "codex", "/repo", "review", time.Minute),
	} {
		t.Run(string(source.Group), func(t *testing.T) {
			m := newModel(t, newFakeClient(source), handoffAgents())
			m = send(m, detectMsg{agents: handoffAgents()()})
			m2, cmd := m.Update(keyH)
			if cmd == nil {
				t.Fatal("opening handoff must refresh agent detection")
			}
			rm := m2.(rootModel)
			if rm.screen != screenHandoff {
				t.Fatalf("screen = %v, want handoff", rm.screen)
			}
			if rm.handoff.sourceID != source.ID {
				t.Errorf("captured source = %q, want %q", rm.handoff.sourceID, source.ID)
			}
			if calls := rm.client.(*fakeClient).sendInputCalls(); len(calls) != 0 {
				t.Fatalf("opening form sent input: %+v", calls)
			}
		})
	}
}

// REWRITTEN under ADR-010 Amendment 4 E2. The superseded test was
// TestHandoff_RefusesPermissionBusyAndEndedBeforeForm: it asserted that `h` on a
// permission-blocked, busy or ended row REFUSED to open the form, bannered the
// reason, and left the screen on the general board. That assertion is now wrong.
// E2 narrows B3's eligibility predicate from a gate on the whole feature to the
// DEFAULT of the form's method field, with the measured reason that the predicate
// refuses at precisely the moment a source cannot cooperate — the only moment a
// hands-off handoff is needed — and that a rate-limited session is byte-identical
// on the wire to a healthy idle one, so there is no predicate to widen. `h` now
// opens the form on ANY row.
//
// The coverage the old test carried was MOVED, not deleted:
//   - the three refusal MESSAGES still exist and are still asserted, on the
//     supervised method at submit, in TestHandoff_SupervisedSubmitStillRevalidates.
//   - which method each row DEFAULTS to is asserted in
//     TestHandsOff_DefaultMethodFollowsEligibilityAndStaysSelectable.
func TestHandoff_OpensTheFormOnEveryRow(t *testing.T) {
	cases := []struct {
		name   string
		source protocol.SessionView
	}{
		{"permission", sNeedsInput("endpoint/permission", "codex", "/repo", "allow?", time.Minute)},
		{"working", sWorking("endpoint/working", "codex", "/repo", "busy", time.Minute)},
		{"completed", sCompleted("endpoint/done", "codex", "/repo", "done", time.Minute)},
		{"lost", sLost("endpoint/lost", "codex", "/repo", "lost", time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeClient(tc.source)
			m := newModel(t, f, handoffAgents())
			m2, cmd := m.Update(keyH)
			if cmd == nil {
				t.Fatal("opening handoff must refresh agent detection")
			}
			rm := m2.(rootModel)
			if rm.screen != screenHandoff {
				t.Fatalf("screen = %v, want handoff", rm.screen)
			}
			if rm.handoff.sourceID != tc.source.ID {
				t.Errorf("captured source = %q, want %q", rm.handoff.sourceID, tc.source.ID)
			}
			// Opening a form is not an action on the source: E3 is that the source is
			// never signalled, and B2 is unchanged for the supervised method too.
			if calls := f.sendInputCalls(); len(calls) != 0 {
				t.Fatalf("opening form sent input: %+v", calls)
			}
			if reqs := f.launchReqs(); len(reqs) != 0 {
				t.Fatalf("opening form launched: %+v", reqs)
			}
		})
	}
}

func TestHandoff_FormHasOnlyTargetAndModelControls(t *testing.T) {
	m := newModel(t, newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute)), handoffAgents())
	m = send(m, detectMsg{agents: handoffAgents()()})
	m = send(m, keyH)

	got := strings.ToLower(view(m))
	for _, want := range []string{"target", "model", "claude", "opus"} {
		if !strings.Contains(got, want) {
			t.Errorf("form missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"sandbox", "skip permission", "danger-full-access"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("form exposed launch scope %q:\n%s", forbidden, got)
		}
	}

	m = send(m, keyRight)
	if got := m.(rootModel).handoff.targetName(); got != "codex" {
		t.Fatalf("right on target selected %q, want codex", got)
	}
	m = send(m, keyTab)
	m = send(m, keyRight)
	if got := m.(rootModel).handoff.model; got != "gpt-5.5" {
		t.Fatalf("right on model selected %q, want gpt-5.5", got)
	}
}

func TestHandoff_ModelEditPasteAndDetectionRefreshPreserveSelection(t *testing.T) {
	f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute))
	m := newModel(t, f, handoffAgents())
	m = send(m, detectMsg{agents: handoffAgents()()})
	m = send(m, keyH)
	m = send(m, keyRight) // codex
	m = send(m, keyTab)   // model
	m = send(m, tea.PasteMsg{Content: "-custom\r\n"})

	rm := m.(rootModel)
	if rm.handoff.targetName() != "codex" || rm.handoff.model != "gpt-5.6-custom" {
		t.Fatalf("edited form = target %q model %q", rm.handoff.targetName(), rm.handoff.model)
	}
	refreshed := handoffAgents()()
	refreshed = append(refreshed, AgentInfo{Name: "gemini", Installed: false, InRange: false})
	m = send(m, detectMsg{gen: rm.detectGen, agents: refreshed})
	rm = m.(rootModel)
	if rm.handoff.targetName() != "codex" || rm.handoff.model != "gpt-5.6-custom" {
		t.Fatalf("refresh clobbered selection = target %q model %q", rm.handoff.targetName(), rm.handoff.model)
	}
}

func TestHandoff_SubmitSendsCompletePromptToCapturedSource(t *testing.T) {
	f := newFakeClient(
		sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute),
		sPrompt("endpoint/other", "codex", "/repo", "other", 2*time.Minute),
	)
	m := newModel(t, f, handoffAgents())
	m = send(m, detectMsg{agents: handoffAgents()()})
	m = send(m, keyH)

	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)
	if rm := m2.(rootModel); rm.screen != screenGeneral {
		t.Fatalf("screen after submit = %v, want general", rm.screen)
	}
	calls := f.sendInputCalls()
	if len(calls) != 1 {
		t.Fatalf("SendInput calls = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].id != "endpoint/source" {
		t.Errorf("SendInput id = %q, want captured source", calls[0].id)
	}
	if !calls[0].req.Submit || calls[0].req.Key != "" {
		t.Errorf("request = %+v, want submitted text", calls[0].req)
	}
	if !strings.Contains(calls[0].req.Text, "swarm handoff --cli 'claude'") {
		t.Errorf("prompt missing selected handoff command:\n%s", calls[0].req.Text)
	}
	if got := len(f.launchReqs()); got != 0 {
		t.Errorf("TUI launched %d sessions directly, want 0", got)
	}
}

func TestHandoff_FormCapturesSourceIDAcrossRegroup(t *testing.T) {
	f := newFakeClient(
		sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute),
		sPrompt("endpoint/other", "codex", "/repo", "other", 2*time.Minute),
	)
	m := newModel(t, f, handoffAgents())
	m = send(m, detectMsg{agents: handoffAgents()()})
	m = send(m, keyH)

	// Moving the source from needs_input to ready_for_review places another row at
	// the original flat selection index. Submission must still resolve the captured ID.
	moved := sReview("endpoint/source", "codex", "/repo", "review", time.Minute)
	m = send(m, eventMsg{ev: protocol.Event{Session: moved}})
	_, cmd := m.Update(keyEnter)
	execCmd(cmd)
	calls := f.sendInputCalls()
	if len(calls) != 1 || calls[0].id != "endpoint/source" {
		t.Fatalf("regrouped submission calls = %+v, want captured endpoint/source", calls)
	}
}

// REWRITTEN under ADR-010 Amendment 4 E2. The superseded test was
// TestHandoff_SubmitRevalidatesSourceState. What it asserted still half-holds and
// half-does-not, which is why it is rewritten rather than patched:
//
//   - STILL TRUE, and still asserted here: B3's revalidation clause is untouched
//     for the SUPERVISED method. It re-resolves the captured row immediately
//     before submission and refuses a permission request, an active turn and an
//     ended process, with the same three separate messages.
//   - NOW WRONG: the old test read as "a source in this state cannot be handed
//     off" -- it was the last gate, so a refusal here was the end of the road. E2
//     changes what a refusal MEANS: the other method is one field away in the
//     same form, and the test now proves that by handing the same changed row off
//     with the hands-off method instead.
//
// The zero-Launch assertion is new and load-bearing: a supervised refusal must not
// quietly fall back to the launch branch (E7 -- no refusal may degrade to a bare,
// context-free launch).
func TestHandoff_SupervisedSubmitStillRevalidates(t *testing.T) {
	cases := []struct {
		changed protocol.SessionView
		wantMsg string
	}{
		{sWorking("endpoint/source", "codex", "/repo", "busy", time.Minute), "busy"},
		{sNeedsInput("endpoint/source", "codex", "/repo", "allow?", time.Minute), "permission"},
		{sCompleted("endpoint/source", "codex", "/repo", "done", time.Minute), "ended"},
	}
	for _, tc := range cases {
		t.Run(tc.wantMsg, func(t *testing.T) {
			f := newHandsOffClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute))
			m := newModel(t, f, handoffAgents())
			m = openForm(m)
			if got := m.(rootModel).handoff.method; got != handoffMethodSupervised {
				t.Fatalf("frozen method = %q, want supervised on a prompting row", got)
			}
			m = send(m, eventMsg{ev: protocol.Event{Session: tc.changed}})
			m2, cmd := m.Update(keyEnter)
			if cmd != nil {
				execCmd(cmd)
			}
			assertNothingIssued(t, f.fakeClient)
			rm := m2.(rootModel)
			if rm.screen != screenHandoff || !strings.Contains(rm.handoff.errMsg, tc.wantMsg) {
				t.Fatalf("state refusal = screen %v error %q, want one naming %q", rm.screen, rm.handoff.errMsg, tc.wantMsg)
			}

			// E2: the refusal is not the end of the road. The same row, in the same
			// open form, hands off with the other method.
			m3 := selectMethod(t, m2, handoffMethodHandsOff)
			m3 = send(m3, keyEnter)
			if !m3.(rootModel).handoff.confirmPending() {
				t.Fatal("codex source handed to a claude target without the E4 confirmation")
			}
			_, cmd = m3.Update(keyRune('y'))
			execCmd(cmd)
			if calls := f.sendInputCalls(); len(calls) != 0 {
				t.Fatalf("hands-off fallback signalled the source: %+v", calls)
			}
			if reqs := f.launchReqs(); len(reqs) != 1 {
				t.Fatalf("hands-off fallback Launch calls = %d, want 1: %+v", len(reqs), reqs)
			}
		})
	}
}

func TestHandoff_DetectionRefreshResizeEscapeAndSendError(t *testing.T) {
	f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute))
	m := newModel(t, f, func() []AgentInfo { return nil })
	m = send(m, keyH)
	if got := strings.ToLower(view(m)); !strings.Contains(got, "checking") {
		t.Fatalf("cold form missing detection state:\n%s", got)
	}
	m = send(m, detectMsg{gen: m.(rootModel).detectGen, agents: handoffAgents()()})
	m = send(m, tea.WindowSizeMsg{Width: 36, Height: 12})
	for _, line := range strings.Split(view(m), "\n") {
		if w := lipgloss.Width(line); w > 36 {
			t.Fatalf("narrow line is %d cells: %q", w, line)
		}
	}
	m2, _ := m.Update(keyEsc)
	if rm := m2.(rootModel); rm.screen != screenGeneral {
		t.Fatalf("Esc screen = %v, want general", rm.screen)
	}

	f.sendInputErr = errors.New("daemon refused")
	m2 = send(m2, tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = send(m2, keyH)
	m2, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatal("submit returned nil command")
	}
	msg := cmd()
	switch msg := msg.(type) {
	case handoffDoneMsg:
		m2, _ = m2.Update(msg)
	case tea.BatchMsg:
		for _, child := range msg {
			if child == nil {
				continue
			}
			if done, ok := child().(handoffDoneMsg); ok {
				m2, _ = m2.Update(done)
			}
		}
	}
	if got := view(m2); !strings.Contains(got, "handoff failed") || !strings.Contains(got, "daemon refused") {
		t.Fatalf("send failure not bannered:\n%s", got)
	}
}

func TestHandoff_EmptyRosterIsNoOp(t *testing.T) {
	m := newModel(t, newFakeClient(), handoffAgents())
	m2, cmd := m.Update(keyH)
	if cmd != nil || m2.(rootModel).screen != screenGeneral {
		t.Fatalf("empty roster changed screen or returned command")
	}
}

func TestHandoff_NoUsableTargetRefusesLocally(t *testing.T) {
	f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute))
	detect := func() []AgentInfo {
		return []AgentInfo{{Name: "claude", Installed: false, InRange: false, Reason: "not installed"}}
	}
	m := newModel(t, f, detect)
	m = send(m, detectMsg{agents: detect()})
	m = send(m, keyH)
	m2, cmd := m.Update(keyEnter)
	if cmd != nil {
		t.Fatal("no-target refusal returned a command")
	}
	rm := m2.(rootModel)
	if rm.screen != screenHandoff || !strings.Contains(rm.handoff.errMsg, "no installed") {
		t.Fatalf("no-target refusal = screen %v error %q", rm.screen, rm.handoff.errMsg)
	}
	if len(f.sendInputCalls()) != 0 {
		t.Fatal("no-target refusal sent source input")
	}
}

// ---------------------------------------------------------------------------
// ADR-010 Amendment 3 C1: the form's third and last field is the supervision mode
// (passive by default), cycled with the arrows; focus wraps over three fields.
// ---------------------------------------------------------------------------

func TestHandoff_FormHasSupervisionAsThirdField(t *testing.T) {
	m := newModel(t, newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute)), handoffAgents())
	m = send(m, detectMsg{agents: handoffAgents()()})
	m = send(m, keyH)

	rm := m.(rootModel)
	if rm.handoff.supervision != protocol.SupervisionPassive {
		t.Fatalf("default supervision = %q, want %q", rm.handoff.supervision, protocol.SupervisionPassive)
	}
	if line := lineContaining(view(m), "supervision"); !strings.Contains(line, "passive") {
		t.Fatalf("form missing the supervision field with its passive default (line %q):\n%s", line, view(m))
	}

	// Focus order is target -> model -> supervision, then back to target.
	m = send(m, keyTab)
	m = send(m, keyTab)
	if f := m.(rootModel).handoff.focus; f != 2 {
		t.Fatalf("focus after two tabs = %d, want 2 (supervision)", f)
	}
	if hint := m.(rootModel).handoff.hint(); !strings.Contains(hint, "arrows change supervision") {
		t.Errorf("supervision hint = %q, want it to say arrows change supervision", hint)
	}
	m = send(m, keyRight)
	if got := m.(rootModel).handoff.supervision; got != protocol.SupervisionManual {
		t.Fatalf("right on supervision selected %q, want manual", got)
	}
	m = send(m, keyRight) // none
	m = send(m, keyRight) // wraps to passive
	if got := m.(rootModel).handoff.supervision; got != protocol.SupervisionPassive {
		t.Fatalf("third right on supervision selected %q, want passive (wrap)", got)
	}
	m = send(m, keyLeft) // wraps back to none
	rm = m.(rootModel)
	if rm.handoff.supervision != protocol.SupervisionNone {
		t.Fatalf("left on supervision selected %q, want none (wrap)", rm.handoff.supervision)
	}
	if line := lineContaining(view(m), "supervision"); !strings.Contains(line, "none") {
		t.Fatalf("form does not show the selected supervision mode (line %q):\n%s", line, view(m))
	}
	if rm.handoff.targetName() != "claude" || rm.handoff.model != "opus" {
		t.Fatalf("cycling supervision disturbed target %q / model %q", rm.handoff.targetName(), rm.handoff.model)
	}

	// AMENDED for ADR-010 Amendment 4 E2, which adds `method` as a FOURTH field: the
	// wrap counts below were 3 and are now 4. Supervision is still the third field and
	// everything else this test asserts is unchanged; only the arithmetic moved.
	// TestHandoff_TabCyclesFourFields covers the new cycle in both directions.
	m = send(m, keyTab)
	if f := m.(rootModel).handoff.focus; f != 3 {
		t.Fatalf("focus after a third tab = %d, want 3 (method)", f)
	}
	m = send(m, keyTab)
	if f := m.(rootModel).handoff.focus; f != 0 {
		t.Fatalf("focus after a fourth tab = %d, want 0 (wraps over four fields)", f)
	}
	m = send(m, keyDown)
	m = send(m, keyDown)
	if f := m.(rootModel).handoff.focus; f != 2 {
		t.Fatalf("focus after two downs = %d, want 2", f)
	}
	m = send(m, keyDown)
	m = send(m, keyDown)
	if f := m.(rootModel).handoff.focus; f != 0 {
		t.Fatalf("focus after four downs = %d, want 0", f)
	}
	// Up moves backward like the launch form: from target it wraps to the last field.
	for i, want := range []int{3, 2, 1, 0} {
		m = send(m, keyUp)
		if f := m.(rootModel).handoff.focus; f != want {
			t.Fatalf("focus after %d up(s) = %d, want %d", i+1, f, want)
		}
	}

	// A detection refresh keeps the chosen mode, as it keeps target and model.
	m = send(m, detectMsg{gen: rm.detectGen, agents: handoffAgents()()})
	if got := m.(rootModel).handoff.supervision; got != protocol.SupervisionNone {
		t.Fatalf("detection refresh reset supervision to %q, want none", got)
	}
}

func TestHandoff_SubmitPassesSupervisionModeIntoPrompt(t *testing.T) {
	for steps, mode := range supervisionModes {
		t.Run(mode, func(t *testing.T) {
			f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute))
			m := newModel(t, f, handoffAgents())
			m = send(m, detectMsg{agents: handoffAgents()()})
			m = send(m, keyH)
			m = send(m, keyTab)
			m = send(m, keyTab)
			for i := 0; i < steps; i++ {
				m = send(m, keyRight)
			}
			_, cmd := m.Update(keyEnter)
			execCmd(cmd)
			calls := f.sendInputCalls()
			if len(calls) != 1 {
				t.Fatalf("SendInput calls = %d, want 1: %+v", len(calls), calls)
			}
			want := "--supervision '" + mode + "'"
			if !strings.Contains(calls[0].req.Text, want) {
				t.Errorf("submitted prompt missing %q:\n%s", want, calls[0].req.Text)
			}
			if !strings.Contains(calls[0].req.Text, "swarm handoff --cli 'claude'") {
				t.Errorf("submitted prompt lost the selected target:\n%s", calls[0].req.Text)
			}
		})
	}
}
