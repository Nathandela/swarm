package relay

// ADR-007 B60(4) -- A REVOKE IS RENEWABLE, BECAUSE THE RETIREMENT IS KEYED ON THE
// ORIENTATION OF WHOEVER FIRED IT.
//
// THE DEFECT, measured in this tree before these tests were written. pairKey(a, b) builds
// "a\x00b" and does not sort, so the two halves of one pairing are two different keys, and
// B47's retirement reads only one of them:
//
//   1. The pairing is always recorded machine-first. handleAuthorizeDevice calls
//      authorizePair(sc.rid, deviceRID, ceremonyID) and the only production callers of
//      authorize_device are the MACHINE (cmd/swarm/remote.go authorizeAtRelay and
//      cmd/swarm-remote/deliver.go deliverEpochGrant), so the live consent lands at
//      pairKey(machine, phone).
//   2. handleDeviceRevoke calls revokeAndPurge(sc.rid, req.Target), so a revoke fired by
//      the PHONE looks the consent up at pairKey(phone, machine) -- the reverse key. It
//      finds nothing and RETIRES NOTHING, while still deleting BOTH pairs edges, so the
//      revoke looks like it worked.
//   3. deliverEpochGrant re-presents the SAME stored consent bytes on every gateway
//      connect. authorizePair's retirement check passes (nothing was retired), the live
//      consent still matches, and both edges are written back.
//
// Measured store state, before/after one phone-fired revoke and one machine reconnect:
//
//	after pairing        pairs["m\x00p"], pairs["p\x00m"], consents["m\x00p"]="ceremony"
//	after phone revoke   consents["m\x00p"]="ceremony"  <- UNTOUCHED; retired_consents EMPTY
//	                     revoked["m\x00p"]              <- inert, see below
//	after re-present     both pairs edges back, authorize_device returned nil
//
// Net: the pairing returns on the machine's next connect and, because nothing about the
// store changed, the phone can revoke again and the machine can undo it again, without
// limit. That is the "renewable" in B60(4), and it is the exact property routing.go and
// store.go both claim is closed: "a retired ceremony id is refused forever. Replaying the
// bytes a revoke left behind therefore restores nothing, which is the whole of B47."
//
// THE BAN DOES NOT SAVE IT, which was checked rather than assumed. The ban revokeAndPurge
// writes is pairKey(rid, pairer) = pairKey(machine, phone), i.e. "the machine is banned BY
// the phone". Under B49 only the handset names a peer at the handshake (mobile/relay.go);
// the machine dials with no ClientAuth.Peer by deliberate decision (cmd/swarm/remote.go
// withMachineRelay), so that row is never queried by anybody. authorizePair clears the
// OTHER orientation, pairKey(device, pairer), so the re-authorize leaves it standing too.
//
// THE PHONE-FIRED REVOKE IS A STATE THIS PRODUCT CONSTRUCTS. The shipped mobile app does
// not call relay device_revoke -- mobile.RevokeThisDevice rides the sealed command plane to
// the gateway (mobile/commands.go) -- but the relay admits the call from anyone holding the
// phone's relay-auth key, because mayActOn(phone, machine) reads the machine's own grant
// over its own route, which authorizePair wrote. That is the stolen handset ADR-007 B49 is
// about, and this tree already drives it: TestB49_APhoneRevokeDoesNotDestroyTheMachine-
// RelayIdentity fires exactly this revoke and treats reaching it as a precondition.
//
// ---------------------------------------------------------------------------------------
// WHY THESE TESTS PIN A PROPERTY AND NOT A MECHANISM.
//
// There are (at least) two admissible remedies and they leave OPPOSITE observable traces:
//
//   A. Retire the credential in both orientations, inside revokeAndPurge. The phone's
//      revoke SUCCEEDS and the machine's re-presentation is then refused.
//   B. Gate the verb on authority instead: require the caller to be the PAIRER, the party
//      bucketConsents names first (consents[caller|target] != nil, an asymmetric durable
//      fact that exists only since B52). The phone's revoke is REFUSED and never severs
//      anything, so there is nothing left to renew.
//
// A test that asserted "the phone's revoke severs the pair and the replay is refused"
// forbids B. A test that asserted "the phone's revoke is refused" forbids A. Both were
// written and both were wrong; the first shape was measured failing remedy B on its
// precondition before this file was rewritten.
//
// So what is asserted here is the DISJUNCTION, which is the actual requirement:
//
//	EITHER the revoke is refused and severs NOTHING (the pairing is whole afterwards),
//	OR it is accepted and severs the pair, and the machine's next connect does not put
//	the pair back.
//
// What may never happen -- and is what happens today -- is the third state: accepted,
// severed, and then silently restored by bytes already sitting in the machine's state
// directory, with the phone never asked.
//
// IT IS NOT A FENCE THAT CANNOT FAIL. It fails today, on both defect tests. It goes green
// under remedy A and under remedy B, each verified by hand-mutating production. And it goes
// red again the moment a later change re-opens device_revoke to the phone without making
// the revoke durable -- which is the regression it exists to catch.
//
// TWO THINGS THESE TESTS DELIBERATELY DO NOT ASSERT.
//
// They do not assert that the machine's re-dial is refused. Refusing it is the mutual
// assured destruction B49 exists to have removed, and TestB49_APhoneRevokeDoesNotDestroy-
// TheMachineRelayIdentity forbids it. The machine reconnecting is a fixture here.
//
// They do not assert any particular error from authorize_device or device_revoke. Remedy A
// answers ErrConsentRetired, remedy B answers ErrNotAuthorized, and a third shape could
// accept the call and simply form no pair from it. All three satisfy every assertion below.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
)

