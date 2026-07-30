package relay

// Round-4 threat review: five unauthenticated/under-authorized abuse paths in the relay,
// each MEASURED against the shipped code rather than inferred.
//
// The common shape is ADR-007 B61's: a caller chooses how much server state one call
// costs. B61 closed that for bucketRetired by pairing a LENGTH bound with a COUNT bound,
// "which is what makes the durable footprint of one authorize_device a constant the relay
// picked". The buckets below never got the same treatment:
//
//	C1  token_register applies no length bound at all, so one call writes up to a frame
//	    (1 MiB) durably — and New -> loadTokens hydrates the WHOLE bucket at construction,
//	    so filling the store turns every subsequent boot into an OOM crash loop whose only
//	    recovery deletes every legitimate pairing edge, consent and token.
//	C2  rendezvous_create overwrites sc.rdvID without detaching the previous slot, and
//	    removeConn detaches only the LAST id — so one unauthenticated connection occupies
//	    the whole rendezvous table and keeps occupying it after it disconnects. The purge
//	    runs only inside rendezvous_create, never in runSweeps.
//	C3  a connection parked in rendezvous_recv has no deadline (readFrame waives the
//	    cumulative handshake deadline once sc.rdvID is set), no quota and no ceiling. It
//	    outlives its own rendezvous slot. Compare mailbox_wait, which is bounded on all
//	    three counts (wait.go).
//	H1  presence answers for ANY routing id after requireAuth alone. Every other verb
//	    touching someone else's route goes through a store predicate (mayActOn, isPairer).
//	    It is a liveness oracle AND an existence oracle.
//	H2  SweepPresence calls deliverPush directly; pushRate guards only handlePushTrigger.
//	    The relay decides when a machine's socket drops, so the relay can drive unbounded
//	    high-priority wakes at the owner's handset.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// --- C1: the durable half -------------------------------------------------

// TestC1_AnOversizedPushTokenNeverReachesTheStore is the disk half of C1. The only bound
// on req.Token today is MaxFrame, and meterOp keys by "rid:"+rid, so a fresh identity per
// call gets a fresh window and the op limit does not bind at all: the real cost of a
// registration is chosen by the caller, at ~1.79 MiB of unreclaimable relay.db per call
// (bbolt never returns freed pages to the OS).
//
// The fence is the B61 one: a length bound, so the durable cost of one token_register is a
// constant the relay picked. The generosity half is asserted too — a bound that refused a
// real provider token would silently disable push for a live handset (PB-PUSH-6), which is
// the failure an operator only hears about from a user who missed a hand-off.
func TestC1_AnOversizedPushTokenNeverReachesTheStore(t *testing.T) {
	srv, cfg, _, _ := startTestRelay(t, nil)

	before := dbSize(t, cfg.DBPath)

	// Eight identities, because one identity's window is not what bounds this: each is
	// free to mint and each gets its own "rid:" op window.
	const identities = 8
	huge := strings.Repeat("A", 1_000_000)
	for i := 0; i < identities; i++ {
		pub, priv := newRelayAuthKey(t)
		cl := dialAuthed(t, srv.URL(), authFor(pub, priv))
		if err := cl.TokenRegister(testCtx(t), huge); err == nil {
			t.Fatalf("identity %d: token_register accepted a %d-byte token; one unauthenticated-cost "+
				"registration must not be able to choose how many MiB of unreclaimable relay.db it writes", i, len(huge))
		}
		// A CLEAN refusal (R-REL.8): the connection is still usable, so the relay refused
		// the token rather than tearing the socket down or exhausting itself.
		if err := cl.TokenRegister(testCtx(t), strings.Repeat("f", 300)); err != nil {
			t.Fatalf("identity %d: a realistic 300-char provider token was refused after the oversized one: %v", i, err)
		}
	}

	// The bound must not have an opinion about the provider's format: a token at the
	// relay's own ceiling is accepted.
	pub, priv := newRelayAuthKey(t)
	atBound := dialAuthed(t, srv.URL(), authFor(pub, priv))
	if err := atBound.TokenRegister(testCtx(t), strings.Repeat("z", 4096)); err != nil {
		t.Fatalf("a 4096-byte token was refused: %v -- the relay's bound is meant to cover every token "+
			"length any push provider has ever been reported to issue", err)
	}

	grew := dbSize(t, cfg.DBPath) - before
	if grew > 512*1024 {
		t.Fatalf("relay.db grew %d bytes over %d registrations (%d bytes/call); the durable cost of one "+
			"token_register must be a constant the RELAY picked, not a size the caller chose",
			grew, identities, grew/identities)
	}
}

