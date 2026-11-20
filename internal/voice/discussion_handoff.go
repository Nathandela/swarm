package voice

import (
	"encoding/json"
	"sort"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
)

// The connector supplies fresh, attributed thread history. Only add receipt references
// here: old private tool output and deleted Discord messages must not reappear.
func discussionHandoff(st State, current Admission, input, provider, nonce string) string {
	type receipt struct {
		Action    string `json:"action"`
		State     string `json:"state"`
		Admission uint64 `json:"admission"`
	}
	var recent []receipt
	for _, action := range st.Actions {
		if action.ConversationID != current.ConversationID || action.Admission >= current.Sequence {
			continue
		}
		source, ok := st.Admissions[admissionKey(current.ConversationID, action.Admission)]
		if !ok || source.Project != current.Project {
			continue
		}
		recent = append(recent, receipt{action.ID, action.State, action.Admission})
	}
	sort.Slice(recent, func(i, j int) bool {
		if recent[i].Admission != recent[j].Admission {
			return recent[i].Admission > recent[j].Admission
		}
		return recent[i].Action < recent[j].Action
	})
	if len(recent) > 8 {
		recent = recent[:8]
	}
	refs, _ := json.Marshal(recent)
	const guidance = "Manager handoff for this Discord discussion. Use swarm_discussion_history for earlier messages, including the former manager’s replies and questions. Use swarm_list to locate the existing worker. Check action receipts with swarm_status before continuing. A settled action alone does not prove successful work. Never repeat an uncertain action. If the human’s request permits a helper and the history is lengthy, a Luna or Haiku worker can summarize a quoted excerpt from this discussion. Check its summary against the receipts."
	// Keep standing instructions, the recovery-only prefix and the full human input
	// byte-for-byte. Optional handoff information uses the remaining wire budget.
	for _, suffix := range []string{"\nRecent action references: " + string(refs), ""} {
		text := input + "\n\n" + guidance + suffix
		if len(text) > protocol.MaxSendInputText {
			continue
		}
		if provider == "claude" {
			if _, err := claudeinput.ManagerEnvelope(nonce, text); err != nil {
				continue
			}
		}
		return text
	}
	return input
}
