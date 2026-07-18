package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// R4.1.1 — pin the raw (ANSI-included) rendered output across the style-hoist
// refactor: every lipgloss.NewStyle() construction in general.go/launch.go's
// render paths moves to a package-level var (matching the existing
// styleTitle/styleDim/styleAgent pattern in tui.go), with no change in what is
// rendered. lipgloss v2's Style.Render does not gate on TTY detection (verified:
// it emits real SGR codes even under `go test`), so these literals were captured
// from the pre-hoist renderer and cover every inline lipgloss.NewStyle() call
// site: the per-group header/icon/status colors (general.go), the selected-row
// and confirm-prompt markers (general.go), and the launch form's focus bar,
// agent-picker highlight, error line, and auth line (launch.go). A cwd constant
// replaces the form's os.Getwd()-derived value so the pin is host-independent.
const pinCwd = "/tmp/swarm-pin-test"

// pinBoard is a dedicated fixture (not fullBoard, general_test.go): every
// session's ago is hours-scale so compactElapsed's displayed bucket ("13h" etc.)
// cannot flip during a slow run (e.g. -race, or a large suite ahead of this
// test) -- unlike a minutes-scale ago, which measurably drifted a bucket under
// -race (12m->13m) during development of this pin.
func pinBoard() *fakeClient {
	return newFakeClient(
		sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "Permission: run db migration?", 13*time.Hour),
		sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "Writing adapter fixture tests", 14*time.Hour),
		sReview("endpoint/s3", "claude", "~/Code/mcp-soml", "Turn finished, review the diff", 15*time.Hour),
		sCompleted("endpoint/s4", "gemini", "~/Code/scratch", "exit 0", 16*time.Hour),
	)
}

// pinCase is one pinned render state: name identifies which lipgloss.NewStyle()
// call site(s) it exercises (see buildPin's cases), want is the exact pre-hoist
// output.
type pinCase struct {
	name string
	want string
}

func TestStyleHoistPinnedOutput(t *testing.T) {
	cases := []pinCase{
		{"general_board", pinGeneralBoard},
		{"general_confirm", pinGeneralConfirm},
		{"general_banner", pinGeneralBanner},
		{"launch_form", pinLaunchForm},
		{"launch_error", pinLaunchError},
		{"launch_authline", pinLaunchAuthline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildPin(t, c.name)
			if got != c.want {
				t.Fatalf("style-hoist pin changed:\n--- got ---\n%q\n--- want ---\n%q", got, c.want)
			}
		})
	}
}

