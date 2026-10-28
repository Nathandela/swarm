package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

var testHookClaudeNativeWritten atomic.Pointer[func(string)]

type claudeNativeState struct {
	claudeFixtureState
	reason            string
	nativeScope       logins.NativeProcessInfo
	deliveredRevision uint64
	busy              bool
}

func (d *Daemon) claudeNativeState(local string) *claudeNativeState {
	lane, ok := d.composerLanes.Load(local)
	if !ok {
		return nil
	}
	l := lane.(*composerLane)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.claudeNative
}
func (d *Daemon) registerClaudeNativeChat(m persist.Meta) error {
	if m.InputEmbargo != "" || m.SchemaVersion != 6 || m.AgentType != "claude" || m.RequiredRuntime != persist.RequiredRuntimeClaudeChat || m.LoginRuntime == nil || m.CLIIdentity == nil || m.CLIIdentity.Version != "2.1.310" || m.ClaudeRetirement != nil || !adapter.IsCanonicalConversationID(m.ConversationID) || d.logins == nil {
		return errClaudeFixtureMessage
	}
	root, err := d.logins.store.OpenClaudeProfileProjects(*m.LoginRuntime)
	if err != nil {
		return err
	}
	if err = root.Close(); err != nil {
		return err
	}
	snap, err := d.core.ClaudeFixtureSnapshot(m.ID)
	if err != nil {
		return err
	}
	scope, err := d.readClaudeNativeScope(m, snap.Identity)
	if err != nil {
		return err
	}
	if err = d.retainClaudeNativeStop(m, scope.Generation); err != nil {
		return err
	}
	lane := d.composerLaneFor(m.ID)
	lane.mu.Lock()
	if lane.claudeNative == nil {
		lane.claudeNative = &claudeNativeState{claudeFixtureState: claudeFixtureState{meta: m, identity: snap.Identity, healthy: true}, nativeScope: scope, deliveredRevision: snap.OwnerRevision}
	}
	lane.mu.Unlock()
	d.registerExecutionSession(m, "", nil, false)
	return nil
}

