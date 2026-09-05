package skeleton

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func skeletonPushBinding() (*pairing.PushBinding, crypto.WakeKey, phonecore.PushAddress) {
	var key crypto.WakeKey
	var addr phonecore.PushAddress
	for i := range key {
		key[i] = byte(0x41 + i)
	}
	for i := range addr {
		addr[i] = byte(0x71 + i)
	}
	capability := func(fill byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	}
	return &pairing.PushBinding{
		WakeKey:                 append([]byte(nil), key[:]...),
		PushAddress:             append([]byte(nil), addr[:]...),
		SubmitCapability:        capability(0x51),
		MachineRevokeCapability: capability(0x52),
		CapabilityRecordVersion: 1,
	}, key, addr
}

func TestVerifyPairingPushBinding_SubmitsAuthenticatedSeqOneAndRequiresProviderAccepted(t *testing.T) {
	binding, key, addr := skeletonPushBinding()
	phone, err := phonecore.Resume(phonecore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := phone.AdoptPushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	requests := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil && r.URL.Path != "/v1/wakes" {
			err = io.ErrUnexpectedEOF
		}
		if err == nil && r.Header.Get("Authorization") != "Swarm-Capability "+binding.SubmitCapability {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			err = phone.AcceptWakeV1(body)
		}
		requests <- err
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"provider_accepted"}`))
	}))
	defer server.Close()
	cfg := &pairingConfig{PushGatewayURL: server.URL, PushHTTPClient: server.Client()}
	stateDir := t.TempDir()
	if err := verifyPairingPushBinding(context.Background(), stateDir, cfg, binding); err != nil {
		t.Fatal(err)
	}
	if err := <-requests; err != nil {
		t.Fatalf("test wake was not a genuine seq=1 envelope for the conveyed address/key: %v", err)
	}
	seq, err := remotegw.OpenSeqSource(remotegw.WakeSeqPath(filepath.Join(stateDir, "remote"), remotegw.PushAddress(addr)))
	if err != nil {
		t.Fatal(err)
	}
	if seq.Issued() < 1 {
		t.Fatal("test wake sequence was not reserved durably")
	}
}

func TestVerifyPairingPushBinding_RejectsNonAcceptedOutcome(t *testing.T) {
	binding, _, _ := skeletonPushBinding()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	}))
	defer server.Close()
	cfg := &pairingConfig{PushGatewayURL: server.URL, PushHTTPClient: server.Client()}
	if err := verifyPairingPushBinding(context.Background(), t.TempDir(), cfg, binding); err == nil ||
		!strings.Contains(err.Error(), "provider_accepted") {
		t.Fatalf("non-accepted gateway outcome = %v", err)
	}
}

func TestVerifyPairingPushBinding_RejectsRegistryInvalidAuthorityBeforeNetwork(t *testing.T) {
	valid, _, _ := skeletonPushBinding()
	for _, tc := range []struct {
		name   string
		mutate func(*pairing.PushBinding)
	}{
		{"malformed machine revoke", func(b *pairing.PushBinding) { b.MachineRevokeCapability = "not-base64" }},
		{"same submit and revoke", func(b *pairing.PushBinding) { b.MachineRevokeCapability = b.SubmitCapability }},
		{"unsupported capability version", func(b *pairing.PushBinding) { b.CapabilityRecordVersion = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := *valid
			binding.WakeKey = append([]byte(nil), valid.WakeKey...)
			binding.PushAddress = append([]byte(nil), valid.PushAddress...)
			tc.mutate(&binding)
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			defer server.Close()
			cfg := &pairingConfig{PushGatewayURL: server.URL, PushHTTPClient: server.Client()}
			if err := verifyPairingPushBinding(context.Background(), t.TempDir(), cfg, &binding); err == nil {
				t.Fatal("invalid authority reached pairing acceptance")
			}
			if requests != 0 {
				t.Fatalf("invalid authority reached gateway verification: %d request(s)", requests)
			}
		})
	}
}

func TestPairingPushRecord_PersistsConveyedWakeKeyAndGatewayNotEpochMaterial(t *testing.T) {
	binding, wake, addr := skeletonPushBinding()
	rec := pairingPushRecord("https://push-swarm.dsfactory.org", binding)
	if err := device.ValidatePushBinding(rec); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.WakeKey, wake[:]) || !bytes.Equal(rec.Address, addr[:]) {
		t.Fatalf("persisted binding lost conveyed authority: %+v", rec)
	}
	if rec.GatewayURL != "https://push-swarm.dsfactory.org" || rec.Transport != device.PushTransportGateway {
		t.Fatalf("persisted gateway coordinates = %+v", rec)
	}
	epoch := crypto.WakeKey{0xff}
	if bytes.Equal(rec.WakeKey, epoch[:]) {
		t.Fatal("persisted pairing wake key was derived from epoch material")
	}
}

func TestBeginPairing_AdvertisesPushOnlyWithConfiguredProductionGateway(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"configured", "https://push-swarm.dsfactory.org", true},
		{"absent", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			reg, err := device.Open(stateDir + "/devices")
			if err != nil {
				t.Fatal(err)
			}
			id, err := crypto.GenerateIdentity()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			api := &coreAPI{stateDir: stateDir, devices: reg, clock: time.Now}
			api.pairing = &pairingConfig{
				Static: id.NoiseStatic(), RelayURL: "ws://127.0.0.1:1", OperatorNamespace: "owner",
				PushGatewayURL: tc.url,
				NewRendezvous: func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
					machine, _ := rendezvousPair()
					return machine, nil
				},
			}
			view, err := api.BeginPairing(ctx, protocol.PairStartReq{Capability: "full"},
				func([]string, string) (bool, error) { return false, nil }, func(protocol.PairResult) {})
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := pairing.DecodeQR(view.QR)
			if err != nil {
				t.Fatal(err)
			}
			got := decoded.Flags&pairing.QRFlagPushBinding != 0
			if got != tc.want {
				t.Fatalf("push QR capability=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadPairingConfig_ProductionPushEndpointReachesBeginPairingCapability(t *testing.T) {
	stateDir := t.TempDir()
	writeTestIdentity(t, stateDir, "push-config-machine")
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL: "ws://127.0.0.1:1", OperatorNamespace: "owner", PushGatewayURL: "https://push-swarm.dsfactory.org",
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PushGatewayURL != "https://push-swarm.dsfactory.org" {
		t.Fatalf("production pairing config lost push endpoint: %q", cfg.PushGatewayURL)
	}
	cfg.NewRendezvous = func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
		machine, _ := rendezvousPair()
		return machine, nil
	}
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	api := &coreAPI{stateDir: stateDir, devices: reg, clock: time.Now, pairing: cfg}
	view, err := api.BeginPairing(context.Background(), protocol.PairStartReq{Capability: "full"},
		func([]string, string) (bool, error) { return false, nil }, func(protocol.PairResult) {})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := pairing.DecodeQR(view.QR)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Flags&pairing.QRFlagPushBinding == 0 {
		t.Fatal("production-loaded endpoint did not advertise push binding support")
	}
}

func TestBeginPairing_NegotiatedPushStagesBeforeTestWakeAndPersistsWithGrant(t *testing.T) {
	var wakeRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/wakes" {
			wakeRequests.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"provider_accepted"}`))
	}))
	defer server.Close()

	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)
	sk.api.pairing.PushGatewayURL = server.URL
	sk.api.pairing.PushHTTPClient = server.Client()
	sk.api.pushHTTPClient = server.Client()

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})
	reply := awaitControl(t, rc, protocol.OpPairStart)
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatal(err)
	}
	if qp.Flags&pairing.QRFlagPushBinding == 0 {
		t.Fatal("configured production pairing did not negotiate push")
	}
	dEnd := recvDeviceEnd(t, deviceEnds)
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	static, err := ks.NoiseStatic()
	if err != nil {
		t.Fatal(err)
	}
	binding, _, _ := skeletonPushBinding()
	done := make(chan error, 1)
	go func() {
		_, err := pairing.RunDevice(context.Background(), pairing.DeviceParams{
			Static: static, Secret: qp.PairingSecret, RendezvousID: qp.RendezvousID,
			Payload: pairing.DevicePayload{
				DeviceName: "push-phone", DeviceRoutingID: bytes.Repeat([]byte{0x11}, 16),
				DeviceRelayAuthPub: ks.RelayAuthPublic(), RecipientPub: ks.RecipientPublic(),
				DeviceCommandSignPub: ks.CommandSigningPublic(),
			},
			Consent:            phoneConsentFor(ks, qp.RendezvousID),
			RequestPushBinding: true,
			PreparePushBinding: func(context.Context) (*pairing.PushBinding, func(), error) {
				return binding, func() {}, nil
			},
		}, dEnd)
		done <- err
	}()
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("missing pairing SAS: %+v", pending.Pairing)
	}
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})
	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing == nil || res.Pairing.DeviceID == "" {
		t.Fatalf("negotiated pairing failed: %+v", res.Pairing)
	}
	if err := <-done; err != nil {
		t.Fatalf("device pairing: %v", err)
	}
	if wakeRequests.Load() != 1 {
		t.Fatalf("test wake requests=%d, want 1", wakeRequests.Load())
	}
	rec, ok := sk.api.devices.Get(res.Pairing.DeviceID)
	if !ok || rec.Push == nil || !bytes.Equal(rec.Push.WakeKey, binding.WakeKey) {
		t.Fatalf("registry lost negotiated push authority: %+v", rec.Push)
	}
	if g, err := grant.Load(sk.api.registryDir(), res.Pairing.DeviceID); err != nil || g == nil {
		t.Fatalf("owned push row has no committed grant: %v, %v", g, err)
	}
	if _, ok := sk.api.pushRevokeCustody.Pending(); ok {
		t.Fatal("successful registry+grant commit left revoke custody pending")
	}
}
