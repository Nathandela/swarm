package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestResponsiveColumnsKeepBoundedFieldsAndGrowIdentityFields(t *testing.T) {
	base := rowColumnsFor(120, 2)
	wide := rowColumnsFor(180, 2)
	if base.name != colName || base.cwd != colCwd || base.summary != colSummaryBaseline {
		t.Fatalf("120-column baseline = %+v; want name=%d cwd=%d summary=%d", base, colName, colCwd, colSummaryBaseline)
	}
	if wide.name <= base.name || wide.cwd <= base.cwd {
		t.Fatalf("wide identity columns = %+v; both must grow from %+v", wide, base)
	}
	if colAgent != 9 || colStatus != 17 || colElapsed != 6 {
		t.Fatalf("bounded semantic columns changed: agent=%d status=%d elapsed=%d", colAgent, colStatus, colElapsed)
	}
}

func TestResponsiveRowsNeverExceedTerminalWidth(t *testing.T) {
	s := sWorking(
		"endpoint/child",
		"opencode-with-an-impossible-suffix",
		"~/a/very/long/path/to/a/worktree/whose/text/used/to/wrap",
		"A deliberately long summary that must be clamped before the terminal wraps it onto a second line",
		time.Minute,
	)
	s.Name = "a very long discussion name that should grow on a wide terminal and clamp on a narrow one"
	// Schema-v0 sessions migrate without LastActivity. Their derived elapsed age is
	// deliberately enormous, so the fixed elapsed field must still stay bounded.
	s.LastActivity = time.Time{}
	// The marker is now `phone sent HH:mm`, three times the width of the bare noun it
	// replaced, so the width discipline is exercised against the string that actually ships.
	sentAt := time.Date(2026, 8, 26, 9, 41, 0, 0, time.Local)
	s.RemoteActivityAt = &sentAt
	s.SpawnedFrom = "parent-with-a-long-name"

	for _, width := range []int{48, 72, 92, 120, 160, 200} {
		t.Run(itoa(width), func(t *testing.T) {
			gm := generalModel{sessions: []protocol.SessionView{s}, width: width}
			for _, confirm := range []bool{false, true} {
				gm.confirm = confirm
				gm.confirmID = s.ID
				row := gm.renderRow(s, status.GroupWorking, true)
				if got := lipgloss.Width(row); got > width {
					t.Fatalf("row width = %d, terminal = %d, confirm=%v:\n%s", got, width, confirm, stripANSI(row))
				}
			}
		})
	}
}

func TestResponsiveWholeBoardNeverExceedsPracticalTerminalWidth(t *testing.T) {
	s := sWorking(
		"endpoint/session",
		"opencode",
		"~/a/very/long/path/to/a/worktree/whose/text/must/not/wrap",
		"A deliberately long summary that must remain on its own row",
		time.Minute,
	)
	s.Name = "a deliberately long discussion name that must remain on its own row"

	for _, width := range []int{36, 48, 72, 120, 160, 200} {
		t.Run(itoa(width), func(t *testing.T) {
			m := newModel(t, newFakeClient(s), detectMixed())
			m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: testRows})
			rm := m.(rootModel)
			for _, confirm := range []bool{false, true} {
				rm.general.confirm = confirm
				rm.general.confirmID = s.ID
				rm.general.bannerText = "a very long notification that also has to remain inside the board"
				rm.general.bannerExpiry = time.Now().Add(time.Hour)
				plain := stripANSI(rm.View().Content)
				for lineNo, line := range strings.Split(plain, "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Fatalf("line %d width = %d, terminal = %d, confirm=%v:\n%s", lineNo+1, got, width, confirm, line)
					}
				}
			}
		})
	}
}

func TestWorkingIndicatorAdvancesOnlyOnAnimationTick(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())
	before := lineContaining(view(m), "building")
	if !strings.Contains(before, "⠋") {
		t.Fatalf("initial Working frame must use the selected Braille icon ⠋:\n%s", before)
	}

	m = send(m, repaintMsg{})
	afterRepaint := lineContaining(view(m), "building")
	if !strings.Contains(afterRepaint, "⠋") {
		t.Fatalf("the one-second elapsed repaint must not add an animation jump:\n%s", afterRepaint)
	}

	repaintN := m.(rootModel).repaintN
	m2, cmd := m.Update(workingAnimationMsg{})
	if cmd == nil {
		t.Fatal("a Working animation tick on the general board must re-arm its 90 ms timer")
	}
	m = m2
	if got := m.(rootModel).repaintN; got != repaintN {
		t.Fatalf("Working animation changed full-repaint nonce from %d to %d; glyph ticks must stay cell-diff only", repaintN, got)
	}
	after := lineContaining(view(m), "building")
	if !strings.Contains(after, "⠙") || strings.Contains(after, "⠋") {
		t.Fatalf("one animation tick must advance Working to ⠙:\n%s", after)
	}
}

func TestWorkingAnimationIntervalIsNinetyMilliseconds(t *testing.T) {
	if workingAnimationInterval != 90*time.Millisecond {
		t.Fatalf("workingAnimationInterval = %s, want the selected 90ms cadence", workingAnimationInterval)
	}
}

func TestWorkingIndicatorUsesFullBrailleLoop(t *testing.T) {
	want := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if len(workingIconFrames) != len(want) {
		t.Fatalf("Working frame count = %d, want full %d-frame Braille loop", len(workingIconFrames), len(want))
	}
	for i, frame := range want {
		if got := groupIcon(status.GroupWorking, uint64(i)); got != frame {
			t.Fatalf("Working frame %d = %q, want %q", i, got, frame)
		}
		if width := lipgloss.Width(frame); width != 1 {
			t.Fatalf("Working frame %q width = %d, want one terminal cell", frame, width)
		}
	}
	if got := groupIcon(status.GroupWorking, uint64(len(want))); got != want[0] {
		t.Fatalf("Working loop does not wrap: got %q, want %q", got, want[0])
	}
}
