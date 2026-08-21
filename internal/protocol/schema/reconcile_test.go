package schema

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SYNC-7 (docs/specifications/
// remote-phaseB-requirements.md 6.6): the machine->phone RECONCILE RECORD, the wire
// carrier of the three rollback authorities PB-STATE-4 (6.1) names.
//
// WHY IT MUST EXIST: the phone's entire inbound plaintext set is journal record,
// terminal snapshot, command_reply and epoch grant (internal/phonecore/snapshot.go),
// and none of them carries the gateway's inbound high-water, its outbound ceilings, or
// the daemon's grant-issuance coordinate. Read literally, PB-STATE-4's authorities are
// unreachable, so its "fail closed for mutating ops" is PERMANENT and the exit
// criterion's "launches"/"types" cannot pass.
//
// THE THREE AUTHORITIES, one field group each (PB-STATE-4 requires a DISTINCT authority
// per coordinate -- one high-water for all three was explicitly rejected):
//
//	(a) phone send-seq             -> InboundHighWater (the gateway's durable inbound
//	                                  accepted high-water, PB-GW-1)
//	(b) phone receive high-waters  -> JournalCeiling + ReplyCeiling, PER BUCKET: the
//	                                  shared journal/terminal bucket and the deliberately
//	                                  separate command-reply bucket (SenderKeyID=0,
//	                                  internal/remotegw/command_in.go)
//	(c) grant watermark            -> GrantEpoch + GrantSeq (the daemon's epoch/grant
//	                                  issuance coordinate; crypto.NewGrantReceiverAt
//	                                  consumes exactly this pair)
//
// 9 rule 4 ("schemas, not adjectives") makes the wire shape itself the criterion, so
// these tests pin the exact JSON -- names, order and PRESENCE -- not a description of it.
//
// RED is undefined-only: schema.ReconcileRecord does not exist yet.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// wireZeroProfile is the committed JSON of the zero-value RemoteProfileV1 (no field
// carries omitempty, so a Profile that was never set still serializes in full -- see
// profile_test.go). testReconcileRecord below leaves Profile unset, so every
// pre-ADR-017 wire-shape const in this file gained this exact suffix.
const wireZeroProfile = `{"version":0,"accepted_actions":null,"accepted_body_versions":null,"interaction_schema_version":0,"terminal_view_version":0,"capability_record_version":0,"relay_tls_policy":"","relay_host":"","relay_spki_pin":null,"terminal_view_max_line_bytes":0,"terminal_view_max_rows":0,"terminal_view_max_rate_hz":0}`

// wireReconcileRecord is the committed JSON of the record below. The framed form (the
// same bytes with a leading kind discriminator) is pinned identically on both sides of
// the wire: internal/remotegw/reconcile_test.go (producer) and
// internal/phonecore/reconcile_test.go (consumer).
const wireReconcileRecord = `{"machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":` + wireZeroProfile + `}`

// testReconcileRecord is the record those bytes encode.
func testReconcileRecord() ReconcileRecord {
	return ReconcileRecord{
		Machine:          "m1",
		EpochID:          7,
		InboundHighWater: 42,
		JournalCeiling:   3,
		ReplyCeiling:     5,
		GrantEpoch:       7,
		GrantSeq:         2,
		IssuedAt:         1700000000000,
	}
}

// TestReconcileRecord_WireShape pins the exact committed JSON: field names AND order.
func TestReconcileRecord_WireShape(t *testing.T) {
	got, err := json.Marshal(testReconcileRecord())
	if err != nil {
		t.Fatalf("marshal reconcile record: %v", err)
	}
	if string(got) != wireReconcileRecord {
		t.Fatalf("reconcile record wire shape =\n  %s\nwant\n  %s", got, wireReconcileRecord)
	}
}

// TestReconcileRecord_ZeroAuthoritiesAreExplicitOnTheWire is the adversarial pin (9
// rule 5). No authority field may carry omitempty: a legitimately-zero authority (a
// gateway that has accepted nothing inbound yet, a machine that has issued no grant
// beyond the first) MUST be distinguishable on the wire from a producer that never set
// the field at all. With omitempty they are the same bytes, and the phone would treat
// "not published" as "authority is 0" -- which raises no high-water and silently leaves
// every retained pre-rollback frame acceptable. That is a security downgrade dressed as
// a serialization convenience.
func TestReconcileRecord_ZeroAuthoritiesAreExplicitOnTheWire(t *testing.T) {
	got, err := json.Marshal(ReconcileRecord{})
	if err != nil {
		t.Fatalf("marshal zero record: %v", err)
	}
	want := `{"machine":"","epoch_id":0,"inbound_high_water":0,"journal_ceiling":0,"reply_ceiling":0,"grant_epoch":0,"grant_seq":0,"issued_at":0,"profile":` + wireZeroProfile + `}`
	if string(got) != want {
		t.Fatalf("zero reconcile record =\n  %s\nwant\n  %s\n(no authority field may be omitempty)", got, want)
	}
}

// TestReconcileRecord_DecodesFromTheCommittedBytes closes the loop: the bytes the
// gateway seals decode back to every authority, so the phone reads what was published.
func TestReconcileRecord_DecodesFromTheCommittedBytes(t *testing.T) {
	var got ReconcileRecord
	if err := json.Unmarshal([]byte(wireReconcileRecord), &got); err != nil {
		t.Fatalf("unmarshal committed bytes: %v", err)
	}
	// ReconcileRecord now carries a Profile field with a slice and a map, so it is not
	// comparable with == -- reflect.DeepEqual is the correct (and only) equality check.
	if !reflect.DeepEqual(got, testReconcileRecord()) {
		t.Fatalf("decoded record = %+v; want %+v", got, testReconcileRecord())
	}
}
