package pushgw

import (
	"fmt"
	"sync"
	"time"
)

const maxAddressesPerInstallation = 20

// RateLimit is a fixed-window bucket: at most Max attempts per Window. The zero value
// RateLimit{} is the sentinel "unset -- apply section 9's proposed default" (withDefaults);
// any OTHER value must be a genuinely usable bucket, enforced by validate.
type RateLimit struct {
	Max    int
	Window time.Duration
}

// QuotaConfig is section 9's buckets: PG-Q-1 (wakes), PG-Q-3 (registrations and
// allocations, each bounded per source IP AND globally, allocations also per installation).
//
// DEFERRED (§13.4): section 9's table also proposes "signed requests per installation, 60
// per hour" -- covering PUT .../token and DELETE /v1/addresses/{addr}, neither of which is
// quota'd by any bucket here. §13.4 marks the whole table proposed, not decided, so this is
// a recorded deferral pending the owner's real numbers, not a silent gap; rotate is the one
// authenticated path that performs an unbounded number of bbolt writes per installation.
type QuotaConfig struct {
	WakesPerAddress            RateLimit
	WakesPerSourceIP           RateLimit
	RegistrationsPerSourceIP   RateLimit
	RegistrationsGlobal        RateLimit
	AllocationsPerSourceIP     RateLimit
	AllocationsGlobal          RateLimit
	AllocationsPerInstallation int
}

// withDefaults fills any unset (zero-valued) bucket with section 9's proposed defaults --
// RegistrationsGlobal and AllocationsPerSourceIP/Global have no number in section 9's
// table (only the four original buckets do); the values below are this bead's own
// proposed defaults, in the same "confirm or replace with measurement" spirit, so a
// caller of NewServer need only override the buckets it wants to narrow.
func (q QuotaConfig) withDefaults() QuotaConfig {
	if q.WakesPerAddress == (RateLimit{}) {
		q.WakesPerAddress = RateLimit{Max: 20, Window: 5 * time.Minute}
	}
	if q.WakesPerSourceIP == (RateLimit{}) {
		q.WakesPerSourceIP = RateLimit{Max: 600, Window: time.Hour}
	}
	if q.RegistrationsPerSourceIP == (RateLimit{}) {
		q.RegistrationsPerSourceIP = RateLimit{Max: 10, Window: time.Hour}
	}
	if q.RegistrationsGlobal == (RateLimit{}) {
		q.RegistrationsGlobal = RateLimit{Max: 2000, Window: time.Hour}
	}
	if q.AllocationsPerSourceIP == (RateLimit{}) {
		q.AllocationsPerSourceIP = RateLimit{Max: 40, Window: time.Hour}
	}
	if q.AllocationsGlobal == (RateLimit{}) {
		q.AllocationsGlobal = RateLimit{Max: 4000, Window: time.Hour}
	}
	if q.AllocationsPerInstallation == 0 {
		q.AllocationsPerInstallation = maxAddressesPerInstallation
	}
	return q
}

// validate fails closed on a bucket that would otherwise disable itself at runtime: a
// zero or negative Window resets on every call (quota.go's fixed-window check becomes a
// tautology), and a zero or negative Max either refuses everything or -- under the old
// "Max<=0 means unlimited" reading this replaces -- allows everything. Section 9 is the
// one place the spec asks for a fail-closed default, so an unusable bucket is a boot-time
// config error, never a silent no-op quota. Called after withDefaults, so every bucket
// reaching here is either a spec-proposed default or an operator's explicit override.
func (q QuotaConfig) validate() error {
	for name, rl := range map[string]RateLimit{
		"WakesPerAddress":          q.WakesPerAddress,
		"WakesPerSourceIP":         q.WakesPerSourceIP,
		"RegistrationsPerSourceIP": q.RegistrationsPerSourceIP,
		"RegistrationsGlobal":      q.RegistrationsGlobal,
		"AllocationsPerSourceIP":   q.AllocationsPerSourceIP,
		"AllocationsGlobal":        q.AllocationsGlobal,
	} {
		if rl.Max <= 0 || rl.Window <= 0 {
			return fmt.Errorf("pushgw: quota %s is unusable (Max=%d Window=%s): both must be positive", name, rl.Max, rl.Window)
		}
	}
	if q.AllocationsPerInstallation <= 0 {
		return fmt.Errorf("pushgw: quota AllocationsPerInstallation is unusable (%d): must be positive", q.AllocationsPerInstallation)
	}
	if q.AllocationsPerInstallation > maxAddressesPerInstallation {
		return fmt.Errorf("pushgw: quota AllocationsPerInstallation is %d: maximum is %d", q.AllocationsPerInstallation, maxAddressesPerInstallation)
	}
	return nil
}

// limiter is an in-memory fixed-window rate limiter. Quota counters are not one of
// PG-RET-10's durable stored fields, so keeping them in memory (reset on restart) is the
// contract, not a shortcut.
type limiter struct {
	mu      sync.Mutex
	windows map[string]windowState
}

type windowState struct {
	count  int
	start  time.Time
	expiry time.Time
}

func newLimiter() *limiter {
	return &limiter{windows: make(map[string]windowState)}
}

// allow reports whether one more attempt fits under rl for key at now, consuming it if so.
// On refusal it also returns the number of whole seconds until the window resets, for
// Retry-After. rl.Max/Window <= 0 is no longer read as "unlimited": QuotaConfig.validate
// refuses that shape at boot, so allow fails CLOSED on it instead -- a bucket that somehow
// reaches here unusable refuses every attempt rather than admitting all of them.
func (l *limiter) allow(key string, rl RateLimit, now time.Time) (ok bool, retryAfterSeconds int) {
	if rl.Max <= 0 || rl.Window <= 0 {
		return false, 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, seen := l.windows[key]
	if !seen || !now.Before(st.expiry) {
		st = windowState{count: 0, start: now, expiry: now.Add(rl.Window)}
	}
	if st.count >= rl.Max {
		remaining := st.expiry.Sub(now)
		secs := int(remaining / time.Second)
		if remaining%time.Second != 0 {
			secs++
		}
		if secs < 1 {
			secs = 1
		}
		l.windows[key] = st
		return false, secs
	}
	st.count++
	l.windows[key] = st
	return true, 0
}

// sweep deletes every window whose bound has already lapsed (PG-RET-4 hardening): the key
// space is attacker-influenced (source IP, push address) and reachable pre-authentication
// (registration's per-source bucket), so an unswept map is unbounded memory growth on an
// unauthenticated path. Called from the same periodic retention sweep as the durable
// store's rows (Server.RunRetention), never on its own timer.
func (l *limiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, st := range l.windows {
		if !now.Before(st.expiry) {
			delete(l.windows, k)
		}
	}
}
