package skeleton

import (
	"encoding/json"
	"log"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
)

const codexRateLimitsUpdated = "account/rateLimits/updated"

type codexQuotaWindow struct {
	UsedPercent *float64 `json:"usedPercent"`
	ResetsAt    *int64   `json:"resetsAt"`
}
type codexQuotaSnapshot struct {
	LimitID      *string           `json:"limitId"`
	Primary      *codexQuotaWindow `json:"primary"`
	Secondary    *codexQuotaWindow `json:"secondary"`
	ReachedType  *string           `json:"rateLimitReachedType"`
	SpendReached *bool             `json:"spendControlReached"`
}
type codexQuotaEvidence struct {
	instance string
	runtime  logins.Runtime
	limits   codexQuotaSnapshot
}

func (q *codexQuotaEvidence) bind(instance string, runtime logins.Runtime) {
	if q.instance != instance || q.runtime != runtime {
		*q = codexQuotaEvidence{instance: instance, runtime: runtime}
	}
}

// v0.163.0's account/rateLimits/updated is sparse. Missing nullable metadata
// cannot clear an earlier rejected window or spend-control reason.
func (q *codexQuotaEvidence) merge(snapshot codexQuotaSnapshot) bool {
	if snapshot.LimitID != nil && *snapshot.LimitID != "" && *snapshot.LimitID != "codex" {
		return false // Another model's bucket cannot explain this thread's failure.
	}
	for _, window := range []*codexQuotaWindow{snapshot.Primary, snapshot.Secondary} {
		if window != nil && (window.UsedPercent == nil || *window.UsedPercent < 0) {
			return false
		}
	}
	if snapshot.Primary != nil {
		q.limits.Primary = snapshot.Primary
	}
	if snapshot.Secondary != nil {
		q.limits.Secondary = snapshot.Secondary
	}
	if snapshot.ReachedType != nil {
		q.limits.ReachedType = snapshot.ReachedType
	}
	if snapshot.SpendReached != nil {
		q.limits.SpendReached = snapshot.SpendReached
	}
	return true
}

func (snapshot codexQuotaSnapshot) reset(now time.Time) time.Time {
	if snapshot.nonRolling() {
		return time.Time{} // Credit/spend rejection is not a rolling quota reset.
	}
	var reset time.Time
	for _, window := range []*codexQuotaWindow{snapshot.Primary, snapshot.Secondary} {
		if window == nil || window.UsedPercent == nil || *window.UsedPercent < 100 {
			continue
		}
		if window.ResetsAt == nil || *window.ResetsAt <= now.Unix() || *window.ResetsAt > now.Add(31*24*time.Hour).Unix() {
			return time.Time{}
		}
		at := time.Unix(*window.ResetsAt, 0)
		if at.After(reset) {
			reset = at
		}
	}
	return reset
}

