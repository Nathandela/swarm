package skeleton

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type nativeDecisionIntent struct {
	Identity opencodebridge.Identity        `json:"identity"`
	Request  protocol.NativeDecisionReq     `json:"request"`
	Dispatch opencodebridge.DispatchRequest `json:"dispatch"`
}
type nativeDecisionOutcome struct {
	Receipt protocol.NativeDecisionReceipt `json:"receipt"`
	Code    protocol.ErrorCode             `json:"code,omitempty"`
	Error   string                         `json:"error,omitempty"`
}

func nativeDecisionEffect(req protocol.NativeDecisionReq) string {
	if req.Kind == "permission_reply" {
		if req.Reply == "reject" {
			return "reject_session_pending_permissions"
		}
		if req.Reply == "always" {
			return "approve_native_patterns_and_matching_session_requests"
		}
	}
	return "single_request"
}
func nativeDecisionResult(id string, req protocol.NativeDecisionReq) protocol.NativeDecisionReceipt {
	return protocol.NativeDecisionReceipt{OperationID: id, Session: req.Session, SessionInstance: req.SessionInstance, RootID: req.RootID, Generation: req.Generation, NativeSessionID: req.NativeSessionID, RequestID: req.RequestID, Kind: req.Kind, State: "uncertain", NativeEffect: nativeDecisionEffect(req)}
}

func (d *Daemon) NativeRequests(local, requestID, target string) (protocol.NativeRequestsReply, error) {
	view, ok := d.openCodeView(local)
	if !ok || !view.CurrentScopeKnown || len(view.Pending) > 128 {
		return protocol.NativeRequestsReply{}, errors.New("native current request scope is unconfirmed")
	}
	out := protocol.NativeRequestsReply{Session: protocol.NamespacedID(d.machineID(), local)}
	if requestID == "" {
		out.Requests = make([]protocol.NativeRequestSummary, 0, len(view.Pending))
		for _, request := range view.Pending {
			summary := request.Permission
			if request.Kind == "question" && len(request.Questions) > 0 {
				summary = request.Questions[0].Question
			}
			out.Requests = append(out.Requests, protocol.NativeRequestSummary{Kind: request.Kind, RequestID: request.ID, NativeSessionID: request.SessionID, Summary: ocPublicText(summary, 512)})
		}
		return out, nil
	}
	cards, err := d.openCodeRequestCards(local, view)
	if err != nil {
		return out, err
	}
	for _, card := range cards {
		if card.RequestID == requestID && card.NativeSessionID == target {
			out.Request = &card
			return out, nil
		}
	}
	for _, pending := range view.Pending {
		if pending.ID == requestID && pending.SessionID == target {
			return out, errors.New("native request is pending but its full context cannot be qualified for a decision")
		}
	}
	return out, errors.New("the exact native request is no longer pending")
}

