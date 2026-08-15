package vt

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// vt-fuzz (Wave R0): pins the fix for the unbounded CSI-parameter hangs.
// Several x/vt handlers loop over their raw, untrusted numeric parameter with
// no bound tied to the grid, so a single large parameter can spin for an
// attacker-chosen amount of wall time while FeedChecked holds e.mu:
//
//   - CHT ('I') and CBT ('Z') traverse tab stops once per unit
//     (docs/verification/r0-red/vt-fuzz-red.txt), and
//   - REP ('b') turns its count into that many full print operations, each of
//     which can wrap the line, scroll the screen and push a row into the
//     10000-line scrollback — three orders of magnitude more expensive per
//     iteration, and the reason a five-digit cap was still not enough (a
//     bounded CI fuzz run hung on a burst of `CSI 99999 b`).
//
// clampCsiParams defends against this by capping consecutive CSI parameter
// digits before the bytes ever reach the upstream parser: csiParamDigitCap
// for ordinary sequences, the relaxed csiPrivateParamDigitCap for
// private-marked ones, whose parameters are O(1) mode selectors.
//
// A C0 control byte embedded in a CSI parameter list defeats a naive version
// of both halves of that scheme, which is what
// docs/verification/r0-red/vt-c0-param-red.txt records; see
// TestClampCsiParams_ForwardedParamsAreBounded's "C0 ..." cases.

// Upstream's parameter-driven loops (REP's prints, CHT/CBT's tab-stop steps)
// run at most `parameter` iterations, so the largest value the upstream parser
// ends up ACCUMULATING from the bytes clampCsiParams forwards IS the worst-case
// operation count a single sequence can force. These are that bound, written as
// absolute numbers rather than derived from csiParamDigitCap /
// csiPrivateParamDigitCap: they are what has to hold for Feed to stay bounded,
// so raising either cap must fail this test rather than silently move the
// goalposts. The relaxed bound applies only to sequences that reach a handler
// with their private marker still attached, because those are the only ones
// upstream never iterates over.
const (
	maxForwardedParamValue        = 999
	maxForwardedPrivateParamValue = 99999
)

// forwardedSeq is one CSI or DCS as the pinned upstream parser dispatches it:
// the command word it matches a handler on (private marker included) and the
// parameter values it accumulated. This is the level the bound has to hold at.
// Scanning the emitted bytes with a regex instead would measure maximal digit
// RUNS, which is not what upstream accumulates: it concatenates the digit runs
// either side of any IgnoreAction byte (0x7F, and 0x3C-0x3F inside a parameter
// list) and any ExecuteAction byte (a C0 control) into a single parameter, so
// `CSI 9 NUL 9 NUL 9 NUL 9 b` has a maximal run of 1 and a parameter of 9999.
type forwardedSeq struct {
	kind   string
	cmd    ansi.Cmd
	params []int
}

// replayThroughUpstream feeds b to a parser configured exactly as x/vt
// configures its own (ansi.NewParser already uses parser.MaxParamsSize) and
// returns every CSI/DCS it dispatches. Driving a real parser rather than
// re-deriving the parse keeps the assertion independent of clampCsiParams'
// own bookkeeping, and covers the 8-bit CSI/DCS introducers (0x9B, 0x90) for
// free.
func replayThroughUpstream(b []byte) []forwardedSeq {
	var seqs []forwardedSeq
	collect := func(kind string, cmd ansi.Cmd, params ansi.Params) {
		s := forwardedSeq{kind: kind, cmd: cmd}
		params.ForEach(0, func(_, param int, _ bool) { s.params = append(s.params, param) })
		seqs = append(seqs, s)
	}
	p := ansi.NewParser()
	p.SetHandler(ansi.Handler{
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) { collect("CSI", cmd, params) },
		HandleDcs: func(cmd ansi.Cmd, params ansi.Params, _ []byte) { collect("DCS", cmd, params) },
	})
	p.Parse(b)
	return seqs
}

