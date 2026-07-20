package remotegw

// End-to-end read path over a REAL relay: the machine (gateway) seals journal records
// via RelaySink and appends them through a real relay.Client mailbox; the phone, on its
// own authenticated relay connection, reads its mailbox and decrypts them under the
// shared epoch content key. This proves the gateway<->relay integration (WebSocket
// auth, relay device-pairing, mailbox append/read) and the E2EE contract together, not
// just the seal/open primitives in isolation.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/status"
)

func relayAuthFor(pub ed25519.PublicKey, priv ed25519.PrivateKey) relay.ClientAuth {
	return relay.ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(challenge []byte) []byte { return ed25519.Sign(priv, challenge) },
	}
}

func TestRelayE2E_MachineForwardsJournalPhoneReads(t *testing.T) {
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer srv.Close()

	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)

	phone, err := relay.Dial(ctx, srv.URL(), relayAuthFor(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	defer phone.Close()
	machine, err := relay.Dial(ctx, srv.URL(), relayAuthFor(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	defer machine.Close()

	// The phone authorizes the machine's relay-auth key, so the machine may append to
	// the phone's mailbox (relay-level pairing, R-REL.11).
	if err := phone.AuthorizeDevice(ctx, mPub); err != nil {
		t.Fatalf("authorize device: %v", err)
	}

	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	sink := NewRelaySink(RelayConfig{
		Appender: machine,
		Target:   phone.RoutingID(),
		EpochID:  2,
		Key:      key,
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	sink.Snapshot([]protocol.JournalRecord{{Cursor: 5, SessionID: "m/s1", Type: "roster", Group: status.Group("working")}}, 5)
	sink.Event(protocol.JournalRecord{Cursor: 6, SessionID: "m/s2", Type: "launched"})
	if err := sink.Err(); err != nil {
		t.Fatalf("relay sink error: %v", err)
	}

	// The phone reads its mailbox over the real relay and decrypts each item.
	items, err := phone.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("mailbox read: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("phone read %d mailbox items; want 2", len(items))
	}
	groups := map[string]status.Group{}
	for i, it := range items {
		env, err := crypto.ParseEnvelope(it.Envelope)
		if err != nil {
			t.Fatalf("item %d parse: %v", i, err)
		}
		plain, err := crypto.OpenMailbox(key, env)
		if err != nil {
			t.Fatalf("item %d open under content key: %v", i, err)
		}
		var rec protocol.JournalRecord
		if err := json.Unmarshal(plain, &rec); err != nil {
			t.Fatalf("item %d decode: %v", i, err)
		}
		groups[rec.SessionID] = rec.Group
	}
	if groups["m/s1"] != status.Group("working") {
		t.Fatalf("s1 group = %q; want working (from the roster snapshot)", groups["m/s1"])
	}
	if _, ok := groups["m/s2"]; !ok {
		t.Fatalf("s2 (live launched event) did not arrive over the relay")
	}
}
