package voice

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// Native completion and a Discord reply have independent receipts.
const discussionFirstReplyWait = 30 * time.Second
const discussionReplyWait = 2 * time.Minute
const discussionReplyStall = 10 * time.Minute

func hasReplyObligation(st State) bool {
	for _, a := range st.Admissions {
		if a.ConversationID == st.CurrentConversation && a.ReplyCheckAfter != 0 {
			return true
		}
	}
	return false
}

type replyEvidence struct {
	output          DiscussionOutput
	workerFinals    []DiscussionOutput
	final           bool
	answered        bool
	progress        uint64
	workerRunning   bool
	unlinkedWorker  bool
	expiredQuestion bool
	expires         time.Time
}

func discussionReplyEvidence(st State, a Admission, now time.Time) replyEvidence {
	var evidence replyEvidence
	attempts := map[string]DiscussionOutput{}
	for _, w := range st.Workers {
		if w.ConversationID == a.ConversationID && w.VMChild && w.Admission == 0 && !w.Ended && !w.RecoveryRequired {
			// Older launches identify the discussion only. Never blame a missing
			// answer while a potentially related worker is still alive.
			evidence.unlinkedWorker = true
		}
		if w.ConversationID == a.ConversationID && w.Admission == a.Sequence && w.JobID != "" {
			attempts[w.AttemptID] = DiscussionOutput{}
			if !w.Ended && !w.RecoveryRequired {
				evidence.workerRunning = true
			}
		}
	}
	for _, wake := range st.Wakes {
		if wake.ConversationID != a.ConversationID {
			continue
		}
		event := st.Events[key(a.ConversationID, wake.EventID)]
		if _, known := attempts[event.AttemptID]; !known || !event.WorkerMessage || event.WakeID != wake.ID {
			continue
		}
		if wake.State == "prepared" || wake.Admission != 0 && st.Admissions[admissionKey(a.ConversationID, wake.Admission)].SettledAt.IsZero() {
			evidence.workerRunning = true
		}
	}
	var monitor DiscussionOutput
	for _, p := range st.Publications {
		if p.ConversationID != a.ConversationID {
			continue
		}
		src := p.DiscussionSource
		if src.AdmissionID != a.Origin.RequestID {
			prior, belongs := attempts[src.AttemptID]
			if belongs && p.Final && src.SystemCause == "" && p.OutputSequence > prior.Sequence && replyMatchesWorker(st, a, p) {
				attempts[src.AttemptID] = DiscussionOutput{DiscussionOutputSource: src, ID: p.ID, Text: p.Text, Sequence: p.OutputSequence}
			}
			continue
		}
		if src.SystemCause != "" {
			continue
		}
		if !p.Final {
			if !p.ReplyMonitor {
				evidence.progress = max(evidence.progress, p.OutputSequence)
			}
			continue
		}
		if p.ReplyMonitor {
			if p.OutputSequence > monitor.Sequence {
				monitor = DiscussionOutput{DiscussionOutputSource: src, ID: p.ID, Text: p.Text, Sequence: p.OutputSequence}
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
	for _, out := range attempts {
		if out.ID != "" {
			evidence.workerFinals = append(evidence.workerFinals, out)
			evidence.progress = max(evidence.progress, out.Sequence)
		}
	}
	if len(attempts) > 0 && len(evidence.workerFinals) == len(attempts) {
		sort.Slice(evidence.workerFinals, func(i, j int) bool { return evidence.workerFinals[i].Sequence < evidence.workerFinals[j].Sequence })
		evidence.output = evidence.workerFinals[len(evidence.workerFinals)-1]
		evidence.workerFinals = evidence.workerFinals[:len(evidence.workerFinals)-1]
		evidence.final = true
		return evidence
	}
	evidence.workerFinals = nil
	if monitor.ID != "" && !evidence.workerRunning && !evidence.unlinkedWorker {
		evidence.output, evidence.final = monitor, true
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
		if (r.State == "pending" || r.State == "expired") && !now.Before(r.ExpiresAt) {
			evidence.expiredQuestion = true
			continue
		}
		if r.State != "pending" {
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

func (e replyEvidence) equal(other replyEvidence) bool {
	return e.output == other.output && e.final == other.final && e.answered == other.answered && e.progress == other.progress && e.workerRunning == other.workerRunning && e.unlinkedWorker == other.unlinkedWorker && e.expiredQuestion == other.expiredQuestion && e.expires.Equal(other.expires) && slices.Equal(e.workerFinals, other.workerFinals)
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
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if err := c.discussion.VerifyGrant(checkCtx, *original.Origin); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, ErrDiscussionSourceChanged) {
					return err
				}
				// A report is optional; an unreadable grant cannot block later human requests.
				return ErrExpired
			}
		}
		return nil
	}
	return ErrExpired
}

// Only unfinished inputs existing at upgrade need classification. Closed historic
// turns and messages between guild participants never acquire a new reply by age.
func (d *DiscussionController) trackLegacyReplies(st State) error {
	for _, a := range st.Admissions {
		if a.ConversationID != d.cfg.ConversationID || a.Origin == nil || !a.SettledAt.IsZero() || a.ReplyTracked || a.ReplyCheckAfter != 0 {
			continue
		}
		return d.store.update(func(current *State) error {
			for k, a := range current.Admissions {
				if a.ConversationID != d.cfg.ConversationID || a.Origin == nil || !a.SettledAt.IsZero() || a.ReplyTracked || a.ReplyCheckAfter != 0 {
					continue
				}
				a.ReplyTracked = d.scope.DMOwnerID != "" && d.scope.validateOrigin(*a.Origin)
				a.ReplyCheckAfter = d.now().Unix()
				putRecord(current, "admissions", &current.Admissions, k, a)
			}
			return nil
		})
	}
	return nil
}
func (d *DiscussionController) classifyLegacyReply(ctx context.Context, a Admission, now time.Time) error {
	expected := false
	err := ErrControllerUnsupported
	if d.scope.ExpectsReply != nil {
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		expected, err = d.scope.ExpectsReply(check, *a.Origin)
		cancel()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return d.store.update(func(st *State) error {
		k := admissionKey(a.ConversationID, a.Sequence)
		current := st.Admissions[k]
		if current.Origin == nil || *current.Origin != *a.Origin || current.ReplyTracked || current.ReplyCheckAfter != a.ReplyCheckAfter {
			return nil
		}
		current.ReplyCheckAfter = 0
		current.ReplyTracked = err == nil
		if expected && err == nil {
			current.ReplyCheckAfter = now.Unix()
		}
		if err != nil {
			current.ReplyReceiptChecks++
			if current.ReplyReceiptChecks >= 3 {
				current.ReplyTracked = true
				current.ReplyDeliveryUnknown = true
			} else {
				current.ReplyCheckAfter = now.Add(discussionReplyWait).Unix()
			}
		} else {
			current.ReplyReceiptChecks = 0
		}
		putRecord(st, "admissions", &st.Admissions, k, current)
		return nil
	})
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
	if !a.ReplyTracked {
		return d.classifyLegacyReply(ctx, a, now)
	}
	evidence := discussionReplyEvidence(st, a, now)
	confirmed := false
	if evidence.output.ID != "" && d.scope.ConfirmedDelivery != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		confirmed = true
		for _, out := range append(slices.Clone(evidence.workerFinals), evidence.output) {
			ids, err := d.scope.ConfirmedDelivery(checkCtx, out)
			if err != nil || len(ids) == 0 {
				confirmed = false
				break
			}
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	var grantErr error
	if evidence.output.ID == "" && !evidence.answered && d.scope.VerifyGrant != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		grantErr = d.scope.VerifyGrant(checkCtx, *a.Origin)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return d.store.update(func(current *State) error {
		k := admissionKey(a.ConversationID, a.Sequence)
		input := current.Admissions[k]
		if input.Origin == nil || *input.Origin != *a.Origin || input.ReplyCheckAfter != a.ReplyCheckAfter || current.Generation != st.Generation || !discussionReplyEvidence(*current, input, now).equal(evidence) {
			return nil
		}
		next := now.Add(discussionReplyWait).Unix()
		input.ReplyWaitingForHuman = false
		if input.ReplyStallSince == 0 || evidence.output.ID != "" || evidence.progress > input.ReplyProgress {
			input.ReplyStallSince = now.Unix()
		}
		if grantErr == nil && evidence.output.ID == "" {
			input.ReplyReceiptChecks = 0
		}
		switch {
		case errors.Is(grantErr, ErrDiscussionSourceChanged):
			next = 0
		case grantErr != nil:
			input.ReplyReceiptChecks++
			if input.ReplyReceiptChecks >= 3 {
				next = 0
				input.ReplyDeliveryUnknown = true
			}
		case evidence.answered, evidence.final && confirmed:
			next = 0
		case evidence.output.ID != "" && confirmed:
			input.ReplyWaitingForHuman = true
			next = min(next, evidence.expires.Unix()+1)
		case evidence.output.ID != "":
			// The existing outbox owns retries and uncertain nonces. A status message must never replay its body.
			if input.ReplyProgress != evidence.output.Sequence {
				input.ReplyReceiptChecks = 0
			}
			input.ReplyReceiptChecks++
			input.ReplyProgress = evidence.output.Sequence
			// Discord can be gone permanently. Preserve the outbox nonce and an explicit
			// local outcome without keeping the model running or sending a second post.
			if input.ReplyReceiptChecks >= 3 {
				next = 0
				input.ReplyDeliveryUnknown = true
			}
		case evidence.progress > input.ReplyProgress:
			input.ReplyProgress = evidence.progress
		case evidence.workerRunning:
			if err := recordReplyNotice(current, input, "The worker hasn’t reported a result yet. Your request is saved.", false); err != nil {
				return err
			}
		case evidence.unlinkedWorker:
			if err := recordReplyNotice(current, input, "A worker is still open in this thread. I can’t verify whether it owns your request. Please check the worker in Swarm.", false); err != nil {
				return err
			}
			if !now.Before(time.Unix(input.ReplyStallSince, 0).Add(discussionReplyStall)) {
				next = 0
				input.ReplyDeliveryUnknown = true
			}
		case !now.Before(time.Unix(input.ReplyStallSince, 0).Add(discussionReplyStall)) && (input.SettledAt.IsZero() || input.ReplyRecovery != 0):
			if err := recordReplyNotice(current, input, "I still can’t verify a result. The work may be running. Please check the dispatcher in Swarm before trying again.", true); err != nil {
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
			if err := recordReplyNotice(current, input, "I couldn’t confirm what happened. Please check the result with the person running Swarm before trying again.", true); err != nil {
				return err
			}
		case evidence.expiredQuestion:
			if err := recordReplyNotice(current, input, "My question expired. Please reply here with the missing detail so I can continue.", true); err != nil {
				return err
			}
		case evidence.progress != 0 || d.scope.VMProjectAccess || current.Conversations[input.ConversationID].Binding.NativeCLI:
			// A normal VM CLI has unrestricted shell tools. A reporting prompt cannot
			// fence another spawn or shell side effect. Publication stays explicit.
			if err := recordReplyNotice(current, input, "I couldn’t verify a final answer. The work may have run. The person running Swarm can check its result.", true); err != nil {
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
			if err := recordReplyNotice(current, input, "I couldn’t confirm an answer. The person running Swarm can check what finished.", true); err != nil {
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
	p := Publication{ID: id, ConversationID: a.ConversationID, Admission: a.Sequence, OutputSequence: n, Text: body, TextHash: hashBytes([]byte(body)), Final: final, ReplyMonitor: true, DiscussionSource: DiscussionOutputSource{AdmissionID: a.Origin.RequestID}}
	putRecord(st, "publications", &st.Publications, k, p)
	return nil
}

// A quiet shared-thread turn must be an explicit decision by its authenticated manager.
func (c *Controller) yieldSharedReply(a Admission) error {
	return c.store.update(func(st *State) error {
		if err := checkBinding(st, c.binding); err != nil {
			return err
		}
		k := admissionKey(c.binding.ConversationID, a.Sequence)
		input := st.Admissions[k]
		if !c.humanDiscussionTurn(input) || input.Origin.GuildID == "" || input.NativeInputID != a.NativeInputID || input.NativeTurnID != a.NativeTurnID {
			return ErrExpired
		}
		e := discussionReplyEvidence(*st, input, time.Now())
		if e.output.ID != "" || e.progress != 0 || e.answered || e.workerRunning {
			return ErrConflict
		}
		for _, act := range st.Actions {
			if act.ConversationID == input.ConversationID && act.Admission == input.Sequence {
				return ErrConflict
			}
		}
		input.ReplyTracked = true
		input.ReplyCheckAfter = 0
		putRecord(st, "admissions", &st.Admissions, k, input)
		return nil
	})
}