// TestClampCsiParams_ForwardedParamsAreBounded is the by-construction bound:
// whatever hostile stream is fed, every parameter the upstream parser
// accumulates from the bytes clampCsiParams forwards stays within the
// operation-count bounds above, and the number of parameters per sequence
// stays within parser.MaxParamsSize. It is an exact, deterministic assertion
// on what upstream receives — no timer, no wall-clock sensitivity.
func TestClampCsiParams_ForwardedParamsAreBounded(t *testing.T) {
	hostile := []struct{ name, in string }{
		{"REP burst that hung CI", "X" + strings.Repeat("\x1b[99999b", 64)},
		{"one absurd REP count", "\x1b[" + strings.Repeat("9", 40) + "b"},
		{"CHT, the earlier hang", "\x1b[" + strings.Repeat("9", 40) + "I"},
		{"DEL padding inside a parameter", "\x1b[12\x7f00000000000I"},
		{"many parameters", "\x1b[" + strings.Repeat("9;", 200) + "9H"},
		{"private marker, absurd mode", "\x1b[?" + strings.Repeat("9", 40) + "h"},
		{"marker then a second parameter", "\x1b[>" + strings.Repeat("9", 40) + ";99999m"},
		{"SGR truecolor components", "\x1b[38;2;99999;99999;99999m"},
		{"over-cap run then a private CSI", "\x1b[" + strings.Repeat("9", 40) + "\x1b[?99h"},
		{"OSC payload digits are not parameters", "\x1b]0;" + strings.Repeat("9", 40) + "\x07\x1b[99999b"},
		{"stray marker byte inside a parameter", "\x1b[9<9>9=9?9b"},

		// C0 controls inside a parameter list. Upstream neither ends the
		// parameter (the digits either side are concatenated) nor keeps the
		// command word (performAction's ExecuteAction does p.cmd = int(b),
		// wiping the private marker), so each of these was an unbounded
		// parameter reaching an iterating handler before the guard learned
		// about ExecuteAction.
		{"C0 splits an unmarked REP count", "X\x1b[" + strings.Repeat("9\x00", 7) + "b"},
		{"C0 splits an unmarked CHT count", "\x1b[" + strings.Repeat("9\x00", 9) + "I"},
		{"C0 strips the private marker", "X\x1b[?99999\x00b"},
		{"C0 strips the marker, burst", "X" + strings.Repeat("\x1b[?99999\x00b", 64)},
		{"C0 strips marker after an intermediate", "X\x1b[?99999 \x00b"},
		{"C0 strips the marker, 8-bit CSI", "X\x9b?99999\x00b"},
		{"C0 before the marker", "X\x1b[\x00?99999b"},
		{"C0 that aborts the sequence", "X\x1b[?99999\x18b\x1b[99999b"},
		{"C0 inside a marked DCS", "X\x1bP>99999\x00q\x1b\\\x1b[99999b"},
		{"every C0 that self-loops in a parameter list", func() string {
			var sb strings.Builder
			sb.WriteString("X\x1b[?9")
			for b := 0; b <= 0x1f; b++ {
				if b == 0x18 || b == 0x1a || b == 0x1b {
					continue // these leave the sequence; covered separately
				}
				sb.WriteString("9")
				sb.WriteByte(byte(b))
			}
			sb.WriteString("b")
			return sb.String()
		}()},
	}
	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(80, 24)
			out := e.clampCsiParams([]byte(tc.in))
			_ = e.Close()

			seqs := replayThroughUpstream(out)
			if len(seqs) == 0 {
				t.Fatalf("clampCsiParams(%.48q...) = %.48q: upstream dispatched no CSI/DCS, so this case asserts nothing", tc.in, string(out))
			}
			for _, s := range seqs {
				want := maxForwardedParamValue
				if s.cmd.Prefix() != 0 {
					want = maxForwardedPrivateParamValue
				}
				if len(s.params) > parser.MaxParamsSize {
					t.Errorf("clampCsiParams(%.48q...) forwarded a %s with %d parameters; upstream bounds a sequence to %d",
						tc.in, s.kind, len(s.params), parser.MaxParamsSize)
				}
				for i, value := range s.params {
					if value > want {
						t.Errorf("clampCsiParams(%.48q...) forwarded %s cmd %#x (prefix %q, final %q) parameter[%d] = %d; upstream may iterate that many times, bound is %d",
							tc.in, s.kind, int(s.cmd), s.cmd.Prefix(), s.cmd.Final(), i, value, want)
					}
				}
			}
		})
	}
}

