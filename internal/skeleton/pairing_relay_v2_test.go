package skeleton

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	remotecrypto "github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relaypurge"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

type relayPairAuthorizeFunc func(context.Context, ed25519.PublicKey, []byte) (relayv2.Binding, error)

func (f relayPairAuthorizeFunc) Authorize(ctx context.Context, pub ed25519.PublicKey, consent []byte) (relayv2.Binding, error) {
	return f(ctx, pub, consent)
}

func TestAuthorizeRelayPairLeavesWriteAheadCleanupOnAmbiguousFailure(t *testing.T) {
	stateDir := t.TempDir()
	machinePub := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	phoneSeed := make([]byte, ed25519.SeedSize)
	phoneSeed[0] = 1
	phonePub := ed25519.NewKeyFromSeed(phoneSeed).Public().(ed25519.PublicKey)
	consent := []byte("opaque-consent")
	want := errors.New("authorized response lost")
	cfg := &pairingConfig{RelayURL: "wss://relay.example", RelayAuthPub: machinePub}
	err := authorizeRelayPair(context.Background(), stateDir, cfg,
		relayPairAuthorizeFunc(func(context.Context, ed25519.PublicKey, []byte) (relayv2.Binding, error) {
			return relayv2.Binding{}, want
		}), phonePub, consent)
	if !errors.Is(err, want) {
		t.Fatalf("authorizeRelayPair = %v, want ambiguous failure", err)
	}
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 || pending[0].RoutingID != relayv2.RoutingID(phonePub) {
		t.Fatalf("write-ahead cleanup = (%+v, %v)", pending, err)
	}
	if err := authorizeRelayPair(context.Background(), stateDir, cfg,
		relayPairAuthorizeFunc(func(context.Context, ed25519.PublicKey, []byte) (relayv2.Binding, error) {
			return relayv2.Binding{MachineRID: relayv2.RoutingID(machinePub), PeerRID: relayv2.RoutingID(phonePub), Generation: 1}, nil
		}), phonePub, consent); err != nil {
		t.Fatal(err)
	}
	if pending, err = store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("confirmed authorization cleanup = (%+v, %v), want retired", pending, err)
	}
}

func TestAuthorizeRelayPairRetiresOnlyItsAttempt(t *testing.T) {
	stateDir := t.TempDir()
	machinePub := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	phoneSeed := make([]byte, ed25519.SeedSize)
	phoneSeed[0] = 2
	phonePub := ed25519.NewKeyFromSeed(phoneSeed).Public().(ed25519.PublicKey)
	consent := []byte("opaque-consent")
	cfg := &pairingConfig{RelayURL: "wss://relay.example", RelayAuthPub: machinePub}
	err := authorizeRelayPair(context.Background(), stateDir, cfg,
		relayPairAuthorizeFunc(func(context.Context, ed25519.PublicKey, []byte) (relayv2.Binding, error) {
			store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
			if err != nil {
				return relayv2.Binding{}, err
			}
			if _, err := store.Record(relayv2.RoutingID(phonePub), cfg.RelayURL, relayv2.RoutingID(machinePub), phonePub, consent); err != nil {
				return relayv2.Binding{}, err
			}
			return relayv2.Binding{MachineRID: relayv2.RoutingID(machinePub), PeerRID: relayv2.RoutingID(phonePub), Generation: 1}, nil
		}), phonePub, consent)
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("authorizeRelayPair = %v, want superseded cleanup", err)
	}
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 1 {
		t.Fatalf("pair authorization erased concurrent revoke cleanup: %+v, %v", pending, err)
	}
}

func TestAuthorizeRelayPairKeepsPairOnCleanupIOError(t *testing.T) {
	if err := relayPairCleanupOutcome(false, errors.New("dir sync")); err != nil {
		t.Fatalf("successful authorize became a pairing failure after cleanup I/O error: %v", err)
	}
	if err := relayPairCleanupOutcome(false, nil); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("clean stale cleanup = %v, want superseded error", err)
	}
}

