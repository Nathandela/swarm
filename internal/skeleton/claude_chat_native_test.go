package skeleton

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// Dormant production registration/send entries, not an exported test factory.
// Initial absence is runtime RED; the assertions below are behavioral once present.
type claudeNativeConsumerEntry interface {
	registerClaudeNativeChat(persist.Meta) error
	sendClaudeNativeChat(string, string, protocol.ComposerSendReq, func() error, string) (protocol.ErrorCode, error)
}

const nativeA2Session = "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772"
const nativeA2Assistant = "79b79d77-e62d-44b9-8e24-94e908f08935"
const nativeA2Result = "89b79d77-e62d-44b9-8e24-94e908f08935"
const nativeA2Question = "Select the baseline fixture choice."

func nativeA2QuestionInput() map[string]any {
	return map[string]any{"questions": []any{map[string]any{
		"question": nativeA2Question, "header": "Baseline", "multiSelect": false,
		"options": []any{map[string]any{"label": "Alpha", "description": "Authorized synthetic choice."}, map[string]any{"label": "Beta", "description": "Unused synthetic choice."}},
	}}}
}

// This is an independently constructed synthetic shim peer/snapshot, NOT a
// managed native birth, initialize-write, stdin, account or Stop proof. The
// existing assembly, durable semantic store, HookSpool/drainer and history API
// are real; the shim author's prepared-child tests separately cover transport.
type nativeA2Fixture struct {
	d                   *Daemon
	entry               claudeNativeConsumerEntry
	meta                persist.Meta
	identity            shimwire.ClaudePrimaryIdentity
	spool               *shim.HookSpool
	mu                  sync.Mutex
	seq, delivered      uint64
	mode, initMode      string
	writes              []shimwire.ClaudeMessageRequest
	unexpected          int
	answers             int
	history             string
	registrationErr     error
	snapshotUnavailable bool
	allowStop           bool
	stops               int
}

func newNativeA2Fixture(t *testing.T, mode, initMode string) *nativeA2Fixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("consumer fixture uses existing Linux synthetic saved-profile helper; no Darwin Keychain/native run")
	}
	root, err := os.MkdirTemp("", "cna2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selected, _, rt := claudeOwnerBirthFixture(t, root, cwd)
	birth, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{SchemaVersion: 6, ID: "native-consumer", AgentType: "claude", RequiredRuntime: "claude-chat", ConversationID: nativeA2Session, Cwd: cwd, CreatedAt: time.Now().UTC(), ShimPID: os.Getpid(), ShimStartTime: birth, LoginRuntime: &rt, CLIIdentity: &persist.CLIIdentity{Path: exe, Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}}
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(m); err != nil {
		t.Fatal(err)
	}
	f := &nativeA2Fixture{meta: m, mode: mode, initMode: initMode, identity: shimwire.ClaudePrimaryIdentity{SessionID: m.ID, RequiredRuntime: m.RequiredRuntime, NativeSessionID: m.ConversationID, ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, PrimaryPID: os.Getpid(), PrimaryStartTime: birth}}
	if initMode != "idle" {
		// Registered before consumer entry: even registration failure/Fatal must
		// prove zero effects after the peer handlers and daemon have joined.
		t.Cleanup(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 || f.answers != 0 || f.unexpected != 0 {
				t.Errorf("refused initialization still wrote input/answer/control: writes=%d answers=%d other=%d", len(f.writes), f.answers, f.unexpected)
			}
		})
	}
	// Synthetic retained scope supplies consumer provenance only; it is not a native birth proof.
	native := logins.NativeProcessInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("c", 32), PID: f.identity.PrimaryPID, PGID: f.identity.PrimaryPID, StartTime: f.identity.PrimaryStartTime, ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, Provider: "claude", Runtime: rt, SessionID: m.ID, Cwd: m.ProviderCwd(), CLIFingerprint: m.CLIIdentity.Fingerprint}
	nativeRaw, _ := json.Marshal(native)
	if err = os.WriteFile(filepath.Join(root, m.ID, logins.NativeProcessFile), nativeRaw, 0600); err != nil {
		t.Fatal(err)
	}
	f.spool, err = shim.OpenHookSpoolProducer(filepath.Join(root, m.ID, shim.HookSpoolFile), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.spool.Close() })
	layout, ok := adapter.AsTranscriptLayout(claude.New())
	if !ok {
		t.Fatal("Claude transcript layout absent")
	}
	f.history = filepath.Join(selected.Home("claude"), "projects", layout.ProjectDirName(cwd), layout.TranscriptFileName(nativeA2Session))
	// Real native order: hook metadata1/2, sessionless initialize acknowledgement3.
	f.append(t, map[string]any{"type": "system", "subtype": "hook_started", "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000001", "hook_id": "startup", "hook_event": "SessionStart", "hook_name": "project"})
	f.append(t, map[string]any{"type": "system", "subtype": "hook_response", "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000002", "hook_id": "startup", "hook_event": "SessionStart", "hook_name": "project", "output": "", "stdout": "", "stderr": "", "exit_code": 0, "outcome": "success"})
	rid, pid, version, state := "native-initialize", f.identity.PrimaryPID, "2.1.310", "idle"
	switch initMode {
	case "wrong-id":
		rid = "foreign-initialize"
	case "wrong-pid":
		pid++
	case "wrong-version":
		version = "2.1.309"
	case "human":
		state = "requires_action"
	case "running":
		state = "running"
	case "unknown-state":
		state = "future-state"
	}
	switch initMode {
	case "system-only":
		f.append(t, map[string]any{"type": "system", "subtype": "init", "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000003", "claude_code_version": "2.1.310", "cwd": cwd})
	case "alias-response":
		f.append(t, json.RawMessage(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"foreign-initialize","REQUEST_ID":"native-initialize","response":{"pid":%d,"claude_code_version":"2.1.310","session_state":"idle"}}}`, pid)))
	case "duplicate-response":
		f.append(t, json.RawMessage(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":"native-initialize","response":{"pid":%d,"pid":%d,"claude_code_version":"2.1.310","session_state":"idle"}}}`, pid+1, pid)))
	default:
		f.append(t, map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": rid, "response": map[string]any{"pid": pid, "claude_code_version": version, "session_state": state, "commands": []any{}, "agents": []any{}}}})
	}
	f.serve(t, filepath.Join(root, m.ID, "shim.sock"))
	launch, _ := json.Marshal(map[string]any{"session_id": m.ID, "required_runtime": m.RequiredRuntime, "conversation_id": m.ConversationID, "hook_socket_path": filepath.Join(root, m.ID, "hooks.sock"), "hook_drain_token": "native-drain-token", "env": []string{}})
	if err = os.WriteFile(filepath.Join(root, m.ID, "shim-launch.json"), launch, 0600); err != nil {
		t.Fatal(err)
	}
	f.d = assemble(t, func(c *Config) {
		c.StateDir = root
		c.SocketPath = filepath.Join(root, "d.sock")
		c.LockPath = filepath.Join(root, "d.lock")
		c.LogPath = filepath.Join(root, "d.log")
	})
	var exists bool
	f.entry, exists = any(f.d).(claudeNativeConsumerEntry)
	if !exists {
		t.Fatal("dormant native consumer/send entries absent; API-absence RED only, not behavioral guard proof")
	}
	// Explicit dormant component registration; public Run/caller admission stays off.
	f.registrationErr = f.entry.registerClaudeNativeChat(m)
	if f.registrationErr != nil && initMode == "idle" {
		t.Fatal(f.registrationErr)
	}
	if f.registrationErr == nil {
		// Explicit synthetic fresh-birth event exercises the production admission latch.
		// This supplies no native process-birth or restart-input qualification.
		f.d.noteClaudeNativeLiveLaunch(m)
		f.d.startHookDrain(m.ID)
	}
	return f
}

