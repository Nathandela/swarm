package engine

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// These rows are byte-exact excerpts from Hermes Agent 0.20.6's classic CLI.
// The classifier deliberately keys on terminal chrome shapes, not on the marker
// phrases alone: any of those phrases can also occur in untrusted agent output.
const (
	hermesBusyRow     = "⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel"
	hermesApprovalNav = "↑/↓ to select, Enter to confirm"
	hermesClarifyNav  = "↑/↓ to select, Enter to lock, Tab next question"
)

func hermesSnap(cols int, lines ...string) *snapSpec {
	return &snapSpec{cols: cols, lines: lines}
}

// snapSpec keeps the table below readable while still constructing production
// vt.Snap values through the package's shared test helper.
type snapSpec struct {
	cols  int
	lines []string
}

func (s *snapSpec) snap() *vt.Snap {
	return snapFromLines(s.cols, 0, len(s.lines)-1, true, s.lines)
}

func TestHermesGridSignatureClassifiesClassicCLIChrome(t *testing.T) {
	cases := []struct {
		name        string
		snap        *snapSpec
		turn        status.Turn
		interaction status.Interaction
		conclusive  bool
	}{
		{
			name: "working",
			snap: hermesSnap(100,
				"tool output",
				"────────────────────────────────────────────────────────────────",
				hermesBusyRow,
				"────────────────────────────────────────────────────────────────"),
			turn: status.TurnActive, interaction: status.InteractionNone, conclusive: true,
		},
		{
			name: "idle composer",
			snap: hermesSnap(100,
				"⚕ swarm-test │ ctx -- │ 0s",
				"────────────────────────────────────────────────────────────────",
				"❯ Ask anything, or type / for commands…",
				"────────────────────────────────────────────────────────────────"),
			turn: status.TurnIdle, interaction: status.InteractionNone, conclusive: true,
		},
		{
			name: "profile-prefixed idle composer",
			snap: hermesSnap(100,
				"────────────────────────────────────────────────────────────────",
				"coder ❯ Ask anything…",
				"────────────────────────────────────────────────────────────────"),
			turn: status.TurnIdle, interaction: status.InteractionNone, conclusive: true,
		},
		{
			name: "approval outranks stale busy row and modal composer",
			snap: hermesSnap(100,
				hermesBusyRow,
				"╭─ Dangerous Command ─────────────────────╮",
				"│ ❯ Allow once                            │",
				"╰─────────────────────────────────────────╯",
				"  "+hermesApprovalNav,
				"──────────────────────────────────────────",
				"⚠ ❯",
				"──────────────────────────────────────────"),
			turn: status.TurnIdle, interaction: status.InteractionPermission, conclusive: true,
		},
		{
			name: "clarification outranks modal composer",
			snap: hermesSnap(100,
				"╭─ Hermes needs your input ───────────────╮",
				"│ ❯ 1. Staging                           │",
				"╰─────────────────────────────────────────╯",
				"  "+hermesClarifyNav,
				"──────────────────────────────────────────",
				"? ❯",
				"──────────────────────────────────────────"),
			turn: status.TurnIdle, interaction: status.InteractionPrompt, conclusive: true,
		},
		{
			name: "narrow idle composer",
			snap: hermesSnap(12, "────────────", "❯ Ask"),
			turn: status.TurnIdle, interaction: status.InteractionNone, conclusive: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, interaction, conclusive := evaluateGridSig(tc.snap.snap(), sigHermes)
			if turn != tc.turn || interaction != tc.interaction || conclusive != tc.conclusive {
				t.Fatalf("Hermes grid = (%s, %s, conclusive=%v), want (%s, %s, conclusive=%v)",
					turn, interaction, conclusive, tc.turn, tc.interaction, tc.conclusive)
			}
		})
	}
}

