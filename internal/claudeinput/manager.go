package claudeinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
)

type managerTool struct {
	CallID    string          `json:"call_id"`
	TurnID    string          `json:"turn_id"`
	AgentID   string          `json:"agent_id,omitempty"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}
type managerInput struct {
	ExpectedExternalInput                                  uint64
	OperationID, Envelope, Hash, MessageID, ProjectionHash string
	Prompted                                               bool
}

func humanHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ManagerObservation names the retained authenticated guard, not a daemon dial.
func (c *Client) ManagerObservation(ctx context.Context, worker Worker) (string, error) {
	var out struct {
		Generation string `json:"generation"`
	}
	err := c.call(ctx, "/manager-observation", request{Worker: worker}, &out)
	if err == nil && out.Generation == "" {
		return "", managerproof.ErrUnavailable
	}
	return out.Generation, err
}
func (b *Broker) managerObservationLocked() string {
	return b.descriptor.Generation + ":" + b.guard.Generation
}
func (c *Client) BindManager(ctx context.Context, worker Worker, binding managerproof.Binding) error {
	return c.call(ctx, "/manager-bind", request{Worker: worker, Binding: &binding}, nil)
}
func (c *Client) SubmitManager(ctx context.Context, worker Worker, id, text string, admission managerproof.Admission) (Message, error) {
	var out Message
	err := c.call(ctx, "/manager-submit", request{Worker: worker, OperationID: id, Text: text, Admission: &admission}, &out)
	return out, err
}
func (c *Client) ResolveManagerCall(ctx context.Context, worker Worker, req managerproof.CallRequest) (managerproof.Proof, error) {
	var out managerproof.Proof
	err := c.call(ctx, "/manager-resolve", request{Worker: worker, ProofRequest: &req}, &out)
	return out, err
}
func (b *Broker) liveLocked() bool {
	return !b.revoked && b.worker.Generation != "" && b.guard.Generation != "" && b.guard.NativeSessionID == b.worker.NativeSessionID && b.guard.Version == b.worker.Version && time.Since(b.seen) < 3*time.Second && time.Since(b.guardSeen) < 3*time.Second
}
func (b *Broker) serveManagerLocked(path string, req request, respond func(any), refuse func(string)) bool {
	switch path {
	case "/manager-observation", "/manager-bind", "/manager-submit", "/manager-prompt", "/manager-append", "/manager-tool-step", "/manager-tool", "/manager-tool-complete", "/manager-idle", "/manager-input-status":
	default:
		return false
	}
	if path == "/manager-idle" {
		if b.descriptor.ManagerConversationID == "" || !b.descriptor.NativeTurns || b.revoked || req.Worker != b.guard || b.guard.Generation == "" || req.TurnID != b.turn.TurnID || b.turn.State != "idle" && b.turn.State != "completed" {
			refuse("native idle continuation unavailable")
			return true
		}
		b.nativeInputReady = true
		if b.manager != nil {
			if err := b.manager.ObserveThreadState("idle"); err != nil {
				refuse("native idle proof retired")
				return true
			}
		}
		respond(nil)
		return true
	}
	if b.descriptor.ManagerConversationID == "" || !b.descriptor.NativeTurns || !b.liveLocked() {
		refuse("manager native proof unavailable")
		return true
	}
	if path == "/manager-observation" {
		if req.Worker != b.worker {
			refuse("manager observer identity mismatch")
			return true
		}
		respond(map[string]string{"generation": b.managerObservationLocked()})
		return true
	}
	if path == "/manager-bind" {
		if req.Worker != b.worker || req.Binding == nil || req.Binding.Validate() != nil || req.Binding.Session != b.descriptor.SessionID || req.Binding.Provider != "claude" || req.Binding.ConversationID != b.descriptor.ManagerConversationID || req.Binding.NativeSessionID != b.worker.NativeSessionID || req.Binding.ObservationGeneration != b.managerObservationLocked() {
			refuse("manager binding mismatch")
			return true
		}
		if b.manager != nil {
			snapshot, err := b.manager.Snapshot()
			if err != nil || snapshot.Binding != *req.Binding {
				refuse("manager binding retired or changed")
				return true
			}
			respond(nil)
			return true
		}
		registry, err := managerproof.NewRegistry(*req.Binding, b.turn.ExternalInput)
		if err != nil {
			refuse("invalid manager binding")
			return true
		}
		b.manager = registry
		if b.turn.TurnID != "" {
			_ = registry.StartTurn(b.turn.TurnID, "")
			if b.turn.State == "completed" {
				registry.CloseTurn(b.turn.TurnID, b.turn.Reason)
			}
		}
		if b.nativeInputReady {
			_ = registry.ObserveThreadState("idle")
		}
		respond(nil)
		return true
	}
	if b.manager == nil {
		refuse("manager binding unavailable")
		return true
	}
	if path == "/manager-input-status" {
		snapshot, err := b.manager.Snapshot()
		if err != nil || req.Worker != b.worker || req.Binding == nil || *req.Binding != snapshot.Binding {
			refuse("manager input history binding unavailable")
			return true
		}
		outcome, err := b.manager.InputStatus(req.NativeInputID)
		if err != nil {
			refuse("manager input history unavailable")
			return true
		}
		if input, ok := b.managerInputs[req.NativeInputID]; ok {
			if message := b.operations[input.OperationID]; message.State == "rejected" {
				outcome.State = "rejected"
				outcome.Reason = message.Reason
			}
		}
		respond(outcome)
		return true
	}
	if path == "/manager-submit" {
		snapshot, err := b.manager.Snapshot()
		if err != nil || req.Worker != b.worker || req.Admission == nil || req.Admission.Validate() != nil || req.Admission.Binding != snapshot.Binding || req.OperationID == "" || len(req.OperationID) > 128 || req.Text == "" || strings.Contains(req.Text, managerEnvelopePrefix) {
			refuse("invalid manager admission")
			return true
		}
		envelope, validText := ManagerEnvelope(req.Admission.Nonce, req.Text)
		if validText != nil {
			refuse("manager text exceeds native envelope capacity")
			return true
		}
		digest := humanHash(req.Text)
		if old, ok := b.operations[req.OperationID]; ok {
			if old.Hash != digest || old.NativeInputID != req.Admission.Nonce || old.Worker != req.Worker || b.managerInputs[req.Admission.Nonce].ExpectedExternalInput != req.Admission.ExpectedExternalInput {
				refuse("manager input identity conflict")
				return true
			}
			respond(old)
			return true
		}
		if _, used := b.managerInputs[req.Admission.Nonce]; used {
			refuse("manager nonce already used; original receipt unavailable")
			return true
		}
		if len(b.operations) >= maxOperations {
			refuse("manager input receipt capacity reached")
			return true
		}
		if err := b.manager.Prepare(req.Admission.Nonce, req.OperationID, digest, req.Admission.ExpectedExternalInput); err != nil {
			refuse("manager admission conflict or capacity")
			return true
		}
		if b.managerInputs == nil {
			b.managerInputs = map[string]managerInput{}
		}
		if old, ok := b.managerInputs[req.Admission.Nonce]; ok && old.OperationID != req.OperationID {
			refuse("manager nonce already registered")
			return true
		}
		b.managerInputs[req.Admission.Nonce] = managerInput{OperationID: req.OperationID, Envelope: envelope, Hash: digest, ExpectedExternalInput: req.Admission.ExpectedExternalInput}
		message := Message{OperationID: req.OperationID, Text: envelope, Hash: digest, State: "prepared", Worker: b.worker, NativeInputID: req.Admission.Nonce}
		b.operations[req.OperationID] = message
		b.order = append(b.order, req.OperationID)
		respond(message)
		return true
	}
	if req.Worker != b.guard {
		refuse("manager observer identity mismatch")
		return true
	}
	if path == "/manager-prompt" {
		if !strings.HasPrefix(req.Text, managerEnvelopePrefix) || len(req.Text) < len(managerEnvelopePrefix)+66 {
			refuse("missing machine input envelope")
			return true
		}
		nonce := req.Text[len(managerEnvelopePrefix) : len(managerEnvelopePrefix)+64]
		input, ok := b.managerInputs[nonce]
		if !ok || input.Prompted || input.Envelope != req.Text {
			refuse("unattested or repeated machine envelope")
			return true
		}
		input.Prompted = true
		b.managerInputs[nonce] = input
		respond(map[string]string{"nonce": nonce})
		return true
	}
	if path == "/manager-append" {
		input, ok := b.managerInputs[req.NativeInputID]
		if !ok || !input.Prompted || input.MessageID != "" || req.MessageID == "" || len(req.MessageID) > 128 || req.Projection != managerProjection(b.descriptor.SenderName, input.Envelope) {
			b.revokeLocked()
			refuse("native input projection mismatch")
			return true
		}
		input.MessageID = req.MessageID
		input.ProjectionHash = humanHash(req.Projection)
		b.managerInputs[req.NativeInputID] = input
		respond(nil)
		return true
	}
	if req.Call == nil {
		refuse("native tool evidence missing")
		return true
	}
	call := req.Call
	var err error
	if path == "/manager-tool-step" {
		err = b.manager.ToolStep(call.CallID, call.TurnID, call.AgentID, call.ToolName)
	} else {
		var digest string
		digest, err = managerproof.ArgumentsHash(call.Arguments)
		if err == nil {
			if path == "/manager-tool-complete" {
				err = b.manager.CompleteTool(call.CallID, call.TurnID, call.AgentID, call.ToolName, digest)
			} else {
				err = b.manager.AttestTool(call.CallID, call.TurnID, call.AgentID, call.ToolName, digest)
			}
		}
	}
	if err != nil {
		b.revokeLocked()
		refuse("native tool evidence rejected")
		return true
	}
	respond(nil)
	return true
}
func (b *Broker) observeManagerTurnLocked(req request) error {
	if b.manager == nil {
		return nil
	}
	turn := req.Turn
	if turn.State == "completed" {
		b.manager.CloseTurn(turn.TurnID, turn.Reason)
		return nil
	}
	if turn.State != "running" {
		return nil
	}
	b.nativeInputReady = false
	if err := b.manager.StartTurn(turn.TurnID, ""); err != nil {
		return err
	}
	if turn.NativeInputID == "" {
		return nil
	}
	input, ok := b.managerInputs[turn.NativeInputID]
	if !ok || input.MessageID == "" || input.MessageID != turn.NativeMessageID || input.ProjectionHash == "" || input.ProjectionHash != humanHash(req.Projection) {
		return managerproof.ErrUnbound
	}
	if err := b.manager.BindInput(turn.NativeInputID, turn.TurnID, input.MessageID); err != nil {
		return err
	}
	message := b.operations[input.OperationID]
	message.State = "started"
	message.NativeTurnID = turn.TurnID
	message.NativeMessageID = input.MessageID
	b.operations[input.OperationID] = message
	input.Envelope = ""
	b.managerInputs[turn.NativeInputID] = input
	return nil
}
func (b *Broker) resolveManager(w http.ResponseWriter, r *http.Request, req request) {
	b.mu.Lock()
	registry := b.manager
	valid := b.liveLocked() && registry != nil && req.Worker == b.worker && req.ProofRequest != nil
	b.mu.Unlock()
	if !valid {
		http.Error(w, "manager native proof unavailable", http.StatusConflict)
		return
	}
	proof, err := registry.Resolve(r.Context(), *req.ProofRequest)
	b.mu.Lock()
	valid = b.liveLocked() && b.manager == registry
	b.mu.Unlock()
	if err != nil || !valid {
		http.Error(w, "manager native proof unavailable", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(proof)
}

func (c *Client) ManagerInputStatus(ctx context.Context, worker Worker, binding managerproof.Binding, nonce string) (managerproof.Turn, error) {
	var out managerproof.Turn
	err := c.call(ctx, "/manager-input-status", request{Worker: worker, Binding: &binding, NativeInputID: nonce}, &out)
	return out, err
}
