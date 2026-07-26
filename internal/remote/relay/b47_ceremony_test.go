package relay

// ADR-007 B47: a revoke does not revoke the consent.
//
// routing.go argued: "There is no nonce and none is needed: the statement is a standing
// grant... and it is REVOKED BY device_revoke rather than by expiry." Both halves true,
// the join unchecked. revokeAndPurge deletes the two pairs edges and writes a ban -- and
// the SIGNATURE is a durable artifact the grantee still holds. Re-presenting it rewrites
// both edges AND, because authorizePair clears a ban placed by that same pairer, lifts the
// ban in the same transaction. `swarm remote revoke <phone>` -- the owner's entire remedy
// for a lost handset -- is undone by bytes already sitting in the machine's state
// directory, i.e. against B41's own adversary.
//
// THE FIX MUST NOT BE "REFUSE A CONSENT FOR A REVOKED PAIRING". Restoring access and
// lifting the ban are the same act (B22, authorizePair), and PB-STATE-10 requires that
// act: a recovered handset comes back on the same routing id and is banned until it runs.
// A refusal keyed on "this pairing was revoked" tests green and re-bricks the recovery.
//
// So the consent is bound to the CEREMONY that produced it. A replay presents a retired
// ceremony id and is refused; a re-pair is a fresh ceremony, so a fresh id, and is
// accepted exactly as before -- ban lift included. Nothing is keyed on revocation at all.

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

