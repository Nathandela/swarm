package relay

// R-REL.7 — persistence store (embedded transactional bbolt). Holds registry,
// per-device mailbox log, cursors, quotas — only ciphertext + routing metadata,
// never plaintext or identity keys; survives restart.

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"testing"
)

// TestRelayStore_SurvivesRestart asserts a mailbox item and its registered
// authorization survive a full relay restart against the same store file.
func TestRelayStore_SurvivesRestart(t *testing.T) {
	srv, cfg, apns, clk := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	env := sp.sealMailbox(t, 1, []byte("durable"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	// Restart the relay against the same DB path.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	srv2, err := New(cfg, WithClock(clk), WithAPNsSink(apns))
	if err != nil {
		t.Fatalf("New(restart): %v", err)
	}
	if err := srv2.Start(testCtx(t)); err != nil {
		t.Fatalf("Start(restart): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	// The device still authenticates (registry survived) and drains its item.
	device := dialAuthed(t, srv2.URL(), authFor(dPub, dPriv))
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead after restart: %v", err)
	}
	if len(items) != 1 || !bytes.Equal(items[0].Envelope, env) {
		t.Fatalf("mailbox item did not survive restart: %+v", items)
	}
}

// TestRelayStore_NoPlaintextAtRest asserts the persisted bbolt file contains the
// opaque ciphertext but never the sealed plaintext.
func TestRelayStore_NoPlaintextAtRest(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	dPub, _ := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	secret := []byte("PLAINTEXT-NEVER-AT-REST-0xDEADBEEF")
	env := sp.sealMailbox(t, 1, secret, clk)
	if _, err := machine.MailboxAppend(testCtx(t), RoutingID(dPub), env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(cfg.DBPath)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if bytes.Contains(raw, secret) {
		t.Fatalf("relay store persisted the sealed plaintext at rest")
	}
	// The opaque ciphertext SHOULD be present — the relay stores it verbatim.
	if !bytes.Contains(raw, env) {
		t.Fatalf("expected the opaque envelope to be persisted verbatim")
	}
}
