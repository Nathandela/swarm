# ADR-032: Swarm is a light organiser: provider configuration drift is accepted, writer holds follow process liveness

- Status: Accepted
- Date: 2026-10-14
- Supersedes: the configuration-freeze and stopped-writer-proof parts of [ADR-028](ADR-028-personal-account-pools.md) and [ADR-031](ADR-031-managed-claude-token-renewal.md) for launch, resume, recovery, automatic onboarding and owner refresh. Credential erasure (retirement) is unchanged.
- Affects: `internal/accountconfig` (`claude_context.go`, `claude_context_update.go`, `codex_context.go`, `native_projection.go`), `internal/accountcheck` (`fence.go`), `internal/skeleton` (`accounts_launch.go`, `account_rotation.go`, `account_retirement.go`, `account_enrollment.go`)

## Context

Claude Code and Codex write ordinary preferences into the managed profile while they run: `/fast`, `/model`, `/config` toggles, `claude mcp add`, project trust and hook trust hashes. The owner also edits `~/.claude/settings.json`, `~/.claude.json` and `~/.codex/config.toml`. Swarm hashed these files at install time and refused any difference (`candidate-configuration-changed`, `account-global-configuration-generation-differs`, `native-configuration-source-changed`, `unsupported-claude-context-setting`). One `/model` in a managed discussion therefore disabled every later launch on that account.

Separately, an owner refresh required every other discussion on the account to be stopped (`configuration-writer-active`), and resume or recovery required a positive stopped-writer proof that only a cleanly exiting shim writes. An idle discussion pinned the account for days, and a SIGKILLed shim pinned it forever.

## Decision

**D1. Drift is accepted and logged, never refused.** When the profile's `settings.json`, `.claude.json` or `config.toml` differs from the installed cohort, the launch proceeds with the profile as it is and logs `accounts: accepted native configuration drift profile=<path> file=<file>`. Codex rewrites the profile's `config.toml` with mode 0644, so the installed-config check (`validateCodexInstalledConfig`) no longer applies the private-file check (0600, owner uid, single link) to it, together with the hash. No boundary moves: `readRegular` still refuses to read a symlinked or group/other-writable `config.toml` (the check then logs it as drift rather than parsing it), and the profile directory stays 0700, so only the owner can place files in it.

**D2. The owner wins on refresh.** When the owner configuration changed, every managed launch, resume, recovery and onboarding refreshes the profile from it, overwriting private edits. Schema-3 resumes, including model-pinned recoveries, re-project; only the frozen environment origin and the model proof carry over. An interrupted refresh followed by another owner change supersedes its stale intent instead of holding.

**D3. A refresh does not wait for other discussions.** It runs under the credential flock only. Rewriting a running CLI's profile is what the owner's own CLIs do to the owner's files.

**D4. Writer holds follow liveness.** A discussion's writers are stopped when its recorded shim (from meta and `native-process.json`), native CLI, backend and their process groups are no longer alive (PID present with the recorded start time, or `kill(-pgid, 0)` not ESRCH). A missing stopped proof never blocks. Retirement keeps the positive proof: PID disappearance alone does not authorize removing credentials.

**D5. Credential routing still refuses.** Owner settings `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`, `forceLoginMethod`, `forceLoginOrgUUID`, and `env` entries that the launch environment scrub or selector check would strip or refuse (for example `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, `AWS_*`) still refuse with `credential-or-provider-settings`. Ordinary `env` entries launch.

**D6. Onboarding running discussions is a separate switch, off by default.** Because D2 lets an onboarding preflight rewrite the chosen profile and then stop and resume each idle source on another account, enabling rotation no longer onboards running unmanaged discussions by itself. Onboarding also needs the per-provider switch in `account-onboarding.json` in the daemon state directory (for example `{"codex":true}`; absent or unreadable means off). It stays off until the cross-account resume acceptance (PLAN section 4) passes; the TUI control arrives in Phase C. The file is outside the account registry, so a downgrade sees no schema change.

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

## Amendment 1 (2026-10-15): identity-drift holds follow the identity read

Same principle as D4, applied to the account identity check (`checkIdentities`, `internal/skeleton/account_model.go`). A failed read of the bound profile's identity is logged once and retried; only a successful read naming another identity is drift. When the identity reads as bound again, an identity-held rotation record still `observed` or `manual-recovery` (never claimed, so it owns no kill or launch obligation) is removed with its aliases and its input lock clears, so a later real drift opens a fresh incident and is held again. Drift records its cause on the account as the optional registry field `auth_reason` (`identity-drift`); a match restores `auth` to `valid` only for that cause, and every other `needs-login` write (login-expired quota fetch, `auth-invalid`, erasure) clears it. A later non-drift failure on a still-held session keeps the hold, so it stays releasable; an owner kill that races the release still ends the session. Input-lock refusals name the job holding the lock (identity check on the account, account move, CLI refresh, unsaved observation, or credential recycle) and its remedy. The field is written only while an account is drift-flagged. A binary older than v0.17.9 refuses such a registry (strict decoding), so the compat card gains `account_auth` (1 from v0.17.9; older cards decode 0) and the upgrade account guard refuses a rollback or downgrade to a card below 1 while any account carries `auth_reason`.
