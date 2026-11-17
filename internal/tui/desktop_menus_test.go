package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

func desktopFiftySessions() []protocol.SessionView {
	sessions := make([]protocol.SessionView, 0, 50)
	for i := range 50 {
		id := "endpoint/s-" + itoa(i)
		var s protocol.SessionView
		if i%2 == 0 {
			s = sWorking(id, "claude", "/repo/p"+itoa(i%5), "working "+itoa(i), time.Duration(i+1)*time.Minute)
		} else {
			s = sCompleted(id, "codex", "/repo/p"+itoa(i%5), "exit 0", time.Duration(i+1)*time.Minute)
		}
		s.Name = "session-" + itoa(i)
		sessions = append(sessions, s)
	}
	return sessions
}

func lineIndexContaining(viewText, sub string) int {
	for i, line := range strings.Split(viewText, "\n") {
		if strings.Contains(line, sub) {
			return i
		}
	}
	return -1
}

func TestDesktopConfirmRendersInlineWithoutBoardWipeOrMetadataDump(t *testing.T) {
	sizes := [][2]int{{120, 40}, {80, 24}}
	boards := []struct {
		name     string
		sessions []protocol.SessionView
	}{
		{
			name: "4-sessions",
			sessions: []protocol.SessionView{
				sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "approval", 13*time.Hour),
				sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "working", 14*time.Hour),
				sReview("endpoint/s3", "claude", "~/Code/mcp-soml", "review", 15*time.Hour),
				sCompleted("endpoint/s4", "gemini", "~/Code/scratch", "exit 0", 16*time.Hour),
			},
		},
		{
			name:     "50-sessions",
			sessions: desktopFiftySessions(),
		},
	}

	for _, board := range boards {
		for _, size := range sizes {
			for _, targetCompleted := range []bool{false, true} {
				caseName := board.name + "/" + itoa(size[0]) + "x" + itoa(size[1])
				wantPrompt := "kill? y/n"
				if targetCompleted {
					caseName += "/completed"
					wantPrompt = "delete? y/n"
				} else {
					caseName += "/running"
				}
				t.Run(caseName, func(t *testing.T) {
					for _, cancelKey := range []tea.KeyPressMsg{keyRune('n'), keyEsc} {
						fc := newFakeClient(board.sessions...)
						m := newModel(t, fc, detectMixed())
						m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
						if targetCompleted {
							// Move selection to the first completed session.
							targetIdx := 3
							if board.name == "50-sessions" {
								targetIdx = 25
							}
							for range targetIdx {
								m = send(m, keyDown)
							}
						}
						m = send(m, keyCtrlX)
						out := view(m)

						if !strings.Contains(out, "swarm ·") {
							t.Fatalf("missing board header in:\n%s", out)
						}
						if !strings.Contains(out, "▾") {
							t.Fatalf("missing expanded section marker in:\n%s", out)
						}
						if gotCount := strings.Count(out, wantPrompt); gotCount != 1 {
							t.Fatalf("count(%q) = %d, want 1:\n%s", wantPrompt, gotCount, out)
						}
						for _, forbidden := range []string{"Session:", "Directory:", "Kill this session?", "Delete this session?"} {
							if strings.Contains(out, forbidden) {
								t.Fatalf("unexpected compact metadata %q in desktop confirm:\n%s", forbidden, out)
							}
						}
						lines := strings.Split(out, "\n")
						bottom := lines[len(lines)-1]
						if !strings.Contains(bottom, "y / ctrl+x confirm · n / esc cancel") {
							t.Fatalf("bottom bar = %q, want confirm/cancel hint", bottom)
						}

						m2, cmd := m.Update(cancelKey)
						if cmd != nil {
							t.Fatalf("cancel key %q unexpectedly returned command", cancelKey.Keystroke())
						}
						if m2.(rootModel).general.confirm {
							t.Fatalf("cancel key %q left confirm pending", cancelKey.Keystroke())
						}
					}

					for _, confirmKey := range []tea.KeyPressMsg{keyRune('y'), keyCtrlX} {
						fc := newFakeClient(board.sessions...)
						m := newModel(t, fc, detectMixed())
						m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
						if targetCompleted {
							targetIdx := 3
							if board.name == "50-sessions" {
								targetIdx = 25
							}
							for range targetIdx {
								m = send(m, keyDown)
							}
						}
						m = send(m, keyCtrlX)
						m2, cmd := m.Update(confirmKey)
						if cmd == nil {
							t.Fatalf("confirm key %q did not issue delete/kill command immediately", confirmKey.Keystroke())
						}
						execCmd(cmd)
						if m2.(rootModel).general.confirm {
							t.Fatalf("confirm key %q did not clear confirm state", confirmKey.Keystroke())
						}
						if targetCompleted {
							if len(fc.deletedIDs()) != 1 {
								t.Fatalf("deleted IDs = %v, want 1", fc.deletedIDs())
							}
						} else {
							if len(fc.killedIDs()) != 1 {
								t.Fatalf("killed IDs = %v, want 1", fc.killedIDs())
							}
						}
					}
				})
			}
		}
	}
}

