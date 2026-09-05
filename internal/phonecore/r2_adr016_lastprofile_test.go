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
	"bytes"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// wireReconcileFrameWithProfile is wireReconcileFrame (reconcile_test.go) with a POPULATED
// profile block instead of the zero one, so LastProfile has something distinguishable from
// "nothing reconciled yet" to hand back.
const wireReconcileFrameWithProfile = `{"kind":"reconcile","machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":{"version":1,"accepted_actions":null,"accepted_body_versions":null,"interaction_schema_version":0,"terminal_view_version":0,"capability_record_version":0,"relay_tls_policy":"webpki","relay_host":"swarm-relay.example.com","relay_spki_pin":null}}`

const wireReconcileFrameWithCurrentProfile = `{"kind":"reconcile","machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":{"version":1,"accepted_actions":["chat_send"],"accepted_body_versions":{"chat_send":1},"interaction_schema_version":1,"terminal_view_version":1,"capability_record_version":1,"relay_tls_policy":"webpki","relay_host":"swarm-relay.example.com","relay_spki_pin":"cHJvZmlsZS1waW4="}}`

// opaqueProfileStore has exactly the powers an implementation in another package has over
// State: Save can retain the whole value Core handed it, but PurgeKeys can clear only exported
// coordinates. In particular it cannot name State.lastProfile, which makes Core responsible
// for enforcing that a disowned State never exposes the retained authority on adoption.
type opaqueProfileStore struct{ st State }

func (s *opaqueProfileStore) Load() State { return s.st }
func (s *opaqueProfileStore) Save(st State) error {
	st.phoneBinding = s.st.phoneBinding
	s.st = st
	return nil
}
func (s *opaqueProfileStore) ActivatePhoneBinding(st State) error { s.st = st; return nil }
func (s *opaqueProfileStore) CommitPhonePairing(st State) error   { s.st = st; return nil }
func (s *opaqueProfileStore) PurgeKeys() error {
	s.st.Keys = crypto.EpochKeys{}
	s.st.Sessions, s.st.Snapshots, s.st.Items = nil, nil, nil
	s.st.PendingOps, s.st.OpOutcomes = nil, nil
	s.st.PushToken = ""
	s.st.Disowned = true
	return nil
}
func (s *opaqueProfileStore) UnsealContent() error { return nil }
func (s *opaqueProfileStore) RewindRelayCursor() error {
	s.st.RelayCursor, s.st.RelayIncarnation = 0, ""
	return nil
}
func (s *opaqueProfileStore) SetRelayIncarnation(v string) error {
	s.st.RelayIncarnation = v
	return nil
}

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

// TestLastProfileAndComposerSurviveProcessDeath is the physical Android regression: the
// journal cache and its capability record already survived SIGKILL, but the profile used to
// validate that record lived only on Core. Every restored structured session therefore routed
// to the status card until another reconcile happened to arrive.
func TestLastProfileAndComposerSurviveProcessDeath(t *testing.T) {
	f := newRollbackFixture(t)
	c, err := Resume(Config{Dir: f.dir, Machine: "m1", WakeSealer: f.wake, ContentSealer: f.content})
	if err != nil {
		t.Fatalf("Resume before reconcile: %v", err)
	}
	if err := c.Mutate(func(st *State) {
		st.Sessions = []CachedSession{{
			SessionID: "m1/structured",
			Capabilities: &schema.SessionCapabilities{
				Provider: "codex", ProviderVersion: "1", AdapterRevision: "r1",
				SessionInstance: "instance-1", StructuredChat: true,
			},
		}}
	}); err != nil {
		t.Fatalf("persist structured session: %v", err)
	}
	raw := sealFrameFrom(t, f.key, machineSender, 7, 3, []byte(wireReconcileFrameWithCurrentProfile))
	if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
		t.Fatalf("deliver current reconcile: %v", err)
	}
	if err := c.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !ComposerAvailable(c.State().Sessions[0].Capabilities, c.LastProfile()) {
		t.Fatal("precondition: structured session is not composer-enabled before process death")
	}

	// PROCESS DEATH: the second Core gets only the durable directory and no inbound frame.
	restored, err := Resume(Config{Dir: f.dir, Machine: "m1", WakeSealer: f.wake, ContentSealer: f.content})
	if err != nil {
		t.Fatalf("Resume after process death: %v", err)
	}
	sessions := restored.State().Sessions
	if len(sessions) != 1 || sessions[0].Capabilities == nil {
		t.Fatalf("restored sessions = %+v, want the structured capability record", sessions)
	}
	profile := restored.LastProfile()
	if profile.CapabilityRecordVersion != 1 {
		t.Fatalf("LastProfile().CapabilityRecordVersion after process death = %d, want 1", profile.CapabilityRecordVersion)
	}
	if !ComposerAvailable(sessions[0].Capabilities, profile) {
		t.Fatal("structured session routed away from chat after process death without a new frame")
	}
}

