package accounts

import (
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	ScopeGlobal          = "global"
	AuthorityUnknown     = "unknown"
	AuthorityAllowed     = "allowed"
	AuthorityDenied      = "denied"
	QuotaFreshness       = 5 * time.Minute
	TrialLeaseDuration   = 60 * time.Second
	InitialTrialBackoff  = 15 * time.Minute
	MaxTrialBackoff      = 6 * time.Hour
	IncidentActiveBudget = 2 * time.Minute
)

var ErrNoCapacity = errors.New("no eligible account capacity; wait for new evidence or owner availability check")

type ScopeState struct {
	Model               string     `json:"model,omitempty"`
	UsedPercent         *int       `json:"used_percent,omitempty"`
	ResetAt             *time.Time `json:"reset_at,omitempty"`
	ObservedAt          time.Time  `json:"observed_at,omitempty"`
	AuthorityObservedAt time.Time  `json:"authority_observed_at,omitempty"`
	UsageObservedAt     time.Time  `json:"usage_observed_at,omitempty"`
	Allowed             bool       `json:"allowed,omitempty"`
	DenialRevision      uint64     `json:"denial_revision"`
	Denied              bool       `json:"denied,omitempty"`
	CooldownUntil       *time.Time `json:"cooldown_until,omitempty"`
	NextTrialAt         *time.Time `json:"next_trial_at,omitempty"`
	TrialBackoffNanos   int64      `json:"trial_backoff_nanos,omitempty"`
	NegativeEventIDs    []string   `json:"negative_event_ids,omitempty"`
}

type ModelException struct {
	GlobalDenialRevision uint64            `json:"global_denial_revision"`
	ObservedAt           time.Time         `json:"observed_at"`
	ScopeDenialRevisions map[string]uint64 `json:"scope_denial_revisions,omitempty"`
}

type QuotaState struct {
	FeedGeneration    uint64                    `json:"feed_generation,omitempty"`
	LastSequence      uint64                    `json:"last_sequence,omitempty"`
	FullRefreshNeeded bool                      `json:"full_refresh_needed,omitempty"`
	Scopes            map[string]ScopeState     `json:"scopes,omitempty"`
	ModelExceptions   map[string]ModelException `json:"model_exceptions,omitempty"`
}

type ScopeObservation struct {
	Scope        string
	Model        string
	UsedPercent  *int
	ResetPresent bool // true with ResetAt nil is an explicit unknown reset
	ResetAt      *time.Time
	Authority    string
}

// ProbeStamp captures denial barriers at operation admission, before I/O. A
// late positive result has clearing authority only while these still match.
type ProbeStamp struct {
	Binding         Binding           `json:"binding"`
	FeedGeneration  uint64            `json:"feed_generation"`
	DenialRevisions map[string]uint64 `json:"denial_revisions"`
}

type Observation struct {
	FeedGeneration uint64
	Sequence       uint64 // local receive sequence when native source has no sequence
	EventID        string // provider operation/sequence identity, if available
	ReceivedAt     time.Time
	Full           bool
	Scopes         []ScopeObservation
	Probe          *ProbeStamp
	Terminal       bool
	WillRetry      bool
}

func validScope(scope string) bool {
	return scope == ScopeGlobal || (len(scope) > 6 && len(scope) <= 256 && (strings.HasPrefix(scope, "model:") || strings.HasPrefix(scope, "bucket:")) && !strings.ContainsAny(scope, "\x00\r\n"))
}

func (q QuotaState) validate() error {
	if len(q.Scopes) > 128 || len(q.ModelExceptions) > 128 {
		return ErrIneligible
	}
	for key, scope := range q.Scopes {
		if !validScope(key) || len(scope.Model) > 256 || (scope.UsedPercent != nil && (*scope.UsedPercent < 0 || *scope.UsedPercent > 100)) || scope.TrialBackoffNanos < 0 || scope.TrialBackoffNanos > int64(MaxTrialBackoff) || len(scope.NegativeEventIDs) > 16 {
			return ErrIneligible
		}
		for _, id := range scope.NegativeEventIDs {
			if len(id) > 256 {
				return ErrIneligible
			}
		}
	}
	return nil
}