func TestDesktopInlineRenameAndTagStayOnBoard(t *testing.T) {
	sizes := [][2]int{{120, 40}, {80, 24}}
	for _, size := range sizes {
		for _, mode := range []string{"rename", "tag", "more-rename"} {
			t.Run(itoa(size[0])+"x"+itoa(size[1])+"/"+mode, func(t *testing.T) {
				m := newModel(t, newFakeClient(desktopFiftySessions()...), detectMixed())
				m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				switch mode {
				case "rename":
					m = send(m, keyRune('e'))
				case "tag":
					m = send(m, keyRune('t'))
				case "more-rename":
					m = send(m, keyRune('m'))
					m = send(m, keyRune('e'))
				}
				rm := m.(rootModel)
				if !rm.general.editing {
					t.Fatalf("expected inline editing active for mode %q", mode)
				}
				out := view(m)
				if !strings.Contains(out, "swarm ·") {
					t.Fatalf("missing board header in %q:\n%s", mode, out)
				}
				if !strings.Contains(out, "▾") {
					t.Fatalf("missing section marker in %q:\n%s", mode, out)
				}
				if !strings.Contains(out, "█") {
					t.Fatalf("missing inline cursor in %q:\n%s", mode, out)
				}
				for _, forbidden := range []string{"Session:", "Rename:", "Tag:"} {
					if strings.Contains(out, forbidden) {
						t.Fatalf("unexpected sheet text %q in %q:\n%s", forbidden, mode, out)
					}
				}
			})
		}
	}
}

func TestHandoffKeepsFieldsInFixedOrderAcrossFocusAndSizes(t *testing.T) {
	sizes := [][2]int{{120, 40}, {80, 24}, {48, 24}, {36, 12}}
	fields := []string{"target", "model", "supervision", "method"}

	assertOrder := func(t *testing.T, v string) {
		t.Helper()
		lines := strings.Split(v, "\n")
		if len(lines) == 0 || !strings.Contains(lines[0], "swarm") || !strings.Contains(lines[0], "handoff") {
			t.Fatalf("top line does not show swarm · handoff:\n%s", v)
		}
		for _, forbidden := range []string{"Session:", "Directory:"} {
			if strings.Contains(v, forbidden) {
				t.Fatalf("unexpected metadata %q injected into handoff form:\n%s", forbidden, v)
			}
		}
		prev := -1
		for _, field := range fields {
			idx := lineIndexContaining(v, field)
			if idx < 0 {
				t.Fatalf("field %q missing from handoff form:\n%s", field, v)
			}
			if idx <= prev {
				t.Fatalf("field %q appeared at line %d <= previous %d (fields reordered):\n%s", field, idx, prev, v)
			}
			prev = idx
		}
	}

	for _, size := range sizes {
		t.Run(itoa(size[0])+"x"+itoa(size[1]), func(t *testing.T) {
			f := newHandsOffClient(sPrompt("endpoint/source", "codex", "/repo/project", "question", time.Minute))
			m := newModel(t, f, handoffAgents())
			m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = openForm(m)

			assertOrder(t, view(m))
			for step := 1; step <= 4; step++ {
				m = send(m, keyDown)
				assertOrder(t, view(m))
			}
			for step := 1; step <= 4; step++ {
				m = send(m, keyUp)
				assertOrder(t, view(m))
			}
			for step := 1; step <= 4; step++ {
				m = send(m, keyTab)
				assertOrder(t, view(m))
			}

			if size[0] >= 80 {
				m = selectMethod(t, m, handoffMethodHandsOff)
				m = send(m, keyEnter)
				if !m.(rootModel).handoff.confirmPending() {
					t.Fatal("expected cross-CLI confirmation pending")
				}
				v := view(m)
				assertOrder(t, v)
				compact := strings.Join(strings.Fields(v), " ")
				if !strings.Contains(compact, "cross-CLI handoff:") || !strings.Contains(compact, "y confirm, n cancel") {
					t.Fatalf("cross-CLI confirmation text incomplete or clamped:\n%s", v)
				}
			}
		})
	}
}

