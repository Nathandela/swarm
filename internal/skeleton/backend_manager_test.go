package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func codexProofFixture() managerproof.Binding {
	return managerproof.Binding{Session: "manager", SessionInstance: "instance", ConversationID: "conversation", Provider: "codex", NativeSessionID: "01a1bc58-3571-7452-bd5c-348fe1b9d7ff", NativeGeneration: "actual-process-generation", ObservationGeneration: "observation-generation", LoginEpoch: "account"}
}
func TestCodexManagerNativeCallProofAndExternalSteering(t *testing.T) {
	b := codexProofFixture()
	s, err := newCodexManagerState(b)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	intent := codexManagerIntent{Binding: b, Nonce: nonce, TextHash: codexManagerTextHash("exact input"), OperationID: "operation"}
	if err := s.prepare(intent); err != nil {
		t.Fatal(err)
	}
	frames := []string{
		`{"method":"turn/started","params":{"threadId":"01a1bc58-3571-7452-bd5c-348fe1b9d7ff","turn":{"id":"native-turn","rootTurnId":"native-turn","status":"inProgress"}}}`,
		`{"method":"item/started","params":{"threadId":"01a1bc58-3571-7452-bd5c-348fe1b9d7ff","turnId":"native-turn","item":{"type":"userMessage","id":"native-message","clientId":"` + nonce + `","content":[{"type":"text","text":"exact input"}]}}}`,
		`{"method":"item/started","params":{"threadId":"01a1bc58-3571-7452-bd5c-348fe1b9d7ff","turnId":"native-turn","item":{"type":"mcpToolCall","id":"native-call","server":"swarm_voice","tool":"publish","arguments":{"text":"hello","turn_id":"MODEL_SPOOF","large":9007199254740993}}}}`,
	}
	for _, raw := range frames {
		if err := s.observe([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	hash, _ := managerproof.ArgumentsHash([]byte(`{"large":9007199254740993,"turn_id":"MODEL_SPOOF","text":"hello"}`))
	request := managerproof.CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"callId":"native-call","itemId":"wrong-responses-item","threadId":"` + b.NativeSessionID + `","sessionId":"` + b.NativeSessionID + `","x-codex-turn-metadata":{"thread_id":"` + b.NativeSessionID + `","session_id":"` + b.NativeSessionID + `","turn_id":"native-turn"}}`)}
	proof, err := s.registry.Resolve(context.Background(), request)
	if err != nil || proof.NativeInputID != nonce || proof.NativeCallID != "native-call" {
		t.Fatalf("exact proof: %+v %v", proof, err)
	}
	manual := `{"method":"item/started","params":{"threadId":"` + b.NativeSessionID + `","turnId":"native-turn","item":{"type":"userMessage","id":"manual","clientId":null,"content":[{"type":"text","text":"manual direction"}]}}}`
	if err := s.observe([]byte(manual)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.registry.Resolve(context.Background(), request); !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("manual steer inherited proof: %v", err)
	}
}

func codexManagerFrame(method, thread string, fields map[string]any) []byte {
	fields["threadId"] = thread
	raw, _ := json.Marshal(map[string]any{"method": method, "params": fields})
	return raw
}
func codexManagerTurnFrame(method, thread, turn, state string) []byte {
	return codexManagerFrame(method, thread, map[string]any{"turn": map[string]any{"id": turn, "rootTurnId": turn, "status": state}})
}
func codexManagerUserFrame(thread, turn, id, client, text string) []byte {
	return codexManagerFrame("item/started", thread, map[string]any{"turnId": turn, "item": map[string]any{"type": "userMessage", "id": id, "clientId": client, "content": []map[string]any{{"type": "text", "text": text}}}})
}

func TestCodexManagerReceiptDoesNotReorderNativeLifecycle(t *testing.T) {
	b := codexProofFixture()
	s, _ := newCodexManagerState(b)
	nonce := strings.Repeat("b", 64)
	if s.registry.ReadyForInput() {
		t.Fatal("missing native status became idle")
	}
	if err := s.prepare(codexManagerIntent{Binding: b, Nonce: nonce, OperationID: "operation", TextHash: codexManagerTextHash("input")}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "own", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "own", "message", nonce, "input"), codexManagerTurnFrame("turn/completed", b.NativeSessionID, "own", "completed"), codexManagerTurnFrame("turn/started", b.NativeSessionID, "manual", "inProgress")} {
		if err := s.observe(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.recordReceipt(nonce, "own"); err != nil {
		t.Fatal(err)
	}
	status, err := s.registry.Snapshot()
	if err != nil || status.Turn.NativeTurnID != "manual" || status.Turn.NativeInputID != "" || s.registry.ReadyForInput() {
		t.Fatalf("late ACK resurrected own turn: %+v %v", status, err)
	}
	if got := s.receipts()[0]; got.Outcome != "completed" || got.Receipt.Status != "admitted" {
		t.Fatalf("late ACK demoted echo/completion: %+v", got)
	}
}

func TestCodexManagerCompletedCallAndForeignThreadFailClosed(t *testing.T) {
	b := codexProofFixture()
	s, _ := newCodexManagerState(b)
	nonce := strings.Repeat("c", 64)
	_ = s.prepare(codexManagerIntent{Binding: b, Nonce: nonce, OperationID: "op", TextHash: codexManagerTextHash("input")})
	for _, frame := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "own", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "own", "message", nonce, "input")} {
		if err := s.observe(frame); err != nil {
			t.Fatal(err)
		}
	}
	args := json.RawMessage(`{"text":"hello","number":9007199254740993}`)
	hash, _ := managerproof.ArgumentsHash(args)
	tool := func(method, thread string) []byte {
		return codexManagerFrame(method, thread, map[string]any{"turnId": "own", "item": map[string]any{"type": "mcpToolCall", "id": "call", "server": "swarm_voice", "tool": "publish", "arguments": args}})
	}
	meta := json.RawMessage(fmt.Sprintf(`{"callId":"call","threadId":%q,"sessionId":%q,"x-codex-turn-metadata":{"thread_id":%q,"session_id":%q,"turn_id":"own"}}`, b.NativeSessionID, b.NativeSessionID, b.NativeSessionID, b.NativeSessionID))
	req := managerproof.CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: meta}
	if err := s.observe(tool("item/started", "foreign")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := s.registry.Resolve(ctx, req); err == nil {
		t.Fatal("foreign call borrowed main thread")
	}
	for _, method := range []string{"item/completed", "item/started"} {
		if err := s.observe(tool(method, b.NativeSessionID)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.registry.Resolve(context.Background(), req); !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("completed call reopened: %v", err)
	}
}

type codexManagerTestConn struct {
	call   func(context.Context, string, any, any) error
	before func()
}

func (c *codexManagerTestConn) Call(ctx context.Context, m string, p, out any) error {
	return c.call(ctx, m, p, out)
}
func (c *codexManagerTestConn) Respond(context.Context, json.RawMessage, any) error { return nil }
func (c *codexManagerTestConn) Close() error                                        { return nil }
func (c *codexManagerTestConn) CallAtWriteBoundary(ctx context.Context, m string, p, out any, before func() error, after func()) error {
	if c.before != nil {
		c.before()
	}
	if err := before(); err != nil {
		return err
	}
	after()
	return c.call(ctx, m, p, out)
}

func newCodexManagerSendRig(t *testing.T) (*Daemon, managerproof.Binding, *backendFeed) {
	t.Helper()
	buildBinaries(t)
	dir, err := os.MkdirTemp("", "swcm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"), ShimBinary: swarmBin, MaxSessions: 4}
	core, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "agent.txt")
	if err := os.WriteFile(script, []byte("idle 600s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"fixture-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	identity := ManagerNativeIdentityForEnv("codex", []string{"HOME=" + home})
	if identity == "" {
		t.Fatal("fixture account missing")
	}
	meta, err := core.Launch(daemon.LaunchSpec{AgentType: "codex", Argv: []string{fakeAgentBin, script}, Cwd: home, ClientEnv: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, CLIIdentity: &persist.CLIIdentity{Path: fakeAgentBin, Version: "0.167.0"}, AuthIdentity: identity, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(meta.ShimPID, syscall.SIGTERM) })
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	meta = managerLifecycleMeta(meta)
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(meta); err != nil {
		t.Fatal(err)
	}
	core, err = daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	pid := os.Getpid()
	start, err := procstart.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	native := shim.NativeProcessInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Generation: strings.Repeat("d", 32), Provider: "codex", ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime, PID: pid, StartTime: start, BackendPID: pid, BackendStartTime: start}
	for name, value := range map[string]any{shim.NativeProcessFile: native, shim.BackendFile: shim.BackendInfo{PID: pid, StartedAtMS: start}} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, meta.ID, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sk := &Daemon{core: core, stateDir: dir}
	if err := sk.recordSessionInstance(meta.ID, "instance", meta.ShimPID, meta.ShimStartTime); err != nil {
		t.Fatal(err)
	}
	b := codexProofFixture()
	b.Session = meta.ID
	b.LoginEpoch = "identity:" + identity
	b.ConversationID = meta.ManagerConversationID
	b.NativeGeneration = native.Generation
	feed := newBackendFeed()
	b.ObservationGeneration = feed.epoch
	sk.backend.live = map[string]*sessionBackend{meta.ID: {threadID: b.NativeSessionID, sessionInstance: b.SessionInstance, feed: feed}}
	sk.captureCodexManagerFrame(meta.ID, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
	return sk, b, feed
}

func TestCodexManagerLostACKReconcilesEchoWithoutRedrive(t *testing.T) {
	sk, b, feed := newCodexManagerSendRig(t)
	nonce := strings.Repeat("e", 64)
	writes := 0
	conn := &codexManagerTestConn{call: func(_ context.Context, method string, params, out any) error {
		writes++
		p := params.(map[string]any)
		if method != "turn/start" || p["threadId"] != b.NativeSessionID || p["clientUserMessageId"] != nonce {
			t.Fatalf("wrong native operation: %s %+v", method, p)
		}
		for _, frame := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "native-turn", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "native-turn", "native-message", nonce, "exact input")} {
			var m struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(frame, &m)
			sk.captureCodexManagerFrame(b.Session, feed, m.Method, frame)
		}
		return errors.New("fixture lost ACK")
	}}
	sk.backend.live[b.Session].conn = conn
	const op = "lost-ack"
	if _, _, err := sk.core.ClaimComposerOperation(op, protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	lane := sk.composerLaneFor(b.Session)
	barrier := lane.enter()
	defer lane.leave()
	code, err := sk.sendCodexManager(b.Session, op, protocol.ComposerSendReq{Text: "exact input", ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: nonce}}, func() error { return sk.core.BeginComposerOperation(op) }, lane, barrier)
	if code != protocol.CodeOutcomeUnknown || !errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
		t.Fatalf("lost ACK was definite: %s %v", code, err)
	}
	receipt, err := sk.semanticOperation(b.Session, op)
	if err != nil || receipt.State != "admitted" || receipt.NativeMessageID != "native-message" || receipt.NativeInputID != nonce || receipt.NativeTurnID != "native-turn" {
		t.Fatalf("lost ACK did not reconcile exact echo: %+v %v", receipt, err)
	}
	record, _ := sk.core.ComposerOperation(op)
	if record.Phase != idempotency.PhaseCompleted {
		t.Fatalf("known native admission remains unresolved: %s", record.Phase)
	}
	if writes != 1 {
		t.Fatalf("native submission redriven %d times", writes)
	}
	// A new observer cannot manufacture input authority from that durable receipt.
	sk.backend.live[b.Session].feed = newBackendFeed()
	b.ObservationGeneration = sk.backend.live[b.Session].feed.epoch
	r, err := sk.codexManagerRegistry(b.Session, b)
	if err != nil {
		t.Fatal(err)
	}
	status, err := r.Snapshot()
	if err != nil || status.Turn.State != "unknown" || r.ReadyForInput() {
		t.Fatalf("restart guessed proof: %+v %v", status, err)
	}
}

func TestCodexManagerRetainsAndSettlesNativeReceipt(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	b := codexProofFixture()
	b.Session = resumeIdentityLocal
	s, err := newCodexManagerState(b)
	if err != nil {
		t.Fatal(err)
	}
	intent := codexManagerIntent{Binding: b, Nonce: strings.Repeat("f", 64), TextHash: codexManagerTextHash("input"), OperationID: "native-receipt"}
	if _, _, err := r.core.ClaimComposerOperation(intent.OperationID, protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(composerOriginIntent{Version: 1, CodexManager: &intent})
	if err := r.core.BindSemanticRequest(intent.OperationID, raw); err != nil {
		t.Fatal(err)
	}
	if err := r.core.BeginComposerOperation(intent.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := s.prepare(intent); err != nil {
		t.Fatal(err)
	}
	for _, frame := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "own", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "own", "message", intent.Nonce, "input")} {
		if err := s.observe(frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.sk.retainCodexManagerReceipt(s.receipts()[0]); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.sk.semanticOperation(b.Session, intent.OperationID)
	if err != nil || receipt.State != "admitted" {
		t.Fatalf("exact echo unresolved: %+v %v", receipt, err)
	}
	rec, _ := r.core.ComposerOperation(intent.OperationID)
	if rec.Phase != idempotency.PhaseCompleted {
		t.Fatalf("known admission still fences later input: %s", rec.Phase)
	}
}

func TestCodexManagerIgnoresLargeNonAuthorityItems(t *testing.T) {
	s, _ := newCodexManagerState(codexProofFixture())
	raw := codexManagerFrame("item/completed", s.binding.NativeSessionID, map[string]any{"turnId": "turn", "item": map[string]any{"type": "agentMessage", "text": strings.Repeat("output", 20000)}})
	if err := s.observe(raw); err != nil {
		t.Fatalf("ordinary output retired manager evidence: %v", err)
	}
	if _, err := s.registry.Snapshot(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexManagerRejectsRewrittenNativeMessageIdentity(t *testing.T) {
	b := codexProofFixture()
	s, _ := newCodexManagerState(b)
	nonce := strings.Repeat("a", 64)
	if err := s.prepare(codexManagerIntent{Binding: b, Nonce: nonce, TextHash: codexManagerTextHash("input"), OperationID: "op"}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "turn", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "turn", "message", nonce, "input")} {
		if err := s.observe(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.observe(codexManagerUserFrame(b.NativeSessionID, "turn", "message", nonce, "changed")); !errors.Is(err, managerproof.ErrConflict) {
		t.Fatalf("changed native echo accepted: %v", err)
	}
	if _, err := s.registry.Snapshot(); !errors.Is(err, managerproof.ErrStale) {
		t.Fatal("conflicting native evidence remained authoritative")
	}
}

func TestCodexManagerPreRegistrationBufferAndRetirement(t *testing.T) {
	b := codexProofFixture()
	feed := newBackendFeed()
	b.ObservationGeneration = feed.epoch
	d := &Daemon{}
	if err := d.recordSessionInstance(b.Session, b.SessionInstance, 1, 1); err != nil {
		t.Fatal(err)
	}
	d.backend.live = map[string]*sessionBackend{b.Session: {threadID: b.NativeSessionID, sessionInstance: b.SessionInstance, feed: feed}}
	d.captureCodexManagerFrame(b.Session, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", "foreign", map[string]any{"status": map[string]any{"type": "active"}}))
	d.captureCodexManagerFrame(b.Session, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
	r, err := d.codexManagerRegistry(b.Session, b)
	if err != nil || !r.ReadyForInput() {
		t.Fatalf("native idle before binding lost: %v", err)
	}
	discardCodexManagerFeed(feed)
	if _, err := r.Snapshot(); !errors.Is(err, managerproof.ErrStale) {
		t.Fatal("retired feed retained proof")
	}
	feed = newBackendFeed()
	b.ObservationGeneration = feed.epoch
	d.backend.live[b.Session].feed = feed
	for i := 0; i < 129; i++ {
		d.captureCodexManagerFrame(b.Session, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
	}
	if _, err := d.codexManagerRegistry(b.Session, b); !errors.Is(err, managerproof.ErrUnavailable) {
		t.Fatalf("overflow guessed complete native history: %v", err)
	}
}

func TestCodexManagerExpiredHistoricalReceiptDoesNotRetireCurrentProof(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	s, err := newCodexManagerState(codexProofFixture())
	if err != nil {
		t.Fatal(err)
	}
	// A settled history entry may outlive the idempotency retention window.
	s.evidence["old"] = codexManagerEvidence{Version: 1, Intent: codexManagerIntent{OperationID: "expired-settled-operation"}}
	r.sk.retainCodexManagerState(s)
	if _, err := s.registry.Snapshot(); err != nil {
		t.Fatalf("unmodified expired history retired current native proof: %v", err)
	}
}

func TestCodexManagerNativeAdmissionRevalidatesBeforeWrite(t *testing.T) {
	for _, scenario := range []string{"native-active", "stop", "account-change", "feed-replaced", "external-count", "durable-begin-fails", "exact-input"} {
		t.Run(scenario, func(t *testing.T) {
			sk, b, feed := newCodexManagerSendRig(t)
			const text = "input with ; $(false) `literal`\n雪"
			nonce := strings.Repeat("a", 64)
			began, writes := false, 0
			lane := sk.composerLaneFor(b.Session)
			barrier := lane.enter()
			defer lane.leave()
			conn := &codexManagerTestConn{call: func(_ context.Context, method string, params, out any) error {
				writes++
				p := params.(map[string]any)
				input := p["input"].([]map[string]any)
				if method != "turn/start" || p["threadId"] != b.NativeSessionID || p["clientUserMessageId"] != nonce || input[0]["text"] != text {
					t.Fatalf("native admission changed: %s %+v", method, p)
				}
				return json.Unmarshal([]byte(`{"turn":{"id":"accepted-native-turn"}}`), out)
			}}
			sk.backend.live[b.Session].conn = conn
			conn.before = func() {
				switch scenario {
				case "native-active":
					sk.captureCodexManagerFrame(b.Session, feed, "turn/started", codexManagerTurnFrame("turn/started", b.NativeSessionID, "manual", "inProgress"))
				case "stop":
					lane.mu.Lock()
					lane.barrier++
					lane.mu.Unlock()
				case "account-change":
					meta, _ := sk.core.Get(b.Session)
					for _, entry := range meta.Env {
						if strings.HasPrefix(entry, "HOME=") {
							if err := os.WriteFile(filepath.Join(strings.TrimPrefix(entry, "HOME="), ".codex", "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"changed-fixture"}}`), 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
				case "feed-replaced":
					sk.backend.mu.Lock()
					sk.backend.live[b.Session] = &sessionBackend{threadID: b.NativeSessionID, sessionInstance: b.SessionInstance, feed: newBackendFeed(), conn: conn}
					sk.backend.mu.Unlock()
				case "external-count":
					r, err := sk.codexManagerRegistry(b.Session, b)
					if err != nil {
						t.Fatal(err)
					}
					r.ExternalActivity()
					if err := r.ObserveThreadState("idle"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, _, err := sk.core.ClaimComposerOperation("op", protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
				t.Fatal(err)
			}
			code, err := sk.sendCodexManager(b.Session, "op", protocol.ComposerSendReq{Text: text, ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: nonce}}, func() error {
				began = true
				if scenario == "durable-begin-fails" {
					return errors.New("fixture cannot retain begin")
				}
				return sk.core.BeginComposerOperation("op")
			}, lane, barrier)
			if scenario == "exact-input" {
				if err != nil || code != "" || writes != 1 || !began {
					t.Fatalf("qualified native admission failed: %s %v writes=%d began=%v", code, err, writes, began)
				}
				return
			}
			if err == nil || writes != 0 || scenario != "durable-begin-fails" && began {
				t.Fatalf("unsafe admission wrote: %s %v writes=%d began=%v", code, err, writes, began)
			}
			if scenario == "durable-begin-fails" && (code != protocol.CodeOutcomeUnknown || !errors.Is(err, protocol.ErrComposerOutcomeUnknown)) {
				t.Fatal("failed durable begin lost uncertainty")
			}
		})
	}
}

