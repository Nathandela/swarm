package skeleton

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
)

var errClaudeFixturePending = errors.New("fixture primary read publication pending")

var errClaudeFixtureMessage = errors.New("fixture chat input custody is unavailable")

type claudeFixtureIntent = daemon.ClaudeFixtureIntent

type claudeFixtureEvidence struct {
	Version           int                 `json:"v"`
	Kind              string              `json:"kind"`
	OperationID       string              `json:"operation_id"`
	Intent            claudeFixtureIntent `json:"intent"`
	State             string              `json:"state"`
	EchoSequence      uint64              `json:"echo_sequence,omitempty"`
	AssistantSequence uint64              `json:"assistant_sequence,omitempty"`
	ResultSequence    uint64              `json:"result_sequence,omitempty"`
	AssistantUUID     string              `json:"assistant_uuid,omitempty"`
	RawHashes         []string            `json:"raw_hashes,omitempty"`
}

type claudeFixtureState struct {
	mu                              sync.Mutex
	evidenceMu                      sync.Mutex
	meta                            persist.Meta
	identity                        shimwire.ClaudePrimaryIdentity
	healthy, initialized            bool
	ready, inputFault               bool
	sequence, folded, ownerRevision uint64
	operation                       string
	userText, initHash              string
	intent                          claudeFixtureIntent
	evidence                        claudeFixtureEvidence
	drainer                         *HookDrainer
}

func (d *Daemon) claudeFixtureState(local string) *claudeFixtureState {
	lane, ok := d.composerLanes.Load(local)
	if !ok {
		return nil
	}
	l := lane.(*composerLane)
	l.mu.Lock()
	s := l.claudeFixture
	l.mu.Unlock()
	return s
}

func (s *claudeFixtureState) fail() { s.mu.Lock(); s.healthy = false; s.ready = false; s.mu.Unlock() }

func (s *claudeFixtureState) snapshot() (shimwire.ClaudePrimaryIdentity, bool, uint64, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identity, s.healthy && s.initialized, s.folded, s.ownerRevision
}

func (d *Daemon) registerClaudeFixture(m persist.Meta) {
	lane := d.composerLaneFor(m.ID)
	lane.mu.Lock()
	if lane.claudeFixture == nil {
		lane.claudeFixture = &claudeFixtureState{meta: m, healthy: true}
	}
	lane.mu.Unlock()
	d.registerExecutionSession(m, "", nil, true)
	d.authorSessionCapabilitiesWhenDecided(m.ID, m.AgentType, m.ShimPID, m.ShimStartTime)
}

func freshClaudeFixture(m persist.Meta) bool {
	return m.SchemaVersion == 6 && m.AgentType == "fake" && m.RequiredRuntime == persist.RequiredRuntimeClaudeChat && m.LoginRuntime == nil && m.CLIIdentity == nil && m.ClaudeRetirement == nil && m.ShimPID > 0 && m.ShimStartTime > 0 && adapter.IsCanonicalConversationID(m.ConversationID)
}

func newClaudeFixtureUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (d *Daemon) sendClaudeFixture(local, operation string, req protocol.ComposerSendReq, begin func() error, source string) (protocol.ErrorCode, error) {
	if req.Origin != nil {
		return protocol.CodeStructuredUnsupported, errClaudeFixtureMessage
	}
	s := d.claudeFixtureState(local)
	if s == nil || source != adapter.SourceOwner || operation == "" || req.Text == "" || !utf8.ValidString(req.Text) || len(req.Text) > shimwire.MaxClaudeFixtureTextBytes {
		return protocol.CodeStructuredUnsupported, errClaudeFixtureMessage
	}
	d.startHookDrain(local)
	s.mu.Lock()
	drainer := s.drainer
	s.mu.Unlock()
	if drainer == nil {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	lane := d.composerLaneFor(local)
	lane.mu.Lock()
	lane.pendingClaudeAdmission++
	lane.mu.Unlock()
	lane.claudeAdmitMu.Lock()
	pending := true
	dropPending := func() {
		if pending {
			lane.mu.Lock()
			lane.pendingClaudeAdmission--
			lane.mu.Unlock()
			pending = false
		}
	}
	defer func() { dropPending(); lane.claudeAdmitMu.Unlock() }()
	instance, ok := d.sessionInstance(local)
	if !ok || instance == "" || req.SessionInstance != instance {
		return protocol.CodeStaleInstance, errClaudeFixtureMessage
	}
	snapshot, err := d.core.ClaudeFixtureSnapshot(local)
	if err != nil || !snapshot.Healthy || snapshot.PendingPartial || snapshot.StdoutEOF || snapshot.StderrEOF {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	if _, _, err = drainer.DrainOnce(); err != nil {
		s.fail()
		return protocol.CodeInputBusy, err
	}
	if e := d.proveClaudeFixtureReady(local, s); e != nil {
		return protocol.CodeInputBusy, e
	}
	if _, e := d.readClaudeFixtureHistory(local, s); e != nil {
		s.fail()
		return protocol.CodeInputBusy, e
	}
	identity, healthy, folded, mirror := s.snapshot()
	if !healthy || identity != snapshot.Identity || folded != snapshot.ObservedSequence || snapshot.DurableSequence != folded || mirror != snapshot.OwnerRevision {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	hash := sha256.Sum256([]byte(req.Text))
	rec, exists := d.core.ComposerOperation(operation)
	if !exists {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	execution, executionErr := d.eng.ExecutionSnapshot(local)
	if executionErr != nil || execution.Validate() != nil || execution.Incarnation != executionIncarnation(s.meta) || execution.ConversationID != identity.NativeSessionID || execution.Incomplete != "dev_fake_chat_subset" || !execution.UnknownActors {
		s.fail()
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	var intent claudeFixtureIntent
	if len(rec.Request) > 0 {
		if parsed, e := daemon.DecodeClaudeFixtureIntent(rec.Request); e != nil {
			return protocol.CodeInputBusy, errClaudeFixtureMessage
		} else {
			intent = parsed
		}
		if intent.Kind != "claude_fixture_message" || intent.Identity != identity || intent.SessionInstance != instance || intent.TextSHA256 != hex.EncodeToString(hash[:]) || intent.TextBytes != len(req.Text) || execution.OwnerRevision != intent.OwnerRevision || intent.ExpectedOwnerRevision != mirror {
			return protocol.CodeInputBusy, errClaudeFixtureMessage
		}
	} else {
		s.mu.Lock()
		used := s.operation != ""
		s.mu.Unlock()
		if used {
			return protocol.CodeInputBusy, errClaudeFixtureMessage
		}
		if execution.OwnerRevision == ^uint64(0) || execution.OwnerRevision != mirror {
			return protocol.CodeInputBusy, errClaudeFixtureMessage
		}
		uuid, e := newClaudeFixtureUUID()
		if e != nil {
			return protocol.CodeInputBusy, e
		}
		intent = claudeFixtureIntent{Version: 1, Kind: "claude_fixture_message", Identity: identity, SessionInstance: instance, NativeUserUUID: uuid, TextSHA256: hex.EncodeToString(hash[:]), TextBytes: len(req.Text), ExpectedOwnerRevision: mirror, OwnerRevision: mirror + 1}
		if e = d.eng.ObserveExecutionOwnerRevision(local, mirror, mirror+1); e != nil {
			s.fail()
			return protocol.CodeInputBusy, e
		}
		bound, _ := json.Marshal(intent)
		if e = d.core.BindSemanticRequest(operation, bound); e != nil {
			s.fail()
			return protocol.CodeInputBusy, e
		}
	}
	s.mu.Lock()
	s.operation, s.intent, s.ownerRevision = operation, intent, intent.OwnerRevision
	s.evidence = claudeFixtureEvidence{Version: 1, Kind: "claude_fixture_message", OperationID: operation, Intent: intent, State: "prepared", RawHashes: []string{s.initHash}}
	s.mu.Unlock()
	dropPending()
	// Persisted admission precedes the existing FIFO; Stop never takes this mutex.
	lane.claudeAdmitMu.Unlock()
	ticket := lane.enter()
	defer lane.leave()
	lane.claudeAdmitMu.Lock()
	if lane.barrierChanged(ticket) {
		return protocol.CodeStaleTurn, errClaudeFixtureMessage
	}
	if err = beginComposerDelivery(begin); err != nil {
		s.fail()
		return protocol.CodeInputBusy, err
	}
	if hook := testHookComposerBegunNotYetSubmitted.Load(); hook != nil {
		(*hook)(local)
	}
	bound, _ := json.Marshal(intent)
	write := shimwire.ClaudeMessageRequest{Version: 1, OperationID: operation, NativeUserUUID: intent.NativeUserUUID, Text: req.Text, Identity: identity, ExpectedOwnerRevision: intent.ExpectedOwnerRevision, OwnerRevision: intent.OwnerRevision, ThroughStreamSequence: folded, ReaderRevision: snapshot.ReaderRevision}
	prepared, err := d.core.PrepareClaudeFixtureDispatch(local, write, bound)
	if err != nil {
		return protocol.CodeInputBusy, err
	}
	defer prepared.Close()
	var result shimwire.ClaudeMessageResult
	err = d.srv.TryWithInputFence(local, func() error {
		if !lane.endMu.TryLock() {
			return errClaudeFixtureMessage
		}
		defer lane.endMu.Unlock()
		lane.mu.Lock()
		invalid := lane.ownerEnding || lane.retired || lane.recycling || lane.pendingClaudeAdmission != 0
		lane.mu.Unlock()
		_, ready, _, current := s.snapshot()
		if invalid || !ready || current != intent.OwnerRevision {
			return errClaudeFixtureMessage
		}
		var e error
		result, e = prepared.Write()
		return e
	})
	prepared.Close()
	if err != nil || result.Status != "written" {
		if result.Status == "uncertain" {
			if e := d.checkpointClaudeFixture(s, "uncertain", 0, "", ""); e != nil {
				s.fail()
			}
			s.mu.Lock()
			s.inputFault = true
			s.mu.Unlock()
			return protocol.CodeOutcomeUnknown, protocol.ErrComposerOutcomeUnknown
		}
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	if err = d.checkpointClaudeFixture(s, "submitted", 0, "", ""); err != nil {
		s.fail()
		return protocol.CodeOutcomeUnknown, protocol.ErrComposerOutcomeUnknown
	}
	return "", nil
}

func (d *Daemon) checkpointClaudeFixture(s *claudeFixtureState, state string, sequence uint64, assistant, rawHash string) error {
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	s.mu.Lock()
	e := s.evidence
	s.mu.Unlock()
	rank := map[string]int{"prepared": 0, "submitted": 1, "admitted": 2, "completed": 3, "uncertain": 1}
	if state == "uncertain" {
		if e.State != "admitted" && e.State != "completed" {
			e.State = state
		}
	} else if rank[state] > rank[e.State] {
		e.State = state
	}
	if rawHash != "" {
		e.RawHashes = append(append([]string(nil), e.RawHashes...), rawHash)
	}
	switch state {
	case "admitted":
		if assistant == "" {
			e.EchoSequence = sequence
		}
	case "completed":
		e.ResultSequence = sequence
	}
	if assistant != "" {
		e.AssistantUUID, e.AssistantSequence = assistant, sequence
	}
	old, ok := d.core.ComposerOperation(e.OperationID)
	if !ok {
		return errClaudeFixtureMessage
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(old.Evidence) == 0 {
		err = d.core.ObserveSemanticReceipt(e.OperationID, raw)
	} else {
		err = d.core.CheckpointSemanticReceipt(e.OperationID, old.Evidence, raw)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.evidence = e
	s.mu.Unlock()
	return nil
}

type claudeFixtureFrame struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype,omitempty"`
	UUID       string          `json:"uuid,omitempty"`
	SessionID  string          `json:"session_id"`
	ParentTool json.RawMessage `json:"parent_tool_use_id,omitempty"`
	Message    struct {
		ID      string `json:"id,omitempty"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message,omitempty"`
	IsError     bool   `json:"is_error,omitempty"`
	Result      string `json:"result,omitempty"`
	FixturePID  int    `json:"fixture_pid,omitempty"`
	FixturePGID int    `json:"fixture_pgid,omitempty"`
	Stdio       *struct {
		Stdin  bool `json:"stdin_tty"`
		Stdout bool `json:"stdout_tty"`
		Stderr bool `json:"stderr_tty"`
	} `json:"fixture_stdio,omitempty"`
}

func fixtureText(f claudeFixtureFrame) (string, bool) {
	returnText := ""
	if len(f.Message.Content) == 1 && f.Message.Content[0].Type == "text" {
		returnText = f.Message.Content[0].Text
		return returnText, true
	}
	return "", false
}

func (d *Daemon) consumeClaudeFixture(s *claudeFixtureState, body []byte) error {
	object, ok := fixtureObject(body, "v", "origin", "native_session_id", "primary", "read_monotonic_ns", "stream_sequence", "raw")
	if !ok {
		return errClaudeFixtureMessage
	}
	if _, ok = fixtureObject(object["primary"], "session_id", "required_runtime", "native_session_id", "shim_pid", "shim_start_time", "primary_pid", "primary_start_time"); !ok {
		return errClaudeFixtureMessage
	}

	var env struct {
		Version  int                            `json:"v"`
		Origin   string                         `json:"origin"`
		Native   string                         `json:"native_session_id"`
		Identity shimwire.ClaudePrimaryIdentity `json:"primary"`
		ReadNS   int64                          `json:"read_monotonic_ns"`
		Sequence uint64                         `json:"stream_sequence"`
		Raw      []byte                         `json:"raw"`
	}
	if json.Unmarshal(body, &env) != nil || env.Version != 1 || env.Origin != "claude_primary" || env.ReadNS <= 0 || env.Identity.SessionID != s.meta.ID || env.Identity.NativeSessionID != s.meta.ConversationID || env.Native != s.meta.ConversationID || env.Identity.RequiredRuntime != persist.RequiredRuntimeClaudeChat || env.Identity.ShimPID != s.meta.ShimPID || env.Identity.ShimStartTime != s.meta.ShimStartTime || env.Identity.PrimaryPID <= 0 || env.Identity.PrimaryStartTime <= 0 {
		return errClaudeFixtureMessage
	}
	frame, decodeErr := decodeClaudeFixtureFrame(env.Raw)
	if decodeErr != nil || frame.SessionID != s.meta.ConversationID {
		return errClaudeFixtureMessage
	}
	s.mu.Lock()
	valid := s.healthy && env.Sequence == s.sequence+1 && env.Sequence <= 4 && (s.sequence == 0 || s.identity == env.Identity)
	intent, evidence, initialized, userText, operation := s.intent, s.evidence, s.initialized, s.userText, s.operation
	s.mu.Unlock()
	if !valid {
		return errClaudeFixtureMessage
	}
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	text, hasText := fixtureText(frame)
	nextState := ""
	if frame.Type != "system" {
		if intent.OwnerRevision == 0 {
			return errClaudeFixtureMessage
		}
		current, e := d.core.ClaudeFixtureSnapshot(s.meta.ID)
		if e != nil {
			return errClaudeFixturePending
		}
		if current.Identity != env.Identity || !current.Healthy || current.StdoutEOF || current.StderrEOF || current.OwnerRevision != intent.OwnerRevision {
			return errClaudeFixtureMessage
		}
		if current.PendingPartial || current.ObservedSequence < env.Sequence || current.DurableSequence < env.Sequence {
			return errClaudeFixturePending
		}
	}
	switch frame.Type {
	case "system":
		if initialized || env.Sequence != 1 || frame.Subtype != "init" || frame.FixturePID != env.Identity.PrimaryPID || frame.FixturePGID != env.Identity.PrimaryPID || frame.Stdio == nil || frame.Stdio.Stdin || frame.Stdio.Stdout || frame.Stdio.Stderr {
			return errClaudeFixtureMessage
		}
		current, e := d.core.ClaudeFixtureSnapshot(s.meta.ID)
		if e != nil {
			return errClaudeFixturePending
		}
		if current.Identity != env.Identity || !current.Healthy || current.PendingPartial || current.StdoutEOF || current.StderrEOF || current.ObservedSequence != env.Sequence || current.DurableSequence != env.Sequence || current.OwnerRevision != 0 {
			return errClaudeFixtureMessage
		}
		text = "Fixture primary initialized"
	case "user":
		h := sha256.Sum256([]byte(text))
		if !initialized || evidence.State != "prepared" && evidence.State != "submitted" && evidence.State != "uncertain" || env.Sequence != 2 || frame.UUID != intent.NativeUserUUID || frame.Message.Role != "user" || !hasText || len(text) != intent.TextBytes || hex.EncodeToString(h[:]) != intent.TextSHA256 || !bytes.Equal(frame.ParentTool, []byte("null")) {
			return errClaudeFixtureMessage
		}
		op, exists := d.core.ComposerOperation(operation)
		bound, e := daemon.DecodeClaudeFixtureIntent(op.Request)
		if !exists || e != nil || bound != intent || (op.Phase != idempotency.PhaseExecuting && op.Phase != idempotency.PhaseCompleted && op.Phase != idempotency.PhaseOutcomeUnknown) {
			return errClaudeFixtureMessage
		}
		nextState = "admitted"
	case "assistant":
		if evidence.EchoSequence != 2 || env.Sequence != 3 || !adapter.IsCanonicalConversationID(frame.UUID) || frame.Message.ID != frame.UUID || frame.Message.Role != "assistant" || !hasText || text != "fixture reply: "+userText || frame.UUID == intent.NativeUserUUID || !bytes.Equal(frame.ParentTool, []byte("null")) {
			return errClaudeFixtureMessage
		}
		nextState = "admitted"
	case "result":
		if evidence.EchoSequence != 2 || evidence.AssistantSequence != 3 || env.Sequence != 4 || frame.Subtype != "success" || frame.IsError || !adapter.IsCanonicalConversationID(frame.UUID) || frame.UUID == evidence.AssistantUUID || frame.UUID == intent.NativeUserUUID || frame.Result != "fixture reply: "+userText {
			return errClaudeFixtureMessage
		}
		rows, e := d.readClaudeFixtureHistory(s.meta.ID, s)
		if e != nil || len(rows) != 3 {
			return errClaudeFixtureMessage
		}
		var last struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal(rows[2], &last) != nil || last.UUID != frame.UUID {
			return errClaudeFixtureMessage
		}
		text, nextState = frame.Result, "completed"
	default:
		return errClaudeFixtureMessage
	}
	itemID := fmt.Sprintf("fixture:%s:%d", s.meta.ID, env.Sequence)
	item := daemon.InteractionItem{V: 1, ItemID: itemID, TS: time.Now(), Kind: "agent_message", Status: "complete", TurnID: intent.NativeUserUUID}
	raw, full, err := fitItem(item, map[string]any{"text": text, "role": frame.Type})
	if err != nil {
		return err
	}
	if full != nil {
		d.itemMu.Lock()
		d.retainDetailLocked(s.meta.ID, itemID, full)
		d.itemMu.Unlock()
	}
	if err = d.core.RecordInteractionRaw(s.meta.ID, raw); err != nil {
		return err
	}
	if nextState != "" {
		assistant := ""
		if frame.Type == "assistant" {
			assistant = frame.UUID
		}
		if err = d.checkpointClaudeFixture(s, nextState, env.Sequence, assistant, func() string { h := sha256.Sum256(env.Raw); return hex.EncodeToString(h[:]) }()); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.identity, s.sequence = env.Identity, env.Sequence
	if frame.Type == "system" {
		s.initialized = true
		h := sha256.Sum256(env.Raw)
		s.initHash = hex.EncodeToString(h[:])
	}
	if frame.Type == "user" {
		s.userText = text
	}
	s.mu.Unlock()
	return nil
}

func (hd *HookDrainer) drainClaudeFixtureLocked() (int, int, error) {
	s := hd.claudeFixture
	_, ready, _, _ := s.snapshot()
	s.mu.Lock()
	notStarted := !s.initialized && s.healthy
	s.mu.Unlock()
	if !ready && !notStarted {
		return 0, 0, errClaudeFixtureMessage
	}
	cursor := readHookCursor(hd.cursorPath)
	response, err := shim.ReadHookSpoolFileResponse(hd.spoolPath, cursor)
	if err != nil || response.Gap {
		s.fail()
		if err == nil {
			err = ErrHookDrainGap
		}
		return 0, 0, err
	}
	count := 0
	for _, rec := range response.Records {
		if err = hd.d.consumeClaudeFixture(s, rec.Body); err != nil {
			if !errors.Is(err, errClaudeFixturePending) {
				s.fail()
			}
			return count, 0, err
		}
		if err = persistHookDrainCursor(hd.cursorPath, rec.Seq); err != nil {
			s.fail()
			return count, 0, err
		}
		s.mu.Lock()
		s.folded = s.sequence
		s.mu.Unlock()
		count++
	}
	return count, 0, nil
}

func (d *Daemon) claudeFixtureReceipt(local, operation string, rec idempotency.Record) protocol.SemanticMessageReceipt {
	r := protocol.SemanticMessageReceipt{OperationID: operation, Session: local, SessionInstance: rec.SessionInstance, State: "uncertain", Agent: "fake"}
	intent, decodeErr := daemon.DecodeClaudeFixtureIntent(rec.Request)
	if decodeErr != nil || intent.Identity.SessionID != local || intent.SessionInstance != rec.SessionInstance {
		return r
	}

	r.NativeMessageID, r.NativeSessionID = intent.NativeUserUUID, intent.Identity.NativeSessionID
	if rec.OperationID != operation || rec.SessionID != local || (rec.Action != "semantic_send" && rec.Action != "composer_send") {
		return r
	}
	var evidence claudeFixtureEvidence
	if len(rec.Evidence) != 0 {
		if json.Unmarshal(rec.Evidence, &evidence) == nil && validClaudeFixtureEvidence(rec, evidence, intent, operation) {
			r.State, r.OutcomeSource = evidence.State, "claude_primary_fixture"
		}
	} else if rec.Phase == idempotency.PhasePrepared {
		r.State = "prepared"
	} else if rec.Phase == idempotency.PhaseFailed {
		var outcome struct {
			OK      bool               `json:"ok"`
			Code    protocol.ErrorCode `json:"code"`
			Message string             `json:"message"`
		}
		_, valid := fixtureObject(rec.Outcome, "ok", "code", "message")
		if len(rec.Outcome) <= 4096 && valid && json.Unmarshal(rec.Outcome, &outcome) == nil && !outcome.OK && outcome.Message != "" && (outcome.Code == protocol.CodeInputBusy || outcome.Code == protocol.CodeStructuredUnsupported) {
			r.State, r.Reason = "rejected", outcome.Message
		}
	}

	return r
}

func (d *Daemon) claudeFixtureCapabilities(local string, c protocol.SessionCapabilities) protocol.SessionCapabilities {
	s := d.claudeFixtureState(local)
	if s == nil {
		return c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthy && s.initialized && s.ready && s.folded == s.sequence {
		c.NativeInteractionVersion = 1
		c.NativeComposer = s.operation == "" && !s.inputFault && c.Provider == "fake"
	}
	return c
}

func (d *Daemon) proveClaudeFixtureReady(local string, s *claudeFixtureState) error {
	s.mu.Lock()
	s.ready = false
	identity, sequence, folded, owner, healthy := s.identity, s.sequence, s.folded, s.ownerRevision, s.healthy && s.initialized
	s.mu.Unlock()
	if !healthy {
		return errClaudeFixtureMessage
	}
	current, err := d.core.ClaudeFixtureSnapshot(local)
	if err != nil {
		return errClaudeFixturePending
	}
	if current.Identity != identity || !current.Healthy || current.PendingPartial || current.StdoutEOF || current.StderrEOF || sequence != folded || current.ObservedSequence != folded || current.DurableSequence != folded || current.OwnerRevision != owner {
		return errClaudeFixtureMessage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthy || s.identity != identity || s.sequence != sequence || s.folded != folded || s.ownerRevision != owner {
		return errClaudeFixturePending
	}
	s.ready = true
	return nil
}

func validClaudeFixtureEvidence(rec idempotency.Record, e claudeFixtureEvidence, intent claudeFixtureIntent, operation string) bool {
	if e.Version != 1 || e.Kind != "claude_fixture_message" || e.OperationID != operation || e.Intent != intent || (rec.Phase != idempotency.PhaseExecuting && rec.Phase != idempotency.PhaseOutcomeUnknown && rec.Phase != idempotency.PhaseCompleted) {
		return false
	}
	canonical, err := json.Marshal(e)
	if err != nil || !bytes.Equal(canonical, rec.Evidence) || len(e.RawHashes) < 1 || len(e.RawHashes) > 4 {
		return false
	}
	for _, hash := range e.RawHashes {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != hash {
			return false
		}
	}
	switch e.State {
	case "prepared", "submitted", "uncertain":
		return e.EchoSequence == 0 && e.AssistantSequence == 0 && e.ResultSequence == 0 && e.AssistantUUID == "" && len(e.RawHashes) == 1
	case "admitted":
		return e.EchoSequence == 2 && e.ResultSequence == 0 && ((e.AssistantSequence == 0 && e.AssistantUUID == "" && len(e.RawHashes) == 2) || (e.AssistantSequence == 3 && adapter.IsCanonicalConversationID(e.AssistantUUID) && e.AssistantUUID != intent.NativeUserUUID && len(e.RawHashes) == 3))
	case "completed":
		return e.EchoSequence == 2 && e.AssistantSequence == 3 && e.ResultSequence == 4 && adapter.IsCanonicalConversationID(e.AssistantUUID) && e.AssistantUUID != intent.NativeUserUUID && len(e.RawHashes) == 4
	default:
		return false
	}
}
