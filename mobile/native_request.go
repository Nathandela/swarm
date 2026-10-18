package swarmmobile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func (a *App) appendNativeDecision(core *phonecore.Core, sc sendCtx, id string, body schema.NativeDecisionReq, envelope []byte) (*Op, error) {
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, envelope); err != nil {
		receipt := &schema.NativeDecisionReceipt{OperationID: id, Session: body.Session, SessionInstance: body.SessionInstance,
			RootID: body.RootID, Generation: body.Generation, NativeSessionID: body.NativeSessionID, RequestID: body.RequestID,
			Kind: body.Kind, State: "uncertain", Reason: "Mailbox admission is unconfirmed; keep this operation ID and inspect its outcome."}
		if saveErr := core.RecordOutcomeIfAbsent(schema.Control{Op: schema.ActionNativeDecide, OperationID: id, SessionID: body.Session, NativeDecisionReceipt: receipt}); saveErr != nil {
			return nil, errors.New("swarmmobile: native answer operation_id=" + id + " is uncertain and its local receipt could not be retained")
		}
	}
	return &Op{Action: schema.ActionNativeDecide, SessionID: body.Session, OperationID: id}, nil
}

// ReplyNativeRequest answers the exact full card this handset holds. Answers are JSON string groups.
func (a *App) ReplyNativeRequest(session, itemID, decisionID, answersJSON string) (op *Op, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	binding, ok := core.Router().Items().PendingNativeRequest(session, itemID)
	if !ok {
		return nil, classed(ErrClassNotFound, errors.New("swarmmobile: this native request is unavailable, incomplete, expired, or already resolved"))
	}
	body, err := nativeAnswer(binding.Request, decisionID, answersJSON)
	if err != nil {
		return nil, classed(ErrClassInvalidRequest, err)
	}
	return a.signedCommand(schema.ActionNativeDecide, session, nil, commandBody{nativeDecision: &body})
}

func nativeAnswer(body schema.NativeDecisionReq, decision, answersJSON string) (schema.NativeDecisionReq, error) {
	invalid := errors.New("swarmmobile: native answer does not match the request kind or answer format")
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
		if decision != "question_reply" || len(answersJSON) > 32<<10 || json.Unmarshal([]byte(answersJSON), &body.Answers) != nil || len(body.Answers) == 0 || len(body.Answers) > 32 {
			return body, invalid
		}
		for _, group := range body.Answers {
			if len(group) == 0 || len(group) > 64 {
				return body, invalid
			}
			for _, answer := range group {
				if answer == "" || len(answer) > 4096 {
					return body, errors.New("swarmmobile: each native answer must contain 1 to 4096 bytes")
				}
			}
		}
	default:
		return body, invalid
	}
	return body, nil
}