func TestCodexManagerObservationReconnectInvalidatesStaleAdmission(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("daemon-restart-%v", restart), func(t *testing.T) {
			sk, b, oldFeed := newCodexManagerSendRig(t)
			old, err := sk.codexManagerRegistry(b.Session, b)
			if err != nil {
				t.Fatal(err)
			}
			old.ExternalActivity()
			if err := old.ObserveThreadState("idle"); err != nil {
				t.Fatal(err)
			}
			discardCodexManagerFeed(oldFeed)
			if restart {
				if err := sk.core.Close(); err != nil {
					t.Fatal(err)
				}
				dir := sk.stateDir
				core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"), ShimBinary: swarmBin, MaxSessions: 4})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = core.Close() })
				sk = &Daemon{core: core, stateDir: dir}
				meta, _ := core.Get(b.Session)
				if err := sk.recordSessionInstance(b.Session, b.SessionInstance, meta.ShimPID, meta.ShimStartTime); err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			conn := &codexManagerTestConn{call: func(_ context.Context, method string, _ any, out any) error {
				writes++
				if method != "turn/start" {
					t.Fatalf("recovery used %q", method)
				}
				return json.Unmarshal([]byte(`{"turn":{"id":"new-native-turn"}}`), out)
			}}
			fresh := newBackendFeed()
			sk.backend.live = map[string]*sessionBackend{b.Session: {threadID: b.NativeSessionID, sessionInstance: b.SessionInstance, feed: fresh, conn: conn}}
			sk.captureCodexManagerFrame(b.Session, fresh, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
			if _, _, err := sk.core.ClaimComposerOperation("stale", protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash-stale"); err != nil {
				t.Fatal(err)
			}
			lane := sk.composerLaneFor(b.Session)
			barrier := lane.enter()
			defer lane.leave()
			began := false
			_, err = sk.sendCodexManager(b.Session, "stale", protocol.ComposerSendReq{Text: "stale queued input", ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: strings.Repeat("e", 64), ExpectedExternalInput: 0}}, func() error { began = true; return sk.core.BeginComposerOperation("stale") }, lane, barrier)
			if err == nil || began || writes != 0 {
				t.Fatalf("observation reset reused old admission: err=%v begin=%v writes=%d", err, began, writes)
			}
			current, err := sk.managerRuntime(b.Session, b.SessionInstance)
			if err != nil {
				t.Fatal(err)
			}
			if current.Binding == b || current.Binding.NativeGeneration != b.NativeGeneration {
				t.Fatalf("observation and physical process identities were conflated: old=%+v new=%+v", b, current.Binding)
			}
			if _, _, err := sk.core.ClaimComposerOperation("reconciled", protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash-reconciled"); err != nil {
				t.Fatal(err)
			}
			_, err = sk.sendCodexManager(b.Session, "reconciled", protocol.ComposerSendReq{Text: "input after explicit reconciliation", ManagerAdmission: &protocol.ManagerAdmission{Binding: current.Binding, Nonce: strings.Repeat("f", 64), ExpectedExternalInput: current.Turn.ExternalInput}}, func() error { return sk.core.BeginComposerOperation("reconciled") }, lane, barrier)
			if err != nil || writes != 1 {
				t.Fatalf("fresh observation could not be explicitly reconciled: err=%v writes=%d", err, writes)
			}
		})
	}
}
