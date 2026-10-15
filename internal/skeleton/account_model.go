package skeleton

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
)

type accountModelRecord struct {
	Binding        accounts.Binding `json:"binding"`
	Model          string           `json:"model"`
	HookSequence   uint64           `json:"hook_sequence,omitempty"`
	PromptID       string           `json:"prompt_id,omitempty"`
	PromptSequence uint64           `json:"prompt_sequence,omitempty"`
}

func exactAccountModel(model string) bool {
	if model == "" || len(model) > 128 {
		return false
	}
	alias := strings.ToLower(strings.SplitN(strings.SplitN(model, "[", 2)[0], ":", 2)[0])
	switch alias {
	case "opus", "opusplan", "sonnet", "haiku", "auto", "default":
		return false
	}
	for _, c := range model {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("-_.:/[]", c) {
			return false
		}
	}
	return true
}

func (m *accountRotationManager) effectiveModel(meta persist.Meta) string {
	if m.modelInboxHeld(meta.ID) {
		return ""
	}
	if meta.AccountBinding == nil {
		return ""
	}
	// Cached hooks and launch options lack ordering against later CLI /model
	// changes. The latest characterized Codex turn_context takes precedence.
	fallback := meta.LaunchOptions["model"]
	if observed, ok := m.w.state.AccountModels[meta.ID]; ok && observed.Binding == *meta.AccountBinding {
		fallback = observed.Model
	}
	if !exactAccountModel(fallback) {
		fallback = ""
	}
	if meta.AgentType != accounts.ProviderCodex {
		return fallback
	}
	resolver := newAccountResumeHistoryResolver(m.w.stateDir, nil)
	path, outcome := resolver.LocateTranscript(meta, meta.ConversationID)
	if outcome != resumeHistoryFound {
		return fallback
	}
	profile, err := m.store.HistoryProfilePath(*meta.AccountBinding)
	if err != nil {
		return ""
	}
	if meta.AccountProjectionRef != "" {
		nativePath, err := accountconfig.NativeHistoryAuthority(m.w.stateDir, meta.AccountProjectionRef, meta.AgentType, meta.ProviderCwd(), meta.AccountBinding.ConfigurationGeneration)
		if err != nil {
			return ""
		}
		if nativePath != "" {
			profile = nativePath
		}
	}
	model, seen, err := readCodexTranscriptModel(profile, path)
	if err != nil {
		return ""
	}
	if seen {
		return model
	}
	return fallback
}

func (m *accountRotationManager) noteModel(meta persist.Meta, model string, sequences ...uint64) error {
	if meta.AccountBinding == nil {
		return accounts.ErrIneligible
	}
	if m.w.state.AccountModels == nil {
		m.w.state.AccountModels = make(map[string]accountModelRecord)
	}
	previous, existed := m.w.state.AccountModels[meta.ID]
	var sequence uint64
	if len(sequences) > 0 {
		sequence = sequences[0]
	}
	if existed && previous.Binding == *meta.AccountBinding && sequence != 0 && sequence <= previous.HookSequence {
		return nil
	}
	schema := m.w.state.AccountSchemaVersion
	next := accountModelRecord{Binding: *meta.AccountBinding, Model: model, HookSequence: sequence}
	if existed && previous.Binding == next.Binding {
		next.PromptID, next.PromptSequence = previous.PromptID, previous.PromptSequence
	}
	m.w.state.AccountModels[meta.ID] = next
	m.w.state.AccountSchemaVersion = accounts.RecoverySchemaVersion
	visible, err := m.w.persistState()
	if err != nil && !visible {
		m.w.state.AccountSchemaVersion = schema
		if existed {
			m.w.state.AccountModels[meta.ID] = previous
		} else {
			delete(m.w.state.AccountModels, meta.ID)
		}
	}
	return err
}

// Observe immutable account identity on the existing writer's ordinary tick.
// One read per generation covers all its live sessions; token refresh is a no-op.
func (m *accountRotationManager) checkIdentities() {
	if m.store == nil || m.w.stateErr != nil {
		return
	}
	type result struct {
		identity string
		err      error
	}
	observed := make(map[string]result)
	for _, meta := range m.w.list() {
		if meta.AccountBinding == nil || meta.Status.Process != "running" {
			continue
		}
		key := historyProfileKey(*meta.AccountBinding)
		value, known := observed[key]
		if !known {
			value.identity, value.err = m.store.NativeIdentity(*meta.AccountBinding)
			if value.err != nil {
				value.identity, value.err = m.store.NativeIdentity(*meta.AccountBinding)
			}
			observed[key] = value
		}
		// Only a successful read that names another identity is drift. An
		// unreadable profile proves nothing; it is said once and retried.
		readKey := "identity-read:" + meta.ID
		if value.err != nil {
			if !m.w.warned[readKey] {
				m.w.once(readKey, "account identity: cannot read the profile identity of account %s (%v); not treating it as drift, retrying each tick", m.accountLabel(meta.AccountBinding.AccountID), value.err)
			}
			continue
		}
		delete(m.w.warned, readKey)
		if value.identity == meta.AccountBinding.Identity {
			m.releaseIdentityHold(meta)
			continue
		}
		registry, err := m.store.Snapshot()
		if err != nil {
			return
		}
		account, ok := registry.Accounts[meta.AccountBinding.AccountID]
		generation, exists := account.Generations[meta.AccountBinding.CredentialGeneration]
		if !ok || !exists || generation.Kind != accounts.KindNative || generation.CredentialErased || generation.CredentialErasing {
			continue
		}
		if m.handles(meta.ID) {
			active := false
			for _, rec := range m.w.state.AccountRotations {
				if rec.SourceID == meta.ID && rec.IdentityHeld && rec.State != accountOwnerCanceled && rec.State != accountComplete {
					active = true
				}
			}
			if active {
				continue
			}
		}
		_ = m.reportFailure(m.w, meta.ID, "identity-drift", m.effectiveModel(meta), "identity-drift:"+fmtUint(meta.AccountBinding.CredentialGeneration))
	}
}