func TestHermesGridSignatureClassifiesUpstreamStateVariants(t *testing.T) {
	for _, symbol := range []string{"❯", ">", "$", "#", "›", "»", "→"} {
		t.Run("prompt symbol "+symbol, func(t *testing.T) {
			turn, interaction, conclusive := evaluateGridSig(hermesSnap(100,
				"────────────────────────────────────────────────────────────────",
				"coder "+symbol+" Ask anything…",
				"────────────────────────────────────────────────────────────────").snap(), sigHermes)
			if turn != status.TurnIdle || interaction != status.InteractionNone || !conclusive {
				t.Fatalf("custom prompt %q = (%s, %s, conclusive=%v), want (idle, none, true)",
					symbol, turn, interaction, conclusive)
			}
		})
		t.Run("working state suffix "+symbol, func(t *testing.T) {
			turn, interaction, conclusive := evaluateGridSig(hermesSnap(100,
				"────────────────────────────────────────────────────────────────",
				"⚕ "+symbol+" msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel",
				"────────────────────────────────────────────────────────────────").snap(), sigHermes)
			if turn != status.TurnActive || interaction != status.InteractionNone || !conclusive {
				t.Fatalf("working suffix %q = (%s, %s, conclusive=%v), want (active, none, true)",
					symbol, turn, interaction, conclusive)
			}
		})
	}

	cases := []struct {
		name        string
		cols        int
		lines       []string
		turn        status.Turn
		interaction status.Interaction
	}{
		{
			name: "compact working icon and placeholder", cols: 63,
			lines: []string{
				"⚕ swarm-test │ ctx -- │ 0s",
				"───────────────────────────────────────────────────────────────",
				"⚕ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel",
			},
			turn: status.TurnActive, interaction: status.InteractionNone,
		},
		{
			name: "compact working bare icon", cols: 40,
			lines: []string{
				"────────────────────────────────────────",
				"⚕",
			},
			turn: status.TurnActive, interaction: status.InteractionNone,
		},
		{
			name: "compact approval icon", cols: 63,
			lines: []string{
				hermesApprovalNav,
				"⚕ swarm-test │ ctx -- │ 0s",
				"───────────────────────────────────────────────────────────────",
				"⚠",
			},
			turn: status.TurnIdle, interaction: status.InteractionPermission,
		},
		{
			name: "compact clarification icon", cols: 63,
			lines: []string{
				hermesClarifyNav,
				"⚕ swarm-test │ ctx -- │ 0s",
				"───────────────────────────────────────────────────────────────",
				"?",
			},
			turn: status.TurnIdle, interaction: status.InteractionPrompt,
		},
		{
			name: "free text clarification", cols: 100,
			lines: []string{
				"type your answer and press Enter",
				"────────────────────────────────────────────────────────────────",
				"✎ > type your answer here and press Enter",
				"────────────────────────────────────────────────────────────────",
			},
			turn: status.TurnIdle, interaction: status.InteractionPrompt,
		},
		{
			name: "compact free text clarification", cols: 63,
			lines: []string{
				"type your answer and press Enter",
				"───────────────────────────────────────────────────────────────",
				"✎ type your answer here and press Enter",
			},
			turn: status.TurnIdle, interaction: status.InteractionPrompt,
		},
		{
			name: "clarification without lock navigation", cols: 100,
			lines: []string{
				hermesApprovalNav,
				"────────────────────────────────────────────────────────────────",
				"? $",
				"────────────────────────────────────────────────────────────────",
			},
			turn: status.TurnIdle, interaction: status.InteractionPrompt,
		},
		{
			name: "slash destructive confirmation", cols: 100,
			lines: []string{
				"type 1/2/3, or ↑/↓ to select, Enter to confirm",
				"────────────────────────────────────────────────────────────────",
				"⚠ # type 1/2/3, or use ↑/↓ then Enter",
				"────────────────────────────────────────────────────────────────",
			},
			turn: status.TurnIdle, interaction: status.InteractionPermission,
		},
		{
			name: "command running", cols: 100,
			lines: []string{
				"⠋ command in progress · input stays active; Enter queues",
				"────────────────────────────────────────────────────────────────",
				"⠙ › ⠸ Processing command...",
				"────────────────────────────────────────────────────────────────",
			},
			turn: status.TurnActive, interaction: status.InteractionNone,
		},
		{
			name: "compact command running", cols: 63,
			lines: []string{
				"⠋ command in progress · input temporarily disabled",
				"───────────────────────────────────────────────────────────────",
				"⠙ ⠸ Processing command...",
			},
			turn: status.TurnActive, interaction: status.InteractionNone,
		},
		{
			name: "compact command bare spinner", cols: 40,
			lines: []string{
				"⠋ command in progress · input temporarily disabled",
				"────────────────────────────────────────",
				"⠙",
			},
			turn: status.TurnActive, interaction: status.InteractionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, interaction, conclusive := evaluateGridSig(hermesSnap(tc.cols, tc.lines...).snap(), sigHermes)
			if turn != tc.turn || interaction != tc.interaction || !conclusive {
				t.Fatalf("Hermes variant = (%s, %s, conclusive=%v), want (%s, %s, true)",
					turn, interaction, conclusive, tc.turn, tc.interaction)
			}
		})
	}
}

