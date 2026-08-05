package main

// Committee item F3 (agents-tracker-726i, ADR-007 B141): the pairing QR as a PNG, beside the
// terminal symbol. The terminal raster is the one display this product cannot control -- cell
// aspect, glyph ink coverage and line leading belong to the terminal -- and the emulator run
// proved the installed app decodes exactly this artifact's geometry end to end. The PNG is
// the promised scan target; the terminal symbol stays as the zero-friction best effort.

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/qrterm"
)

const (
	// pngScale is 16 px per module: the geometry the emulator e2e decoded, with no
	// interpolation ambiguity at any viewer zoom.
	pngScale = 16
	// pngQuietZone is the QR spec's four modules -- the terminal renderer has to bargain
	// this down to fit a row budget; an image file does not.
	pngQuietZone = 4
)

// writePairingPNG renders payload as a PNG under dir and returns its path.
//
// THE FILE CARRIES THE PAIRING SECRET -- the first time that secret is ever persisted to
// disk -- so it is created 0600 and with O_EXCL: an existing file at the path is a refusal,
// which is also what makes a planted symlink an error rather than a write through it. The
// caller owns removal; the secret must not outlive the ceremony on disk.
func writePairingPNG(payload, dir, rendezvousID string) (string, error) {
	sym, err := qrterm.Encode(payload)
	if err != nil {
		return "", err
	}

	side := (sym.Size() + 2*pngQuietZone) * pngScale
	img := image.NewGray(image.Rect(0, 0, side, side))
	// Light everywhere first, dark modules painted over: the quiet zone falls out of the
	// same fill rather than being a case.
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	for my := 0; my < sym.Size(); my++ {
		for mx := 0; mx < sym.Size(); mx++ {
			if !sym.Dark(mx, my) {
				continue
			}
			x0 := (mx + pngQuietZone) * pngScale
			y0 := (my + pngQuietZone) * pngScale
			for y := y0; y < y0+pngScale; y++ {
				for x := x0; x < x0+pngScale; x++ {
					img.SetGray(x, y, color.Gray{Y: 0})
				}
			}
		}
	}

	path := filepath.Join(dir, fmt.Sprintf("pairing-qr-%s.png", rendezvousID))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
