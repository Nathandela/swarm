# Epic 14 — EARS Requirement Traceability Matrix

**Closes**: E14.7 / GG-3 — "every EARS requirement maps to at least one test, or an exclusion approved via ADR or audit waiver."
**Source**: [docs/specifications/system-spec.md](../specifications/system-spec.md), EARS Requirements section.
**Method**: every test cited was confirmed present by `grep "^func <Name>("` against its named file. Rows marked CI-enforced/doc-only are satisfied by a non-Go-test artifact, named explicitly. Rows marked VERIFY-pending have a built, gated, compile-verified test whose confirmation against live CLIs requires one deliberate human-run (billable, never automatic/CI).

## Daemon and shim lifecycle (D)

| ID | Requirement (short) | Coverage |
|---|---|---|
| D-1 | Client auto-starts detached daemon on cold state | `daemon/autostart_test.go:TestAutoStart_ColdStateDirSpawnsAndConnects` |
| D-2 | PTY owned by dedicated shim, independent of terminal/daemon | `e2e/skeleton_e2e_test.go:TestE2E_WalkingSkeleton_GG1`, `e2e/skeleton_e2e_test.go:TestE2E_Scenario3_SurvivesClientClose` |
| D-3 | Terminal close leaves running sessions unaffected | `e2e/skeleton_e2e_test.go:TestE2E_Scenario3_SurvivesClientClose` |
| D-4 | Daemon start rebuilds registry, reconnects live shims, marks gone ones `lost` by PID+start-time | `daemon/reconcile_test.go:TestReconcile_ReconnectsLiveShim`, `TestReconcile_LostOnStartTimeMismatch`, `TestReconcile_LostOnReapedPID` |
| D-5 | Daemon crash/upgrade: sessions continue, reconnectable, no data loss | `daemon/survival_test.go:TestSurvival_KillDashNineReconnectsAll`, `daemon/realkill_test.go:TestSurvival_RealKillNineReconnectsAll`, `daemon/version_test.go:TestVersionSkew_RestartIsSafe`, `e2e/killmidwrite_e2e_test.go:TestE2E_DaemonKilledMidInputWrite` (killed mid-input-write, not just mid-idle-attach), `daemon/soak_test.go:TestSoak_RestartUpgrade_50CyclesZeroLoss` (50 consecutive kill/restart cycles, zero session loss) |
| D-6 | State dir 0700, socket 0600; flock before bind; stale socket unlinked only under lock | `daemon/perms_test.go:TestPermissions_StateDirAndSocket`, `TestPermissions_LockUnderPrivateDir`, `daemon/singleton_test.go:TestSingleton_StaleSocketReclaimedUnderLock` |
| D-7 | Second daemon fails the lock and exits; client retries to the winner | `daemon/singleton_test.go:TestSingleton_SecondOpenLoses`, `TestSingleton_WinnerReachableAfterLoss`, `daemon/autostart_test.go:TestAutoStart_IdempotentWhenAlreadyRunning` |
| D-8 | Version skew at handshake surfaces a clear error naming `swarm daemon restart`, states it's safe | `protocol/handshake_test.go:TestHandshake_ServerVersionMismatchReturnsD8Error`, `TestHandshake_ClientDialReturnsIncompatibleVersion`, `daemon/daemon_fixes_test.go:TestVersionSkew_ErrorStatesRestartIsSafe`, `daemon/version_test.go:TestVersionSkew_DialDetectsAndNamesFix`, `TestVersionSkew_RestartIsSafe` |

## Sessions (S)

