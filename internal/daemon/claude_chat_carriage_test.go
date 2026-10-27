package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestClaudeChatReservationIsolatedFromOpenCodeAndRetainsReplayUUID(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	spec := LaunchSpec{AgentType: "fake", Cwd: t.TempDir(), ClientEnv: []string{"PATH=/usr/bin", "OPENCODE_TUI_CONFIG=/not-an-opencode-launch"}, RequiredRuntime: "claude-chat", ClaudeChatFixture: "/fixture/fake", Argv: []string{"/fixture/fake", "--claude-primary-fixture"}, Options: map[string]string{"claude_interface": "swarm-chat"}, OperationID: "dev-chat-reservation"}
	reserved := errors.New("private reservation boundary")
	probes := 0
	m, err := d.launch(spec, func(phase launchPhase, _ persist.Meta) error {
		if phase == phaseReserved {
			probes++
			return reserved
		}
		return nil
	})
	if !errors.Is(err, reserved) || probes != 1 {
		t.Fatalf("chat inherited OpenCode configuration or missed reserve: probes=%d err=%v", probes, err)
	}
	memory, ok := d.Get(m.ID)
	disk, e := d.store.Load(m.ID)
	if !ok || e != nil || memory.SchemaVersion != 6 || disk.SchemaVersion != 6 || !adapter.IsCanonicalConversationID(m.ConversationID) || memory.ConversationID != m.ConversationID || disk.ConversationID != m.ConversationID {
		t.Fatal("canonical reservation lost one native UUID", e)
	}
	path := filepath.Join(d.cfg.StateDir, m.ID, "meta.json")
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, err := d.launch(spec, func(launchPhase, persist.Meta) error { probes++; return nil }); !errors.Is(err, ErrLaunchOutcomeUnknown) {
		t.Fatalf("inflight replay redrove or fabricated success: %v", err)
	}
	after, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(before, after) || probes != 1 || len(d.List()) != 1 {
		t.Fatal("inflight replay changed saved UUID/reservation", e)
	}
	// Same origin must remain refused for a genuine OpenCode selection.
	openCode := LaunchSpec{AgentType: "opencode", Cwd: t.TempDir(), ClientEnv: spec.ClientEnv, RequiredRuntime: persist.RequiredRuntimeOpenCode}
	if _, err := d.launch(openCode, nil); err == nil || !strings.Contains(err.Error(), "OpenCode") {
		t.Fatalf("OpenCode selection guard was bypassed: %v", err)
	}
}

func TestClaudeChatRuntimeDispatchUsesExactValidator(t *testing.T) {
	for _, kind := range []string{"ordinary", "opencode", "chat", "unknown", "chat-invalid-argv", "chat-mixed-binding", "ordinary-mixed-binding", "opencode-wrong-provider", "ordinary-chat-selector", "opencode-chat-selector"} {
		t.Run(kind, func(t *testing.T) {
			d := openDaemon(t, daemonConfig(t))
			dir := filepath.Join(d.cfg.StateDir, "planned")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			const uuid = "10000000-0000-4000-8000-000000000001"
			spec := LaunchSpec{AgentType: "fake", RequiredRuntime: "claude-chat", ConversationID: uuid, Cwd: t.TempDir(), ClaudeChatFixture: "/fixture/fake", Argv: []string{"/fixture/fake", "--claude-primary-fixture", "--session-id=" + uuid}}
			want := kind == "ordinary" || kind == "opencode" || kind == "chat"
			switch kind {
			case "ordinary":
				spec.RequiredRuntime = ""
				spec.ConversationID = ""
				spec.ClaudeChatFixture = ""
				spec.Argv = []string{"/fixture/fake"}
			case "opencode":
				spec.RequiredRuntime = persist.RequiredRuntimeOpenCode
				spec.AgentType = "opencode"
				spec.ConversationID = ""
				spec.ClaudeChatFixture = ""
				spec.Argv = []string{"/native/opencode"}
				spec.OpenCodeEnv = []string{"HOME=" + t.TempDir()}
			case "unknown":
				spec.RequiredRuntime = "unknown-runtime"
			case "chat-invalid-argv":
				spec.Argv[0] = "/fixture/other"
			case "chat-mixed-binding":
				spec.OpenCodeBinding = &persist.OpenCodeBinding{}
			case "ordinary-mixed-binding":
				spec.RequiredRuntime = ""
				spec.ConversationID = ""
				spec.ClaudeChatFixture = ""
				spec.OpenCodeBinding = &persist.OpenCodeBinding{}
			case "opencode-wrong-provider":
				spec.RequiredRuntime = persist.RequiredRuntimeOpenCode
				spec.ConversationID = ""
				spec.ClaudeChatFixture = ""
				spec.Argv = []string{"/fixture/fake"}
			case "ordinary-chat-selector":
				spec.RequiredRuntime = ""
			case "opencode-chat-selector":
				spec.RequiredRuntime = persist.RequiredRuntimeOpenCode
				spec.AgentType = "opencode"
			}
			meta := persist.Meta{ID: "planned", AgentType: spec.AgentType, Cwd: spec.Cwd, RequiredRuntime: spec.RequiredRuntime, ConversationID: spec.ConversationID}
			err := d.prepareLaunchRuntime(&spec, &meta, dir)
			if (err == nil) != want {
				t.Fatalf("runtime dispatch accepted=%t want=%t: %v", err == nil, want, err)
			}
			if kind == "opencode" {
				if spec.OpenCodeBinding == nil || spec.OpenCodeLaunch == nil || meta.RequiredRuntime != persist.RequiredRuntimeOpenCode {
					t.Fatal("real OpenCode validator did not prepare its native binding")
				}
			} else {
				entries, e := os.ReadDir(dir)
				if e != nil || len(entries) != 0 {
					t.Fatal("non-OpenCode dispatch created OpenCode artifacts", e)
				}
			}
		})
	}
	// Unknown runtime must also refuse before launch reserves any row or directory.
	d := openDaemon(t, daemonConfig(t))
	if _, err := d.launch(LaunchSpec{AgentType: "fake", RequiredRuntime: "unknown-runtime", Cwd: t.TempDir(), Argv: []string{"/fixture/fake"}}, nil); err == nil || !strings.Contains(err.Error(), "unknown required runtime") {
		t.Fatal("unknown runtime fell back to ordinary launch", err)
	}
	if len(d.List()) != 0 {
		t.Fatal("unknown runtime reserved a session")
	}
}
