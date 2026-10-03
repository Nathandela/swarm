# Agent CLI refresh

Swarm observes installed Claude Code and Codex updates and resumes eligible idle
sessions using the updated installation. Swarm does not install the provider
update. The refreshed session has a new swarm ID and the same provider conversation
ID. Its name, tag, environment, launch options and supervision lineage carry over.
The ended source row and transcript remain available for recovery.

## Commands

```sh
swarm refresh
swarm refresh --json
swarm refresh --auto off
swarm refresh --auto on
```

Reporting is read-only and does not start a daemon or probe a provider. The setting
is independent of `swarm relogin --auto`; disabling CLI refresh prevents new
automatic refresh kills, while recovery already owed by a prior kill is retained.
Settings live in `cli-refresh.json` in the daemon's state directory. The shared
credential/CLI coordinator persists its work in `auth-watch-state.json`.

## When a session refreshes

Newly launched sessions capture an installation observation. The watcher compares
that observation with the executable selected by the session's saved PATH and
working directory. A newer numeric release must be observed consistently before
a refresh is considered. Detection runs on the existing 30-second watcher cadence;
large rosters, slow probes and busy sessions can take longer than one interval.

Refresh waits for an idle turn, no permission/input interaction, no attached
controller, and no unresolved input or ContextGuard operation. These conditions
are rechecked at the final lifecycle boundary. Swarm never intentionally interrupts
an active turn to apply an update.

Old sessions without installation observations remain untouched. Installing this
feature and restarting the daemon does not upgrade those sessions in place. They
become eligible after a normal explicit resume or a new launch under this build.
Worktree-isolated sessions and sessions without a captured provider conversation ID
also remain untouched. Commit or preserve their work and handle them explicitly.

Equal versions, downgrades, prereleases, missing executables, failed probes, and
unstable installation changes do not authorize a new automatic kill. Each session
keeps its own saved environment: an update in another PATH entry does not override
a deliberately pinned installation.

## Understanding progress

The report identifies source session, provider, target version, replacement ID,
state and latest explanation. A pending request waits for its safe boundary; a
waiting request has a replacement whose startup observation is not yet complete.
A complete request means the replacement was running with matching installation
observations across process startup. A blocked request needs manual attention.

A refresh that fails before creating a replacement retains its recovery obligation
and retries with backoff. Once a replacement exists, its identity is retained even
if it exits. Swarm will not repeatedly spawn replacements for an incompatible CLI
release. The source row remains available to inspect and explicitly resume after
the installation is repaired. Missing or corrupt coordinator state is reported
and holds automatic destructive work rather than silently discarding obligations.

Do not delete the coordinator state to clear a warning: it is the record of which
session terminations the daemon owns. Read the reported error and daemon log, repair
the installation or settings, and explicitly recover the relevant conversation.

## Observation limits

`CLIIdentity` and `observed-cli.json` describe the selected installation and
filesystem metadata around startup. They are not attestation of arbitrary wrapper
dependencies or a guarantee that every provider's transient in-memory state
survives a version change. A resume request names the exact conversation; a seeded
conversation ID alone is not proof the provider successfully loaded it.

Codex runs both a terminal process and an app-server. The shim checks the installation
across both starts and withholds its observation if an intervening update or
degraded backend makes the result uncertain. Source history is retained in that
case. The test suite exercises this with real mock child processes; compatibility
of arbitrary future provider releases is not assumed from those tests.

## Installed-build verification

After publishing and installing a build, verify `swarm version` and
`swarm refresh --json`, then restart the daemon so it loads the implementation.
The daemon restart preserves existing shim processes. Validate process identities
before and after that restart.

The disposable integration tests exercise executable replacement without touching
real provider conversations:

```sh
go test ./internal/skeleton -run '^TestCLIRefresh' -count=1
go test -race ./internal/shim -run '^TestCLIRefresh' -count=1
```

Run tests with inherited `SWARM_*` session-routing variables removed, except any
explicit test-binary override. This prevents live-session routing from contaminating
the fake daemon/hook fixtures.

## Rolling back swarm

Before installing an older swarm build, disable new CLI refreshes with
`swarm refresh --auto off`. Inspect `swarm refresh --json` and allow owned
replacements to finish, or explicitly recover blocked conversations. Back up the
shared `auth-watch-state.json` before downgrading. An older build does not understand
the CLI refresh records and may discard that recovery information when it saves
its own auth state. Mid-refresh downgrade is therefore not a supported automatic
recovery path; retained source history remains available for manual recovery.
