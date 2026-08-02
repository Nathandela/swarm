package main

// FAILING-FIRST tests for Phase B slice S3, requirement PB-PAIR-1 (§4.4, §6.3): the
// `swarm remote pair` verb must put a genuinely SCANNABLE QR symbol on the terminal.
//
// THE DEFECT (verified at a2b6397). remote.go:280-281 prints
//
//	fmt.Fprintln(stdout, "Scan this QR on your phone to pair:")
//	fmt.Fprintln(stdout, sess.QR)
//
// — the literal payload STRING. No QR encoder exists in the repo and go.mod carries no
// QR dependency. There is nothing for a camera to scan, so the exit criterion's first
// verb ("your Android phone pairs") has no path.
//
// INTENDED PRODUCTION (RED — GREEN implements it):
//
//  1. `swarm remote pair` encodes sess.QR into a QR SYMBOL and draws it on stdout as a
//     2D block of terminal cells (the renderer contract is pinned by
//     internal/remote/qrterm/qrterm_test.go; the symbol assertions here deliberately do
//     NOT go through it, so they compile and fail on today's behaviour rather than on a
//     missing package. TestRemotePair_QRPresentationFitsTheTerminalRowBudget is the one
//     exception, added later: it compares the drawn block against what the renderer makes
//     of the FULL terminal box, which is a statement about the box the CLI passes and can
//     only be made against the renderer itself. It still pins no glyph family or density.)
//  2. The drawing paints an EXPLICIT LIGHT background — PB-PAIR-1(a): filled blocks on
//     a dark terminal are an inverted symbol and most scanners reject them, and §5 pins
//     the product theme dark.
//  3. The drawing fits a standard 80x24 terminal — PB-PAIR-1(b). Terminal size is read
//     from COLUMNS/LINES when set, else the controlling terminal, else 80x24. (stdout
//     is an injected io.Writer here, so the environment is the only channel a test can
//     drive; COLUMNS/LINES is the POSIX convention.)
//  4. PB-PAIR-1(c) fallback: when the terminal cannot render a symbol (TERM=dumb) or is
//     too small for one, the command prints the payload string for manual entry and does
//     NOT instruct the operator to scan something unscannable. Exit stays 0.
//
// These assertions are deliberately renderer-AGNOSTIC — any glyph family in the Block
// Elements or Legacy Computing ranges is accepted — because PB-PAIR-1(b)'s sizing budget
// is tight and pinning the density here could make the fit criterion unsatisfiable.
// Symbol-level structure (finder patterns, timing patterns, module fidelity) is asserted
// in internal/remote/qrterm/qrterm_test.go, which owns the renderer contract.
//
// RED today: TestRemotePair_RendersAScannableQRSymbol and its two siblings find no
// symbol block at all — stdout carries one line of payload text.
//
// Reused from remote_pair_test.go, NOT redeclared: newScriptedPairingHost /
// startFakePairingDaemon; shortStateDir from remote_devices_test.go.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/qrterm"
)

// realPairingPayload is the string production will hand the renderer once PB-PAIR-7
// lands: a pairing QR carrying a relay endpoint. PB-PAIR-7 requires PB-PAIR-1(b)'s
// sizing be re-derived from the payload production actually mints, so this test never
// substitutes a conveniently short stand-in.
func realPairingPayload(t *testing.T) string {
	t.Helper()
	return pairingPayloadFor(t, "wss://relay.example.com:8443")
}

// pairingPayloadFor is the payload production mints for relayURL — the real codec, the
// real fixed-size id and secret — so the relay URL is the only thing that varies, exactly
// as it is the only thing an operator varies in <stateDir>/remote/relay.json.
func pairingPayloadFor(t *testing.T, relayURL string) string {
	t.Helper()
	var id [16]byte
	var secret [32]byte
	for i := range id {
		id[i] = byte(i*7 + 3)
	}
	for i := range secret {
		secret[i] = byte(i*11 + 5)
	}
	s, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      relayURL,
		RendezvousID:  id,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("pairing.EncodeQR(relay URL of %d chars): %v", len(relayURL), err)
	}
	return s
}

// runPairWithQR drives `swarm remote pair` to completion against the scripted fake owner
// daemon, with the daemon handing back payload as the pairing QR, and returns stdout.
func runPairWithQR(t *testing.T, payload string) string {
	t.Helper()
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	host.view.QR = payload
	startFakePairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	return stdout.String()
}

