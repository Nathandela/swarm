package skeleton

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Nathandela/swarm/internal/idempotency"
)

// A bounded assistant page can omit its original user after a long native tool loop.
func (p *openCodeTranscript) recoverOwnedInputs(ctx context.Context, reader openCodeReader, state *OpenCodeState, epoch uint64) error {
	view := state.View()
	if view.Root != p.identity.RootID || view.Generation != p.identity.BackendGeneration || !p.current() {
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
		entry, ok := owned[candidate.MessageID]
		if !ok {
			continue
		}
		operation, ok := p.d.core.ComposerOperation(entry.operation)
		if !ok || operation.Phase == idempotency.PhasePrepared || operation.Phase == idempotency.PhaseFailed {
			continue
		}
		retained, err := readOpenCodeEvidence(operation.Evidence)
		if err != nil || !admissionMatchesIntent(retained.Receipt, entry.operation, entry.intent) {
			continue
		}
		row, err := p.readMessage(ctx, reader, candidate.MessageID)
		if err != nil {
			return err
		}
		observed, err := qualifyOpenCodeAdmission(entry.operation, entry.intent, row)
		if err != nil || observed.Agent != retained.Receipt.Agent || *observed.Model != *retained.Receipt.Model || observed.Variant != retained.Receipt.Variant {
			return errors.New("native recovered user differs from retained admission")
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
