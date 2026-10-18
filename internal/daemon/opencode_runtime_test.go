package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

type openCodeRuntimeFixture struct {
	initialRequest atomic.Value
	rejectReason   atomic.Value
	rejectBadProof atomic.Bool
	d              *Daemon
	m              persist.Meta
	b              opencodebridge.Binding
	initialCalls   atomic.Int32
	released       atomic.Bool
	observing      atomic.Bool
	dropReply      atomic.Bool
	haveReceipt    atomic.Bool
	wrongRoot      atomic.Bool
}

func newOpenCodeRuntimeFixture(t *testing.T) *openCodeRuntimeFixture {
	t.Helper()
	f := &openCodeRuntimeFixture{}
	cfg := daemonConfig(t)
	cfg.OpenCodeReady = func(_ context.Context, m persist.Meta) error {
		if m.OpenCodeBinding.Phase != persist.OpenCodePhaseReady {
			t.Error("observer barrier preceded durable readiness")
		}
		f.observing.Store(true)
		return nil
	}
	f.d = openDaemon(t, cfg)
	dir := filepath.Join(cfg.StateDir, "native")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := LaunchSpec{RequiredRuntime: persist.RequiredRuntimeOpenCode, AgentType: "opencode", Cwd: t.TempDir(), Argv: []string{"/native/opencode"}, InitialPrompt: "real initial request", OpenCodeEnv: []string{"XDG_DATA_HOME=/native"}}
	f.m = persist.Meta{ID: "native", AgentType: "opencode", Cwd: spec.Cwd, Status: status.Status{Process: status.ProcessRunning}}
	if err := f.d.prepareOpenCodeLaunch(&spec, &f.m, dir); err != nil {
		t.Fatal(err)
	}
	if err := f.d.saveMeta(f.m); err != nil {
		t.Fatal(err)
	}
	lc := shimSpawnConfig{SessionID: f.m.ID, SessionDir: dir, Cwd: spec.Cwd, Argv: spec.Argv, OpenCode: spec.OpenCodeLaunch, OpenCodeEnv: spec.OpenCodeEnv, OpenCodeInitialPrompt: spec.InitialPrompt, SocketPath: shimSocketPath(cfg.StateDir, f.m.ID)}
	raw, _ := json.Marshal(lc)
	if err := os.WriteFile(filepath.Join(dir, shimLaunchConfigFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	f.b, err = f.d.SessionOpenCodeBridge(f.m.ID)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", f.b.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.b.Token {
			w.WriteHeader(401)
			return
		}
		identity := opencodebridge.Identity{Version: 1, WorkerID: f.b.WorkerID, SessionInstance: f.b.SessionInstance, RootID: f.b.RootID, Directory: f.b.Directory, BackendGeneration: f.b.BackendGeneration, NativeCreatedAtMS: 123}
		if f.wrongRoot.Load() {
			identity.RootID = "ses_" + strings.Repeat("e", 32)
		}
		ready := opencodebridge.Ready{Identity: identity, NativeVersion: "1.19.3", Bound: true, Ready: true, InitialSupported: true}
		switch r.URL.Path {
		case "/ready":
			_ = json.NewEncoder(w).Encode(ready)
		case "/ack":
			m, _ := f.d.Get(f.m.ID)
			if m.OpenCodeBinding.Phase == persist.OpenCodePhasePlanned || m.OpenCodeBinding.NativeCreatedAtMS != 123 {
				t.Error("ack before durable native binding")
			}
			ready.Acknowledged = true
			_ = json.NewEncoder(w).Encode(ready)
		case "/initial":
			if r.Method == http.MethodPost {
				var request opencodebridge.InitialRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid initial request")
					w.WriteHeader(400)
					return
				}
				f.initialRequest.Store(request)
				f.initialCalls.Add(1)
				m, _ := f.d.Get(f.m.ID)
				if !f.observing.Load() || m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialStarted {
					t.Error("initial write before observation/durable started marker")
				}
				f.haveReceipt.Store(true)
				if f.dropReply.Swap(false) {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						_ = conn.Close()
					}
					return
				}
			}
			if !f.haveReceipt.Load() {
				_ = json.NewEncoder(w).Encode(nil)
				return
			}
			if reason, ok := f.rejectReason.Load().(string); ok && reason != "" {
				effect := "none"
				if f.rejectBadProof.Load() {
					effect = "unknown"
				}
				_ = json.NewEncoder(w).Encode(opencodebridge.Receipt{Identity: identity, Status: "rejected", Reason: reason, NativeEffect: effect, OperationID: f.m.OpenCodeBinding.Initial.OperationID, RequestHash: f.m.OpenCodeBinding.Initial.RequestHash})
				return
			}
			_ = json.NewEncoder(w).Encode(opencodebridge.Receipt{Identity: identity, Status: "admitted", OperationID: f.m.OpenCodeBinding.Initial.OperationID, RequestHash: f.m.OpenCodeBinding.Initial.RequestHash,
				MessageID: "msg_" + strings.Repeat("m", 26), Agent: "plan", Model: &opencodebridge.Model{ProviderID: "opencode", ModelID: "deepseek-v4.1-flash-free"}, Variant: "max"})
		default:
			w.WriteHeader(404)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	shimListener, err := net.Listen("unix", lc.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shimListener.Close() })
	go func() {
		for {
			conn, err := shimListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				for {
					_, raw, err := wire.ReadFrame(conn)
					if err != nil {
						return
					}
					ctrl, err := shimwire.Decode(raw)
					if err != nil {
						return
					}
					reply := shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, OpenCodeInputEmbargo: true}
					if ctrl.Type == shimwire.TypeOpenCodeEmbargoRelease {
						m, _ := f.d.Get(f.m.ID)
						if m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialAdmitted {
							t.Error("release before native admission")
						}
						if ctrl.IncidentID != lc.OpenCode.Generation || ctrl.Token != lc.OpenCode.EmbargoToken {
							t.Error("release authority mismatch")
						}
						f.released.Store(true)
						reply.Type = shimwire.TypeOpenCodeEmbargoResult
					}
					data, _ := shimwire.Encode(reply)
					if wire.WriteFrame(conn, wire.TControl, data) != nil {
						return
					}
				}
			}()
		}
	}()
	return f
}

