package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	CapManagerInterrupt      = "manager-interrupt"
	OpManagerInterrupt       = "manager_interrupt"
	OpManagerInterruptStatus = "manager_interrupt_status"
)

type ManagerInterruptReq = schema.ManagerInterruptReq
type ManagerInterruptReceipt = schema.ManagerInterruptReceipt

type ManagerInterruptSource interface {
	ManagerInterruptSupported() bool
	ManagerInterrupt(context.Context, string, string, ManagerInterruptReq, bool) (ManagerInterruptReceipt, error)
}

// ManagerInterruptOperationID is stable for the exact original manager turn.
func ManagerInterruptOperationID(req ManagerInterruptReq) string {
	raw, _ := json.Marshal(req)
	digest := sha256.Sum256(raw)
	return "manager-interrupt:" + hex.EncodeToString(digest[:])
}

func (c *Client) ManagerInterrupt(ctx context.Context, req ManagerInterruptReq, statusOnly bool) (ManagerInterruptReceipt, error) {
	var none ManagerInterruptReceipt
	if !slices.Contains(c.caps, CapManagerInterrupt) {
		return none, managerproof.ErrUnavailable
	}
	if req.Validate() != nil {
		return none, managerproof.ErrInvalid
	}
	op := OpManagerInterrupt
	if statusOnly {
		op = OpManagerInterruptStatus
	}
	id := ManagerInterruptOperationID(req)
	r, err := c.managerRequest(ctx, Control{Op: op, BodyVersion: 1, EndpointID: c.endpointID,
		SessionID: req.Binding.Session, OperationID: id, ManagerInterruptRequest: &req})
	if err != nil {
		return none, err
	}
	if r.Op == OpError && r.ErrorCode == CodeStaleInstance {
		return none, managerproof.ErrStale
	}
	if r.Op != op || r.SessionID != req.Binding.Session || r.OperationID != id || r.ManagerInterrupt == nil ||
		r.ManagerInterrupt.OperationID != id || r.ManagerInterrupt.NativeInputID != req.NativeInputID ||
		r.ManagerInterrupt.NativeTurnID != req.NativeTurnID || !r.ManagerInterrupt.ValidState() {
		return none, managerproof.ErrUnavailable
	}
	return *r.ManagerInterrupt, nil
}

func (cc *clientConn) handleManagerInterrupt(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("manager interruption is owner-only", CodeNotAuthorized)
		return
	}
	source, ok := cc.srv.d.(ManagerInterruptSource)
	if !ok || !cc.hasCap(CapManagerInterrupt) {
		cc.replyErrorCode("manager interruption was not negotiated", CodeNotImplemented)
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	if c.ManagerInterruptRequest == nil || c.ManagerInterruptRequest.Validate() != nil || c.ManagerInterruptRequest.Binding.Session != c.SessionID ||
		ManagerInterruptOperationID(*c.ManagerInterruptRequest) != c.OperationID {
		cc.replyErrorCode("invalid exact manager interrupt request", CodeInvalidField)
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
	req := *c.ManagerInterruptRequest
	req.Binding.Session = local
	receipt, err := source.ManagerInterrupt(ctx, local, c.OperationID, req, c.Op == OpManagerInterruptStatus)
	if errors.Is(err, managerproof.ErrStale) {
		cc.replyErrorCode("manager interrupt target changed", CodeStaleInstance)
		return
	}
	if errors.Is(err, managerproof.ErrQuarantined) {
		cc.replyErrorCode("native manager custody lost", CodeManagerCustodyLost)
		return
	}
	if err != nil || receipt.OperationID != c.OperationID || receipt.NativeInputID != req.NativeInputID ||
		receipt.NativeTurnID != req.NativeTurnID || !receipt.ValidState() {
		cc.replyErrorCode("manager interrupt receipt unavailable", CodeUnavailable)
		return
	}
	_ = cc.writeControl(Control{Op: c.Op, EndpointID: cc.endpointID, SessionID: c.SessionID, OperationID: c.OperationID, ManagerInterrupt: &receipt})
}
