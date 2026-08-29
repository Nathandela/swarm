// Failing-first contract for the hands-off handoff method in the handoff form
// (ADR-010 Amendment 4, clauses E1-E7). The rule the whole file is written
// against is E2's: status SUGGESTS the method and never DECIDES it.
package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// sLost is a session whose supervisor lost track of the process: not running, but
// it never exited cleanly either. It is the fourth row `h` must now open the form
// on (E2), alongside permission-blocked, busy and ended.
func sLost(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView {
	return mkSession(id, agent, cwd, status.GroupCompleted,
		status.Status{Process: status.ProcessLost, Turn: status.TurnIdle, Interaction: status.InteractionUnknown},
		summary, ago)
}

// handsOffClient is the suite's fake plus the Capabilities() accessor the
// production *protocol.Client carries. The TUI discovers it by type assertion,
// exactly as `swarm reattach` discovers external-resume (cmd/swarm/reattach.go).
type handsOffClient struct {
	*fakeClient
	caps []string
}

func (c *handsOffClient) Capabilities() []string { return c.caps }

// newHandsOffClient is a daemon that DID negotiate the capability. Construct the
// struct directly, with no caps, to model an older daemon.
func newHandsOffClient(sessions ...protocol.SessionView) *handsOffClient {
	return &handsOffClient{fakeClient: newFakeClient(sessions...), caps: []string{protocol.CapHandsOffHandoff}}
}

// openForm presses `h` on the selected row with agent detection already settled.
func openForm(m tea.Model) tea.Model {
	m = send(m, detectMsg{agents: handoffAgents()()})
	return send(m, keyH)
}

// selectMethod tabs to the method field (the fourth) and cycles it to want.
func selectMethod(t *testing.T, m tea.Model, want string) tea.Model {
	t.Helper()
	for i := 0; i < len(handoffMethods)+3 && m.(rootModel).handoff.focus != 3; i++ {
		m = send(m, keyTab) // target -> model -> supervision -> method
	}
	if got := m.(rootModel).handoff.focus; got != 3 {
		t.Fatalf("tabbing never reached the method field (focus = %d)", got)
	}
	for i := 0; i <= len(handoffMethods); i++ {
		if m.(rootModel).handoff.method == want {
			return m
		}
		m = send(m, keyRight)
	}
	t.Fatalf("method never cycled to %q", want)
	return m
}

// E2: the default follows B3's predicate -- supervised where it holds, hands-off
// where it does not -- and hands-off stays selectable on EVERY row, including a
// healthy idle one, because the human may know something the roster cannot see (a
// rate-limited claude session is byte-identical on the wire to a healthy idle one).
func TestHandsOff_DefaultMethodFollowsEligibilityAndStaysSelectable(t *testing.T) {
	cases := []struct {
		name   string
		source protocol.SessionView
		want   string
	}{
		{"prompt", sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute), handoffMethodSupervised},
		{"healthy-idle", sReview("endpoint/source", "codex", "/repo", "review", time.Minute), handoffMethodSupervised},
		{"permission", sNeedsInput("endpoint/source", "codex", "/repo", "allow?", time.Minute), handoffMethodHandsOff},
		{"working", sWorking("endpoint/source", "codex", "/repo", "busy", time.Minute), handoffMethodHandsOff},
		{"completed", sCompleted("endpoint/source", "codex", "/repo", "done", time.Minute), handoffMethodHandsOff},
		{"lost", sLost("endpoint/source", "codex", "/repo", "lost", time.Minute), handoffMethodHandsOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := openForm(newModel(t, newHandsOffClient(tc.source), handoffAgents()))
			if got := m.(rootModel).handoff.method; got != tc.want {
				t.Fatalf("default method = %q, want %q", got, tc.want)
			}
			// The method is DISPLAYED: pressing Enter may never perform a branch the
			// human did not see (E2).
			if line := lineContaining(view(m), "method"); !strings.Contains(line, tc.want) {
				t.Fatalf("form does not show the frozen method (line %q):\n%s", line, view(m))
			}
			// Selectable on every row, in both directions.
			m2 := selectMethod(t, m, handoffMethodHandsOff)
			if got := m2.(rootModel).handoff.method; got != handoffMethodHandsOff {
				t.Fatalf("hands-off unreachable on this row: method = %q", got)
			}
			m2 = selectMethod(t, m2, handoffMethodSupervised)
			if got := m2.(rootModel).handoff.method; got != handoffMethodSupervised {
				t.Fatalf("supervised unreachable on this row: method = %q", got)
			}
		})
	}
}

