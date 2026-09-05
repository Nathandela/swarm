package pairing

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func negotiatedBinding() *PushBinding {
	return &PushBinding{
		WakeKey:                 make([]byte, 32),
		PushAddress:             make([]byte, 16),
		SubmitCapability:        "submit-capability",
		MachineRevokeCapability: "machine-revoke-capability",
		CapabilityRecordVersion: 1,
	}
}

func TestPushBindingNegotiation_NewPeersPrepareAndVerifyOnlyAfterAgreement(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid, secret := fill16(0x91), fill32(0x92)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	mp.PushBindingSupport = true
	var staged atomic.Int32
	mp.StagePushBinding = func(_ context.Context, b *PushBinding, payload DevicePayload) error {
		if b == nil || payload.DeviceName == "" {
			return errors.New("stager did not receive the authenticated binding and device payload")
		}
		staged.Add(1)
		return nil
	}
	var verified atomic.Int32
	mp.VerifyPushBinding = func(_ context.Context, b *PushBinding) error {
		if b == nil {
			t.Fatal("verifier received nil binding")
		}
		if staged.Load() != 1 {
			t.Fatal("provider verification ran before durable revoke custody")
		}
		verified.Add(1)
		return nil
	}
	dp := newDeviceParams(dID, secret, rid)
	dp.RequestPushBinding = true
	var prepared atomic.Int32
	dp.PreparePushBinding = func(context.Context) (*PushBinding, func(), error) {
		prepared.Add(1)
		return negotiatedBinding(), func() { t.Error("successful pairing revoked its binding") }, nil
	}

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mErr != nil || dErr != nil || mo == nil || do == nil {
		t.Fatalf("negotiated pairing: machine=(%v,%v) device=(%v,%v)", mo, mErr, do, dErr)
	}
	if prepared.Load() != 1 || staged.Load() != 1 || verified.Load() != 1 {
		t.Fatalf("prepare=%d stage=%d verify=%d, want exactly one of each", prepared.Load(), staged.Load(), verified.Load())
	}
	if mo.PushBinding == nil {
		t.Fatal("negotiated pairing conveyed no binding")
	}
	if !do.Machine.PushBindingSupport {
		t.Fatal("device outcome did not record the authenticated machine acknowledgement")
	}
}

func TestPushBindingNegotiation_UnnegotiatedPeersDoNotCreateBinding(t *testing.T) {
	cases := []struct {
		name           string
		machineSupport bool
		deviceRequest  bool
	}{
		{name: "request absent", machineSupport: true},
		{name: "support absent", deviceRequest: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mID, _ := crypto.GenerateIdentity()
			dID, _ := crypto.GenerateIdentity()
			rid, secret := fill16(0x93), fill32(0x94)
			mp := newMachineParams(mID, secret, rid, acceptConfirm)
			mp.PushBindingSupport = tc.machineSupport
			dp := newDeviceParams(dID, secret, rid)
			dp.RequestPushBinding = tc.deviceRequest
			var prepared atomic.Int32
			dp.PreparePushBinding = func(context.Context) (*PushBinding, func(), error) {
				prepared.Add(1)
				return negotiatedBinding(), func() {}, nil
			}

			mEnd, dEnd := newRendezvousPipe()
			mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
			if mErr != nil || dErr != nil || mo == nil || do == nil {
				t.Fatalf("unnegotiated pairing: machine=(%v,%v) device=(%v,%v)", mo, mErr, do, dErr)
			}
			if prepared.Load() != 0 || mo.PushBinding != nil {
				t.Fatalf("prepare=%d binding=%+v; an unnegotiated peer must not create a binding", prepared.Load(), mo.PushBinding)
			}
		})
	}
}

func TestPushBindingNegotiation_VerificationFailureDeclinesAndRevokes(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid, secret := fill16(0x95), fill32(0x96)
	want := errors.New("provider did not accept test wake")
	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	mp.PushBindingSupport = true
	mp.VerifyPushBinding = func(context.Context, *PushBinding) error { return want }
	dp := newDeviceParams(dID, secret, rid)
	dp.RequestPushBinding = true
	var revoked atomic.Int32
	dp.PreparePushBinding = func(context.Context) (*PushBinding, func(), error) {
		return negotiatedBinding(), func() { revoked.Add(1) }, nil
	}

	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mo != nil || !errors.Is(mErr, want) {
		t.Fatalf("machine outcome/error=(%v,%v), want nil wrapping verification refusal", mo, mErr)
	}
	if do != nil || !errors.Is(dErr, ErrPairingDeclined) {
		t.Fatalf("device outcome/error=(%v,%v), want authenticated decline", do, dErr)
	}
	if revoked.Load() != 1 {
		t.Fatalf("revoke count=%d, want exactly 1", revoked.Load())
	}
}

func TestPushBindingNegotiation_MissingDurableStageDeclinesBeforeProviderVerification(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid, secret := fill16(0x97), fill32(0x98)
	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	mp.StagePushBinding = nil
	var verified atomic.Int32
	mp.VerifyPushBinding = func(context.Context, *PushBinding) error {
		verified.Add(1)
		return nil
	}
	dp := newDeviceParams(dID, secret, rid)
	dp.RequestPushBinding = true
	var revoked atomic.Int32
	dp.PreparePushBinding = func(context.Context) (*PushBinding, func(), error) {
		return negotiatedBinding(), func() { revoked.Add(1) }, nil
	}
	mEnd, dEnd := newRendezvousPipe()
	mo, mErr, do, dErr := drivePair(t, NewMachine(mp), dp, mEnd, dEnd)
	if mo != nil || !errors.Is(mErr, ErrNoPushVerifier) {
		t.Fatalf("machine outcome/error=(%v,%v), want missing-stage refusal", mo, mErr)
	}
	if do != nil || !errors.Is(dErr, ErrPairingDeclined) {
		t.Fatalf("device outcome/error=(%v,%v), want authenticated decline", do, dErr)
	}
	if verified.Load() != 0 || revoked.Load() != 1 {
		t.Fatalf("provider verification=%d revoke=%d, want 0 then 1", verified.Load(), revoked.Load())
	}
}

func TestQRPushBindingFlag_RoundTripsWithoutChangingLegacyShape(t *testing.T) {
	p := QRPayload{Flags: QRFlagPushBinding, RelayURL: "wss://relay.example", RendezvousID: fill16(1), PairingSecret: fill32(2)}
	encoded, err := EncodeQR(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeQR(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Flags&QRFlagPushBinding == 0 {
		t.Fatal("push-binding support flag was lost")
	}
	legacy := p
	legacy.Flags = 0
	legacyEncoded, err := EncodeQR(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyGot, err := DecodeQR(legacyEncoded)
	if err != nil || legacyGot.Flags != 0 {
		t.Fatalf("legacy QR changed: flags=%#x err=%v", legacyGot.Flags, err)
	}
}
