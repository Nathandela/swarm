# Project Instructions for AI Agents

Read [AGENTS.md](AGENTS.md) for the Rust build, reference gates and repository map.

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


## Development agreements

Use the build commands and repository map in [AGENTS.md](AGENTS.md). The Rust application lives in `crates/swarm`. The roadmap identifies the original Go revisions used for differential tests and the per-milestone exit criteria. [Migration roadmap](docs/specifications/rust-migration/README.md)

- Capture a failing case before fixing a behavioral difference. Retain the observed failure and the passing regression in the relevant verification record.
- Evaluate differences against the pinned Go oracle and the user's accepted behavior changes.
- Record changes to architectural decisions according to the [ADR convention](docs/adr/README.md).
- Keep code, comments and documentation free of emojis.
- Commit and push small verified changes to the migration branch.
