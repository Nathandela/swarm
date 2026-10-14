---
title: Phase A verification — accepted drift, selector refusals, ported fixes, robustness
entity: verification-phase-a
type: verification
tags: [accounts, codex, journal, verification, adr-032]
related: [adr-032, adr-028, adr-014, adr-010, account-pools]
---

# Phase A verification

Date: 2026-10-14. Branch `fix/2pk-phase-a`, from main `e538d1e9`. Evidence logs are kept in the
lab directory `swarm-lab/impl/` (`2pk.1` to `2pk.4`, `phase-a`).

## Scope

- **2pk.1 (A1, A2), ADR-032.** Launch, resume, recovery and onboarding accept native configuration
  drift (they log it), and the owner's settings win on refresh. Writer holds follow process
  liveness. The `configuration-writer-active` fence is removed from the launch path.
- **2pk.2 (A3, A4).** A launch moves to the next account only when the refusal is scoped to one
  account. Every refusal is shown in the owner error, the daemon log and the TUI banner.
- **2pk.3 (A5 to A7).** The Codex model scan skips oversized non-`turn_context` lines. Managed
  Codex keeps the sandbox network override (ADR-028 amendment). Every Codex app-server runs in the
  session cwd (ADR-010 amendment 5). A refused socket dial names the sandbox.
- **2pk.4.** Journal retention is bounded (ADR-014 A12). Backend readiness has a budget. The
  managed shim reaps orphans. Resume history degrades when the paginated rollout is missing.
  Test binaries sweep leaked helpers (B94 allowlist).
- **Final-review fixes.**
  - F1: onboarding of running discussions has its own per-provider switch,
    `account-onboarding.json`, off by default (ADR-032 D6).
  - F2: `journal.Open` unlinks retained empty segments except the newest.
  - F3: only a launch-dial failure writes the `backend_never_served` degraded marker, and only that
    marker widens the Codex resume windows.
  - F4: profile-scoped refusals and invalid credentials are retried on the next account. The dead
    `configuration-writer-active` arm is deleted.
  - F5: ADR-032 D1 records that the private-file check on `config.toml` was dropped.

## Test-first evidence

Each item has a test commit before its fix commit. Each `red.txt` shows the new tests failing for
the intended reason on the unmodified code. Each `green.txt` shows the same tests passing after
the fix.

| Item | Red tests (abridged) | Evidence |
|------|----------------------|----------|
| 2pk.1 | `TestPrepareNative_*Drift*`, `TestPrepareNative_PinnedRecoveryAfterOwnerDriftReprojects`, `TestWriterHold_AKilledShimWithoutStoppedProofDoesNotBlock`, `TestLaunch_AnIdleWriterOnTheSameAccountDoesNotBlockANewDiscussion` | `impl/2pk.1/{red,green}.txt` |
| 2pk.2 | `TestBindAccountLaunch_*`, `TestLaunchRefusal_IsWrittenToTheDaemonLog`, `TestBanner_LaunchRefusalNamesTheAccountAndRemedy`, `TestSelectInitial_SkipsAnExhaustedAccountThatIsNotMarkedDenied` | `impl/2pk.2/{red,green}.txt` |
| 2pk.3 | `TestReadCodexTranscriptModel_SkipsAToolOutputLineLongerThanTheLineCap`, `TestNativeProjectionKeepsTheOwnerSandboxNetworkOverride`, `TestBackendLaunch_*`, `TestDispatchAgentVerb_APermissionRefusedDialDoesNotBlameAMissingDaemon` | `impl/2pk.3/{red,green}.txt` |
| 2pk.4 | `TestOpen_MaxTotalBytesDropsTheOldestSegmentsBeforeLoading`, `TestBackendReady_*`, `TestManagedShimReapsAnAdoptedOrphanWhileTheSessionRuns`, `TestResumeHistory_CodexMatchesADegradedLaunchStartedAfterTheBackendBudget`, `TestB94_EveryExportedSymbolIsReachableFromProduction` | `impl/2pk.4/{red,green}.txt` |
| F1 to F4 | `TestAccountEnrollmentRequiresTheOnboardingSwitch`, `TestOpen_UnlinksRetainedEmptySegmentsExceptTheNewest`, `TestBackendNeverServed_OnlyTheLaunchDialFailureIsDegradedStartEvidence`, `TestBindAccountLaunch_RetriesEveryProfileScopedRefusal` | `impl/phase-a/red.txt` |

F3 and F4 changed the expectations of earlier tests on purpose, at the reviewer's request. The
degraded-window table now uses `backend_never_served` and also checks that a
`backend_unavailable` marker gives no degraded start. The retry tests use codes that the code can
still raise.

## Final gates

The gates ran on the integrated tree in a checkout path without "agent" in it, with
`GOFLAGS=-buildvcs=false` and the inherited `SWARM_*` session variables unset. They started only
once the host load was below 10. Results are recorded in `impl/phase-a/gates.txt` and
`gates-final.txt`.

- `go build ./...` and `go vet ./...` pass. `golangci-lint run` reports 0 issues, the same as main.
- `go test ./...` passes in every package.
- `-race` on accounts, accountconfig, accountcheck, skeleton, protocol, tui, daemon, cmd/swarm,
  journal and shim leaves only failures that main also has:
  - `accountcheck` `TestOwnedCheckCancellationContainsDetachedWriter` fails the same way on main.
  - `shim` `TestManagedShimContainsDetachedWritersBeforeDurableProof` fails the same way on main.
  - `skeleton` `TestResumeMigration_AssemblyWiresTrustedHomeFilesystemResolver` hits an
    intermittent data race. `Serve` sets `coreAPI` fields after `newCoreAPI` has started its watch
    goroutine. That code is unchanged from main.
- These failed once under load and then passed 3 of 3 runs alone:
  - `skeleton` `TestGapReauth_RepeatedRetrySchedulesHaveOneWorker`.
  - `daemon` `TestSurvival_RealKillNineReconnectsAll` (under `-race`).
- These failed only because of the environment:
  - `cmd/swarm` `TestRunHook_*` and `hookclient` `TestEnvHookSocket_UnsetMeansPostSmartNeverDials`
    failed when the live `SWARM_*` session variables were inherited.
  - `tui` `TestLaunch_NameFieldRendersAfterDirectory` fails when the checkout path contains "agent".

## Residual risks

- A quota or auth rotation that starts after an owner configuration change is still refused,
  because successor feasibility requires the two projections to be equal. The fix is deferred to
  Phase C (`swarm-2pk.6`).
- Onboarding of running discussions stays off until the cross-account resume acceptance passes.
  The TUI control for it arrives in Phase C.
- Tests inherit the live `SWARM_*` session environment when they run inside a Swarm session
  (`swarm-cd5`).
- The `Serve` and `newCoreAPI` field-assignment data race is pre-existing (`swarm-nq2`).
- The live daemon log shows VT emulator panics (`ultraviolet.Buffer.InsertLineArea`). They are
  unrelated to Phase A and are not yet tracked.