func (f *nativeA2Fixture) append(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	body, err := json.Marshal(map[string]any{"v": 1, "origin": "claude_primary", "native_session_id": nativeA2Session, "primary": f.identity, "read_monotonic_ns": int64(f.seq), "stream_sequence": f.seq, "raw": raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.spool.Append(body); err != nil {
		t.Fatal(err)
	}
}

func (f *nativeA2Fixture) snapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapshotUnavailable {
		return map[string]any{}
	}
	return map[string]any{"identity": f.identity, "observed_sequence": f.seq, "durable_sequence": f.seq, "reader_revision": f.seq, "owner_revision": f.delivered, "healthy": true, "pending_partial": false, "stdout_eof": false, "stderr_eof": false, "initialize_request_id": "native-initialize", "initialize_response_sequence": 3, "initialized": true}
}

func (f *nativeA2Fixture) serve(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	var handlers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(time.Second))
				typ, raw, err := wire.ReadFrame(c)
				if err != nil || typ != wire.TControl {
					return
				}
				h, err := shimwire.Decode(raw)
				if err != nil || h.Type != shimwire.TypeHello {
					return
				}
				if h.ClaudeStopV1 {
					binding := shimwire.ClaudeStopBinding{Version: 1, SchemaVersion: f.meta.SchemaVersion, SessionID: f.meta.ID, RequiredRuntime: f.meta.RequiredRuntime, NativeSessionID: f.meta.ConversationID, Incarnation: executionIncarnation(f.meta), Runtime: *f.meta.LoginRuntime, Cwd: f.meta.ProviderCwd(), CLIIdentity: *f.meta.CLIIdentity, ShimPID: f.meta.ShimPID, ShimStartTime: f.meta.ShimStartTime, ScopeGeneration: strings.Repeat("c", 32)}
					reply := shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, ClaudeStopV1: true, ClaudeStopBinding: &binding}
					if f.initMode == "stop-unavailable" {
						reply.ClaudeStopV1 = false
						reply.ClaudeStopBinding = nil
					}
					body, _ := shimwire.Encode(reply)
					if wire.WriteFrame(c, wire.TControl, body) != nil {
						return
					}
					typ, body, e := wire.ReadFrame(c)
					if e != nil {
						return
					}
					request, e := shimwire.Decode(body)
					f.mu.Lock()
					valid := e == nil && typ == wire.TControl && request.Type == shimwire.TypeClaudeStop && request.ClaudeStop != nil && request.ClaudeStop.Binding == binding && f.allowStop
					if valid {
						f.stops++
					} else {
						f.unexpected++
					}
					f.mu.Unlock()
					if !valid {
						return
					}
					body, _ = shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeStopResult, ClaudeStopResult: &shimwire.ClaudeStopResult{Version: 1, Binding: binding, Status: "delivered"}})
					_ = wire.WriteFrame(c, wire.TControl, body)
					return
				}
				reply, _ := json.Marshal(map[string]any{"type": shimwire.TypeHello, "wire_version": shimwire.Version, "claude_primary_v1": true, "claude_primary": f.snapshot()})
				if wire.WriteFrame(c, wire.TControl, reply) != nil {
					return
				}
				typ, raw, err = wire.ReadFrame(c)
				if err != nil {
					return
				}
				h, err = shimwire.Decode(raw)
				if err != nil || typ != wire.TControl {
					f.mu.Lock()
					f.unexpected++
					f.mu.Unlock()
					return
				}
				if h.Type != shimwire.TypeClaudeMessage || h.ClaudeMessage == nil {
					f.mu.Lock()
					f.unexpected++
					if h.Type == "control_response" {
						f.answers++
					}
					f.mu.Unlock()
					return
				}
				req := *h.ClaudeMessage
				f.mu.Lock()
				f.writes = append(f.writes, req)
				f.delivered = req.OwnerRevision
				f.mu.Unlock()
				result := shimwire.ClaudeMessageResult{Version: 1, OperationID: req.OperationID, NativeUserUUID: req.NativeUserUUID, Identity: f.identity, OwnerRevision: req.OwnerRevision, Status: "written"}
				if f.mode == "outcome-unknown" {
					result.Status = "uncertain"
				}
				encoded, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeMessageResult, ClaudeMessageResult: &result})
				_ = wire.WriteFrame(c, wire.TControl, encoded)
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done; handlers.Wait() })
}