func (d *Daemon) ApplyNativeDecision(machine, id string, req protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, protocol.ErrorCode, error) {
	result := nativeDecisionResult(id, req)
	if err := protocol.ValidateNativeDecision(req); err != nil {
		return result, protocol.CodeInvalidField, err
	}
	endpoint, local, ok := protocol.ParseID(req.Session)
	if !ok || id == "" || len(id) > 128 || (d.machineID() != "" && (endpoint != d.machineID() || machine != "" && machine != d.machineID())) {
		return result, protocol.CodeInvalidField, errors.New("native decision names another worker or machine")
	}
	lane := d.composerLaneFor(local)
	lane.enter()
	defer lane.leave()
	lane.endMu.Lock()
	defer lane.endMu.Unlock()
	hash := hex.EncodeToString(schema.NativeDecisionContentHash(&req))
	phase, raw, err := d.core.ClaimComposerOperation(id, protocol.OpNativeDecide, local, req.SessionInstance, hash)
	if err != nil {
		return result, protocol.CodeInvalidField, err
	}
	if phase == string(idempotency.PhaseCompleted) || phase == string(idempotency.PhaseFailed) {
		var saved nativeDecisionOutcome
		if json.Unmarshal(raw, &saved) != nil || saved.Receipt.OperationID != id || saved.Receipt.Session != req.Session || saved.Receipt.SessionInstance != req.SessionInstance || saved.Receipt.RootID != req.RootID || saved.Receipt.Generation != req.Generation || saved.Receipt.NativeSessionID != req.NativeSessionID || saved.Receipt.RequestID != req.RequestID || saved.Receipt.Kind != req.Kind || phase == string(idempotency.PhaseCompleted) && saved.Receipt.State != "admitted" || phase == string(idempotency.PhaseFailed) && saved.Receipt.State != "rejected" {
			result.Reason = "native_receipt_custody_unconfirmed"
			return result, "", nil
		}
		if saved.Error != "" {
			return saved.Receipt, saved.Code, errors.New(saved.Error)
		}
		return saved.Receipt, saved.Code, nil
	}
	if phase != string(idempotency.PhasePrepared) {
		return d.reconcileNativeDecision(local, id, req)
	}
	var code protocol.ErrorCode
	attempt := func() error {
		lane.mu.Lock()
		ending := lane.ownerEnding || lane.retired || lane.recycling
		lane.mu.Unlock()
		if ending || lane.recycleRefusal() != "" {
			code = protocol.CodeInputBusy
			return errors.New("native worker is ending or recycling")
		}
		if d.srv != nil && d.srv.IsControlled(local) || d.remoteSrv != nil && d.remoteSrv.IsControlled(local) {
			code = protocol.CodeInputBusy
			return errors.New("native worker is controlled by an attached client")
		}
		binding, e := d.core.SessionOpenCodeBridge(local)
		if e != nil {
			return e
		}
		identity := opencodebridge.Identity{Version: binding.Version, WorkerID: binding.WorkerID, SessionInstance: binding.SessionInstance, RootID: binding.RootID, Directory: binding.Directory, BackendGeneration: binding.BackendGeneration, NativeCreatedAtMS: binding.NativeCreatedAtMS}
		meta, exists := d.core.Get(local)
		instance, current := d.sessionInstance(local)
		if !exists || !current || instance != req.SessionInstance || !openCodeMetaMatches(meta, identity) || identity.RootID != req.RootID || identity.BackendGeneration != req.Generation {
			code = protocol.CodeStaleInstance
			return errors.New("native worker incarnation changed")
		}
		view, exists := d.openCodeView(local)
		if !exists || !view.CurrentScopeKnown || view.Root != req.RootID || view.Generation != req.Generation {
			code = protocol.CodeUnavailable
			return errors.New("native request scope is unconfirmed")
		}
		cards, e := d.openCodeRequestCards(local, view)
		if e != nil {
			code = protocol.CodeUnavailable
			return e
		}
		found := false
		for _, card := range cards {
			if card.RequestID == req.RequestID && card.NativeSessionID == req.NativeSessionID && card.RequestHash == req.RequestHash && card.ExpiresAt != nil && card.ExpiresAt.Equal(*req.ExpiresAt) && time.Now().Before(*req.ExpiresAt) {
				found = true
				break
			}
		}
		if !found {
			code = protocol.CodeStaleApproval
			return errors.New("native request content or expiry changed")
		}
		var pending OpenCodeRequest
		for _, request := range view.Pending {
			if request.ID == req.RequestID && request.SessionID == req.NativeSessionID {
				pending = request
				break
			}
		}
		if e := nativeAnswersMatch(pending, req); e != nil {
			code = protocol.CodeInvalidField
			return e
		}
		priorOperations := d.core.PendingNativeDecisionOperations(local, req.SessionInstance)
		if len(priorOperations) > 128 {
			code = protocol.CodeOutcomeUnknown
			return errors.New("unresolved native decisions exceed the request lookup bound")
		}
		for _, priorID := range priorOperations {
			if priorID == id {
				continue
			}
			prior, exists := d.core.ComposerOperation(priorID)
			if !exists {
				continue
			}
			var intent nativeDecisionIntent
			if json.Unmarshal(prior.Request, &intent) != nil {
				code = protocol.CodeOutcomeUnknown
				return errors.New("prior native decision custody is unconfirmed")
			}
			if intent.Identity.BackendGeneration == identity.BackendGeneration && intent.Request.NativeSessionID == req.NativeSessionID && intent.Request.RequestID == req.RequestID {
				code = protocol.CodeOutcomeUnknown
				return errors.New("this exact native request has an unconfirmed prior answer; it was not replayed")
			}
		}
		dispatch := opencodebridge.DispatchRequest{Version: 1, SessionInstance: binding.SessionInstance, BackendGeneration: binding.BackendGeneration, RootID: binding.RootID, OperationID: id, Kind: req.Kind, TargetID: req.NativeSessionID, RequestID: req.RequestID, Reply: req.Reply, Answers: req.Answers, ExpectedRequest: append(json.RawMessage(nil), pending.Raw...), RequestExpiresAtMS: req.ExpiresAt.UnixMilli()}
		encoded, e := json.Marshal(dispatch)
		if e != nil || len(encoded) > 64<<10 {
			code = protocol.CodeInvalidField
			return errors.New("encoded native decision exceeds bridge boundary")
		}
		intent := nativeDecisionIntent{Identity: identity, Request: req, Dispatch: dispatch}
		encoded, e = json.Marshal(intent)
		if e != nil {
			return e
		}
		if e = d.core.BindNativeDecisionRequest(id, encoded); e != nil {
			return e
		}
		if e = d.core.BeginComposerOperation(id); e != nil {
			result.Reason = "execution_custody_unconfirmed"
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 32*time.Second)
		defer cancel()
		receipt, e := d.core.DispatchOpenCode(ctx, local, dispatch)
		if e != nil {
			result.Reason = "native_receipt_unavailable"
			return nil
		}
		if receipt.Status == "refused" && receipt.OperationID == id {
			result.State = "rejected"
			result.Reason = ocPublicText(receipt.Reason, 256)
			result.NativeEffect = "none"
			return nil
		}
		d.acceptNativeDecisionReceipt(id, intent, receipt, &result)
		return nil
	}
	fenced := attempt
	if d.remoteSrv != nil {
		next := fenced
		fenced = func() error { return d.remoteSrv.WithInputFence(local, next) }
	}
	if d.srv != nil {
		next := fenced
		fenced = func() error { return d.srv.WithInputFence(local, next) }
	}
	err = fenced()
	if err != nil {
		result.State = "rejected"
		result.Reason = ocPublicText(err.Error(), 256)
		result.NativeEffect = "none"
	}
	if result.State == "uncertain" {
		return result, "", nil
	}
	outcome := nativeDecisionOutcome{Receipt: result, Code: code}
	if err != nil {
		outcome.Error = err.Error()
	}
	encoded, _ := json.Marshal(outcome)
	if commitErr := d.core.CommitComposerOperation(id, encoded, result.State == "admitted"); commitErr != nil {
		result.State = "uncertain"
		result.Reason = "native_receipt_commit_unconfirmed"
		return result, "", nil
	}
	return result, code, err
}

