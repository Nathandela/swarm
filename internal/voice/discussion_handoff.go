package voice

import (
	"encoding/json"
	"sort"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
)

// The connector supplies fresh, attributed thread history. Only add receipt references
// here: old private tool output and deleted Discord messages must not reappear.
func discussionHandoff(st State, current Admission, input, provider, nonce string, vmAccess bool) string {
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
	type workerRef struct {
		Job      string `json:"job"`
		Attempt  string `json:"attempt"`
		Session  string `json:"session"`
		Instance string `json:"instance"`
	}
	var workers []workerRef
	for _, w := range st.Workers {
		if w.ConversationID != current.ConversationID || w.Reserved || w.Ended || w.JobID == "" || w.Session == "" || !vmAccess && w.Project != current.Project {
			continue
		}
		workers = append(workers, workerRef{w.JobID, w.AttemptID, w.Session, w.SessionInstance})
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Session < workers[j].Session })
	if len(workers) > 8 {
		workers = workers[:8]
	}
	workerJSON, _ := json.Marshal(workers)
	workerText := ""
	if len(workers) > 0 {
		workerText = "\nRetained worker sessions: " + string(workerJSON)
	}
	const guidance = "Manager handoff for this Discord discussion. Read earlier requests, replies, and edits with swarm_discussion_history. Inspect existing workers, their evidence, and action receipts. A settled action alone does not prove successful work. Never repeat an uncertain action. Resume outstanding work. Treat a restatement as a follow-up, carrying forward any new instruction. If history is incomplete, ask only for the missing detail."
	// Keep standing instructions, the recovery-only prefix and the full human input
	// byte-for-byte. Optional handoff information uses the remaining wire budget.
	for _, suffix := range []string{workerText + "\nRecent action references: " + string(refs), workerText, ""} {
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
