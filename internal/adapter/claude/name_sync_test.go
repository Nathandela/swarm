package claude

import (
	"slices"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

func TestCommandCarriesSwarmSessionNameToClaude(t *testing.T) {
	argv, err := New().Command(adapter.LaunchSpec{
		Cwd:     "/work/proj",
		Name:    "parser cleanup",
		Options: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !slices.ContainsFunc(argv, func(arg string) bool { return arg == "--name" }) {
		t.Fatalf("Command argv = %v, want Claude's --name flag", argv)
	}
	for i := range len(argv) - 1 {
		if argv[i] == "--name" && argv[i+1] == "parser cleanup" {
			return
		}
	}
	t.Fatalf("Command argv = %v, want adjacent --name %q", argv, "parser cleanup")
}

func TestResumeCarriesSwarmSessionNameToClaude(t *testing.T) {
	argv, err := New().Resume(adapter.ResumeSpec{
		Cwd:            "/work/proj",
		ConversationID: "8e4c6620-bfdd-43c3-93de-e69a2f97a01f",
		Name:           "continued parser cleanup",
		Options:        map[string]string{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Reconciled at the merge with main, which made Resume hook-enabled: a resumed
	// session now carries --settings so swarm stays its status observer. The original
	// exact-argv assertion predates that and cannot be restored -- the settings JSON is
	// generated, so equality against a fixed slice can never hold again. The three
	// invariants that matter are pinned instead, and NOTHING this test asserted is
	// dropped: the name still has to reach Claude adjacent to --name, the conversation
	// still has to reach it adjacent to --resume, and the hooks main added must survive.
	adjacent(t, argv, "--name", "continued parser cleanup")
	adjacent(t, argv, "--resume", "8e4c6620-bfdd-43c3-93de-e69a2f97a01f")
	if !slices.Contains(argv, "--settings") {
		t.Fatalf("Resume argv = %v, want the hook settings a resumed session needs", argv)
	}
}

// adjacent asserts argv carries flag immediately followed by value.
func adjacent(t *testing.T, argv []string, flag, value string) {
	t.Helper()
	for i := range len(argv) - 1 {
		if argv[i] == flag && argv[i+1] == value {
			return
		}
	}
	t.Fatalf("argv = %v, want adjacent %s %q", argv, flag, value)
}
