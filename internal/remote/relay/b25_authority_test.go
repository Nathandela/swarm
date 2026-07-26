package relay

// ADR-007 B25 — RELAY PAIRING IS CREATED UNILATERALLY, SO AUTHENTICATION IS BEING
// READ AS AUTHORITY.
//
// handleAuthorizeDevice (server.go) calls authorizePair(sc.rid, deviceRID): it records a
// pairing between the CALLER's routing id and ANY routing id the caller names, with the
// named party never consulted. Its only gate is requireAuth, and relay registration is
// OPEN (handleAuthInit accepts any self-minted keypair), so that gate proves an identity
// and nothing whatever about authority over the named routing id.
//
// Every relay verb that means "act on somebody else's route" then gates on exactly that
// unilateral pairing: handleMailboxAppend, handlePushTrigger and handleDeviceRevoke all
// test isPaired(sc.rid, req.Target). So a party that mints a throwaway keypair, connects,
// and names the MACHINE's routing id acquires the machine's route for free.
//
// These are the three faces of one missing check. They are separated because they fail
// separately: the pairing itself (TestB25_AuthorizeDeviceNeedsNoConsent), the availability
// harm ADR-007 B24 named (TestB25_SelfPairedStrangerCanFloodTheMachineMailbox), and the
// permanent lockout B24 escalated it into (TestB25_StrangerCanPermanentlyBanTheMachine).
//
// WHAT THEY ASSERT, AND WHAT THEY DELIBERATELY DO NOT. They assert the security property
// — an unconsented party cannot pair with, ban, or append to a routing id — and never the
// shape of a fix. In particular NONE of them requires authorize_device to be REFUSED: the
// recorded fix direction (B25: pairing must be mutual) lets a one-sided authorize succeed
// as a recorded intent and simply never forms a pair from it. A test that demanded an
// error from authorize_device would forbid that fix.

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

// b25Party is one relay identity: its keypair, its derived routing id, and (once dialed)
// its authenticated client.
type b25Party struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	rid  string
	cl   *Client
}

func newB25Party(t *testing.T, srv *Server) b25Party {
	t.Helper()
	pub, priv := newRelayAuthKey(t)
	return b25Party{
		pub:  pub,
		priv: priv,
		rid:  RoutingID(pub),
		cl:   dialAuthed(t, srv.URL(), authFor(pub, priv)),
	}
}

// b25LegitPair wires the machine and the phone the way PRODUCTION wires them, so that
// nothing these tests assert depends on a fixture shape production never takes:
// cmd/swarm/remote.go authorizeAtRelay at pairing and cmd/swarm-remote/deliver.go
// deliverEpochGrant on every gateway connect, both carrying the CONSENT the phone signed
// during the SAS ceremony (ADR-007 B27/B38, pairing msg3 -> device.Record.ConsentSig).
//
// ONE CALL IS THE WHOLE PAIRING NOW, and that is a change from the two-legged wiring this
// fixture used to mirror. A consented authorize_device records both directed edges at once —
// the phone's proven grant over its own route, and the machine's grant over its own — so the
// phone-side leg mobile/relay.go used to issue on every reconnect is gone from production:
// it could only ever have written an edge it cannot prove, which is the hole itself.
//
// It then round-trips one append each way, so the tests below start from a pairing that is
// demonstrably live rather than merely recorded.
func b25LegitPair(t *testing.T, srv *Server, clk *fakeClock) (machine, phone b25Party) {
	t.Helper()
	machine = newB25Party(t, srv)
	phone = newB25Party(t, srv)

	if err := machine.cl.AuthorizeDevice(testCtx(t), machine.pubOf(phone),
		consentTo(phone.priv, machine.rid)); err != nil {
		t.Fatalf("machine authorizes phone (cmd/swarm/remote.go authorizeAtRelay): %v", err)
	}

	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := machine.cl.MailboxAppend(testCtx(t), phone.rid, sp.sealMailbox(t, 1, []byte("m->p"), clk)); err != nil {
		t.Fatalf("precondition: the legitimately paired machine cannot append to the phone: %v", err)
	}
	if _, err := phone.cl.MailboxAppend(testCtx(t), machine.rid, sp.sealMailbox(t, 2, []byte("p->m"), clk)); err != nil {
		t.Fatalf("precondition: the legitimately paired phone cannot append to the machine: %v", err)
	}
	return machine, phone
}

// pubOf is sugar for naming another party's relay-auth key on the wire.
func (b b25Party) pubOf(other b25Party) ed25519.PublicKey { return ed25519.PublicKey(other.pub) }

