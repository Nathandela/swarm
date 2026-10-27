package tui

import (
	"strings"
	"testing"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/agy"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/adapter/hermes"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/charmbracelet/x/ansi"
)

func TestResponsiveFormsFocusAndError(t *testing.T) {
	m := openLaunch(t, newFakeClient()).(rootModel)
	m.width, m.height, m.launch.width = 40, 8, 40
	m.launch.focus = m.launch.promptIndex()
	m.launch.prompt.set("draft世界")
	m.launch.errMsg = "directory is required"
	v := view(m)
	for _, want := range []string{"prompt", "draft世界", "directory is required"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
}

func TestResponsiveFormsSelectedProvider(t *testing.T) {
	m := newLaunchModel([]AgentInfo{{Name: "claude"}, {Name: "codex"}, {Name: "hermes"}, {Name: "opencode"}}, true, 40)
	m.agentIdx = 3
	if v := ansi.Strip(m.agentValue()); !strings.Contains(v, "opencode") {
		t.Fatalf("selected provider hidden: %s", v)
	}
}

func TestResponsiveFormsSchemaKeepsOptionFocus(t *testing.T) {
	agents := []AgentInfo{{Name: "claude", Options: []adapter.OptionSpec{{Key: "model", Label: "Model", Type: "string"}}}}
	m := newLaunchModel(agents, true, 40)
	m.focus = m.optionsIndex()
	m.options["model"] = newLineEditor("draft")
	agents[0].Options = append([]adapter.OptionSpec{{Key: "extra", Label: "Extra", Type: "string"}}, agents[0].Options...)
	m.refreshAgents(agents)
	if i, ok := m.optionFocus(); !ok || m.optSpecs[i].Key != "model" {
		t.Fatalf("schema lost focused model: %d", m.focus)
	}
}

func TestResponsiveFormsTooSmallBlocksMutationButAllowsCancel(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {1, 2}, {2, 3}, {31, 8}, {32, 7}} {
		m := openLaunch(t, newFakeClient()).(rootModel)
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(rootModel)
		_, cmd := m.Update(keyEnter)
		if cmd != nil {
			t.Errorf("%v initiated launch", size)
		}
		next, _ = m.Update(keyEsc)
		if next.(rootModel).screen != screenGeneral {
			t.Errorf("%v Escape trapped", size)
		}
		v := ansi.Strip(m.View().Content)
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("%v overflow %q", size, line)
			}
		}
	}
}

func TestResponsiveFormsLongPairingRequiresVisibleStop(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, tea.WindowSizeMsg{Width: 32, Height: 8})
	m = send(m, pairPendingMsg{SAS: []string{strings.Repeat("verify-code ", 30)}, DeviceName: "phone"})
	next, cmd := m.Update(keyRune('y'))
	if cmd != nil || next.(rootModel).pairing == nil {
		t.Fatal("hidden long pairing approved")
	}
	m = send(next, tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(view(m), "Allow") {
		t.Fatalf("confirmation stop not visible:\n%s", view(m))
	}
	next, cmd = m.Update(keyRune('y'))
	if cmd == nil || next.(rootModel).pairing != nil {
		t.Fatal("visible stop could not approve")
	}
}

