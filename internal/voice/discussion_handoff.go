package voice

import (
	"context"
	"encoding/json"
	"errors"
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

type discussionReportRef struct {
	Job                 string   `json:"job"`
	Attempt             string   `json:"attempt"`
	ConfirmedMessageIDs []string `json:"confirmed_discord_message_ids,omitempty"`
	publicationID       string
}

type discussionReplyRef struct {
	LatestWorkerReport  *discussionReportRef `json:"latest_scheduled_worker_report,omitempty"`
	Message             string               `json:"message"`
	Reply               string               `json:"reply"`
	NativeTurnEnd       string               `json:"former_manager_turn_end,omitempty"`
	ConfirmedMessageIDs []string             `json:"confirmed_discord_message_ids,omitempty"`
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
	reports := make(map[string]Publication)
	pendingTargets := make(map[string]bool)
	for _, a := range st.Actions {
		if a.ConversationID == current.ConversationID && a.State != "settled" {
			pendingTargets[a.Target] = true
		}
	}
	for _, p := range st.Publications {
		if p.ConversationID != current.ConversationID || !p.Final || p.DiscussionSource.AttemptID == "" || p.DiscussionSource.SystemCause != "" {
			continue
		}
		id := workerReportOrigin(st, p, current.Project, pendingTargets)
		if id != "" && (reports[id].ID == "" || p.OutputSequence > reports[id].OutputSequence) {
			reports[id] = p
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
		ref := discussionReplyRef{Message: a.Origin.MessageID, Reply: reply, publicationID: p.ID, seq: a.Sequence, origin: a.Origin}
		if report, ok := reports[a.Origin.RequestID]; ok {
			ref.LatestWorkerReport = &discussionReportRef{Job: report.DiscussionSource.JobID, Attempt: report.DiscussionSource.AttemptID, publicationID: report.ID}
		}
		replies = append(replies, ref)
	}
	sort.Slice(replies, func(i, j int) bool { return replies[i].seq > replies[j].seq })
	if len(replies) > 8 {
		replies = replies[:8]
	}
	return replies
}

// Recover only an unsteered, scoped launch or resume with an exact wake and worker incarnation.
// VM shell launches retain discussion parentage but have no durable human admission grant.
func workerReportOrigin(st State, p Publication, project string, pending map[string]bool) string {
	src := p.DiscussionSource
	wake := st.Wakes[key(p.ConversationID, src.AdmissionID)]
	event := st.Events[key(p.ConversationID, wake.EventID)]
	if wake.ID == "" || wake.Admission != p.Admission || event.WakeID != wake.ID || event.RecoveryOnly || event.JobID != src.JobID || event.AttemptID != src.AttemptID {
		return ""
	}
	var metadata struct{ Session, Instance, Kind string }
	if json.Unmarshal(event.Metadata, &metadata) != nil {
		return ""
	}
	target := key(metadata.Session, metadata.Instance)
	worker := st.Workers[key(p.ConversationID, target)]
	if worker.VMChild || worker.RecoveryRequired || worker.Project != project || worker.ActionID != src.AttemptID || worker.AttemptID != src.AttemptID || worker.JobID != src.JobID || pending[target] {
		return ""
	}
	nativeProof := event.WorkerProof && event.ID == discussionEvidenceID(worker)
	completionNote := event.WorkerMessage && metadata.Kind == "complete"
	if !nativeProof && !completionNote {
		return ""
	}
	launch := st.Actions[key(p.ConversationID, src.AttemptID)]
	var grant controllerActionRequest
	var receipt createdWorkerResult
	if launch.State != "settled" || launch.Admission != worker.Admission || json.Unmarshal(launch.Request, &grant) != nil || json.Unmarshal(launch.Receipt, &receipt) != nil || grant.Project != project || grant.Origin == nil || receipt.Session != worker.Session || receipt.SessionInstance != worker.SessionInstance {
		return ""
	}
	created := grant.Tool == "swarm_create" && receipt.State == "created"
	resumed := grant.Tool == "swarm_resume" && receipt.State == "resumed"
	if !created && !resumed {
		return ""
	}
	original := st.Admissions[admissionKey(p.ConversationID, launch.Admission)]
	if original.Origin == nil || *original.Origin != *grant.Origin || original.Project != project || !singleReportScope(p.DiscussionAttempts, src.AttemptID) || !singleReportScope(p.DiscussionProjects, project) {
		return ""
	}
	return original.Origin.RequestID
}

func singleReportScope(encoded, value string) bool {
	if encoded == "" {
		return true
	}
	var scope []string
	return json.Unmarshal([]byte(encoded), &scope) == nil && len(scope) == 1 && scope[0] == value
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
	delivery := func(id string) ([]string, error) {
		p, ok := st.Publications[key(current.ConversationID, id)]
		if !ok || !p.Exported || c.discussion.ConfirmedDelivery == nil {
			return nil, nil
		}
		return c.discussion.ConfirmedDelivery(ctx, DiscussionOutput{DiscussionOutputSource: p.DiscussionSource, ID: p.ID, Sequence: p.OutputSequence, Text: p.Text})
	}
	var err error
	if ref.ConfirmedMessageIDs, err = delivery(ref.publicationID); err != nil {
		return nil, err
	}
	if report := ref.LatestWorkerReport; report != nil {
		report.ConfirmedMessageIDs, err = delivery(report.publicationID)
		if errors.Is(err, ErrExpired) || errors.Is(err, ErrStale) {
			ref.LatestWorkerReport = nil
		} else if err != nil {
			return nil, err
		}
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
