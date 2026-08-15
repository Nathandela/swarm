package phonecore

// ADR-016 W4.1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "A relay-supplied or
// unauthenticated hint is ignored." The migration ladder (mobile App.applyRelayTLSPolicy)
// must consume RemoteProfileV1 from nowhere but a reconcile record Core.Reconcile has
// already authenticated -- never a value a caller could construct or read from elsewhere.
//
// Core.LastProfile is the seam this pins: it takes NO parameters, so there is no signature
// through which an unauthenticated profile can reach it, and it answers the zero value until
// a Reconcile call has actually succeeded.

import (
	"testing"
)

// wireReconcileFrameWithProfile is wireReconcileFrame (reconcile_test.go) with a POPULATED
// profile block instead of the zero one, so LastProfile has something distinguishable from
// "nothing reconciled yet" to hand back.
const wireReconcileFrameWithProfile = `{"kind":"reconcile","machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":{"version":1,"accepted_actions":null,"accepted_body_versions":null,"interaction_schema_version":0,"terminal_view_version":0,"capability_record_version":0,"relay_tls_policy":"webpki","relay_host":"swarm-relay.example.com","relay_spki_pin":null}}`

// TestADR016W4_LastProfileIsEmptyUntilAReconcileSucceeds is the "never an unauthenticated
// hint" half stated as a starting condition: with nothing yet reconciled, LastProfile must
// not manufacture a policy from nothing.
func TestADR016W4_LastProfileIsEmptyUntilAReconcileSucceeds(t *testing.T) {
	f := newRollbackFixture(t)
	c := f.resume(t)

	got := c.LastProfile()
	if got.RelayTLSPolicy != "" {
		t.Fatalf("LastProfile().RelayTLSPolicy = %q before any Reconcile call; want empty", got.RelayTLSPolicy)
	}
}

// TestADR016W4_LastProfileReturnsExactlyWhatTheSucceedingReconcileAdopted is the positive
// half: once Core.Reconcile has matched this phone's machine/epoch and succeeded, LastProfile
// hands back that SAME sealed record's profile -- the only source applyRelayTLSPolicy may
// ever read from (mobile App.adoptReconcile wires the two together in production).
func TestADR016W4_LastProfileReturnsExactlyWhatTheSucceedingReconcileAdopted(t *testing.T) {
	f := newRollbackFixture(t)
	c := f.resume(t)

	raw := sealFrameFrom(t, f.key, machineSender, 7, 3, []byte(wireReconcileFrameWithProfile))
	if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
		t.Fatalf("deliver the reconcile record: %v", err)
	}
	if err := c.Reconcile(); err != nil {
		t.Fatalf("Reconcile(): %v", err)
	}

	got := c.LastProfile()
	if got.RelayTLSPolicy != "webpki" {
		t.Errorf("LastProfile().RelayTLSPolicy = %q, want %q", got.RelayTLSPolicy, "webpki")
	}
	if got.RelayHost != "swarm-relay.example.com" {
		t.Errorf("LastProfile().RelayHost = %q, want %q", got.RelayHost, "swarm-relay.example.com")
	}
}

// TestADR016W4_LastProfileIgnoresARecordReconcileRefused is the fence the reviewer named
// explicitly: a record that ARRIVED but named the WRONG machine/epoch -- refused by
// Reconcile's own match check -- must not be readable through LastProfile. Reading
// router.reconcileRecord() again (rather than what Reconcile itself stored) would have
// leaked exactly this.
func TestADR016W4_LastProfileIgnoresARecordReconcileRefused(t *testing.T) {
	f := newRollbackFixture(t)
	c := f.resume(t)

	wrongEpoch := `{"kind":"reconcile","machine":"m1","epoch_id":9,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":{"version":1,"accepted_actions":null,"accepted_body_versions":null,"interaction_schema_version":0,"terminal_view_version":0,"capability_record_version":0,"relay_tls_policy":"webpki","relay_host":"attacker.example.com","relay_spki_pin":null}}`
	raw := sealFrameFrom(t, f.key, machineSender, 7, 3, []byte(wrongEpoch))
	if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
		t.Fatalf("deliver the reconcile record: %v", err)
	}
	if err := c.Reconcile(); err == nil {
		t.Fatalf("Reconcile() with a wrong-epoch record succeeded; want the machine/epoch refusal")
	}

	got := c.LastProfile()
	if got.RelayHost != "" {
		t.Fatalf("LastProfile() leaked a profile from a record Reconcile refused: %+v", got)
	}
}
