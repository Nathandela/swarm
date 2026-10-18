package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeHistoryKeepsLegacyWireAndIndependentCursorlessItems(t *testing.T) {
	legacy := InteractionHistoryReq{Session: "m/s", Limit: 20}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"session":"m/s","before_item":"","limit":20}` {
		t.Fatalf("legacy read wire changed: %s", raw)
	}
	request := InteractionHistoryReq{Session: "m/s", Limit: 20, Native: true, NativeCursor: "opaque-native-cursor"}
	reply := Control{History: &request, NativeHistory: &NativeHistoryPage{Version: 2, Scope: strings.Repeat("a", 64), Items: []json.RawMessage{json.RawMessage(`{"v":2,"kind":"message_snapshot","provenance":"imported","item_id":"native"}`)}, NextCursor: "next"}}
	raw, err = json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var got Control
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.History.Native || got.History.NativeCursor != request.NativeCursor || got.NativeHistory == nil || got.NativeHistory.NextCursor != "next" || len(got.Journal) != 0 {
		t.Fatalf("native page became journal cursors: %s", raw)
	}
}
