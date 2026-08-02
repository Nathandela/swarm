package relay

// ADR-007 B61: the durable consent store is attacker-writable without bound, and the
// consent handler never checks that the grantor and the grantee are different parties.
//
// FOUR FAILURES SHARE ONE ROOT -- handleAuthorizeDevice validates the SIGNATURE over the
// ceremony id and nothing else about the id or the parties:
//
//   (a) a party may consent to ITSELF. handleAuthorizeDevice verifies the caller's named
//       pubkey over ConsentMessage(ceremonyID, sc.rid) and then derives deviceRID from
//       that same pubkey, never checking deviceRID != sc.rid. Naming your own key
//       satisfies every check, and pairs[X\0X] is written.
//
//   (b) bucketRetired is unbounded, attacker-writable, and NOTHING anywhere deletes from
//       it. Each supersession of a pair writes one row keyed by the ceremony id, so a
//       single connection drives unbounded durable growth. bbolt never returns pages to
//       the OS, so the growth is unreclaimable.
//
//   (c) THE PUSH TOKEN IS NEVER DROPPED AGAIN once a self-edge exists. revokeAndPurge
//       forgets the token only `if !grantsAnyone(pb, rid)` (ADR-007 B49, correct in
//       isolation), and pairs[phone\0phone] makes grantsAnyone(phone) true forever. That
//       falsifies PB-PUSH-9's deletion on revoke and the PB-PUSH-6 sentence in
//       revokeAndPurge's own doc comment.
//
//   (d) AND THE OWNER'S REVOKE CAN BE BRICKED OUTRIGHT, which is the sharpest of the
//       four. A ceremony id rides into bucketConsents as a bbolt VALUE (unbounded) but
//       becomes part of a bbolt KEY (max 32768 bytes) in retiredKey -- and only at
//       supersession and at revoke. So an id above that limit is ACCEPTED at pairing and
//       then aborts the whole revokeAndPurge transaction forever after: device_revoke
//       and every re-pairing return bad_request, the pairs edges are never deleted, and
//       the owner can neither disown the device nor recover. internal/remote/device's
//       registry.go states outright that it does not validate the consent's length
//       "because the relay is the authority that verifies it" -- and the relay does not.
//
// (b) AND (d) DO NOT DEPEND ON (a), which is why none of their tests below uses a
// self-consent: the attacker signs with a SECOND keypair it minted itself and never
// connects, so a guard written only for (a) leaves both wide open. Measured on this tree
// before any fix: 200 supersessions with 32000-byte ceremony ids grew relay.db by
// 16,744,448 bytes (~84 KB/call) with NO self-edge present. That is residual 4.9 in the
// concrete -- a fence for one error class does not transfer to another in the same arm.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// b61MaxRetiredPerPair is the fence's GENEROSITY, not a specification. A real pairing
// retires one ceremony per re-pairing, and a device is re-paired a handful of times in
// its life; 64 is far above that and far below "unbounded". The implementer may pick any
// smaller bound: what is fenced is that SOME bound exists, not this number.
const b61MaxRetiredPerPair = 64

// b61ReachableCeremonyLen is a ceremony id that fits inside bbolt's 32768-byte key limit,
// so it is refused only by a real length bound and never incidentally by the storage
// engine. It is the (b) disk-amplification shape.
const b61ReachableCeremonyLen = 32000

// b61UnstorableCeremonyLen exceeds bbolt's 32768-byte key limit, so retiredKey cannot be
// written at all. It is the (d) revoke-bricking shape.
const b61UnstorableCeremonyLen = 40000

// b61LoopCtx is the context for the fences that drive hundreds of round-trips.
//
// testCtx's deadline is 5s for the WHOLE test, which is right when it bounds one
// handshake and wrong here: a loop that supersedes a pairing hundreds of times exhausts
// it, and then the subject call fails with "context deadline exceeded" for a reason that
// has nothing to do with the property under test. That is how a fence stops testing
// anything, so these tests carry their own budget and assert SPECIFIC errors.
func b61LoopCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// b61CountRetired reports how many rows bucketRetired holds.
func b61CountRetired(t *testing.T, st *store) int {
	t.Helper()
	n := 0
	if err := st.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRetired).ForEach(func(_, _ []byte) error { n++; return nil })
	}); err != nil {
		t.Fatalf("count retired_consents: %v", err)
	}
	return n
}