func (d *Daemon) sendClaudeNativeChat(local, operation string, req protocol.ComposerSendReq, begin func() error, source string) (protocol.ErrorCode, error) {
	s := d.claudeNativeState(local)
	if s == nil || source != adapter.SourceOwner || req.Origin != nil || operation == "" || req.Text == "" || !utf8.ValidString(req.Text) || len(req.Text) > shimwire.MaxClaudeFixtureTextBytes {
		return protocol.CodeStructuredUnsupported, errClaudeFixtureMessage
	}
	resolved, err := d.resolveChatSession(d.machineID(), req.Session)
	if err != nil || resolved != local {
		return protocol.CodeInvalidField, errClaudeFixtureMessage
	}
	instance, ok := d.sessionInstance(local)
	if !ok || instance != req.SessionInstance {
		return protocol.CodeStaleInstance, errClaudeFixtureMessage
	}
	lane := d.composerLaneFor(local)
	lane.mu.Lock()
	lane.pendingClaudeAdmission++
	lane.mu.Unlock()
	lane.claudeAdmitMu.Lock()
	pending := true
	drop := func() {
		if pending {
			lane.mu.Lock()
			lane.pendingClaudeAdmission--
			lane.mu.Unlock()
			pending = false
		}
	}
	defer func() { drop(); lane.claudeAdmitMu.Unlock() }()
	execution, err := d.eng.ExecutionSnapshot(local)
	if err != nil || execution.Validate() != nil || execution.Incarnation != executionIncarnation(s.meta) || execution.ConversationID != s.meta.ConversationID || !execution.UnknownActors {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	rec, ok := d.core.ComposerOperation(operation)
	if !ok || rec.OperationID != operation || rec.SessionID != local || rec.SessionInstance != instance || (rec.Action != "semantic_send" && rec.Action != "composer_send") || rec.RequestHash != hex.EncodeToString(schema.ComposerSendContentHash(&req)) {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	metaCurrent, exists := d.core.Get(local)
	if !exists || metaCurrent.ShimPID != s.meta.ShimPID || metaCurrent.ShimStartTime != s.meta.ShimStartTime || metaCurrent.ConversationID != s.meta.ConversationID || metaCurrent.LoginRuntime == nil || metaCurrent.CLIIdentity == nil || *metaCurrent.LoginRuntime != *s.meta.LoginRuntime || *metaCurrent.CLIIdentity != *s.meta.CLIIdentity || metaCurrent.ProviderCwd() != s.meta.ProviderCwd() {
		return protocol.CodeStaleInstance, errClaudeFixtureMessage
	}
	s.mu.Lock()
	identity, delivered := s.identity, s.deliveredRevision
	s.mu.Unlock()
	hash := sha256.Sum256([]byte(req.Text))
	var intent daemon.ClaudeFixtureIntent
	if len(rec.Request) > 0 {
		intent, err = daemon.DecodeClaudeNativeIntent(rec.Request)
		if err != nil || intent.Identity != identity || intent.SessionInstance != instance || intent.TextSHA256 != hex.EncodeToString(hash[:]) || intent.TextBytes != len(req.Text) || intent.OwnerRevision != execution.OwnerRevision {
			return protocol.CodeInputBusy, errClaudeFixtureMessage
		}
	} else {
		if execution.OwnerRevision == ^uint64(0) {
			return protocol.CodeInputBusy, errClaudeFixtureMessage
		}
		uuid, e := newClaudeFixtureUUID()
		if e != nil {
			return protocol.CodeInputBusy, e
		}
		intent = daemon.ClaudeFixtureIntent{Version: 1, Kind: "claude_native_message", Identity: identity, SessionInstance: instance, NativeUserUUID: uuid, TextSHA256: hex.EncodeToString(hash[:]), TextBytes: len(req.Text), ExpectedOwnerRevision: delivered, OwnerRevision: execution.OwnerRevision + 1}
		if err = d.eng.ObserveExecutionOwnerRevision(local, execution.OwnerRevision, intent.OwnerRevision); err != nil {
			return protocol.CodeInputBusy, err
		}
		bound, _ := json.Marshal(intent)
		if err = d.core.BindSemanticRequest(operation, bound); err != nil {
			s.fail()
			return protocol.CodeInputBusy, err
		}
	}
	// Intent survives readiness refusal and supersedes queued older operations.
	s.mu.Lock()
	blocked := s.busy || s.reason != "" || s.inputFault
	if !blocked {
		s.operation = operation
		s.intent = intent
		s.evidence = claudeFixtureEvidence{Version: 2, Kind: "claude_native_message", OperationID: operation, Intent: intent, State: "prepared", RawHashes: []string{s.initHash}}
	}
	s.ownerRevision = intent.OwnerRevision
	s.mu.Unlock()
	if blocked {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	d.startHookDrain(local)
	s.mu.Lock()
	hd := s.drainer
	s.mu.Unlock()
	if hd == nil {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	if _, _, err = hd.DrainOnce(); err != nil {
		return protocol.CodeInputBusy, err
	}
	if err = d.proveClaudeNativeReady(local, s); err != nil {
		return protocol.CodeInputBusy, err
	}
	drop()
	lane.claudeAdmitMu.Unlock()
	ticket := lane.enter()
	defer lane.leave()
	lane.claudeAdmitMu.Lock()
	if lane.barrierChanged(ticket) {
		return protocol.CodeStaleTurn, errClaudeFixtureMessage
	}
	current, e := d.eng.ExecutionSnapshot(local)
	if e != nil || current.OwnerRevision != intent.OwnerRevision {
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	if err = beginComposerDelivery(begin); err != nil {
		return protocol.CodeInputBusy, err
	}
	if hook := testHookComposerBegunNotYetSubmitted.Load(); hook != nil {
		(*hook)(local)
	}
	snap, err := d.core.ClaudeFixtureSnapshot(local)
	if err != nil {
		return protocol.CodeInputBusy, err
	}
	s.mu.Lock()
	folded := s.folded
	s.mu.Unlock()
	write := shimwire.ClaudeMessageRequest{Version: 1, OperationID: operation, NativeUserUUID: intent.NativeUserUUID, Text: req.Text, Identity: intent.Identity, ExpectedOwnerRevision: intent.ExpectedOwnerRevision, OwnerRevision: intent.OwnerRevision, ThroughStreamSequence: folded, ReaderRevision: snap.ReaderRevision}
	bound, _ := json.Marshal(intent)
	prepared, err := d.core.PrepareClaudeNativeDispatch(local, write, bound)
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
		s.mu.Lock()
		invalid = invalid || s.ownerRevision != intent.OwnerRevision || s.busy || s.reason != "" || !s.healthy
		s.mu.Unlock()
		if invalid {
			return errClaudeFixtureMessage
		}
		var e error
		result, e = prepared.Write()
		return e
	})
	prepared.Close()
	if err != nil || result.Status != "written" {
		if result.Status == "uncertain" {
			s.mu.Lock()
			s.inputFault = true
			s.mu.Unlock()
			_ = d.checkpointClaudeFixture(&s.claudeFixtureState, "uncertain", 0, "", "")
			return protocol.CodeOutcomeUnknown, protocol.ErrComposerOutcomeUnknown
		}
		return protocol.CodeInputBusy, errClaudeFixtureMessage
	}
	if hook := testHookClaudeNativeWritten.Load(); hook != nil {
		(*hook)(local)
	}
	s.mu.Lock()
	if s.operation == operation && s.evidence.State != "completed" {
		s.busy = true
	}
	s.deliveredRevision = intent.OwnerRevision
	s.mu.Unlock()
	if err = d.checkpointClaudeFixture(&s.claudeFixtureState, "submitted", 0, "", ""); err != nil {
		s.fail()
		return protocol.CodeOutcomeUnknown, protocol.ErrComposerOutcomeUnknown
	}
	return "", nil
}

func (d *Daemon) proveClaudeNativeReady(local string, s *claudeNativeState) error {
	snap, err := d.core.ClaudeFixtureSnapshot(local)
	if err != nil {
		return err
	}
	current, ok := d.core.Get(local)
	if !ok || current.InputEmbargo != "" || current.LoginRuntime == nil || current.CLIIdentity == nil || *current.LoginRuntime != *s.meta.LoginRuntime || *current.CLIIdentity != *s.meta.CLIIdentity || current.ProviderCwd() != s.meta.ProviderCwd() {
		return errClaudeFixtureMessage
	}
	scope, err := d.readClaudeNativeScope(current, snap.Identity)
	if err != nil || scope != s.nativeScope {
		return errClaudeFixtureMessage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthy || !s.initialized || s.busy || s.reason != "" || s.inputFault || snap.Identity != s.identity || !snap.Initialized || !snap.Healthy || snap.PendingPartial || snap.StdoutEOF || snap.StderrEOF || s.sequence != s.folded || snap.ObservedSequence != s.folded || snap.DurableSequence != s.folded {
		return errClaudeFixtureMessage
	}
	return nil
}

func (hd *HookDrainer) drainClaudeNativeLocked() (int, int, error) {
	s := hd.claudeNative
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
		if err = hd.d.consumeClaudeNative(s, rec.Body); err != nil {
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

func (d *Daemon) consumeClaudeNative(s *claudeNativeState, body []byte) error {
	if !strictClaudeNativeJSON(body, "envelope", 0) {
		return errClaudeFixtureMessage
	}
	var origin struct {
		Origin string `json:"origin"`
	}
	if json.Unmarshal(body, &origin) != nil {
		return errClaudeFixtureMessage
	}
	if origin.Origin == "claude_hook" {
		return d.consumeClaudeNativeHook(s, body)
	}
	object, ok := fixtureObject(body, "v", "origin", "native_session_id", "primary", "read_monotonic_ns", "stream_sequence", "raw")
	if !ok {
		return errClaudeFixtureMessage
	}
	if _, ok = fixtureObject(object["primary"], "session_id", "required_runtime", "native_session_id", "shim_pid", "shim_start_time", "primary_pid", "primary_start_time"); !ok {
		return errClaudeFixtureMessage
	}
	var env struct {
		V        int                            `json:"v"`
		Origin   string                         `json:"origin"`
		Native   string                         `json:"native_session_id"`
		Identity shimwire.ClaudePrimaryIdentity `json:"primary"`
		Read     int64                          `json:"read_monotonic_ns"`
		Seq      uint64                         `json:"stream_sequence"`
		Raw      []byte                         `json:"raw"`
	}
	if json.Unmarshal(body, &env) != nil || env.V != 1 || env.Origin != "claude_primary" || env.Native != s.meta.ConversationID || env.Read <= 0 {
		return errClaudeFixtureMessage
	}
	snap, err := d.core.ClaudeFixtureSnapshot(s.meta.ID)
	if err != nil {
		return errClaudeFixturePending
	}
	s.mu.Lock()
	valid := s.healthy && env.Identity == s.identity && env.Seq == s.sequence+1
	intent, evidence := s.intent, s.evidence
	s.mu.Unlock()
	if !valid || snap.Identity != env.Identity || !snap.Healthy || snap.StdoutEOF || snap.StderrEOF {
		return errClaudeFixtureMessage
	}
	if snap.DurableSequence < env.Seq || snap.ObservedSequence < env.Seq {
		return errClaudeFixturePending
	}
	if !strictClaudeNativeJSON(env.Raw, "frame", 0) {
		return errClaudeFixtureMessage
	}
	fields, ok := decodeStrictObject(env.Raw)
	if !ok {
		return errClaudeFixtureMessage
	}
	var f struct {
		Type, Subtype, UUID string
		Session             string          `json:"session_id"`
		Parent              json.RawMessage `json:"parent_tool_use_id"`
		Replay              bool            `json:"isReplay"`
		State               string
		User                string   `json:"user_message_uuid"`
		Users               []string `json:"user_message_uuids"`
		Error               bool     `json:"is_error"`
		Result              string
		Message             struct {
			ID, Role string
			Content  json.RawMessage
		}
		Response struct {
			Subtype  string
			ID       string `json:"request_id"`
			Response struct {
				PID     int
				Version string `json:"claude_code_version"`
				State   string `json:"session_state"`
			}
		}
		Request struct {
			Subtype string
			Tool    string `json:"tool_name"`
		}
	}
	if json.Unmarshal(env.Raw, &f) != nil {
		return errClaudeFixtureMessage
	}
	if f.Type != "control_response" && f.Type != "control_request" && f.Session != s.meta.ConversationID {
		return errClaudeFixtureMessage
	}
	next, assistant, text, reason := "", "", "", ""
	switch f.Type {
	case "control_response":
		s.mu.Lock()
		initialized := s.initialized
		s.mu.Unlock()
		if initialized || !snap.Initialized || snap.InitializeResponseSequence != env.Seq || snap.InitializeRequestID == "" || f.Response.ID != snap.InitializeRequestID || f.Response.Subtype != "success" || f.Response.Response.PID != env.Identity.PrimaryPID || f.Response.Response.Version != "2.1.310" {
			return errClaudeFixtureMessage
		}
		s.mu.Lock()
		s.initialized = true
		s.busy = f.Response.Response.State != "idle"
		if s.busy {
			s.reason = "native initialization state " + f.Response.Response.State
		}
		s.mu.Unlock()
	case "user":
		var value string
		h := sha256.Sum256(nil)
		if json.Unmarshal(f.Message.Content, &value) == nil {
			h = sha256.Sum256([]byte(value))
		}
		if !f.Replay || f.UUID != intent.NativeUserUUID || f.Message.Role != "user" || !bytes.Equal(f.Parent, []byte("null")) || len(value) != intent.TextBytes || hex.EncodeToString(h[:]) != intent.TextSHA256 || evidence.EchoSequence != 0 || intent.OwnerRevision == 0 {
			return errClaudeFixtureMessage
		}
		rec, exists := d.core.ComposerOperation(s.operation)
		bound, e := daemon.DecodeClaudeNativeIntent(rec.Request)
		if !exists || e != nil || bound != intent || rec.Phase != idempotency.PhaseExecuting && rec.Phase != idempotency.PhaseCompleted && rec.Phase != idempotency.PhaseOutcomeUnknown {
			return errClaudeFixtureMessage
		}
		next, text = "admitted", value
	case "assistant":
		if evidence.EchoSequence == 0 || !adapter.IsCanonicalConversationID(f.UUID) || f.Message.Role != "assistant" || f.Message.ID == "" || !bytes.Equal(f.Parent, []byte("null")) {
			return errClaudeFixtureMessage
		}
		var blocks []struct {
			Type, Text, Name, ID string
			Input                json.RawMessage
		}
		if json.Unmarshal(f.Message.Content, &blocks) != nil {
			return errClaudeFixtureMessage
		}
		for _, b := range blocks {
			if b.Type == "text" {
				text += b.Text
			}
			if b.Type == "tool_use" {
				if b.ID == "" || b.Name == "" {
					return errClaudeFixtureMessage
				}
				if _, valid := decodeStrictObject(b.Input); !valid {
					return errClaudeFixtureMessage
				}
				if b.Name == "AskUserQuestion" {
					reason = b.Name
				}
				if b.Name == "Task" || b.Name == "Agent" {
					reason = "unhandled native actor " + b.Name
				}
			} else if b.Type != "text" && b.Type != "thinking" && b.Type != "redacted_thinking" {
				reason = "unhandled native block " + b.Type
			}
		}
		next, assistant = "admitted", f.UUID
	case "result":
		correlated := f.User == intent.NativeUserUUID && f.User != ""
		for _, id := range f.Users {
			if id != intent.NativeUserUUID {
				correlated = false
			}
		}
		if f.User == "" && len(f.Users) > 0 {
			correlated = true
			for _, id := range f.Users {
				if id != intent.NativeUserUUID || id == "" {
					correlated = false
				}
			}
		}
		if evidence.EchoSequence != 0 && correlated && !f.Error && f.Subtype == "success" && adapter.IsCanonicalConversationID(f.UUID) && f.UUID != intent.NativeUserUUID && f.UUID != evidence.AssistantUUID {
			next = "completed"
			text = f.Result

		} else {
			reason = "uncorrelated or unsuccessful native result"
		}
	case "control_request":
		reason = f.Request.Tool
		if reason == "" {
			reason = "unhandled native control request"
		}
	case "system":
		if f.Subtype == "session_state_changed" {
			s.mu.Lock()
			if f.State == "running" {
				s.busy = true
			} else if f.State != "idle" {
				reason = "native state " + f.State
			}
			s.mu.Unlock()
		}
	case "command_lifecycle", "rate_limit_event":
	default:
		reason = "unhandled native event " + f.Type
	}
	if reason != "" {
		s.mu.Lock()
		s.reason = reason
		s.busy = true
		s.mu.Unlock()
		m, ok := d.core.Get(s.meta.ID)
		if ok {
			m.Status.Turn = status.TurnIdle
			m.Status.Interaction = status.InteractionPrompt
			if err = d.core.SetStatus(m.ID, m.Status); err != nil {
				return err
			}
		}
	}
	item := daemon.InteractionItem{V: 1, ItemID: fmt.Sprintf("claude-native:%s:%d", s.meta.ID, env.Seq), TS: time.Now(), Kind: "agent_message", Status: "complete", TurnID: intent.NativeUserUUID}
	raw, full, err := fitItem(item, map[string]any{"text": text, "role": f.Type, "native": json.RawMessage(env.Raw)})
	if err != nil {
		return err
	}
	if full != nil {
		d.itemMu.Lock()
		d.retainDetailLocked(s.meta.ID, item.ItemID, full)
		d.itemMu.Unlock()
	}
	if err = d.core.RecordInteractionRaw(s.meta.ID, raw); err != nil {
		return err
	}
	if next != "" {
		hash := sha256.Sum256(env.Raw)
		if err = d.checkpointClaudeFixture(&s.claudeFixtureState, next, env.Seq, assistant, hex.EncodeToString(hash[:])); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.sequence = env.Seq
	if next == "completed" {
		s.busy = false
	}
	if f.Type == "control_response" {
		h := sha256.Sum256(env.Raw)
		s.initHash = hex.EncodeToString(h[:])
	}
	s.mu.Unlock()
	_ = fields
	return nil
}

func (d *Daemon) claudeNativeChatReceipt(local, operation string, rec idempotency.Record) protocol.SemanticMessageReceipt {
	r := protocol.SemanticMessageReceipt{OperationID: operation, Session: local, SessionInstance: rec.SessionInstance, State: "uncertain", Agent: "claude"}
	intent, err := daemon.DecodeClaudeNativeIntent(rec.Request)
	if err != nil || intent.Identity.SessionID != local || intent.SessionInstance != rec.SessionInstance || rec.OperationID != operation || rec.SessionID != local {
		return r
	}
	r.NativeMessageID = intent.NativeUserUUID
	r.NativeSessionID = intent.Identity.NativeSessionID
	var evidence claudeFixtureEvidence
	if json.Unmarshal(rec.Evidence, &evidence) == nil && evidence.Version == 2 && evidence.Kind == "claude_native_message" && evidence.OperationID == operation && evidence.Intent == intent {
		r.State = evidence.State
		r.OutcomeSource = "claude_primary_native"
	} else if rec.Phase == idempotency.PhasePrepared {
		r.State = "prepared"
	}
	if s := d.claudeNativeState(local); s != nil {
		s.mu.Lock()
		r.Reason = s.reason
		s.mu.Unlock()
	}
	return r
}

func (d *Daemon) readClaudeNativeHistory(s *claudeNativeState) ([]json.RawMessage, error) {
	root, err := d.logins.store.OpenClaudeProfileProjects(*s.meta.LoginRuntime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	layout, _ := adapter.AsTranscriptLayout(claude.New())
	name := layout.ProjectDirName(s.meta.ProviderCwd())
	before, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && d.claudeNativePristine(s) {
			return []json.RawMessage{}, nil
		}
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errClaudeFixtureMessage
	}
	project, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = project.Close() }()
	after, err := project.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		return nil, errClaudeFixtureMessage
	}
	if _, err = project.Lstat(layout.TranscriptFileName(s.meta.ConversationID)); errors.Is(err, os.ErrNotExist) && d.claudeNativePristine(s) {
		return []json.RawMessage{}, nil
	}
	resolver := filesystemResumeHistoryResolver{}
	budget := historyBudget{limits: resumeHistoryLimits{MaxEntries: 4096, MaxOpenFiles: 1, MaxRecordBytes: schema.MaxNativeHistoryPageBytes, MaxTotalBytes: schema.MaxNativeHistoryPageBytes}}
	f, outcome := resolver.openCandidate(project, "", layout.TranscriptFileName(s.meta.ConversationID), &budget)
	if outcome != resumeHistoryFound {
		return nil, errClaudeFixturePending
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, schema.MaxNativeHistoryPageBytes+1))
	if err != nil || len(raw) > schema.MaxNativeHistoryPageBytes || len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return nil, errClaudeFixturePending
	}
	var rows []json.RawMessage
	seen := map[string]bool{}
	s.mu.Lock()
	intent := s.intent
	s.mu.Unlock()

	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var row struct {
			Type, UUID string
			Session    string  `json:"sessionId"`
			Parent     *string `json:"parentUuid"`
		}
		if !strictClaudeNativeJSON(line, "history", 0) || json.Unmarshal(line, &row) != nil || row.Session != "" && row.Session != s.meta.ConversationID {
			return nil, errClaudeFixtureMessage
		}
		if row.UUID == "" {
			switch row.Type {
			case "file-history-snapshot", "queue-operation", "summary":
				if row.Parent != nil {
					return nil, errClaudeFixtureMessage
				}
				if len(rows) >= 4096 {
					return nil, errClaudeFixtureMessage
				}
				rows = append(rows, append(json.RawMessage(nil), line...))
				continue
			default:
				return nil, errClaudeFixtureMessage
			}
		}
		if row.Session != s.meta.ConversationID || !adapter.IsCanonicalConversationID(row.UUID) || seen[row.UUID] || row.Parent != nil && !seen[*row.Parent] {
			return nil, errClaudeFixtureMessage
		}
		if len(rows) >= 4096 {
			return nil, errClaudeFixtureMessage
		}
		if row.UUID == intent.NativeUserUUID {
			var owner struct {
				Message struct {
					Role    string
					Content json.RawMessage
				}
			}
			var text string
			if json.Unmarshal(line, &owner) != nil || owner.Message.Role != "user" || json.Unmarshal(owner.Message.Content, &text) != nil {
				return nil, errClaudeFixtureMessage
			}
			h := sha256.Sum256([]byte(text))
			if len(text) != intent.TextBytes || hex.EncodeToString(h[:]) != intent.TextSHA256 {
				return nil, errClaudeFixtureMessage
			}

		}
		seen[row.UUID] = true
		rows = append(rows, append(json.RawMessage(nil), line...))
	}
	return rows, nil
}
func (d *Daemon) claudeNativeHistory(req schema.InteractionHistoryReq, local string) (schema.NativeHistoryPage, protocol.ErrorCode, error) {
	s := d.claudeNativeState(local)
	if s == nil || req.NativeCursor != "" || req.BeforeItem != "" {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errClaudeFixtureMessage
	}
	d.startHookDrain(local)
	s.mu.Lock()
	hd := s.drainer
	healthy := s.healthy
	s.mu.Unlock()
	if hd == nil || !healthy {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errClaudeFixtureMessage
	}
	if _, _, err := hd.DrainOnce(); err != nil {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, err
	}
	rows, err := d.readClaudeNativeHistory(s)
	if err != nil {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, err
	}
	instance, ok := d.sessionInstance(local)
	if !ok {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errClaudeFixtureMessage
	}
	bound, _ := json.Marshal(struct {
		Identity shimwire.ClaudePrimaryIdentity
		Instance string
	}{s.identity, instance})
	hash := sha256.Sum256(bound)
	return schema.NativeHistoryPage{Version: 2, Scope: hex.EncodeToString(hash[:]), Items: rows, Floor: true}, "", nil
}

// readClaudeNativeScope binds the captured primary to its durable owned writer record.
func (d *Daemon) readClaudeNativeScope(m persist.Meta, id shimwire.ClaudePrimaryIdentity) (logins.NativeProcessInfo, error) {
	var n logins.NativeProcessInfo
	root, err := os.OpenRoot(d.stateDir)
	if err != nil {
		return n, err
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(m.ID)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0077 != 0 {
		return n, errClaudeFixtureMessage
	}
	session, err := root.OpenRoot(m.ID)
	if err != nil {
		return n, err
	}
	defer func() { _ = session.Close() }()
	after, err := session.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		return n, errClaudeFixtureMessage
	}
	f, err := session.OpenFile(logins.NativeProcessFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return n, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return n, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > 64<<10 {
		return n, errClaudeFixtureMessage
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return n, errClaudeFixtureMessage
	}
	if !strictClaudeNativeJSON(raw, "scope", 0) {
		return n, errClaudeFixtureMessage
	}
	object, ok := decodeStrictObject(raw)
	if _, runtimeOK := decodeStrictObject(object["runtime"]); !runtimeOK {
		return n, errClaudeFixtureMessage
	}
	if !ok {
		return n, errClaudeFixtureMessage
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&n) != nil {
		return n, errClaudeFixtureMessage
	}
	generation, e := hex.DecodeString(n.Generation)
	if e != nil || len(generation) != 16 || hex.EncodeToString(generation) != n.Generation || n.SchemaVersion != logins.ClaudeNativeWriterSchemaVersion || n.Provider != "claude" || n.Runtime != *m.LoginRuntime || n.SessionID != m.ID || n.Cwd != m.ProviderCwd() || n.CLIFingerprint != m.CLIIdentity.Fingerprint || n.ShimPID != m.ShimPID || n.ShimStartTime != m.ShimStartTime || n.PID != id.PrimaryPID || n.StartTime != id.PrimaryStartTime || n.PGID != n.PID || n.BackendPID != 0 || n.BackendStartTime != 0 || n.IncidentID != "" {
		return n, errClaudeFixtureMessage
	}
	_ = object
	return n, nil
}
func (d *Daemon) claudeNativePristine(s *claudeNativeState) bool {
	execution, err := d.eng.ExecutionSnapshot(s.meta.ID)
	if err != nil || execution.OwnerRevision != 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy && s.initialized && s.ownerRevision == 0 && s.operation == "" && s.intent.OwnerRevision == 0
}
func (d *Daemon) consumeClaudeNativeHook(s *claudeNativeState, body []byte) error {
	object, ok := fixtureObject(body, "v", "origin", "primary", "scope", "raw")
	if !ok {
		return errClaudeFixtureMessage
	}
	if _, ok = fixtureObject(object["primary"], "session_id", "required_runtime", "native_session_id", "shim_pid", "shim_start_time", "primary_pid", "primary_start_time"); !ok {
		return errClaudeFixtureMessage
	}
	var env struct {
		V       int                            `json:"v"`
		Origin  string                         `json:"origin"`
		Primary shimwire.ClaudePrimaryIdentity `json:"primary"`
		Scope   logins.NativeProcessInfo       `json:"scope"`
		Raw     []byte                         `json:"raw"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&env) != nil {
		return errClaudeFixtureMessage
	}
	expected := s.nativeScope
	expected.PID = 0
	expected.PGID = 0
	expected.StartTime = 0
	identity := s.identity
	identity.PrimaryPID = 0
	identity.PrimaryStartTime = 0
	if env.V != 1 || env.Origin != "claude_hook" || env.Primary != identity || env.Scope != expected || len(env.Raw) == 0 {
		return errClaudeFixtureMessage
	}
	// Hook callbacks remain opaque: even primary-shaped bytes cannot admit or settle input.
	hash := sha256.Sum256(body)
	item := daemon.InteractionItem{V: 1, ItemID: "claude-hook:" + hex.EncodeToString(hash[:]), TS: time.Now(), Kind: "agent_message", Status: "complete"}
	raw, full, err := fitItem(item, map[string]any{"role": "hook", "raw": env.Raw})
	if err != nil {
		return err
	}
	if full != nil {
		d.itemMu.Lock()
		d.retainDetailLocked(s.meta.ID, item.ItemID, full)
		d.itemMu.Unlock()
	}
	return d.core.RecordInteractionRaw(s.meta.ID, raw)
}

// Claude JSON accepts unknown metadata, but never ambiguous authoritative keys.
func strictClaudeNativeJSON(raw []byte, context string, depth int) bool {
	if depth > 64 {
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '[' {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !strictClaudeNativeJSON(value, context, depth+1) {
				return false
			}
		}
		return true
	}
	if raw[0] != '{' {
		return json.Valid(raw)
	}
	object, ok := decodeStrictObject(raw)
	if !ok {
		return false
	}
	var keys []string
	switch context {
	case "frame":
		keys = []string{"type", "subtype", "uuid", "session_id", "parent_tool_use_id", "isReplay", "state", "user_message_uuid", "user_message_uuids", "is_error", "result", "message", "response", "request"}
	case "history":
		keys = []string{"type", "uuid", "sessionId", "parentUuid", "isSidechain", "message"}
	case "message":
		keys = []string{"id", "role", "content"}
	case "block":
		keys = []string{"type", "text", "name", "id", "input"}
	case "response":
		keys = []string{"subtype", "request_id", "response"}
	case "initialize":
		keys = []string{"pid", "claude_code_version", "session_state"}
	case "request":
		keys = []string{"subtype", "tool_name"}
	case "scope":
		keys = []string{"schema_version", "generation", "backend_pid", "backend_start_time", "pid", "pgid", "start_time", "shim_pid", "shim_start_time", "provider", "runtime", "incident_id", "session_id", "cwd", "cli_fingerprint"}
	case "runtime":
		keys = []string{"provider", "epoch", "identity", "home", "external_auth"}
	case "envelope":
		keys = []string{"v", "origin", "native_session_id", "primary", "scope", "read_monotonic_ns", "stream_sequence", "raw"}
	case "primary":
		keys = []string{"session_id", "required_runtime", "native_session_id", "shim_pid", "shim_start_time", "primary_pid", "primary_start_time"}
	}
	for key, value := range object {
		for _, known := range keys {
			if strings.EqualFold(key, known) && key != known {
				return false
			}
		}
		child := ""
		switch {
		case (context == "frame" || context == "history") && key == "message":
			child = "message"
		case context == "message" && key == "content":
			child = "block"
		case context == "frame" && key == "response":
			child = "response"
		case context == "response" && key == "response":
			child = "initialize"
		case context == "frame" && key == "request":
			child = "request"
		case context == "scope" && key == "runtime":
			child = "runtime"
		case context == "envelope" && key == "primary":
			child = "primary"
		case context == "envelope" && key == "scope":
			child = "scope"
		}
		if !strictClaudeNativeJSON(value, child, depth+1) {
			return false
		}
	}
	return true
}
