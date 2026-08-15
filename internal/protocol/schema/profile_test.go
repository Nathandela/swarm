package schema

// FAILING-FIRST (TDD RED, GG-5) tests for ADR-017 T5 / playbook 6.3: the machine-
// authored RemoteProfileV1 the daemon seals to the phone DURING RECONCILIATION. The
// asynchronous E2EE mailbox has no local `hello` (T5), so this is the only channel a
// phone learns which action/body versions, interaction-schema version, TerminalView
// version and capability-record version the machine currently accepts.
//
// CARRIER: RemoteProfileV1 rides the EXISTING ReconcileRecord (PB-SYNC-7) as a new
// named field -- no new mailbox frame kind, no envelope change (IS-LAYER-1: "new
// payloads ride existing families"). internal/remote/crypto stays untouched: this is
// still an ordinary sealed mailbox plaintext with a kind tag.
//
// THE SEAMS these tests pin (undefined symbols -> compile-fail RED):
//
//	type RemoteProfileV1 struct {
//	    Version                  int
//	    AcceptedActions          []string
//	    AcceptedBodyVersions     map[string]int
//	    InteractionSchemaVersion int
//	    TerminalViewVersion      int
//	    CapabilityRecordVersion  int
//	}
//	ReconcileRecord gains: Profile RemoteProfileV1 `json:"profile"` (no omitempty)
//
// WHY Profile MAY NOT CARRY omitempty, stated because the reason is easy to miss for a
// struct-typed field: encoding/json never treats a non-pointer struct as "empty", so
// `omitempty` on Profile would be a silent no-op on THIS type today -- but reconcile.go's
// rule ("NO FIELD MAY CARRY omitempty ... a producer that never published the field" must
// stay distinguishable from a legitimate zero value) is a SOURCE-LEVEL convention, not
// only a runtime one: a reader who sees `omitempty` on a sibling authority field reads it
// as "this may legitimately be absent", which is false for every field in this record,
// Profile included. TestReconcileRecord_ProfileFieldTag_NoOmitempty pins the tag text
// itself for exactly this reason.

import (
	"encoding/json"
	"reflect"
	"testing"
)

// wireRemoteProfileV1 is the committed JSON of testRemoteProfile below. Map keys marshal
// in sorted order (encoding/json), so "kill" precedes "launch".
const wireRemoteProfileV1 = `{"version":1,"accepted_actions":["launch","kill"],"accepted_body_versions":{"kill":1,"launch":2},"interaction_schema_version":1,"terminal_view_version":1,"capability_record_version":1}`

func testRemoteProfile() RemoteProfileV1 {
	return RemoteProfileV1{
		Version:                  1,
		AcceptedActions:          []string{"launch", "kill"},
		AcceptedBodyVersions:     map[string]int{"launch": 2, "kill": 1},
		InteractionSchemaVersion: 1,
		TerminalViewVersion:      1,
		CapabilityRecordVersion:  1,
	}
}

// TestRemoteProfileV1_WireShape pins the exact committed JSON: field names AND order,
// the same discipline TestReconcileRecord_WireShape applies to its siblings.
func TestRemoteProfileV1_WireShape(t *testing.T) {
	got, err := json.Marshal(testRemoteProfile())
	if err != nil {
		t.Fatalf("marshal RemoteProfileV1: %v", err)
	}
	if string(got) != wireRemoteProfileV1 {
		t.Fatalf("RemoteProfileV1 wire shape =\n  %s\nwant\n  %s", got, wireRemoteProfileV1)
	}
}

// TestRemoteProfileV1_DecodesFromTheCommittedBytes closes the loop: the bytes the
// machine seals decode back to every field, so the phone reads what was published.
func TestRemoteProfileV1_DecodesFromTheCommittedBytes(t *testing.T) {
	var got RemoteProfileV1
	if err := json.Unmarshal([]byte(wireRemoteProfileV1), &got); err != nil {
		t.Fatalf("unmarshal committed bytes: %v", err)
	}
	if !reflect.DeepEqual(got, testRemoteProfile()) {
		t.Fatalf("decoded profile = %+v; want %+v", got, testRemoteProfile())
	}
}

// testReconcileRecordWithProfile extends reconcile_test.go's testReconcileRecord with a
// fully populated profile, so the round-trip below exercises every field of both.
func testReconcileRecordWithProfile() ReconcileRecord {
	r := testReconcileRecord()
	r.Profile = testRemoteProfile()
	return r
}

// wireReconcileRecordWithProfile is wireReconcileRecord (reconcile_test.go) with the
// profile object appended as the record's last field.
const wireReconcileRecordWithProfile = `{"machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":` + wireRemoteProfileV1 + `}`

// TestReconcileRecord_CarriesProfile_WireShape pins that Profile lands on the wire as a
// named field beside the three existing authorities, not folded into them and not a
// second reconcile shape.
func TestReconcileRecord_CarriesProfile_WireShape(t *testing.T) {
	got, err := json.Marshal(testReconcileRecordWithProfile())
	if err != nil {
		t.Fatalf("marshal reconcile record with profile: %v", err)
	}
	if string(got) != wireReconcileRecordWithProfile {
		t.Fatalf("reconcile record wire shape =\n  %s\nwant\n  %s", got, wireReconcileRecordWithProfile)
	}
}

// TestReconcileRecord_CarriesProfile_RoundTrips: the profile survives an unmarshal with
// EVERY field intact -- the exact contract T5 asks for ("profile round-trips through the
// reconcile path with every field").
func TestReconcileRecord_CarriesProfile_RoundTrips(t *testing.T) {
	var got ReconcileRecord
	if err := json.Unmarshal([]byte(wireReconcileRecordWithProfile), &got); err != nil {
		t.Fatalf("unmarshal committed bytes: %v", err)
	}
	if !reflect.DeepEqual(got, testReconcileRecordWithProfile()) {
		t.Fatalf("decoded record = %+v; want %+v", got, testReconcileRecordWithProfile())
	}
}

// TestReconcileRecord_ProfileFieldTag_NoOmitempty pins the source-level convention this
// file's header explains: the json tag is exactly "profile", never "profile,omitempty".
func TestReconcileRecord_ProfileFieldTag_NoOmitempty(t *testing.T) {
	f, ok := reflect.TypeOf(ReconcileRecord{}).FieldByName("Profile")
	if !ok {
		t.Fatalf("ReconcileRecord has no Profile field")
	}
	if tag := f.Tag.Get("json"); tag != "profile" {
		t.Fatalf("Profile json tag = %q; want exactly %q (omitempty is prohibited on every ReconcileRecord field, Profile included -- reconcile.go's rule)", tag, "profile")
	}
}
