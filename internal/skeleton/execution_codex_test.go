package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

type codexExecutionTestConn struct {
	loaded string
	native *appserver.Client
}

func (c *codexExecutionTestConn) PeerProcess() (int, uint32, bool) { return c.native.PeerProcess() }

func (c *codexExecutionTestConn) Call(_ context.Context, method string, _, out any) error {
	if method == "thread/loaded/list" {
		*(out.(*json.RawMessage)) = json.RawMessage(c.loaded)
		return nil
	}
	return fmt.Errorf("unexpected fixture RPC %s", method)
}
func (*codexExecutionTestConn) Respond(context.Context, json.RawMessage, any) error { return nil }
func (*codexExecutionTestConn) Close() error                                        { return nil }

type codexExecutionRig struct {
	saveMu          sync.Mutex
	saveHook        func(persist.ExecutionRecord) error
	d               *Daemon
	local, instance string
	feed            *backendFeed
	channel         daemon.BackendChannel
	conn            *codexExecutionTestConn
	meta            persist.Meta
	store           *persist.Store
}

// Synthetic shim/backend identities exercise the production FIFO and private
// metadata proof; this fixture does not claim native provider qualification.
func newCodexExecutionRig(t *testing.T, conversation string, fresh bool) *codexExecutionRig {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "swce-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const local = "native"
	instance := "owned-instance"
	executable := filepath.Join(root, "codex-fixture")
	if err = os.WriteFile(executable, []byte("synthetic native executable metadata"), 0700); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(executable)
	if err != nil {
		t.Fatal(err)
	}
	nativeSocket := filepath.Join(root, local, "codex.sock")
	if err = os.MkdirAll(filepath.Dir(nativeSocket), 0700); err != nil {
		t.Fatal(err)
	}
	native := exec.Command(os.Args[0], "-test.run=^TestCodexExecutionOwnedEndpointProcess$")
	native.Env = append(os.Environ(), "SWARM_EXECUTION_ENDPOINT_FIXTURE="+nativeSocket)
	native.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = native.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = native.Process.Kill(); _ = native.Wait() })
	waitBackendIngress(t, func() bool { _, e := os.Stat(nativeSocket); return e == nil })
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	nativeStart, err := procstart.StartTime(native.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{ID: local, AgentType: "codex", Cwd: root, ShimPID: os.Getpid(), ShimStartTime: start, ConversationID: conversation, CreatedAt: time.Now(), LastActivity: time.Now(), Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, CLIIdentity: &persist.CLIIdentity{Path: executable, Version: qualifiedCodexExecutionVersion, Fingerprint: fingerprint}, LoginRuntime: &logins.Runtime{Provider: logins.ProviderCodex, ExternalAuth: true, Epoch: 1, Identity: strings.Repeat("a", 64), Home: filepath.Join(root, "login")}}
	if err = store.Save(m); err != nil {
		t.Fatal(err)
	}
	shimListener, err := net.Listen("unix", filepath.Join(root, local, "shim.sock"))
	if err != nil {
		t.Fatal(err)
	}
	shimDone := make(chan struct{})
	go func() {
		defer close(shimDone)
		for {
			c, e := shimListener.Accept()
			if e != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(time.Second))
			_, _, e = wire.ReadFrame(c)
			if e == nil {
				raw, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
				_ = wire.WriteFrame(c, wire.TControl, raw)
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = shimListener.Close(); <-shimDone })
	info := shim.BackendInfo{PID: native.Process.Pid, PGID: native.Process.Pid, StartedAtMS: nativeStart, SocketPath: filepath.Join(root, local, "codex.sock")}
	raw, _ := json.Marshal(info)
	if err = os.WriteFile(filepath.Join(root, local, shim.BackendFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "daemon.sock"), LockPath: filepath.Join(root, "daemon.lock"), LogPath: filepath.Join(root, "daemon.log"), MaxSessions: 8})
	if err != nil {
		t.Fatal(err)
	}
	r := &codexExecutionRig{store: store}
	d := &Daemon{core: core, stateDir: root, executionStore: store, eng: engine.New(engine.Config{StoreExecution: r.saveExecution})}
	d.capStore.instances = map[string]string{local: instance}
	d.setAdapterForTest(func(string) (adapter.Adapter, bool) { return codex.New(), true })
	if current, ok := core.Get(local); !ok || current.Status.Process != status.ProcessRunning {
		t.Fatalf("synthetic shim did not reconcile: %+v", current)
	}
	if err = d.eng.RegisterExecutionSession(local, "", m.ShimPID, codex.New().SignalSources(), m.Status, engine.ExecutionIdentity{Incarnation: executionIncarnation(m), ConversationID: conversation, ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	feed := newBackendFeed()
	feed.userAgent = "codex_cli_rs/0.163.0"
	if !d.startBackendIngress(local, instance, feed) {
		t.Fatal("ingress unavailable")
	}
	t.Cleanup(func() { d.stopBackendIngress(); _ = core.Close() })
	r.d = d
	r.local = local
	r.instance = instance
	r.feed = feed
	r.meta = m
	r.channel = daemon.BackendChannel{SocketPath: info.SocketPath}
	pin, err := appserver.CaptureUnixEndpoint(info.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := appserver.Dial(context.Background(), info.SocketPath, appserver.Options{Endpoint: pin, OnClose: func(error) { feed.ingress.stop() }, OnFrame: func(method string, raw json.RawMessage) { feed.ingress.enqueue(method, raw, time.Now()) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	r.conn = &codexExecutionTestConn{loaded: `{"data":[]}`, native: peer}
	d.prepareCodexExecutionNative(context.Background(), local, instance, r.channel, feed)
	d.finishCodexExecutionPreparation(context.Background(), local, instance, r.channel, r.conn, feed, json.RawMessage(`{"userAgent":"codex_cli_rs/0.163.0"}`), fresh)
	if conversation != "" && !d.registerBackendFeedForInstance(local, instance, conversation, r.conn, feed) {
		t.Fatal("exact backend feed not registered")
	}
	return r
}

func (r *codexExecutionRig) enqueue(t *testing.T, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if !r.feed.ingress.enqueue(method, raw, time.Now()) {
		t.Fatal("native frame rejected")
	}
}
func (r *codexExecutionRig) drain(t *testing.T) {
	t.Helper()
	waitBackendIngress(t, func() bool { return r.feed.ingress.pending.Load() == 0 })
}

func codexExecutionNativeFrames(t *testing.T, scenario string) (string, []struct {
	Method string
	Params any
}) {
	t.Helper()
	raw, err := os.ReadFile("../../docs/verification/provider-logins/native-handover-capabilities-2026-10-24.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Codex struct {
			Results []struct {
				Scenario string
				Items    []struct {
					Event, Type, ID, Turn string
					Phase                 *string
					Exit                  *int
				} `json:"item_summary"`
			} `json:"results"`
		} `json:"codex"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, result := range receipt.Codex.Results {
		if result.Scenario != scenario {
			continue
		}
		turn := result.Items[0].Turn
		thread := resumeIdentityCodexID
		frames := []struct {
			Method string
			Params any
		}{{"turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress"}}}}
		for _, item := range result.Items {
			if item.Turn != turn {
				continue
			}
			body := map[string]any{"id": item.ID, "type": item.Type}
			if item.Type == "agentMessage" {
				body["phase"] = item.Phase
			}
			if item.Type == "commandExecution" {
				body["status"] = "inProgress"
				if item.Event == "item/completed" {
					body["status"] = "completed"
					body["exitCode"] = item.Exit
				}
			}
			frames = append(frames, struct {
				Method string
				Params any
			}{item.Event, map[string]any{"threadId": thread, "turnId": turn, "item": body}})
		}
		frames = append(frames, struct {
			Method string
			Params any
		}{"turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "failed", "error": map[string]any{"codexErrorInfo": "usageLimitExceeded"}}}})
		return turn, frames
	}
	t.Fatal("native scenario unavailable")
	return "", nil
}

func TestCodexExecutionCandidateRequiresProductionIngress(t *testing.T) {
	d := &Daemon{}
	if _, ok := d.codexQuotaCandidate("missing"); ok {
		t.Fatal("missing ingress yielded candidate")
	}
}

func TestCodexExecutionProductionIngressNativeQuotaTrace(t *testing.T) {
	for _, scenario := range []string{"quota", "finalquota", "unknownquota"} {
		t.Run(scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, "", true)
			r.enqueue(t, "thread/started", map[string]any{"thread": map[string]any{"id": resumeIdentityCodexID}})
			r.drain(t)
			if !r.d.registerBackendFeedForInstance(r.local, r.instance, resumeIdentityCodexID, r.conn, r.feed) {
				t.Fatal("adopted native feed not installed")
			}
			turn, frames := codexExecutionNativeFrames(t, scenario)
			for _, frame := range frames {
				r.enqueue(t, frame.Method, frame.Params)
			}
			r.drain(t)
			candidate, ok := r.d.codexQuotaCandidate(r.local)
			if ok != (scenario == "quota") {
				snapshot, _ := r.d.eng.ExecutionSnapshot(r.local)
				t.Fatalf("native %s candidate=%+v eligible=%v record=%+v", scenario, candidate, ok, snapshot.Codex)
			}
			if ok && (candidate.TurnID != turn || candidate.FeedEpoch != r.feed.epoch) {
				t.Fatal("production worker lost exact native turn/feed")
			}
		})
	}
}

func (r *codexExecutionRig) quotaTrace(t *testing.T, omitTerminal bool) []struct {
	Method string
	Params any
} {
	t.Helper()
	_, frames := codexExecutionNativeFrames(t, "quota")
	count := len(frames)
	if omitTerminal {
		count--
	}
	for _, frame := range frames[:count] {
		r.enqueue(t, frame.Method, frame.Params)
	}
	r.drain(t)
	return frames
}

func TestCodexExecutionBirthProofSeparatesResumeAndRejoin(t *testing.T) {
	for _, scenario := range []string{"fresh-resume", "rejoin", "preloaded", "malformed-list", "wrong-handshake", "early-control"} {
		t.Run(scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, scenario != "rejoin")
			if scenario == "preloaded" || scenario == "malformed-list" || scenario == "wrong-handshake" {
				r.conn.loaded = `{"data":["` + resumeIdentityCodexID + `"]}`
				handshake := json.RawMessage(`{"userAgent":"codex_cli_rs/0.163.0"}`)
				if scenario == "malformed-list" {
					r.conn.loaded = `{}`
				}
				if scenario == "wrong-handshake" {
					r.conn.loaded = `{"data":[]}`
					handshake = json.RawMessage(`{"userAgent":"codex_cli_rs/0.162.0"}`)
				}
				r.d.finishCodexExecutionPreparation(context.Background(), r.local, r.instance, r.channel, r.conn, r.feed, handshake, true)
			}
			if scenario == "early-control" {
				r.feed.codexExecution.mu.Lock()
				r.feed.codexExecution.preparedAt = time.Now().Add(time.Hour)
				r.feed.codexExecution.mu.Unlock()
			}
			r.quotaTrace(t, false)
			candidate, ok := r.d.codexQuotaCandidate(r.local)
			if ok != (scenario == "fresh-resume") {
				t.Fatalf("%s candidate=%+v eligible=%v", scenario, candidate, ok)
			}
		})
	}
}

func TestCodexExecutionProductionFencesForeignLossRetirementAndNativeIdentity(t *testing.T) {
	for _, scenario := range []string{"foreign-terminal", "unknown-collab", "gap", "retired", "metadata-start", "metadata-permissions", "cli-changed", "instance-changed"} {
		t.Run(scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			frames := r.quotaTrace(t, (scenario == "foreign-terminal" || scenario == "unknown-collab"))
			switch scenario {
			case "foreign-terminal":
				p := frames[len(frames)-1].Params.(map[string]any)
				p["threadId"] = resumeIdentityOtherCodex
				r.enqueue(t, frames[len(frames)-1].Method, p)
				r.drain(t)
			case "unknown-collab":
				terminal := frames[len(frames)-1]
				turn := terminal.Params.(map[string]any)["turn"].(map[string]any)["id"]
				for _, method := range []string{"item/started", "item/completed"} {
					r.enqueue(t, method, map[string]any{"threadId": resumeIdentityCodexID, "turnId": turn, "item": map[string]any{"id": "child-handle", "type": "collabAgentToolCall", "status": "completed"}})
				}
				r.enqueue(t, terminal.Method, terminal.Params)
				r.drain(t)
			case "gap":
				r.feed.ingress.markLoss()
			case "retired":
				r.feed.retireIngress()
			case "metadata-start":
				info := r.feed.codexExecution.native
				info.StartedAtMS++
				raw, _ := json.Marshal(info)
				if err := os.WriteFile(filepath.Join(r.d.stateDir, r.local, shim.BackendFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "metadata-permissions":
				if err := os.Chmod(filepath.Join(r.d.stateDir, r.local, shim.BackendFile), 0644); err != nil {
					t.Fatal(err)
				}
			case "cli-changed":
				if err := os.WriteFile(r.meta.CLIIdentity.Path, []byte("changed installation"), 0700); err != nil {
					t.Fatal(err)
				}
			case "instance-changed":
				r.d.capStore.mu.Lock()
				r.d.capStore.instances[r.local] = "replacement"
				r.d.capStore.mu.Unlock()
			}
			if candidate, ok := r.d.codexQuotaCandidate(r.local); ok {
				t.Fatalf("%s used invalid source proof: %+v", scenario, candidate)
			}
		})
	}
}

func TestCodexExecutionCandidateFencesGenericQueueEvenWithEligibleEngine(t *testing.T) {
	r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
	r.quotaTrace(t, false)
	if _, ok := r.d.eng.CodexQuotaCandidate(r.local); !ok {
		t.Fatal("fixture did not reach engine candidate")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	original := r.feed.ingress.process
	r.feed.ingress.process = func(f backendIngressFrame) { close(entered); <-release; original(f) }
	r.enqueue(t, "thread/tokenUsage/updated", map[string]any{"threadId": resumeIdentityCodexID})
	<-entered
	result := make(chan bool, 1)
	go func() { _, ok := r.d.codexQuotaCandidate(r.local); result <- ok }()
	select {
	case ok := <-result:
		if ok {
			close(release)
			t.Fatal("pending generic frame left candidate usable")
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("candidate getter waited behind generic worker instead of refusing")
	}
	close(release)
	r.drain(t)
	if _, ok := r.d.codexQuotaCandidate(r.local); !ok {
		t.Fatal("clean drained observational frame did not preserve candidate")
	}
}

func TestCodexExecutionRejectsAmbiguousHandshake(t *testing.T) {
	for _, raw := range []string{`{"userAgent":"codex_cli_rs/0.163.0","UserAgent":"codex_cli_rs/0.163.0"}`, `{"userAgent":"codex_cli_rs/0.163.0","userAgent":"codex_cli_rs/0.163.0"}`, `{"UserAgent":"codex_cli_rs/0.163.0"}`, `{"userAgent":{"version":"0.163.0"}}`} {
		if qualifiedCodexExecutionHandshake(json.RawMessage(raw), "codex_cli_rs/0.163.0") {
			t.Fatal("ambiguous native handshake qualified")
		}
	}
}

func (r *codexExecutionRig) saveExecution(record persist.ExecutionRecord) error {
	r.saveMu.Lock()
	hook := r.saveHook
	r.saveMu.Unlock()
	if hook != nil {
		if err := hook(record); err != nil {
			return err
		}
	}
	return r.store.SaveExecution(record)
}
func (r *codexExecutionRig) setSaveHook(hook func(persist.ExecutionRecord) error) {
	r.saveMu.Lock()
	r.saveHook = hook
	r.saveMu.Unlock()
}

func TestCodexExecutionProductionPersistenceFailureCannotQualify(t *testing.T) {
	r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
	frames := r.quotaTrace(t, true)
	r.setSaveHook(func(record persist.ExecutionRecord) error {
		if record.Codex != nil && record.Codex.State == "terminal" {
			return syscall.ENOSPC
		}
		return nil
	})
	terminal := frames[len(frames)-1]
	r.enqueue(t, terminal.Method, terminal.Params)
	r.drain(t)
	if _, ok := r.d.codexQuotaCandidate(r.local); ok {
		t.Fatal("non-durable terminal used by production candidate")
	}
	durable, err := r.store.LoadExecution(r.local)
	if err != nil {
		t.Fatal(err)
	}
	if durable.Codex.State == "terminal" {
		t.Fatal("terminal unexpectedly committed despite injected ENOSPC")
	}
	r.setSaveHook(nil)
	r.enqueue(t, "thread/tokenUsage/updated", map[string]any{"threadId": resumeIdentityCodexID})
	r.drain(t)
	if _, ok := r.d.codexQuotaCandidate(r.local); ok {
		t.Fatal("unrelated later frame laundered failed evidence")
	}
}

func TestCodexExecutionDialCapturesBeforeInitializeAndDistinguishesRejoin(t *testing.T) {
	for _, scenario := range []string{"fresh-resume", "rejoin", "early-native-work", "native-replaced-during-init", "wrong-native-peer", "missing-proof", "unqualified-cli", "ambiguous-handshake"} {
		t.Run(scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, false)
			replacement := r.feed.codexExecution.native
			if scenario == "native-replaced-during-init" || scenario == "wrong-native-peer" {
				next := exec.Command("/bin/sleep", "60")
				next.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := next.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = next.Process.Kill(); _ = next.Wait() }()
				started, err := procstart.StartTime(next.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}
				replacement.PID = next.Process.Pid
				replacement.PGID = next.Process.Pid
				replacement.StartedAtMS = started
			}
			_ = r.d.forgetBackendForFeed(r.local, r.feed.epoch)
			if scenario == "wrong-native-peer" {
				raw, _ := json.Marshal(replacement)
				if err := os.WriteFile(filepath.Join(r.d.stateDir, r.local, shim.BackendFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing-proof" {
				if err := os.WriteFile(filepath.Join(r.d.stateDir, r.local, shim.BackendFile), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unqualified-cli" {
				if err := r.d.core.ClearCLIIdentity(r.local); err != nil {
					t.Fatal(err)
				}
			}
			config, _ := json.Marshal(map[string]any{"scenario": scenario, "replacement": replacement})
			if err := os.WriteFile(r.channel.SocketPath+".config", config, 0600); err != nil {
				t.Fatal(err)
			}

			var accesses atomic.Int32
			r.d.backendAuthAccess = func(context.Context, string, bool) (backendAuthTokens, error) {
				accesses.Add(1)
				return backendAuthTokens{AccessToken: "SYNTHETIC-PRIVATE", AccountID: "fixture"}, nil
			}
			conn, feed, err := r.d.dialSessionBackend(r.local, r.instance, r.channel, scenario != "rejoin")
			if scenario == "wrong-native-peer" || scenario == "missing-proof" || scenario == "unqualified-cli" {
				if accesses.Load() != 0 {
					t.Fatal("unverified peer retrieved private authentication material")
				}
				if err == nil {
					_ = conn.Close()
					t.Fatal("foreign live peer admitted before auth bootstrap")
				}
				if _, e := os.Stat(r.channel.SocketPath + ".auth"); !os.IsNotExist(e) {
					t.Fatal("foreign peer received authentication payload")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			r.feed = feed
			if !r.d.registerBackendFeedForInstance(r.local, r.instance, resumeIdentityCodexID, conn, feed) {
				t.Fatal("dialed feed not registered")
			}
			r.quotaTrace(t, false)
			candidate, ok := r.d.codexQuotaCandidate(r.local)
			if ok != (scenario == "fresh-resume") {
				t.Fatalf("%s candidate=%+v qualified=%v", scenario, candidate, ok)
			}
		})
	}
}

func TestCodexExecutionEmptyLoadedProofIsUnambiguous(t *testing.T) {
	for _, raw := range []string{`{"data":[],"Data":["unobserved-thread"]}`, `{"data":[],"data":["unobserved-thread"]}`, `{"data":null}`, `{}`, `{"data":["thread"]}`} {
		if qualifiedEmptyCodexThreads(json.RawMessage(raw)) {
			t.Fatal("ambiguous or nonempty native thread list qualified")
		}
	}
	if !qualifiedEmptyCodexThreads(json.RawMessage(`{"data":[]}`)) {
		t.Fatal("explicit empty native list denied")
	}
}

func TestCodexExecutionResidualHookTokenCannotBecomeBackendAuthority(t *testing.T) {
	r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
	if err := r.d.eng.RegisterExecutionSession(r.local, "residual-token", r.meta.ShimPID, codex.New().SignalSources(), r.meta.Status, engine.ExecutionIdentity{Incarnation: executionIncarnation(r.meta), ConversationID: resumeIdentityCodexID, ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	r.quotaTrace(t, false)
	if _, ok := r.d.codexQuotaCandidate(r.local); ok {
		t.Fatal("rejoin hook producer token silently became native backend authority")
	}
}

// This subprocess owns the actual endpoint and bounded synthetic native protocol.
func TestCodexExecutionOwnedEndpointProcess(t *testing.T) {
	socket := os.Getenv("SWARM_EXECUTION_ENDPOINT_FIXTURE")
	if socket == "" {
		t.Skip("owned subprocess only")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ws, err := websocket.Accept(w, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		var opening struct{ Scenario string }
		if cfg, e := os.ReadFile(socket + ".config"); e == nil {
			_ = json.Unmarshal(cfg, &opening)
		}
		if opening.Scenario == "wrong-native-peer" || opening.Scenario == "missing-proof" || opening.Scenario == "unqualified-cli" {
			refresh, _ := json.Marshal(map[string]any{"id": "early-auth", "method": backendAuthRefreshMethod, "params": map[string]any{"reason": "unauthorized"}})
			if ws.Write(request.Context(), websocket.MessageText, refresh) != nil {
				return
			}
		}
		for {
			_, raw, err := ws.Read(request.Context())
			if err != nil {
				return
			}
			if bytes.Contains(raw, []byte("SYNTHETIC-PRIVATE")) {
				if os.WriteFile(socket+".auth", []byte("private payload received"), 0600) != nil {
					return
				}
			}
			var call struct {
				ID     json.RawMessage
				Method string
				Params json.RawMessage
			}
			if json.Unmarshal(raw, &call) != nil {
				return
			}
			var config struct {
				Scenario    string
				Replacement shim.BackendInfo
			}
			if cfg, e := os.ReadFile(socket + ".config"); e == nil {
				_ = json.Unmarshal(cfg, &config)
			}
			var result any = map[string]any{}
			switch call.Method {
			case "initialize":
				if config.Scenario == "native-replaced-during-init" {
					proof, _ := json.Marshal(config.Replacement)
					if os.WriteFile(filepath.Join(filepath.Dir(socket), shim.BackendFile), proof, 0600) != nil {
						return
					}
				}
				if config.Scenario == "ambiguous-handshake" {
					if ws.Write(request.Context(), websocket.MessageText, []byte(`{"id":`+string(call.ID)+`,"result":{"userAgent":"codex_cli_rs/0.163.0","UserAgent":"codex_cli_rs/0.163.0"}}`)) != nil {
						return
					}
					continue
				}
				if config.Scenario == "early-native-work" {
					body, _ := json.Marshal(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "unexpected-initial-turn", "status": "inProgress"}}})
					if ws.Write(request.Context(), websocket.MessageText, body) != nil {
						return
					}
				}
				result = map[string]any{"userAgent": "codex_cli_rs/0.163.0"}
			case "account/login/start":
				if err := os.WriteFile(socket+".auth", []byte("auth method received"), 0600); err != nil {
					return
				}
				result = map[string]any{"type": "chatgptAuthTokens"}
			case "thread/loaded/list":
				result = map[string]any{"data": []string{}}
			case "fixture/emit":
				var frames []struct {
					Binary bool
					Raw    string
				}
				if json.Unmarshal(call.Params, &frames) != nil || len(frames) > 128 {
					return
				}
				for _, fr := range frames {
					typ := websocket.MessageText
					if fr.Binary {
						typ = websocket.MessageBinary
					}
					if len(fr.Raw) > 65536 || ws.Write(request.Context(), typ, []byte(fr.Raw)) != nil {
						return
					}
				}
			}
			if len(call.ID) == 0 {
				continue
			}
			body, _ := json.Marshal(map[string]any{"id": call.ID, "result": result})
			if ws.Write(request.Context(), websocket.MessageText, body) != nil {
				return
			}
		}
	})}
	timer := time.AfterFunc(45*time.Second, func() { _ = server.Close() })
	defer timer.Stop()
	_ = server.Serve(listener)
}

func TestCodexExecutionInvalidWebSocketFrameFencesTerminal(t *testing.T) {
	for _, invalid := range []string{`{"method":`, `{"method":"ignored","method":"turn/completed","params":{}}`, `{"Method":"turn/completed","params":{}}`, `{"method":null,"id":1,"result":{}}`, `{"method":"","id":1,"result":{}}`, `{"id":1,"params":{"turn":{"status":"failed"}}}`, `{"id":123,"error":null}`, "BINARY"} {
		t.Run(invalid, func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			_, trace := codexExecutionNativeFrames(t, "quota")
			// Every execution event travels over the actual owned child's WebSocket.
			frames := []map[string]any{}
			for i, frame := range trace {
				if i == len(trace)-1 {
					frames = append(frames, map[string]any{"Raw": invalid, "Binary": invalid == "BINARY"})
				}
				raw, _ := json.Marshal(map[string]any{"method": frame.Method, "params": frame.Params})
				frames = append(frames, map[string]any{"Raw": string(raw)})
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var reply json.RawMessage
			if err := r.conn.native.Call(ctx, "fixture/emit", frames, &reply); err == nil {
				t.Fatal("invalid stream remained usable")
			}
			r.drain(t)
			if candidate, ok := r.d.codexQuotaCandidate(r.local); ok {
				t.Fatalf("invalid frame permitted terminal authority: %+v", candidate)
			}
		})
	}
}

func TestCodexExecutionEndpointAndPeerFences(t *testing.T) {
	for _, scenario := range []string{"replaced-socket", "public-socket", "symlink", "wrong-peer"} {
		t.Run(scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			r.quotaTrace(t, false)
			if _, ok := r.d.codexQuotaCandidate(r.local); !ok {
				t.Fatal("baseline candidate unavailable")
			}
			switch scenario {
			case "replaced-socket":
				if err := os.Rename(r.channel.SocketPath, r.channel.SocketPath+".old"); err != nil {
					t.Fatal(err)
				}
				ln, err := net.Listen("unix", r.channel.SocketPath)
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				if err = os.Chmod(r.channel.SocketPath, 0600); err != nil {
					t.Fatal(err)
				}
			case "public-socket":
				if err := os.Chmod(r.channel.SocketPath, 0666); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(r.channel.SocketPath, r.channel.SocketPath+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(r.channel.SocketPath+".old", r.channel.SocketPath); err != nil {
					t.Fatal(err)
				}
			case "wrong-peer":
				// Metadata can describe a different live process while the actual fd stays old.
				next := exec.Command("/bin/sleep", "30")
				next.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := next.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = next.Process.Kill(); _ = next.Wait() }()
				started, err := procstart.StartTime(next.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}
				r.feed.codexExecution.mu.Lock()
				native := r.feed.codexExecution.native
				native.PID = next.Process.Pid
				native.PGID = native.PID
				native.StartedAtMS = started
				r.feed.codexExecution.native = native
				r.feed.codexExecution.mu.Unlock()
				raw, _ := json.Marshal(native)
				if err = os.WriteFile(filepath.Join(r.d.stateDir, r.local, shim.BackendFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
				if codexExecutionPeerMatches(r.conn, native.PID, r.feed.codexExecution.endpoint) {
					t.Fatal("live metadata PID substituted for kernel peer")
				}
			}
			if candidate, ok := r.d.codexQuotaCandidate(r.local); ok {
				t.Fatalf("%s retained authority: %+v", scenario, candidate)
			}
		})
	}
}
