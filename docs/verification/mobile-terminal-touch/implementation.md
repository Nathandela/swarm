# Mobile terminal interaction implementation verification

Date: 2026-10-25. Scope: Swarm's terminal dashboard over SSH, under [ADR-035](../../adr/ADR-035-mobile-terminal-navigation.md) and the [accepted plan](../../specifications/mobile-terminal-touch-plan.md). Implementation and local qualification are complete. Actual Termux and iOS-client qualification remains open in `agents-tracker-nzo8.5`; the parent feature `agents-tracker-nzo8` remains open for that gate. No release, installation or hosted/native-phone deployment was performed.

Subsequent owner-authorized [v0.23.0 publication and two-host installation](../mobile-terminal-v0230-release.md) are recorded separately. The no-release statement above describes this implementation qualification task.

## Result

The board now has a bounded viewport independent of selection, useful compact rows, semantic collapsible groups, pinned input/review counts with explicit attention reveal, and New/Open-or-Show-or-Expand/More controls. More owns a scrollable menu and a captured session identity. Pointer actions require matching release with stable target and geometry. Existing keyboard commands remain reachable; offscreen session actions reveal their target and consume the first action.

Forms use a bounded height-aware viewport, preserve drafts/cursor/errors through resize, retain selected providers and option-key focus through schema changes, and expose full target/decision context before confirmation. The board/menu minimum is 24×4 cells; text/decision screens require 32×8. Smaller sizes retain drafts and cancellation but block initiating actions. Free-text forms release mouse capture for native editing/selection. Raw attached-agent input ownership remains unchanged.

Rendering no longer forces an unchanged full redraw each second. Existing elapsed reevaluation and small Working animation updates remain; hidden indicators stop scheduling during form overlays. Wrapped resume results carry the reconnect generation, so stale results cannot replace the current screen while accepted current results still autoattach.

## Source and independent review

The broad qualification revision is `b148471209855d97656a74809bf2d8d66192fb13`, based on main `2ab161353751f40e7b38bd76b7697bc4792f10bb`. Supplemental test-only checkpoint `b1545f681349571a3c47d2b1f96009ab1269275b` adds the geometry sweep described below. This record is a subsequent documentation-only change. Fresh whole-feature reviewers Sol (`gpt-6.1-sol`) and Astra (`gpt-6-astra`) independently approved production code at pre-rebase `316c98cc`; no production Go delta exists between that approved snapshot and the final revision. Subsequent changes contain upstream documentation and reviewed test-only corrections.

Sol implemented the board and forms in separate worktrees; Luna handled redraw and lint cleanup; Astra challenged each slice. Root integrated, inspected six compact/menu/form frames, and added failing regressions. Fresh Sol and Astra then audited the complete implementation independently. The material findings were fixed and rechecked:

| Finding | Resolution and retained check |
|---|---|
| A fake attach placeholder and weak spinner assertion could miss repaint damage | Exercise actual Bubble Tea ReleaseTerminal/RestoreTerminal with external terminal damage; a ClearScreen mutation must fail the spinner-diff test |
| Modified pairing keys could approve; disconnected cancellation could be trapped | Shared strict approval predicate and unconditional safe cancellation/deny paths |
| Narrow confirmation lost provider identity or essential submit/cancel hints | Inspectable Agent/ID/Directory context, pinned action/target, essential narrow hints, exact frame goldens |
| More could rebind a target or change Kill to Delete after a process update | Captured ID and process eligibility, explicit invalidation, no underlying-board fall-through |
| Fold/move/removal/reconcile could lose semantic focus or old reading position | Resolve surviving old content/navigation identities before mutation; one restoration owner per event path |
| Wrapped resume completion lacked reconnect generation | Stamp/filter the wrapper and preserve accepted current autoattach |
| Working ticks survived hidden form overlays | Visibility-based scheduling, covered by pairing/inline-form regression |

Both final reviewers reported no remaining production-code blocker. Their approvals cover source and local evidence, not handset compatibility. The separate test-fixture corrections below were independently reviewed by Astra as well.

## Failing-first evidence and traceability

The retained logs record RED, the smallest correction, GREEN, and limits: [board](board-tdd.txt), [forms](forms-tdd.txt), [renderer](render-tdd.txt), [integration/visual review](integration-tdd.txt), [lint](lint-tdd.txt), [deadline fixture](deadline-fixture-tdd.txt), and [CLI-refresh fixture](cli-refresh-fixture-tdd.txt), and [supplemental geometry](geometry-tdd.txt). The [planning probes](README.md) are historical defect reproductions, not feature acceptance tests.

