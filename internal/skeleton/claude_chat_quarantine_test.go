package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// Structural managed metadata only: no account, grant, native CLI or birth authority.
func a2QuarantineMeta(t *testing.T, root, id string, chat bool) persist.Meta {
	t.Helper()
	ls, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ls.Close() }()
	registry := filepath.Join(root, "logins", "registry.json")
	raw, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["schema_version"], _ = json.Marshal(logins.ClaudeProfileCustodySchemaVersion)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, raw, 0600); err != nil {
		t.Fatal(err)
	}
	home, err := ls.ClaudeProfileHome(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{SchemaVersion: 5, ID: id, AgentType: "claude", Cwd: root, CreatedAt: time.Now().UTC(), Status: status.Status{Process: status.ProcessLost, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, LoginRuntime: &logins.Runtime{Provider: "claude", Epoch: 1, Identity: strings.Repeat("a", 64), Home: home}, CLIIdentity: &persist.CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}}
	if chat {
		m.SchemaVersion = 6
		m.RequiredRuntime = "claude-chat"
	}
	return m
}

type a2QuarantineFixture struct {
	d        *Daemon
	m        persist.Meta
	hd       *HookDrainer
	response shim.HookDrainResponse
	raw      []byte
	paths    []string
}

func a2NewQuarantineFixture(t *testing.T, chat bool) *a2QuarantineFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "a2q")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := a2QuarantineMeta(t, root, "retained", chat)
	if err := store.Save(m); err != nil {
		t.Fatalf("metadata prerequisite must be GREEN before quarantine RED: %v", err)
	}
	loaded, err := store.Load(m.ID)
	if err != nil || loaded.RequiredRuntime != m.RequiredRuntime || loaded.SchemaVersion != m.SchemaVersion {
		t.Fatal("managed fixture roundtrip failed", err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "d"), LockPath: filepath.Join(root, "l"), LogPath: filepath.Join(root, "log"), MaxSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	d := &Daemon{core: core, stateDir: root, closing: make(chan struct{}), eng: engine.New(engine.Config{}), adapterFor: func(string) (adapter.Adapter, bool) { return nil, false }}
	d.initInteractions()
	registerTestSession(d, m.ID, "a2-token") // Restored ordinary authority must NOT bypass mode quarantine.
	path := filepath.Join(root, m.ID, shim.HookSpoolFile)
	producer, err := shim.OpenHookSpoolProducer(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(engine.Callback{SessionID: m.ID, Token: "a2-token", Sequence: 1, Event: "PostToolUse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Append(raw); err != nil {
		t.Fatal(err)
	}
	// A new origin must remain raw evidence, never disposable ordinary-hook rejection.
	if _, err := producer.Append([]byte(`{"v":1,"origin":"claude_primary","raw":"c3ludGhldGlj"}`)); err != nil {
		t.Fatal(err)
	}
	response := shim.HookDrainResponse{SpoolIncarnation: producer.IncarnationID(), ProducerCertificate: producer.ProducerCertificate()}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	response, err = shim.ReadHookSpoolFileResponse(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	hd := NewHookDrainer(d, m.ID, "", filepath.Join(root, m.ID, "hook.fold"))
	hd.SetSpoolPath(path)
	if err := persistHookDrainCursor(hd.cursorPath, 1); err != nil {
		t.Fatal(err)
	}
	cert, _ := json.Marshal(hookCursorCertificate{Incarnation: response.SpoolIncarnation, Producer: response.ProducerCertificate, Sequence: 1})
	if err := os.WriteFile(hd.cursorPath+".cert", cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := persistHookGapCheckpoint(hookGapCursorPath(hd.cursorPath), hookGapCheckpoint{SpoolIncarnation: response.SpoolIncarnation, Boundary: 77}); err != nil {
		t.Fatal(err)
	}
	paths := []string{path, path + ".incarnation", path + ".producer", path + ".floor", hd.cursorPath, hd.cursorPath + ".cert", hookGapCursorPath(hd.cursorPath), filepath.Join(root, m.ID, "meta.json"), filepath.Join(root, m.ID, "hook.applied")}
	return &a2QuarantineFixture{d: d, m: m, hd: hd, response: response, raw: raw, paths: paths}
}

type a2FileSnapshot struct {
	data   []byte
	exists bool
}

func a2Snapshot(t *testing.T, paths []string) map[string]a2FileSnapshot {
	t.Helper()
	result := map[string]a2FileSnapshot{}
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil && !os.IsNotExist(e) {
			t.Fatal(e)
		}
		result[p] = a2FileSnapshot{b, e == nil}
	}
	return result
}
func a2Unchanged(t *testing.T, before map[string]a2FileSnapshot) {
	t.Helper()
	for p, want := range before {
		b, e := os.ReadFile(p)
		if e != nil && !os.IsNotExist(e) {
			t.Fatal(e)
		}
		if (e == nil) != want.exists || !bytes.Equal(b, want.data) {
			t.Errorf("quarantine mutated retained file %s", filepath.Base(p))
		}
	}
}

// A real listener witnesses ANY outgoing DRAIN/FoldSeq; no absence sleep is needed.
func a2DrainWitness(t *testing.T, dir string) (string, *atomic.Int64, *atomic.Uint64) {
	t.Helper()
	path := filepath.Join(dir, "w")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var fold atomic.Uint64
	var handlers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(time.Second))
				var tag [1]byte
				if _, e := c.Read(tag[:]); e != nil {
					return
				}
				var req shim.HookDrainRequest
				if json.NewDecoder(c).Decode(&req) == nil {
					calls.Add(1)
					fold.Store(req.FoldSeq)
				}
				_, _ = c.Write([]byte(`{"records":[]}`))
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done; handlers.Wait() })
	return path, &calls, &fold
}

