package relayv2

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	remotecrypto "github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestWorkerdNoiseMailboxReconnectReplayAndRevoke(t *testing.T) {
	baseURL := os.Getenv("RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("RELAY_V2_HTTP is set by services/relay/test/run.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	machinePub, machinePriv := deterministicKey(0)
	phonePub, phonePriv := deterministicKey(32)
	machineRID := RoutingID(machinePub)
	phoneRID := RoutingID(phonePub)
	profile := Profile{RelayURL: baseURL, MachineRID: machineRID, OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true}}

	control, err := Dial(ctx, profile, privateAuth(machinePub, machinePriv, RoleMachine, PurposeControl))
	if err != nil {
		t.Fatalf("Dial machine control: %v", err)
	}
	defer control.Close()

	var rendezvous [16]byte
	copy(rendezvous[:], []byte("noise-workerd-v2"))
	var secret [32]byte
	for i := range secret {
		secret[i] = byte(0xa0 + i)
	}
	machineIdentity, err := remotecrypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	phoneIdentity, err := remotecrypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ceremony := hex.EncodeToString(rendezvous[:])
	sas := make(chan [6]string, 1)
	machineTransport := NewMachinePairTransport(control)
	machine := pairing.NewMachine(pairing.MachineParams{
		Static:       machineIdentity.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rendezvous,
		LocalConsole: true,
		Confirm:      func(_ context.Context, got [6]string, _ string) (bool, error) { return got == <-sas, nil },
		Payload: pairing.MachinePayload{
			Hostname:            "workerd.test",
			MachineRoutingID:    mustHex(t, machineRID),
			MachineRelayAuthPub: machinePub,
			RecipientPub:        machineIdentity.RecipientPublic(),
			MachineSignPub:      machinePub,
			MachineEndpointID:   "workerd-machine",
			EpochID:             1,
		},
	})
	type machineResult struct {
		out *pairing.MachineOutcome
		err error
	}
	machineDone := make(chan machineResult, 1)
	go func() {
		out, err := machine.Pair(ctx, machineTransport)
		machineDone <- machineResult{out, err}
	}()
	select {
	case <-machineTransport.Created():
	case <-ctx.Done():
		t.Fatal("PAIR_CREATE did not complete")
	}
	phoneTransport, err := DialPair(ctx, Profile{RelayURL: profile.RelayURL, Security: relay.Security{AllowLoopbackCleartext: true}}, ceremony)
	if err != nil {
		t.Fatalf("DialPair: %v", err)
	}
	defer phoneTransport.Close()
	deviceDone := make(chan error, 1)
	go func() {
		_, err := pairing.RunDevice(ctx, pairing.DeviceParams{
			Static:       phoneIdentity.NoiseStatic(),
			Secret:       secret,
			RendezvousID: rendezvous,
			Payload: pairing.DevicePayload{
				DeviceName:           "Workerd Phone",
				DeviceRoutingID:      mustHex(t, phoneRID),
				DeviceRelayAuthPub:   phonePub,
				RecipientPub:         phoneIdentity.RecipientPublic(),
				DeviceCommandSignPub: phonePub,
			},
			DeviceSAS: func(_ context.Context, got [6]string) error { sas <- got; return nil },
			Consent: func(machine pairing.MachinePayload) ([]byte, error) {
				if !bytes.Equal(machine.MachineRelayAuthPub, machinePub) {
					return nil, errors.New("unexpected machine relay key")
				}
				sig := ed25519.Sign(phonePriv, ConsentMessage(ceremony, machineRID))
				return MarshalConsent(ceremony, sig), nil
			},
		}, phoneTransport)
		deviceDone <- err
	}()
	paired := <-machineDone
	if paired.err != nil {
		t.Fatalf("machine Noise pairing: %v", paired.err)
	}
	if err := <-deviceDone; err != nil {
		t.Fatalf("phone Noise pairing: %v", err)
	}
	if !bytes.Equal(paired.out.Device.DeviceRelayAuthPub, phonePub) {
		t.Fatal("Noise result did not authenticate phone relay key")
	}
	binding, err := control.Authorize(ctx, phonePub, paired.out.Device.ConsentSig)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if binding.PeerRID != phoneRID || binding.Generation == 0 {
		t.Fatalf("binding = %+v", binding)
	}

	machineStream := dialForTest(t, ctx, profile, privateAuth(machinePub, machinePriv, RoleMachine, PurposeStream))
	defer machineStream.Close()
	phoneStream := dialForTest(t, ctx, profile, privateAuth(phonePub, phonePriv, RolePhone, PurposeStream))
	phoneSub, err := phoneStream.Subscribe(ctx, binding, Checkpoint{})
	if err != nil {
		t.Fatalf("phone Subscribe: %v", err)
	}
	machineSub, err := machineStream.Subscribe(ctx, binding, Checkpoint{})
	if err != nil {
		t.Fatalf("machine Subscribe: %v", err)
	}

	keys, err := remotecrypto.NewEpochKeys()
	if err != nil {
		t.Fatal(err)
	}
	machineReceiver := remotecrypto.NewMailboxReceiver()
	phoneReceiver := remotecrypto.NewMailboxReceiver()
	phoneEffects := 0
	machineSender := remotecrypto.KeyID(machinePub)
	phoneSender := remotecrypto.KeyID(phonePub)
	first := seal(t, keys.ContentKey, machineSender, 1, "machine-one")
	if _, err := machineStream.Append(ctx, binding, "machine-1", first); err != nil {
		t.Fatalf("machine Append: %v", err)
	}
	delivery := receivePlain(t, ctx, phoneSub, phoneReceiver, keys.ContentKey, "machine-one")
	phoneEffects++
	if err := phoneSub.Ack(ctx, delivery.Cursor); err != nil {
		t.Fatalf("phone Ack: %v", err)
	}
	phoneOne := seal(t, keys.ContentKey, phoneSender, 1, "phone-one")
	if _, err := phoneStream.Append(ctx, binding, "phone-1", phoneOne); err != nil {
		t.Fatalf("phone Append: %v", err)
	}
	machineDelivery := receivePlain(t, ctx, machineSub, machineReceiver, keys.ContentKey, "phone-one")
	if err := machineSub.Ack(ctx, machineDelivery.Cursor); err != nil {
		t.Fatalf("machine Ack: %v", err)
	}

	checkpoint := Checkpoint{Incarnation: phoneSub.Incarnation(), Cursor: delivery.Cursor}
	phoneStream.Close()
	second := seal(t, keys.ContentKey, machineSender, 2, "machine-two")
	if _, err := machineStream.Append(ctx, binding, "machine-2", second); err != nil {
		t.Fatalf("offline Append: %v", err)
	}
	phoneStream = dialForTest(t, ctx, profile, privateAuth(phonePub, phonePriv, RolePhone, PurposeStream))
	defer phoneStream.Close()
	phoneSub, err = phoneStream.Subscribe(ctx, binding, checkpoint)
	if err != nil {
		t.Fatalf("reconnect Subscribe: %v", err)
	}
	delivery = receivePlain(t, ctx, phoneSub, phoneReceiver, keys.ContentKey, "machine-two")
	phoneEffects++
	if err := phoneSub.Ack(ctx, delivery.Cursor); err != nil {
		t.Fatalf("reconnect Ack: %v", err)
	}

	for seq := uint64(3); seq <= 259; seq++ {
		body := seal(t, keys.ContentKey, machineSender, seq, "burst")
		if _, err := machineStream.Append(ctx, binding, messageID(seq), body); err != nil {
			t.Fatalf("burst Append %d: %v", seq, err)
		}
		d := receivePlain(t, ctx, phoneSub, phoneReceiver, keys.ContentKey, "burst")
		phoneEffects++
		if err := phoneSub.Ack(ctx, d.Cursor); err != nil {
			t.Fatalf("burst Ack %d: %v", seq, err)
		}
	}
	replayed, err := machineStream.Append(ctx, binding, "machine-1", first)
	if err != nil {
		t.Fatalf("old retry Append: %v", err)
	}
	if replayed.Deduped {
		t.Fatal("transport promised dedupe beyond its 256-ACKed-receipt window")
	}
	retry, err := phoneSub.Recv(ctx)
	if err != nil {
		t.Fatalf("receive old retry: %v", err)
	}
	envelope, err := remotecrypto.ParseEnvelope(retry.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phoneReceiver.Accept(keys.ContentKey, envelope); !errors.Is(err, remotecrypto.ErrStaleSeq) {
		t.Fatalf("endpoint replay result = %v, want ErrStaleSeq", err)
	}
	if phoneEffects != 259 {
		t.Fatalf("delayed retry changed semantic effect count: got %d, want 259", phoneEffects)
	}
	if err := phoneSub.Ack(ctx, retry.Cursor); err != nil {
		t.Fatalf("Ack old retry: %v", err)
	}

	if err := control.Revoke(ctx, binding); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	select {
	case <-phoneStream.Done():
	case <-ctx.Done():
		t.Fatal("revocation did not close the phone stream")
	}
}

