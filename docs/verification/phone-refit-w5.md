# Phone refit W5: words everywhere (verification evidence)

Bead `agents-tracker-d45a.5`. Contract: `docs/specifications/phone-refit-playbook.md` §6
(W5.1-W5.5), read with the amendment blocks of §3, §4, §5, §7, §8. Worktree `refit-w5`, branch
`refit/w5`, branched from `main` at `a1507537`. Each step below records the tests written first,
the RED run (exact failure text where the run's result XML was still on disk when captured; test
name, exception and line otherwise), the GREEN run, and one negative control (the fix perturbed
back, the tests shown failing, the file restored with `git checkout --`, 0 dirty after).

Environment: go1.26.5 darwin/amd64, golangci-lint 2.12.2 (matches `.github/workflows/ci.yml`).
Gradle ran from a lane script (`gradle-lane.sh`, in the session scratchpad): `pgrep -f
'^/[^ ]*java .*gradle-wrapper[.]jar'` empty before each start, a start stamp touched, every
`app/build/test-results/testDebugUnitTest/*.xml` checked newer than the stamp, and
`app/libs/swarm.aar` (mtime 1787932204, size 12657410) checked unmoved across every run. Fourteen
lane runs in all; the machine was shared with other fleets throughout. No test result was deleted.

## Findings recorded before the first edit

1. **The checker and W5.2 pull one row two ways.** `scripts/check-conversation-copy.py` binds
   `bubble.refused` to `Composer.kt` and `ErrorRouting.kt` as a contiguous substring, while W5.2's
   `machine.ifEmpty { "your computer" }` would leave no contiguous sentence in `Composer.kt`.
   Resolved in the INPUT_BUSY arm alone: `if (machine.isEmpty()) "Not sent. Finish typing on your
   computer first." else "Not sent. Finish typing on $machine first."` -- the unnamed sentence is
   one literal the sheet binds, the named one is the same words over the name.
2. **Row 12 has no well.** `killSwitchPanel` (substrate row 12) draws the command as an inline
   `Mono.InlineStrong` span inside its body and `Kit.emphasised` throws when the body lacks it;
   `KillSwitchPanelTest` pins the span and the two-child shape. So `MachineAndLaunch.kt:175`'s
   "(well:)" is drawn as row 12's own cell: the sentence ends at a colon and the verb follows in
   the cell -- `Remote control is off on nathans-mbp. To turn it on: swarm remote on`. The other
   three "(well:)" rows (LaunchPreset x2, PairOnly x2) reuse the kit's `monoWell` under the
   sentence, as the contract asks.
3. **`{device}` is in nobody's reach.** No model on the phone carries this device's id, so the
   repair-required well carries two lines, `swarm remote devices` then `swarm remote revoke
   <device-id>` (the two commands `UNREGISTER_FIRST` already spelled). `REVOKE_UNCONFIRMED`'s well
   is `swarm remote devices`, as tabled.
4. **One `android/gate` test asserts UI copy.** `w6o3_terminalpaironly_test.go`
   `TestW6O3_TheRemedyIsStatedBeforeTheControlThatNeedsIt` reads `copyFor`'s REPAIR_REQUIRED arm,
   requires a three-part `PairOnlyCopy`, and requires the BODY to contain both `swarm remote`
   steps. Per the contract it is not edited; it is red on this branch and reported to the
   orchestrator for a ruling (accept the 4-part copy and read the `command` slot, or keep the
   commands in that one body).
5. **The blast radius is wider than W5.5's nineteen classes.** Test lines carrying an old literal
   were inventoried before editing (100 non-comment lines across 40 files); the runs below found
   more (`contains(...)` clauses on the old words). Every changed assertion is in the table.
6. **Line numbers.** Every anchor was located by its literal; the real lines at `a1507537` are in
   the commit diffs.

## W5.4 The two tables (commit `059765b7`)

Tests first: the 39 classes below edited to the "Proposed" literals (script
`w5_step1_tests.py`, 74 exact-match edits), plus new tests for the wells:
`LaunchPresetScreenTest` `a denial's command is the well's text and never part of the sentence`,
`LaunchPresetWellTest` (new file: `a denial with a command draws it in a well under the sentence`,
`a denial with no command draws no well`), `PairOnlyScreenTest` `the repair-required screen's
commands are a well and not a sentence`, `PairOnlyViewTest` `a command the screen points at is
drawn in a well under the sentence`; `SyncStatusTest` gains `SyncStatus.NOT_SEEN` for the
never-heard pill; `internal/verify/conversation_copy_test.go`'s mutation fixtures move to the
sentences they mutate (bubble.stale with a curled apostrophe for the near-miss; the sync row).

### RED 1, compile (lane `step1-red`, gradle exit=1): the API the tests reach for

```
e: ui/SyncStatusTest.kt:186:33 Unresolved reference 'NOT_SEEN'.
e: ui/screens/LaunchPresetScreenTest.kt:176:60 Unresolved reference 'commandFor'.
e: ui/screens/LaunchPresetWellTest.kt:28:9 No parameter with name 'availabilityCommand' found.
e: ui/screens/LaunchPresetWellTest.kt:41:77 Unresolved reference 'COMMAND'.
e: ui/screens/PairOnlyPurgeNoticeTest.kt:136:28 Unresolved reference 'REVOKE_COMMAND'.
e: ui/screens/PairOnlyPurgeNoticeTest.kt:137:28 Unresolved reference 'revokeCommandFor'.
e: ui/screens/PairOnlyScreenTest.kt:144:84 Unresolved reference 'command'.
e: ui/screens/PairOnlyViewTest.kt:260:13 No parameter with name 'revokedCommand' found.
(25 lines in all, every one a member of the new API; scratchpad step1-red-compile.txt)
```

Go, same tree (`go test ./internal/verify/`):

```
--- FAIL: TestCopySheet_TheCheckerCatchesARetypedSentence (0.32s)
    conversation_copy_test.go:206: a curled apostrophe substituted for the straight one passed the checker. ...
--- FAIL: TestCopySheet_TheCheckerCatchesASentenceThatLeftTheCode (0.30s)
    conversation_copy_test.go:228: a screen replaced a signed sentence with an invented one and the checker passed:
--- FAIL: TestCopySheet_ABoundRowWithNothingToCompareIsAFailure (0.23s)
    conversation_copy_test.go:294: the sync row no longer tables its sentence; this control cannot run
```
(The new sentences existed nowhere yet, so each mutation was a no-op and the checker accepted it.)

### RED 2, behavioural (lane `step1-red2`, gradle exit=1; the structure half applied first, no sentence changed)

`tests=391 failures=60 errors=0`, every failure a copy assertion in an expected class:
SessionDetailHeaderTest 8, SettingsPanelConnectionTest 7, MachinesPanelRound3Test 4,
PairOnlyRevokeNoticeTest 3, ApprovalSheetPanelTest 3, TranscriptDecisionTest 2, SyncStatusTest 2,
SettingsPanelScreenTest 2, PairingPanelScreenTest 2, PairingEntryRoutingTest 2,
PairedMachineRowTest 2, MachinesPanelRound2Test 2, LaunchPanelScreenTest 2,
ErrorRoutingRefusalCopyTest 2, ComposerSendStateTest 2, and one each in TranscriptOverflowTest,
TranscriptChatRenderTest, SettingsScreenTest, SettingsPanelViewTest,
SettingsPanelConnectionViewTest, SessionDetailKillVerdictTest, PairingCameraCopyTest,
PairOnlyScreenTest, PairOnlyPurgeNoticeTest, MachinesPanelViewTest, MachinesPanelScreenTest,
LaunchPresetScreenTest, ConnectionUiStaleNoticeTest, ComposerShutReasonTest, ActivityPanelTest
(names and lines in scratchpad step1-red-behavioural.txt; the run's XML was rewritten by the
next lane run, so the texts below are the same assertions captured on the controls run):