// b60Pair is one machine/phone pairing wired the way production wires it (a single
// consented authorize_device from the machine, cf. b25LegitPair) but with the ceremony id
// NAMED, so the very same credential bytes can be re-presented later -- which is what
// deliverEpochGrant does on every gateway connect.
type b60Pair struct {
	srv     *Server
	machine b25Party
	phone   b25Party
	consent []byte // the one credential the phone ever signed
}

func newB60Pair(t *testing.T, srv *Server, clk *fakeClock, ceremonyID string) b60Pair {
	t.Helper()
	machine := newB25Party(t, srv)
	phone := newB25Party(t, srv)
	consent := consentToCeremony(phone.priv, ceremonyID, machine.rid)

	if err := machine.cl.AuthorizeDevice(testCtx(t), machine.pubOf(phone), consent); err != nil {
		t.Fatalf("machine authorizes phone (cmd/swarm/remote.go authorizeAtRelay): %v", err)
	}
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := machine.cl.MailboxAppend(testCtx(t), phone.rid, sp.sealMailbox(t, 1, []byte("m->p"), clk)); err != nil {
		t.Fatalf("precondition: the legitimately paired machine cannot append to the phone: %v", err)
	}
	return b60Pair{srv: srv, machine: machine, phone: phone, consent: consent}
}

// b60Edges is the state of the two DIRECTED authorizations of one pairing. Both are carried
// rather than isPaired, which is their conjunction and would be satisfied by a half-pair.
type b60Edges struct{ machineOverPhone, phoneOverMachine bool }

func (e b60Edges) any() bool { return e.machineOverPhone || e.phoneOverMachine }
func (e b60Edges) String() string {
	return fmt.Sprintf("machine->phone=%v phone->machine=%v", e.machineOverPhone, e.phoneOverMachine)
}

func (p b60Pair) edges() b60Edges {
	return b60Edges{
		machineOverPhone: p.srv.st.mayActOn(p.machine.rid, p.phone.rid),
		phoneOverMachine: p.srv.st.mayActOn(p.phone.rid, p.machine.rid),
	}
}

