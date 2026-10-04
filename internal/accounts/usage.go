package accounts

import (
	"errors"
	"reflect"
	"time"
)

var ErrUsageSuperseded = errors.New("newer account quota evidence superseded this usage read")

// RecordUsage merges passive HTTP telemetry without consuming a native event
// sequence or granting model access. Native events can already have reserved
// their next sequence in the durable inbox before this read completes.
func (s *Store) RecordUsage(expected uint64, binding Binding, baseline QuotaState, updates []ScopeObservation, observedAt time.Time) (Registry, error) {
	telemetry := append([]ScopeObservation(nil), updates...)
	for i := range telemetry {
		telemetry[i].Authority = AuthorityUnknown
	}
	// Reuse the quota trust-boundary validation; only its validated usage fields
	// are copied below, never the synthetic sequence or authority fields.
	parsed, err := ReduceQuota(QuotaState{}, Observation{FeedGeneration: 1, Sequence: 1, ReceivedAt: observedAt, Scopes: telemetry}, time.Now())
	if err != nil {
		return Registry{}, err
	}
	return s.mutate(expected, func(r *Registry) error {
		account, generation, err := usageBindingRecord(*r, binding)
		if err != nil || account.CurrentGeneration != binding.CredentialGeneration || account.Lifecycle == LifecycleRetiring || generation.Kind != KindNative || generation.Identity == "" {
			return ErrIneligible
		}
		if account.Quota.FeedGeneration != baseline.FeedGeneration || account.Quota.LastSequence != baseline.LastSequence {
			return ErrUsageSuperseded
		}
		for _, update := range telemetry {
			if !reflect.DeepEqual(account.Quota.Scopes[update.Scope], baseline.Scopes[update.Scope]) {
				return ErrUsageSuperseded
			}
		}
		quota := cloneQuota(account.Quota)
		for _, update := range telemetry {
			scope, exists := quota.Scopes[update.Scope]
			validated := parsed.Scopes[update.Scope]
			if scope.Model != "" && update.Model != "" && scope.Model != update.Model {
				return ErrIneligible
			}
			if update.UsedPercent != nil {
				scope.UsedPercent = validated.UsedPercent
				scope.UsageObservedAt = observedAt
				if scope.ObservedAt.IsZero() {
					scope.ObservedAt = observedAt
				}
			}
			if update.ResetPresent {
				scope.ResetAt = validated.ResetAt
			}
			// Applicability belongs to native authority. Narrowing an existing
			// denied bucket here would bypass it for other models.
			if !exists && update.Model != "" {
				scope.Model = validated.Model
			}
			quota.Scopes[update.Scope] = scope
		}
		if err := quota.validate(); err != nil {
			return err
		}
		account.Quota = quota
		r.Accounts[account.ID] = account
		return nil
	})
}