// releaseIdentityHold undoes what identity drift did once the bound identity
// reads back: the held record, which never claimed the session, is dropped
// with its aliases and input lock, and a needs-login that only drift set
// returns to valid. A later drift starts a fresh incident.
func (m *accountRotationManager) releaseIdentityHold(meta persist.Meta) {
	w := m.w
	for key, rec := range w.state.AccountRotations {
		if rec.SourceID != meta.ID || !rec.IdentityHeld || (rec.State != accountObserved && rec.State != accountBlocked) || w.state.Killed[rec.SourceID] {
			continue
		}
		old := w.state
		w.state.AccountRotations = maps.Clone(old.AccountRotations)
		delete(w.state.AccountRotations, key)
		if visible, err := w.persistState(); err != nil && !visible {
			w.state = old
			return
		}
		m.viewMu.Lock()
		for alias, original := range m.aliases {
			if original == key {
				delete(m.aliases, alias)
			}
		}
		m.viewMu.Unlock()
		if w.clearRecycle != nil {
			w.clearRecycle(rec.SourceID)
		}
	}
	registry, err := m.store.Snapshot()
	if err != nil {
		return
	}
	account := registry.Accounts[meta.AccountBinding.AccountID]
	if account.Auth == accounts.AuthNeedsLogin && account.AuthReason == accounts.AuthReasonDrift && account.CurrentGeneration == meta.AccountBinding.CredentialGeneration {
		if _, err := m.store.SetAuth(registry.Revision, account.ID, accounts.AuthValid); err == nil {
			logAuthChange(account, accounts.AuthValid, "identity-restored")
		}
	}
}

func (m *accountRotationManager) accountLabel(id string) string {
	if registry, err := m.store.Snapshot(); err == nil && registry.Accounts[id].Label != "" {
		return registry.Accounts[id].Label
	}
	return id
}

// noteHold names the job holding local's input lock in refusals.
func (m *accountRotationManager) noteHold(local, reason string) {
	if m.d != nil {
		m.d.noteAuthRecycleReason(local, reason)
	}
}

func (m *accountRotationManager) noteIdentityHold(local, accountID string) {
	if m.d != nil {
		label := m.accountLabel(accountID)
		m.noteHold(local, fmt.Sprintf("is held by the account identity check on %q -- its profile now holds another login; end the session and resume it (sign %q in again from Accounts first if it shows needs-login)", label, label))
	}
}

func readCodexTranscriptModel(profile, path string) (string, bool, error) {
	root, err := historyOpenRoot(profile)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = root.Close() }()
	if !strings.HasPrefix(path, profile+"/") {
		return "", false, errors.New("account native model: invalid transcript origin")
	}
	f, err := historyOpenFile(root, strings.TrimPrefix(path, profile+"/"))
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() > accountHistoryMaxBytes {
		return "", false, errors.New("account native model: transcript limit")
	}
	r := bufio.NewReaderSize(io.LimitReader(f, accountHistoryMaxBytes+1), 64<<10)
	model := ""
	seen := false
	for {
		line, oversized, err := readCappedLine(r, 8<<20)
		if oversized {
			// Tool output lines reach 20 MB; the model lives in small turn_context records.
			// An oversized turn_context could name the newest model, so it still refuses.
			// The head check assumes Codex's compact serde output, which writes "type" as the
			// second key with no spaces. A spaced or reordered turn_context would be skipped.
			if bytes.Contains(line, []byte(`"type":"turn_context"`)) {
				return "", false, errors.New("account native model: unreadable transcript")
			}
		} else {
			var entry struct {
				Type    string `json:"type"`
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "turn_context" {
				seen = true
				model = ""
				if rejectDuplicateJSONKeys(line) == nil && exactAccountModel(entry.Payload.Model) {
					model = entry.Payload.Model
				}
			}
		}
		if err == io.EOF {
			return model, seen, nil
		}
		if err != nil {
			return "", false, errors.New("account native model: unreadable transcript")
		}
	}
}

// readCappedLine reads one line. A line longer than limit is consumed whole but only its first
// 64 KiB is returned, with oversized set, so the caller can classify it without holding it.
func readCappedLine(r *bufio.Reader, limit int) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !oversized {
			if len(line)+len(chunk) > limit {
				oversized = true
				line = line[:min(len(line), 64<<10)]
			} else {
				line = append(line, chunk...)
			}
		}
		if err != bufio.ErrBufferFull {
			return line, oversized, err
		}
	}
}
