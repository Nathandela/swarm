# ADR-009: Inter-session orchestration — agent-initiated spawn, handoff, observation, and steering via local CLI verbs

**Status**: Proposed (design lock for the inter-session orchestration epic; ratifies to Accepted when Phase 1 lands)
**Date**: 2026-07-24

## Context

The product need: while working with one CLI agent (e.g. Claude Code) inside a swarm
session, the user wants to continue or fan out that work in a NEW swarm session running a
possibly different CLI (codex, gemini, opencode) with continuity of context — triggered
either by the agent itself (a shared slash command) or from the swarm TUI (key combo +
CLI/model picker). Long-term, one session's agent should be able to observe and steer
other sessions ("full autonomous control" is the accepted end state for the personal
single-owner deployment).

Prior-art research (docs/research/inter-session-orchestration-landscape.md) found no
vendor-native cross-CLI mechanism: Claude Code's Agent Teams is Claude-only; ACP is an
editor-to-agent protocol with no agent-to-agent primitive and no cross-vendor session
transfer; every working cross-vendor orchestrator (claude-squad, uzi, AWS CAO, agmsg)
reduces to "spawn the target CLI with a text prompt, pass context as plain text." The
one proven portable context mechanism is the agent-authored handoff document.

The codebase is already most of the way there:

- `OpLaunch` carries `InitialPrompt`, cwd, worktree and resume options; the daemon accepts
  concurrent clients on the main socket; `internal/protocol/client.go` is a complete client
  library. Spawning needs no new RPC.
- `OpSubscribe` streams latest-state `SessionView` snapshots (ADR-008); `OpList` returns the
  roster with server-derived status Groups. Observation of status needs no new RPC.
- The one in-session-to-daemon channel already exists: the daemon injects
  `SWARM_HOOK_{SESSIONID,TOKEN,SOCKET}` into every agent's environment.
- The unmerged remote-control work (worktree `remote-control-research`, its ADR-007)
  already designed and reviewed the hard steering pieces: a `take_control` control session
  bound to the attach lease, a four-clause input gate, a shared per-session tap permitting
  concurrent multi-tier control (its 2026-07-24 amendment, Decision G, relaxing P-5 for the
  personal single-owner model), read-only `terminal_subscribe` snapshots, and a durable
  journal. Local orchestration must reuse those mechanics, not grow a parallel path.

Constraints: protocol changes are ADR-gated with lockstep `protocol.md` rows (GG-7);
ADR-004's v1 trust model is filesystem permissions — any same-user process already holds
full daemon power via the socket; a daemon-launched session is not a process-group
descendant of its spawner (S-4), so lineage must be explicit metadata; Claude Code treats
text+CR in one PTY write as an unsubmitted paste (`agents-tracker-r3p`), so injected input
needs split writes.

## Decision

### D1. Transport: thin CLI verbs over the existing protocol client

`cmd/swarm` gains agent-facing verbs — `swarm spawn`, `swarm ls --json`, `swarm watch`,
`swarm peek`, `swarm send` — implemented as thin wrappers over `internal/protocol.Client`
against the existing main socket. Shelling out to `swarm` is the lowest common denominator
every target CLI supports with zero per-CLI configuration. No MCP server and no ACP client
in v1 (see Alternatives).

### D2. Handoff contract: agent-authored document plus pointers

`swarm spawn --cli <name> [--model m] [--handoff <file> | --delegate <file>] [--from <session-id>]`
launches via `OpLaunch` with an `InitialPrompt` instructing the new agent to read the
handoff file first. The handoff file is authored by the source agent (goal, current state,
decisions, next steps) and carries pointers, not payloads: the source swarm session id, the
source CLI's native transcript path (raw — no cross-vendor normalization in v1), git state,
and relevant beads issue ids. Handoff files live under the swarm state dir
(`<stateDir>/handoffs/`), never in the repo. `--handoff` and `--delegate` share mechanics
and differ only in recorded intent (D4) and defaults (handoff: same cwd; delegate:
`--worktree` recommended).

### D3. Two triggers, one code path

- **Agent-initiated**: a shared slash command per CLI (`/swarm-handoff`, `/swarm-delegate`)
  instructs the agent to write the handoff doc and run `swarm spawn`. `swarm agents install`
  writes the command files into each CLI's convention (`.claude/commands/` or skills,
  `~/.codex/prompts/`, `.gemini/commands/*.toml`, opencode commands) from one template
  source, so the prompt content is maintained once.
- **TUI-initiated**: a keybinding opens a target picker, then the TUI injects the same
  slash command text into the current session's PTY via its controller lease (split
  text-then-CR writes per `agents-tracker-r3p`). Injection requires the session to be at a
  prompt; if it is not, the TUI surfaces that instead of queueing.

### D4. Lineage metadata and roster visibility

Launch gains an optional spawned-from link recorded in session meta (mirroring the
`ResumedFrom` pattern) with an intent tag (`handoff` | `delegate`). The roster/`SessionView`
exposes it; the TUI shows a "handed off -> <session>" badge on the source and the lineage on
the child. The source session stays alive after a handoff — no automatic lifecycle coupling
(consistent with S-4/D-2); the user closes it manually.

