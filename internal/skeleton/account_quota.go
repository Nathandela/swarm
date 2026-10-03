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

// A feed callback never waits for the recovery owner while holding the backend
// replacement fence. The owner rechecks exactly the captured feed before apply.
func (m *accountRotationManager) NoteNativeFrame(local, instance string, feed *backendFeed, method string, raw []byte, at time.Time) {
	if m == nil || len(raw) > 1<<20 || (method != "account/rateLimits/updated" && method != "error" && method != "turn/completed" && method != "turn/started") {
		return
	}
	copyRaw := append([]byte(nil), raw...)
	op := accountOwnerOperation{apply: func(w *authWatcher) error {
		if w.stateErr != nil || feed.retired.Load() {
			return nil
		}
		if current, ok := m.d.sessionInstance(local); !ok || current != instance {
			return nil
		}
		backend, ok := m.d.sessionBackendFor(local)
		if !ok || backend.feed != feed {
			return nil
		}
		meta, ok := w.get(local)
		if !ok || meta.AccountBinding == nil || meta.AgentType != accounts.ProviderCodex {
			return nil
		}
		if rejectDuplicateJSONKeys(copyRaw) != nil {
			return nil
		}
		if m.nativeFeeds == nil {
			m.nativeFeeds = make(map[string]string)
		}
		if m.nativeFeeds[local] != feed.epoch {
			r, err := m.store.Snapshot()
			if err != nil {
				return err
			}
			if _, err = m.store.RequireQuotaRefresh(r.Revision, *meta.AccountBinding); err != nil && !errors.Is(err, accounts.ErrIneligible) {
				return err
			}
			m.nativeFeeds[local] = feed.epoch
		}
		var envelope struct {
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(copyRaw, &envelope) != nil {
			return nil
		}
		if method == "account/rateLimits/updated" {
			updates, err := normalizeNativeQuota(envelope.Params)
			if err != nil {
				return nil
			}
			return m.observe(*meta.AccountBinding, updates, nil, false, "", at)
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
		info := json.RawMessage(nil)
		if params.Error != nil {
			info = params.Error.Info
		}
		if method == "turn/completed" && params.Turn.Error != nil {
			info = params.Turn.Error.Info
			params.TurnID = params.Turn.ID
		}
		var code string
		_ = json.Unmarshal(info, &code)
		class := ""
		switch code {
		case "usageLimitExceeded":
			class = "quota"
		case "unauthorized":
			class = "auth-invalid"
		}
		if class != "" && !params.WillRetry {
			return m.reportFailure(w, local, class, meta.LaunchOptions["model"], params.ThreadID+":"+params.TurnID+":"+code)
		}
		if method == "turn/started" {
			m.beginTrial(local, params.Turn.ID)
		}
		if method == "turn/completed" && params.Turn.Status == "completed" && params.Turn.Error == nil {
			m.completeTrial(local, params.Turn.ID)
		}
		return nil
	}}
	select {
	case m.w.managedOps <- op:
	case <-m.w.stop:
	default: /* A missing observation cannot clear a denial. */
	}
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

func (m *accountRotationManager) completeTrial(local, turnID string) {
	for _, rec := range m.w.state.AccountRotations {
		if rec.CandidateID != local || !rec.InputReleased || rec.TrialTurnID == "" || rec.TrialTurnID != turnID || (rec.State != accountCommitted && rec.State != accountUnknown) {
			continue
		}
		rec.Trial = nil
		rec.State = accountComplete
		rec.LastError = ""
		_ = m.persist(rec)
	}
}

func (m *accountRotationManager) NoteConversation(local, conversation string, raw []byte, sequences ...uint64) {
	if len(raw) > 1<<20 || !adapter.IsCanonicalConversationID(conversation) {
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Model     string `json:"model"`
	}
	if rejectDuplicateJSONKeys(raw) != nil || json.Unmarshal(raw, &body) != nil || body.AgentID != "" || body.SessionID != conversation {
		return
	}
	op := accountOwnerOperation{apply: func(w *authWatcher) error {
		meta, ok := w.get(local)
		if !ok || meta.AccountBinding == nil || meta.AgentType != accounts.ProviderClaude || meta.ConversationID != conversation {
			return nil
		}
		model := body.Model
		if !exactAccountModel(model) {
			model = ""
		}
		if err := m.noteModel(meta, model); err != nil {
			return err
		}
		for _, rec := range w.state.AccountRotations {
			if rec.CandidateID == local && rec.State == accountLaunched && rec.Destination != nil && *meta.AccountBinding == *rec.Destination && rec.ConversationID == conversation && body.Model == rec.Incident.Model {
				rec.ConversationProven = true
				if len(sequences) > 0 {
					rec.NativeHookSequence = sequences[0]
				}
				return m.persist(rec)
			}
		}
		return nil
	}}
	select {
	case m.w.managedOps <- op:
	case <-m.w.stop:
	default:
	}
}

func (m *accountRotationManager) NoteClaudeFailure(cb engine.Callback) {
	var body struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Error     string `json:"error"`
	}
	if len(cb.Raw) > 1<<20 || rejectDuplicateJSONKeys(cb.Raw) != nil || json.Unmarshal(cb.Raw, &body) != nil || body.AgentID != "" {
		return
	}
	class := ""
	switch body.Error {
	case "rate_limit":
		class = "quota"
	case "authentication_failed":
		class = "auth-invalid"
	default:
		return
	}
	op := accountOwnerOperation{apply: func(w *authWatcher) error {
		meta, ok := w.get(cb.SessionID)
		if !ok || meta.AccountBinding == nil || meta.AgentType != accounts.ProviderClaude || meta.ConversationID != body.SessionID {
			return nil
		}
		return m.reportFailure(w, cb.SessionID, class, meta.LaunchOptions["model"], body.SessionID+":"+fmtUint(cb.Sequence)+":"+body.Error)
	}}
	select {
	case m.w.managedOps <- op:
	case <-m.w.stop:
	default:
	}
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

func (m *accountRotationManager) beginTrial(local, turnID string) {
	if turnID == "" || len(turnID) > 256 {
		return
	}
	for _, rec := range m.w.state.AccountRotations {
		if rec.CandidateID == local && rec.InputReleased && (rec.State == accountCommitted || rec.State == accountUnknown) {
			rec.TrialTurnID = turnID
			_ = m.persist(rec)
		}
	}
}

// Claude root hooks delimit a new owner-submitted turn. Replay, subagent Stop,
// failed StopFailure and stop-hook recursion cannot close a recovery incident.
func (m *accountRotationManager) NoteClaudeTurn(cb engine.Callback) {
	if cb.Event != "UserPromptSubmit" && cb.Event != "Stop" {
		return
	}
	var body struct {
		SessionID      string `json:"session_id"`
		AgentID        string `json:"agent_id"`
		Prompt         string `json:"prompt"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if len(cb.Raw) > 1<<20 || rejectDuplicateJSONKeys(cb.Raw) != nil || json.Unmarshal(cb.Raw, &body) != nil || body.AgentID != "" || body.StopHookActive {
		return
	}
	ad, found := registry.New(accounts.ProviderClaude)
	if !found {
		return
	}
	shaper, ok := adapter.AsInteractionSource(ad)
	if !ok {
		return
	}
	ownerPrompt, completed := false, false
	for _, item := range shaper.Interactions(adapter.HookPayload{Event: cb.Event, Raw: cb.Raw}) {
		if item.Kind == adapter.KindUserMessage && item.Source == adapter.SourceOwner && !strings.HasPrefix(strings.TrimSpace(item.Text), "/") {
			ownerPrompt = true
		}
		if item.Kind == adapter.KindAgentMessage && item.Status == adapter.StatusCompleted && strings.TrimSpace(item.Text) != "" {
			completed = true
		}
	}
	op := accountOwnerOperation{apply: func(w *authWatcher) error {
		meta, ok := w.get(cb.SessionID)
		if !ok || meta.AccountBinding == nil || meta.AgentType != accounts.ProviderClaude || meta.ConversationID != body.SessionID {
			return nil
		}
		if cb.Event == "UserPromptSubmit" && strings.HasPrefix(strings.TrimSpace(body.Prompt), "/model") {
			if err := m.noteModel(meta, ""); err != nil {
				return err
			}
		}
		for _, rec := range w.state.AccountRotations {
			if rec.CandidateID != cb.SessionID || !rec.InputReleased || (rec.State != accountCommitted && rec.State != accountUnknown) || cb.Sequence <= rec.NativeHookSequence {
				continue
			}
			if cb.Event == "UserPromptSubmit" {
				if !ownerPrompt {
					rec.TrialTurnID = ""
					rec.TrialHookSequence = 0
					return m.persist(rec)
				}
				rec.TrialHookSequence = cb.Sequence
				rec.TrialTurnID = "claude:" + fmtUint(cb.Sequence)
				return m.persist(rec)
			}
			if completed && cb.Sequence > rec.TrialHookSequence && rec.TrialHookSequence > rec.NativeHookSequence && meta.Status.Turn == "idle" && meta.Status.Interaction == "none" && !w.sessionUnsafe(meta.ID) {
				m.completeTrial(meta.ID, rec.TrialTurnID)
			}
		}
		return nil
	}}
	select {
	case m.w.managedOps <- op:
	case <-m.w.stop:
	default:
	}
}
