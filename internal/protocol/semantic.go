package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const semanticReplayWindow = time.Hour
const semanticClockSkew = 30 * time.Second

var ErrSemanticSendRejected = errors.New("semantic message rejected without a new effect")

// NewSemanticOperationID creates an owner operation ID to persist before sending.
func NewSemanticOperationID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("semantic:%d:%x", time.Now().UnixMilli(), nonce), nil
}

// SemanticSend submits one incarnation-bound message; a receipt is not model completion.
func (c *Client) SemanticSend(operationID string, req ComposerSendReq) error {
	if !slices.Contains(c.caps, CapSemanticSend) {
		return errors.New("protocol: semantic messages require an updated daemon; run swarm daemon restart")
	}
	if !validSemanticOperationID(operationID, time.Now()) {
		return errors.New("protocol: invalid or expired semantic operation ID; an earlier attempt may still have an effect")
	}
	resp, err := c.requestWithTimeout(Control{Op: OpSemanticSend, EndpointID: c.endpointID,
		SessionID: req.Session, OperationID: operationID, BodyVersion: 1, ComposerSend: &req}, 35*time.Second)
	if err != nil {
		return err
	}
	if resp.Op == OpError {
		if resp.ErrorCode == CodeInputBusy || resp.ErrorCode == CodeStaleTurn || resp.ErrorCode == CodeStaleInstance || resp.ErrorCode == CodeStructuredUnsupported {
			return fmt.Errorf("%w: %s (%s)", ErrSemanticSendRejected, resp.Error, resp.ErrorCode)
		}
		return fmt.Errorf("protocol: semantic message: %s (%s)", resp.Error, resp.ErrorCode)
	}
	if resp.Op != OpOK || resp.OperationID != operationID {
		_ = c.Close()
		return errors.New("protocol: semantic message received an uncorrelated receipt; outcome unknown")
	}
	return nil
}

func validSemanticOperationID(id string, now time.Time) bool {
	if len(id) > 128 {
		return false
	}
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "semantic" || len(parts[1]) > 13 || len(parts[2]) != 32 {
		return false
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ms <= 0 || strconv.FormatInt(ms, 10) != parts[1] {
		return false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || strings.ToLower(parts[2]) != parts[2] {
		return false
	}
	issued := time.UnixMilli(ms)
	return !issued.After(now.Add(semanticClockSkew)) && now.Sub(issued) <= semanticReplayWindow
}

func (cc *clientConn) handleSemanticSend(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("semantic_send is available only on the owner socket", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapSemanticSend) {
		cc.replyErrorCode("semantic_send capability was not negotiated", CodeNotImplemented)
		return
	}
	if !validSemanticOperationID(c.OperationID, cc.srv.now()) {
		cc.replyErrorCode("semantic_send requires a timestamped operation ID within the one-hour replay window", CodeInvalidField)
		return
	}
	cc.handleComposerSend(c)
}
