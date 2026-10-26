package skeleton

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
)

type openCodeEvidence struct {
	Version       int                    `json:"version"`
	Receipt       opencodebridge.Receipt `json:"receipt"`
	Outcome       OpenCodeOutcome        `json:"outcome,omitempty"`
	OutcomeSource string                 `json:"outcome_source,omitempty"`
	OutcomeAt     int64                  `json:"outcome_at,omitempty"`
}

func nativeSelectionText(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsFunc(value, unicode.IsControl)
}

// The native UI omits its default variant; bridge receipts spell it "default".
func sameOpenCodeVariant(a, b string) bool {
	return a == b || a == "" && b == "default" || a == "default" && b == ""
}

func qualifyOpenCodeAdmission(operationID string, intent openCodeSendIntent, raw json.RawMessage) (opencodebridge.Receipt, error) {
	var row struct {
		Info struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Role      string `json:"role"`
			Agent     string `json:"agent"`
			Variant   string `json:"variant"`
			Model     struct {
				ProviderID string `json:"providerID"`
				ModelID    string `json:"modelID"`
				Variant    string `json:"variant"`
			} `json:"model"`
			Time struct {
				Created int64 `json:"created"`
			} `json:"time"`
		} `json:"info"`
		Parts []struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			Type      string `json:"type"`
			Text      string `json:"text"`
			Synthetic bool   `json:"synthetic"`
			Ignored   bool   `json:"ignored"`
		} `json:"parts"`
	}
	refuse := errors.New("native user admission does not match durable request")
	if len(raw) > opencodebridge.MaxResponseBytes || json.Unmarshal(raw, &row) != nil || row.Info.ID != intent.MessageID || row.Info.SessionID != intent.Identity.RootID || row.Info.Role != "user" || intent.NotBeforeMS <= 0 || row.Info.Time.Created < intent.NotBeforeMS || !nativeSelectionText(row.Info.Agent, 128) || !nativeSelectionText(row.Info.Model.ProviderID, 128) || !nativeSelectionText(row.Info.Model.ModelID, 256) || len(row.Parts) == 0 {
		return opencodebridge.Receipt{}, refuse
	}
	variant := row.Info.Model.Variant
	if variant == "" {
		variant = row.Info.Variant
	} else if row.Info.Variant != "" && row.Info.Variant != variant {
		return opencodebridge.Receipt{}, refuse
	}
	if len(variant) > 128 || strings.ContainsFunc(variant, unicode.IsControl) {
		return opencodebridge.Receipt{}, refuse
	}
	count := 0
	text := ""
	seen := map[string]bool{}
	for _, part := range row.Parts {
		if part.ID == "" || seen[part.ID] || part.SessionID != intent.Identity.RootID || part.MessageID != intent.MessageID {
			return opencodebridge.Receipt{}, refuse
		}
		seen[part.ID] = true
		if part.Synthetic || part.Ignored {
			continue
		}
		if part.Type != "text" {
			return opencodebridge.Receipt{}, refuse
		}
		count++
		text = part.Text
	}
	hash := sha256.Sum256([]byte(text))
	if count != 1 || hex.EncodeToString(hash[:]) != intent.TextHash {
		return opencodebridge.Receipt{}, refuse
	}
	return opencodebridge.Receipt{Identity: intent.Identity, OperationID: operationID, Status: "admitted", Kind: "prompt", TargetID: intent.Identity.RootID, MessageID: intent.MessageID, RequestHash: intent.TextHash, Agent: row.Info.Agent, Model: &opencodebridge.Model{ProviderID: row.Info.Model.ProviderID, ModelID: row.Info.Model.ModelID}, Variant: variant, SelectionBasis: "native_message_receipt", SelectionMessageID: intent.MessageID, NativeEffect: "observed_native_user_message"}, nil
}
func qualifyObservedOpenCodeAdmission(operationID string, intent openCodeSendIntent, input OpenCodeInput) (opencodebridge.Receipt, error) {
	refuse := errors.New("native observed input does not prove admission")
	if input.MessageID != intent.MessageID || !input.PlainTextInput || !input.Classified || input.Synthetic || input.TextTruncated || input.At < intent.NotBeforeMS || !nativeSelectionText(input.Agent, 128) || !nativeSelectionText(input.ProviderID, 128) || !nativeSelectionText(input.ModelID, 256) || len(input.Variant) > 128 || strings.ContainsFunc(input.Variant, unicode.IsControl) {
		return opencodebridge.Receipt{}, refuse
	}
	hash := sha256.Sum256([]byte(input.Text))
	if hex.EncodeToString(hash[:]) != intent.TextHash {
		return opencodebridge.Receipt{}, refuse
	}
	return opencodebridge.Receipt{Identity: intent.Identity, OperationID: operationID, Status: "admitted", Kind: "prompt", TargetID: intent.Identity.RootID, MessageID: intent.MessageID, RequestHash: intent.TextHash, Agent: input.Agent, Model: &opencodebridge.Model{ProviderID: input.ProviderID, ModelID: input.ModelID}, Variant: input.Variant, SelectionBasis: "native_observed_input", SelectionMessageID: intent.MessageID, NativeEffect: "observed_native_user_message"}, nil
}