// reconnectAndRePresent is one gateway boot: the machine dials afresh (its previous socket
// was severed by the revoke, exactly as handleDeviceRevoke severs the target's) and runs
// deliverEpochGrant's authorize_device with the SAME stored bytes. The returned error is
// reported for diagnosis only -- no assertion is made on it, because refusing the call is
// one admissible remedy among several.
func (p b60Pair) reconnectAndRePresent(t *testing.T, ctx context.Context) error {
	t.Helper()
	// No ClientAuth.Peer: the machine asks for nobody's revocation verdict (ADR-007 B49,
	// cmd/swarm/remote.go withMachineRelay). If this dial is ever refused, the finding is
	// B49's, not this one.
	cl, err := Dial(ctx, p.srv.URL(), authFor(p.machine.pub, p.machine.priv))
	if err != nil {
		t.Fatalf("fixture: the machine cannot re-dial its own relay after the revoke: %v.\n"+
			"  That is ADR-007 B49's mutual assured destruction, not B60(4), and "+
			"TestB49_APhoneRevokeDoesNotDestroyTheMachineRelayIdentity is its fence.", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl.AuthorizeDevice(ctx, p.machine.pubOf(p.phone), p.consent)
}

// revokeThenReconnect fires ONE revoke from revoker at target and then boots the machine's
// gateway, which re-presents the stored consent. It states the disjunction the file header
// describes, and returns true iff the revoke was accepted, severed the pair, and the pair
// then CAME BACK -- i.e. iff the caller has just watched one turn of the renewable cycle.
//
// The two branches are both real requirements, so neither is a free pass:
//   - refused: the refusal must be TOTAL. A relay that answered an error while still
//     deleting an edge would be this same defect wearing an error code.
//   - accepted: it must actually sever, and it must stay severed across the reconnect.
func (p b60Pair) revokeThenReconnect(t *testing.T, ctx context.Context, revoker, target b25Party, where string) bool {
	t.Helper()
	before := p.edges()
	revokeErr := revoker.cl.DeviceRevoke(ctx, target.rid)
	afterRevoke := p.edges()

	if revokeErr != nil {
		// ADMISSIBLE. The relay refused the revoke outright -- remedy B's shape, and also
		// the honest answer once a previous revoke has already severed the pair. Nothing was
		// severed here, so there is nothing for a reconnect to restore.
		if (before.machineOverPhone && !afterRevoke.machineOverPhone) ||
			(before.phoneOverMachine && !afterRevoke.phoneOverMachine) {
			t.Fatalf("%s: device_revoke was REFUSED (%v) but destroyed part of the pairing "+
				"anyway (%s -> %s).\n"+
				"  A refusal that still deletes an edge leaves exactly the state B60(4) is "+
				"about: severed at the relay, restorable by replaying bytes the machine "+
				"already holds. handleDeviceRevoke must decide before it writes.",
				where, revokeErr, before, afterRevoke)
		}
		return false
	}

	// ACCEPTED. Then it has to mean something.
	if afterRevoke.any() {
		t.Fatalf("%s: device_revoke returned OK but the pairing still stands (%s).\n"+
			"  A revoke that reports success and severs nothing is worse than one that "+
			"refuses: the owner is told their remedy was applied.", where, afterRevoke)
	}

	// The machine's gateway boots and re-presents the SAME stored consent, as
	// cmd/swarm-remote/deliver.go deliverEpochGrant does on every connect. Nothing new was
	// signed and the phone was never asked.
	rerr := p.reconnectAndRePresent(t, ctx)
	back := p.edges()
	if back.any() {
		t.Logf("%s: revoked (edges gone), then the machine's connect restored the pairing "+
			"(%s; re-presentation returned %v)", where, back, rerr)
		return true
	}
	return false
}

// TestB60_APhoneFiredRevokeIsNeverUndoneByTheGatewaysNextConnect is the defect at its
// smallest: one revoke, one reconnect.
func TestB60_APhoneFiredRevokeIsNeverUndoneByTheGatewaysNextConnect(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)
	p := newB60Pair(t, srv, clk, "the-one-ceremony-the-owner-ran")

	if p.revokeThenReconnect(t, ctx, p.phone, p.machine, "phone-fired revoke") {
		t.Fatalf("ADR-007 B60(4): the phone's revoke was UNDONE by the machine's next connect.\n"+
			"  The pairing is back (%s) after re-presenting the consent stored at pairing time.\n"+
			"  pairKey(a, b) is \"a\\x00b\" and does not sort, so revokeAndPurge(phone, machine) "+
			"looked the live consent up at pairKey(phone, machine) while authorizePair had "+
			"stored it at pairKey(machine, phone). It retired NOTHING, and deleted both edges "+
			"anyway -- so the revoke looked like it worked and authorizePair's retirement check "+
			"had nothing to refuse.\n"+
			"  store.go and routing.go both state this is closed: \"a retired ceremony id is "+
			"refused forever. Replaying the bytes a revoke left behind therefore restores "+
			"nothing, which is the whole of B47.\"\n"+
			"  Either remedy closes it: retire the credential in both orientations, or refuse "+
			"a revoke from a caller that is not the pairer. This test requires neither in "+
			"particular -- only that the revoke, once made, stays made.", p.edges())
	}

	// The same statement at the wire, where it is a product behaviour rather than a storage
	// predicate -- and it is asserted only in the world where the revoke was ACCEPTED, since
	// a remedy that refuses the revoke leaves the pairing legitimately intact and the machine
	// legitimately able to append.
	if !p.edges().any() {
		sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
		cl, err := Dial(ctx, srv.URL(), authFor(p.machine.pub, p.machine.priv))
		if err != nil {
			t.Fatalf("machine re-dial for the append probe: %v", err)
		}
		t.Cleanup(func() { _ = cl.Close() })
		if _, err := cl.MailboxAppend(ctx, p.phone.rid, sp.sealMailbox(t, 2, []byte("m->p after revoke"), clk)); err == nil {
			t.Fatal("ADR-007 B60(4): the pairs edges are gone but the machine can still append to " +
				"the revoked phone's mailbox. mayActOn and handleMailboxAppend disagree, so the " +
				"hole is reachable from the side that was not measured.")
		}
	}
}

// TestB60_APhoneFiredRevokeIsNotRenewable pins the property the smaller test cannot state:
// the undo is REPEATABLE. Nothing in the store changes across a cycle -- the consent row is
// never retired and never deleted -- so revoke/reconnect is a loop the phone can never win,
// not a one-shot race it lost.
//
// Every cycle runs before anything is asserted, so the RED evidence shows the LOOP rather
// than one failure. Under either remedy the loop simply produces no restorations: remedy B
// refuses every cycle, remedy A accepts the first and refuses the rest (there is nothing
// left to revoke), and revokeThenReconnect holds both to their own requirement.
func TestB60_APhoneFiredRevokeIsNotRenewable(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)
	p := newB60Pair(t, srv, clk, "the-one-ceremony-the-owner-ran")

	const cycles = 3
	var restored []int
	for cycle := 1; cycle <= cycles; cycle++ {
		if p.revokeThenReconnect(t, ctx, p.phone, p.machine, fmt.Sprintf("cycle %d", cycle)) {
			restored = append(restored, cycle)
		}
	}
	if len(restored) > 0 {
		t.Fatalf("ADR-007 B60(4): the revoke is RENEWABLE. The phone revoked and the machine's "+
			"next connect put the pairing straight back on cycles %v of %d.\n"+
			"  Every cycle leaves the store byte-identical -- revokeAndPurge(phone, machine) "+
			"reads pairKey(phone, machine) and the live consent sits at pairKey(machine, phone), "+
			"so nothing is ever retired and nothing is ever consumed -- and the loop has no exit. "+
			"The revoked party cannot revoke its way out, and no number of revokes is more "+
			"durable than one.", restored, cycles)
	}
}

