package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/protocol"
)

type nativeQuotaWindow struct {
	UsedPercent *int   `json:"usedPercent"`
	ResetsAt    *int64 `json:"resetsAt"`
}
type nativeQuotaSnapshot struct {
	LimitID   string             `json:"limitId"`
	Model     string             `json:"normalModelSlug"`
	Primary   *nativeQuotaWindow `json:"primary"`
	Secondary *nativeQuotaWindow `json:"secondary"`
	Reached   *string            `json:"rateLimitReachedType"`
	Spend     *bool              `json:"spendControlReached"`
}
type nativeQuotaReply struct {
	AccountID  *string                        `json:"accountId"`
	Allowed    *bool                          `json:"ordinaryUsageAllowed"`
	RateLimits *nativeQuotaSnapshot           `json:"rateLimits"`
	ByLimit    map[string]nativeQuotaSnapshot `json:"rateLimitsByLimitId"`
}

func normalizeNativeQuota(raw []byte) ([]accounts.ScopeObservation, error) {
	if len(raw) > 1<<20 || rejectDuplicateJSONKeys(raw) != nil {
		return nil, accounts.ErrIneligible
	}
	var reply nativeQuotaReply
	if json.Unmarshal(raw, &reply) != nil || reply.RateLimits == nil || len(reply.ByLimit) > 32 {
		return nil, accounts.ErrIneligible
	}
	authority := accounts.AuthorityUnknown
	if reply.Allowed != nil {
		if *reply.Allowed {
			authority = accounts.AuthorityAllowed
		} else {
			authority = accounts.AuthorityDenied
		}
	}
	updates := []accounts.ScopeObservation{{Scope: accounts.ScopeGlobal, Authority: authority}}
	seen := map[string]bool{accounts.ScopeGlobal: true}
	appendSnapshot := func(snapshot nativeQuotaSnapshot, key string) error {
		if len(key) > 180 || len(snapshot.Model) > 128 || strings.ContainsAny(key+snapshot.Model, "\x00\r\n") {
			return accounts.ErrIneligible
		}
		for _, window := range []struct {
			name  string
			value *nativeQuotaWindow
		}{{"primary", snapshot.Primary}, {"secondary", snapshot.Secondary}} {
			if window.value == nil {
				continue
			}
			if window.value.UsedPercent == nil || *window.value.UsedPercent < 0 || *window.value.UsedPercent > 100 {
				return accounts.ErrIneligible
			}
			scope := "bucket:" + key + ":" + window.name
			if seen[scope] {
				continue
			}
			seen[scope] = true
			update := accounts.ScopeObservation{Scope: scope, Model: snapshot.Model, UsedPercent: window.value.UsedPercent, ResetPresent: true, Authority: accounts.AuthorityUnknown}
			if window.value.ResetsAt != nil {
				reset := time.Unix(*window.value.ResetsAt, 0)
				update.ResetAt = &reset
			}
			if *window.value.UsedPercent == 100 || snapshot.Reached != nil || snapshot.Spend != nil && *snapshot.Spend {
				update.Authority = accounts.AuthorityDenied
			} else if reply.Allowed != nil && *reply.Allowed {
				update.Authority = accounts.AuthorityAllowed
			}
			updates = append(updates, update)
		}
		return nil
	}
	key := reply.RateLimits.LimitID
	if key == "" {
		key = "ordinary"
	}
	if err := appendSnapshot(*reply.RateLimits, key); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(reply.ByLimit))
	for key := range reply.ByLimit {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := appendSnapshot(reply.ByLimit[key], key); err != nil {
			return nil, err
		}
	}
	return updates, nil
}

