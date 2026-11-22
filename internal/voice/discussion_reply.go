package voice

import (
	"context"
	"fmt"
	"time"
)

// Native completion and a Discord reply have independent receipts.
const discussionFirstReplyWait = 30 * time.Second
const discussionReplyWait = 2 * time.Minute
const replyMonitorCause = "dispatcher_reply_status"

func hasReplyObligation(st State) bool {
	for _, a := range st.Admissions {
		if a.ReplyCheckAfter != 0 {
			return true
		}
	}
	return false
}

type replyEvidence struct {
	output        DiscussionOutput
	final         bool
	answered      bool
	progress      uint64
	workerRunning bool
	expires       time.Time
}

func discussionReplyEvidence(st State, a Admission, now time.Time) replyEvidence {
	var evidence replyEvidence
	workers := 0
	for _, w := range st.Workers {
		if w.ConversationID == a.ConversationID && w.Admission == a.Sequence && w.JobID != "" {
			workers++
			if !w.Ended && !w.RecoveryRequired {
				evidence.workerRunning = true
			}
		}
	}
	for _, p := range st.Publications {
		if p.ConversationID != a.ConversationID {
			continue
		}
		src := p.DiscussionSource
		if src.AdmissionID != a.Origin.RequestID {
			// Only a single proven scoped worker can speak for the original admission.
			if workers != 1 || !p.Final || src.JobID == "" || !replyMatchesWorker(st, a, p) {
				continue
			}
		}
		if src.SystemCause != "" && src.SystemCause != replyMonitorCause {
			continue
		}
		if !p.Final {
			if src.SystemCause == "" {
				evidence.progress = max(evidence.progress, p.OutputSequence)
			}
			continue
		}
		if p.OutputSequence > evidence.output.Sequence {
			evidence.final = true
			evidence.output = DiscussionOutput{DiscussionOutputSource: src, ID: p.ID, Text: p.Text, Sequence: p.OutputSequence}
		}
	}
	if evidence.final {
		return evidence
	}
	for _, r := range st.Requests {
		if r.ConversationID != a.ConversationID || r.Kind != "manager" || r.DiscussionSource.AdmissionID != a.Origin.RequestID || r.OutputSequence <= evidence.output.Sequence {
			continue
		}
		if r.State == "answered" {
			evidence = replyEvidence{answered: true, output: DiscussionOutput{Sequence: r.OutputSequence}}
			continue
		}
		if r.State != "pending" || !now.Before(r.ExpiresAt) {
			continue
		}
		body, err := discussionQuestionText(r)
		if err != nil {
			continue
		}
		evidence = replyEvidence{output: DiscussionOutput{DiscussionOutputSource: r.DiscussionSource, ID: r.ID, Text: body, QuestionID: r.ID, QuestionHash: r.Hash, Sequence: r.OutputSequence}, expires: r.ExpiresAt}
	}
	return evidence
}

func replyMatchesWorker(st State, a Admission, p Publication) bool {
	pending := make(map[string]bool)
	for _, action := range st.Actions {
		if action.ConversationID == a.ConversationID && action.State != "settled" {
			pending[action.Target] = true
		}
	}
	return workerReportOrigin(st, p, a.Project, pending) == a.Origin.RequestID
}

// A queued reporting turn can outlive its source revision or an earlier worker result.
func (c *Controller) checkReplyRecovery(ctx context.Context, st State, report Admission) error {
	if report.RecoveryOutput == nil || !report.RecoveryOnly {
		return ErrCorrupt
	}
	for _, original := range st.Admissions {
		if original.ConversationID != report.ConversationID || original.ReplyRecovery != report.Sequence || original.Origin == nil {
			continue
		}
		if !c.discussion.validateOrigin(*original.Origin) || original.Origin.RequestID != report.RecoveryOutput.AdmissionID {
			return ErrStale
		}
		evidence := discussionReplyEvidence(st, original, time.Now())
		if original.ReplyCheckAfter == 0 || evidence.output.ID != "" || evidence.answered {
			return ErrExpired
		}
		if c.discussion.VerifyGrant != nil {
			return c.discussion.VerifyGrant(ctx, *original.Origin)
		}
		return nil
	}
	return ErrExpired
}

