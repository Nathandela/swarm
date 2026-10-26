package skeleton

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/worktree"
)

// handoffTemporaryRoots names the directories a reboot may erase. An agent handoff's
// worktree must never land beneath one (ADR-010 Amendment 10). A variable so tests,
// whose fixtures all live under the OS temp dir, can stand for a durable checkout.
var handoffTemporaryRoots = func() []string { return []string{os.TempDir(), "/tmp"} }

// isAgentHandoffLaunch reports a launch one agent made to hand work to another: a
// handoff or delegation with a lineage parent, never a resume of either.
func isAgentHandoffLaunch(spec daemon.LaunchSpec) bool {
	return spec.SpawnedFrom != "" &&
		(spec.SpawnIntent == protocol.SpawnIntentHandoff || spec.SpawnIntent == protocol.SpawnIntentDelegate) &&
		spec.Options[protocol.OptionResumeFrom] == "" &&
		spec.Options[protocol.OptionResumeConversationID] == ""
}

// isolateHandoffChild gives a first-class handoff child a worktree of its own when its
// checkout is a git work tree (ADR-010 Amendment 10). The worktree starts at that
// checkout's HEAD; uncommitted changes there stay with the source. A checkout outside
// git has nothing to isolate and is returned unchanged. The caller's map is not mutated.
func isolateHandoffChild(spec daemon.LaunchSpec) daemon.LaunchSpec {
	if spec.Options[protocol.OptionWorktree] == "true" || !worktree.IsRepo(spec.Cwd) {
		return spec
	}
	opts := maps.Clone(spec.Options)
	if opts == nil {
		opts = map[string]string{}
	}
	opts[protocol.OptionWorktree] = "true"
	spec.Options = opts
	return spec
}

// refuseTemporaryHandoffWorktree refuses an agent handoff whose worktree would be
// created under a temporary directory, where a reboot erases the successor's work with
// it.
func refuseTemporaryHandoffWorktree(spec daemon.LaunchSpec) error {
	if spec.Options[protocol.OptionWorktree] != "true" {
		return nil
	}
	for _, path := range []string{spec.Cwd, filepath.Join(spec.Cwd, ".swarm", "worktrees")} {
		// Resolve existing ancestors too: the worktree root may not exist yet.
		resolved, err := canonicalCleanupPath(path)
		if err != nil {
			return fmt.Errorf("handoff: resolve worktree destination: %w", err)
		}
		for _, root := range handoffTemporaryRoots() {
			if root == "" {
				continue
			}
			r, err := canonicalCleanupPath(root)
			if err != nil {
				return fmt.Errorf("handoff: resolve temporary root: %w", err)
			}
			if cleanupContains(r, resolved) {
				return fmt.Errorf("handoff: the worktree destination %q is under the temporary directory %q, which a reboot erases; hand off from a durable checkout instead", path, root)
			}
		}
	}
	return nil
}

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
