# Agent Instructions

The active product on this branch is a Rust terminal application for managing coding-agent sessions. The per-user supervisor owns session state. Each shim owns its PTY and can survive a supervisor restart. The supported provider families are Claude Code, Codex, agy, Hermes Agent and OpenCode. Discord handles the retained remote and voice interaction.

Use the [migration roadmap](docs/specifications/rust-migration/README.md) for scope and the [verification ledger](docs/verification/rust-migration.md) for what has passed. The stable Go product is the pinned behavioral reference. Its mobile clients, phone connectors and managed account rotation are outside the Rust product. Provider-native authentication and schedule fallback between agent types are retained.

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

## Build and test this branch

```sh
cargo build --locked --workspace
cargo fmt --all -- --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo test --locked --workspace
```

Use [the local qualification harness](scripts/verification/parity/README.md) for Go/Rust comparisons, failure injection and whole-PTY checks. Use temporary installation and state directories throughout testing.

## Go reference gates

In a checkout of the pinned Go reference, use Go toolchain >= 1.26 (raised from 1.22 by the VT emulator dependency — see ADR-005; to 1.25 by
Phase B's mandatory `golang.org/x/mobile` tool directive — see ADR-008, which supersedes ADR-005's
floor without superseding ADR-005; to 1.26 by the `golang.org/x/net` CVE fix — ADR-008 Amendment 1).

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run
```

At an epic close, GG-4 also requires `-race` on every package that spawns goroutines.

Share the existing Go and Gradle caches across worktrees; do not create per-worktree cache copies.
Use the test supervisor's per-run `TMPDIR` for scratch, keeping short socket roots inside it.

## CI policy

Operative release builds and quality gates for the Rust candidate run locally. The branch
has no GitHub Actions workflows. Follow the [manual Rust preview release runbook](docs/operations/rust-preview-release.md)
for the freeze, build, verification and distribution steps. Historical Go workflows remain in their pinned references.

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