| Contract | Representative acceptance coverage in `internal/tui` |
|---|---|
| Independent scroll; reveal before every contextual action; sequential wheel clamping | `TestMobileBoardWheelKeepsFocusAndRevealFirst`, `TestMobileBoardWheelOrderChromeAndPaddedRows` |
| Useful identity and bounded geometry | `TestMobileBoardCompactIdentityAndBounds`, `TestMobileBoardWidthBoundariesAndUnicode`, existing responsive-board width tests and Unicode form/cursor tests, `TestMobileFrames` |
| Folding, header/session focus and explicit attention | `TestMobileBoardFoldIsSemanticAndAttentionReveals`, `TestMobileBoardCollapseModesAndNearbyFocus`, `TestMobileBoardFocusedHeaderSurvivesRefreshPaths` |
| Stable release targeting and cancellation | `TestMobileBoardResizeAndDragCancelClick`, `TestMobileBoardMenuReleaseAndNativeOwnership`, existing `TestMouse*` routing tests |
| Captured More actions and invalidation | `TestMobileBoardMoreUsesCapturedTargetAndInvalidatesKill`, `TestMobileBoardMoreQuitRemainsLocalWhenDisconnected` |
| Status/tag moves, removal and authoritative replacement | `TestMobileBoardVanishedHeaderKeepsOldOrderNeighbor`, `TestMobileBoardEventRemovalHasSingleAnchorOwner`, `TestMobileReviewRemovedRosterFocusUsesOldOrder` |
| Required notices, attention and version at small sizes | `TestMobileBoardAttentionBannerVersionTogether`, `TestMobileBoardOversizedErrorIsNonactivating`, `TestMobileBoardRequiredBannerTailReturnsOnResize` |
| Readable forms, selected provider/options, cursor/draft preservation | `TestResponsiveFormsFocusAndError`, `TestResponsiveFormsSelectedProvider`, `TestResponsiveFormsSchemaKeepsOptionFocus`, `TestResponsiveFormsAllProviderLastOptions`, `TestResponsiveFormsGeometryAndRetainedDrafts` |
| Mutation/approval guards, long decisions and cancellation | `TestResponsiveFormsEveryInitiatingKeyIsSizeGuarded`, `TestResponsiveFormsLongPairingRequiresVisibleStop`, `TestResponsiveFormsLongConfirmCannotRearmAfterCancel`, `TestResponsiveFormsPairingRejectsModifiedApprovalKeys`, `TestResponsiveFormsDisconnectPreservesDeny` |
| Resume generation and accepted completion | `TestResumeGenerationCurrentSuccessAutoattaches`, `TestResumeGenerationStaleResultCannotReplaceCurrentScreen`, `TestResponsiveFormsBusyResumeEscapeAndAcceptedResult` |
| Real renderer diffs and terminal handover | `TestRepaint_UnchangedFrameWritesNoRendererBytes`, `TestRepaint_ElapsedBucketChangeReachesRenderer`, `TestWorkingAnimationWritesOnlyChangedGlyph`, `TestBannerExpiryChangesReachRenderer`, `TestResizeReachesRenderer`, `TestTerminalRestoreRedrawsUnchangedBoard` |
| No update-allocation regression and no hidden work ticks | `TestApply_NoFlatAllocOnSameGroupUpdate`, `TestMobileReviewHiddenWorkingAnimationStops` |

Six inspected plain frames are retained in [TestMobileFrames](../../../internal/tui/testdata/TestMobileFrames): 24×4/40×8 board, folded board, 24×4 More, 40×8 prompt and 32×8 confirmation. Existing ANSI style pins remain exact; five changes remove only the old nonce, while confirmation additionally gains the required target context. The liveness assertion checks the actual changed row/status instead of assuming a forced repaint.

## Final local gates

Machine: macOS `darwin/amd64`, Go 1.26.5; golangci-lint 2.12.2. Gates run serially with `GOMAXPROCS=2`, package parallelism 2 (1 for the final race suite), shared Go caches, inherited `SWARM_*` removed, and a short canonical task TMPDIR under `/private/tmp`. Existing per-package supervisors retain fixture ownership and cleanup. Exact commands, source IDs, durations, result counts and raw-log hashes are in [qualification.json](qualification.json).

| Gate | Result | Seconds |
|---|---|---:|
| Go build | PASS | 9.788 |
| Go vet | PASS | 2.705 |
| golangci-lint (0 issues) | PASS | 5.122 |
| Complete normal Go suite (`49f1bc82`) | PASS | 1024.143 |
| Complete Go race suite | PASS | 2017.576 |
| 50-session real-daemon first-paint gate | PASS | 16.945 |
| 1,000-sample real-shim echo gate | PASS | 7.832 |
| Linux amd64 cross-build | PASS | 60.264 |

The normal full suite ran on `49f1bc82` and passed all 78 packages containing tests. The later Go edits are independently reviewed test-fixture readiness corrections, verified by focused normal/race checks and the complete final race run. Production Go code is unchanged. Platform/service/helper-dependent skips are recorded in the manifest; a zero exit is not a claim that those external gates ran.

