package hermes

import (
	"strings"
	"testing"
)

func TestModernNativeIdentityPreservesClassicResume(t *testing.T) {
	for _, id := range []string{"local-" + strings.Repeat("a", 64), "20260829_140000_abcdef"} {
		if !New().(interface{ IsValidConversationID(string) bool }).IsValidConversationID(id) {
			t.Fatalf("valid Hermes identity refused: %s", id)
		}
	}
	for _, id := range []string{"local-" + strings.Repeat("a", 63), "local-" + strings.Repeat("A", 64), "local-" + strings.Repeat("a", 63) + "/"} {
		if New().(interface{ IsValidConversationID(string) bool }).IsValidConversationID(id) {
			t.Fatalf("invalid identity accepted: %q", id)
		}
	}
}
