package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
)

var testHookClaudeNativeReceiptObserved atomic.Pointer[func()]

type claudeInputIntent struct {
	Hash       string             `json:"hash"`
	Generation string             `json:"generation"`
	Worker     claudeinput.Worker `json:"worker"`
}

func (d *Daemon) sendClaudeNative(local, operationID string, req protocol.ComposerSendReq, begin func() error, lane *composerLane, barrier uint64) (protocol.ErrorCode, error) {
	if prior := d.core.UnobservedSemanticOperation(local, operationID); prior != "" {
		_, _ = d.semanticOperation(local, prior)
		if d.core.UnobservedSemanticExecution(local, operationID) {
			return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: a prior native message has no confirmed submission; nothing later was sent", protocol.ErrComposerOutcomeUnknown)
		}
	}
	desc, err := d.core.SessionClaudeInput(local)
	if err != nil {
		return protocol.CodeStructuredUnsupported, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := claudeinput.NewClient(desc)
	proof, err := client.Ready(ctx)
	if err != nil {
		return protocol.CodeStructuredUnsupported, errors.New("claude native message input is unproven; explicitly refresh this session before sending")
	}
	hash := sha256.Sum256([]byte(req.Text))
	raw, err := json.Marshal(composerOriginIntent{Version: 1, Origin: req.Origin, ClaudeInput: &claudeInputIntent{Generation: desc.Generation, Hash: hex.EncodeToString(hash[:]), Worker: proof}})
	if err != nil {
		return protocol.CodeInvalidField, err
	}
	if err = d.core.BindSemanticRequest(operationID, raw); err != nil {
		return protocol.CodeInvalidField, err
	}
	lane.mu.Lock()
	if lane.barrier != barrier || lane.ownerEnding || lane.retired || lane.recycling {
		lane.mu.Unlock()
		return protocol.CodeInputBusy, errors.New("claude input was superseded; nothing was submitted")
	}
	if err = beginComposerDelivery(begin); err != nil {
		lane.mu.Unlock()
		return protocol.CodeInvalidField, err
	}
	_, err = client.Submit(ctx, proof, operationID, req.Text)
	lane.mu.Unlock()
	if err != nil {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: native submission is unconfirmed and will not be replayed", protocol.ErrComposerOutcomeUnknown)
	}
	for {
		result, readErr := client.Operation(ctx, proof, operationID)
		if readErr == nil && (result.State == "submitted" || result.State == "started" || result.State == "rejected") {
			if hook := testHookClaudeNativeReceiptObserved.Load(); hook != nil {
				(*hook)()
			}
			if result, err = d.retainClaudeReceipt(operationID, result); err != nil {
				return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: native receipt could not be retained", protocol.ErrComposerOutcomeUnknown)
			}
			if result.State == "rejected" {
				return protocol.CodeInputBusy, errors.New("Claude native queue refused the message: " + result.Reason)
			}
			return "", nil
		}
		select {
		case <-ctx.Done():
			return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: native queue reply is unconfirmed; no terminal keys were sent", protocol.ErrComposerOutcomeUnknown)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (d *Daemon) claudeNativeReceipt(local, operationID string, intent composerOriginIntent, rec idempotency.Record, result protocol.SemanticMessageReceipt) protocol.SemanticMessageReceipt {
	binding := intent.ClaudeInput
	result.NativeSessionID = binding.Worker.NativeSessionID
	result.NativeGeneration = binding.Generation
	var retained claudeinput.Message
	valid := json.Unmarshal(rec.Evidence, &retained) == nil && retained.OperationID == operationID && retained.Worker == binding.Worker && retained.Hash == binding.Hash
	if desc, err := d.core.SessionClaudeInput(local); err == nil && desc.Generation == binding.Generation {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		current, err := claudeinput.NewClient(desc).Operation(ctx, binding.Worker, operationID)
		cancel()
		if err == nil && current.OperationID == operationID && current.Worker == binding.Worker && current.Hash == binding.Hash && (!valid || current.Hash == retained.Hash) {
			if current.State == "submitted" || current.State == "started" || current.State == "rejected" {
				saved, err := d.retainClaudeReceipt(operationID, current)
				if err != nil {
					result.State = "uncertain"
					result.Reason = "native receipt could not be retained"
					return result
				}
				current = saved
			}
			retained = current
			valid = true
		}
	}
	if !valid {
		return result
	}
	switch retained.State {
	case "submitted", "started":
		result.State = "submitted"
	case "rejected":
		result.State = "rejected"
		result.Reason = retained.Reason
	default:
		result.State = "uncertain"
		return result
	}
	if rec.Phase == idempotency.PhaseExecuting || rec.Phase == idempotency.PhaseOutcomeUnknown {
		failed := retained.State == "rejected"
		outcome := struct {
			OK      bool               `json:"ok"`
			Code    protocol.ErrorCode `json:"code,omitempty"`
			Message string             `json:"message,omitempty"`
		}{OK: !failed}
		if failed {
			outcome.Code = protocol.CodeInputBusy
			outcome.Message = "Claude native queue refused the message: " + retained.Reason
		}
		raw, _ := json.Marshal(outcome)
		evidence, _ := json.Marshal(retained)
		if err := d.core.SettleNativeInput(operationID, evidence, raw, failed); err != nil {
			result.State = "uncertain"
			result.Reason = "native disposition could not be durably settled"
		}
	}
	return result
}

// A concurrent status read can retain a stronger native result before the send's reply.
func (d *Daemon) retainClaudeReceipt(operationID string, result claudeinput.Message) (claudeinput.Message, error) {
	for range 3 {
		record, ok := d.core.ComposerOperation(operationID)
		if !ok {
			return result, errors.New("native operation custody unavailable")
		}
		var old claudeinput.Message
		if len(record.Evidence) > 0 {
			if json.Unmarshal(record.Evidence, &old) != nil || old.OperationID != result.OperationID || old.Hash != result.Hash || old.Worker != result.Worker {
				return result, errors.New("native receipt identity conflict")
			}
			if old.State == "started" || old.State == "rejected" {
				return old, nil
			}
		}
		raw, _ := json.Marshal(result)
		var err error
		if len(record.Evidence) == 0 {
			err = d.core.ObserveSemanticReceipt(operationID, raw)
		} else {
			err = d.core.CheckpointSemanticReceipt(operationID, record.Evidence, raw)
		}
		if err == nil {
			return result, nil
		}
	}
	return result, errors.New("native receipt changed during retention")
}
