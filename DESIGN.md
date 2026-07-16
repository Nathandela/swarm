# Design brief

Working document. Decisions locked so far, requirements gathered, and open questions to iterate on.

## Vision

A centralized agent session manager for the terminal, as close as possible to the experience of Claude Code's Agent View, but agent-agnostic: it tracks and controls sessions of any supported coding CLI, keeps them running when the terminal closes, and later extends to remote control from a phone.

## Decisions

| Area | Decision | Rationale |
|---|---|---|
| Architecture | Own supervisor daemon + PTY workers (Anthropic-style), not tmux-backed | Matches the Agent View design the user wants to replicate; the daemon's client protocol is the same surface a V2 mobile remote will need |
| Language | Go | Single static binary, easy distribution, most reference clones (claude-squad, agent-of-empires, izll/agent-session-manager) are Go and can be cribbed from |
| TUI | Bubble Tea (+ Lip Gloss, Bubbles) | Standard Go TUI stack |
| Agents v1 | Claude Code, Codex CLI, Gemini CLI, OpenCode, AGY | Per-agent adapter modules behind one interface |
| Status detection | Native hooks where available (Claude Code), PTY output parsing fallback | Best accuracy per unit of effort |

## Requirements (v1)

- **Background running.** Sessions live under the daemon, independent of any terminal. Close the terminal, agents keep working.
- **General agents view.** Central list of all sessions grouped by state (needs input / working / ready for review / completed), Agent View-style. From it:
  - launch new agents: pick working directory (free-text path entry is fine), agent CLI, model/harness options
  - see state notifications as sessions change status
- **Keyboard-first navigation.** Arrow keys to move, Enter to attach, Esc to go back, Ctrl+X (or similar) to kill/delete a session.
- **Attach = the raw CLI.** Inside a session you get the agent CLI's own interface untouched, plus minimal harness chrome to return to the general view. No fluff.
- **Minimal aesthetic.** Claude Code's terminal minimalism is the reference.
- **Easy install.** One CLI command to install (Homebrew tap and/or `go install`), launched by app name.

## V2 (design for it now, build later)

- **Mobile remote control.** A phone app (or PWA) controlling sessions remotely; ideally one remote controlling the agent views of multiple machines.
- Architectural implication for v1: the TUI must talk to the daemon over a real protocol (Unix domain socket first), never in-process. The same protocol, exposed through a relay/bridge, becomes the mobile transport. Multi-machine means the client treats "a daemon" as one of possibly many endpoints.

## Open questions

1. **App/binary name.** `at` collides with the Unix `at` command. Candidates needed.
2. **Daemon lifecycle.** Auto-start on first client launch vs launchd/systemd service; idle shutdown policy.
3. **Attach mechanics.** PTY proxying through the daemon (full passthrough, resize, scrollback) — the hardest technical piece; define escape key to detach back to the general view.
4. **Notifications.** In-TUI only, or also OS notifications (macOS notification center) when a background session needs input?
5. **Worktree isolation.** Per-session git worktrees like Agent View, or run in the chosen directory as-is?
6. **Session persistence.** What survives a daemon restart: roster + transcripts on disk, re-spawn on attach (Agent View keeps transcript, kills idle process after ~1h).
7. **Per-agent adapters.** Exact capability matrix per CLI: how each is spawned headless/interactive, what hooks exist, what to parse for status.