```
FAILURE ComposerSendStateTest > inputBusyGetsTheSentenceTheShimEarned
    org.junit.ComparisonFailure: ... expected:<Not sent[. Finish typing on your computer first].> but was:<Not sent[ — the terminal's input line was not empty].>
FAILURE ActivityPanelTest > the unstamped section has no heading   (W5.3, same shape)
```

### The checker RED (the sheet's control), Kotlin moved, sheet not yet

`python3 scripts/check-conversation-copy.py .` exit=1, 15 DRIFT lines: `bubble.refused`
(ErrorRouting.kt, Composer.kt), `bubble.stale` (both), `composer.nochat` x2, `composer.offline`
x2, `composer.torn` x2 (Composer.kt), `decision.settled.owner`, `decision.unknown`, `empty`
(TranscriptPanel.kt), `earlier` (SessionDetailPanel.kt), `sync` (ConnectionUi.kt). With the
sheet's bound rows moved in the same commit: exit=0, "20 binding(s) checked across 15 of 28
tabled row(s)"; `go test ./internal/verify/` ok.

### GREEN (lane `step1-green2`, gradle exit=0)

`tests=401 failures=0 errors=0 skipped=0`, xml fresh 39/39. (`step1-green`, the first attempt,
was 402/15: fifteen `contains(...)` clauses on words the tables removed -- listed under W5.5 as
"round 2" -- and the row-12 body, finding 2 above.)

### Negative control P1 (`Composer.kt`'s unnamed INPUT_BUSY sentence set back to the old wording)

```
FAILURE ComposerSendStateTest > inputBusyGetsTheSentenceTheShimEarned
    org.junit.ComparisonFailure: ... expected:<Not sent[. Finish typing on your computer first].> but was:<Not sent[ — the terminal's input line was not empty].>
FAILURE ComposerTest > noticeFor falls back to your computer
    org.junit.ComparisonFailure: expected:<Not sent[. Finish typing on your computer first].> but was:<Not sent[ — the terminal's input line was not empty].>
checker under P1 alone: exit=1, DRIFT bubble.refused android/app/src/main/kotlin/dev/swarm/phone/ui/kit/Composer.kt
```

Done when (§6): `grep -rn "your machine\|the machine\|structured\|event stream\|input line"
android/app/src/main` returns only KDoc, plus the identifiers `structuredChat` (facade field),
`STRUCTURED_GAP`/`"structured_gap"` and `"structured_unsupported"` (wire tokens) -- verified with
a comment-stripping grep (scratchpad `codegrep.py`): zero code lines for the four phrases, and
`structured` only in those identifiers.

## W5.2 Naming the computer (commit `6028c7d9`)

Tests first: `ComposerTest` `noticeFor names the machine when one is known`, `noticeFor falls
back to your computer`; `SessionDetailComposerTest` `the composer's refusal names the computer
the header names`; `SettingsPanelScreenTest` `the pending notice names the computer the panel is
given`.

RED 1 (lane `step2-red`, compile): `e: ui/kit/ComposerTest.kt:199:51 No parameter with name
'machine' found.` (and :212). RED 2 (lane `step2-red2`; the model signature in, the two call
sites not): `tests=46 failures=2`:

```
the composer's refusal names the computer the header names :: org.junit.ComparisonFailure: expected:<...t. Finish typing on [MacBookPro] first.> but was:<...t. Finish typing on [your computer] first.>
the pending notice names the computer the panel is given :: java.lang.AssertionError: the pending notice does not name the computer whose confirmation it waits for
```

GREEN (lane `step2-green2`): `tests=105 failures=0` over ComposerTest, ComposerSendStateTest,
SessionDetailComposerTest, SessionDetailVerdictTest, SettingsPanelScreenTest,
SettingsPanelViewTest, SettingsScreenTest. (`step2-green` was 105/3: three
`SessionDetailVerdictTest` clauses on W5.4's sentences -- the class was outside step 1's filter --
moved to the shipped words; in the table.)

