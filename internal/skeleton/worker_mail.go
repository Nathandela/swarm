package skeleton

import "github.com/Nathandela/swarm/internal/protocol/schema"

func (a *coreAPI) WorkerMail(endpoint, session string, req schema.WorkerMailReq, fresh bool) (schema.WorkerMailReceipt, error) {
	return a.core.WorkerMail(endpoint, session, req, fresh)
}
