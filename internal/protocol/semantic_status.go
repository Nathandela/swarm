package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (c *Client) SemanticOperation(session, operationID string) (SemanticMessageReceipt, error) {
	if !slices.Contains(c.caps, CapSemanticSend) {
		return SemanticMessageReceipt{}, errors.New("protocol: semantic receipts were not negotiated")
	}
	response, err := c.requestWithTimeout(Control{Op: OpSemanticStatus, EndpointID: c.endpointID, SessionID: session, OperationID: operationID}, 35*time.Second)
	if err != nil {
		return SemanticMessageReceipt{}, err
	}
	if response.Op == OpError {
		return SemanticMessageReceipt{}, fmt.Errorf("protocol: semantic receipt: %s", response.Error)
	}
	if response.Op != OpSemanticStatus || response.OperationID != operationID || response.SemanticReceipt == nil || response.SemanticReceipt.OperationID != operationID || response.SemanticReceipt.Session != session {
		_ = c.Close()
		return SemanticMessageReceipt{}, errors.New("protocol: semantic receipt identity is unconfirmed")
	}
	return *response.SemanticReceipt, nil
}

func (cc *clientConn) handleSemanticStatus(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("semantic_status is available only on the owner socket", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapSemanticSend) {
		cc.replyErrorCode("semantic receipt capability was not negotiated", CodeNotImplemented)
		return
	}
	initial := strings.HasPrefix(c.OperationID, "claude-initial:") && len(c.OperationID) > len("claude-initial:") && len(c.OperationID) <= 128
	if !initial && (len(c.OperationID) > 128 || !strings.HasPrefix(c.OperationID, "semantic:")) {
		cc.replyErrorCode("semantic_status requires a semantic operation ID", CodeInvalidField)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	reader, ok := cc.srv.d.(SemanticOperationReader)
	if !ok {
		cc.replyErrorCode("semantic receipts are not supported by this daemon", CodeNotImplemented)
		return
	}
	unlock := cc.srv.lockComposerOperation(c.OperationID)
	defer unlock()
	receipt, err := reader.SemanticOperation(local, c.OperationID)
	if err != nil {
		cc.replyError("semantic receipt: " + err.Error())
		return
	}
	if receipt.OperationID != c.OperationID || receipt.Session != local || initial && receipt.Agent != "claude" {
		cc.replyError("semantic receipt identity mismatch")
		return
	}
	receipt.Session = c.SessionID
	_ = cc.writeControl(Control{Op: OpSemanticStatus, EndpointID: cc.endpointID, SessionID: c.SessionID, OperationID: c.OperationID, SemanticReceipt: &receipt})
}
