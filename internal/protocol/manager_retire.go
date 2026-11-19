package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	CapManagerRetire      = "manager-retire"
	OpManagerRetire       = "manager_retire"
	OpManagerRetireStatus = "manager_retire_status"
)

type ManagerRetireReq = schema.ManagerRetireReq
type ManagerRetireReceipt = schema.ManagerRetireReceipt

type ManagerRetireSource interface {
	ManagerRetireSupported() bool
	ManagerRetire(context.Context, string, string, ManagerRetireReq, bool) (ManagerRetireReceipt, error)
}

func validManagerRetireOperationID(id string) bool {
	body, ok := strings.CutPrefix(id, "manager-retire:")
	if !ok || len(body) != 64 || strings.ToLower(body) != body {
		return false
	}
	_, err := hex.DecodeString(body)
	return err == nil
}

// ManagerRetireOperationID is stable for the exact quiescent manager checkpoint.
func ManagerRetireOperationID(req ManagerRetireReq) string {
	raw, _ := json.Marshal(req)
	digest := sha256.Sum256(raw)
	return "manager-retire:" + hex.EncodeToString(digest[:])
}

func (c *Client) ManagerRetire(ctx context.Context, req ManagerRetireReq, statusOnly bool) (ManagerRetireReceipt, error) {
	var none ManagerRetireReceipt
	if !slices.Contains(c.caps, CapManagerRetire) {
		return none, managerproof.ErrUnavailable
	}
	if req.Validate() != nil {
		return none, managerproof.ErrInvalid
	}
	op := OpManagerRetire
	if statusOnly {
		op = OpManagerRetireStatus
	}
	id := ManagerRetireOperationID(req)
	r, err := c.managerRequest(ctx, Control{Op: op, BodyVersion: 1, EndpointID: c.endpointID,
		SessionID: req.Binding.Session, OperationID: id, ManagerRetireRequest: &req})
	if err != nil {
		return none, err
	}
	if r.Op == OpError && r.ErrorCode == CodeStaleInstance {
		return none, managerproof.ErrStale
	}
	if r.Op != op || r.SessionID != req.Binding.Session || r.OperationID != id || r.ManagerRetire == nil ||
		r.ManagerRetire.OperationID != id || !r.ManagerRetire.ValidState() {
		return none, managerproof.ErrUnavailable
	}
	return *r.ManagerRetire, nil
}

func (cc *clientConn) handleManagerRetire(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("manager retirement is owner-only", CodeNotAuthorized)
		return
	}
	source, ok := cc.srv.d.(ManagerRetireSource)
	if !ok || !cc.hasCap(CapManagerRetire) {
		cc.replyErrorCode("manager retirement was not negotiated", CodeNotImplemented)
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	if c.ManagerRetireRequest == nil || c.ManagerRetireRequest.Validate() != nil || c.ManagerRetireRequest.Binding.Session != c.SessionID ||
		ManagerRetireOperationID(*c.ManagerRetireRequest) != c.OperationID {
		cc.replyErrorCode("invalid exact manager retirement request", CodeInvalidField)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	unlock := cc.srv.lockComposerOperation(c.OperationID)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req := *c.ManagerRetireRequest
	req.Binding.Session = local
	receipt, err := source.ManagerRetire(ctx, local, c.OperationID, req, c.Op == OpManagerRetireStatus)
	if errors.Is(err, managerproof.ErrStale) {
		cc.replyErrorCode("manager retirement target changed", CodeStaleInstance)
		return
	}
	if errors.Is(err, managerproof.ErrQuarantined) {
		cc.replyErrorCode("native manager custody lost", CodeManagerCustodyLost)
		return
	}
	if err != nil || receipt.OperationID != c.OperationID || !receipt.ValidState() {
		cc.replyErrorCode("manager retirement receipt unavailable", CodeUnavailable)
		return
	}
	_ = cc.writeControl(Control{Op: c.Op, EndpointID: cc.endpointID, SessionID: c.SessionID, OperationID: c.OperationID, ManagerRetire: &receipt})
}
