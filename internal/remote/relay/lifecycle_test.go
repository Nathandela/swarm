package relay

// A8 lifecycle (R-REL.11/.12/.13) — relay account / routing / APNs-token /
// de-authorization lifecycle. Routing id is derived from the relay-auth pubkey
// (HKDF); a machine proves control via the R-REL.2 challenge; a device is
// authorized only for its paired machine's routes; push tokens have a
// register/refresh/delete lifecycle; revocation invalidates the relay-auth
// registration AND purges the device mailbox relay-side.

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
)

// TestRelay_MachineRegistrationAndRoutingProof asserts the routing id is an
// opaque HKDF of the relay-auth pubkey (deterministic, collision-distinct, not
// the raw key) and that authenticating binds the connection to it (proof of
// control via the challenge).
func TestRelay_MachineRegistrationAndRoutingProof(t *testing.T) {
	pubA, privA := newRelayAuthKey(t)
	pubB, _ := newRelayAuthKey(t)

	// Deterministic + distinct + opaque (not the raw pubkey hex).
	if RoutingID(pubA) != RoutingID(pubA) {
		t.Fatalf("RoutingID is not deterministic")
	}
	if RoutingID(pubA) == RoutingID(pubB) {
		t.Fatalf("RoutingID collides for distinct keys")
	}
	if RoutingID(pubA) == hex.EncodeToString(pubA) {
		t.Fatalf("RoutingID is the raw pubkey, not an HKDF derivation")
	}

	srv, _, _, _ := startTestRelay(t, nil)
	machine := dialAuthed(t, srv.URL(), authFor(pubA, privA))
	if machine.RoutingID() != RoutingID(pubA) {
		t.Fatalf("authenticated routing id: got %q, want %q", machine.RoutingID(), RoutingID(pubA))
	}
}

// TestRelay_DeviceAuthorizedOnlyForPairedRoutes asserts a device is authorized
// only for its paired machine's routes: an unpaired machine cannot write to the
// device's mailbox, and a second device sees only its own mailbox (no
// cross-route access; enumeration is refused by construction — there is no list
// endpoint).
func TestRelay_DeviceAuthorizedOnlyForPairedRoutes(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)

	m1Pub, m1Priv := newRelayAuthKey(t)
	m2Pub, m2Priv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	d2Pub, d2Priv := newRelayAuthKey(t)

	m1 := dialAuthed(t, srv.URL(), authFor(m1Pub, m1Priv))
	m2 := dialAuthed(t, srv.URL(), authFor(m2Pub, m2Priv))
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	device2 := dialAuthed(t, srv.URL(), authFor(d2Pub, d2Priv))

	// m1 authorizes device; m2 authorizes device2. Routes are paired m1<->device
	// and m2<->device2.
	if err := m1.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("m1.AuthorizeDevice: %v", err)
	}
	if err := m2.AuthorizeDevice(testCtx(t), ed25519.PublicKey(d2Pub)); err != nil {
		t.Fatalf("m2.AuthorizeDevice: %v", err)
	}

	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	env := sp.sealMailbox(t, 1, []byte("for-device"), clk)

	// Paired write is allowed.
	if _, err := m1.MailboxAppend(testCtx(t), RoutingID(dPub), env); err != nil {
		t.Fatalf("paired MailboxAppend refused: %v", err)
	}
	// Cross-route write from an unpaired machine is refused.
	if _, err := m2.MailboxAppend(testCtx(t), RoutingID(dPub), env); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("cross-route append: got %v, want ErrNotAuthorized", err)
	}
	// A device may only append toward routes it is authorized for; device
	// appending into device2's route is refused.
	if _, err := device.MailboxAppend(testCtx(t), RoutingID(d2Pub), env); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("device cross-route append: got %v, want ErrNotAuthorized", err)
	}

	// Read isolation: device2 drains only its own (empty) mailbox — it never sees
	// device's item.
	items2, err := device2.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("device2 MailboxRead: %v", err)
	}
	if len(items2) != 0 {
		t.Fatalf("device2 saw %d items from a route it is not paired to", len(items2))
	}
}

