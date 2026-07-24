package qrterm

// FAILING-FIRST tests for Phase B slice S3, requirement PB-PAIR-1 (§4.4, §6.3): the
// machine must render a genuinely SCANNABLE QR symbol, not a raw string.
//
// THE DEFECT (verified at a2b6397). cmd/swarm/remote.go:280-281 prints
// "Scan this QR on your phone to pair:" followed by the literal payload STRING. There
// is no QR encoder anywhere in the repo (no qrcode dependency in go.mod);
// internal/remote/pairing/qr.go:86 DecodeQR parses a STRING, it does not read a camera
// frame. There is nothing for a camera to scan.
//
// INTENDED PRODUCTION (RED — this package does not exist yet; GREEN implements it).
// This file IS the contract; the exported surface below is what cmd/swarm consumes:
//
//	// Package qrterm renders a payload string as a scannable QR symbol for a terminal.
//	package qrterm
//
//	// ErrTooLarge reports that a symbol cannot be drawn inside the requested box.
//	var ErrTooLarge error
//
//	// Symbol is an encoded QR symbol: a Size()xSize() grid of modules, quiet zone
//	// EXCLUDED.
//	type Symbol struct{ ... }
//
//	// Encode encodes payload into the smallest QR symbol that carries it.
//	func Encode(payload string) (*Symbol, error)
//
//	// Size is the module count per side. QR version v is 4v+17, so 21..177.
//	func (s *Symbol) Size() int
//
//	// Dark reports whether the module at column x, row y is dark. Out of range is light.
//	func (s *Symbol) Dark(x, y int) bool
//
//	// ECC is the error-correction level the symbol was encoded at: "L", "M", "Q" or "H".
//	// PB-PAIR-1(b) requires the level be STATED; this is where it is stated.
//	func (s *Symbol) ECC() string
//
//	// Rendering is a Symbol drawn for a terminal.
//	type Rendering struct {
//	    // Text is the drawing, terminal rows joined by "\n" with NO trailing newline.
//	    // It carries whatever escape sequences the drawing needs.
//	    Text string
//	    // Cols, Rows are the terminal cells Text occupies.
//	    Cols, Rows int
//	    // QuietZone is the light margin, in MODULES, drawn on every side.
//	    QuietZone int
//	    // Image is what a camera sees, quiet zone INCLUDED: Image[y][x] reports a DARK
//	    // module. Its side is Size()+2*QuietZone. It is the drawing's own account of
//	    // the picture it painted, and exists so the rendering can be checked for
//	    // orientation and completeness without a camera.
//	    Image [][]bool
//	}
//
//	// Render draws s inside a cols x rows terminal box, dark modules on an EXPLICITLY
//	// LIGHT background — a symbol drawn against a dark terminal's own background is
//	// inverted and most scanners reject it (PB-PAIR-1(a)) — with a quiet-zone margin.
//	// It returns ErrTooLarge, never a cropped or scaled-down symbol, when the box is
//	// too small; that refusal is what drives the CLI's PB-PAIR-1(c) fallback.
//	func (s *Symbol) Render(cols, rows int) (Rendering, error)
//
// DELIBERATELY NOT PINNED, because PB-PAIR-1(b)'s sizing budget is tight enough that
// fixing them could make the fit criterion unsatisfiable: the glyph family (half-block,
// quadrant, sextant), the exact SGR colors, the ECC level, and the quiet-zone width
// beyond a floor of 2. The tests assert the OBSERVABLE properties those choices have to
// produce.
//
// RED today: the package has no non-test files, so this is a missing-API compile RED,
// unambiguous by name (`undefined: Encode`) — the same shape as this repo's other
// new-API RED files (internal/skeleton/pairing_config_test.go). `go build ./...` is
// unaffected. The CLI-level PB-PAIR-1 tests in cmd/swarm/remote_pair_qr_test.go
// deliberately do NOT import this package, so they compile today and produce a clean
// assertion RED against the current raw-string behavior.

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// realisticPayload builds the string a real `swarm remote pair` will encode ONCE
// PB-PAIR-7 lands: a pairing QR carrying a relay endpoint. Sizing must be re-derived
// from the payload production actually mints (PB-PAIR-7's note on PB-PAIR-1(b)), so
// these tests never invent a convenient short string.
//
// The relay URL is a realistic public deployment (28 characters); the id and secret are
// full-entropy-shaped, since base64url of random bytes is what the codec really emits.
func realisticPayload(t *testing.T) string {
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
		RelayURL:      "wss://relay.example.com:8443",
		RendezvousID:  id,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("pairing.EncodeQR: %v", err)
	}
	return s
}

