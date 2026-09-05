package skeleton

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

func TestPBOPS5_DaemonPairingPassesConfiguredPinToRelayV2Dial(t *testing.T) {
	want := sha256.Sum256([]byte("configured relay public key"))
	stateDir := t.TempDir()
	writeTestIdentity(t, stateDir, "b34.local")
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL: "wss://relay.example.test", OperatorNamespace: "owner",
		SPKIPin: base64.StdEncoding.EncodeToString(want[:]),
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	stopped := errors.New("dial observed")
	original := dialRelayV2MachineControl
	t.Cleanup(func() { dialRelayV2MachineControl = original })
	var got relayv2.Profile
	dialRelayV2MachineControl = func(_ context.Context, profile relayv2.Profile, _ relayv2.Auth) (*relayv2.Conn, error) {
		got = profile
		return nil, stopped
	}
	if _, err := cfg.NewRendezvous(context.Background(), [16]byte{}); !errors.Is(err, stopped) {
		t.Fatalf("NewRendezvous error = %v, want injected dial stop", err)
	}
	if !bytes.Equal(got.Security.PinnedSPKISHA256, want[:]) {
		t.Fatalf("relay-v2 dial pin = %x, want configured %x", got.Security.PinnedSPKISHA256, want)
	}
}
