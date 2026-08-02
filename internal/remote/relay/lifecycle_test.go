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
	"time"
)

// routingIDKATs pin RoutingID to fixed answers for fixed relay-auth pubkeys.
//
// WHY A KNOWN-ANSWER TEST AND NOT A DETERMINISM CHECK. This fence used to read
//
//	if RoutingID(pubA) != RoutingID(pubA) { t.Fatalf("RoutingID is not deterministic") }
//
// which compares a pure function's output to itself: the branch is unreachable,
// so it asserted nothing and had been decorative since Phase 1. Determinism
// WITHIN one build is a property of the language, not of this derivation.
//
// The property that matters is stability ACROSS BUILDS. A routing id is how a
// paired handset addresses its machine, and it is derived on both sides from the
// same pubkey rather than exchanged -- so a change to the salt, the info string,
// the hash or the output length re-points every already-paired device at a
// mailbox nobody writes to. Nothing errors: the phone authenticates fine, reads
// an empty mailbox, and reports itself connected to a machine that has gone
// silent. Only a pinned answer catches that before it ships.
//
// The vectors are the edges plus one ordinary key: an all-zero pubkey, an
// all-0xff pubkey, and a fixed pattern. They are written as pubkey hex so the
// derivation can be reproduced by hand from the source of the key alone.
var routingIDKATs = []struct{ pubHex, want string }{
	{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"87c35f00062cfd5143ab04b0df39e3be",
	},
	{
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"d875d1a4cdae5b0344e2332d6a3e52b8",
	},
	{
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"223c3e746f6f290885d8b2e96e86af0e",
	},
}

// TestRelay_RoutingIDIsPinnedAcrossBuilds is the cross-build stability fence
// described on routingIDKATs.
func TestRelay_RoutingIDIsPinnedAcrossBuilds(t *testing.T) {
	for i, kat := range routingIDKATs {
		pub, err := hex.DecodeString(kat.pubHex)
		if err != nil {
			t.Fatalf("KAT %d: bad pubkey hex: %v", i, err)
		}
		got := RoutingID(ed25519.PublicKey(pub))
		if got != kat.want {
			t.Errorf("KAT %d: RoutingID(%s) = %s, want %s. The routing-id derivation has "+
				"CHANGED (salt, info, hash or output length). Every device already paired "+
				"derives the old value and will address a mailbox this relay no longer "+
				"routes -- silently, with no error on either side. If the change is "+
				"deliberate it needs a migration and an ADR, not a new pinned value",
				i, kat.pubHex, got, kat.want)
		}
	}
}

// TestRelay_MachineRegistrationAndRoutingProof asserts the routing id is an
// opaque HKDF of the relay-auth pubkey (collision-distinct, not the raw key) and
// that authenticating binds the connection to it (proof of control via the
// challenge). Cross-build stability is pinned separately, by
// TestRelay_RoutingIDIsPinnedAcrossBuilds.
func TestRelay_MachineRegistrationAndRoutingProof(t *testing.T) {
	pubA, privA := newRelayAuthKey(t)
	pubB, _ := newRelayAuthKey(t)

	// Distinct + opaque (not the raw pubkey hex).
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
	if err := m1.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, m1.RoutingID())); err != nil {
		t.Fatalf("m1.AuthorizeDevice: %v", err)
	}
	if err := m2.AuthorizeDevice(testCtx(t), ed25519.PublicKey(d2Pub), consentTo(d2Priv, m2.RoutingID())); err != nil {
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
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	wake := func(seq uint64) []byte { return sp.sealPush(t, seq, clk) }

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
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
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
	//
	// The dial NAMES ITS MACHINE, exactly as mobile/relay.go's does. A ban is scoped to the
	// relationship it ended (ADR-007 B49) rather than standing against the routing id
	// globally, because a global one made every device_revoke mutual assured destruction —
	// so the verdict is the named peer's, and this is the peer that placed it. The assertion
	// is unchanged.
	if _, err := Dial(testCtx(t), srv.URL(), authForPeer(dPub, dPriv, RoutingID(mPub))); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked device reconnect: got %v, want ErrRevoked", err)
	}
	// The relay-side mailbox is purged (no drainable pre-rotation backlog).
	if d := srv.MailboxDepth(devRID); d != 0 {
		t.Fatalf("revoked device mailbox not purged: depth %d, want 0", d)
	}
}