// E2: the method is FROZEN when the form opens. A roster event arriving while the
// form is open must never change it -- otherwise pressing Enter performs a branch
// the human did not see. Only the human changes the method.
func TestHandsOff_MethodIsFrozenWhileTheFormIsOpen(t *testing.T) {
	t.Run("healthy source degrades", func(t *testing.T) {
		f := newHandsOffClient(sReview("endpoint/source", "codex", "/repo", "review", time.Minute))
		m := openForm(newModel(t, f, handoffAgents()))
		if got := m.(rootModel).handoff.method; got != handoffMethodSupervised {
			t.Fatalf("default method = %q, want supervised", got)
		}
		m = send(m, eventMsg{ev: protocol.Event{Session: sCompleted("endpoint/source", "codex", "/repo", "done", time.Minute)}})
		if got := m.(rootModel).handoff.method; got != handoffMethodSupervised {
			t.Fatalf("a roster event changed the frozen method to %q", got)
		}
	})
	t.Run("dead source recovers", func(t *testing.T) {
		f := newHandsOffClient(sCompleted("endpoint/source", "codex", "/repo", "done", time.Minute))
		m := openForm(newModel(t, f, handoffAgents()))
		if got := m.(rootModel).handoff.method; got != handoffMethodHandsOff {
			t.Fatalf("default method = %q, want hands-off", got)
		}
		m = send(m, eventMsg{ev: protocol.Event{Session: sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute)}})
		if got := m.(rootModel).handoff.method; got != handoffMethodHandsOff {
			t.Fatalf("a roster event changed the frozen method to %q", got)
		}
		// A detection refresh keeps it too, as it keeps target and model.
		m = send(m, detectMsg{gen: m.(rootModel).detectGen, agents: handoffAgents()()})
		if got := m.(rootModel).handoff.method; got != handoffMethodHandsOff {
			t.Fatalf("a detection refresh changed the frozen method to %q", got)
		}
	})
}

// E1/E3: a hands-off submit issues exactly ONE Launch and ZERO SendInput. The
// source is never signalled. The launch carries the NAMESPACED source id in
// handoff_from (protocol.md; the daemon converts it to the local id for
// spawned_from) and the form's chosen model, and it carries NO client-side
// lineage -- the daemon stamps spawned_from, spawn_intent and supervision.
func TestHandsOff_SubmitIssuesOneLaunchAndNoSendInput(t *testing.T) {
	// A same-CLI handoff so no cross-vendor confirmation intervenes (E4), on a
	// HEALTHY idle row so this also proves hands-off is not gated on status.
	f := newHandsOffClient(sPrompt("endpoint/source", "claude", "/repo", "q", time.Minute))
	m := newModel(t, f, handoffAgents())
	m = selectMethod(t, openForm(m), handoffMethodHandsOff)

	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)
	if calls := f.sendInputCalls(); len(calls) != 0 {
		t.Fatalf("hands-off submit signalled the source: %+v", calls)
	}
	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("Launch calls = %d, want exactly 1: %+v", len(reqs), reqs)
	}
	req := reqs[0]
	if req.Options[protocol.OptionHandoffFrom] != "endpoint/source" {
		t.Errorf("%s = %q, want the NAMESPACED source id", protocol.OptionHandoffFrom, req.Options[protocol.OptionHandoffFrom])
	}
	if req.Agent != "claude" {
		t.Errorf("launch agent = %q, want the form's target claude", req.Agent)
	}
	if req.Options["model"] != "opus" {
		t.Errorf("launch model = %q, want the form's chosen opus", req.Options["model"])
	}
	if req.Cwd != "/repo" {
		t.Errorf("launch cwd = %q, want the source's cwd", req.Cwd)
	}
	if req.Cols < 1 || req.Rows < 1 {
		t.Errorf("launch dims = %dx%d, want a sized terminal", req.Cols, req.Rows)
	}
	// The client's whole new authority is naming the source (E1). Lineage is the
	// daemon's to stamp, and supervision is left EMPTY rather than none (E3).
	if req.SpawnedFrom != "" || req.SpawnIntent != "" || req.Supervision != "" {
		t.Errorf("client stamped lineage: spawned_from=%q spawn_intent=%q supervision=%q",
			req.SpawnedFrom, req.SpawnIntent, req.Supervision)
	}
	if req.Options[protocol.OptionResumeFrom] != "" || req.Options[protocol.OptionResumeConversationID] != "" {
		t.Errorf("hands-off launch also carried a resume key: %+v", req.Options)
	}
	if rm := m2.(rootModel); rm.screen == screenHandoff {
		t.Error("form stayed open after a successful hands-off submit")
	}
}

