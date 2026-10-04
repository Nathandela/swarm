package accounts

import (
	"errors"
	"testing"
	"time"
)

func observation(sequence uint64, now time.Time, updates ...ScopeObservation) Observation {
	return Observation{FeedGeneration: 1, Sequence: sequence, ReceivedAt: now, Full: true, Scopes: updates}
}

func TestInitialDefaultModelSelectionDoesNotWeakenRecovery(t *testing.T) {
	s, _ := testStore(t)
	_, account := admitNative(t, s, "native-default")
	r, _ := s.Snapshot()
	r.Enabled[ProviderCodex] = true
	now := time.Now()
	request := SelectionRequest{Provider: ProviderCodex, ConfigurationGeneration: 1}
	selected, err := SelectInitial(r, request, now)
	if err != nil || selected.Binding.AccountID != account.ID || !selected.Unknown {
		t.Fatalf("unresolved native default was not assigned as unknown: %+v %v", selected, err)
	}
	if _, err := Select(r, request, now); !errors.Is(err, ErrIneligible) {
		t.Fatal("recovery accepted an unresolved model")
	}
	for _, mutate := range []func(*SelectionRequest){
		func(r *SelectionRequest) { r.Failed = true },
		func(r *SelectionRequest) { r.Current = &selected.Binding },
		func(r *SelectionRequest) { r.TriedAccounts = map[string]bool{account.ID: true} },
	} {
		invalid := request
		mutate(&invalid)
		if _, err := SelectInitial(r, invalid, now); !errors.Is(err, ErrIneligible) {
			t.Fatal("initial assignment accepted recovery state")
		}
	}
	for _, scope := range []ScopeState{
		{Denied: true, DenialRevision: 1},
		{Model: "limited-native-model", Denied: true, DenialRevision: 1},
	} {
		record := r.Accounts[account.ID]
		key := ScopeGlobal
		if scope.Model != "" {
			key = "model:" + scope.Model
		}
		record.Quota = QuotaState{Scopes: map[string]ScopeState{key: scope}}
		r.Accounts[account.ID] = record
		if _, err := SelectInitial(r, request, now); !errors.Is(err, ErrNoCapacity) {
			t.Fatal("unresolved default bypassed a known capacity denial")
		}
	}
}

func TestSparseObservationDoesNotRefreshAllowedAuthorityOrUsage(t *testing.T) {
	old := time.Now().Add(-QuotaFreshness - time.Minute)
	now := old.Add(QuotaFreshness + time.Minute)
	usage := 10
	q, err := ReduceQuota(QuotaState{}, observation(1, old, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityAllowed, UsedPercent: &usage}), old)
	if err != nil {
		t.Fatal(err)
	}
	q, err = ReduceQuota(q, observation(2, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityUnknown, ResetPresent: true}), now)
	if err != nil {
		t.Fatal(err)
	}
	usable, unknown, utilization := quotaEligibility(q, "model", now)
	if !usable || !unknown || utilization != nil || !q.Scopes[ScopeGlobal].AuthorityObservedAt.Equal(old) || !q.Scopes[ScopeGlobal].UsageObservedAt.Equal(old) {
		t.Fatalf("sparse telemetry refreshed prior authority/usage: %+v %v %v %v", q, usable, unknown, utilization)
	}
}

func TestSelectionStableWithMixedKnownAndMissingUsage(t *testing.T) {
	s, _ := testStore(t)
	_, a := admitNative(t, s, "usage-a")
	_, b := admitNative(t, s, "usage-b")
	_, c := admitNative(t, s, "usage-c")
	r, _ := s.Snapshot()
	r.Enabled[ProviderCodex] = true
	now := time.Now()
	low, high := 10, 20
	for id, usage := range map[string]*int{a.ID: &low, b.ID: &high, c.ID: nil} {
		account := r.Accounts[id]
		account.Quota = QuotaState{Scopes: map[string]ScopeState{ScopeGlobal: {Allowed: true, ObservedAt: now, UsedPercent: usage}}}
		r.Accounts[id] = account
	}
	request := SelectionRequest{Provider: ProviderCodex, Model: "model", ConfigurationGeneration: 1, ActiveCounts: map[string]int{a.ID: 100, b.ID: 0, c.ID: 50}}
	for iteration := 0; iteration < 1000; iteration++ {
		selected, err := Select(r, request, now)
		if err != nil || selected.Binding.AccountID != a.ID {
			t.Fatalf("unstable mixed telemetry selection at %d: %+v %v", iteration, selected, err)
		}
	}
}