// Check at most one overdue request per sync. A missing Discord receipt can never replay its publication.
func (d *DiscussionController) checkDiscussionReply(ctx context.Context, st State) error {
	now := d.now()
	var a Admission
	for _, candidate := range st.Admissions {
		if candidate.ConversationID != d.cfg.ConversationID || candidate.Origin == nil || candidate.ReplyCheckAfter == 0 || candidate.ReplyCheckAfter > now.Unix() {
			continue
		}
		if a.Sequence == 0 || candidate.ReplyCheckAfter < a.ReplyCheckAfter || candidate.ReplyCheckAfter == a.ReplyCheckAfter && candidate.Sequence < a.Sequence {
			a = candidate
		}
	}
	if a.Sequence == 0 {
		return nil
	}
	evidence := discussionReplyEvidence(st, a, now)
	confirmed := false
	if evidence.output.ID != "" && d.scope.ConfirmedDelivery != nil {
		ids, err := d.scope.ConfirmedDelivery(ctx, evidence.output)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		confirmed = err == nil && len(ids) > 0
	}
	return d.store.update(func(current *State) error {
		k := admissionKey(a.ConversationID, a.Sequence)
		input := current.Admissions[k]
		if input.Origin == nil || *input.Origin != *a.Origin || input.ReplyCheckAfter != a.ReplyCheckAfter || current.Generation != st.Generation || discussionReplyEvidence(*current, input, now) != evidence {
			return nil
		}
		next := now.Add(discussionReplyWait).Unix()
		input.ReplyWaitingForHuman = false
		switch {
		case evidence.answered, evidence.final && confirmed:
			next = 0
		case evidence.output.ID != "" && confirmed:
			input.ReplyWaitingForHuman = true
			next = min(next, evidence.expires.Unix()+1)
		case evidence.output.ID != "":
			// The existing outbox owns retries and uncertain nonces. A status message must never replay its body.
			if input.ReplyProgress == evidence.output.Sequence {
				if err := recordReplyNotice(current, input, "I have a reply, but Discord hasn’t confirmed delivery. Your request is saved.", false); err != nil {
					return err
				}
			}
			input.ReplyProgress = evidence.output.Sequence
		case evidence.progress > input.ReplyProgress:
			input.ReplyProgress = evidence.progress
		case evidence.workerRunning && current.Publications[key(input.ConversationID, replyNoticeID(input, false))].ID == "":
			if err := recordReplyNotice(current, input, "The worker hasn’t reported a result yet. Your request is saved.", false); err != nil {
				return err
			}
		case input.Phase == "superseded" || input.Phase == "rejected" && !input.SourceCutover:
			next = 0
		case input.SettledAt.IsZero():
			if input.Phase == "running" || input.Phase == "submitted" || input.Phase == "admitted" {
				if err := recordReplyNotice(current, input, "I’m still waiting for the answer. Your message is saved.", false); err != nil {
					return err
				}
			}
		case input.Phase != "completed":
			if err := recordReplyNotice(current, input, "I couldn’t confirm what happened. Open the dispatcher in Swarm to check your request before trying again.", true); err != nil {
				return err
			}
		case input.ReplyRecovery == 0:
			source := discussionOutputSource(current, input.ConversationID, input)
			for _, other := range current.Admissions {
				if other.ConversationID == input.ConversationID && other.RecoveryOnly && other.RecoveryOutput != nil && other.RecoveryOutput.AdmissionID == source.AdmissionID {
					input.ReplyRecovery = other.Sequence
					break
				}
			}
			if input.ReplyRecovery == 0 {
				b := current.Conversations[input.ConversationID].Binding
				follow, err := allocateAdmission(current, b, "reply-check:"+input.Origin.RequestID)
				if err != nil {
					return err
				}
				follow.Project, follow.RecoveryOnly, follow.RecoveryOutput = input.Project, true, &source
				follow.InputText = fmt.Sprintf("Your Discord turn ended without a confirmed answer. Read the original message %q and its receipts using swarm_discussion_history; check the same workers. Publish the existing result, or say exactly what is still unknown, with final=true. Do not repeat the job or launch workers.", input.Origin.MessageID)
				putRecord(current, "admissions", &current.Admissions, admissionKey(input.ConversationID, follow.Sequence), follow)
				input.ReplyRecovery = follow.Sequence
			}
		case current.Admissions[admissionKey(input.ConversationID, input.ReplyRecovery)].Reason == ErrDiscussionSourceChanged.Error():
			next = 0
		case !current.Admissions[admissionKey(input.ConversationID, input.ReplyRecovery)].SettledAt.IsZero():
			if err := recordReplyNotice(current, input, "I couldn’t confirm an answer. Your request is saved. Open the dispatcher in Swarm to check what finished.", true); err != nil {
				return err
			}
		}
		input.ReplyCheckAfter = next
		putRecord(current, "admissions", &current.Admissions, k, input)
		return nil
	})
}

func replyNoticeID(a Admission, final bool) string {
	prefix := "reply-wait-"
	if final {
		prefix = "reply-unresolved-"
	}
	return prefix + hashValue([]string{a.Origin.RequestID, a.Origin.Revision})
}

func recordReplyNotice(st *State, a Admission, body string, final bool) error {
	id := replyNoticeID(a, final)
	k := key(a.ConversationID, id)
	if _, ok := st.Publications[k]; ok {
		return nil
	}
	n, err := nextDiscussionOutput(st, a.ConversationID)
	if err != nil {
		return err
	}
	p := Publication{ID: id, ConversationID: a.ConversationID, Admission: a.Sequence, OutputSequence: n, Text: body, TextHash: hashBytes([]byte(body)), Final: final, DiscussionSource: DiscussionOutputSource{AdmissionID: a.Origin.RequestID, SystemCause: replyMonitorCause}}
	putRecord(st, "publications", &st.Publications, k, p)
	return nil
}
