# RED evidence: Wave R7 -- Codex as a token-live native backend (Mirror M4.1-M4.5)

- **Date:** 2026-08-20 (UTC; per-command timestamps in `r7-red.txt`)
- **Role:** RED (contract-test writer). No production source was written.
- **Bead:** `agents-tracker-hggx.8`
- **Design:** ADR-013 Amendment 2026-08-20 §R7.1-§R7.10 (revision 2), ADR-010 Amendment
  2026-08-20 (revision 2)
- **Raw transcript:** `r7-red.txt` beside this file

## Baseline honesty

`go build ./...` exits **0** at capture time. Every file this slice adds is a `_test.go` file, a
fixture, or this evidence; `git status` shows the only modified tracked files are the two ADRs
the design agent wrote, and no `.go` file under `internal/` is modified. The one new directory,
`internal/appserver/`, contains **only test files** -- which is itself the RED for that package.

## What a reader CAN and CANNOT conclude from this

**CAN:** every R7 row now has failing contract tests that fail because the implementation is
absent, and the failures name the missing symbol or the missing behavior. The fixture corpus
those tests drive was extended from the CLI's own generated bindings, offline, and its
provenance is written down per file.

**CANNOT:** nothing here shows a working Codex backend. No test in this slice has ever talked to
a real `codex app-server`; the two that would are behind `//go:build realcli` and have not been
run. The two measurements ADR-013 makes obligations (rollout-to-resume, and whether
`thread/read` backfills losslessly) are **not taken** -- they are written as realcli tests and
are listed as open in `r1-codex-fixtures/r7-open-questions.md`. No Android work is in this
slice.

## The two shapes of RED, and why both were captured

**Compile-fail RED** is the default for a slice that introduces new seams, and it is this
package's own precedent (`r6_*_test.go`). `go vet` per package names the first undefined symbol;
they are in `r7-red.txt`.

**Runtime RED** is stronger, because a compile failure cannot show that a test would still fail
for the right reason once the code exists. For `internal/adapter/codex` it was captured by
temporarily adding a stub file declaring the intended contract types (`BackendSpec`,
`BackendPlan`, `BackendSource`, `AsBackendSource`, `KeystrokeComposer`, `AsKeystrokeComposer`,
`CheckBackendPlan`, `ResolveBackend`) plus the `Interaction.ClientRef` field, running the suite,
and **deleting the stub immediately after**. The transcript is in `r7-red.txt` under "STAGED
RUNTIME RED". Twenty tests fail naming the absent `BackendSource` / `InteractionSource` /
declared-row; four PASS, and those four are regression fences that are correctly green at HEAD:

| Test | Why it is green at HEAD, and must stay green |
|---|---|
| `TestR7CodexBackend_NoKeystrokeSeamEverOnCodex` | Codex proves no `ApprovalApplier`, no `TurnInterrupter` and (once the seam exists) no `KeystrokeComposer`. Playbook §8.2 made STRUCTURAL. |
| `TestR7CodexSignalSources_TheTurnRowsAreUnchanged` | `turn/started`->active and `turn/completed`->idle were always right; M4.5 pays the D1 debt by building the PRODUCER, not by rewriting the mapping. |
| `TestR7CodexSignalSources_TheGridHeuristicRowSTAYS` | ADR-007's T-3 fallback, and the only status a pre-R7 Codex session has ever had. |
| `TestR7CodexSignalSources_EveryDeclaredEventIsARealMethod` | Anti-drift: a row naming a method the server does not have can never fire. Reads the recorded inventories from disk, so it cannot pass vacuously. |

The same staged-stub technique was used to TYPE-CHECK (not run) every other new test file
against the intended contract, so none of them is hiding a syntax or wiring error behind the
compile failure. All eight stub files and the four temporary production edits were removed; the
`git status` above is the proof.

## Fixture extension, and its provenance

Three new files under `docs/verification/r1-codex-fixtures/`, all generated **offline** from
`codex app-server generate-ts` / `generate-json-schema` against the same installed
codex-cli 0.147.0 under an isolated `CODEX_HOME`. No account was touched and nothing was
invented:

- `r7-PROVENANCE.md` -- the three provenance classes (RECORDED / SCHEMA-DERIVED / STILL UNKNOWN)
  and the rule for each: a test that needs a VALUE must use a RECORDED file; a test that needs a
  SHAPE may use a schema-derived one.
- `r7-schema-methods.txt` -- the ten server-to-client request methods, which
  `protocol-methods.txt` (notifications only) does not list.
- `r7-schema-derived-frames.json` -- frames for the shapes the gate never captured, plus the
  per-kind decision vocabulary.
- `r7-open-questions.md` -- what the bindings did NOT settle, each with its probe.

**Five things the bindings settled that the design had listed as open**, all now driven by tests:

1. `item/completed` for a `commandExecution` carries `aggregatedOutput` AND `exitCode`, so R7
   needs no `outputDelta` accumulator (ADR-013 §R7.9 question 1, answered in the lean direction).