Negative control P2 (`Composer.kt`'s named arm ignores the name):

```
FAILURE ComposerTest > noticeFor names the machine when one is known
    org.junit.ComparisonFailure: expected:<...t. Finish typing on [MacBookPro] first.> but was:<...t. Finish typing on [your computer] first.>
FAILURE SessionDetailComposerTest > the composer's refusal names the computer the header names
    org.junit.ComparisonFailure: expected:<...t. Finish typing on [MacBookPro] first.> but was:<...t. Finish typing on [your computer] first.>
```

Where the name is not in reach, "your computer", and no parameter grown: the five holders §6
names (`ErrorRouting.byToken`, `ConnectionUi`, `SyncStatus`, `PairingUi`/`PairingPanel`,
`TerminalFallbackScreen`), and also `SessionDetailScreen.historyCapacityNotice()` (spoken from
`PhoneSurface`'s load-earlier lane, where no label is in scope), `LaunchPresetScreen` (its
`noticeFor` takes a state alone; `PhoneSurface.presetPanelOnScreen` has no label either),
`MachinesPanelScreen.FORGET_CONFIRM` / `ADD_CONFIRM` (constants; `brokenNotice(row)` and
`switchedTo(name)`, which take the row, say the name). `MachinePane.killSwitchExplanationOf`
grew `machine = ""` (not in the must-not list) so `SettingsPanel.connectionOf` names the
computer it already has. `TriageInboxScreen`'s `machineNames` were in reach but no W5.4 row
touches that screen.

## W5.3 Chrome removed (commit `6b4e12e3`)

Tests first: `TranscriptViewTest` `the transcript is composed of the parts its recorded
composition names` -> `assertNull(kitFind(SECTION_LABEL))`; the constructor calls in
`TranscriptViewTest`, `TranscriptViewDecisionTest` and the `ScreenAirSweepTest` fixture drop
`heading`; `ActivityPanelTest` gains `the unstamped section has no heading`; `kill.confirm` is
the checker's.

RED 1 (lane `step3-red`, compile): `e: ui/screens/TranscriptViewTest.kt:81:42 No value passed
for parameter 'heading'.` (and TranscriptViewDecisionTest.kt:81). RED 2 (lane `step3-red2`; the
transcript half in, the Activity and kill halves not): `tests=81 failures=2`:

```
the unstamped section has no heading :: org.junit.ComparisonFailure: expected:<[]> but was:<[Journal]>
a conversation with nothing in it draws its copy, not an empty area :: java.lang.AssertionError: the heading went with the rows, so an empty conversation is a gap where a section used to be rather than a section saying it is empty
```

(The second is the W3-era clause that pinned the label over an empty conversation; its
successor asserts the label's absence. `TranscriptPanelTest`'s `assertTrue(panel.heading.isNotEmpty())`
went with the field.) Checker RED on `kill.confirm` (Kotlin moved, sheet not yet): exit=1,
`DRIFT android/app/src/main/kotlin/dev/swarm/phone/ui/screens/SessionDetailPanel.kt`; with the
row moved in the same commit, exit=0 and `internal/verify` ok.

GREEN (lane `step3-green2`): `tests=98 failures=0` over TranscriptViewTest,
TranscriptViewDecisionTest, TranscriptPanelTest, ScreenAirSweepTest, ActivityPanelTest,
ActivityPanelViewTest, SessionDetailMenuTest, SessionDetailPanelTest. (`step3-green` was 98/2:
two `SessionDetailPanelTest` clauses on W5.4's words, in the table.)

Negative control P3 (the label re-added over the conversation; the unstamped section headed
"Journal" again):

```
FAILURE TranscriptViewTest > the transcript is composed of the parts its recorded composition names
    java.lang.AssertionError: the conversation draws a heading over itself. It IS the screen, and a label saying so is chrome standing between the reader and the first message (phone refit W5.3) expected null, but was:<android.widget.TextView{ba5f862 ...}>
FAILURE TranscriptViewTest > a conversation with nothing in it draws its copy, not an empty area
    java.lang.AssertionError: an empty conversation drew a heading over its empty state; ... expected null, but was:<android.widget.TextView{f597be ...}>
FAILURE ActivityPanelTest > the unstamped section has no heading
    org.junit.ComparisonFailure: expected:<[]> but was:<[Journal]>
```

`android/gate/s24_screens_test.go`: the kit-reach inventory for `TranscriptView.kt` required it
to spend `sectionLabel` ("the heading over the conversation"); the row is dropped under a dated
note (a composition inventory, not a copy assertion). `TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit`
passes. The Activity view is unchanged: the unstamped section's `sectionLabel` is drawn with ""
(the slot keeps the day-heading pages and the unstamped page on one geometry); "Journal" the
word is gone.

## The sheet (commit `d69bbcfa`, plus the bound rows in `059765b7` and `6b4e12e3`)

(a) The fifteen bound rows match the Kotlin byte for byte: checker exit 0, 20 bindings checked,
`internal/verify` ok (all `TestCopySheet_*`). (b) Rows and drawn stages that quote a table
sentence follow the tables: `header.state` (capitalised), `tool.clipped`, `gap`, `earlier`,
`composer.working`, the composer stages, the clipped card, the settled/unknown decision stages,
the kill dialog. (c) The token block is the Slate palette from
`android/app/src/main/res/values/colors.xml` (ADR-021), `--lit` follows W4.8's 0.18, the
seventeen `rgba(...)` derivations of hero/err follow their tokens, "champagne" in the
`decisionPill` row and the footer's "Obsidian" follow. (d) `grep -rn "Decision needed" docs
android/app/src/main` outside the playbook's "Now" column and the verification files' quoted
RED texts: only `docs/verification/remote-phaseA-a7-review.md:95` ("Decision needed (ADR)",
the unrelated sense W6 recorded); the drawing's two stages had already moved with W6's review
round. No test reads the sheet's CSS, so the palette change has no test control; the checker
RED above is the sheet's control.

## W5.5 Test churn

Rule applied: a test moves to the new sentence only where its subject is the sentence; a
fixture literal is swapped and nothing else moves. Of the seven named fixture classes,
`KitDensityTest` (2 literals), `PressFeedbackAuditTest` (2) and `ScreenAirSweepTest` (3, plus
the `heading` line the field's removal forces) carried an old literal; `MotionTest`,
`VerbDispatchRound3Test`, `PhoneLaunchSurfaceTest`, `SettingsSurfaceReplaceTest` carried none
(their diff against `a1507537` is empty). Of the twelve named subject classes, `DecisionPillTest`
already said "Needs your answer" (W6.2) and is untouched. Tests outside both lists changed
because a `contains(...)` clause pinned an old word; each is below. Tests deleted because their
clause has no successor in the tables: `ConnectionUiStaleNoticeTest` `it says the thing the
reader can act on -- that waiting is enough` ("repairs itself"), `PairedMachineRowTest`'s two
sublabel tests (replaced by `the row has no sublabel`), the second-session clause of
`LaunchPresetRound2Test.outcomeUnknownNeverPromisesAnIdempotentReconfirm`, and the
`heading` assertion of `TranscriptPanelTest`'s empty-transcript test. No `android/gate` test was
edited for copy (finding 4).

Edits made by hand outside the scripts (all in the same rule): `TranscriptPanelTest.kt`
`assertTrue(panel.heading.isNotEmpty())` removed; `SessionDetailPanelTest.kt`
`headerSubtitle.contains("working")` -> `"Working"` and `notice.contains("live-only")` ->
`"didn't get through"`; `TranscriptViewTest.kt` empty-conversation `assertNotNull(SECTION_LABEL)`
-> `assertNull`; `ScreenAirSweepTest.kt` `heading = "CONVERSATION",` dropped;
`LaunchPresetRound2Test.kt` `notice.contains("second session")` block dropped.

### Changed assertions (scripted; before -> after, first 150 characters)

| step | test file | before | after |
|---|---|---|---|
| W5.4 | `ui/ErrorRoutingRefusalCopyTest.kt` | `private val refusedCopy = "Not sent — the terminal's input line was not empty."` | `private val refusedCopy = "Not sent. Finish typing on your computer first."` |
| W5.4 | `ui/ErrorRoutingRefusalCopyTest.kt` | `"Not sent — the conversation moved on. Read the latest turn and send again."` | `"Not sent. There's a new reply. Read it, then send again."` |
| W5.4 | `ui/ErrorRoutingRefusalCopyTest.kt` | `the user reads \"Your message was refused and not delivered\" --` | `the user reads \"Not sent. Try again.\" --` |
| W5.4 | `ui/ConnectionUiStaleNoticeTest.kt` | `"This view may be missing events. It repairs itself when the link recovers."` | `"Some updates may be missing."` |
| W5.4 | `ui/PairingCameraCopyTest.kt` | `"The camera did not start. Close any other app using it and try again, or enter " + "the code instead.",` | `"Camera didn't start. Close other camera apps, or enter the code.",` |
| W5.4 | `ui/SyncStatusTest.kt` | `"QUIET 18h"` | `"Last seen 18h"` |
| W5.4 | `ui/SyncStatusTest.kt` | `"QUIET 4m"` | `"Last seen 4m"` |
| W5.4 | `ui/SyncStatusTest.kt` | `"QUIET 3d"` | `"Last seen 3d"` |
| W5.4 | `ui/SyncStatusTest.kt` | `fun `a machine never heard from carries the word alone`() {` | `fun `a machine never heard from says not seen yet`() {` |
| W5.4 | `ui/SyncStatusTest.kt` | `assertEquals(SyncStatus.QUIET, status(freshness = neverHeard).pill)` | `assertEquals(SyncStatus.NOT_SEEN, status(freshness = neverHeard).pill)` |
| W5.4 | `ui/SyncStatusTest.kt` | `assertEquals("SYNCING", status(reconciled = false).pill)` | `assertEquals("Syncing…", status(reconciled = false).pill)` |
| W5.4 | `ui/SyncStatusTest.kt` | `assertEquals("BROKEN", status(state = ConnectionState.REVOKED).pill)` | `assertEquals("Offline", status(state = ConnectionState.REVOKED).pill)` |
| W5.4 | `ui/screens/ActivityPanelTest.kt` | `assertTrue( "the notice does not say the history is INCOMPLETE. `stale` on its own leaves a " + "reader to guess whether the list is old or has record` | `assertEquals( "the notice does not say entries are MISSING, in the reader's words (phone refit W5.4)", "Some entries are missing.", holed, )` |
| W5.4 | `ui/kit/GapDividerTest.kt` | `"records missing"` | `"Missing messages"` |
| W5.4 | `ui/kit/EarlierChipTest.kt` | `"Load earlier messages"` | `"Show earlier"` |
| W5.4 | `ui/screens/PairedMachineRowTest.kt` | `assertEquals( "Paired with nathans-mbp", PairedMachineRowScreen.of("nathans-mbp").label, )` | `assertEquals( "nathans-mbp", PairedMachineRowScreen.of("nathans-mbp").label, )` |
| W5.4 | `ui/screens/PairedMachineRowTest.kt` | `@Test fun `the copy is honest that replacing ends the current pairing`() { val row = PairedMachineRowScreen.of("nathans-mbp") assertTrue( "the row's c` | `/** * Phone refit W5.4: the row is the computer's name and nothing under it. The cost of * replacing is the confirmation's to state, once, when the co` |
| W5.4 | `ui/screens/PairingPanelScreenTest.kt` | `"Paired with nathans-mbp",` | `"nathans-mbp",` |
| W5.4 | `ui/screens/PairingPanelScreenTest.kt` | `"This pairing was interrupted before it finished. Nothing was joined.",` | `"Pairing was interrupted.",` |
| W5.4 | `ui/screens/MachinesPanelScreenTest.kt` | `assertTrue( "the notice does not name the row's OWN fault; a generic failure sentence routes " + "the user to the wholesale remedy ('clear this app's ` | `assertEquals( "the notice does not name the row that cannot open and the two per-row remedies, " + "which is what keeps a user off the wholesale remed` |
| W5.4 | `ui/screens/MachinesPanelViewTest.kt` | `assertTrue( "the broken pairing's own fault is nowhere on screen; App.SelectMachine's refusal " + "must be a user-visible state on the row that owns i` | `assertTrue( "the broken pairing's notice is nowhere on screen; App.SelectMachine's refusal " + "must be a user-visible state on the row that owns it, ` |
| W5.4 | `ui/screens/MachinesPanelRound2Test.kt` | `"Forget this computer? This phone deletes its pairing keys and cached sessions " + "for it. The computer itself is untouched, and other computers are ` | `"Forget this computer? You can pair it again later.",` |
| W5.4 | `ui/screens/MachinesPanelRound2Test.kt` | `"No computers are paired yet. Pair this phone with a computer first; Computers " + "fills in from the first pairing.",` | `"No computers yet. Pair one first.",` |
| W5.4 | `ui/screens/MachinesPanelRound3Test.kt` | `"Selected laptop. This phone's live session has not moved to it yet.",` | `"Now viewing laptop.",` |
| W5.4 | `ui/screens/MachinesPanelRound3Test.kt` | `"Adding a computer registers it on this phone. That computer still needs its own " + "pairing ceremony before it can answer, and switching to it recor` | `"You'll pair with it next.",` |
| W5.4 | `ui/screens/MachinesPanelRound3Test.kt` | `"Add this computer now? This phone briefly disconnects from your computers while " + "the pairing is registered, and anything typed but not sent is di` | `"Add this computer? The app reconnects for a moment.",` |
| W5.4 | `ui/screens/MachinesPanelRound3Test.kt` | `"Add computer is still running; nothing was sent.",` | `"Still adding…",` |
| W5.4 | `ui/screens/ApprovalSheetPanelTest.kt` | `"Your machine could not apply this answer."` | `"Couldn't send your answer. Try again."` |
| W5.4 | `ui/screens/SessionDetailKillVerdictTest.kt` | `assertTrue(notice.contains("did not end"))` | `assertTrue(notice.contains("Couldn't end"))` |
| W5.4 | `ui/screens/SessionDetailHeaderTest.kt` | `"idle · $MACHINE"` | `"Idle · $MACHINE"` |
| W5.4 | `ui/screens/SessionDetailHeaderTest.kt` | `"working · $MACHINE"` | `"Working · $MACHINE"` |
| W5.4 | `ui/screens/SessionDetailHeaderTest.kt` | `"not connected · $MACHINE"` | `"Not connected · $MACHINE"` |
| W5.4 | `ui/screens/SessionDetailHeaderTest.kt` | `"ended · $MACHINE"` | `"Ended · $MACHINE"` |
| W5.4 | `ui/screens/SessionDetailHeaderTest.kt` | `"needs you · $MACHINE"` | `"Needs you · $MACHINE"` |
| W5.4 | `ui/screens/SessionDetailHeaderTest.kt` | `"idle",` | `"Idle",` |
| W5.4 | `ui/kit/ComposerSendStateTest.kt` | `"Not sent — the terminal's input line was not empty.",` | `"Not sent. Finish typing on your computer first.",` |
| W5.4 | `ui/kit/ComposerSendStateTest.kt` | `"Add feedback...",` | `"Add a note while it works",` |
| W5.4 | `ui/kit/ComposerShutReasonTest.kt` | `assertTrue("a torn record says so: it is what the machine proved", torn.placeholder.contains("gap"))` | `assertEquals("a torn record says the chat is paused here (phone refit W5.4)", "Chat is paused here.", torn.placeholder)` |
| W5.4 | `ui/MachineAndLaunchTest.kt` | `"Your machine is online."` | `"Your computer is online."` |
| W5.4 | `ui/MachineAndLaunchTest.kt` | `"Not heard from your machine yet."` | `"Not seen yet"` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `"Your machine is online.",` | `"Your computer is online.",` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `row.presenceLine.contains("Not heard from your machine for") && row.presenceLine.contains("the relay's word and not your machine's"),` | `row.presenceLine.startsWith("Online · last seen ") && row.presenceLine.endsWith(" ago"),` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `assertTrue( "the panel lost the sentence saying the switch is off at the machine", off!!.body.contains("Remote control is switched off at your machine` | `assertEquals( "the sentence names the computer the switch is off on, and nothing else: the " + "command is the well's text, never the sentence's (phon` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `fun `one channel with a hole is named, in the wire's own word`() {` | `fun `one channel with a hole says updates are missing`() {` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `"The journal view has a gap.",` | `"Some updates are missing.",` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `"The journal and terminal views have gaps.",` | `"Some updates are missing.",` |
| W5.4 | `ui/screens/SettingsPanelConnectionTest.kt` | `"The journal, terminal and reply views have gaps.",` | `"Some updates are missing.",` |
| W5.4 | `ui/screens/SettingsPanelConnectionViewTest.kt` | `assertEquals("The journal and terminal views have gaps.", textOf(lines.single()))` | `assertEquals("Some updates are missing.", textOf(lines.single()))` |
| W5.4 | `ui/screens/SettingsPanelViewTest.kt` | `listOf("Needs your decision", "Task done")` | `listOf("Needs your answer", "Task done")` |
| W5.4 | `ui/screens/SettingsPanelViewTest.kt` | `listOf("Approvals and blocked prompts", "Completions and failures")` | `listOf("Approvals and questions", "Finished and failed sessions")` |
| W5.4 | `ui/screens/SettingsPanelScreenTest.kt` | `listOf("Needs your decision", "Task done")` | `listOf("Needs your answer", "Task done")` |
| W5.4 | `ui/screens/SettingsPanelScreenTest.kt` | `listOf("Approvals and blocked prompts", "Completions and failures")` | `listOf("Approvals and questions", "Finished and failed sessions")` |
| W5.4 | `ui/SettingsScreenTest.kt` | `"Battery saver can delay these notifications.",` | `"Battery saver can delay these.",` |
| W5.4 | `ui/screens/TranscriptOverflowTest.kt` | `"records missing · repair",` | `"Missing messages · Reload",` |
| W5.4 | `ui/screens/TranscriptChatRenderTest.kt` | `"records missing · repair",` | `"Missing messages · Reload",` |
| W5.4 | `ui/screens/TranscriptDecisionTest.kt` | `"This version of swarm cannot show this question. Answer it at your machine, or " + "update the app.",` | `"Update the app to answer here.",` |
| W5.4 | `ui/screens/TranscriptDecisionTest.kt` | `"answered at your machine",` | `"answered at your computer",` |
| W5.4 | `ui/PairingEntryRoutingTest.kt` | `"That code does not look right. It is ten characters from your machine's screen -- " +` | `"That code does not look right. It is ten characters from your computer's screen -- " +` |
| W5.4 | `ui/PairingEntryRoutingTest.kt` | `"full code your machine printed.",` | `"full code your computer printed.",` |
| W5.4 | `ui/screens/LaunchPanelScreenTest.kt` | `"Working directory on your machine",` | `"Working directory on your computer",` |
| W5.4 | `ui/screens/LaunchPanelScreenTest.kt` | `"Waiting for your machine to answer the launch.",` | `"Waiting for your computer to answer the launch.",` |
| W5.4 | `ui/screens/PairOnlyRevokeNoticeTest.kt` | `assertTrue( "the notice does not say the machine has not confirmed the removal, which is the " + "whole of what this phone knows", notice.contains("no` | `assertEquals( "the notice does not say the phone is unpaired and where to look if pairing is " + "refused, which is the whole of what this phone knows` |
| W5.4 | `ui/screens/PairOnlyRevokeNoticeTest.kt` | `assertTrue(notice.contains("swarm remote revoke")) }` | `assertFalse("a command inside a sentence", notice.contains("swarm remote")) assertEquals(PairOnlyScreen.REVOKE_COMMAND, PairOnlyScreen.revokeCommandFo` |
| W5.4 | `ui/screens/PairOnlyRevokeNoticeTest.kt` | `assertTrue( "a revoke that never reached the machine leaves the device registered for certain, " + "and the screen does not say so", notice.contains("` | `assertTrue( "a revoke that never reached the machine leaves the device registered for certain, " + "and the screen does not say so", notice.endsWith("` |
| W5.4 | `ui/screens/PairOnlyPurgeNoticeTest.kt` | `assertTrue( "the machine-side remedy went with them: this device is still registered AND still " + "holding key material", notice.contains("swarm remo` | `assertEquals( "the machine-side remedy went with them: this device is still registered AND still " + "holding key material", PairOnlyScreen.REVOKE_COM` |
| W5.4 | `ui/screens/PairOnlyPurgeNoticeTest.kt` | `assertTrue( "this device is still registered for certain -- the command never left the handset -- " + "and the screen stopped saying so once a second ` | `assertTrue( "this device is still registered for certain -- the command never left the handset -- " + "and the screen stopped saying so once a second ` |
| W5.4 | `ui/kit/KitDensityTest.kt` | `gapDivider(context, "records missing")` | `gapDivider(context, "Missing messages")` |
| W5.4 | `ui/kit/KitDensityTest.kt` | `earlierChip(context, "Load earlier messages")` | `earlierChip(context, "Show earlier")` |
| W5.4 | `ui/kit/PressFeedbackAuditTest.kt` | `gapDivider(context, "records missing - repair")` | `gapDivider(context, "Missing messages - Reload")` |
| W5.4 | `ui/kit/PressFeedbackAuditTest.kt` | `earlierChip(context, "Load earlier messages")` | `earlierChip(context, "Show earlier")` |
| W5.4 | `ui/screens/ScreenAirSweepTest.kt` | `notices = listOf("Notifications are blocked for this app."),` | `notices = listOf("Notifications are blocked."),` |
| W5.4 | `ui/screens/ScreenAirSweepTest.kt` | `permissionRedirectLabel = "Open notification settings",` | `permissionRedirectLabel = "Open settings",` |
| W5.4 | `ui/screens/ScreenAirSweepTest.kt` | `outcome = "The machine refused: remote control is disabled.",` | `outcome = "Remote control is off on your computer.",` |
| W5.4 round 2 | `ui/screens/SettingsPanelConnectionTest.kt` | `assertEquals( "the sentence names the computer the switch is off on, and nothing else: the " + "command is the well's text, never the sentence's (phon` | `assertEquals( "the sentence names the computer the switch is off on and ends before the verb, " + "which row 12 draws as its inline mono cell (phone r` |
| W5.4 round 2 | `ui/screens/SettingsPanelConnectionTest.kt` | `row.presenceLine.contains("for 18h"),` | `row.presenceLine.contains("18h"),` |
| W5.4 round 2 | `ui/ConnectionUiStaleNoticeTest.kt` | `@Test fun `it says the thing the reader can act on -- that waiting is enough`() { val stale = StreamView(stream = "journal", stale = true, resyncPendi` | `(removed)` |
| W5.4 round 2 | `ui/ErrorRoutingRefusalCopyTest.kt` | `fun `the sentence names the input line and never the reader`() { val copy = ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY).message asser` | `fun `the sentence opens by saying the message did not go`() { val copy = ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY).message assertTr` |
| W5.4 round 2 | `ui/PairingEntryRoutingTest.kt` | `"That is not a relay address. It looks like wss://host:port -- your machine printed " +` | `"That is not a relay address. It looks like wss://host:port -- your computer printed " +` |
| W5.4 round 2 | `ui/SettingsScreenTest.kt` | `assertTrue( "the PERMANENTLY_DENIED sentence does not name the control beside it, so the words " + "and the button are two separate things a reader ha` | `assertEquals( "the PERMANENTLY_DENIED sentence says the one fact, and the control beside it is the " + "remedy (phone refit W5.4)", "Notifications are` |
| W5.4 round 2 | `ui/SettingsScreenTest.kt` | `assertTrue( "the notice does not name the control beside it, so the two can drift into a " + "sentence describing an action the screen does not offer"` | `assertEquals( "the notice says the one fact, and the control beside it is the remedy (phone refit W5.4)", "Alerts are off in Android.", blocked.delive` |
| W5.4 round 2 | `ui/screens/LaunchPresetScreenTest.kt` | `val copy = LaunchPresetScreen.noticeFor(LaunchAvailability.NO_PRESETS) assertTrue( "the empty-preset state must point at the terminal-side authoring v` | `val command = LaunchPresetScreen.commandFor(LaunchAvailability.NO_PRESETS) assertTrue( "the empty-preset state must point at the terminal-side authori` |
| W5.4 round 2 | `ui/screens/LaunchPresetScreenTest.kt` | `assertTrue("outcome_unknown copy is empty; honest uncertainty still needs a sentence", notice.isNotEmpty()) for (overclaim in listOf("launched", "star` | `assertTrue("outcome_unknown copy is empty; honest uncertainty still needs a sentence", notice.isNotEmpty()) assertTrue("outcome_unknown copy \"$notice` |
| W5.4 round 2 | `ui/screens/PairedMachineRowTest.kt` | `assertTrue( "the confirmation does not say the pairing ends: '$question'", question.contains("pairing", ignoreCase = true), ) assertTrue( "the confirm` | `assertTrue( "the confirmation does not say pairing is what ends: '$question'", question.contains("pair", ignoreCase = true), ) assertTrue( "the confir` |
| W5.4 round 2 | `ui/screens/SessionDetailKillVerdictTest.kt` | `notice.contains("did not end"),` | `notice.contains("Couldn't end"),` |
| W5.4 round 2 | `ui/MachineAndLaunchTest.kt` | `assertTrue("the user is told HOW LONG", line.contains("for 19h")) assertTrue("the relay's word is attributed to the relay", line.contains("relay"))` | `assertTrue("the user is told HOW LONG", line.contains("19h")) assertTrue("the relay's word leads and the phone's own clock follows it", line.startsWit` |
| W5.4 round 2 | `ui/MachineAndLaunchTest.kt` | `assertEquals("Remote control is on. Only the machine can switch it off.", disengaged.killSwitchExplanation) val engaged = pane(killSwitchEngaged = tru` | `assertEquals("Remote control is on. Only your computer can switch it off.", disengaged.killSwitchExplanation) val engaged = pane(killSwitchEngaged = t` |
| W5.2 | `ui/screens/SessionDetailVerdictTest.kt` | `"conversation that goes further back: the sentence has to name where the rest is", notice.contains("machine"),` | `"conversation that goes further back: the sentence has to name where the rest is", notice.contains("computer"),` |
| W5.2 | `ui/screens/SessionDetailVerdictTest.kt` | `"the sentence must be about this conversation, not about a verb or a category", notice.contains("conversation"),` | `"the sentence must say more could not be loaded, not name a verb or a category", notice.startsWith("Couldn't load more"),` |
| W5.2 | `ui/screens/SessionDetailVerdictTest.kt` | `"and report it if it keeps happening\" -- advice for a retry that can never work", notice.contains("no longer"),` | `"and report it if it keeps happening\" -- advice for a retry that can never work", notice.contains("all that's left"),` |
| W5.3 | `ui/screens/TranscriptViewTest.kt` | `listOf( TranscriptTag.SECTION_LABEL to "the heading over the conversation", TranscriptTag.BLOCK to "one interaction item", ).forEach { (tag, what) -> ` | `assertNotNull("the transcript renders nothing for one interaction item", root.kitFind(TranscriptTag.BLOCK)) assertNull( "the conversation draws a head` |
| W5.3 | `ui/screens/TranscriptViewTest.kt` | `TranscriptPanel(heading = "Conversation", blocks = blocks, emptyCopy = "Nothing yet.")` | `TranscriptPanel(blocks = blocks, emptyCopy = "Nothing yet.")` |
| W5.3 | `ui/screens/TranscriptViewDecisionTest.kt` | `TranscriptPanel(heading = "Conversation", blocks = blocks, emptyCopy = "Nothing yet.")` | `TranscriptPanel(blocks = blocks, emptyCopy = "Nothing yet.")` |
| W5.3 | `ui/screens/ScreenAirSweepTest.kt` | `transcript = TranscriptPanel( heading = "CONVERSATION",` | `transcript = TranscriptPanel(` |
| W5.5 | `ui/PairingFlowTest.kt` | `fun `the different-machine state names the cause and says nothing was lost`() { val message = PairingFlow.messageFor(PairingStep.DIFFERENT_MACHINE) as` | `fun `the different-machine state names the cause`() { val message = PairingFlow.messageFor(PairingStep.DIFFERENT_MACHINE) assertTrue( "the message mus` |
| W5.5 | `ui/screens/LaunchPresetRound2Test.kt` | `assertTrue( "outcome_unknown copy \"$notice\" must say a re-confirm is a NEW launch", notice.contains("new launch"),` | `assertTrue( "outcome_unknown copy \"$notice\" must send the reader to check before trying again", notice.contains("before trying again"),` |
| W5.5 | `ui/screens/LaunchPresetRound3Test.kt` | `fun fetchKillSwitchRefusalStillNamesTheSwitch() { val fetch = LaunchPresetScreen.fetchNoticeFor(LaunchDeliveryNotice.KILL_SWITCH).lowercase() assertTr` | `fun fetchKillSwitchRefusalSaysThePresetsCouldNotLoad() { val fetch = LaunchPresetScreen.fetchNoticeFor(LaunchDeliveryNotice.KILL_SWITCH).lowercase() a` |
| W5.5 | `ui/screens/PairOnlyTerminalReasonTest.kt` | `"failure loop PB-APP-10 forbids, reached through the remedy", copy.body.contains(step),` | `"failure loop PB-APP-10 forbids, reached through the remedy. The command is " + "the well's text under the body (phone refit W5.1)", copy.command.cont` |
| W5.5 | `ui/screens/PairOnlyTerminalReasonTest.kt` | `"the repair_required screen never says that pairing is refused until the machine " + "side is cleared, so the order of the two steps is left for the u` | `"the repair_required screen never says the machine side comes FIRST, so the order " + "of the two steps is left for the user to discover by failing at` |
| W5.5 | `ui/screens/PairingGuidanceTest.kt` | `page.cameraNotice.contains("camera"),` | `page.cameraNotice.contains("camera", ignoreCase = true),` |
| W5.5 | `ui/screens/SyncStatusViewTest.kt` | `assertEquals("QUIET 18h", (pill as TextView).text.toString())` | `assertEquals("Last seen 18h", (pill as TextView).text.toString())` |
| W5.5 | `ui/screens/TerminalFallbackViewTest.kt` | `fun `the header names the provider, its detected version, and what is missing`() { val root = view() assertTrue( "the header must name the missing cap` | `fun `the header names the provider, its detected version, and says chat is off`() { val root = view() assertTrue( "the header must say chat is off for` |
| W5.5 | `ui/screens/TerminalFallbackViewTest.kt` | `assertTrue(textOf(root, TerminalFallbackTag.STALENESS).contains("out of date"))` | `assertTrue(textOf(root, TerminalFallbackTag.STALENESS).contains("ago"))` |
| W5.5 | `ui/screens/TerminalFallbackViewTest.kt` | `textOf(view(), TerminalFallbackTag.INTERLEAVING).contains("interleave"),` | `textOf(view(), TerminalFallbackTag.INTERLEAVING).contains("typing"),` |
| W5.5 | `ui/screens/TerminalFallbackViewTest.kt` | `TerminalFallbackModel.controlBanner(0L).contains("Control ended"),` | `TerminalFallbackModel.controlBanner(0L).contains("ended"),` |

## Untouched, byte-identical against `a1507537` (`git diff --stat a1507537..HEAD -- <path>` empty)

`ui/kit/DecisionPillTest.kt`, `ui/kit/MotionTest.kt`, `VerbDispatchRound3Test.kt`,
`PhoneLaunchSurfaceTest.kt`, `SettingsSurfaceReplaceTest.kt`, `ui/kit/KillSwitchPanelTest.kt`,
`ui/screens/NeedVocabularyTest.kt`, `ui/screens/TriageInboxScreenTest.kt`,
`ui/screens/TriageInboxScreen.kt`, `ui/kit/KillSwitchPanel.kt`, `ui/kit/MonoWell.kt`,
`android/app/src/main/res` (whole), `android/gate/w6o3_terminalpaironly_test.go`,
`android/gate/r1_takecontrolgone_test.go`, `android/gate/s23_kit_test.go`, `mobile/`,
`internal/skeleton/`, `internal/daemon/`, `scripts/check-conversation-copy.py`,
`android/app/libs` (the AAR was not rebuilt: W5 touches no exported facade field).

## Gates on the final tree (`3b7662d4`)

```
go build ./...        exit=0
go vet ./...          exit=0
golangci-lint run     0 issues.   exit=0   (fresh GOLANGCI_LINT_CACHE)
go test -race -count=1 -timeout 40m ./...   (SWARM_* unset)   exit=1
    63 packages ok; 2 FAIL:
    android/gate      TestW6O3_TheRemedyIsStatedBeforeTheControlThatNeedsIt   (finding 4, reported, not edited)
    internal/upgrade  TestActivateInstallsAndHandsOffToTheNewBinarysConverge, TestAGoInstallOwnedBinaryRefuses
        one isolated rerun (go test -race ./internal/upgrade/): the same two fail, deterministically;
        W5's diff on internal/upgrade is empty; the same two fail on main's own worktree
        (refit-main at c3148a5f); the installed swarm is a cask (/usr/local/Caskroom/swarm/0.13.5)
        and the tests read the binary's owner -- pre-existing and environmental, not W5's.
    android/gate and internal/verify rerun on the restored tree after the controls: only w6o3 red; verify ok.
Kotlin full suite (lane `full2`): gradle exit=0, tests=1647 failures=0 errors=0 skipped=0, xml fresh 205/205,
    aar unmoved. (Lane `full`, before the W5.5 round: 1647/10, the ten clauses in the table.)
```

## Commits

```
059765b7 Say it in the reader's words, on every screen (W5.4)
6028c7d9 Name the computer where the screen knows it (W5.2)
6b4e12e3 Drop the chrome the words no longer need (W5.3)
d69bbcfa Drawing sheet: Slate palette, per ADR-021 (W5.4)
3b7662d4 Move the clauses the full suite still pinned to old words (W5.5)
```

## Not applied as written, and why

- `MachineAndLaunch.kt:175` "(well:)": row 12's inline mono cell instead (finding 2).
- `PairOnlyScreen.kt:149` `{device}`: not in reach; two-line well (finding 3).
- `{m}` at `SettingsScreen.kt:178`, `LaunchPresetScreen.kt` rows, `MachinesPanelScreen`
  `FORGET_CONFIRM`/`ADD_CONFIRM`, `historyCapacityNotice`: "your computer"/"this computer" (W5.2
  section above); `SettingsScreen.pendingNotice` gained `pendingNoticeFor(machine)` so the panel's
  copy of the sentence names the computer.
- `ConnectionUi.kt:254-255` "Last seen 3 h ago": the age comes from `sinceLastHeard`, shared with
  the Inbox row ("Working · 4m", W7.1) and the sync pill, so it reads `Last seen 3h ago`;
  `TerminalFallbackScreen`'s own literal reads `Updated {n} s ago` as tabled.
- `SessionDetailPanel.kt:515-519` "(mono, lower case)": the words are capitalised; the subtitle's
  `Mono.Meta` appearance is the header kit's and is not a literal, so it stays.
- `SettingsPanel.healthOf`: both arms said the same sentence, so the `when` is an `if`.
- `LaunchPresetScreen.fetchNoticeFor`: all arms say "Couldn't load presets."; the machine's own
  words ride the detail, as before.
- `MachinesPanelScreen.brokenNotice`: the row's `brokenReason` no longer reaches the screen (the
  table names the row instead); `ENTRY_SUBLABEL` and `SessionDetailScreen.COMPOSER_ABSENT` were
  unused constants carrying banned phrases and are deleted.
- `ErrorRouting.STATE_CORRUPT`'s routed sentence still carries its two commands inline: not a
  table row, drawn as a notice with no well; the noun moved to "your computer".
- `PairOnlyScreen`: a revoke notice that also reports a purge failure puts the purge sentence
  between the colon and the well (the notice is one string); an edge case, recorded.

## Rulings landed after the first push (`992f2b00`)

Three orchestrator rulings arrived after the evidence above was committed and pushed; each landed
as its own commit, then the gates were re-run and this addendum written.

### The s24 inventory edit, reverted (`9fcdc6c1`)

The 2-line edit dropped `TranscriptView.kt`'s required `sectionLabel` row from
`android/gate/s24_screens_test.go`'s PB-DS-6 kit-reach inventory -- a composition requirement
W5.3's label removal made false, not a literal the table moved. Per the rule ("anything but a
literal the wave's table moved: revert it and stop on it") it is reverted and stopped on:
`TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit` is red on the branch
(`TranscriptView.kt: does not reach sectionLabel -- the heading over the conversation`) until the
orchestrator rules on the row.

### The w6o3 gate, amended per ruling (a) (`b0d5e40d`)

Changed anchor, guarantee preserved: the gate's guarantee is the ORDER (the remedy is stated
before the control that needs it), not the sentence. `w6o3CopyFaults` reads the four-part
`PairOnlyCopy(title, body, control, command)`; both machine-side steps must be in the command
slot, must not be in the body (W5.1 as a fence), must not be on the control; `w6o3Text` resolves
a `$CONST` template inside a literal so the well's two lines stay spelled once; a new
`w6o3OrderFaults` reads `PairOnlyView.kt` and requires `monoWell(context, copy.command)` between
`PairOnlyTag.BODY` and `PairOnlyTag.CTA`. `TestW6O3_TheCopyScanDiscriminates` cuts the empty
well, the steps in the body, the steps on the control and the three-part copy;
`TestW6O3_TheOrderScanDiscriminates` cuts the well below the control and no well.