func (snapshot codexQuotaSnapshot) blocking() bool {
	if snapshot.nonRolling() {
		return true
	}
	for _, window := range []*codexQuotaWindow{snapshot.Primary, snapshot.Secondary} {
		if window != nil && window.UsedPercent != nil && *window.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

func (snapshot codexQuotaSnapshot) nonRolling() bool {
	return snapshot.SpendReached != nil && *snapshot.SpendReached || snapshot.ReachedType != nil && *snapshot.ReachedType != "rate_limit_reached"
}

// The native backend is already authenticated and incarnation-fenced by its
// caller. Persist only a terminal subscription fact, never native error text.
func (m *loginManager) observeNativeFailure(local, instance string, feed *backendFeed, method string, raw []byte) {
	if m == nil || instance == "" || feed == nil || feed.retired.Load() || len(raw) > 1<<20 || (method != "error" && method != "turn/completed" && method != codexRateLimitsUpdated) || rejectDuplicateJSONKeys(raw) != nil {
		return
	}
	meta, ok := m.get(local)
	if !ok || meta.LoginRuntime == nil || meta.AgentType != logins.ProviderCodex {
		return
	}
	if meta.LoginRuntime.ExternalAuth {
		if method == codexRateLimitsUpdated {
			m.noteCodexHint(local, false)
		} else if codexTerminalUsageHint(method, raw) {
			m.noteCodexHint(local, true)
		}
		return
	}

	if method == codexRateLimitsUpdated {
		var envelope struct {
			Params struct {
				Limits *codexQuotaSnapshot `json:"rateLimits"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Params.Limits == nil {
			return
		}
		feed.loginQuotaMu.Lock()
		feed.loginQuota.bind(instance, *meta.LoginRuntime)
		merged := feed.loginQuota.merge(*envelope.Params.Limits)
		now := time.Now()
		reset := feed.loginQuota.limits.reset(now)
		blocking := feed.loginQuota.limits.blocking()
		nonRolling := feed.loginQuota.limits.nonRolling()
		feed.loginQuotaMu.Unlock()
		freshUnknown := envelope.Params.Limits.blocking() && envelope.Params.Limits.reset(now).IsZero()
		if merged && blocking && (!reset.IsZero() || freshUnknown) {
			if err := m.recordExhaustionFact(meta, reset, false, nonRolling); err != nil {
				log.Printf("login: native subscription reset could not be saved; recovery deferred: %v", err)
			}
		}
		return
	}
	var envelope struct {
		Params struct {
			ThreadID  string `json:"threadId"`
			WillRetry *bool  `json:"willRetry"`
			Error     *struct {
				Info string `json:"codexErrorInfo"`
			} `json:"error"`
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Info string `json:"codexErrorInfo"`
				} `json:"error"`
			} `json:"turn"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Params.ThreadID != meta.ConversationID {
		return
	}
	if method == "error" && (envelope.Params.WillRetry == nil || *envelope.Params.WillRetry) {
		return
	}
	if envelope.Params.WillRetry != nil && *envelope.Params.WillRetry {
		return
	}
	code := ""
	if envelope.Params.Error != nil {
		code = envelope.Params.Error.Info
	}
	if method == "turn/completed" {
		if envelope.Params.Turn.Status != "failed" || envelope.Params.Turn.Error == nil {
			return
		}
		code = envelope.Params.Turn.Error.Info
	}
	if code == "unauthorized" {
		if err := m.recordAuthFailure(meta); err != nil {
			log.Printf("login: native sign-in failure could not be saved; recovery deferred: %v", err)
		}
		return
	}
	if code != "usageLimitExceeded" {
		return
	}
	feed.loginQuotaMu.Lock()
	feed.loginQuota.bind(instance, *meta.LoginRuntime)
	reset := feed.loginQuota.limits.reset(time.Now())
	nonRolling := feed.loginQuota.limits.nonRolling()
	feed.loginQuotaMu.Unlock()
	if err := m.recordExhaustionFact(meta, reset, true, nonRolling); err != nil {
		log.Printf("login: native subscription exhaustion could not be saved; switch deferred: %v", err)
	}
}

func codexTerminalUsageHint(method string, raw []byte) bool {
	var envelope struct {
		Params struct {
			WillRetry *bool `json:"willRetry"`
			Error     *struct {
				Info string `json:"codexErrorInfo"`
			} `json:"error"`
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Info string `json:"codexErrorInfo"`
				} `json:"error"`
			} `json:"turn"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Params.WillRetry != nil && *envelope.Params.WillRetry {
		return false
	}
	info := ""
	if method == "error" && envelope.Params.Error != nil {
		info = envelope.Params.Error.Info
	}
	if method == "turn/completed" && envelope.Params.Turn.Status == "failed" && envelope.Params.Turn.Error != nil {
		info = envelope.Params.Turn.Error.Info
	}
	return info == "unauthorized" || info == "usageLimitExceeded"
}
