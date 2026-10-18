package opencode

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

func TestNativeConversationIDValidation(t *testing.T) {
	validator, ok := adapter.AsConversationIDValidator(New())
	if !ok {
		t.Fatal("OpenCode must validate its native conversation IDs")
	}
	for _, id := range []string{
		"ses_eb0e24fcfffezuXlKs7DaFmVfm",
		"ses_0123456789abcdef0123456789abcdef",
	} {
		if !validator.IsValidConversationID(id) {
			t.Errorf("characterized native ID rejected: %q", id)
		}
	}
	for _, id := range []string{
		"", "ses_abcdefghij", "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b",
		"ses_0123456789ABCDEF0123456789ABCDEF", "ses_" + strings.Repeat("a", 129),
		"ses_eb0e24fcfffezuXlKs7DaFmVfm/../other", "ses_eb0e24fcfffezuXlKs7DaFmVfm\n",
		"ses_eb0e24fcfffezuXlKs7DaFmVfµ", "msg_eb0e24fcfffezuXlKs7DaFmVfm",
	} {
		if validator.IsValidConversationID(id) {
			t.Errorf("unsafe native ID accepted: %q", id)
		}
	}
}
