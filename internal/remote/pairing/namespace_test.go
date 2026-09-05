package pairing

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func namespacedMachinePayload() MachinePayload {
	return MachinePayload{
		Hostname: "machine", MachineRoutingID: []byte("routing"),
		MachineRelayAuthPub: []byte("auth"), RecipientPub: []byte("recipient"),
		MachineSignPub: []byte("sign"), MachineEndpointID: "machine-id",
		RelayTLSPolicy: "webpki", RelayHost: "s.example.workers.dev",
		OperatorNamespace: "owner", EpochID: 7,
	}
}

func TestMachinePayloadCarriesRequiredOperatorNamespace(t *testing.T) {
	want := namespacedMachinePayload()
	got, err := decodeMachinePayload(encodeMachinePayload(want))
	if err != nil {
		t.Fatalf("decodeMachinePayload: %v", err)
	}
	if got.OperatorNamespace != want.OperatorNamespace {
		t.Fatalf("OperatorNamespace = %q, want %q", got.OperatorNamespace, want.OperatorNamespace)
	}
}

func TestMachinePayloadRejectsMissingOrNoncanonicalOperatorNamespace(t *testing.T) {
	for _, namespace := range []string{"", "Owner", "owner_2"} {
		p := namespacedMachinePayload()
		p.OperatorNamespace = namespace
		if _, err := decodeMachinePayload(encodeMachinePayload(p)); err == nil {
			t.Errorf("decode accepted operator namespace %q", namespace)
		}
	}
}

func TestMachinePayloadRejectsPreNamespaceWireShape(t *testing.T) {
	p := namespacedMachinePayload()
	var b []byte
	for _, field := range [][]byte{
		[]byte(p.Hostname), p.MachineRoutingID, p.MachineRelayAuthPub, p.RecipientPub,
		p.MachineSignPub, []byte(p.MachineEndpointID), p.RelaySPKIPin,
		[]byte(p.RelayTLSPolicy), []byte(p.RelayHost),
	} {
		b = appendField(b, field)
	}
	b = binary.BigEndian.AppendUint32(b, p.EpochID)
	if _, err := decodeMachinePayload(b); err == nil {
		t.Fatal("decode accepted the retired msg2 shape with no authenticated namespace")
	}
}

func TestRunDeviceRejectsMissingNamespaceBeforeSASConsentOrCommit(t *testing.T) {
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	rid, secret := fill16(0x31), fill32(0x32)
	mp := newMachineParams(machineID, secret, rid, acceptConfirm)
	mp.Payload.OperatorNamespace = ""
	dp := newDeviceParams(deviceID, secret, rid)
	var verified, sas, consent, committed bool
	dp.VerifyMachine = func(MachinePayload) error { verified = true; return nil }
	dp.DeviceSAS = func(context.Context, [6]string) error { sas = true; return nil }
	dp.Consent = func(MachinePayload) ([]byte, error) { consent = true; return []byte("consent"), nil }
	dp.Commit = func(*DeviceOutcome) error { committed = true; return nil }

	machineEnd, deviceEnd := newRendezvousPipe()
	machineOut, _, deviceOut, deviceErr := drivePairCancel(t, NewMachine(mp), dp, machineEnd, deviceEnd)
	if machineOut != nil || deviceOut != nil || !errors.Is(deviceErr, errMalformedPayload) {
		t.Fatalf("outcomes=(%v,%v) device error=%v, want no outcome and malformed payload", machineOut, deviceOut, deviceErr)
	}
	if verified || sas || consent || committed {
		t.Fatalf("callbacks reached after invalid msg2: verify=%v sas=%v consent=%v commit=%v", verified, sas, consent, committed)
	}
}