RED (the gate before the amendment, from the first `go test -race ./...` run):

```
--- FAIL: TestW6O3_TheRemedyIsStatedBeforeTheControlThatNeedsIt (0.00s)
    w6o3_terminalpaironly_test.go:287: agents-tracker-w6o3: the repair_required screen offers a bare pairing control:
          dev/swarm/phone/ui/screens/PairOnlyScreen.kt: the REPAIR_REQUIRED arm does not compose a three-part PairOnlyCopy, so this fence cannot tell the body from the control
```

GREEN: the five `TestW6O3_*` pass. Negative controls, in place on the tree, each restored with
`git checkout --` (0 dirty after, gate green after):

```
== control 1: the commands back in the body sentence (REPAIR_REQUIRED_CAUSE + " $UNREGISTER_COMMANDS")
    PairOnlyScreen.kt: repair_required puts `swarm remote devices` in the BODY sentence. A command is a well's text and never a sentence's (phone refit W5.1): ...
    PairOnlyScreen.kt: repair_required puts `swarm remote revoke` in the BODY sentence. ...
== control 2: the command slot emptied (command = "")
    PairOnlyScreen.kt: repair_required's well never names `swarm remote devices`. The machine still holds this device's registration and `swarm remote pair` is refused while it does (PB-STATE-10), ...
    PairOnlyScreen.kt: repair_required's well never names `swarm remote revoke`. ...
== control 3: the well drawn below the CTA (the `copy.command` block moved after the ctaButton)
    PairOnlyView.kt: the command's well is drawn outside the body-then-control order (body at 2808, well at 3646, control at 2940). A remedy shown after the control is a decoration, ...
```

