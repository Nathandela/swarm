package relay

// ADR-007 B37/B38 -- THE FIRST-USE CLAUSE IS AN UNAUTHENTICATED, REMOTELY-REACHABLE,
// PERMANENT DoS, BECAUSE THE PREMISE IT RESTED ON IS FALSE.
//
// B27 accepted mayActOn's trust-on-first-use clause -- "you may act on a target that has
// authorized nobody" -- on the argument that a target's relay-auth PUBLIC key is disclosed
// only at the relay handshake and over the SAS-authenticated pairing channel, so the window
// was reachable only by the relay operator, to whom availability is already conceded.
//
// The premise is false in at least three places, each of which hands the pubkey to a party
// that is NOT the relay operator:
//
//	1. auth_init carries the full relay-auth pubkey over a connection to which no transport
//	   policy is applied (B34), so a PASSIVE ON-PATH OBSERVER of a ws:// dial reads it.
//	2. pairing msg2 carries MachineRelayAuthPub one full round-trip BEFORE the mandatory
//	   desktop confirm and before the SAS is derived at all, so a QR PHOTOGRAPHER reads it
//	   with zero confirms -- needing no on-path position and surviving wss:// (B38).
//	3. pairing msg3 carries DeviceRelayAuthPub before the SAS check, so a REJECTED SAS
//	   still discloses it.
//
// Holding the pubkey is the whole attack: authorize_device accepts any 32 bytes,
// device_revoke is behind the SAME permissive rule, revokeAndPurge records the ATTACKER as
// the banner, and only the banner may lift a ban. A machine that has never dialled the
// relay is permanently banned by a party that photographed a QR code.
//
// WHAT THESE TESTS ASSERT, AND WHAT THEY DELIBERATELY DO NOT. They assert the security
// property -- a party that knows only a target's relay-auth PUBLIC key cannot pair with,
// append to, push to, or revoke that target -- and, in the negative controls, that the
// legitimate bootstrap the first-use clause existed to serve still completes. They do not
// assert the shape of a fix beyond the one thing ADR-007 B27 recorded as the complete
// remedy and B38 made mandatory: an authorize_device naming a target must carry PROOF THE
// TARGET CONSENTED, verifiable by the relay under the target's own relay-auth key.
//
// The negative controls matter as much as the refusals: TestB38_ConsentedAuthorizeBootstraps
// is the exact shape that killed the B25 mutual-pairing direction (machine authorizes phone,
// then IMMEDIATELY appends the epoch grant, before the phone has ever connected), so a fence
// that passed by refusing everything would fail there.

import (
	"errors"
	"testing"
)

// TestB38_ObserverOfThePubkeyCannotPairWithANeverPairedMachine is disclosure route 1 and 2
// reduced to what they actually yield: the victim's relay-auth PUBLIC key, in the hands of
// a party with no relationship to it. That is exactly what a passive observer of an
// unprotected auth_init reads, and exactly what a QR photographer reads out of msg2.
//
// The machine here has NEVER dialled the relay and has authorized nobody -- the state B27's
// first-use clause called "bootstrapping" and the state a machine is actually in between
// `swarm remote init` and its first pairing.
func TestB38_ObserverOfThePubkeyCannotPairWithANeverPairedMachine(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	// The victim: a machine identity that has never connected to this relay.
	machinePub, _ := newRelayAuthKey(t)
	machineRID := RoutingID(machinePub)

	// The attacker: a keypair minted seconds ago. Its entire capital is the victim's
	// public key, which every one of the three disclosure routes hands it.
	attacker := newB25Party(t, srv)

	err := attacker.cl.AuthorizeDevice(testCtx(t), machinePub, nil)
	if err == nil {
		t.Fatalf("ADR-007 B38: authorize_device naming a never-paired machine was ACCEPTED with no "+
			"proof the machine consented.\n"+
			"  attacker rid %s named machine rid %s\n"+
			"  The attacker holds only the machine's PUBLIC key -- read off auth_init by a passive\n"+
			"  observer (B34/B37) or off pairing msg2 by a QR photographer with zero desktop\n"+
			"  confirms (B38). authorize_device must prove the TARGET consented.",
			attacker.rid, machineRID)
	}
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("want ErrNotAuthorized for an unconsented authorize_device, got %v", err)
	}
	if srv.st.isPaired(attacker.rid, machineRID) || srv.st.isPaired(machineRID, attacker.rid) {
		t.Fatalf("ADR-007 B38: a pairing formed out of the attacker's say-so alone (attacker %s, machine %s)",
			attacker.rid, machineRID)
	}
}