func TestDenialRevisionBarrierSparseNullAndDuplicate(t *testing.T) {
	now := time.Now()
	q := QuotaState{FeedGeneration: 1}
	b := Binding{SchemaVersion: 1, Provider: ProviderCodex, AccountID: "11111111111111111111111111111111", CredentialGeneration: 1, Identity: identityDigest(ProviderCodex, "a"), ConfigurationGeneration: 1}
	probe := CaptureProbe(b, q, []string{ScopeGlobal, "model:x"})
	reset := now.Add(time.Hour)
	deny := observation(1, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied, ResetPresent: true, ResetAt: &reset})
	deny.EventID = "request-1"
	q, err := ReduceQuota(q, deny, now)
	if err != nil {
		t.Fatal(err)
	}
	positive := observation(2, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityAllowed, ResetPresent: true})
	positive.Probe = &probe
	q, err = ReduceQuota(q, positive, now)
	if err != nil {
		t.Fatal(err)
	}
	global := q.Scopes[ScopeGlobal]
	if !global.Denied || global.DenialRevision != 1 || global.ResetAt != nil || global.CooldownUntil == nil || !global.CooldownUntil.Equal(reset) {
		t.Fatalf("stale positive/null reset cleared denial/cooldown: %+v", global)
	}
	deny.Sequence = 3
	q, _ = ReduceQuota(q, deny, now)
	if q.Scopes[ScopeGlobal].DenialRevision != 1 {
		t.Fatal("exact duplicate negative advanced barrier")
	}
	lowUsage := 2
	q, _ = ReduceQuota(q, observation(4, now, ScopeObservation{Scope: ScopeGlobal, UsedPercent: &lowUsage}), now)
	if !q.Scopes[ScopeGlobal].Denied {
		t.Fatal("sparse telemetry cleared denial")
	}
	freshProbe := CaptureProbe(b, q, []string{ScopeGlobal})
	positive.Sequence = 5
	positive.Probe = &freshProbe
	q, err = ReduceQuota(q, positive, now)
	if err != nil || q.Scopes[ScopeGlobal].Denied || !q.Scopes[ScopeGlobal].Allowed {
		t.Fatalf("matching fresh positive failed to clear: %+v %v", q, err)
	}
}

func TestModelSuccessCannotClearOtherScopeAndOldFeeds(t *testing.T) {
	now := time.Now()
	q, _ := ReduceQuota(QuotaState{}, observation(1, now,
		ScopeObservation{Scope: "model:x", Authority: AuthorityDenied},
		ScopeObservation{Scope: "model:y", Authority: AuthorityDenied},
		ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied}), now)
	stamp := CaptureProbe(Binding{}, q, []string{"model:x"})
	positive := observation(2, now, ScopeObservation{Scope: "model:x", Authority: AuthorityAllowed})
	positive.Probe = &stamp
	q, _ = ReduceQuota(q, positive, now)
	if q.Scopes["model:x"].Denied || !q.Scopes["model:y"].Denied || !q.Scopes[ScopeGlobal].Denied {
		t.Fatal("model-scoped positive cleared broader denial")
	}
	newFeed := observation(1, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityAllowed})
	newFeed.FeedGeneration = 2
	newFeed.Full = false
	q, _ = ReduceQuota(q, newFeed, now)
	if !q.FullRefreshNeeded || !q.Scopes[ScopeGlobal].Denied {
		t.Fatal("feed reconnect relied on sparse positive")
	}
	old := observation(9, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied})
	previous := q.Scopes[ScopeGlobal].DenialRevision
	q, _ = ReduceQuota(q, old, now)
	if q.FeedGeneration != 2 || q.Scopes[ScopeGlobal].DenialRevision != previous {
		t.Fatal("old feed mutated current quota")
	}
}

