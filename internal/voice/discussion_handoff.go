package voice

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
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
	replies := priorDiscussionReplies(st, current, "")
	var replyText string
	if len(replies) != 0 {
		raw, _ := json.Marshal(replies)
		replyText = "\nPrior human messages: " + string(raw)
	}
	const guidance = "Manager handoff. Handle the current instruction. Read current Discord evidence with swarm_discussion_history. Use source receipt and a human message ID as anchor to verify its original revision. A scheduled final covers the original revision only. Address new requests and edits once. Never replay uncertain actions. A manager turn ending or reply being queued does not prove worker success or delivery. Inspect retained worker and action receipts for outstanding work."
	// Preserve the full current input. Prefer human reply dispositions when space is short.
	for _, suffix := range []string{replyText + workerText + "\nRecent action references: " + string(refs), replyText + workerText, replyText, ""} {
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

type discussionReplyRef struct {
	Message             string   `json:"message"`
	Reply               string   `json:"reply"`
	NativeTurnEnd       string   `json:"former_manager_turn_end,omitempty"`
	ConfirmedMessageIDs []string `json:"confirmed_discord_message_ids,omitempty"`
	publicationID       string
	seq                 uint64
	origin              *schema.MessageOrigin
}

func priorDiscussionReplies(st State, current Admission, anchor string) []discussionReplyRef {
	// Same-discussion publications establish intent. Delivery receipts belong to the connector.
	representative := map[string]Publication{}
	for _, p := range st.Publications {
		id := p.DiscussionSource.AdmissionID
		if p.ConversationID == current.ConversationID && id != "" && p.DiscussionSource.SystemCause == "" {
			// Keep the latest final. Progress is relevant only until a final exists.
			old, found := representative[id]
			if !found || p.Final && !old.Final || p.Final == old.Final && p.OutputSequence > old.OutputSequence {
				representative[id] = p
			}
		}
	}
	var replies []discussionReplyRef
	for _, a := range st.Admissions {
		if a.ConversationID != current.ConversationID || a.Sequence >= current.Sequence || a.Project != current.Project || a.Origin == nil || a.Origin.MessageID == "" || (anchor != "" && a.Origin.MessageID != anchor) {
			continue
		}
		reply := "outcome unknown"
		p, ok := representative[a.Origin.RequestID]
		if ok {
			reply = "progress scheduled"
			if p.Final {
				reply = "final scheduled"
			}
		}
		replies = append(replies, discussionReplyRef{Message: a.Origin.MessageID, Reply: reply, publicationID: p.ID, seq: a.Sequence, origin: a.Origin})
	}
	sort.Slice(replies, func(i, j int) bool { return replies[i].seq > replies[j].seq })
	if len(replies) > 8 {
		replies = replies[:8]
	}
	return replies
}

// Read one original human revision through the connector's live discussion and grant checks.
func (c *Controller) discussionReplyReceipt(ctx context.Context, st State, current Admission, anchor string) (json.RawMessage, error) {
	if c.discussion.VerifyGrant == nil || c.discussion.VerifyAudience == nil {
		return nil, ErrControllerUnsupported
	}
	if err := c.discussion.VerifyAudience(ctx); err != nil {
		return nil, err
	}
	refs := priorDiscussionReplies(st, current, anchor)
	if len(refs) == 0 {
		return nil, ErrNotFound
	}
	ref := refs[0]
	if !c.discussion.validateOrigin(*ref.origin) {
		return nil, ErrExpired
	}
	if err := c.discussion.VerifyGrant(ctx, *ref.origin); err != nil {
		return nil, err
	}
	if current.Project != "" && c.discussion.ProjectEnrolled != nil && !c.discussion.ProjectEnrolled(current.Project) {
		return nil, ErrExpired
	}
	if p, ok := st.Publications[key(current.ConversationID, ref.publicationID)]; ok && p.Exported && c.discussion.ConfirmedDelivery != nil {
		output := DiscussionOutput{DiscussionOutputSource: p.DiscussionSource, ID: p.ID, Sequence: p.OutputSequence, Text: p.Text}
		ids, err := c.discussion.ConfirmedDelivery(ctx, output)
		if err != nil {
			return nil, err
		}
		ref.ConfirmedMessageIDs = ids
	}
	ref.NativeTurnEnd = c.formerDiscussionTurnEnd(ctx, st.Admissions[admissionKey(current.ConversationID, ref.seq)])
	return controllerResult(ref)
}

// Only query the original authenticated native ledger. Terminal prose and drafts stay private.
func (c *Controller) formerDiscussionTurnEnd(ctx context.Context, a Admission) string {
	reader, ok := c.runtime.(interface {
		ManagerInputStatus(context.Context, protocol.ManagerRuntimeBinding, string) (protocol.ManagerNativeTurn, error)
	})
	if !ok || a.Origin == nil || a.NativeInputID == "" {
		return ""
	}
	var req protocol.ComposerSendReq
	if json.Unmarshal(a.Request, &req) != nil || req.ManagerAdmission == nil || req.ManagerAdmission.Validate() != nil || req.Origin == nil || *req.Origin != *a.Origin {
		return ""
	}
	old := req.ManagerAdmission.Binding
	if old.ConversationID != c.binding.ConversationID || old.Session != req.Session || old.SessionInstance != req.SessionInstance || old.Session == c.binding.SessionID || old.NativeSessionID == c.binding.NativeSessionID || req.ManagerAdmission.Nonce != a.NativeInputID {
		return ""
	}
	turn, err := reader.ManagerInputStatus(ctx, old, a.NativeInputID)
	if err != nil || turn.NativeInputID != a.NativeInputID || turn.State != "completed" || turn.NativeTurnID == "" || a.NativeTurnID != "" && a.NativeTurnID != turn.NativeTurnID || turn.ExternallySteered {
		return ""
	}
	switch turn.Reason {
	case "answer", "aborted", "error", "refusal":
		return turn.Reason
	default:
		return ""
	}
}
