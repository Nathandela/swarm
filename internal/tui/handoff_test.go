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

func TestHandoff_RefusesPermissionBusyAndEndedBeforeForm(t *testing.T) {
	cases := []struct {
		name   string
		source protocol.SessionView
		want   string
	}{
		{"permission", sNeedsInput("endpoint/permission", "codex", "/repo", "allow?", time.Minute), "permission"},
		{"working", sWorking("endpoint/working", "codex", "/repo", "busy", time.Minute), "busy"},
		{"completed", sCompleted("endpoint/done", "codex", "/repo", "done", time.Minute), "ended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(t, newFakeClient(tc.source), handoffAgents())
			m2, cmd := m.Update(keyH)
			if cmd != nil {
				t.Fatal("refusal unexpectedly returned a command")
			}
			if rm := m2.(rootModel); rm.screen != screenGeneral {
				t.Fatalf("screen = %v, want general", rm.screen)
			}
			if got := view(m2); !strings.Contains(strings.ToLower(got), tc.want) {
				t.Fatalf("view does not explain %s refusal:\n%s", tc.want, got)
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

func TestHandoff_SubmitRevalidatesSourceState(t *testing.T) {
	for _, changed := range []protocol.SessionView{
		sWorking("endpoint/source", "codex", "/repo", "busy", time.Minute),
		sNeedsInput("endpoint/source", "codex", "/repo", "allow?", time.Minute),
		sCompleted("endpoint/source", "codex", "/repo", "done", time.Minute),
	} {
		t.Run(string(changed.Status.Interaction)+string(changed.Status.Process), func(t *testing.T) {
			f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "question", time.Minute))
			m := newModel(t, f, handoffAgents())
			m = send(m, detectMsg{agents: handoffAgents()()})
			m = send(m, keyH)
			m = send(m, eventMsg{ev: protocol.Event{Session: changed}})
			m2, cmd := m.Update(keyEnter)
			if cmd != nil {
				execCmd(cmd)
			}
			if calls := f.sendInputCalls(); len(calls) != 0 {
				t.Fatalf("changed source received input: %+v", calls)
			}
			if rm := m2.(rootModel); rm.screen != screenHandoff || rm.handoff.errMsg == "" {
				t.Fatalf("state refusal not shown in form: %+v", rm.handoff)
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
