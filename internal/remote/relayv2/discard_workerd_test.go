package relayv2

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestWorkerdDiscardRetryRecovery(t *testing.T) {
	baseURL := os.Getenv("RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("RELAY_V2_HTTP is set by services/relay/test/run.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	machinePub, machinePriv := deterministicKey(0)
	phonePub, phonePriv := deterministicKey(32)
	machineRID, phoneRID := RoutingID(machinePub), RoutingID(phonePub)
	profile := Profile{RelayURL: baseURL, MachineRID: machineRID, OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true}}
	control := dialForTest(t, ctx, profile, privateAuth(machinePub, machinePriv, RoleMachine, PurposeControl))
	defer control.Close()
	const ceremony = "00000000000000000000000000000000"
	binding, err := control.Authorize(ctx, phonePub, MarshalConsent(ceremony, ed25519.Sign(phonePriv, ConsentMessage(ceremony, machineRID))))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if binding.PeerRID != phoneRID {
		t.Fatalf("binding peer = %q, want %q", binding.PeerRID, phoneRID)
	}

	machine := dialForTest(t, ctx, profile, privateAuth(machinePub, machinePriv, RoleMachine, PurposeStream))
	defer machine.Close()
	phone := dialForTest(t, ctx, profile, privateAuth(phonePub, phonePriv, RolePhone, PurposeStream))
	phoneBinding, err := phone.PhoneBinding()
	if err != nil {
		t.Fatalf("PhoneBinding: %v", err)
	}
	sub, err := phone.Subscribe(ctx, phoneBinding, Checkpoint{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := machine.Append(ctx, binding, "before-discard", []byte("before")); err != nil {
		t.Fatalf("Append before discard: %v", err)
	}
	before, err := sub.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv before discard: %v", err)
	}

	checkpoint, err := sub.Discard(ctx)
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if checkpoint.Cursor != before.Cursor || checkpoint.Incarnation == sub.Incarnation() {
		t.Fatalf("Discard checkpoint = %+v, previous incarnation = %q", checkpoint, sub.Incarnation())
	}
	if _, err := machine.Append(ctx, binding, "intervening-before-retry", []byte("intervening")); err != nil {
		t.Fatalf("Append before exact retry: %v", err)
	}
	retry, err := sub.Discard(ctx)
	if err != nil {
		t.Fatalf("exact DISCARD retry: %v", err)
	}
	if retry != checkpoint {
		t.Fatalf("exact DISCARD retry = %+v, want %+v", retry, checkpoint)
	}

	recovered, err := phone.Subscribe(ctx, phoneBinding, checkpoint)
	if err != nil {
		t.Fatalf("Subscribe after discard: %v", err)
	}
	intervening, err := recovered.Recv(ctx)
	if err != nil || string(intervening.Ciphertext) != "intervening" {
		t.Fatalf("intervening mail after exact retry = (%+v, %v)", intervening, err)
	}
	if err := recovered.Ack(ctx, intervening.Cursor); err != nil {
		t.Fatalf("Ack intervening mail: %v", err)
	}
	if _, err := machine.Append(ctx, binding, "pending-before-retry", []byte("pending")); err != nil {
		t.Fatalf("Append pending before retry: %v", err)
	}
	if retry, err = sub.Discard(ctx); err != nil || retry != checkpoint {
		t.Fatalf("DISCARD retry after new subscription/ACK = (%+v, %v), want %+v", retry, err, checkpoint)
	}
	pending, err := recovered.Recv(ctx)
	if err != nil || string(pending.Ciphertext) != "pending" {
		t.Fatalf("pending mail after DISCARD retry = (%+v, %v)", pending, err)
	}
	if err := recovered.Ack(ctx, pending.Cursor); err != nil {
		t.Fatalf("Ack pending mail: %v", err)
	}
	phone.Close()

	// A blank checkpoint is recovery-only: it echoes after=0 but starts at durable ACK.
	reconnected := dialForTest(t, ctx, profile, privateAuth(phonePub, phonePriv, RolePhone, PurposeStream))
	defer reconnected.Close()
	blank, err := reconnected.Subscribe(ctx, phoneBinding, Checkpoint{})
	if err != nil {
		t.Fatalf("blank checkpoint below ACK: %v", err)
	}
	if _, err := machine.Append(ctx, binding, "after-blank", []byte("blank")); err != nil {
		t.Fatalf("Append after blank recovery: %v", err)
	}
	if delivery, err := blank.Recv(ctx); err != nil || delivery.Cursor <= pending.Cursor {
		t.Fatalf("blank recovery delivery = (%+v, %v), want cursor above %d", delivery, err, pending.Cursor)
	}

	// A valid but unrelated old incarnation is not a discard retry.
	reconnected.Close()
	staleIncarnation := dialForTest(t, ctx, profile, privateAuth(phonePub, phonePriv, RolePhone, PurposeStream))
	defer staleIncarnation.Close()
	_, err = staleIncarnation.call(ctx, "DISCARDED", map[string]any{
		"v": 2, "type": "DISCARD", "peer_rid": machineRID, "generation": formatUint64(phoneBinding.Generation), "incarnation": "AAAAAAAAAAAAAAAAAAAAAA",
	})
	var protocol *ProtocolError
	if !errors.As(err, &protocol) || protocol.Code != "incarnation_mismatch" {
		t.Fatalf("arbitrary stale incarnation = %v, want incarnation_mismatch", err)
	}

	const replacementCeremony = "11111111111111111111111111111111"
	currentBinding, err := control.Authorize(ctx, phonePub, MarshalConsent(replacementCeremony, ed25519.Sign(phonePriv, ConsentMessage(replacementCeremony, machineRID))))
	if err != nil {
		t.Fatalf("replace binding: %v", err)
	}
	_, err = machine.call(ctx, "DISCARDED", map[string]any{
		"v": 2, "type": "DISCARD", "peer_rid": phoneRID, "generation": formatUint64(binding.Generation), "incarnation": checkpoint.Incarnation,
	})
	protocol = nil
	if !errors.As(err, &protocol) || protocol.Code != "stale_generation" {
		t.Fatalf("stale generation = %v, want stale_generation", err)
	}
	if err := control.Revoke(ctx, currentBinding); err != nil {
		t.Fatalf("Revoke current binding: %v", err)
	}
	_, err = machine.call(ctx, "DISCARDED", map[string]any{
		"v": 2, "type": "DISCARD", "peer_rid": phoneRID, "generation": formatUint64(currentBinding.Generation), "incarnation": "AAAAAAAAAAAAAAAAAAAAAA",
	})
	protocol = nil
	if !errors.As(err, &protocol) || protocol.Code != "stale_generation" {
		t.Fatalf("revoked binding DISCARD = %v, want stale_generation", err)
	}
}
