package qrterm_test

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/qrterm"
)

// TestWritePosterForEmulator writes the pairing QR as a large PNG poster, for pointing the
// Android emulator's virtual-scene camera at. It is a TOOL, not an assertion about the
// product, so it only runs when SWARM_WRITE_POSTER names the output path:
//
//	SWARM_WRITE_POSTER=/tmp/qr-poster.png go test ./internal/remote/qrterm/ -run Poster
//
// The payload is a real EncodeQR string over a plausible relay URL, so the symbol under the
// camera is the same version and density the product actually draws.
func TestWritePosterForEmulator(t *testing.T) {
	path := os.Getenv("SWARM_WRITE_POSTER")
	if path == "" {
		t.Skip("SWARM_WRITE_POSTER unset: this test writes an emulator poster, it asserts nothing")
	}

	var p pairing.QRPayload
	p.RelayURL = "wss://192.168.1.50:8443"
	for i := range p.RendezvousID {
		p.RendezvousID[i] = byte(i)
	}
	for i := range p.PairingSecret {
		p.PairingSecret[i] = byte(0x40 + i)
	}
	payload, err := pairing.EncodeQR(p)
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	t.Logf("payload (%d bytes): %s", len(payload), payload)

	sym, err := qrterm.Encode(payload)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	t.Logf("symbol: %d modules, ECC %s", sym.Size(), sym.ECC())

	const (
		canvas    = 1536
		perModule = 16
		quietZone = 4
	)
	side := (sym.Size() + 2*quietZone) * perModule
	if side > canvas {
		t.Fatalf("symbol %d px does not fit a %d px canvas", side, canvas)
	}
	originX := (canvas - side) / 2
	originY := (canvas - side) / 2

	img := image.NewGray(image.Rect(0, 0, canvas, canvas))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for my := 0; my < sym.Size(); my++ {
		for mx := 0; mx < sym.Size(); mx++ {
			if !sym.Dark(mx, my) {
				continue
			}
			x0 := originX + (mx+quietZone)*perModule
			y0 := originY + (my+quietZone)*perModule
			for dy := 0; dy < perModule; dy++ {
				for dx := 0; dx < perModule; dx++ {
					img.SetGray(x0+dx, y0+dy, color.Gray{Y: 0})
				}
			}
		}
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	t.Logf("wrote %s: %dx%d canvas, symbol %d px at (%d,%d)", path, canvas, canvas, side, originX, originY)
}
