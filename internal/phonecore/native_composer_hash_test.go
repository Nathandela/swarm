package phonecore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestNativeComposerClippedEchoRequiresExactQualifiedFullInputHash(t *testing.T) {
	text := strings.Repeat("<", 4096)
	hash := sha256.Sum256([]byte(text))
	for _, tc := range []struct {
		name, hash, role, source, operation, provenance string
		delivered                                       bool
	}{
		{"exact", hex.EncodeToString(hash[:]), "user", "phone", "op-native", "live", true},
		{"no_hash", "", "user", "phone", "op-native", "live", false},
		{"wrong_hash", strings.Repeat("a", 64), "user", "phone", "op-native", "live", false},
		{"assistant", hex.EncodeToString(hash[:]), "assistant", "phone", "op-native", "live", false},
		{"owner", hex.EncodeToString(hash[:]), "user", "owner", "op-native", "live", false},
		{"other_operation", hex.EncodeToString(hash[:]), "user", "phone", "other-op", "live", false},
		{"imported", hex.EncodeToString(hash[:]), "user", "phone", "op-native", "imported", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"v": 2, "kind": "message_snapshot", "item_id": "native-user", "ts": time.Now().UTC(), "role": tc.role, "source": tc.source, "operation_id": tc.operation, "provenance": tc.provenance, "input_sha256": tc.hash, "text": text[:2048], "truncated": true})
			if err != nil {
				t.Fatal(err)
			}
			items := NewItemStore()
			if !items.Apply(schema.JournalRecord{Type: RecordTypeInteraction, SessionID: "m1/s1", Cursor: 1, Item: body}) {
				t.Fatal("clipped native snapshot was not folded")
			}
			p := testComposerPublication("op-native")
			p.Text, p.Composer.Text = text, text
			st := State{Items: items.Session("m1/s1"), PendingPublications: []PendingPublication{p}}
			settleComposerItems(&st)
			if delivered := len(st.PendingPublications) == 0; delivered != tc.delivered {
				t.Fatalf("clipped native phone send delivered=%v, want=%v", delivered, tc.delivered)
			}
		})
	}
}
