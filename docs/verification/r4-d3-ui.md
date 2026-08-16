# R4 D3 follow-on — the machine switcher and global inbox, composed and reachable

Bead: agents-tracker-0ox9. Date: 2026-08-16. Rounds: RED + GREEN (round 1),
review fix pack RED + GREEN (round 2), review fix pack RED + GREEN (round 3).
Failing-first evidence: `r4-red/d3-ui-red.txt` (round 1),
`r4-red/d3-ui-red-round2.txt` (round 2), `r4-red/d3-ui-red-round3.txt`
(round 3). Every round-1 frozen contract (MachinesPanelScreenTest,
MachinesPanelViewTest, GlobalInboxScreenTest, PhoneSurfaceNavigationTest's
no-Machines-tab assertion) is binding and green. One existing test was
changed in round 3 and only by STRENGTHENING it — see §5.

## 0. Scope: what a user can and cannot complete (RESTORED, round 3)

Round 2 deleted the D3 honesty disclosure from
`docs/verification/r4-multimachine.md` and replaced it with a blanket claim
that all four R4 affordances were user-reachable, repeating that claim here.
That is the overclaim the honesty amendment existed to prevent, inverted. The
disclosure is written back, in both files, and fenced:
`android/gate/r4_d3_round3_test.go:TestR4D3R3_TheD3RecordDisclosesWhatAUserCannotComplete`.

COMPLETABLE BY A USER:

- SWITCH_COMPUTER — records the selection, marks the row `selected`, speaks the
  success. It does NOT re-target this phone's live relay session
  (`mobile/machines.go:19-21`), and `MachinesPanelScreen.switchedTo` says so in
  the same sentence.
- FORGET_COMPUTER — asks, then destroys exactly one pairing.
- GLOBAL_INBOX — as a readout; rows are inert by decision (§3 (b)).

NOT COMPLETABLE BY A USER IN THIS SLICE:

- ADD_COMPUTER — reachable, wired to `App.AddMachine`, and it cannot be
  finished here. Registering the pairing is all this slice does; the second
  machine's namespace then awaits THAT machine's own pairing ceremony, which is
  **bead agents-tracker-ak2s** and is explicitly out of this slice by ruling.
  Until ak2s lands, a hand-added row has no live client, shows its last-sync age
  forever and is forget-only. This is stated ON SCREEN under the add form
  (`MachinesPanelScreen.ADD_LIMITS`), not only in this file.

## 1. The six verbs, wired

| Verb | Production caller | Screen control |
|---|---|---|
| `App.Machines` | `FacadeBridge.machines()` -> `PhoneSurface.machinesPanel` | the switcher's row snapshot, drawn only where `MachinesScreen.destinationFor` answers MACHINES |
| `App.AddMachine` | `PhoneSurface.addComputer` (asks `ADD_CONFIRM`, then stop -> add -> start on the COMMAND lane under one single-flight key) | `Add computer` CTA (`MachinesPanelScreen.ADD_LABEL`) over the surface-owned two-field form, with `ADD_LIMITS` stated under it |
| `App.SelectMachine` | `PhoneSurface.switchComputer` (marks the row via `selectedMachine`, speaks `switchedTo`) | a healthy row's tap, BY MACHINE ID (MM4); a broken row takes no tap and renders its own fault at rest |
| `App.ForgetMachine` | `PhoneSurface.forgetComputer`, behind an AlertDialog asking `MachinesPanelScreen.FORGET_CONFIRM` | per-row `Forget this computer` denyChip |
| `App.GlobalInbox` | `FacadeBridge.globalInbox()` -> `PhoneSurface.drawGlobalInbox` | `All sessions` entry on the switcher |
| `MachineList.Cap` | `FacadeBridge.machines()` snapshot -> `MachinesPanelScreen.of(cap = ...)` | the honest cap sentence, rendered only when exceeded (ADR-018) |

