package skeleton

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/accountusage"
	"github.com/Nathandela/swarm/internal/protocol"
)

type quotaUsageSource func(context.Context, *accounts.Store, accounts.Binding) (accountusage.Result, error)

type accountQuotaFetchState struct {
	generation                      uint64
	serial                          uint64
	state, message                  string
	lastAttempt, next, blockedUntil time.Time
	backoff                         time.Duration
	cancel                          context.CancelFunc
	labels                          map[string]string
}

type accountQuotaRequest struct {
	id                 string
	generation, serial uint64
}

// All bookkeeping is protected by accountManager.mu; workers perform only
// bounded reads outside that lock. Fetch status is transient, existing quota
// usage timestamps remain durable and native event authority stays separate.
type accountQuotaFetcher struct {
	m      *accountManager
	source quotaUsageSource
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	queue  chan accountQuotaRequest
	items  map[string]*accountQuotaFetchState
	active map[accountQuotaRequest]context.CancelFunc
	serial uint64
}

func (m *accountManager) startQuotaFetching() { m.startQuotaFetchingWith(accountusage.Fetch) }

func (m *accountManager) startQuotaFetchingWith(source quotaUsageSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.quota != nil || m.unavailable != nil || m.store == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &accountQuotaFetcher{m: m, source: source, ctx: ctx, cancel: cancel, queue: make(chan accountQuotaRequest, 256), items: make(map[string]*accountQuotaFetchState), active: make(map[accountQuotaRequest]context.CancelFunc)}
	m.quota = q
	q.wg.Add(3)
	for i := 0; i < 2; i++ {
		go q.worker()
	}
	go func() {
		defer q.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				if m.unavailable == nil {
					if r, err := m.store.Snapshot(); err == nil {
						q.scheduleLocked(r, time.Now())
					}
				}
				m.mu.Unlock()
			}
		}
	}()
	if r, err := m.store.Snapshot(); err == nil {
		q.scheduleLocked(r, time.Now())
	}
}

func quotaFetchEligible(a accounts.Account) bool {
	g := a.Generations[a.CurrentGeneration]
	return a.Auth == accounts.AuthValid && a.Lifecycle != accounts.LifecycleRetiring && g.Kind == accounts.KindNative && g.Source == accounts.SourceNativeLogin && g.Identity != "" && !g.CredentialErased && !g.CredentialErasing
}

func (q *accountQuotaFetcher) stateLocked(a accounts.Account, now time.Time) *accountQuotaFetchState {
	s := q.items[a.ID]
	if s != nil && s.generation == a.CurrentGeneration {
		return s
	}
	if s != nil && s.cancel != nil {
		s.cancel()
	}
	generationChanged := s != nil
	s = &accountQuotaFetchState{generation: a.CurrentGeneration, state: "idle"}
	// A daemon restart can reuse a still fresh durable usage reading.
	var oldest time.Time
	missingAge := false
	for _, scope := range a.Quota.Scopes {
		at := quotaUsageObservedAt(scope)
		if scope.UsedPercent != nil {
			if at.IsZero() || at.After(now.Add(time.Minute)) || (scope.ResetAt != nil && !scope.ResetAt.After(now)) {
				missingAge = true
			} else if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
		}
	}
	if !oldest.IsZero() {
		s.state = "ready"
		if !missingAge && !generationChanged {
			s.next = oldest.Add(accounts.QuotaFreshness)
		}
	}
	q.items[a.ID] = s
	return s
}

func (q *accountQuotaFetcher) requestLocked(a accounts.Account, now time.Time, manual bool) error {
	if !quotaFetchEligible(a) {
		return accounts.ErrIneligible
	}
	if q.ctx.Err() != nil {
		return protocol.ErrAccountsUnavailable
	}
	s := q.stateLocked(a, now)
	if s.state == "loading" || now.Before(s.blockedUntil) || (!manual && now.Before(s.next)) {
		return nil
	}
	q.serial++
	req := accountQuotaRequest{id: a.ID, generation: a.CurrentGeneration, serial: q.serial}
	select {
	case q.queue <- req:
		s.serial = req.serial
		s.state = "loading"
		s.message = ""
		s.next = time.Time{}
		return nil
	default:
		return accounts.ErrInUse
	}
}

func (q *accountQuotaFetcher) scheduleLocked(r accounts.Registry, now time.Time) {
	for id, s := range q.items {
		a, exists := r.Accounts[id]
		if !exists || !quotaFetchEligible(a) {
			if s.cancel != nil {
				s.cancel()
			}
			delete(q.items, id)
		}
	}
	for _, a := range r.Accounts {
		if quotaFetchEligible(a) {
			_ = q.requestLocked(a, now, false)
		}
	}
}

func (q *accountQuotaFetcher) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case request := <-q.queue:
			q.fetch(request)
		}
	}
}