// TestRelay_RevokedDeviceLiveSocketClosed (ME-1) asserts r_device_revoke
// severs a CONNECTED device's live relay socket, not just its ability to
// reconnect. Against the reviewed handleDeviceRevoke (server.go:900-904) the
// revoked target's serverConn is only marked old.superseded.Store(true) and
// dropped from s.sessions — cancel()/CloseNow() on that connection's ws is
// NEVER called. serveConn's read loop (server.go:355-364) blocks on
// sc.ws.Read(sc.ctx) with no deadline once authenticated, so a device that was
// online at revoke time keeps its socket and goroutine alive until IT
// disconnects. This mirrors TestRelay_IdleHandshakeTimeout's
// blocking-read-must-unblock-within-a-bound pattern, but targets the
// REVOKED, still-connected device's own connection instead of an idle one.
func TestRelay_RevokedDeviceLiveSocketClosed(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	devRID := RoutingID(dPub)

	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}

	// Revoke while the device's own connection is still live (authenticated,
	// present in s.sessions) — this is the ME-1 scenario the deauth-only test
	// above (TestRelay_RevokedDeviceDeauthorizedAndPurged) does not cover.
	if err := machine.DeviceRevoke(testCtx(t), devRID); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}

	// A blocking read on the revoked device's OWN live connection must unblock
	// with an error (relay-initiated close) within a bound. Today it never does:
	// handleDeviceRevoke does not cancel()/CloseNow() the connected serverConn,
	// so this read hangs until the test's own deadline fires.
	done := make(chan error, 1)
	go func() {
		_, _, err := device.conn.ReadMsg()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("revoked device's live connection read returned no error; want a relay-initiated close severing the socket (ME-1)")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("revoked device's live socket not closed within bound; handleDeviceRevoke never cancels/CloseNow()s the connected serverConn (ME-1)")
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

// TestRelay_ABanIsLiftedOnlyByTheIdentityThatPlacedIt is ADR-007 B24, and its absence is
// why B22 shipped a safety argument that was false.
//
// B22 made authorize_device CLEAR the revoked bit so that revoke and re-pair stop being
// mutually exclusive (PB-STATE-10). Its reason for calling that safe was that the verb is
// served only on an AUTHENTICATED connection and a revoked routing id cannot authenticate --
// so "only the owner's machine can reach it". The second half is true and the conclusion does
// not follow: relay auth is OPEN REGISTRATION (handleAuthInit accepts any self-minted
// keypair) and handleAuthorizeDevice checks only requireAuth, with no ownership or role check
// anywhere. Authentication proves identity, not authority.
//
// So the ban has to be keyed to WHO placed it. This drives the exact path the reviewer used:
// a throwaway identity authenticates and authorizes the banned routing id by name.
//
// IT ASSERTS THE BAN, NOT THE OP. The openness itself pre-dates B22 -- any identity could
// always self-pair with any target -- so authorize_device succeeding for a stranger is not
// what changed and is not what this fences. What changed is that the same open verb started
// clearing bans.
//
// The dials NAME THE BANNING MACHINE (ADR-007 B49): a ban is scoped to the relationship it
// ended, so the verdict belongs to the peer that placed it. Every assertion is unchanged --
// what this fences is that a THIRD party cannot clear that relationship's ban, which is the
// rule B49's key-shape change preserves rather than relaxes.
func TestRelay_ABanIsLiftedOnlyByTheIdentityThatPlacedIt(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	if err := machine.DeviceRevoke(testCtx(t), RoutingID(dPub)); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}
	if _, err := Dial(testCtx(t), srv.URL(), authForPeer(dPub, dPriv, RoutingID(mPub))); !errors.Is(err, ErrRevoked) {
		t.Fatalf("precondition: the revoked device dials with %v, want ErrRevoked", err)
	}

	// The throwaway identity. Minting it and authenticating as it is the whole cost of the
	// attack: the relay has no registration step to refuse.
	aPub, aPriv := newRelayAuthKey(t)
	attacker := dialAuthed(t, srv.URL(), authFor(aPub, aPriv))
	if err := attacker.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, attacker.RoutingID())); err != nil {
		t.Fatalf("a self-minted identity could not even issue authorize_device (%v); this test "+
			"needs that path REACHED, because what it fences is the ban surviving it", err)
	}

	if _, err := Dial(testCtx(t), srv.URL(), authForPeer(dPub, dPriv, RoutingID(mPub))); !errors.Is(err, ErrRevoked) {
		t.Fatalf("ADR-007 B24: a self-minted third-party identity lifted the ban the machine "+
			"placed -- the revoked device now dials with %v, want ErrRevoked.\n"+
			"That is B22's falsified claim in one path: a revoked device mints a throwaway "+
			"keypair, authenticates as it, calls authorize_device naming its OWN revoked routing "+
			"id, and un-bans itself. End-to-end crypto is untouched (it cannot open new-epoch "+
			"frames and its commands still fail the registry signature check), so the reachable "+
			"harm is relay-plane: appends against the machine's mailbox up to the depth cap, a "+
			"denial of service against the legitimate phone. The revoked bucket must record the "+
			"BANNING routing id and authorize_device must clear a ban only when the pairer "+
			"matches the banner", err)
	}
}