// Admission persists normalized evidence without waiting for the recovery owner.
// The caller proves this exact feed under the backend replacement fence.
func (m *accountRotationManager) NoteNativeFrame(local, instance string, feed *backendFeed, method string, raw []byte, at time.Time) error {
	if m == nil || feed == nil || feed.retired.Load() || len(raw) > 1<<20 || (method != "account/rateLimits/updated" && method != "error" && method != "turn/completed" && method != "turn/started") {
		return nil
	}
	meta, ok := m.w.get(local)
	if !ok || meta.AccountBinding == nil || meta.AgentType != accounts.ProviderCodex || rejectDuplicateJSONKeys(raw) != nil {
		return nil
	}
	if m.d == nil {
		return nil
	}
	if current, ok := m.d.sessionInstance(local); !ok || current != instance {
		return nil
	}
	if backend, live := m.d.sessionBackendFor(local); live && backend.feed != feed {
		return nil
	}
	rec, ok := m.inboxRecord(local, "native", meta.ConversationID, 0)
	if !ok {
		return nil
	}
	rec.Instance, rec.Feed, rec.ReceivedAt = instance, feed.epoch, at.UTC()
	var envelope struct {
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	if method == "account/rateLimits/updated" {
		updates, err := normalizeNativeQuota(envelope.Params)
		if err != nil {
			return nil
		}
		rec.Scopes = updates
		return m.acceptInbox(rec)
	}
	var params struct {
		ThreadID  string `json:"threadId"`
		TurnID    string `json:"turnId"`
		WillRetry bool   `json:"willRetry"`
		Error     *struct {
			Info json.RawMessage `json:"codexErrorInfo"`
		} `json:"error"`
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Info json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(envelope.Params, &params) != nil || params.ThreadID != meta.ConversationID {
		return nil
	}
	var info json.RawMessage
	if params.Error != nil {
		info = params.Error.Info
	}
	if method == "turn/completed" {
		params.TurnID = params.Turn.ID
		if params.Turn.Error != nil {
			info = params.Turn.Error.Info
		}
	}
	var code string
	_ = json.Unmarshal(info, &code)
	if !params.WillRetry {
		switch code {
		case "usageLimitExceeded":
			rec.Class = "quota"
		case "unauthorized":
			rec.Class = "auth-invalid"
		}
	}
	rec.TurnID = params.TurnID
	if method == "turn/started" {
		rec.TurnID = params.Turn.ID
		rec.Started = true
	}
	rec.Completed = method == "turn/completed" && params.Turn.Status == "completed" && params.Turn.Error == nil
	if rec.Class == "" && !rec.Started && !rec.Completed {
		return nil
	}
	if len(rec.TurnID) > 256 {
		return nil
	}
	return m.acceptInbox(rec)
}

func (m *accountRotationManager) observe(binding accounts.Binding, updates []accounts.ScopeObservation, probe *accounts.ProbeStamp, full bool, event string, at time.Time) error {
	registry, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	a, ok := registry.Accounts[binding.AccountID]
	if !ok {
		return accounts.ErrIneligible
	}
	feed := a.Quota.FeedGeneration
	if feed == 0 {
		feed = 1
	}
	_, err = m.store.Observe(registry.Revision, binding, accounts.Observation{FeedGeneration: feed, Sequence: a.Quota.LastSequence + 1, ReceivedAt: at, Full: full, Scopes: updates, Probe: probe, EventID: event})
	return err
}

func (m *accountRotationManager) completeTrial(local, turnID string) error {
	for _, rec := range m.w.state.AccountRotations {
		if rec.CandidateID != local || !rec.InputReleased || rec.TrialTurnID == "" || rec.TrialTurnID != turnID || (rec.State != accountCommitted && rec.State != accountUnknown) {
			continue
		}
		rec.Trial = nil
		rec.State = accountComplete
		rec.LastError = ""
		if err := m.persist(rec); err != nil {
			return err
		}
	}
	return nil
}

func (m *accountRotationManager) NoteConversation(local, conversation string, raw []byte, sequences ...uint64) error {
	if len(raw) > 1<<20 || !adapter.IsCanonicalConversationID(conversation) {
		return nil
	}
	var body struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Model     string `json:"model"`
	}
	if rejectDuplicateJSONKeys(raw) != nil || json.Unmarshal(raw, &body) != nil || body.AgentID != "" || body.SessionID != conversation {
		return nil
	}
	var sequence uint64
	if len(sequences) > 0 {
		sequence = sequences[0]
	}
	rec, ok := m.inboxRecord(local, "model", conversation, sequence)
	if !ok || rec.Binding.Provider != accounts.ProviderClaude {
		return nil
	}
	if exactAccountModel(body.Model) {
		rec.Model = body.Model
	}
	return m.acceptInbox(rec)
}

func (m *accountRotationManager) NoteClaudeFailure(cb engine.Callback) error {
	var body struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Error     string `json:"error"`
	}
	if len(cb.Raw) > 1<<20 || rejectDuplicateJSONKeys(cb.Raw) != nil || json.Unmarshal(cb.Raw, &body) != nil || body.AgentID != "" {
		return nil
	}
	class := ""
	switch body.Error {
	case "rate_limit":
		class = "quota"
	case "authentication_failed":
		class = "auth-invalid"
	default:
		return nil
	}
	rec, ok := m.inboxRecord(cb.SessionID, "failure", body.SessionID, cb.Sequence)
	if !ok || rec.Binding.Provider != accounts.ProviderClaude {
		return nil
	}
	rec.Class = class
	return m.acceptInbox(rec)
}

