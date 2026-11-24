package protocol

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const CapWorkerMail = "dispatcher-worker-mail"
const CapWorkerMailFiles = "dispatcher-worker-mail-files"
const OpWorkerMail = "worker_mail"

type workerMailSource interface {
	WorkerMail(string, string, schema.WorkerMailReq, bool) (schema.WorkerMailReceipt, error)
}

func (c *Client) WorkerMail(local string, r schema.WorkerMailReq) (schema.WorkerMailReceipt, error) {
	var receipt schema.WorkerMailReceipt
	if !slices.Contains(c.caps, CapWorkerMail) {
		return receipt, errors.New("worker mailbox requires a compatible supervisor")
	}
	if len(r.Paths) > 0 && !slices.Contains(c.caps, CapWorkerMailFiles) {
		return receipt, errors.New("worker files require an updated supervisor")
	}
	session := NamespacedID(c.endpointID, local)
	resp, err := c.request(Control{Op: OpWorkerMail, SessionID: session, EndpointID: c.endpointID, WorkerMail: &r})
	if err != nil {
		return receipt, err
	}
	if resp.Op == OpError {
		return receipt, fmt.Errorf("worker mailbox: %s", resp.Error)
	}
	if resp.Op != OpWorkerMail || resp.EndpointID != c.endpointID || resp.SessionID != session || resp.WorkerMailReceipt == nil || resp.WorkerMailReceipt.ID != r.ID {
		return receipt, errors.New("worker mailbox: uncorrelated receipt")
	}
	return *resp.WorkerMailReceipt, nil
}
func (cc *clientConn) handleWorkerMail(c Control) {
	source, ok := cc.srv.d.(workerMailSource)
	endpoint, local, valid := ParseID(c.SessionID)
	if !ok || cc.srv.remoteTier || cc.srv.completionTier || !cc.hasCap(CapWorkerMail) || !valid || !validLocalID(local) || endpoint != cc.endpointID || c.EndpointID != endpoint || c.WorkerMail == nil || len(c.WorkerMail.Proof) != 64 || len(c.WorkerMail.ID) > 128 || c.WorkerMail.ID == "" {
		cc.replyErrorCode("invalid worker mailbox request", CodeInvalidField)
		return
	}
	if len(c.WorkerMail.Paths) != 0 {
		if !cc.hasCap(CapWorkerMailFiles) {
			cc.replyErrorCode("worker file handoff capability was not negotiated", CodeCapabilityRefused)
			return
		}
		if c.WorkerMail.Action != "message" || c.WorkerMail.Kind != "complete" || len(c.WorkerMail.Paths) > schema.MaxPublicationFiles {
			cc.replyErrorCode("invalid worker file handoff", CodeInvalidField)
			return
		}
	}
	receipt, err := source.WorkerMail(endpoint, local, *c.WorkerMail, SemanticOperationReplayable(c.WorkerMail.ID, cc.srv.now()))
	if err != nil {
		cc.replyErrorCode(err.Error(), CodeUnavailable)
		return
	}
	_ = cc.writeControl(Control{Op: OpWorkerMail, EndpointID: endpoint, SessionID: c.SessionID, WorkerMailReceipt: &receipt})
}
