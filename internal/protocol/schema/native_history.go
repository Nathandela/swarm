package schema

import "encoding/json"

const (
	MaxNativeHistoryCursorBytes = 8192
	MaxNativeHistoryItems       = 200
	MaxNativeHistoryPageBytes   = 512 << 10
)

// NativeHistoryPage contains imported replacement snapshots, never journal records.
type NativeHistoryPage struct {
	WorkerEvidenceLimited bool                `json:"worker_evidence_limited,omitempty"`
	Worker                *WorkerTurnEvidence `json:"worker,omitempty"`
	Version               int                 `json:"v"`
	Items                 []json.RawMessage   `json:"items"`
	NextCursor            string              `json:"next_cursor,omitempty"`
	Floor                 bool                `json:"floor"`
	Scope                 string              `json:"scope"`
}

// WorkerHistoryScope restricts an owner read to one launch prompt or admitted native turn.
type WorkerHistoryScope struct {
	ManagerConversation string `json:"manager_conversation"`
	SessionInstance     string `json:"session_instance"`
	Parent              string `json:"parent"`
	TurnID              string `json:"turn_id,omitempty"`
	PromptHash          string `json:"prompt_hash"`
}

type WorkerTurnEvidence struct {
	TextTruncated bool   `json:"text_truncated,omitempty"`
	TurnID        string `json:"turn_id"`
	State         string `json:"state"`
	Text          string `json:"text,omitempty"`
}
