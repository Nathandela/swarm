package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/jsontext"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

type codexManagerIntent struct {
	ExpectedExternalInput uint64               `json:"expected_external_input"`
	Binding               managerproof.Binding `json:"binding"`
	Nonce                 string               `json:"nonce"`
	TextHash              string               `json:"text_hash"`
	OperationID           string               `json:"operation_id"`
}

type codexManagerEvidence struct {
	Version int                `json:"version"`
	Intent  codexManagerIntent `json:"intent"`
	Receipt struct {
		Status    string `json:"status"`
		TurnID    string `json:"turn_id"`
		MessageID string `json:"message_id,omitempty"`
	} `json:"receipt"`
	Outcome       string `json:"outcome,omitempty"`
	OutcomeSource string `json:"outcome_source,omitempty"`
	OutcomeAt     int64  `json:"outcome_at,omitempty"`
}

type codexManagerEvent struct {
	method, thread, turn, parent, state, kind, id, client, textHash, tool, argumentsHash string
}

// This buffer belongs to one physical feed. Reconnection never restores permissions
// from a durable receipt: missing native lifecycle evidence remains unknown.
type codexManagerFeed struct {
	mu      sync.Mutex
	state   *codexManagerState
	pending []codexManagerEvent
	dropped bool
}
type codexManagerState struct {
	mu         sync.Mutex
	binding    managerproof.Binding
	registry   *managerproof.Registry
	intents    map[string]codexManagerIntent
	evidence   map[string]codexManagerEvidence
	unretained map[string]codexManagerEvidence
	messages   map[string]codexManagerEvent
}

func codexManagerTextHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
func newCodexManagerState(binding managerproof.Binding) (*codexManagerState, error) {
	if binding.Provider != "codex" {
		return nil, managerproof.ErrInvalid
	}
	r, err := managerproof.NewRegistry(binding)
	if err != nil {
		return nil, err
	}
	return &codexManagerState{binding: binding, registry: r, intents: map[string]codexManagerIntent{}, evidence: map[string]codexManagerEvidence{}, unretained: map[string]codexManagerEvidence{}, messages: map[string]codexManagerEvent{}}, nil
}
func (s *codexManagerState) prepare(intent codexManagerIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if intent.Binding != s.binding {
		return managerproof.ErrStale
	}
	if err := s.registry.Prepare(intent.Nonce, intent.OperationID, intent.TextHash, intent.ExpectedExternalInput); err != nil {
		return err
	}
	if old, ok := s.intents[intent.Nonce]; ok && old != intent {
		return managerproof.ErrConflict
	}
	s.intents[intent.Nonce] = intent
	return nil
}

