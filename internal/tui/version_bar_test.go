package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nathandela/swarm/internal/version"
)

// The build version rides the right end of the bottom bar, on the same row as the
// keyboard shortcuts (bead agents-tracker-2c50): what is installed is visible without
// leaving the board, bare ("0.13.3"). Under `go test` it is version.Version's default.

func bottomRow(t *testing.T, m tea.Model) string {
	t.Helper()
	lines := strings.Split(stripANSI(m.View().Content), "\n")
	return lines[len(lines)-1]
}

func TestBottomBar_ShowsTheBuildVersionAtTheRightEdge(t *testing.T) {
	m := newModel(t, pinBoard(), detectMixed())
	row := bottomRow(t, m)
	want := version.Version
	if !strings.HasSuffix(row, want) {
		t.Fatalf("bottom row %q does not end with %q", row, want)
	}
	if !strings.HasPrefix(row, "  ↑↓ move") {
		t.Fatalf("bottom row %q lost the shortcuts", row)
	}
	if got := lipgloss.Width(row); got != testCols {
		t.Fatalf("bottom row is %d cells wide, want the version flush at column %d", got, testCols)
	}
}

func TestBottomBar_KeepsTheVersionThroughAConfirmPrompt(t *testing.T) {
	m := newModel(t, pinBoard(), detectMixed())
	m = send(m, keyCtrlX)
	row := bottomRow(t, m)
	if !strings.HasPrefix(row, "  y confirm") || !strings.HasSuffix(row, version.Version) {
		t.Fatalf("confirm row %q, want the confirm keys left and the version right", row)
	}
}

func TestBottomBar_DropsTheVersionWhenTheRowCannotHoldBoth(t *testing.T) {
	m := newModel(t, pinBoard(), detectMixed())
	m = send(m, tea.WindowSizeMsg{Width: 40, Height: testRows})
	row := bottomRow(t, m)
	if strings.Contains(row, version.Version) {
		t.Fatalf("a 40-column row still carries the version: %q", row)
	}
	if !strings.HasPrefix(row, "  ↑↓ move") {
		t.Fatalf("narrow row %q lost the shortcuts", row)
	}
	if got := lipgloss.Width(row); got > 40 {
		t.Fatalf("narrow row is %d cells, wider than the terminal", got)
	}
}