func nativeAnswersMatch(pending OpenCodeRequest, req protocol.NativeDecisionReq) error {
	if req.Kind == "permission_reply" {
		if pending.Kind != "permission" {
			return errors.New("native request kind changed")
		}
		return nil
	}
	if pending.Kind != "question" {
		return errors.New("native request kind changed")
	}
	if req.Kind == "question_reject" {
		return nil
	}
	if len(req.Answers) != len(pending.Questions) {
		return errors.New("answers do not match native questions")
	}
	for i, question := range pending.Questions {
		if !question.Multiple && len(req.Answers[i]) > 1 {
			return errors.New("native question allows one answer")
		}
		offered := map[string]bool{}
		for _, option := range question.Options {
			if offered[option.Label] {
				return errors.New("native question has duplicate labels")
			}
			offered[option.Label] = true
		}
		seen := map[string]bool{}
		for _, answer := range req.Answers[i] {
			if seen[answer] || question.Custom != nil && !*question.Custom && !offered[answer] {
				return errors.New("answer was not offered by the native question")
			}
			seen[answer] = true
		}
	}
	return nil
}

func (d *Daemon) acceptNativeDecisionReceipt(id string, intent nativeDecisionIntent, receipt opencodebridge.Receipt, result *protocol.NativeDecisionReceipt) {
	if receipt.Identity != intent.Identity || receipt.OperationID != id || receipt.Kind != intent.Request.Kind || receipt.TargetID != intent.Request.NativeSessionID || receipt.RequestID != intent.Request.RequestID {
		result.Reason = "native_receipt_identity_unconfirmed"
		return
	}
	if receipt.Status == "rejected" {
		result.State = "rejected"
		result.Reason = receipt.Reason
		result.NativeEffect = "none"
		return
	}
	if receipt.Status != "admitted" || receipt.NativeEffect != nativeDecisionEffect(intent.Request) {
		result.Reason = "native_processing_unconfirmed"
		return
	}
	encoded, _ := json.Marshal(receipt)
	if err := d.core.ObserveNativeDecisionReceipt(id, encoded); err != nil {
		result.Reason = "native_evidence_persistence_failed"
		return
	}
	result.State = "admitted"
	result.Reason = ""
	result.NativeEffect = receipt.NativeEffect
	return
}