Navigation: Settings panel row named `Computers` (`ENTRY_LABEL`, spent by
`SettingsPanelView.kt`, behavioural coverage `SettingsPanelMachinesEntryTest`)
-> `PhoneSurface.openMachines` -> `drawMachines` through the first-run
resolver -> `openGlobalInbox` one level deeper. The six
`android/unbound-verbs.tsv` rows (previously :58-63) are deleted;
`boundverbledger_test.go` and `r4_d3_reachability_test.go` hold both halves.

## 2. Round-2 review findings and their fixes

1. BLOCKING, Add could never succeed: `mobile/machines.go:418` refuses
   AddMachine while `a.sess != nil` (every foregrounded phone), and the
   ErrClassInvalidRequest routed to a "report a bug" toast. Fixed
   surface-side, never by weakening the facade's guard: `addComputer`'s verb
   is now `app.stop(); try { app.addMachine(id, name) } finally
   { app.start() }` -- the surface satisfies the stated precondition (the MM6
   migration must not race a live drain) on the lane, off the looper. Gate:
   `TestR4D3R2_AddComputerSatisfiesTheMigrationPreconditionItself`.
2. BLOCKING, one-tap Forget: now asks `MachinesPanelScreen.FORGET_CONFIRM`
   (exact copy frozen by `MachinesPanelRound2Test`) via AlertDialog before
   `machineVerb`, mrq5's own mechanism spent at last. Gate:
   `TestR4D3R2_ForgetAsksTheModelsQuestionBeforeTheVerb`.
3. BLOCKING, back gesture unarmed: `pushDrillDown()` is now the single
   arming predicate (`detail != null || machinesOpen || globalInboxOpen`),
   called by the detail setter, the open/close quartet, the panel-drop paths
   and the tab-pop; `PhoneActivity.handleOnBackPressed` commits through
   `PhoneSurface.closeDrillDown()`, which pops innermost-first (global inbox,
   switcher, session detail) and clears the predictive-back preview. Gate:
   `TestR4D3R2_TheBackGestureIsArmedForTheNewDrillDowns`.
4. Silent no-op on roster refusal / PAIR_ONLY: `machinesPanel` and
   `drawGlobalInbox` refusals go through `say(...)` (line + toast over the
   screen the user is on); the resolver's PAIR_ONLY answer is spoken as
   `MachinesPanelScreen.PAIR_FIRST`. Gate:
   `TestR4D3R2_ARosterRefusalIsNeverASilentNoOp`.
