package codex

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/pelletier/go-toml/v2"
)

func TestManagerMCPAndFullPolicyReachCodexBackendForFreshAndResume(t *testing.T) {
	manager := &adapter.ManagerSpec{Command: "/private/space 'quote;$()/swarm", Args: []string{"voice", "mcp", "--socket", "/private/s 'x;$/voice.sock", "--auth-file", "/private/s 'x;$/auth", "--conversation", "conversation-1"}}
	for _, resume := range []bool{false, true} {
		var argv []string
		var err error
		if resume {
			argv, err = New().Resume(adapter.ResumeSpec{ConversationID: "019a056e-f192-7961-84cc-b4fa60e47aee", Manager: manager})
		} else {
			argv, err = New().Command(adapter.LaunchSpec{Manager: manager, InitialPrompt: "inspect the repo"})
		}
		if err != nil {
			t.Fatal(err)
		}
		src, _ := adapter.AsBackendSource(New())
		plan, _ := src.Backend(adapter.BackendSpec{SocketPath: "/private/session/codex.sock", AgentArgv: argv})
		joined := strings.Join(plan.Args, "\n")
		for _, want := range []string{"mcp_servers.swarm_voice=", `env_vars=["SWARM_SESSION_ID"]`, `approval_policy="never"`, `sandbox_mode="danger-full-access"`, manager.Command, manager.Args[3], manager.Args[5]} {
			if !strings.Contains(joined, want) {
				t.Fatalf("manager native tool owner missing %q: %q", want, joined)
			}
		}
		for i := 0; i+1 < len(plan.Args); i++ {
			if plan.Args[i] == "-c" {
				var parsed map[string]any
				if err := toml.Unmarshal([]byte(plan.Args[i+1]), &parsed); err != nil {
					t.Fatalf("native config override invalid TOML: %q: %v", plan.Args[i+1], err)
				}
				i++
			}
		}
		if resume {
			for _, arg := range plan.AgentCommandArgs {
				if strings.HasPrefix(arg, "mcp_servers.swarm_voice=") || strings.HasPrefix(arg, "approval_policy=") || strings.HasPrefix(arg, "sandbox_mode=") {
					t.Fatalf("remote resume TUI retained backend-only override: %q", arg)
				}
			}
		}
	}
}

func TestFreshManagerSuppressesOnlyOptionalStartupUpdate(t *testing.T) {
	const updateOverride = "check_for_update_on_startup=false"
	for _, manager := range []*adapter.ManagerSpec{nil, {Command: "/private/swarm"}} {
		argv, err := New().Command(adapter.LaunchSpec{Manager: manager})
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(strings.Join(argv, "\n"), "\n-c\n"+updateOverride); has != (manager != nil) {
			t.Fatalf("manager %t got unexpected startup argv %q", manager != nil, argv)
		}
	}
}

func TestQualifiedManagerVersions(t *testing.T) {
	for version, want := range map[string]bool{"0.167.0": true, "0.170.0": true, "0.169.0": false, "0.170.1": false, "": false} {
		if got := SupportsNativeTurns(version); got != want {
			t.Errorf("version %q: got %v, want %v", version, got, want)
		}
	}
}
