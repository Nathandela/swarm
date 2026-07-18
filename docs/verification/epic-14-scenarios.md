# Epic 14 — Scenario Traceability Matrix

**Closes**: E14.1 / GG-1 — "all 18 spec scenarios automated (no required test skipped/flag-disabled)."
**Source**: [docs/specifications/system-spec.md](../specifications/system-spec.md), Scenario table.
**Method**: every test cited below was confirmed present by `grep "^func <Name>("` against its named file — no test is listed from memory or inference. Paths are relative to `internal/`.

| # | Scenario (Given/When/Then, short) | Test(s) — file:function |
|---|---|---|
| 1 | No daemon running; user runs `swarm`; daemon auto-starts, view < 100 ms | autostart: `daemon/autostart_test.go:TestAutoStart_ColdStateDirSpawnsAndConnects`; the `<100 ms` first-paint clause (N-1): `tui/firstpaint_gate_test.go:TestFirstPaintGate_RealDaemon_FiftySessions_P95` (real daemon + 50 sessions, p95 <100 ms fail-closed, non-race CI lane) |
| 2 | Launch form, valid dir; pick claude + model, Enter; shim spawns agent, appears under Working | `skeleton/serve_test.go:TestSkeleton_LaunchAppearsGroupedOverProtocol`, `e2e/skeleton_e2e_test.go:TestE2E_WalkingSkeleton_GG1` |
| 3 | Session working; terminal closed; agent keeps running, reopening swarm shows it | `e2e/skeleton_e2e_test.go:TestE2E_Scenario3_SurvivesClientClose` |
| 4 | Claude session running; Claude requests permission; `needs_input` ≤ 1s via authenticated hook, row highlighted + banner | `e2e/adapter_wiring_e2e_test.go:TestE2E_AdapterWiring_ClaudeLaunchAndHook`, `e2e/l1composite_e2e_test.go:TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad`, `tui/banner_test.go:TestBanner_OnNeedsInputTransition` |
| 5 | Codex session running; turn lifecycle event fires; status derived from typed event, not parsing | `adapter/codex/codex_test.go:TestSignalSources_DeclaresTypedEventsWithStatusMapping`, `engine/derivedims_test.go:TestDeriveDims` (adapter + engine unit coverage; live confirmation against the real Codex app-server stream is the gated `internal/smoke/realcli_test.go` VERIFY item, see [epic-14-realcli-smoke.md](epic-14-realcli-smoke.md)) |
| 6 | Any session, no typed signal; CLI idle at prompt; grid heuristic flags idle, staleness guard prevents false working | `engine/heuristic_test.go:TestHeuristicInconclusiveMapsToUnknown`, `engine/staleness_test.go:TestStalenessGuardFlipsActiveToUnknown`, `e2e/engine_wiring_e2e_test.go:TestE2E_EngineWiring_OutputTapDrivesHeuristic` |
| 7 | Session selected; Enter; grid snapshot painted instantly, typing reaches agent | `skeleton/serve_test.go:TestSkeleton_AttachSnapshotAndInput` |
| 8 | Attached; Ctrl+\; back to general view, session continues | `skeleton/serve_test.go:TestSkeleton_DetachLeavesSessionAndReattachSucceeds` |
| 9 | Attached; terminal resized; agent reflows (lease holds resize authority) | `skeleton/serve_test.go:TestSkeleton_ResizeUnderLease` |
| 10 | Sessions running; daemon killed -9, restarted; shims kept agents alive, daemon reconnects, nothing lost | `daemon/realkill_test.go:TestSurvival_RealKillNineReconnectsAll`, `daemon/soak_test.go:TestSoak_RestartUpgrade_50CyclesZeroLoss` |
| 11 | Sessions running; `brew upgrade swarm` + daemon restart; same as scenario 10 — upgrade is safe and says so | `daemon/realkill_test.go:TestRestart_RealDaemonHandsOff`, `daemon/version_test.go:TestVersionSkew_RestartIsSafe`, `daemon/soak_test.go:TestSoak_RestartUpgrade_50CyclesZeroLoss` |
| 12 | Machine rebooted; swarm reopened; sessions marked lost, transcripts intact, resume offered where supported | `e2e/resume_e2e_test.go:TestE2E_ResumeAsNewSession_R2`, `daemon/reconcile_test.go:TestReconcile_LostOnReapedPID` |
| 13 | Launch form; nonexistent dir; inline error, daemon re-validates too | `tui/launch_test.go:TestLaunch_InvalidCwdRefused`, `protocol/revalidation_test.go:TestRevalidate_LaunchRejectsNonexistentCwd` |
| 14 | gemini not installed; launch form opened; greyed with install hint | `tui/launch_test.go:TestLaunch_AgentPickerGreysUnavailable` |
| 15 | Spinner runs overnight; transcript capped/rotated, near-zero client-idle CPU | `transcript/transcript_test.go:TestRotatesAtExactlyMaxBytesBoundary`, `transcript/transcript_test.go:TestSpinnerCollapseCarriageReturnFrames`, `daemon/idlecpu_integration_test.go:TestDaemonIdleCPUNearZero` |
| 16 | Second client attaches; lease refused/transferred explicitly, stale input rejected | `skeleton/serve_test.go:TestSkeleton_TwoClientSupersede`, `protocol/lease_test.go:TestLease_SecondAttachSupersedesWithHigherGeneration` |
| 17 | Agent spawns MCP servers; kill session; whole process group terminated, nothing leaks | `shim/signal_test.go:TestSignal_TermGraceKill_WholeGroup`, `shim/shim_containment_test.go:TestShimSelfContainsAgentGroupOnSIGTERM` |
| 18 | Launch from terminal with venv + API key; agent runs; agent sees the launching terminal's environment | `shim/spawn_test.go:TestSpawn_EnvIsCapturedNotInherited`, `daemon/launch_test.go:TestLaunch_FiltersClientEnv` |

## Notes

- All 18 scenarios have at least one confirmed-present, real test covering them; no scenario relies on a `-short`-skipped or flag-disabled test.
- Scenario 5's live confirmation against a real Codex app-server stream is the same deliberately-gated, never-CI, human-run harness documented for the D1/D2 real-CLI VERIFY items (`internal/smoke/realcli_test.go`, [epic-14-realcli-smoke.md](epic-14-realcli-smoke.md)); adapter- and engine-level coverage here is automated and green.
- This table is the artifact referenced by E14.1 in [epic-14-evidence.md](epic-14-evidence.md); invariant (S1-S12/L1-L3) traceability is a separate concern covered by [epic-14-invariants.md](epic-14-invariants.md).