// TestRemotePair_RendersAScannableQRSymbol is the headline PB-PAIR-1 assertion: the
// output carries a 2D module matrix, not a bare string.
func TestRemotePair_RendersAScannableQRSymbol(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	payload := realPairingPayload(t)
	out := runPairWithQR(t, payload)

	block := findSymbolBlock(out)
	if len(block) == 0 {
		t.Fatalf("`swarm remote pair` printed NO QR symbol — stdout carries only text "+
			"(the %d-character payload string is not scannable; PB-PAIR-1). Output:\n%s",
			len(payload), out)
	}
	if len(block) < 8 {
		t.Fatalf("the rendered QR block is only %d rows tall; a QR symbol is at least 21 "+
			"modules per side, so no plausible glyph density draws it in fewer than 8 rows", len(block))
	}

	// A symbol is a rectangle: every row the same width, and wide enough to be a symbol
	// rather than a decorative rule.
	w := cellWidth(block[0])
	for i, ln := range block {
		if got := cellWidth(ln); got != w {
			t.Fatalf("rendered QR row %d is %d cells wide, row 0 is %d; a QR symbol is a "+
				"rectangular module matrix", i, got, w)
		}
	}
	if w < 10 {
		t.Fatalf("rendered QR block is only %d cells wide; too narrow to carry a QR symbol", w)
	}
}

// TestRemotePair_QRFitsAStandardTerminal is PB-PAIR-1(b). The constraint is load-bearing,
// not decorative: production emits ~81 characters today, but PB-PAIR-7 adds the relay
// URL and the payload grows, pushing the symbol to a higher QR version. A symbol that
// scrolls off the top of an 80x24 terminal cannot be photographed.
func TestRemotePair_QRFitsAStandardTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	out := runPairWithQR(t, realPairingPayload(t))

	block := findSymbolBlock(out)
	if len(block) == 0 {
		t.Fatalf("no QR symbol rendered, so the 80x24 fit cannot be checked (PB-PAIR-1). Output:\n%s", out)
	}
	if len(block) > 24 {
		t.Errorf("rendered QR symbol is %d rows tall; it does not fit a standard 80x24 terminal "+
			"(PB-PAIR-1(b)) — either the rendering gets denser or the payload gets shorter", len(block))
	}
	if w := cellWidth(block[0]); w > 80 {
		t.Errorf("rendered QR symbol is %d cells wide; it does not fit a standard 80-column terminal", w)
	}
	// Nothing else on the pairing screen may wrap either — a wrapped line reflows the
	// symbol and destroys it.
	for _, ln := range strings.Split(out, "\n") {
		if w := cellWidth(ln); w > 80 {
			t.Errorf("pairing output line is %d cells wide (>80) and will wrap: %q", w, stripCSI(ln))
		}
	}
}

// TestRemotePair_QRUsesALightQuietZone is PB-PAIR-1(a). §5 pins the product theme dark;
// a symbol drawn with filled blocks against the terminal's own background is inverted,
// and most scanners reject an inverted symbol. The light background therefore has to be
// PAINTED by the drawing, which is observable as an explicit background-colour escape on
// every rendered row — including the quiet-zone rows, which carry no glyph data and
// would otherwise be left as bare whitespace showing the dark terminal through.
func TestRemotePair_QRUsesALightQuietZone(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	out := runPairWithQR(t, realPairingPayload(t))

	block := findSymbolBlock(out)
	if len(block) == 0 {
		t.Fatalf("no QR symbol rendered, so the quiet zone cannot be checked (PB-PAIR-1). Output:\n%s", out)
	}
	for i, ln := range block {
		if !paintsLightBackground(ln) {
			t.Fatalf("rendered QR row %d sets no explicit light background (row = %q); on a dark "+
				"terminal — which §5 pins as the product theme — the symbol renders inverted and "+
				"scanners reject it (PB-PAIR-1(a))", i, ln)
		}
	}
}

