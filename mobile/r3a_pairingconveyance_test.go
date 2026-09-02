// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, part 2 of the scope:
// the PAIRING CONVEYANCE of the push binding (ADR-015 P7, playbook 3.2: "The phone conveys
// the address, submit capability, machine-revoke capability, and phone-generated wake key
// inside the authenticated pairing exchange"), extended -- NOT a parallel channel -- from
// the existing internal/remote/pairing transcript, plus the phone's SessionCapabilities
// interest (ADR-017 T2 / RemoteProfileV1's capability_record_version, declared by the
// consumer at the one authenticated moment the two ends meet).
//
// WHAT IS UNDER TEST. The pairing package's conveyance surface that does not exist yet:
//
//   - pairing.PushBinding -- the five-field record the DEVICE supplies:
//     WakeKey                 (32 bytes, phone-generated per pairing, P7)
//     PushAddress             (16 opaque gateway-minted bytes, PG-ALLOC-1)
//     SubmitCapability        (base64url, shown once by the gateway)
//     MachineRevokeCapability (base64url, distinct from submit)
//     CapabilityRecordVersion (the SessionCapabilities record version this phone consumes)
//   - pairing.DeviceParams.PushBinding  -- the device's half of the transcript.
//   - pairing.MachineOutcome.PushBinding -- what the machine must persist BEFORE
//     confirming pairing (playbook 3.2); nil when the device conveyed none (a pre-R3
//     phone build), which the machine treats as a legacy_relay pairing under P12.
//
// WHY THE TEST LIVES IN mobile/. The phone is the party that mints the wake key and
// receives the allocation, and mobile/ is the owned surface this slice extends; the test
// drives the REAL pairing handshake (pairing.NewMachine + pairing.RunDevice) over an
// in-memory rendezvous pipe, so the conveyance is proven through the authenticated
// transcript rather than through a struct literal.
//
// NOTHING HERE TOUCHES A RELAY, FCM, OR A HANDSET.
package swarmmobile_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// r3aPipeEnd is one end of an in-memory two-party rendezvous, faithful to the relay's
// forward-only role: opaque bytes, verbatim.
type r3aPipeEnd struct {
	outbox chan []byte
	inbox  chan []byte
	mu     sync.Mutex
	sent   [][]byte
}

func r3aNewPipe() (machine, device *r3aPipeEnd) {
	aToB := make(chan []byte, 16)
	bToA := make(chan []byte, 16)
	return &r3aPipeEnd{outbox: aToB, inbox: bToA}, &r3aPipeEnd{outbox: bToA, inbox: aToB}
}

func (f *r3aPipeEnd) Create(context.Context, string) error   { return nil }
func (f *r3aPipeEnd) Claim(context.Context, string) error    { return nil }
func (f *r3aPipeEnd) Complete(context.Context, string) error { return nil }

func (f *r3aPipeEnd) Send(_ context.Context, msg []byte) error {
	cp := append([]byte(nil), msg...)
	f.mu.Lock()
	f.sent = append(f.sent, cp)
	f.mu.Unlock()
	f.outbox <- cp
	return nil
}

func (f *r3aPipeEnd) Recv(ctx context.Context) ([]byte, error) {
	select {
	case msg := <-f.inbox:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *r3aPipeEnd) wire() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.sent...)
}

func r3aFill(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// r3aMachineParams / r3aDeviceParams build one well-formed pairing pair, mirroring the
// pairing package's own harness shapes.
func r3aMachineParams(t *testing.T, secret [32]byte, rid [16]byte) pairing.MachineParams {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("crypto.GenerateIdentity: %v", err)
	}
	return pairing.MachineParams{
		Static:       id.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm: func(context.Context, [6]string, string) (bool, error) {
			return true, nil
		},
		PushBindingSupport: true,
		StagePushBinding: func(context.Context, *pairing.PushBinding, pairing.DevicePayload) error {
			return nil
		},
		VerifyPushBinding: func(context.Context, *pairing.PushBinding) error {
			return nil
		},
		Payload: pairing.MachinePayload{
			Hostname:            "r3a-machine.local",
			MachineRoutingID:    r3aFill(0x31, 16),
			MachineRelayAuthPub: r3aFill(0x32, 32),
			RecipientPub:        id.RecipientPublic(),
			EpochID:             1,
		},
	}
}