// Refresh uses a live, bound app-server and performs no model request. It does
// not start a CLI for a stopped discussion or borrow another account's backend.
func (m *accountRotationManager) Refresh(accountID string) error {
	m.refreshMu.Lock()
	if _, busy := m.refreshing[accountID]; busy {
		m.refreshMu.Unlock()
		return accounts.ErrInUse
	}
	if time.Since(m.refreshAt[accountID]) < 2*time.Second {
		m.refreshMu.Unlock()
		return accounts.ErrInUse
	}
	m.refreshing[accountID] = make(chan struct{})
	m.refreshAt[accountID] = time.Now()
	m.refreshMu.Unlock()
	defer func() { m.refreshMu.Lock(); delete(m.refreshing, accountID); m.refreshMu.Unlock() }()
	registry, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	account, ok := registry.Accounts[accountID]
	if !ok || account.Provider != accounts.ProviderCodex {
		return accounts.ErrIneligible
	}
	var local string
	var binding accounts.Binding
	var backend *sessionBackend
	for _, meta := range m.w.list() {
		if meta.AccountBinding != nil && meta.AccountBinding.AccountID == accountID && meta.AccountBinding.CredentialGeneration == account.CurrentGeneration {
			if live, ok := m.d.sessionBackendFor(meta.ID); ok {
				local = meta.ID
				binding = *meta.AccountBinding
				backend = live
				break
			}
		}
	}
	if local == "" {
		return protocol.ErrAccountsUnavailable
	}
	scopes := []string{accounts.ScopeGlobal}
	for scope := range account.Quota.Scopes {
		if scope != accounts.ScopeGlobal {
			scopes = append(scopes, scope)
		}
	}
	quota := account.Quota
	if quota.FeedGeneration == 0 {
		quota.FeedGeneration = 1
	}
	stamp := accounts.CaptureProbe(binding, quota, scopes)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err = backend.conn.Call(ctx, "account/rateLimits/read", map[string]any{}, &raw); err != nil {
		return protocol.ErrAccountsUnavailable
	}
	updates, err := normalizeNativeQuota(raw)
	if err != nil {
		return err
	}
	var reply nativeQuotaReply
	_ = json.Unmarshal(raw, &reply)
	if reply.AccountID != nil {
		auth, _ := json.Marshal(map[string]any{"tokens": map[string]string{"account_id": *reply.AccountID}})
		identity, err := accounts.CodexIdentity(auth)
		if err != nil || identity != binding.Identity {
			return accounts.ErrIdentityChanged
		}
	}
	for _, update := range updates {
		if _, known := stamp.DenialRevisions[update.Scope]; !known {
			stamp.DenialRevisions[update.Scope] = 0
		}
	}
	return m.submit(func(w *authWatcher) error {
		current, ok := m.d.sessionBackendFor(local)
		if !ok || current.feed != backend.feed || current.sessionInstance != backend.sessionInstance {
			return accounts.ErrIneligible
		}
		return m.observe(binding, updates, &stamp, true, "", w.clock())
	})
}

var errAccountAvailabilityUnsafe = errors.New("account availability check cannot safely represent this native installation; use native recovery")

func (m *accountRotationManager) beginTrial(local, turnID string) error {
	if turnID == "" || len(turnID) > 256 {
		return nil
	}
	for _, rec := range m.w.state.AccountRotations {
		if rec.CandidateID == local && rec.InputReleased && (rec.State == accountCommitted || rec.State == accountUnknown) {
			rec.TrialTurnID = turnID
			if err := m.persist(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// Claude root hooks delimit a new owner-submitted turn. Replay, subagent Stop,
// failed StopFailure and stop-hook recursion cannot close a recovery incident.
func (m *accountRotationManager) NoteClaudeTurn(cb engine.Callback) error {
	if cb.Event != "UserPromptSubmit" && cb.Event != "Stop" {
		return nil
	}
	var body struct {
		SessionID      string `json:"session_id"`
		AgentID        string `json:"agent_id"`
		Prompt         string `json:"prompt"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if len(cb.Raw) > 1<<20 || rejectDuplicateJSONKeys(cb.Raw) != nil || json.Unmarshal(cb.Raw, &body) != nil || body.AgentID != "" || body.StopHookActive {
		return nil
	}
	ad, found := registry.New(accounts.ProviderClaude)
	if !found {
		return nil
	}
	shaper, ok := adapter.AsInteractionSource(ad)
	if !ok {
		return nil
	}
	rec, ok := m.inboxRecord(cb.SessionID, "claude-turn", body.SessionID, cb.Sequence)
	if !ok || rec.Binding.Provider != accounts.ProviderClaude {
		return nil
	}
	rec.Started = cb.Event == "UserPromptSubmit"
	rec.ClearModel = rec.Started && strings.HasPrefix(strings.TrimSpace(body.Prompt), "/model")
	for _, item := range shaper.Interactions(adapter.HookPayload{Event: cb.Event, Raw: cb.Raw}) {
		if item.Kind == adapter.KindUserMessage && item.Source == adapter.SourceOwner && !strings.HasPrefix(strings.TrimSpace(item.Text), "/") {
			rec.OwnerPrompt = true
		}
		if item.Kind == adapter.KindAgentMessage && item.Status == adapter.StatusCompleted && strings.TrimSpace(item.Text) != "" {
			rec.Completed = true
		}
	}
	return m.acceptInbox(rec)
}