func (f *nativeA2Fixture) drain(t *testing.T) error {
	t.Helper()
	f.d.drains.mu.Lock()
	hd := f.d.drains.drainers[f.meta.ID]
	f.d.drains.mu.Unlock()
	if hd == nil {
		t.Fatal("native routing never installed existing HookDrainer")
	}
	_, _, err := hd.DrainOnce()
	return err
}

func (f *nativeA2Fixture) claim(t *testing.T, text string) (string, protocol.ComposerSendReq) {
	t.Helper()
	op, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := f.d.sessionInstance(f.meta.ID)
	if !ok {
		t.Fatal("native instance absent")
	}
	req := protocol.ComposerSendReq{Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), SessionInstance: instance, Text: text}
	hash := schema.ComposerSendContentHash(&req)
	if _, _, err = f.d.core.ClaimComposerOperation(op, "semantic_send", f.meta.ID, instance, hex.EncodeToString(hash)); err != nil {
		t.Fatal(err)
	}
	return op, req
}

func (f *nativeA2Fixture) send(t *testing.T, text string) (string, error) {
	t.Helper()
	op, req := f.claim(t, text)
	_, err := f.d.composerSendFrom(f.d.machineID(), op, req, func() error { return f.d.core.BeginComposerOperation(op) }, adapter.SourceOwner)
	return op, err
}