// TestB25_AuthorizeDeviceNeedsNoConsent is the ROOT CAUSE on its own, separable from what
// the resulting pairing is then used for.
//
// A stranger — a keypair minted seconds ago that has never appeared in any pairing, on
// either side — names the machine's routing id in authorize_device. Afterwards the relay
// considers the two paired, in both directions, purely because the stranger said so. The
// machine authorized nobody; it was not asked and cannot object.
//
// The assertion is on isPaired because isPaired IS the authority decision — it is the
// single predicate handleMailboxAppend, handlePushTrigger and handleDeviceRevoke each
// consult before letting a caller act on someone else's route. It is not a storage detail:
// if it answers true here, every one of those verbs is open to the stranger.
//
// It does NOT assert that authorize_device returns an error. A one-sided authorize may
// legitimately be accepted and recorded as an intent (B25's mutual-pairing direction does
// exactly that); what may not happen is a PAIRING forming out of one side's say-so.
func TestB25_AuthorizeDeviceNeedsNoConsent(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, _ := b25LegitPair(t, srv, clk)

	// The whole cost of the attack: mint a keypair and authenticate as it. The relay has
	// no registration step at which to refuse.
	stranger := newB25Party(t, srv)

	// Naming the machine. Nothing here proves any relationship to it — the routing id is
	// derived from the machine's relay-auth PUBLIC key, which the stranger is free to know.
	_ = stranger.cl.AuthorizeDevice(testCtx(t), stranger.pubOf(machine), nil)

	if srv.st.isPaired(stranger.rid, machine.rid) || srv.st.isPaired(machine.rid, stranger.rid) {
		t.Fatalf("ADR-007 B25: a stranger PAIRED ITSELF with the machine by naming it.\n"+
			"  stranger rid %s -> machine rid %s: isPaired=%v\n"+
			"  machine  rid %s -> stranger rid %s: isPaired=%v\n"+
			"The machine never authorized this identity and was never asked. "+
			"handleAuthorizeDevice calls authorizePair(sc.rid, deviceRID) behind requireAuth ALONE, "+
			"and relay registration is open — so the only thing that gate establishes is that the "+
			"caller holds the private key for its OWN routing id, which is true of every keypair "+
			"ever generated. THERE IS NO AUTHORITY CHECK OVER THE NAMED ROUTING ID: nothing tests "+
			"that the named party consented, and a pairing forms out of one side's say-so.\n"+
			"isPaired is not bookkeeping — it is the predicate handleMailboxAppend, handlePushTrigger "+
			"and handleDeviceRevoke each consult to decide whether a caller may act on someone else's "+
			"route. True here means all three are open to this stranger.",
			stranger.rid, machine.rid, srv.st.isPaired(stranger.rid, machine.rid),
			machine.rid, stranger.rid, srv.st.isPaired(machine.rid, stranger.rid))
	}
}