// TestLastProfileIsDeepCloned keeps a caller from mutating the authenticated authority held by
// Core through a map or slice returned from LastProfile.
func TestLastProfileIsDeepCloned(t *testing.T) {
	f := newRollbackFixture(t)
	c := f.resume(t)
	raw := sealFrameFrom(t, f.key, machineSender, 7, 3, []byte(wireReconcileFrameWithCurrentProfile))
	if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
		t.Fatalf("deliver current reconcile: %v", err)
	}
	if err := c.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := c.LastProfile()
	got.AcceptedActions[0] = "kill"
	got.AcceptedBodyVersions["chat_send"] = 99
	got.RelaySPKIPin[0] ^= 0xff
	again := c.LastProfile()
	if again.AcceptedActions[0] != "chat_send" || again.AcceptedBodyVersions["chat_send"] != 1 ||
		!bytes.Equal(again.RelaySPKIPin, []byte("profile-pin")) {
		t.Fatalf("LastProfile returned aliases into authenticated state: %+v", again)
	}
}

// TestLastProfileIsFencedByIdentityReplacement prevents a profile authenticated for one
// machine epoch from authorizing capability records after pairing rotates either identity.
func TestLastProfileIsFencedByIdentityReplacement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*State)
	}{
		{"new epoch", func(st *State) { st.EpochID = 8; st.Keys = crypto.EpochKeys{} }},
		{"new machine", func(st *State) { st.Machine = "m2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRollbackFixture(t)
			c, err := Resume(Config{Dir: f.dir, Machine: "m1", WakeSealer: f.wake, ContentSealer: f.content})
			if err != nil {
				t.Fatalf("Resume before reconcile: %v", err)
			}
			raw := sealFrameFrom(t, f.key, machineSender, 7, 3, []byte(wireReconcileFrameWithCurrentProfile))
			if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
				t.Fatalf("deliver current reconcile: %v", err)
			}
			if err := c.Reconcile(); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if err := c.Mutate(tc.mutate); err != nil {
				t.Fatalf("replace identity: %v", err)
			}
			if got := c.LastProfile(); !reflect.DeepEqual(got, schema.RemoteProfileV1{}) {
				t.Fatalf("LastProfile after %s = %+v, want zero/fail-closed", tc.name, got)
			}
			machine := c.State().Machine
			restored, err := Resume(Config{Dir: f.dir, Machine: machine, WakeSealer: f.wake, ContentSealer: f.content})
			if err != nil {
				t.Fatalf("Resume after %s: %v", tc.name, err)
			}
			if got := restored.LastProfile(); !reflect.DeepEqual(got, schema.RemoteProfileV1{}) {
				t.Fatalf("LastProfile after restart following %s = %+v, want zero/fail-closed", tc.name, got)
			}
		})
	}
}

// TestLastProfileIsFencedFromAnOpaqueInjectedStoreAfterDisown covers the Store boundary,
// not fileStore's in-package implementation. An external Store cannot name the private
// lastProfile field to clear it during PurgeKeys, so Core must treat Disowned as the final
// authority fence both immediately and on a later Resume.
func TestLastProfileIsFencedFromAnOpaqueInjectedStoreAfterDisown(t *testing.T) {
	f := newRollbackFixture(t)
	store := &opaqueProfileStore{st: f.store.st}
	c, err := Resume(Config{State: store, Machine: "m1"})
	if err != nil {
		t.Fatalf("Resume before reconcile: %v", err)
	}
	raw := sealFrameFrom(t, f.key, machineSender, 7, 3, []byte(wireReconcileFrameWithCurrentProfile))
	if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
		t.Fatalf("deliver current reconcile: %v", err)
	}
	if err := c.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if c.LastProfile().CapabilityRecordVersion != 1 {
		t.Fatal("precondition: authenticated profile was not adopted")
	}

	if err := c.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if got := c.LastProfile(); !reflect.DeepEqual(got, schema.RemoteProfileV1{}) {
		t.Fatalf("LastProfile after injected-store disown = %+v, want zero/fail-closed", got)
	}

	restored, err := Resume(Config{State: store, Machine: "m1"})
	if err != nil {
		t.Fatalf("Resume after injected-store disown: %v", err)
	}
	if got := restored.LastProfile(); !reflect.DeepEqual(got, schema.RemoteProfileV1{}) {
		t.Fatalf("LastProfile after restart from injected-store disown = %+v, want zero/fail-closed", got)
	}
}
