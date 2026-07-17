# Epic 14 — Contracts Evidence Manifest

**Closes**: E14.6 — "for every contracts-under-test row, the manifest names the covering tests AND demonstrates they satisfy that row's declared LIGHT/MEDIUM/FULL scope; gaps fail the epic."
**Source**: [docs/specifications/build-plan.md](../specifications/build-plan.md), "Contracts under test" table (Epic 14 section).
**Method**: every test cited was confirmed present by `grep "^func <Name>("` against its named file. Scope demonstration quotes the row's own declared method (e.g. "round-trip + migration + torn-write") and shows a test matching each named method, not just a same-topic test.

---

### 1. `status` package types + derivation — E1 → E5, E6, E7, E10 — Data-only — LIGHT: schema/derivation table tests

`internal/status/status_test.go:TestDeriveCompleteness` is a table-driven test asserting the derivation function covers every (process, turn, interaction) combination against the spec's derivation table — the LIGHT bar (schema/derivation tests, no behavior). `TestDerive` and `TestStringConstants` round out the vocabulary. Consumers stay data-only: `protocol/groups_test.go` (E6, daemon computes the group from these types), `protocol/boundary_test.go:TestBoundary_ClientDoesNotReDeriveStatus` (E7 never re-derives), `engine/derivedims_test.go:TestDeriveDims` (E10 maps raw signals into these types). **Scope satisfied.**

### 2. meta.json schema (+schema_version) — E1 → E5 — Data-only — LIGHT: round-trip + migration + torn-write

All three named methods have a matching test: round-trip is `persist/persist_test.go:TestSaveLoadRoundTripAllFields`; migration is `persist/persist_test.go:TestMigrationFromOlderSchema` (+ `TestLoadRejectsFutureSchemaVersion` for the forward-compat half); torn-write is `persist/crash_test.go:TestCrashDuringSaveNeverTears` and `persist/persist_test.go:TestCrashDuringWriteLeavesOldIntact`, plus the complementary error-return path `persist/diskfull_test.go:TestSaveDiskFullNeverTears` (ENOSPC mid-commit fails clean, never torn, never a leftover temp file). Consumer E5: `daemon/sidefile_test.go:TestSidefile_MergeExitIntoMeta` and `TestSidefile_ShimNeverWritesMeta` confirm the daemon is the sole writer merging into this schema. **Scope satisfied — all three declared methods present, not just adjacent coverage.**

### 3. Fixture-corpus schema — E9 → E11 — Data-only — LIGHT: schema validation

`adapter/fixture_test.go:TestFixtureSchemaVersionConstant`, `TestFixtureValidate`, `TestFixtureValidate_EmptyHooksAllowed`, and `adapter/fixtureio/fixtureio_test.go:TestLoadFixture_RejectsFutureSchemaVersion`, `TestLoadFixture_RejectsGarbage`, `TestLoadFixture_RejectsZeroVersion` are pure schema-validation tests (no CLI behavior asserted). Consumer E11: `adapter/claude/claude_test.go` and `adapter/codex/codex_test.go` load real fixtures from `testdata/` through this same schema for their conformance/capability tests. **Scope satisfied.**

### 4. Snapshot byte format — E2 → E4, E6, E8 — Data-only — LIGHT: round-trip vs emulator state

`vt/emulator_test.go:TestSnapshot_Deterministic` and `TestSnapshotFidelity_Vim` serialize a snapshot and decode it back against the emulator's known state — the declared "round-trip vs emulator state" method exactly. `TestSnapshotVersioning` covers the format's version tag. Consumers: E4 `shim/socket_test.go:TestSocket_AttachDeliversSnapshot`, E6 `protocol/ordering_test.go`, E8 `attach/passthrough_test.go:TestPassthrough_SnapshotPaintedBeforeLiveFrames` all decode the same byte format produced here. **Scope satisfied.**

### 5. Emulator API (Feed/Snapshot/Resize) — E2 → E4, E9 — Behavioral — MEDIUM: golden grids + fuzz

Both declared methods present: golden grids in `vt/emulator_test.go:TestGoldenGrids` (includes a real alt-screen TUI capture per E2.5); fuzz in `vt/fuzz_test.go:FuzzFeedSplitConsistency`, wired into CI's dedicated `fuzz` job (`.github/workflows/ci.yml`, 30s fuzztime). Consumers: E4 `shim/socket_test.go:TestSocket_ResizeUpdatesEmulatorDims` exercises Resize through the shim; E9 `adapter/capability_test.go:TestCapability_FeedsRealGridNotNil` confirms the adapter capability path feeds a real, non-nil grid. **Scope satisfied.**

### 6. Transcript writer — E3 → E4 — Behavioral — MEDIUM: cap/rotate/collapse/disk-full

