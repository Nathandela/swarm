package remotegw

import (
	"encoding/json"
	"fmt"

	"github.com/Nathandela/swarm/internal/protocol"
)

// maxJournalReseedPlaintextBytes leaves enough room for the mailbox envelope (78 bytes),
// base64 expansion, and the relay's conservative 96-byte single-item JSON wrapper under
// its 1 MiB frame. 767 KiB yields a worst-case framed append below that limit with more
// than 1 KiB of headroom.
const maxJournalReseedPlaintextBytes = 767 << 10

type reseedInteractionHeader struct {
	ItemID        string `json:"item_id"`
	Kind          string `json:"kind"`
	InteractionID string `json:"interaction_id"`
}

// boundJournalReseed keeps a repair atomic by trimming only the oldest optional event
// records. The complete roster and final cursor are authority and are never reduced.
// Unresolved approval requests are mandatory even when older than the selected tail:
// without their original cursors the phone cannot display or resolve a question that still
// blocks the machine. If those mandatory facts alone cannot fit, fail instead of emitting
// an oversized frame or silently dropping authority.
func boundJournalReseed(rs protocol.JournalReseed) (protocol.JournalReseed, error) {
	if fits, err := fitsJournalReseed(rs); err != nil {
		return protocol.JournalReseed{}, err
	} else if fits {
		return rs, nil
	}
	required := unresolvedApprovalRecords(rs.Events)
	mandatory := rs
	mandatory.Events = reseedEventsFromFloor(rs.Events, len(rs.Events), required)
	mandatoryFits, err := fitsJournalReseed(mandatory)
	if err != nil {
		return protocol.JournalReseed{}, err
	}
	if !mandatoryFits {
		return protocol.JournalReseed{}, fmt.Errorf(
			"remotegw: roster and unresolved approvals exceed the atomic journal reseed limit of %d bytes",
			maxJournalReseedPlaintextBytes,
		)
	}

	// Predicate: a later floor includes fewer optional records and is therefore no
	// larger. Find the earliest floor that fits, preserving the newest possible tail.
	lo, hi := 0, len(rs.Events)
	for lo < hi {
		mid := (lo + hi) / 2
		candidate := rs
		candidate.Events = reseedEventsFromFloor(rs.Events, mid, required)
		fits, err := fitsJournalReseed(candidate)
		if err != nil {
			return protocol.JournalReseed{}, err
		}
		if fits {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	rs.Events = reseedEventsFromFloor(rs.Events, lo, required)
	return rs, nil
}

func fitsJournalReseed(rs protocol.JournalReseed) (bool, error) {
	body, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: rs})
	if err != nil {
		return false, fmt.Errorf("remotegw: marshal journal reseed: %w", err)
	}
	return len(body) <= maxJournalReseedPlaintextBytes, nil
}

func reseedEventsFromFloor(events []protocol.JournalRecord, floor int, required map[int]bool) []protocol.JournalRecord {
	out := make([]protocol.JournalRecord, 0, len(events)-floor+len(required))
	for i, rec := range events {
		if i >= floor || required[i] {
			out = append(out, rec)
		}
	}
	return out
}

func unresolvedApprovalRecords(events []protocol.JournalRecord) map[int]bool {
	pending := map[string]int{}
	for i, rec := range events {
		if rec.Type != "interaction" || len(rec.Item) == 0 {
			continue
		}
		var item reseedInteractionHeader
		if json.Unmarshal(rec.Item, &item) != nil {
			continue
		}
		switch item.Kind {
		case "approval_request":
			if item.ItemID != "" {
				key := rec.SessionID + "\x00" + item.ItemID
				pending[key] = i
			}
		case "approval_resolved":
			if item.InteractionID != "" {
				delete(pending, rec.SessionID+"\x00"+item.InteractionID)
			}
		}
	}
	required := make(map[int]bool, len(pending))
	for _, index := range pending {
		required[index] = true
	}
	return required
}