`{device}` out of reach (accepted by the ruling): the well carries `swarm remote devices` then
`swarm remote revoke <device-id>`, one per line; recorded as a table row applied differently.

### W5.2 names the machine where the row is known (`86ff83eb`)

Tests first: `MachinesPanelRound2Test` asserts `FORGET_CONFIRM("desk")` == "Forget desk? You can
pair it again later."; `MachinesPanelRound3Test` asserts `ADD_CONFIRM("laptop")` == "Add laptop?
The app reconnects for a moment."; `SessionDetailVerdictTest` asserts
`historyCapacityNotice("MacBookPro")` names it; `SessionDetailHeaderTest` gains `the panel
carries the machine label it was built from`.

RED (lane `w52fu-red`, compile):

```
e: MachinesPanelRound2Test.kt:55:33 Expression 'FORGET_CONFIRM' of type 'String' cannot be invoked as a function.
e: MachinesPanelRound3Test.kt:161:33 Expression 'ADD_CONFIRM' of type 'String' cannot be invoked as a function.
e: SessionDetailHeaderTest.kt:424:59 Unresolved reference 'machineLabel'.
e: SessionDetailVerdictTest.kt:163:55 Too many arguments for 'fun historyCapacityNotice(): String'.
```

GREEN (lane `w52fu-green`): `tests=69 failures=0` over MachinesPanelRound2Test,
MachinesPanelRound3Test, MachinesPanelViewTest, MachinesPanelViewRound3Test,
SessionDetailVerdictTest, SessionDetailHeaderTest, SessionDetailPanelTest; `TestR4D3*` green.