// TestB25_SelfPairedStrangerCanFloodTheMachineMailbox measures the harm ADR-007 B24 named
// but never bounded — "appends against the machine's mailbox up to the per-target depth
// cap, i.e. a denial of service against the legitimate phone's appends".
//
// It asserts the REACH rather than restating the sentence: the stranger fills the machine's
// mailbox to MailboxMaxItems, and the legitimate phone's next append — a real keystroke
// batch on the owner's own paired handset — is refused with ErrQuotaExceeded. The depth cap
// is on LIVE depth, so this holds the phone out for as long as the stranger keeps the
// mailbox full, and the machine cannot drain items it cannot decrypt fast enough to matter.
//
// The property asserted is the stranger's FIRST append being refused. Everything after it
// runs only to show what one unrefused append leads to.
func TestB25_SelfPairedStrangerCanFloodTheMachineMailbox(t *testing.T) {
	const depthCap = 4
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.MailboxMaxItems = depthCap
	})
	machine, phone := b25LegitPair(t, srv, clk)

	// The machine's mailbox already holds the phone's one legitimate append from the
	// fixture; drain it so the depth budget below starts from a known floor.
	items, err := machine.cl.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("machine drains its own mailbox: %v", err)
	}
	if len(items) > 0 {
		if err := machine.cl.MailboxAck(testCtx(t), items[len(items)-1].Cursor); err != nil {
			t.Fatalf("machine acks its own mailbox: %v", err)
		}
	}

	stranger := newB25Party(t, srv)
	_ = stranger.cl.AuthorizeDevice(testCtx(t), stranger.pubOf(machine), nil)

	sp := newSealParty(t, []byte("stranger-sender-pub-00000000000x"), []byte("machine-recipient-pub-000000000x"))

	// THE PROPERTY. An identity the machine never authorized may not put a byte in its
	// mailbox. handleMailboxAppend gates on isPaired(sc.rid, req.Target) and nothing else.
	first, err := stranger.cl.MailboxAppend(testCtx(t), machine.rid, sp.sealMailbox(t, 1, []byte("flood-1"), clk))
	if errors.Is(err, ErrNotAuthorized) {
		return
	}
	if err != nil {
		t.Fatalf("stranger's append to the machine was refused, but not as unauthorized: %v "+
			"(want ErrNotAuthorized — this test needs the AUTHORITY gate to be what stops it, "+
			"not a rate limit or a malformed request)", err)
	}

	// It landed. Establish how far it reaches: fill the machine's mailbox to its cap.
	stored := 1
	for stored < depthCap {
		if _, err := stranger.cl.MailboxAppend(testCtx(t), machine.rid,
			sp.sealMailbox(t, uint64(stored+1), []byte("flood-n"), clk)); err != nil {
			t.Fatalf("stranger filled %d/%d of the machine's mailbox then hit %v; the flood is "+
				"bounded differently than assumed and this test's reach measurement is wrong",
				stored, depthCap, err)
		}
		stored++
	}

	// The owner's own paired handset, locked out of its own machine.
	_, phoneErr := phone.cl.MailboxAppend(testCtx(t), machine.rid, sp.sealMailbox(t, 99, []byte("owner keystroke"), clk))

	// The refusal is the DEPTH cap, not the rate window: this whole test spends 6 appends
	// against the machine's per-target MailboxAppendPerMin budget, which is untouched. Drain
	// and ack, and the phone gets in again — which is exactly why the flood is sustainable
	// rather than a one-shot: the stranger only has to keep the mailbox full.
	drained, _ := machine.cl.MailboxRead(testCtx(t), 0)
	if len(drained) > 0 {
		_ = machine.cl.MailboxAck(testCtx(t), drained[len(drained)-1].Cursor)
	}
	_, phoneAfterDrain := phone.cl.MailboxAppend(testCtx(t), machine.rid, sp.sealMailbox(t, 100, []byte("owner keystroke"), clk))

	t.Fatalf("ADR-007 B25: a stranger APPENDED TO THE MACHINE'S MAILBOX and shut the owner's "+
		"phone out of it.\n"+
		"  stranger rid %s (minted in this test, never authorized by anyone) stored cursor %d and "+
		"then %d/%d of the machine's depth cap\n"+
		"  the legitimately paired phone's next append returned: %v (want it to still work)\n"+
		"  after the machine drained and acked the stranger's items, the same phone append "+
		"returned: %v — so the refusal above is the DEPTH cap the stranger filled, not a rate "+
		"window (this test spends 6 appends of MailboxAppendPerMin=600)\n"+
		"handleMailboxAppend's ONLY authority gate is isPaired(sc.rid, req.Target), and the stranger "+
		"satisfied it by calling authorize_device on itself against the machine's routing id. "+
		"THERE IS NO CHECK THAT THE TARGET EVER AUTHORIZED THE CALLER. This is the reachable harm "+
		"ADR-007 B24 recorded: end-to-end crypto is untouched — none of these frames will ever open "+
		"— but the mailbox is a shared, capped resource and filling it is a denial of service against "+
		"the legitimate device. The cap is on LIVE depth, so it lasts as long as the stranger keeps "+
		"re-filling it.",
		stranger.rid, first, stored, depthCap, phoneErr, phoneAfterDrain)
}

