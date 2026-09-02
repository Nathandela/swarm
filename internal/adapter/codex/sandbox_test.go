package codex

// FAILING-FIRST for ADR-010 Amendment 5 F2: every codex argv swarm composes lifts the
// sandbox's network filter, because the swarm CLI dials the daemon over a unix socket
// and codex's workspace-write sandbox blocks connect(2) outright. Measured with
// `codex sandbox` on codex-cli 0.151.0, 2026-09-02:
//
//	swarm ls -> dial unix .../daemon.sock: connect: operation not permitted
//
// so `swarm handoff`, `watch`, `peek` and `send` all failed from a codex source. The
// override is codex's own config key for exactly this, passed as `-c key=value`. It goes
// on the agent argv AND the app-server argv: with a backend the app-server executes the
// commands, and which process's config wins is codex's business, not swarm's.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

const wantNetworkOverride = "sandbox_workspace_write.network_access=true"

func hasConfigOverride(argv []string, kv string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && argv[i+1] == kv {
			return true
		}
	}
	return false
}

func TestSandbox_EveryCodexArgvLiftsTheNetworkFilterForTheSwarmCLI(t *testing.T) {
	a := newAdapter()
	cmd, err := a.Command(adapter.LaunchSpec{Cwd: "/work", Options: map[string]string{"sandbox": "workspace-write"}, InitialPrompt: "go"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	res, err := a.Resume(adapter.ResumeSpec{Cwd: "/work", ConversationID: fixtureConversationID})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	plan := r7Plan(t, r7Sock)
	for name, argv := range map[string][]string{"Command": cmd, "Resume": res, "app-server": plan.Args} {
		if !hasConfigOverride(argv, wantNetworkOverride) {
			t.Errorf("%s argv %v does not carry `-c %s`; the swarm CLI cannot reach the daemon from inside the sandbox without it", name, argv, wantNetworkOverride)
		}
	}
	// The override precedes the positional prompt, or codex reads it as prompt text.
	if last := cmd[len(cmd)-1]; last != "go" {
		t.Errorf("Command argv %v must end with the initial prompt, got %q", cmd, last)
	}
}