All four declared dimensions have a direct test in `internal/transcript/transcript_test.go`: cap/rotate — `TestRotatesAtExactlyMaxBytesBoundary`, `TestRotatesJustPastMaxBytesBoundary`, `TestRotationCapsTotalFilesAtMaxFiles`; collapse — `TestSpinnerCollapseCarriageReturnFrames`, `TestSpinnerCollapseCursorHomeFrames`; disk-full — `TestWriteNeverErrorsWhenSinkFailsEveryCall`, `TestDropsIncomingTailWhenBufferFull`. Consumer E4: `shim/exit_test.go:TestExit_SideFilesCompleteAndOrdered` confirms the shim's transcript is flushed/closed in the right order on exit. **Scope satisfied — literally the four named methods, each with its own test.**

### 7. daemon⇄shim wire (G2) — E4 → E5 — Behavioral — MEDIUM + old-shim × new-daemon compat matrix

The G2 message set (snapshot, stream, write, resize, signal, exit-report) is exercised in `shim/socket_test.go`: `TestSocket_HelloHandshake`, `TestSocket_AttachDeliversSnapshot`, `TestSocket_ResizeUpdatesEmulatorDims`, `TestSocket_ResizePropagatesToPTYWinsize`, `TestContinuity_SnapshotThenStream_Boundary`, `TestContinuity_ActiveLoadOrdering` — this satisfies the MEDIUM behavioral bar. The compat-matrix half, previously a gap, is now closed: `daemon/compatmatrix_test.go:TestCompatMatrix_OldShimNewDaemon` holds the daemon at the current wire version and varies the shim's wire version across `{old, matched, new}` via build-tagged test-only shim variants, asserting the matched pair interoperates and BOTH skew directions are detected and marked `lost` with zero signals sent (S3) — the single-adjacent-build smoke in `daemon/version_test.go:TestVersionSkew_SmokeReconnectRealShim` remains as the interop-only proof point. **Scope satisfied — both the MEDIUM behavioral bar and the compat-matrix extension are covered.**

### 8. client⇄daemon protocol — E6 → E7, E8 — Behavioral — MEDIUM: contract tests vs stub both sides

The protocol package's own suite (`protocol/ops_test.go`, `protocol/codec_test.go`, `protocol/revalidation_test.go`, `protocol/handshake_test.go`) runs every client operation against a stub daemon (`serveStub`/`dialClient` helpers, e.g. used in `protocol/fanout_test.go`), proving the daemon side of the contract without a real shim underneath. `internal/skeleton/serve_test.go` (`TestSkeleton_LaunchAppearsGroupedOverProtocol`, `TestSkeleton_AttachSnapshotAndInput`, `TestSkeleton_DetachLeavesSessionAndReattachSucceeds`) exercises the same contract with a real client dialing a real (in-process) server, closing the loop the row calls for. Consumers: E7 `tui/stub_test.go:TestStub_NewAcceptsFakeClientNoDaemon` and the whole `tui` suite run against a fake `protocol.Client`, never a live daemon (E7.7); E8's attach tests build on the same skeleton-level contract. **Scope satisfied.**

### 9. Adapter interface conformance — E9 → E11 — Behavioral — MEDIUM: conformance suite, two real adapters

`internal/adapter/conformance_test.go` is the generic E9 conformance suite (`TestConformance_AcceptsConformantAdapter`, `TestConformance_RejectsViolations`, plus goroutine/fd-leak and parallel-safety checks). Both v1.0 adapters pass it: `adapter/claude/claude_test.go:TestConformance` and `adapter/codex/codex_test.go:TestConformance`. This is exactly the declared method — "conformance suite, two real adapters" — not a proxy for it. **Scope satisfied.**

### 10. Hook callback (token + sequence) — E10 → E11 — Behavioral — MEDIUM: auth/replay negative tests

`internal/engine/auth_test.go` provides the auth negative tests (tokenless, foreign-token, replayed-sequence, post-session-end, unregistered-session, all asserted no-op) and `internal/engine/antireplay_test.go` provides the sequence-ordering negative tests (`TestExactReplayRejected`, `TestStaleSameDimensionRejectedNoRegression`, `TestFarBelowHighWaterRejected`). Consumer E11: `internal/e2e/adapter_wiring_e2e_test.go:TestE2E_AdapterWiring_ClaudeLaunchAndHook` and `internal/e2e/engine_wiring_e2e_test.go:TestE2E_EngineWiring_HookChangesStatus` exercise the real Claude adapter posting a real token through this same auth path. **Scope satisfied.**

### 11. Attach flow (lease/snapshot/raw/resize) — E4+E6+E7+E8 — Composition — FULL: end-to-end under load + failure injection

