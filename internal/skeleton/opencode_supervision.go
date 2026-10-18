package skeleton

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

var errSupervisionRetry = errors.New("native notification was durably rejected without an effect")

func (d *Daemon) sendSupervision(local string, input protocol.SendInputReq, operationID string) error {
	m, ok := d.core.Get(local)
	if !ok {
		return errors.New("notification source is unavailable")
	}
	if m.RequiredRuntime != persist.RequiredRuntimeOpenCode {
		return d.srv.SendInput(local, input)
	}
	if !input.Submit || input.Text == "" || input.Key != "" || len(input.Text) > protocol.MaxSendInputText {
		return errors.New("native notification requires one bounded submitted message")
	}
	if rec, exists := d.core.ComposerOperation(operationID); exists {
		if rec.Action != protocol.OpSemanticSend || rec.SessionID != local {
			return errors.New("native notification custody conflicts with its source")
		}
		switch rec.Phase {
		case idempotency.PhaseCompleted:
			return nil
		case idempotency.PhaseFailed:
			return errSupervisionRetry
		case idempotency.PhasePrepared:
		case idempotency.PhaseExecuting, idempotency.PhaseOutcomeUnknown:
			receipt, err := d.semanticOperation(local, operationID)
			if err == nil && nativeNotificationAdmitted(receipt.State) {
				return nil
			}
			return errors.New("native notification admission remains unconfirmed; nothing was replayed")
		default:
			return errors.New("native notification has an unqualified durable phase; nothing was replayed")
		}
	}
	if prior := d.core.UnobservedSemanticOperation(local, operationID); prior != "" {
		receipt, err := d.semanticOperation(local, prior)
		if err != nil || !nativeNotificationAdmitted(receipt.State) || d.core.UnobservedSemanticExecution(local, operationID) {
			return errors.New("prior native admission remains unconfirmed; the new notification was not submitted")
		}
	}
	instance, ok := d.sessionInstance(local)
	if !ok {
		return errors.New("native notification source incarnation is unavailable")
	}
	req := protocol.ComposerSendReq{Session: protocol.NamespacedID(d.api.endpointID, local), SessionInstance: instance, Text: input.Text}
	if old, exists := d.core.ComposerOperation(operationID); exists && old.SessionInstance != instance {
		if err := d.core.CommitComposerOperation(operationID, []byte(`{"ok":false}`), false); err != nil {
			return err
		}
		return errSupervisionRetry
	}
	hash := hex.EncodeToString(schema.ComposerSendContentHash(&req))
	phase, _, err := d.core.ClaimComposerOperation(operationID, protocol.OpSemanticSend, local, instance, hash)
	if err != nil {
		return err
	}
	if phase != string(idempotency.PhasePrepared) {
		return fmt.Errorf("native notification could not claim prepared custody: %s", phase)
	}
	code, sendErr := d.semanticSendTransactional(d.api.endpointID, operationID, req, func() error {
		return d.core.BeginComposerOperation(operationID)
	})
	if errors.Is(sendErr, protocol.ErrComposerOutcomeUnknown) {
		return sendErr
	}
	if sendErr != nil {
		if rec, exists := d.core.ComposerOperation(operationID); exists && rec.Phase == idempotency.PhasePrepared {
			return sendErr
		}
	}
	outcome := struct {
		OK      bool               `json:"ok"`
		Code    protocol.ErrorCode `json:"code,omitempty"`
		Message string             `json:"message,omitempty"`
	}{OK: sendErr == nil, Code: code}
	if sendErr != nil {
		outcome.Message = sendErr.Error()
	}
	raw, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	if err := d.core.CommitComposerOperation(operationID, raw, sendErr == nil); err != nil {
		return err
	}
	if sendErr != nil {
		return errSupervisionRetry
	}
	return nil
}

func nativeNotificationAdmitted(state string) bool {
	switch state {
	case "admitted", "completed", "failed", "cancelled", "declined":
		return true
	}
	return false
}

func (d *Daemon) supervisionMeta(local string) (persist.Meta, bool) {
	m, ok := d.core.Get(local)
	if !ok || m.RequiredRuntime != persist.RequiredRuntimeOpenCode || m.Status.Process != status.ProcessRunning {
		return m, ok
	}
	b := m.OpenCodeBinding
	if b == nil || b.Phase != persist.OpenCodePhaseReady || b.Initial != nil && b.Initial.Phase != persist.OpenCodeInitialAdmitted {
		m.Status.Turn, m.Status.Interaction = status.TurnUnknown, status.InteractionNone
		return m, true
	}
	if m.Status.Turn == status.TurnIdle && m.Status.Interaction == status.InteractionNone {
		view, observed := d.openCodeView(local)
		if !observed || view.Degraded || !view.BusyKnown || view.Busy || view.Current != nil {
			m.Status.Turn = status.TurnUnknown
		}
	}
	return m, true
}
