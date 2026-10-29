package skeleton

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func a2AssertPassiveCapabilities(t *testing.T, d *Daemon, id string) {
	t.Helper()
	c, ok := d.sessionCapabilities(id)
	if ok && (c.NativeComposer || c.NativeInteractionVersion != 0 || c.StructuredChat || c.TerminalFallback || c.TerminalControl || c.Interrupt) {
		t.Fatalf("quarantine retained active capabilities: %#v", c)
	}
	if !ok {
		t.Error("recognized runtime must publish an explicit passive capability row")
	}
}

func TestClaudeChatQuarantineRunningAndEndedStartup(t *testing.T) {
	for _, phase := range []string{"running-registration-before-core", "ended-startup"} {
		t.Run(phase, func(t *testing.T) {
			f := a2NewQuarantineFixture(t, true)
			before := a2Snapshot(t, f.paths)
			if phase == "running-registration-before-core" {
				core := f.d.core
				f.d.core = nil
				m := f.m
				m.Status.Process = status.ProcessRunning
				f.d.registerSession(m, "a2-token") // Actual reconcile callback receives mode while core=nil.
				func() {
					defer func() {
						if caught := recover(); caught != nil {
							t.Errorf("quarantined early callback panicked: %T", caught)
						}
					}()
					if err := f.d.ingestHookBytes(f.raw); err == nil {
						t.Error("early callback registered ordinary hook authority")
					}
				}()
				f.d.core = core
				f.d.authorCapabilitiesForRunning()
				a2Unchanged(t, before)
				return
			}
			// Actual Serve enumerates terminal rows independently of OnSessionStart.
			if err := f.d.core.Close(); err != nil {
				t.Fatal(err)
			}
			assembled := assembleAt(t, f.d.stateDir)
			t.Cleanup(func() { _ = assembled.Close() })
			assembled.startHookDrainsForRunning()
			assembled.retryHookRetirements()
			if err := assembled.Close(); err != nil {
				t.Fatal(err)
			}
			a2Unchanged(t, before)
		})
	}
}

// A real old-mode synthetic shim survives daemon Close. Only its persisted row is
// authored as managed6 for reader tests; this grants NO native/profile birth proof.
func a2SurvivingQuarantine(t *testing.T) (*Daemon, persist.Meta, []string) {
	t.Helper()
	initial := assemble(t)
	ordinary := launchFake(t, initial, "ask retained-fixture\n")
	root := initial.stateDir
	channel, ok := initial.core.SessionHookChannel(ordinary.ID)
	if !ok {
		t.Fatal("fixture has no hook channel")
	}
	// Launch confirms control readiness; this fixture also needs the later hook listener.
	func() {
		ready := false
		defer func() {
			if ready {
				return
			}
			log, err := os.Open(filepath.Join(root, "d.log"))
			if err != nil {
				t.Logf("hook readiness log: %v", err)
				return
			}
			defer func() { _ = log.Close() }()
			if info, err := log.Stat(); err == nil && info.Size() > 4096 {
				_, _ = log.Seek(-4096, io.SeekEnd)
			}
			tail, err := io.ReadAll(io.LimitReader(log, 4096))
			t.Logf("hook readiness daemon/shim log tail: %s (read error: %v)", tail, err)
		}()
		waitForSocket(t, channel.SocketPath)
		ready = true
	}()
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	m := a2QuarantineMeta(t, root, ordinary.ID, true)
	m.ShimPID, m.ShimStartTime = ordinary.ShimPID, ordinary.ShimStartTime
	m.Cwd = ordinary.Cwd
	m.Status.Process = status.ProcessRunning
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, m.ID, "meta.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(m.ID); err != nil || got.RequiredRuntime != "claude-chat" {
		t.Fatal("metadata prerequisite failed", err)
	}
	record := persist.ExecutionRecord{Version: persist.ExecutionVersion, SessionID: m.ID, Incarnation: executionIncarnation(m), OwnerRevision: 7, MainTurn: status.TurnIdle, Interaction: status.InteractionNone, UnknownActors: true, Incomplete: "quarantine-pending", Actors: map[string]persist.ExecutionActor{"known-child": {Active: true, Sequence: 2, StartSequence: 1}}}
	if err := store.SaveExecution(record); err != nil {
		t.Fatal("execution custody prerequisite", err)
	}
	paths := []string{filepath.Join(root, m.ID, "hooks.spool"), filepath.Join(root, m.ID, "hooks.spool.floor"), filepath.Join(root, m.ID, "hook.fold"), filepath.Join(root, m.ID, "hook.fold.cert"), filepath.Join(root, m.ID, "hook.fold.gap")}
	// Existing producer accepts the hook while no daemon reader is running.
	// No test overwrites the producer's spool or takes its lock.
	if ack, err := hookclient.PostToShim(channel.SocketPath, engine.Callback{SessionID: m.ID, Token: "a2-unmapped-token", Sequence: 123, Event: "PostToolUse"}); err != nil || !ack {
		t.Fatal("retained hook was not accepted", err)
	}
	beforeStartup := a2Snapshot(t, paths)
	restarted := assembleAt(t, root)
	t.Cleanup(func() { _ = restarted.core.Kill(m.ID); _ = restarted.Close() })
	a2Unchanged(t, beforeStartup)
	return restarted, m, paths
}

