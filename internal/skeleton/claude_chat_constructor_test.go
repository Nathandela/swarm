package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestClaudeChatConstructorConnectedLaunchCaptureStop(t *testing.T) {
	d := assemble(t)
	script := filepath.Join(t.TempDir(), "ordinary-fallback.txt")
	if err := os.WriteFile(script, []byte("ask must-not-use-PTY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := daemon.LaunchSpec{AgentType: "fake", Cwd: t.TempDir(), Options: map[string]string{"claude_interface": "swarm-chat", "script": script}, ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24, OperationID: "dev-chat-constructor"}
	m, err := d.api.Launch(spec)
	if m.ID != "" {
		t.Cleanup(func() { _ = d.core.Kill(m.ID) })
	}
	if err != nil {
		t.Fatalf("actual dev-only constructor launch refused: %v", err)
	}
	if m.RequiredRuntime != "claude-chat" || !adapter.IsCanonicalConversationID(m.ConversationID) {
		t.Fatalf("launch fell back to PTY or lost returned native identity: %+v", m)
	}
	// Launch returns its local value; canonical writer stamps belong to registry/disk.
	memory, ok := d.core.Get(m.ID)
	store, e := persist.NewStore(d.stateDir)
	if e != nil {
		t.Fatal(e)
	}
	disk, e := store.Load(m.ID)
	if !ok || e != nil || memory.SchemaVersion != 6 || disk.SchemaVersion != 6 {
		t.Fatalf("canonical chat reservation missing: memoryVersion=%d diskVersion=%d found=%t err=%v", memory.SchemaVersion, disk.SchemaVersion, ok, e)
	}
	if m.ShimPID <= 0 || m.ShimStartTime <= 0 {
		t.Fatal("launch returned no exact shim identity")
	}
	for _, row := range []persist.Meta{m, memory, disk} {
		if row.ID != m.ID || row.AgentType != "fake" || row.RequiredRuntime != m.RequiredRuntime || row.ConversationID != m.ConversationID || row.ShimPID != m.ShimPID || row.ShimStartTime != m.ShimStartTime {
			t.Fatal("returned/registry/durable chat identity diverged")
		}
		if row.LoginRuntime != nil || row.CLIIdentity != nil || row.ClaudeRetirement != nil {
			t.Fatal("fake constructor acquired managed authority")
		}
	}
	// Check the actual daemon-written config, including its daemon-owned storage root.
	launchRaw, e := os.ReadFile(filepath.Join(d.stateDir, m.ID, "shim-launch.json"))
	if e != nil {
		t.Fatal(e)
	}
	var launch struct {
		Root     string   `json:"login_state_root"`
		Dir      string   `json:"session_dir"`
		Runtime  string   `json:"required_runtime"`
		NativeID string   `json:"conversation_id"`
		Fixture  string   `json:"claude_chat_fixture"`
		Argv     []string `json:"argv"`
	}
	if e := json.Unmarshal(launchRaw, &launch); e != nil {
		t.Fatal(e)
	}
	if launch.Root != d.stateDir || launch.Dir != filepath.Join(d.stateDir, m.ID) || launch.Runtime != m.RequiredRuntime || launch.NativeID != m.ConversationID || launch.Fixture != d.api.fakeAgentBin || len(launch.Argv) != 3 || launch.Argv[0] != launch.Fixture || launch.Argv[1] != "--claude-primary-fixture" || launch.Argv[2] != "--session-id="+m.ConversationID {
		t.Fatal("daemon-owned chat config binding was lost or replaced by caller input")
	}
	if _, ok := d.core.SessionBackend(m.ID); ok {
		t.Fatal("stdio primary launched a sidecar")
	}
	replayed, e := d.api.Launch(spec)
	if e != nil || replayed.ID != m.ID || replayed.ConversationID != m.ConversationID || replayed.ShimPID != m.ShimPID || replayed.ShimStartTime != m.ShimStartTime || len(d.core.List()) != 1 {
		t.Fatal("settled replay changed native identity or started another primary", e)
	}

	var initSeen bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !initSeen {
		response, e := shim.ReadHookSpoolFileResponse(filepath.Join(d.stateDir, m.ID, shim.HookSpoolFile), 0)
		if e == nil {
			for _, rec := range response.Records {
				var envelope struct {
					V               int    `json:"v"`
					Origin          string `json:"origin"`
					NativeSessionID string `json:"native_session_id"`
					Raw             []byte `json:"raw"`
					ReadNS          int64  `json:"read_monotonic_ns"`
					Sequence        uint64 `json:"stream_sequence"`
					Primary         struct {
						LocalID   string `json:"session_id"`
						Runtime   string `json:"required_runtime"`
						NativeID  string `json:"native_session_id"`
						ShimPID   int    `json:"shim_pid"`
						ShimStart int64  `json:"shim_start_time"`
						PID       int    `json:"primary_pid"`
						Start     int64  `json:"primary_start_time"`
					} `json:"primary"`
				}
				if json.Unmarshal(rec.Body, &envelope) != nil || envelope.Origin != "claude_primary" {
					continue
				}
				var frame struct {
					Type      string `json:"type"`
					Subtype   string `json:"subtype"`
					SessionID string `json:"session_id"`
					Stdio     *struct {
						StdinTTY  bool `json:"stdin_tty"`
						StdoutTTY bool `json:"stdout_tty"`
						StderrTTY bool `json:"stderr_tty"`
					} `json:"fixture_stdio"`
				}
				if err := json.Unmarshal(envelope.Raw, &frame); err != nil {
					t.Fatal(err)
				}
				if envelope.V != 1 || envelope.NativeSessionID != m.ConversationID || frame.SessionID != m.ConversationID || envelope.ReadNS <= 0 || envelope.Sequence == 0 || envelope.Primary.LocalID != m.ID || envelope.Primary.Runtime != "claude-chat" || envelope.Primary.NativeID != m.ConversationID || envelope.Primary.ShimPID != m.ShimPID || envelope.Primary.ShimStart != m.ShimStartTime || envelope.Primary.PID <= 0 || envelope.Primary.Start <= 0 {
					t.Fatal("captured primary identity mismatch")
				}
				if frame.Type == "user" {
					t.Fatal("constructor submitted an automatic user message")
				}
				if frame.Type == "system" && frame.Subtype == "init" {
					if frame.Stdio == nil || frame.Stdio.StdinTTY || frame.Stdio.StdoutTTY || frame.Stdio.StderrTTY {
						t.Fatal("primary stdio still owns terminal endpoints")
					}
					initSeen = true
				}
			}
		}
		if !initSeen {
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !initSeen {
		t.Fatal("actual primary stdout did not reach retained spool")
	}
	a2AssertPassiveCapabilities(t, d, m.ID)
	client := dialClient(t, d)
	namespaced := protocol.NamespacedID(d.api.endpointID, m.ID)
	if att, err := client.Attach(namespaced); err == nil {
		_ = att.Detach()
		t.Fatal("raw attach fallback admitted")
	}
	if err := client.Kill(namespaced); err != nil {
		t.Fatalf("owner Stop refused: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if row, ok := d.core.Get(m.ID); ok && row.Status.Process != status.ProcessRunning {
			if _, err := os.Stat(filepath.Join(d.stateDir, m.ID, shim.ExitFile)); err == nil {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("real shim did not finish primary Stop/exit publication")
}