// finderModule is the module value the QR standard fixes at offset (dx, dy) inside a
// 7x7 finder pattern: a dark 7x7 border, a light 5x5 ring, a dark 3x3 core.
func finderModule(dx, dy int) bool {
	if dx == 0 || dx == 6 || dy == 0 || dy == 6 {
		return true
	}
	return dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
}

// TestEncode_ProducesARealQRSymbol is the "not a string" assertion at its strongest
// available strength. The repo has no QR symbol DECODER and this slice does not add a
// dependency to get one, so scannability cannot be proved by decoding. What CAN be
// proved is that the module grid carries the fixed structures every conformant QR
// symbol has and no ad-hoc drawing does: a legal version size, three 7x7 finder
// patterns, and the two timing lines a scanner uses to lock the module pitch.
func TestEncode_ProducesARealQRSymbol(t *testing.T) {
	sym, err := Encode(realisticPayload(t))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// A QR symbol of version v has side 4v+17, v in 1..40.
	size := sym.Size()
	if size < 21 || size > 177 || (size-17)%4 != 0 {
		t.Fatalf("Size() = %d; not a legal QR symbol side (4v+17 for v in 1..40)", size)
	}

	if ecc := sym.ECC(); ecc != "L" && ecc != "M" && ecc != "Q" && ecc != "H" {
		t.Errorf("ECC() = %q; want one of L, M, Q, H (PB-PAIR-1(b): the level must be stated)", ecc)
	}

	// The three finder patterns: top-left, top-right, bottom-left.
	corners := []struct {
		name   string
		ox, oy int
	}{
		{"top-left", 0, 0},
		{"top-right", size - 7, 0},
		{"bottom-left", 0, size - 7},
	}
	for _, c := range corners {
		for dy := 0; dy < 7; dy++ {
			for dx := 0; dx < 7; dx++ {
				want := finderModule(dx, dy)
				if got := sym.Dark(c.ox+dx, c.oy+dy); got != want {
					t.Fatalf("%s finder pattern broken at module (%d,%d): Dark = %v, want %v; "+
						"the drawing is not a conformant QR symbol", c.name, c.ox+dx, c.oy+dy, got, want)
				}
			}
		}
	}

	// The timing patterns: row 6 and column 6 alternate dark/light between the finders.
	for x := 8; x < size-8; x++ {
		if want := x%2 == 0; sym.Dark(x, 6) != want {
			t.Fatalf("horizontal timing pattern broken at column %d: Dark = %v, want %v", x, !want, want)
		}
	}
	for y := 8; y < size-8; y++ {
		if want := y%2 == 0; sym.Dark(6, y) != want {
			t.Fatalf("vertical timing pattern broken at row %d: Dark = %v, want %v", y, !want, want)
		}
	}
}