func (f *nativeA2Fixture) emitTurn(t *testing.T) shimwire.ClaudeMessageRequest {
	t.Helper()
	f.mu.Lock()
	if len(f.writes) != 1 {
		f.mu.Unlock()
		t.Fatal("owner was not written exactly once")
	}
	req := f.writes[0]
	f.mu.Unlock()
	// No transcript existed when first input crossed the mock peer: that first
	// write must not deadlock waiting for the history file it itself creates.
	if _, err := os.Stat(f.history); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("first write incorrectly required/precreated native history", err)
	}
	echoUUID, text := req.NativeUserUUID, req.Text
	if f.mode == "wrong-echo" {
		echoUUID = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	}
	content := []any{map[string]any{"type": "text", "text": "native first"}, map[string]any{"type": "text", "text": "native final"}}
	if f.mode == "human" {
		content = []any{map[string]any{"type": "tool_use", "id": "question-tool", "name": "AskUserQuestion", "input": nativeA2QuestionInput()}}
	}
	for _, value := range []any{
		map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running", "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000004"},
		map[string]any{"type": "command_lifecycle", "command_uuid": req.NativeUserUUID, "state": "started", "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000005"},
		map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed"}, "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000006"},
		map[string]any{"type": "system", "subtype": "init", "cwd": f.meta.Cwd, "session_id": nativeA2Session, "uuid": "139a9413-96aa-4000-8000-000000000007", "claude_code_version": "2.1.310", "model": "native-model", "tools": []any{"Read"}, "mcp_servers": []any{}},
		map[string]any{"type": "user", "uuid": echoUUID, "session_id": nativeA2Session, "parent_tool_use_id": nil, "isReplay": true, "message": map[string]any{"role": "user", "content": text}},
		map[string]any{"type": "assistant", "uuid": nativeA2Assistant, "session_id": nativeA2Session, "parent_tool_use_id": nil, "user_message_uuid": req.NativeUserUUID, "message": map[string]any{"id": "msg_native_independent_id", "type": "message", "role": "assistant", "content": content, "stop_reason": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}},
	} {
		f.append(t, value)
	}
	if f.mode == "human" {
		f.writeHistory(t, req, content)
		f.append(t, map[string]any{"type": "system", "subtype": "session_state_changed", "state": "requires_action", "session_id": nativeA2Session})
		f.append(t, map[string]any{"type": "control_request", "request_id": "native-question", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "AskUserQuestion", "display_name": "AskUserQuestion", "tool_use_id": "question-tool", "requires_user_interaction": true, "input": nativeA2QuestionInput()}})
		return req
	}
	owner, isError, terminal := req.NativeUserUUID, false, "completed"
	if f.mode == "unrelated-result" {
		owner = "3389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	}
	if f.mode == "quota-error" {
		isError = true
		terminal = "rate_limit"
	}
	if f.mode == "duplicate-echo" {
		f.append(t, map[string]any{"type": "user", "uuid": req.NativeUserUUID, "session_id": nativeA2Session, "parent_tool_use_id": nil, "isReplay": true, "message": map[string]any{"role": "user", "content": text}})
	}
	f.append(t, map[string]any{"type": "result", "subtype": "success", "uuid": nativeA2Result, "session_id": nativeA2Session, "is_error": isError, "terminal_reason": terminal, "stop_reason": "end_turn", "user_message_uuid": owner, "user_message_uuids": []string{owner}, "result_index": 0, "result": "native final", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	if f.mode == "unflushed-history" {
		return req
	}
	f.writeHistory(t, req, content)
	return req
}

func (f *nativeA2Fixture) writeHistory(t *testing.T, req shimwire.ClaudeMessageRequest, content []any) {
	t.Helper()
	rows := []any{
		map[string]any{"type": "user", "uuid": req.NativeUserUUID, "parentUuid": nil, "sessionId": nativeA2Session, "isSidechain": false, "message": map[string]any{"role": "user", "content": req.Text}},
		map[string]any{"type": "attachment", "uuid": "439a9413-96aa-4000-8000-000000000001", "parentUuid": req.NativeUserUUID, "sessionId": nativeA2Session, "attachment": map[string]any{"type": "fixture"}},
		map[string]any{"type": "assistant", "uuid": nativeA2Assistant, "parentUuid": "439a9413-96aa-4000-8000-000000000001", "sessionId": nativeA2Session, "isSidechain": false, "message": map[string]any{"id": "msg_native_independent_id", "role": "assistant", "content": content}},
	}
	if err := os.MkdirAll(filepath.Dir(f.history), 0700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, row := range rows {
		raw, _ := json.Marshal(row)
		body = append(body, raw...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(f.history, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func nativeA2Receipt(t *testing.T, f *nativeA2Fixture, op string) protocol.SemanticMessageReceipt {
	t.Helper()
	if _, ok := f.d.core.ComposerOperation(op); !ok {
		t.Fatal("native operation disappeared")
	}
	r, err := f.d.semanticOperation(f.meta.ID, op)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Reopen only the independently retained log, never the daemon's in-memory
// Get cache or a second writable idempotency.Store on the live namespace.
func nativeA2RetainedRecord(root, operation string) (idempotency.Record, error) {
	var found idempotency.Record
	f, err := os.Open(filepath.Join(root, "idempotency", "idempotency.log"))
	if err != nil {
		return found, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() > 8<<20 {
		return found, fmt.Errorf("retained semantic log unavailable/over budget: %v", err)
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	count := 0
	for scanner.Scan() {
		count++
		if count > 4096 {
			return found, errors.New("retained semantic record budget")
		}
		var rec idempotency.Record
		if err = json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return found, err
		}
		if rec.OperationID == operation {
			found = rec
		}
	}
	if err = scanner.Err(); err != nil {
		return found, err
	}
	if found.OperationID == "" || len(found.Request) == 0 {
		return found, errors.New("native intent not independently retained")
	}
	return found, nil
}

func nativeA2DiskIntent(f *nativeA2Fixture, operation string, req protocol.ComposerSendReq, revision uint64) error {
	execution, err := f.d.executionStore.LoadExecution(f.meta.ID)
	if err != nil {
		return err
	}
	if execution.OwnerRevision != revision || execution.Incarnation != executionIncarnation(f.meta) || execution.ConversationID != nativeA2Session || !execution.UnknownActors {
		return errors.New("native execution file did not retain exact newer owner revision")
	}
	rec, err := nativeA2RetainedRecord(f.d.stateDir, operation)
	if err != nil {
		return err
	}
	var intent struct {
		Kind            string                         `json:"kind"`
		Identity        shimwire.ClaudePrimaryIdentity `json:"identity"`
		SessionInstance string                         `json:"session_instance"`
		NativeUserUUID  string                         `json:"native_user_uuid"`
		OwnerRevision   uint64                         `json:"owner_revision"`
		TextSHA256      string                         `json:"text_sha256"`
		TextBytes       int                            `json:"text_bytes"`
	}
	hash := sha256.Sum256([]byte(req.Text))
	if json.Unmarshal(rec.Request, &intent) != nil || rec.OperationID != operation || rec.Action != "semantic_send" || rec.SessionID != f.meta.ID || rec.SessionInstance != req.SessionInstance || rec.RequestHash != hex.EncodeToString(schema.ComposerSendContentHash(&req)) || intent.Kind == "" || intent.Kind == "claude_fixture_message" || intent.Identity != f.identity || intent.SessionInstance != req.SessionInstance || !adapter.IsCanonicalConversationID(intent.NativeUserUUID) || intent.OwnerRevision != revision || intent.TextSHA256 != hex.EncodeToString(hash[:]) || intent.TextBytes != len(req.Text) {
		return errors.New("independently reopened semantic intent did not bind newer revision/instance/UUID/text")
	}
	return nil
}

func TestClaudeNativeChatConsumerConnectedEchoResultAndActualHistory(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	op, err := f.send(t, "owner Unicode 🐝\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	req := f.emitTurn(t)
	if err = f.drain(t); err != nil {
		t.Fatal(err)
	}
	receipt := nativeA2Receipt(t, f, op)
	if receipt.State != "completed" || receipt.Agent != "claude" || receipt.NativeSessionID != nativeA2Session || receipt.NativeMessageID != req.NativeUserUUID {
		t.Fatalf("native correlated result did not settle MAIN operation: %+v", receipt)
	}
	page, _, err := f.d.nativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), Limit: 200})
	if err != nil || page.Version != 2 || page.Scope == "" || len(page.Items) < 2 {
		t.Fatal("real native history unavailable", page, err)
	}
	raw, _ := json.Marshal(page.Items)
	if !strings.Contains(string(raw), req.NativeUserUUID) || !strings.Contains(string(raw), nativeA2Assistant) || strings.Contains(string(raw), `"type":"result"`) {
		t.Fatal("native history lost UUIDs or invented result row")
	}
	execution, e := f.d.eng.ExecutionSnapshot(f.meta.ID)
	if e != nil || !execution.UnknownActors || execution.OwnerRevision != req.OwnerRevision {
		t.Fatal("message invented complete actor custody or lost intent", execution, e)
	}
	if !f.d.executionCustodyUnsafe(f.meta.ID) {
		t.Fatal("native main result granted automatic custody")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 || f.unexpected != 0 {
		t.Fatal("native message replayed or sent decision/control")
	}
}

func TestClaudeNativeChatConsumerWrongInitializeAndBlockedStatesNeverWrite(t *testing.T) {
	for _, kind := range []string{"wrong-id", "wrong-pid", "wrong-version", "system-only", "human", "running", "unknown-state", "alias-response", "duplicate-response"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", kind)
			if f.registrationErr != nil {
				return
			} // Earlier exact-binding refusal is safe.
			_ = f.drain(t)
			_, err := f.send(t, "valid owner intent blocked before provider write")
			if err == nil {
				t.Fatal("unsupported handshake/state admitted input")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 || f.unexpected != 0 {
				t.Fatal("blocked initialize wrote native input/control")
			}
		})
	}
}

func TestClaudeNativeChatConsumerUnrelatedErrorAndDuplicateNeverBecomeSuccess(t *testing.T) {
	for _, kind := range []string{"wrong-echo", "duplicate-echo", "unrelated-result", "quota-error"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeA2Fixture(t, kind, "idle")
			if err := f.drain(t); err != nil {
				t.Fatal(err)
			}
			op, err := f.send(t, "one owner result must be correlated")
			if err != nil {
				t.Fatal(err)
			}
			f.emitTurn(t)
			_ = f.drain(t)
			receipt := nativeA2Receipt(t, f, op)
			if receipt.State == "completed" {
				t.Fatalf("%s became successful completion: %+v", kind, receipt)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 1 || f.unexpected != 0 {
				t.Fatal("invalid output caused native resend/control")
			}
		})
	}
}

func TestClaudeNativeChatConsumerResultDoesNotInventUnflushedHistory(t *testing.T) {
	f := newNativeA2Fixture(t, "unflushed-history", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	op, err := f.send(t, "first input creates its own native history")
	if err != nil {
		t.Fatal(err)
	}
	f.emitTurn(t)
	if err = f.drain(t); err != nil {
		t.Fatal("correlated native result must fold independently of history flush lag", err)
	}
	if r := nativeA2Receipt(t, f, op); r.State != "completed" {
		t.Fatal("history flush lag erased correlated MAIN result", r)
	}
	page, _, err := f.d.nativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), Limit: 200})
	if err == nil || len(page.Items) != 0 {
		t.Fatal("post-intent missing history regained pristine empty authority or fabricated rows", page, err)
	}
}

func TestClaudeNativeChatConsumerHistoryRefusesForeignSessionAndLeafAlias(t *testing.T) {
	for _, kind := range []string{"foreign-session", "leaf-symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", "idle")
			if err := f.drain(t); err != nil {
				t.Fatal(err)
			}
			if _, err := f.send(t, "actual native history only"); err != nil {
				t.Fatal(err)
			}
			f.emitTurn(t)
			if err := f.drain(t); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(f.history)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "foreign-session" {
				raw = []byte(strings.ReplaceAll(string(raw), nativeA2Session, "2f700d8f-d3e3-42c1-9ab0-dcdb22f27772"))
				err = os.WriteFile(f.history, raw, 0600)
			} else {
				foreign := filepath.Join(t.TempDir(), "same-native-bytes.jsonl")
				if err = os.WriteFile(foreign, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(f.history); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(foreign, f.history)
			}
			if err != nil {
				t.Fatal(err)
			}
			page, _, err := f.d.nativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), Limit: 200})
			if err == nil || len(page.Items) != 0 {
				t.Fatalf("unsafe history accepted: %s %+v %v", kind, page, err)
			}
		})
	}
}

