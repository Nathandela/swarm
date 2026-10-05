package accountcheck

import (
	"strings"
	"testing"
)

func TestIsolatedClaudeWorkerDoesNotUseDiscussionDiagnosticsOrRecoveryPolicy(t *testing.T) {
	actual := isolatedEnvironment([]string{"HOME=/ordinary-owner", "CLAUDE_CODE_DIAGNOSTICS_FILE=/discussion/private-diagnostics", "CLAUDE_CODE_NO_MODEL_FALLBACK=true", "CLAUDE_CODE_MAX_RETRIES=7"})
	for _, entry := range actual {
		if strings.HasPrefix(entry, "CLAUDE_CODE_DIAGNOSTICS_FILE=") || strings.HasPrefix(entry, "CLAUDE_CODE_NO_MODEL_FALLBACK=") {
			t.Fatal("quota worker inherited discussion-owned diagnostic/recovery policy")
		}
	}
	if !strings.Contains(strings.Join(actual, "\n"), "CLAUDE_CODE_MAX_RETRIES=0") {
		t.Fatal("bounded worker retry policy lost")
	}
}