func parseCodexManagerEvent(raw []byte) (codexManagerEvent, bool, error) {
	var event codexManagerEvent
	object := func(raw json.RawMessage) map[string]json.RawMessage { v, _ := decodeStrictObject(raw); return v }
	str := func(raw json.RawMessage) string { v, _ := strictJSONString(raw); return v }
	frame := object(raw)
	p := object(frame["params"])
	if frame == nil || p == nil {
		return event, false, managerproof.ErrInvalid
	}
	event.method = str(frame["method"])
	event.thread = str(p["threadId"])
	event.turn = str(p["turnId"])
	switch event.method {
	case "thread/started":
		thread := object(p["thread"])
		event.thread = str(thread["id"])
		event.state = str(object(thread["status"])["type"])
	case "thread/status/changed":
		event.state = str(object(p["status"])["type"])
	case "turn/started", "turn/completed":
		turn := object(p["turn"])
		event.turn = str(turn["id"])
		event.parent = str(turn["rootTurnId"])
		event.state = str(turn["status"])
	case "item/started", "item/completed":
		item := object(p["item"])
		if item == nil {
			return event, false, managerproof.ErrInvalid
		}
		event.kind = str(item["type"])
		event.id = str(item["id"])
		switch event.kind {
		case "userMessage":
			event.client = str(item["clientId"])
			var content []json.RawMessage
			if json.Unmarshal(item["content"], &content) == nil && len(content) == 1 {
				part := object(content[0])
				text, ok := strictJSONString(part["text"])
				if str(part["type"]) == "text" && ok {
					event.textHash = codexManagerTextHash(text)
				}
			}
		case "mcpToolCall":
			if str(item["server"]) != "swarm_voice" {
				return event, false, nil
			}
			event.tool = str(item["tool"])
			var err error
			event.argumentsHash, err = managerproof.ArgumentsHash(item["arguments"])
			if err != nil {
				return event, false, err
			}
		default:
			return event, false, nil
		}
	default:
		return event, false, nil
	}
	// Ignore ordinary output before applying authority limits; retain only hashes.
	if jsontext.ValidateUnicode(raw) != nil {
		return event, false, managerproof.ErrInvalid
	}
	for _, id := range []string{event.thread, event.turn, event.parent, event.id, event.client, event.tool} {
		if len(id) > 256 {
			return event, false, managerproof.ErrInvalid
		}
	}
	return event, true, nil
}
func (s *codexManagerState) observe(raw []byte) error {
	event, relevant, err := parseCodexManagerEvent(raw)
	if err != nil {
		s.registry.Retire()
		return err
	}
	if !relevant {
		return nil
	}
	return s.observeEvent(event)
}
func (s *codexManagerState) observeEvent(e codexManagerEvent) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.thread != s.binding.NativeSessionID {
		return nil
	}
	defer func() {
		if err != nil {
			s.registry.Retire()
		}
	}()
	switch e.method {
	case "thread/started", "thread/status/changed":
		return s.registry.ObserveThreadState(e.state)
	case "turn/started":
		if e.parent != "" && e.parent != e.turn {
			return nil
		}
		return s.registry.StartTurn(e.turn, "")
	case "turn/completed":
		if e.parent != "" && e.parent != e.turn {
			return nil
		}
		reason, outcome := "answer", "completed"
		if e.state == "failed" {
			reason, outcome = "error", "failed"
		} else if e.state == "interrupted" {
			reason, outcome = "aborted", "cancelled"
		} else if e.state != "completed" {
			return managerproof.ErrInvalid
		}
		s.registry.CloseTurn(e.turn, reason)
		for nonce, old := range s.evidence {
			if old.Receipt.TurnID == e.turn && old.Receipt.Status == "admitted" {
				old.Outcome = outcome
				old.OutcomeSource = "live"
				old.OutcomeAt = time.Now().UnixMilli()
				s.evidence[nonce] = old
				s.unretained[nonce] = old
			}
		}
	case "item/started", "item/completed":
		switch e.kind {
		case "userMessage":
			if e.id == "" {
				return managerproof.ErrInvalid
			}
			if old, seen := s.messages[e.id]; seen {
				if old.turn != e.turn || old.client != e.client || old.textHash != e.textHash {
					return managerproof.ErrConflict
				}
				return nil
			}
			if len(s.messages) >= managerproof.MaxRecords {
				return managerproof.ErrCapacity
			}
			s.messages[e.id] = e
			intent, ok := s.intents[e.client]
			if !ok || intent.TextHash != e.textHash {
				s.registry.ExternalActivity()
				return nil
			}
			if err = s.registry.BindInput(e.client, e.turn, e.id); err != nil {
				return err
			}
			evidence := s.evidence[e.client]
			evidence.Version = 1
			evidence.Intent = intent
			evidence.Receipt.Status = "admitted"
			evidence.Receipt.TurnID = e.turn
			evidence.Receipt.MessageID = e.id
			s.evidence[e.client] = evidence
			s.unretained[e.client] = evidence
		case "mcpToolCall":
			if e.method == "item/completed" {
				return s.registry.CompleteTool(e.id, e.turn, "", e.tool, e.argumentsHash)
			}
			if err = s.registry.ToolStep(e.id, e.turn, "", e.tool); err != nil {
				return err
			}
			return s.registry.AttestTool(e.id, e.turn, "", e.tool, e.argumentsHash)
		}
	}
	return nil
}
func (s *codexManagerState) recordReceipt(nonce, turn string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.registry.RecordTurnReceipt(nonce, turn); err != nil {
		return err
	}
	old := s.evidence[nonce]
	if old.Receipt.TurnID != "" && old.Receipt.TurnID != turn {
		return managerproof.ErrConflict
	}
	if old.Receipt.Status == "" {
		old.Version = 1
		old.Intent = s.intents[nonce]
		old.Receipt.Status = "submitted"
		old.Receipt.TurnID = turn
		s.evidence[nonce] = old
		s.unretained[nonce] = old
	}
	return nil
}
func (s *codexManagerState) receipts() []codexManagerEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]codexManagerEvidence, 0, len(s.evidence))
	for _, e := range s.evidence {
		out = append(out, e)
	}
	return out
}
func (s *codexManagerState) pendingReceipts() []codexManagerEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]codexManagerEvidence, 0, len(s.unretained))
	for _, e := range s.unretained {
		out = append(out, e)
	}
	return out
}
func (s *codexManagerState) retained(e codexManagerEvidence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unretained[e.Intent.Nonce] == e {
		delete(s.unretained, e.Intent.Nonce)
	}
}