// TestB38_ObserverOfThePubkeyCannotBanANeverPairedMachine is the harm B37 measured, and the
// reason this defect is critical rather than merely wrong: device_revoke sits behind the
// SAME authority rule as append, revokeAndPurge records the ATTACKER as the banner, and
// authorizePair lets only the banner lift a ban. The victim's own owner cannot undo it.
//
// The assertion is on the machine's ability to REGISTER afterwards, not on the revoke's
// error alone: a refusal that still banned the target would satisfy a weaker test and leave
// the machine bricked.
func TestB38_ObserverOfThePubkeyCannotBanANeverPairedMachine(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	machinePub, machinePriv := newRelayAuthKey(t)
	machineRID := RoutingID(machinePub)
	attacker := newB25Party(t, srv)

	// Both halves of the chain, run for real: self-pair, then revoke.
	_ = attacker.cl.AuthorizeDevice(testCtx(t), machinePub, nil)
	revokeErr := attacker.cl.DeviceRevoke(testCtx(t), machineRID)
	if revokeErr == nil {
		t.Fatalf("ADR-007 B38: a stranger holding only the machine's public key REVOKED it (rid %s)", machineRID)
	}
	if !errors.Is(revokeErr, ErrNotAuthorized) {
		t.Fatalf("want ErrNotAuthorized for an unconsented device_revoke, got %v", revokeErr)
	}

	// The measurement that makes this a DoS finding: the machine's FIRST EVER dial.
	if srv.st.revokedBy(machineRID, attacker.rid) {
		t.Fatalf("ADR-007 B38: the machine is banned at the relay by a party it never met (rid %s)", machineRID)
	}
	if _, err := Dial(testCtx(t), srv.URL(), authFor(machinePub, machinePriv)); err != nil {
		t.Fatalf("ADR-007 B38: the machine's first ever dial was refused: %v\n"+
			"  A machine that has never dialled this relay is permanently locked out, and only the\n"+
			"  banner -- the attacker -- can lift the ban.", err)
	}
}

// TestB38_ObserverOfThePubkeyCannotAppendOrPushToANeverPairedMachine covers the other two
// verbs behind the same rule. They are separated from the revoke because they fail
// separately and because a fix that closed only the gravest verb would leave a mailbox
// flood and an unsolicited-wake channel open against any never-paired identity.
func TestB38_ObserverOfThePubkeyCannotAppendOrPushToANeverPairedMachine(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)

	machinePub, _ := newRelayAuthKey(t)
	machineRID := RoutingID(machinePub)
	attacker := newB25Party(t, srv)
	_ = attacker.cl.AuthorizeDevice(testCtx(t), machinePub, nil)

	sp := newSealParty(t, []byte("attacker-sender-pub-00000000000x"), []byte("machine-recipient-pub-000000000x"))
	if _, err := attacker.cl.MailboxAppend(testCtx(t), machineRID, sp.sealMailbox(t, 1, []byte("x"), clk)); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: a stranger appended to a never-paired machine's mailbox: %v", err)
	}
	if err := attacker.cl.PushTrigger(testCtx(t), machineRID, []byte("wake")); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: a stranger pushed to a never-paired machine: %v", err)
	}
}

