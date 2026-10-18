package swarmmobile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// appendNativeDecisionLocked shares the caller's allocation-to-append bucket lock.
func (a *App) appendNativeDecisionLocked(core *phonecore.Core, sc sendCtx, id, itemID string, body schema.NativeDecisionReq, envelope []byte) (*Op, error) {
	operation, fresh, err := core.BeginNativeDecision(id, itemID, body, time.Now())
	if err != nil {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: native answer was not published: "+err.Error()))
	}
	if !fresh {
		return &Op{Action: schema.ActionNativeDecide, SessionID: body.Session, OperationID: operation}, nil
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, envelope); err != nil {
		receipt := &schema.NativeDecisionReceipt{OperationID: id, Session: body.Session, SessionInstance: body.SessionInstance,
			RootID: body.RootID, Generation: body.Generation, NativeSessionID: body.NativeSessionID, RequestID: body.RequestID,
			Kind: body.Kind, State: "uncertain", Reason: "Mailbox admission is unconfirmed; keep this operation ID and inspect its outcome."}
		if saveErr := core.RecordOutcomeIfAbsent(schema.Control{Op: schema.ActionNativeDecide, OperationID: id, SessionID: body.Session, NativeDecisionReceipt: receipt}); saveErr != nil {
			return nil, classed(ErrClassInternal, errors.New("swarmmobile: native answer operation_id="+id+" is uncertain and its local receipt could not be retained"))
		}
	}
	return &Op{Action: schema.ActionNativeDecide, SessionID: body.Session, OperationID: id}, nil
}

// ReplyNativeRequest answers the exact full card this handset holds. Answers are JSON string groups.
func (a *App) ReplyNativeRequest(session, itemID, expectedRequestHash, decisionID, answersJSON string) (op *Op, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	binding, ok := core.Router().Items().PendingNativeRequest(session, itemID)
	if !ok {
		return nil, classed(ErrClassNotFound, errors.New("swarmmobile: this native request is unavailable, incomplete, expired, or already resolved"))
	}
	if expectedRequestHash == "" || binding.Request.RequestHash != expectedRequestHash {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: this native request changed after it was displayed; inspect its current content before answering"))
	}
	body, err := nativeAnswer(binding.Request, decisionID, answersJSON)
	if err != nil {
		return nil, err
	}
	return a.signedCommand(schema.ActionNativeDecide, session, nil, commandBody{nativeDecision: &body, nativeItem: itemID})
}

func nativeAnswer(body schema.NativeDecisionReq, decision, answersJSON string) (schema.NativeDecisionReq, error) {
	invalid := classed(ErrClassInvalidRequest, errors.New("swarmmobile: native answer does not match the request kind or answer format"))
	body.Reply, body.Answers = "", nil
	switch body.Kind {
	case "permission_reply":
		if decision != "once" && decision != "always" && decision != "reject" || strings.TrimSpace(answersJSON) != "" {
			return body, invalid
		}
		body.Reply = decision
	case "question_reply":
		if decision == "question_reject" {
			if strings.TrimSpace(answersJSON) != "" {
				return body, invalid
			}
			body.Kind = "question_reject"
			return body, nil
		}
		if decision != "question_reply" || len(answersJSON) > 32<<10 || json.Unmarshal([]byte(answersJSON), &body.Answers) != nil || len(body.Answers) == 0 || len(body.Answers) > 16 {
			return body, invalid
		}
		for _, group := range body.Answers {
			if len(group) == 0 || len(group) > 128 {
				return body, invalid
			}
			for _, answer := range group {
				if answer == "" || len(answer) > 4096 || strings.ContainsRune(answer, 0) {
					return body, classed(ErrClassInvalidRequest, errors.New("swarmmobile: each native answer must contain 1 to 4096 bytes"))
				}
			}
		}
		encoded, _ := json.Marshal(body.Answers)
		if len(encoded) > 16384 {
			return body, invalid
		}
	default:
		return body, invalid
	}
	return body, nil
}