// TestRemotePair_FallsBackWhenTheSymbolCannotBeDrawn is PB-PAIR-1(c). Two terminals
// cannot take a symbol: one that cannot draw the glyphs, and one too small to hold it.
// In both the command must degrade to the payload string for manual entry (PB-PAIR-2's
// fallback channel) — and must NOT tell the operator to scan something unscannable,
// which is precisely today's misleading behaviour.
func TestRemotePair_FallsBackWhenTheSymbolCannotBeDrawn(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"dumb terminal", map[string]string{"TERM": "dumb", "COLUMNS": "80", "LINES": "24"}},
		{"terminal too small", map[string]string{"TERM": "xterm-256color", "COLUMNS": "40", "LINES": "10"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			payload := realPairingPayload(t)
			out := runPairWithQR(t, payload)

			if block := findSymbolBlock(out); len(block) != 0 {
				t.Errorf("a %s got a %d-row symbol block; the renderer must refuse and fall back "+
					"rather than emit a symbol the terminal cannot show", tc.name, len(block))
			}
			if !strings.Contains(out, payload) {
				t.Errorf("fallback output does not carry the payload string for manual entry; got:\n%s", out)
			}
			// The line that introduces the payload must not tell the operator to scan it.
			// A raw string under a "Scan this QR" heading IS the PB-PAIR-1 defect, and it
			// is what the command prints today.
			if lead := lineIntroducing(out, payload); strings.Contains(strings.ToLower(lead), "scan") {
				t.Errorf("the fallback introduces the payload with %q — but nothing scannable was "+
					"drawn; the manual-entry fallback must not be presented as a scan target", lead)
			}
		})
	}
}

// TestRemotePair_FallbackNamesTheRealCause is slice S3 review finding B1's second half.
// PB-PAIR-1(c)'s fallback is not just "print the payload" — it is the ONLY feedback the
// operator gets, so it has to name the cause they can act on. Three distinct causes lead
// here and they are fixed in three different places: the terminal cannot draw glyphs (use
// another terminal), the window is too small (resize it), or the payload is too big for
// ANY standard terminal (shorten the relay URL in the config file). Reporting "terminal
// too small" for the third — on an 80x24 terminal that is neither small nor incapable —
// actively misdirects debugging away from the config file, which is where the fix is.
func TestRemotePair_FallbackNamesTheRealCause(t *testing.T) {
	// 44 characters: an ordinary regional endpoint, past the QR's size ceiling. A daemon
	// can still be holding one — relay.json predates the write-time bound.
	const oversizedRelayURL = "wss://swarm-relay.us-east-1.example.com:8443"

	cases := []struct {
		name      string
		env       map[string]string
		payload   func(*testing.T) string
		wants     []string // substrings the reason MUST carry (lower-cased match)
		forbidden []string // substrings that would misdirect the operator
	}{
		{
			name:      "terminal cannot draw glyphs",
			env:       map[string]string{"TERM": "dumb", "COLUMNS": "80", "LINES": "24"},
			payload:   realPairingPayload,
			wants:     []string{"terminal"},
			forbidden: []string{"relay url", "too small"},
		},
		{
			name:      "terminal too small",
			env:       map[string]string{"TERM": "xterm-256color", "COLUMNS": "40", "LINES": "10"},
			payload:   realPairingPayload,
			wants:     []string{"terminal", "too small"},
			forbidden: []string{"relay url"},
		},
		{
			name: "payload too large for any standard terminal",
			env:  map[string]string{"TERM": "xterm-256color", "COLUMNS": "80", "LINES": "24"},
			payload: func(t *testing.T) string {
				return pairingPayloadFor(t, oversizedRelayURL)
			},
			wants:     []string{"relay url"},
			forbidden: []string{"too small"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			payload := tc.payload(t)
			out := runPairWithQR(t, payload)

			if block := findSymbolBlock(out); len(block) != 0 {
				t.Fatalf("a symbol block of %d rows was drawn; this case must fall back", len(block))
			}
			reason := fallbackReasonLine(out)
			if reason == "" {
				t.Fatalf("the fallback printed no reason at all; the operator is left with a "+
					"payload string and no idea why. Output:\n%s", out)
			}
			low := strings.ToLower(reason)
			for _, want := range tc.wants {
				if !strings.Contains(low, want) {
					t.Errorf("fallback reason %q does not mention %q; the operator cannot tell "+
						"which of the three causes they hit, and only one of them is fixed by "+
						"resizing the window", reason, want)
				}
			}
			for _, bad := range tc.forbidden {
				if strings.Contains(low, bad) {
					t.Errorf("fallback reason %q claims %q, which is not the cause here; it sends "+
						"the operator to fix the wrong thing", reason, bad)
				}
			}
		})
	}
}