Local non-race performance output:

```text
firstpaint_gate_test.go:205: first-paint p95 over 25 runs @ 50 real sessions: 4.549875ms (raceEnabled=false)
latency_gate_test.go:174: live-shim keystroke->PTY echo p95 over 1000 samples = 231.667µs (budget 10ms, raceEnabled=false)
```

These gates use real local daemon/shim/PTY fixtures. They exclude phone rendering and SSH round-trip latency; the first-paint metric covers eager roster List plus model initialization, size Update and first View; it does not measure terminal display.

The unchanged-frame regression measures **0 renderer bytes**, versus **406** before removing the forced clear/nonce. The separate earlier planning probe measured 316 bytes with its different fixture. These are captured local renderer bytes, not compressed SSH traffic or a battery measurement. Existing same-group apply remains zero-allocation. The board worker's local render benchmark was approximately 0.07–0.30ms for 50 sessions and 0.25–2.02ms for 500 sessions; no cache/index was added.

## Supplemental boundary coverage

Final evidence review found that sampled board widths did not fulfill the explicit boundary-width coverage contract. Test-only checkpoint `b1545f681349571a3c47d2b1f96009ab1269275b` adds `TestMobileBoardWidthBoundariesAndUnicode`: every width 1–200 at heights 3/4/5, 7/8/9 and 11/12/13, across status/repo/tag grouping, for **5,400 geometries**. Fixtures include long CJK, combining-character and emoji names/tags/paths. Raw rows must fit before outer frame clipping; the selected visible row must retain its name, independently of a matching Unicode header.

The focused normal run passed in 12.076s; race passed in 21.440s. External overlays introducing a one-cell row overflow or replacing session names while preserving Unicode headers both failed the named test. Astra reviewed the assertions and independently passed the normal test in 3.895s. Full vet and lint were rechecked successfully after this test addition. [Geometry evidence](geometry-tdd.txt) records the commands and controls; the manifest records source and log hashes. Production Go code did not change, so the broad gates and performance measurements above retain their original snapshot rather than being relabeled as reruns.

## Failed attempts and fixture corrections

Failures were retained and diagnosed before accepting the final gates:

- The host's default `/tmp` symlink failed strict native-path fixtures; unchanged CLI tests passed with canonical `/private/tmp`. An overly long canonical temporary root then exceeded Unix-socket path limits. The final runner uses a short canonical task root; production path validation was unchanged.
- Two legacy TUI expectations contradicted the accepted interaction contract. The removed-option test now actually removes the focused option key and proves fallback directory typing; retained keys stay covered separately. Confirmation checks explicit submit/cancel keys plus the right-aligned version. The reconnect banner test reconstructs the rendered terminal with the existing VT emulator, because valid incremental output can reuse a prefix and never contain the full string in one raw write.
- The login-worker deadline test could expire during fixture preparation before reaching authentication. The test now arms its real configured deadline after preparation, deliberately delays detached startup, proves authentication and a live escaped child, then requires deadline failure and both writers dead. A WithDeadline→WithCancel mutation fails. The ordinary progress poll also uses the admitted job deadline plus the existing cleanup allowance: its old 8-second wait could expire during valid startup or cleanup within the fixture’s 20-second job. Delaying startup beyond 8 seconds proves the distinction. Conflict and runner-crash tests now require their exact non-deadline failure causes; causal mutants that postpone handling until the deadline fail. Writer-containment predicates and the real deadline test remain intact. No production worker behavior changed; see [deadline evidence](deadline-fixture-tdd.txt), `agents-tracker-irkm`.
- The first race run at `49f1bc82` failed `TestCLIRefreshInstalledUpgradeRealResume`: PTY “ready” could precede persisted post-spawn identity, so replacing the executable too soon made production correctly withhold that observation. A 3.1s delayed-observation overlay reproduced the exact failure; the same overlay passed after waiting for the complete old identity before replacement. Normal and race focused checks passed, followed by Astra's independent normal run. No production shim/watcher behavior changed; see [CLI-refresh evidence](cli-refresh-fixture-tdd.txt), `agents-tracker-iz7h`. The failed race run was interrupted through its package supervisor to retain fixture cleanup and is not counted as a pass.

## Qualification boundary

`agents-tracker-nzo8.5` requires both actual SSH clients: record terminal/OS/version/font, keyboard-open/closed cell dimensions, touch press/release and swipe/wheel traces, native selection/paste, Ctrl+q access, repeated resize/attach/detach, and tmux only if actually used. No device was attached to the available Android bridge; no iOS device connector was available, and the iOS app is still unidentified. No physical trial is claimed.