func validOpenCodeIntent(operationID, local string, intent openCodeSendIntent) bool {
	if intent.Origin != nil && intent.Origin.Validate() != nil {
		return false
	}
	if intent.Source != "" && intent.Source != adapter.SourceOwner && intent.Source != adapter.SourcePhone && intent.Source != "supervisor" {
		return false
	}
	id := intent.Identity
	generation, ge := hex.DecodeString(id.BackendGeneration)
	hash, he := hex.DecodeString(intent.TextHash)
	return id.Version == 1 && id.WorkerID == local && id.NativeCreatedAtMS > 0 && id.NativeCreatedAtMS <= 9007199254740991 && id.SessionInstance == id.BackendGeneration && ge == nil && len(generation) == 16 && strings.ToLower(id.BackendGeneration) == id.BackendGeneration && filepath.IsAbs(id.Directory) && filepath.Clean(id.Directory) == id.Directory && !strings.ContainsFunc(id.Directory, unicode.IsControl) && adapter.IsOpenCodeConversationID(id.RootID) && intent.NotBeforeMS >= id.NativeCreatedAtMS && intent.NotBeforeMS <= 9007199254740991 && he == nil && len(hash) == 32 && strings.ToLower(intent.TextHash) == intent.TextHash && intent.MessageID == openCodeMessageID(operationID, id)
}

func readOpenCodeEvidence(raw []byte) (openCodeEvidence, error) {
	var evidence openCodeEvidence
	if len(raw) == 0 {
		return evidence, nil
	}
	if json.Unmarshal(raw, &evidence) != nil {
		return evidence, errors.New("native evidence unreadable")
	}
	var shape struct {
		Receipt json.RawMessage `json:"receipt"`
	}
	_ = json.Unmarshal(raw, &shape)
	if len(shape.Receipt) == 0 {
		var receipt opencodebridge.Receipt
		if json.Unmarshal(raw, &receipt) != nil {
			return evidence, errors.New("native receipt unreadable")
		}
		evidence = openCodeEvidence{Version: 1, Receipt: receipt}
	}
	if evidence.Version != 1 || evidence.Receipt.Status != "admitted" || evidence.Receipt.Model == nil || !nativeSelectionText(evidence.Receipt.Agent, 128) || !nativeSelectionText(evidence.Receipt.Model.ProviderID, 128) || !nativeSelectionText(evidence.Receipt.Model.ModelID, 256) {
		return evidence, errors.New("native admission proof unavailable")
	}
	if evidence.Outcome != "" && !qualifiedOpenCodeOutcome(evidence.Outcome, evidence.OutcomeSource, evidence.OutcomeAt) {
		return evidence, errors.New("native terminal checkpoint unqualified")
	}
	return evidence, nil
}
func qualifiedOpenCodeOutcome(outcome OpenCodeOutcome, source string, at int64) bool {
	if at <= 0 {
		return false
	}
	if source != OpenCodeOutcomeSourceLive && source != OpenCodeOutcomeSourceImported && source != OpenCodeOutcomeSourceReconciled {
		return false
	}
	switch outcome {
	case OpenCodeOutcomeCompleted, OpenCodeOutcomeFailed, OpenCodeOutcomeCancelled, OpenCodeOutcomeDeclined:
		return true
	}
	return false
}
func admissionMatchesIntent(receipt opencodebridge.Receipt, operationID string, intent openCodeSendIntent) bool {
	return receipt.Status == "admitted" && receipt.Identity == intent.Identity && receipt.OperationID == operationID && receipt.Kind == "prompt" && receipt.TargetID == intent.Identity.RootID && receipt.MessageID == intent.MessageID && receipt.Model != nil && nativeSelectionText(receipt.Agent, 128) && nativeSelectionText(receipt.Model.ProviderID, 128) && nativeSelectionText(receipt.Model.ModelID, 256)
}
func (d *Daemon) recordOpenCodeAdmission(operationID string, intent openCodeSendIntent, receipt opencodebridge.Receipt) error {
	if !admissionMatchesIntent(receipt, operationID, intent) {
		return errors.New("native admission identity conflicts with custody")
	}
	for attempt := 0; attempt < 3; attempt++ {
		rec, ok := d.core.ComposerOperation(operationID)
		if !ok {
			return errors.New("native operation retired")
		}
		if len(rec.Evidence) > 0 {
			prior, err := readOpenCodeEvidence(rec.Evidence)
			if err != nil || !admissionMatchesIntent(prior.Receipt, operationID, intent) || prior.Receipt.Agent != receipt.Agent || *prior.Receipt.Model != *receipt.Model || !sameOpenCodeVariant(prior.Receipt.Variant, receipt.Variant) {
				return errors.New("native admission conflicts with retained receipt")
			}
			return nil
		}
		raw, _ := json.Marshal(openCodeEvidence{Version: 1, Receipt: receipt})
		if err := d.core.ObserveSemanticReceipt(operationID, raw); err == nil {
			return nil
		}
	}
	return errors.New("native admission could not be retained")
}

