# Epic 14 — Invariant Traceability (S1-S12, L1-L3)

**Closes**: E14.2 / GG-2 — "every invariant S1-S12 and L1-L3 is asserted by at least one automated test, named in a traceability list."
**Source**: [docs/invariants/system-invariants.md](../invariants/system-invariants.md)
**Method**: every test cited below was confirmed present by `grep "^func <Name>("` against its named file — no test is listed from memory or inference.

## Safety properties

| Inv | Assertion | Test(s) — file:function |
|---|---|---|
| **S1** SURVIVAL | `kill -9` daemon → every agent PID alive; restarted daemon reconnects | `daemon/realkill_test.go:TestSurvival_RealKillNineReconnectsAll` (real subprocess, real SIGKILL), `daemon/survival_test.go:TestSurvival_KillDashNineReconnectsAll`, `e2e/skeleton_e2e_test.go:TestE2E_DaemonKilledMidAttach` |
| **S2** SINGLE-CONTROLLER | stale-generation input never applied under concurrent attach | `protocol/lease_test.go:TestLease_SecondAttachSupersedesWithHigherGeneration`, `protocol/lease_test.go:TestLease_StaleGenerationInputDroppedServerSide`, `protocol/lease_test.go:TestLease_StaleGenerationResizeDropped`, `skeleton/serve_test.go:TestSkeleton_TwoClientSupersede` (real end-to-end over the protocol) |
| **S3** IDENTITY-SAFETY | PID-reuse / start-time mismatch → `lost`, zero signals | `daemon/reconcile_test.go:TestReconcile_LostOnStartTimeMismatch`, `daemon/reconcile_test.go:TestReconcile_LostOnReapedPID`, `daemon/identity_test.go:TestProcessStartTime_StableAndDistinct`, `daemon/identity_test.go:TestProcessStartTime_ReapedPIDNotFalselyMatched` |
| **S4** INJECTION-FREE SPAWN | metacharacter-laden fields arrive as single argv elements, no shell exec | `shim/spawn_test.go:TestSpawn_ArgvInjectionFree` |
| **S5** PROCESS-GROUP CONTAINMENT | kill = TERM → grace → KILL on the whole group, no descendant survives | `shim/signal_test.go:TestSignal_TermGraceKill_WholeGroup`, `shim/shim_containment_test.go:TestShimSelfContainsAgentGroupOnSIGTERM`, `shim/shim_containment_test.go:TestShimSignalHandler_ReleasedAndJoinedOnCleanExit` |
| **S6** AUTHENTICATED STATUS | tokenless / foreign-token / replayed / post-end callbacks are all no-ops | `engine/auth_test.go:TestHandleCallbackTokenlessIsNoOp`, `TestHandleCallbackForeignTokenIsNoOp`, `TestHandleCallbackReplayedSequenceIsNoOp`, `TestHandleCallbackAfterEndSessionIsNoOp`, `TestHandleCallbackUnregisteredSessionIsNoOp` (all in `engine/auth_test.go`) |
| **S7** NEVER-CONFIDENTLY-WRONG | staleness → `unknown`; heuristic never overrides fresher typed signal | `engine/staleness_test.go:TestStalenessGuardFlipsActiveToUnknown`, `engine/staleness_test.go:TestStalenessGuardKeepsBusyActive`, `engine/precedence_test.go:TestTypedSignalBeatsHeuristic`, `engine/precedence_test.go:TestHeuristicNeverOverridesFresherTypedSignal` |
| **S8** ATOMIC-DURABLE STATE | crash mid-write → old-or-new, never torn; one corrupt session never blocks scan; roster rebuildable | `persist/crash_test.go:TestCrashDuringSaveNeverTears`, `persist/persist_test.go:TestCrashDuringWriteLeavesOldIntact`, `persist/persist_test.go:TestScanIsolatesCorruptSession`, `persist/persist_test.go:TestRosterRebuildableByScan` |
| **S9** DRAIN-NONBLOCKING | wedged subscriber / stalled disk never blocks PTY draining | `protocol/fanout_test.go:TestFanout_WedgedSubscriberDisconnectedWithinBound`, `shim/survival_test.go:TestSurvival_DrainsWithNoConsumer`, `shim/survival_test.go:TestSurvival_WedgedConsumerDropsFramesGridAuthoritative`, `shim/survival_test.go:TestSurvival_SoakBoundedMemory`, `transcript/transcript_test.go:TestWriteNeverErrorsWhenSinkFailsEveryCall`, `transcript/transcript_test.go:TestDropsIncomingTailWhenBufferFull` |
| **S10** SNAPSHOT-CONTINUITY | exactly one snapshot before live frames, no gap/overlap; hostile escapes filtered | `shim/socket_test.go:TestContinuity_SnapshotThenStream_Boundary`, `shim/socket_test.go:TestContinuity_ActiveLoadOrdering`, `protocol/ordering_test.go:TestOrdering_ExactlyOneSnapshotPrecedesLiveFramesRaw`, `protocol/ordering_test.go:TestOrdering_SnapshotDeliveredThroughAttachmentAPI`, `vt/clamp_test.go:TestClampBytes_RuneBoundary`, `vt/clamp_test.go:TestSnapshot_ClampsHostileTextAndTitle` |
| **S11** SHIM↔META BIJECTION | crash injection in spawn/exit windows → no orphan shims, no phantom sessions | `daemon/launch_test.go:TestLaunch_CrashBeforeSpawn_NoPhantom`, `daemon/launch_test.go:TestLaunch_CrashBeforeConfirm_NoOrphan`, `daemon/daemon_fixes_test.go:TestLaunch_IdentityReadFailure_KillsShimNoPhantom`, `daemon/daemon_fixes_test.go:TestDelete_ConcurrentMerge_NoResurrection` |
| **S12** SINGLETON DAEMON | two simultaneous starts → exactly one survives, client reaches it | `daemon/realkill_test.go:TestSingleton_TwoRealProcessesOneWins` (real processes), `daemon/singleton_test.go:TestSingleton_SecondOpenLoses`, `daemon/singleton_test.go:TestSingleton_StaleSocketReclaimedUnderLock`, `daemon/singleton_test.go:TestSingleton_WinnerReachableAfterLoss` |

