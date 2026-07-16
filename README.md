# swarm

Every coding agent on your machine, in one keyboard-driven terminal view — running in the background, surviving the terminal (and the daemon).

Inspired by Claude Code's Agent View, but agent-agnostic: Claude Code and Codex first, then Gemini CLI, OpenCode, AGY — each behind a tested adapter. Go + Bubble Tea; supervisor daemon + per-session shim processes owning PTYs.

## Status

Design approved (Gate 2). See:

- [Documentation index](docs/INDEX.md) — everything, one hop away
- [System specification](docs/specifications/system-spec.md) — EARS requirements, diagrams, scenarios
- [Build plan](docs/specifications/build-plan.md) — 15 ordered epics (Gate 3-approved); backlog in beads (`bd ready`)
- [ADRs](docs/adr/) — the four foundational decisions
- [Audit report](docs/verification/audit-001-system-spec.md) — committee findings that shaped Draft 2
- [Design preview](docs/design/ui-preview.html) — navigable UI mockup
- [Landscape research](docs/research/agent_view_landscape.md)
- [DESIGN.md](DESIGN.md) — original design brief (superseded by the spec)
