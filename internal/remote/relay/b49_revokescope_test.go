package relay

// ADR-007 B49 -- EVERY device_revoke IS MUTUAL ASSURED DESTRUCTION, AND THESE ARE ITS FENCES.
//
// THE DEFECT, measured in this tree before the tests were written. The ban is enforced at
// REGISTRATION (handleAuthInit's isRevoked) and it is GLOBAL: one bucket entry keyed by the
// banned routing id alone, consulted by every dial that routing id ever makes. authorizePair
// is its only deleter, and only when the caller IS the recorded banner (B24). So the ban a
// phone places on the machine can be lifted by the phone and by nobody else -- and the lift
// requires an authorize_device naming the machine, which since B38 demands a signature under
// the MACHINE's private relay-auth key. The phone has never held that key. The two conditions
// are individually satisfiable and JOINTLY UNSATISFIABLE: whoever fires first permanently
// removes the other's relay identity.
//
// It has two entry points and they are one defect. B40's is a stolen handset firing at the
// machine; B46's is an interceptor holding a harvested consent firing at the phone.
//
// WHAT THESE TESTS ASSERT. One property, stated once: a revoke severs the relationship the
// revoker is party to, and does not destroy the counterparty's ability to exist at the relay
// or to be recovered by anyone else. That covers registration (B49), the counterparty's
// queued frames and its push token (ADR-007 B27's objection to B26, which said scoping the
// ban does not scope the PURGE), and the revoked signal PB-APP-10 depends on, which must
// survive the scoping rather than be traded away for it.
//
// WHAT THEY DELIBERATELY DO NOT ASSERT. Nothing here says how a phone comes to hold a
// harvested consent -- that is B46's disclosure and B47's remedy, and it is granted outright
// in the fixtures below, because what is fenced is what a revoke DESTROYS, not how the
// pairing that authorised it came to exist.

import (
	"bytes"
	"errors"
	"testing"
)

// authForPeer is authFor plus the one coordinate a dialer states about itself: the peer
// whose revocation of this identity it wants to be told about at the handshake.
//
// It exists because the relay cannot tell a machine from a handset. After a revoke both
// parties hold the identical relay state -- no edges, one ban -- so any registration rule
// symmetric in (banner, victim) either refuses both (the mutual assured destruction B49
// measured) or neither (PB-APP-10's signal lost). The asymmetry has to come from the dialer,
// and it is the one fact only the dialer holds: which peer's verdict it is here for.
func authForPeer(pub, priv []byte, peer string) ClientAuth {
	a := authFor(pub, priv)
	a.Peer = peer
	return a
}