func TestResponsiveFormsEveryInitiatingKeyIsSizeGuarded(t *testing.T) {
	c := newLoginsTestClient()
	base := newModel(t, c, detectMixed()).(rootModel)
	base.width, base.height = 31, 8
	cases := []struct {
		name   string
		screen screen
		key    tea.KeyPressMsg
	}{
		{"options save", screenOptions, keyEnter},
		{"account activate", screenLogins, keyEnter},
		{"account cancel switch", screenLogins, keyRune('c')},
		{"account remove", screenLogins, keyRune('d')},
		{"account relogin", screenLogins, keyRune('s')},
		{"handoff submit", screenHandoff, keyEnter},
		{"resume relocate", screenResume, keyEnter},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := base
			m.screen = tt.screen
			m.logins = loginsModel{available: true, loaded: true, focus: 1, reply: protocol.LoginsReply{Logins: []protocol.LoginView{{ID: "saved", Provider: "claude", Label: "work"}}}}
			next, cmd := m.Update(tt.key)
			if cmd != nil {
				t.Fatal("initiated operation below minimum")
			}
			if next.(rootModel).screen != tt.screen {
				t.Fatal("size guard discarded form")
			}
		})
	}
	for _, tag := range []bool{false, true} {
		m := base
		m.screen = screenGeneral
		m.general.editing = true
		m.general.editID = "captured"
		m.general.editTag = tag
		m.general.edit.set("draft")
		next, cmd := m.Update(keyEnter)
		if cmd != nil || !next.(rootModel).general.editing {
			t.Fatal("inline save bypassed guard")
		}
	}
	m := base
	m.general.confirm = true
	m.general.confirmID = "captured"
	for _, k := range []tea.KeyPressMsg{keyRune('y'), keyCtrlX} {
		next, cmd := m.Update(k)
		if cmd != nil || !next.(rootModel).general.confirm {
			t.Fatal("confirm bypassed guard")
		}
	}
	m.pairing = &pairingModal{deviceName: "phone", sas: []string{"code"}}
	next, cmd := m.Update(keyRune('y'))
	if cmd != nil || next.(rootModel).pairing == nil {
		t.Fatal("pairing bypassed size guard")
	}
	m.connectionLost = true
	next, cmd = m.Update(keyEsc)
	if cmd == nil || next.(rootModel).pairing != nil {
		t.Fatal("disconnected pairing cannot be denied")
	}
}

func TestResponsiveFormsLongContextKeepsNativeEditorNavigation(t *testing.T) {
	m := openLaunch(t, newFakeClient()).(rootModel)
	m.width, m.height, m.launch.width = 32, 8, 32
	m.launch.focus = m.launch.promptIndex()
	m.launch.prompt.set("draft")
	m.launch.errMsg = strings.Repeat("validation context ", 20)
	m = send(m, tea.KeyPressMsg{Code: tea.KeyHome}).(rootModel)
	if m.launch.prompt.cursor != 0 {
		t.Fatal("reading context stole native Home")
	}
	m = send(m, keyDown).(rootModel)
	if !m.launch.isWorktree() {
		t.Fatal("long validation context trapped field navigation")
	}
	_, cmd := m.Update(keyEnter)
	if cmd != nil {
		t.Fatal("hidden validation context submitted")
	}
	for i := 0; i < 30; i++ {
		m = send(m, tea.KeyPressMsg{Code: tea.KeyPgDown}).(rootModel)
	}
	if !strings.Contains(view(m), "▌ Confirm") {
		t.Fatal("confirmation stop unreachable")
	}
}

func TestResponsiveFormsGeometryAndRetainedDrafts(t *testing.T) {
	m := openLaunch(t, newFakeClient()).(rootModel)
	m.launch.focus = m.launch.promptIndex()
	m.launch.prompt.set("draft 世界 é 🐙 tail")
	m.launch.prompt.cursor = 7
	m.launch.errMsg = strings.Repeat("long validation 世界 🐙 ", 12)
	for _, size := range [][2]int{{24, 4}, {32, 8}, {36, 12}, {40, 18}, {48, 24}, {72, 12}, {90, 16}, {80, 24}, {120, 40}, {31, 8}, {32, 7}, {33, 9}, {1, 1}, {1, 2}, {2, 3}} {
		m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]}).(rootModel)
		v := view(m)
		if lines := strings.Split(v, "\n"); len(lines) != size[1] {
			t.Errorf("%v height=%d", size, len(lines))
		}
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("%v width overflow %q", size, line)
			}
		}
		if m.launch.prompt.text != "draft 世界 é 🐙 tail" || m.launch.prompt.cursor != 7 {
			t.Fatalf("%v changed draft or cursor", size)
		}
	}
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: 8}).(rootModel)
	if !strings.Contains(view(m), "prompt") || !strings.Contains(view(m), "█") {
		t.Fatal("restored form did not restore active cursor")
	}
}

