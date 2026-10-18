package protocol

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const semanticReplayWindow = time.Hour
const semanticClockSkew = 30 * time.Second

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
