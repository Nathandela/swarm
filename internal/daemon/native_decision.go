package daemon

import "errors"

func (d *Daemon) BindNativeDecisionRequest(operationID string, request []byte) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	rec, ok := d.idem.Get(operationID)
	if !ok || rec.Action != "native_decide" {
		return errors.New("native decision has no durable custody")
	}
	return d.idem.SetRequest(operationID, request)
}

func (d *Daemon) ObserveNativeDecisionReceipt(operationID string, receipt []byte) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	rec, ok := d.idem.Get(operationID)
	if !ok || rec.Action != "native_decide" {
		return errors.New("native decision has no durable custody")
	}
	return d.idem.SetEvidence(operationID, receipt)
}

func (d *Daemon) PendingNativeDecisionOperations(local, instance string) []string {
	return d.idem.PendingRequestOperations("native_decide", local, instance, 128)
}
