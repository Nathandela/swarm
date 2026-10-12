package skeleton

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestResumeRelocationPreservesConversationAndContext(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		for _, worktree := range []bool{false, true} {
			t.Run(agent+"/worktree="+strconv.FormatBool(worktree), func(t *testing.T) {
				source := endedCodexSource("source", map[string]string{"model": "saved-model"})
				source.AgentType = agent
				source.Cwd = filepath.Join(t.TempDir(), "missing-repo")
				if worktree {
					source.AgentCwd = filepath.Join(t.TempDir(), "removed-worktree")
					source.LaunchOptions[protocol.OptionWorktree] = "true"
				}
				source.Name, source.Tag = "Saved label", "WIP"
				source.SpawnedFrom, source.SpawnIntent, source.Supervision = "parent", "handoff", "passive"
				destination := t.TempDir()
				spec := daemon.LaunchSpec{AgentType: agent, Cwd: destination, Options: map[string]string{
					protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID),
					"resume_relocate":         "true",
				}}
				got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath)
				if err != nil {
					t.Fatal(err)
				}
				if got.Cwd != destination || got.ResumedFrom != source.ID || got.ConversationID != source.ConversationID || got.Name != source.Name || got.Tag != source.Tag || got.SpawnedFrom != source.SpawnedFrom || got.Supervision != source.Supervision {
					t.Fatalf("relocation lost continuity: %+v", got)
				}
				if !argvContains(got.Argv, source.ConversationID) || got.Options["model"] != "saved-model" || got.Options[protocol.OptionWorktree] != "" {
					t.Fatalf("relocation changed the conversation or options: %+v", got)
				}
				if agent == "codex" && (!argvContains(got.Argv, "--cd") || !argvContains(got.Argv, destination)) {
					t.Fatalf("Codex must explicitly override the saved thread cwd: %v", got.Argv)
				}
			})
		}
	}
}

func TestResumeRelocationRejectsInvalidDestination(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	source := endedCodexSource("source", nil)
	for _, destination := range []string{"", "relative", filepath.Join(dir, "missing"), file} {
		t.Run(destination, func(t *testing.T) {
			spec := daemon.LaunchSpec{AgentType: "codex", Cwd: destination, Options: map[string]string{
				protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID),
				"resume_relocate":         "true",
			}}
			if _, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err == nil {
				t.Fatal("invalid relocation destination accepted")
			}
		})
	}
}

func TestResumeRelocationNeverChains(t *testing.T) {
	source := endedCodexSource("source", map[string]string{"resume_relocate": "true"})
	source.Cwd = t.TempDir()
	spec := daemon.LaunchSpec{AgentType: "codex", Cwd: "/wrong", Options: map[string]string{
		protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID),
	}}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Options["resume_relocate"] != "" || got.Cwd != source.Cwd || argvContains(got.Argv, "--cd") {
		t.Fatalf("an earlier relocation changed a later ordinary resume: %+v", got)
	}
}
