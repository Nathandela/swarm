package protocol

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func (c *Client) NativeInteractionHistory(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	if !req.Native || req.Limit <= 0 || req.Limit > schema.MaxNativeHistoryItems || len(req.NativeCursor) > schema.MaxNativeHistoryCursorBytes {
		return schema.NativeHistoryPage{}, errors.New("protocol: invalid native history bounds")
	}
	reply, err := c.request(Control{Op: OpInteractionHistory, EndpointID: c.endpointID, SessionID: req.Session, History: &req})
	if err != nil {
		return schema.NativeHistoryPage{}, err
	}
	if reply.Op == OpError {
		return schema.NativeHistoryPage{}, fmt.Errorf("protocol: native history: %s", reply.Error)
	}
	if reply.Op != OpInteractionHistory || reply.SessionID != req.Session || reply.NativeHistory == nil || len(reply.Journal) != 0 {
		return schema.NativeHistoryPage{}, errors.New("protocol: native history identity unavailable")
	}
	page := *reply.NativeHistory
	scope, scopeErr := hex.DecodeString(page.Scope)
	raw, err := json.Marshal(page)
	if err != nil || len(raw) > schema.MaxNativeHistoryPageBytes || (page.Version != 1 && page.Version != 2) || len(page.Scope) != 64 || scopeErr != nil || len(scope) != 32 || hex.EncodeToString(scope) != page.Scope || len(page.Items) > req.Limit || len(page.NextCursor) > schema.MaxNativeHistoryCursorBytes {
		return schema.NativeHistoryPage{}, errors.New("protocol: native history reply exceeds contract")
	}
	return page, nil
}