// TestB25_StrangerCanPermanentlyBanTheMachine is the whole B25 defect end to end, and the
// reason it is graver than the append hole it shares a gate with.
//
// A throwaway identity that has never legitimately paired with anything self-pairs with the
// machine's routing id and calls device_revoke against it. revokeAndPurge bans the machine
// and destroys its relay state. The machine's relay-auth key is durable, so it comes back on
// the SAME routing id and is refused at the handshake, forever.
//
// FOREVER IS THE PART THAT MATTERS, and it is ADR-007 B24's doing. Before B24 any
// authorize_device cleared any ban, and mobile/relay.go onConnected authorizes the machine on
// every authenticated reconnect — so an attacker-placed ban on the machine self-healed the
// next time the owner opened the app. B24 correctly narrowed the clear to the identity that
// PLACED the ban; the placer here is the attacker; so the ordinary reconnect no longer lifts
// it and no party the owner controls can. The test drives that reconnect explicitly, because
// a fix that only restored the old self-healing would be papering over this.
//
// This is not covered by "the relay is untrusted": the threat model concedes availability to
// the RELAY OPERATOR, not to any anonymous party who can reach the relay's port.
func TestB25_StrangerCanPermanentlyBanTheMachine(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone := b25LegitPair(t, srv, clk)

	stranger := newB25Party(t, srv)
	_ = stranger.cl.AuthorizeDevice(testCtx(t), stranger.pubOf(machine), nil)

	// THE PROPERTY. An identity the machine never authorized may not revoke it.
	// handleDeviceRevoke gates on isPaired(sc.rid, req.Target) and nothing else.
	revokeErr := stranger.cl.DeviceRevoke(testCtx(t), machine.rid)
	if errors.Is(revokeErr, ErrNotAuthorized) {
		return
	}
	if revokeErr != nil {
		t.Fatalf("stranger's revoke of the machine was refused, but not as unauthorized: %v "+
			"(want ErrNotAuthorized — this test needs the AUTHORITY gate to be what stops it)", revokeErr)
	}

	// It landed. Establish that the machine is out, and that nothing the owner controls
	// puts it back.
	_, redialErr := Dial(testCtx(t), srv.URL(), authFor(machine.pub, machine.priv))
	if redialErr == nil {
		t.Fatalf("ADR-007 B25: a stranger's device_revoke against the machine SUCCEEDED (no error), " +
			"yet the machine still reaches the relay. The revoke path is not gated on any authority " +
			"over the target (handleDeviceRevoke checks only isPaired(sc.rid, req.Target), which the " +
			"stranger created for itself), but its effect is not what this test assumed — report this, " +
			"the recorded consequence in ADR-007 B25 may be wrong even though the missing check is real")
	}
	if !errors.Is(redialErr, ErrRevoked) {
		t.Fatalf("stranger's revoke landed and the machine now fails to dial with %v, want ErrRevoked", redialErr)
	}

	// THE B24 INTERACTION. The owner's own machine re-authorizes the phone — the recovery
	// path cmd/swarm/remote.go authorizeAtRelay takes on a re-pair, and the only automatic
	// clear that exists. It is driven explicitly because what this test measures is the ban
	// SURVIVING the owner's every remedy.
	if err := machine.cl.AuthorizeDevice(testCtx(t), machine.pubOf(phone),
		consentTo(phone.priv, machine.rid)); err != nil {
		t.Fatalf("the owner's re-authorize (cmd/swarm/remote.go authorizeAtRelay) failed outright: %v; "+
			"this test needs that path REACHED, because what it measures is the ban surviving it", err)
	}

	_, afterHeal := Dial(testCtx(t), srv.URL(), authFor(machine.pub, machine.priv))

	t.Fatalf("ADR-007 B25: a throwaway identity PERMANENTLY BANNED THE MACHINE, and no party the "+
		"owner controls can lift it.\n"+
		"  stranger rid %s — a keypair minted in this test, never authorized by the machine or the phone\n"+
		"  it self-paired via authorize_device, then device_revoke(%s) returned: %v\n"+
		"  the machine re-dialing its own relay: %v\n"+
		"  after the phone's ordinary reconnect authorize (mobile/relay.go onConnected), the machine "+
		"re-dialing again: %v\n"+
		"THE MISSING CHECK IS AUTHORITY OVER THE TARGET. handleDeviceRevoke gates on "+
		"isPaired(sc.rid, req.Target); handleAuthorizeDevice creates exactly that pairing from the "+
		"caller's word alone, behind requireAuth, over open registration. Authentication proves "+
		"identity, not authority — the same join that falsified ADR-007 B22.\n"+
		"AND IT IS PERMANENT BECAUSE OF B24. bucketRevoked now records the BANNING rid and "+
		"authorizePair clears a ban only for that banner; the banner is the stranger; so the phone's "+
		"reconnect — the path that used to heal this automatically — no longer does. B24's narrowing "+
		"is right on its own terms; what it did was convert this second defect from a transient denial "+
		"of service into a durable destruction of the machine's relay identity, reachable by anyone "+
		"who can open a socket to the relay. Confidentiality and integrity are untouched. Availability "+
		"is gone, and it does not come back.",
		stranger.rid, machine.rid, revokeErr, redialErr, afterHeal)
}
