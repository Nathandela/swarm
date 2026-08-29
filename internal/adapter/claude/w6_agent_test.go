package claude

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// TestActionFor_ClassifiesEachToolByName — the §7 classification per Claude tool name, as a
// table, with the phone-refit-playbook W6.1 row: `Task` is `agent`. The Task arm reads only
// the tool NAME, which every PreToolUse payload carries, so unlike the R6 arms it needs no
// recorded corpus to be honest about -- it surfaces no argument. An unknown tool stays
// IS-TOOL-2's `other`.
func TestActionFor_ClassifiesEachToolByName(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"Bash", "execute"},
		{"Read", "read"},
		{"Edit", "edit"},
		{"Write", "write"},
		{"Grep", "search"},
		{"Glob", "search"},
		{"WebFetch", "fetch"},
		{"Task", "agent"},
		{"Telepathy", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			got := actionFor(tc.tool, toolInput{})
			if got.Type != tc.want {
				t.Errorf("actionFor(%q).Type = %q, want %q", tc.tool, got.Type, tc.want)
			}
			if err := (adapter.Interaction{Kind: adapter.KindToolRun, Tool: tc.tool, Action: got}).Validate(); err != nil {
				t.Errorf("the %q classification does not validate against the wire vocabulary: %v", tc.tool, err)
			}
		})
	}
}