func TestHermesGridSignatureRequiresTerminalComposerChrome(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{
		{
			name: "quoted complete busy chrome",
			lines: []string{
				"────────────────────────────────────────────────────────────────",
				hermesBusyRow,
				"────────────────────────────────────────────────────────────────",
			},
		},
		{
			name: "quoted complete approval chrome",
			lines: []string{
				hermesApprovalNav,
				"────────────────────────────────────────────────────────────────",
				"⚠ ❯",
				"────────────────────────────────────────────────────────────────",
			},
		},
		{
			name: "quoted complete clarification chrome",
			lines: []string{
				hermesClarifyNav,
				"────────────────────────────────────────────────────────────────",
				"? ❯",
				"────────────────────────────────────────────────────────────────",
			},
		},
		{
			name: "quoted complete free text chrome",
			lines: []string{
				"type your answer and press Enter",
				"────────────────────────────────────────────────────────────────",
				"✎ ❯ type your answer here and press Enter",
				"────────────────────────────────────────────────────────────────",
			},
		},
		{
			name: "quoted complete command chrome",
			lines: []string{
				"⠋ command in progress · input temporarily disabled",
				"────────────────────────────────────────────────────────────────",
				"⠙ ❯ ⠸ Processing command...",
				"────────────────────────────────────────────────────────────────",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := append([]string(nil), tc.lines...)
			lines = append(lines,
				"ordinary agent prose below the quoted rule",
				"────────────────────────────────────────────────────────────────",
				"❯ Ask anything…",
				"─────────",
				"────────────────────────────────────────────────────────────────")
			turn, interaction, conclusive := evaluateGridSig(hermesSnap(100, lines...).snap(), sigHermes)
			if turn != status.TurnIdle || interaction != status.InteractionNone || !conclusive {
				t.Fatalf("quoted complete chrome plus terminal idle composer = (%s, %s, conclusive=%v), want (idle, none, true)",
					turn, interaction, conclusive)
			}
		})
	}
}

func TestHermesGridSignatureWidthBoundary(t *testing.T) {
	t.Run("63 columns requires compact top rule", func(t *testing.T) {
		turn, interaction, conclusive := evaluateGridSig(hermesSnap(63,
			"───────────────────────────────────────────────────────────────",
			"❯ Ask anything…").snap(), sigHermes)
		if turn != status.TurnIdle || interaction != status.InteractionNone || !conclusive {
			t.Fatalf("63-column compact chrome = (%s, %s, %v), want idle/none/true", turn, interaction, conclusive)
		}
	})
	t.Run("64 columns requires wide lower rule", func(t *testing.T) {
		turn, interaction, conclusive := evaluateGridSig(hermesSnap(64,
			"────────────────────────────────────────────────────────────────",
			"❯ Ask anything…").snap(), sigHermes)
		if turn != status.TurnUnknown || interaction != status.InteractionUnknown || conclusive {
			t.Fatalf("64-column partial wide chrome = (%s, %s, %v), want inconclusive", turn, interaction, conclusive)
		}
	})
	t.Run("compact accepts viewport-clipped source placeholder", func(t *testing.T) {
		row := []rune("⚕ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel")
		turn, interaction, conclusive := evaluateGridSig(hermesSnap(40,
			"────────────────────────────────────────",
			string(row[:40])).snap(), sigHermes)
		if turn != status.TurnActive || interaction != status.InteractionNone || !conclusive {
			t.Fatalf("clipped compact working chrome = (%s, %s, %v), want active/none/true", turn, interaction, conclusive)
		}
	})
}