func TestClaudeChatQuarantineSurvivesDaemonCloseAndAdoption(t *testing.T) {
	d, m, paths := a2SurvivingQuarantine(t)
	before := a2Snapshot(t, paths)
	if got, ok := d.core.Get(m.ID); !ok || got.ShimPID != m.ShimPID || got.ShimStartTime != m.ShimStartTime {
		t.Fatal("reader did not retain same live synthetic scope")
	}
	a2AssertPassiveCapabilities(t, d, m.ID)
	custody, err := d.eng.ExecutionSnapshot(m.ID)
	if err != nil || !custody.Actors["known-child"].Active || custody.Incomplete == "" || custody.OwnerRevision != 7 {
		t.Fatal("startup erased or falsely completed retained custody", err)
	}
	d.drains.mu.Lock()
	_, draining := d.drains.drainers[m.ID]
	d.drains.mu.Unlock()
	if draining {
		t.Error("startup registered an ordinary hook drainer")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	again := assembleAt(t, d.stateDir)
	t.Cleanup(func() { _ = again.core.Kill(m.ID); _ = again.Close() })
	a2AssertPassiveCapabilities(t, again, m.ID)
	a2Unchanged(t, before)
}

func TestClaudeChatUnboundQuarantineStopRemainsUnconfirmed(t *testing.T) {
	d, m, paths := a2SurvivingQuarantine(t)
	before := a2Snapshot(t, append(paths, filepath.Join(d.stateDir, m.ID, "meta.json"), filepath.Join(d.stateDir, m.ID, "execution.json")))
	client := dialClient(t, d)
	id := protocol.NamespacedID(d.api.endpointID, m.ID)
	if att, err := client.Attach(id); err == nil {
		_ = att.Detach()
		t.Error("owner raw Attach accepted live quarantine")
	}
	if err := client.SendInput(id, protocol.SendInputReq{Text: "blocked draft"}); err == nil {
		t.Error("owner raw input accepted live quarantine")
	}
	direct := protocol.FromDaemon(d.core)
	directServer := protocol.NewServer(direct, "a2-direct")
	defer func() { _ = directServer.Close() }() // Closes adapter event source as well as server workers.
	if stream, err := direct.Attach(m.ID); err == nil {
		_ = stream.Close()
		t.Error("direct daemon adapter opened quarantined stream")
	}
	if _, err := d.api.SampleSnapshot(m.ID); err == nil {
		t.Error("direct raw sampler accepted live quarantine")
	}
	if started, err := procstart.StartTime(m.ShimPID); err != nil || started != m.ShimStartTime {
		t.Fatal("owner Stop fixture lost its exact live shim before Kill", err)
	}
	startedAt := time.Now()
	if err := client.Kill(id); err == nil || !strings.Contains(err.Error(), "unconfirmed") {
		t.Fatalf("unbound old shim must remain unconfirmed: %v", err)
	}
	if time.Since(startedAt) > 3*time.Second {
		t.Fatal("unbound Stop waited on unrelated lifecycle work")
	}
	if started, err := procstart.StartTime(m.ShimPID); err != nil || started != m.ShimStartTime || syscall.Kill(m.ShimPID, 0) != nil {
		t.Fatal("unbound Stop signalled or lost the exact retained shim", err)
	}
	// This old-mode synthetic shim has no native birth/Stop binding. Test-owned
	// cleanup is registered by a2SurvivingQuarantine, outside owner Stop authority.
	a2Unchanged(t, before)
}
