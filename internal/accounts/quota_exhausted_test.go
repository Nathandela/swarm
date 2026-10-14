package accounts

import (
	"errors"
	"testing"
	"time"
)

// An account at 100% whose reset is still ahead is out of quota even before a
// provider denial is recorded. A2: the selector used to rank it first because its
// usage was fresh while the healthy alternative's was not.
func TestSelectInitial_SkipsAnExhaustedAccountThatIsNotMarkedDenied(t *testing.T) {
	s, _ := testStore(t)
	_, spent := admitNative(t, s, "spent")
	_, spare := admitNative(t, s, "spare")
	r, _ := s.Snapshot()
	r.Enabled[ProviderCodex] = true
	now := time.Now()
	full, none, reset := 100, 0, now.Add(time.Hour)
	setScope := func(id string, scope ScopeState) {
		account := r.Accounts[id]
		account.Quota = QuotaState{Scopes: map[string]ScopeState{ScopeGlobal: scope}}
		r.Accounts[id] = account
	}
	setScope(spent.ID, ScopeState{Allowed: true, ObservedAt: now, UsedPercent: &full, ResetAt: &reset})
	setScope(spare.ID, ScopeState{Allowed: true, ObservedAt: now.Add(-time.Hour), UsedPercent: &none})
	for _, model := range []string{"", "gpt-fixed"} {
		request := SelectionRequest{Provider: ProviderCodex, Model: model, ConfigurationGeneration: 1}
		selected, err := SelectInitial(r, request, now)
		if err != nil || selected.Binding.AccountID != spare.ID {
			t.Fatalf("model %q: selected %q (%v), want the account with quota left %q", model, selected.Binding.AccountID, err, spare.ID)
		}
	}

	delete(r.Accounts, spare.ID)
	if _, err := SelectInitial(r, SelectionRequest{Provider: ProviderCodex, ConfigurationGeneration: 1}, now); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("the only account is exhausted until %v, want ErrNoCapacity, got %v", reset, err)
	}
	past := now.Add(-time.Minute)
	setScope(spent.ID, ScopeState{Allowed: true, ObservedAt: now, UsedPercent: &full, ResetAt: &past})
	if selected, err := SelectInitial(r, SelectionRequest{Provider: ProviderCodex, ConfigurationGeneration: 1}, now); err != nil || selected.Binding.AccountID != spent.ID {
		t.Fatalf("an account whose reset has passed is usable again: %+v %v", selected, err)
	}
}