func TestHermesGridSignatureRejectsBareProseAndPartialChrome(t *testing.T) {
	cases := []struct {
		name string
		snap *snapSpec
	}{
		{"blank", hermesSnap(40, "", "")},
		{"approval phrase in prose", hermesSnap(80, "The screen says Enter to confirm when you are ready.")},
		{"clarification phrase in prose", hermesSnap(80, "I will quote Enter to lock, Tab next question here.")},
		{"busy phrase in prose", hermesSnap(80, "Documentation calls Ctrl+C cancel the active operation.")},
		{"busy phrase without Hermes status prefix", hermesSnap(80, "· Ctrl+C cancel")},
		{"composer without lower border", hermesSnap(80, "❯ Ask anything…")},
		{"border without composer", hermesSnap(80, "────────────────────────────────")},
		{"partial redraw", hermesSnap(80, "coder ❯ Ask anything…", "redrawing")},
		{"truncated narrow busy row", hermesSnap(18, "⚕ ❯ msg=interrupt", "──────────────────")},
		{"busy row damaged by partial border redraw", hermesSnap(80,
			"──❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel",
			"────────────────────────────────────────────────────────────────")},
		{"partial approval modal", hermesSnap(40, "⚠ ❯", "────────────────────────")},
		{"partial clarification modal", hermesSnap(40, "? ❯", "────────────────────────")},
		{"compact caduceus with arbitrary prose", hermesSnap(40,
			"────────────────────────────────────────", "⚕ arbitrary prose")},
		{"compact approval icon with arbitrary prose", hermesSnap(40,
			hermesApprovalNav, "────────────────────────────────────────", "⚠ arbitrary prose")},
		{"compact clarification icon with arbitrary prose", hermesSnap(40,
			hermesClarifyNav, "────────────────────────────────────────", "? arbitrary prose")},
		{"compact command spinner with arbitrary prose", hermesSnap(40,
			"⠋ command in progress · input temporarily disabled",
			"────────────────────────────────────────", "⠙ arbitrary prose")},
		{"marker above bounded region", hermesSnap(80,
			hermesBusyRow,
			"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "latest")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn, interaction, conclusive := evaluateGridSig(tc.snap.snap(), sigHermes)
			if turn != status.TurnUnknown || interaction != status.InteractionUnknown || conclusive {
				t.Fatalf("Hermes grid = (%s, %s, conclusive=%v), want inconclusive", turn, interaction, conclusive)
			}
		})
	}

	turn, interaction, conclusive := evaluateGridSig(nil, sigHermes)
	if turn != status.TurnUnknown || interaction != status.InteractionUnknown || conclusive {
		t.Fatalf("nil Hermes grid = (%s, %s, conclusive=%v), want inconclusive", turn, interaction, conclusive)
	}
}

func TestHermesGridSignatureQuotedMarkersDoNotOverrideRealIdleChrome(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{
		{
			name: "markers embedded in prose",
			lines: []string{
				"Agent output: ↑/↓ to select, Enter to confirm",
				"Agent output: ↑/↓ to select, Enter to lock, Tab next question",
				"Agent output: ⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel",
			},
		},
		{
			name:  "exact approval nav line quoted by model",
			lines: []string{hermesApprovalNav},
		},
		{
			name:  "exact clarification nav line quoted by model",
			lines: []string{hermesClarifyNav},
		},
		{
			name:  "exact busy status row quoted by model",
			lines: []string{hermesBusyRow},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := append([]string(nil), tc.lines...)
			lines = append(lines,
				"────────────────────────────────────────────────────────────────",
				"❯ Ask anything…",
				"────────────────────────────────────────────────────────────────")
			turn, interaction, conclusive := evaluateGridSig(hermesSnap(100, lines...).snap(), sigHermes)
			if turn != status.TurnIdle || interaction != status.InteractionNone || !conclusive {
				t.Fatalf("quoted markers plus idle composer = (%s, %s, conclusive=%v), want (idle, none, true)", turn, interaction, conclusive)
			}
		})
	}
}

func TestHermesInconclusiveOutputPreservesCommittedState(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	sources := []adapter.SignalSource{{
		Kind:       "heuristic",
		Descriptor: map[string]string{"grid": sigHermes},
	}}
	e.RegisterSession("hermes", "tok", 1, sources)

	e.OnOutput("hermes", hermesSnap(100,
		"────────────────────────────────────────────────────────────────",
		hermesBusyRow,
		"────────────────────────────────────────────────────────────────").snap())
	got, ok := rec.last()
	if !ok || got.s.Turn != status.TurnActive || got.s.Interaction != status.InteractionNone {
		t.Fatalf("busy setup status = %+v (emitted=%v), want active/none", got.s, ok)
	}
	settled := rec.count()

	e.OnOutput("hermes", hermesSnap(100, "partial redraw", "❯ not bordered yet").snap())
	if rec.count() != settled {
		t.Fatalf("inconclusive Hermes redraw emitted %d change(s), want none", rec.count()-settled)
	}
	if got, _ := rec.last(); got.s.Turn != status.TurnActive || got.s.Interaction != status.InteractionNone {
		t.Fatalf("inconclusive Hermes redraw changed status to %+v, want active/none preserved", got.s)
	}
}