// b61Grantee mints a keypair the attacker owns but never connects, so every test below
// drives a consent between two DISTINCT parties without any self-edge -- the shape that
// survives a fix for (a).
func b61Grantee(t *testing.T, srv *Server) (pub []byte, sign func(ceremonyID, granteeRID string) []byte, rid string) {
	t.Helper()
	p, priv := newRelayAuthKey(t)
	return p, func(ceremonyID, granteeRID string) []byte {
		return consentToCeremony(priv, ceremonyID, granteeRID)
	}, RoutingID(p)
}

// TestB61_APartyCannotConsentToItself is (a).
//
// The consent signature proves "the named device granted this caller". When the caller
// NAMES ITSELF that statement is vacuous -- it proves only that a party holding a key
// signed something about the party holding that key -- yet it writes a real pairs edge
// that every later authority and retention decision reads. Nothing in the ceremony this
// credential is supposed to carry the outcome of can produce it: a pairing has two
// parties by construction.
func TestB61_APartyCannotConsentToItself(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	x := newB25Party(t, srv)

	err := x.cl.AuthorizeDevice(ctx, x.pub, consentToCeremony(x.priv, newTestCeremonyID(), x.rid))
	if err == nil {
		t.Fatalf("a party consented to ITSELF and the relay accepted it.\n" +
			"  handleAuthorizeDevice verifies the signature under the named pubkey and then\n" +
			"  derives deviceRID from that same pubkey, never checking deviceRID != sc.rid.\n" +
			"  The resulting pairs[X\\x00X] makes grantsAnyone(X) true forever, which disables\n" +
			"  the push-token purge PB-PUSH-9 requires (see the token test in this file).")
	}

	// The refusal must leave NO durable trace, or the edge is written anyway.
	if err := srv.st.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketPairs).Get(pairKey(x.rid, x.rid)) != nil {
			t.Errorf("the self-consent was refused but pairs[X\\x00X] was written anyway")
		}
		if tx.Bucket(bucketConsents).Get(pairKey(x.rid, x.rid)) != nil {
			t.Errorf("the self-consent was refused but a live consent row was recorded for it")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect store: %v", err)
	}
	if srv.st.mayActOn(x.rid, x.rid) {
		t.Errorf("a party may act on itself: mayActOn(X, X) = true")
	}
}

// TestB61_AnOversizedCeremonyIdCannotBrickTheOwnersRevoke is (d), and it is the reason
// the length bound is a safety fence rather than a disk-quota nicety.
//
// The device chooses the ceremony id -- it signs it -- and a hostile device is precisely
// the threat B25/B38 exist for. internal/remote/device/registry.go persists the consent
// blob without a length check by explicit design ("the relay is the authority that
// verifies it"), and cmd/swarm-remote/deliver.go hands it to the relay verbatim. So an
// oversized id is reachable through the ordinary product path, and once accepted the
// owner's `swarm remote revoke` is dead for good.
func TestB61_AnOversizedCeremonyIdCannotBrickTheOwnersRevoke(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machine := newB25Party(t, srv)
	devicePub, deviceConsent, deviceRID := b61Grantee(t, srv)

	oversized := strings.Repeat("A", b61UnstorableCeremonyLen)
	err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(oversized, machine.rid))
	if err == nil {
		// It was accepted. Name the harm in the failure rather than merely asserting a
		// bound, so the remedy is not mistaken for a disk-usage tweak.
		revErr := machine.cl.DeviceRevoke(ctx, deviceRID)
		repairErr := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(newTestCeremonyID(), machine.rid))
		t.Fatalf("a %d-byte ceremony id was ACCEPTED, and the pairing is now PERMANENTLY UNREVOKABLE.\n"+
			"  device_revoke     -> %v\n"+
			"  re-pair (recovery) -> %v\n"+
			"  still mayActOn(device -> machine) after the revoke = %v\n"+
			"  retiredKey(pairer, device, live) is %d bytes, above bbolt's 32768-byte key limit, so\n"+
			"  revokeAndPurge's Put aborts the WHOLE transaction and the pairs edges are never\n"+
			"  deleted. The device chose this id and signed it; the machine stores and replays it\n"+
			"  unvalidated by design. The owner can neither disown the device nor re-pair to\n"+
			"  recover -- ADR-007 B47/B49 and PB-STATE-10 all fail at once.",
			b61UnstorableCeremonyLen, revErr, repairErr,
			srv.st.mayActOn(deviceRID, machine.rid),
			len(retiredKey(machine.rid, deviceRID, oversized)))
	}

	// Refused -- and the refusal must not have half-written the pairing.
	if srv.st.mayActOn(deviceRID, machine.rid) || srv.st.mayActOn(machine.rid, deviceRID) {
		t.Fatalf("the oversized consent was refused but its pairs edges were written anyway")
	}

	// And the owner's revoke must still work for a legitimate pairing of the same pair:
	// the bound must refuse the credential, not poison the relationship.
	if err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(newTestCeremonyID(), machine.rid)); err != nil {
		t.Fatalf("after refusing the oversized consent, a legitimate re-pair of the same pair failed: %v", err)
	}
	if err := machine.cl.DeviceRevoke(ctx, deviceRID); err != nil {
		t.Fatalf("after refusing the oversized consent, the owner's revoke failed: %v", err)
	}
	if srv.st.mayActOn(deviceRID, machine.rid) {
		t.Fatalf("the owner revoked and the device may still act on the machine")
	}
}

