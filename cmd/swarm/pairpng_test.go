package main

// Committee item F3 (agents-tracker-726i): `swarm remote pair` writes the pairing QR as a PNG
// beside the terminal symbol, because the terminal raster is the one thing every line of field
// evidence indicts and a clean image is the one artifact the installed app has PROVEN it
// decodes (the emulator run reached the confirm screen from exactly this geometry).
//
// The conditions are the committee's, unanimously: pure black on white at an integer scale
// with a four-module quiet zone; 0600 and O_EXCL (the file carries the pairing secret -- the
// first time that secret ever touches disk -- so an existing file or a planted symlink is a
// refusal, not a target); written under <stateDir>/remote, never /tmp; REMOVED when the pair
// verb exits, on every path; and the path printed ABOVE the symbol, because rows printed after
// the symbol scroll its finder patterns off a 24-row screen.

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/qrterm"
)

func TestWritePairingPNG_WritesTheSymbolAtSpecGeometry(t *testing.T) {
	dir := t.TempDir()
	payload := realPairingPayload(t)
	path, err := writePairingPNG(payload, dir, "rvz-test-1234")
	if err != nil {
		t.Fatalf("writePairingPNG: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("PNG written to %s, want inside %s", path, dir)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("PNG mode %v, want 0600: the file carries the pairing secret", info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the written file does not decode as PNG: %v", err)
	}

	sym, err := qrterm.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	const scale, qz = 16, 4
	wantSide := (sym.Size() + 2*qz) * scale
	if img.Bounds().Dx() != wantSide || img.Bounds().Dy() != wantSide {
		t.Fatalf("PNG is %dx%d, want %dx%d (%d modules + %d quiet on each side at %dpx)",
			img.Bounds().Dx(), img.Bounds().Dy(), wantSide, wantSide, sym.Size(), qz, scale)
	}

	// Every module must round-trip: sample each 16x16 block's centre. The quiet zone is
	// covered by the same loop -- qrterm.Symbol reports out-of-range as light.
	for my := -qz; my < sym.Size()+qz; my++ {
		for mx := -qz; mx < sym.Size()+qz; mx++ {
			px := (mx+qz)*scale + scale/2
			py := (my+qz)*scale + scale/2
			r, g, b, _ := img.At(px, py).RGBA()
			dark := r < 0x4000 && g < 0x4000 && b < 0x4000
			if dark != sym.Dark(mx, my) {
				t.Fatalf("module (%d,%d): PNG dark=%v, symbol dark=%v -- the image does not "+
					"carry the symbol", mx, my, dark, sym.Dark(mx, my))
			}
		}
	}

	// O_EXCL: a second write to the same ceremony's path is a refusal, never an overwrite.
	if _, err := writePairingPNG(payload, dir, "rvz-test-1234"); err == nil {
		t.Fatal("writePairingPNG overwrote an existing file; a planted symlink would have " +
			"been followed")
	}
}

func TestRemotePair_OffersThePNGAboveTheSymbolAndRemovesItOnExit(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLUMNS", "80")
	t.Setenv("LINES", "24")

	out := runPairWithView(t, realPairingPayload(t), "K73-M2QF-9TD")

	pngLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Or scan the QR image at: ") {
			pngLine = line
			break
		}
	}
	if pngLine == "" {
		t.Fatalf("`swarm remote pair` never printed the QR image path.\nOutput:\n%s", out)
	}
	path := strings.TrimSpace(strings.SplitN(pngLine, "Or scan the QR image at: ", 2)[1])
	if !strings.HasSuffix(path, ".png") || !filepath.IsAbs(path) {
		t.Fatalf("the printed artifact %q is not an absolute .png path", path)
	}
	if filepath.Base(filepath.Dir(path)) != "remote" {
		t.Fatalf("PNG at %s, want under <stateDir>/remote (never a shared tmp)", path)
	}
	if strings.Index(out, pngLine) > strings.Index(out, "Scan this QR on your phone to pair:") {
		t.Fatal("the PNG path printed BELOW the symbol; every row after the symbol scrolls " +
			"its finder patterns off a 24-row screen")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the PNG survived the verb's exit (stat err=%v); the pairing secret must "+
			"not outlive the ceremony on disk", err)
	}
}

func TestRemotePair_ThePNGIsOfferedWhereNoSymbolCanBeDrawn(t *testing.T) {
	// The fallback path is where the image matters MOST: a terminal that cannot draw block
	// glyphs previously left manual entry as the only route.
	t.Setenv("TERM", "dumb")

	out := runPairWithView(t, realPairingPayload(t), "K73-M2QF-9TD")
	if !strings.Contains(out, "Or scan the QR image at: ") {
		t.Fatalf("the no-symbol fallback offered no QR image.\nOutput:\n%s", out)
	}
}