// TestEncode_SymbolDependsOnThePayload guards the degenerate pass: a renderer that
// returns a fixed picture would satisfy every structural assertion above. Two different
// payloads must produce different module grids.
func TestEncode_SymbolDependsOnThePayload(t *testing.T) {
	a, err := Encode(realisticPayload(t))
	if err != nil {
		t.Fatalf("Encode(a): %v", err)
	}
	// The relay URL is base64url-encoded inside the payload, so the plaintext host never
	// appears in it -- the original strings.Replace on "relay.example.com" was a silent
	// no-op, so this compared Encode(x) against Encode(x), which EVERY deterministic
	// encoder fails: identical payloads give identical grids, the loop below never
	// returns early, and execution reaches t.Fatal. The test failed loudly rather than
	// passing vacuously. Mutate the encoded body directly so the payloads genuinely differ.
	b, err := Encode(mutateOneChar(t, realisticPayload(t)))
	if err != nil {
		t.Fatalf("Encode(b): %v", err)
	}
	if a.Size() != b.Size() {
		return // different versions is already a difference
	}
	for y := 0; y < a.Size(); y++ {
		for x := 0; x < a.Size(); x++ {
			if a.Dark(x, y) != b.Dark(x, y) {
				return
			}
		}
	}
	t.Fatal("two different payloads encoded to an identical module grid; the symbol does not carry the payload")
}

// TestRender_FitsAStandard80x24Terminal is PB-PAIR-1(b). The constraint is load-bearing,
// not decorative: production emits ~81 characters today, but PB-PAIR-7 adds the relay
// URL and the payload grows, pushing the symbol to a higher QR version. A symbol that
// scrolls off the top of an 80x24 terminal cannot be photographed.
func TestRender_FitsAStandard80x24Terminal(t *testing.T) {
	sym, err := Encode(realisticPayload(t))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	r, err := sym.Render(80, 24)
	if err != nil {
		t.Fatalf("Render(80, 24) on the real post-PB-PAIR-7 pairing payload: %v; the symbol "+
			"MUST fit a standard terminal — either the rendering gets denser or the payload "+
			"gets shorter (PB-PAIR-1(b))", err)
	}
	if r.Cols > 80 || r.Rows > 24 {
		t.Fatalf("Render(80, 24) reported %dx%d cells; does not fit a standard terminal", r.Cols, r.Rows)
	}

	// Reported dimensions must be the dimensions actually drawn: a rendering that claims
	// to fit while emitting a wider block still scrolls.
	lines := strings.Split(r.Text, "\n")
	if len(lines) != r.Rows {
		t.Fatalf("Rendering.Text has %d rows but Rendering.Rows = %d", len(lines), r.Rows)
	}
	for i, ln := range lines {
		if w := utf8.RuneCountInString(stripANSI(ln)); w != r.Cols {
			t.Fatalf("Rendering.Text row %d is %d cells wide but Rendering.Cols = %d; the drawing "+
				"must be a rectangle of exactly the reported size", i, w, r.Cols)
		}
	}
}