func (d *Daemon) codexManagerRegistry(local string, binding managerproof.Binding) (*managerproof.Registry, error) {
	entry, ok := d.sessionBackendFor(local)
	if !ok || entry.feed == nil || entry.feed.retired.Load() || entry.sessionInstance != binding.SessionInstance || entry.threadID != binding.NativeSessionID || binding.Session != local || binding.ObservationGeneration != entry.feed.epoch {
		return nil, managerproof.ErrStale
	}
	f := &entry.feed.manager
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dropped {
		return nil, managerproof.ErrUnavailable
	}
	if f.state == nil {
		s, err := newCodexManagerState(binding)
		if err != nil {
			return nil, err
		}
		for _, event := range f.pending {
			if err = s.observeEvent(event); err != nil {
				f.dropped = true
				return nil, err
			}
		}
		f.state = s
		f.pending = nil
	} else if f.state.binding != binding {
		f.state.registry.Retire()
		return nil, managerproof.ErrStale
	}
	if entry.feed.retired.Load() {
		f.state.registry.Retire()
		return nil, managerproof.ErrStale
	}
	return f.state.registry, nil
}
func (d *Daemon) captureCodexManagerFrame(local string, feed *backendFeed, method string, raw []byte) {
	switch method {
	case "thread/started", "thread/status/changed", "turn/started", "turn/completed", "item/started", "item/completed":
	default:
		return
	}
	event, relevant, err := parseCodexManagerEvent(raw)
	f := &feed.manager
	f.mu.Lock()
	if f.dropped {
		f.mu.Unlock()
		return
	}
	if err != nil {
		f.dropped = true
		if f.state != nil {
			f.state.registry.Retire()
		}
		f.pending = nil
		f.mu.Unlock()
		return
	}
	if !relevant {
		f.mu.Unlock()
		return
	}
	s := f.state
	if s == nil {
		if len(f.pending) >= 128 {
			f.dropped = true
			f.pending = nil
		} else {
			f.pending = append(f.pending, event)
		}
		f.mu.Unlock()
		return
	}
	err = s.observeEvent(event)
	if err != nil {
		f.dropped = true
	}
	f.mu.Unlock()
	if err == nil {
		d.retainCodexManagerState(s)
	}
}
func (d *Daemon) retainCodexManagerState(s *codexManagerState) {
	for _, e := range s.pendingReceipts() {
		if _, err := d.retainCodexManagerReceipt(e); err != nil {
			s.registry.Retire()
			return
		}
		s.retained(e)
	}
}
func discardCodexManagerFeed(feed *backendFeed) {
	if feed == nil {
		return
	}
	f := &feed.manager
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = true
	f.pending = nil
	if f.state != nil {
		f.state.registry.Retire()
	}
}
func (d *Daemon) retainCodexManagerReceipt(next codexManagerEvidence) (codexManagerEvidence, error) {
	for range 4 {
		rec, ok := d.core.ComposerOperation(next.Intent.OperationID)
		if !ok {
			return next, managerproof.ErrUnavailable
		}
		var request composerOriginIntent
		if json.Unmarshal(rec.Request, &request) != nil || request.Version != 1 || request.CodexManager == nil || *request.CodexManager != next.Intent || rec.SessionID != next.Intent.Binding.Session || rec.SessionInstance != next.Intent.Binding.SessionInstance {
			return next, managerproof.ErrConflict
		}
		if len(rec.Evidence) > 0 {
			var old codexManagerEvidence
			if json.Unmarshal(rec.Evidence, &old) != nil || old.Version != 1 || old.Intent != next.Intent || old.Receipt.TurnID != next.Receipt.TurnID {
				return next, managerproof.ErrConflict
			}
			if old.Receipt.Status == "admitted" && next.Receipt.Status != "admitted" || old.Outcome != "" {
				return old, nil
			}
		}
		raw, _ := json.Marshal(next)
		var err error
		if len(rec.Evidence) == 0 {
			err = d.core.ObserveSemanticReceipt(next.Intent.OperationID, raw)
		} else {
			err = d.core.CheckpointSemanticReceipt(next.Intent.OperationID, rec.Evidence, raw)
		}
		if err == nil {
			return next, nil
		}
	}
	return next, errors.New("codex native receipt changed during retention")
}

