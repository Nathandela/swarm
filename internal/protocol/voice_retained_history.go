package protocol

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type RetainedHistoryPage struct {
	Records []JournalRecord
	Floor   bool
}

// RetainedHistoryPage reads the bounded structured journal, never a native CLI history export.
func (c *Client) RetainedHistoryPage(req schema.InteractionHistoryReq) (RetainedHistoryPage, error) {
	endpoint, local, valid := ParseID(req.Session)
	if !valid || endpoint != c.endpointID {
		return RetainedHistoryPage{}, errors.New("protocol: retained history requires current endpoint identity")
	}
	if !slices.Contains(c.caps, CapJournal) || req.Session == "" || req.Native || req.NativeCursor != "" || req.Limit <= 0 || req.Limit > 100 || len(req.BeforeItem) > 128 {
		return RetainedHistoryPage{}, errors.New("protocol: invalid retained history request")
	}
	reply, err := c.requestWithTimeout(Control{Op: OpInteractionHistory, EndpointID: c.endpointID, SessionID: req.Session, History: &req}, 10*time.Second)
	if err != nil {
		return RetainedHistoryPage{}, err
	}
	if reply.Op != OpInteractionHistory || reply.SessionID != req.Session || reply.NativeHistory != nil {
		return RetainedHistoryPage{}, errors.New("protocol: retained history unavailable or identity unconfirmed")
	}
	encoded, err := json.Marshal(reply.Journal)
	if err != nil || len(encoded) > schema.MaxNativeHistoryPageBytes {
		return RetainedHistoryPage{}, errors.New("protocol: retained history exceeds bounded source page")
	}
	var previous uint64
	for _, record := range reply.Journal {
		if record.Cursor == 0 || record.Cursor <= previous {
			return RetainedHistoryPage{}, errors.New("protocol: retained history order is unconfirmed")
		}
		previous = record.Cursor
		if record.SessionID != req.Session && record.SessionID != local {
			return RetainedHistoryPage{}, errors.New("protocol: retained history contains foreign discussion")
		}
	}
	return RetainedHistoryPage{Records: reply.Journal, Floor: reply.HistoryFloor}, nil
}
