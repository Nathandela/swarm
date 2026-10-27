package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/agyinput"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
)

type agyInputIntent struct {
	Proof agyinput.Proof `json:"proof"`
	Hash  string         `json:"hash"`
}

func (d *Daemon) sendAgyNative(local, operationID string, req protocol.ComposerSendReq, begin func() error, lane *composerLane, barrier uint64) (protocol.ErrorCode, error) {
	if prior := d.core.UnobservedSemanticOperation(local, operationID); prior != "" {
		_, _ = d.semanticOperation(local, prior)
		if d.core.UnobservedSemanticExecution(local, operationID) {
			return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: prior native delivery remains unconfirmed; nothing later was sent", protocol.ErrComposerOutcomeUnknown)
		}
	}
	desc, err := d.core.SessionAgyInput(local)
	if err != nil {
		return protocol.CodeStructuredUnsupported, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := agyinput.NewClient(desc)
	proof, err := client.Ready(ctx)
	if err != nil {
		return protocol.CodeStructuredUnsupported, errors.New("agy native input is unproven; complete registration from its own tool context or explicitly refresh this session")
	}
	hash := sha256.Sum256([]byte(req.Text))
	digest := hex.EncodeToString(hash[:])
	raw, _ := json.Marshal(composerOriginIntent{Version: 1, Origin: req.Origin, AgyInput: &agyInputIntent{Proof: proof, Hash: digest}})
	if err = d.core.BindSemanticRequest(operationID, raw); err != nil {
		return protocol.CodeInvalidField, err
	}
	lane.mu.Lock()
	if lane.barrier != barrier || lane.ownerEnding || lane.retired || lane.recycling {
		lane.mu.Unlock()
		return protocol.CodeInputBusy, errors.New("native input was superseded; nothing was submitted")
	}
	if err = beginComposerDelivery(begin); err != nil {
		lane.mu.Unlock()
		return protocol.CodeInvalidField, err
	}
	receipt, err := client.Submit(ctx, proof, operationID, req.Text)
	lane.mu.Unlock()
	if errors.Is(err, agyinput.ErrRejected) {
		return protocol.CodeInputBusy, err
	}
	if err != nil || receipt.State != "submitted" || receipt.OperationID != operationID || receipt.Proof != proof || receipt.Hash != digest {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: agy native submission remains unconfirmed; it will not be replayed", protocol.ErrComposerOutcomeUnknown)
	}
	evidence, _ := json.Marshal(receipt)
	if err = d.core.ObserveSemanticReceipt(operationID, evidence); err != nil {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: native submission receipt could not be retained", protocol.ErrComposerOutcomeUnknown)
	}
	return "", nil
}

func (d *Daemon) agyNativeReceipt(local, operationID string, intent composerOriginIntent, rec idempotency.Record, result protocol.SemanticMessageReceipt) protocol.SemanticMessageReceipt {
	binding := intent.AgyInput
	result.NativeSessionID = binding.Proof.ConversationID
	result.NativeGeneration = binding.Proof.Generation
	var receipt agyinput.Receipt
	valid := json.Unmarshal(rec.Evidence, &receipt) == nil && receipt.OperationID == operationID && receipt.Proof == binding.Proof && receipt.Hash == binding.Hash && receipt.State == "submitted"
	if !valid {
		if desc, err := d.core.SessionAgyInput(local); err == nil && desc.Generation == binding.Proof.Generation {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			current, err := agyinput.NewClient(desc).Operation(ctx, binding.Proof, operationID)
			cancel()
			if err == nil && current.OperationID == operationID && current.Proof == binding.Proof && current.Hash == binding.Hash && current.State == "submitted" {
				raw, _ := json.Marshal(current)
				if d.core.ObserveSemanticReceipt(operationID, raw) == nil {
					receipt = current
					valid = true
				}
			}
		}
	}
	if valid {
		result.State = "submitted"
		if rec.Phase == idempotency.PhaseExecuting || rec.Phase == idempotency.PhaseOutcomeUnknown {
			_ = d.core.CommitComposerOperation(operationID, []byte(`{"ok":true}`), true)
		}
	}
	return result
}