func (d *Daemon) sendCodexManager(local, operationID string, req protocol.ComposerSendReq, begin func() error, lane *composerLane, barrier uint64) (protocol.ErrorCode, error) {
	if req.ManagerAdmission == nil || req.ManagerAdmission.Validate() != nil {
		return protocol.CodeInvalidField, managerproof.ErrInvalid
	}
	current, err := d.checkedManagerBinding(local, req.ManagerAdmission.Binding)
	if err != nil {
		return protocol.CodeInputBusy, err
	}
	r, err := d.codexManagerRegistry(local, current.Binding)
	if err != nil {
		return protocol.CodeStructuredUnsupported, err
	}
	entry, ok := d.sessionBackendFor(local)
	if !ok || entry.feed == nil {
		return protocol.CodeStructuredUnsupported, managerproof.ErrUnavailable
	}
	bounded, ok := entry.conn.(backendWriteBoundary)
	if !ok {
		return protocol.CodeStructuredUnsupported, errors.New("codex manager requires the native atomic write boundary")
	}
	if !r.ReadyForInput() {
		return protocol.CodeInputBusy, errors.New("codex manager native thread is running or its idle state is unproven; nothing was sent")
	}
	if d.core.UnobservedSemanticExecution(local, operationID) {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: prior native input is unresolved; nothing was sent", protocol.ErrComposerOutcomeUnknown)
	}
	intent := codexManagerIntent{Binding: current.Binding, Nonce: req.ManagerAdmission.Nonce, TextHash: codexManagerTextHash(req.Text), OperationID: operationID, ExpectedExternalInput: req.ManagerAdmission.ExpectedExternalInput}
	raw, _ := json.Marshal(composerOriginIntent{Version: 1, Origin: req.Origin, CodexManager: &intent})
	if err = d.core.BindSemanticRequest(operationID, raw); err != nil {
		return protocol.CodeInvalidField, err
	}
	entry.feed.manager.mu.Lock()
	s := entry.feed.manager.state
	entry.feed.manager.mu.Unlock()
	if s == nil {
		return protocol.CodeStructuredUnsupported, managerproof.ErrUnavailable
	}
	if err = s.prepare(intent); err != nil {
		return protocol.CodeInputBusy, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	boundaryRejected := false
	// Acquire replacement before the app-server writer: replacement may join a
	// guard worker that is itself waiting for that writer.
	replacementHeld := d.contextGuards != nil
	if replacementHeld {
		d.contextGuards.replaceMu.Lock()
	}
	defer func() {
		if replacementHeld {
			d.contextGuards.replaceMu.Unlock()
		}
	}()
	err = bounded.CallAtWriteBoundary(ctx, "turn/start", map[string]any{"threadId": entry.threadID, "clientUserMessageId": intent.Nonce, "input": []map[string]any{{"type": "text", "text": req.Text}}}, &result, func() error {
		// Revalidate account and process authority before acquiring the short write fence.
		if _, e := d.checkedManagerBinding(local, current.Binding); e != nil {
			boundaryRejected = true
			return e
		}
		unlockFeed := func() {
			entry.feed.manager.mu.Unlock()
		}
		if instance, ok := d.sessionInstance(local); !ok || instance != current.Binding.SessionInstance || !d.backendRegistrationCurrent(local, entry) || entry.feed.epoch != intent.Binding.ObservationGeneration {
			boundaryRejected = true
			return managerproof.ErrStale
		}
		entry.feed.manager.mu.Lock()
		lane.mu.Lock()
		if lane.barrier != barrier || lane.retired || lane.ownerEnding || lane.recycling || entry.feed.retired.Load() || !r.ReadyForInput() {
			lane.mu.Unlock()
			unlockFeed()
			boundaryRejected = true
			return errComposerStopped
		}
		if e := r.CheckExternalInput(intent.ExpectedExternalInput); e != nil {
			lane.mu.Unlock()
			unlockFeed()
			boundaryRejected = true
			return e
		}
		if e := beginComposerDelivery(begin); e != nil {
			lane.mu.Unlock()
			unlockFeed()
			boundaryRejected = true
			return e
		}
		return nil
	}, func() {
		lane.mu.Unlock()
		entry.feed.manager.mu.Unlock()
		if d.contextGuards != nil {
			d.contextGuards.replaceMu.Unlock()
			replacementHeld = false
		}
	})
	if err != nil {
		if boundaryRejected {
			if errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
				return protocol.CodeOutcomeUnknown, err
			}
			return protocol.CodeInputBusy, err
		}
		err = classifyComposerBackendError(err)
		if errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
			return protocol.CodeOutcomeUnknown, err
		}
		return protocol.CodeInputBusy, err
	}
	if result.Turn.ID == "" {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: native turn acknowledgement missing; input will not be replayed", protocol.ErrComposerOutcomeUnknown)
	}
	if err = s.recordReceipt(intent.Nonce, result.Turn.ID); err != nil {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: conflicting native turn receipt", protocol.ErrComposerOutcomeUnknown)
	}
	for _, e := range s.pendingReceipts() {
		if e.Intent.OperationID == operationID {
			if _, err = d.retainCodexManagerReceipt(e); err != nil {
				return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: native receipt could not be retained", protocol.ErrComposerOutcomeUnknown)
			}
			s.retained(e)
		}
	}
	return "", nil
}

