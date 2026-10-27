package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/hermesinput"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type hermesInputIntent struct {
	Hash       string                 `json:"hash"`
	Descriptor hermesinput.Descriptor `json:"descriptor"`
}

type hermesInputEvidence struct {
	Hash       string                 `json:"hash"`
	Descriptor hermesinput.Descriptor `json:"descriptor"`
	Receipt    hermesinput.Receipt    `json:"receipt"`
}

func (d *Daemon) sendHermesNative(local, operationID string, req protocol.ComposerSendReq, begin func() error, lane *composerLane, barrier uint64) (protocol.ErrorCode, error) {
	if prior := d.core.UnobservedSemanticOperation(local, operationID); prior != "" {
		_, _ = d.semanticOperation(local, prior)
		if d.core.UnobservedSemanticExecution(local, operationID) {
			return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: a previous Hermes admission remains unconfirmed", protocol.ErrComposerOutcomeUnknown)
		}
	}
	desc, err := d.core.SessionHermesInput(local)
	if err != nil {
		return protocol.CodeStructuredUnsupported, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = hermesinput.Validate(ctx, desc.Binding); err != nil {
		return protocol.CodeStructuredUnsupported, errors.New("hermes native owner binding changed; explicitly refresh this session")
	}
	hash := sha256.Sum256([]byte(req.Text))
	intent := hermesInputIntent{Hash: hex.EncodeToString(hash[:]), Descriptor: desc}
	raw, err := json.Marshal(composerOriginIntent{Version: 1, Origin: req.Origin, HermesInput: &intent})
	if err != nil {
		return protocol.CodeInvalidField, err
	}
	if err = d.core.BindSemanticRequest(operationID, raw); err != nil {
		return protocol.CodeInvalidField, err
	}
	lane.mu.Lock()
	if lane.barrier != barrier || lane.ownerEnding || lane.retired || lane.recycling {
		lane.mu.Unlock()
		return protocol.CodeInputBusy, errors.New("hermes input was superseded before submission")
	}
	if err = beginComposerDelivery(begin); err != nil {
		lane.mu.Unlock()
		return protocol.CodeInvalidField, err
	}
	receipt, err := hermesinput.Submit(ctx, desc.Binding, operationID, req.Text)
	lane.mu.Unlock()
	if err != nil {
		var refusal *hermesinput.Refusal
		if errors.As(err, &refusal) {
			return protocol.CodeInputBusy, fmt.Errorf("hermes native admission refused the message (code %d)", refusal.Code)
		}
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: Hermes admission is unconfirmed and will not be replayed", protocol.ErrComposerOutcomeUnknown)
	}
	evidence, err := json.Marshal(hermesInputEvidence{Hash: intent.Hash, Descriptor: desc, Receipt: receipt})
	if err != nil {
		return protocol.CodeOutcomeUnknown, err
	}
	if err = d.core.ObserveSemanticReceipt(operationID, evidence); err != nil {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: Hermes native receipt could not be retained", protocol.ErrComposerOutcomeUnknown)
	}
	return "", nil
}

func (d *Daemon) hermesNativeReceipt(local, operationID string, intent composerOriginIntent, rec idempotency.Record, result protocol.SemanticMessageReceipt) protocol.SemanticMessageReceipt {
	binding := intent.HermesInput
	result.NativeSessionID = binding.Descriptor.Binding.SessionID
	result.NativeGeneration = binding.Descriptor.Generation
	if rec.Phase == idempotency.PhasePrepared || rec.Phase == idempotency.PhaseFailed {
		return result
	}
	result.State = "uncertain"
	var retained hermesInputEvidence
	valid := json.Unmarshal(rec.Evidence, &retained) == nil && retained.Hash == binding.Hash && retained.Descriptor == binding.Descriptor && retained.Receipt.AdmissionID != "" && retained.Receipt.AuthorityEpoch == binding.Descriptor.Binding.AuthorityEpoch && retained.Receipt.Ref.ProfileID == binding.Descriptor.Binding.Home && retained.Receipt.Ref.SessionID == binding.Descriptor.Binding.SessionID
	if !valid {
		if desc, err := d.core.SessionHermesInput(local); err == nil && desc == binding.Descriptor {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			activity, err := hermesinput.Inspect(ctx, desc.Binding)
			cancel()
			if err == nil {
				for _, pending := range activity.Snapshot.Pending {
					if pending.InputID == operationID && pending.AdmissionID != "" && pending.AuthorityEpoch == desc.Binding.AuthorityEpoch && pending.Ref.ProfileID == desc.Binding.Home && pending.Ref.SessionID == desc.Binding.SessionID {
						retained = hermesInputEvidence{Hash: binding.Hash, Descriptor: desc, Receipt: pending}
						raw, _ := json.Marshal(retained)
						if d.core.ObserveSemanticReceipt(operationID, raw) == nil {
							valid = true
						}
						break
					}
				}
			}
		}
	}
	if valid && (retained.Receipt.Status == "queued" || retained.Receipt.Status == "started" || retained.Receipt.Status == "terminal") {
		result.State = "submitted"
		if rec.Phase == idempotency.PhaseExecuting || rec.Phase == idempotency.PhaseOutcomeUnknown {
			_ = d.core.CommitComposerOperation(operationID, []byte(`{"ok":true}`), true)
		}
	}
	return result
}

func (d *Daemon) sampleHermesNative(id string) {
	desc, err := d.core.SessionHermesInput(id)
	if err != nil {
		_ = d.core.SetStatus(id, status.Status{Turn: status.TurnUnknown, Interaction: status.InteractionError})
		return
	}
	_ = d.core.SetConversationID(id, desc.Binding.SessionID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	activity, err := hermesinput.Inspect(ctx, desc.Binding)
	s := status.Status{Turn: status.TurnUnknown, Interaction: status.InteractionError}
	if err == nil && !activity.Unknown {
		s.Interaction = status.InteractionNone
		s.Turn = status.TurnIdle
		if activity.Background || activity.Snapshot.ExecutionState == "running" || len(activity.Snapshot.Pending) > 0 {
			s.Turn = status.TurnActive
		}
		if activity.Snapshot.ExecutionState == "unknown" {
			s.Turn = status.TurnUnknown
			s.Interaction = status.InteractionError
		}
		for _, prompt := range activity.Snapshot.Prompts {
			s.Attention = true
			if prompt.Kind == "approval" {
				s.Interaction = status.InteractionPermission
			} else {
				s.Interaction = status.InteractionPrompt
			}
		}
	}
	_ = d.core.SetHermesStatus(id, desc.Generation, s)
}