// An empty model means "the agent's own default" and must NOT be sent as an empty
// --model value. An earlier draft dropped the model entirely and silently
// discarded the user's selection; this pins both halves.
func TestHandsOff_EmptyModelIsOmittedFromOptions(t *testing.T) {
	noDefault := func() []AgentInfo {
		return []AgentInfo{{Name: "claude", Installed: true, InRange: true, Options: []adapter.OptionSpec{
			{Key: "model", Label: "Model", Type: "string"},
		}}}
	}
	f := newHandsOffClient(sPrompt("endpoint/source", "claude", "/repo", "q", time.Minute))
	m := newModel(t, f, noDefault)
	m = send(m, detectMsg{agents: noDefault()})
	m = selectMethod(t, send(m, keyH), handoffMethodHandsOff)
	if got := m.(rootModel).handoff.model.text; got != "" {
		t.Fatalf("fixture model = %q, want empty", got)
	}
	_, cmd := m.Update(keyEnter)
	execCmd(cmd)
	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("Launch calls = %d, want exactly 1: %+v", len(reqs), reqs)
	}
	if _, ok := reqs[0].Options["model"]; ok {
		t.Errorf("empty model sent as an option value: %+v", reqs[0].Options)
	}
	if reqs[0].Options[protocol.OptionHandoffFrom] != "endpoint/source" {
		t.Errorf("empty model lost the handoff source: %+v", reqs[0].Options)
	}
}

// E7: every refusal in this flow is NAMED and launches nothing -- no refusal may
// degrade to a bare, context-free launch.
func TestHandsOff_RefusalsLaunchNothing(t *testing.T) {
	t.Run("source left the roster", func(t *testing.T) {
		f := newHandsOffClient(sPrompt("endpoint/source", "claude", "/repo", "q", time.Minute))
		m := newModel(t, f, handoffAgents())
		m = selectMethod(t, openForm(m), handoffMethodHandsOff)
		m = send(m, deleteDoneMsg{id: "endpoint/source"})
		m2, cmd := m.Update(keyEnter)
		execCmd(cmd)
		assertNothingIssued(t, f.fakeClient)
		if rm := m2.(rootModel); rm.screen != screenHandoff || rm.handoff.errMsg == "" {
			t.Fatalf("missing-source refusal not shown in form: %+v", rm.handoff)
		}
	})
	t.Run("no usable target", func(t *testing.T) {
		detect := func() []AgentInfo {
			return []AgentInfo{{Name: "claude", Installed: false, InRange: false, Reason: "not installed"}}
		}
		f := newHandsOffClient(sCompleted("endpoint/source", "claude", "/repo", "done", time.Minute))
		m := newModel(t, f, detect)
		m = send(m, detectMsg{agents: detect()})
		m = send(m, keyH)
		if got := m.(rootModel).handoff.method; got != handoffMethodHandsOff {
			t.Fatalf("default method = %q, want hands-off on an ended row", got)
		}
		m2, cmd := m.Update(keyEnter)
		execCmd(cmd)
		assertNothingIssued(t, f.fakeClient)
		if rm := m2.(rootModel); rm.screen != screenHandoff || !strings.Contains(rm.handoff.errMsg, "no installed") {
			t.Fatalf("no-target refusal = screen %v error %q", rm.screen, rm.handoff.errMsg)
		}
	})
}

