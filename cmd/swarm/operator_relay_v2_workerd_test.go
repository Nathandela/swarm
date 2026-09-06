package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	remotecrypto "github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relaypurge"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

func operatorV2Identity(t *testing.T, stateDir string) *machineid.Identity {
	t.Helper()
	keys, err := remotecrypto.NewEpochKeys()
	if err != nil {
		t.Fatal(err)
	}
	var noise, recipient [32]byte
	for i := range noise {
		noise[i], recipient[i] = byte(64+i), byte(96+i)
	}
	relaySeed, grantSeed := make([]byte, 32), make([]byte, 32)
	for i := range relaySeed {
		relaySeed[i], grantSeed[i] = byte(i), byte(128+i)
	}
	id := machineid.NewFromMaterial("operator-v2", machineid.Material{
		NoiseStaticPriv: noise, RecipientPriv: recipient,
		RelayAuthPriv: ed25519.NewKeyFromSeed(relaySeed), GrantSignPriv: ed25519.NewKeyFromSeed(grantSeed),
		EpochKeys: keys, EpochID: 1, GrantSeq: 1,
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

func operatorV2Phone(first byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = first + byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

func operatorV2Consent(priv ed25519.PrivateKey, ceremony, machineRID string) []byte {
	return relayv2.MarshalConsent(ceremony, ed25519.Sign(priv, relayv2.ConsentMessage(ceremony, machineRID)))
}

func TestOperatorRelayV2RevokeAndDeferredRetry(t *testing.T) {
	baseURL := os.Getenv("OPERATOR_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("OPERATOR_RELAY_V2_HTTP is set by the fresh-workerd operator gate")
	}
	stateDir := t.TempDir()
	id := operatorV2Identity(t, stateDir)
	relayConfig := relaycfg.Config{RelayURL: baseURL, OperatorNamespace: "local-test", TLSPolicy: relaycfg.PolicyWebPKI}
	if err := relaycfg.Save(stateDir, relayConfig); err != nil {
		t.Fatal(err)
	}
	relaySecurity, err := relayConfig.Security()
	if err != nil {
		t.Fatal(err)
	}
	machineRID := relayv2.RoutingID(id.RelayAuthPublic())
	profile := relayv2.Profile{RelayURL: baseURL, MachineRID: machineRID, OperatorNamespace: "local-test", Security: relaySecurity}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	authorize := func(pub ed25519.PublicKey, consent []byte) relayv2.Binding {
		t.Helper()
		var binding relayv2.Binding
		err := withMachineRelay(stateDir, func(ctx context.Context, control *relayv2.Conn) error {
			var err error
			binding, err = control.Authorize(ctx, pub, consent)
			return err
		})
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
		return binding
	}
	phoneDial := func(pub ed25519.PublicKey, priv ed25519.PrivateKey) (*relayv2.Conn, error) {
		return relayv2.Dial(ctx, profile, relayv2.Auth{PublicKey: pub,
			Sign: func(message []byte) ([]byte, error) { return ed25519.Sign(priv, message), nil },
			Role: relayv2.RolePhone, Purpose: relayv2.PurposeStream})
	}

	pub, priv := operatorV2Phone(32)
	consent := operatorV2Consent(priv, "11111111111111111111111111111111", machineRID)
	binding := authorize(pub, consent)
	phone, err := phoneDial(pub, priv)
	if err != nil {
		t.Fatalf("phone auth before revoke: %v", err)
	}
	if verdict, err := purgeRelayState(stateDir, binding.PeerRID, pub, consent); err != nil || verdict != relayPurgeDone {
		t.Fatalf("direct owner revoke = (%v, %v), want done", verdict, err)
	}
	select {
	case <-phone.Done():
	case <-ctx.Done():
		t.Fatal("revoke did not close the live phone stream")
	}
	if retry, err := purgeRelayState(stateDir, binding.PeerRID, pub, consent); err != nil || retry != relayPurgeNone {
		t.Fatalf("response-loss retry = (%v, %v), want already settled", retry, err)
	}

	deferredPub, deferredPriv := operatorV2Phone(64)
	deferredConsent := operatorV2Consent(deferredPriv, "22222222222222222222222222222222", machineRID)
	deferred := authorize(deferredPub, deferredConsent)
	store, err := relaypurge.Open(relaypurge.StorePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(deferred.PeerRID, baseURL, machineRID, deferredPub, deferredConsent); err != nil {
		t.Fatal(err)
	}
	if left := relaypurge.DriveMachineObligations(stateDir, t.Logf); left != 0 {
		t.Fatalf("deferred v2 purge left %d obligation(s)", left)
	}
	if _, err := phoneDial(deferredPub, deferredPriv); !relayV2Code(err, "not_authorized") {
		t.Fatalf("deferred purge phone auth = %v, want not_authorized", err)
	}

	oldPub, oldPriv := operatorV2Phone(96)
	oldConsent := operatorV2Consent(oldPriv, "33333333333333333333333333333333", machineRID)
	old := authorize(oldPub, oldConsent)
	if _, err := store.Record(old.PeerRID, baseURL, machineRID, oldPub, oldConsent); err != nil {
		t.Fatal(err)
	}
	newConsent := operatorV2Consent(oldPriv, "44444444444444444444444444444444", machineRID)
	newBinding := authorize(oldPub, newConsent)
	if newBinding.Generation <= old.Generation {
		t.Fatalf("replacement generation = %d, old = %d", newBinding.Generation, old.Generation)
	}
	if left := relaypurge.DriveMachineObligations(stateDir, t.Logf); left != 0 {
		t.Fatalf("retired old-consent obligation left %d", left)
	}
	current, err := phoneDial(oldPub, oldPriv)
	if err != nil {
		t.Fatalf("stale purge revoked replacement generation: %v", err)
	}
	current.Close()
}

func TestRelayDoctorV2UsesConfiguredMachineAndKeepsLiveStream(t *testing.T) {
	baseURL := os.Getenv("OPERATOR_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("OPERATOR_RELAY_V2_HTTP is set by the fresh-workerd operator gate")
	}
	stateDir := t.TempDir()
	operatorV2Identity(t, stateDir)
	if err := relaycfg.Save(stateDir, relaycfg.Config{RelayURL: baseURL, OperatorNamespace: "local-test", TLSPolicy: relaycfg.PolicyWebPKI}); err != nil {
		t.Fatal(err)
	}
	id, err := machineid.Load(filepath.Join(stateDir, "remote", remoteIdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := relayv2.Dial(ctx, relayv2.Profile{RelayURL: baseURL,
		MachineRID: relayv2.RoutingID(id.RelayAuthPublic()), OperatorNamespace: "local-test",
		Security: relay.Security{AllowLoopbackCleartext: true}}, relayv2.Auth{PublicKey: id.RelayAuthPublic(),
		Sign: func(message []byte) ([]byte, error) { return id.RelayAuthSign(message), nil },
		Role: relayv2.RoleMachine, Purpose: relayv2.PurposeStream})
	if err != nil {
		t.Fatalf("open live machine stream: %v", err)
	}
	defer stream.Close()
	t.Setenv(daemon.EnvStateDir, stateDir)
	for run := 1; run <= 2; run++ {
		var stdout, stderr bytes.Buffer
		if code := runRelay([]string{"doctor"}, &stdout, &stderr); code != 0 {
			t.Fatalf("doctor run %d = %d, stdout=%s stderr=%s", run, code, stdout.String(), stderr.String())
		}
		for _, step := range []string{"DNS resolution", "TCP+TLS", "Relay-v2 edge", "Relay-v2 rendezvous"} {
			if got := doctorStepStatus(t, stdout.String(), step); got != statusOK {
				t.Fatalf("doctor run %d step %q = %q, output=%s", run, step, got, stdout.String())
			}
		}
		select {
		case <-stream.Done():
			t.Fatal("doctor control probe superseded the live machine stream")
		default:
		}
	}
}
