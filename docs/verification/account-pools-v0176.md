# Account pools v0.17.6 verification

Investigation and installed acceptance date: 2026-10-12. Signed [v0.17.6](https://github.com/Nathandela/swarm/releases/tag/v0.17.6) is published and installed from main `93fb14b5b5125637a8ce80481bbfda15b1cf1a21`. The qualified-runtime defect is corrected; the live Codex account still has a separate configuration-writer hold described below. This record does not claim unrestricted rotation acceptance.

## Installed reproduction

On v0.17.5, enabling the Codex pool and requesting a new discussion through owner RPC returned `launch: accounts: private account or configuration is unavailable; review Accounts before resuming`. The previous disabled setting was restored after that experiment. The ambient CLI was Codex 0.162.1, whereas account configuration and enrollment require characterized 0.160.0. Claude already retained its qualified 2.1.289 executable; Codex followed the mutable npm installation.

Seven enrolled accounts were present: four Claude and three Codex. Only one Codex account had remaining observed capacity; the other two were exhausted. Initial visible coverage was seven managed/four unmanaged Claude discussions and one managed/twenty unmanaged Codex discussions. Thirteen unmanaged Codex discussions were running, and most used unqualified 0.161/0.162 versions. Native history compatibility for those versions is not asserted.

Daemon logs also contained failed resumes after unmanaged auth/CLI recycling. That path could stop a source before discovering that newly enabled account assignment could not prepare a successor. Doctor was green despite these application-level failures.

## Corrections and regression boundaries

- Managed Codex launch, enrollment, availability checks and recovery select a qualified runtime. Exact operator-provisioned native 0.160.0 is retained with content and file-identity checks, using the existing private Claude runtime mechanism. The daemon performs no downloads and does not change global authentication or the ambient CLI. Tampered retained state refuses fallback.
- Enabling a pool discovers compatible, observed, idle running discussions. The existing durable recycler owns stopping and resuming. Preflight locates history, compiles the native resume, selects an eligible account and freezes the private configuration before any stop. The selected binding and projection survive daemon restart; credential retirement retains pending destinations. Active turns, input/permission holds, controllers, duplicate conversations, unsupported versions and changed configuration hold the source.
- Upgrade worker capability 4 fences older readers from retained Codex enrollment and running-discussion enrollment plans. Existing protocol and shim wire versions remain unchanged.
- PR [#54](https://github.com/Nathandela/swarm/pull/54) adds explicit relocation for resumed discussions whose checkout disappeared. Connected-code review and targeted resume/PTY checks passed. Its switch-style lint correction was pushed to the contributor branch, and all official CI gates passed before merge.

New regression tests cover both providers, exact conversation/account preservation, one successor, durable restart, pending CLI-update adoption, pool disable before stop, CLI-refresh opt-out, unsafe sources, retained runtime restart/vendor deletion, tampering and upgrade compatibility. Detached native worker tests exercise account creation and cancellation without authenticating a real subscription.

The official Codex `rust-v0.160.0` Linux amd64 musl archive was verified against GitHub's published asset SHA-256 `306865417d4ee7a927785852910a527f41e1e159add390ac5ae3accb67d44a13`. Its private executable content SHA-256 is `12eb3e81114588aca3b7998f4f19e8997b056aca08e57a7ca7c8a3ec8c652aad`.

## Gates and deployment

- PR [#55](https://github.com/Nathandela/swarm/pull/55) merged after exact-source branch [CI 38280699430](https://github.com/Nathandela/swarm/actions/runs/38280699430) and PR [CI 38280706207](https://github.com/Nathandela/swarm/actions/runs/38280706207) passed. The latter needed a retry for the existing reconnect-banner timing failure (`swarm-1r4`); no check was bypassed.
- Local build, vet, lint (zero issues), and the full normal test suite passed. Focused onboarding, runtime-retention, cancellation and quota-denial regressions passed for both providers, including race-instrumented quota-denial preflight. Full local race execution failed three native/fixture timing checks: `TestDetachedCodexNativeSocketAlias`, `TestClaudePrivatePTYAttachAndReady`, and `TestOrphanSweep_InstanceDeathClosesEveryOpenItemBeforeTheTerminalSessionStatus`. The orphan check passed in isolation; the Claude stopped-writer timeout also reproduced with the unchanged main enrollment implementation. These VM boundaries are tracked in `swarm-bse` and `swarm-of8`; the full local race suite is not reported green.
- Tag [release gates 38283733398](https://github.com/Nathandela/swarm/actions/runs/38283733398) passed on attempt 2. Attempt 1 failed the synthetic shim startup deadline in `TestLaunchManagedClaudeInjectsDiagnosticsAndFallbackAfterPolicyFilter/owner-false`; that test passed separately with `-race`, then the failed Linux job and dependent publishers passed on retry. The immutable tag and source did not change. All release gates, including the complete race suite, passed before publication at 17:52:18 UTC.
- `swarm upgrade --stage --json` verified and staged the signed release; activation installed it and deferred automatic restart around a working discussion. Explicit daemon restart preserved PTYs but its readiness command timed out. The replacement subsequently answered as 0.17.6 and its `/proc/<pid>/exe` inode matched `/usr/local/bin/swarm`. Upgrade convergence then completed using the existing systemd user bus; all nine doctor checks were OK.
- Before activation: 206 records, 24 running shims, seven accounts. Immediately afterward: identical records, conversation IDs, shim PID/start identities and credential generations. After disposable canary cleanup: 209 records, all original histories retained, all 23 non-canary original shims still running with unchanged identities, and the same seven account generations. Both provider switches were restored to their original values (Claude enabled, Codex disabled).

## Installed native experiments

### Claude: creation and automatic running-discussion onboarding

Fresh managed discussion `a3gmgftyzjosf3nm` launched on the retained private 2.1.289 executable and returned `SWARM_CLAUDE_ROTATOR_OK` from the real provider. Its selected account had completed native first-use setup.

To test onboarding independently, Claude was briefly disabled while creating one disposable unmanaged discussion with qualified 2.1.289, then immediately re-enabled. Source `kzc3e5rb3rffsabr` returned `SWARM_CLAUDE_ENROLL_OK`. Once idle, the durable recycler created exactly one managed child `bbhcdgrza5bhx6ki`, preserving conversation `54b1f6fd-df74-434a-ab92-d77e944d47dc` and the preselected account/projection. The child answered “What exact marker did you return in your previous reply?” with the original marker, which was absent from that prompt. The enrollment plan reached `complete`. Both managed canaries were then stopped through the owner kill operation, retaining history and `native-writers-stopped.json` with `writers_stopped: true`.

### Codex: runtime correction and safe configuration hold

Unmanaged qualified 0.160.0 source `gpwubeqar6kkbtpd` returned `SWARM_ROTATOR_CANARY_OK` from the real provider before activation. Enabling the installed pool retained the verified private 0.160.0 executable and used it for account workers. The original unsupported-runtime launch error no longer applied, but a fresh managed launch and this source's onboarding both refused with `account configuration conflict: configuration-writer-active`.

The only account with observed capacity already served an existing running managed discussion. Its frozen owner `config.toml` digest differs from the current owner file. Installing the new owner-global configuration would mutate that account's shared native profile while the discussion still owns a writer. The refusal is the existing safety fence, not a reason to remove proof files or kill that discussion. No onboarding plan or successor was created for the Codex canary; its original process stayed running until explicit test cleanup. Actionable resolution and safe configuration-cohort design are tracked in `swarm-7ga`. Ending all affected writers normally is required by the current profile-update contract before a new owner launch can refresh that cohort.

### Account creation and cancellation: both providers

Native Codex device-code job `587489a08acd45059664f233edce5772` and Claude personal-login job `2791e32ebd95442888508783fc961dcf` reached `authenticating`, using retained 0.160.0 and 2.1.289 respectively. Both were cancelled; the Claude job briefly reported `cancelling` before `cancelled`. Their exact worker processes exited and temporary job directories were collected through the guarded cleanup path. No account was admitted and no existing credential generation changed. Authorization codes and URLs were not included in the evidence output.

## Acceptance limits and follow-up

- Browser sign-in and admission of a new real subscription were not completed. Native worker startup/cancellation does not establish authenticated identity admission. Two Claude profiles still need native first-use setup; verified login alone does not complete it.
- Authenticated Codex automatic onboarding and two-healthy-account quota failover remain unproved on this VM: one account has capacity but the configuration-writer hold above applies, and two accounts are exhausted. Controlled two-account acceptance remains `swarm-n21`; quota exhaustion was not manufactured.
- Codex 0.161/0.162 running sources remain unqualified and unmanaged. They are deliberately left running; qualification is tracked in `swarm-nvq`.
- Existing timing-sensitive test cleanup left 40 task-owned synthetic shim/agent processes. Those exact processes were terminated after PID/start checks; all production identities were rechecked unchanged. Fixture cleanup is tracked in `swarm-1rh`. Unrelated VM processes and caches were preserved.
