What this repo does: Swarm runs coding-agent sessions under durable per-session shims, presents them in a terminal dashboard, and exposes selected controls to a paired Android phone. This audit assumes one owner, tens of concurrent sessions, repeated daemon/backend reconnects, and long-lived machine and phone state.

Ten Luna agents performed bounded audits, implementation, and independent reviews. This is a risk-based review of the main flows, not a claim that every line was read or that the repository has no remaining bugs. Findings below describe the audited state before this campaign's fixes; the accompanying verification record reports the resulting checks.

## Must fix

1. **A reused operation ID can report success for another command** (`internal/daemon/launch.go`, `ClaimIdempotentOp`).
   - **What this is:** Kill and delete retain outcomes so retrying the same request does not repeat its effect.
   - **Problem:** A successful `kill(A, X)` followed by `delete(B, X)` returned the old success without deleting B. Prepared and executing records could also permit a conflicting action to run under the same ID.
   - **Fix:** Reject an existing record with a different action or session before interpreting its phase. Implemented through `agents-tracker-rgk9.1`, preserving launch-store semantics and exact retries.
   - **If we skip it:** A buggy authenticated client receives a false destructive-command result or reuses another command's execution record.

2. **The phone can keep reconnecting behind an expired mailbox head** (`mobile/relay.go`, `drainPhone` and the reconnect loop).
   - **What this is:** The phone validates encrypted retained frames before acknowledging them.
   - **Problem:** An authenticated head older than the ten-minute age bound is refused without acknowledgment. With no later machine traffic, reconnects encounter the same head; successful dials also reset backoff before any delivery progresses.
   - **Fix:** Existing issue `agents-tracker-y86g` owns recovery and reconnect cadence. Preserve the current explicit roster-refresh/discard contract until its recovery policy is resolved; this campaign does not silently discard retained mail.
   - **If we skip it:** A phone returning to a quiet machine can remain stale and generate repeated relay connections.

3. **The older board layout can overwrite the latest choice** (`internal/tui/layout_store.go`, `applyLayout` and `saveLayoutCmd`).
   - **What this is:** Applying board options changes the visible layout immediately and saves it asynchronously.
   - **Problem:** Apply A, apply B, finish B's save, then finish A's save: the board displays B but restart restores A. Atomic replacement prevents partial JSON, but does not order commands.
   - **Fix:** Share a save generation and mutex across model copies; skip stale commands and serialize active store writes outside the UI loop. Implemented through `agents-tracker-rgk9.4`, with both overlapping-save and reverse-execution regressions.
   - **If we skip it:** Rapid option changes silently restore an earlier preference on the next start.

4. **The onboarding instructions advertise an insufficient Go version** (`README.md`).
   - **What this is:** The README tells contributors which toolchain builds the project.
   - **Problem:** Its badge and prose said Go 1.25 while `go.mod` requires 1.26. Version-specific “latest release” prose in README and AGENTS had also drifted.
   - **Fix:** Match the module's Go 1.26 floor and point release summaries to the releases page. Implemented through `agents-tracker-rgk9.5`.
   - **If we skip it:** New contributors start with an unsupported toolchain and stale release guidance.

## Should fix

5. **A call queued on a closed backend can record possible execution** (`internal/appserver/appserver.go`, `callAtWriteBoundary`).
   - **What this is:** A callback durably records execution immediately before a serialized app-server write.
   - **Problem:** The old liveness check preceded the writer lock. Closing the connection or canceling the context while the call waited could still run that callback, making a request that could not be dispatched appear ambiguous to ContextGuard or the composer.
   - **Fix:** Recheck closure and cancellation under the writer lock before the callback, cleaning up the waiter on refusal. Implemented through `agents-tracker-rgk9.2`; queued-close and queued-cancellation tests exercise the actual interleavings.
   - **If we skip it:** Backend disconnects can leave unnecessary recovery holds and suppress safe retries.

6. **A retired Android terminal interface still has a full implementation** (`PhoneSurface.kt`, `TerminalFallbackScreen.kt`, `TerminalFallbackView.kt`, `TerminalWatchLane.kt`).
   - **What this is:** ADR-017's Android amendment requires every session to use the conversation shell, disabling its composer inline when necessary.
   - **Problem:** No current route called the terminal drawer or its watch reconciliation, yet the separate screen, bridge, timer, and tests remained. Whole-tree references, resources, manifests, and dynamic-name searches found no hidden entry point.
   - **Fix:** Remove the unreachable Android implementation, its terminal-only component options, and tests for deleted subjects. Implemented through `agents-tracker-rgk9.3`; retain Go/wire compatibility and tests for surviving capability, lifecycle, dispatch, and facade behavior.
   - **If we skip it:** Maintainers continue reviewing a second interface and watcher that the product cannot reach.

