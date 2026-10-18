package daemon

import (
	"errors"

	"github.com/Nathandela/swarm/internal/idempotency"
)

func (d *Daemon) ComposerOperation(operationID string) (idempotency.Record, bool) {
	return d.idem.Get(operationID)
}

func (d *Daemon) UnobservedSemanticExecution(session, exceptOperation string) bool {
	return d.idem.UnobservedExecution("semantic_send", session, exceptOperation)
}

func (d *Daemon) BindSemanticRequest(operationID string, request []byte) error {
	rec, ok := d.idem.Get(operationID)
	if !ok || rec.Action != "semantic_send" {
		return errors.New("native request has no semantic operation custody")
	}
	return d.idem.SetRequest(operationID, request)
}

func (d *Daemon) ObserveSemanticReceipt(operationID string, receipt []byte) error {
	rec, ok := d.idem.Get(operationID)
	if !ok || rec.Action != "semantic_send" {
		return errors.New("native receipt has no semantic operation custody")
	}
	return d.idem.SetEvidence(operationID, receipt)
}
