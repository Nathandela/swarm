package remotegw

// FAILING-FIRST (TDD RED, GG-5) test for ADR-017 T5 / playbook 6.3: the GATEWAY half of
// sealing RemoteProfileV1 into the reconcile record. It rides the SAME reconcile path
// reconcile_test.go pins (PB-SYNC-7) -- no new mailbox frame kind, internal/remote/crypto
// untouched.
//
// THE SEAM this test pins (undefined symbol -> compile-fail RED):
//
//	RelayConfig gains: Profile protocol.RemoteProfileV1
//	RelaySink.Reconcile's authorities() folds cfg.Profile into the sealed record verbatim

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// testProfile is a fully populated RemoteProfileV1, mirroring
// internal/protocol/schema/profile_test.go's testRemoteProfile so the two round-trip
// against the identical value on both sides of the reconcile path.
func testProfile() protocol.RemoteProfileV1 {
	return protocol.RemoteProfileV1{
		Version:                  1,
		AcceptedActions:          []string{"launch", "kill"},
		AcceptedBodyVersions:     map[string]int{"launch": 2, "kill": 1},
		InteractionSchemaVersion: 1,
		TerminalViewVersion:      1,
		CapabilityRecordVersion:  1,
	}
}

// newProfileSink is newReconcileSink (reconcile_test.go) plus a configured profile.
func newProfileSink(app MailboxAppender, key crypto.ContentKey, src ReconcileSource, profile protocol.RemoteProfileV1) *RelaySink {
	fixed := time.Unix(1_700_000_000, 0)
	return NewRelaySink(RelayConfig{
		Appender:       app,
		Target:         "phone-routing-id",
		Machine:        "m1",
		EpochID:        7,
		Key:            key,
		RecipientKeyID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		SenderKeyID:    [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Now:            func() time.Time { return fixed },
		Authorities:    src,
		Profile:        profile,
	})
}

// TestRelaySink_ReconcileCarriesTheConfiguredProfile: Reconcile() seals the SAME
// RemoteProfileV1 the sink was configured with, with every field intact -- "profile
// round-trips through the reconcile path with every field", the machine-side half.
func TestRelaySink_ReconcileCarriesTheConfiguredProfile(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	want := testProfile()
	sink := newProfileSink(app, key, testAuthorities, want)

	if err := sink.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(app.envs) != 1 {
		t.Fatalf("appended %d envelopes; want 1", len(app.envs))
	}

	_, plain := openPlaintext(t, key, app.envs[0])
	var got struct {
		Profile protocol.RemoteProfileV1 `json:"profile"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode reconcile plaintext: %v", err)
	}
	if got.Profile.Version != want.Version ||
		got.Profile.InteractionSchemaVersion != want.InteractionSchemaVersion ||
		got.Profile.TerminalViewVersion != want.TerminalViewVersion ||
		got.Profile.CapabilityRecordVersion != want.CapabilityRecordVersion {
		t.Fatalf("sealed profile scalars = %+v; want %+v", got.Profile, want)
	}
	if len(got.Profile.AcceptedActions) != len(want.AcceptedActions) {
		t.Fatalf("sealed accepted_actions = %v; want %v", got.Profile.AcceptedActions, want.AcceptedActions)
	}
	for i, a := range want.AcceptedActions {
		if got.Profile.AcceptedActions[i] != a {
			t.Fatalf("accepted_actions[%d] = %q; want %q", i, got.Profile.AcceptedActions[i], a)
		}
	}
	if len(got.Profile.AcceptedBodyVersions) != len(want.AcceptedBodyVersions) {
		t.Fatalf("sealed accepted_body_versions = %v; want %v", got.Profile.AcceptedBodyVersions, want.AcceptedBodyVersions)
	}
	for k, v := range want.AcceptedBodyVersions {
		if got.Profile.AcceptedBodyVersions[k] != v {
			t.Fatalf("accepted_body_versions[%q] = %d; want %d", k, got.Profile.AcceptedBodyVersions[k], v)
		}
	}
}

// TestRelaySink_ReconcileWithNoProfileSealsTheZeroValue: an unconfigured Profile is a
// wiring gap, not a fabricated authority -- it seals the zero-value profile rather than
// omitting the field (Profile carries no omitempty, schema/profile_test.go). Without
// omitempty, an unconfigured Profile and a deliberately-empty one are byte-identical on
// the wire; version==0 is the reserved not-yet-published sentinel that is the only thing
// distinguishing them (schema.CurrentProfileVersion starts at 1), which is why this test
// pins the zero value explicitly rather than asserting the key is merely present.
func TestRelaySink_ReconcileWithNoProfileSealsTheZeroValue(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	sink := newProfileSink(app, key, testAuthorities, protocol.RemoteProfileV1{})

	if err := sink.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	_, plain := openPlaintext(t, key, app.envs[0])
	var got struct {
		Profile protocol.RemoteProfileV1 `json:"profile"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode reconcile plaintext: %v", err)
	}
	// RemoteProfileV1 carries a slice and a map, so it is not comparable with == --
	// reflect.DeepEqual is the correct (and only) equality check for it.
	if !reflect.DeepEqual(got.Profile, protocol.RemoteProfileV1{}) {
		t.Fatalf("sealed profile = %+v; want the zero value", got.Profile)
	}
}

// TestRelaySink_ReconcileWithOnlyRelayTLSPolicyConfiguredSealsThatHalfHonestly is ADR-016
// "profile"'s STRENGTHENING of the test above, not a replacement of it: the assertion
// above stays true (an entirely unconfigured Profile still seals as the whole zero value,
// still a wiring gap for the R1 fields this ADR does not own -- AcceptedActions,
// InteractionSchemaVersion and the rest have "no production caller yet",
// schema/profile.go's own words). What changes is that "unconfigured" is no longer a
// single all-or-nothing fact once ADR-016 becomes the profile's first REAL publisher: a
// machine that has run `swarm remote init` always has SOME relay TLS policy (webpki is the
// default, W1), so the relay-TLS third of the profile is populated on every real machine
// while the rest of the struct legitimately waits on its own R1 wiring.
//
// A test that only ever asked "is the whole profile zero" could not see that distinction:
// it would report a profile carrying a real, machine-configured policy as indistinguishable
// from one that never configured anything. That is the "strengthening, not weakening" this
// ADR's RED phase is required to leave behind -- the zero-value check keeps every byte of
// its old meaning, and this test adds the byte-level claim it could not make on its own.
func TestRelaySink_ReconcileWithOnlyRelayTLSPolicyConfiguredSealsThatHalfHonestly(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	// Deliberately only the ADR-016 fields: the rest of RemoteProfileV1 stays the zero
	// value, exactly as it does on a real machine today (no production caller for
	// AcceptedActions etc. yet).
	half := protocol.RemoteProfileV1{
		RelayTLSPolicy: "webpki",
		RelayHost:      "swarm-relay.example.com",
	}
	sink := newProfileSink(app, key, testAuthorities, half)

	if err := sink.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	_, plain := openPlaintext(t, key, app.envs[0])
	var got struct {
		Profile protocol.RemoteProfileV1 `json:"profile"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode reconcile plaintext: %v", err)
	}
	if got.Profile.RelayTLSPolicy != "webpki" || got.Profile.RelayHost != "swarm-relay.example.com" {
		t.Fatalf("the configured relay-TLS half did not seal honestly: got %+v, want policy=webpki "+
			"host=swarm-relay.example.com", got.Profile)
	}
	// The UNCONFIGURED half stays genuinely zero -- this is not "the whole profile got
	// filled in because ONE field was set", which would be the opposite defect (a producer
	// fabricating authority it was never given).
	if got.Profile.Version != 0 || len(got.Profile.AcceptedActions) != 0 || got.Profile.InteractionSchemaVersion != 0 {
		t.Fatalf("fields ADR-016 does not own were non-zero: %+v (a real relay-TLS publisher "+
			"must not fabricate authority over fields it was never configured with)", got.Profile)
	}
}
