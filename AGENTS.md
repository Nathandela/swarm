# Agent Instructions

The active product on this branch is a Rust terminal application for managing coding-agent sessions. The per-user supervisor owns session state. Each shim owns its PTY and can survive a supervisor restart. The supported provider families are Claude Code, Codex, agy, Hermes Agent and OpenCode. Discord handles the retained remote and voice interaction.

Use the [migration roadmap](docs/specifications/rust-migration/README.md) for scope and the [verification ledger](docs/verification/rust-migration.md) for what has passed. The stable Go product is the pinned behavioral reference. Its mobile clients, phone connectors and managed account rotation are outside the Rust product. Provider-native authentication and schedule fallback between agent types are retained.

## Entry points

| Need | Read |
|---|---|
| Everything, one hop away | [docs/INDEX.md](docs/INDEX.md) |
| Current implementation milestones and ownership | [Rust migration roadmap](docs/specifications/rust-migration/README.md) |
| Rust architecture and module boundaries | [Rust design](docs/specifications/rust-migration/rust-design.md) |
| Behavioral oracle, comparisons and release gates | [Qualification contract](docs/specifications/rust-migration/qualification.md) |
| Observed native results and outstanding release gates | [Verification ledger](docs/verification/rust-migration.md) |
| Original requirements, invariants and decisions | [System specification](docs/specifications/system-spec.md) · [Invariants](docs/invariants/system-invariants.md) · [Decision index](docs/adr/README.md) |
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

Run Go gates in a separate reference checkout. The executable builder checks out each source pin independently. Corrections specific to the Go test harness are recorded at `6ea90b40`. [Reference builder](scripts/verification/build_rust_references.py)

Use Go 1.26 or newer for the pinned reference. Share the existing Go cache across checkouts. The original build, vet, lint, normal and race gates are recorded with the pinned test populations. The fleet accepts a short per-run `TMPDIR` for sockets and test state. [Reference checks](docs/specifications/rust-migration/reference-checks.md)

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

Close a Rust milestone when its roadmap exit criteria, local Cargo gates and required native witnesses pass on the identified source revision. Link the evidence under `docs/verification/` in Beads and record any outstanding external gates there. Use the original implementation goals when verifying the Go reference itself.