// TestB60_TheMachineFiredRevokeStaysMade is the ORIENTATION CONTROL, and it is green before
// any fix as well as after. It states the SAME property as the two tests above, through the
// same helper, with only the direction of the revoke changed. That is what makes the pair of
// results a measurement of ORIENTATION rather than of revocation in general.
//
// It is also the fence over the cheapest wrong fix. A remedy that merely moves the
// retirement to the other key -- retiredKey(rid, pairer, ...) inside revokeAndPurge --
// turns the tests above green and turns THIS one red, which is the same defect facing the
// other way. `swarm remote revoke` is the owner's only remedy for a lost handset and it
// runs in exactly this direction (cmd/swarm/remote.go purgeRelayState).
func TestB60_TheMachineFiredRevokeStaysMade(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)
	p := newB60Pair(t, srv, clk, "the-one-ceremony-the-owner-ran")

	if p.revokeThenReconnect(t, ctx, p.machine, p.phone, "machine-fired revoke") {
		t.Fatalf("the MACHINE-fired revoke was undone by a re-presentation of the stored consent "+
			"(%s).\n"+
			"  This direction is B47's own case and was green before B60(4) was addressed. If it "+
			"is red now, the retirement has been moved to the other orientation rather than made "+
			"orientation-free, and the defect merely changed which party it favours.", p.edges())
	}
	if p.edges().any() {
		t.Fatalf("the machine's own `swarm remote revoke` did not sever the pairing (%s)", p.edges())
	}
}