func TestHermesGridSignatureLiveFixtureTimelines(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		terminalTurn status.Turn
		terminalInt  status.Interaction
	}{
		{"normal", "../adapter/hermes/testdata/normal.json", status.TurnIdle, status.InteractionNone},
		{"approval", "../adapter/hermes/testdata/approval.json", status.TurnIdle, status.InteractionPermission},
		{"clarification", "../adapter/hermes/testdata/clarify.json", status.TurnIdle, status.InteractionPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx, err := fixtureio.LoadFixture(tc.path)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}
			readings := replayHermesCapture(t, fx.PTYCapture, 16)

			firstActive, lastActive := -1, -1
			lastConclusive := hermesReading{}
			terminalIndex := -1
			seenInconclusiveDuringWork := false
			for i, reading := range readings {
				if reading.conclusive {
					if !validHermesFixtureReading(tc.name, reading) {
						t.Fatalf("live capture produced impossible conclusive state (%s, %s) at byte offset %d",
							reading.turn, reading.interaction, reading.offset)
					}
					if terminalIndex >= 0 && (reading.turn != tc.terminalTurn || reading.interaction != tc.terminalInt) {
						t.Fatalf("live capture left terminal state at byte offset %d: got (%s, %s), want (%s, %s)",
							reading.offset, reading.turn, reading.interaction, tc.terminalTurn, tc.terminalInt)
					}
					lastConclusive = reading
				}
				if reading.turn == status.TurnActive {
					if firstActive < 0 {
						firstActive = i
					}
					lastActive = i
				}
				if tc.terminalInt != status.InteractionNone && reading.conclusive &&
					reading.turn == tc.terminalTurn && reading.interaction == tc.terminalInt {
					if terminalIndex < 0 {
						terminalIndex = i
					}
				}
			}
			if firstActive < 0 {
				t.Fatal("live capture never classified active")
			}
			for _, reading := range readings[firstActive : lastActive+1] {
				if !reading.conclusive {
					seenInconclusiveDuringWork = true
					continue
				}
				if reading.turn == status.TurnIdle {
					t.Fatalf("live capture falsely classified idle inside active envelope at byte offset %d: interaction=%s",
						reading.offset, reading.interaction)
				}
			}
			if !seenInconclusiveDuringWork {
				t.Fatal("fixture did not exercise an inconclusive working redraw")
			}
			if tc.terminalInt == status.InteractionNone {
				for i := lastActive + 1; i < len(readings); i++ {
					if readings[i].conclusive && readings[i].turn == tc.terminalTurn && readings[i].interaction == tc.terminalInt {
						terminalIndex = i
						break
					}
				}
			}
			if terminalIndex < 0 {
				t.Fatalf("live capture never reached terminal (%s, %s)", tc.terminalTurn, tc.terminalInt)
			}
			if !lastConclusive.conclusive || lastConclusive.turn != tc.terminalTurn || lastConclusive.interaction != tc.terminalInt {
				t.Fatalf("last conclusive live frame = (%s, %s) at offset %d, want (%s, %s)",
					lastConclusive.turn, lastConclusive.interaction, lastConclusive.offset, tc.terminalTurn, tc.terminalInt)
			}
		})
	}
}

func validHermesFixtureReading(scenario string, reading hermesReading) bool {
	if !reading.conclusive {
		return true
	}
	if reading.turn == status.TurnActive && reading.interaction == status.InteractionNone {
		return true
	}
	if reading.turn != status.TurnIdle {
		return false
	}
	switch scenario {
	case "normal":
		return reading.interaction == status.InteractionNone
	case "approval":
		return reading.interaction == status.InteractionNone || reading.interaction == status.InteractionPermission
	case "clarification":
		return reading.interaction == status.InteractionNone || reading.interaction == status.InteractionPrompt
	default:
		return false
	}
}

type hermesReading struct {
	offset      int
	turn        status.Turn
	interaction status.Interaction
	conclusive  bool
}

// replayHermesCapture evaluates the complete live stream at a deliberately
// finer cadence than the 128-byte feasibility probe. Synthetic tests above
// cover every dangerous partial shape; this fixture replay proves those rules
// against the production VT emulator throughout all three characterized runs.
func replayHermesCapture(t *testing.T, capture []byte, step int) []hermesReading {
	t.Helper()
	if step < 1 {
		step = 1
	}
	emu := vt.NewEmulator(100, 30)
	defer func() { _ = emu.Close() }()

	readings := make([]hermesReading, 0, len(capture)/step+1)
	for off := 0; off < len(capture); {
		end := off + step
		if end > len(capture) {
			end = len(capture)
		}
		emu.Feed(capture[off:end])
		off = end
		turn, interaction, conclusive := evaluateGridSig(decodeSnap(t, emu), sigHermes)
		readings = append(readings, hermesReading{
			offset: off, turn: turn, interaction: interaction, conclusive: conclusive,
		})
	}
	return readings
}