func TestOpenCodeRuntimeBindingAckObservationAndInitialOrder(t *testing.T) {
	f := newOpenCodeRuntimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.d.joinOpenCode(ctx, f.m.ID); err != nil {
		t.Fatal(err)
	}
	m, _ := f.d.Get(f.m.ID)
	if m.OpenCodeBinding.Phase != persist.OpenCodePhaseReady || m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialAdmitted || m.OpenCodeBinding.Initial.MessageID != "msg_"+strings.Repeat("m", 26) || !f.released.Load() {
		t.Fatalf("incomplete native admission: %+v", m.OpenCodeBinding)
	}
	if err := f.d.joinOpenCode(ctx, f.m.ID); err != nil {
		t.Fatal(err)
	}
	if f.initialCalls.Load() != 1 {
		t.Fatal("rejoin replayed initial input")
	}
}

func TestOpenCodeRuntimeLostReplyReconcilesWithoutReposting(t *testing.T) {
	f := newOpenCodeRuntimeFixture(t)
	f.dropReply.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = f.d.joinOpenCode(ctx, f.m.ID)
	m, _ := f.d.Get(f.m.ID)
	if m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialUncertain || f.released.Load() {
		t.Fatal("lost native response became success or released owner input")
	}
	if err := f.d.joinOpenCode(ctx, f.m.ID); err != nil {
		t.Fatal(err)
	}
	if f.initialCalls.Load() != 1 || !f.released.Load() {
		t.Fatal("recovery did not use exact retained receipt")
	}
}

func TestOpenCodeRuntimeWrongRootNeverBindsOrSubmits(t *testing.T) {
	f := newOpenCodeRuntimeFixture(t)
	f.wrongRoot.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if f.d.joinOpenCode(ctx, f.m.ID) == nil {
		t.Fatal("wrong native root accepted")
	}
	m, _ := f.d.Get(f.m.ID)
	if m.OpenCodeBinding.Phase != persist.OpenCodePhasePlanned || f.initialCalls.Load() != 0 || f.released.Load() {
		t.Fatal("wrong identity crossed binding boundary")
	}
}