// protocol.md: the absence of `hands-off-handoff` is a compatibility boundary, not
// a best-effort downgrade. An older daemon does not know the option key and would
// silently ignore it, launching a context-free agent into the user's checkout. The
// client refuses VISIBLY instead. Fail-closed: a client that cannot even be asked
// for its negotiated capabilities is treated as an older daemon.
func TestHandsOff_CapabilityNotNegotiatedRefusesVisibly(t *testing.T) {
	source := sCompleted("endpoint/source", "codex", "/repo", "done", time.Minute)
	t.Run("negotiated nothing", func(t *testing.T) {
		f := &handsOffClient{fakeClient: newFakeClient(source)}
		m := newModel(t, f, handoffAgents())
		m = selectMethod(t, openForm(m), handoffMethodHandsOff)
		m2, cmd := m.Update(keyEnter)
		execCmd(cmd)
		assertNothingIssued(t, f.fakeClient)
		rm := m2.(rootModel)
		// The client now negotiates the capability at hello (cmd/swarm/main.go
		// tuiCaps), so a refusal here means a genuinely OLDER DAEMON, not a
		// misconfigured client. Say that, and say the supervised method still works --
		// the user is mid-form and the other method is one field away.
		for _, want := range []string{"too old", "hands-off", "supervised"} {
			if !strings.Contains(rm.handoff.errMsg, want) {
				t.Errorf("capability refusal %q does not mention %q", rm.handoff.errMsg, want)
			}
		}
		if rm.screen != screenHandoff {
			t.Fatalf("capability refusal left the form: screen = %v", rm.screen)
		}
		// The capability refusal precedes the cross-CLI disclosure confirmation: never
		// ask the owner to approve a disclosure that cannot happen (source codex,
		// target claude here).
		if rm.handoff.confirmPending() {
			t.Error("an unsupported daemon still asked for the cross-CLI confirmation")
		}
		if !strings.Contains(strings.ToLower(view(m2)), "hands-off") {
			t.Fatalf("capability refusal is not visible:\n%s", view(m2))
		}
	})
	t.Run("client without the accessor", func(t *testing.T) {
		f := newFakeClient(source)
		m := newModel(t, f, handoffAgents())
		m = selectMethod(t, openForm(m), handoffMethodHandsOff)
		m2, cmd := m.Update(keyEnter)
		execCmd(cmd)
		assertNothingIssued(t, f)
		if rm := m2.(rootModel); rm.screen != screenHandoff || rm.handoff.errMsg == "" {
			t.Fatalf("fail-closed refusal missing: %+v", rm.handoff)
		}
	})
	t.Run("supervised is unaffected", func(t *testing.T) {
		f := newFakeClient(sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute))
		m := newModel(t, f, handoffAgents())
		m = openForm(m)
		_, cmd := m.Update(keyEnter)
		execCmd(cmd)
		if calls := f.sendInputCalls(); len(calls) != 1 {
			t.Fatalf("supervised submit blocked by the hands-off capability: %+v", calls)
		}
	})
}