// TestB61_AnOverlongCeremonyIdIsRefused is (b)'s per-row cost. This id FITS in a bbolt
// key, so it is stored happily today and nothing but a real bound refuses it: measured at
// ~84 KB of unreclaimable relay.db per call, ~72 GB/day at the configured OpsPerMin of
// 600, from one connection.
//
// The sizes below start at 1024 -- 32x what production sends -- so the implementer keeps
// a free choice of the exact cutoff. What is fenced is that a bound exists and that it
// sits far below bbolt's limit, not that it takes any particular value.
func TestB61_AnOverlongCeremonyIdIsRefused(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	for _, n := range []int{1024, 4096, b61ReachableCeremonyLen} {
		machine := newB25Party(t, srv)
		devicePub, deviceConsent, deviceRID := b61Grantee(t, srv)

		if err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(strings.Repeat("A", n), machine.rid)); err == nil {
			t.Errorf("a %d-byte ceremony id was accepted (production sends 32: hex of the 16-byte "+
				"rendezvous id, mobile/pairing.go). Each supersession writes one bucketRetired row "+
				"keyed by it, nothing ever deletes from that bucket, and bbolt never returns pages "+
				"to the OS.", n)
			if srv.st.mayActOn(deviceRID, machine.rid) {
				t.Errorf("  ... and it wrote live pairs edges")
			}
		}
	}
}

// TestB61_LegitimateCeremonyIdsAreStillAccepted is the control for the two length tests.
// IF THIS BREAKS, PAIRING IS BRICKED for every device.
//
// The first id is byte-for-byte what production sends: hex of the 16-byte rendezvous id
// (mobile/pairing.go, and the identical fixture in internal/skeleton's pairing
// integration test and every package's b47_consent_test.go). The rest are the ids the
// existing B47 suite already drives, including TestB47_AnUnknownCeremonyIsAccepted's own
// -- that test exists so deliverEpochGrant survives a store rebuild, and its failure is
// fatal to the gateway.
//
// Note the shapes: a bound on FORMAT (hex only, exactly 32 chars) would satisfy
// production and break every one of the human-readable ids below. Only a LENGTH bound
// admits both.
func TestB61_LegitimateCeremonyIdsAreStillAccepted(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	for _, cid := range []string{
		newTestCeremonyID(),               // production: hex(rendezvousID), 32 chars
		"a-ceremony-this-relay-never-saw", // TestB47_AnUnknownCeremonyIsAccepted
		"the-one-pairing-that-happened",   // TestB47 store-rebuild fixture
		"the-re-pairing-after-recovery",   // PB-STATE-10 recovery fixture
		"ceremony-A",
	} {
		machine := newB25Party(t, srv)
		devicePub, deviceConsent, _ := b61Grantee(t, srv)
		if err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(cid, machine.rid)); err != nil {
			t.Errorf("a legitimate ceremony id %q (%d bytes) was refused: %v.\n"+
				"  A bound that refuses this brings down pairing itself.", cid, len(cid), err)
		}
	}
}

