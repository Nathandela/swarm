package vt

// R1.4.1(a)/(b) perf baseline benchmarks (docs/verification/perf-baseline-2026-07-18.md):
// Feed throughput (plain/styled, 80x24/200x50) and Snapshot build+Marshal /
// DecodeSnapshot cost. gridCols/gridRows (80x24) are the existing package
// constants (emulator_test.go); bigCols/bigRows add the 200x50 case.

import (
	"bytes"
	"testing"
)

const (
	bigCols = 200
	bigRows = 50
)

// genPlainFrame builds a full-grid frame of unstyled text: cols 'x' per row,
// CRLF-terminated, mimicking a plain-text program repainting the screen.
func genPlainFrame(cols, rows int) []byte {
	var b bytes.Buffer
	line := bytes.Repeat([]byte("x"), cols)
	for y := 0; y < rows; y++ {
		b.Write(line)
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// genStyledFrame builds a full-grid frame with an SGR color change every 4
// cells, mimicking colorized output (e.g. a syntax-highlighted log) so the
// benchmark exercises the parser's attribute-transition path, not just glyphs.
func genStyledFrame(cols, rows int) []byte {
	colors := []string{"31", "32", "33", "34", "35", "36", "37"}
	var b bytes.Buffer
	ci := 0
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x += 4 {
			n := 4
			if x+n > cols {
				n = cols - x
			}
			b.WriteString("\x1b[")
			b.WriteString(colors[ci%len(colors)])
			b.WriteByte('m')
			b.Write(bytes.Repeat([]byte("x"), n))
			ci++
		}
		b.WriteString("\x1b[0m\r\n")
	}
	return b.Bytes()
}

func benchFeed(b *testing.B, cols, rows int, payload []byte) {
	e := NewEmulator(cols, rows)
	defer e.Close()
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Feed(payload)
	}
}

func BenchmarkFeed_Plain_80x24(b *testing.B) {
	benchFeed(b, gridCols, gridRows, genPlainFrame(gridCols, gridRows))
}

func BenchmarkFeed_Plain_200x50(b *testing.B) {
	benchFeed(b, bigCols, bigRows, genPlainFrame(bigCols, bigRows))
}

func BenchmarkFeed_Styled_80x24(b *testing.B) {
	benchFeed(b, gridCols, gridRows, genStyledFrame(gridCols, gridRows))
}

func BenchmarkFeed_Styled_200x50(b *testing.B) {
	benchFeed(b, bigCols, bigRows, genStyledFrame(bigCols, bigRows))
}

// benchSnapshot measures buildSnap + json.Marshal (Emulator.Snapshot) on a
// grid pre-filled by payload.
func benchSnapshot(b *testing.B, cols, rows int, payload []byte) {
	e := NewEmulator(cols, rows)
	defer e.Close()
	e.Feed(payload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Snapshot(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSnapshot_80x24(b *testing.B) {
	benchSnapshot(b, gridCols, gridRows, genStyledFrame(gridCols, gridRows))
}

func BenchmarkSnapshot_200x50(b *testing.B) {
	benchSnapshot(b, bigCols, bigRows, genStyledFrame(bigCols, bigRows))
}

// benchDecodeSnapshot measures DecodeSnapshot on the JSON bytes Snapshot
// produces for a grid pre-filled by payload.
func benchDecodeSnapshot(b *testing.B, cols, rows int, payload []byte) {
	e := NewEmulator(cols, rows)
	defer e.Close()
	e.Feed(payload)
	snap, err := e.Snapshot()
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(snap)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeSnapshot(snap); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeSnapshot_80x24(b *testing.B) {
	benchDecodeSnapshot(b, gridCols, gridRows, genStyledFrame(gridCols, gridRows))
}

func BenchmarkDecodeSnapshot_200x50(b *testing.B) {
	benchDecodeSnapshot(b, bigCols, bigRows, genStyledFrame(bigCols, bigRows))
}