// E4: where the target CLI differs from the source's, submit takes an explicit
// confirmation naming BOTH providers -- launching the successor is what sends one
// vendor's raw transcript (pasted credentials, environment dumps, proprietary
// source) to a second model provider. A same-CLI handoff takes none: the content
// reaches nobody new. The rule is target CLI != source CLI, deliberately not a
// CLI-to-vendor map: opencode and agy route to different providers depending on
// the chosen model, so the CLI name is not a reliable vendor boundary and the
// conservative rule is the honest one.
func TestHandsOff_CrossCLIConfirmation(t *testing.T) {
	t.Run("same CLI takes no confirmation", func(t *testing.T) {
		f := newHandsOffClient(sPrompt("endpoint/source", "claude", "/repo", "q", time.Minute))
		m := newModel(t, f, handoffAgents())
		m = selectMethod(t, openForm(m), handoffMethodHandsOff)
		m2, cmd := m.Update(keyEnter)
		execCmd(cmd)
		if m2.(rootModel).handoff.confirmPending() {
			t.Fatal("a claude-to-claude handoff asked for a cross-CLI confirmation")
		}
		if reqs := f.launchReqs(); len(reqs) != 1 {
			t.Fatalf("Launch calls = %d, want 1: %+v", len(reqs), reqs)
		}
	})
	t.Run("cross CLI confirms then launches", func(t *testing.T) {
		f := newHandsOffClient(sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute))
		m := newModel(t, f, handoffAgents())
		m = selectMethod(t, openForm(m), handoffMethodHandsOff) // target claude, source codex
		m2, cmd := m.Update(keyEnter)
		execCmd(cmd)
		rm := m2.(rootModel)
		if !rm.handoff.confirmPending() {
			t.Fatal("a codex-to-claude handoff did not take a confirmation")
		}
		assertNothingIssued(t, f.fakeClient)
		got := strings.ToLower(view(m2))
		for _, want := range []string{"codex", "claude", "transcript"} {
			if !strings.Contains(got, want) {
				t.Errorf("confirmation does not name %q:\n%s", want, got)
			}
		}
		m3, cmd := m2.Update(keyRune('y'))
		execCmd(cmd)
		if reqs := f.launchReqs(); len(reqs) != 1 {
			t.Fatalf("confirmed Launch calls = %d, want 1: %+v", len(reqs), reqs)
		}
		if calls := f.sendInputCalls(); len(calls) != 0 {
			t.Fatalf("confirmed hands-off signalled the source: %+v", calls)
		}
		if m3.(rootModel).screen == screenHandoff {
			t.Error("form stayed open after a confirmed hands-off submit")
		}
	})
	t.Run("cancelling launches nothing", func(t *testing.T) {
		for _, cancel := range []tea.KeyPressMsg{keyRune('n'), keyEsc} {
			f := newHandsOffClient(sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute))
			m := newModel(t, f, handoffAgents())
			m = selectMethod(t, openForm(m), handoffMethodHandsOff)
			m = send(m, keyEnter)
			if !m.(rootModel).handoff.confirmPending() {
				t.Fatal("cross-CLI submit did not take a confirmation")
			}
			m2, cmd := m.Update(cancel)
			execCmd(cmd)
			assertNothingIssued(t, f.fakeClient)
			if rm := m2.(rootModel); rm.handoff.confirmPending() {
				t.Errorf("%v left the confirmation open", cancel)
			}
		}
	})
}

// E6: the running-source warning. It is INFORMATIONAL, not blocking -- the source
// is left alive on purpose, so the mitigation is honesty, not enforcement. It is a
// different thing from the cross-CLI confirmation and must not be merged with it.
func TestHandsOff_RunningSourceWarnsWithoutBlocking(t *testing.T) {
	f := newHandsOffClient(sPrompt("endpoint/source", "claude", "/repo", "q", time.Minute))
	m := newModel(t, f, handoffAgents())
	m = selectMethod(t, openForm(m), handoffMethodHandsOff)
	got := strings.ToLower(view(m))
	if !strings.Contains(got, "may still be running") || !strings.Contains(got, "git status") {
		t.Fatalf("running-source warning missing from the form:\n%s", got)
	}
	// Informational: Enter still launches, with no confirmation of its own.
	_, cmd := m.Update(keyEnter)
	execCmd(cmd)
	if reqs := f.launchReqs(); len(reqs) != 1 {
		t.Fatalf("the warning blocked the launch: %+v", reqs)
	}

	// An ended source gets no such warning: it is not editing anything.
	f2 := newHandsOffClient(sCompleted("endpoint/source", "claude", "/repo", "done", time.Minute))
	m2 := newModel(t, f2, handoffAgents())
	m2 = openForm(m2)
	if got := strings.ToLower(view(m2)); strings.Contains(got, "may still be running") {
		t.Fatalf("ended source warned about a live writer:\n%s", got)
	}
}

