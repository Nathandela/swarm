package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type ControllerWorkers interface {
	List() ([]protocol.SessionView, error)
	NativeHistoryPage(schema.InteractionHistoryReq) (schema.NativeHistoryPage, error)
	RetainedHistoryPage(schema.InteractionHistoryReq) (protocol.RetainedHistoryPage, error)
	LaunchOperation(string, protocol.LaunchReq) (string, string, error)
	LaunchOperationStatus(string) (schema.OperationOutcomeView, error)
	SemanticSend(string, protocol.ComposerSendReq) error
	SemanticOperation(string, string) (protocol.SemanticMessageReceipt, error)
	NativeRequests(protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error)
	NativeDecide(string, protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error)
	NativeDecisionStatus(string, string) (protocol.NativeDecisionReceipt, error)
}

var _ ControllerWorkers = (*protocol.Client)(nil)

type controllerActionRequest struct {
	WorkerJobID     string                      `json:"worker_job_id,omitempty"`
	WorkerAttemptID string                      `json:"worker_attempt_id,omitempty"`
	WorkerWakeID    string                      `json:"worker_wake_id,omitempty"`
	Origin          *schema.MessageOrigin       `json:"origin,omitempty"`
	Project         string                      `json:"project,omitempty"`
	Tool            string                      `json:"tool"`
	Arguments       json.RawMessage             `json:"arguments"`
	Launch          *protocol.LaunchReq         `json:"launch,omitempty"`
	Send            *protocol.ComposerSendReq   `json:"send,omitempty"`
	Decision        *protocol.NativeDecisionReq `json:"decision,omitempty"`
}

func decodeControllerInput[T any](raw json.RawMessage) (T, error) {
	var input T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return input, ErrInvalid
	}
	return input, validateManagerToolInput(input)
}
func controllerResult(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxUtteranceBytes {
		return nil, errManagerPayloadTooLarge
	}
	return raw, nil
}