// buildPin renders the named pin state.
func buildPin(t *testing.T, name string) string {
	t.Helper()
	switch name {
	case "general_board":
		// general.go: per-group header (bold) x4, icon/status-token (plain) x4,
		// selected-row prefix bar (colAmber).
		m := newModel(t, pinBoard(), detectMixed())
		return m.View().Content
	case "general_confirm":
		// general.go:410 confirm-prompt marker (colNeedsInput).
		m := newModel(t, pinBoard(), detectMixed())
		m = send(m, keyCtrlX)
		return m.View().Content
	case "general_banner":
		// general.go:192 transition banner (colAmber, bold). ago is hours-scale
		// (see pinBoard) so the elapsed column's display bucket is immune to the
		// suite's own wall-clock jitter between capture and assertion.
		fw := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", 12*time.Hour))
		m := newModel(t, fw, detectMixed())
		m = send(m, eventMsg{ev: protocol.Event{Session: sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission: run tests?", 11*time.Hour)}})
		return m.View().Content
	case "launch_form":
		// launch.go:499 focused-field bar, launch.go:534 selected-agent highlight.
		m := newModel(t, pinBoard(), detectMixed())
		m = send(m, detectMsg{gen: 0, agents: detectMixed()()})
		m = send(m, keyRune('n'))
		rm := m.(rootModel)
		rm.launch.cwd = pinCwd
		return rm.View().Content
	case "launch_error":
		// launch.go:454 inline validation error line.
		m := newModel(t, pinBoard(), detectMixed())
		m = send(m, detectMsg{gen: 0, agents: detectMixed()()})
		m = send(m, keyRune('n'))
		rm := m.(rootModel)
		rm.launch.cwd = "/definitely-does-not-exist-xyz-swarm-test"
		var me tea.Model = rm
		me = send(me, keyEnter)
		return me.View().Content
	case "launch_authline":
		// launch.go:492 ANTHROPIC_API_KEY auth line.
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
		m := newModel(t, pinBoard(), detectMixed())
		m = send(m, detectMsg{gen: 0, agents: detectMixed()()})
		m = send(m, keyRune('n'))
		rm := m.(rootModel)
		rm.launch.cwd = pinCwd
		return rm.View().Content
	default:
		t.Fatalf("unknown pin case %q", name)
		return ""
	}
}

const pinGeneralBoard = "\x1b[1;38;2;255;207;95mswarm\x1b[m                                                                                            \x1b[38;2;138;138;138m3 running · 1 needs you\x1b[m\n\n  \x1b[1;38;2;255;95;95mNEEDS INPUT\x1b[m\n\x1b[38;2;255;207;95m▌\x1b[m \x1b[38;2;255;95;95m●\x1b[m \x1b[1mclaude   \x1b[m\x1b[38;2;138;138;138m~/Code/quanthome-api    \x1b[m\x1b[38;2;255;95;95mneeds input      \x1b[m\x1b[38;2;138;138;138m13h   Permission: run db migration?\x1b[m\n\n  \x1b[1;38;2;95;175;255mWORKING\x1b[m\n  \x1b[38;2;95;175;255m◐\x1b[m \x1b[1mcodex    \x1b[m\x1b[38;2;138;138;138m~/Code/agents-tracker   \x1b[m\x1b[38;2;95;175;255mworking          \x1b[m\x1b[38;2;138;138;138m14h   Writing adapter fixture tests\x1b[m\n\n  \x1b[1;38;2;95;215;95mREADY FOR REVIEW\x1b[m\n  \x1b[38;2;95;215;95m✓\x1b[m \x1b[1mclaude   \x1b[m\x1b[38;2;138;138;138m~/Code/mcp-soml         \x1b[m\x1b[38;2;95;215;95mready for review \x1b[m\x1b[38;2;138;138;138m15h   Turn finished, review the diff\x1b[m\n\n  \x1b[1;38;2;138;138;138mCOMPLETED\x1b[m\n  \x1b[38;2;138;138;138m─\x1b[m \x1b[1mgemini   \x1b[m\x1b[38;2;138;138;138m~/Code/scratch          \x1b[m\x1b[38;2;138;138;138mcompleted        \x1b[m\x1b[38;2;138;138;138m16h   exit 0\x1b[m\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n  \x1b[38;2;138;138;138m↑↓ navigate   ⏎ attach (ctrl+q returns)   n new   ctrl+x kill   esc quit\x1b[m\x1b[m"

const pinGeneralConfirm = "\x1b[1;38;2;255;207;95mswarm\x1b[m                                                                                            \x1b[38;2;138;138;138m3 running · 1 needs you\x1b[m\n\n  \x1b[1;38;2;255;95;95mNEEDS INPUT\x1b[m\n\x1b[38;2;255;95;95mkill? y/n\x1b[m \x1b[38;2;255;95;95m●\x1b[m \x1b[1mclaude   \x1b[m\x1b[38;2;138;138;138m~/Code/quanthome-api    \x1b[m\x1b[38;2;255;95;95mneeds input      \x1b[m\x1b[38;2;138;138;138m13h   Permission: run db migration?\x1b[m\n\n  \x1b[1;38;2;95;175;255mWORKING\x1b[m\n  \x1b[38;2;95;175;255m◐\x1b[m \x1b[1mcodex    \x1b[m\x1b[38;2;138;138;138m~/Code/agents-tracker   \x1b[m\x1b[38;2;95;175;255mworking          \x1b[m\x1b[38;2;138;138;138m14h   Writing adapter fixture tests\x1b[m\n\n  \x1b[1;38;2;95;215;95mREADY FOR REVIEW\x1b[m\n  \x1b[38;2;95;215;95m✓\x1b[m \x1b[1mclaude   \x1b[m\x1b[38;2;138;138;138m~/Code/mcp-soml         \x1b[m\x1b[38;2;95;215;95mready for review \x1b[m\x1b[38;2;138;138;138m15h   Turn finished, review the diff\x1b[m\n\n  \x1b[1;38;2;138;138;138mCOMPLETED\x1b[m\n  \x1b[38;2;138;138;138m─\x1b[m \x1b[1mgemini   \x1b[m\x1b[38;2;138;138;138m~/Code/scratch          \x1b[m\x1b[38;2;138;138;138mcompleted        \x1b[m\x1b[38;2;138;138;138m16h   exit 0\x1b[m\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n  \x1b[38;2;138;138;138my confirm   n cancel\x1b[m\x1b[m"

const pinGeneralBanner = "\x1b[1;38;2;255;207;95mswarm\x1b[m                                                                                            \x1b[38;2;138;138;138m1 running · 1 needs you\x1b[m\n\n  \x1b[1;38;2;255;207;95m● claude needs input\x1b[m\n\n  \x1b[1;38;2;255;95;95mNEEDS INPUT\x1b[m\n\x1b[38;2;255;207;95m▌\x1b[m \x1b[38;2;255;95;95m●\x1b[m \x1b[1mclaude   \x1b[m\x1b[38;2;138;138;138m~/Code/x                \x1b[m\x1b[38;2;255;95;95mneeds input      \x1b[m\x1b[38;2;138;138;138m11h   Permission: run tests?\x1b[m\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n  \x1b[38;2;138;138;138m↑↓ navigate   ⏎ attach (ctrl+q returns)   n new   ctrl+x kill   esc quit\x1b[m\x1b[m"

const pinLaunchForm = "\x1b[1;38;2;255;207;95mswarm\x1b[m\x1b[38;2;138;138;138m · new session\x1b[m\n\n\x1b[38;2;255;207;95m▌\x1b[m \x1b[38;2;138;138;138mdirectory   \x1b[m/tmp/swarm-pin-test█\n  \x1b[38;2;138;138;138magent       \x1b[m\x1b[38;2;138;138;138m◂ \x1b[m\x1b[38;2;255;207;95m● claude\x1b[m   \x1b[38;2;138;138;138m○ codex (upgrade codex to >= 1.2.0)\x1b[m   \x1b[38;2;138;138;138m✕ gemini (install: npm i -g @google/gemini-cli)\x1b[m\x1b[38;2;138;138;138m ▸\x1b[m\n  \x1b[38;2;138;138;138mModel       \x1b[mopus \x1b[38;2;138;138;138m▾\x1b[m\n  \x1b[38;2;138;138;138mprompt      \x1b[m\x1b[38;2;138;138;138m(optional)\x1b[m\n  \x1b[38;2;138;138;138mworktree    \x1b[m[ ] \x1b[38;2;138;138;138misolate in a git worktree\x1b[m\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n  \x1b[38;2;138;138;138mtype or paste · tab/↑↓ next · enter launch · esc cancel\x1b[m\x1b[m"

const pinLaunchError = "\x1b[1;38;2;255;207;95mswarm\x1b[m\x1b[38;2;138;138;138m · new session\x1b[m\n\n\x1b[38;2;255;207;95m▌\x1b[m \x1b[38;2;138;138;138mdirectory   \x1b[m/definitely-does-not-exist-xyz-swarm-test█\n  \x1b[38;2;138;138;138magent       \x1b[m\x1b[38;2;138;138;138m◂ \x1b[m\x1b[38;2;255;207;95m● claude\x1b[m   \x1b[38;2;138;138;138m○ codex (upgrade codex to >= 1.2.0)\x1b[m   \x1b[38;2;138;138;138m✕ gemini (install: npm i -g @google/gemini-cli)\x1b[m\x1b[38;2;138;138;138m ▸\x1b[m\n  \x1b[38;2;138;138;138mModel       \x1b[mopus \x1b[38;2;138;138;138m▾\x1b[m\n  \x1b[38;2;138;138;138mprompt      \x1b[m\x1b[38;2;138;138;138m(optional)\x1b[m\n  \x1b[38;2;138;138;138mworktree    \x1b[m[ ] \x1b[38;2;138;138;138misolate in a git worktree\x1b[m\n\n  \x1b[38;2;255;95;95mdirectory /definitely-does-not-exist-xyz-swarm-test does not exist\x1b[m\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n  \x1b[38;2;138;138;138mtype or paste · tab/↑↓ next · enter launch · esc cancel\x1b[m\x1b[m"

const pinLaunchAuthline = "\x1b[1;38;2;255;207;95mswarm\x1b[m\x1b[38;2;138;138;138m · new session\x1b[m\n\n\x1b[38;2;255;207;95m▌\x1b[m \x1b[38;2;138;138;138mdirectory   \x1b[m/tmp/swarm-pin-test█\n  \x1b[38;2;138;138;138magent       \x1b[m\x1b[38;2;138;138;138m◂ \x1b[m\x1b[38;2;255;207;95m● claude\x1b[m   \x1b[38;2;138;138;138m○ codex (upgrade codex to >= 1.2.0)\x1b[m   \x1b[38;2;138;138;138m✕ gemini (install: npm i -g @google/gemini-cli)\x1b[m\x1b[38;2;138;138;138m ▸\x1b[m\n  \x1b[38;2;138;138;138mModel       \x1b[mopus \x1b[38;2;138;138;138m▾\x1b[m\n  \x1b[38;2;138;138;138mprompt      \x1b[m\x1b[38;2;138;138;138m(optional)\x1b[m\n  \x1b[38;2;138;138;138mworktree    \x1b[m[ ] \x1b[38;2;138;138;138misolate in a git worktree\x1b[m\n\n  \x1b[38;2;255;207;95mauth: ANTHROPIC_API_KEY from env (API billing)\x1b[m\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n  \x1b[38;2;138;138;138mtype or paste · tab/↑↓ next · enter launch · esc cancel\x1b[m\x1b[m"

// R4.1.1 — alloc evidence: testing.AllocsPerRun on generalModel.view() (the whole
// board, four groups) and renderRow (one row) before/after the hoist. Logged, not
// asserted, per the plan's "record measured numbers" allowance — the exact count
// is compiler/arch-sensitive; the delta is what matters and is recorded in the
// hoist commit message.
func TestStyleHoist_AllocBudget(t *testing.T) {
	sessions := []protocol.SessionView{
		sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "Permission: run db migration?", 12*time.Minute),
		sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "Writing adapter fixture tests", 3*time.Minute),
		sReview("endpoint/s3", "claude", "~/Code/mcp-soml", "Turn finished, review the diff", 1*time.Hour),
		sCompleted("endpoint/s4", "gemini", "~/Code/scratch", "exit 0", 2*time.Hour),
	}
	gm := newGeneralModel(sessions)
	gm.width = testCols

	viewAllocs := testing.AllocsPerRun(200, func() { _ = gm.view() })
	t.Logf("generalModel.view() AllocsPerRun = %.1f", viewAllocs)

	row := sessions[0]
	rowAllocs := testing.AllocsPerRun(500, func() { _ = gm.renderRow(row, status.GroupNeedsInput, true) })
	t.Logf("generalModel.renderRow() AllocsPerRun = %.1f", rowAllocs)
}
