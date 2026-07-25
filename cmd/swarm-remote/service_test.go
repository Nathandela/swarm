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
	"path/filepath"
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

// TestServiceConfigFromParams_WiresDurableOutbox closes PB-GW-8 at the PRODUCTION seam.
// resolveGatewayParams provisions <stateDir>/remote/outbound-journal.outbox, and
// remotegw.NewService only reaches it through ServiceConfig.Outbox -- a nil there falls back
// to an in-memory outbox that OpenOutbox("") cannot fail to build, so a dropped mapping is
// SILENT: the sidecar starts, the file is created and stays empty, every restart re-reads the
// journal from cursor 0 and re-appends the whole thing at fresh seqs into the same
// 600-per-tumbling-minute mailbox, and a delivery-unknown append is re-sealed instead of
// re-appended verbatim.
//
// Non-nil is not enough to prove that, so this asserts DURABILITY: a commit made through the
// mapped ServiceConfig.Outbox must be readable by a fresh OpenOutbox over the on-disk file,
// which the in-memory fallback cannot satisfy.
func TestServiceConfigFromParams_WiresDurableOutbox(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	if p.Outbox == nil {
		t.Fatal("resolveGatewayParams returned a nil Outbox; the durable outbound-journal custody is not provisioned at all (PB-GW-8)")
	}

	cfg := serviceConfigFromParams(p, noopMailbox{})
	if cfg.Outbox == nil {
		t.Fatal("ServiceConfig.Outbox is nil: resolveGatewayParams provisions the durable outbox but " +
			"serviceConfigFromParams drops it, so the production Service runs an IN-MEMORY outbox. " +
			"PB-GW-8 is then closed only in tests -- every restart re-reads the journal from 0 and " +
			"re-appends it at fresh seqs, and a delivery-unknown append is re-sealed rather than " +
			"re-appended verbatim")
	}

	// Durable, not merely present: commit through the mapped outbox, then reopen the file.
	const cursor = 9
	if err := cfg.Outbox.Reserve(cursor, []byte("sealed-envelope-bytes")); err != nil {
		t.Fatalf("reserve through the mapped outbox: %v", err)
	}
	if err := cfg.Outbox.Commit(cursor); err != nil {
		t.Fatalf("commit through the mapped outbox: %v", err)
	}
	reopened, err := remotegw.OpenOutbox(filepath.Join(stateDir, "remote", "outbound-journal.outbox"))
	if err != nil {
		t.Fatalf("reopen the on-disk outbox: %v", err)
	}
	if got := reopened.Cursor(); got != cursor {
		t.Errorf("a reopened <stateDir>/remote/outbound-journal.outbox reports cursor %d, want %d: "+
			"the outbox the Service was handed is not backed by the provisioned file, so nothing it "+
			"commits survives the restarts PB-LIFE-1/-5 make routine", got, cursor)
	}
}
