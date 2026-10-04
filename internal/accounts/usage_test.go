package accounts

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestUsageReadCannotConsumePendingNativeDenialSequence(t *testing.T) {
	s, _ := testStore(t)
	_, a := admitNative(t, s, "pending-denial")
	b, err := s.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Snapshot()
	now := time.Now().UTC()
	usage := 46
	reset := now.Add(time.Hour)
	baseline := r.Accounts[a.ID].Quota
	r, err = s.RecordUsage(r.Revision, b, baseline, []ScopeObservation{{Scope: "bucket:codex:primary", UsedPercent: &usage, Authority: AuthorityAllowed, ResetPresent: true, ResetAt: &reset}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if q := r.Accounts[a.ID].Quota; q.FeedGeneration != baseline.FeedGeneration || q.LastSequence != baseline.LastSequence || q.Scopes["bucket:codex:primary"].Allowed {
		t.Fatal("usage read changed native authority or consumed a sequence")
	}
	oldUsage := 100
	oldReset := now.Add(2 * time.Hour)
	// This event was already durably assigned sequence 1 before the HTTP read.
	r, err = s.Observe(r.Revision, b, observation(1, now.Add(-time.Second), ScopeObservation{Scope: "bucket:codex:primary", Authority: AuthorityDenied, UsedPercent: &oldUsage, ResetPresent: true, ResetAt: &oldReset}))
	if err != nil || !r.Accounts[a.ID].Quota.Scopes["bucket:codex:primary"].Denied {
		t.Fatal("pending native denial was lost", err)
	}
	if got := r.Accounts[a.ID].Quota.Scopes["bucket:codex:primary"]; *got.UsedPercent != usage || !got.UsageObservedAt.Equal(now) {
		t.Fatal("older queued telemetry overwrote the newer usage reading")
	}
	if got := r.Accounts[a.ID].Quota.Scopes["bucket:codex:primary"]; got.ResetAt == nil || !got.ResetAt.Equal(reset) || got.CooldownUntil == nil || !got.CooldownUntil.Equal(oldReset) {
		t.Fatal("older queued reset replaced usage reset or lost denial cooldown")
	}
}

func TestUsageReadPreservesDenialAndSparseUsageAge(t *testing.T) {
	s, _ := testStore(t)
	_, a := admitNative(t, s, "latched-usage")
	b, _ := s.CurrentBinding(a.ID, 1)
	r, _ := s.Snapshot()
	old := time.Now().UTC().Add(-time.Minute)
	used := 100
	reset := old.Add(time.Hour)
	r, err := s.Observe(r.Revision, b, observation(1, old, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied, UsedPercent: &used, ResetPresent: true, ResetAt: &reset}))
	if err != nil {
		t.Fatal(err)
	}
	before := r.Accounts[a.ID].Quota
	r, err = s.RecordUsage(r.Revision, b, before, []ScopeObservation{{Scope: ScopeGlobal, Authority: AuthorityAllowed}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, r.Accounts[a.ID].Quota) {
		t.Fatal("sparse usage changed native authority, reset, or usage age")
	}
}

func TestUsageReadRejectsSupersededAndRetiredAccount(t *testing.T) {
	s, _ := testStore(t)
	_, a := admitNative(t, s, "superseded-usage")
	b, _ := s.CurrentBinding(a.ID, 1)
	r, _ := s.Snapshot()
	baseline := r.Accounts[a.ID].Quota
	now := time.Now().UTC()
	r, err := s.Observe(r.Revision, b, observation(1, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied}))
	if err != nil {
		t.Fatal(err)
	}
	usage := 0
	if _, err = s.RecordUsage(r.Revision, b, baseline, []ScopeObservation{{Scope: "bucket:codex:primary", UsedPercent: &usage}}, now); !errors.Is(err, ErrUsageSuperseded) {
		t.Fatal("late HTTP read overwrote newer native evidence", err)
	}
	r, err = s.SetLifecycle(r.Revision, a.ID, LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordUsage(r.Revision, b, r.Accounts[a.ID].Quota, []ScopeObservation{{Scope: "bucket:codex:primary", UsedPercent: &usage}}, now); !errors.Is(err, ErrIneligible) {
		t.Fatal("retired account accepted usage", err)
	}
}