// TestFeed_RepeatCountBurstStaysBounded pins the hang CI's bounded fuzz run
// found: a burst of maximal REP sequences, each of which turns its count into
// that many prints. The same construction is committed as a fuzz corpus entry
// (testdata/fuzz/FuzzFeedSplitConsistency), kept shorter there because the
// fuzzer replays and mutates every seed; this test scales it up.
//
// The guard is a RATIO against a floor measured in the same process, on the
// same emulator size, immediately before: the cheapest per-byte work this
// wrapper can do that still touches the whole grid and the scrollback, one LF
// per byte. An absolute deadline cannot be stated honestly here, because CI
// runs this package under `go test -race` (.github/workflows/ci.yml) and that
// moves the wall time by more than an order of magnitude. The ratio does not
// move, because both halves pay the same -race and same machine-load tax.
// Measured five runs each way on the pinned upstream at 80x24:
//
//	           floor (2321 LF)   clamped burst    ratio
//	plain      23-39ms           76-101ms         2.6-3.5x
//	-race      293-368ms         610-783ms        1.7-2.7x
//	unclamped  25ms / 347ms      10.2s / 97.0s    402x / 280x
//
// So the 100x threshold sits ~37x above the worst clamped ratio observed and
// ~2.8x below the unclamped one, in both configurations. The exact bound
// itself is asserted without any timing at all in
// TestClampCsiParams_ForwardedParamsAreBounded; this test exists to catch a
// regression that keeps the digit bound but makes each iteration expensive.
func TestFeed_RepeatCountBurstStaysBounded(t *testing.T) {
	const bursts = 256
	const maxRatioOverFloor = 100

	burst := []byte("X" + strings.Repeat("\x1b[99999b", bursts))
	floor := []byte(strings.Repeat("\n", len(burst)))

	feed := func(input []byte) time.Duration {
		e := NewEmulator(80, 24)
		defer func() { _ = e.Close() }()
		e.Feed([]byte(strings.Repeat("x", 80))) // give REP something to repeat
		start := time.Now()
		e.Feed(input)
		return time.Since(start)
	}

	floorCost := feed(floor)
	burstCost := feed(burst)
	if floorCost <= 0 {
		t.Fatalf("floor measurement of %d LFs came back as %v; cannot form a ratio", len(floor), floorCost)
	}
	if ratio := float64(burstCost) / float64(floorCost); ratio > maxRatioOverFloor {
		t.Fatalf("Feed of %d maximal REP sequences took %v, %.0fx the %v floor of the same number of LFs; bound is %dx",
			bursts, burstCost, ratio, floorCost, maxRatioOverFloor)
	}
}

