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

func (noopMailbox) MailboxWait(_ context.Context, _ uint64) ([]relay.Item, bool, error) {
	return nil, false, nil
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

// TestServiceConfigFromParams_WiresTheGrantWatermark closes the third authority of the
// reconcile record (PB-STATE-4(c)) at the PRODUCTION seam.
//
// The grant watermark is (EpochID, GrantSeq), and only the machine identity holds the grant
// seq -- ServiceConfig.GrantSeq is the sole way into gatewayAuthorities.GrantWatermark. A
// dropped mapping is SILENT: NewService happily publishes grant_seq 0, the phone adopts it
// monotonically (so 0 changes nothing and no error appears anywhere), and the coordinate
// PB-STATE-4 exists to re-anchor after a rollback is simply never carried. Non-zero is
// therefore asserted against the IDENTITY'S OWN value, not merely against zero.
func TestServiceConfigFromParams_WiresTheGrantWatermark(t *testing.T) {
	stateDir := t.TempDir()
	id := writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	if p.GrantSeq != id.GrantSeq() {
		t.Fatalf("resolveGatewayParams read grant seq %d from <stateDir>/remote/machine.key; want %d "+
			"(the identity's own grant-issuance coordinate)", p.GrantSeq, id.GrantSeq())
	}
	if p.EpochID != id.EpochID() {
		t.Fatalf("resolveGatewayParams read epoch %d; want %d", p.EpochID, id.EpochID())
	}

	cfg := serviceConfigFromParams(p, noopMailbox{})
	if cfg.GrantSeq != id.GrantSeq() {
		t.Fatalf("ServiceConfig.GrantSeq = %d, want %d: resolveGatewayParams reads the grant "+
			"watermark but serviceConfigFromParams drops it, so every reconcile record the "+
			"production gateway publishes carries grant_seq 0 and PB-STATE-4(c) is closed only in tests",
			cfg.GrantSeq, id.GrantSeq())
	}
}

// TestServiceConfigFromParams_WiresThePushPath closes PB-PUSH-0/-3/-8/-10 at the PRODUCTION
// seam, in the same shape as the outbox and grant-watermark tests above and for the same
// reason: every one of these three mappings fails SILENTLY when dropped.
//
//   - WakeKey dropped => every wake is sealed under an ALL-ZERO key. SealWake succeeds, the
//     relay forwards 78 opaque bytes, the provider delivers, and the phone cannot open a
//     single one. Nothing errors anywhere on this side.
//   - PushSeq dropped => NewPushNotifier falls back to an in-memory source that cannot fail
//     to build, so the wake seq restarts at 1 on every gateway restart and the phone's
//     persisted replay coordinate (PB-STATE-1) refuses every wake after the first restart.
//   - PushPrefs dropped => the notifier fails closed and suppresses every wake, and the
//     command bridge refuses the push_prefs verb. Push simply never works, loudly only in a
//     PushNotifier.Err() nobody is required to read.
//
// Presence is not enough for the seq, so this asserts DURABILITY the in-memory fallback
// cannot satisfy: a seq issued through the mapped source must not be reissued by a fresh
// handle over the on-disk file.
func TestServiceConfigFromParams_WiresThePushPath(t *testing.T) {
	stateDir := t.TempDir()
	id := writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	p, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	cfg := serviceConfigFromParams(p, noopMailbox{})

	// The wake key is the identity's own, not a zero value that would seal unopenable wakes.
	wantWake := id.EpochKeys().WakeKey
	if cfg.WakeKey != wantWake {
		t.Fatalf("ServiceConfig.WakeKey is not the machine identity's wake key: every wake the "+
			"production gateway seals would be opened by nobody (got %x..., want %x...)",
			cfg.WakeKey[:4], wantWake[:4])
	}
	if cfg.WakeKey == (crypto.WakeKey{}) {
		t.Fatal("ServiceConfig.WakeKey is all zero")
	}
	// ...and it is NOT the content key: sealing wakes under the content key would hand the
	// NSE-readable key path a key that opens session content (A15/F10).
	if crypto.ContentKey(cfg.WakeKey) == cfg.Key {
		t.Fatal("ServiceConfig.WakeKey equals the content key: the push path must hold a content-free key")
	}

	if cfg.PushPrefs == nil {
		t.Fatal("ServiceConfig.PushPrefs is nil: the production notifier fails closed and suppresses " +
			"every wake, and the push_prefs verb is refused, with PB-PUSH-8/-10 closed only in tests")
	}
	if cfg.PushSeq == nil {
		t.Fatal("ServiceConfig.PushSeq is nil: the wake replay coordinate restarts at 1 on every " +
			"gateway restart and the phone refuses every wake after one (PB-PUSH-3)")
	}

	// Durable, not merely present.
	issued, err := cfg.PushSeq.Next()
	if err != nil {
		t.Fatalf("Next through the mapped push seq: %v", err)
	}
	reopened, err := remotegw.OpenSeqSource(filepath.Join(stateDir, "remote", "outbound-push.seq"))
	if err != nil {
		t.Fatalf("reopen the on-disk push seq: %v", err)
	}
	next, err := reopened.Next()
	if err != nil {
		t.Fatalf("Next through the reopened push seq: %v", err)
	}
	if next <= issued {
		t.Errorf("a reopened <stateDir>/remote/outbound-push.seq reissued %d after %d: the seq the "+
			"Service was handed is not backed by the provisioned file, so every restart replays wake "+
			"seqs the phone has already refused", next, issued)
	}

	// The preference custody is the provisioned FILE, not an in-memory stand-in: what the
	// command bridge stores must be what a restarted notifier reads.
	if err := cfg.PushPrefs.SavePrefs(remotegw.PushPrefs{Version: 3, NeedsInput: false, Finished: true}); err != nil {
		t.Fatalf("SavePrefs through the mapped custody: %v", err)
	}
	reopenedPrefs, err := remotegw.OpenPushPrefs(filepath.Join(stateDir, "remote", "push-prefs.json"))
	if err != nil {
		t.Fatalf("reopen the on-disk push preference: %v", err)
	}
	got, err := reopenedPrefs.LoadPrefs()
	if err != nil {
		t.Fatalf("LoadPrefs through the reopened custody: %v", err)
	}
	if got != (remotegw.PushPrefs{Version: 3, NeedsInput: false, Finished: true}) {
		t.Errorf("a reopened <stateDir>/remote/push-prefs.json holds %+v, want the record saved through "+
			"the mapped custody: the preference the user sets does not survive a gateway restart", got)
	}
}
