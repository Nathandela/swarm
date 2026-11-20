package daemon

import (
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestManagerClaudeEnrollsNativeBrokerWithDiscordDisabled(t *testing.T) {
	cfg := daemonConfig(t)
	cfg.NativeInputEnabled = func() bool { return false }
	d := openDaemon(t, cfg)
	killAllSessions(t, d)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.AgentType = "claude"
	spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: "2.1.319"}
	spec.Manager = &ManagerLaunchSpec{ConversationID: "voice-conversation", BridgeSocket: "/private/service/voice.sock", AuthFile: "/private/service/auth", Command: cfg.ShimBinary}
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(cfg.StateDir, m.ID, shimLaunchConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var lc shimSpawnConfig
	if err := json.Unmarshal(body, &lc); err != nil {
		t.Fatal(err)
	}
	if !lc.ClaudeInput {
		t.Fatal("manager native broker enrollment depends on Discord toggle")
	}
	if enrolled, err := d.SessionClaudeInputEnrollment(m.ID); err != nil || !enrolled {
		t.Fatalf("durable manager broker proof = %v, %v", enrolled, err)
	}
	if m.Role != "manager" || m.ManagerConversationID != "voice-conversation" {
		t.Fatalf("manager role absent: %+v", m)
	}
}

func TestManagerCodexVersionQualificationPrecedesEveryLaunchEffect(t *testing.T) {
	for _, version := range []string{"nil", "", "0.166.0", "0.168.0", "0.167.0"} {
		t.Run(version, func(t *testing.T) {
			effects := 0
			cfg := daemonConfig(t)
			stop := errors.New("fixture stops before worktree/native effects")
			cfg.PreLaunch = func(string, LaunchSpec) (string, error) { effects++; return "", stop }
			d := openDaemon(t, cfg)
			spec := announceSpec(t, t.TempDir()+"/pid")
			spec.AgentType = "codex"
			spec.CLIIdentity = nil
			if version != "nil" {
				spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: version}
			}
			spec.Manager = &ManagerLaunchSpec{ConversationID: "manager", Command: cfg.ShimBinary}
			_, err := d.Launch(spec)
			if version == "0.167.0" {
				if effects != 1 || !errors.Is(err, stop) {
					t.Fatalf("qualified runtime never reached sentinel: count=%d err=%v", effects, err)
				}
				return
			}
			if effects != 0 || err == nil || len(d.List()) != 0 {
				t.Fatalf("unqualified runtime had effects: count=%d sessions=%d err=%v", effects, len(d.List()), err)
			}
		})
	}
}

func TestManagerCodexCannotLaunchWithoutItsNativeBackend(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	killAllSessions(t, d)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.AgentType = "codex"
	spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: "0.167.0"}
	spec.Manager = &ManagerLaunchSpec{ConversationID: "voice-conversation", Command: d.ManagerBridgeCommand()}
	if _, err := d.Launch(spec); err == nil {
		t.Fatal("manager launched without required app-server backend")
	}
}

func TestManagerClaudeRequiresQualifiedNativeTurns(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	killAllSessions(t, d)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.AgentType = "claude"
	spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: "2.1.296"}
	spec.Manager = &ManagerLaunchSpec{ConversationID: "voice-conversation", Command: d.ManagerBridgeCommand()}
	if _, err := d.Launch(spec); err == nil {
		t.Fatal("manager accepted legacy input-only runtime without native-turn lifecycle")
	}
}

func TestManagerReplayBootstrapPrecedesNativeActivity(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			d := openDaemon(t, daemonConfig(t))
			spec := announceSpec(t, t.TempDir()+"/pid")
			spec.AgentType = provider
			version := "2.1.319"
			if provider == "codex" {
				version = "0.167.0"
			}
			spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: version}
			spec.Manager = &ManagerLaunchSpec{ConversationID: "replay-conversation", Command: d.cfg.ShimBinary}
			sentinel := errors.New("stop before native spawn")
			_, err := d.launch(spec, func(phase launchPhase, m persist.Meta) error {
				if phase != phaseReserved {
					return nil
				}
				if m.ManagerReplayID == "" {
					t.Fatal("manager reservation lacks durable replay identity")
				}
				r, err := managerproof.OpenReplay(filepath.Join(d.cfg.StateDir, m.ID), m.ID, m.ManagerConversationID, m.ManagerReplayID)
				if err != nil {
					t.Fatal(err)
				}
				_ = r.Close()
				return sentinel
			})
			if !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeCLIManagerLaunchUsesActualOwnerEndpoint(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	killAllSessions(t, d)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.AgentType = "claude"
	spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: "2.1.319"}
	spec.Manager = &ManagerLaunchSpec{NativeCLI: true, ConversationID: "discord-teammate-test", Command: cfg.ShimBinary}
	spec.ClientEnv = append(spec.ClientEnv, EnvStateDir+"=/wrong-state", EnvSocket+"=/wrong-socket")
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(cfg.StateDir, m.ID, shimLaunchConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var lc shimSpawnConfig
	if err = json.Unmarshal(body, &lc); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{schema.EnvManagerVMCLI + "=1", EnvStateDir + "=" + cfg.StateDir, EnvSocket + "=" + cfg.SocketPath, EnvLock + "=" + cfg.LockPath, EnvLog + "=" + cfg.LogPath, hookclient.EnvSessionID + "=" + m.ID} {
		if !slices.Contains(lc.Env, entry) {
			t.Errorf("missing manager environment %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
	if !m.ManagerNativeCLI || slices.Contains(lc.Env, EnvStateDir+"=/wrong-state") || slices.Contains(lc.Env, EnvSocket+"=/wrong-socket") {
		t.Fatal("wrong launcher identity")
	}
}
