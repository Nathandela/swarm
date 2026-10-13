# Codex minimum version and migration readiness

Date: 2026-10-13. Baseline installed release: v0.17.6. Implementation target: v0.17.7.

## Policy correction

The previous implementation admitted exactly Codex 0.160.0 and selected its retained copy even when the owner installed a newer release. The requested policy is now a minimum stable version: Codex 0.160.0 and later. Managed launch, native sign-in, availability/recovery, existing-discussion onboarding and history transfer use the installed executable, with the existing ownership, exact process/executable identity, configuration and stopped-writer checks. A missing installation refuses execution instead of silently downgrading to a retained binary.

Old retained executables remain intact for running processes and compatibility inspection. Claude's existing retained-runtime policy is unchanged. Credential erasure retains its independently characterized inventory; this change does not authorize deletion of unknown later-version artifacts. Worker capability 5 prevents rollback to a binary that cannot manage newer Codex sessions, native enrollment workers or pending account lifecycle operations. Protocol and shim wire versions are unchanged.

## Regression and native evidence

Regression cases cover the minimum boundary, later patch/minor/major versions, malformed and prerelease rejection, following an npm upgrade, no old-cache fallback, startup method availability, onboarding a 0.162.1 source into one 0.162.1 managed child, original conversation continuity, account history round trip and interrupted publication, pre-stop refusal below the minimum, and rollback compatibility. The detached native sign-in fixture also completes its authenticated-proof flow on 0.162.1 without admitting an account.

Automatic onboarding and recovery also refuse to move a newer running Codex discussion to an older installed CLI. This guard does not pin the executable: equal versions and forward upgrades are allowed. Its onboarding regression passed with `-race`.

The initial policy commit `fda6e464` passed [complete PR CI 38381090281](https://github.com/Nathandela/swarm/actions/runs/38381090281), including the full race suite. The final downgrade guard requires the same complete gates before merge and publication.

Build, vet and lint passed locally. The first full normal suite hit `/tmp` exhaustion during linker/build fixtures and sandbox access restrictions on `/tmp/codex-daemon-1000`; it also exposed stale fixtures that still considered 0.161.0 unsupported. Those fixtures were changed to versions below the minimum. The task's build cache was moved to the spacious workspace volume, and affected native/race checks were rerun outside the per-command sandbox. The full first local suite is not reported green. Required complete CI gates remain the merge and release boundary.

The actual installed Codex CLI was 0.162.1. Its npm launcher had mode 0775, which failed the existing ownership check. Only group/other write permission was removed (0775 to 0755); contents and owner updater access were preserved. A disposable daemon and private candidate profile then advertised installed 0.162.1, completed the native `account/login/start` device-code RPC and reached `authenticating`. Job `f0575a2b27a5431b8c85ed8bdd8b4547` was cancelled; its exact worker exited, its private job directory was collected and no account was admitted. Codes and authorization URLs were not printed. This establishes native startup/cancellation, not successful browser sign-in or account admission.

## Live migration readiness

A read-only check of the actual native history resolver and post-spawn CLI observations found 13 visible unmanaged running Codex discussions, plus one already managed. Eleven unmanaged sources had their original history and matching CLI observation; most were previously excluded solely by the exact version gate. Eligibility still depends on idle/controller state, model proof and destination configuration/capacity.

Two sources had no recorded conversation ID and no post-spawn observation. The current WIP's genuine native thread ID and rollout were independently found, but native startup occurred 51 seconds after Swarm reserved the discussion; the unknown-ID resolver accepts only 30 seconds. Automatic association was not fabricated and the time window was not silently widened. These exceptions remain running; explicit identity recovery is tracked in `swarm-e8j`.

The only Codex account with observed remaining capacity still has a live discussion using an older owner-configuration cohort. Swarm refuses to overwrite that shared profile (`configuration-writer-active`, `swarm-7ga`). Two other Codex accounts are exhausted. The minimum-version correction does not remove this independent fence or prove real two-account failover (`swarm-n21`).

Enabling each provider is the existing bulk onboarding route for eligible running discussions: wait for a safe idle point, prepare the account/history/settings, then stop and resume exactly once. Ended history entries are resumed individually when needed. Do not blindly stop missing-proof exceptions or revive every ended discussion.

No live pool was toggled and no production discussion was stopped during these checks. After isolated native test cleanup, all 211 records, 24 running discussion identities, seven account generations and both switches were unchanged. Original switches: Claude enabled, Codex disabled.