func (d *Daemon) codexManagerReceipt(local, operationID string, rec idempotency.Record, result protocol.SemanticMessageReceipt) protocol.SemanticMessageReceipt {
	var origin composerOriginIntent
	if json.Unmarshal(rec.Request, &origin) != nil || origin.CodexManager == nil {
		return result
	}
	i := *origin.CodexManager
	if i.OperationID != operationID || i.Binding.Session != local || i.Binding.SessionInstance != rec.SessionInstance || i.Binding.Validate() != nil || !managerproof.ValidHash(i.Nonce) || !managerproof.ValidHash(i.TextHash) {
		result.State = "uncertain"
		return result
	}
	result.NativeSessionID = i.Binding.NativeSessionID
	result.NativeGeneration = i.Binding.NativeGeneration
	var e codexManagerEvidence
	if json.Unmarshal(rec.Evidence, &e) != nil || e.Version != 1 || e.Intent != i || e.Receipt.TurnID == "" {
		if rec.Phase != idempotency.PhasePrepared && rec.Phase != idempotency.PhaseFailed {
			result.State = "uncertain"
		}
		return result
	}
	result.NativeInputID = i.Nonce
	result.NativeTurnID = e.Receipt.TurnID
	switch e.Receipt.Status {
	case "admitted":
		if e.Receipt.MessageID == "" {
			result.State = "uncertain"
			return result
		}
		result.State = "admitted"
		result.NativeMessageID = e.Receipt.MessageID
	case "submitted":
		result.State = "submitted"
	default:
		result.State = "uncertain"
		return result
	}
	if rec.Phase == idempotency.PhaseExecuting || rec.Phase == idempotency.PhaseOutcomeUnknown {
		if err := d.core.CommitComposerOperation(operationID, []byte(`{"ok":true}`), true); err != nil {
			result.State = "uncertain"
			result.Reason = "native receipt is known; durable delivery settlement is unconfirmed"
			return result
		}
	}
	if e.Outcome != "" {
		if e.Receipt.Status != "admitted" || (e.Outcome != "completed" && e.Outcome != "failed" && e.Outcome != "cancelled") || e.OutcomeSource != "live" || e.OutcomeAt <= 0 {
			result.State = "uncertain"
			return result
		}
		result.State = e.Outcome
		result.OutcomeSource = e.OutcomeSource
	}
	return result
}