func r3aDeviceParams(t *testing.T, secret [32]byte, rid [16]byte, binding *pairing.PushBinding) pairing.DeviceParams {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("crypto.GenerateIdentity: %v", err)
	}
	return pairing.DeviceParams{
		Static:       id.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		Payload: pairing.DevicePayload{
			DeviceName:         "r3a phone",
			DeviceRoutingID:    r3aFill(0x41, 16),
			DeviceRelayAuthPub: r3aFill(0x42, 32),
			RecipientPub:       id.RecipientPublic(),
		},
		Consent: func(m pairing.MachinePayload) ([]byte, error) {
			return append([]byte("consent-for:"), m.MachineRelayAuthPub...), nil
		},
		RequestPushBinding: binding != nil,
		PushBinding:        binding,
		// ROUND 4: the revoke arm is MANDATORY beside a binding (ErrNoPushRevoke) -- msg4
		// releases the wake key and both gateway capabilities before the machine decides,
		// so a device with no way to take them back is refused before it releases anything.
		// These tests drive the SUCCESSFUL conveyance, where the arm is never called; the
		// non-accept exits that DO call it are pinned in the pairing package's own
		// r3ar4_revokeobligation_test.go. It is set only when there is a binding, so the
		// nil-binding compatibility case still exercises the pre-R3 shape exactly.
		RevokePushBinding: r3aRevokeArm(t, binding),
	}
}

// r3aRevokeArm is the mandatory revoke arm for a device that conveys a binding, and nil for
// one that does not. It FAILS the test if it is ever invoked: every caller here drives a
// pairing that succeeds, and a revoke on a successful pairing would kill the address the
// machine was just told to wake.
func r3aRevokeArm(t *testing.T, binding *pairing.PushBinding) func() {
	t.Helper()
	if binding == nil {
		return nil
	}
	return func() {
		t.Error("RevokePushBinding was invoked on a pairing that succeeded")
	}
}

func r3aDrivePair(t *testing.T, mp pairing.MachineParams, dp pairing.DeviceParams) (*pairing.MachineOutcome, *pairing.DeviceOutcome, [][]byte) {
	t.Helper()
	mEnd, dEnd := r3aNewPipe()
	m := pairing.NewMachine(mp)

	var (
		mo   *pairing.MachineOutcome
		do   *pairing.DeviceOutcome
		mErr error
		dErr error
		wg   sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		mo, mErr = m.Pair(context.Background(), mEnd)
	}()
	go func() {
		defer wg.Done()
		do, dErr = pairing.RunDevice(context.Background(), dp, dEnd)
	}()
	wg.Wait()
	if mErr != nil {
		t.Fatalf("machine Pair: %v", mErr)
	}
	if dErr != nil {
		t.Fatalf("device RunDevice: %v", dErr)
	}
	if mo == nil || do == nil {
		t.Fatal("nil outcome on a completed pairing")
	}
	return mo, do, append(mEnd.wire(), dEnd.wire()...)
}