### D5. Observation: reuse list/subscribe; add a read-only peek

`swarm ls --json` (OpList) and `swarm watch` (OpSubscribe; ADR-008 coalescing semantics
documented to consumers) cover status. `swarm peek <session>` returns a rendered, escape-
filtered snapshot of the session's current screen plus optional transcript tail, via a
read-only path that does not take or supersede the controller lease — porting the
`terminal_subscribe`/tap design from the remote-control worktree rather than abusing
`attach`. This is the "observer mode" P-5 deferred, delivered through the already-reviewed
tap mechanism.

### D6. Steering: port the control-session mechanics, without the remote crypto

`swarm send <session> [--text s | --key enter|esc|ctrl-c]` injects input through a local
control session that reuses the take_control/tap mechanics from the remote-control work:
a bounded (TTL) control session bound to the current lease generation, riding the shared
per-session tap so it does not supersede an attached human (concurrent owner control per
that ADR's Decision G). The remote tier's device signatures, gate tokens, and idempotency
binding are NOT required locally: on the main socket every client is owner-tier by
ADR-004's construction. Input writes honor the r3p split-write discipline.

### D7. Security posture: convenience, not new authority

These verbs add no authority beyond what ADR-004 already grants any same-user process
(the socket is interactive code execution as the user). "Full autonomous control" —
agents spawning, observing, and steering sessions without per-action confirmation — is the
accepted posture for the personal single-owner deployment. Two boundaries are kept
anyway: steering rides an explicit bounded control session (auditable, TTL'd, visible in
the TUI as an active-control indicator), and the remote tier remains fully out of scope
here — remote clients keep every ADR-007 control (signatures, policy, kill switch). If a
finer local model is ever needed, the per-session hook token is the natural gating seam.

### D8. Sequencing and the remote-control worktree

Phase 1 (spawn + handoff + install + lineage + TUI trigger) and Phase 2 observation via
list/subscribe touch no contested code and land on main first. `swarm peek` (D5) and
`swarm send` (D6) depend on porting the tap/control-session mechanics that currently live
only in the remote-control worktree; that port lands as its own reviewed slice (either by
merging the remote epic's daemon-side pieces first or by cherry-picking the tap + control
session into main), and the steering ops' `protocol.md` rows land with it (GG-7). Note:
that worktree numbers its remote-access ADR "ADR-007", which collides with main's ADR-007
— it renumbers on merge.

## Consequences

### Positive

- Spawn-with-context works across all four CLIs with zero per-CLI protocol integration;
  the only per-CLI surface is a generated slash-command file and the adapter that already
  exists per CLI.
- One code path serves both triggers; the TUI path is just automated typing of the same
  command the user could type.
- Observation and steering reuse designs that already survived cross-model review, and the
  local port is strictly simpler (crypto layer removed, not bypassed).
- The remote epic later reuses the same merged tap/control-session code instead of
  reconciling a divergent local fork.

### Negative

- Any process inside any session can drive every other session (accepted; documented).
- Handoff fidelity is bounded by what the source agent writes; native transcript pointers
  are raw per-vendor formats the reading agent must interpret itself.
- PTY slash-command injection is timing- and prompt-state-sensitive; it fails (visibly)
  when the source agent is mid-turn.
- Phase 3 steering is coupled to the remote-control worktree's merge/port schedule.

## Alternatives Considered

- **MCP server in front of the daemon**: typed, discoverable tools, but requires MCP
  registration in every CLI, and the request/response tool model fits leases, streaming
  output, and approvals poorly — OpenAI retired `codex mcp-server` for its app-server for
  exactly this reason. May return later as a thin wrapper over the same verbs; rejected as
  the v1 transport.
- **ACP as the control plane** (swarm drives CLIs as ACP servers instead of PTYs):
  structured turns and permissions, adapters exist for all target vendors — but it replaces
  swarm's core value (real PTYs that survive daemon crashes, real TUIs), the codex adapter
  is immature, and ACP has no agent-to-agent or cross-vendor session-transfer primitive
  anyway. Watch; do not adopt.
- **External message bus (agmsg-style shared SQLite / file inboxes + per-CLI hooks)**:
  works without daemon changes, but swarm already owns a daemon, a subscribe stream, and
  (in the worktree) a durable journal — a second bus fragments state and adds per-CLI
  polling hacks. Rejected.
- **Full transcript replay into the new session**: no cross-vendor tooling exists, formats
  are proprietary and huge, and even ACP's `session/load` only replays an agent's own
  stored session. Rejected in favor of the handoff document plus raw transcript pointers.
- **Simple lease-steal for `swarm send`** (attach, write, detach): trivial on main today,
  but it kicks an attached human off mid-keystroke and creates a second input path the
  remote work must later reconcile. Rejected in favor of porting the tap/control-session.

## Spec amendments this ADR governs

Phase 1 needs no protocol changes (`OpLaunch`/`OpList`/`OpSubscribe` suffice); the
spawned-from meta field and any `SessionView` exposure get their `protocol.md` rows when
added (GG-7). Phase 2/3 port the observation/steering ops; their ops, fields, and the
system-spec invariant updates (P-5 observer mode, concurrent-control note) land with the
port, never silently.
