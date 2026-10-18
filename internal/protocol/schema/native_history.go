package schema

import "encoding/json"

const (
	MaxNativeHistoryCursorBytes = 8192
	MaxNativeHistoryItems       = 200
	MaxNativeHistoryPageBytes   = 512 << 10
)

// NativeHistoryPage contains imported replacement snapshots, never journal records.
type NativeHistoryPage struct {
	Version    int               `json:"v"`
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Floor      bool              `json:"floor"`
	Scope      string            `json:"scope"`
}
