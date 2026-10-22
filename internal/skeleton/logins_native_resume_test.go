package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/status"
)

func TestProviderSwitchUsesNativeCodexModelForModernRolloutLayouts(t *testing.T) {
	for _, layout := range []string{"physical-only", "compressed-only", "original-and-new-physical", "native-index-only"} {
		t.Run(layout, func(t *testing.T) {
			f := newLoginFixture(t)
			source := f.rows["source-1"]
			source.Status.Process = status.ProcessExited
			var original string
			if err := filepath.WalkDir(f.runtime.Home, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), source.ConversationID+".jsonl") {
					original = path
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if original == "" {
				t.Fatal("missing native fixture")
			}
			switch layout {
			case "physical-only":
				if err := os.Rename(original, strings.TrimSuffix(original, ".jsonl")+"_523e4567-e89b-42d3-a456-426614174013.jsonl"); err != nil {
					t.Fatal(err)
				}
			case "compressed-only":
				// Swarm must not parse compressed bytes to restore a captured native thread.
				if err := os.Remove(original); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(original+".zst", []byte("native-owned compressed history"), 0600); err != nil {
					t.Fatal(err)
				}
			case "original-and-new-physical":
				if err := os.WriteFile(strings.TrimSuffix(original, ".jsonl")+"_523e4567-e89b-42d3-a456-426614174013.jsonl", []byte("native selects the current physical rollout"), 0600); err != nil {
					t.Fatal(err)
				}
			case "native-index-only":
				if err := os.Remove(original); err != nil {
					t.Fatal(err)
				}
			}
			options, err := switchResumeOptions(source, f.runtime, "machine")
			if err != nil {
				t.Fatal(err)
			}
			if model, present := options["model"]; !present || model != "" {
				t.Fatal("stale launch model was not explicitly cleared")
			}
			spec, err := composeLaunchSpec(daemon.LaunchSpec{AgentType: "codex", Options: options, LoginRuntime: &f.runtime}, "machine", "", srcGetter(source.ID, source), stubLookPath)
			if err != nil {
				t.Fatal(err)
			}
			if spec.ConversationID != source.ConversationID || spec.Cwd != source.ProviderCwd() {
				t.Fatal("native identity/cwd changed")
			}
			if strings.Contains(strings.Join(spec.Argv, " "), "--model") || spec.Options["model"] != "" {
				t.Fatalf("native saved model overridden: %v", spec.Argv)
			}
			// A captured exact ID is authoritative; historical identity recovery must not
			// inspect rollout filenames, compression, or timestamp heuristics here.
			api := &coreAPI{historyResolver: &fakeResumeHistoryResolver{result: resumeHistoryResult{Outcome: resumeHistoryUnsafe}}}
			if err = api.ensureResumeConversationID(source.ID, source); err != nil {
				t.Fatal(err)
			}
		})
	}
}
