package pairing

// ADR-007 B48: the device's check on the authenticated msg2, which is where the relay
// certificate the pairing dial accepted unverified meets the pin the real machine
// authored. The seam has to run at msg2 and fail closed there: a device that discovers a
// terminated connection AFTER showing its operator a SAS has already spent the one piece
// of human attention the ceremony gets.

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func TestB48_VerifyMachineRunsOnMsg2BeforeAnythingIsShownOrSigned(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	wantPin := bytes.Repeat([]byte{0xD4}, 32)
	mp := newMachineParams(mID, secret, rid, acceptConfirm)
	mp.Payload.RelaySPKIPin = wantPin

	dp := newDeviceParams(dID, secret, rid)
	errTerminated := errors.New("relay certificate does not match the machine's pin")

	var sawPin []byte
	sasShown, consentAsked := 0, 0
	dp.VerifyMachine = func(m MachinePayload) error {
		sawPin = append([]byte(nil), m.RelaySPKIPin...)
		return errTerminated
	}
	dp.DeviceSAS = func(context.Context, [6]string) error { sasShown++; return nil }
	dp.Consent = func(MachinePayload) ([]byte, error) { consentAsked++; return []byte("c"), nil }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mEnd, dEnd := newRendezvousPipe()
	machineDone := make(chan struct{})
	go func() { defer close(machineDone); _, _ = NewMachine(mp).Pair(ctx, mEnd) }()

	_, err := RunDevice(ctx, dp, dEnd)
	if !errors.Is(err, errTerminated) {
		t.Fatalf("RunDevice = %v, want the VerifyMachine refusal", err)
	}
	if !bytes.Equal(sawPin, wantPin) {
		t.Fatalf("VerifyMachine saw RelaySPKIPin %x, want the msg2 value %x: a check handed anything "+
			"but the AUTHENTICATED payload compares against a value the attacker chose", sawPin, wantPin)
	}
	if sasShown != 0 {
		t.Errorf("the operator was shown a SAS for a connection already known to be terminated")
	}
	if consentAsked != 0 {
		t.Errorf("the device signed a relay-route consent after refusing the machine")
	}
	// msg3 never left the device: the machine is still parked on Recv.
	select {
	case <-machineDone:
		t.Error("the machine completed a leg whose device refused at msg2")
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	<-machineDone
}

// TestB48_ANilVerifyMachineIsANoOp keeps the seam optional: every existing caller that
// does not set it pairs exactly as before.
func TestB48_ANilVerifyMachineIsANoOp(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	mEnd, dEnd := newRendezvousPipe()
	dp := newDeviceParams(dID, secret, rid)
	if dp.VerifyMachine != nil {
		t.Fatal("the fixture sets VerifyMachine; this test asserts the nil path")
	}
	_, mErr, do, dErr := drivePair(t, NewMachine(newMachineParams(mID, secret, rid, acceptConfirm)), dp, mEnd, dEnd)
	if mErr != nil || dErr != nil || do == nil {
		t.Fatalf("pairing with no VerifyMachine failed: machine=%v device=%v", mErr, dErr)
	}
}
