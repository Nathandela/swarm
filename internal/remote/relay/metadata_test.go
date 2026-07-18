package relay

// R-REL.10 — metadata retention + log scrubbing. No plaintext/ciphertext bodies
// in logs; mailbox items purged after ack + a retention cap even if unacked.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"
)

// TestRelay_LogsNoBodies asserts the relay's logs never contain envelope bodies:
// neither the opaque ciphertext nor (obviously) any sealed plaintext.
func TestRelay_LogsNoBodies(t *testing.T) {
	var logs bytes.Buffer
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	clk := newFakeClock()
	srv, err := New(cfg, WithClock(clk), WithAPNsSink(&mockAPNs{}), WithLogWriter(&logs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	secret := []byte("PLAINTEXT-MUST-NOT-BE-LOGGED-42")
	env := sp.sealMailbox(t, 1, secret, clk)
	if _, err := machine.MailboxAppend(testCtx(t), RoutingID(dPub), env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}
	if _, err := device.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("MailboxRead: %v", err)
	}

	out := logs.Bytes()
	if bytes.Contains(out, secret) {
		t.Fatalf("logs leaked sealed plaintext")
	}
	if bytes.Contains(out, env) {
		t.Fatalf("logs leaked the opaque ciphertext body")
	}
}

// TestRelay_RetentionPurge asserts unacked mailbox items are purged once they
// pass the retention cap (evaluated on the injected clock).
func TestRelay_RetentionPurge(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.RetentionCap = 7 * 24 * time.Hour
	})
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	env := sp.sealMailbox(t, 1, []byte("stale"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	// Before the cap: the (unacked) item is still retained.
	clk.Advance(6 * 24 * time.Hour)
	srv.SweepRetention(testCtx(t))
	if items, err := device.MailboxRead(testCtx(t), 0); err != nil || len(items) != 1 {
		t.Fatalf("item purged before retention cap: got %d err=%v, want 1", len(items), err)
	}

	// Past the cap: the item is purged even though it was never acked.
	clk.Advance(2 * 24 * time.Hour)
	srv.SweepRetention(testCtx(t))
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead after purge: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("item survived past the retention cap: got %d, want 0", len(items))
	}
}