// TestFeed_LongCsiParamStaysBounded asserts that a Feed call carrying a CSI
// CHT parameter far larger than any real terminal would use returns well
// within a small deadline, one byte at a time (the worst case: every byte
// lands at split index 0, so nothing but the clamp can bound it — see
// TestClampCsiParams_DropAtIndexZero for the same edge case isolated from
// Feed/the parser).
func TestFeed_LongCsiParamStaysBounded(t *testing.T) {
	// 20 digits: interpreted whole by the unclamped upstream parser this is a
	// parameter far beyond any tab-stop count, i.e. an effectively unbounded
	// loop (see the red evidence's timing table: an 11-digit parameter alone
	// already ran 9-15s).
	input := []byte("\x1b[" + strings.Repeat("9", 20) + "I")

	e := NewEmulator(80, 24)
	t.Cleanup(func() { _ = e.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, b := range input {
			e.Feed([]byte{b})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Feed of a %d-digit CSI CHT parameter, one byte at a time, did not return within 2s", len(input)-3)
	}
}

// TestClampCsiParams_DropAtIndexZero pins the reviewed blocker: when the
// first dropped byte of a Feed call is at index 0 (the case for any
// one-byte-at-a-time Feed, e.g. a PTY read loop flushing a child's output
// byte by byte), clampCsiParams must still drop it. A prior version used
// out != nil as its "a drop happened" sentinel, but append(nil, p[:0]...)
// itself evaluates to nil, so a drop at index 0 was silently invisible and
// the whole unclamped input was forwarded.
func TestClampCsiParams_DropAtIndexZero(t *testing.T) {
	e := NewEmulator(80, 24)
	t.Cleanup(func() { _ = e.Close() })

	// Feed the parameter digits up to the cap so the guard's digit count
	// already sits at the boundary going into the next call.
	if got := e.clampCsiParams([]byte("\x1b[123")); string(got) != "\x1b[123" {
		t.Fatalf("setup call unexpectedly modified: %q", got)
	}

	// The next digit, one over cap, arrives alone as its own call -- byte
	// index 0 -- exactly what a PTY read loop flushing a child's output one
	// byte at a time produces. It must still be dropped.
	if got := e.clampCsiParams([]byte("4")); string(got) != "" {
		t.Fatalf("clampCsiParams(%q) with the guard already at cap = %q, want empty (over-cap digit at index 0 must be dropped)", "4", got)
	}
}

// TestClampCsiParams_Table exercises clampCsiParams directly across the
// cases the reviewed bugs and their neighbors depend on: dropping at a
// mid-slice index still works; a fresh parameter under the cap is
// untouched; splitting an over-cap parameter across two calls (as
// FuzzFeedSplitConsistency does) clamps the same regardless of where the
// split falls, because guard state persists across calls; a private
// marker selects the relaxed cap for that sequence only; and a C0 control
// inside a parameter list neither restarts the digit count nor is allowed
// to strip a private marker, while every C0 that ends or aborts the
// sequence — and every C0 outside one — is forwarded untouched.
func TestClampCsiParams_Table(t *testing.T) {
	cases := []struct {
		name  string
		feeds []string
		want  string
	}{
		{
			name:  "under cap forwarded unchanged",
			feeds: []string{"\x1b[123I"},
			want:  "\x1b[123I",
		},
		{
			name:  "over cap drops trailing digits, keeps final byte",
			feeds: []string{"\x1b[1234567I"},
			want:  "\x1b[123I",
		},
		{
			name:  "drop at index 0 of the call",
			feeds: []string{"\x1b[1234", "567I"},
			want:  "\x1b[123I",
		},
		{
			name:  "drop at a mid-slice index",
			feeds: []string{"\x1b[12", "34567I"},
			want:  "\x1b[123I",
		},
		{
			name:  "split entirely inside the cap forwards both parts unchanged",
			feeds: []string{"\x1b[1", "23I"},
			want:  "\x1b[123I",
		},
		{
			name:  "each parameter is capped independently",
			feeds: []string{"\x1b[12345;67890H"},
			want:  "\x1b[123;678H",
		},
		{
			name:  "maximal repeat count is clamped to the ordinary cap",
			feeds: []string{"\x1b[99999b"},
			want:  "\x1b[999b",
		},
		{
			name:  "private marker keeps four-digit mode numbers intact",
			feeds: []string{"\x1b[?1049h\x1b[?2004h\x1b[?2026h"},
			want:  "\x1b[?1049h\x1b[?2004h\x1b[?2026h",
		},
		{
			name:  "private marker still caps at the relaxed cap",
			feeds: []string{"\x1b[?1234567h"},
			want:  "\x1b[?12345h",
		},
		{
			name:  "the relaxed cap applies to every parameter of the marked sequence",
			feeds: []string{"\x1b[>1234567;7654321m"},
			want:  "\x1b[>12345;76543m",
		},
		{
			name:  "the marker's scope ends with its sequence",
			feeds: []string{"\x1b[?1049h\x1b[99999b"},
			want:  "\x1b[?1049h\x1b[999b",
		},
		{
			name:  "a marker split across calls still selects the relaxed cap",
			feeds: []string{"\x1b[?", "1049h"},
			want:  "\x1b[?1049h",
		},
		{
			// The 8-bit CSI introducer is a single byte, so it is the only
			// thing that can end the previous sequence's marker scope here.
			name:  "an 8-bit CSI after a marked sequence gets the ordinary cap",
			feeds: []string{"\x1b[?1h\x9b99999b"},
			want:  "\x1b[?1h\x9b999b",
		},
		{
			name:  "an 8-bit CSI carries its own marker",
			feeds: []string{"\x9b?1049h"},
			want:  "\x9b?1049h",
		},
		{
			// Upstream concatenates the digits either side of the C0, so the
			// guard must not restart its count on it.
			name: "a C0 inside a parameter does not restart the digit count",
			// Five digits, four NULs: the first three digits survive, the
			// fourth and fifth are dropped, every NUL is forwarded.
			feeds: []string{"\x1b[9\x009\x009\x009\x009b"},
			want:  "\x1b[9\x009\x009\x00\x00b",
		},
		{
			name:  "a C0 inside a parameter, split across calls",
			feeds: []string{"\x1b[9\x009", "\x009\x009b"},
			want:  "\x1b[9\x009\x009\x00b",
		},
		{
			// Dropping it is what keeps the marker attached at dispatch, which
			// is the premise the relaxed cap rests on.
			name:  "a C0 inside a marked parameter list is dropped",
			feeds: []string{"\x1b[?99999\x00b"},
			want:  "\x1b[?99999b",
		},
		{
			name:  "a C0 after a marked sequence's intermediate byte is dropped",
			feeds: []string{"\x1b[?99999 \x00b"},
			want:  "\x1b[?99999 b",
		},
		{
			// CAN/SUB abort the sequence outright, so the marker never reaches
			// a handler and the byte must reach upstream to do the aborting.
			name:  "CAN inside a marked parameter list is forwarded",
			feeds: []string{"\x1b[?99999\x18b"},
			want:  "\x1b[?99999\x18b",
		},
		{
			name:  "SUB inside a marked parameter list is forwarded",
			feeds: []string{"\x1b[?99999\x1ab"},
			want:  "\x1b[?99999\x1ab",
		},
		{
			// A C1 control likewise leaves the sequence.
			name:  "a C1 control inside a marked parameter list is forwarded",
			feeds: []string{"\x1b[?99999\x84b"},
			want:  "\x1b[?99999\x84b",
		},
		{
			name:  "ordinary control characters outside a sequence are untouched",
			feeds: []string{"a\rb\nc\td\x00e\x1b[?1049h\n\r"},
			want:  "a\rb\nc\td\x00e\x1b[?1049h\n\r",
		},
		{
			// DEL and the stray-marker bytes are IgnoreAction inside a
			// parameter list: forwarded, and the digit count runs through them
			// exactly as upstream's accumulation does.
			name:  "IgnoreAction bytes inside a parameter are forwarded, count runs through",
			feeds: []string{"\x1b[9\x7f9<9>99b"},
			want:  "\x1b[9\x7f9<9>b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(80, 24)
			t.Cleanup(func() { _ = e.Close() })

			var got strings.Builder
			for _, part := range tc.feeds {
				got.Write(e.clampCsiParams([]byte(part)))
			}
			if got.String() != tc.want {
				t.Errorf("feeds %q clamped to %q, want %q", tc.feeds, got.String(), tc.want)
			}
		})
	}
}

// TestClampCsiParams_RealSequencesAreUntouched pins the other half of the
// contract: the clamp must be invisible to the sequences a real CLI agent
// emits. A regression that tightened the caps far enough to bound the hang
// but broke alternate screen or truecolor SGR would pass every bound assertion
// above and still be wrong.
func TestClampCsiParams_RealSequencesAreUntouched(t *testing.T) {
	real := []string{
		"\x1b[?1049h", "\x1b[?1049l", // alternate screen
		"\x1b[?2004h", "\x1b[?2004l", // bracketed paste
		"\x1b[?2026h", "\x1b[?2026l", // synchronized output
		"\x1b[?25l", "\x1b[?25h", // cursor visibility
		"\x1b[?1006h", "\x1b[?1002h", // SGR mouse
		"\x1b[38;2;255;128;0m\x1b[48;5;236m\x1b[0m", // truecolor and 256-color SGR
		"\x1b[24;80H\x1b[1;24r\x1b[2J\x1b[K",        // cursor, scroll region, erase
		"\x1b[?2026$p",                              // DECRQM
		"\x1bP+q544e\x1b\\",                         // XTGETTCAP
		"\x1b[>4;2m\x1b[>u\x1b[=1u",                 // modifyOtherKeys, kitty keyboard
		"\x1b]0;title\x07\x1b]11;?\x1b\\",           // OSC
		"hello\r\n\tworld\x08\x0b\x0c",              // plain text and C0 controls
	}
	for _, in := range real {
		t.Run(strings.ReplaceAll(in, "\x1b", "ESC"), func(t *testing.T) {
			e := NewEmulator(80, 24)
			t.Cleanup(func() { _ = e.Close() })
			if got := string(e.clampCsiParams([]byte(in))); got != in {
				t.Errorf("clampCsiParams(%q) = %q, want it forwarded byte-identical", in, got)
			}
		})
	}
}
