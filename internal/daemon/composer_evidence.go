package daemon

import (
	"errors"

	"github.com/Nathandela/swarm/internal/idempotency"
)

func (d *Daemon) ComposerOperation(operationID string) (idempotency.Record, bool) {
	return d.idem.Get(operationID)
}

func (d *Daemon) UnobservedSemanticExecution(session, exceptOperation string) bool {
	return d.UnobservedSemanticOperation(session, exceptOperation) != ""
}

func (d *Daemon) UnobservedSemanticOperation(session, exceptOperation string) string {
	if id := d.idem.UnobservedOperation("semantic_send", session, exceptOperation); id != "" {
		return id
	}
	return d.idem.UnobservedOperation("composer_send", session, exceptOperation)
}

func (d *Daemon) BindSemanticRequest(operationID string, request []byte) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	rec, ok := d.idem.Get(operationID)
	if !ok || !nativeInputAction(rec.Action) {
		return errors.New("native request has no semantic operation custody")
	}
	return d.idem.SetRequest(operationID, request)
}

func (d *Daemon) ObserveSemanticReceipt(operationID string, receipt []byte) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	rec, ok := d.idem.Get(operationID)
	if !ok || !nativeInputAction(rec.Action) {
		return errors.New("native receipt has no semantic operation custody")
	}
	return d.idem.SetEvidence(operationID, receipt)
}

func (d *Daemon) SemanticRequestOperations(session string) []string {
	return append(d.idem.RequestOperations("semantic_send", session), d.idem.RequestOperations("composer_send", session)...)
}
func (d *Daemon) CheckpointSemanticReceipt(operationID string, expected, next []byte) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	rec, ok := d.idem.Get(operationID)
	if !ok || !nativeInputAction(rec.Action) || len(rec.Request) == 0 {
		return errors.New("native checkpoint has no semantic request custody")
	}
	return d.idem.CompareEvidence(operationID, expected, next)
}

func nativeInputAction(action string) bool {
	return action == "semantic_send" || action == "composer_send"
}

// Composer writers share Close's final writer barrier before singleton handoff.
func (d *Daemon) lockComposerCustody() (func(), error) {
	d.writeMu.Lock()
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		d.writeMu.Unlock()
		return nil, errors.New("daemon: composer custody is closed")
	}
	return d.writeMu.Unlock, nil
}