// The method is the form's FOURTH field, so focus wraps over four, not three --
// in BOTH directions. The backward step was (focus+2)%3 and becomes (focus+3)%4,
// which is exactly the kind of arithmetic that goes wrong silently.
func TestHandoff_TabCyclesFourFields(t *testing.T) {
	m := newModel(t, newHandsOffClient(sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute)), handoffAgents())
	m = openForm(m)
	for i, want := range []int{1, 2, 3, 0} {
		m = send(m, keyTab)
		if f := m.(rootModel).handoff.focus; f != want {
			t.Fatalf("focus after %d tab(s) = %d, want %d", i+1, f, want)
		}
	}
	for i, want := range []int{1, 2, 3, 0} {
		m = send(m, keyDown)
		if f := m.(rootModel).handoff.focus; f != want {
			t.Fatalf("focus after %d down(s) = %d, want %d", i+1, f, want)
		}
	}
	for i, want := range []int{3, 2, 1, 0} {
		m = send(m, keyUp)
		if f := m.(rootModel).handoff.focus; f != want {
			t.Fatalf("focus after %d up(s) = %d, want %d", i+1, f, want)
		}
	}
	for i := 0; i < 3; i++ {
		m = send(m, keyTab)
	}
	if hint := m.(rootModel).handoff.hint(); !strings.Contains(hint, "method") {
		t.Errorf("method hint = %q, want it to say the arrows change the method", hint)
	}
}

// assertNothingIssued is the E7 invariant: a refusal writes nothing to the source
// and launches nothing.
func assertNothingIssued(t *testing.T, f *fakeClient) {
	t.Helper()
	if reqs := f.launchReqs(); len(reqs) != 0 {
		t.Fatalf("refusal launched %d session(s): %+v", len(reqs), reqs)
	}
	if calls := f.sendInputCalls(); len(calls) != 0 {
		t.Fatalf("refusal signalled the source: %+v", calls)
	}
}

// TestHandsOff_ConfirmationDoesNotAuthorizeAMovingTarget closes a finding from adversarial
// review: the E4 disclosure confirmation used to be a bare boolean, so it authorized
// "a launch" rather than "THIS launch".
//
// The concrete sequence, which needs no adversary -- an agent CLI merely has to stop being
// detected while the question is on screen:
//
//  1. source is codex, target is claude, so the form asks the human to approve showing a
//     codex transcript to claude;
//  2. a detectMsg arrives saying claude is no longer usable;
//  3. refreshAgents silently reselects the next usable CLI -- codex -- and resets the model;
//  4. the pending confirmation is still true, so `y` launches CODEX.
//
// The human approved a cross-vendor disclosure to claude and the machine would have
// launched a different target entirely -- one that, being the same CLI as the source, would
// not even have required a confirmation. A disclosure decision has to name what it
// authorizes, so the confirmation now freezes the target and model it was asked about and
// is cancelled outright if either moves underneath it.
func TestHandsOff_ConfirmationDoesNotAuthorizeAMovingTarget(t *testing.T) {
	onlyCodex := func() []AgentInfo {
		all := handoffAgents()()
		out := make([]AgentInfo, 0, len(all))
		for _, a := range all {
			if a.Name == "claude" {
				a.Installed = false // claude just stopped being usable
			}
			out = append(out, a)
		}
		return out
	}

	f := newHandsOffClient(sPrompt("endpoint/source", "codex", "/repo", "q", time.Minute))
	m := newModel(t, f, handoffAgents())
	m = selectMethod(t, openForm(m), handoffMethodHandsOff) // target claude, source codex

	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)
	if !m2.(rootModel).handoff.confirmPending() {
		t.Fatal("a codex-to-claude handoff did not take a confirmation")
	}

	// The target moves while the question is on screen.
	m3 := send(m2, detectMsg{gen: m2.(rootModel).detectGen, agents: onlyCodex()})

	m4, cmd := m3.Update(keyRune('y'))
	execCmd(cmd)

	if reqs := f.launchReqs(); len(reqs) != 0 {
		t.Fatalf("y launched %d session(s) after the confirmed target changed: %+v\n"+
			"the human approved a disclosure to claude; this would launch something else", len(reqs), reqs)
	}
	if m4.(rootModel).handoff.confirmPending() {
		t.Error("the stale confirmation is still pending; it must be cancelled when its target moves")
	}
	if got := strings.ToLower(view(m4)); !strings.Contains(got, "changed") && !strings.Contains(got, "cancel") {
		t.Errorf("the human is not told why the confirmation went away:\n%s", got)
	}
}