func TestClaudeNativeChatConsumerQuestionRemainsVisibleWithoutAnswer(t *testing.T) {
	f := newNativeA2Fixture(t, "human", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	op, err := f.send(t, "Ask the baseline fixture question, then wait for its authorized answer.")
	if err != nil {
		t.Fatal(err)
	}
	req := f.emitTurn(t)
	if err = f.drain(t); err != nil {
		t.Fatal(err)
	}
	receipt := nativeA2Receipt(t, f, op)
	if receipt.State == "completed" || !strings.Contains(receipt.Reason, "AskUserQuestion") {
		t.Fatalf("connected receipt lost actual pending question reason: %+v", receipt)
	}
	page, _, err := f.d.nativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), Limit: 200})
	if err != nil || page.Version != 2 {
		t.Fatal("human wait hid actual native history", err)
	}
	raw, _ := json.Marshal(page.Items)
	for _, want := range []string{req.NativeUserUUID, "question-tool", "AskUserQuestion", nativeA2Question, "Alpha", "Beta"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("connected history lost pending question field %q", want)
		}
	}
	current, ok := f.d.core.Get(f.meta.ID)
	if !ok || (current.Status.Interaction != status.InteractionPrompt && current.Status.Interaction != status.InteractionPermission) || status.Derive(current.Status) != status.GroupNeedsInput {
		t.Fatalf("connected status did not surface pending native question: %+v", current.Status)
	}
	if _, err = f.send(t, "must remain blocked, not an implicit answer"); err == nil {
		t.Fatal("unanswered native request allowed another owner input")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 || f.answers != 0 || f.unexpected != 0 {
		t.Fatal("consumer answered/replayed while the real-shaped question remained pending")
	}
}