func (d *Daemon) reconcileNativeDecision(local, id string, req protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, protocol.ErrorCode, error) {
	result := nativeDecisionResult(id, req)
	record, ok := d.core.ComposerOperation(id)
	var intent nativeDecisionIntent
	if !ok || json.Unmarshal(record.Request, &intent) != nil || hex.EncodeToString(schema.NativeDecisionContentHash(&intent.Request)) != record.RequestHash || intent.Request.RequestID != req.RequestID || intent.Identity.WorkerID != local || intent.Identity.RootID != req.RootID || intent.Identity.BackendGeneration != req.Generation || intent.Identity.NativeCreatedAtMS <= 0 {
		result.Reason = "native_decision_custody_unconfirmed"
		return result, "", nil
	}
	var receipt opencodebridge.Receipt
	if len(record.Evidence) > 0 {
		if json.Unmarshal(record.Evidence, &receipt) != nil {
			result.Reason = "native_receipt_custody_unconfirmed"
			return result, "", nil
		}
	} else {
		binding, err := d.core.SessionOpenCodeBridge(local)
		if err != nil || binding.RootID != intent.Identity.RootID || binding.BackendGeneration != intent.Identity.BackendGeneration || binding.NativeCreatedAtMS != intent.Identity.NativeCreatedAtMS || binding.Directory != intent.Identity.Directory {
			result.Reason = "native_receipt_generation_unavailable"
			return result, "", nil
		}
		client, err := opencodebridge.NewClient(binding.SocketPath, binding.Token)
		if err != nil {
			return result, "", nil
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		live, err := client.OperationReceipt(ctx, id)
		if err != nil || live == nil {
			result.Reason = "native_processing_unconfirmed"
			return result, "", nil
		}
		receipt = *live
	}
	d.acceptNativeDecisionReceipt(id, intent, receipt, &result)
	if result.State == "uncertain" {
		return result, "", nil
	}
	encoded, _ := json.Marshal(nativeDecisionOutcome{Receipt: result})
	if err := d.core.CommitComposerOperation(id, encoded, result.State == "admitted"); err != nil {
		result.State = "uncertain"
		result.Reason = "native_receipt_commit_unconfirmed"
	}
	return result, "", nil
}

func (a *coreAPI) ApplyNativeDecision(machine, id string, req protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, protocol.ErrorCode, error) {
	if a.nativeDecision == nil {
		return protocol.NativeDecisionReceipt{}, protocol.CodeNotImplemented, errors.New("native decisions are unavailable")
	}
	return a.nativeDecision(machine, id, req)
}
func (a *coreAPI) NativeRequests(local, requestID, target string) (protocol.NativeRequestsReply, error) {
	if a.nativeRequestRead == nil {
		return protocol.NativeRequestsReply{}, errors.New("native request reader is unavailable")
	}
	return a.nativeRequestRead(local, requestID, target)
}

func (d *Daemon) NativeDecisionStatus(local, id string) (protocol.NativeDecisionReceipt, error) {
	lane := d.composerLaneFor(local)
	lane.enter()
	defer lane.leave()
	record, ok := d.core.ComposerOperation(id)
	if !ok || record.Action != protocol.OpNativeDecide || record.SessionID != local {
		return protocol.NativeDecisionReceipt{}, errors.New("no native decision operation belongs to this worker")
	}
	if record.Phase == idempotency.PhaseCompleted || record.Phase == idempotency.PhaseFailed {
		var outcome nativeDecisionOutcome
		if json.Unmarshal(record.Outcome, &outcome) != nil || outcome.Receipt.OperationID != id || outcome.Receipt.Session != protocol.NamespacedID(d.machineID(), local) {
			return protocol.NativeDecisionReceipt{}, errors.New("native decision receipt custody is unconfirmed")
		}
		return outcome.Receipt, nil
	}
	var intent nativeDecisionIntent
	if json.Unmarshal(record.Request, &intent) != nil {
		return protocol.NativeDecisionReceipt{OperationID: id, Session: protocol.NamespacedID(d.machineID(), local), SessionInstance: record.SessionInstance, State: "uncertain", Reason: "native_decision_intent_unavailable"}, nil
	}
	receipt, _, err := d.reconcileNativeDecision(local, id, intent.Request)
	return receipt, err
}

func (a *coreAPI) NativeDecisionStatus(local, id string) (protocol.NativeDecisionReceipt, error) {
	if a.nativeDecisionStatus == nil {
		return protocol.NativeDecisionReceipt{}, errors.New("native decision status is unavailable")
	}
	return a.nativeDecisionStatus(local, id)
}
