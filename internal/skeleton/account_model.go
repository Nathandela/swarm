package skeleton

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
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
		if value.err == nil && value.identity == meta.AccountBinding.Identity {
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
	scanner := bufio.NewScanner(io.LimitReader(f, accountHistoryMaxBytes+1))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	model := ""
	seen := false
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Type == "turn_context" {
			seen = true
			model = ""
			if rejectDuplicateJSONKeys(scanner.Bytes()) == nil && exactAccountModel(entry.Payload.Model) {
				model = entry.Payload.Model
			}
		}
	}
	if scanner.Err() != nil {
		return "", false, errors.New("account native model: unreadable transcript")
	}
	return model, seen, nil
}