func cloneQuota(q QuotaState) QuotaState {
	copy := q
	copy.Scopes = make(map[string]ScopeState, len(q.Scopes))
	for key, scope := range q.Scopes {
		if scope.UsedPercent != nil {
			value := *scope.UsedPercent
			scope.UsedPercent = &value
		}
		if scope.ResetAt != nil {
			value := *scope.ResetAt
			scope.ResetAt = &value
		}
		if scope.CooldownUntil != nil {
			value := *scope.CooldownUntil
			scope.CooldownUntil = &value
		}
		if scope.NextTrialAt != nil {
			value := *scope.NextTrialAt
			scope.NextTrialAt = &value
		}
		scope.NegativeEventIDs = append([]string(nil), scope.NegativeEventIDs...)
		copy.Scopes[key] = scope
	}
	copy.ModelExceptions = make(map[string]ModelException, len(q.ModelExceptions))
	for key, exception := range q.ModelExceptions {
		if exception.ScopeDenialRevisions != nil {
			copyRevisions := make(map[string]uint64, len(exception.ScopeDenialRevisions))
			for scope, revision := range exception.ScopeDenialRevisions {
				copyRevisions[scope] = revision
			}
			exception.ScopeDenialRevisions = copyRevisions
		}
		copy.ModelExceptions[key] = exception
	}
	return copy
}

func CaptureProbe(binding Binding, q QuotaState, scopes []string) ProbeStamp {
	stamp := ProbeStamp{Binding: binding, FeedGeneration: q.FeedGeneration, DenialRevisions: make(map[string]uint64, len(scopes))}
	for _, key := range scopes {
		stamp.DenialRevisions[key] = q.Scopes[key].DenialRevision
	}
	return stamp
}

// ReduceQuota updates telemetry independently from latched denial. A missing,
// null, stale or uncorrelated positive value cannot restore denied capacity.
func ReduceQuota(q QuotaState, o Observation, now time.Time) (QuotaState, error) {
	if err := q.validate(); err != nil {
		return q, err
	}
	if o.FeedGeneration == 0 || o.Sequence == 0 || len(o.Scopes) == 0 || len(o.Scopes) > 128 || len(o.EventID) > 256 || o.ReceivedAt.IsZero() || o.ReceivedAt.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || o.ReceivedAt.After(now.Add(time.Minute)) {
		return q, ErrIneligible
	}
	seen := make(map[string]bool)
	for _, update := range o.Scopes {
		if !validScope(update.Scope) || seen[update.Scope] || len(update.Model) > 256 || (update.UsedPercent != nil && (*update.UsedPercent < 0 || *update.UsedPercent > 100)) || (update.Authority != "" && update.Authority != AuthorityUnknown && update.Authority != AuthorityAllowed && update.Authority != AuthorityDenied) {
			return q, ErrIneligible
		}
		if update.Scope == ScopeGlobal && update.Model != "" {
			return q, ErrIneligible
		}
		if update.ResetAt != nil && (update.ResetAt.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || update.ResetAt.After(now.Add(366*24*time.Hour))) {
			return q, ErrIneligible
		}
		seen[update.Scope] = true
	}
	q = cloneQuota(q)
	if o.FeedGeneration < q.FeedGeneration || (o.FeedGeneration == q.FeedGeneration && o.Sequence <= q.LastSequence) {
		return q, nil
	}
	if o.FeedGeneration != q.FeedGeneration {
		q.FeedGeneration = o.FeedGeneration
		q.LastSequence = 0
		q.FullRefreshNeeded = true
	}
	q.LastSequence = o.Sequence
	if o.Full {
		q.FullRefreshNeeded = false
	}
	for _, update := range o.Scopes {
		scope := q.Scopes[update.Scope]
		if scope.AuthorityObservedAt.IsZero() && scope.Allowed {
			scope.AuthorityObservedAt = scope.ObservedAt
		}
		if scope.UsageObservedAt.IsZero() && scope.UsedPercent != nil {
			scope.UsageObservedAt = scope.ObservedAt
		}
		if scope.Model != "" && update.Model != "" && scope.Model != update.Model {
			return q, ErrIneligible
		}
		if update.Model != "" {
			scope.Model = update.Model
		}
		if strings.HasPrefix(update.Scope, "model:") {
			scope.Model = strings.TrimPrefix(update.Scope, "model:")
		}
		if update.UsedPercent != nil {
			value := *update.UsedPercent
			scope.UsedPercent = &value
			scope.UsageObservedAt = o.ReceivedAt
		}
		if update.ResetPresent {
			scope.ResetAt = update.ResetAt
		}
		scope.ObservedAt = o.ReceivedAt
		switch update.Authority {
		case AuthorityDenied:
			duplicate := false
			if o.EventID != "" {
				for _, id := range scope.NegativeEventIDs {
					duplicate = duplicate || id == o.EventID
				}
			}
			if !duplicate {
				if scope.DenialRevision == ^uint64(0) {
					return q, ErrIneligible
				}
				scope.DenialRevision++
				if o.EventID != "" {
					scope.NegativeEventIDs = append(scope.NegativeEventIDs, o.EventID)
					if len(scope.NegativeEventIDs) > 16 {
						scope.NegativeEventIDs = scope.NegativeEventIDs[1:]
					}
				}
			}
			scope.Denied = true
			scope.Allowed = false
			scope.AuthorityObservedAt = o.ReceivedAt
			if update.ResetAt != nil && update.ResetAt.After(now) {
				if scope.CooldownUntil == nil || update.ResetAt.After(*scope.CooldownUntil) {
					value := *update.ResetAt
					scope.CooldownUntil = &value
				}
			}
			if scope.TrialBackoffNanos == 0 {
				scope.TrialBackoffNanos = int64(InitialTrialBackoff)
			}
			if scope.CooldownUntil == nil && (scope.NextTrialAt == nil || !duplicate) {
				deadline := now.Add(time.Duration(scope.TrialBackoffNanos))
				scope.NextTrialAt = &deadline
			}
		case AuthorityAllowed:
			captured, covered := uint64(0), false
			if o.Probe != nil && o.Probe.FeedGeneration == o.FeedGeneration {
				captured, covered = o.Probe.DenialRevisions[update.Scope]
			}
			if !q.FullRefreshNeeded && ((!scope.Denied && scope.DenialRevision == 0) || (covered && captured == scope.DenialRevision)) {
				scope.Allowed = true
				scope.AuthorityObservedAt = o.ReceivedAt
				if covered && captured == scope.DenialRevision {
					scope.Denied = false
					scope.CooldownUntil = nil
					scope.NextTrialAt = nil
					scope.TrialBackoffNanos = 0
				}
			}
		}
		q.Scopes[update.Scope] = scope
	}
	return q, nil
}

