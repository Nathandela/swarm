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
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// Real daemon/shim custody with a private HTTP fixture; this does not qualify the native plugin API.
func TestOpenCodeAssemblyObservesBeforeReleasingNativeInput(t *testing.T) {
	d := assemble(t)
	t.Cleanup(func() {
		for _, m := range d.core.List() {
			_ = d.core.Kill(m.ID)
		}
	})
	type launchResult struct {
		meta persist.Meta
		err  error
	}
	launched := make(chan launchResult, 1)
	cwd, home := t.TempDir(), t.TempDir()
	script := mustScript(t, "idle 60s\n")
	go func() {
		m, err := d.core.Launch(daemon.LaunchSpec{AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, Argv: []string{fakeAgentBin, script}, Cwd: cwd, OpenCodeEnv: []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24})
		launched <- launchResult{m, err}
	}()
	var binding opencodebridge.Binding
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range d.core.List() {
			if b, err := d.core.SessionOpenCodeBridge(m.ID); err == nil {
				binding = b
				break
			}
		}
		if binding.SocketPath != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if binding.SocketPath == "" {
		t.Fatal("native launch never reserved private binding")
	}
	identity := opencodebridge.Identity{Version: 1, WorkerID: binding.WorkerID, SessionInstance: binding.SessionInstance, RootID: binding.RootID, Directory: binding.Directory, BackendGeneration: binding.BackendGeneration, NativeCreatedAtMS: 123}
	listener, err := net.Listen("unix", binding.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	var subscribed atomic.Bool
	events := make(chan opencodebridge.Event, 8)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+binding.Token {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/ready", "/ack":
			_ = json.NewEncoder(w).Encode(opencodebridge.Ready{Identity: identity, NativeVersion: "1.19.3", Bound: true, Ready: true, Acknowledged: r.URL.Path == "/ack"})
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			subscribed.Store(true)
			identityRaw, _ := json.Marshal(identity)
			frame, _ := json.Marshal(opencodebridge.Event{Type: "swarm.bridge.connected", Properties: identityRaw})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			w.(http.Flusher).Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case event := <-events:
					raw, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", raw)
					w.(http.Flusher).Flush()
				}
			}
		case "/snapshot":
			if !subscribed.Load() {
				t.Error("snapshot preceded event subscription")
			}
			row, _ := json.Marshal(map[string]any{"id": binding.RootID, "directory": binding.Directory, "time": map[string]int64{"created": 123}})
			_ = json.NewEncoder(w).Encode(opencodebridge.Snapshot{Identity: identity, Session: row, Statuses: map[string]json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Messages: []json.RawMessage{}, Children: []json.RawMessage{}, Descendants: []json.RawMessage{}, HierarchyComplete: true, HierarchyScope: openCodeHierarchyScope})
		default:
			w.WriteHeader(404)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	select {
	case result := <-launched:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native observation barrier never completed")
	}
	view, ok := d.openCodeView(binding.WorkerID)
	if !ok || !view.BusyKnown || view.Busy || view.Degraded {
		t.Fatalf("missing reconciled native projection: %+v (%v)", view, ok)
	}
	if m, _ := d.core.Get(binding.WorkerID); m.Status.Turn != status.TurnIdle || m.OpenCodeBinding.Phase != persist.OpenCodePhaseReady {
		t.Fatalf("native status did not reach sole metadata writer: %+v", m.Status)
	}
	properties, _ := json.Marshal(map[string]any{"sessionID": binding.RootID, "status": map[string]string{"type": "busy"}})
	events <- opencodebridge.Event{Type: "session.status", Properties: properties}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m, _ := d.core.Get(binding.WorkerID); m.Status.Turn == status.TurnActive {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("live native status never reached metadata")
}