func TestClaudeChatDirectLiveDiskApplyAndFinalStayQuarantined(t *testing.T) {
	for _, method := range []string{"live", "disk", "apply", "final", "stop", "fallback"} {
		t.Run(method, func(t *testing.T) {
			f := a2NewQuarantineFixture(t, true)
			before := a2Snapshot(t, f.paths)
			path, calls, fold := a2DrainWitness(t, f.d.stateDir)
			f.hd.hookSocketPath = path
			// Flush would replace an existing certificate BEFORE ordinary apply does anything.
			f.hd.certificatePending = &hookCursorCertificate{Incarnation: f.response.SpoolIncarnation, Producer: "pending-marker", Sequence: 1}
			switch method {
			case "live":
				_, _, _ = f.hd.DrainOnce()
			case "disk":
				_, _, _ = f.hd.drainFromSpoolFile()
			case "apply":
				_, _, _ = f.hd.applyLocked(f.response, 1)
			case "final":
				f.hd.FinalDrain()
			case "stop":
				stopped := make(chan struct{})
				f.d.drains.stop = map[string]chan struct{}{f.m.ID: stopped}
				f.d.drains.drainers = map[string]*HookDrainer{f.m.ID: f.hd}
				f.d.stopHookDrain(f.m.ID)
				select {
				case <-stopped:
				default:
					t.Error("stopHookDrain did not close loop")
				}
				if _, ok := f.d.drains.drainers[f.m.ID]; ok {
					t.Error("stopHookDrain retained drainer registration")
				}
			case "fallback":
				if err := f.d.ingestHookBytes(f.raw); err == nil || errors.Is(err, errHookIngestRejected) {
					t.Error("quarantine became accepted/disposable ordinary hook")
				}
			}
			if calls.Load() != 0 || fold.Load() != 0 {
				t.Errorf("quarantine sent DRAIN/FoldSeq: calls=%d fold=%d", calls.Load(), fold.Load())
			}
			a2Unchanged(t, before)
		})
	}
}

func TestClaudeChatCachedCertificateAndQueuedRetryCannotRetire(t *testing.T) {
	for _, method := range []string{"retire", "pending", "queued", "missing-current"} {
		t.Run(method, func(t *testing.T) {
			f := a2NewQuarantineFixture(t, true)
			before := a2Snapshot(t, f.paths)
			if method == "pending" {
				f.hd.certificatePending = &hookCursorCertificate{Incarnation: f.response.SpoolIncarnation, Producer: "pending-marker", Sequence: 1}
			}
			if method == "missing-current" {
				f.d.core = nil
			} // Captured mode must not become ordinary when lookup disappears.
			if method == "queued" {
				f.d.drains.retirement = map[string]hookRetirementRetry{f.m.ID: {drainer: f.hd, remaining: 2}}
				f.d.retryHookRetirements()
			} else {
				_ = f.hd.retireCertifiedPrefix()
			}
			a2Unchanged(t, before)
		})
	}
}

func TestClaudeChatOrdinaryHookControlStillFoldsAndRetires(t *testing.T) {
	f := a2NewQuarantineFixture(t, false)
	// Ordinary controls intentionally use only the valid hook prefix.
	f.response.Records = f.response.Records[:1]
	if a, s, e := f.hd.applyLocked(f.response, 1); e != nil || a != 1 || s != 0 {
		t.Fatalf("ordinary callback changed: applied=%d skipped=%d err=%v", a, s, e)
	}
	if e := f.hd.retireCertifiedPrefix(); e != nil {
		t.Fatal(e)
	}
	remaining, e := shim.ReadHookSpoolFileResponse(f.paths[0], 1)
	if e != nil || len(remaining.Records) != 1 || remaining.Records[0].Seq != 2 {
		t.Fatal("ordinary certified prefix was not retired", e)
	}
}