// ManagerCall runs only after private bridge authentication. Native coordinates come from the daemon proof.
func (c *Controller) ManagerCall(ctx context.Context, call ManagerToolCall) (json.RawMessage, error) {
	status, err := c.nativeStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.Turn.ExternallySteered {
		return nil, ErrControllerPaused
	}
	args, err := managerproof.CanonicalJSON(call.Arguments)
	if err != nil {
		return nil, ErrInvalid
	}
	digest, err := managerproof.ArgumentsHash(args)
	if err != nil {
		return nil, ErrInvalid
	}
	meta, err := json.Marshal(call.Meta)
	if err != nil {
		return nil, ErrInvalid
	}
	proof, err := c.runtime.ResolveManagerCall(ctx, protocol.ManagerCallReq{Binding: runtimeBinding(c.binding), ToolName: call.Name, ArgumentsHash: digest, HostMeta: meta})
	if err != nil {
		return nil, c.nativeError(err)
	}
	if !proof.Main || !managerproof.ValidHash(proof.NativeInputID) || proof.NativeTurnID == "" || proof.NativeCallID == "" {
		return nil, ErrStale
	}
	status, err = c.nativeStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.Turn.ExternallySteered || status.Turn.NativeInputID != proof.NativeInputID || status.Turn.NativeTurnID != proof.NativeTurnID || status.Turn.State != "running" {
		return nil, ErrStale
	}
	st, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	var admission Admission
	for _, a := range st.Admissions {
		if a.ConversationID == c.binding.ConversationID && a.NativeInputID == proof.NativeInputID {
			admission = a
			break
		}
	}
	if admission.NativeBindingRetired || admission.Sequence == 0 || !admission.SettledAt.IsZero() || admission.Phase == "completed" || admission.Phase == "rejected" || admission.ExternalInput != status.Turn.ExternalInput {
		return nil, ErrExpired
	}
	if err = c.store.controllerInputPhase(c.binding, admission.Sequence, "running", proof.NativeTurnID, ""); err != nil {
		return nil, err
	}
	admission.NativeTurnID = proof.NativeTurnID
	if err := c.discussionEffect(admission, call.Name); err != nil {
		return nil, err
	}
	if admission.RecoveryOnly {
		switch call.Name {
		case "swarm_create", "swarm_resume", "swarm_send", "swarm_decision", "swarm_interrupt":
			return nil, ErrControllerUncertain
		}
	}
	switch call.Name {
	case "publish":
		input, err := decodeControllerInput[PublishInput](args)
		if err != nil {
			return nil, err
		}
		publicationID, err := c.publicationReference(st, input.OutputID, admission.Sequence, proof.NativeTurnID)
		if err != nil {
			return nil, err
		}
		permit, err := c.store.controllerSpeechPermit(c.binding, admission.Sequence, proof.NativeInputID, proof.NativeTurnID)
		if err != nil {
			return nil, err
		}
		publication, err := c.store.PublishSpeech(c.binding, permit, Publication{Admission: admission.Sequence, ID: publicationID, NativeTurnID: proof.NativeTurnID, Sequence: input.Sequence, Text: input.Text, Final: input.Final})
		if err != nil {
			return nil, err
		}
		return controllerResult(publication)
	case "ask":
		input, err := decodeControllerInput[AskInput](args)
		if err != nil {
			return nil, err
		}
		return c.managerQuestion(input, args, proof, admission)

	case "swarm_read_note":
		input, err := decodeControllerInput[SessionInput](args)
		if err != nil {
			return nil, err
		}
		return c.readDiscussionWorkerNote(input, admission)

	case "swarm_reply":
		input, err := decodeControllerInput[WorkerReplyInput](args)
		if err != nil {
			return nil, err
		}
		if err := c.replyToDiscussionWorker(ctx, admission, input.Text); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"state":"available"}`), nil
	case "swarm_discussion_answer":
		input, err := decodeControllerInput[DiscussionAnswerInput](args)
		if err != nil {
			return nil, err
		}
		if input.RequestID == "" {
			return c.discussionQuestions(st, admission, input.After)
		}
		answer, err := c.settleDiscussionAnswer(admission, input)
		if err != nil {
			return nil, err
		}
		return controllerResult(answer)
	case "swarm_discussion_history":
		if c.discussion == nil || c.discussion.History == nil {
			return nil, ErrControllerUnsupported
		}
		input, err := decodeControllerInput[DiscussionHistoryInput](args)
		if err != nil {
			return nil, err
		}
		return c.discussion.History(ctx, input)
	case "swarm_list":
		input, err := decodeControllerInput[ListInput](args)
		if err != nil {
			return nil, err
		}
		return c.workerPage(input)

	case "swarm_read":
		input, err := decodeControllerInput[ReadInput](args)
		if err != nil {
			return nil, err
		}
		if c.discussion != nil {
			return c.readDiscussionWorker(ctx, input, admission)
		}
		return c.readWorker(input)

	case "swarm_current_request":
		input, err := decodeControllerInput[SessionInput](args)
		if err != nil {
			return nil, err
		}
		if _, err = c.workerTarget(input, false); err != nil {
			return nil, err
		}
		requests, err := c.workers.NativeRequests(protocol.NativeRequestsReq{Session: input.Session})
		if err != nil {
			return nil, err
		}
		if len(requests.Requests) == 1 {
			selected := requests.Requests[0]
			requests, err = c.workers.NativeRequests(protocol.NativeRequestsReq{Session: input.Session, RequestID: selected.RequestID, NativeSessionID: selected.NativeSessionID})
			if err != nil {
				return nil, err
			}
		} else if len(requests.Requests) > 1 {
			requests.Reason = "multiple native requests require terminal selection"
		}
		if requests.Request != nil && requests.Request.SessionInstance != input.SessionInstance {
			return nil, ErrStale
		}
		return controllerResult(requests)
	case "swarm_status":
		input, err := decodeControllerInput[StatusInput](args)
		if err != nil {
			return nil, err
		}
		reference, err := c.actionReference(st, input.ActionID, admission.Sequence, false)
		if err != nil {
			return nil, err
		}
		action, ok := st.Actions[key(c.binding.ConversationID, reference)]
		if !ok {
			return nil, ErrNotFound
		}
		result, err := c.reconcileAction(ctx, action)
		if err != nil {
			return nil, fmt.Errorf("voice: action %s: %w", action.ID, err)
		}
		return result, nil
	case "swarm_interrupt":
		if _, err := decodeControllerInput[InterruptInput](args); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: owner interrupt lacks an instance-bound native effect receipt", ErrControllerUnsupported)
	case "swarm_create", "swarm_resume", "swarm_send", "swarm_decision":
		return c.managerEffect(ctx, call.Name, args, admission)
	default:
		return nil, ErrInvalid
	}
}
func (c *Controller) StopSpeech(turn string) error { return c.store.RevokeSpeech(c.binding, turn) }
func (c *Controller) workerTarget(target SessionInput, nativeComposer bool) (protocol.SessionView, error) {
	if c.workers == nil {
		return protocol.SessionView{}, ErrControllerUnsupported
	}
	if c.discussion != nil {
		tracked, err := c.TrackedWorker(target.Session, target.SessionInstance)
		if err != nil {
			return protocol.SessionView{}, err
		}
		if !tracked {
			return protocol.SessionView{}, ErrNotFound
		}
	}
	if target.Session == c.binding.SessionID {
		return protocol.SessionView{}, ErrInvalid
	}
	rows, err := c.workers.List()
	if err != nil {
		return protocol.SessionView{}, err
	}
	for _, row := range rows {
		if row.ID == target.Session {
			if row.Role == "manager" {
				return row, ErrStale
			}
			if row.Capabilities == nil {
				return row, ErrControllerNativeUnavailable
			}
			if row.Capabilities.SessionInstance != target.SessionInstance {
				return row, ErrStale
			}
			if nativeComposer && !row.Capabilities.NativeOwnerInput {
				if row.Agent == "codex" || row.Agent == "claude" || row.Agent == "opencode" {
					return row, ErrControllerNativeUnavailable
				}
				return row, ErrControllerUnsupported
			}
			return row, nil
		}
	}
	return protocol.SessionView{}, ErrNotFound
}
func (c *Controller) workerResumeRequest(input ResumeInput, admission uint64) (*protocol.LaunchReq, error) {
	st, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	ref, err := c.actionReference(st, input.ActionID, admission, true)
	if err != nil {
		return nil, err
	}
	if prior, ok := st.Actions[key(c.binding.ConversationID, ref)]; ok {
		var request controllerActionRequest
		if json.Unmarshal(prior.Request, &request) != nil {
			return nil, ErrCorrupt
		}
		if request.Tool != "swarm_resume" || prior.Target != key(input.Session, input.SessionInstance) {
			return nil, ErrConflict
		}
		if request.Launch == nil || request.Launch.Options[protocol.OptionResumeFrom] != input.Session || request.Launch.ResumeSourceInstance != input.SessionInstance {
			return nil, ErrCorrupt
		}
		return request.Launch, nil
	}
	row, err := c.workerTarget(input.SessionInput, false)
	if err != nil {
		return nil, err
	}
	endpoint, parent, ok := protocol.ParseID(c.binding.SessionID)
	sourceEndpoint, _, valid := protocol.ParseID(row.ID)
	if !ok || !valid || sourceEndpoint != endpoint || row.RosterHidden || row.Status.Process != status.ProcessExited {
		return nil, ErrStale
	}
	if c.discussion != nil && row.Agent != "codex" {
		return nil, fmt.Errorf("%w: this Discord worker provider cannot supply exact result evidence", ErrControllerUnsupported)
	}
	if !registry.IsProduction(row.Agent) {
		return nil, ErrControllerUnsupported
	}
	return &protocol.LaunchReq{Agent: row.Agent, Name: row.Name, Tag: row.Tag, Cwd: row.Cwd, Cols: 120, Rows: 32,
		Options: map[string]string{protocol.OptionResumeFrom: row.ID}, ResumeSourceInstance: input.SessionInstance,
		SpawnedFrom: parent, SpawnIntent: schema.SpawnIntentDelegate}, nil
}

func (c *Controller) managerEffect(ctx context.Context, name string, args json.RawMessage, admission Admission) (result json.RawMessage, effectErr error) {
	if c.discussion != nil {
		c.discussionEffects.Lock()
		defer c.discussionEffects.Unlock()
		if err := c.discussionEffect(admission, name); err != nil {
			return nil, err
		}
	}
	var reference string
	defer func() {
		if errors.Is(effectErr, ErrControllerUncertain) && reference != "" {
			effectErr = fmt.Errorf("voice: action %s: %w", reference, effectErr)
		}
	}()
	if c.workers == nil {
		return nil, ErrControllerUnsupported
	}
	var id, target, operation string
	request := controllerActionRequest{Tool: name, Arguments: args, Origin: admission.Origin, Project: admission.Project}
	switch name {
	case "swarm_create":
		input, err := decodeControllerInput[CreateInput](args)
		if err != nil {
			return nil, err
		}
		_, parent, valid := protocol.ParseID(c.binding.SessionID)
		if !valid {
			return nil, ErrStale
		}
		if c.discussion != nil {
			if input.Agent != "codex" && input.Agent != "claude" {
				return nil, fmt.Errorf("%w: use Codex for delegated Discord work with verified results", ErrControllerUnsupported)
			}
		}
		id = input.ActionID
		target = "new-worker:" + id
		options := map[string]string{}
		if input.Model != "" {
			options["model"] = input.Model
		}
		prompt := input.Prompt
		if c.discussion != nil {
			provenance, _ := json.Marshal(admission.Origin)
			prompt = discussionWorkerGuidance(input.Agent) + "\n\nSupervisor-verified Discord work attribution " + string(provenance) + "\n\n" + prompt
		}
		request.Launch = &protocol.LaunchReq{Agent: input.Agent, Name: input.Name, Cwd: input.Cwd, Options: options, InitialPrompt: prompt, Worktree: input.Worktree, Cols: 120, Rows: 32, SpawnedFrom: parent, SpawnIntent: schema.SpawnIntentDelegate}
	case "swarm_resume":
		input, err := decodeControllerInput[ResumeInput](args)
		if err != nil {
			return nil, err
		}
		id, target = input.ActionID, key(input.Session, input.SessionInstance)
		request.Launch, err = c.workerResumeRequest(input, admission.Sequence)
		if err != nil {
			return nil, err
		}
	case "swarm_send":
		input, err := decodeControllerInput[SendInput](args)
		if err != nil {
			return nil, err
		}
		if c.discussion != nil {
			_, err = c.discussionWorkerForSend(input.SessionInput)
		} else {
			_, err = c.workerTarget(input.SessionInput, true)
		}
		if err != nil {
			return nil, err
		}
		id = input.ActionID
		target = key(input.Session, input.SessionInstance)
		request.Send = &protocol.ComposerSendReq{Session: input.Session, SessionInstance: input.SessionInstance, Text: input.Text, Origin: admission.Origin}
		if c.discussion != nil {
			request.Send.WorkerManagerConversation = c.binding.ConversationID
			st, err := c.store.readView()
			if err != nil {
				return nil, err
			}
			if admission.Origin == nil {
				origin, wakeID, err := c.discussionContinuation(st, admission, target, id)
				if err != nil {
					return nil, err
				}
				request.Origin, request.Send.Origin, request.WorkerWakeID = origin, origin, wakeID
				request.Send.Text = "System continuation under the previously authorized Discord job. Only continue the original scope.\n\n" + request.Send.Text
			}
		}
	case "swarm_decision":
		input, err := decodeControllerInput[DecisionInput](args)
		if err != nil {
			return nil, err
		}
		if err = protocol.ValidateNativeDecision(input.Request); err != nil {
			return nil, ErrInvalid
		}
		if _, err = c.workerTarget(SessionInput{Session: input.Request.Session, SessionInstance: input.Request.SessionInstance}, false); err != nil {
			return nil, err
		}
		id = input.ActionID
		target = key(input.Request.Session, input.Request.SessionInstance)
		request.Decision = &input.Request
	}
	action, err := c.prepareManagerAction(ctx, name, id, target, request, admission)
	if err != nil {
		return nil, err
	}
	operation = action.OperationID
	id = action.ID
	reference = action.ID

	if action.State == "settled" {
		return controllerActionResult(action.ID, action.Receipt)
	}
	if action.State != "prepared" {
		return c.reconcileAction(ctx, action)
	}
	if name == "swarm_resume" && !protocol.LaunchOperationReplayable(operation, time.Now()) {
		return nil, c.rejectWorkerResume(action, ErrExpired)
	}
	if c.discussion != nil && request.Launch != nil {
		var prepared controllerActionRequest
		if json.Unmarshal(action.Request, &prepared) != nil {
			return nil, ErrCorrupt
		}
		request.Launch = prepared.Launch
		if err := c.verifyDiscussionLaunch(ctx, prepared); err != nil {
			if errors.Is(err, ErrDiscussionSourceChanged) || errors.Is(err, ErrExpired) {
				return nil, c.rejectWorkerResume(action, err)
			}
			return nil, err
		}
	}
	status, err := c.nativeStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.Turn.ExternallySteered || status.Turn.NativeInputID != admission.NativeInputID || status.Turn.ExternalInput != admission.ExternalInput || status.Turn.State != "running" || status.Turn.NativeTurnID != admission.NativeTurnID {
		return nil, ErrStale
	}

	queueSend := c.discussion != nil && name == "swarm_send"
	if err = c.store.controllerActionExecuting(c.binding, id, queueSend); err != nil {
		if errors.Is(err, ErrConflict) {
			return c.reconcileAction(ctx, action)
		}
		if errors.Is(err, ErrExpired) {
			if name == "swarm_resume" {
				return nil, c.rejectWorkerResume(action, err)
			}
			if settleErr := c.store.SettleAction(c.binding, id, json.RawMessage(`{"state":"rejected","reason":"native admission ended before effect"}`)); settleErr != nil {
				return nil, settleErr
			}
		}
		return nil, err
	}
	if queueSend {
		return queuedDiscussionSendReceipt(action)
	}
	switch name {
	case "swarm_create", "swarm_resume":
		session, canonical, err := c.workers.LaunchOperation(operation, *request.Launch)
		if err != nil {
			if name == "swarm_resume" {
				switch {
				case errors.Is(err, protocol.ErrOwnerResumeStaleSource):
					return nil, c.rejectWorkerResume(action, ErrStale)
				case errors.Is(err, ErrControllerUnsupported):
					return nil, c.rejectWorkerResume(action, ErrControllerUnsupported)
				}
			}
			return c.reconcileAction(ctx, action)
		}
		return c.createdWorkerReceipt(ctx, action, session, canonical)

	case "swarm_send":
		if err = c.workers.SemanticSend(operation, *request.Send); err != nil {
			return c.reconcileAction(ctx, action)
		}
	case "swarm_decision":
		receipt, err := c.workers.NativeDecide(operation, *request.Decision)
		if err == nil && receipt.State != "uncertain" {
			raw, _ := json.Marshal(receipt)
			if err = c.store.settleControllerWorkerAction(c.binding, action, request.Decision.Session, request.Decision.SessionInstance, raw); err != nil {
				return nil, err
			}
			return c.storedControllerActionResult(action)
		}
	}
	return c.reconcileAction(ctx, action)
}
func (c *Controller) rejectWorkerResume(action Action, reason error) error {
	raw, err := controllerResult(struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}{"rejected", reason.Error()})
	if err != nil {
		return err
	}
	return errors.Join(reason, c.store.settleControllerWorkerAction(c.binding, action, "", "", raw))
}

func (c *Controller) reconcileAction(ctx context.Context, action Action) (json.RawMessage, error) {
	current, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	if err = checkBinding(&current, c.binding); err != nil {
		return nil, err
	}
	stored, ok := current.Actions[key(c.binding.ConversationID, action.ID)]
	if !ok {
		return nil, ErrNotFound
	}
	if stored.OperationID != action.OperationID || stored.RequestHash != action.RequestHash {
		return nil, ErrConflict
	}
	action = stored
	if action.State == discussionQueuedSend {
		return queuedDiscussionSendReceipt(action)
	}
	if action.State == "settled" {
		return controllerActionResult(action.ID, action.Receipt)
	}
	if c.workers == nil {
		return nil, ErrControllerUnsupported
	}
	var request controllerActionRequest
	if json.Unmarshal(action.Request, &request) != nil {
		return nil, ErrCorrupt
	}
	var receipt any
	settled := false
	switch request.Tool {
	case "swarm_create", "swarm_resume":
		outcome, err := c.workers.LaunchOperationStatus(action.OperationID)
		if err != nil {
			return nil, ErrControllerUncertain
		}
		endpoint, _, valid := protocol.ParseID(c.binding.SessionID)
		session, applied := protocol.AppliedOwnerLaunchSession(endpoint, outcome)
		if !valid || !applied {
			return nil, ErrControllerUncertain
		}
		return c.createdWorkerReceipt(ctx, action, session, "")
	case "swarm_send":
		if request.Send == nil {
			return nil, ErrCorrupt
		}
		result, err := c.workers.SemanticOperation(request.Send.Session, action.OperationID)
		if err != nil {
			return nil, ErrControllerUncertain
		}
		if result.OperationID != action.OperationID || result.Session != request.Send.Session || result.SessionInstance != request.Send.SessionInstance {
			return nil, ErrStale
		}
		receipt = result
		settled = result.State == "submitted" || result.State == "admitted" || result.State == "rejected" || result.State == "completed" || result.State == "failed" || result.State == "cancelled"
	case "swarm_decision":
		if request.Decision == nil {
			return nil, ErrCorrupt
		}
		result, err := c.workers.NativeDecisionStatus(request.Decision.Session, action.OperationID)
		if err != nil {
			return nil, ErrControllerUncertain
		}
		if result.OperationID != action.OperationID || result.Session != request.Decision.Session || result.SessionInstance != request.Decision.SessionInstance || result.RequestID != request.Decision.RequestID || result.Generation != request.Decision.Generation {
			return nil, ErrStale
		}
		receipt = result
		settled = result.State == "admitted" || result.State == "rejected"
	default:
		return nil, fmt.Errorf("%w: unknown stored manager action", ErrCorrupt)
	}
	if !settled {
		return nil, ErrControllerUncertain
	}
	raw, err := controllerResult(receipt)
	if err != nil {
		return nil, err
	}
	var target SessionInput
	if request.Send != nil {
		target = SessionInput{request.Send.Session, request.Send.SessionInstance}
	}
	if request.Decision != nil {
		target = SessionInput{request.Decision.Session, request.Decision.SessionInstance}
	}
	if target.Session != "" {
		err = c.store.settleControllerWorkerAction(c.binding, action, target.Session, target.SessionInstance, raw)
	} else {
		err = c.store.SettleAction(c.binding, action.ID, raw)
	}
	if err != nil {
		return nil, err
	}
	return c.storedControllerActionResult(action)
}

func (c *Controller) managerQuestion(input AskInput, _ json.RawMessage, proof protocol.ManagerCallProof, admission Admission) (json.RawMessage, error) {
	c.metadata.Lock()
	defer c.metadata.Unlock()
	st, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	id := c.businessReference("request", admission.Sequence, "", input.RequestID)
	if strings.HasPrefix(input.RequestID, "request:") {
		prior, ok := st.Requests[key(c.binding.ConversationID, input.RequestID)]
		if !ok {
			return nil, ErrNotFound
		}
		if prior.NativeInputID != proof.NativeInputID || prior.NativeTurnID != proof.NativeTurnID {
			return nil, ErrExpired
		}
		id = input.RequestID
	}
	task := c.businessReference("task", admission.Sequence, "", input.TaskID)
	if strings.HasPrefix(input.TaskID, "task:") {
		known := false
		for _, prior := range st.Requests {
			if prior.ConversationID == c.binding.ConversationID && prior.TaskID == input.TaskID {
				known = true
				break
			}
		}
		if !known {
			return nil, ErrNotFound
		}
		task = input.TaskID
	}
	recoveryContext := admission.RecoveryOnly
	for _, origin := range st.Admissions {
		if origin.ConversationID == c.binding.ConversationID && origin.NativeInputID == proof.NativeInputID {
			recoveryContext = origin.RecoveryOnly
		}
	}
	if prior, ok := st.Requests[key(c.binding.ConversationID, id)]; ok {
		recoveryContext = prior.RecoveryContext
	}
	payload, err := json.Marshal(struct {
		Question string   `json:"question"`
		Choices  []string `json:"choices,omitempty"`
	}{input.Question, input.Choices})
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(24 * time.Hour)
	if prior, ok := st.Requests[key(c.binding.ConversationID, id)]; ok {
		expires = prior.ExpiresAt
	}

	request, err := c.store.PutRequest(c.binding, Request{RecoveryContext: recoveryContext, ID: id, Kind: "manager", NativeInputID: proof.NativeInputID, NativeTurnID: proof.NativeTurnID, TaskID: task, Payload: payload, ExpiresAt: expires})
	if err != nil {
		return nil, err
	}
	return controllerResult(request)
}

func (c *Controller) prepareManagerAction(ctx context.Context, name, id, target string, request controllerActionRequest, admission Admission) (Action, error) {
	c.metadata.Lock()
	defer c.metadata.Unlock()
	st, err := c.store.Snapshot()
	if err != nil {
		return Action{}, err
	}
	reference, err := c.actionReference(st, id, admission.Sequence, true)
	if err != nil {
		return Action{}, err
	}
	id = reference
	request.Arguments, err = withoutBusinessID(request.Arguments, "action_id")
	if err != nil {
		return Action{}, err
	}
	prior, exists := st.Actions[key(c.binding.ConversationID, id)]
	if c.discussion != nil && request.Launch != nil && exists && prior.State != "prepared" {
		var old controllerActionRequest
		if json.Unmarshal(prior.Request, &old) != nil {
			return Action{}, ErrCorrupt
		}
		if old.Tool != name || !bytes.Equal(old.Arguments, request.Arguments) || old.Origin == nil || request.Origin == nil ||
			*old.Origin != *request.Origin || prior.NativeInputID != admission.NativeInputID {
			return Action{}, ErrConflict
		}
		return prior, nil
	}
	if name == "swarm_create" {
		target = "new-worker:" + id
	} else if c.discussion != nil {
		if request.Launch != nil {
			source := request.Launch.Options[protocol.OptionResumeFrom]
			target = key(source, request.Launch.ResumeSourceInstance)
		}
		association, ok := st.Workers[key(c.binding.ConversationID, target)]
		if !ok || association.JobID == "" || !c.projectPath(association.Project, association.Project) {
			return Action{}, ErrStale
		}
		request.Project = association.Project
		if name == "swarm_send" {
			request.WorkerJobID, request.WorkerAttemptID = association.JobID, association.AttemptID
			for _, effect := range st.Actions {
				if effect.ID != id && effect.Target == target && effect.ConversationID == c.binding.ConversationID &&
					(effect.State == "executing" || effect.State == discussionQueuedSend) {
					return Action{}, ErrControllerUncertain
				}
			}
		}
	}
	if c.discussion != nil && request.Launch != nil && !exists {
		if err := c.verifyDiscussionLaunch(ctx, request); err != nil {
			return Action{}, err
		}
	}
	var operation string
	if exists {
		operation = prior.OperationID
	} else {
		var err error
		switch name {
		case "swarm_create", "swarm_resume":
			operation, err = protocol.NewLaunchOperationID()
		case "swarm_send":
			operation, err = protocol.NewSemanticOperationID()
		case "swarm_decision":
			operation = "decision-" + hashValue(struct{ Conversation, Action string }{c.binding.ConversationID, id})
		}
		if err != nil {
			return Action{}, err
		}
	}
	if c.discussion != nil && request.Launch != nil {
		if exists {
			var old controllerActionRequest
			if json.Unmarshal(prior.Request, &old) != nil || old.Launch == nil {
				return Action{}, ErrCorrupt
			}
			request.Launch = old.Launch
		} else {
			var token [32]byte
			if _, err := rand.Read(token[:]); err != nil {
				return Action{}, err
			}
			request.Launch.WorkerBridge = c.refs.Socket + ":" + hex.EncodeToString(token[:])
		}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return Action{}, err
	}
	action, err := c.store.PrepareAction(c.binding, Action{ID: id, Admission: admission.Sequence, NativeInputID: admission.NativeInputID, Target: target, OperationID: operation, Request: raw})
	if err != nil {
		return Action{}, err
	}
	session, instance := "", ""
	if request.Send != nil {
		session, instance = request.Send.Session, request.Send.SessionInstance
	}
	if request.Decision != nil {
		session, instance = request.Decision.Session, request.Decision.SessionInstance
	}
	if err = c.store.reserveControllerWorker(c.binding, action, session, instance); err != nil {
		return Action{}, err
	}
	return action, nil
}

type createdWorkerResult struct {
	ObservedGroup   status.Group                `json:"observed_group"`
	ObservedStatus  status.Status               `json:"observed_status"`
	State           string                      `json:"state"`
	Session         string                      `json:"session"`
	SessionInstance string                      `json:"session_instance"`
	Name            string                      `json:"name,omitempty"`
	Capabilities    *schema.SessionCapabilities `json:"capabilities"`
}

func (c *Controller) createdWorkerReceipt(ctx context.Context, action Action, session, name string) (json.RawMessage, error) {
	var intent controllerActionRequest
	if json.Unmarshal(action.Request, &intent) != nil || intent.Launch == nil {
		return nil, ErrCorrupt
	}
	endpoint, _, valid := protocol.ParseID(c.binding.SessionID)
	launchEndpoint, _, validCreated := protocol.ParseID(session)
	if !valid || !validCreated || endpoint != launchEndpoint || session == intent.Launch.Options[protocol.OptionResumeFrom] {
		return nil, ErrControllerUncertain
	}
	resultState := "created"
	if intent.Tool == "swarm_resume" {
		resultState = "resumed"
	}
	receiptWindow := time.Second
	pollInterval := 50 * time.Millisecond
	if c.discussion != nil {
		receiptWindow = 15 * time.Second
		pollInterval = 250 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, receiptWindow)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		var rows []protocol.SessionView
		var err error
		if roster, ok := c.workers.(interface {
			ListContext(context.Context) ([]protocol.SessionView, error)
		}); ok {
			rows, err = roster.ListContext(ctx)
		} else {
			rows, err = c.workers.List()
		}
		if err != nil {
			return nil, ErrControllerUncertain
		}
		for _, row := range rows {
			if row.ID != session {
				continue
			}
			if row.Role == "manager" || row.Agent != intent.Launch.Agent ||
				c.discussion != nil && (row.SpawnedFrom != intent.Launch.SpawnedFrom || row.SpawnIntent != schema.SpawnIntentDelegate) {
				return nil, ErrControllerUncertain
			}
			if row.Capabilities == nil || row.Capabilities.SessionInstance == "" {
				break
			}
			if name == "" {
				name = row.Name
			}
			raw, err := controllerResult(createdWorkerResult{State: resultState, Session: session, SessionInstance: row.Capabilities.SessionInstance, Name: name, Capabilities: row.Capabilities, ObservedGroup: row.Group, ObservedStatus: row.Status})
			if err != nil {
				return nil, err
			}
			if err = c.store.settleControllerWorkerAction(c.binding, action, session, row.Capabilities.SessionInstance, raw); err != nil {
				return nil, err
			}
			return c.storedControllerActionResult(action)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		}
	}
	return nil, ErrControllerUncertain
}
func trackedControllerWorker(st State, conversation, session, instance string) bool {
	_, ok := st.Workers[key(conversation, key(session, instance))]
	return ok
}

func (c *Controller) TrackedWorker(session, instance string) (bool, error) {
	st, err := c.store.Snapshot()
	if err != nil {
		return false, err
	}
	if err = checkBinding(&st, c.binding); err != nil {
		return false, err
	}
	return trackedControllerWorker(st, c.binding.ConversationID, session, instance), nil
}

// StopTrackingWorker stops future notifications, preserving any already accepted input/effect guards.
// It neither interrupts the worker nor declares its task successful.
func (c *Controller) StopTrackingWorker(expected WorkerAssociation) error {
	return c.store.stopControllerWorker(c.binding, expected)
}

func (c *Controller) storedControllerActionResult(expected Action) (json.RawMessage, error) {
	st, err := c.store.Snapshot()
	if err != nil {
		return nil, err
	}
	if err = checkBinding(&st, c.binding); err != nil {
		return nil, err
	}
	action, ok := st.Actions[key(c.binding.ConversationID, expected.ID)]
	if !ok {
		return nil, ErrNotFound
	}
	if action.OperationID != expected.OperationID || action.RequestHash != expected.RequestHash {
		return nil, ErrConflict
	}
	if action.State != "settled" {
		return nil, ErrControllerUncertain
	}
	return controllerActionResult(action.ID, action.Receipt)
}
