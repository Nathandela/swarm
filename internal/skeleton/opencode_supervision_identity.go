package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type nativeSupervisionEvent struct {
	Key, InputID, ObservedInputID string
	Known, Fresh, Owned           bool
}

func (d *Daemon) nativeSupervisionState(m persist.Meta) nativeSupervisionEvent {
	view, _ := d.openCodeView(m.ID)
	event := openCodeSupervisionEvent(m, view)
	if !event.Known || event.Fresh || event.Owned || event.InputID == "" {
		return event
	}
	owned, ok := d.ownedOpenCodeInputs(m.ID, view)[event.InputID]
	if !ok {
		return event
	}
	record, ok := d.core.ComposerOperation(owned.operation)
	if !ok || record.Phase == idempotency.PhaseFailed {
		return event
	}
	evidence, err := readOpenCodeEvidence(record.Evidence)
	if err != nil || !admissionMatchesIntent(evidence.Receipt, owned.operation, owned.intent) {
		return event
	}
	for _, input := range view.Inputs {
		if input.MessageID != event.InputID {
			continue
		}
		observed, err := qualifyObservedOpenCodeAdmission(owned.operation, owned.intent, input)
		event.Owned = err == nil && observed.Agent == evidence.Receipt.Agent && *observed.Model == *evidence.Receipt.Model && observed.Variant == evidence.Receipt.Variant
		break
	}
	return event
}

// Native identity survives observer hydration; epochs and request-card expiry are not events.
func openCodeSupervisionEvent(m persist.Meta, view OpenCodeView) nativeSupervisionEvent {
	b := m.OpenCodeBinding
	if b == nil || b.Phase != persist.OpenCodePhaseReady || b.NativeCreatedAtMS <= 0 {
		return nativeSupervisionEvent{}
	}
	key := func(value any) string {
		raw, _ := json.Marshal([]any{m.ConversationID, b.NativeCreatedAtMS, value})
		return fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	if status.Derive(m.Status) == status.GroupCompleted {
		return nativeSupervisionEvent{Key: key([]string{"process", b.Generation, string(m.Status.Process)}), Known: true, Fresh: true}
	}
	if b.Initial != nil && b.Initial.Phase != persist.OpenCodeInitialAdmitted {
		return nativeSupervisionEvent{}
	}
	if view.Root != m.ConversationID || view.Generation != b.Generation || view.Degraded || !view.BusyKnown || !view.CurrentScopeKnown {
		return nativeSupervisionEvent{}
	}
	observed := ""
	if input := view.Current; view.Busy && input != nil && input.Classified && !input.Synthetic {
		observed = input.MessageID
	}
	if len(view.Pending) > 0 {
		if len(view.Pending) > 128 {
			return nativeSupervisionEvent{}
		}
		keys := make([]string, 0, len(view.Pending))
		for _, request := range view.Pending {
			if len(request.Raw) == 0 || len(request.Raw) > 32<<10 {
				return nativeSupervisionEvent{}
			}
			var content map[string]any
			decoder := json.NewDecoder(bytes.NewReader(request.Raw))
			decoder.UseNumber()
			if decoder.Decode(&content) != nil || content == nil || content["id"] != request.ID || content["sessionID"] != request.SessionID {
				return nativeSupervisionEvent{}
			}
			keys = append(keys, key([]any{request.Kind, request.SessionID, request.ID, content}))
		}
		sort.Strings(keys)
		return nativeSupervisionEvent{Key: key(keys), Known: true, Fresh: true, ObservedInputID: observed}
	}
	if view.Busy || view.Current != nil {
		return nativeSupervisionEvent{Known: true, ObservedInputID: observed}
	}
	var latest *OpenCodeInput
	for i := range view.Inputs {
		input := &view.Inputs[i]
		if !input.Classified || input.Synthetic {
			continue
		}
		if latest == nil || input.At > latest.At || input.At == latest.At && input.MessageID > latest.MessageID {
			latest = input
		}
	}
	if latest == nil {
		return nativeSupervisionEvent{Known: true}
	}
	switch latest.Outcome {
	case OpenCodeOutcomeCompleted, OpenCodeOutcomeFailed, OpenCodeOutcomeCancelled, OpenCodeOutcomeDeclined:
	default:
		return nativeSupervisionEvent{}
	}
	return nativeSupervisionEvent{Key: key([]string{"input", latest.MessageID, string(latest.Outcome)}), InputID: latest.MessageID, Owned: b.Initial != nil && b.Initial.Phase == persist.OpenCodeInitialAdmitted && b.Initial.MessageID == latest.MessageID, Known: true, Fresh: latest.OutcomeSource == OpenCodeOutcomeSourceLive || latest.OutcomeSource == OpenCodeOutcomeSourceReconciled}
}

func (s *supervisor) evaluateNativeLocked(r *supervisionRecord, m persist.Meta) bool {
	if s.nativeEvent == nil {
		return r.Pending != nil
	}
	event := s.nativeEvent(m)
	if !event.Known {
		return r.Pending != nil
	}
	changed := false
	if event.ObservedInputID != "" && event.ObservedInputID != r.NativeObservedInput {
		r.NativeObservedInput = event.ObservedInputID
		changed = true
	}
	if event.Key == "" || event.Key == r.NativeDeliveredKey {
		if r.Pending != nil {
			r.Pending = nil
			changed = true
		}
		if changed {
			s.persistLocked(r)
		}
		return false
	}
	if r.Pending != nil && r.Pending.NativeKey == event.Key {
		if changed {
			s.persistLocked(r)
		}
		return true
	}
	observed := event.InputID != "" && event.InputID == r.NativeObservedInput
	if !event.Fresh && !event.Owned && !observed {
		if r.Pending != nil {
			r.Pending = nil
			changed = true
		}
		if changed {
			s.persistLocked(r)
		}
		return false
	}
	r.Seq++
	r.LastGroup = string(status.Derive(m.Status))
	r.Pending = &supervisionEvent{Seq: r.Seq, NativeKey: event.Key, Group: status.Derive(m.Status), Interaction: m.Status.Interaction}
	s.persistLocked(r)
	return true
}
