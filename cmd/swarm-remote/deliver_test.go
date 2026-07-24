package main

// FAILING-FIRST (TDD RED, GG-5) test for the gateway half of ADR-007 decision C5: on
// connect, the gateway authorizes the paired device with the relay (closing the
// dead-authorize_device gap) and delivers the persisted sealed EpochGrant to the DEVICE
// mailbox as a tagged plaintext bootstrap frame the phone can find WITHOUT a ContentKey.
//
// Stands the delivery up against the real in-process relay (relay.New/Start/URL), the
// same harness internal/skeleton's gateway E2E uses. deliverEpochGrant does not exist
// yet -- this file is intentionally RED (compile-fail) until GREEN adds it plus the
// gatewayParams.Grant / gatewayParams.DeviceRelayAuthPub fields.

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// authSpy wraps a real relay client, recording the device pubkey AuthorizeDevice was
// called for while delegating every call (incl. MailboxAppend) to the real relay.
type authSpy struct {
	*relay.Client
	authorized ed25519.PublicKey
}

func (s *authSpy) AuthorizeDevice(ctx context.Context, devicePub ed25519.PublicKey) error {
	s.authorized = devicePub
	return s.Client.AuthorizeDevice(ctx, devicePub)
}

func dialRelayClient(t *testing.T, ctx context.Context, url string) (*relay.Client, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("relay-auth key: %v", err)
	}
	c, err := relay.Dial(ctx, url, relay.ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(ch []byte) []byte { return ed25519.Sign(priv, ch) },
	})
	if err != nil {
		t.Fatalf("relay.Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, pub
}

func TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rcfg := relay.DefaultConfig()
	rcfg.Listen = "127.0.0.1:0"
	rcfg.TLSMode = "off"
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	relaySrv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := relaySrv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer relaySrv.Close()

	machineRelay, _ := dialRelayClient(t, ctx, relaySrv.URL())
	deviceRelay, devicePub := dialRelayClient(t, ctx, relaySrv.URL())

	// Seed the sealed grant exactly as enroll.Enroll produces it.
	dks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	_, signPriv, _ := ed25519.GenerateKey(nil)
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	seeded, err := crypto.SealEpochGrant(signPriv, dks.RecipientPublic(), 1, 1, keys)
	if err != nil {
		t.Fatalf("seal grant: %v", err)
	}

	p := gatewayParams{
		PhoneTarget:        deviceRelay.RoutingID(), // the device's own mailbox
		DeviceRelayAuthPub: devicePub,
		Grant:              seeded,
	}

	spy := &authSpy{Client: machineRelay}
	if err := deliverEpochGrant(ctx, spy, p); err != nil {
		t.Fatalf("deliverEpochGrant: %v", err)
	}

	// (i) AuthorizeDevice was called for the paired device (so the relay routes the
	// machine->device append at all -- an unauthorized append is refused).
	if !ed25519.PublicKey.Equal(spy.authorized, devicePub) {
		t.Fatalf("AuthorizeDevice called for %x, want the device pub %x", spy.authorized, devicePub)
	}

	// (ii) the device mailbox holds the bootstrap frame, parsing to the seeded grant.
	items, err := deviceRelay.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("device mailbox read: %v", err)
	}
	var found *crypto.EpochGrant
	for _, it := range items {
		if g, ok := grant.ParseBootstrap(it.Envelope); ok {
			found = g
			break
		}
	}
	if found == nil {
		t.Fatal("device mailbox has no epoch_grant_bootstrap frame after delivery")
	}
	if found.EpochID != seeded.EpochID || found.GrantSeq != seeded.GrantSeq ||
		string(found.Sealed) != string(seeded.Sealed) || string(found.Sig) != string(seeded.Sig) {
		t.Fatal("delivered bootstrap grant != seeded grant")
	}
}
