package phonecore

import (
	"encoding/hex"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const maxNativeHistorySessions = 256

// NativeHistoryState belongs to the encrypted history cache, not the live journal cursor.
type NativeHistoryState struct {
	Scope              string `json:"scope,omitempty"`
	Cursor             string `json:"cursor,omitempty"`
	SessionInstance    string `json:"session_instance,omitempty"`
	PendingOperationID string `json:"pending_operation_id,omitempty"`
	AppliedOperationID string `json:"applied_operation_id,omitempty"`
	AppliedBucket      Bucket `json:"applied_bucket,omitempty"`
	AppliedSequence    uint64 `json:"applied_sequence,omitempty"`
}

func validNativeHistoryScope(scope string) bool {
	raw, err := hex.DecodeString(scope)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == scope
}
func validNativeHistoryState(states map[string]NativeHistoryState) bool {
	if len(states) > maxNativeHistorySessions {
		return false
	}
	for session, s := range states {
		if session == "" || len(session) > 256 || !utf8.ValidString(session) || (s.Scope != "" && !validNativeHistoryScope(s.Scope)) || len(s.Cursor) > schema.MaxNativeHistoryCursorBytes || !utf8.ValidString(s.Cursor) || len(s.SessionInstance) > 256 || len(s.PendingOperationID) > 256 || len(s.AppliedOperationID) > 256 || (s.Scope == "" && s.Cursor != "") {
			return false
		}
	}
	return true
}
func nativeHistoryInstance(st State, session string) string {
	for _, s := range st.Sessions {
		if s.SessionID == session && s.Capabilities != nil {
			return s.Capabilities.SessionInstance
		}
	}
	return ""
}
func nativeHistoryAvailable(st State, session string) bool {
	for _, s := range st.Sessions {
		if s.SessionID == session && s.Capabilities != nil && s.Capabilities.Provider == "opencode" && s.Capabilities.NativeInteractionVersion == 2 {
			return true
		}
	}
	for _, item := range st.Items {
		if item.SessionID == session {
			if _, ok := decodeNativeItem(item.Body); ok {
				return true
			}
		}
	}
	return false
}

// HistoryRequest selects the existing native history cursor only for proven native content.
func (c *Core) HistoryRequest(session, before string, limit int) schema.InteractionHistoryReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := schema.InteractionHistoryReq{Session: session, BeforeItem: before, Limit: limit, Native: nativeHistoryAvailable(c.st, session)}
	saved := c.st.NativeHistory[session]
	if req.Native && before != "" && saved.SessionInstance == nativeHistoryInstance(c.st, session) {
		req.NativeCursor = saved.Cursor
	}
	return req
}
func validNativeHistoryPage(page *schema.NativeHistoryPage) bool {
	if page == nil || page.Version != 2 || !validNativeHistoryScope(page.Scope) || page.Items == nil || len(page.Items) > schema.MaxNativeHistoryItems || len(page.NextCursor) > schema.MaxNativeHistoryCursorBytes || !utf8.ValidString(page.NextCursor) || (page.Floor && page.NextCursor != "") || (!page.Floor && page.NextCursor == "") {
		return false
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > schema.MaxNativeHistoryPageBytes {
		return false
	}
	ids := map[string]string{}
	for _, raw := range page.Items {
		w, ok := decodeNativeItem(raw)
		if !ok || w.Provenance != "imported" || w.OperationID != "" || (ids[w.ItemID] != "" && ids[w.ItemID] != w.Kind) || len(w.ItemID) > 256 {
			return false
		}
		switch w.Kind {
		case "message_snapshot", "tool_snapshot", "file_snapshot", "plan_snapshot":
		default:
			return false
		}
		switch w.Source {
		case "", "native", "derived":
		default:
			return false
		}
		var proof struct {
			Hash string `json:"input_sha256"`
		}
		if json.Unmarshal(raw, &proof) != nil || proof.Hash != "" {
			return false
		}
		ids[w.ItemID] = w.Kind
	}
	return true
}
func nativeHistoryReplyCurrent(st State, p PendingPublication, page *schema.NativeHistoryPage) bool {
	if p.History == nil || !p.History.Native || !validNativeHistoryPage(page) {
		return false
	}
	saved := st.NativeHistory[p.SessionID]
	if saved.PendingOperationID != p.OperationID || p.SessionInstance != nativeHistoryInstance(st, p.SessionID) {
		return false
	}
	if p.History.NativeCursor != "" && (p.History.NativeCursor != saved.Cursor || p.NativeHistoryScope != saved.Scope || page.Scope != saved.Scope) {
		return false
	}
	return p.History.BeforeItem == "" || p.NativeHistoryScope == "" || page.Scope == p.NativeHistoryScope
}
func (s *ItemStore) applyNativeHistory(session string, page *schema.NativeHistoryPage, reset bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	shadow := &ItemStore{items: append([]Item(nil), s.items...)}
	if reset {
		kept := shadow.items[:0]
		for _, item := range shadow.items {
			if item.SessionID == session && item.Cursor == 0 && item.LastCursor == 0 && item.Backfilled && nativeKind(item.Kind) {
				continue
			}
			kept = append(kept, item)
		}
		shadow.items = kept
	}
	for _, raw := range page.Items {
		w, _ := decodeNativeItem(raw)
		if i := shadow.indexOf(session, w.ItemID); i >= 0 {
			old := shadow.items[i]
			if old.Cursor > 0 || old.LastCursor > 0 || !old.Backfilled || old.TSUnixMs >= w.TS.UnixMilli() {
				continue
			}
			shadow.items = append(shadow.items[:i:i], shadow.items[i+1:]...)
		}
		shadow.applyNativeLocked(schema.JournalRecord{Type: RecordTypeInteraction, SessionID: session, Item: raw}, true)
	}
	if shadow.backfillHeldLocked(session) > MaxBackfillPerSession {
		return false
	}
	sort.SliceStable(shadow.items, func(i, j int) bool {
		a, b := shadow.items[i], shadow.items[j]
		if a.Cursor != b.Cursor {
			return a.Cursor < b.Cursor
		}
		return a.Cursor == 0 && a.TSUnixMs < b.TSUnixMs
	})
	s.items = shadow.items
	return true
}