| ID | Requirement (short) | Coverage |
|---|---|---|
| S-1 | Launch spawns shim, execs CLI in fresh PTY in requested cwd, argv arrays only, own process group | `shim/spawn_test.go:TestSpawn_ArgvInjectionFree`, `TestSpawn_Cwd`, `TestSpawn_OwnProcessGroup`, `daemon/launch_test.go:TestLaunch_TwoPhaseHappy` |
| S-2 | Session persists all named fields (id, agent, cwd, options, env, timestamps, status, PID+start-time, conv-id, exit code, schema_version) | `persist/persist_test.go:TestSaveLoadRoundTripAllFields` |
| S-3 | Worktree toggle creates `.swarm/worktrees/<id>` + branch, errors if not a git repo, tears down on delete | `internal/worktree/worktree_test.go:TestCreateMakesWorktreeAndBranch`, `TestCreateInNonRepoDirErrors`, `TestRemoveTearsDownWorktree`, `daemon/hookpoints_test.go:TestPreLaunchPreDeleteRegisterWorktreeHooks`, `e2e/worktree_e2e_test.go:TestE2E_Worktree_LaunchRunTeardown` (composite: real launch → agent's actual cwd is the worktree → real delete tears it down, through the assembled daemon) |
| S-4 | Kill signals the process group (TERM→grace→KILL); outcome recorded | `shim/signal_test.go:TestSignal_TermGraceKill_WholeGroup`, `daemon/lifecycle_test.go:TestKill_TerminatesAndRecordsOutcome` |
| S-5 | Shim maintains VT grid + append-only transcript, capped/rotated, redraw frames collapsed | `vt/emulator_test.go` (grid state suite), `transcript/transcript_test.go` (rotation + spinner-collapse suite) |
| S-6 | Launch RPC carries allowlist-filtered client env; shim spawns with it, never the daemon's | `persist/persist_test.go:TestFilterEnvAllowlist`, `daemon/launch_test.go:TestLaunch_FiltersClientEnv`, `shim/spawn_test.go:TestSpawn_EnvIsCapturedNotInherited` |
| S-7 | Daemon enforces max concurrent session count, rejects over-cap with a clear error | `daemon/lifecycle_test.go:TestMaxSessions_CapEnforced` |

## Client protocol (P)

| ID | Requirement (short) | Coverage |
|---|---|---|
| P-1 | One UDS, control (NDJSON) + data (binary) planes, versioned/negotiated, framed with a max size | `protocol/codec_test.go:TestCodec_ControlIsSnakeCaseJSON`, `TestCodec_DataPlaneDemuxesByType`, `TestCodec_OversizedControlRejectedBeforeAlloc`, `wire/wire_test.go` |
| P-2 | Control ops (handshake/list/launch/kill/delete/attach/detach/resize/subscribe); data frames (input/output/snapshot) | `protocol/ops_test.go:TestOps_ListReturnsStampedViews`, `TestOps_LaunchForwardsAndFiltersEnv`, `TestOps_KillAndDeleteForwardLocalID`, `protocol/codec_test.go:TestCodec_EveryControlOpRoundTrips` |
| P-3 | Status event pushed to subscribers; slow/dead one disconnected via bounded queue, never blocking | `protocol/fanout_test.go:TestFanout_StatusChangeReachesLiveSubscriberWithin1s`, `TestFanout_WedgedSubscriberDisconnectedWithinBound` |
| P-4 | Client disconnect mid-attach: session continues, lease + stream released cleanly | `protocol/lease_test.go:TestLease_ReleasedOnClientEOF` |
| P-5 | Exclusive controller lease with generation id; stale-generation messages rejected | `protocol/lease_test.go:TestLease_FirstAttachGetsGeneration`, `TestLease_SecondAttachSupersedesWithHigherGeneration`, `TestLease_StaleGenerationInputDroppedServerSide`, `TestLease_StaleGenerationResizeDropped` |
| P-6 | Server re-validates every client-supplied field server-side | `protocol/revalidation_test.go` (7 tests: cwd existence/type, unknown agent, oversized options, huge dimensions, bad ids, out-of-range resize) |

## General view (V)

| ID | Requirement (short) | Coverage |
|---|---|---|
| V-1 | Lists sessions grouped Needs input / Working / Ready for review / Completed | `tui/general_test.go:TestGeneral_GroupsInFixedOrder`, `TestGeneral_EmptyGroupsOmitted`, `protocol/groups_test.go` (server-side derivation), `protocol/boundary_test.go:TestBoundary_ClientDoesNotReDeriveStatus` |
| V-2 | Status event reflected within 1s without user action | `e2e/l1composite_e2e_test.go:TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad` (the full chain, closed: real daemon, real hook callback, real protocol.Client, real rendered TUI, ≤1s under output load, fail-closed), plus `tui/liveness_test.go:TestLiveness_EventMovesRowGroup` (client half), `engine/fanout_test.go:TestEmitIsSynchronousOnHookChange` et al. (engine half), `protocol/fanout_test.go:TestFanout_StatusChangeReachesLiveSubscriberWithin1s` (protocol half) |
| V-3 | Keyboard-only nav: ↑/↓/j/k, Enter, Esc, Ctrl+X (confirm), `n` | `tui/keymap_test.go:TestKeymap_SelectionWrapsAcrossGroups`, `TestKeymap_EscQuitsFromGeneral`, `TestKeymap_CtrlXKillsRunningSession`, `TestKeymap_CtrlXDeletesCompletedSession`, `TestKeymap_SecondCtrlXResolvesConfirm`, `TestKeymap_ConfirmCancelledByN` |
| V-4 | Each row shows agent, shortened cwd, status, elapsed/last-activity, last-output summary | `tui/general_test.go:TestGeneral_RowShowsAllFields`, `TestGeneral_CwdShortenedToTilde`, `TestGoldenGeneralView` |
| V-5 | Needs-input / Ready-for-review transitions surface highlight + banner | `tui/banner_test.go:TestBanner_OnNeedsInputTransition`, `TestBanner_OnReadyForReviewTransition`, `TestBanner_NotShownOnInitialListing`, `TestBanner_NotShownOnWorkingTransition` |
| V-6 | Minimal aesthetic, no mouse, subtle color, no decoration without information | `tui/general_test.go:TestGoldenGeneralView`, `tui/launch_test.go:TestGoldenLaunchForm` — golden-file comparison against `ui-preview.html`. Weaker coverage: goldens prove pixel-stable rendering, not "minimalism" as a testable property; treated as satisfied by design review + golden lock-in, not a strict assertion. |

## Launch flow (L)

| ID | Requirement (short) | Coverage |
|---|---|---|
| L-1 | Form collects cwd (`~` expansion), agent, schema-driven options, initial prompt, worktree toggle | `tui/launch_test.go:TestLaunch_FormRendersFields`, `TestLaunch_TildeExpansionAndSubmit`, `TestLaunch_SubmitComposesLaunchReq` |
| L-2 | Agent picker offers only detected+version-compatible CLIs; others greyed with install hint | `tui/launch_test.go:TestLaunch_AgentPickerGreysUnavailable`, `adapter/claude/claude_test.go:TestDetect_VersionGreying_L2`, `adapter/codex/codex_test.go:TestDetect_VersionGreying_L2` |
| L-3 | Nonexistent/non-dir cwd → inline error, refused; daemon re-validates | `tui/launch_test.go:TestLaunch_InvalidCwdRefused`, `protocol/revalidation_test.go:TestRevalidate_LaunchRejectsNonexistentCwd`, `TestRevalidate_LaunchRejectsCwdThatIsNotADirectory` |

## Attach (A)

| ID | Requirement (short) | Coverage |
|---|---|---|
| A-1 | Attach = raw mode (IXON off), full passthrough, ANSI untouched | `attach/passthrough_test.go:TestPassthrough_KeystrokesForwardedToSession`, `attach/pty_test.go:TestPTY_IXONOffWhileRaw` |
| A-2 | Detach key defaults to Ctrl+\\, configurable | `attach/passthrough_test.go:TestPassthrough_DetachKeyDetachesAndIsNotForwarded`, `TestPassthrough_ConfigurableDetachKey` |
| A-3 | Resize propagates to PTY, following the attach lease | `attach/passthrough_test.go:TestPassthrough_ResizePropagatesCurrentSize` |
| A-4 | Attach delivers a serialized grid snapshot then the live stream — never raw history, never blank | `attach/passthrough_test.go:TestPassthrough_SnapshotPaintedBeforeLiveFrames`, `protocol/ordering_test.go:TestOrdering_ExactlyOneSnapshotPrecedesLiveFramesRaw`, `TestOrdering_SnapshotDeliveredThroughAttachmentAPI`, `shim/socket_test.go:TestContinuity_SnapshotThenStream_Boundary` |
| A-5 | Chrome at most one thin line, toggleable off | `attach/passthrough_test.go:TestPassthrough_ChromeToggle` |

## Adapters and status detection (T)

| ID | Requirement (short) | Coverage |
|---|---|---|
| T-1 | Adapter interface: detection+version, argv composition, options schema, signal descriptors, resume+conv-id | `adapter/adapter_test.go:TestAdapterInterfaceMethodSet`, `TestFrozenTypeShape`, `adapter/conformance_test.go` (suite) |
| T-2 | Typed signals preferred (hooks/app-server events), authenticated with a per-session token, per-invocation install | `adapter/claude/claude_test.go:TestSignalSources_DeclaresSixHooksWithStatusMapping`, `adapter/codex/codex_test.go:TestSignalSources_DeclaresTypedEventsWithStatusMapping`, `daemon/launch_inject_test.go:TestNewHookToken_RandomPerSession`, `TestInjectHookEnv_PostFilter`, `TestLaunch_InjectsHookEnvToAgent`, `engine/auth_test.go` (auth suite). **VERIFY-pending against real CLIs** (Epic 11 deferrals D1/D2 — real hook value names, real Codex app-server event names): `internal/smoke/realcli_test.go:TestRealCLISmoke`, gated (`//go:build realcli` + `SWARM_REALCLI=1`, never CI, billable) — see [epic-14-realcli-smoke.md](epic-14-realcli-smoke.md). The mapping mechanism itself is fully unit-tested above; only its fidelity against the live CLIs awaits a human-run confirmation. |
| T-3 | Grid heuristics as fallback, evaluated on output + low-frequency poll | `engine/heuristic_test.go:TestHeuristicConclusiveGrids`, `TestHeuristicInconclusiveMapsToUnknown`, `TestNoBusyPoll`, `TestTickReevaluatesStaleHeuristicActive`, `adapter/claude/claude_test.go:TestGridHeuristicFallback_ClassifiesIdlePrompt`, `adapter/codex/codex_test.go:TestGridHeuristicFallback_ClassifiesIdlePrompt` |
| T-4 | Inconclusive / no-output-no-CPU beyond threshold → `unknown` | `engine/staleness_test.go:TestStalenessGuardFlipsActiveToUnknown`, `TestStalenessGuardKeepsBusyActive`, `engine/cpuparse_test.go`, `engine/cpu_integration_test.go:TestSampleCPURealProcesses` |
| T-5 | New adapter requires no core/protocol/TUI changes | `adapter/claude/claude_test.go:TestImportBoundary_T5`, `adapter/codex/codex_test.go:TestImportBoundary_T5`, `adapter/refadapter/refadapter_test.go:TestReferenceAdapter_ImportBoundary` |
| T-6 | Characterization harness records real CLI behavior into a fixture corpus before adapter code | `cmd/swarm-char/char_test.go:TestCharacterize_ProducesSchemaValidFixture`, `adapter/fixture_test.go` (schema suite), `adapter/capability_test.go` (capability-matrix entry suite), `internal/smoke/realcli_test.go:TestRealCLISmoke` (re-records the Claude/Codex fixtures from a live capture on drift — VERIFY-pending a human run, see [epic-14-realcli-smoke.md](epic-14-realcli-smoke.md)) |
| T-7 | v1.0 ships Claude Code + Codex only; Gemini/OpenCode/AGY later | `adapter/registry/registry_test.go:TestNew_KnownAdapters` (asserts claude/codex/reference registered, `"gemini"` explicitly returns `(nil, false)`) |

## Persistence (R)

| ID | Requirement (short) | Coverage |
|---|---|---|
| R-1 | State under `$XDG_STATE_HOME/swarm` 0700; meta.json atomic + schema_version; transcripts 0600 capped/rotated; roster is a rebuildable index | `persist/persist_test.go:TestDefaultDirUsesXDGStateHome`, `TestDefaultDirFallsBackWithoutXDG`, `TestSaveLeavesNoTempFile`, `TestPermissionsUnderPermissiveUmask`, `TestRosterRebuildableByScan`, `transcript/transcript_test.go` (0600 + rotation suite), `persist/diskfull_test.go:TestSaveDiskFullNeverTears` (ENOSPC error-return path: fails clean, no torn/partial/leftover-temp file) |
| R-2 | Resume-as-new-session via `resumed_from`, offered when adapter supports resume + conv id captured | `e2e/resume_e2e_test.go:TestE2E_ResumeAsNewSession_R2`, `tui/resume_test.go:TestResume_KeyIssuesResumeLaunchOnEndedRow`, `TestResume_LaunchFailureSurfacesToBanner`, `TestResume_KeyIsNoOpOnRunningRow`, `skeleton/launch_compose_test.go:TestComposeLaunchSpec_ValidClaudeResume`, `TestComposeLaunchSpec_InvalidResumeRejected`, `TestComposeLaunchSpec_NoConversationIDRejected`, `persist/persist_test.go:TestResumedFromRoundTrips` |
| R-3 | Completed sessions remain listed until deleted; Ctrl+X on a completed row deletes; deletion tears down worktrees | `tui/keymap_test.go:TestKeymap_CtrlXDeletesCompletedSession`, `daemon/lifecycle_test.go:TestDelete_RunningKillsThenRemoves`, `persist/persist_test.go:TestDeleteRemovesSessionDir`, `internal/worktree/worktree_test.go:TestRemoveTearsDownWorktree`, `e2e/worktree_e2e_test.go:TestE2E_Worktree_LaunchRunTeardown` (worktree teardown proven through the assembled daemon, not just the unit) |

## Non-functional (N)

| ID | Requirement (short) | Coverage |
|---|---|---|
| N-1 | First paint <100ms p95 @<=50 sessions | `tui/firstpaint_test.go:TestFirstPaint_FiftySessionsUnderBudget` (TUI-side render-path budget against a stub, E7.6), and now the real gate: `tui/firstpaint_gate_test.go:TestFirstPaintGate_RealDaemon_FiftySessions_P95` — a real daemon assembly with 50 real launched sessions behind a real `protocol.Client`, p95 asserted fail-closed against the 100ms production budget in the non-race build (the authoritative N-1 gate; a generous sanity ceiling applies only under `-race`, documented in-file as measuring the race detector's own overhead, not the system). |
| N-2 | Attach passthrough <10ms added latency p95 | `attach/latency_test.go:TestPassthrough_KeystrokeEchoLatencyP95` (E8.5, measures the attach-layer overhead in isolation; the true end-to-end budget over a live shim is noted in-file as E14.4 scope) |
| N-3 | Event-driven, no busy polling; PTY always drained; spinner churn collapsed; idle CPU near-zero | `engine/heuristic_test.go:TestNoBusyPoll`, `engine/run_test.go:TestRunRespectsPollIntervalNoBusyPoll`, `transcript/transcript_test.go` (spinner-collapse suite), `shim/survival_test.go` (drain-under-load suite), `daemon/idlecpu_integration_test.go:TestDaemonIdleCPUNearZero` (build-tagged `integration`, runs in CI's integration lane: a real daemon + N real shims idle >=60s, summed CPU asserted fail-closed under a 5%-of-one-core threshold) |
| N-4 | Single static binary (CGO_ENABLED=0), 4 platforms, zero runtime deps | CI-enforced, not a Go test: `.github/workflows/ci.yml` "Assert linux binary is statically linked (E1.2)" + darwin otool step, release job "Assert linux artifacts are statically linked (N-4)" + "zero runtime deps via ldd (N-4)"; `cmd/swarm/role_test.go` (single-binary role dispatch) |
| N-5 | Repo follows the agentic-codebase-manifesto reference architecture | Doc-only: `docs/governance/` + `PROVENANCE.md` (Epic 0), CI "docs (required files)" job asserts their presence. Not unit-tested; satisfied by the governance artifact + CI file-presence check. |
| N-6 | Attach passes through alt screen/colors/cursor faithfully; OSC 52 and hostile sequences filtered | `attach/passthrough_test.go` (chrome/passthrough suite), `vt/emulator_test.go:TestHostileEscapesFiltered`, `vt/clamp_test.go:TestSnapshot_ClampsHostileTextAndTitle` |
| N-7 | Documented limitation: machine sleep pauses agents | Doc-only: `README.md` states the limitation and cites requirement N-7 directly. Not testable; satisfied by documentation (E0.5). |

## V2-forward constraints (F)

| ID | Requirement (short) | Coverage |
|---|---|---|
| F-1 | Protocol versioned + capability-negotiated from v1; endpoint id + namespaced session ids everywhere | `protocol/handshake_test.go:TestHandshake_MatchingVersionSucceeds`, `TestHandshake_EndpointIDsUnique`, `TestHandshake_CapabilityIntersection`, `protocol/namespacing_test.go:TestNamespacing_IDRoundTrip`, `TestNamespacing_ForeignEndpointRejected`, `TestNamespacing_ForeignSessionNamespaceRejected`, `TestNamespacing_EveryViewCarriesEndpointAndNamespacedID` |
| F-2 | No UDS-specific assumptions in message schemas | `protocol/namespacing_test.go:TestF2_NoTransportSpecificFieldsInMessages` (reflection check over every wire struct's field names/json tags, banning fd/socket/peer-cred style fields) |

## Summary

Of the ~59 EARS ids in the spec, every one maps to at least one confirmed-existing test, a CI-enforced check, or a documentation artifact — none rely on a bare "documented justification" (GG-3's prohibition). The three rows previously flagged in-flight (**V-2**'s 1s composite, **N-1**'s live-daemon first-paint, **N-3**'s idle-CPU threshold) are now closed by tests landed in the Epic 14 build wave: `e2e/l1composite_e2e_test.go`, `tui/firstpaint_gate_test.go`, and `daemon/idlecpu_integration_test.go` respectively.

The only remaining open items are the **D1/D2 real-CLI VERIFY items** under T-2 and T-6: `internal/smoke/realcli_test.go:TestRealCLISmoke` exists, compiles clean, and is correctly double-gated (`//go:build realcli` + `SWARM_REALCLI=1`) — but confirming it against the live, authenticated `claude`/`codex` CLIs is a billable action that must be run once by a human, never automatically or in CI. This is a deliberate, documented deferral (see [epic-14-realcli-smoke.md](epic-14-realcli-smoke.md)), not a missing test.
