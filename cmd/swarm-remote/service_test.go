// FAILING-FIRST (TDD RED, GG-5) test for the gateway binary's ServiceConfig
// mapping (slice G2): serviceConfigFromParams is the PURE function that
// copies a resolved gatewayParams (slice G1, cmd/swarm-remote/config.go) plus
// a dialed relay Mailbox into a remotegw.ServiceConfig. It does not exist
// yet -- this file is intentionally RED (compile-fail) until GREEN adds it.
//
// THE CONTRACT this test freezes (undefined symbol -> compile-fail RED):
//   - func serviceConfigFromParams(p gatewayParams, mailbox remotegw.Mailbox) remotegw.ServiceConfig
//     copying DaemonSocket, Relay (the mailbox), PhoneTarget, Key, EpochID,
//     RecipientKeyID, SenderKeyID from p. Forwarder and timing/Now are left
//     to remotegw.NewService's defaults (not this function's concern).
//
// run()/main() (the process glue that dials relay.Dial and calls this
// mapping + remotegw.NewService + Service.Run) are NOT tested here: they are
// thin glue with no branching logic, the Service itself is E2E-tested in
// internal/skeleton/gatewayservice_e2e_test.go, and the full observe+kill E2E
// is slice E1.
package main

import (
	"context"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// noopMailbox is a minimal remotegw.Mailbox fake: serviceConfigFromParams
// only needs to store it into ServiceConfig.Relay, never call it, so every
// method is an unused no-op that exists solely to satisfy the interface.
type noopMailbox struct{}

func (noopMailbox) MailboxRead(_ context.Context, _ uint64) ([]relay.Item, error) {
	return nil, nil
}

func (noopMailbox) MailboxAppend(_ context.Context, _ string, _ []byte) (uint64, error) {
	return 0, nil
}

func (noopMailbox) MailboxAck(_ context.Context, _ uint64) error {
	return nil
}

// TestServiceConfigFromParams_MapsAllFields proves serviceConfigFromParams
// copies every gatewayParams field remotegw.Service needs into ServiceConfig,
// plus wires the dialed Mailbox in as Relay, with no field dropped or
// transposed.
func TestServiceConfigFromParams_MapsAllFields(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	var recipientKeyID, senderKeyID [8]byte
	for i := range recipientKeyID {
		recipientKeyID[i] = byte(0x10 + i)
		senderKeyID[i] = byte(0x20 + i)
	}

	p := gatewayParams{
		DaemonSocket:   "/tmp/swarm-remote-test/remote.sock",
		RelayURL:       "wss://relay.example.test/v1",
		PhoneTarget:    "phone-routing-id-distinctive",
		Key:            key,
		EpochID:        7,
		RecipientKeyID: recipientKeyID,
		SenderKeyID:    senderKeyID,
	}
	mailbox := noopMailbox{}

	cfg := serviceConfigFromParams(p, mailbox)

	if cfg.DaemonSocket != p.DaemonSocket {
		t.Errorf("DaemonSocket = %q, want %q", cfg.DaemonSocket, p.DaemonSocket)
	}
	if cfg.Relay != remotegw.Mailbox(mailbox) {
		t.Errorf("Relay = %#v, want the fake mailbox %#v", cfg.Relay, mailbox)
	}
	if cfg.PhoneTarget != p.PhoneTarget {
		t.Errorf("PhoneTarget = %q, want %q", cfg.PhoneTarget, p.PhoneTarget)
	}
	if cfg.Key != p.Key {
		t.Errorf("Key = %x, want %x", cfg.Key, p.Key)
	}
	if cfg.EpochID != p.EpochID {
		t.Errorf("EpochID = %d, want %d", cfg.EpochID, p.EpochID)
	}
	if cfg.RecipientKeyID != p.RecipientKeyID {
		t.Errorf("RecipientKeyID = %x, want %x", cfg.RecipientKeyID, p.RecipientKeyID)
	}
	if cfg.SenderKeyID != p.SenderKeyID {
		t.Errorf("SenderKeyID = %x, want %x", cfg.SenderKeyID, p.SenderKeyID)
	}
}
