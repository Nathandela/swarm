package voice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
)

type managerRecoveryRequest struct {
	Expected      ManagerBinding  `json:"expected"`
	ExternalInput *uint64         `json:"external_input,omitempty"`
	Admission     uint64          `json:"admission,omitempty"`
	OperationID   string          `json:"operation_id,omitempty"`
	NativeInputID string          `json:"native_input_id,omitempty"`
	NativeTurnID  string          `json:"native_turn_id,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
	RequestHash   string          `json:"request_hash,omitempty"`
	AnswerID      string          `json:"answer_id,omitempty"`
	Answer        json.RawMessage `json:"answer,omitempty"`
}

func readManagerRecoveryRequest(r *http.Request, fields ...string) (managerRecoveryRequest, error) {
	var req managerRecoveryRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxControlFrameBytes+1))
	if err != nil || r.Header.Get("Content-Type") != "application/json" || len(raw) > MaxControlFrameBytes ||
		validateObjectMembers(raw, append(fields, "expected")...) != nil {
		return req, ErrInvalid
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil ||
		validateObjectMembers(members["expected"], "conversation_id", "provider", "session_id", "session_instance",
			"native_session_id", "native_generation", "observation_generation", "login_epoch", "generation") != nil ||
		json.Unmarshal(raw, &req) != nil || req.Expected.Generation == 0 {
		return req, ErrInvalid
	}
	return req, nil
}

func (c *managerControl) recoveryAction(r *http.Request) (any, error) {
	var fields []string
	switch r.URL.Path {
	case "/v1/recovery/interrupt", "/v1/recovery/interrupt-status":
		if r.Method != http.MethodPost {
			return nil, ErrNotFound
		}
		req, err := readManagerRecoveryRequest(r, "native_input_id", "native_turn_id")
		if err != nil {
			return nil, err
		}
		return c.interruptManager(r.Context(), req, r.URL.Path == "/v1/recovery/interrupt-status")
	case "/v1/recovery/replacement":
		if r.Method == http.MethodGet {
			return c.replacementStatus()
		}
		if r.Method != http.MethodPost {
			return nil, ErrNotFound
		}
		req, err := readManagerRecoveryRequest(r)
		if err != nil {
			return nil, err
		}
		return c.queueReplacement(r.Context(), req.Expected)
	case "/v1/recovery/clarification":
		if r.Method == http.MethodGet {
			return c.clarification()
		}
		return nil, ErrNotFound
	case "/v1/recovery/resume":
		fields = []string{"external_input"}
	case "/v1/recovery/acknowledge":
		fields = []string{"external_input", "admission", "operation_id", "native_turn_id"}
	case "/v1/recovery/answer":
		fields = []string{"request_id", "request_hash", "answer_id", "answer"}
	default:
		return nil, ErrNotFound
	}
	if r.Method != http.MethodPost {
		return nil, ErrNotFound
	}
	req, err := readManagerRecoveryRequest(r, fields...)
	if err != nil {
		return nil, err
	}
	if c.controller == nil || c.binding != req.Expected {
		return nil, ErrStale
	}
	if err = c.store.checkControllerBinding(c.binding); err != nil {
		return nil, localRecoveryError(err)
	}
	var result any = struct{}{}
	switch r.URL.Path {
	case "/v1/recovery/resume":
		if req.ExternalInput == nil {
			return nil, ErrInvalid
		}
		err = c.controller.ResumeManual(r.Context(), *req.ExternalInput)
	case "/v1/recovery/acknowledge":
		if req.ExternalInput == nil || req.Admission == 0 || !configText(req.OperationID, 128) || !configText(req.NativeTurnID, 256) {
			return nil, ErrInvalid
		}
		err = c.controller.AcknowledgeUncertainty(r.Context(), InputRecoveryAck{
			Admission: req.Admission, OperationID: req.OperationID, NativeTurnID: req.NativeTurnID, ExternalInput: *req.ExternalInput,
		})
	case "/v1/recovery/answer":
		if !configText(req.RequestID, 128) || !managerproof.ValidHash(req.RequestHash) || !configText(req.AnswerID, 128) || len(req.Answer) == 0 {
			return nil, ErrInvalid
		}
		var receipt Admission
		receipt, err = c.controller.AnswerQuestion(req.RequestID, req.RequestHash, req.AnswerID, req.Answer)
		receipt.Request, receipt.InputText = nil, ""
		result = receipt
	}
	if err != nil {
		return nil, localRecoveryError(err)
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return result, nil
}

func localRecoveryError(err error) error {
	if errors.Is(err, managerproof.ErrQuarantined) {
		return ErrStale
	}
	return err
}

func (c *managerControl) clarification() (Request, error) {
	st, err := c.store.Snapshot()
	if err != nil {
		return Request{}, err
	}
	if err = checkBinding(&st, c.binding); err != nil {
		return Request{}, err
	}
	now := time.Now()
	var next Request
	for _, q := range st.Requests {
		if q.ConversationID == c.binding.ConversationID && q.Kind == "manager" && q.State == "pending" && now.Before(q.ExpiresAt) &&
			!st.CancelledTasks[key(q.ConversationID, q.TaskID)] &&
			(next.ID == "" || q.ExpiresAt.Before(next.ExpiresAt) || q.ExpiresAt.Equal(next.ExpiresAt) && q.ID < next.ID) {
			next = q
		}
	}
	if next.ID == "" {
		return Request{}, ErrNotFound
	}
	return next, nil
}

func (c *ManagerClient) ResumeManual(ctx context.Context, expected ManagerBinding, externalInput uint64) error {
	return c.callJSON(ctx, http.MethodPost, "/v1/recovery/resume", &managerRecoveryRequest{Expected: expected, ExternalInput: &externalInput}, &struct{}{})
}

func (c *ManagerClient) AcknowledgeUncertainty(ctx context.Context, expected ManagerBinding, ack InputRecoveryAck) error {
	req := &managerRecoveryRequest{Expected: expected, ExternalInput: &ack.ExternalInput, Admission: ack.Admission, OperationID: ack.OperationID, NativeTurnID: ack.NativeTurnID}
	return c.callJSON(ctx, http.MethodPost, "/v1/recovery/acknowledge", req, &struct{}{})
}

func (c *ManagerClient) Clarification(ctx context.Context) (Request, error) {
	var result Request
	if err := c.callJSON(ctx, http.MethodGet, "/v1/recovery/clarification", nil, &result); err != nil {
		return Request{}, err
	}
	if result.ID == "" || !managerproof.ValidHash(result.Hash) || result.Kind != "manager" || result.State != "pending" {
		return Request{}, ErrCorrupt
	}
	return result, nil
}

func (c *ManagerClient) AnswerQuestion(ctx context.Context, expected ManagerBinding, id, hash, answerID string, answer json.RawMessage) (Admission, error) {
	var result Admission
	req := &managerRecoveryRequest{Expected: expected, RequestID: id, RequestHash: hash, AnswerID: answerID, Answer: answer}
	if err := c.callJSON(ctx, http.MethodPost, "/v1/recovery/answer", req, &result); err != nil {
		return Admission{}, err
	}
	if result.Sequence == 0 || result.ConversationID != expected.ConversationID || result.SourceID != "answer:"+key(id, answerID) {
		return Admission{}, ErrCorrupt
	}
	return result, nil
}

// ReplaceManager records an explicit fresh launch. Poll Replacement for its outcome.
func (c *ManagerClient) ReplaceManager(ctx context.Context, expected ManagerBinding) (ManagerReplacement, error) {
	var result ManagerReplacement
	req := &managerRecoveryRequest{Expected: expected}
	if err := c.callJSON(ctx, http.MethodPost, "/v1/recovery/replacement", req, &result); err != nil {
		return ManagerReplacement{}, err
	}
	if identity(result.Previous) != identity(expected) || result.OperationID == "" {
		return ManagerReplacement{}, ErrCorrupt
	}
	return result, nil
}

func (c *ManagerClient) Replacement(ctx context.Context) (ManagerReplacement, error) {
	var result ManagerReplacement
	if err := c.callJSON(ctx, http.MethodGet, "/v1/recovery/replacement", nil, &result); err != nil {
		return ManagerReplacement{}, err
	}
	if result.Previous.Generation == 0 || result.OperationID == "" {
		return ManagerReplacement{}, ErrCorrupt
	}
	return result, nil
}
