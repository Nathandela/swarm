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
	want := []string{"claude", "--resume", "8e4c6620-bfdd-43c3-93de-e69a2f97a01f", "--name", "continued parser cleanup"}
	if !slices.Equal(argv, want) {
		t.Fatalf("Resume argv = %v, want %v", argv, want)
	}
}