Terminals own native touch translation, pinch and long-press selection. Swarm cannot synthesize absent wheel reports. Mouse reports contain no displayed-frame identity: a stale pre-press coordinate arriving after a model change remains ambiguous. Named ID-bound decision context and confirmations mitigate consequential actions; geometry generations do not eliminate that transport limit. Text workflows still require a keyboard. Native Android, Mosh, hosted relay/Firestore/workerd, mobile artifact builds, installed live providers, and native Linux runtime qualification are outside this SSH-dashboard change's claim.

## Attached `[swarm]` return button — 2026-10-27

Source integration is committed as `185efa042f612e67bb5b6356c8a2db9164cc9be6`, then [published and installed in v0.25.0](../mobile-terminal-v0250-release.md) after all 17 exact-tag jobs succeeded. The [detailed integration contract](../../specifications/mobile-terminal-touch-plan.md#attached-swarm-button-integration-amendment--2026-10-27) and [ADR-035 amendment](../../adr/ADR-035-mobile-terminal-navigation.md#attached-swarm-button-amendment--2026-10-27) describe the supported envelope. [The qualification manifest](attached-button.json) pins changed source/fixture hashes, commands, raw-log hashes, causal controls and remaining gates. Actual handset qualification and busy supervisor convergence remain separate open gates.

**Options → CLI button** is saved locally and defaults off. Supported sessions show `[swarm]` before their name in the existing footer, with no extra row. Matching unmodified left press/release invokes the existing detach path while the session continues; Ctrl+Q remains. Tests cover native startup ordering, temporary legacy fallback, pixel/unknown/ambiguous modes, mode query/save ordering, 7/8-bit reports, split UTF-8/control sequences, paste, drag/key/focus/wheel cancellation, native gesture ownership, blocked paints and resize, write failures, tiny geometry, pending prefixes and restoration. Old snapshots without authoritative mouse state remain unarmed. Actual old agents are not restarted to create that metadata.

The real-client smoke starts a real private daemon, shim and PTYs, answers one agent prompt, returns to the board, reattaches and answers the next prompt in the same running session. Both keyboard and touch paths passed. An isolated overlay disconnecting the production preference wire failed on the missing button, establishing that this check cannot pass by observing source-model state alone. The 32×8 selection disclosure also received a failing-first rendered-screen regression. All negative controls use external overlays, not mutations of the shared working tree.

Build, vet and lint passed after the final corrections. The full normal command passed **9,781 tests/subtests in 82 packages**, with 65 skips. The first full race command is retained as **failed**: it had no data-race reports, but two test fixtures failed. Their repairs change no application code:

- The supervisor's 500 ms watchdog included copied-executable startup and expired before readiness/report publication. A 750 ms delayed-start control reproduced it; ordinary scenarios now have a 30 s runaway guard, while the deliberate timeout uses Go's actual 5 s watchdog and checks its failure cause. Existing kernel-identity, cleanup, retained-scratch and exact-exit assertions remain. A wrong-panic control still fails. Complete package normal/race passed in 12.105/21.605 s.
- The reconnect banner fixture used a VT with default linefeed semantics against a non-TTY renderer that assumes LF→CRLF translation. Replaying the exact failed full paint plus partial suffix reproduced the missing prefix. The test VT now models that translation, retains the three-second waits and exact banner expectation, and additionally verifies reconstructed-screen disappearance before the original final-model check. A missing-banner transcript control fails. Complete TUI normal/race passed in 39.084/57.690 s.

Astra independently approved both repairs and the connected implementation; Sol independently approved the attach/TUI integration. Exact-tag hosted full race and the existing CI/container gates remain mandatory before publication. CI's main test lane is a full race run; no separate hosted full-normal rerun is claimed. Passing affected-package reruns do not relabel the initial failed command.

Standalone Go 1.26.5 test binaries also ran on native Linux amd64: full VT and attach suites, plus the focused preference tests, all passed. They used private scratch and required no VM Go upgrade or live Swarm action. Mac added-input echo p95 was 15.25 µs with touch enabled over 1,000 samples (default 15.459 µs); Linux measured 16.722 µs (default 13.115 µs), against the existing 10 ms local budget. The existing live-shim gate measured 265.708 µs; first paint at 50 real sessions measured 3.663542 ms p95. These are local harness measurements, not phone or SSH latency. Bounded VT split-consistency fuzzing passed 14,850 executions.

`agents-tracker-ppbw.4` retains actual Termux/iOS qualification. The terminal-wide selection/scrolling tradeoff is disclosed even at the minimum form size. Delayed reports have no mode/frame epoch, and ambiguous early Escape/`ESC [` input retains the preexisting 50 ms timeout. Native/tmux combinations, actual touch translation and thumb accuracy remain unqualified. Source integration and generic optional-terminal publication do not close that physical gate.
