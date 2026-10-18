package skeleton

import (
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

// isHandoffChildLaunch reports the first-class `swarm handoff` shape. The
// supervision mode marks the command -- the hands-off form composes none and
// `swarm spawn` sends none -- while resume options mark a child relaunch whose
// directory resumeDirectory already owns.
func isHandoffChildLaunch(spec daemon.LaunchSpec) bool {
	return spec.SpawnedFrom != "" &&
		spec.SpawnIntent == protocol.SpawnIntentHandoff &&
		spec.Supervision != "" &&
		spec.Options[protocol.OptionResumeFrom] == "" &&
		spec.Options[protocol.OptionResumeConversationID] == ""
}

// handoffDirectory resolves a first-class child's cwd from the source row:
// ProviderCwd, or the source's recorded launch cwd when AgentCwd was never
// captured (the row, not the caller's copy; an absent row is refused by the
// caller).
func handoffDirectory(source persist.Meta) (string, error) {
	// Mirror resumeDirectory: a worktree-flagged row with no captured AgentCwd
	// names no trustworthy checkout.
	if source.LaunchOptions[protocol.OptionWorktree] == "true" && source.AgentCwd == "" {
		return "", fmt.Errorf("handoff: source worktree path was not captured; refusing to hand off in another checkout")
	}
	providerCwd := source.ProviderCwd()
	if providerCwd == "" {
		return source.Cwd, nil
	}
	if fi, err := os.Stat(providerCwd); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("handoff: the source's working directory %q is no longer a directory, so the successor has nowhere to continue", providerCwd)
	}
	return providerCwd, nil
}
