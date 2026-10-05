package skeleton

import (
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
)

// Admission happens before callback acknowledgement. Pending native transcript
// writes therefore cannot hide newer input, including across daemon restart.
func (m *accountRotationManager) admitClaudePromptLocked(input accountInboxRecord) {
	if input.Binding.Provider != accounts.ProviderClaude || input.Kind != "claude-turn" || !input.Started {
		return
	}
	if m.inboxPrompts == nil {
		m.inboxPrompts = make(map[string]accountInboxRecord)
	}
	old, exists := m.inboxPrompts[input.Local]
	if !exists || old.Binding != input.Binding || old.ShimPID != input.ShimPID || old.ShimStartTime != input.ShimStartTime || input.Sequence > old.Sequence {
		m.inboxPrompts[input.Local] = input
	}
}

func (m *accountRotationManager) latestClaudePromptLocked(local string, binding accounts.Binding) (string, uint64) {
	observed := m.w.state.AccountModels[local]
	prompt, sequence := "", uint64(0)
	if observed.Binding == binding {
		prompt, sequence = observed.PromptID, observed.PromptSequence
	}
	if input, ok := m.inboxPrompts[local]; ok && input.Binding == binding && input.Sequence > sequence {
		prompt, sequence = input.TurnID, input.Sequence
	}
	return prompt, sequence
}

func (m *accountRotationManager) claudeFailureSuperseded(input accountInboxRecord) bool {
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	prompt, sequence := m.latestClaudePromptLocked(input.Local, input.Binding)
	return sequence != 0 && (sequence >= input.Sequence || prompt != input.TurnID)
}

func (m *accountRotationManager) claudeFailureAdmitted(input accountInboxRecord) bool {
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	prompt, sequence := m.latestClaudePromptLocked(input.Local, input.Binding)
	return adapter.IsCanonicalConversationID(prompt) && prompt == input.TurnID && sequence != 0 && sequence < input.Sequence
}

func (m *accountRotationManager) currentClaudeFailureLocked(rec accountRotationRecord) bool {
	prompt, sequence := m.latestClaudePromptLocked(rec.SourceID, rec.SourceBinding)
	return !m.inboxFatal && !m.inboxErrors[rec.SourceID] && !m.inboxHolds[rec.SourceID] && adapter.IsCanonicalConversationID(rec.FailedPromptID) && rec.FailedPromptID == prompt && sequence != 0 && sequence < rec.FailedPromptSequence
}

func (m *accountRotationManager) currentClaudeFailure(rec accountRotationRecord) bool {
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	return m.currentClaudeFailureLocked(rec)
}

// Serialize the last prompt check with durable claim and process stop. Native
// hook admission cannot acknowledge newer input inside that critical section.
func (m *accountRotationManager) withClaudeFailureFence(rec accountRotationRecord, stop func() error) error {
	if !rec.NativeClaudeFailure {
		return stop()
	}
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	if !m.currentClaudeFailureLocked(rec) {
		return errAuthRecycleUnsafe
	}
	return stop()
}

func (m *accountRotationManager) persistClaudePrompt(meta persist.Meta, input accountInboxRecord) error {
	old, exists := m.w.state.AccountModels[meta.ID]
	if exists && old.Binding == input.Binding && input.Sequence <= old.PromptSequence {
		return nil
	}
	if m.w.state.AccountModels == nil {
		m.w.state.AccountModels = make(map[string]accountModelRecord)
	}
	next := old
	if next.Binding != input.Binding {
		next = accountModelRecord{Binding: input.Binding}
	}
	next.PromptID, next.PromptSequence = input.TurnID, input.Sequence
	schema := m.w.state.AccountSchemaVersion
	m.w.state.AccountSchemaVersion = accounts.RecoverySchemaVersion
	m.w.state.AccountModels[meta.ID] = next
	visible, err := m.w.persistState()
	if err != nil && !visible {
		m.w.state.AccountSchemaVersion = schema
		if exists {
			m.w.state.AccountModels[meta.ID] = old
		} else {
			delete(m.w.state.AccountModels, meta.ID)
		}
	}
	return err
}