// TestC1_ARestartCannotBeMadeToHydrateAnUnboundedTokenMap is the severe half, and it is
// the one the disk measurement hides. New calls loadTokens (server.go:243, store.go:616),
// which reads the ENTIRE tokens bucket into a map at construction and fails closed if it
// cannot. So a filled store is not a disk problem that degrades: it is an OOM on EVERY
// start — a crash loop whose only recovery is deleting relay.db, which destroys every
// legitimate pairing edge, consent and token on that relay.
func TestC1_ARestartCannotBeMadeToHydrateAnUnboundedTokenMap(t *testing.T) {
	srv, cfg, apns, clk := startTestRelay(t, nil)

	const identities = 8
	huge := strings.Repeat("B", 1_000_000)
	for i := 0; i < identities; i++ {
		pub, priv := newRelayAuthKey(t)
		cl := dialAuthed(t, srv.URL(), authFor(pub, priv))
		_ = cl.TokenRegister(testCtx(t), huge)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The boot the attacker is aiming at.
	srv2, err := New(cfg, WithClock(clk), WithPushSink(apns))
	if err != nil {
		t.Fatalf("New(restart): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	srv2.mu.Lock()
	rows, resident := len(srv2.tokens), 0
	for _, tok := range srv2.tokens {
		resident += len(tok)
	}
	srv2.mu.Unlock()

	// 4096 is the relay's per-token ceiling; the assertion is that the resident cost of a
	// boot is (rows x a constant the relay picked), not (rows x whatever was sent).
	if resident > rows*4096 {
		t.Fatalf("a restart hydrated %d bytes of push tokens from %d rows (%d bytes/row); loadTokens is "+
			"unconditional and fail-closed, so an unbounded row makes every subsequent boot an OOM "+
			"crash loop recoverable only by deleting relay.db", resident, rows, resident/max(rows, 1))
	}
}

// --- C2: the rendezvous table --------------------------------------------

// TestC2_OneConnectionCannotOccupyTheWholeRendezvousTable is the measured finding:
// handleRendezvousCreate overwrites sc.rdvID/sc.rdvInbox without detaching the previous
// slot, so ONE unauthenticated connection creates as many slots as the table holds, a
// legitimate machine's create is refused quota_exceeded, and — because removeConn detaches
// only s.rendezvous[sc.rdvID], the LAST id — the slots stay occupied after the squatter
// disconnects. rendezvous_create needs no authentication.
func TestC2_OneConnectionCannotOccupyTheWholeRendezvousTable(t *testing.T) {
	const table = 4
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.MaxConcurrentRendezvous = table
		c.Quotas.MaxConcurrentConnections = 0
	})

	squatter := dialRaw(t, srv.URL())
	for i := 0; i < table; i++ {
		// Only the FIRST of these may hold a slot when the connection is done: a
		// connection has one rendezvous, so creating another must release the last.
		_ = squatter.RendezvousCreate(testCtx(t), fmt.Sprintf("squat-%d", i))
	}
	if n := liveRendezvous(srv); n > 1 {
		t.Fatalf("one connection holds %d of %d rendezvous slots; a connection participates in at most "+
			"one rendezvous, so creating another must detach the previous slot", n, table)
	}

	// The victim: a real machine showing a QR.
	victim := dialRaw(t, srv.URL())
	if err := victim.RendezvousCreate(testCtx(t), "victim-machine"); err != nil {
		t.Fatalf("a legitimate machine's rendezvous_create was refused (%v) while one unauthenticated "+
			"connection held the table; no phone on this relay can pair", err)
	}

	// And the squatter's slot does not outlive the squatter: nothing can claim it any
	// more, so holding the table entry is pure occupation.
	if err := squatter.Close(); err != nil {
		t.Fatalf("squatter.Close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for liveRendezvous(srv) > 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := liveRendezvous(srv); n != 1 {
		t.Fatalf("%d rendezvous slots are still occupied after the only connection that created them "+
			"disconnected; want 1 (the victim's)", n)
	}
}

// TestC2_AnAgedRendezvousSlotIsReclaimedWithoutAFurtherCreate pins the OTHER half:
// purgeExpiredRendezvous runs only inside handleRendezvousCreate, never in runSweeps. A
// table filled by connections that then go quiet is therefore never reclaimed until some
// stranger happens to call create — which is the one call the full table refuses.
func TestC2_AnAgedRendezvousSlotIsReclaimedWithoutAFurtherCreate(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, func(c *Config) {
		c.SweepInterval = 20 * time.Millisecond // production wiring; the TTL decision still reads clk
	})

	conn := dialRaw(t, srv.URL())
	if err := conn.RendezvousCreate(testCtx(t), "aged-slot"); err != nil {
		t.Fatalf("create: %v", err)
	}
	clk.Advance(cfg.RendezvousTTL + time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for liveRendezvous(srv) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := liveRendezvous(srv); n != 0 {
		t.Fatalf("%d expired rendezvous slots survived the maintenance sweep; the purge runs only inside "+
			"rendezvous_create, so a full table is reclaimed only by the call it refuses", n)
	}
}

// TestC2_AnOversizedRendezvousLabelIsRefusedBeforeItIsRetained is the other half of C2's
// footprint: rendezvous_create carries no requireAuth, and the id it is handed is retained
// twice (as a table key while the slot lives, as a burn-set key after it dies). Without a
// length bound the caller chose how many bytes of server memory one anonymous call cost —
// up to a frame — which is the same defect as C1 in a different bucket.
func TestC2_AnOversizedRendezvousLabelIsRefusedBeforeItIsRetained(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	conn := dialRaw(t, srv.URL())
	if err := conn.RendezvousCreate(testCtx(t), strings.Repeat("r", 200_000)); err == nil {
		t.Fatal("rendezvous_create accepted a 200000-byte label from an unauthenticated caller")
	}
	if n := liveRendezvous(srv); n != 0 {
		t.Fatalf("the rendezvous table retained %d slot(s) keyed by an unbounded label", n)
	}
	// Production sends 32 bytes (hex of the 16-byte QR field); the bound is four times
	// that, and the relay has no opinion about the alphabet.
	if err := conn.RendezvousCreate(testCtx(t), strings.Repeat("a", 128)); err != nil {
		t.Fatalf("a 128-byte rendezvous label was refused: %v", err)
	}
}

// --- C3: the immortal parked connection ----------------------------------

// TestC3_AConnectionParkedInRendezvousRecvIsBounded. readFrame waives the cumulative
// handshake deadline as soon as sc.rdvID is set, handleRendezvousRecv has no meterOp and
// no ceiling, and the park happens INSIDE dispatch — so no deadline in readFrame can ever
// apply to it. An unauthenticated socket with no deadline, no quota and no slot
// accounting, still live long after its own slot has aged out.
//
// mailbox_wait (wait.go) is the shape to match: it goes to real trouble to be bounded.
func TestC3_AConnectionParkedInRendezvousRecvIsBounded(t *testing.T) {
	const ttl = 300 * time.Millisecond
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.HandshakeTimeout = ttl
		c.RendezvousTTL = ttl
		c.Quotas.MaxConcurrentConnections = 0
	})

	parked := dialRaw(t, srv.URL())
	if err := parked.RendezvousCreate(testCtx(t), "park-forever"); err != nil {
		t.Fatalf("create: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := parked.RendezvousRecv(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("rendezvous_recv returned data nobody sent")
		}
	case <-time.After(4 * ttl):
		t.Fatalf("a connection parked in rendezvous_recv was still parked after %v (4x the rendezvous "+
			"lifetime); the park has no ceiling, so an unauthenticated caller holds a server goroutine "+
			"and a socket for as long as it likes", 4*ttl)
	}

	// And the socket does not survive its own rendezvous either: once the slot it joined is
	// dead there is nothing left for this connection to do unauthenticated. The relay sends
	// nothing unsolicited, so any read that returns here is the relay severing the socket.
	severed := make(chan error, 1)
	go func() {
		_, _, err := parked.ReadMsg()
		severed <- err
	}()
	select {
	case <-severed:
	case <-time.After(4 * ttl):
		t.Fatalf("the connection was still live %v after its rendezvous expired; an unauthenticated "+
			"connection's lifetime must be bounded by a constant the relay picked", 4*ttl)
	}
}