func TestStickySelectionUnknownLeaseAndLogicalAccountAttempt(t *testing.T) {
	s, _ := testStore(t)
	_, a := admitNative(t, s, "a")
	_, other := admitNative(t, s, "b")
	current, _ := s.CurrentBinding(a.ID, 1)
	r, _ := s.Snapshot()
	r, _ = s.SetEnabled(r.Revision, ProviderCodex, true)
	now := time.Now()
	denied := r.Accounts[a.ID]
	denied.Quota, _ = ReduceQuota(denied.Quota, observation(1, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied}), now)
	r.Accounts[a.ID] = denied
	request := SelectionRequest{Provider: ProviderCodex, Model: "gpt", ConfigurationGeneration: 1, Current: &current}
	selected, err := Select(r, request, now)
	if err != nil || !selected.Sticky || selected.Binding.AccountID != a.ID {
		t.Fatal("telemetry alone rotated healthy current")
	}
	request.Failed = true
	selected, err = Select(r, request, now)
	if err != nil || selected.Binding.AccountID != other.ID || !selected.Unknown {
		t.Fatalf("unknown fallback: %+v %v", selected, err)
	}
	request.Trials = []TrialLease{{ID: "trial", Binding: selected.Binding, Model: request.Model, Deadline: now.Add(TrialLeaseDuration)}}
	if _, err := Select(r, request, now); !errors.Is(err, ErrNoCapacity) {
		t.Fatal("unknown trial launch storm admitted")
	}
	if _, err := Select(r, request, now.Add(TrialLeaseDuration)); err != nil {
		t.Fatalf("expired trial still blocked unknown capacity: %v", err)
	}
	request.Trials = nil
	request.TriedAccounts = map[string]bool{other.ID: true}
	newGeneration := r.Accounts[other.ID]
	newGeneration.CurrentGeneration++
	g := newGeneration.Generations[1]
	g.Number++
	newGeneration.Generations[g.Number] = g
	r.Accounts[other.ID] = newGeneration
	if _, err := Select(r, request, now); !errors.Is(err, ErrNoCapacity) {
		t.Fatal("credential generation churn reset logical-account trial")
	}
}

func TestResetCrossingAndOwnerHalfOpenModelException(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Minute)
	q, err := ReduceQuota(QuotaState{}, observation(1, now,
		ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied, ResetPresent: true, ResetAt: &reset},
		ScopeObservation{Scope: "bucket:5h", Authority: AuthorityDenied, ResetPresent: true, ResetAt: &reset}), now)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{SchemaVersion: 1, Provider: ProviderClaude, AccountID: "11111111111111111111111111111111", CredentialGeneration: 1, Identity: identityDigest(ProviderClaude, "a"), ConfigurationGeneration: 1}
	if _, err := AcquireHalfOpen(b, q, "sonnet", "owner-check", true, now, nil); !errors.Is(err, ErrNoCapacity) {
		t.Fatal("future reset allowed model check")
	}
	due := reset.Add(time.Second)
	if usable, _, _ := quotaEligibility(q, "sonnet", due); usable {
		t.Fatal("reset crossing claimed recovered capacity")
	}
	if _, err := AcquireHalfOpen(b, q, "sonnet", "timer-check", false, due, nil); !errors.Is(err, ErrIneligible) {
		t.Fatal("background timer authorized native model check")
	}
	permit, err := AcquireHalfOpen(b, q, "sonnet", "owner-check", true, due, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireHalfOpen(b, q, "sonnet", "duplicate", true, due, &permit); !errors.Is(err, ErrIneligible) {
		t.Fatal("duplicate owner clicks gained parallel permits")
	}
	if err := permit.Spend(b, q, due); err != nil {
		t.Fatal(err)
	}
	if err := permit.Spend(b, q, due); !errors.Is(err, ErrIneligible) {
		t.Fatal("one permit admitted two native requests")
	}
	q, err = CompleteHalfOpen(q, permit, "owner-check", true, false, due)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Scopes[ScopeGlobal].Denied || !q.Scopes["bucket:5h"].Denied {
		t.Fatal("model proof asserted global quota recovery")
	}
	if allowed, unknown, _ := quotaEligibility(q, "sonnet", due); !allowed || unknown {
		t.Fatal("model-scoped exception unavailable")
	}
	if allowed, _, _ := quotaEligibility(q, "opus", due); allowed {
		t.Fatal("sonnet proof cleared opus restriction")
	}
	if allowed, unknown, _ := quotaEligibility(q, "sonnet", due.Add(QuotaFreshness+time.Second)); !allowed || !unknown {
		t.Fatal("stale model proof discarded or fabricated fresh capacity")
	}
	q, _ = ReduceQuota(q, observation(2, due, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied}), due)
	if allowed, _, _ := quotaEligibility(q, "sonnet", due); allowed {
		t.Fatal("new global denial failed to invalidate model exception")
	}
}