End-to-end under load: `internal/skeleton/serve_test.go:TestSkeleton_SnapshotContinuityUnderLoad` and `TestSkeleton_TwoClientSupersede` (lease), `TestSkeleton_ResizeUnderLease` (resize under lease), `attach/passthrough_test.go:TestPassthrough_SnapshotPaintedBeforeLiveFrames` (raw mode + snapshot ordering) compose across E4/E6/E7/E8. Failure injection: `internal/e2e/skeleton_e2e_test.go:TestE2E_DaemonKilledMidAttach` (daemon killed mid-idle-attach), `internal/e2e/killmidwrite_e2e_test.go:TestE2E_DaemonKilledMidInputWrite` (the harsher case — daemon killed while a client is actively streaming PTY input, input relay severed mid-frame, reconnects to the same session) plus `attach/passthrough_test.go:TestPassthrough_PanicRestoresTerminalAndReturnsError` and `TestPassthrough_SignalRestoresTerminal` (terminal-restoration failure injection). **Scope satisfied — both halves of FULL (end-to-end-under-load, failure-injection) have a named test.**

### 12. Session lifecycle (launch→kill/delete, crash windows) — E4+E5 — Composition — FULL: S11 bijection under crash injection

`daemon/launch_test.go:TestLaunch_CrashBeforeSpawn_NoPhantom` and `TestLaunch_CrashBeforeConfirm_NoOrphan` inject a crash at each of the two-phase launch's boundary points and assert the S11 bijection (no orphan shim, no phantom session) directly — this is precisely the declared method, not an adjacent test. `daemon/lifecycle_test.go:TestKill_TerminatesAndRecordsOutcome` and `TestDelete_RunningKillsThenRemoves` cover the launch→kill→delete lifecycle end to end. **Scope satisfied.**

### 13. Daemon restart/upgrade survival — E4+E5+E6 — Composition — FULL: scenarios 10/11 soak

`daemon/realkill_test.go:TestSurvival_RealKillNineReconnectsAll` proves scenario 10 with a real subprocess and a real `SIGKILL` (single cycle); `daemon/version_test.go:TestVersionSkew_RestartIsSafe` proves scenario 11's safety property (also single-cycle). The declared soak scope, previously a gap, is now closed: `daemon/soak_test.go:TestSoak_RestartUpgrade_50CyclesZeroLoss` runs the daemon through 50 consecutive kill(-9-equivalent `abandon()`)/restart cycles over real detached `swarm shim` subprocesses, re-opening the daemon over the same state dir each cycle and asserting every session reconnects as the same shim with a byte-intact transcript, failing on the first lost session and naming the cycle. **Scope satisfied.**

### 14. Status pipeline (signal→engine→fan-out→TUI) — E9+E10+E6+E7 — Composition — FULL: ≤1 s latency, staleness, precedence

Staleness and precedence, the two qualitative sub-claims, are each fully proven: `engine/staleness_test.go:TestStalenessGuardFlipsActiveToUnknown` / `TestStalenessGuardKeepsBusyActive`, `engine/precedence_test.go:TestTypedSignalBeatsHeuristic` / `TestHeuristicNeverOverridesFresherTypedSignal`. The ≤1s latency claim, previously proven only at each individual hop, is now also proven chained: `internal/e2e/l1composite_e2e_test.go:TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad` drives a real authenticated hook callback through a real daemon's engine, over a real `protocol.Client` fan-out, into a real bubbletea TUI rendered via `teatest`, under continuous PTY output load, asserting the new status appears in the rendered view within 1s (fail-closed). The per-hop tests remain as the mechanism-level evidence: `engine/fanout_test.go` (engine emits synchronously, no deferral), `protocol/fanout_test.go:TestFanout_StatusChangeReachesLiveSubscriberWithin1s` (protocol delivers within 1s), `tui/liveness_test.go:TestLiveness_EventMovesRowGroup` (TUI reflects the event), and real-adapter wiring in `internal/e2e/engine_wiring_e2e_test.go` (`TestE2E_EngineWiring_HookChangesStatus`, `TestE2E_EngineWiring_StatusPersisted`, `TestE2E_EngineWiring_OutputTapDrivesHeuristic`). **Scope satisfied.**

---

## Summary

All 14 rows are now fully satisfied against their declared scope, with the covering tests demonstrated to match each row's own named method (not just topically adjacent tests). The three gaps flagged in the prior pass are closed by tests landed in the Epic 14 build wave:

- **Row 7** (daemon⇄shim wire compat matrix): `daemon/compatmatrix_test.go:TestCompatMatrix_OldShimNewDaemon`.
- **Row 13** (daemon restart/upgrade soak): `daemon/soak_test.go:TestSoak_RestartUpgrade_50CyclesZeroLoss` (50 consecutive cycles, zero loss).
- **Row 14** (status pipeline composite): `internal/e2e/l1composite_e2e_test.go:TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad`.

The same closures resolve the matching gaps previously flagged in [epic-14-invariants.md](epic-14-invariants.md) (L1) and [epic-14-ears-matrix.md](epic-14-ears-matrix.md) (V-2, N-1, N-3). The only work still genuinely open across all three verification artifacts is the D1/D2 real-CLI VERIFY smoke (`internal/smoke/realcli_test.go:TestRealCLISmoke`) — built, gated, and compile-verified, but requiring one human-run against the live, authenticated CLIs before those two Epic 11 deferrals can be marked confirmed rather than fixture-proven.