// TestC3_ARendezvousRecvIsMetered is the quota half of C3. handleRendezvousRecv was the
// one state-touching op with no meterOp at all — mailbox_wait, the other op that parks, is
// metered exactly once per call for precisely this reason (wait.go). Metered on a
// connection that joined nothing, so the assertion is about the meter and not about the
// park.
func TestC3_ARendezvousRecvIsMetered(t *testing.T) {
	const quota = 3
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.OpsPerMin = quota
		c.Quotas.MaxConcurrentConnections = 0
	})

	conn := dialRaw(t, srv.URL())
	for i := 0; i < quota+3; i++ {
		if _, err := conn.RendezvousRecv(testCtx(t)); errors.Is(err, ErrQuotaExceeded) {
			return
		}
	}
	t.Fatalf("rendezvous_recv admitted %d calls from one unauthenticated source with no "+
		"ErrQuotaExceeded; every state-touching op needs a per-source quota (CR-2 / R-REL.8)", quota+3)
}

// --- H1: presence authority ----------------------------------------------

// TestH1_PresenceIsNotAnOracleForAStranger. handlePresence does meterOp + requireAuth and
// then answers for ANY routing id. requireAuth proves an identity and nothing more — relay
// auth is OPEN REGISTRATION — so an identity minted seconds ago, paired with nobody, reads
// the liveness of a machine it has no edge to. Recorded in Phase 1
// (docs/verification/remote-phase1-relay-review.md:141) with the fix in one line.
//
// It is an EXISTENCE oracle as well as a liveness one, which is why the second half
// asserts the two answers are indistinguishable.
func TestH1_PresenceIsNotAnOracleForAStranger(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	machineRID := RoutingID(mPub)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}

	// The production caller (mobile/app.go: the phone asking about its own pinned
	// machine) is unaffected: it is exactly the party the machine granted.
	if p, err := device.Presence(testCtx(t), machineRID); err != nil || p.State != PresenceOnline {
		t.Fatalf("the paired device's own presence query: got %v err=%v, want PresenceOnline", p.State, err)
	}
	// A caller may always ask about ITSELF; that is no one else's route.
	if p, err := device.Presence(testCtx(t), RoutingID(dPub)); err != nil || p.State != PresenceOnline {
		t.Fatalf("a device's presence query about its own routing id: got %v err=%v, want PresenceOnline", p.State, err)
	}

	// The stranger: minted seconds ago, paired with nobody.
	sPub, sPriv := newRelayAuthKey(t)
	stranger := dialAuthed(t, srv.URL(), authFor(sPub, sPriv))
	got, err := stranger.Presence(testCtx(t), machineRID)
	if err != nil {
		t.Fatalf("stranger presence query errored (%v); it must be answered, but with nothing", err)
	}
	if got.State == PresenceOnline {
		t.Fatalf("a stranger read %q for a machine it has no edge to; every other verb touching someone "+
			"else's route goes through a store predicate (mayActOn, isPairer) and presence goes through "+
			"nothing", got.State)
	}

	// The existence half: a routing id the relay has NEVER seen must be indistinguishable
	// from one it holds but the caller has no authority over.
	unknownPub, _ := newRelayAuthKey(t)
	never, err := stranger.Presence(testCtx(t), RoutingID(unknownPub))
	if err != nil {
		t.Fatalf("presence for a never-seen routing id errored: %v", err)
	}
	if never.State != got.State {
		t.Fatalf("presence distinguishes a live-but-unauthorized route (%q) from a never-seen one (%q); "+
			"it is an existence oracle", got.State, never.State)
	}
}