func TestClaudeChatSamplingAndAutomaticBypassesHaveZeroEffects(t *testing.T) {
	f := a2NewQuarantineFixture(t, true)
	sampled := 0
	f.d.sampleGridFrom(f.m.ID, func() ([]byte, error) { sampled++; return nil, errors.New("controlled sampler") })
	if sampled != 0 {
		t.Error("quarantine sampled raw PTY")
	}
	autoMeta := f.m
	autoMeta.Status.Process = status.ProcessRunning
	autoMeta.Status.Turn = status.TurnIdle
	lf := newLoginFixture(t)
	lf.rows[f.m.ID] = autoMeta
	resolved := 0
	lf.m.d.authw.resolve = func(string, []string) (string, error) { resolved++; return "", errors.New("controlled resolve") }
	if err := lf.m.preflightSources([]persist.Meta{autoMeta}); err == nil || resolved != 0 {
		t.Error("automatic switch reached feasibility callback")
	}
	if err := lf.m.inputGate(f.m.ID); err == nil {
		t.Error("quarantine login input gate accepted")
	}
	af := newAuthFake(identityA)
	af.add(autoMeta)
	w := testWatcher(t, af)
	w.state.Killed[f.m.ID] = true
	w.resolve = func(string, []string) (string, error) { resolved++; return "", errors.New("controlled resolve") }
	resolved = 0
	_ = w.recycle("claude", autoMeta)
	if resolved != 0 || len(af.killed) != 0 || len(af.launched) != 0 || len(af.deleted) != 0 {
		t.Error("restored automatic recycle crossed quarantine")
	}
	dapi := &coreAPI{core: f.d.core, tap: newTapManager(func(string) (protocol.SessionStream, error) {
		t.Error("direct Attach reached raw stream")
		return nil, errors.New("controlled raw")
	})}
	if _, err := dapi.Attach(f.m.ID); err == nil {
		t.Error("direct coreAPI Attach accepted quarantine")
	}
	resolvedAdapter := 0
	f.d.adapterFor = func(string) (adapter.Adapter, bool) { resolvedAdapter++; return nil, false }
	before := a2Snapshot(t, f.paths)
	f.d.captureConversationID(f.m.ID)
	f.d.captureConversationIDFinal(f.m.ID)
	a2Unchanged(t, before)
	if resolvedAdapter != 0 {
		t.Error("quarantine attempted native conversation recapture")
	}
}

func TestClaudeChatCachedCapabilitiesCannotResurrectRawOrComposer(t *testing.T) {
	f := a2NewQuarantineFixture(t, true)
	f.d.registerSessionCapabilities(f.m.ID, protocol.SessionCapabilities{Provider: "claude", SessionInstance: "cached-a2", StructuredChat: true, Interrupt: true})
	a2AssertPassiveCapabilities(t, f.d, f.m.ID)
}

func TestClaudeChatRestoredSupervisionRefusesQuarantine(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "quarantined"}[chat], func(t *testing.T) {
			var live *Daemon
			var meta persist.Meta
			if chat {
				live, meta, _ = a2SurvivingQuarantine(t)
			} else {
				live = assemble(t)
				meta = launchFake(t, live, "ask ordinary-fence-fixture\n")
			}
			// SetStatus cannot resurrect a Lost row. Both controls need an actual running shim.
			f := &a2QuarantineFixture{d: &Daemon{core: live.core, logins: live.logins}, m: meta}
			if row, ok := live.core.Get(meta.ID); !ok || row.Status.Process != status.ProcessRunning {
				t.Fatal("automatic-fence fixture is not actually running")
			}
			if err := f.d.core.SetStatus(f.m.ID, status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}); err != nil {
				t.Fatal(err)
			}
			opened := 0
			up := newFakeUpstream(mustSnap(t, "synthetic"))
			api := &coreAPI{core: f.d.core, tap: newTapManager(func(string) (protocol.SessionStream, error) { opened++; return up, nil })}
			f.d.srv = protocol.NewServer(api, "a2")
			f.d.srv.SetManualInputGateFunc(live.logins.manualInputGate)
			f.d.srv.SetNewTurnGateFunc(live.logins.semanticInputGate)
			t.Cleanup(func() { _ = f.d.srv.Close(); _ = up.Close() })
			err := f.d.sendSupervision(f.m.ID, protocol.SendInputReq{Text: "known attention", Submit: true}, "a2-restored-supervision")
			if chat {
				if err == nil || opened != 0 {
					t.Error("restored supervision opened raw stream")
				}
			} else if err != nil || opened != 1 {
				t.Fatal("ordinary supervision control failed", err)
			}
		})
	}
}

