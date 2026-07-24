# Inter-session orchestration — prior-art landscape (2026-07-24)

Research input to ADR-009. Question: from inside a running coding-CLI agent session,
spawn a new session (possibly another vendor's CLI) with context continuity; long-term,
let one session's agent observe and steer others. Findings as of mid-2026; this space
moves fast.

## Headline findings

1. No vendor-native cross-CLI spawn-with-context mechanism exists.
2. Cross-vendor transcript replay does not exist and is not close: Claude Code JSONL
   (parentUuid-chained) and codex rollouts (RolloutItem JSONL) are proprietary and
   structurally incompatible; even ACP's `session/load` only re-streams an agent's own
   stored session.
3. The one portable context mechanism proven in practice is the agent-authored handoff
   document passed as plain text: every target CLI accepts an arbitrary launch prompt.
4. Working cross-vendor orchestrators all reduce to: terminal manager (usually tmux) +
   git worktrees + text prompt at spawn + out-of-band state (files/SQLite) for messaging.

## Per-CLI primitives relevant to swarm

### Claude Code
- Slash commands: `.claude/commands/*.md` (project, git-shared) or `~/.claude/commands/`;
  skills (`.claude/skills/<name>/SKILL.md`) are the current recommended form.
- Hooks: 27+ lifecycle events (SessionStart, Stop, PreToolUse, ...); SessionStart can
  inject payload into a fresh session.
- Headless: `claude -p "prompt" --output-format json` returns a session_id;
  `claude -p --resume <id>` continues it.
- Transcripts: `~/.claude/projects/<url-encoded-cwd>/<session-uuid>.jsonl` plus a
  `sessions-index.json` with auto-summaries.
- Agent Teams ("swarm mode", experimental flag `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`):
  teammates via the Task tool, inbox files under `~/.claude/teams/<team>/inboxes/`,
  tasks under `~/.claude/tasks/<team>/`. Claude-Code-instances only — cannot host a
  codex/gemini teammate. Not a fit for swarm's cross-CLI goal.

### codex CLI
- Custom prompts: `~/.codex/prompts/*.md` as `/prompts:name` (deprecated in favor of
  skills; user-home-scoped, not repo-shared).
- Headless: `codex exec` (sandbox flags, `-c` config overrides).
- Resume: `codex resume --last` / by id; rollouts at
  `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl` + `~/.codex/session_index.jsonl`.
- Programmatic control: `codex mcp-server` exists but OpenAI moved to `codex app-server`
  (bidirectional JSON-RPC powering all Codex surfaces) because MCP's request/response
  tool model could not carry streaming diffs, approvals, thread persistence, or
  server-initiated requests. Relevant precedent for our MCP-vs-CLI-verb decision.

### Gemini CLI
- Custom commands: TOML under `.gemini/commands/` (project) or `~/.gemini/commands/`.
- `--experimental-acp`: native ACP server; Gemini is ACP's reference agent.

## Protocols

- **ACP (Agent Client Protocol, Zed)**: JSON-RPC/stdio editor-to-agent standard
  (session/new, session/prompt, session/load, fs/*, terminal/*, request_permission).
  Adapters: Gemini native; Claude Code via `claude-code-acp` (wraps the Agent SDK);
  codex adapters experimental. No agent-to-agent primitive; `session/load` is not a
  cross-vendor transcript transport. A future structured control plane candidate for
  driving CLIs programmatically, at the cost of replacing the PTY model — rejected for
  now in ADR-009.
- **A2A (Google)**: HTTP/SSE agent-to-agent delegation for enterprise agents; no coding
  CLI adoption found. Not currently relevant.

## Orchestrators surveyed

| Tool | Spawn | Context passing | Inter-agent comms | Cross-vendor |
|------|-------|-----------------|-------------------|--------------|
| claude-squad (smtg-ai) | tmux + worktree, TUI | initial task text | none | yes |
| vibe-kanban (BloopAI) | kanban card -> worktree + terminal | card text | none | yes (10+ CLIs) |
| Tmux-Orchestrator | 3-tier hierarchy in tmux panes | files | files | Claude-focused |
| claude-swarm family | YAML teams | MCP calls | MCP between instances | mostly Claude |
| happy / omnara | remote-control existing sessions | n/a | n/a | Claude + codex |
| uzi (devflowinc) | worktrees + tmux | initial prompt | `uzi broadcast` to all | Claude + codex |
| AWS cli-agent-orchestrator | tmux per provider CLI + local server | `handoff` (sync) / `assign` (async) / `send_message` primitives over MCP | MCP | yes — closest named prior art |
| agmsg (fujibee) | spawns CLI in new pane, pre-joined to a team | shared SQLite (WAL) inbox; delivery via SessionStart/Stop hooks (Claude) or wrapper commands (codex/gemini); messages injected as natural text | SQLite bus | yes — smallest working reference |
| crystal (stravu) | desktop app, parallel worktrees | initial prompt | none | codex + Claude |
| container-use (dagger) | MCP; container + branch per agent | infra isolation only | MCP | yes |

Takeaways for swarm: AWS CAO's handoff/assign/send_message verb set is the closest match
to ADR-009's spawn/--handoff/--delegate/send surface. agmsg demonstrates that "inject
messages as natural text the agent reads" works across vendors without any protocol. None
of these has swarm's daemon/shim persistence — swarm's existing subscribe stream and
(worktree) journal replace their ad-hoc buses.

## Context-handoff patterns in practice

- AGENTS.md as converging static-context standard (codex native; Claude Code falls back
  to it; Gemini reads GEMINI.md).
- Agent-authored handoff documents seeded as the new session's first prompt — the
  dominant working pattern, formalized by e.g. softaworks/agent-toolkit's
  session-handoff skill. Vendor-neutral by construction. Chosen for ADR-009.
- MCP memory servers as vendor-neutral shared state (all three CLIs are MCP clients) —
  viable later; redundant with swarm's own state for v1.
- git commits / PR descriptions as coarse implicit handoff.
