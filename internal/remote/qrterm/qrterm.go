// Package qrterm renders a payload string as a scannable QR symbol for a terminal
// (PB-PAIR-1). Encoding is delegated to rsc.io/qr, a dependency-free pure-Go encoder;
// this package is the wrapping seam that owns the terminal drawing — the glyph family,
// the painted light background, the quiet zone, and the refusal to draw a symbol that
// does not fit.
package qrterm

import (
	"errors"
	"strings"

	"rsc.io/qr"
)

// ErrTooLarge reports that a symbol cannot be drawn inside the requested box. It is a
// REFUSAL, never a cropped or scaled symbol: a partial symbol still looks scannable and
// is not, so the caller must degrade to manual entry instead (PB-PAIR-1(c)).
var ErrTooLarge = errors.New("qrterm: symbol does not fit the terminal")

// eccLevel is the error-correction level every symbol is encoded at, and eccName is how
// Symbol.ECC states it (PB-PAIR-1(b) requires the level be stated). The two must agree.
//
// L (20% redundant) is deliberate, not a default: the pairing payload is ~119 characters,
// which L carries in a version-6 symbol (41 modules) while M needs version 7 (45). The
// caller reserves a row of a standard 24-row terminal for the heading the operator reads
// the symbol under, so the drawing gets 23 rows — 46 module rows at half-block density.
// 41 modules plus a 2-module quiet zone on each side is 45 and fits; 45 plus the same
// quiet zone is 49 and does not (PB-PAIR-1(b)). The symbol is read off a bright screen at
// arm's length rather than a scuffed label, so the redundancy L gives up buys nothing here.
const (
	eccLevel = qr.L
	eccName  = "L"
)

// Quiet-zone bounds, in modules. The standard specifies 4; a terminal drawing trades
// down against the row budget, and 2 is the floor below which the finder patterns lose
// their margin and scanners start failing.
const (
	maxQuietZone = 4
	minQuietZone = 2
)

// Half-block glyphs: one terminal cell carries two vertically stacked modules, which
// keeps a module very nearly square, since a cell is about twice as tall as it is wide.
// Quadrants and sextants pack more modules per cell but distort the module aspect ratio,
// and the pairing symbol already fits at half-block density, so the denser families would
// cost scannability and buy nothing.
const (
	cellBothLight = ' '
	cellUpperDark = '▀' // UPPER HALF BLOCK
	cellLowerDark = '▄' // LOWER HALF BLOCK
	cellBothDark  = '█' // FULL BLOCK
)

// rowPrefix paints black-on-bright-white across every drawn row and rowSuffix restores
// the terminal's own colours. The light background is PAINTED, never inherited: the
// product theme is dark, and a symbol drawn against a dark background is inverted, which
// most scanners reject (PB-PAIR-1(a)).
const (
	rowPrefix = "\x1b[30;107m"
	rowSuffix = "\x1b[0m"
)

// Symbol is an encoded QR symbol: a Size()xSize() grid of modules, quiet zone EXCLUDED.
type Symbol struct {
	code *qr.Code
}

// Encode encodes payload into the smallest QR symbol that carries it.
func Encode(payload string) (*Symbol, error) {
	code, err := qr.Encode(payload, eccLevel)
	if err != nil {
		return nil, err
	}
	return &Symbol{code: code}, nil
}

// Size is the module count per side. QR version v is 4v+17, so 21..177.
func (s *Symbol) Size() int { return s.code.Size }

// Dark reports whether the module at column x, row y is dark. Out of range is light,
// which is what makes the quiet zone fall out of the same read as the symbol itself.
func (s *Symbol) Dark(x, y int) bool { return s.code.Black(x, y) }

// ECC is the error-correction level the symbol was encoded at: "L", "M", "Q" or "H".
func (s *Symbol) ECC() string { return eccName }

// Rendering is a Symbol drawn for a terminal.
type Rendering struct {
	// Text is the drawing, terminal rows joined by "\n" with NO trailing newline. It
	// carries whatever escape sequences the drawing needs.
	Text string
	// Cols, Rows are the terminal cells Text occupies.
	Cols, Rows int
	// QuietZone is the light margin, in MODULES, drawn on every side.
	QuietZone int
	// Image is what a camera sees, quiet zone INCLUDED: Image[y][x] reports a DARK
	// module. Its side is Size()+2*QuietZone.
	Image [][]bool
}

// Render draws s inside a cols x rows terminal box, dark modules on an explicitly light
// background, with the widest standard-conformant quiet zone the box has room for. It
// returns ErrTooLarge, never a cropped or scaled-down symbol, when even the minimum quiet
// zone does not fit.
func (s *Symbol) Render(cols, rows int) (Rendering, error) {
	for qz := maxQuietZone; qz >= minQuietZone; qz-- {
		side := s.Size() + 2*qz
		if side <= cols && cellRows(side) <= rows {
			return s.draw(qz), nil
		}
	}
	return Rendering{}, ErrTooLarge
}

// draw paints the symbol with a qz-module light margin. It is only called with a quiet
// zone Render has already proved fits.
func (s *Symbol) draw(qz int) Rendering {
	side := s.Size() + 2*qz
	img := make([][]bool, side)
	for y := range img {
		img[y] = make([]bool, side)
		for x := range img[y] {
			img[y][x] = s.Dark(x-qz, y-qz)
		}
	}

	var b strings.Builder
	rows := cellRows(side)
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(rowPrefix)
		for x := 0; x < side; x++ {
			upper := img[2*r][x]
			// An odd-sided drawing's last cell has no lower module: light, which only
			// widens the bottom margin.
			lower := 2*r+1 < side && img[2*r+1][x]
			b.WriteRune(halfBlock(upper, lower))
		}
		b.WriteString(rowSuffix)
	}
	return Rendering{Text: b.String(), Cols: side, Rows: rows, QuietZone: qz, Image: img}
}

// cellRows is the terminal rows a side-module-tall drawing needs at half-block density.
func cellRows(side int) int { return (side + 1) / 2 }

// halfBlock is the glyph for a cell whose upper and lower modules are as given.
func halfBlock(upper, lower bool) rune {
	switch {
	case upper && lower:
		return cellBothDark
	case upper:
		return cellUpperDark
	case lower:
		return cellLowerDark
	default:
		return cellBothLight
	}
}