// TestRelay_TokenRegisterRefreshDelete asserts the APNs push-token lifecycle:
// register targets the token, refresh replaces it, delete stops delivery.
func TestRelay_TokenRegisterRefreshDelete(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, nil)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	wake := func(seq uint64) []byte { return sp.sealMailbox(t, seq, []byte("wake"), clk) }

	// Register T1 -> push routes to T1.
	if err := device.TokenRegister(testCtx(t), "token-1"); err != nil {
		t.Fatalf("TokenRegister T1: %v", err)
	}
	if err := machine.PushTrigger(testCtx(t), devRID, wake(1)); err != nil {
		t.Fatalf("PushTrigger after T1: %v", err)
	}
	if got := apns.all(); len(got) != 1 || got[0].token != "token-1" {
		t.Fatalf("push after T1: %+v", got)
	}

	// Refresh to T2 -> push routes to T2, never the stale T1.
	if err := device.TokenRegister(testCtx(t), "token-2"); err != nil {
		t.Fatalf("TokenRegister T2 (refresh): %v", err)
	}
	if err := machine.PushTrigger(testCtx(t), devRID, wake(2)); err != nil {
		t.Fatalf("PushTrigger after T2: %v", err)
	}
	got := apns.all()
	if len(got) != 2 || got[1].token != "token-2" {
		t.Fatalf("push after refresh did not target T2: %+v", got)
	}

	// Delete -> a subsequent push has no target and reaches APNs for nobody.
	if err := device.TokenDelete(testCtx(t)); err != nil {
		t.Fatalf("TokenDelete: %v", err)
	}
	_ = machine.PushTrigger(testCtx(t), devRID, wake(3))
	if got := apns.all(); len(got) != 2 {
		t.Fatalf("push after token delete still delivered: %+v", got)
	}
}

// TestRelay_RevokedDeviceDeauthorizedAndPurged asserts r_device_revoke both
// invalidates the device's relay-auth registration (no reconnect) and purges its
// relay-side mailbox (no drainable pre-rotation backlog).
func TestRelay_RevokedDeviceDeauthorizedAndPurged(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	if _, err := machine.MailboxAppend(testCtx(t), devRID, sp.sealMailbox(t, 1, []byte("pre-revoke"), clk)); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}
	if srv.MailboxDepth(devRID) == 0 {
		t.Fatalf("precondition: device mailbox should hold the pre-revoke item")
	}

	// The machine revokes the device.
	if err := machine.DeviceRevoke(testCtx(t), devRID); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}

	// Relay-auth is invalidated: the revoked device can no longer authenticate.
	if _, err := Dial(testCtx(t), srv.URL(), authFor(dPub, dPriv)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked device reconnect: got %v, want ErrRevoked", err)
	}
	// The relay-side mailbox is purged (no drainable pre-rotation backlog).
	if d := srv.MailboxDepth(devRID); d != 0 {
		t.Fatalf("revoked device mailbox not purged: depth %d, want 0", d)
	}
}

// TestRelay_DuplicateConnectionResolved asserts a second authenticated
// connection for the same routing id is resolved deterministically: the newest
// wins (takeover) and the older connection is severed.
func TestRelay_DuplicateConnectionResolved(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	pub, priv := newRelayAuthKey(t)

	first, err := Dial(testCtx(t), srv.URL(), authFor(pub, priv))
	if err != nil {
		t.Fatalf("Dial first: %v", err)
	}
	second, err := Dial(testCtx(t), srv.URL(), authFor(pub, priv))
	if err != nil {
		t.Fatalf("Dial second (duplicate): %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	// The newest connection is live.
	if _, err := second.Presence(testCtx(t), RoutingID(pub)); err != nil {
		t.Fatalf("takeover connection not live: %v", err)
	}
	// The superseded connection is severed: its next op fails.
	if _, err := first.Presence(testCtx(t), RoutingID(pub)); !errors.Is(err, ErrDuplicateConnection) {
		t.Fatalf("superseded connection still usable: got %v, want ErrDuplicateConnection", err)
	}
}
