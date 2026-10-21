# Agent Instructions

swarm is a terminal application that centralizes coding-agent CLI sessions in one keyboard-driven dashboard. The Go + Bubble Tea client talks to a per-user supervisor daemon; per-session shims own the PTYs and preserve live sessions across terminal close and daemon restart. Public and released; see the [releases page](https://github.com/Nathandela/swarm/releases).

The production adapters are Claude Code, Codex, agy, Hermes Agent, and OpenCode; adapter capabilities differ. OpenCode's native worker lifecycle and controls are deployed from v0.19.0, while its unattended status/attention parity and longer qualification remain open. Android remote control is a separate product surface: the current clean replacement is an accepted architecture with implementation and hosted/physical-device gates still open. See the current boundaries map in [docs/INDEX.md](docs/INDEX.md), then the linked source of truth before making claims about a capability.

This file is a map, not a manual — read the linked source of truth before acting, don't rely on this summary.

## Entry points

| Need | Read |
|---|---|
| Everything, one hop away | [docs/INDEX.md](docs/INDEX.md) |
| Requirements (EARS ids), architecture, scenarios | [docs/specifications/system-spec.md](docs/specifications/system-spec.md) |
| 15 ordered epics, contracts, gap resolutions | [docs/specifications/build-plan.md](docs/specifications/build-plan.md) |
| Per-epic exit criteria, global goals, orchestration protocol | [docs/specifications/implementation-goals.md](docs/specifications/implementation-goals.md) |
| Safety/liveness invariants (S1-S12, L1-L3) | [docs/invariants/system-invariants.md](docs/invariants/system-invariants.md) |
| Foundational architecture decisions | [docs/adr/README.md](docs/adr/README.md) |
| Vendored codebase-governance principles | [docs/governance/](docs/governance/) |

## Build, test, run

Go toolchain >= 1.26 (raised from 1.22 by the VT emulator dependency — see ADR-005; to 1.25 by
Phase B's mandatory `golang.org/x/mobile` tool directive — see ADR-008, which supersedes ADR-005's
floor without superseding ADR-005; to 1.26 by the `golang.org/x/net` CVE fix — ADR-008 Amendment 1).

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run
```

At an epic close, GG-4 also requires `-race` on every package that spawns goroutines.

## CI policy

GitHub Actions runs only for release tags (`v*`), at the owner's request to limit Actions usage.
`release.yml` calls the reusable CI and container gates before publishing. Do not add branch,
pull-request, scheduled, or manual triggers. The former nightly stress workflow is removed.

## Beads workflow

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->

## Verification

An epic closes only when every exit criterion listed for it in implementation-goals.md is demonstrably true (passing test, produced artifact, or recorded verification) and the GG-4 quality gates (build, vet, lint, test, `-race`) are green. Evidence is recorded per epic under `docs/verification/`.