func TestExpiredReceiptBeyondCleanupBatchDoesNotSuppressRetry(t *testing.T) {
	baseURL := os.Getenv("RELAY_V2_EXPIRY_HTTP")
	if baseURL == "" {
		t.Skip("RELAY_V2_EXPIRY_HTTP is set by services/relay/test/run.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	machinePub, machinePriv := deterministicKey(0)
	phonePub, phonePriv := deterministicKey(32)
	machineRID := RoutingID(machinePub)
	profile := Profile{RelayURL: baseURL, MachineRID: machineRID, OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true}}
	control := dialForTest(t, ctx, profile, privateAuth(machinePub, machinePriv, RoleMachine, PurposeControl))
	defer control.Close()
	ceremony := "33" + "333333333333333333333333333333"
	credential := MarshalConsent(ceremony, ed25519.Sign(phonePriv, ConsentMessage(ceremony, machineRID)))
	binding, err := control.Authorize(ctx, phonePub, credential)
	if err != nil {
		t.Fatal(err)
	}
	stream := dialForTest(t, ctx, profile, privateAuth(machinePub, machinePriv, RoleMachine, PurposeStream))
	defer stream.Close()
	body := []byte("opaque-ciphertext")
	for i := uint64(0); i < 300; i++ {
		if _, err := stream.Append(ctx, binding, "expiry-"+formatUint64(i), body); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	time.Sleep(3100 * time.Millisecond)
	retry, err := stream.Append(ctx, binding, "expiry-299", body)
	if err != nil {
		t.Fatalf("expired retry: %v", err)
	}
	if retry.Deduped || retry.Cursor != 301 {
		t.Fatalf("expired receipt suppressed retry outside GC batch: %+v", retry)
	}
}

func deterministicKey(start byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = start + byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

func privateAuth(pub ed25519.PublicKey, priv ed25519.PrivateKey, role Role, purpose Purpose) Auth {
	return Auth{PublicKey: pub, Role: role, Purpose: purpose, Sign: func(message []byte) ([]byte, error) {
		return ed25519.Sign(priv, message), nil
	}}
}

func dialForTest(t *testing.T, ctx context.Context, profile Profile, auth Auth) *Conn {
	t.Helper()
	c, err := Dial(ctx, profile, auth)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	b, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seal(t *testing.T, key remotecrypto.ContentKey, sender [8]byte, seq uint64, plaintext string) []byte {
	t.Helper()
	envelope, err := remotecrypto.SealMailbox(key, remotecrypto.EnvelopeHeader{
		Version: remotecrypto.VersionV1, EpochID: 1, Seq: seq, SenderKeyID: sender, IssuedAt: time.Now().UnixMilli(),
	}, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Marshal()
}

func receivePlain(t *testing.T, ctx context.Context, sub *Subscription, receiver *remotecrypto.MailboxReceiver, key remotecrypto.ContentKey, want string) Delivery {
	t.Helper()
	d, err := sub.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	envelope, err := remotecrypto.ParseEnvelope(d.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	got, err := receiver.Accept(key, envelope)
	if err != nil {
		t.Fatalf("accept mailbox: %v", err)
	}
	if string(got.Plaintext) != want {
		t.Fatalf("plaintext = %q, want %q", got.Plaintext, want)
	}
	return d
}

func messageID(seq uint64) string {
	return "machine-burst-" + formatUint64(seq)
}