// TestRender_DrawsTheSymbolDarkOnLight is PB-PAIR-1(a). Filled blocks left to inherit a
// dark terminal background produce an INVERTED symbol, which most scanners reject, and
// §5 pins the product theme dark — so the light quiet zone has to be painted, not
// assumed. It also checks the drawing is faithful: the image a camera sees is the
// encoded symbol, whole, with a light margin.
func TestRender_DrawsTheSymbolDarkOnLight(t *testing.T) {
	sym, err := Encode(realisticPayload(t))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	r, err := sym.Render(80, 24)
	if err != nil {
		t.Fatalf("Render(80, 24): %v", err)
	}

	// A quiet zone must exist. The QR standard asks for 4 modules; a terminal drawing
	// may trade down against the 24-row budget, but 2 is the floor below which the
	// finder patterns lose their margin and scanners start failing.
	if r.QuietZone < 2 {
		t.Fatalf("Rendering.QuietZone = %d; want at least 2 light modules on every side "+
			"(the QR standard specifies 4; going below that is a documented tradeoff, "+
			"going below 2 is not)", r.QuietZone)
	}

	side := sym.Size() + 2*r.QuietZone
	if len(r.Image) != side {
		t.Fatalf("Rendering.Image has %d rows; want Size()+2*QuietZone = %d", len(r.Image), side)
	}
	for y, row := range r.Image {
		if len(row) != side {
			t.Fatalf("Rendering.Image row %d has %d columns; want %d", y, len(row), side)
		}
	}

	// The quiet zone is LIGHT on all four sides. If this fails the symbol is inverted.
	for i := 0; i < side; i++ {
		for _, p := range [][2]int{{i, 0}, {i, side - 1}, {0, i}, {side - 1, i}} {
			if r.Image[p[1]][p[0]] {
				t.Fatalf("Rendering.Image has a DARK module at (%d,%d), on the quiet-zone border; "+
					"the symbol is inverted (dark quiet zone), which most scanners reject "+
					"(PB-PAIR-1(a))", p[0], p[1])
			}
		}
	}
	for y := 0; y < r.QuietZone; y++ {
		for x := 0; x < side; x++ {
			if r.Image[y][x] || r.Image[side-1-y][x] {
				t.Fatalf("quiet-zone row %d is not fully light", y)
			}
		}
	}

	// The drawing is the symbol, module for module.
	for y := 0; y < sym.Size(); y++ {
		for x := 0; x < sym.Size(); x++ {
			if got, want := r.Image[r.QuietZone+y][r.QuietZone+x], sym.Dark(x, y); got != want {
				t.Fatalf("Rendering.Image at symbol module (%d,%d) = %v, want %v; the drawing "+
					"does not reproduce the encoded symbol", x, y, got, want)
			}
		}
	}

	// The light background is PAINTED, not inherited: every drawn row explicitly sets a
	// light background colour, so a dark-themed terminal cannot invert the symbol.
	for i, ln := range strings.Split(r.Text, "\n") {
		if !setsLightBackground(ln) {
			t.Fatalf("Rendering.Text row %d sets no explicit light background (row = %q); on a "+
				"dark terminal the symbol renders inverted and scanners reject it "+
				"(PB-PAIR-1(a))", i, ln)
		}
	}
}

// TestRender_RefusesABoxItDoesNotFit pins the failure mode PB-PAIR-1(c)'s fallback is
// built on: too small a terminal must be a refusal, never a cropped, scaled or wrapped
// symbol that looks scannable and is not.
func TestRender_RefusesABoxItDoesNotFit(t *testing.T) {
	sym, err := Encode(realisticPayload(t))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	r, err := sym.Render(10, 4)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Render(10, 4) = (%dx%d, %v); want ErrTooLarge — a symbol that does not fit "+
			"must be refused, not cropped", r.Cols, r.Rows, err)
	}
}

// stripANSI removes CSI escape sequences so a drawing's true cell width can be counted.
func stripANSI(s string) string {
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

// setsLightBackground reports whether s contains an SGR sequence selecting a light
// background: 47 (white), 107 (bright white), 48;5;N for a light 256-colour index, or
// 48;2;R;G;B for a light truecolour value. This is the observable form of "the drawing
// does not rely on the terminal's own background".
func setsLightBackground(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) || s[j] != 'm' {
			continue
		}
		if lightBackgroundSGR(strings.Split(s[i+2:j], ";")) {
			return true
		}
	}
	return false
}

func lightBackgroundSGR(params []string) bool {
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case "47", "107":
			return true
		case "48":
			if i+2 < len(params) && params[i+1] == "5" {
				return atoiDefault(params[i+2], -1) >= 250 || params[i+2] == "15" || params[i+2] == "231"
			}
			if i+4 < len(params) && params[i+1] == "2" {
				return atoiDefault(params[i+2], 0) >= 200 &&
					atoiDefault(params[i+3], 0) >= 200 &&
					atoiDefault(params[i+4], 0) >= 200
			}
		}
	}
	return false
}

func atoiDefault(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// mutateOneChar returns payload with a single character of its base64url body changed, so
// the result is guaranteed to be a different payload than the input.
func mutateOneChar(t *testing.T, payload string) string {
	t.Helper()
	i := strings.LastIndex(payload, ":")
	if i < 0 || i+2 >= len(payload) {
		t.Fatalf("payload has no encoded body to mutate: %q", payload)
	}
	body := []byte(payload[i+1:])
	if body[0] == 'A' {
		body[0] = 'B'
	} else {
		body[0] = 'A'
	}
	return payload[:i+1] + string(body)
}