7. **A generated executable is tracked at the repository root** (`e2e2session`).
   - **What this is:** A 5,124,096-byte Mach-O probe executable remained in Git.
   - **Problem:** No build, runtime, packaging, configuration, or dynamic reference uses it. The sole textual reference is historical evidence that excluded it from a copy.
   - **Fix:** Delete it and ignore that exact root path, through `agents-tracker-rgk9.5`. Keep historical evidence unchanged.
   - **If we skip it:** Current checkouts carry a stale executable without a supported role. Removal does not shrink existing Git history.

8. **The remote activity audit has no retention bound** (`internal/skeleton/launchpresets.go`, `RecordRemoteActivity`).
   - **What this is:** Remote launch outcomes append owner-only JSON records to `remote-activity.log`.
   - **Problem:** The writer has no byte, record, or age limit. Repeated requests grow audit storage indefinitely.
   - **Fix:** Existing issue `agents-tracker-85yb` owns the retention/forensic tradeoff and the eventual bounded writer. This campaign preserves existing audit history.
   - **If we skip it:** Long-running remote use continues accumulating audit-only disk usage.

## Attack plan and implementation boundary

The executable plan is recorded in the design and child issues of **`agents-tracker-rgk9`**. Beads is the task tracker; this document records findings and decisions.

The first phase protects operation identity and the serialized backend write boundary. Tests must fail against the previous implementation before fixes are accepted. The second phase removes the proved-unreachable Android surface, keeping tests for surviving subjects, and fixes layout command ordering. The third phase removes the unsupported binary and corrects onboarding guidance. Integration requires independent review, the project quality gates, and committed/pushed evidence.

Independent Android review caught a generic main-thread wrapper guard being removed with terminal-specific tests. That guard was restored over all production Kotlin, with snippet-based negative controls for inline calls, proper command dispatch, and the wrong dispatch plane. This is why deleting old test files required a subject-by-subject disposition, recorded in [android-disposition.txt](android-disposition.txt).

Remaining work has different evidence requirements. Mailbox recovery needs an explicit retained-frame recovery contract; audit retention needs a policy before deletion. Existing macOS process-fixture and platform lint work is tracked in `agents-tracker-nsxw`, `agents-tracker-vyhy.10`, and `agents-tracker-4p4q`. The complete shortcut ledger is in [debt.txt](debt.txt); it records comment claims, including legacy comments, rather than treating each marked limit as a newly proved defect.

## Coverage

| Audit lane | Paths and flows reviewed | Limits |
|---|---|---|
| Runtime core | Daemon launch/reconcile/kill/delete, shim shutdown, persistence, idempotency, journals, transcripts, process identity/containment, worktrees, engine callbacks | Selective high-risk reads; not every test or source file |
| Native integration | Adapters, app-server boundary, OpenCode bridge, provider login/config custody, hooks, VT/status, ContextGuard dispatch | Existing adapter/attach issues retained; no live provider qualification |
| Remote | Protocol auth/replay, bounded wire frames, gateway inbound checkpoints/outbox, relay-v2 mailbox, phone ingress, push/enrollment boundaries | No cloud deployment, handset acceptance, Firestore emulator, or penetration test |
| Android | Session routing, composer capability, lifecycle, pairing/push paths, retired watcher reachability, Kotlin tests and source gates | No physical-handset or visual acceptance run |
| CLI/TUI | Startup, command dispatch, attach, reconnect, errors/status, upgrade/converge, layout storage | No interaction with the owner's installed daemon |
| Tooling | Build/dependency pins, release/deploy configuration, workflows, entry documentation, tracked binary references | No dependency vulnerability survey or release publication |
| Debt/review | All 34 ignore-aware marker hits; independent runtime, layout, and Android diff reviews | No arbitrary file splitting, dependency removal, or whole-repository rewrite justified by this evidence |

**Verdict:** The confirmed bounded fixes and dead-code removal improve the working baseline. Mailbox recovery, audit retention, and platform-gate debt still prevent an “ultra clean” or universally green claim.

**Lean:** The Android production diff removes 1,298 net Kotlin lines, plus the 5,124,096-byte executable; zero dependencies added or removed. Tests for retired subjects are removed separately and are not counted as production savings.

**Not checked:** Every source line, physical Android behavior, live provider compatibility, deployed cloud behavior, Linux runtime integration, fuzz/soak campaigns, and a fresh vulnerability audit. See [README.md](README.md) for exact checks actually run and their outcomes.
