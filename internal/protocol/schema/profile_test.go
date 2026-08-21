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
// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5) EXTENDS this seam
// rather than adding a second one, per this file's own comment above CurrentProfileVersion:
// "ADR-016 is the currently known co-owner: it adds relay_tls_policy, relay_host and the
// pin set to this same struct (ADR-016:194) ... their GG-7 field-table obligation is
// ADR-016's own". Three fields join RemoteProfileV1, additive, NO omitempty (the same rule
// this file's header gives for every other field -- "an absent key must stay
// distinguishable from a legitimately-zero one"):
//
//	RelayTLSPolicy string `json:"relay_tls_policy"`
//	RelayHost      string `json:"relay_host"`
//	RelaySPKIPin   []byte `json:"relay_spki_pin"`
//
// RelaySPKIPin carries no omitempty EVEN THOUGH MachinePayload's and phonecore.State's own
// copies of this same coordinate do (both are `,omitempty`): those two are single-shot
// pairing-time and restart-time artifacts where "the key is absent" already reads
// correctly as "no pin published". RemoteProfileV1 is different -- it rides EVERY
// reconcile, on an EXISTING pairing, and it is the channel W4/W9's migration ladder polls
// for a change. An `omitempty` RelaySPKIPin would make "the machine stopped publishing a
// pin" (W9 step 6, the intended end state) byte-identical to "this reconcile record is
// silent on the pin, nothing changed" -- exactly the ambiguity ReconcileRecord's own
// header forbids for every field here, and precisely the ambiguity W9 step 6 depends on
// being resolvable ("no observable change on a migrated handset" must be a comparison
// against an explicit absence, not an inference from a missing key).
//
// GG-7: this struct's field table lives in docs/specifications/protocol.md under "The
// RemoteProfileV1 record", with the header "Field" (RemoteProfileV1 is not one of the four
// GG-7-reflected types walked by wireJSONTags/protocolmd_bidi_test.go), so the three new
// rows are added there in this same change.
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
// in sorted order (encoding/json), so "kill" precedes "launch". The three ADR-016 fields
// ride LAST, appended in field-declaration order, matching RemoteCommand's own convention
// of adding new fields at the end of the struct rather than reordering.
const wireRemoteProfileV1 = `{"version":1,"accepted_actions":["launch","kill"],"accepted_body_versions":{"kill":1,"launch":2},"interaction_schema_version":1,"terminal_view_version":1,"capability_record_version":1,"relay_tls_policy":"webpki","relay_host":"swarm-relay.example.com","relay_spki_pin":"MzItYnl0ZS1zaGEyNTYtZGlnZXN0LW9mLXNwa2khIQ==","terminal_view_max_line_bytes":0,"terminal_view_max_rows":0,"terminal_view_max_rate_hz":0}`

func testRemoteProfile() RemoteProfileV1 {
	return RemoteProfileV1{
		Version:                  1,
		AcceptedActions:          []string{"launch", "kill"},
		AcceptedBodyVersions:     map[string]int{"launch": 2, "kill": 1},
		InteractionSchemaVersion: 1,
		TerminalViewVersion:      1,
		CapabilityRecordVersion:  1,
		RelayTLSPolicy:           "webpki",
		RelayHost:                "swarm-relay.example.com",
		RelaySPKIPin:             []byte("32-byte-sha256-digest-of-spki!!"),
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

// TestRemoteProfileV1_RelayTLSFieldsAreIndependent is ADR-016 W1's Conformance-table
// mutation control, at the profile layer: a policy with no pin, and a pin with no policy,
// each round-trip verbatim -- neither is inferred from the other, and RelaySPKIPin's
// json.RawMessage-style presence (a non-nil zero-length slice differs from nil under
// encoding/json only via omitempty, which this field does NOT carry) still distinguishes
// "no pin" (nil) from an explicitly empty one is not claimed here; what IS pinned is that
// setting one field never mutates the other.
func TestADR016W1_RemoteProfileV1_RelayTLSFieldsAreIndependent(t *testing.T) {
	webpkiNoPin := RemoteProfileV1{Version: 1, RelayTLSPolicy: "webpki", RelayHost: "swarm-relay.example.com"}
	b, err := json.Marshal(webpkiNoPin)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RemoteProfileV1
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RelayTLSPolicy != "webpki" || got.RelayHost != "swarm-relay.example.com" {
		t.Errorf("got %+v, want policy=webpki host=swarm-relay.example.com", got)
	}
	if len(got.RelaySPKIPin) != 0 {
		t.Errorf("RelaySPKIPin = %x, want empty: a webpki profile with no pin configured must not "+
			"manufacture one on decode", got.RelaySPKIPin)
	}

	pinnedNoPolicy := RemoteProfileV1{Version: 1, RelaySPKIPin: []byte("a-configured-pin-of-some-length")}
	b2, err := json.Marshal(pinnedNoPolicy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got2 RemoteProfileV1
	if err := json.Unmarshal(b2, &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got2.RelayTLSPolicy != "" {
		t.Errorf("RelayTLSPolicy = %q, want empty: a pin's presence must never imply pinned_spki "+
			"(W1's mutation control)", got2.RelayTLSPolicy)
	}
	if string(got2.RelaySPKIPin) != string(pinnedNoPolicy.RelaySPKIPin) {
		t.Errorf("RelaySPKIPin = %x, want %x", got2.RelaySPKIPin, pinnedNoPolicy.RelaySPKIPin)
	}
}

// TestADR016W1_RemoteProfileV1_NoFieldCarriesOmitempty extends
// TestReconcileRecord_ProfileFieldTag_NoOmitempty's discipline onto the three new fields,
// for the reason this file's header now states: RemoteProfileV1 rides every reconcile, and
// an omitted key must never be confused with an explicit zero value on ANY field, the new
// ones included.
func TestADR016W1_RemoteProfileV1_NoFieldCarriesOmitempty(t *testing.T) {
	ty := reflect.TypeOf(RemoteProfileV1{})
	for _, name := range []string{"RelayTLSPolicy", "RelayHost", "RelaySPKIPin"} {
		f, ok := ty.FieldByName(name)
		if !ok {
			t.Fatalf("RemoteProfileV1 has no %s field", name)
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag != name2wireTag(name) {
			t.Errorf("%s json tag = %q, want exactly %q (no omitempty)", name, tag, name2wireTag(name))
		}
	}
}

func name2wireTag(goName string) string {
	switch goName {
	case "RelayTLSPolicy":
		return "relay_tls_policy"
	case "RelayHost":
		return "relay_host"
	case "RelaySPKIPin":
		return "relay_spki_pin"
	default:
		return ""
	}
}
