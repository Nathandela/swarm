package skeleton

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

type openCodeSendFixture struct {
	promptReceipt        func(opencodebridge.DispatchRequest, opencodebridge.Receipt) opencodebridge.Receipt
	initialNativeMode    bool
	nativeDecisionMode   bool
	nativeQuestionMode   bool
	lastDecisionDispatch atomic.Value
	pendingNative        atomic.Value
	decisionReceipt      atomic.Value
	receiptAvailable     atomic.Bool
	rowAvailable         atomic.Bool
	nativeRow            atomic.Value
	d                    *Daemon
	client               *protocol.Client
	binding              opencodebridge.Binding
	identity             opencodebridge.Identity
	request              protocol.ComposerSendReq
	calls                atomic.Int32
	dropReply            atomic.Bool
	blockDispatch        atomic.Bool
	dispatchEntered      chan struct{}
	dispatchRelease      chan struct{}
	historyPage          atomic.Value // optional read-only snapshot fixture
	historyPagingFail    atomic.Bool
	nativeEvents         chan opencodebridge.Event
}

// The real daemon, shim writer and owner protocol surround a private native bridge fixture.
func newOpenCodeSendFixture(t *testing.T, configure ...func(*openCodeSendFixture)) *openCodeSendFixture {
	t.Helper()
	f := &openCodeSendFixture{dispatchEntered: make(chan struct{}, 4), dispatchRelease: make(chan struct{}), nativeEvents: make(chan opencodebridge.Event, 8)}
	for _, option := range configure {
		option(f)
	}
	if f.d == nil {
		f.d = assemble(t)
	}
	spec := daemon.LaunchSpec{AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, Argv: []string{fakeAgentBin, mustScript(t, "idle 60s\n")}, Cwd: t.TempDir(), OpenCodeEnv: []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24}
	if f.initialNativeMode {
		spec.InitialPrompt = "exact initial input"
	}
	launch := make(chan error, 1)
	go func() {
		_, err := f.d.core.Launch(spec)
		launch <- err
	}()
	t.Cleanup(func() {
		for _, m := range f.d.core.List() {
			_ = f.d.core.Kill(m.ID)
		}
	})
	until := time.Now().Add(5 * time.Second)
	for f.binding.SocketPath == "" && time.Now().Before(until) {
		for _, m := range f.d.core.List() {
			if b, err := f.d.core.SessionOpenCodeBridge(m.ID); err == nil {
				f.binding = b
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if f.binding.SocketPath == "" {
		t.Fatal("native bridge was not allocated")
	}
	f.identity = opencodebridge.Identity{Version: 1, WorkerID: f.binding.WorkerID, SessionInstance: f.binding.SessionInstance, RootID: f.binding.RootID, Directory: f.binding.Directory, BackendGeneration: f.binding.BackendGeneration, NativeCreatedAtMS: 123}
	listener, err := net.Listen("unix", f.binding.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if f.nativeDecisionMode {
		kind, prefix := "permission", "per_"
		if f.nativeQuestionMode {
			kind, prefix = "question", "que_"
		}
		f.pendingNative.Store([]json.RawMessage{nativeRequestFixture(t, f.binding.RootID, prefix+strings.Repeat("a", 32), kind).Raw})
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.binding.Token {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/ready", "/ack":
			_ = json.NewEncoder(w).Encode(opencodebridge.Ready{Identity: f.identity, NativeVersion: "1.19.3", Bound: true, Ready: true, Acknowledged: true, DraftGuardSupported: true, InitialSupported: f.initialNativeMode})
		case "/initial":
			var req opencodebridge.InitialRequest
			if !f.initialNativeMode || json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			f.calls.Add(1)
			messageID := "msg_" + strings.Repeat("a", 32)
			row, _ := json.Marshal(map[string]any{"info": map[string]any{"id": messageID, "sessionID": f.binding.RootID, "role": "user", "agent": "build", "model": map[string]string{"providerID": "fixture", "modelID": "fixture-model", "variant": "max"}, "time": map[string]int64{"created": time.Now().UnixMilli()}}, "parts": []any{map[string]any{"id": "prt_initial", "sessionID": f.binding.RootID, "messageID": messageID, "type": "text", "text": req.Text}}})
			f.nativeRow.Store(json.RawMessage(row))
			_ = json.NewEncoder(w).Encode(opencodebridge.Receipt{Identity: f.identity, OperationID: req.OperationID, RequestHash: req.RequestHash, Status: "admitted", MessageID: messageID, Agent: "build", Model: &opencodebridge.Model{ProviderID: "fixture", ModelID: "fixture-model"}, Variant: "max", SelectionBasis: "native_composer"})
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			props, _ := json.Marshal(f.identity)
			frame, _ := json.Marshal(opencodebridge.Event{Type: "swarm.bridge.connected", Properties: props})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-f.nativeEvents:
					frame, _ := json.Marshal(event)
					if _, err := fmt.Fprintf(w, "data: %s\n\n", frame); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/snapshot":
			if read, ok := f.historyPage.Load().(func(int, string) (opencodebridge.Snapshot, error)); ok {
				limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
				page, err := read(limit, r.URL.Query().Get("before"))
				if err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(page)
				return
			}
			if f.historyPagingFail.Load() && r.URL.Query().Get("before") == "older" {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"reason":"native_read_failed"}`))
				return
			}
			cursor := ""
			if f.historyPagingFail.Load() {
				cursor = "older"
			}
			permissions, questions := []json.RawMessage{}, []json.RawMessage{}
			if f.nativeDecisionMode {
				pending, _ := f.pendingNative.Load().([]json.RawMessage)
				if f.nativeQuestionMode {
					questions = pending
				} else {
					permissions = pending
				}
			}
			row, _ := json.Marshal(map[string]any{"id": f.binding.RootID, "directory": f.binding.Directory, "version": "1.19.3", "time": map[string]int64{"created": 123}})
			_ = json.NewEncoder(w).Encode(opencodebridge.Snapshot{Identity: f.identity, Session: row, NextCursor: cursor, Statuses: map[string]json.RawMessage{}, Permissions: permissions, Questions: questions, Messages: []json.RawMessage{}, Children: []json.RawMessage{}, Descendants: []json.RawMessage{}, HierarchyComplete: true, HierarchyScope: openCodeHierarchyScope})
		case "/message":
			raw, ok := f.nativeRow.Load().(json.RawMessage)
			if !f.rowAvailable.Load() || !ok {
				w.WriteHeader(404)
				_ = json.NewEncoder(w).Encode(map[string]string{"reason": "native_message_missing"})
				return
			}
			_, _ = w.Write(raw)
		case "/receipt":
			if f.receiptAvailable.Load() {
				_ = json.NewEncoder(w).Encode(f.decisionReceipt.Load())
			} else {
				_ = json.NewEncoder(w).Encode(nil)
			}
		case "/dispatch":
			var req opencodebridge.DispatchRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				t.Error("invalid native dispatch")
				w.WriteHeader(400)
				return
			}
			f.calls.Add(1)
			select {
			case f.dispatchEntered <- struct{}{}:
			default:
			}
			if f.blockDispatch.Load() {
				select {
				case <-f.dispatchRelease:
				case <-r.Context().Done():
					return
				}
			}
			rec, ok := f.d.core.ComposerOperation(req.OperationID)
			if !ok || rec.Phase != idempotency.PhaseExecuting || len(rec.Request) == 0 {
				t.Error("native effect preceded durable request and execution custody")
			}
			if f.nativeDecisionMode {
				f.lastDecisionDispatch.Store(req)
				if rec.Action != protocol.OpNativeDecide || len(req.ExpectedRequest) == 0 || req.RequestExpiresAtMS <= time.Now().UnixMilli() {
					t.Error("native decision effect lacks durable content/expiry proof")
				}
				effect := "single_request"
				if req.Reply == "always" {
					effect = "approve_native_patterns_and_matching_session_requests"
				}
				if req.Reply == "reject" {
					effect = "reject_session_pending_permissions"
				}
				receipt := opencodebridge.Receipt{Identity: f.identity, Kind: req.Kind, TargetID: req.TargetID, RequestID: req.RequestID, OperationID: req.OperationID, Status: "admitted", NativeStatus: 200, NativeEffect: effect}
				f.decisionReceipt.Store(receipt)
				if f.dropReply.Load() {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						_ = conn.Close()
					}
					return
				}
				_ = json.NewEncoder(w).Encode(receipt)
				return
			}
			row, _ := json.Marshal(map[string]any{"info": map[string]any{"id": req.MessageID, "sessionID": f.binding.RootID, "role": "user", "agent": "build", "model": map[string]string{"providerID": "fixture", "modelID": "fixture-model", "variant": "max"}, "time": map[string]int64{"created": time.Now().UnixMilli()}}, "parts": []any{map[string]any{"id": "prt_native", "sessionID": f.binding.RootID, "messageID": req.MessageID, "type": "text", "text": req.Text}}})
			f.nativeRow.Store(json.RawMessage(row))
			if f.dropReply.Load() {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
				} else {
					_ = conn.Close()
				}
				return
			}
			receipt := opencodebridge.Receipt{Identity: f.identity, Kind: "prompt", TargetID: f.binding.RootID, OperationID: req.OperationID, Status: "admitted", MessageID: req.MessageID, Agent: "build", Model: &opencodebridge.Model{ProviderID: "fixture", ModelID: "fixture-model"}, Variant: "max", SelectionBasis: "native_session_selection"}
			if f.promptReceipt != nil {
				receipt = f.promptReceipt(req, receipt)
			}
			_ = json.NewEncoder(w).Encode(receipt)
		default:
			w.WriteHeader(404)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	select {
	case err := <-launch:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native launch barrier timed out")
	}
	f.d.authorCapabilitiesForRunning()
	f.client, err = protocol.Dial(f.d.SocketPath(), []string{protocol.CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.client.Close() })
	cap, ok := f.d.sessionCapabilities(f.binding.WorkerID)
	if !ok || cap.SessionInstance == "" {
		t.Fatal("no generic session incarnation")
	}
	f.request = protocol.ComposerSendReq{Session: protocol.NamespacedID(f.client.EndpointID(), f.binding.WorkerID), SessionInstance: cap.SessionInstance, Text: "exact native message"}
	return f
}

func TestOpenCodeOwnerSemanticSendUsesDurableNativeCustody(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.client.SemanticSend(id, f.request); err != nil {
			t.Fatalf("native semantic attempt%d: %v", i, err)
		}
	}
	rec, ok := f.d.core.ComposerOperation(id)
	if !ok || rec.Phase != idempotency.PhaseCompleted || len(rec.Request) == 0 || len(rec.Evidence) == 0 || f.calls.Load() != 1 {
		t.Fatalf("native receipt did not survive in durable operation custody: %+v calls%d", rec, f.calls.Load())
	}
}

func TestOpenCodeOwnerUncertainSendNeverReposts(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.client.SemanticSend(id, f.request); err == nil {
			t.Fatal("lost native receipt claimed admission")
		}
	}
	later, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.SemanticSend(later, f.request); err == nil {
		t.Fatal("later input overtook unresolved native admission")
	}
	rec, ok := f.d.core.ComposerOperation(id)
	if !ok || rec.Phase != idempotency.PhaseExecuting || len(rec.Request) == 0 || f.calls.Load() != 1 {
		t.Fatalf("ambiguous native effect lost its non-replay guard: %+v calls%d", rec, f.calls.Load())
	}
}

func TestOpenCodeOwnerEndWinsBeforeNativeSubmission(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	if err := f.d.withOwnerSessionEnd(f.binding.WorkerID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.SemanticSend(id, f.request); err == nil || f.calls.Load() != 0 {
		t.Fatal("native submission crossed an owner end intent while Process was still running")
	}
	rec, ok := f.d.core.ComposerOperation(id)
	if !ok || rec.Phase == idempotency.PhaseExecuting || len(rec.Evidence) != 0 {
		t.Fatal("owner end refusal crossed native execution boundary")
	}
}

func TestOpenCodeNativeSubmissionAndOwnerEndShareOneLifecycleFence(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.blockDispatch.Store(true)
	t.Cleanup(func() {
		select {
		case <-f.dispatchRelease:
		default:
			close(f.dispatchRelease)
		}
	})
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { sent <- f.client.SemanticSend(id, f.request) }()
	select {
	case <-f.dispatchEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("native write never reached guarded dispatch")
	}
	ownerStarted, ownerAction, ownerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		close(ownerStarted)
		ownerDone <- f.d.withOwnerSessionEnd(f.binding.WorkerID, func() error { close(ownerAction); return nil })
	}()
	<-ownerStarted
	select {
	case <-ownerAction:
		t.Fatal("owner terminal action crossed an in-flight native dispatch")
	case <-time.After(50 * time.Millisecond):
	}
	close(f.dispatchRelease)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ownerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner terminal action did not resume")
	}
}