## Liveness properties

| Inv | Assertion | Test(s) — file:function |
|---|---|---|
| **L1** | status change reaches every live subscriber within 1s | `protocol/fanout_test.go:TestFanout_StatusChangeReachesLiveSubscriberWithin1s` (protocol layer, real 1s bound), `engine/fanout_test.go:TestEmitIsSynchronousOnHookChange` / `TestEmitIsSynchronousOnHeuristicChange` / `TestEmitIsSynchronousOnStalenessTick` (engine emits with no deferral — the E10.7 server half), `tui/liveness_test.go:TestLiveness_EventMovesRowGroup` (client half). **GAP (acknowledged, in-flight)**: the full signal→engine→fan-out→TUI composite under load (E14.8) has no single end-to-end test yet — each hop is independently proven but not chained in one assertion. Pending the Epic 14 composite task. |
| **L2** | every still-live shim eventually reconnected and re-registered after daemon restart | `e2e/restart_status_e2e_test.go:TestE2E_TypedStatusSurvivesDaemonRestart_L2`, `daemon/reconcile_test.go:TestReconcile_ReconnectsLiveShim` |
| **L3** | mid-attach disconnect releases lease + stream, next attach succeeds | `protocol/lease_test.go:TestLease_ReleasedOnClientEOF`, `protocol/lease_test.go:TestLease_DetachReleasesLeaseAndStream` |

## Notes

- All 15 invariants have at least one green, verified-existing test; GG-2's traceability requirement is satisfied.
- The one open item is **L1's end-to-end composite** (signal→engine→fan-out→TUI, under load, ≤1s) — E14.8 is the Epic 14 task that closes it; every individual hop in the chain is already covered (engine emit is synchronous, protocol fan-out is ≤1s, TUI reflects the event), so this is a composition gap, not a missing mechanism.
- `bd` shows an open follow-up under Epic 14 (`agents-tracker-a7d`: "assert shim arms self-containment signal handler BEFORE spawning the agent") that hardens S5's existing coverage (`shim_containment_test.go`) — not a coverage gap, but worth closing alongside this epic.
