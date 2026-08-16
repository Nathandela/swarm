package remotegw

// WakeRetryScheduler is PG-OBL-9's retry driver (bd agents-tracker-hggx.4.3): the
// timer-driven loop that re-Drives one address's live wake obligation after a retryable
// failure, independent of triggers and relay redials. Before it existed, a retryable
// submit failure was retried only when a NEW trigger or a redial happened to call Drive
// again -- so one transient blip on an idle machine lost the wake until restart, the
// bead's named residual.
//
// The shape, pinned by r3pl_obl9_backoff_test.go's frozen contract:
//
//   - Kick drives the obligation NOW (synchronously, via Machine.Drive), then arms
//     exactly one timer for the next attempt when the outcome was retryable.
//   - The delay doubles per consecutive retryable failure, starting from BaseDelay
//     (exponential backoff).
//   - A gateway refusal carrying Retry-After is honoured as a FLOOR: the next attempt
//     is scheduled no earlier than it, whatever the backoff says (spec §6.4's "honour
//     Retry-After" row; WakeSubmitError.RetryAfter is where the submitter parks it).
//   - The whole retry is bounded by the obligation's OWN expiry, never by an attempt
//     count: the scheduler itself never counts attempts against a cap -- a timer that
//     fires past expires_at simply hits Drive's own expiry branch, which marks the
//     obligation expired WITHOUT submitting, and the terminal state is what stops the
//     scheduling.
//
// It deliberately follows the package's existing seams -- Now like every machine, After
// like PushConfig.After -- and adds no dependency. Like PushConfig.After's timer, an
// armed retry is never cancelled: at most one is outstanding per scheduler, each fire is
// one bounded submit attempt, and the obligation's five-minute expiry bounds the whole
// series, so a scheduler abandoned at shutdown leaks a strictly bounded tail rather
// than needing a lifecycle this package's timers do not otherwise have.

import (
	"context"
	"errors"
	"sync"
	"time"
)

// defaultWakeRetryBaseDelay is the first-retry backoff when WakeRetryConfig.BaseDelay
// is zero. One second doubles to the obligation's whole five-minute expiry inside nine
// attempts -- prompt at first, polite to a struggling gateway by the end.
const defaultWakeRetryBaseDelay = time.Second

// WakeRetryConfig configures one address's wake-obligation retry scheduler (PG-OBL-9):
// the timer-driven driver that re-Drives a live obligation after a retryable failure,
// independent of triggers and redials.
type WakeRetryConfig struct {
	Machine   *WakeObligationMachine
	Store     ObligationStore
	Address   PushAddress
	Now       func() time.Time                // nil => time.Now
	After     func(d time.Duration, f func()) // nil => time.AfterFunc; the same deterministic timer seam as PushConfig.After
	BaseDelay time.Duration                   // first-retry backoff (0 => default)
	// Prefs, when set, is re-read before EVERY drive this scheduler performs -- the
	// PB-PUSH-8 at-send re-read for the redrive/retry paths, which otherwise carry a
	// provisional obligation's authorization frozen at trigger time across crashes and
	// backoff waits. Push fully off (every category disabled) durably supersedes the
	// live obligation instead of driving it; an unreadable preference fails closed (no
	// drive) and retries. Nil skips the gate: the live trigger path is already gated at
	// the notifier, and test harnesses drive the machine directly.
	Prefs PushPrefsSource
}

// WakeRetryScheduler drives one address's live wake obligation to a terminal state on
// its own timer. See the file header for the contract.
type WakeRetryScheduler struct {
	cfg   WakeRetryConfig
	after func(d time.Duration, f func())

	mu       sync.Mutex
	armed    bool // exactly one retry timer outstanding at a time
	failures int  // consecutive retryable failures; doubles the delay, reset on terminal
	stopped  bool // Stop was called: armed timers fire as no-ops, ending the retry tail
}

// The scheduler is a drop-in gatewayObligationDriver (and superseder, and provisional
// triggerer), so service.go can wire it AS the TransportRouter's gateway arm: every
// live trigger's Drive then arms the retry loop, not only the startup redrive. Pinned
// so a dropped method is a compile error.
var (
	_ gatewayObligationDriver     = (*WakeRetryScheduler)(nil)
	_ gatewayObligationSuperseder = (*WakeRetryScheduler)(nil)
	_ gatewayProvisionalTriggerer = (*WakeRetryScheduler)(nil)
)

// NewWakeRetryScheduler returns a scheduler for cfg.Address's obligation machine.
func NewWakeRetryScheduler(cfg WakeRetryConfig) *WakeRetryScheduler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = defaultWakeRetryBaseDelay
	}
	after := cfg.After
	if after == nil {
		after = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	return &WakeRetryScheduler{cfg: cfg, after: after}
}

// Kick drives the address's live obligation now and schedules the retry tail.
func (s *WakeRetryScheduler) Kick(ctx context.Context) { _ = s.drive(ctx) }

// Trigger delegates to the machine, satisfying gatewayObligationDriver.
func (s *WakeRetryScheduler) Trigger() error { return s.cfg.Machine.Trigger() }

// TriggerProvisional delegates to the machine, so the deferred-wake pre-append's
// identity report (TransportRouter.PreAppendProvisionalObligation) reaches through
// this wrapper unchanged.
func (s *WakeRetryScheduler) TriggerProvisional() (uint64, error) {
	return s.cfg.Machine.TriggerProvisional()
}