func TestLoadPairingConfigUsesNativeRelayV2MachineControl(t *testing.T) {
	baseURL := os.Getenv("SKELETON_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("SKELETON_RELAY_V2_HTTP is set by the fresh-workerd pairing gate")
	}
	stateDir := t.TempDir()
	id := writeAllowedRelayV2Identity(t, stateDir, "relay-v2-machine")
	if err := relaycfg.Save(stateDir, relaycfg.Config{RelayURL: baseURL, OperatorNamespace: "local-test", TLSPolicy: relaycfg.PolicyWebPKI}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if got := relayv2.RoutingID(id.RelayAuthPublic()); got != "88564c8ede170d2ed321e21e61354184" {
		t.Fatalf("deterministic machine RID = %s", got)
	}

	sk := assemble(t)
	sk.api.pairing = cfg
	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})
	start := awaitControl(t, rc, protocol.OpPairStart)
	if start.Pairing == nil || start.Pairing.QR == "" {
		t.Fatalf("pair_start = %+v", start.Pairing)
	}
	qr, err := pairing.DecodeQR(start.Pairing.QR)
	if err != nil {
		t.Fatal(err)
	}
	if qr.RelayURL != baseURL {
		t.Fatalf("QR relay URL = %q, want configured %q", qr.RelayURL, baseURL)
	}
	ceremony := hex.EncodeToString(qr.RendezvousID[:])

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var phoneTransport *relayv2.PairTransport
	deadline := time.Now().Add(5 * time.Second)
	for {
		phoneTransport, err = relayv2.DialPair(ctx, relayv2.Profile{
			RelayURL: qr.RelayURL, Security: relay.Security{AllowLoopbackCleartext: true},
		}, ceremony)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("DialPair after machine create: %v", err)
	}
	defer phoneTransport.Close()
	ks, err := remotecrypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	static, err := ks.NoiseStatic()
	if err != nil {
		t.Fatal(err)
	}
	phoneSAS := make(chan [6]string, 1)
	phoneRID, err := hex.DecodeString(relayv2.RoutingID(ks.RelayAuthPublic()))
	if err != nil {
		t.Fatal(err)
	}
	deviceDone := make(chan error, 1)
	go func() {
		_, err := pairing.RunDevice(ctx, pairing.DeviceParams{
			Static: static, Secret: qr.PairingSecret, RendezvousID: qr.RendezvousID,
			Payload: pairing.DevicePayload{
				DeviceName: "Relay v2 Phone", DeviceRoutingID: phoneRID,
				DeviceRelayAuthPub: ks.RelayAuthPublic(), RecipientPub: ks.RecipientPublic(),
				DeviceCommandSignPub: ks.CommandSigningPublic(),
			},
			DeviceSAS: func(_ context.Context, sas [6]string) error { phoneSAS <- sas; return nil },
			Consent: func(machine pairing.MachinePayload) ([]byte, error) {
				sig, err := ks.SignRelayAuth(relayv2.ConsentMessage(ceremony, relayv2.RoutingID(machine.MachineRelayAuthPub)))
				if err != nil {
					return nil, err
				}
				return relayv2.MarshalConsent(ceremony, sig), nil
			},
		}, phoneTransport)
		deviceDone <- err
	}()

	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil {
		t.Fatal("pair_pending omitted pairing")
	}
	var phoneWords [6]string
	select {
	case phoneWords = <-phoneSAS:
	case <-ctx.Done():
		t.Fatal("phone did not derive SAS")
	}
	if machineSAS := strings.Join(pending.Pairing.SAS, " "); machineSAS != strings.Join(phoneWords[:], " ") {
		t.Fatalf("cross-end SAS mismatch: machine=%q phone=%q", machineSAS, strings.Join(phoneWords[:], " "))
	}
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})
	result := awaitControl(t, rc, protocol.OpPairResult)
	if result.Pairing == nil || result.Pairing.DeviceID == "" {
		t.Fatalf("pair_result = %+v", result.Pairing)
	}
	if err := <-deviceDone; err != nil {
		t.Fatalf("device pairing: %v", err)
	}
	records := sk.api.devices.List()
	if len(records) != 1 || records[0].Capability != device.CapFull {
		t.Fatalf("registry = %+v, want one full-capability device", records)
	}
	phoneStream, err := relayv2.Dial(ctx, relayv2.Profile{
		RelayURL: baseURL, MachineRID: relayv2.RoutingID(id.RelayAuthPublic()),
		OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true},
	}, relayv2.Auth{PublicKey: ks.RelayAuthPublic(), Sign: ks.SignRelayAuth,
		Role: relayv2.RolePhone, Purpose: relayv2.PurposeStream})
	if err != nil {
		t.Fatalf("phone stream immediately after pair: %v", err)
	}
	defer phoneStream.Close()
	binding, err := phoneStream.PhoneBinding()
	if err != nil || binding.Generation == 0 {
		t.Fatalf("phone binding = (%+v, %v), want a nonzero Worker generation", binding, err)
	}
}

func writeAllowedRelayV2Identity(t *testing.T, stateDir, hostname string) *machineid.Identity {
	t.Helper()
	epochKeys, err := remotecrypto.NewEpochKeys()
	if err != nil {
		t.Fatal(err)
	}
	var noise, recipient [32]byte
	for i := range noise {
		noise[i], recipient[i] = byte(64+i), byte(96+i)
	}
	relaySeed := make([]byte, ed25519.SeedSize)
	for i := range relaySeed {
		relaySeed[i] = byte(i)
	}
	grantSeed := make([]byte, ed25519.SeedSize)
	for i := range grantSeed {
		grantSeed[i] = byte(128 + i)
	}
	id := machineid.NewFromMaterial(hostname, machineid.Material{
		NoiseStaticPriv: noise, RecipientPriv: recipient,
		GrantSignPriv: ed25519.NewKeyFromSeed(grantSeed),
		RelayAuthPriv: ed25519.NewKeyFromSeed(relaySeed),
		EpochKeys:     epochKeys, EpochID: 1, GrantSeq: 1,
	})
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := id.Save(filepath.Join(remoteDir, remoteIdentityFile)); err != nil {
		t.Fatal(err)
	}
	return id
}