// TestB61_TheRetiredConsentBucketDoesNotGrowWithoutBound is (b) proper, and it is
// deliberately driven with PRODUCTION-SHAPED ceremony ids so that NO length bound can
// satisfy it. Shortening the rows does not bound their number: at 600 ops/min a ~98-byte
// row still accrues ~86 MB/day that nothing ever reclaims.
//
// Only a retention mechanism -- a sweep, or a per-pair cap -- can make this pass. See
// TestB61_ARetiredCeremonyIsStillRefusedAfterTheBoundIsReached for the constraint that
// mechanism must respect.
func TestB61_TheRetiredConsentBucketDoesNotGrowWithoutBound(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := b61LoopCtx(t)

	machine := newB25Party(t, srv)
	devicePub, deviceConsent, _ := b61Grantee(t, srv)

	const supersessions = 256
	for i := 0; i < supersessions; i++ {
		if i%100 == 0 {
			clk.Advance(2 * time.Minute) // keep the 600/min ops quota from closing the connection
		}
		err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(newTestCeremonyID(), machine.rid))
		if i == 0 && err != nil {
			t.Fatalf("the FIRST authorize of a pairing must always be accepted: %v", err)
		}
		// After the bound is reached a refusal IS the guard working, so later errors are
		// not failures here. What is fenced is the durable footprint below.
	}

	if rows := b61CountRetired(t, srv.st); rows > b61MaxRetiredPerPair {
		t.Fatalf("one connection drove %d supersessions of ONE pair and left %d rows in "+
			"retired_consents (bound %d).\n"+
			"  Nothing in this package deletes from bucketRetired -- the only references are the "+
			"bucket-creation loop, the read in authorizePair, and the two Puts in authorizePair and "+
			"revokeAndPurge. bbolt never returns freed pages to the OS, so this growth is "+
			"unreclaimable for the life of the file.\n"+
			"  No victim, no pairing and no stolen key are involved: the grantee keypair is one the "+
			"attacker minted and never connected.",
			supersessions, rows, b61MaxRetiredPerPair)
	}
}

// TestB61_ALegitimatePairingMayBeRedoneManyTimes is the control for the bound above: the
// cap must not be so tight that an owner who re-pairs a device several times is refused.
// PB-STATE-10 recovery is a re-pairing, and a device that cannot be re-paired is bricked.
func TestB61_ALegitimatePairingMayBeRedoneManyTimes(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machine := newB25Party(t, srv)
	devicePub, deviceConsent, _ := b61Grantee(t, srv)

	for i := 0; i < 8; i++ {
		if err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(newTestCeremonyID(), machine.rid)); err != nil {
			t.Fatalf("re-pairing #%d of a legitimate pair was refused: %v.\n"+
				"  Whatever bounds retired_consents must leave ordinary re-pairing working.", i+1, err)
		}
	}
}