func (d *Daemon) semanticOperation(local, operationID string) (protocol.SemanticMessageReceipt, error) {
	rec, ok := d.core.ComposerOperation(operationID)
	if !ok || rec.Action != protocol.OpSemanticSend && rec.Action != protocol.ActionComposerSend || rec.SessionID != local {
		return protocol.SemanticMessageReceipt{}, errors.New("semantic operation does not belong to this session")
	}
	result := protocol.SemanticMessageReceipt{OperationID: operationID, Session: local, SessionInstance: rec.SessionInstance, State: "uncertain"}
	switch rec.Phase {
	case idempotency.PhasePrepared:
		result.State = "prepared"
	case idempotency.PhaseFailed:
		result.State = "rejected"
	case idempotency.PhaseCompleted:
		result.State = "submitted"
	}
	if len(rec.Request) == 0 {
		return result, nil
	}
	var originIntent composerOriginIntent
	if json.Unmarshal(rec.Request, &originIntent) == nil && originIntent.Version == 1 && originIntent.Origin != nil {
		if originIntent.Origin.Validate() == nil {
			result.Origin = originIntent.Origin
			if originIntent.ClaudeInput != nil {
				return d.claudeNativeReceipt(local, operationID, originIntent, rec, result), nil
			}
		}
		return result, nil
	}
	var intent openCodeSendIntent
	if json.Unmarshal(rec.Request, &intent) != nil || !validOpenCodeIntent(operationID, local, intent) {
		result.State = "uncertain"
		result.Reason = "durable native request identity is unconfirmed"
		return result, nil
	}
	result.Origin = intent.Origin
	result.NativeSessionID, result.NativeMessageID, result.NativeGeneration, result.NativeCreatedAtMS = intent.Identity.RootID, intent.MessageID, intent.Identity.BackendGeneration, intent.Identity.NativeCreatedAtMS
	if len(rec.Evidence) == 0 && (rec.Phase == idempotency.PhaseExecuting || rec.Phase == idempotency.PhaseOutcomeUnknown) {
		receipt, err := d.reconcileOpenCodeAdmission(local, operationID, intent)
		if err != nil {
			result.Reason = "native admission remains unconfirmed; the request was not replayed"
			return result, nil
		}
		if err = d.recordOpenCodeAdmission(operationID, intent, receipt); err != nil {
			result.Reason = "native admission could not be retained"
			return result, nil
		}
		rec, _ = d.core.ComposerOperation(operationID)
	}
	if len(rec.Evidence) == 0 {
		return result, nil
	}
	evidence, err := readOpenCodeEvidence(rec.Evidence)
	if err != nil || !admissionMatchesIntent(evidence.Receipt, operationID, intent) || evidence.Outcome != "" && evidence.OutcomeAt < intent.NotBeforeMS {
		result.State = "uncertain"
		result.Reason = "retained native receipt identity is unconfirmed"
		return result, nil
	}
	if rec.Phase == idempotency.PhaseExecuting || rec.Phase == idempotency.PhaseOutcomeUnknown {
		if err := d.core.CommitComposerOperation(operationID, []byte(`{"ok":true}`), true); err != nil {
			result.Reason = "native admission is known; durable delivery commit is unconfirmed"
		}
	}
	if view, ok := d.openCodeView(local); ok {
		d.checkpointOpenCodeOutcomes(local, view)
		if updated, ok := d.core.ComposerOperation(operationID); ok {
			if next, e := readOpenCodeEvidence(updated.Evidence); e == nil && admissionMatchesIntent(next.Receipt, operationID, intent) {
				evidence = next
			}
		}
	}
	result.State = "admitted"
	result.Agent = evidence.Receipt.Agent
	result.Model = evidence.Receipt.Model.ProviderID + "/" + evidence.Receipt.Model.ModelID
	result.Variant = evidence.Receipt.Variant
	result.SelectionBasis = evidence.Receipt.SelectionBasis
	result.SelectionMessageID = evidence.Receipt.SelectionMessageID
	if evidence.Outcome != "" {
		result.State = string(evidence.Outcome)
		result.OutcomeSource = evidence.OutcomeSource
	}
	return result, nil
}
func (d *Daemon) reconcileOpenCodeAdmission(local, operationID string, intent openCodeSendIntent) (opencodebridge.Receipt, error) {
	b, err := d.core.SessionOpenCodeBridge(local)
	if err != nil {
		return opencodebridge.Receipt{}, err
	}
	if b.WorkerID != intent.Identity.WorkerID || b.RootID != intent.Identity.RootID || b.BackendGeneration != intent.Identity.BackendGeneration || b.Directory != intent.Identity.Directory || b.NativeCreatedAtMS != intent.Identity.NativeCreatedAtMS {
		return opencodebridge.Receipt{}, errors.New("native bridge no longer owns this request")
	}
	c, err := opencodebridge.NewClient(b.SocketPath, b.Token)
	if err != nil {
		return opencodebridge.Receipt{}, err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready, err := c.Ready(ctx)
	if err != nil || !ready.Bound || ready.Identity != intent.Identity || !opencodebridge.SupportedNativeVersion(ready.NativeVersion) {
		return opencodebridge.Receipt{}, errors.New("native request profile identity unavailable")
	}
	raw, err := c.Message(ctx, intent.MessageID)
	if err != nil {
		return opencodebridge.Receipt{}, err
	}
	return qualifyOpenCodeAdmission(operationID, intent, raw)
}

const maxOpenCodeReceiptIndex = 100_000

type openCodeOwnedInput struct {
	operation string
	intent    openCodeSendIntent
	outcome   OpenCodeOutcome
	source    string
	at        int64
}
type openCodeOwnedIndex struct {
	generation string
	loaded     bool
	inputs     map[string]openCodeOwnedInput
}
type openCodeReceiptIndexes struct {
	mu       sync.Mutex
	sessions map[string]*openCodeOwnedIndex
}

func (d *Daemon) trackOpenCodeIntent(local, operation string, intent openCodeSendIntent) {
	d.nativeReceipts.mu.Lock()
	defer d.nativeReceipts.mu.Unlock()
	if d.nativeReceipts.sessions == nil {
		d.nativeReceipts.sessions = map[string]*openCodeOwnedIndex{}
	}
	index := d.nativeReceipts.sessions[local]
	if index == nil || index.generation != intent.Identity.BackendGeneration {
		index = &openCodeOwnedIndex{generation: intent.Identity.BackendGeneration, inputs: map[string]openCodeOwnedInput{}}
		d.nativeReceipts.sessions[local] = index
	}
	if _, exists := index.inputs[intent.MessageID]; !exists && len(index.inputs) >= maxOpenCodeReceiptIndex {
		index.inputs = map[string]openCodeOwnedInput{}
		index.loaded = false
	}
	if old, ok := index.inputs[intent.MessageID]; !ok || old.operation == operation {
		index.inputs[intent.MessageID] = openCodeOwnedInput{operation: operation, intent: intent}
	}
}
func (d *Daemon) ownedOpenCodeInputs(local string, view OpenCodeView) map[string]openCodeOwnedInput {
	d.nativeReceipts.mu.Lock()
	defer d.nativeReceipts.mu.Unlock()
	if d.nativeReceipts.sessions == nil {
		d.nativeReceipts.sessions = map[string]*openCodeOwnedIndex{}
	}
	index := d.nativeReceipts.sessions[local]
	if index == nil || index.generation != view.Generation {
		index = &openCodeOwnedIndex{generation: view.Generation, inputs: map[string]openCodeOwnedInput{}}
		d.nativeReceipts.sessions[local] = index
	}
	if !index.loaded {
		for _, id := range d.core.SemanticRequestOperations(local) {
			if len(index.inputs) >= maxOpenCodeReceiptIndex {
				break
			}
			record, ok := d.core.ComposerOperation(id)
			if !ok || record.Phase == idempotency.PhaseFailed {
				continue
			}
			var intent openCodeSendIntent
			if json.Unmarshal(record.Request, &intent) != nil || !validOpenCodeIntent(id, local, intent) || intent.Identity.BackendGeneration != view.Generation || intent.Identity.RootID != view.Root {
				continue
			}
			input := openCodeOwnedInput{operation: id, intent: intent}
			if proof, err := readOpenCodeEvidence(record.Evidence); err == nil && admissionMatchesIntent(proof.Receipt, id, intent) {
				input.outcome, input.source, input.at = proof.Outcome, proof.OutcomeSource, proof.OutcomeAt
			}
			index.inputs[intent.MessageID] = input
		}
		index.loaded = true
	}
	found := map[string]openCodeOwnedInput{}
	for _, input := range view.Inputs {
		if owned, ok := index.inputs[input.MessageID]; ok {
			found[input.MessageID] = owned
		}
	}
	return found
}
func (d *Daemon) checkpointOpenCodeOutcomes(local string, view OpenCodeView) {
	owned := d.ownedOpenCodeInputs(local, view)
	for _, input := range view.Inputs {
		record, ok := owned[input.MessageID]
		if !ok || !input.Classified || input.Synthetic || input.At < record.intent.NotBeforeMS || input.OutcomeAt < input.At || !qualifiedOpenCodeOutcome(input.Outcome, input.OutcomeSource, input.OutcomeAt) || (record.outcome == input.Outcome && record.source == input.OutcomeSource && record.at == input.OutcomeAt) {
			continue
		}
		for attempt := 0; attempt < 3; attempt++ {
			current, ok := d.core.ComposerOperation(record.operation)
			if !ok {
				break
			}
			evidence, err := readOpenCodeEvidence(current.Evidence)
			if len(current.Evidence) == 0 {
				receipt, qualificationErr := qualifyObservedOpenCodeAdmission(record.operation, record.intent, input)
				if qualificationErr != nil {
					break
				}
				evidence = openCodeEvidence{Version: 1, Receipt: receipt}
			} else if err != nil || !admissionMatchesIntent(evidence.Receipt, record.operation, record.intent) {
				break
			}
			if evidence.Outcome != "" {
				newRank, oldRank := ocOutcomeRank(input.Outcome), ocOutcomeRank(evidence.Outcome)
				if newRank < oldRank || newRank == oldRank && input.OutcomeAt <= evidence.OutcomeAt {
					break
				}
			}
			evidence.Outcome, evidence.OutcomeSource, evidence.OutcomeAt = input.Outcome, input.OutcomeSource, input.OutcomeAt
			next, _ := json.Marshal(evidence)
			saved := bytes.Equal(next, current.Evidence)
			if !saved {
				if len(current.Evidence) == 0 {
					saved = d.core.ObserveSemanticReceipt(record.operation, next) == nil
				} else {
					saved = d.core.CheckpointSemanticReceipt(record.operation, current.Evidence, next) == nil
				}
			}
			if saved {
				d.nativeReceipts.mu.Lock()
				if index := d.nativeReceipts.sessions[local]; index != nil && index.generation == view.Generation {
					entry := index.inputs[input.MessageID]
					entry.outcome, entry.source, entry.at = input.Outcome, input.OutcomeSource, input.OutcomeAt
					index.inputs[input.MessageID] = entry
				}
				d.nativeReceipts.mu.Unlock()
				break
			}
		}
	}
}