// TestB47_AReplayedConsentDoesNotUnRevoke is the reviewer's probe as an in-tree fence.
func TestB47_AReplayedConsentDoesNotUnRevoke(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machinePub, machinePriv := newRelayAuthKey(t)
	phonePub, phonePriv := newRelayAuthKey(t)
	phoneRID := RoutingID(phonePub)

	machine := dialAuthed(t, srv.URL(), authFor(machinePub, machinePriv))

	// The one consent the phone ever signed, during the pairing ceremony.
	consent := consentToCeremony(phonePriv, "ceremony-the-owner-ran", machine.RoutingID())
	if err := machine.AuthorizeDevice(ctx, phonePub, consent); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	first := dialAuthed(t, srv.URL(), authFor(phonePub, phonePriv))
	_ = first.Close()

	// The owner loses the phone and revokes it.
	if err := machine.DeviceRevoke(ctx, phoneRID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := Dial(ctx, srv.URL(), authFor(phonePub, phonePriv)); err == nil {
		t.Fatal("precondition: revoke did not take effect, so this test proves nothing")
	}

	// Replay the SAME consent bytes. Nothing new was signed; the phone was never asked.
	if err := machine.AuthorizeDevice(ctx, phonePub, consent); !errors.Is(err, ErrConsentRetired) {
		t.Fatalf("replayed consent = %v, want ErrConsentRetired.\n"+
			"  Anything that can read the machine's state directory can otherwise undo the owner's "+
			"only remedy for a lost handset, without the phone ever being asked.", err)
	}
	if c, err := Dial(ctx, srv.URL(), authFor(phonePub, phonePriv)); err == nil {
		_ = c.Close()
		t.Fatal("the revoked phone dials again after a consent replay")
	}
}

// TestB47_AFreshCeremonyStillRecoversARevokedDevice is the other half, and it is the half
// that makes the fix admissible at all. If it fails, PB-STATE-10 is re-bricked: revoke and
// re-pair become mutually exclusive again, which is the exact wall ADR-007 B22 removed.
//
// IT IS ALSO THE NON-VACUITY CONTROL for the test above. A relay that refused every
// consent for a previously-revoked pair would pass that one and fail this one.
func TestB47_AFreshCeremonyStillRecoversARevokedDevice(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machinePub, machinePriv := newRelayAuthKey(t)
	phonePub, phonePriv := newRelayAuthKey(t)
	phoneRID := RoutingID(phonePub)

	machine := dialAuthed(t, srv.URL(), authFor(machinePub, machinePriv))

	if err := machine.AuthorizeDevice(ctx, phonePub,
		consentToCeremony(phonePriv, "the-first-pairing", machine.RoutingID())); err != nil {
		t.Fatalf("first authorize: %v", err)
	}
	if err := machine.DeviceRevoke(ctx, phoneRID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !srv.st.isRevoked(phoneRID) {
		t.Fatal("precondition: the phone is not banned, so the recovery below is not a recovery")
	}

	// The owner recovers the handset and pairs again: a NEW ceremony, so a new id, and the
	// phone signs a new consent in front of them. This is the whole of PB-STATE-10's
	// documented remedy, and it must still lift the ban the revoke placed (ADR-007 B22).
	if err := machine.AuthorizeDevice(ctx, phonePub,
		consentToCeremony(phonePriv, "the-re-pairing-after-recovery", machine.RoutingID())); err != nil {
		t.Fatalf("re-pair after revoke: %v.\n"+
			"  This is PB-STATE-10's only remedy for a recovered handset. Refusing it makes revoke "+
			"and re-pair mutually exclusive again, which is worse than the replay it was meant to stop.", err)
	}
	if srv.st.isRevoked(phoneRID) {
		t.Fatal("the re-pairing did not lift the ban its own revoke placed (ADR-007 B22)")
	}
	c, err := Dial(ctx, srv.URL(), authFor(phonePub, phonePriv))
	if err != nil {
		t.Fatalf("the recovered phone still cannot dial: %v", err)
	}
	_ = c.Close()
}

// TestB47_ARetiredCeremonyStaysRetiredAcrossLaterPairings closes the two-stale-consents
// case. Retiring only at REVOKE would leave every consent from an earlier ceremony live,
// so an adversary holding two of them spends one and keeps the other. A pair has exactly
// one live consent: recording a new one retires the old, whether or not a revoke ever
// happened.
func TestB47_ARetiredCeremonyStaysRetiredAcrossLaterPairings(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machinePub, machinePriv := newRelayAuthKey(t)
	phonePub, phonePriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(machinePub, machinePriv))

	oldConsent := consentToCeremony(phonePriv, "ceremony-A", machine.RoutingID())
	newConsent := consentToCeremony(phonePriv, "ceremony-B", machine.RoutingID())

	if err := machine.AuthorizeDevice(ctx, phonePub, oldConsent); err != nil {
		t.Fatalf("authorize A: %v", err)
	}
	// A re-pairing with no revoke in between: B supersedes A.
	if err := machine.AuthorizeDevice(ctx, phonePub, newConsent); err != nil {
		t.Fatalf("authorize B: %v", err)
	}
	if err := machine.AuthorizeDevice(ctx, phonePub, oldConsent); !errors.Is(err, ErrConsentRetired) {
		t.Fatalf("replaying the SUPERSEDED consent = %v, want ErrConsentRetired: a pair has one "+
			"live consent, and an adversary holding two must not be able to spend the spare", err)
	}
	// And the live one is still live.
	if err := machine.AuthorizeDevice(ctx, phonePub, newConsent); err != nil {
		t.Fatalf("re-presenting the LIVE consent = %v, want acceptance", err)
	}
}

// TestB47_TheGatewayMayRePresentItsLiveConsentForever is the bootstrap this ADR has now
// briefed three fixes into. cmd/swarm-remote's deliverEpochGrant authorizes the phone and
// IMMEDIATELY appends the sealed epoch grant on EVERY gateway connect, with the same
// stored bytes, and its failure is FATAL to the gateway. A single-use consent would brick
// the machine on its second boot.
func TestB47_TheGatewayMayRePresentItsLiveConsentForever(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machinePub, machinePriv := newRelayAuthKey(t)
	phonePub, phonePriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(machinePub, machinePriv))

	stored := consentToCeremony(phonePriv, "the-one-pairing-that-happened", machine.RoutingID())
	for i := 0; i < 5; i++ {
		if err := machine.AuthorizeDevice(ctx, phonePub, stored); err != nil {
			t.Fatalf("gateway connect %d re-presenting its stored consent: %v.\n"+
				"  deliverEpochGrant runs this on every connect and its failure is fatal to the "+
				"gateway; a consent that is single-USE rather than single-CEREMONY bricks the machine.", i, err)
		}
	}
}

// TestB47_TheCeremonyIdIsSIGNED, not merely carried. The credential travels as one blob so
// a caller cannot present a ceremony id its signature does not cover -- but the fence is
// that the statement itself binds it, so swapping the id in the envelope cannot make a
// retired signature live again.
func TestB47_TheCeremonyIdIsSigned(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	ctx := testCtx(t)

	machinePub, machinePriv := newRelayAuthKey(t)
	phonePub, phonePriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(machinePub, machinePriv))

	// The signature covers ceremony A; the envelope claims ceremony B.
	sig := ed25519.Sign(phonePriv, ConsentMessage("ceremony-A", machine.RoutingID()))
	relabelled := MarshalConsent("ceremony-B", sig)

	if err := machine.AuthorizeDevice(ctx, phonePub, relabelled); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("a consent re-labelled with a ceremony its signature does not cover = %v, want "+
			"ErrNotAuthorized: otherwise retiring an id retires nothing, because the holder simply "+
			"relabels it", err)
	}
}
