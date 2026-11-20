package daemon

import (
	"crypto/subtle"
	"errors"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/workermail"
)

func (d *Daemon) workerProof(session, proof, purpose string) bool {
	token, ok := readShimLaunchToken(d.sessionDir(session))
	return ok && len(proof) == 64 && subtle.ConstantTimeCompare([]byte(workermail.Proof(token, purpose)), []byte(proof)) == 1
}
func (d *Daemon) WorkerMail(endpoint, session string, req schema.WorkerMailReq, fresh bool) (schema.WorkerMailReceipt, error) {
	var receipt schema.WorkerMailReceipt
	// Order note acceptance against the manager's final queue check and durable retirement claim.
	if req.Action == "message" {
		unlock, err := d.lockComposerCustody()
		if err != nil {
			return receipt, err
		}
		defer unlock()
	}
	m, ok := d.Get(session)
	if !ok || m.WorkerDiscussion == "" || m.WorkerAttempt == "" || m.WorkerInstance == "" || m.Status.Process != status.ProcessRunning || !d.shimIdentityMatches(m) || !d.workerProof(session, req.Proof, "worker") {
		return receipt, errors.New("worker mailbox unavailable for this incarnation")
	}
	b := workermail.Binding{Discussion: m.WorkerDiscussion, Parent: m.SpawnedFrom, Session: endpoint + "/" + session, Instance: m.WorkerInstance, Attempt: m.WorkerAttempt, Agent: m.AgentType, Project: m.Cwd}
	var n workermail.Note
	var err error
	switch req.Action {
	case "message":
		n, err = workermail.Submit(d.cfg.StateDir, b, req.ID, req.Kind, req.Text, fresh)
	case "reply", "receipt":
		n, err = workermail.Lookup(d.cfg.StateDir, b, req.ID)
	default:
		return receipt, errors.New("invalid worker mailbox action")
	}
	if err != nil {
		return receipt, err
	}
	receipt.ID, receipt.State = n.ID, n.State
	if req.Action == "reply" && n.State != "superseded" {
		receipt.State = "pending"
		if n.Answer != "" {
			if err = workermail.MarkAnswerRead(d.cfg.StateDir, n); err != nil {
				return schema.WorkerMailReceipt{}, err
			}
			receipt.State, receipt.Text = "available", n.Answer
		}
	}
	return receipt, nil
}
