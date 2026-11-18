package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	mu                  sync.Mutex
	setupMu             sync.Mutex
	actor               atomic.Pointer[managerproof.Custody]
	published           atomic.Pointer[codexManagerState]
	lost                atomic.Bool
	observedBeforeClaim atomic.Bool
	replay              *managerproof.Replay
	cancel              context.CancelFunc
	done                chan struct{}
	state               *codexManagerState
	pending             []codexManagerEvent
	pendingBytes        int
}
type codexManagerState struct {
	replay     *managerproof.Replay
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
func newCodexManagerState(binding managerproof.Binding, persistent ...*managerproof.Replay) (*codexManagerState, error) {
	if binding.Provider != "codex" {
		return nil, managerproof.ErrInvalid
	}
	r, err := managerproof.NewRegistry(binding)
	var replay *managerproof.Replay
	if len(persistent) == 1 {
		replay = persistent[0]
		r, err = managerproof.NewPersistentRegistry(binding, replay)
	}
	if err != nil {
		return nil, err
	}
	return &codexManagerState{binding: binding, registry: r, replay: replay, intents: map[string]codexManagerIntent{}, evidence: map[string]codexManagerEvidence{}, unretained: map[string]codexManagerEvidence{}, messages: map[string]codexManagerEvent{}}, nil
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
	for _, id := range []string{event.thread, event.turn, event.parent, event.id, event.client, event.tool, event.state} {
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
		if err := s.registry.CustodyError(); err != nil {
			return err
		}
		if s.replay != nil {
			for id, message := range s.messages {
				if message.turn == e.turn {
					if _, owned := s.intents[message.client]; !owned {
						if err := s.replay.CloseKeys(s.binding, managerproof.ReplayKey{Kind: managerproof.ReplayMessage, ID: id, Owner: managerproof.ReplayFingerprint(message.turn, message.client)}); err != nil {
							return err
						}
						delete(s.messages, id)
					}
				}
			}
		}
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
				if s.replay != nil {
					if err := s.replay.Reserve(s.binding, managerproof.ReplayKey{Kind: managerproof.ReplayMessage, ID: e.id, Owner: managerproof.ReplayFingerprint(e.turn, e.client)}); err != nil {
						return err
					}
				}
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
		if s.replay != nil && !s.registry.InputDetailRetained(nonce) {
			receipt, readErr := s.replay.Terminal(s.binding, nonce)
			if readErr == nil && receipt.NativeTurnID == turn {
				return nil
			}
		}
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

func (s *codexManagerState) releaseTerminalDetail() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for nonce, e := range s.evidence {
		if e.Outcome == "" || s.unretained[nonce].Version != 0 || s.registry.InputDetailRetained(nonce) {
			continue
		}
		delete(s.intents, nonce)
		delete(s.evidence, nonce)
		for id, event := range s.messages {
			if event.turn == e.Receipt.TurnID {
				delete(s.messages, id)
			}
		}
	}
	return s.registry.CustodyError()
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

// Claim precedes the native reader and initial backend_attach. A replacement
// observer may read generic native status but never renew this dirty manager owner.
func (d *Daemon) prepareCodexManagerFeed(local, instance string, feed *backendFeed) error {
	f := &feed.manager
	f.setupMu.Lock()
	defer f.setupMu.Unlock()
	if f.lost.Load() {
		return managerproof.ErrQuarantined
	}
	if c := f.actor.Load(); c != nil {
		return c.Err()
	}
	if d.core == nil {
		return nil
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.Role != "manager" {
		return nil
	}
	if meta.ManagerReplayID == "" || meta.ManagerConversationID == "" {
		f.lost.Store(true)
		d.backend.managerFailures.Store(local, struct{}{})
		return managerproof.ErrQuarantined
	}
	if f.observedBeforeClaim.Load() {
		f.lost.Store(true)
		d.backend.managerFailures.Store(local, struct{}{})
		return managerproof.ErrQuarantined
	}
	current, ok := d.sessionInstance(local)
	if !ok || current != instance {
		return managerproof.ErrStale
	}
	parent, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	c := managerproof.NewCustody(parent, func(error) {
		f.lost.Store(true)
		d.backend.managerFailures.Store(local, struct{}{})
		if s := f.published.Load(); s != nil {
			s.registry.Retire()
		}
		cancel()
	}, func() {
		if s := f.published.Load(); s != nil {
			s.registry.NotifyCustodyProgress()
		}
	})
	f.actor.Store(c)
	ctx, stop := context.WithTimeout(context.Background(), backendCallTimeout)
	defer stop()
	err := c.Do(ctx, 256, func(context.Context) error {
		replay, err := managerproof.OpenReplay(filepath.Join(d.stateDir, local), local, meta.ManagerConversationID, meta.ManagerReplayID)
		if err != nil {
			return err
		}
		f.replay = replay
		return replay.Claim(feed.epoch)
	})
	if err != nil {
		f.lost.Store(true)
		d.backend.managerFailures.Store(local, struct{}{})
		cancel()
		c.Close()
		if f.replay != nil {
			_ = f.replay.Close()
		}
		close(f.done)
		return managerproof.ErrQuarantined
	}
	go func() {
		select {
		case <-d.closing:
		case <-parent.Done():
		}
		discardCodexManagerFeed(feed)
	}()
	return nil
}

func codexManagerEventBytes(e codexManagerEvent) int {
	return 384 + len(e.method) + len(e.thread) + len(e.turn) + len(e.parent) + len(e.state) + len(e.kind) + len(e.id) + len(e.client) + len(e.textHash) + len(e.tool) + len(e.argumentsHash)
}

func (d *Daemon) codexManagerCustodyError(local string) error {
	if _, failed := d.backend.managerFailures.Load(local); failed {
		return managerproof.ErrQuarantined
	}
	return nil
}

func (d *Daemon) applyCodexManagerEvent(feed *backendFeed, event codexManagerEvent) error {
	f := &feed.manager
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lost.Load() || feed.retired.Load() {
		return managerproof.ErrQuarantined
	}
	s := f.state
	if s == nil {
		if len(f.pending) >= 128 || f.pendingBytes+codexManagerEventBytes(event) > 256<<10 {
			return managerproof.ErrCapacity
		}
		f.pending = append(f.pending, event)
		f.pendingBytes += codexManagerEventBytes(event)
		return nil
	}
	if err := s.observeEvent(event); err != nil {
		return err
	}
	if err := s.registry.CustodyError(); err != nil {
		return err
	}
	return d.retainCodexManagerState(s)
}

func (d *Daemon) codexManagerRegistry(local string, binding managerproof.Binding) (*managerproof.Registry, error) {
	entry, ok := d.sessionBackendFor(local)
	if !ok || entry.feed == nil || entry.feed.retired.Load() || entry.sessionInstance != binding.SessionInstance || entry.threadID != binding.NativeSessionID || binding.Session != local || binding.ObservationGeneration != entry.feed.epoch {
		return nil, managerproof.ErrStale
	}
	f := &entry.feed.manager
	if f.lost.Load() {
		return nil, managerproof.ErrQuarantined
	}
	c := f.actor.Load()
	if c == nil {
		if f.observedBeforeClaim.Load() || d.codexManagerCustodyError(local) != nil {
			return nil, managerproof.ErrQuarantined
		}
		return nil, managerproof.ErrUnavailable
	}
	if c.Err() != nil {
		return nil, managerproof.ErrQuarantined
	}
	if s := f.published.Load(); s != nil {
		if s.binding != binding {
			return nil, managerproof.ErrQuarantined
		}
		if err := s.registry.CustodyError(); err != nil {
			return nil, err
		}
		return s.registry, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	err := c.Do(ctx, 256, func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.lost.Load() || entry.feed.retired.Load() {
			return managerproof.ErrQuarantined
		}
		if f.state != nil {
			if f.state.binding != binding {
				return managerproof.ErrQuarantined
			}
			return nil
		}
		s, err := newCodexManagerState(binding, f.replay)
		if err != nil {
			return err
		}
		s.registry.SetProofPending(func() bool { return f.lost.Load() || c.Pending() || c.Err() != nil })
		for _, event := range f.pending {
			if err = s.observeEvent(event); err != nil {
				return err
			}
		}
		if err = s.registry.CustodyError(); err != nil {
			return err
		}
		f.state = s
		f.pending = nil
		f.pendingBytes = 0
		f.published.Store(s)
		return nil
	})
	if err != nil {
		f.lost.Store(true)
		d.backend.managerFailures.Store(local, struct{}{})
		return nil, managerproof.ErrQuarantined
	}
	return f.published.Load().registry, nil
}

// The native reader only parses bounded evidence and enqueues. It never takes
// the feed/write fence, waits for a receipt, or touches durable custody.
func (d *Daemon) captureCodexManagerFrame(local string, feed *backendFeed, method string, raw []byte) {
	switch method {
	case "thread/started", "thread/status/changed", "turn/started", "turn/completed", "item/started", "item/completed":
	default:
		return
	}
	f := &feed.manager
	if f.lost.Load() || feed.retired.Load() {
		return
	}
	event, relevant, err := parseCodexManagerEvent(raw)
	if err != nil {
		f.lost.Store(true)
		if f.actor.Load() != nil {
			d.backend.managerFailures.Store(local, struct{}{})
		}
		if s := f.published.Load(); s != nil {
			s.registry.Retire()
		}
		if f.actor.Load() != nil && f.cancel != nil {
			f.cancel()
		}
		return
	}
	if !relevant {
		return
	}
	c := f.actor.Load()
	if c == nil {
		f.observedBeforeClaim.Store(true)
		return
	}
	if c.Enqueue(codexManagerEventBytes(event), func(context.Context) error { return d.applyCodexManagerEvent(feed, event) }) != nil {
		f.lost.Store(true)
	}
}

// Terminal lookup is private history only. It never claims an observer or
// consults current positive readiness/account authority.
func (d *Daemon) codexManagerTerminal(ctx context.Context, local string, binding managerproof.Binding, nonce string) (managerproof.TerminalReceipt, error) {
	var empty managerproof.TerminalReceipt
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if binding.Session != local {
		if d.api == nil || binding.Session != protocol.NamespacedID(d.api.endpointID, local) {
			return empty, managerproof.ErrStale
		}
		binding.Session = local
	}
	if binding.Validate() != nil || binding.Provider != "codex" || !managerproof.ValidHash(nonce) || d.core == nil {
		return empty, managerproof.ErrInvalid
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.Role != "manager" || meta.AgentType != "codex" || meta.ManagerReplayID == "" || meta.ManagerConversationID != binding.ConversationID {
		return empty, managerproof.ErrUnavailable
	}
	if entry, ok := d.sessionBackendFor(local); ok && entry.feed != nil {
		f := &entry.feed.manager
		f.setupMu.Lock()
		replay := f.replay
		f.setupMu.Unlock()
		if replay != nil {
			receipt, err := replay.Terminal(binding, nonce)
			if ctx.Err() != nil {
				return empty, ctx.Err()
			}
			// An open observer already owns the ledger lock. Retrying misses
			// through another open would only wait on that same lock.
			return receipt, err
		}
	}
	replay, err := managerproof.OpenReplay(filepath.Join(d.stateDir, local), local, meta.ManagerConversationID, meta.ManagerReplayID)
	if err != nil {
		return empty, err
	}
	defer func() { _ = replay.Close() }()
	receipt, err := replay.Terminal(binding, nonce)
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	return receipt, err
}
func (d *Daemon) retainCodexManagerState(s *codexManagerState) error {
	for _, e := range s.pendingReceipts() {
		if _, err := d.retainCodexManagerReceipt(e); err != nil {
			s.registry.Retire()
			return err
		}
		s.retained(e)
	}
	return s.releaseTerminalDetail()
}
func discardCodexManagerFeed(feed *backendFeed) {
	if feed == nil {
		return
	}
	f := &feed.manager
	f.lost.Store(true)
	f.setupMu.Lock()
	cancel := f.cancel
	c := f.actor.Load()
	done := f.done
	f.setupMu.Unlock()
	if c == nil {
		return
	}
	if cancel != nil {
		cancel()
	}
	c.Close()
	f.setupMu.Lock()
	if f.replay != nil {
		_ = f.replay.Close()
		f.replay = nil
	}
	if done != nil {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	f.setupMu.Unlock()
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
		return codexManagerProofCode(err, protocol.CodeInputBusy), err
	}
	r, err := d.codexManagerRegistry(local, current.Binding)
	if err != nil {
		return codexManagerProofCode(err, protocol.CodeStructuredUnsupported), err
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
	f := &entry.feed.manager
	c := f.actor.Load()
	if f.lost.Load() || c == nil || c.Err() != nil {
		return protocol.CodeManagerCustodyLost, managerproof.ErrQuarantined
	}
	s := f.published.Load()
	if s == nil {
		return protocol.CodeStructuredUnsupported, managerproof.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	raw, _ := json.Marshal(composerOriginIntent{Version: 1, Origin: req.Origin, CodexManager: &intent})
	if err = c.Do(ctx, len(raw), func(context.Context) error {
		if err := d.core.BindSemanticRequest(operationID, raw); err != nil {
			return err
		}
		return s.prepare(intent)
	}); err != nil {
		if s.registry.CustodyError() != nil {
			f.lost.Store(true)
			d.backend.managerFailures.Store(local, struct{}{})
			if f.cancel != nil {
				f.cancel()
			}
			err = managerproof.ErrQuarantined
		}
		return codexManagerProofCode(err, protocol.CodeInputBusy), err
	}
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
		if lane.barrier != barrier || lane.retired || lane.ownerEnding || lane.recycling || entry.feed.retired.Load() || f.lost.Load() || c.Err() != nil || !r.ReadyForInput() {
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
		if c.Pending() || c.Err() != nil || f.lost.Load() {
			lane.mu.Unlock()
			unlockFeed()
			boundaryRejected = true
			return fmt.Errorf("%w: native custody changed before the request write", protocol.ErrComposerOutcomeUnknown)
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
			return codexManagerProofCode(err, protocol.CodeInputBusy), err
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
	if err = c.Do(ctx, 256, func(context.Context) error {
		if err := s.recordReceipt(intent.Nonce, result.Turn.ID); err != nil {
			return err
		}
		return d.retainCodexManagerState(s)
	}); err != nil {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: conflicting native turn receipt", protocol.ErrComposerOutcomeUnknown)
	}
	return "", nil
}

func codexManagerProofCode(err error, fallback protocol.ErrorCode) protocol.ErrorCode {
	if errors.Is(err, managerproof.ErrQuarantined) {
		return protocol.CodeManagerCustodyLost
	}
	return fallback
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