// TestB49_APhoneRevokeDoesNotDestroyTheMachineRelayIdentity is B40's entry point, and the
// measurement ADR-007 B49 records verbatim: legitimate consented pair, phone revokes machine,
// machine's re-dial refused, and the phone's own authorize_device naming the machine answers
// `not authorized for route` because it cannot produce the machine's signature.
//
// THE ASSERTION IS ON THE MACHINE'S NEXT DIAL AND ON WHAT IT CAN DO AFTERWARDS. A fix that
// merely let the machine reconnect while leaving it unable to pair again would satisfy a
// weaker test and leave the owner with a machine that can talk to nobody -- so the recovery
// the owner actually performs (`swarm remote pair` against a replacement handset) is driven
// end to end.
//
// THE SETUP WAS RESTATED WHEN ADR-007 B60(4) CLOSED, AND NO ASSERTION BELOW CHANGED. The
// fixture used to be one line -- b25LegitPair, then the phone revokes the machine -- because
// handleDeviceRevoke gated on store.mayActOn, which a legitimately paired phone satisfies
// against its own machine. B60(4) showed that admitting that call was itself the defect: the
// revoke landed in the orientation OPPOSITE the one bucketConsents stores the pairing in, so
// it retired nothing and the machine's next deliverEpochGrant put the pairing back, without
// limit. device_revoke now requires the caller to be the PAIRER, so the one-line fixture no
// longer reaches a revoke at all and this test would have measured nothing.
//
// So the credential that admits the revoke is GRANTED OUTRIGHT, exactly as the harvested
// consent is granted to the interceptor in the next test and for the same stated reason: what
// is fenced here is what a revoke DESTROYS, never how the pairing that authorised it came to
// exist. The phone is made the machine's pairer too, which puts a REAL ban on the machine
// (revoked[machine|phone]) and so keeps every assertion below load-bearing -- a fixture whose
// revoke was refused would leave no ban, and the machine's survival would be vacuous.
//
// That this pairing direction has no production producer today (only mobile/pairing.go signs
// a route consent, and it names the MACHINE as pairer) is the point rather than an objection:
// B49's property must hold for ANY admitted revoke fired at the machine, and this is the fence
// that still catches it if a later change re-opens one -- which is what B60(4) was.
func TestB49_APhoneRevokeDoesNotDestroyTheMachineRelayIdentity(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone := b25LegitPair(t, srv, clk)

	// The handset holds a consent under the machine's own relay-auth key, so it is the
	// machine's pairer and device_revoke is open to it (ADR-007 B60(4)).
	if err := phone.cl.AuthorizeDevice(testCtx(t), phone.pubOf(machine),
		consentTo(machine.priv, phone.rid)); err != nil {
		t.Fatalf("precondition: the granted consent did not make the handset the machine's "+
			"pairer (%v); this test needs a revoke AT the machine reached, because what it "+
			"measures is the machine surviving one", err)
	}

	// The stolen handset fires.
	if err := phone.cl.DeviceRevoke(testCtx(t), machine.rid); err != nil {
		t.Fatalf("precondition: the handset could not revoke the machine (%v); this test "+
			"needs that path REACHED, because what it measures is the machine surviving it", err)
	}
	// NON-VACUITY. The ban this test is about must actually stand against the machine;
	// without it the dial below proves nothing.
	if !srv.st.revokedBy(machine.rid, phone.rid) {
		t.Fatal("precondition: the handset's revoke was accepted but placed no ban on the " +
			"machine, so the survival asserted below is vacuous")
	}

	// THE PROPERTY. The machine still exists at the relay.
	cl, err := Dial(testCtx(t), srv.URL(), authFor(machine.pub, machine.priv))
	if err != nil {
		t.Fatalf("ADR-007 B49: a handset the owner had paired PERMANENTLY DESTROYED the machine's "+
			"relay identity. The machine re-dialling its own relay: %v.\n"+
			"  The ban is global (handleAuthInit's isRevoked) and only its banner may lift it "+
			"(B24), and the banner is the handset -- which cannot lift it either, because "+
			"authorize_device naming the machine needs a signature under the MACHINE's private "+
			"relay-auth key (B38) that the handset has never held. The two conditions are "+
			"individually satisfiable and jointly unsatisfiable.\n"+
			"  A revoke must sever the relationship the revoker is party to and nothing else.", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	// AND IT IS RECOVERABLE BY THE OWNER, which is the half a bare reconnect does not prove.
	// A replacement handset, paired the way cmd/swarm/remote.go authorizeAtRelay pairs one.
	replacement := newB25Party(t, srv)
	if err := cl.AuthorizeDevice(testCtx(t), replacement.pubOf(replacement),
		consentTo(replacement.priv, machine.rid)); err != nil {
		t.Fatalf("ADR-007 B49: the machine reconnected but cannot pair a replacement handset: %v.\n"+
			"  Reconnecting is not recovery; the owner's remedy for a lost handset is a re-pair.", err)
	}
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := cl.MailboxAppend(testCtx(t), replacement.rid, sp.sealMailbox(t, 9, []byte("m->new"), clk)); err != nil {
		t.Fatalf("ADR-007 B49: the machine paired a replacement handset but cannot reach it: %v", err)
	}
}

// TestB49_AnInterceptorRevokeDoesNotDestroyThePhoneRelayIdentity is B46's entry point --
// the same defect fired the other way.
//
// The interceptor holds the phone's consent signature, which pairing msg3 discloses before
// the SAS check (B38's disclosure route 3). That is granted here rather than derived: what is
// fenced is that a party the owner never authorised cannot, with one revoke, remove the
// phone from the relay for good and strand it from the machine it IS paired with.
func TestB49_AnInterceptorRevokeDoesNotDestroyThePhoneRelayIdentity(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone := b25LegitPair(t, srv, clk)

	// The interceptor pairs with the phone off the harvested consent (ADR-007 B46).
	interceptor := newB25Party(t, srv)
	if err := interceptor.cl.AuthorizeDevice(testCtx(t), interceptor.pubOf(phone),
		consentTo(phone.priv, interceptor.rid)); err != nil {
		t.Fatalf("precondition: the harvested consent did not form a pairing (%v); this test needs "+
			"that path REACHED, because what it measures is the phone surviving the revoke it enables", err)
	}
	if err := interceptor.cl.DeviceRevoke(testCtx(t), phone.rid); err != nil {
		t.Fatalf("precondition: the interceptor's revoke was refused (%v)", err)
	}

	// THE PROPERTY. The phone still exists at the relay, and it still names its OWN machine
	// as the peer whose verdict it is here for -- which the interceptor is not.
	cl, err := Dial(testCtx(t), srv.URL(), authForPeer(phone.pub, phone.priv, machine.rid))
	if err != nil {
		t.Fatalf("ADR-007 B49/B46: a party holding nothing but a harvested consent signature "+
			"PERMANENTLY DESTROYED the phone's relay identity. The phone re-dialling: %v.\n"+
			"  The ban is global, so a ban placed by ANY party refuses EVERY dial; only its banner "+
			"may lift it, and the banner is the attacker.", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	// AND THE RELATIONSHIP THE INTERCEPTOR WAS NEVER PARTY TO IS UNTOUCHED.
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := machine.cl.MailboxAppend(testCtx(t), phone.rid, sp.sealMailbox(t, 11, []byte("m->p after"), clk)); err != nil {
		t.Fatalf("ADR-007 B49: the interceptor's revoke severed the phone's pairing with its own "+
			"machine: %v.\n  revokeAndPurge must delete only the two edges of the pair it names", err)
	}
}

// TestB49_TheRevokedSignalSurvivesTheScoping is the requirement the scoping must not trade
// away, and the reason B26's direction has to be checked rather than argued.
//
// PB-APP-10 needs a revoked handset to be TOLD, as an explicit re-pair prompt, and the only
// signal it ever gets is relay.ErrRevoked at the handshake (mobile/relay.go's dial switch ->
// connRevoked; ADR-007 B23 records the state sequence). Pair-scoping a ban that is enforced
// at registration removes exactly that, so the signal has to be re-established at the same
// delivery point: the dialer names the peer it is here for, and the relay answers for THAT
// peer's ban and no other.
//
// Both directions are asserted, because either alone is satisfiable by a mistake: a relay
// that answers codeRevoked for every dial has not scoped anything, and one that never
// answers it has thrown PB-APP-10 away.
//
// THE LAST LEG IS THE JOINT FENCE OVER B47 AND B49, and it is why this test builds its
// pairing by hand instead of through b25LegitPair: it has to HOLD the consent bytes the
// ceremony produced, so it can present the same ones again.
//
// Neither fix is sufficient alone and the tree must show both. B47 retires the ceremony a
// revoke ends, so the durable signature sitting in the machine's state directory can no
// longer undo the owner's only remedy. B49 makes the ban survivable in the first place --
// before it, the lift was reachable ONLY by that replay, so closing the replay without
// this would have left a revoke with no legitimate undo at all. Together: the replay is
// refused, and a fresh ceremony still recovers the handset end to end.
//
// IT IS ALSO THE VACUITY CONTROL, and it is the trap B47 records rather than argues. The
// forbidden shape -- "refuse any consent for a pair that is currently revoked" -- PASSES
// the replay assertion and FAILS the recovery assertion below, because a recovered
// handset's re-pair is a consent for a currently-revoked pair too. A fence that asserted
// only the refusal would go green on the change that re-bricks PB-STATE-10.
func TestB49_TheRevokedSignalSurvivesTheScoping(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	machine := newB25Party(t, srv)
	phone := newB25Party(t, srv)

	// The pairing ceremony the owner ran, with its consent kept for the replay below.
	firstConsent := consentToCeremony(phone.priv, newTestCeremonyID(), machine.rid)
	if err := machine.cl.AuthorizeDevice(testCtx(t), machine.pubOf(phone), firstConsent); err != nil {
		t.Fatalf("the owner's pairing (cmd/swarm/remote.go authorizeAtRelay): %v", err)
	}

	if err := machine.cl.DeviceRevoke(testCtx(t), phone.rid); err != nil {
		t.Fatalf("machine revokes phone (`swarm remote revoke`): %v", err)
	}

	// THE SIGNAL. The handset names its pinned machine, exactly as mobile/relay.go's dial does.
	if _, err := Dial(testCtx(t), srv.URL(), authForPeer(phone.pub, phone.priv, machine.rid)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("PB-APP-10 / ADR-007 B23: a handset its own machine revoked dialled with %v, want "+
			"ErrRevoked.\n"+
			"  relay.ErrRevoked is the ONLY signal a revoked handset ever gets. Without it "+
			"mobile/relay.go's dial switch has no arm to match, and the phone redials every 250 ms "+
			"forever behind a spinner -- the failure LOOP the requirement forbids, reached by the "+
			"owner doing exactly what the product says to do when a handset is lost.", err)
	}

	// AND IT IS SCOPED. The same ban says nothing to a dialer that is here about another peer.
	third := newB25Party(t, srv)
	cl, err := Dial(testCtx(t), srv.URL(), authForPeer(phone.pub, phone.priv, third.rid))
	if err != nil {
		t.Fatalf("ADR-007 B49: the ban answered a dial about a peer that never placed it: %v.\n"+
			"  A global ban is what makes every revoke mutual assured destruction; the verdict "+
			"must be the named peer's alone.", err)
	}
	_ = cl.Close()

	// THE REPLAY DOES NOT LIFT IT (ADR-007 B47). The identical bytes the pairing produced are
	// still on the machine's disk, and re-presenting them used to rewrite both edges and clear
	// the ban in one transaction -- the owner's whole remedy undone by a file, with the phone
	// never asked. The retirement check runs in handleAuthorizeDevice, strictly upstream of
	// authorizePair, so the ban-clear below is never reached by these bytes.
	if err := machine.cl.AuthorizeDevice(testCtx(t), machine.pubOf(phone), firstConsent); !errors.Is(err, ErrConsentRetired) {
		t.Fatalf("ADR-007 B47: replaying the retired ceremony's consent returned %v, want "+
			"ErrConsentRetired.\n"+
			"  B49 makes the ban survivable; B47 is what stops it being lifted by bytes the grantee "+
			"already holds. Neither is sufficient alone.", err)
	}
	if _, err := Dial(testCtx(t), srv.URL(), authForPeer(phone.pub, phone.priv, machine.rid)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("ADR-007 B47/B49: the replayed consent was refused and the handset is un-banned "+
			"anyway (dial = %v, want ErrRevoked) -- so something else cleared the ban", err)
	}

	// AND A FRESH CEREMONY DOES (ADR-007 B22/B24, and PB-STATE-10's whole recovery).
	// The owner recovers the handset and pairs again: a new ceremony, a new id, and the phone
	// signs in front of them. THIS is the assertion the forbidden shape fails -- a re-pair is
	// a consent for a currently-revoked pair, so a relay that refused those would stop here.
	if err := machine.cl.AuthorizeDevice(testCtx(t), machine.pubOf(phone),
		consentToCeremony(phone.priv, newTestCeremonyID(), machine.rid)); err != nil {
		t.Fatalf("the owner's re-pair (cmd/swarm/remote.go authorizeAtRelay): %v.\n"+
			"  A fresh ceremony is PB-STATE-10's only remedy for a recovered handset; refusing it "+
			"keyed on the pair being revoked re-bricks the recovery B22 removed.", err)
	}
	back, err := Dial(testCtx(t), srv.URL(), authForPeer(phone.pub, phone.priv, machine.rid))
	if err != nil {
		t.Fatalf("ADR-007 B22/B24: the machine that placed the ban re-authorized the handset and it "+
			"still cannot reach the relay: %v.\n  Revoke and re-pair must not be mutually exclusive "+
			"(PB-STATE-10: fail closed must not mean bricked).", err)
	}
	_ = back.Close()
}

// TestB49_ARevokeDoesNotDestroyAnotherSendersQueuedFrames is ADR-007 B27's objection to B26,
// answered rather than argued around.
//
// B27 falsified pair-scoping with: revokeAndPurge ALSO deletes the target's whole mailbox and
// its push token, both keyed per TARGET, so scoping the ban does not scope the purge. The
// anonymous reach that made that critical is gone -- B38's consent signature means nothing
// reaches device_revoke without the target's own signature -- but the objection is structural
// and it is met here in its own terms: the record already carries its SENDER (store.go's
// recordV1, what mailboxDepthFrom reads), so a revoke can delete exactly the revoker's frames.
func TestB49_ARevokeDoesNotDestroyAnotherSendersQueuedFrames(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone := b25LegitPair(t, srv, clk)

	interceptor := newB25Party(t, srv)
	if err := interceptor.cl.AuthorizeDevice(testCtx(t), interceptor.pubOf(phone),
		consentTo(phone.priv, interceptor.rid)); err != nil {
		t.Fatalf("precondition: harvested-consent pairing: %v", err)
	}

	// THE REVOKER'S FRAMES ARE ADJACENT AND INTERLEAVED WITH THE MACHINE'S, deliberately.
	// A scoped purge walks the mailbox deleting under a cursor, and a cursor that advances
	// after a delete can step over the element that shifted into the freed slot -- so two
	// ADJACENT frames from the revoker is the case that catches a purge which silently leaves
	// half the backlog drainable, and a scoped purge that only ever deletes one frame at a
	// time would never notice.
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	fromMachine := sp.sealMailbox(t, 21, []byte("undelivered machine output"), clk)
	if _, err := machine.cl.MailboxAppend(testCtx(t), phone.rid, fromMachine); err != nil {
		t.Fatalf("machine appends to the phone: %v", err)
	}
	for i, body := range [][]byte{[]byte("interceptor-1"), []byte("interceptor-2")} {
		if _, err := interceptor.cl.MailboxAppend(testCtx(t), phone.rid,
			sp.sealMailbox(t, uint64(22+i), body, clk)); err != nil {
			t.Fatalf("interceptor appends to the phone: %v", err)
		}
	}
	fromMachine2 := sp.sealMailbox(t, 24, []byte("more machine output"), clk)
	if _, err := machine.cl.MailboxAppend(testCtx(t), phone.rid, fromMachine2); err != nil {
		t.Fatalf("machine appends to the phone again: %v", err)
	}
	before := srv.MailboxDepth(phone.rid)

	if err := interceptor.cl.DeviceRevoke(testCtx(t), phone.rid); err != nil {
		t.Fatalf("interceptor revokes the phone: %v", err)
	}

	if d := srv.MailboxDepth(phone.rid); d != before-2 {
		t.Fatalf("ADR-007 B27: a revoke destroyed %d of the %d frames queued for the phone, not the "+
			"2 the revoker had sent.\n"+
			"  revokeAndPurge deletes the target's ENTIRE mailbox, which is keyed per target and "+
			"not per pair, so a party to one relationship destroys every other party's undelivered "+
			"output on demand. The stored record carries its sender; the purge must use it.", before-d, before)
	}
	// The surviving frames must be the MACHINE's, byte for byte -- a purge that kept the
	// wrong one would satisfy a count. (The fixture's own m->p frame is queued too, which is
	// why this reads the mailbox rather than trusting the depth.)
	cl := dialAuthed(t, srv.URL(), authForPeer(phone.pub, phone.priv, machine.rid))
	items, err := cl.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("phone reads its mailbox: %v", err)
	}
	survived := 0
	for _, it := range items {
		if bytes.Equal(it.Envelope, fromMachine) || bytes.Equal(it.Envelope, fromMachine2) {
			survived++
		}
	}
	if survived != 2 {
		t.Fatalf("ADR-007 B27: %d of the machine's 2 undelivered frames survived the interceptor's "+
			"revoke (%d items back in all)", survived, len(items))
	}
}

// TestB49_ARevokeSilencesThePhoneOnlyWhenItSeversItsLastRelationship is the other half of
// B27's objection: the push token is keyed per target too.
//
// The token is what a BACKGROUNDED handset is woken by (ADR-007 B16 disconnects it), so
// dropping it is not a nuisance the next reconnect repairs -- with no wake there is no next
// reconnect. A party to one relationship must not be able to silence a handset another
// relationship still depends on; and PB-PUSH-6's requirement that a revoked device's token is
// forgotten must survive intact, so both directions are asserted here.
func TestB49_ARevokeSilencesThePhoneOnlyWhenItSeversItsLastRelationship(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, nil)
	machine, phone := b25LegitPair(t, srv, clk)

	if err := phone.cl.TokenRegister(testCtx(t), "phone-token"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	interceptor := newB25Party(t, srv)
	if err := interceptor.cl.AuthorizeDevice(testCtx(t), interceptor.pubOf(phone),
		consentTo(phone.priv, interceptor.rid)); err != nil {
		t.Fatalf("precondition: harvested-consent pairing: %v", err)
	}

	// A revoke that leaves the phone's pairing with its machine standing must leave the phone
	// wakeable BY THAT MACHINE.
	if err := interceptor.cl.DeviceRevoke(testCtx(t), phone.rid); err != nil {
		t.Fatalf("interceptor revokes the phone: %v", err)
	}
	if err := machine.cl.PushTrigger(testCtx(t), phone.rid, []byte("wake")); err != nil {
		t.Fatalf("machine push after the interceptor's revoke: %v", err)
	}
	if got := len(apns.all()); got != 1 {
		t.Fatalf("ADR-007 B27: the interceptor's revoke SILENCED the phone -- the machine's wake "+
			"reached the provider %d times, want 1.\n"+
			"  revokeAndPurge drops the target's push token, which is keyed per target and not per "+
			"pair. A backgrounded handset is disconnected (ADR-007 B16), so with no token there is "+
			"no wake, no reconnect, and no re-registration to repair it: the silence is permanent.", got)
	}

	// And the revoke that severs the LAST relationship still forgets it (PB-PUSH-6).
	if err := machine.cl.DeviceRevoke(testCtx(t), phone.rid); err != nil {
		t.Fatalf("machine revokes the phone: %v", err)
	}
	if tok := srv.tokens[phone.rid]; tok != "" {
		t.Fatalf("PB-PUSH-6: the owner revoked the handset and the relay still holds its push "+
			"token (%q) -- a provider-visible identifier for a device its owner disowned", tok)
	}
}
