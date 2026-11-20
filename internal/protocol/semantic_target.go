package protocol

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

var ErrSemanticTargetNotNegotiated = errors.New("protocol: semantic target lookup was not negotiated")

type semanticTargetSource interface {
	ResolveSemanticTarget(local string) (string, ErrorCode, error)
}

// SemanticTarget resolves an exact owner target without submitting input.
func (c *Client) SemanticTarget(session string) (string, error) {
	if !slices.Contains(c.caps, CapSemanticTarget) {
		return "", ErrSemanticTargetNotNegotiated
	}
	r, err := c.requestWithTimeout(Control{Op: OpSemanticTarget, EndpointID: c.endpointID, SessionID: session}, 3*time.Second)
	if err != nil {
		return "", err
	}
	if r.Op == OpError {
		return "", fmt.Errorf("protocol: semantic target: %s (%s)", r.Error, r.ErrorCode)
	}
	if r.Op != OpSemanticTarget || r.EndpointID != c.endpointID || r.SessionID != session || r.SemanticTargetInstance == "" {
		_ = c.Close()
		return "", errors.New("protocol: semantic target identity is unconfirmed")
	}
	return r.SemanticTargetInstance, nil
}

func (cc *clientConn) handleSemanticTarget(c Control) {
	if cc.srv.remoteTier || cc.srv.completionTier {
		cc.replyErrorCode("semantic_target is available only on the owner socket", CodeNotAuthorized)
		return
	}
	source, ok := cc.srv.d.(semanticTargetSource)
	if !ok || !cc.hasCap(CapSemanticTarget) {
		cc.replyErrorCode("semantic target lookup was not negotiated", CodeNotImplemented)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	instance, code, err := source.ResolveSemanticTarget(local)
	if err != nil {
		cc.replyErrorCode(err.Error(), code)
		return
	}
	_ = cc.writeControl(Control{Op: OpSemanticTarget, EndpointID: cc.endpointID, SessionID: c.SessionID, SemanticTargetInstance: instance})
}
