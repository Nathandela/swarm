package skeleton

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
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
	d *Daemon
	client *protocol.Client
	binding opencodebridge.Binding
	identity opencodebridge.Identity
	request protocol.ComposerSendReq
	calls atomic.Int32
	dropReply atomic.Bool
}

// The real daemon, shim writer and owner protocol surround a private native bridge fixture.
func newOpenCodeSendFixture(t *testing.T) *openCodeSendFixture {
	t.Helper()
	f := &openCodeSendFixture{d: assemble(t)}
	spec := daemon.LaunchSpec{AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, Argv: []string{fakeAgentBin, mustScript(t, "idle 60s\n")}, Cwd: t.TempDir(), OpenCodeEnv: []string{"HOME="+t.TempDir(), "PATH="+os.Getenv("PATH")}, Cols: 80, Rows: 24}
	launch := make(chan error, 1)
	go func() {
		_, err := f.d.core.Launch(spec)
		launch <- err
	}()
	t.Cleanup(func() { for _, m := range f.d.core.List() { _ = f.d.core.Kill(m.ID) } })
	until := time.Now().Add(5*time.Second)
	for f.binding.SocketPath == "" && time.Now().Before(until) {
		for _, m := range f.d.core.List() { if b, err := f.d.core.SessionOpenCodeBridge(m.ID); err == nil { f.binding = b; break } }
		time.Sleep(5*time.Millisecond)
	}
	if f.binding.SocketPath == "" { t.Fatal("native bridge was not allocated") }
	f.identity = opencodebridge.Identity{Version: 1, WorkerID: f.binding.WorkerID, SessionInstance: f.binding.SessionInstance, RootID: f.binding.RootID, Directory: f.binding.Directory, BackendGeneration: f.binding.BackendGeneration, NativeCreatedAtMS: 123}
	listener, err := net.Listen("unix", f.binding.SocketPath)
	if err != nil { t.Fatal(err) }
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.binding.Token { w.WriteHeader(401); return }
		switch r.URL.Path {
		case "/ready", "/ack":
			_ = json.NewEncoder(w).Encode(opencodebridge.Ready{Identity: f.identity, NativeVersion: "1.19.3", Bound: true, Ready: true, Acknowledged: r.URL.Path == "/ack"})
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			props, _ := json.Marshal(f.identity)
			frame, _ := json.Marshal(opencodebridge.Event{Type: "swarm.bridge.connected", Properties: props})
			fmt.Fprintf(w, "data: %s\n\n", frame); w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/snapshot":
			row, _ := json.Marshal(map[string]any{"id": f.binding.RootID, "directory": f.binding.Directory, "time": map[string]int64{"created": 123}})
			_ = json.NewEncoder(w).Encode(opencodebridge.Snapshot{Identity: f.identity, Session: row, Statuses: map[string]json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Messages: []json.RawMessage{}, Children: []json.RawMessage{}})
		case "/dispatch":
			var req opencodebridge.DispatchRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil { t.Error("invalid native dispatch"); w.WriteHeader(400); return }
			f.calls.Add(1)
			rec, ok := f.d.core.ComposerOperation(req.OperationID)
			if !ok || rec.Phase != idempotency.PhaseExecuting || len(rec.Request) == 0 { t.Error("native effect preceded durable request and execution custody") }
			if f.dropReply.Load() {
				conn, _, err := w.(http.Hijacker).Hijack(); if err != nil { t.Error(err) } else { _ = conn.Close() }; return
			}
			_ = json.NewEncoder(w).Encode(opencodebridge.Receipt{Identity: f.identity, Kind: "prompt", TargetID: f.binding.RootID, OperationID: req.OperationID, Status: "admitted", MessageID: req.MessageID, Agent: "build", Model: &opencodebridge.Model{ProviderID: "fixture", ModelID: "fixture-model"}, Variant: "max", SelectionBasis: "native_session_selection"})
		default: w.WriteHeader(404)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	select { case err := <-launch: if err != nil { t.Fatal(err) }; case <-time.After(5*time.Second): t.Fatal("native launch barrier timed out") }
	f.d.authorCapabilitiesForRunning()
	f.client, err = protocol.Dial(f.d.SocketPath(), []string{protocol.CapSemanticSend})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = f.client.Close() })
	cap, ok := f.d.sessionCapabilities(f.binding.WorkerID)
	if !ok || cap.SessionInstance == "" { t.Fatal("no generic session incarnation") }
	f.request = protocol.ComposerSendReq{Session: protocol.NamespacedID(f.client.EndpointID(), f.binding.WorkerID), SessionInstance: cap.SessionInstance, Text: "exact native message"}
	return f
}

func TestOpenCodeOwnerSemanticSendUsesDurableNativeCustody(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	id, err := protocol.NewSemanticOperationID()
	if err != nil { t.Fatal(err) }
	for i := 0; i < 2; i++ { if err := f.client.SemanticSend(id, f.request); err != nil { t.Fatalf("native semantic attempt%d: %v", i, err) } }
	rec, ok := f.d.core.ComposerOperation(id)
	if !ok || rec.Phase != idempotency.PhaseCompleted || len(rec.Request) == 0 || len(rec.Evidence) == 0 || f.calls.Load() != 1 {
		t.Fatalf("native receipt did not survive in durable operation custody: %+v calls%d", rec, f.calls.Load())
	}
}

func TestOpenCodeOwnerUncertainSendNeverReposts(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, err := protocol.NewSemanticOperationID()
	if err != nil { t.Fatal(err) }
	for i := 0; i < 2; i++ { if err := f.client.SemanticSend(id, f.request); err == nil { t.Fatal("lost native receipt claimed admission") } }
	later, err := protocol.NewSemanticOperationID()
	if err != nil { t.Fatal(err) }
	if err := f.client.SemanticSend(later, f.request); err == nil { t.Fatal("later input overtook unresolved native admission") }
	rec, ok := f.d.core.ComposerOperation(id)
	if !ok || rec.Phase != idempotency.PhaseExecuting || len(rec.Request) == 0 || f.calls.Load() != 1 {
		t.Fatalf("ambiguous native effect lost its non-replay guard: %+v calls%d", rec, f.calls.Load())
	}
}
