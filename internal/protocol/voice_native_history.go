package protocol

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// NativeHistoryPage reads only the native source; an absent page never falls back to journal history.
func (c *Client) NativeHistoryPage(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	if !slices.Contains(c.caps, CapJournal) || req.Session == "" || !req.Native || req.Limit <= 0 || req.Limit > schema.MaxNativeHistoryItems || len(req.NativeCursor) > schema.MaxNativeHistoryCursorBytes || req.BeforeItem != "" {
		return schema.NativeHistoryPage{}, errors.New("protocol: invalid native history page request")
	}
	reply, err := c.requestWithTimeout(Control{Op: OpInteractionHistory, EndpointID: c.endpointID, SessionID: req.Session, History: &req}, 10*time.Second)
	if err != nil {
		return schema.NativeHistoryPage{}, err
	}
	if reply.Op != OpInteractionHistory || reply.SessionID != req.Session || reply.NativeHistory == nil || len(reply.Journal) != 0 {
		return schema.NativeHistoryPage{}, errors.New("protocol: native history unavailable or identity unconfirmed")
	}
	page := *reply.NativeHistory
	if req.Worker != nil {
		if page.Version != 1 || page.Scope != "authorized_worker_turn" || !page.Floor || (page.WorkerEvidenceLimited && page.Worker != nil) || page.NextCursor != "" || len(page.Items) != 0 {
			return schema.NativeHistoryPage{}, errors.New("protocol: invalid scoped worker history response")
		}
		if proof := page.Worker; proof != nil {
			if proof.TurnID == "" || (req.Worker.TurnID != "" && req.Worker.TurnID != proof.TurnID) || len(proof.Text) > 4096 || !utf8.ValidString(proof.Text) {
				return schema.NativeHistoryPage{}, errors.New("protocol: invalid scoped worker evidence")
			}
			switch proof.State {
			case "completed", "failed", "interrupted":
			default:
				return schema.NativeHistoryPage{}, errors.New("protocol: invalid worker turn state")
			}
		}
		return page, nil
	}
	if page.Worker != nil || page.WorkerEvidenceLimited {
		return schema.NativeHistoryPage{}, errors.New("protocol: unexpected worker evidence")
	}
	scope, scopeErr := hex.DecodeString(page.Scope)
	if scopeErr != nil || len(scope) != 32 || hex.EncodeToString(scope) != page.Scope || page.Items == nil || !utf8.ValidString(page.NextCursor) || (page.Floor && page.NextCursor != "") || (!page.Floor && page.NextCursor == "") {
		return schema.NativeHistoryPage{}, errors.New("protocol: invalid native history page binding")
	}
	raw, err := json.Marshal(page)
	if err != nil || len(raw) > schema.MaxNativeHistoryPageBytes || len(page.Items) > req.Limit || len(page.NextCursor) > schema.MaxNativeHistoryCursorBytes || page.Version != 2 {
		return schema.NativeHistoryPage{}, errors.New("protocol: invalid native history page response")
	}
	return page, nil
}
