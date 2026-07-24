# Agent Instructions

swarm is a terminal application that centralizes every coding-agent CLI session on a machine into one keyboard-driven, Agent View-style dashboard. A Go + Bubble Tea client talks to a per-user supervisor daemon, which orchestrates per-session shim processes that own the PTYs — so sessions survive terminal close and daemon crash/upgrade alike. Public and released (latest v0.5.1); epics 0-14 are implemented with verification evidence under docs/verification/.

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

Go toolchain >= 1.24 (raised from 1.22 by the VT emulator dependency — see ADR-005).

```bash
go build ./...
go test ./...
golangci-lint run
```

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
