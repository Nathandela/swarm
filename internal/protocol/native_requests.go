package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const OpNativeRequests = "native_requests"

type (
	NativeRequestsReq    = schema.NativeRequestsReq
	NativeRequestsReply  = schema.NativeRequestsReply
	NativeRequestSummary = schema.NativeRequestSummary
)
type NativeRequestReader interface {
	NativeRequests(local, requestID, nativeSessionID string) (NativeRequestsReply, error)
}

func validNativeRequestSelectors(req NativeRequestsReq) bool {
	return req.Session != "" && len(req.Session) <= 256 && (req.RequestID == "") == (req.NativeSessionID == "") && len(req.RequestID) <= 128 && len(req.NativeSessionID) <= 128
}

func (c *Client) NativeRequests(req NativeRequestsReq) (NativeRequestsReply, error) {
	if !slices.Contains(c.caps, CapNativeRequests) || !validNativeRequestSelectors(req) {
		return NativeRequestsReply{}, errors.New("native request read is unnegotiated or has invalid selectors")
	}
	response, err := c.requestWithTimeout(Control{Op: OpNativeRequests, EndpointID: c.endpointID, SessionID: req.Session, NativeRequests: &req}, 10*time.Second)
	if err != nil {
		return NativeRequestsReply{}, err
	}
	if response.Op == OpError {
		return NativeRequestsReply{}, fmt.Errorf("native requests: %s (%s)", response.Error, response.ErrorCode)
	}
	result := response.NativeRequestsResult
	if response.Op != OpNativeRequests || result == nil || result.Session != req.Session || len(result.Requests) > 128 {
		return NativeRequestsReply{}, errors.New("native request read identity is unconfirmed")
	}
	if req.RequestID != "" {
		card := result.Request
		if card == nil || card.Session != req.Session || card.RequestID != req.RequestID || card.NativeSessionID != req.NativeSessionID {
			return NativeRequestsReply{}, errors.New("native request card identity is unconfirmed")
		}
		encoded, _ := json.Marshal(card)
		if len(encoded) > schema.MaxNativeRequestViewBytes {
			return NativeRequestsReply{}, errors.New("native request card exceeds bound")
		}
	}
	return *result, nil
}

func (cc *clientConn) handleNativeRequests(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("native_requests is available only on the owner socket", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapNativeRequests) {
		cc.replyErrorCode("native request reads were not negotiated", CodeNotImplemented)
		return
	}
	if c.NativeRequests == nil || c.NativeRequests.Session != c.SessionID || !validNativeRequestSelectors(*c.NativeRequests) {
		cc.replyErrorCode("native request selectors are invalid", CodeInvalidField)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	reader, ok := cc.srv.d.(NativeRequestReader)
	if !ok {
		cc.replyErrorCode("native request reads are unsupported", CodeNotImplemented)
		return
	}
	result, err := reader.NativeRequests(local, c.NativeRequests.RequestID, c.NativeRequests.NativeSessionID)
	if err != nil {
		cc.replyErrorCode("native requests: "+err.Error(), CodeUnavailable)
		return
	}
	result.Session = c.SessionID
	if len(result.Requests) > 128 {
		cc.replyErrorCode("native request summary exceeds bound", CodeUnavailable)
		return
	}
	if result.Request != nil {
		encoded, _ := json.Marshal(result.Request)
		if len(encoded) > schema.MaxNativeRequestViewBytes || result.Request.Session != c.SessionID || result.Request.RequestID != c.NativeRequests.RequestID || result.Request.NativeSessionID != c.NativeRequests.NativeSessionID {
			cc.replyErrorCode("native request card is unavailable", CodeUnavailable)
			return
		}
	}
	_ = cc.writeControl(Control{Op: OpNativeRequests, EndpointID: cc.endpointID, SessionID: c.SessionID, NativeRequestsResult: &result})
}