func TestClaudeNativeChatConsumerBusyIntentPersistsBeforeLaneAndDoesNotDeliver(t *testing.T) {
	f := newNativeA2Fixture(t, "human", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	first, err := f.send(t, "first native owner")
	if err != nil {
		t.Fatal(err)
	}
	f.emitTurn(t)
	if err = f.drain(t); err != nil {
		t.Fatal(err)
	}
	old := nativeA2Receipt(t, f, first)
	if old.State == "completed" {
		t.Fatal("human wait settled as completed")
	}
	before, e := f.d.executionStore.LoadExecution(f.meta.ID)
	if e != nil {
		t.Fatal(e)
	}
	op, req := f.claim(t, "new explicit owner while lane held and human blocked")
	lane := f.d.composerLaneFor(f.meta.ID)
	lane.enter()
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := f.entry.sendClaudeNativeChat(f.meta.ID, op, req, func() error { return f.d.core.BeginComposerOperation(op) }, adapter.SourceOwner)
		done <- err
	}()
	defer func() {
		lane.leave()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("native send did not join after held lane released")
		}
	}()
	// Revision must move while the actual FIFO is held, not after delivery readiness.
	deadline := time.Now().Add(time.Second)
	advanced := false
	var diskErr error
	for time.Now().Before(deadline) {
		diskErr = nativeA2DiskIntent(f, op, req, before.OwnerRevision+1)
		if diskErr == nil {
			advanced = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !advanced {
		t.Fatal("valid queued intent did not reach actual execution file and independently reopened semantic log while FIFO held", diskErr)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("human-busy second message was delivered")
		}
		if err = nativeA2DiskIntent(f, op, req, before.OwnerRevision+1); err != nil {
			t.Fatal("busy refusal lost independently retained native intent before FIFO release", err)
		}
	case <-time.After(time.Second):
		t.Fatal("known human busy refusal waited on FIFO despite durable intent")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 || f.delivered != before.OwnerRevision || f.answers != 0 || f.unexpected != 0 {
		t.Fatal("busy intent changed delivered revision or answered human")
	}
}

func TestClaudeNativeChatConsumerMixedHookCannotInitializeOrSettle(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	s := f.d.claudeNativeState(f.meta.ID)
	s.mu.Lock()
	primary := s.identity
	scope := s.nativeScope
	sequence := s.sequence
	s.mu.Unlock()
	primary.PrimaryPID = 0
	primary.PrimaryStartTime = 0
	scope.PID = 0
	scope.PGID = 0
	scope.StartTime = 0
	// A callback that impersonates a result remains opaque hook evidence.
	raw := []byte(`{"type":"result","subtype":"success","session_id":"` + nativeA2Session + `","result":"forged hook"}`)
	body, _ := json.Marshal(map[string]any{"v": 1, "origin": "claude_hook", "primary": primary, "scope": scope, "raw": raw})
	if _, err := f.spool.Append(body); err != nil {
		t.Fatal(err)
	}
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	unchanged := s.sequence == sequence && s.folded == sequence && s.operation == ""
	s.mu.Unlock()
	if !unchanged {
		t.Fatal("opaque callback gained primary authority")
	}
	page, _, err := f.d.nativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), Limit: 200})
	if err != nil || page.Version != 2 || len(page.Items) != 0 {
		t.Fatal("pristine native missing history did not remain empty", page, err)
	}
	scope.Generation = strings.Repeat("d", 32)
	body, _ = json.Marshal(map[string]any{"v": 1, "origin": "claude_hook", "primary": primary, "scope": scope, "raw": raw})
	if _, err = f.spool.Append(body); err != nil {
		t.Fatal(err)
	}
	if err = f.drain(t); err == nil {
		t.Fatal("foreign preclaim generation accepted")
	}
}