// Drive is Kick with the machine's own Drive error surfaced, so a caller that reports
// push-path failures (TransportRouter -> PushNotifier.Err) still sees them.
func (s *WakeRetryScheduler) Drive(ctx context.Context) error { return s.drive(ctx) }

// Supersede delegates to the machine, so the deferred-wake cancellation path
// (TransportRouter.SupersedeObligation) reaches through this wrapper unchanged. An
// armed retry timer for a superseded obligation is left to fire: it finds a terminal
// record, submits nothing, and stops -- the same self-healing no-op as any other
// terminal outcome.
func (s *WakeRetryScheduler) Supersede(wakeSeq uint64, ownAppends int, reason string) error {
	return s.cfg.Machine.Supersede(wakeSeq, ownAppends, reason)
}

// Stop ends the retry tail: armed timers still fire (they are never cancelled, per the
// file header) but as no-ops, so a Service that has shut down cannot keep submitting
// wakes for up to WakeV1Expiry afterwards -- nor race the NEXT generation's scheduler,
// which redrives any still-live obligation itself (PG-OBL-8). Synchronous Trigger/Drive
// calls are unaffected; a stopped scheduler simply schedules nothing further.
func (s *WakeRetryScheduler) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

// drive performs one gated Drive (driveOnce) and, when the obligation is STILL LIVE
// afterwards (every live post-Drive state means the outcome was retryable -- terminal
// states are how delivery, expiry, abandonment and supersession all present) OR the
// store could not even be read, arms exactly one timer for the next attempt. A read
// error must arm rather than end the series: a transient Get failure after a retryable
// submit would otherwise silently strand a live pending obligation until an unrelated
// trigger or a restart -- precisely the residual PG-OBL-9 exists to close.
func (s *WakeRetryScheduler) drive(ctx context.Context) error {
	driveErr := s.driveOnce(ctx)

	ob, ok, err := s.cfg.Store.Get(s.cfg.Address)
	if err == nil && (!ok || !ob.nonTerminal()) {
		// Terminal or absent: the retry horizon is over. Reset the backoff so the NEXT
		// obligation minted for this address starts from BaseDelay again -- consecutive
		// failures, not lifetime failures, are what double the delay. `armed` is
		// deliberately NOT touched: only the timer's own callback clears it, so a
		// still-outstanding timer (a concurrent Kick reached this terminal state while
		// one was pending) keeps blocking a second arm until it fires its no-op.
		s.mu.Lock()
		s.failures = 0
		s.mu.Unlock()
		return driveErr
	}
	if err != nil {
		driveErr = errors.Join(driveErr, err)
	}

	s.mu.Lock()
	if s.armed {
		// A retry timer is already outstanding (a concurrent Kick raced this one); it
		// will re-drive and re-schedule on its own. Never double-arm -- and never count
		// this call against the backoff curve either: `failures` is only advanced by the
		// call that actually arms, so a concurrent Kick cannot inflate the next delay.
		s.mu.Unlock()
		return driveErr
	}
	s.failures++
	delay := s.cfg.BaseDelay
	// Doubling by loop rather than by shift: the count is unbounded (expiry, not
	// attempts, is the cap) and a shift past 62 would overflow. WakeV1Expiry is a
	// natural ceiling -- any delay at or beyond it fires into Drive's expiry branch
	// anyway, which is exactly the bound PG-OBL-9 wants.
	for i := 1; i < s.failures && delay < WakeV1Expiry; i++ {
		delay *= 2
	}
	var wse *WakeSubmitError
	if errors.As(driveErr, &wse) && wse.RetryAfter > delay {
		delay = wse.RetryAfter
	}
	s.armed = true
	s.mu.Unlock()

	s.after(delay, func() {
		s.mu.Lock()
		s.armed = false
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}
		// context.Background, deliberately: the ctx a trigger handed Kick/Drive is
		// routinely cancelled the moment that call returns (push.go's send() wraps its
		// PushTrigger in a 5s timeout and cancels on exit), and a retry driven under a
		// dead context would fail as a transport error forever. The submit itself stays
		// bounded regardless: HTTPWakeSubmitter's default client carries its own 30s
		// timeout, the obligation's expiry bounds the series, and Stop ends the tail
		// when the owning Service shuts down.
		_ = s.drive(context.Background())
	})
	return driveErr
}

// driveOnce is one preference-gated Drive (PB-PUSH-8 re-read at send, for the paths
// with no notifier in front of them: startup redrive and this scheduler's own timers).
// Push fully off durably supersedes the live obligation -- every category disabled
// suppresses every coalesced trigger, whichever preference once authorized it -- and
// an unreadable preference fails closed without driving (the caller's arm logic
// retries it). With any category still enabled the wake may go out: a coalesced
// obligation does not record which categories authorized its triggers, so this is the
// documented per-category residual (docs/verification/r3-red/obligations-red.txt),
// not an oversight.
func (s *WakeRetryScheduler) driveOnce(ctx context.Context) error {
	if s.cfg.Prefs != nil {
		prefs, err := s.cfg.Prefs.LoadPrefs()
		if err != nil {
			return err
		}
		if !prefs.NeedsInput && !prefs.Finished {
			return s.cfg.Machine.SupersedeAll(wakeOutcomePreferenceSuppressed)
		}
	}
	return s.cfg.Machine.Drive(ctx)
}
