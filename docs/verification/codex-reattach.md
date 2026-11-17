# Codex CLI reattachment

2026-11-17 · `swarm reattach --cli codex`

The CLI previously rejected every provider except Claude, even though the daemon
already supports external Codex conversation IDs and idempotent native-identity
adoption. Codex now has a separate discovery path that feeds that existing launch
contract without changing the protocol or adapter architecture.

## Behavior and boundaries

- Default discovery reads `~/.codex/sessions` metadata for `source: "cli"` only.
  `--session UUID` selects one native conversation; internal subagents and other
  Codex product sources are skipped, including subagents with deleted worktrees.
- Both `rollout-<timestamp>-<thread UUID>.jsonl` and modern
  `rollout-<timestamp>-<thread UUID>_<physical UUID>.jsonl` are supported. Metadata
  IDs must match the filename's stable thread UUID. Duplicate IDs with conflicting
  directories refuse the operation before any launch.
- Only the first metadata record is parsed. Compressed-only `.jsonl.zst`, native
  index-only histories, and native names stored separately are not discovered.
  Imported names initially use `codex <UUID prefix>`.
- The native root may itself be an alias. Descendant directory symlinks are not
  traversed; matching nonregular rollouts are refused. Anchored `os.Root` reads,
  no-follow/nonblocking opens and inode comparisons constrain discovery. Limits:
  16,384 directory entries, depth 8, 4,096 rollout files, 64 KiB per metadata record,
  16 MiB total metadata. Invalid matching metadata or missing CLI cwd refuses
  discovery before any launch; selecting one UUID avoids unrelated rollouts.
- `--dry-run` neither stops nor launches a provider and never rewrites history.
  Native metadata does not prove that the original CLI stopped. Actual imports
  require `--take-over`, acknowledging the owner already stopped the original
  Codex CLI. The command does not kill Codex processes.
- `--all --dry-run` also lists `archived_sessions`. An actual import containing
  an archived conversation refuses before launching anything and gives
  `codex unarchive UUID` guidance. Native archive state is never changed by swarm.
- The daemon does not preserve arbitrary `CODEX_HOME` or `CODEX_PROFILE` selectors.
  Imports therefore refuse custom homes/profiles, allowing an alias of the default
  home. Custom homes remain inspectable with `--dry-run`. Default `HOME` and the
  ordinary launch environment are forwarded. Managed Codex logins redirect native
  history, so the daemon refuses external Codex adoption while a managed login is
  active and tells the owner to return to native sign-in.

Archive behavior was corrected before implementation after inspecting the primary
upstream test
[`thread_resume_rejects_archived_session_by_id`](https://github.com/openai/codex/blob/main/codex-rs/app-server/tests/suite/v2/thread_resume.rs).
It explicitly expects archived UUID resume to fail and recommends native unarchive.
Modern logical/physical filename handling follows the upstream
[rollout resolver](https://github.com/openai/codex/blob/main/codex-rs/rollout/src/list.rs).
The initial test assumption that raw custom-home forwarding guaranteed preservation
was also corrected before implementation after tracing the daemon environment
allowlist and managed-login admission.

## Failing-first evidence

Run from the fresh checkout. The original command regression run:

```sh
go test ./cmd/swarm -run TestCodexReattach -count=1
```

failed because `--cli codex` exited 2 with Claude-only usage and `--session` was
undefined. Follow-up regressions failed before each corresponding fix:

```text
TestCodexReattachFindsPhysicalRolloutAndSkipsSubagents:
  exit=1 ... invalid Codex session metadata or conversation id
TestCodexReattachRefusesArchivedBeforeLaunching:
  exit=0 launches=2
TestCodexReattachSkipsSubagentWithDeletedWorktree:
  exit=1 ... session cwd is not an existing directory
TestCodexExternalResumeRefusesManagedHomeRedirection:
  external native discussion redirected into managed home: runtime=... err=<nil>
```

The final focused regression commands passed:

```sh
umask 022
go test ./cmd/swarm -run 'TestCodexReattach|TestClaudeReattach|TestRunReattach' -count=1
go test ./internal/skeleton -run 'TestCodexExternalResumeRefusesManagedHomeRedirection|TestExternalResumeIsIdempotentByProviderIdentity|TestComposeLaunchSpec_External' -count=1
```

`umask 022` is necessary for the existing login fixture's private-directory policy
in this environment; with the shell's default `0002`, it refuses fixture storage
before reaching the test assertion. Fixtures use isolated native homes and fake
launch clients. No live user conversations were stopped or imported, and no
billable Codex turn was started by this verification.