func TestClaudeNativeChatConsumerSnapshotRefusalStillRetainsValidIntent(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	op, req := f.claim(t, "retain valid intent before unavailable snapshot")
	f.mu.Lock()
	f.snapshotUnavailable = true
	f.mu.Unlock()
	if _, err := f.entry.sendClaudeNativeChat(f.meta.ID, op, req, func() error { return f.d.core.BeginComposerOperation(op) }, adapter.SourceOwner); err == nil {
		t.Fatal("missing snapshot admitted input")
	}
	if err := nativeA2DiskIntent(f, op, req, 1); err != nil {
		t.Fatal("snapshot refusal lost durable intent", err)
	}
}
func TestClaudeNativeChatConsumerRejectsAliasedAndDuplicateAuthoritativeJSON(t *testing.T) {
	for _, kind := range []string{"echo-alias", "echo-duplicate-content", "result-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", "idle")
			if err := f.drain(t); err != nil {
				t.Fatal(err)
			}
			op, err := f.send(t, "exact JSON owner")
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			req := f.writes[0]
			f.mu.Unlock()
			text, _ := json.Marshal(req.Text)
			echo := fmt.Sprintf(`{"type":"user","uuid":%q,"session_id":%q,"parent_tool_use_id":null,"isReplay":true,"message":{"role":"user","content":%s}}`, req.NativeUserUUID, nativeA2Session, text)
			switch kind {
			case "echo-alias":
				echo = fmt.Sprintf(`{"type":"user","uuid":"2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a","UUID":%q,"session_id":%q,"parent_tool_use_id":null,"isReplay":true,"message":{"role":"user","content":%s}}`, req.NativeUserUUID, nativeA2Session, text)
			case "echo-duplicate-content":
				echo = fmt.Sprintf(`{"type":"user","uuid":%q,"session_id":%q,"parent_tool_use_id":null,"isReplay":true,"message":{"role":"user","content":"foreign text","content":%s}}`, req.NativeUserUUID, nativeA2Session, text)
			}
			f.append(t, json.RawMessage(echo))
			if kind == "result-alias" {
				f.append(t, json.RawMessage(fmt.Sprintf(`{"type":"result","subtype":"success","uuid":%q,"session_id":%q,"is_error":false,"user_message_uuid":"3389ef09-4c19-4d50-8fdd-1fc95bdcfd4a","USER_MESSAGE_UUID":%q,"result":"forged completion"}`, nativeA2Result, nativeA2Session, req.NativeUserUUID)))
			}
			if err = f.drain(t); err == nil {
				t.Fatal("aliased/duplicate authority accepted")
			}
			r := nativeA2Receipt(t, f, op)
			if r.State == "completed" || kind != "result-alias" && r.State == "admitted" {
				t.Fatal("ambiguous JSON gained owner authority", r)
			}
		})
	}
}
func TestClaudeNativeChatConsumerPreparedRefusalKeepsPriorHistory(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	if _, err := f.send(t, "first native owner"); err != nil {
		t.Fatal(err)
	}
	f.emitTurn(t)
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.snapshotUnavailable = true
	f.mu.Unlock()
	op, req := f.claim(t, "prepared but never delivered owner")
	if _, err := f.entry.sendClaudeNativeChat(f.meta.ID, op, req, func() error { return f.d.core.BeginComposerOperation(op) }, adapter.SourceOwner); err == nil {
		t.Fatal("unavailable snapshot allowed input")
	}
	f.mu.Lock()
	f.snapshotUnavailable = false
	f.mu.Unlock()
	page, _, err := f.d.nativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: protocol.NamespacedID(f.d.machineID(), f.meta.ID), Limit: 200})
	if err != nil || len(page.Items) < 2 {
		t.Fatal("prepared refusal hid actual previous history", page, err)
	}
}
func TestClaudeNativeChatConsumerOrdinaryToolResultAllowsNextOwner(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	op, err := f.send(t, "read then finish")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	req := f.writes[0]
	f.mu.Unlock()
	f.append(t, map[string]any{"type": "user", "uuid": req.NativeUserUUID, "session_id": nativeA2Session, "parent_tool_use_id": nil, "isReplay": true, "message": map[string]any{"role": "user", "content": req.Text}})
	content := []any{map[string]any{"type": "tool_use", "id": "read-tool", "name": "Read", "input": map[string]any{"file_path": "/synthetic"}}, map[string]any{"type": "text", "text": "read completed"}}
	f.append(t, map[string]any{"type": "assistant", "uuid": nativeA2Assistant, "session_id": nativeA2Session, "parent_tool_use_id": nil, "message": map[string]any{"id": "msg_read", "role": "assistant", "content": content}})
	f.append(t, map[string]any{"type": "result", "subtype": "success", "uuid": nativeA2Result, "session_id": nativeA2Session, "is_error": false, "user_message_uuid": req.NativeUserUUID, "result": "read completed"})
	f.writeHistory(t, req, content)
	if err = f.drain(t); err != nil {
		t.Fatal(err)
	}
	if r := nativeA2Receipt(t, f, op); r.State != "completed" {
		t.Fatal(r)
	}
	if _, err = f.send(t, "next explicit owner"); err != nil {
		t.Fatal("ordinary tool permanently blocked owner input", err)
	}
}

func TestClaudeNativeChatConsumerHistoryAndScopeRejectCaseAliases(t *testing.T) {
	for _, kind := range []string{"history", "scope"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", "idle")
			if err := f.drain(t); err != nil {
				t.Fatal(err)
			}
			if kind == "scope" {
				raw, _ := json.Marshal(f.d.claudeNativeState(f.meta.ID).nativeScope)
				raw = []byte(strings.Replace(string(raw), `"cwd":`, `"cwd":"/foreign","CWD":`, 1))
				if err := os.WriteFile(filepath.Join(f.d.stateDir, f.meta.ID, logins.NativeProcessFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := f.d.readClaudeNativeScope(f.meta, f.identity); err == nil {
					t.Fatal("case alias overrode canonical foreign native scope")
				}
				return
			}
			if _, err := f.send(t, "read exact native history"); err != nil {
				t.Fatal(err)
			}
			f.emitTurn(t)
			if err := f.drain(t); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(f.history)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.ReplaceAll(string(raw), `"sessionId":"`+nativeA2Session+`"`, `"sessionId":"2f700d8f-d3e3-42c1-9ab0-dcdb22f27772","SESSIONID":"`+nativeA2Session+`"`))
			if err = os.WriteFile(f.history, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = f.d.readClaudeNativeHistory(f.d.claudeNativeState(f.meta.ID)); err == nil {
				t.Fatal("history case alias replaced foreign session")
			}
		})
	}
}