// Complete quiet-policy dependencies, with no prior input or application poller.
// The synthetic in-process shim is stopped via its control socket, NEVER by
// signalling the test PID recorded as its exact process/start identity.
func a2QuietPolicyFixture(t *testing.T) (*Daemon, persist.Meta, func() error) {
	t.Helper()
	root, err := os.MkdirTemp("", "a2cg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := a2QuarantineMeta(t, root, "session", false)
	m.ShimPID = os.Getpid()
	m.ShimStartTime, err = procstart.StartTime(m.ShimPID)
	if err != nil {
		t.Fatal(err)
	}
	m.Status = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	h := startHookShim(t, filepath.Join(root, m.ID), m.ID)
	t.Cleanup(func() { h.kill(t) })
	// Existing helper's private socket is c.sock; ordinary daemon lookup uses shim.sock.
	if err := os.Symlink("c.sock", filepath.Join(root, m.ID, "shim.sock")); err != nil {
		t.Fatal(err)
	}
	cfg := daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "d"), LockPath: filepath.Join(root, "l"), LogPath: filepath.Join(root, "log"), MaxSessions: 2}
	openReader := func() (*daemon.Daemon, error) {
		c, e := daemon.Open(cfg)
		if e == nil {
			t.Cleanup(func() { _ = c.Close() })
		}
		return c, e
	}
	core, err := openReader()
	if err != nil {
		t.Fatal(err)
	}
	loginStore, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loginStore.Close() })
	d := &Daemon{core: core, stateDir: root, eng: engine.New(engine.Config{}), executionStore: store}
	d.api = &coreAPI{core: core}
	d.logins = &loginManager{d: d, store: loginStore}
	d.api.logins = d.logins
	transition := func() error {
		// Counterfactual old prepared dispatch across reader5→6, NOT supported live conversion.
		if err := d.core.Close(); err != nil {
			return err
		}
		path := filepath.Join(root, m.ID, "meta.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		fields["schema_version"] = json.RawMessage("6")
		fields["required_runtime"] = json.RawMessage(`"claude-chat"`)
		raw, err = json.Marshal(fields)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
		next, err := openReader()
		if err != nil {
			return err
		}
		d.core = next
		d.api.core = next
		got, ok := next.Get(m.ID)
		if !ok || got.ID != m.ID || got.ShimPID != m.ShimPID || got.ShimStartTime != m.ShimStartTime || got.Cwd != m.Cwd || got.AgentCwd != m.AgentCwd || got.LoginRuntime == nil || *got.LoginRuntime != *m.LoginRuntime || got.CLIIdentity == nil || *got.CLIIdentity != *m.CLIIdentity || got.SchemaVersion != 6 || got.RequiredRuntime != "claude-chat" {
			return errors.New("reader transition changed exact source identity")
		}
		return nil
	}
	return d, m, transition
}

func TestClaudeChatContextGuardFinalRuntimeFence(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "reader-transition-to-chat"}[change], func(t *testing.T) {
			policy, m, transition := a2QuietPolicyFixture(t)
			if !policy.contextGuardQuiet(m.ID) {
				row, ok := policy.core.Get(m.ID)
				t.Fatalf("ordinary quiet fixture ineligible: found=%v process=%s turn=%s interaction=%s controlled=%v input_refused=%v", ok, row.Status.Process, row.Status.Turn, row.Status.Interaction, policy.anyControlled(m.ID), policy.logins.semanticInputGate(m.ID) != nil)
			}
			rig := newDispatchRig(t)
			var checks, transitions atomic.Int64
			rig.withQuietFunc(t, func() bool {
				eligible := policy.contextGuardQuiet(m.ID)
				n := checks.Add(1)
				// Check1 is promotion; check2 is actual queue-head admission. Only after
				// its REAL true result may the new reader become current before final write.
				if change && n == 2 {
					if !eligible {
						t.Error("ordinary policy lost eligibility before reader transition")
						return false
					}
					if err := transition(); err != nil {
						t.Errorf("controlled reader transition failed: %v", err)
						return false
					}
					transitions.Add(1)
				}
				return eligible
			})
			// Drive the existing reducer/worker into Prepared, not a direct call from Armed.
			rig.ingestUsage(t, 1, 85, 100)
			rig.waitCalls(t, 1)
			awaitContextGuardView(t, rig.manager, "session", func(v protocol.ContextGuardView) bool {
				return v.Phase == "awaiting_confirmation" || v.Phase == "pending_idle"
			})
			wrote := false
			for _, call := range rig.conn.snapshot() {
				wrote = wrote || call.wrote
			}
			if change {
				if transitions.Load() != 1 || checks.Load() < 3 {
					t.Error("reader change did not reach final real policy check")
				}
				if policy.contextGuardQuiet(m.ID) {
					t.Error("new6 reader is still eligible for automatic compaction")
				}
				if wrote {
					t.Error("prepared ContextGuard crossed final runtime fence")
				}
			} else if !wrote {
				t.Error("genuinely eligible ordinary ContextGuard did not write")
			}
		})
	}
}