// TestRemotePair_QRPresentationFitsTheTerminalRowBudget is the PB-PAIR-1(b) assertion the
// three tests above structurally CANNOT make. They render into an io.Writer buffer, which
// has no viewport and therefore no scrolling, so they can only prove the SYMBOL is 24 rows
// or fewer — never that the symbol survives the chrome that is always printed with it.
//
// A real terminal scrolls. `swarm remote pair` prints a heading above the symbol, and the
// manual-entry code, the rendezvous id and the expiry alongside it, and only then BLOCKS
// waiting for the phone. Whatever is on screen during that block IS the scan target. If
// the heading plus the symbol plus everything printed after the symbol exceeds LINES, the
// terminal has already scrolled by the difference and the TOP of the symbol is gone —
// including the two upper finder patterns, which are how a scanner locates and orients a
// symbol at all. A symbol without its finders is not scannable, so a presentation that
// overflows the row budget defeats the whole slice while still passing every assertion
// above.
//
// The budget is the whole terminal, and it is spent in ONE direction. Rows printed ABOVE
// the symbol scroll off the top harmlessly — by the time the command blocks they are gone
// and the symbol has simply moved up. Rows printed BELOW it push the symbol off instead,
// and those are the rows that destroy it. So the invariant is not "chrome + symbol <=
// LINES"; it is: nothing is printed after the symbol, and the symbol itself is no taller
// than the viewport. Charging the heading against the symbol's height is a full module of
// quiet zone the symbol never gets back (S3 review N2), and it buys nothing: the heading
// is the first thing to scroll away.
//
// Checked across the terminal heights the symbol has to survive, since the renderer picks
// a different quiet zone at each and only the smallest is at the standard's floor.
func TestRemotePair_QRPresentationFitsTheTerminalRowBudget(t *testing.T) {
	for _, lines := range []int{23, 24, 25, 26, 40} {
		t.Run(fmt.Sprintf("LINES=%d", lines), func(t *testing.T) {
			const cols = 80
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("COLUMNS", strconv.Itoa(cols))
			t.Setenv("LINES", strconv.Itoa(lines))

			payload := realPairingPayload(t)
			screen := scanTimeLines(runPairWithQR(t, payload))

			first, last := symbolBlockBounds(screen)
			if first < 0 {
				t.Fatalf("no QR symbol rendered, so the row budget cannot be checked (PB-PAIR-1). Output:\n%s",
					strings.Join(screen, "\n"))
			}

			// (1) Nothing after the symbol. Every such row scrolls the symbol's top — its
			// upper finder patterns, which is how a scanner locates and orients a symbol at
			// all — off the screen the operator is photographing.
			if after := len(screen) - 1 - last; after != 0 {
				t.Errorf("%d row(s) are printed after the symbol and before the pairing blocks on "+
					"the phone; the terminal scrolls by that much and the symbol's upper finder "+
					"patterns go off screen (PB-PAIR-1(b)). Printed after:\n%s",
					after, strings.Join(screen[last+1:], "\n"))
			}

			// (2) The symbol itself fits the viewport, so all of it is on screen at once.
			height := last - first + 1
			if height > lines {
				t.Errorf("the symbol is %d rows tall but LINES=%d; %d row(s) of it are scrolled off "+
					"the top and nothing can scan it", height, lines, height-lines)
			}

			// (3) The presentation hands the renderer the WHOLE viewport. Reserving rows out
			// of the box costs the symbol quiet zone — the one margin a scanner needs and the
			// one thing this presentation can still give it for free. Renderer-agnostic: the
			// expectation is whatever the renderer itself makes of the full box.
			sym, err := qrterm.Encode(payload)
			if err != nil {
				t.Fatalf("qrterm.Encode(payload): %v", err)
			}
			want, err := sym.Render(cols, lines)
			if err != nil {
				t.Fatalf("qrterm.Render(%d, %d) on the real pairing payload: %v", cols, lines, err)
			}
			if got := cellWidth(screen[first]); height != want.Rows || got != want.Cols {
				t.Errorf("the drawn symbol is %dx%d but the renderer makes %dx%d (quiet zone %d) of "+
					"the full %dx%d box: the presentation is reserving rows it does not need, and "+
					"the symbol pays for them in quiet zone", got, height, want.Cols, want.Rows,
					want.QuietZone, cols, lines)
			}
		})
	}
}

