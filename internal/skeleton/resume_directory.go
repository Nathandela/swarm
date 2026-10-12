package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func validateResumeRelocation(spec daemon.LaunchSpec) error {
	if relocate, present := spec.Options[protocol.OptionResumeRelocate]; present {
		if relocate != "true" || spec.Options[protocol.OptionResumeFrom] == "" || spec.Options[protocol.OptionResumeConversationID] != "" {
			return fmt.Errorf("resume: resume_relocate requires an owner-tier linked resume")
		}
	}
	return nil
}

// resumeDirectory preserves the original checkout unless the owner explicitly
// chooses relocation. The source metadata remains unchanged for history lookup.
func resumeDirectory(spec daemon.LaunchSpec, source persist.Meta) (string, error) {
	if spec.Options[protocol.OptionResumeRelocate] == "true" {
		if !filepath.IsAbs(spec.Cwd) {
			return "", fmt.Errorf("resume: relocation directory must be absolute")
		}
		info, err := os.Stat(spec.Cwd)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("resume: relocation directory %q is not an existing directory", spec.Cwd)
		}
		return filepath.Clean(spec.Cwd), nil
	}
	if source.LaunchOptions[protocol.OptionWorktree] == "true" && source.AgentCwd == "" {
		return "", fmt.Errorf("resume: source worktree path was not captured; refusing to resume in another checkout")
	}
	cwd := spec.Cwd
	if saved := source.ProviderCwd(); saved != "" && (spec.AgentType == "codex" || source.AgentCwd != "") {
		cwd = saved
		if len(cwd) > 1 {
			cwd = strings.TrimRight(cwd, string(filepath.Separator))
			if cwd == "" {
				cwd = string(filepath.Separator)
			}
		}
	}
	if source.AgentCwd != "" {
		if info, err := os.Stat(source.AgentCwd); err != nil || !info.IsDir() {
			return "", fmt.Errorf("resume: source checkout %q is no longer available", source.AgentCwd)
		}
	}
	return cwd, nil
}
