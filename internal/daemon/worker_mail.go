package daemon

import (
	"crypto/subtle"
	"errors"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/workermail"
)

// Hold the slot through the mailbox commit to bound queued buffers as well as readers.
var workerFileCaptures = make(chan struct{}, 4)

func (d *Daemon) workerProof(session, proof, purpose string) bool {
	token, ok := readShimLaunchToken(d.sessionDir(session))
	return ok && len(proof) == 64 && subtle.ConstantTimeCompare([]byte(workermail.Proof(token, purpose)), []byte(proof)) == 1
}
func (d *Daemon) WorkerMail(endpoint, session string, req schema.WorkerMailReq, fresh bool) (schema.WorkerMailReceipt, error) {
	var receipt schema.WorkerMailReceipt
	m, ok := d.Get(session)
	if !ok || m.WorkerDiscussion == "" || m.WorkerAttempt == "" || m.WorkerInstance == "" || m.Status.Process != status.ProcessRunning || !d.shimIdentityMatches(m) || !d.workerProof(session, req.Proof, "worker") {
		return receipt, errors.New("worker mailbox unavailable for this incarnation")
	}
	var files []workermail.FileSubmission
	if len(req.Paths) > 0 {
		if req.Action != "message" || req.Kind != "complete" {
			return receipt, errors.New("worker files: use a completion note to hand off files")
		}
		select {
		case workerFileCaptures <- struct{}{}:
			defer func() { <-workerFileCaptures }()
		default:
			return receipt, errors.New("worker files: several handoffs are being saved; retry this completion shortly")
		}
		var err error
		files, err = workermail.CaptureFiles(m.ProviderCwd(), req.Paths)
		if err != nil {
			return receipt, err
		}
	}
	// Order the durable queue commit against manager retirement. Opening worker
	// files can wait on a filesystem, so finish it before acquiring this fence.
	if req.Action == "message" {
		unlock, err := d.lockComposerCustody()
		if err != nil {
			return receipt, err
		}
		defer unlock()
		current, ok := d.Get(session)
		if !ok || current.WorkerDiscussion != m.WorkerDiscussion || current.WorkerInstance != m.WorkerInstance ||
			current.WorkerAttempt != m.WorkerAttempt || current.SpawnedFrom != m.SpawnedFrom || current.ProviderCwd() != m.ProviderCwd() ||
			current.Status.Process != status.ProcessRunning || !d.shimIdentityMatches(current) || !d.workerProof(session, req.Proof, "worker") {
			return receipt, errors.New("worker mailbox unavailable for this incarnation")
		}
		m = current
	}
	b := workermail.Binding{ParentInputHash: m.WorkerParentInputHash, Discussion: m.WorkerDiscussion, Parent: m.SpawnedFrom, Session: endpoint + "/" + session, Instance: m.WorkerInstance, Attempt: m.WorkerAttempt, Agent: m.AgentType, Project: m.Cwd}
	var n workermail.Note
	var err error
	switch req.Action {
	case "message":
		n, err = workermail.Submit(d.cfg.StateDir, b, req.ID, req.Kind, req.Text, fresh, files...)
	case "reply", "receipt":
		n, err = workermail.Lookup(d.cfg.StateDir, b, req.ID)
	default:
		return receipt, errors.New("invalid worker mailbox action")
	}
	if err != nil {
		return receipt, err
	}
	receipt.ID, receipt.State, receipt.Files = n.ID, n.State, n.Files
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
