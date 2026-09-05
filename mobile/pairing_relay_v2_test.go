package swarmmobile

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	remotecrypto "github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

func TestRelayV2PairingErrorsMapToNativeStates(t *testing.T) {
	for code, want := range map[string]string{
		"pairing_not_found":      pairExpired,
		"pairing_rate_limited":   pairRateLimited,
		"pairing_full":           pairRateLimited,
		"pairing_directory_full": pairRateLimited,
		"not_authorized":         "",
	} {
		err := fmt.Errorf("pairing transport: %w", &relayv2.ProtocolError{Code: code})
		if got := relayV2PairState(err); got != want {
			t.Errorf("relayV2PairState(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestPairingUsesNativeRelayV2Transport(t *testing.T) {
	baseURL := os.Getenv("MOBILE_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("MOBILE_RELAY_V2_HTTP is set by the fresh-workerd pairing gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	machineSeed := make([]byte, ed25519.SeedSize)
	for i := range machineSeed {
		machineSeed[i] = byte(i)
	}
	machinePriv := ed25519.NewKeyFromSeed(machineSeed)
	machinePub := machinePriv.Public().(ed25519.PublicKey)
	machineRID := relayv2.RoutingID(machinePub)
	if machineRID != "88564c8ede170d2ed321e21e61354184" {
		t.Fatalf("deterministic machine RID = %s", machineRID)
	}
	profile := relayv2.Profile{RelayURL: baseURL, MachineRID: machineRID,
		OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true}}
	control, err := relayv2.Dial(ctx, profile, relayv2.Auth{
		PublicKey: machinePub, Role: relayv2.RoleMachine, Purpose: relayv2.PurposeControl,
		Sign: func(message []byte) ([]byte, error) { return ed25519.Sign(machinePriv, message), nil },
	})
	if err != nil {
		t.Fatalf("Dial machine control: %v", err)
	}
	defer control.Close()

	machineIdentity, err := remotecrypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	machineSignPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var ceremonyID [16]byte
	var secret [32]byte
	if _, err := rand.Read(ceremonyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	machineSAS := make(chan [6]string, 1)
	approveMachine := make(chan struct{})
	transport := relayv2.NewMachinePairTransport(control)
	type machineResult struct {
		out *pairing.MachineOutcome
		err error
	}
	machineDone := make(chan machineResult, 1)
	go func() {
		out, err := pairing.NewMachine(pairing.MachineParams{
			Static: machineIdentity.NoiseStatic(), Secret: secret, RendezvousID: ceremonyID,
			LocalConsole: true,
			Confirm: func(ctx context.Context, sas [6]string, _ string) (bool, error) {
				machineSAS <- sas
				select {
				case <-approveMachine:
					return true, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			},
			Payload: pairing.MachinePayload{
				Hostname: "workerd-mobile.test", MachineRoutingID: mustDecodeRID(t, machineRID),
				MachineRelayAuthPub: machinePub, RecipientPub: machineIdentity.RecipientPublic(),
				MachineSignPub: machineSignPub, MachineEndpointID: "workerd-mobile",
				RelayTLSPolicy: "webpki", OperatorNamespace: "local-test", EpochID: 1,
			},
		}).Pair(ctx, transport)
		machineDone <- machineResult{out: out, err: err}
	}()
	select {
	case <-transport.Created():
	case <-ctx.Done():
		t.Fatal("machine did not create relay-v2 ceremony")
	}
	qr, err := pairing.EncodeQR(pairing.QRPayload{RelayURL: baseURL, RendezvousID: ceremonyID, PairingSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(&Config{StateDir: t.TempDir(), RelayURL: baseURL}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer func() { _ = app.Close() }()
	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	origin, err := p.Origin()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatalf("ConfirmOrigin: %v", err)
	}
	var phoneSAS string
	for phoneSAS == "" {
		phoneSAS, err = p.SAS()
		if err == nil && phoneSAS != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("phone did not derive SAS: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	var machineWords [6]string
	select {
	case machineWords = <-machineSAS:
	case <-ctx.Done():
		t.Fatal("machine did not derive SAS")
	}
	if want := strings.Join(machineWords[:], " "); phoneSAS != want {
		t.Fatalf("cross-end SAS mismatch: phone=%q machine=%q", phoneSAS, want)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm phone SAS: %v", err)
	}
	close(approveMachine)
	var paired machineResult
	select {
	case paired = <-machineDone:
	case <-ctx.Done():
		t.Fatal("machine did not receive the pairing ACK")
	}
	if paired.err != nil {
		t.Fatalf("machine pairing: %v", paired.err)
	}
	if paired.out == nil {
		t.Fatal("machine pairing returned no authenticated device")
	}
	if _, err := control.Authorize(ctx, ed25519.PublicKey(paired.out.Device.DeviceRelayAuthPub), paired.out.Device.ConsentSig); err != nil {
		t.Fatalf("relay-v2 rejected mobile consent: %v", err)
	}
	for {
		state, err := p.State()
		if err == nil && state == pairPaired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("phone did not publish paired state: state=%q err=%v", state, err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func mustDecodeRID(t *testing.T, rid string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(rid)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
