package phonecore

import (
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func nativeKind(kind string) bool {
	switch kind {
	case "message_snapshot", "tool_snapshot", "file_snapshot", "plan_snapshot", "permission_request", "question_request", "input_outcome":
		return true
	}
	return false
}
func nativeRequestKind(kind string) bool {
	return kind == "permission_request" || kind == "question_request"
}

type nativeWireItem struct {
	wireItem
	Role           string                    `json:"role"`
	Provenance     string                    `json:"provenance"`
	AuthorityValid bool                      `json:"authority_valid"`
	Request        *schema.NativeRequestView `json:"request"`
}

func decodeNativeItem(raw []byte) (nativeWireItem, bool) {
	var w nativeWireItem
	if len(raw) > 16<<10 || json.Unmarshal(raw, &w) != nil || w.V != 2 || w.ItemID == "" || w.TS.IsZero() || !nativeKind(w.Kind) {
		return w, false
	}
	switch w.Provenance {
	case "live", "imported", "reconciled":
	default:
		return w, false
	}
	if w.Kind == "message_snapshot" && w.Role != "user" && w.Role != "assistant" {
		return w, false
	}
	return w, true
}

func (s *ItemStore) applyNativeLocked(rec schema.JournalRecord, backfill bool) bool {
	w, ok := decodeNativeItem(rec.Item)
	if !ok {
		return false
	}
	next := Item{SessionID: rec.SessionID, ItemID: w.ItemID, Cursor: rec.Cursor, LastCursor: rec.Cursor, Kind: w.Kind, Status: w.Status, TurnID: w.TurnID, TSUnixMs: w.TS.UnixMilli(), Text: w.Text, Truncated: w.Truncated, Detail: w.Detail, Source: w.Source, ToolKind: w.ToolKind, OperationID: w.OperationID, Body: append(json.RawMessage(nil), rec.Item...), Backfilled: backfill}
	if nativeRequestKind(w.Kind) {
		next.Resolved = w.Status != "in_progress"
		next.Detail = false
	}
	if i := s.indexOf(rec.SessionID, w.ItemID); i >= 0 {
		old := s.items[i]
		if rec.Cursor <= old.LastCursor || old.Kind != w.Kind {
			return false
		}
		next.Cursor, next.Backfilled = old.Cursor, old.Backfilled
		s.items[i] = next
	} else {
		s.insertLocked(next)
	}
	s.trimLocked(rec.SessionID)
	return true
}

func (s *ItemStore) applyNativeDetailLocked(rec schema.JournalRecord) bool {
	w, ok := decodeNativeItem(rec.Item)
	if !ok || nativeRequestKind(w.Kind) || w.Kind == "input_outcome" {
		return false
	}
	i := s.indexOf(rec.SessionID, w.ItemID)
	if i < 0 || s.items[i].Kind != w.Kind {
		return false
	}
	it := s.items[i]
	it.Text = w.Text
	it.Body = append(json.RawMessage(nil), rec.Item...)
	it.Truncated = w.Truncated
	it.Detail = w.Detail
	s.items[i] = it
	return true
}

// NativeRequestBinding is exact displayed native authority, never a selected latest request.
type NativeRequestBinding struct{ Request schema.NativeDecisionReq }

func (s *ItemStore) PendingNativeRequest(session, itemID string) (NativeRequestBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(session, itemID)
	if i < 0 {
		return NativeRequestBinding{}, false
	}
	item := s.items[i]
	if !nativeRequestKind(item.Kind) || item.Resolved || item.Truncated || item.Degraded {
		return NativeRequestBinding{}, false
	}
	w, ok := decodeNativeItem(item.Body)
	if !ok || !w.AuthorityValid || w.Truncated || w.Status != "in_progress" || w.Provenance == "imported" || w.Request == nil {
		return NativeRequestBinding{}, false
	}
	request := w.Request
	if request.Session != session || request.SessionInstance == "" || request.RootID == "" || request.Generation == "" || request.NativeSessionID == "" || request.RequestID == "" || request.ExpiresAt == nil || !time.Now().Before(*request.ExpiresAt) || len(request.RequestHash) != 64 {
		return NativeRequestBinding{}, false
	}
	hash, err := hex.DecodeString(request.RequestHash)
	if err != nil || hex.EncodeToString(hash) != request.RequestHash {
		return NativeRequestBinding{}, false
	}
	kind := "permission_reply"
	if item.Kind == "question_request" {
		if request.Kind != "question" {
			return NativeRequestBinding{}, false
		}
		kind = "question_reply"
	} else if request.Kind != "permission" {
		return NativeRequestBinding{}, false
	}
	expiry := *request.ExpiresAt
	return NativeRequestBinding{Request: schema.NativeDecisionReq{Session: session, SessionInstance: request.SessionInstance, RootID: request.RootID, Generation: request.Generation, NativeSessionID: request.NativeSessionID, RequestID: request.RequestID, RequestHash: request.RequestHash, ExpiresAt: &expiry, Kind: kind}}, true
}
