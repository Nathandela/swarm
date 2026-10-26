package idempotency

import (
	"bytes"
	"encoding/json"
	"errors"
)

// SettleNativeInput atomically binds a terminal delivery outcome to a retained native
// input disposition. Admission-shaped evidence keeps the ordinary no-demotion guard.
func (s *Store) SettleNativeInput(operationID string, expected, outcome []byte, failed bool) error {
	var terminal struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(outcome, &terminal) != nil || terminal.OK == nil || *terminal.OK == failed {
		return errors.New("idempotency: native input outcome conflicts with disposition")
	}
	var proof struct {
		OperationID string `json:"operation_id"`
		State       string `json:"state"`
	}
	if len(expected) == 0 || json.Unmarshal(expected, &proof) != nil || proof.OperationID != operationID || failed && proof.State != "rejected" || !failed && proof.State != "submitted" && proof.State != "started" || !json.Valid(outcome) {
		return errors.New("idempotency: native input disposition is unconfirmed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncPendingDirectoryLocked(); err != nil {
		return err
	}
	record, ok := s.records[operationID]
	if !ok || (record.Action != "semantic_send" && record.Action != "composer_send") || !bytes.Equal(record.Evidence, expected) {
		return errors.New("idempotency: native input receipt changed")
	}
	phase := PhaseCompleted
	if failed {
		phase = PhaseFailed
	}
	if record.Phase == phase && bytes.Equal(record.Outcome, outcome) {
		return nil
	}
	if record.Phase != PhaseExecuting && record.Phase != PhaseOutcomeUnknown {
		return errors.New("idempotency: native input already settled")
	}
	record.Phase = phase
	record.Outcome = append(json.RawMessage(nil), outcome...)
	record.UpdatedAt = s.clock()
	if err := s.appendLocked(record); err != nil {
		return err
	}
	s.records[operationID] = record
	return nil
}
