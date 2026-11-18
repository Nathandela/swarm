package voice

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/protocol"
)

// Retained v1 agent text is a delta stream; fold whole items, never cut an increment or bridge a gap.
func retainedWorkerItems(records []protocol.JournalRecord) ([]json.RawMessage, string, error) {
	type folded struct {
		body      map[string]json.RawMessage
		text      strings.Builder
		kind      string
		index     int
		truncated bool
	}
	items := []json.RawMessage{}
	pending := map[string]*folded{}
	before := ""
	flush := func() error {
		for _, item := range pending {
			if item.kind == "agent_message" {
				raw, _ := json.Marshal(item.text.String())
				item.body["text"] = raw
				if item.truncated {
					item.body["truncated"] = json.RawMessage(`true`)
				}
			}
			raw, err := json.Marshal(item.body)
			if err != nil {
				return err
			}
			items[item.index] = raw
		}
		return nil
	}
	for _, record := range records {
		if record.Type == "structured_gap" {
			if err := flush(); err != nil {
				return nil, "", err
			}
			pending = map[string]*folded{}
			gap := record.Item
			if len(gap) == 0 {
				gap = json.RawMessage(`{"kind":"structured_gap","history_available":false}`)
			}
			items = append(items, gap)
			continue
		}
		if len(record.Item) == 0 {
			continue
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(record.Item, &body) != nil {
			return nil, "", ErrCorrupt
		}
		var id, kind string
		_ = json.Unmarshal(body["item_id"], &id)
		_ = json.Unmarshal(body["kind"], &kind)
		if before == "" && id != "" {
			before = id
		}
		if id == "" {
			items = append(items, record.Item)
			continue
		}
		item, exists := pending[id]
		if !exists {
			item = &folded{kind: kind, index: len(items)}
			pending[id] = item
			items = append(items, nil)
		} else if item.kind != kind {
			return nil, "", ErrCorrupt
		}
		if kind == "agent_message" {
			var text string
			if value, ok := body["text"]; ok && json.Unmarshal(value, &text) != nil {
				return nil, "", ErrCorrupt
			}
			item.text.WriteString(text)
			var truncated bool
			_ = json.Unmarshal(body["truncated"], &truncated)
			item.truncated = item.truncated || truncated
		}
		item.body = body
	}
	if err := flush(); err != nil {
		return nil, "", err
	}
	return items, before, nil
}