// TestB38_AConsentSignatureIsNotTransferable is the property that makes carrying consent to
// the relay safe at all. The signature is a statement about ONE grantee -- it names the
// grantee's routing id -- so a copy of it (off the machine's disk, off the wire, out of a
// device registry) confers nothing on the party that copied it.
//
// It also pins the two forgeries that would otherwise be equivalent to no check at all:
// a signature by the CALLER over its own grant, and a well-formed signature by the wrong key.
func TestB38_AConsentSignatureIsNotTransferable(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	machine := newB25Party(t, srv)
	phonePub, phonePriv := newRelayAuthKey(t)
	phoneRID := RoutingID(phonePub)

	// The genuine article: the phone consents to the MACHINE.
	genuine := consentTo(phonePriv, machine.rid)
	if err := machine.cl.AuthorizeDevice(testCtx(t), phonePub, genuine); err != nil {
		t.Fatalf("precondition: the machine holding the phone's consent was refused: %v", err)
	}

	// A thief that copied the consent off the machine cannot use it: it names the machine.
	thief := newB25Party(t, srv)
	if err := thief.cl.AuthorizeDevice(testCtx(t), phonePub, genuine); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: a COPIED consent signature authorized a different caller "+
			"(thief %s replayed the phone's consent for machine %s): %v", thief.rid, machine.rid, err)
	}
	if srv.st.mayActOn(thief.rid, phoneRID) {
		t.Fatalf("ADR-007 B38: the thief may act on the phone's route by replaying a consent that names another grantee")
	}

	// A caller signing its OWN grant proves nothing about the target.
	self := consentTo(thief.priv, thief.rid)
	if err := thief.cl.AuthorizeDevice(testCtx(t), phonePub, self); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: a self-signed consent was accepted as the target's: %v", err)
	}

	// A well-formed signature under an unrelated key.
	_, wrongPriv := newRelayAuthKey(t)
	if err := thief.cl.AuthorizeDevice(testCtx(t), phonePub, consentTo(wrongPriv, thief.rid)); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: a consent signed by the wrong key was accepted: %v", err)
	}
}

// TestB38_ConsentedAuthorizeBootstraps IS THE NEGATIVE CONTROL, and it is the exact shape
// that falsified ADR-007 B25's mutual-pairing direction: the machine authorizes the phone
// and IMMEDIATELY appends the sealed epoch grant -- the append that DELIVERS the ContentKey,
// whose failure is fatal (cmd/swarm-remote/main.go) -- with the phone never having connected
// to the relay at all.
//
// It also asserts the reverse direction, because the phone must be able to append its
// commands to the machine the moment it does connect, and nothing else writes that edge.
func TestB38_ConsentedAuthorizeBootstraps(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)

	machine := newB25Party(t, srv)
	phonePub, phonePriv := newRelayAuthKey(t)
	phoneRID := RoutingID(phonePub)

	// The phone is OFFLINE: it has never dialled this relay, exactly as at a first pairing.
	// All the machine holds is the consent the phone signed during the SAS ceremony.
	if err := machine.cl.AuthorizeDevice(testCtx(t), phonePub, consentTo(phonePriv, machine.rid)); err != nil {
		t.Fatalf("bootstrap: the machine's consented authorize_device was refused: %v", err)
	}

	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := machine.cl.MailboxAppend(testCtx(t), phoneRID, sp.sealMailbox(t, 1, []byte("epoch-grant"), clk)); err != nil {
		t.Fatalf("bootstrap: the machine could not append the epoch grant to the phone it just paired: %v\n"+
			"  This is TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap's path, and its failure is\n"+
			"  fatal to the gateway. A fix that breaks it is the B25 direction over again.", err)
	}

	// The phone connects for the first time and must be able to reach the machine.
	phone := dialAuthed(t, srv.URL(), authFor(phonePub, phonePriv))
	if _, err := phone.MailboxAppend(testCtx(t), machine.rid, sp.sealMailbox(t, 2, []byte("p->m"), clk)); err != nil {
		t.Fatalf("bootstrap: the paired phone could not append to its machine: %v", err)
	}
}

// TestB38_ConsentIsRequiredEvenOnAnEstablishedIdentity closes the door B27's rule left ajar
// in the other direction. The first-use clause is not merely narrowed here, it is GONE: an
// authorize_device with no consent is refused whether the target is bootstrapping or long
// established, so there is no state of the target in which a stranger's say-so counts.
func TestB38_ConsentIsRequiredEvenOnAnEstablishedIdentity(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	machine := newB25Party(t, srv)
	phonePub, phonePriv := newRelayAuthKey(t)
	if err := machine.cl.AuthorizeDevice(testCtx(t), phonePub, consentTo(phonePriv, machine.rid)); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	stranger := newB25Party(t, srv)
	if err := stranger.cl.AuthorizeDevice(testCtx(t), machine.pub, nil); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: an unconsented authorize_device against an ESTABLISHED identity was accepted: %v", err)
	}
	if err := stranger.cl.DeviceRevoke(testCtx(t), machine.rid); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ADR-007 B38: a stranger revoked an established machine: %v", err)
	}
}