// TestR3A_PairingConveysThePushBindingToTheMachine: the conveyance itself. The device
// supplies the five-field binding; the machine's outcome carries every field VERBATIM --
// this is the record the machine must persist before confirming pairing, and any drift
// (a re-encoded key, a truncated capability) is a machine that can never wake this phone.
func TestR3A_PairingConveysThePushBindingToTheMachine(t *testing.T) {
	var secret [32]byte
	copy(secret[:], r3aFill(0x5A, 32))
	var rid [16]byte
	copy(rid[:], r3aFill(0x11, 16))

	supplied := &pairing.PushBinding{
		WakeKey:                 r3aFill(0x71, 32),
		PushAddress:             r3aFill(0x72, 16),
		SubmitCapability:        "submit-capability-r3a-000000000000000000000",
		MachineRevokeCapability: "revoke-capability-r3a-0000000000000000000000",
		CapabilityRecordVersion: 1,
	}

	mo, _, _ := r3aDrivePair(t, r3aMachineParams(t, secret, rid), r3aDeviceParams(t, secret, rid, supplied))

	got := mo.PushBinding
	if got == nil {
		t.Fatal("the machine outcome carries no push binding")
	}
	if !bytes.Equal(got.WakeKey, supplied.WakeKey) {
		t.Error("the wake key did not cross the transcript verbatim")
	}
	if !bytes.Equal(got.PushAddress, supplied.PushAddress) {
		t.Error("the push address did not cross the transcript verbatim")
	}
	if got.SubmitCapability != supplied.SubmitCapability {
		t.Errorf("submit capability: got %q want %q", got.SubmitCapability, supplied.SubmitCapability)
	}
	if got.MachineRevokeCapability != supplied.MachineRevokeCapability {
		t.Errorf("machine-revoke capability: got %q want %q", got.MachineRevokeCapability, supplied.MachineRevokeCapability)
	}
	if got.CapabilityRecordVersion != supplied.CapabilityRecordVersion {
		t.Errorf("capability record version: got %d want %d", got.CapabilityRecordVersion, supplied.CapabilityRecordVersion)
	}
}

// TestR3A_PairingWithoutABindingConveysNone: compatibility. A device that supplies no
// binding (a pre-R3 build, or a phone whose gateway registration was refused and is
// honestly foreground-only) completes the pairing exactly as today, and the machine sees
// nil -- the P12 legacy_relay state, never a zero-valued binding a machine might persist
// as real.
func TestR3A_PairingWithoutABindingConveysNone(t *testing.T) {
	var secret [32]byte
	copy(secret[:], r3aFill(0x5B, 32))
	var rid [16]byte
	copy(rid[:], r3aFill(0x12, 16))

	mo, _, _ := r3aDrivePair(t, r3aMachineParams(t, secret, rid), r3aDeviceParams(t, secret, rid, nil))
	if mo.PushBinding != nil {
		t.Fatalf("a device that conveyed no binding produced %+v at the machine", mo.PushBinding)
	}
}

// TestR3A_TheWakeKeyIsNeverOnTheWireInTheClear: the transcript is the AUTHENTICATED
// pairing exchange -- the wake key must cross only inside the Noise session, never as
// plaintext bytes on the rendezvous. This is the same fence the pairing suite pins for
// the pairing secret (R-PAIR.1), extended to the new secret this slice adds; it would
// catch a conveyance bolted on beside the transcript (the "parallel channel" the scope
// forbids) as well as a plaintext leak in the transcript itself.
func TestR3A_TheWakeKeyIsNeverOnTheWireInTheClear(t *testing.T) {
	var secret [32]byte
	copy(secret[:], r3aFill(0x5C, 32))
	var rid [16]byte
	copy(rid[:], r3aFill(0x13, 16))

	wakeKey := r3aFill(0x73, 32)
	supplied := &pairing.PushBinding{
		WakeKey:                 wakeKey,
		PushAddress:             r3aFill(0x74, 16),
		SubmitCapability:        "submit-capability-r3a-111111111111111111111",
		MachineRevokeCapability: "revoke-capability-r3a-1111111111111111111111",
		CapabilityRecordVersion: 1,
	}

	mo, _, wire := r3aDrivePair(t, r3aMachineParams(t, secret, rid), r3aDeviceParams(t, secret, rid, supplied))
	if mo.PushBinding == nil {
		t.Fatal("the machine outcome carries no push binding")
	}
	for i, frame := range wire {
		if bytes.Contains(frame, wakeKey) {
			t.Fatalf("wire frame %d carries the wake key in the clear", i)
		}
		if bytes.Contains(frame, supplied.PushAddress) {
			t.Fatalf("wire frame %d carries the push address in the clear", i)
		}
		if bytes.Contains(frame, []byte(supplied.SubmitCapability)) {
			t.Fatalf("wire frame %d carries the submit capability in the clear", i)
		}
		if bytes.Contains(frame, []byte(supplied.MachineRevokeCapability)) {
			t.Fatalf("wire frame %d carries the machine-revoke capability in the clear", i)
		}
	}
}
