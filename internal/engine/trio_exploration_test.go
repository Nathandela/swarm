package engine

// TEMPORARY EXPLORATION TEST — cli-trio integration strategy phase (agy /
// opencode / vibe). Replays real recorded PTY captures through the vt emulator
// and classifies the grid with the production evaluateGrid heuristic at many
// byte offsets, reporting the state timeline each CLI's TUI produces. Delete
// this file before the implementation epic; it must never run in CI (it skips
// unless SWARM_TRIO_FIXDIR points at a directory of fx_<cli>.json fixtures).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
)

func TestTrioExploration_HeuristicTimeline(t *testing.T) {
	dir := os.Getenv("SWARM_TRIO_FIXDIR")
	if dir == "" {
		t.Skip("SWARM_TRIO_FIXDIR not set; temporary exploration test")
	}
	for _, cli := range []string{"agy", "opencode", "vibe"} {
		t.Run(cli, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "fx_"+cli+".json"))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fx adapter.Fixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			cap := fx.PTYCapture
			emu := vt.NewEmulator(100, 30)
			defer emu.Close()

			const steps = 120
			chunk := len(cap) / steps
			if chunk == 0 {
				chunk = 1
			}
			lastState := ""
			for off := 0; off < len(cap); off += chunk {
				end := off + chunk
				if end > len(cap) {
					end = len(cap)
				}
				emu.Feed(cap[off:end])
				snap := snapAt(t, emu)
				turn, inter := evaluateGrid(snap)
				state := fmt.Sprintf("%s/%s", turn, inter)
				if state != lastState {
					t.Logf("offset %6d/%d: %-20s last-line=%q", end, len(cap), state, lastLine(snap))
					lastState = state
				}
			}
			snap := snapAt(t, emu)
			turn, inter := evaluateGrid(snap)
			t.Logf("FINAL: %s/%s", turn, inter)
			t.Logf("final grid (non-blank lines):")
			for _, l := range gridLines(snap) {
				t.Logf("  |%s", l)
			}
		})
	}
}

// TestTrioExploration_IdleGrid dumps the full rendered grid just BEFORE each
// CLI's exit sequence begins — the mid-session idle steady state the heuristic
// would need to classify. Same env gate; temporary.
func TestTrioExploration_IdleGrid(t *testing.T) {
	dir := os.Getenv("SWARM_TRIO_FIXDIR")
	if dir == "" {
		t.Skip("SWARM_TRIO_FIXDIR not set; temporary exploration test")
	}
	markers := map[string][]byte{
		"agy":      []byte("Resume with -c"),
		"opencode": []byte("\x1b[?1049l"),
		"vibe":     []byte("\x1b[?1049l"),
	}
	for cli, marker := range markers {
		t.Run(cli, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, "fx_"+cli+".json"))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fx adapter.Fixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}
			cut := bytes.LastIndex(fx.PTYCapture, marker)
			if cut < 0 {
				t.Fatalf("marker %q not found in capture", marker)
			}
			cut -= 200 // back off past the exit redraw preamble
			if cut < 0 {
				cut = 0
			}
			emu := vt.NewEmulator(100, 30)
			defer emu.Close()
			emu.Feed(fx.PTYCapture[:cut])
			snap := snapAt(t, emu)
			turn, inter := evaluateGrid(snap)
			t.Logf("idle-steady-state classification: %s/%s, cursor row=%d visible=%v", turn, inter, snap.CursorY, snap.CursorVisible)
			for i, l := range gridLines(snap) {
				t.Logf("  %2d|%s", i, l)
			}
		})
	}
}

func snapAt(t *testing.T, emu *vt.Emulator) *vt.Snap {
	t.Helper()
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

func lastLine(snap *vt.Snap) string {
	_, text, ok := lastContentLine(snap)
	if !ok {
		return ""
	}
	return text
}

func gridLines(snap *vt.Snap) []string {
	var out []string
	for _, line := range snap.Lines {
		var b strings.Builder
		for _, r := range line.Runs {
			b.WriteString(r.Text)
		}
		if t := strings.TrimRight(b.String(), " "); t != "" {
			out = append(out, t)
		}
	}
	return out
}