func (s *Store) Observe(expected uint64, b Binding, observation Observation) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		a, g, err := bindingRecord(*r, b)
		if err != nil || a.CurrentGeneration != b.CredentialGeneration || g.Identity == "" {
			return ErrIneligible
		}
		if observation.Probe != nil && observation.Probe.Binding != b {
			return ErrIneligible
		}
		quota, err := ReduceQuota(a.Quota, observation, time.Now())
		if err != nil {
			return err
		}
		a.Quota = quota
		r.Accounts[a.ID] = a
		return nil
	})
}

type TrialLease struct {
	ID       string    `json:"id"`
	Binding  Binding   `json:"binding"`
	Model    string    `json:"model"`
	Deadline time.Time `json:"deadline"`
}

type SelectionRequest struct {
	Provider                string
	Model                   string
	ConfigurationGeneration uint64
	Current                 *Binding
	Failed                  bool
	ManualPinned            bool
	TriedAccounts           map[string]bool
	ActiveCounts            map[string]int
	Trials                  []TrialLease
}

type Selection struct {
	Binding Binding
	Sticky  bool
	Unknown bool
}

func relevantScopes(q QuotaState, model string) []string {
	keys := make([]string, 0, len(q.Scopes))
	for key, scope := range q.Scopes {
		if scope.Model == "" || scope.Model == model {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func quotaEligibility(q QuotaState, model string, now time.Time) (allowed, unknown bool, utilization *int) {
	allowed = true
	keys := relevantScopes(q, model)
	unknown = len(keys) == 0 || q.FullRefreshNeeded
	for _, key := range keys {
		scope := q.Scopes[key]
		if scope.Denied {
			exception, ok := q.ModelExceptions[model]
			captured, covered := exception.ScopeDenialRevisions[key]
			if key == ScopeGlobal && !covered {
				captured, covered = exception.GlobalDenialRevision, true
			}
			if !ok || !covered || captured != scope.DenialRevision {
				return false, false, nil
			}
			if !fresh(exception.ObservedAt, now) {
				unknown = true
			}
			continue
		}
		authorityAt := scope.AuthorityObservedAt
		if authorityAt.IsZero() {
			authorityAt = scope.ObservedAt
		}
		if !scope.Allowed || !fresh(authorityAt, now) {
			unknown = true
		}
		usageAt := scope.UsageObservedAt
		if usageAt.IsZero() {
			usageAt = scope.ObservedAt
		}
		if scope.UsedPercent != nil && fresh(usageAt, now) && (utilization == nil || *scope.UsedPercent > *utilization) {
			value := *scope.UsedPercent
			utilization = &value
		}
	}
	return allowed, unknown, utilization
}

func fresh(observed, now time.Time) bool {
	return !observed.IsZero() && !observed.After(now.Add(time.Minute)) && now.Sub(observed) <= QuotaFreshness
}

// Select is pure. The recovery authority must durably reserve the selection,
// then revalidate its registry revision/binding before any claim or spawn.
func Select(r Registry, request SelectionRequest, now time.Time) (Selection, error) {
	return selectAccount(r, request, now, false)
}

// SelectInitial assigns a fresh discussion before the native CLI resolves its
// default model. Unresolved capacity stays unknown; a known denial cannot be
// bypassed by omitting --model. Recovery must keep using the strict Select.
func SelectInitial(r Registry, request SelectionRequest, now time.Time) (Selection, error) {
	if request.Current != nil || request.Failed || len(request.TriedAccounts) != 0 {
		return Selection{}, ErrIneligible
	}
	return selectAccount(r, request, now, true)
}

func selectAccount(r Registry, request SelectionRequest, now time.Time, initial bool) (Selection, error) {
	if !validProvider(request.Provider) || (request.Model == "" && !initial) || request.ConfigurationGeneration == 0 {
		return Selection{}, ErrIneligible
	}
	if request.Current != nil && !request.Failed {
		a, g, err := bindingRecord(r, *request.Current)
		if err == nil && a.Provider == request.Provider && !g.CredentialErased {
			return Selection{Binding: *request.Current, Sticky: true}, nil
		}
	}
	if request.ManualPinned || !r.Enabled[request.Provider] {
		return Selection{}, ErrNoCapacity
	}
	type candidate struct {
		selection   Selection
		utilization *int
		count       int
	}
	candidates := make([]candidate, 0, len(r.Accounts))
	for _, a := range r.Accounts {
		if a.Provider != request.Provider || a.Lifecycle != LifecycleEnabled || a.Auth != AuthValid || request.TriedAccounts[a.ID] {
			continue
		}
		g := a.Generations[a.CurrentGeneration]
		if g.Kind != KindNative || g.Identity == "" || g.Verification == VerificationUnverified || g.CredentialErased || g.CredentialErasing {
			continue
		}
		usable, unknown, utilization := quotaEligibility(a.Quota, request.Model, now)
		if request.Model == "" {
			unknown = true
			for _, scope := range a.Quota.Scopes {
				if scope.Denied {
					usable = false
				}
			}
		}
		if !usable {
			continue
		}
		count := request.ActiveCounts[a.ID]
		occupied := false
		for _, lease := range request.Trials {
			if lease.Binding.AccountID == a.ID && (request.Model == "" || lease.Model == request.Model) && lease.Deadline.After(now) {
				count++
				occupied = true
			}
		}
		if unknown && occupied {
			continue
		}
		b := Binding{SchemaVersion: SchemaVersion, Provider: a.Provider, AccountID: a.ID, CredentialGeneration: g.Number, Identity: g.Identity, ConfigurationGeneration: request.ConfigurationGeneration}
		candidates = append(candidates, candidate{selection: Selection{Binding: b, Unknown: unknown}, utilization: utilization, count: count})
	}
	if len(candidates) == 0 {
		return Selection{}, ErrNoCapacity
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.selection.Unknown != b.selection.Unknown {
			return !a.selection.Unknown
		}
		if (a.utilization == nil) != (b.utilization == nil) {
			return a.utilization != nil
		}
		if a.utilization != nil && b.utilization != nil && *a.utilization != *b.utilization {
			return *a.utilization < *b.utilization
		}
		if a.count != b.count {
			return a.count < b.count
		}
		return a.selection.Binding.AccountID < b.selection.Binding.AccountID
	})
	return candidates[0].selection, nil
}

// Incident belongs in the existing serialized recovery journal. Reauth changes
// neither logical tried-account accounting nor actual-spawn/active-time budget.
type Incident struct {
	ID                   string          `json:"id"`
	Provider             string          `json:"provider"`
	Model                string          `json:"model"`
	TriedAccounts        map[string]bool `json:"tried_accounts"`
	SpawnCount           int             `json:"spawn_count"`
	SpawnLimit           int             `json:"spawn_limit"`
	RemainingActiveNanos int64           `json:"remaining_active_nanos"`
	Exhausted            bool            `json:"exhausted"`
}

func NewIncident(id, provider, model string, initialPoolSize int) Incident {
	if initialPoolSize < 0 {
		initialPoolSize = 0
	}
	if initialPoolSize > 3 {
		initialPoolSize = 3
	}
	return Incident{ID: id, Provider: provider, Model: model, TriedAccounts: make(map[string]bool), SpawnLimit: initialPoolSize, RemainingActiveNanos: int64(IncidentActiveBudget), Exhausted: initialPoolSize == 0}
}

func (i *Incident) ReserveAccount(id string) error {
	if !validID(id) || i.Exhausted || i.TriedAccounts[id] || i.SpawnCount >= i.SpawnLimit || i.RemainingActiveNanos <= 0 {
		return ErrNoCapacity
	}
	if i.TriedAccounts == nil {
		i.TriedAccounts = make(map[string]bool)
	}
	i.TriedAccounts[id] = true
	return nil
}

func (i *Incident) RecordSpawn(id string) error {
	if !i.TriedAccounts[id] || i.Exhausted || i.SpawnCount >= i.SpawnLimit || i.RemainingActiveNanos <= 0 {
		i.Exhausted = true
		return ErrNoCapacity
	}
	i.SpawnCount++
	return nil
}

func (i *Incident) SpendActive(elapsed time.Duration) error {
	if elapsed < 0 {
		return ErrIneligible
	}
	i.RemainingActiveNanos -= int64(elapsed)
	if i.RemainingActiveNanos <= 0 {
		i.RemainingActiveNanos = 0
		i.Exhausted = true
		return ErrNoCapacity
	}
	return nil
}

// HalfOpenPermit is persisted by the owner before an explicit availability
// request. It authorizes one attempt, never performs a native call itself.
type HalfOpenPermit struct {
	OperationID     string     `json:"operation_id"`
	OwnerRequested  bool       `json:"owner_requested"`
	Stamp           ProbeStamp `json:"stamp"`
	Model           string     `json:"model"`
	Deadline        time.Time  `json:"deadline"`
	Spent           bool       `json:"spent"`
	WorkerPID       int        `json:"worker_pid,omitempty"`
	WorkerPGID      int        `json:"worker_pgid,omitempty"`
	WorkerStartTime int64      `json:"worker_start_time,omitempty"`
}

// CompleteAvailability writes an authenticated owner check result only against
// the frozen generation and denial revisions captured before its single exec.
func (s *Store) CompleteAvailability(expected uint64, binding Binding, permit HalfOpenPermit, success, globalPermission bool) (Registry, error) {
	return s.mutate(expected, func(registry *Registry) error {
		account, generation, err := bindingRecord(*registry, binding)
		if err != nil || account.CurrentGeneration != binding.CredentialGeneration || generation.Kind != KindNative || permit.Stamp.Binding != binding {
			return ErrIneligible
		}
		quota, err := CompleteHalfOpen(account.Quota, permit, permit.OperationID, success, globalPermission, time.Now())
		if err != nil {
			return err
		}
		account.Quota = quota
		registry.Accounts[account.ID] = account
		return nil
	})
}

func AcquireHalfOpen(binding Binding, q QuotaState, model, operationID string, ownerRequested bool, now time.Time, occupied *HalfOpenPermit) (HalfOpenPermit, error) {
	if !ownerRequested || operationID == "" || model == "" || binding.Identity == "" || (occupied != nil && occupied.Deadline.After(now)) {
		return HalfOpenPermit{}, ErrIneligible
	}
	scopes := relevantScopes(q, model)
	denied := false
	for _, key := range scopes {
		scope := q.Scopes[key]
		if !scope.Denied {
			continue
		}
		denied = true
		if (scope.CooldownUntil != nil && scope.CooldownUntil.After(now)) || (scope.NextTrialAt != nil && scope.NextTrialAt.After(now)) {
			return HalfOpenPermit{}, ErrNoCapacity
		}
	}
	if !denied {
		return HalfOpenPermit{}, ErrIneligible
	}
	return HalfOpenPermit{OperationID: operationID, OwnerRequested: true, Stamp: CaptureProbe(binding, q, scopes), Model: model, Deadline: now.Add(time.Minute)}, nil
}

func (p *HalfOpenPermit) Spend(binding Binding, q QuotaState, now time.Time) error {
	if p.Spent || !p.OwnerRequested || p.Stamp.Binding != binding || !p.Deadline.After(now) || p.Stamp.FeedGeneration != q.FeedGeneration {
		return ErrIneligible
	}
	for key, captured := range p.Stamp.DenialRevisions {
		if q.Scopes[key].DenialRevision != captured {
			return ErrIneligible
		}
	}
	p.Spent = true
	return nil
}

// CompleteHalfOpen consumes only this matching spent operation. Failure retains
// denial and doubles local retry backoff; time passing never clears a denial.
// Success without global permission creates a model-only global exception.
func CompleteHalfOpen(q QuotaState, p HalfOpenPermit, operationID string, success, globalPermission bool, now time.Time) (QuotaState, error) {
	if !p.Spent || p.OperationID != operationID || (!p.Deadline.After(now) && success) || p.Stamp.FeedGeneration != q.FeedGeneration {
		return q, ErrIneligible
	}
	for key, revision := range p.Stamp.DenialRevisions {
		if q.Scopes[key].DenialRevision != revision {
			return q, ErrIneligible
		}
	}
	q = cloneQuota(q)
	for key := range p.Stamp.DenialRevisions {
		scope := q.Scopes[key]
		if success {
			if scope.Model == "" && scope.Denied && !globalPermission {
				exception := q.ModelExceptions[p.Model]
				exception.ObservedAt = now
				if exception.ScopeDenialRevisions == nil {
					exception.ScopeDenialRevisions = make(map[string]uint64)
				}
				exception.ScopeDenialRevisions[key] = scope.DenialRevision
				if key == ScopeGlobal {
					exception.GlobalDenialRevision = scope.DenialRevision
				}
				q.ModelExceptions[p.Model] = exception
			} else {
				scope.Denied = false
				scope.Allowed = true
				scope.CooldownUntil = nil
				scope.NextTrialAt = nil
				scope.TrialBackoffNanos = 0
				scope.ObservedAt = now
				scope.AuthorityObservedAt = now
			}
		} else if scope.Denied {
			backoff := time.Duration(scope.TrialBackoffNanos)
			if backoff < InitialTrialBackoff {
				backoff = InitialTrialBackoff
			} else {
				backoff *= 2
			}
			if backoff > MaxTrialBackoff {
				backoff = MaxTrialBackoff
			}
			scope.TrialBackoffNanos = int64(backoff)
			deadline := now.Add(backoff)
			scope.NextTrialAt = &deadline
		}
		q.Scopes[key] = scope
	}
	return q, nil
}

// RequireQuotaRefresh invalidates telemetry after a physical feed gap without
// clearing any denial, cooldown, exception revision or incident accounting.
func (s *Store) RequireQuotaRefresh(expected uint64, b Binding) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		a, _, err := bindingRecord(*r, b)
		if err != nil || a.CurrentGeneration != b.CredentialGeneration || a.Quota.FeedGeneration == ^uint64(0) {
			return ErrIneligible
		}
		a.Quota.FeedGeneration++
		a.Quota.LastSequence = 0
		a.Quota.FullRefreshNeeded = true
		r.Accounts[a.ID] = a
		return nil
	})
}
