package phonecore

// FAILING-FIRST (TDD RED, GG-5) test for ADR-017 T5 / playbook 6.3: the PHONE half of
// sealing RemoteProfileV1 into the reconcile record. It rides the SAME reconcile frame
// reconcile_test.go pins (kindReconcile, reconcileFrame, MailboxRouter.Reconciled) --
// PROFILE NEEDS NO NEW DECODE LOGIC, because it is just another field on the already-
// embedded schema.ReconcileRecord. This test is therefore the SEAM PIN (the field must
// exist for this to compile) plus the end-to-end proof that nothing between the sealed
// bytes and Reconciled() drops or truncates it.
//
// THE SEAM this test pins (undefined symbol -> compile-fail RED):
//
//	schema.ReconcileRecord gains: Profile schema.RemoteProfileV1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// testProfileForPhonecore mirrors internal/remotegw/profile_test.go's testProfile and
// internal/protocol/schema/profile_test.go's testRemoteProfile: the SAME value sealed on
// the machine side, so this test proves the phone reads back exactly what was sent.
func testProfileForPhonecore() protocol.RemoteProfileV1 {
	return protocol.RemoteProfileV1{
		Version:                  1,
		AcceptedActions:          []string{"launch", "kill"},
		AcceptedBodyVersions:     map[string]int{"launch": 2, "kill": 1},
		InteractionSchemaVersion: 1,
		TerminalViewVersion:      1,
		CapabilityRecordVersion:  1,
	}
}

// TestMailboxRouter_ReconciledSurfacesTheSealedProfile: a reconcile frame sealed with a
// populated profile round-trips through Accept -> Reconciled with every field intact.
func TestMailboxRouter_ReconciledSurfacesTheSealedProfile(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	rec := wantReconcileRecord()
	rec.Profile = testProfileForPhonecore()
	plain, err := json.Marshal(reconcileFrame{Kind: kindReconcile, ReconcileRecord: rec})
	if err != nil {
		t.Fatalf("marshal reconcile frame: %v", err)
	}

	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 3, plain)); err != nil {
		t.Fatalf("accept reconcile frame: %v", err)
	}

	got, ok := router.Reconciled()
	if !ok {
		t.Fatalf("Reconciled() reports no record; the router dropped the reconcile frame")
	}
	if !reflect.DeepEqual(got.Profile, rec.Profile) {
		t.Fatalf("reconciled profile = %+v; want %+v (every field must survive the wire)", got.Profile, rec.Profile)
	}
	// The three pre-existing authorities must survive alongside the new field -- adding
	// Profile must not disturb PB-STATE-4's existing contract.
	if got.InboundHighWater != rec.InboundHighWater || got.GrantEpoch != rec.GrantEpoch || got.GrantSeq != rec.GrantSeq {
		t.Fatalf("reconciled authorities = %+v; want %+v (Profile must be ADDITIVE)", got, rec)
	}
}