// TestB61_ARetiredCeremonyIsStillRefusedAfterTheBoundIsReached is the constraint that
// makes bounding retired_consents hard, and it is stated as its own fence because the
// obvious retention mechanism violates it.
//
// ADR-007 B47 requires a retired ceremony id to be refused FOREVER: that is the entire
// content of a durable revoke against a grantee who still holds the signed bytes. A sweep
// that evicts the OLDEST retirement to make room hands the attacker a way to launder a
// revoked credential back into authority -- drive enough supersessions to push the
// retirement out, then replay it. The bound and the forever-refusal are satisfiable
// together only by a mechanism that FORGETS NOTHING: refusing further supersessions once
// the cap is reached does that; evicting does not.
func TestB61_ARetiredCeremonyIsStillRefusedAfterTheBoundIsReached(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := b61LoopCtx(t)

	machine := newB25Party(t, srv)
	devicePub, deviceConsent, _ := b61Grantee(t, srv)

	// The first ceremony, and the credential its grantee keeps a copy of.
	//
	// It is all-zeroes ON PURPOSE, and it is still exactly production's shape (32 hex
	// chars, hex of a 16-byte rendezvous id). It is both the FIRST id inserted and the
	// LOWEST of them lexicographically, so it is the row that an eviction policy drops
	// first whether it evicts by insertion order, by a stored timestamp, or by bbolt's
	// own key order. A random id here would survive a lexicographic sweep ~1 time in 8
	// and make this fence flaky instead of decisive.
	first := strings.Repeat("0", 32)
	firstConsent := deviceConsent(first, machine.rid)
	if err := machine.cl.AuthorizeDevice(ctx, devicePub, firstConsent); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	// A second ceremony retires the first (authorizePair's supersession rule).
	if err := machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(newTestCeremonyID(), machine.rid)); err != nil {
		t.Fatalf("second authorize: %v", err)
	}
	// The refusal is asserted BY ITS SPECIFIC ERROR here and below, not merely as
	// "some error". A retired ceremony must come back as ErrConsentRetired -- B47's
	// whole point is that the refusal names its remedy (pair the device again). Testing
	// for err != nil would also be satisfied by a dropped connection, and that is not a
	// hypothetical: at the configured OpsPerMin of 600 an earlier draft of this test
	// exhausted the quota mid-loop, and its subject replay came back "use of closed
	// network connection" -- passing while the property under test was in fact broken.
	if err := machine.cl.AuthorizeDevice(ctx, devicePub, firstConsent); !errors.Is(err, ErrConsentRetired) {
		t.Fatalf("precondition (ADR-007 B47): a superseded ceremony must be refused with "+
			"ErrConsentRetired, got %v", err)
	}

	// Now push far past any plausible bound, exactly as an attacker would to flush the
	// retirement out of a swept bucket. The clock is advanced so the ops quota never
	// closes this connection: see the note above.
	const supersessions = 256
	for i := 0; i < supersessions; i++ {
		if i%100 == 0 {
			clk.Advance(2 * time.Minute)
		}
		_ = machine.cl.AuthorizeDevice(ctx, devicePub, deviceConsent(newTestCeremonyID(), machine.rid))
	}

	if err := machine.cl.AuthorizeDevice(ctx, devicePub, firstConsent); !errors.Is(err, ErrConsentRetired) {
		t.Fatalf("ADR-007 B47 BROKEN BY THE RETENTION MECHANISM: after %d further supersessions "+
			"the retired ceremony %q is no longer refused (got %v).\n"+
			"  Whatever bounds retired_consents evicted this retirement, and a grantee holding the "+
			"bytes a revoke left behind can now replay them back into authority by driving "+
			"supersessions. A retirement must be forgotten NEVER -- bound the bucket by refusing "+
			"new supersessions at the cap, not by dropping old retirements.", supersessions, first, err)
	}
}

// TestB61_TheOwnersRevokeForgetsThePushTokenDespiteASelfConsent is (c).
//
// It is a COMPOSED fence, and it is satisfied by (a)'s guard rather than by any change to
// revokeAndPurge -- which is the correct outcome and is stated so it is not mistaken for
// an independent defect in B49's condition. `if !grantsAnyone(pb, rid)` is right: one
// party to one relationship must not silence a handset another relationship depends on.
// What was wrong is that a party could manufacture a relationship WITH ITSELF, and
// grantsAnyone cannot tell that edge from a real one. Deliberately NOT fenced here: a
// self-edge injected below the handler, because once (a) is closed the product cannot
// construct that state and a test that reaches around the handler to build it would be
// fencing a state that does not exist.
func TestB61_TheOwnersRevokeForgetsThePushTokenDespiteASelfConsent(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)

	machine, phone := b25LegitPair(t, srv, clk)
	if err := phone.cl.TokenRegister(ctx, "phone-token"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}

	// The phone attempts to consent to itself. Once (a) is closed this is refused, and
	// the assertions below must hold either way: the owner's revoke forgets the token.
	_ = phone.cl.AuthorizeDevice(ctx, phone.pub, consentToCeremony(phone.priv, newTestCeremonyID(), phone.rid))

	if err := machine.cl.DeviceRevoke(ctx, phone.rid); err != nil {
		t.Fatalf("the owner's revoke failed: %v", err)
	}

	if tok := srv.tokens[phone.rid]; tok != "" {
		t.Errorf("PB-PUSH-9: the owner revoked the handset and the relay's write-through cache "+
			"still holds its push token (%q).\n"+
			"  One self-consent wrote pairs[phone\\x00phone], so grantsAnyone(phone) is true forever "+
			"and revokeAndPurge's `if !grantsAnyone(pb, rid)` never fires again -- for ANY revoke by "+
			"ANY party. PB-PUSH-6's 'unreachable provider-visible identifier for a device its owner "+
			"disowned' is exactly what is left behind.", tok)
	}
	if err := srv.st.db.View(func(tx *bolt.Tx) error {
		if row := tx.Bucket(bucketTokens).Get([]byte(phone.rid)); row != nil {
			t.Errorf("PB-PUSH-9: the durable push-token row survived the owner's revoke (%q). "+
				"A cache cleared while this row stands resurrects the token on the next restart.", row)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect tokens bucket: %v", err)
	}
}