func TestHalfOpenNewDenialAndFailureBackoff(t *testing.T) {
	now := time.Now()
	q, _ := ReduceQuota(QuotaState{}, observation(1, now, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied}), now)
	b := Binding{Identity: identityDigest(ProviderClaude, "a")}
	due := now.Add(InitialTrialBackoff)
	p, err := AcquireHalfOpen(b, q, "sonnet", "check", true, due, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Spend(b, q, due); err != nil {
		t.Fatal(err)
	}
	failed, err := CompleteHalfOpen(q, p, "check", false, false, due)
	if err != nil || !failed.Scopes[ScopeGlobal].Denied || failed.Scopes[ScopeGlobal].TrialBackoffNanos != int64(2*InitialTrialBackoff) {
		t.Fatalf("failed backoff: %+v %v", failed, err)
	}
	newer, _ := ReduceQuota(q, observation(2, due, ScopeObservation{Scope: ScopeGlobal, Authority: AuthorityDenied}), due)
	if _, err := CompleteHalfOpen(newer, p, "check", true, true, due); !errors.Is(err, ErrIneligible) {
		t.Fatal("late half-open result cleared new denial")
	}
	if _, err := CompleteHalfOpen(q, p, "check", true, true, p.Deadline); !errors.Is(err, ErrIneligible) {
		t.Fatal("expired result admitted capacity")
	}
}

func TestIncidentCountsEverySpawnWithoutGenerationBudgetReset(t *testing.T) {
	i := NewIncident("incident", ProviderCodex, "gpt", 5)
	a := "11111111111111111111111111111111"
	b := "22222222222222222222222222222222"
	if i.SpawnLimit != 3 {
		t.Fatal("spawn cap exceeds three")
	}
	if err := i.ReserveAccount(a); err != nil {
		t.Fatal(err)
	}
	if err := i.RecordSpawn(a); err != nil {
		t.Fatal(err)
	}
	if err := i.RecordSpawn(a); err != nil {
		t.Fatal(err)
	}
	if err := i.ReserveAccount(a); !errors.Is(err, ErrNoCapacity) {
		t.Fatal("same logical account reserved after refresh")
	}
	if err := i.ReserveAccount(b); err != nil {
		t.Fatal(err)
	}
	if err := i.RecordSpawn(b); err != nil {
		t.Fatal(err)
	}
	if err := i.RecordSpawn(b); !errors.Is(err, ErrNoCapacity) || !i.Exhausted {
		t.Fatal("restart gained an uncounted fourth spawn")
	}
	other := NewIncident("time-budget", ProviderCodex, "gpt", 3)
	if err := other.SpendActive(IncidentActiveBudget); !errors.Is(err, ErrNoCapacity) || other.RemainingActiveNanos != 0 || !other.Exhausted {
		t.Fatal("active execution budget failed closed")
	}
}
