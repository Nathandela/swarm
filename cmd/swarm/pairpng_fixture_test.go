package main

// The cross-language fence (committee, Opus's ruling): Go WRITES the pairing PNG, Kotlin
// DECODES it, and no toolchain sees both sides -- so the exact artifact the product emits is
// COMMITTED, and each side is pinned to it. This test is the Go half: the committed fixture
// carries exactly the symbol writePairingPNG produces for the fixed payload below. The JVM
// half (android/app/src/test) feeds the same bytes through FrameDecoder and must get the
// payload text back, which is read from the sibling .payload.txt so neither side transcribes
// the other's constant.
//
// Regenerate DELIBERATELY with -update-pairing-fixture when the artifact geometry changes,
// and expect the JVM side to be re-run in the same commit.

import (
	"bytes"
	"flag"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/qrterm"
)

var updatePairingFixture = flag.Bool("update-pairing-fixture", false,
	"rewrite the committed pairing PNG fixture and its payload sidecar")

const fixtureRelDir = "../../android/app/src/test/resources"

func fixturePayload(t *testing.T) string {
	t.Helper()
	var rvz [16]byte
	var secret [32]byte
	for i := range rvz {
		rvz[i] = byte(i + 1)
	}
	for i := range secret {
		secret[i] = byte(0xA0 + i)
	}
	payload, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      "wss://relay.example:8443",
		RendezvousID:  rvz,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	return payload
}

func TestPairingPNGFixture_IsTheArtifactTheProductEmits(t *testing.T) {
	payload := fixturePayload(t)
	pngPath := filepath.Join(fixtureRelDir, "pairing-qr-fixture.png")
	txtPath := filepath.Join(fixtureRelDir, "pairing-qr-fixture.payload.txt")

	if *updatePairingFixture {
		tmp := t.TempDir()
		p, err := writePairingPNG(payload, tmp, "fixture")
		if err != nil {
			t.Fatalf("writePairingPNG: %v", err)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(fixtureRelDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pngPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(txtPath, []byte(payload+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sidecar, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("no committed payload sidecar (%v); run -update-pairing-fixture once", err)
	}
	if got := string(bytes.TrimRight(sidecar, "\n")); got != payload {
		t.Fatalf("the sidecar payload does not match this test's fixed payload:\n got %q\nwant %q",
			got, payload)
	}

	raw, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatalf("no committed fixture (%v); run -update-pairing-fixture once", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("committed fixture does not decode as PNG: %v", err)
	}
	sym, err := qrterm.Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	const scale, qz = pngScale, pngQuietZone
	wantSide := (sym.Size() + 2*qz) * scale
	if img.Bounds().Dx() != wantSide || img.Bounds().Dy() != wantSide {
		t.Fatalf("fixture is %dx%d, want %dx%d: the committed artifact has drifted from the "+
			"writer; regenerate deliberately", img.Bounds().Dx(), img.Bounds().Dy(), wantSide, wantSide)
	}
	for my := -qz; my < sym.Size()+qz; my++ {
		for mx := -qz; mx < sym.Size()+qz; mx++ {
			r, g, b, _ := img.At((mx+qz)*scale+scale/2, (my+qz)*scale+scale/2).RGBA()
			dark := r < 0x4000 && g < 0x4000 && b < 0x4000
			if dark != sym.Dark(mx, my) {
				t.Fatalf("fixture module (%d,%d) disagrees with the writer's symbol; the "+
					"committed artifact has drifted", mx, my)
			}
		}
	}
}
