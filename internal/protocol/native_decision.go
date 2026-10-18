package protocol

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	OpNativeDecide     = "native_decide"
	ActionNativeDecide = schema.ActionNativeDecide
	CapNativeRequests  = "native-requests-v1"
)

type (
	NativeDecisionReq     = schema.NativeDecisionReq
	NativeDecisionReceipt = schema.NativeDecisionReceipt
	NativeRequestView     = schema.NativeRequestView
	NativeQuestionView    = schema.NativeQuestionView
	NativeQuestionOption  = schema.NativeQuestionOption
	NativeDecisionOption  = schema.NativeDecisionOption
)

type NativeDecisionApplier interface {
	ApplyNativeDecision(machine, operationID string, req NativeDecisionReq) (NativeDecisionReceipt, ErrorCode, error)
}

func ValidateNativeDecision(req NativeDecisionReq) error {
	for _, value := range []string{req.Session, req.SessionInstance, req.RootID, req.Generation, req.NativeSessionID, req.RequestID} {
		if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return errors.New("invalid native request identity")
		}
	}
	hash, err := hex.DecodeString(req.RequestHash)
	if err != nil || len(hash) != 32 || strings.ToLower(req.RequestHash) != req.RequestHash || req.ExpiresAt == nil || req.ExpiresAt.UnixMilli() <= 0 || req.ExpiresAt.UnixMilli() > 1<<53-1 {
		return errors.New("invalid native request hash or expiry")
	}
	switch req.Kind {
	case "permission_reply":
		if !slices.Contains([]string{"once", "always", "reject"}, req.Reply) || len(req.Answers) != 0 {
			return errors.New("invalid native permission reply")
		}
	case "question_reject":
		if req.Reply != "" || len(req.Answers) != 0 {
			return errors.New("invalid native question rejection")
		}
	case "question_reply":
		if req.Reply != "" || len(req.Answers) == 0 || len(req.Answers) > 16 {
			return errors.New("invalid native question answers")
		}
		for _, answers := range req.Answers {
			if answers == nil || len(answers) > 128 {
				return errors.New("invalid native question answer group")
			}
			for _, answer := range answers {
				if answer == "" || len(answer) > 4096 || !utf8.ValidString(answer) || strings.ContainsRune(answer, 0) {
					return errors.New("invalid native question answer")
				}
			}
		}
		encoded, _ := json.Marshal(req.Answers)
		if len(encoded) > 16384 {
			return errors.New("native answers exceed bound")
		}
	default:
		return errors.New("unsupported native decision kind")
	}
	return nil
}

func (c *Client) NativeDecide(operationID string, req NativeDecisionReq) (NativeDecisionReceipt, error) {
	if !slices.Contains(c.caps, CapNativeRequests) {
		return NativeDecisionReceipt{}, errors.New("native request decisions were not negotiated")
	}
	if err := ValidateNativeDecision(req); err != nil {
		return NativeDecisionReceipt{}, err
	}
	response, err := c.requestWithTimeout(Control{Op: OpNativeDecide, EndpointID: c.endpointID, SessionID: req.Session, OperationID: operationID, BodyVersion: 1, NativeDecision: &req}, 35*time.Second)
	if err != nil {
		return NativeDecisionReceipt{}, err
	}
	if response.Op == OpError {
		return NativeDecisionReceipt{}, fmt.Errorf("native decision: %s (%s)", response.Error, response.ErrorCode)
	}
	receipt := response.NativeDecisionReceipt
	if response.Op != OpNativeDecide || response.OperationID != operationID || receipt == nil || !nativeDecisionReceiptMatches(*receipt, operationID, req) || !slices.Contains([]string{"admitted", "rejected", "uncertain"}, receipt.State) {
		_ = c.Close()
		return NativeDecisionReceipt{}, errors.New("native decision receipt identity is unconfirmed")
	}
	return *receipt, nil
}

func nativeDecisionReceiptMatches(receipt NativeDecisionReceipt, operationID string, req NativeDecisionReq) bool {
	return receipt.OperationID == operationID && receipt.Session == req.Session && receipt.SessionInstance == req.SessionInstance && receipt.RootID == req.RootID && receipt.Generation == req.Generation && receipt.NativeSessionID == req.NativeSessionID && receipt.RequestID == req.RequestID && receipt.Kind == req.Kind
}

func (cc *clientConn) handleNativeDecide(c Control) {
	if !cc.requireRemoteAuthz(c, ActionNativeDecide, c.SessionID, schema.NativeDecisionContentHash(c.NativeDecision)) {
		return
	}
	if !cc.hasCap(CapNativeRequests) {
		cc.replyErrorCode("native request decisions were not negotiated", CodeNotImplemented)
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	if c.NativeDecision == nil || c.NativeDecision.Session != c.SessionID || c.OperationID == "" || len(c.OperationID) > 128 || strings.IndexFunc(c.OperationID, unicode.IsControl) >= 0 {
		cc.replyErrorCode("native decision identity is invalid", CodeInvalidField)
		return
	}
	if err := ValidateNativeDecision(*c.NativeDecision); err != nil {
		cc.replyErrorCode(err.Error(), CodeInvalidField)
		return
	}
	if _, ok := cc.resolveSession(c); !ok {
		return
	}
	applier, ok := cc.srv.d.(NativeDecisionApplier)
	if !ok {
		cc.replyErrorCode("native request decisions are unsupported", CodeNotImplemented)
		return
	}
	receipt, code, err := applier.ApplyNativeDecision(cc.endpointID, c.OperationID, *c.NativeDecision)
	if err != nil && !(receipt.State == "rejected" && receipt.NativeEffect == "none" && nativeDecisionReceiptMatches(receipt, c.OperationID, *c.NativeDecision)) {
		cc.replyErrorCode("native decision: "+err.Error(), code)
		return
	}
	_ = cc.writeControl(Control{Op: OpNativeDecide, EndpointID: cc.endpointID, SessionID: c.SessionID, OperationID: c.OperationID, NativeDecisionReceipt: &receipt})
}