// sasGateMarker introduces the SAS gate — the first line `swarm remote pair` prints after
// the phone has connected. Everything before it is the screen the operator scans from;
// everything from it on is printed after the scan and cannot disturb it.
const sasGateMarker = "Device: "

// scanTimeLines is the pairing output the operator is looking at while scanning: every
// line up to the SAS gate, with the empty element left by the final newline dropped.
func scanTimeLines(out string) []string {
	lines := strings.Split(out, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i, ln := range lines {
		if strings.HasPrefix(ln, sasGateMarker) {
			return lines[:i]
		}
	}
	return lines
}

// symbolBlockBounds locates the drawn symbol in lines — the first and last index of the
// longest run of module lines — or (-1, -1) when nothing was drawn.
func symbolBlockBounds(lines []string) (first, last int) {
	first, last, best, run := -1, -1, 0, -1
	for i := 0; i <= len(lines); i++ {
		if i < len(lines) && isModuleLine(lines[i]) {
			if run < 0 {
				run = i
			}
			continue
		}
		if run >= 0 && i-run > best {
			first, last, best = run, i-1, i-run
		}
		run = -1
	}
	return first, last
}

// findSymbolBlock returns the longest run of consecutive lines that are drawn entirely
// out of module cells — the block a camera would see. A module cell is a space or a
// glyph from Block Elements (U+2580..U+259F) or Legacy Computing (U+1FB00..U+1FBFF), so
// half-block, quadrant and sextant renderings all qualify and none is mandated.
func findSymbolBlock(out string) []string {
	var best, cur []string
	for _, ln := range strings.Split(out, "\n") {
		if isModuleLine(ln) {
			cur = append(cur, ln)
			continue
		}
		if len(cur) > len(best) {
			best = cur
		}
		cur = nil
	}
	if len(cur) > len(best) {
		best = cur
	}
	return best
}

// lineIntroducing returns the last non-empty line before the one carrying payload — the
// heading the operator reads the payload under. It is "" when payload is not on a line
// of its own.
func lineIntroducing(out, payload string) string {
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, payload) {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if strings.TrimSpace(lines[j]) != "" {
				return lines[j]
			}
		}
		return ""
	}
	return ""
}

// fallbackReasonLine returns the line on which the PB-PAIR-1(c) fallback explains why no
// symbol was drawn, or "" when it explained nothing.
func fallbackReasonLine(out string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(strings.ToLower(ln), "no qr symbol") {
			return ln
		}
	}
	return ""
}

func isModuleLine(ln string) bool {
	plain := stripCSI(ln)
	if utf8.RuneCountInString(plain) < 8 {
		return false
	}
	for _, r := range plain {
		if r == ' ' || (r >= 0x2580 && r <= 0x259f) || (r >= 0x1fb00 && r <= 0x1fbff) {
			continue
		}
		return false
	}
	return true
}

// cellWidth is a line's width in terminal cells, escape sequences excluded.
func cellWidth(ln string) int { return utf8.RuneCountInString(stripCSI(ln)) }

// stripCSI removes CSI escape sequences.
func stripCSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j + 1
			continue
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += w
	}
	return b.String()
}

// paintsLightBackground reports whether ln contains an SGR sequence selecting a light
// background: 47 (white), 107 (bright white), 48;5;N for a light 256-colour index, or
// 48;2;R;G;B for a light truecolour value. It is the observable form of "the drawing
// does not rely on the terminal's own background".
func paintsLightBackground(ln string) bool {
	for i := 0; i < len(ln); i++ {
		if ln[i] != 0x1b || i+1 >= len(ln) || ln[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(ln) && (ln[j] < 0x40 || ln[j] > 0x7e) {
			j++
		}
		if j < len(ln) && ln[j] == 'm' && lightBackgroundParams(strings.Split(ln[i+2:j], ";")) {
			return true
		}
	}
	return false
}

func lightBackgroundParams(params []string) bool {
	for i, p := range params {
		switch p {
		case "47", "107":
			return true
		case "48":
			if i+2 < len(params) && params[i+1] == "5" {
				n := sgrNum(params[i+2])
				return n == 15 || n == 231 || n >= 250
			}
			if i+4 < len(params) && params[i+1] == "2" {
				return sgrNum(params[i+2]) >= 200 && sgrNum(params[i+3]) >= 200 && sgrNum(params[i+4]) >= 200
			}
		}
	}
	return false
}

func sgrNum(s string) int {
	n := 0
	if s == "" {
		return -1
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