Applied differently, and why: `FORGET_CONFIRM` and `ADD_CONFIRM` are function-typed `val`s
under their old names rather than renamed functions, because `android/gate/r4_d3_round2_test.go`
and `r4_d3_round3_test.go` require the token in `forgetComputer`/`addComputer`'s block and a
`(const )?val FORGET_CONFIRM` declaration that production spends. The forget name is the pressed
row's `displayName` looked up in `machinesDrawn` (the press is on a drawn row; the id is what
the callback carries); the add name is the typed name, or the typed id. `historyCapacityNotice`
names the drawn panel's `machineLabel` only where the drawn panel is the session whose history
hit capacity (`detailDrawn?.takeIf { it.sessionId == target }`), "your computer" otherwise; the
panel gained the field because the label was composed at `PhoneSurface:2939` into a
`SessionDetail` the surface does not retain. `LaunchPresetScreen` stays "your computer" (ruled).

Negative control for the follow-up (lane `w52fu-controls`; `FORGET_CONFIRM` and
`historyCapacityNotice` made to ignore the name; both files restored, 0 dirty):

```
FAILURE MachinesPanelRound2Test > theForgetQuestionNamesWhatItDestroysAndWhatItDoesNot
    org.junit.ComparisonFailure: ... expected:<Forget [desk]? You can pair it ag...> but was:<Forget [this computer]? ...>
FAILURE SessionDetailVerdictTest > the phone says why it stopped offering more history, rather than just stopping
    java.lang.AssertionError: the sentence does not name the computer the rest is on when the screen knows it (W5.2)
```

