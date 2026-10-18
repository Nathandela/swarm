package phonecore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestNativeComposerSettlementRequiresExactQualifiedPhoneUserSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, role, provenance, source, operation, text string
		truncated, delivered                            bool
	}{
		{"exact_live", "user", "live", "phone", "op-native", "hello", false, true},
		{"exact_reconciled", "user", "reconciled", "phone", "op-native", "hello", false, true},
		{"imported", "user", "imported", "phone", "op-native", "hello", false, false},
		{"assistant", "assistant", "live", "phone", "op-native", "hello", false, false},
		{"owner", "user", "live", "owner", "op-native", "hello", false, false},
		{"other_operation", "user", "live", "phone", "op-other", "hello", false, false},
		{"other_text", "user", "live", "phone", "op-native", "different", false, false},
		{"truncated", "user", "live", "phone", "op-native", "hello", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"v": 2, "kind": "message_snapshot", "item_id": "native-user", "ts": time.Now().UTC(), "role": tc.role, "provenance": tc.provenance, "source": tc.source, "operation_id": tc.operation, "text": tc.text, "truncated": tc.truncated})
			if err != nil {
				t.Fatal(err)
			}
			items := NewItemStore()
			if !items.Apply(schema.JournalRecord{Type: RecordTypeInteraction, SessionID: "m1/s1", Cursor: 1, Item: body}) {
				t.Fatal("test snapshot was not accepted by native fold")
			}
			p := testComposerPublication("op-native")
			p.Text, p.Composer.Text = "hello", "hello"
			st := State{Items: items.Session("m1/s1"), PendingPublications: []PendingPublication{p}}
			settleComposerItems(&st)
			if delivered := len(st.PendingPublications) == 0; delivered != tc.delivered {
				t.Fatalf("native echo delivered=%v, want=%v", delivered, tc.delivered)
			}
		})
	}
}
