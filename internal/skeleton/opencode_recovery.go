package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
)

// A bounded assistant page can omit its original user after a long native tool loop.
func (p *openCodeTranscript) recoverOwnedInputs(ctx context.Context, reader openCodeReader, state *OpenCodeState, epoch uint64) error {
	view := state.View()
	meta, current := p.d.core.Get(p.local)
	if view.Root != p.identity.RootID || view.Generation != p.identity.BackendGeneration || !current || !openCodeMetaMatches(meta, p.identity) {
		return errors.New("native input recovery binding changed")
	}
	lookup := OpenCodeView{Root: view.Root, Generation: view.Generation}
	seen := map[string]bool{}
	add := func(parent string) {
		if parent != "" && state.inputs[parent] == nil && !seen[parent] {
			seen[parent] = true
			lookup.Inputs = append(lookup.Inputs, OpenCodeInput{MessageID: parent})
		}
	}
	for _, message := range view.Messages {
		if message.Role == "assistant" && !message.Summary {
			add(message.ParentID)
		}
	}
	for _, outcome := range view.Uncertain {
		add(outcome.InputID)
	}
	// Candidate count is bounded by the existing message and uncertain-outcome caches.
	owned := p.d.ownedOpenCodeInputs(p.local, lookup)
	for _, candidate := range lookup.Inputs {
		entry, expected := initialOpenCodeRecovery(meta, p.identity, candidate.MessageID)
		needsAdmission := false
		if expected == nil {
			var ok bool
			entry, ok = owned[candidate.MessageID]
			if !ok {
				continue
			}
			operation, ok := p.d.core.ComposerOperation(entry.operation)
			if !ok || operation.Phase == idempotency.PhasePrepared || operation.Phase == idempotency.PhaseFailed {
				continue
			}
			retained, err := readOpenCodeEvidence(operation.Evidence)
			if len(operation.Evidence) > 0 {
				if err != nil || !admissionMatchesIntent(retained.Receipt, entry.operation, entry.intent) {
					continue
				}
				expected = &retained.Receipt
			} else if operation.Phase == idempotency.PhaseExecuting || operation.Phase == idempotency.PhaseOutcomeUnknown {
				needsAdmission = true
			} else {
				continue
			}
		}
		row, err := p.readMessage(ctx, reader, candidate.MessageID)
		if err != nil {
			return err
		}
		observed, err := qualifyOpenCodeAdmission(entry.operation, entry.intent, row)
		if err != nil || expected != nil && (observed.Agent != expected.Agent || *observed.Model != *expected.Model || observed.Variant != expected.Variant) {
			return errors.New("native recovered user differs from owned input")
		}
		if needsAdmission {
			if err := p.d.recordOpenCodeAdmission(entry.operation, entry.intent, observed); err != nil {
				return err
			}
		}
		if err := state.ImportHistory([]json.RawMessage{row}, epoch); err != nil {
			return err
		}
		if err := p.row(row, "reconciled"); err != nil {
			return err
		}
	}
	return nil
}

// Initial admission is held in launch metadata, not in the semantic operation ledger.
func initialOpenCodeRecovery(meta persist.Meta, identity opencodebridge.Identity, messageID string) (openCodeOwnedInput, *opencodebridge.Receipt) {
	b := meta.OpenCodeBinding
	if b == nil || b.Initial == nil || b.Initial.Phase != persist.OpenCodeInitialAdmitted || b.Initial.MessageID != messageID || !openCodeMetaMatches(meta, identity) || !persist.ValidOpenCodeBinding(*b) {
		return openCodeOwnedInput{}, nil
	}
	initial := b.Initial
	provider, model, _ := strings.Cut(initial.Model, "/")
	entry := openCodeOwnedInput{operation: initial.OperationID, intent: openCodeSendIntent{Identity: identity, MessageID: messageID, TextHash: initial.RequestHash, NotBeforeMS: b.NativeCreatedAtMS}}
	expected := &opencodebridge.Receipt{Agent: initial.Agent, Model: &opencodebridge.Model{ProviderID: provider, ModelID: model}, Variant: initial.Variant}
	return entry, expected
}
