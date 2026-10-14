# ADR-032: Swarm is a light organiser: provider configuration drift is accepted, writer holds follow process liveness

- Status: Proposed
- Date: 2026-10-14
- Supersedes: the configuration-freeze and stopped-writer-proof parts of [ADR-028](ADR-028-personal-account-pools.md) and [ADR-031](ADR-031-managed-claude-token-renewal.md) for launch, resume, recovery, automatic onboarding and owner refresh. Credential erasure (retirement) is unchanged.
- Affects: `internal/accountconfig` (`claude_context.go`, `claude_context_update.go`, `codex_context.go`, `native_projection.go`), `internal/accountcheck` (`fence.go`), `internal/skeleton` (`accounts_launch.go`, `account_rotation.go`, `account_retirement.go`)

## Context

Claude Code and Codex write ordinary preferences into the managed profile while they run: `/fast`, `/model`, `/config` toggles, `claude mcp add`, project trust and hook trust hashes. The owner also edits `~/.claude/settings.json`, `~/.claude.json` and `~/.codex/config.toml`. Swarm hashed these files at install time and refused any difference (`candidate-configuration-changed`, `account-global-configuration-generation-differs`, `native-configuration-source-changed`, `unsupported-claude-context-setting`). One `/model` in a managed discussion therefore disabled every later launch on that account.

Separately, an owner refresh required every other discussion on the account to be stopped (`configuration-writer-active`), and resume or recovery required a positive stopped-writer proof that only a cleanly exiting shim writes. An idle discussion pinned the account for days, and a SIGKILLed shim pinned it forever.

## Decision

**D1. Drift is accepted and logged, never refused.** When the profile's `settings.json`, `.claude.json` or `config.toml` differs from the installed cohort, the launch proceeds with the profile as it is and logs `accounts: accepted native configuration drift profile=<path> file=<file>`.

**D2. The owner wins on refresh.** When the owner configuration changed, every managed launch, resume, recovery and onboarding refreshes the profile from it, overwriting private edits. Schema-3 resumes, including model-pinned recoveries, re-project; only the frozen environment origin and the model proof carry over. An interrupted refresh followed by another owner change supersedes its stale intent instead of holding.

**D3. A refresh does not wait for other discussions.** It runs under the credential flock only. Rewriting a running CLI's profile is what the owner's own CLIs do to the owner's files.

**D4. Writer holds follow liveness.** A discussion's writers are stopped when its recorded shim (from meta and `native-process.json`), native CLI, backend and their process groups are no longer alive (PID present with the recorded start time, or `kill(-pgid, 0)` not ESRCH). A missing stopped proof never blocks. Retirement keeps the positive proof: PID disappearance alone does not authorize removing credentials.

**D5. Credential routing still refuses.** Owner settings `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`, `forceLoginMethod`, `forceLoginOrgUUID`, and `env` entries that the launch environment scrub or selector check would strip or refuse (for example `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, `AWS_*`) still refuse with `credential-or-provider-settings`. Ordinary `env` entries launch.

## Consequences

Positive: ordinary native use no longer poisons an account; idle or crashed discussions no longer pin it; onboarding and resumes follow owner changes.

Negative:
- A private `/model` or `/fast` choice is lost at the next owner change.
- A concurrent rewrite of `.claude.json` can lose one update (the race PLAN section 5 accepts).
- A sibling refresh in the sub-second window between launch preparation and shim start makes the new shim's marker revalidation fail. The launch is refused once and a retry succeeds.
- Rotation successor feasibility still requires the successor projection to equal the source projection, so a quota or auth rotation started after an owner change remains refused until Phase C rebuilds rotation on the recycler.

The structural fix (credentials-only profiles linking the owner's files) is Phase B; deleting the freeze and proof machinery is Phase D.

## Alternatives Considered

- Skip the refresh while another writer is alive. Rejected: the launch would then run against a marker for a different cohort, which needs new fallback machinery for little benefit.
- Merge private edits into owner refreshes. Rejected as new merge machinery; Phase B removes the copies instead.