### Changed assertions and anchors from the rulings

| step | file | before | after |
|---|---|---|---|
| ruling (a) | `android/gate/w6o3_terminalpaironly_test.go` | `w6o3CopyFaults`: three-part `PairOnlyCopy`, both steps required IN the body, forbidden on the control | four-part copy; both steps required in the `command` slot, forbidden in the body and on the control; `w6o3OrderFaults` over `PairOnlyView.kt` (well between BODY and CTA); discriminators for each cut. Guarantee preserved: the remedy is stated before the control. |
| W5.2 follow-up | `ui/screens/MachinesPanelRound2Test.kt` | `"Forget this computer? You can pair it again later.", MachinesPanelScreen.FORGET_CONFIRM` | `"Forget desk? You can pair it again later.", MachinesPanelScreen.FORGET_CONFIRM("desk")` |
| W5.2 follow-up | `ui/screens/MachinesPanelRound3Test.kt` | `"Add this computer? The app reconnects for a moment.", MachinesPanelScreen.ADD_CONFIRM` | `"Add laptop? The app reconnects for a moment.", MachinesPanelScreen.ADD_CONFIRM("laptop")` |
| W5.2 follow-up | `ui/screens/SessionDetailVerdictTest.kt` | (no clause) | `historyCapacityNotice("MacBookPro").contains("on MacBookPro")` |
| W5.2 follow-up | `ui/screens/SessionDetailHeaderTest.kt` | (no test) | `the panel carries the machine label it was built from` |
| s24 (reverted) | `android/gate/s24_screens_test.go` | at `a1507537` | at `a1507537` (the 2-line inventory edit of `6b4e12e3` reverted in `9fcdc6c1`, pending a ruling) |

