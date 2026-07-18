package relay

// R-REL.9 — relay ops (lean). cmd/swarm-relay reads one config file and boots;
// E2EE confidentiality does NOT depend on TLS (TLS = metadata defense only), so
// a full round-trip works over plain ws:// on localhost.

import (
	"crypto/ed25519"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestRelay_BootsFromConfigLocalhost asserts the relay boots from a written
// config file (round-tripped through LoadConfig) and serves on localhost.
func TestRelay_BootsFromConfigLocalhost(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "relay.conf")

	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(dir, "relay.db")
	if err := WriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("WriteConfigFile: %v", err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Listen != cfg.Listen || loaded.DBPath != cfg.DBPath || loaded.TLSMode != cfg.TLSMode {
		t.Fatalf("LoadConfig did not round-trip: got %+v want %+v", loaded, cfg)
	}

	srv, err := New(loaded)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(testCtx(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The booted relay speaks the protocol on its localhost address.
	conn := dialRaw(t, srv.URL())
	if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
		t.Fatalf("Hello against config-booted relay: %v", err)
	}
}

// TestRelay_E2EEOverPlainWS runs a full mailbox round-trip over plain ws://,
// proving confidentiality does not depend on TLS: the machine seals under the
// content key, the relay stores/serves opaque bytes, and the device recovers the
// exact plaintext.
func TestRelay_E2EEOverPlainWS(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil) // TLSMode "off" -> plain ws://
	if !strings.HasPrefix(srv.URL(), "ws://") {
		t.Fatalf("test relay is not plain ws://: %q", srv.URL())
	}

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}

	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	plaintext := []byte("confidential session content over plain ws")
	env := sp.sealMailbox(t, 1, plaintext, clk)
	if _, err := machine.MailboxAppend(testCtx(t), RoutingID(dPub), env); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}

	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("read count: got %d, want 1", len(items))
	}
	parsed, err := crypto.ParseEnvelope(items[0].Envelope)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	got, err := crypto.OpenMailbox(sp.keys.ContentKey, parsed)
	if err != nil {
		t.Fatalf("OpenMailbox (confidentiality over plain ws should hold): %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-tripped plaintext: got %q, want %q", got, plaintext)
	}
}
