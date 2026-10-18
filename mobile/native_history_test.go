package swarmmobile

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/phonecore"
	"testing"
	"time"
)

func TestNativeHistoryBackfillVisibleOnlyFromTranscriptOrigin(t *testing.T) {
	a := approveApp(t)
	raw, _ := json.Marshal(map[string]any{"v": 2, "kind": "message_snapshot", "item_id": "native-old", "ts": time.Now().UTC(), "role": "assistant", "provenance": "imported", "text": "old native words"})
	if err := a.core.Mutate(func(st *phonecore.State) {
		st.Items = []phonecore.Item{{SessionID: "m1/s1", ItemID: "native-old", Kind: "message_snapshot", Text: "old native words", Backfilled: true, Body: raw}}
	}); err != nil {
		t.Fatal(err)
	}
	page, err := a.ReadTranscript("m1/s1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := page.Count(); n != 1 {
		t.Fatal("cursorless native history disappeared at from0")
	}
	if next, _ := page.NextCursor(); next != 0 {
		t.Fatal("native history invented journal highwater")
	}
	page, err = a.ReadTranscript("m1/s1", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := page.Count(); n != 0 {
		t.Fatal("positive journal page replayed cursorless native history")
	}
}