func TestOpenCodeRuntimeRetirementAfterDurableBindingNeverPanicsOrReleases(t *testing.T) {
	for _, phase := range []string{"bound", "ready", "started", "admitted"} {
		t.Run(phase, func(t *testing.T) {
			f := newOpenCodeRuntimeFixture(t)
			f.d.cfg.onMetaSave = func(m persist.Meta) {
				current := m.OpenCodeBinding.Phase
				if m.OpenCodeBinding.Initial != nil && (phase == "started" || phase == "admitted") {
					current = m.OpenCodeBinding.Initial.Phase
				}
				if current == phase {
					f.d.mu.Lock()
					delete(f.d.sessions, m.ID)
					f.d.mu.Unlock()
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := f.d.joinOpenCode(ctx, f.m.ID); err == nil {
				t.Fatal("retired native session acknowledged success")
			}
			if f.released.Load() {
				t.Fatal("retired native session released owner input")
			}
			if phase != "admitted" && f.initialCalls.Load() != 0 {
				t.Fatal("retirement crossed native initial write")
			}
		})
	}
}

func TestOpenCodeInitialCarriesRequestedChoicesToNativePreflight(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			f := newOpenCodeRuntimeFixture(t)
			if explicit {
				f.m.LaunchOptions = map[string]string{"model": "opencode/deepseek-v4.1-flash-free", "agent": "plan"}
				if err := f.d.saveMeta(f.m); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := f.d.joinOpenCode(ctx, f.m.ID); err != nil {
				t.Fatal(err)
			}
			request := f.initialRequest.Load().(opencodebridge.InitialRequest)
			if request.ExpectedModel != f.m.LaunchOptions["model"] || request.ExpectedAgent != f.m.LaunchOptions["agent"] {
				t.Fatal("native preflight did not receive exact requested choices")
			}
		})
	}
}

func TestOpenCodeInitialDefiniteRejectionIsDurableAndNeverReposted(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			f := newOpenCodeRuntimeFixture(t)
			f.rejectReason.Store("initial_model_unavailable")
			f.dropReply.Store(lost)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := f.d.joinOpenCode(ctx, f.m.ID)
			if lost {
				if err == nil {
					t.Fatal("lost reply accepted")
				}
				err = f.d.joinOpenCode(ctx, f.m.ID)
			}
			if err == nil || !strings.Contains(err.Error(), "initial_model_unavailable") || strings.Contains(err.Error(), "uncertain") {
				t.Fatalf("definite refusal mislabeled: %v", err)
			}
			m, _ := f.d.Get(f.m.ID)
			raw, _ := json.Marshal(m.OpenCodeBinding.Initial)
			if m.OpenCodeBinding.Initial.Phase != "rejected" || !strings.Contains(string(raw), `"reason":"initial_model_unavailable"`) || f.released.Load() {
				t.Fatalf("definite rejection not retained: %s", raw)
			}
			f.d.openCodeRuntimeError(f.m.ID, err)
			saved, loadErr := f.d.store.Load(f.m.ID)
			if loadErr != nil || saved.OpenCodeBinding.Initial.Phase != "rejected" || !strings.Contains(saved.BackendPlanError, "initial_model_unavailable") {
				t.Fatalf("refusal not durable/actionable: %+v %v", saved, loadErr)
			}
			for _, change := range []func(*persist.OpenCodeBinding){func(b *persist.OpenCodeBinding) { b.Initial.Reason = "changed_reason" }, func(b *persist.OpenCodeBinding) {
				b.Initial.Phase = persist.OpenCodeInitialStarted
				b.Initial.Reason = ""
			}} {
				next := *persist.CloneOpenCodeBinding(saved.OpenCodeBinding)
				next.Revision++
				change(&next)
				if f.d.SetOpenCodeBinding(saved.ID, saved.OpenCodeBinding.Revision, next) == nil {
					t.Fatal("terminal rejection custody changed")
				}
			}
			_ = f.d.joinOpenCode(ctx, f.m.ID)
			if f.initialCalls.Load() != 1 || f.released.Load() {
				t.Fatal("rejected initial was replayed or released")
			}
		})
	}
}
func TestOpenCodeInitialRejectionWithoutNoEffectProofStaysUncertain(t *testing.T) {
	f := newOpenCodeRuntimeFixture(t)
	f.rejectReason.Store("initial_model_unavailable")
	f.rejectBadProof.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = f.d.joinOpenCode(ctx, f.m.ID)
	m, _ := f.d.Get(f.m.ID)
	if m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialUncertain || f.released.Load() {
		t.Fatal("unqualified rejection became a no-effect proof")
	}
}
