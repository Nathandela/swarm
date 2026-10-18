package skeleton

// agents-tracker-xl6k: a first-class `swarm handoff` child must launch in the
// checkout the source's agent actually worked in. The CLI can send nothing but the
// source's roster cwd -- SessionView carries no AgentCwd -- and for a
// worktree-isolated source that is the repository root, not the
// <repo>/.swarm/worktrees/<slug> checkout the agent ran in. The daemon owns the
// authoritative value in the source's persisted meta (persist.Meta.AgentCwd, behind
// ProviderCwd), so it corrects rather than inherits, as the hands-off composition
// and the resume path do for their shapes.

import (
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

// isHandoffChildLaunch reports whether the spec is the first-class `swarm handoff`
// shape: a lineaged launch with handoff intent and a supervision mode and without
// resume options. The supervision mode is what marks the command -- the hands-off
// form composes an empty one and `swarm spawn` sends none -- so the gating shape
// overrides no caller's explicit choice of directory, and a resume is excluded
// because recycling a supervised handoff child relaunches it AS a resume while the
// original lineage fields remain set, and the resume layer already resolves that
// directory.
func isHandoffChildLaunch(spec daemon.LaunchSpec) bool {
	return spec.SpawnedFrom != "" &&
		spec.SpawnIntent == protocol.SpawnIntentHandoff &&
		spec.Supervision != "" &&
		spec.Options[protocol.OptionResumeFrom] == "" &&
		spec.Options[protocol.OptionResumeConversationID] == ""
}

// handoffDirectory resolves the working directory of a first-class `swarm handoff`
// child: the source's ProviderCwd, not the roster cwd the CLI sent. A source
// recorded before AgentCwd existed (ProviderCwd degrades to Cwd) keeps the cwd the
// CLI sent, and a recorded directory that no longer exists refuses by name, so a
// torn-down worktree surfaces as a clear refusal rather than a fallback launch.
// An absent lineage row is refused by the caller, before this runs.
func handoffDirectory(source persist.Meta) (string, error) {
	// Mirror resumeDirectory's unknowable-checkout refusal: a worktree-isolated
	// launch captured no AgentCwd, so its ProviderCwd degrades to the roster cwd --
	// the wrong checkout, presented as if it were the confirmed one.
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