// TestB60_AFreshCeremonyStillPairsAfterAPhoneFiredRevoke is the NON-VACUITY CONTROL, and it
// is the one that makes a remedy admissible at all rather than merely effective.
//
// PB-STATE-10 requires that revoke and re-pair not be mutually exclusive (ADR-007 B22, B47).
// A remedy that closed B60(4) by refusing every authorize_device for a pair that was ever
// revoked, or by leaving the pair permanently unpairable after a contested revoke, would
// turn the two tests above green and re-brick the owner's only recovery. What must be
// refused is the RETIRED CREDENTIAL or the UNAUTHORIZED CALLER -- never the pairing.
//
// It asserts the same outcome whatever the phone's revoke did, which is deliberate: under a
// remedy that refuses the revoke the pairing is still whole and the fresh consent supersedes
// the old one; under a remedy that accepts it the fresh ceremony is the recovery. Both end
// with the owner able to reach their phone. Green today, and it must stay green.
func TestB60_AFreshCeremonyStillPairsAfterAPhoneFiredRevoke(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	ctx := testCtx(t)
	p := newB60Pair(t, srv, clk, "the-ceremony-before-the-revoke")

	// Fired without assertion: whether it is admitted is the subject of the tests above, and
	// this one must hold either way.
	revokeErr := p.phone.cl.DeviceRevoke(ctx, p.machine.rid)

	// The owner runs `swarm remote pair` again: a NEW ceremony, so a new id, and the phone
	// signs a new consent in front of them.
	cl, err := Dial(ctx, srv.URL(), authFor(p.machine.pub, p.machine.priv))
	if err != nil {
		t.Fatalf("the machine cannot re-dial to re-pair: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	fresh := consentToCeremony(p.phone.priv, "the-re-pairing-the-owner-ran", p.machine.rid)
	if err := cl.AuthorizeDevice(ctx, ed25519.PublicKey(p.phone.pub), fresh); err != nil {
		t.Fatalf("re-pairing with a FRESH ceremony after a phone-fired revoke (which returned "+
			"%v): %v.\n"+
			"  Closing B60(4) must refuse the retired CREDENTIAL or the unauthorized CALLER, "+
			"never the pairing. Keying a refusal on \"this pair was revoked\" passes the B60 "+
			"tests and re-bricks PB-STATE-10, which is the wall ADR-007 B22 removed and B47 was "+
			"careful not to rebuild.", revokeErr, err)
	}
	if e := p.edges(); !e.machineOverPhone || !e.phoneOverMachine {
		t.Fatalf("the fresh ceremony was accepted but formed no pairing (%s)", e)
	}
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := cl.MailboxAppend(ctx, p.phone.rid, sp.sealMailbox(t, 3, []byte("m->p re-paired"), clk)); err != nil {
		t.Fatalf("the re-paired machine cannot reach its phone: %v", err)
	}
}