func TestResponsiveFormsLongConfirmCannotRearmAfterCancel(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, tea.WindowSizeMsg{Width: 32, Height: 8})
	pending := pairPendingMsg{DeviceName: "phone", SAS: []string{strings.Repeat("verify ", 50)}}
	m = send(m, pending)
	m = send(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = send(m, keyEsc)
	m = send(m, pending)
	next, cmd := m.Update(keyRune('y'))
	if cmd != nil || next.(rootModel).pairing == nil {
		t.Fatal("reopened decision inherited old confirmation focus")
	}
}

func TestResponsiveFormsInlineTargetSurvivesRegroupAndRejectsDeletion(t *testing.T) {
	s := sWorking("endpoint/original", "claude", "~/Code/original", "working", 0)
	s.Name = "original"
	m := newModel(t, newFakeClient(s), detectMixed())
	m = send(m, keyRune('e'))
	m = send(m, tea.WindowSizeMsg{Width: 32, Height: 8})
	rm := m.(rootModel)
	rm.general.grouping = groupByRepo
	if v := view(rm); !strings.Contains(v, "original") || !strings.Contains(v, "█") {
		t.Fatalf("captured editor lost target or cursor:\n%s", v)
	}
	rm.general.remove(s.ID)
	_, cmd := rm.Update(keyEnter)
	if cmd != nil {
		t.Fatal("vanished inline target still submitted")
	}
}

func TestResponsiveFormsBusyResumeEscapeAndAcceptedResult(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed()).(rootModel)
	m.screen = screenResume
	m.resume.busy = true
	m.width, m.height = 1, 1
	next, _ := m.Update(keyEsc)
	if next.(rootModel).screen != screenGeneral {
		t.Fatal("busy resume trapped Escape")
	}
	next, _ = next.Update(resumeResultMsg{result: launchResultMsg{id: "accepted-session", agent: "claude"}})
	if next.(rootModel).screen != screenAttach {
		t.Fatal("shrinking or leaving form cancelled accepted resume completion")
	}
}

func TestResponsiveFormsAllProviderLastOptions(t *testing.T) {
	for _, a := range []adapter.Adapter{claude.New(), codex.New(), agy.New(), hermes.New(), opencode.New()} {
		t.Run(a.Name(), func(t *testing.T) {
			m := newModel(t, newFakeClient(), nil).(rootModel)
			m.screen = screenLaunch
			m.width, m.height = 32, 8
			m.launch = newLaunchModel([]AgentInfo{{Name: a.Name(), Installed: true, InRange: true, Options: a.Options()}}, true, 32)
			opts := a.Options()
			if len(opts) == 0 {
				t.Fatal("provider fixture has no options")
			}
			m.launch.focus = m.launch.optionsIndex() + len(opts) - 1
			line := formFocusedLine(m.launch.view())
			if !strings.Contains(view(m), ansi.Strip(line)) {
				t.Fatalf("last option not visible:\n%s", view(m))
			}
		})
	}
}

func TestResponsiveFormsLongHandoffExposesWarningAndDisclosure(t *testing.T) {
	m := newModel(t, newFakeClient(), nil).(rootModel)
	m.width, m.height = 32, 8
	m.screen = screenHandoff
	m.handoff = handoffModel{sourceID: "source", sourceAgent: "claude", sourceCwd: "/checkout", sourceRunning: true, method: handoffMethodHandsOff, confirmTarget: "codex", detected: true, agents: []AgentInfo{{Name: "codex", Installed: true, InRange: true}}, width: 32}
	initial := view(m)
	next, cmd := m.Update(keyRune('y'))
	if cmd != nil || !next.(rootModel).handoff.confirmPending() {
		t.Fatal("hidden disclosure approved")
	}
	sawWarning, sawDisclosure := false, false
	for i := 0; i < 50; i++ {
		v := strings.Join(strings.Fields(view(m)), " ")
		sawWarning = sawWarning || strings.Contains(v, "git status first")
		sawDisclosure = sawDisclosure || strings.Contains(v, "transcript written by")
		if !strings.Contains(view(m), "Handoff:") {
			t.Fatal("target/action unpinned during decision reading")
		}
		m = send(m, keyDown).(rootModel)
	}
	if !sawWarning || !sawDisclosure {
		t.Fatalf("unreachable required text: warning=%v disclosure=%v initial:\n%s", sawWarning, sawDisclosure, initial)
	}
	if !strings.Contains(view(m), "▌ Confirm") {
		t.Fatal("confirmation stop unreachable")
	}
}