// --- H2: the sweep's pushes ----------------------------------------------

// TestH2_ThePresenceSweepChargesItsPushesAgainstThePushWindow. SweepPresence calls
// deliverPush directly (server.go:1531-1558) and pushRate guards only handlePushTrigger.
// The relay decides when a machine's socket has dropped, so the relay — the declared
// adversary — can drive unbounded HIGH-PRIORITY wakes at the owner's handset while looking
// like nothing more than an unreliable network: battery, notification churn, and the
// owner's own FCM quota.
func TestH2_ThePresenceSweepChargesItsPushesAgainstThePushWindow(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.PushPerMin = 1
		c.PresenceTimeout = time.Second
	})

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-handset"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}

	mPub, mPriv := newRelayAuthKey(t)
	machineRID := RoutingID(mPub)
	{
		machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
		if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
			t.Fatalf("AuthorizeDevice: %v", err)
		}
		_ = machine.Close()
		waitDisconnected(t, srv, machineRID)
	}

	// Twelve flaps, all inside ONE rate window (2 s of logical time each, 24 s total).
	const flaps = 12
	for i := 0; i < flaps; i++ {
		clk.Advance(2 * time.Second)
		srv.SweepPresence(testCtx(t))
		machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
		_ = machine.Close()
		waitDisconnected(t, srv, machineRID)
	}
	clk.Advance(2 * time.Second)
	srv.SweepPresence(testCtx(t))

	if got := len(apns.all()); got > 1 {
		t.Fatalf("%d presence flaps produced %d delivered pushes with PushPerMin=1; the sweep's pushes "+
			"are charged against no rate window at all", flaps, got)
	}

	// The window is clock-driven, not a one-shot fuse: capacity returns.
	clk.Advance(61 * time.Second)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	_ = machine.Close()
	waitDisconnected(t, srv, machineRID)
	clk.Advance(2 * time.Second)
	srv.SweepPresence(testCtx(t))
	if got := len(apns.all()); got != 2 {
		t.Fatalf("pushes after the rate window rolled over: got %d, want 2 (one per window)", got)
	}
}

// --- helpers --------------------------------------------------------------

func dbSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat(%s): %v", path, err)
	}
	return fi.Size()
}

func liveRendezvous(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rendezvous)
}

// waitDisconnected blocks until the relay has observed rid's connection go away, so a
// clock advance that follows cannot race the server-side teardown.
func waitDisconnected(t *testing.T, s *Server, rid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		p := s.presence[rid]
		gone := p != nil && !p.connected
		s.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("relay never observed %s disconnect", rid)
}