// TestRelay_TheBanningMachineCanLiftItsOwnBan is the other direction of the same fence, and
// it is what stops B24's policy from being a quiet re-brick.
//
// B22's semantics are the requirement: the owner's machine authorizing a routing id IS the
// owner's decision to un-ban it, and without it revoke and re-pair are mutually exclusive and
// PB-STATE-10 is unsatisfiable. A policy that keys a ban to its placer must therefore still
// let THAT placer lift it -- which is what `swarm remote revoke` and `swarm remote pair` do,
// both over the machine's own relay identity (cmd/swarm/remote.go withMachineRelay).
//
// The dials name that same machine as their peer (ADR-007 B49), which is what makes the
// before/after pair meaningful once a ban is scoped to one relationship: the handset asks the
// banner for its verdict and gets ErrRevoked, then asks again after the re-pair and is let in.
func TestRelay_TheBanningMachineCanLiftItsOwnBan(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	if err := machine.DeviceRevoke(testCtx(t), RoutingID(dPub)); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}
	if _, err := Dial(testCtx(t), srv.URL(), authForPeer(dPub, dPriv, RoutingID(mPub))); !errors.Is(err, ErrRevoked) {
		t.Fatalf("precondition: the revoked device dials with %v, want ErrRevoked", err)
	}

	// The re-pair: the SAME machine authorizes the same handset again.
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("re-authorize after revoke: %v", err)
	}
	conn, err := Dial(testCtx(t), srv.URL(), authForPeer(dPub, dPriv, RoutingID(mPub)))
	if err != nil {
		t.Fatalf("ADR-007 B22/B24: the machine that placed the ban re-authorized the handset and "+
			"it still cannot reach the relay: %v.\nThe phone's relay-auth key is minted once per "+
			"install, so a handset that recovers without a full app-data wipe returns on the SAME "+
			"routing id. A ban its own placer cannot lift means revoke and re-pair stay mutually "+
			"exclusive and the recovery trades one brick for another", err)
	}
	_ = conn.Close()
}