func TestClaudeNativeChatConsumerCompletedDrainBeforeSendReturnsDoesNotResurrectBusy(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	arrived, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	hook := func(local string) {
		if local == f.meta.ID {
			close(arrived)
			<-release
		}
	}
	testHookClaudeNativeWritten.Store(&hook)
	defer testHookClaudeNativeWritten.Store(nil)
	defer unblock()
	op, req := f.claim(t, "finish while sender is returning")
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := f.entry.sendClaudeNativeChat(f.meta.ID, op, req, func() error { return f.d.core.BeginComposerOperation(op) }, adapter.SourceOwner)
		done <- err
	}()
	defer func() {
		unblock()
		select {
		case <-joined:
		case <-time.After(2 * time.Second):
			t.Error("sender did not join")
		}
	}()
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("sender never released write custody")
	}
	f.emitTurn(t)
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	if r := nativeA2Receipt(t, f, op); r.State != "completed" {
		t.Fatal("drain did not settle MAIN before sender resumes", r)
	}
	testHookClaudeNativeWritten.Store(nil)
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not resume")
	}
	if _, err := f.send(t, "next explicit owner after completed result"); err != nil {
		t.Fatal("late sender resurrected busy after completed result", err)
	}
}

func TestClaudeNativeChatConsumerPublicCallerAcceptsNamespacedOwner(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	client := dialClient(t, f.d, protocol.CapSemanticSend, protocol.CapJournal)
	op, req := f.claim(t, "actual namespaced owner caller")
	if err := client.SemanticSend(op, req); err != nil {
		t.Fatal("owner protocol caller refused valid native message", err)
	}
	if err := nativeA2DiskIntent(f, op, req, 1); err != nil {
		t.Fatal(err)
	}
	written := f.emitTurn(t)
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	receipt, err := client.SemanticOperation(req.Session, op)
	if err != nil || receipt.State != "completed" || receipt.Agent != "claude" || receipt.NativeMessageID != written.NativeUserUUID || receipt.NativeSessionID != nativeA2Session {
		t.Fatal("protocol receipt lost exact native MAIN completion", receipt, err)
	}
	page, err := client.NativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: req.Session, Limit: 200})
	if err != nil || page.Version != 2 || page.Scope == "" || len(page.Items) < 2 {
		t.Fatal("protocol caller lost actual native history", page, err)
	}
	raw, _ := json.Marshal(page.Items)
	if !strings.Contains(string(raw), written.NativeUserUUID) || !strings.Contains(string(raw), nativeA2Assistant) || strings.Contains(string(raw), `"type":"result"`) {
		t.Fatal("protocol native history lost identity or fabricated result")
	}
	execution, err := f.d.eng.ExecutionSnapshot(f.meta.ID)
	if err != nil || !execution.UnknownActors || execution.OwnerRevision != 1 || !f.d.executionCustodyUnsafe(f.meta.ID) {
		t.Fatal("protocol message granted automatic actor custody", execution, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 || f.writes[0].Text != req.Text || f.answers != 0 || f.unexpected != 0 {
		t.Fatal("public native caller did not write exact owner once")
	}
}

func TestClaudeNativeChatConsumerRegistrationRequiresRetainedStopBeforeInput(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "stop-unavailable")
	if f.registrationErr == nil || f.d.claudeNativeState(f.meta.ID) != nil {
		t.Fatal("native consumer published without retained Stop")
	}
	op, req := f.claim(t, "cannot write without independently retained Stop")
	if _, err := f.d.composerSendFrom(f.d.machineID(), op, req, func() error { return f.d.core.BeginComposerOperation(op) }, adapter.SourceOwner); err == nil {
		t.Fatal("unbound Stop admitted native owner")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 || f.stops != 0 || f.answers != 0 || f.unexpected != 0 {
		t.Fatal("failed Stop registration caused provider effects")
	}
}

func TestClaudeNativeChatConsumerRefusalDiagnosticsDoNotExposeFrames(t *testing.T) {
	for _, boundary := range []string{"snapshot", "decode", "initialize"} {
		t.Run(boundary, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", "wrong-id")
			const secret = "DO_NOT_EXPOSE_NATIVE_FIELD"
			frame := map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "native-initialize", "response": map[string]any{"pid": f.identity.PrimaryPID, "claude_code_version": "2.1.310", "session_state": "idle"}}}
			s := &claudeNativeState{claudeFixtureState: claudeFixtureState{meta: f.meta, identity: f.identity, healthy: true, sequence: 2}}
			expected := "native " + boundary + " refused"
			switch boundary {
			case "snapshot":
				s.healthy = false
			case "decode":
				frame["message"] = secret // A known but unused field still participates in Go decoding.
				expected = "native frame decode refused"
			case "initialize":
				frame["response"].(map[string]any)["request_id"] = secret
			}
			raw, err := json.Marshal(frame)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"v": 1, "origin": "claude_primary", "native_session_id": nativeA2Session, "primary": f.identity, "read_monotonic_ns": 1, "stream_sequence": 3, "raw": raw})
			if err != nil {
				t.Fatal(err)
			}
			err = f.d.consumeClaudeNative(s, body)
			if !errors.Is(err, errClaudeFixtureMessage) || !strings.Contains(err.Error(), expected) {
				t.Fatal("refusal lost its error identity or boundary")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "native-initialize") || strings.Contains(err.Error(), nativeA2Session) {
				t.Fatal("diagnostic exposed native frame values")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 || f.answers != 0 || f.unexpected != 0 {
				t.Fatal("diagnostic refusal acquired provider effects")
			}
		})
	}
}
