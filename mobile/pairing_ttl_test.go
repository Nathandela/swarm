package swarmmobile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// PB-PAIR-4 / section 6.0: the native relay-v2 Worker expires a ceremony
// after one minute. The phone's deadline starts when the QR is displayed, not
// when the user confirms it, so it can never outlive that authority.
func TestPBPAIR4_LocalPairingDeadlineIsOneMinuteFromDisplay(t *testing.T) {
	if pairingTTL != time.Minute {
		t.Fatalf("pairingTTL = %v, want the section 6.0/native relay-v2 pairing window %v", pairingTTL, time.Minute)
	}
	_, file, _, _ := runtime.Caller(0)
	worker, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(file)), "services", "relay", "src", "worker.mjs"))
	if err != nil || !strings.Contains(string(worker), "const PAIR_TTL_MS = 60_000;") {
		t.Fatal("native relay-v2 pairing window no longer matches the phone's one-minute deadline")
	}
	app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	qr, err := pairing.EncodeQR(pairing.QRPayload{RelayURL: "wss://relay.example", PairingSecret: [32]byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatal(err)
	}
	at, err := p.DeadlineMillis()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.UnixMilli(at)
	if got := deadline.Sub(before); got < pairingTTL-time.Second || got > pairingTTL+time.Second {
		t.Fatalf("pairing deadline starts %v from display, want %v", got, pairingTTL)
	}
}