5. Last-sync age not rendered: `MachinesPanelScreen.statusLine(row, nowUnixMs)`
   (clocked overload; one-argument form untouched) spends
   `MachineFreshness.sinceLastHeard` -- the app's one elapsed-duration model --
   rendering `synced 4m ago` / `never synced`; `drawMachines` passes
   `System.currentTimeMillis()` and folds the minute into its redraw guard so
   the age is as fresh as the last render (the sync pill's own freshness).
   Gate: `TestR4D3R2_TheMachinesDrawSpendsTheClockSeam`; words frozen in
   `MachinesPanelRound2Test`, rendering in `MachinesPanelViewRound2Test`.
6. This document, plus the amended D3 section of `r4-multimachine.md`.

## 3. Non-blocking observations, decided

- (a) `FacadeBridge.machines()` runs registry open + per-namespace Resume on
  the looper at first draw. DEFERRED as a known cost, stated here: it is the
  same synchronous-draw shape the settings roster already has, it is outside
  `s25_mainthread_test.go`'s SEND-plane fence by that gate's own derivation,
  and moving the roster read onto a lane is a surface-wide redraw-model
  change this fix pack must not smuggle in. Filed as follow-up work.
- (b) Global inbox rows are INERT by decision: `App.SelectMachine` does not
  retarget the live session, so a row tap could only pretend to navigate.
  The list is a readout this slice; row navigation belongs with a
  select-and-retarget slice.
- (c) The settings entry now has behavioural coverage:
  `SettingsPanelMachinesEntryTest` (composes by recorded name, fires, and a
  null wiring composes no dead control).
- (d) The `addForm` slot and `onBack` now have behavioural coverage:
  `MachinesPanelViewRound2Test` (disclosed as pins of round-1 behaviour).

## 4. Gates (round 2 GREEN, 2026-08-16, all exit 0)

- Kotlin: `./gradlew --no-daemon --rerun-tasks --no-build-cache
  :app:testDebugUnitTest` BUILD SUCCESSFUL in 3m37s (end 13:19:40Z); 153
  result XMLs, zero failures/errors; new classes ran
  (MachinesPanelRound2Test 7/7, MachinesPanelViewRound2Test 3/3,
  SettingsPanelMachinesEntryTest 2/2) beside the round-1 frozen contracts
  (MachinesPanelScreenTest 7/7, MachinesPanelViewTest 8/8,
  GlobalInboxScreenTest 3/3, MachinesScreenTest 6/6). AAR untouched
  throughout (app/libs/swarm.aar mtime Aug 16 13:51:16, 12427801 B, before
  and after); result XMLs freshly written (15:19:39 local), none deleted.
- Go: `go build ./...` clean; `go vet` clean; `golangci-lint run
  ./android/gate/...` 0 issues; `go test -race ./android/gate/` ok (46.9s,
  round-1 and round-2 D3 gates green together); `go test -run TestB94
  ./internal/verify/` ok. `mobile/` untouched this round.


## 5. Round-3 review findings and their fixes

1. BLOCKING, a successful SWITCH_COMPUTER was indistinguishable from a dead
   button: `switchComputer` settled with `machineVerb`'s default no-op, and
   `drawMachines`' equality guard early-returned because the panel was
   byte-identical — and the panel COULD NOT change, since `registrymanager`
   only flips `Connected` when the roster exceeds the cap and `MachineInfo`
   carries no current-machine fact at all. Fixed surface-side, both halves:
   `PhoneSurface.selectedMachine` records the switch and rides the panel
   (`MachinesPanel.selectedMachineId`), so the row renders
   `MachinesPanelScreen.SELECTED_MARK` first on its status line AND the panel
   now differs across a switch, which is what ends the early return; and the
   success is spoken through `say(PressFeedback.ofSuccess(...))` with
   `MachinesPanelScreen.switchedTo(name)`, whose sentence states the limit
   (`the live session has not moved`) rather than overclaiming. The mark is
   this surface's memory: a rebuilt Activity marks no row, which asserts
   nothing, instead of inventing a fact the facade does not publish. Gates:
   `TestR4D3R3_ASuccessfulSwitchIsNotSilent`,
   `TestR4D3R3_TheSelectedMachineReachesThePanelAndTheRow`; words frozen in
   `MachinesPanelRound3Test`, rendering in `MachinesPanelViewRound3Test`.
2. BLOCKING, ADD_COMPUTER cannot be completed by a user and the record claimed
   it could. Per the orchestrator's ruling the fix is HONESTY, not a pairing
   ceremony (bead agents-tracker-ak2s owns that and is out of this slice):
   `MachinesPanelScreen.ADD_LIMITS` is drawn unconditionally under the add form
   as a `notice`, the way ADR-018's cap sentence is drawn, and states both
   limits in `mobile/machines.go:19-21`'s own words — the added computer still
   needs its own pairing ceremony, and switching does not move the live
   session. The deleted scoped disclosure is restored in both verification
   records (§0). Gates: `TestR4D3R3_TheAddFormsLimitsAreComposed`,
   `TestR4D3R3_TheD3RecordDisclosesWhatAUserCannotComplete`.
3. BLOCKING, Add severed every input lease and destroyed unsent input with no
   confirmation while the LESS destructive Forget asked, and a double tap ran
   the whole sequence twice. `addComputer` now asks
   `MachinesPanelScreen.ADD_CONFIRM` — modelled on `FORGET_CONFIRM` and naming
   the real blast radius (`App.Stop` → `suspendInput` → `coalesce.Abandon` +
   `Leases().SeverAll` + a real disconnect) — before anything reaches the lane;
   the verb, its ordering and its `finally` are unchanged inside the same
   function, so round 2's fence still binds. The double tap is fenced by an
   OPT-IN key on `VerbDispatch.enqueue` (`key: Any? = null`, returns whether the
   work was accepted): unkeyed work stays undroppable for the push-token
   reconciliation (agents-tracker-b6iu), keyed work is single-flight per key,
   and the refusal is SAID (`ADD_IN_FLIGHT`) rather than swallowed. Gates:
   `TestR4D3R3_AddAsksBeforeItSeversEveryLease`,
   `TestR4D3R3_ASecondAddWhileOneIsRunningIsRefusedOutLoud`; behaviour in
   `VerbDispatchRound3Test` (6 tests, hand-driven executors).
4. MAJOR, test integrity: `MachinesPanelViewTest.aRowRendersItsNeedsInputCount`
   became vacuous without its text changing. Its `view()` helper passed no
   `nowUnixMs`, so round 2's clocked sublabel used the REAL clock over
   `lastSyncUnixMs = 1000L` and rendered `synced 20681d ago`, which satisfies
   `contains("2")` for a machine with `needsInput = 0`. STRENGTHENED, never
   weakened: the helper now pins `nowUnixMs = 10_000L` and the assertion asks
   for the phrase `2 sessions need input`; a negative control
   (`aRowWithNothingWaitingRendersNoNeedsInputPhrase`) was added beside it, and
   the vacuity itself is demonstrated permanently by
   `MachinesPanelRound3Test.theLastSyncAgeAloneSuppliesDigitsAndNoNeedsInputPhrase`.
5. MINOR, `MachinesPanelView.kt` and `GlobalInboxView.kt` were absent from
   `s24ScreenComponents`, so `TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit`
   asked nothing of them beyond "calls at least one kit factory". Both are
   claimed now with their real composition (and `PairOnlyView.kt`, found by the
   same sweep and unclaimed since it landed), and the omission is closed at its
   class rather than its instance:
   `TestR4D3R3_EveryComposedScreenIsClaimedByTheCompositionTable` fails on ANY
   view-building screen with no entry in the table.

## 6. Gates (round 3 GREEN, 2026-08-16, all exit 0)

- Kotlin: `./gradlew --no-daemon --rerun-tasks --no-build-cache
  :app:testDebugUnitTest` BUILD SUCCESSFUL in 3m30s (start 13:55:09Z, end
  13:58:40Z); 156 result XMLs, 1240 tests, zero failures/errors. New classes
  ran (VerbDispatchRound3Test 6/6, MachinesPanelRound3Test 9/9,
  MachinesPanelViewRound3Test 3/3) beside every earlier frozen contract
  (MachinesPanelViewTest 9/9 = 8 frozen + one new negative control,
  MachinesPanelRound2Test, MachinesPanelViewRound2Test,
  MachinesPanelScreenTest, GlobalInboxScreenTest, MachinesScreenTest,
  VerbDispatchTest). AAR untouched throughout (app/libs/swarm.aar mtime
  Aug 16 13:51:16, 12427801 B, before and after); result XMLs freshly written
  (15:58:39 local), none deleted.
- Go: `go build ./...` clean; `go vet ./android/gate/` clean;
  `golangci-lint run ./android/gate/...` 0 issues; `go test -race
  ./android/gate/` ok (51.5s -- rounds 1, 2 and 3 green together);
  `go test -count=1 -run TestB94 ./internal/verify/` ok (3.4s).
  `mobile/` and `internal/**` untouched this round.
- Failing-first record: `docs/verification/r4-red/d3-ui-red-round3.txt`
  (7/7 Go gates RED, 39-error Kotlin compile-RED, and the vacuity proof for
  the test-integrity finding).
