# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

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


## Build & Test

Go toolchain >= 1.24 (raised by the VT emulator dependency, ADR-005).

```bash
go build ./...
go test ./...        # -race on packages that spawn goroutines
go vet ./...
golangci-lint run
```

All four must be green before any epic closes (implementation-goals.md GG-4).

## Architecture Overview

Terminal multi-CLI agent tracker: a Bubble Tea TUI client talks over a UDS to a supervisor daemon, which orchestrates per-session shim processes that own the PTYs — sessions survive terminal close and daemon crash/upgrade. Start at [AGENTS.md](AGENTS.md) and [docs/INDEX.md](docs/INDEX.md); the authoritative documents are the system spec, build plan, implementation goals, invariants, and ADRs linked there.

## Conventions & Patterns

- **TDD is mandatory**: write failing tests before implementation; the failing-first run must be evidenced (implementation-goals.md GG-5). Never modify a test to make it pass — if a test seems wrong, stop and discuss.
- **Verification exit criteria**: an epic is done only when every exit criterion in docs/specifications/implementation-goals.md is demonstrably true and its evidence file exists under docs/verification/.
- **Decision changes require an ADR** (docs/adr/README.md has the convention); never silently drift from the spec.
- **No emojis** in code, comments, or docs.
- **Commit and push often** — small checkpoints that can be walked back.
