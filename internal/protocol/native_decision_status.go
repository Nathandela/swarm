package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

const OpNativeDecisionStatus = "native_decision_status"

type NativeDecisionReader interface {
	NativeDecisionStatus(local, operationID string) (NativeDecisionReceipt, error)
}

func (c *Client) NativeDecisionStatus(session, operationID string) (NativeDecisionReceipt, error) {
	if !slices.Contains(c.caps, CapNativeRequests) {
		return NativeDecisionReceipt{}, errors.New("native request decisions were not negotiated")
	}
	response, err := c.requestWithTimeout(Control{Op: OpNativeDecisionStatus, EndpointID: c.endpointID, SessionID: session, OperationID: operationID}, 35*time.Second)
	if err != nil {
		return NativeDecisionReceipt{}, err
	}
	if response.Op == OpError {
		return NativeDecisionReceipt{}, fmt.Errorf("native decision status: %s (%s)", response.Error, response.ErrorCode)
	}
	receipt := response.NativeDecisionReceipt
	if response.Op != OpNativeDecisionStatus || response.OperationID != operationID || receipt == nil || receipt.OperationID != operationID || receipt.Session != session || !slices.Contains([]string{"admitted", "rejected", "uncertain"}, receipt.State) {
		return NativeDecisionReceipt{}, errors.New("native decision status identity is unconfirmed")
	}
	return *receipt, nil
}

func (cc *clientConn) handleNativeDecisionStatus(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("native_decision_status is available only on the owner socket", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapNativeRequests) {
		cc.replyErrorCode("native request decisions were not negotiated", CodeNotImplemented)
		return
	}
	if c.OperationID == "" || len(c.OperationID) > 128 || strings.IndexFunc(c.OperationID, unicode.IsControl) >= 0 {
		cc.replyErrorCode("native decision operation ID is invalid", CodeInvalidField)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	reader, ok := cc.srv.d.(NativeDecisionReader)
	if !ok {
		cc.replyErrorCode("native decision status is unsupported", CodeNotImplemented)
		return
	}
	receipt, err := reader.NativeDecisionStatus(local, c.OperationID)
	if err != nil {
		cc.replyErrorCode("native decision status: "+err.Error(), CodeUnavailable)
		return
	}
	if receipt.OperationID != c.OperationID || receipt.Session != c.SessionID {
		cc.replyErrorCode("native decision status identity is unconfirmed", CodeUnavailable)
		return
	}
	_ = cc.writeControl(Control{Op: OpNativeDecisionStatus, EndpointID: cc.endpointID, SessionID: c.SessionID, OperationID: c.OperationID, NativeDecisionReceipt: &receipt})
}