func TestResponsiveFormsPairingRejectsModifiedApprovalKeys(t *testing.T) {
	for _, size := range [][2]int{{31, 8}, {32, 8}, {120, 40}} {
		for _, k := range []tea.KeyPressMsg{{Code: 'y', Mod: tea.ModCtrl}, {Code: 'y', Text: "y", Mod: tea.ModAlt}, {Code: tea.KeyEnter, Mod: tea.ModCtrl}, {Code: tea.KeyEnter, Mod: tea.ModAlt}} {
			m := newModel(t, newFakeClient(), nil)
			m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = send(m, pairPendingMsg{DeviceName: "phone", SAS: []string{strings.Repeat("verify ", 40)}})
			for _, atStop := range []bool{false, true} {
				if atStop {
					m = send(m, tea.KeyPressMsg{Code: tea.KeyEnd})
				}
				next, cmd := m.Update(k)
				if cmd != nil || next.(rootModel).pairing == nil {
					t.Fatalf("%v key=%s stop=%v approved", size, k.Keystroke(), atStop)
				}
			}
		}
	}
}

func TestResponsiveFormsDisconnectPreservesDeny(t *testing.T) {
	s := sWorking("endpoint/original", "claude", "~/Code/original", "working", 0)
	for _, screen := range []screen{screenGeneral, screenHandoff} {
		for _, k := range []tea.KeyPressMsg{keyRune('n'), keyEsc} {
			for _, size := range [][2]int{{31, 8}, {32, 8}} {
				m := newModel(t, newFakeClient(s), nil).(rootModel)
				m.screen = screen
				m.width, m.height = size[0], size[1]
				if screen == screenGeneral {
					m.general.confirm = true
					m.general.confirmID = s.ID
					m.general.confirmKill = true
				} else {
					m.handoff = handoffModel{sourceID: s.ID, sourceAgent: s.Agent, confirmTarget: "codex", sourceCwd: s.Cwd}
				}
				m.connectionLost = true
				next, cmd := m.Update(k)
				if cmd != nil {
					t.Fatal("deny initiated daemon operation")
				}
				rm := next.(rootModel)
				if rm.general.confirm || rm.handoff.confirmPending() {
					t.Fatalf("screen=%v size=%v key=%s cannot deny after disconnect", screen, size, k.Keystroke())
				}
			}
		}
	}
}

func TestResponsiveFormsCapturedAgentInspectable(t *testing.T) {
	s := sWorking("endpoint/original", "claude", "~/Code/original", "working", 0)
	s.Name = "named-session"
	for _, screen := range []screen{screenGeneral, screenResume, screenHandoff} {
		m := newModel(t, newFakeClient(s), nil).(rootModel)
		m.width, m.height = 32, 8
		m.screen = screen
		switch screen {
		case screenGeneral:
			m.general.confirm = true
			m.general.confirmID = s.ID
			m.general.confirmKill = true
		case screenResume:
			m.resume.source = s
			m.resume.directory.width = 32
		case screenHandoff:
			m.handoff = handoffModel{sourceID: s.ID, sourceAgent: s.Agent, sourceCwd: s.Cwd, width: 32}
		}
		seen := ""
		for i := 0; i < 30; i++ {
			seen += view(m)
			m = send(m, tea.KeyPressMsg{Code: tea.KeyPgDown}).(rootModel)
		}
		for _, want := range []string{"named-session", "Agent: claude", s.ID, s.Cwd} {
			if !strings.Contains(seen, want) {
				t.Errorf("screen=%v missing inspectable context %q", screen, want)
			}
		}
	}
}
