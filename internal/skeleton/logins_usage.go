package skeleton

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

type loginUsageSnapshot struct {
	runtime            logins.Runtime
	loginID            string
	credentialRevision uint64
	fiveHour, weekly   *protocol.LoginUsageWindow
}

func nativeUsageWindow(used float64, reset int64, observed, now time.Time) *protocol.LoginUsageWindow {
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 || reset > now.Add(8*24*time.Hour).Unix() {
		return nil
	}
	w := &protocol.LoginUsageWindow{RemainingPercent: int(math.Floor(100 - used)), ObservedAt: observed, ResetsAt: time.Unix(reset, 0)}
	if !w.Fresh(now) {
		return nil
	}
	return w
}

// Only the selected generation is retained; switching or restarting starts unknown.
func (m *loginManager) recordLoginUsage(meta persist.Meta, fiveHour, weekly *protocol.LoginUsageWindow) {
	if m == nil || meta.LoginRuntime == nil || !managedLoginProvider(meta.AgentType) || fiveHour == nil && weekly == nil {
		return
	}
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	r, err := m.store.Snapshot()
	if err != nil || r.MigrationPending {
		return
	}
	if _, switching := r.Switches[meta.AgentType]; switching {
		return
	}
	p := r.Providers[meta.AgentType]
	l, ok := loginByID(r, p.ActiveID)
	want := logins.Runtime{Provider: meta.AgentType, Epoch: p.Epoch, Identity: l.Identity, Home: m.store.Home(meta.AgentType)}
	if !ok || l.AuthState != logins.AuthReady || *meta.LoginRuntime != want {
		return
	}
	s := &m.usage[loginProviderIndex(meta.AgentType)]
	if s.runtime != want || s.loginID != l.ID || s.credentialRevision != l.CredentialRevision {
		*s = loginUsageSnapshot{runtime: want, loginID: l.ID, credentialRevision: l.CredentialRevision}
	}
	if meta.AgentType == logins.ProviderClaude && fiveHour != nil && (s.fiveHour == nil || fiveHour.ObservedAt.After(s.fiveHour.ObservedAt)) {
		s.fiveHour = fiveHour
	}
	if weekly != nil && (s.weekly == nil || weekly.ObservedAt.After(s.weekly.ObservedAt)) {
		s.weekly = weekly
	}
}

func (m *loginManager) loginUsage(r logins.Registry, l logins.Login, now time.Time) (fiveHour, weekly *protocol.LoginUsageWindow) {
	if !managedLoginProvider(l.Provider) || r.MigrationPending || l.AuthState != logins.AuthReady {
		return nil, nil
	}
	if _, switching := r.Switches[l.Provider]; switching {
		return nil, nil
	}
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	s := m.usage[loginProviderIndex(l.Provider)]
	p := r.Providers[l.Provider]
	if p.ActiveID != l.ID || s.loginID != l.ID || s.credentialRevision != l.CredentialRevision || s.runtime != (logins.Runtime{Provider: l.Provider, Epoch: p.Epoch, Identity: l.Identity, Home: m.store.Home(l.Provider)}) {
		return nil, nil
	}
	if l.Provider == logins.ProviderClaude && s.fiveHour.Fresh(now) {
		copy := *s.fiveHour
		fiveHour = &copy
	}
	if s.weekly.Fresh(now) {
		copy := *s.weekly
		weekly = &copy
	}
	return fiveHour, weekly
}

// Called only behind the native backend's instance/feed fence.
func (m *loginManager) observeCodexUsage(local, method string, raw []byte, observed time.Time) {
	if m == nil || method != codexRateLimitsUpdated || len(raw) > 1<<20 || rejectDuplicateJSONKeys(raw) != nil {
		return
	}
	meta, ok := m.get(local)
	if !ok || meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil {
		return
	}
	type window struct {
		Used    *float64 `json:"usedPercent"`
		Minutes int      `json:"windowDurationMins"`
		Reset   int64    `json:"resetsAt"`
	}
	var envelope struct {
		Params struct {
			Limits struct {
				ID                 string `json:"limitId"`
				Primary, Secondary *window
			} `json:"rateLimits"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Params.Limits.ID != "" && envelope.Params.Limits.ID != "codex" {
		return
	}
	var weekly *protocol.LoginUsageWindow
	for _, w := range []*window{envelope.Params.Limits.Primary, envelope.Params.Limits.Secondary} {
		if w == nil || w.Minutes != 7*24*60 {
			continue
		}
		if weekly != nil || w.Used == nil {
			return // Conflicting weekly buckets are not a reliable reading.
		}
		weekly = nativeUsageWindow(*w.Used, w.Reset, observed, time.Now())
		if weekly == nil {
			return
		}
	}
	m.recordLoginUsage(meta, nil, weekly)
}

func (m *loginManager) observeClaudeUsage(cb engine.Callback) {
	meta, ok := m.get(cb.SessionID)
	if !ok || meta.AgentType != logins.ProviderClaude || meta.LoginRuntime == nil || meta.ConversationID == "" || cb.Payload["session_id"] != meta.ConversationID {
		return
	}
	observed, err := time.Parse(time.RFC3339Nano, cb.Payload["observed_at"])
	if err != nil {
		return
	}
	window := func(name string) *protocol.LoginUsageWindow {
		used, e := strconv.ParseFloat(cb.Payload[name+"_used_percentage"], 64)
		reset, re := strconv.ParseInt(cb.Payload[name+"_resets_at"], 10, 64)
		if e != nil || re != nil {
			return nil
		}
		return nativeUsageWindow(used, reset, observed, time.Now())
	}
	m.recordLoginUsage(meta, window("five_hour"), window("seven_day"))
}
