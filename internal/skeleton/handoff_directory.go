package skeleton

// agents-tracker-xl6k: a first-class `swarm handoff` child inherits the wrong
// checkout. The CLI (cmd/swarm/handoff.go) can send nothing but the source's roster
// cwd -- SessionView deliberately carries no AgentCwd (four reviewers refused
// widening that frozen wire type) -- and SessionView.Cwd is the LAUNCH cwd. For a
// worktree-isolated source that is the repository root, while the source's agent
// actually ran in <repo>/.swarm/worktrees/<slug>: a separate checkout with separate
// files and possibly a different branch. The daemon holds the value the client is
// missing in the source's persisted meta (persist.Meta.AgentCwd, behind
// ProviderCwd), so -- exactly like the hands-off composition (STEP 5b) and the
// resume path (resumeDirectory) -- it corrects rather than inherits.

import (
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

// isHandoffChildLaunch reports whether the spec is the first-class `swarm handoff`
// wire shape: a handoff-intent lineaged launch WITH a supervision mode and WITHOUT
// resume options. That shape is reachable only from the owner tier (the remote tier
// refuses every supervision mode, R-POL.9) and only from the CLI: the hands-off form
// composes supervision EMPTY by construction (composeHandsOffLaunch STEP 7), and
// `swarm spawn` sends no supervision at all, so the mode is what marks the command
// and overrides no caller's explicit choice of directory. A resume is excluded
// because recycling a supervised handoff child (authwatch, cli_refresh) re-launches
// it AS a resume with the ORIGINAL lineage fields still set -- the resume layer
// already picks that directory from the child's own record (resumeDirectory).
func isHandoffChildLaunch(spec daemon.LaunchSpec) bool {
	return spec.SpawnedFrom != "" &&
		spec.SpawnIntent == protocol.SpawnIntentHandoff &&
		spec.Supervision != "" &&
		spec.Options[protocol.OptionResumeFrom] == "" &&
		spec.Options[protocol.OptionResumeConversationID] == ""
}

// handoffDirectory resolves the working directory of a first-class `swarm handoff`
// child: the source's ProviderCwd, the directory the agent was actually working in,
// rather than the roster cwd the CLI sent.
//
// A source that is not on this daemon leaves the client's cwd untouched: the CLI
// verifies roster membership before the launch, and lineage stays metadata
// everywhere else the daemon treats SpawnedFrom. A source recorded before AgentCwd
// existed (ProviderCwd degrades to Cwd) changes nothing, since that Cwd is what the
// CLI sent. The stat refusals mirror the hands-off composition's: the protocol layer
// stats the CLIENT's cwd and never this override, so a worktree torn down since the
// source ran would otherwise surface as an obscure spawn failure instead of a named
// refusal.
func handoffDirectory(source persist.Meta) (string, error) {
	providerCwd := source.ProviderCwd()
	if providerCwd == "" {
		return source.Cwd, nil
	}
	if fi, err := os.Stat(providerCwd); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("handoff: the source's working directory %q is no longer a directory, so the successor has nowhere to continue", providerCwd)
	}
	return providerCwd, nil
}