func (q *accountQuotaFetcher) matchesLocked(req accountQuotaRequest) (*accountQuotaFetchState, bool) {
	s := q.items[req.id]
	return s, s != nil && s.generation == req.generation && s.serial == req.serial && s.state == "loading"
}

func (q *accountQuotaFetcher) fetch(req accountQuotaRequest) {
	m := q.m
	m.mu.Lock()
	s, ok := q.matchesLocked(req)
	if !ok || q.ctx.Err() != nil || m.unavailable != nil {
		m.mu.Unlock()
		return
	}
	r, err := m.store.Snapshot()
	a := r.Accounts[req.id]
	if err != nil || !quotaFetchEligible(a) || a.CurrentGeneration != req.generation {
		delete(q.items, req.id)
		m.mu.Unlock()
		return
	}
	g := a.Generations[a.CurrentGeneration]
	binding := accounts.Binding{SchemaVersion: accounts.SchemaVersion, Provider: a.Provider, AccountID: a.ID, CredentialGeneration: a.CurrentGeneration, Identity: g.Identity, ConfigurationGeneration: 1}
	baseline := a.Quota
	ctx, cancel := context.WithTimeout(q.ctx, 10*time.Second)
	s.cancel = cancel
	q.active[req] = cancel
	s.lastAttempt = time.Now()
	m.mu.Unlock()
	result, fetchErr := q.source(ctx, m.store, binding)
	cancel()
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(q.active, req)
	s, ok = q.matchesLocked(req)
	if !ok || q.ctx.Err() != nil || m.unavailable != nil {
		return
	}
	s.cancel = nil
	if fetchErr == nil {
		// Unrelated labels/admissions may change the CAS revision during I/O.
		// RecordUsage rejects any actual quota/binding change against baseline.
		for attempt := 0; attempt < 3; attempt++ {
			r, err = m.store.Snapshot()
			if err != nil {
				break
			}
			_, err = m.store.RecordUsage(r.Revision, binding, baseline, result.Observations, now)
			if !errors.Is(err, accounts.ErrRevisionConflict) {
				break
			}
		}
		fetchErr = err
	}
	if errors.Is(fetchErr, accounts.ErrUsageSuperseded) {
		s.state = "ready"
		s.message = ""
		s.next = now.Add(time.Minute)
		s.blockedUntil = s.next
		return
	}
	if fetchErr == nil {
		s.state = "ready"
		s.message = ""
		s.labels = result.Labels
		s.backoff = 0
		s.next = now.Add(accounts.QuotaFreshness)
		s.blockedUntil = now.Add(30 * time.Second)
		return
	}
	s.state = "error"
	s.message = "Quota refresh unavailable. Try again later."
	if s.backoff == 0 {
		s.backoff = time.Minute
	} else {
		s.backoff *= 2
	}
	if s.backoff > 30*time.Minute {
		s.backoff = 30 * time.Minute
	}
	delay := s.backoff
	var safe *accountusage.Error
	if errors.As(fetchErr, &safe) {
		s.message = safe.Error()
		if safe.RetryAfter > delay {
			delay = safe.RetryAfter
		}
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	s.next = now.Add(delay)
	s.blockedUntil = s.next
}

func quotaUsageObservedAt(scope accounts.ScopeState) time.Time {
	if !scope.UsageObservedAt.IsZero() {
		return scope.UsageObservedAt
	}
	return scope.ObservedAt
}

func quotaScopeLabel(scope string) string {
	switch scope {
	case "bucket:claude:five_hour":
		return "5-hour usage"
	case "bucket:claude:seven_day":
		return "Weekly usage"
	case "bucket:claude:seven_day_opus":
		return "Weekly Opus usage"
	case "bucket:claude:seven_day_sonnet":
		return "Weekly Sonnet usage"
	case "bucket:codex:primary":
		return "Primary usage"
	case "bucket:codex:secondary":
		return "Secondary usage"
	case accounts.ScopeGlobal:
		return "Overall usage"
	default:
		return strings.TrimPrefix(scope, "bucket:")
	}
}

func (q *accountQuotaFetcher) projectLocked(a accounts.Account, view *protocol.AccountView) {
	view.RefreshSupported = quotaFetchEligible(a)
	if !view.RefreshSupported {
		view.QuotaFetchState = "unsupported"
		return
	}
	s := q.items[a.ID]
	if s == nil || s.generation != a.CurrentGeneration {
		view.QuotaFetchState = "idle"
		return
	}
	view.QuotaFetchState = s.state
	view.QuotaFetchError = s.message
	if !s.lastAttempt.IsZero() {
		at := s.lastAttempt
		view.QuotaLastAttemptAt = &at
	}
	if !s.next.IsZero() {
		at := s.next
		view.QuotaNextRefreshAt = &at
	}
	for i := range view.Quota {
		if label := s.labels[view.Quota[i].Label]; label != "" {
			view.Quota[i].Label = label
		} else {
			view.Quota[i].Label = quotaScopeLabel(view.Quota[i].Label)
		}
	}
}