### Gates on the final tree (`86ff83eb`)

```
go build ./...        exit=0
go vet ./...          exit=0
golangci-lint run     0 issues.   exit=0
Kotlin full suite (lane `full3`): gradle exit=0, tests=1648 failures=0 errors=0 skipped=0, xml fresh 205/205, aar unmoved
go test -race -count=1 -timeout 40m ./...  (run 2, concurrent with the Kotlin suite; 140 stale *.test
    processes from other sessions were resident on the machine)   exit=1
    59 packages ok; FAIL:
    android/gate      TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit only (the s24 revert, pending ruling);
                      TestW6O3_* and TestR4D3* green
    internal/upgrade  the same two as before (pre-existing on main, environmental)
    internal/attach, internal/converge, internal/daemon   the 40m wall clock (2640s each)
    internal/skeleton TestApprove_AStaleOrMismatchedApproveIsRefusedWithACodeAndAppliesNothing/a_rewritten_content_hash
                      (approval_validate_r4_test.go:168: error_code = "invalid_field"; want "stale_approval")
    ONE isolated rerun of those four (go test -race -timeout 25m, lane idle):
        attach ok 4.3s, converge ok 2.3s, daemon ok 61.3s;
        skeleton FAIL on a different test, TestS18_ARevokeCarriesTheSIGNEDTargetAndNotTheSigner
        (s18_sec6_adversarial_test.go:325: gateway service did not stop within 2s of cancel) -- the S18
        timing red the brief lists as known. W5's diff on all five packages is empty, and skeleton was
        ok (457.5s) in the first full run on this branch (`w5-gorace.log`).
```

## Commits (final)

```
059765b7 Say it in the reader's words, on every screen (W5.4)
6028c7d9 Name the computer where the screen knows it (W5.2)
6b4e12e3 Drop the chrome the words no longer need (W5.3)
d69bbcfa Drawing sheet: Slate palette, per ADR-021 (W5.4)
3b7662d4 Move the clauses the full suite still pinned to old words (W5.5)
992f2b00 Record W5's verification evidence
9fcdc6c1 Revert the s24 inventory row edit, pending a ruling
b0d5e40d Amend the w6o3 gate: the remedy is the well, before the control
86ff83eb W5.2 names the machine where the row is known
```