2. `TurnStartParams` and `TurnSteerParams` both carry `clientUserMessageId`, and the
   `userMessage` item carries `clientId` -- so the composer echo is **exact** and the text
   correlation whose mis-attribution is recorded at `chat.go:52-70` is not carried onto Codex.
3. `TurnSteerResponse` is `{turnId}` only -- §R7.5's question is answered "no", and moot.
4. `ThreadReadParams{includeTurns}` populates `Thread.turns`, so a lossless post-outage backfill
   plausibly exists (§R7.6's "largest open mechanical question"); WHETHER it is lossless is Q4,
   measured only under realcli.
5. The decision vocabulary is **per approval kind**, which is why ADR-010 §5 and the R1 gate
   disagreed. `CommandExecutionApprovalDecision` has two OBJECT variants that cannot ride a
   string `DecisionChoice.ID`; the test asserts they are NOT offered (Q1).

## Inventory: file, first failure, and the M-row it serves

See the parent task's returned inventory for line-level detail. Summary by row:

| Row | Files |
|---|---|
| M4.1 shim topology | `internal/adapter/r7_backendsource_test.go`, `internal/adapter/codex/r7_backend_test.go`, `internal/shimwire/r7_backendattach_test.go`, `internal/shim/r7_backend_test.go`, `internal/daemon/r7_backendlaunch_test.go` |
| M4.2 InteractionSource + batching | `internal/appserver/r7_wsupgrade_test.go`, `internal/adapter/codex/r7_interaction_test.go`, `internal/skeleton/r7_pump_test.go`, `internal/remotegw/r7_itemwindow_test.go` |
| M4.3 native approvals | `internal/skeleton/r7_approval_native_test.go`, `internal/skeleton/r7_backend_e2e_test.go` |
| M4.4 composer/interrupt dispatch | `internal/skeleton/r7_composersink_test.go`, `internal/skeleton/r7_backend_e2e_test.go` |
| M4.5 typed status | `internal/engine/r7_typedevent_test.go`, `internal/adapter/codex/r7_signalsources_test.go` |
| Lifecycle + capability honesty | `internal/skeleton/r7_lifecycle_test.go`, `internal/shim/r7_backend_test.go`, `internal/daemon/r7_backendlaunch_test.go` |
| realcli obligations (NEVER in CI) | `internal/appserver/r7_realcli_test.go` |

## The named mutation fences these tests carry

Each is a fence the design names and this slice makes real. A GREEN agent must be able to make
each mutation and watch a permanent test fail.

| Mutation | Test that must fail |
|---|---|
| Delete the `-backendPgid` kill from `finishEscalation` | `TestR7ShimBackend_ATermIgnoringBackendIsDEADAfterRunReturns` |
| Delete `SysProcAttr{Setpgid:true}` on the backend | `TestR7ShimBackend_TheBackendLeadsItsOwnProcessGroup` |
| Join `backendCmd.Wait()` before `finishEscalation` | `TestR7ShimBackend_TheJoinDoesNotBlockRun` |
| Make backend liveness `dial(socketPath) == nil` | `TestR7BackendLiveness_ADialThatSUCCEEDSIsNotLiveness` |
| Delete the session-dir containment check (9c) | `TestR7BackendSource_ObligationNineC_ThePlanMayNameNoPathOutsideTheSessionDir` |
| Revert `DefaultItemWindow` to 125 ms | `TestR7ItemWindow_AtThreeStreamingSessionsTheTerminalPlaneSTILLGetsSlots` |
| Mint a hook token for a backend session | `TestR7ApplyTypedEvent_RequiresNoTokenAndNoDurableSequence` |
| Reach the keystroke path on a Codex composer send | `TestR7ComposerSink_NoBackendAndNoKeystrokeSeamREFUSESHavingTypedNothing` and the e2e's `assertPTYUntouched` |
| Degrade on a successful post-restart rejoin | `TestR7Lifecycle_ADaemonRestartWithASuccessfulRejoinNeitherGapsNorDegrades` |
| Derive `structured_chat` from the adapter TYPE | `TestR7Capabilities_StructuredChatIsSeamANDLiveBackendPerSessionInstance` |

## The live defect this slice fences, reachable at HEAD before any R7 code

`Daemon.composerSend` (`internal/skeleton/chat.go:113`) calls `injectComposerText`
(`chat.go:227`) -- text plus a CR into the PTY -- for **every provider, with no seam and no
provider check anywhere on the path**. A phone send to a Codex session today types into the
Codex TUI, which playbook §8.2 forbids in as many words. It is fenced in three places, by
observation of the fake CLI's own stdin rather than by reading the source:
`TestR7ComposerSink_NoBackendAndNoKeystrokeSeamREFUSESHavingTypedNothing`,
`TestR7NativeApproval_ThePhonesApproveGoesOutAsAJSONRPCReplyAndNOTHINGIsTyped`, and the real
assembled path in `TestR7E2E_APhoneComposerSendReachesTheCodexBackendAsTurnStartThroughTheRealGateway`.