func TestShortSplitPaneScrollsFormsInOrderWithoutFieldTeleporting(t *testing.T) {
	multiOptionAgents := func() []AgentInfo {
		return []AgentInfo{
			{
				Name:      "codex",
				Installed: true,
				InRange:   true,
				Options: []adapter.OptionSpec{
					{Key: "model", Label: "Model", Type: "string", Suggest: []string{"gpt-5.6", "gpt-5.5"}, Default: "gpt-5.6"},
					{Key: "sandbox", Label: "Sandbox mode", Type: "choice", Choices: []string{"read-only", "workspace-write", "danger-full-access"}, Default: "workspace-write"},
					{Key: "approval", Label: "Approval policy", Type: "choice", Choices: []string{"on-request", "never"}, Default: "on-request"},
				},
			},
		}
	}
	fieldTokens := []string{
		"directory   ",
		"name        ",
		"tag         ",
		"agent       ",
		"Model       ",
		"Sandbox mode",
		"Approval po",
		"prompt      ",
		"worktree    ",
	}

	for _, size := range [][2]int{{120, 10}, {80, 10}} {
		t.Run(itoa(size[0])+"x"+itoa(size[1]), func(t *testing.T) {
			m := newModel(t, newFakeClient(), multiOptionAgents)
			m = send(m, detectMsg{agents: multiOptionAgents()})
			m = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = send(m, keyRune('n'))

			steps := len(fieldTokens)
			for step := range steps {
				v := view(m)
				lines := strings.Split(v, "\n")
				if len(lines) == 0 || !strings.Contains(lines[0], "swarm") || !strings.Contains(lines[0], "new session") {
					t.Fatalf("step %d: header missing from row 0:\n%s", step, v)
				}
				if !strings.Contains(v, "▌ ") {
					t.Fatalf("step %d: focused field indicator missing:\n%s", step, v)
				}
				prev := -1
				for _, tok := range fieldTokens {
					idx := lineIndexContaining(v, tok)
					if idx < 0 {
						continue
					}
					if idx <= prev {
						t.Fatalf("step %d: field %q teleported out of order (line %d <= %d):\n%s", step, strings.TrimSpace(tok), idx, prev, v)
					}
					prev = idx
				}
				m = send(m, keyDown)
			}
		})
	}

	t.Run("validation-error-pinned-with-focused-directory", func(t *testing.T) {
		m := newModel(t, newFakeClient(), multiOptionAgents)
		m = send(m, detectMsg{agents: multiOptionAgents()})
		m = send(m, tea.WindowSizeMsg{Width: 120, Height: 10})
		m = send(m, keyRune('n'))
		rm := m.(rootModel)
		rm.launch.errMsg = "directory is required"
		v := view(rm)
		if !strings.Contains(v, "▌ ") || !strings.Contains(v, "directory   ") {
			t.Fatalf("focused directory hidden when validation error shown:\n%s", v)
		}
		if !strings.Contains(v, "directory is required") {
			t.Fatalf("validation error hidden at height 10:\n%s", v)
		}
	})
}

func TestDesktopLoginsRestoresMarginAndTopSpacer(t *testing.T) {
	m := newModel(t, newLoginsTestClient(), detectMixed()).(rootModel)
	m.width, m.height = 120, 40
	m.screen = screenLogins
	m.logins = loginsModel{
		available: true,
		loaded:    true,
		reply: protocol.LoginsReply{
			Providers: []protocol.ProviderLoginView{{Provider: "claude", State: "native"}},
		},
	}
	v := view(m)
	lines := strings.Split(v, "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected short view:\n%s", v)
	}
	if lines[0] != "" {
		t.Fatalf("line 0 = %q, want blank top spacer", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  swarm · logins") {
		t.Fatalf("line 1 = %q, want 2-space margin prefix", lines[1])
	}
}
