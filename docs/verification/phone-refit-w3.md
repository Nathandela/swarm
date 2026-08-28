# Phone refit W3: One button (verification evidence)

Bead `agents-tracker-d45a.3`. Contract: `docs/specifications/phone-refit-playbook.md` §4
(W3.1-W3.4). Worktree `refit-w3`, branch `refit/w3`, branched from `main` at 1a0e7b29 (W2
merged). Each item below records the RED run (tests written first, exact failure text), the
GREEN run, and one negative control per behavioural change (the fix perturbed back, the test
shown failing, the file restored).

Environment: go1.26.5 darwin/amd64, golangci-lint 2.12.2 (matches `.github/workflows/ci.yml`).
The Gradle lane is shared by four worktrees; every Kotlin run below went through
`w3-gradle.sh` (waits for `pgrep -f gradle-wrapper.jar` to be empty, records START, runs
`./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache`, then checks every
result XML is newer than START and `app/libs/swarm.aar` did not move).

## What the JVM can and cannot reach, stated first

`PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` on every Robolectric run (the phone
core is a gomobile AAR of `.so` files), so the Activity's own surface never opens a drill-down and
no press on a JVM ever reaches `Press.settle`. Two consequences for this wave's tests:

1. The `PhoneSurfaceControlsTest` cases that need a WORKING session build their own
   `PhoneSurface(activity, PhoneRuntime(activity), VerbDispatch.direct())` (the arrangement
   `SettingsSurfaceReadTest` already uses) and draw a real `SessionDetailScreen.of(...)` panel with an
   open turn through the production draw path. That needs one seam: `PhoneSurface.drawDetail(panel)`
   becomes `internal` (its body is unchanged).
2. W3.4's "not in a toast" is a fact about the settle, which the JVM cannot run. It is pinned the
   way W2.3 pinned its toast (`w23_refusalonce_test.go`): a Go gate over the two production
   functions that declare the press and draw the sealing (`android/gate/w34_onebutton_test.go`),
   plus `SessionScreensTest` for the word. No Kotlin test is written under the contract's name
   for that item, because on the JVM it could not fail.

## RED

### Pass 1 (Kotlin, behavioural): the tests that compile against the tree at 1a0e7b29

`w3-gradle.sh --tests PhoneSurfaceControlsTest --tests SessionScreensTest --tests TranscriptTurnAndAnchorTest`:

```
START=1787896023 (Fri Aug 28 07:47:03 CEST 2026)
PhoneSurfaceControlsTest > the composer region is the bar and its notice FAILED
    java.lang.AssertionError: the pinned region under the conversation holds more than the composer bar and the notice under it, so something is still standing between the transcript and the field -- the full-width Stop the one-button ruling removes. Its children were:
    TextView
    KitStack
    TextView expected:<2> but was:<3>
12 tests completed, 1 failed
BUILD FAILED in 3m 51s
GRADLE_EXIT=1
SUMMARY: xml files=2 stale(older than START)=0 tests=12 failures=1 errors=0
AAR unchanged (mtime 1787894314)
script exit=1
```

`SessionScreensTest.kt` holds `SessionDetailTest` (no class is named after the file), so the
first run never selected it; rerun alone (`w3-gradle.sh --tests dev.swarm.phone.ui.SessionDetailTest`):

```
START=1787899612 (Fri Aug 28 08:46:52 CEST 2026)
SessionDetailTest > the sealed interrupt says Stopped FAILED
    org.junit.ComparisonFailure: expected:<[Stopped]> but was:<[Interrupt sent]>
6 tests completed, 1 failed
BUILD FAILED in 2m 49s
GRADLE_EXIT=1
SUMMARY: xml files=1 stale(older than START)=0 tests=6 failures=1 errors=0
AAR unchanged (mtime 1787894314)
script exit=1
```

W3.1's region test fails on the shipped shape (Stop, bar, notice). The transcript fence
`a turn the daemon opened on the CLI's own envelope reads open from its first tool item` PASSES on
this tree by design -- it is a fence against a tightening, and its RED is taken by perturbation
under the negative controls below.

### Pass 2 (Kotlin, compile): the tests over symbols that do not exist yet

`w3-gradle.sh --tests PhoneSurfaceControlsTest --tests ComposerTest --tests SessionDetailPanelTest --tests SessionDetailMenuTest`
(the filter only trims execution; the RED is the test source set failing to compile):

```
START=1787901036 (Fri Aug 28 09:10:36 CEST 2026)
e: .../PhoneSurfaceControlsTest.kt:15:31 Unresolved reference 'ComposerActionGlyph'.
e: .../PhoneSurfaceControlsTest.kt:240:37 Unresolved reference 'COMPOSER_SEND'.
e: .../PhoneSurfaceControlsTest.kt:245:21 Cannot access 'fun drawDetail(panel: SessionDetailPanel): Unit': it is private in 'dev.swarm.phone.PhoneSurface'.
e: .../PhoneSurfaceControlsTest.kt:249:37 Unresolved reference 'COMPOSER_STOP'.
    (... the same three references at :252-:305, 16 lines)
e: .../ui/kit/ComposerTest.kt:139:22 Unresolved reference 'composerAction'.
e: .../ui/kit/ComposerTest.kt:140:48 Unresolved reference 'COMPOSER_ACTION_DP'.
e: .../ui/kit/ComposerTest.kt:161:22 Unresolved reference 'composerAction'.
e: .../ui/screens/SessionDetailMenuTest.kt:94:92 Unresolved reference 'MENU_STOP'.
e: .../ui/screens/SessionDetailMenuTest.kt:98:52 Unresolved reference 'MENU_STOP'.
e: .../ui/screens/SessionDetailMenuTest.kt:99:42 Unresolved reference 'COMPOSER_STOP'.
e: .../ui/screens/SessionDetailPanelTest.kt:129:69 Unresolved reference 'composerWorking'.
e: .../ui/screens/SessionDetailPanelTest.kt:140:66 Unresolved reference 'composerWorking'.
> Task :app:compileDebugUnitTestKotlin FAILED
BUILD FAILED in 2m 27s
GRADLE_EXIT=1
SUMMARY: xml files=1 stale(older than START)=1 tests=0 failures=0 errors=0
AAR unchanged (mtime 1787894314)
script exit=1
```

### s23 inventory gate (Go), the row for `composerAction` added before the factory exists

```
--- FAIL: TestPBDS6_EveryInboxComponentIsAKitFactory (0.02s)
    s23_kit_test.go:887: PB-DS-6: Composer.kt declares no top-level `fun composerAction(`. The requirement is one factory per visual element; a component that exists only as inline view-building inside a screen is the copy-paste this requirement names.
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	1.490s
```

## GREEN

### Go gates, with the implementation in the tree

`go test -count=1 ./android/gate` -> `ok  	github.com/Nathandela/swarm/android/gate	10.919s` (TestW34, the
re-anchored TestT4LTA, TestR6R2's `app.interrupt(` / `interruptOp = ` / `launchOutcome(interruptOp)`,
TestPBDS6/7 with the `composerAction` inventory row and the `{ action-box }` metric join, PB-DS-12's
new touch-target row, TestResourceXMLIsWellFormed over the two new vectors, s25's plane scan, all green).
Two findings on the first run, both folded in before this result: PB-DS-1 refused a `0` literal in
`composerAction`'s `setPadding` (it now passes the view's own `paddingLeft`/`paddingRight`), and
PB-DS-12 asked for an `s23TouchTargets` row for a file that spends `MIN_TARGET_DP`.

`go build ./...` exit 0, `go vet ./...` exit 0, `golangci-lint run` 0 issues (2.12.2,
`GOLANGCI_LINT_CACHE` under the worktree).

### 4lta, re-anchored (a changed anchor, not a changed assertion)

`TestT4LTA_AnOfflineStopPressSaysSoRatherThanResolvingToNothing` read its subject from the literal
`val stop = actionButton` and `t.Fatal()`s when that is gone. W3.1 deletes `stop` by contract, so
the anchor moves to `private fun interruptPlan(` -- the one plan the square and the menu's Stop row
both press -- and its four assertions (a `StopAction.NOT_SENT` arm, the `stopNotSent` latch, a
`say(`, `NOT_SENT_NOTICE`) are byte-identical. The diff of the gate, in full:

```
-	at := strings.Index(code, "val stop = actionButton")
+	at := strings.Index(code, "private fun interruptPlan(")
 	if at < 0 {
-		t.Fatal("agents-tracker-4lta: PhoneSurface.kt no longer builds its Stop with actionButton, " +
-			"so this fence's subject has changed shape. A fence whose subject silently " +
-			"disappeared reports clean forever")
+		t.Fatal("agents-tracker-4lta: PhoneSurface.kt no longer plans its interrupt in " +
+			"interruptPlan(), so this fence's subject has changed shape. A fence whose subject " +
+			"silently disappeared reports clean forever")
 	}
 	plan, ok := d0b8Balanced(code, at, '{', '}')
 	if !ok {
-		t.Fatal("agents-tracker-4lta: the Stop control passes no plan lambda this fence can read")
+		t.Fatal("agents-tracker-4lta: interruptPlan() has no body this fence can read")
 	}
```
(plus a paragraph in the file's header comment recording the move). The NOT_SENT arm therefore
stays in the square's stop branch, which is also what keeps `SessionDetailPanelTest:132` and the
three `SessionStopOfflineTest` cases untouched.

### W3.4 gate (Go), against the tree at 1a0e7b29

`go test -count=1 ./android/gate -run 'TestW34|TestPBDS6|TestPBDS7'` (the two PB-DS gates rerun
because row 9 of `docs/design/substrate-components.md` gained the `action box 40` cell the new
`KitMetrics.COMPOSER_ACTION_DP` is joined to; both stay green):

```
--- FAIL: TestW34_AStopIsSaidOnceUnderTheComposerAndNeverToasted (0.00s)
    w34_onebutton_test.go:27: PhoneSurface.kt has no interruptPlan: nothing on the surface plans an interrupt (W3.2)
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	1.853s
FAIL
```

The gate was then widened on the orchestrator's ruling (both halves: nothing on the stop path
toasts, AND the word is a `noticeLine(` child the settle adds to `composerRegion` and
`drawComposerRegion` clears). Its RED against the same tree (`go test -count=1 ./android/gate -run TestW34`):

```
--- FAIL: TestW34_AStopIsSaidOnceUnderTheComposerAndNeverToasted (0.01s)
    w34_onebutton_test.go:33: PhoneSurface.kt has no "private val send: ImageView = pressable(composerAction(activity))": the composer's one control is not built as W3.2 specifies
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	1.040s
FAIL
```

## Resumed

The fleet that wrote everything above died on the account's session limit at 10:14 (bead note),
after committing the three commits below at 10:13:56-57 and leaving this file untracked. A
second fleet resumed at 11:54 from the disk state; everything from here on is its work. The
first agent's last Kotlin run (`w3-green1.log`, START 10:07:53, `:app:testDebugUnitTest`
full suite: `tests=1604 failures=0 errors=0`, AAR unchanged) finished at 10:13:11, forty-five
seconds before the commits were made, so it is very likely the committed tree; it is not
relied on -- the full suite is rerun on the committed tree under "Final gates" below.

## Commits (on `main` 1a0e7b29)

| SHA | Item | What it lands |
|---|---|---|
| 00bb0902 | W3.2 (kit) | `composerAction(context)` in `ui/kit/Composer.kt` (40 dp box on the 48 dp floor, level-list glyphs `ComposerActionGlyph`, tinted `--p-ink`); `KitMetrics.COMPOSER_ACTION_DP = 40f`; `swarm_send.xml`, `swarm_stop.xml`; `substrate-components.md` row 9 `action-box 40 (AMENDED 2026-08-28 ...)`; `s23Inbox` and `s23TouchTargets` rows for `composerAction`; `ComposerTest` square + floor cases |
| aa2b04af | W3.2 (fence) | `TranscriptScreen.openTurnOf` comments reworded (`TranscriptPanel.kt:88-93`, `:810-815`); `TranscriptTurnAndAnchorTest` "a turn the daemon opened on the CLI's own envelope reads open from its first tool item" |
| d2477292 | W3.1, W3.2 (surface), W3.3, W3.4 | `PhoneSurface.kt`: `stop`, `stopQuestion`, `stop.text = panel.stopLabel`, `stop.enable` deleted; `composerRegion` adds `composer` + `composerShutDetail` only (KDoc rewritten); `interruptPlan()` with `confirmation = ""`, no `ask`; `send: ImageView = pressable(composerAction(activity))` reading `stopping()` live; `drawComposerAction()` on every region draw and every text change; `stoppedNotice` added by `rememberInterrupt`, removed by `drawComposerRegion`; `MENU_STOP -> press(send, ::interruptPlan)`; `pressable<V : View>`; `drawDetail` internal. `SessionDetailPanel.kt`: `stopLabel`/`stopConfirmation`/`STOP`/`STOP_CONFIRMATION` deleted; `composerWorking`; `MENU_STOP`, `COMPOSER_SEND`, `COMPOSER_STOP`; menu Stop row while `composerWorking`. `SessionScreens.kt`: `stopVisible` deleted; `INTERRUPT_SENT = "Stopped"` with its KDoc. `SessionDetailView.kt`: patch-whitelist comment. Tests and gates listed under "Changed anchors" below; `PhoneScreenDriver` + both `PbE2E2*` smokes press the control by description |

## Contract audit (the second fleet's, against the tree at d2477292)

Every §4 target, read from the tree rather than from the commit messages:

- W3.1: `grep -rn 'stopQuestion|STOP_CONFIRMATION|stopConfirmation|stopVisible|stopLabel' android/app/src/main` finds nothing;
  the only `STOP` identifiers left are `PairingControl.STOP` (a different screen) and `ComposerActionGlyph.STOP`.
  `composerRegion` (`PhoneSurface.kt:728-735`) adds `composer` and `composerShutDetail`; `decisionPillControl`
  is inserted at 0 and `stoppedNotice` at the end, both on the add-and-remove rule. The menu row is a MODEL
  decision (`SessionDetailScreen.menuChoicesFor`, `SessionDetailPanel.kt:514`), first, `destructive = false`;
  `ui/kit/ConversationMenu.kt` renders `MenuChoice` generically and needed no change. Its press
  (`PhoneSurface.kt:3573`) is `press(send, ::interruptPlan)` -- the plan, never the square's click, so a
  draft in the field cannot turn a menu Stop into a send.
- W3.2: `stopping()` (`:540`) = `detailDrawn?.composerWorking == true && typed.text.isBlank()`, read at
  press (`:491`), on every region draw (`:3467`) and on every `afterTextChanged` (`:560-564`).
  `composerWorking` (`SessionDetailPanel.kt:271`) = `transcript.latestTurnId.isNotEmpty()`, derived from
  `transcript`, which `sessionDetailRedraw`'s patch admits (`SessionDetailView.kt:680-682`).
- W3.3: the square passes no `ask` (`:490`); `interruptPlan`'s `Press` carries `confirmation = ""` (`:391`).
  `StopAction.CONFIRM` stays a model arm (`SessionScreens.kt`, `SessionStopOfflineTest.kt:67` untouched).
- W3.4: `stoppedNotice` (`:707-710`, centred `noticeLine()`); `rememberInterrupt` (`:4689-4694`) writes
  `SessionDetail.INTERRUPT_SENT` and adds it to `composerRegion`; `drawComposerRegion` (`:3448`) removes it
  first; no `say(`/`Toast` on either path (w34, both halves).
- Row 9 / s23: `docs/design/substrate-components.md:254` (row 9 only; the diff of that file is one line)
  carries `action-box 40 (AMENDED 2026-08-28, phone refit W3.2, owner ruling: ...)`; `KitMetrics.COMPOSER_ACTION_DP`
  is `derived: ... #9 Composer { action-box }`; `s23Inbox` and `s23TouchTargets` each gain a `composerAction` row.
- `PressFeedbackAuditTest.kt:78-92` audits kit factories' feedback, not `Press.confirmation`; `o6_haptics_test.go`
  has no `confirmation` requirement (`grep -in confirmation` is empty). Neither constrains an empty confirmation.

### Changed anchors and assertions, before / after

| File | Before | After |
|---|---|---|
| `PhoneSurfaceControlsTest.kt:86` (`requiredControls`) | `"Send line" to "\"types\": there is no control that sends a keystroke",` | entry removed with a comment: the control speaks (`COMPOSER_SEND`/`COMPOSER_STOP`) and the surface tests below read the word off the control |
| `PhoneLaunchSurfaceTest.kt:268-278` | prose: "the send control keeps PB-SEC-12 clause 1's touch filter ... The label assertion in `PhoneSurfaceControlsTest` reads that list" | prose: "the control is the bar's 40 dp square since phone refit W3, spoken 'Send' or 'Stop' ... reads the control by its description" (no assertion changed) |
| `SessionScreensTest.kt` | `stop is persistent regardless of what the session has done` (`assertTrue(detail().stopVisible)`) | DELETED with a note naming the ruling and the two tests that carry PB-APP-3's argument now |
| `SessionScreensTest.kt` | (none) | `the sealed interrupt says Stopped` (`assertEquals("Stopped", SessionDetail.INTERRUPT_SENT)`) |
| `SessionDetailMenuTest.kt` | `panel(...)` had no `working` parameter | `working: Boolean = false` sets `turnId = "turn-a"` on the one item; new case `Stop is offered only while the session works` |
| `4lta_offlinestop_test.go:42-50` | anchor `val stop = actionButton`, "passes no plan lambda" | anchor `private fun interruptPlan(`, "has no body"; the four assertions (`StopAction.NOT_SENT`, `stopNotSent`, `say(`, `NOT_SENT_NOTICE`) byte-identical |
| `PbE2E2PairAndTypeTest.kt`, `PbE2E2ResumeTest.kt` | `awaitPressable("Send line", ...)`, `press("Send line")` | `awaitDescribedPressable("Send", ...)`, `pressDescribed("Send")` (`PhoneScreenDriver.kt`, new helpers) |

Untouched, byte-identical to 1a0e7b29 (`git diff --stat 1a0e7b29..HEAD -- <file>` empty): `SessionStopOfflineTest.kt`
(`:48,67,75`), `CtaButtonTest.kt`, `ComposerSendStateTest.kt`, `PressFeedbackAuditTest.kt`, `r6_chat_ui_test.go`,
`resxml_test.go`, `s22b_spacing_test.go`, `o6_haptics_test.go`. `SessionDetailPanelTest.kt`'s
`an offline Stop says it was not sent and does not promise a retry` (was `:132`, now `:170`) is unchanged; only
one test and two helpers were added above it.

## Gates on d2477292 (second fleet, 11:57)

`w3-go-gates.sh` (`PATH` with `$HOME/go/bin`, `GOLANGCI_LINT_CACHE` under the worktree; go1.26.5, golangci-lint 2.12.2 = `.github/workflows/ci.yml:66`):

```
date: Fri Aug 28 11:57:19 CEST 2026  HEAD: d2477292  dirty: 0
build exit=0
vet exit=0
0 issues.
lint exit=0
ok  	github.com/Nathandela/swarm/android/gate	104.867s
gate test exit=0
```

(`android/gate` under `-race -count=1`, the `env -u SWARM_*` prefix; it holds r6 `:270-271`/`:312`, w34, the
re-anchored 4lta, PB-DS-6/7/12, `TestResourceXMLIsWellFormed` over the two vectors, s22b.)

## Negative controls

One per behavioural change. Each is the fix perturbed back on the committed tree, the named test shown failing
with its text, the file restored (`git checkout -- <file>`; `git status` shows only `.lintcache/` and this file
afterwards). Perturbations are applied by `w3-nc.py <set>`, which requires each replacement to match exactly once.

### Go (each `go test -count=1 ./android/gate -run <gate>`, seconds apart, restored between)

**W3.4, half one -- the press must not toast.** `interruptPlan`'s `confirmation = ""` -> `confirmation = SessionDetail.INTERRUPT_SENT`:
```
--- FAIL: TestW34_AStopIsSaidOnceUnderTheComposerAndNeverToasted (0.02s)
    w34_onebutton_test.go:50: interruptPlan still hands SessionDetail.INTERRUPT_SENT to the press, which dispatchPress spends as a toast; W3.4 draws it under the composer instead. Press: (... confirmation = SessionDetail.INTERRUPT_SENT, ...)
    w34_onebutton_test.go:54: interruptPlan's press declares no empty confirmation, so a later reader cannot tell silence chosen from a sentence forgotten (W3.4).
FAIL	github.com/Nathandela/swarm/android/gate	0.929s
```

**W3.4, half two -- the word is a notice line the region holds.** `rememberInterrupt`'s
`if (stoppedNotice.parent == null) composerRegion.addView(stoppedNotice)` deleted:
```
--- FAIL: TestW34_AStopIsSaidOnceUnderTheComposerAndNeverToasted (0.02s)
    w34_onebutton_test.go:74: rememberInterrupt never adds stoppedNotice to composerRegion, so the word is written on a view nothing holds (W3.4). Body: (... stoppedNotice.text = SessionDetail.INTERRUPT_SENT })
FAIL	github.com/Nathandela/swarm/android/gate	0.840s
```

**4lta re-anchor -- the NOT_SENT arm is still read at its new address.** `say(PressFeedback.ofUnsent(SessionDetail.NOT_SENT_NOTICE))` deleted from `interruptPlan`:
```
--- FAIL: TestT4LTA_AnOfflineStopPressSaysSoRatherThanResolvingToNothing (0.01s)
    4lta_offlinestop_test.go:74: agents-tracker-4lta: the Stop plan never calls say(): { ... StopAction.NOT_SENT -> { stopNotSentFor = target; null } ... }
    4lta_offlinestop_test.go:83: agents-tracker-4lta: the Stop plan's answer does not carry SessionDetail.NOT_SENT_NOTICE
FAIL	github.com/Nathandela/swarm/android/gate	0.756s
```

**Row 9 is the number's only origin.** `substrate-components.md:254` `action-box 40` -> `action-box 44`:
```
--- FAIL: TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber (0.02s)
    s23_kit_test.go:2564: PB-DS-7: Kit.kt:405: COMPOSER_ACTION_DP = 40, and `#9 Composer` states action-box 44. The design source never drew this component, so that row is the only place its numbers exist -- a constant that drifts from it drifts from everything.
FAIL	github.com/Nathandela/swarm/android/gate	1.797s
```

### Kotlin (three lane runs over the six W3 classes: `PhoneSurfaceControlsTest`, `SessionDetailMenuTest`, `TranscriptTurnAndAnchorTest`, `ComposerTest`, `SessionDetailTest`, `SessionDetailPanelTest`; `w3-nc-run.sh <set>` applies the set, runs `w3-lane.sh --tests ...`, then `git checkout -- android/app/src/main/kotlin`)

`w3-lane.sh` is the resumed fleet's lane script: it waits on `pgrep -f '^/[^ ]*java .*gradle-wrapper[.]jar'`
(the wrapper JVM only), records START, runs `./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache`,
then counts every result XML older than START as stale and checks `app/libs/swarm.aar`'s mtime.

**NC-1** (`START=1787911278`, Fri Aug 28 12:01:18 CEST 2026). Five perturbations, five named tests, one each;
the other 37 tests in the six classes stay green:

| Change | Perturbation | Failure |
|---|---|---|
| W3.1 the full-width Stop leaves the region | `composerRegion`: `addView(TextView(activity))` before `addView(composer)` | `PhoneSurfaceControlsTest > the composer region is the bar and its notice FAILED` -- "the pinned region under the conversation holds more than the composer bar and the notice under it ... Its children were: TextView KitStack TextView expected:<2> but was:<3>" |
| W3.1 the menu Stop row is on the working fact | `menuChoicesFor`: `if (panel.composerWorking)` dropped (row always offered) | `SessionDetailMenuTest > Stop is offered only while the session works FAILED` -- "an idle session offers Stop, a tap that can only be refused for want of a turn to name" |
| W3.2 fence: a turn opens on any item | `openTurnOf`: `open = item.turnId` -> `if (item.kind == USER_MESSAGE) open = item.turnId` | `TranscriptTurnAndAnchorTest > a turn the daemon opened on the CLI's own envelope reads open from its first tool item FAILED` -- "... draws idle and the one button offers Send where it owes Stop expected:<[turn-c]> but was:<[]>" (the seven `user_message` cases at `:65-115` stay green under the tightening, which is why the fence was needed) |
| W3.3 no confirmation | the square: `pressable(composerAction(activity), ask = { "Interrupt?" })` | `PhoneSurfaceControlsTest > pressing the square while the session works interrupts without asking FAILED` -- "a question stood between the press and the interrupt ... expected null, but was:<androidx.appcompat.app.AlertDialog@74f031b9>" (`confirmThenPress` shows the dialog before `press`'s runtime gate, so the JVM sees it) |
| W3.2 the box is 40 dp | `composerAction`: `minimumWidth = box` -> `minimumWidth = box + room` | `ComposerTest > the composer action is a 40dp square FAILED` -- "the control's box is not 40 dp wide expected:<40> but was:<44>" |

```
42 tests completed, 5 failed
BUILD FAILED in 2m 35s
GRADLE_EXIT=1
SUMMARY: xml files=6 stale(older than START)=0 tests=42 failures=5 errors=0
AAR unchanged (mtime 1787894314)
restored; tracked changes now: 0
```

**NC-2** (`START=1787911587`, Fri Aug 28 12:06:27 CEST 2026). Three perturbations; the other 38 tests stay green:

| Change | Perturbation | Failure |
|---|---|---|
| W3.2 the field is read LIVE (a fast typist's tap never aborts the agent) | `stopping()`: `&& typed.text.isBlank()` dropped | `PhoneSurfaceControlsTest > typing into the field turns Stop back into Send FAILED` -- "there is a draft in the field and the control still says Stop, so a typist who taps after their first word interrupts the agent instead of sending expected:<S[end]> but was:<S[top]>" (the sibling `speaks Send when idle and Stop ...`, which never types, stays green: the two tests pin two different facts) |
| W3.4 the word | `INTERRUPT_SENT = "Stopped"` -> `"Interrupt sent"` | `SessionDetailTest > the sealed interrupt says Stopped FAILED` -- "expected:<[Stopped]> but was:<[Interrupt sent]>" |
| W3.2 the 48 dp floor under the 40 dp box | `composerAction`: `minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)` -> `minimumHeight = box` | `ComposerTest > the square keeps the 48dp touch floor FAILED` -- "the control measures under 48 dp tall, so the square shrank the target with it -- a target is not a size (row 4's own words) expected:<48> but was:<40>"; and the square case's second assertion, "the box between the paddings is not square ... expected:<40> but was:<32>" (the 4 dp of room each side now eats the box) |

```
42 tests completed, 4 failed
BUILD FAILED in 3m 4s
GRADLE_EXIT=1
SUMMARY: xml files=6 stale(older than START)=0 tests=42 failures=4 errors=0
AAR unchanged (mtime 1787894314)
restored; tracked changes now: 0
```

**NC-3** (`START=1787911973`, Fri Aug 28 12:12:53 CEST 2026). One perturbation, and the cascade IS the point
(tbpm.4's hazard closed at the model): `SessionDetailPanel.composerWorking` inverted
(`transcript.latestTurnId.isNotEmpty()` -> `.isEmpty()`). Its own test fails first, and the menu row and every
surface test that reads the fact fail with it; nothing else in the six classes moves:

```
SessionDetailPanelTest > composerWorking is the open turn, the same fact the header reads FAILED
    java.lang.AssertionError: an open turn does not read as working
SessionDetailMenuTest > Stop is offered only while the session works FAILED
    java.lang.AssertionError: an idle session offers Stop, a tap that can only be refused for want of a turn to name
PhoneSurfaceControlsTest > the one composer control speaks Send when idle and Stop when the session works and the field is empty FAILED
    org.junit.ComparisonFailure: the session works and the field is empty, and the control still says Send: the one thing a reader can do to a working agent from here is stop it expected:<S[top]> but was:<S[end]>
PhoneSurfaceControlsTest > typing into the field turns Stop back into Send FAILED
    org.junit.ComparisonFailure: expected:<S[top]> but was:<S[end]>
PhoneSurfaceControlsTest > pressing the square while the session works interrupts without asking FAILED
    org.junit.ComparisonFailure: expected:<S[top]> but was:<S[end]>
42 tests completed, 5 failed
BUILD FAILED in 3m 3s
GRADLE_EXIT=1
SUMMARY: xml files=6 stale(older than START)=0 tests=42 failures=5 errors=0
AAR unchanged (mtime 1787894314)
restored; tracked changes now: 0
```

Negative-control ledger, change -> control: W3.1 region -> NC-1; W3.1 menu row -> NC-1 (and NC-3); W3.2 square
predicate, live field -> NC-2; W3.2 `composerWorking` one source -> NC-3; W3.2 fence -> NC-1; W3.2 40 dp box and
48 dp floor -> NC-1 and NC-2; W3.3 no confirmation -> NC-1; W3.4 word -> NC-2; W3.4 notice line, not a toast ->
Go w34 (both halves); 4lta re-anchor -> Go; row 9 origin -> Go PB-DS-7.

## Final gates (committed tree d2477292, second fleet)

Go, 11:57 (`w3-go-gates.sh`, output under "Gates on d2477292" above): `go build ./...` 0, `go vet ./...` 0,
`golangci-lint run` 0 issues, `android/gate` ok under `-race`.

Go full suite, 12:16 (`w3-final-go.sh`: the `env -u SWARM_*` prefix, `go test -race -count=1 -timeout 40m ./...`,
load 8.09 at start with the Kotlin suite compiling alongside): 62 packages ok, 7 with no
test files, two FAIL, both timing assertions in packages this wave does not touch:

```
--- FAIL: TestE2E_ReplayProductionPath_AgyOpencode (10.72s)
    replay_e2e_test.go:403: agy: idle observed only 2.769920708s after the first active sample (want >= 3s ...)
FAIL	github.com/Nathandela/swarm/internal/e2e	36.641s
--- FAIL: TestS18_ARevokeCarriesTheSIGNEDTargetAndNotTheSigner (2.90s)
    s18_sec6_adversarial_test.go:325: gateway service did not stop within 2s of cancel
FAIL	github.com/Nathandela/swarm/internal/skeleton	481.024s
```

Rerun once in isolation (§1.6), 12:29, after the Kotlin suite had released the machine (load 6.26):

```
ok  	github.com/Nathandela/swarm/internal/e2e	27.210s
ok  	github.com/Nathandela/swarm/internal/skeleton	430.658s
rerun exit=0
```

Kotlin full suite, 12:21 (`w3-final-kotlin.sh` -> `w3-lane.sh` with no filter: the gate's exact
`./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache`):

```
HEAD: d2477292  tracked changes: 0
START=1787912471 (Fri Aug 28 12:21:11 CEST 2026)
BUILD SUCCESSFUL in 7m 45s
GRADLE_EXIT=0
SUMMARY: xml files=202 stale(older than START)=0 tests=1604 failures=0 errors=0
AAR unchanged (mtime 1787894314)
```

The tests this wave added or changed, all in that run: `PhoneSurfaceControlsTest` 7 (the four W3 cases and the
three ledger cases), `ComposerTest` 6, `SessionDetailMenuTest` 5, `SessionDetailPanelTest` 11, `SessionDetailTest` 5,
`TranscriptTurnAndAnchorTest` 8.

## Done when

- One control sends and stops: `app.composerSend(` and `app.interrupt(` are both reachable from `send`'s plan
  (r6 `:270-271` reads both verbs, both latches, both `launchOutcome`s; w34 reads `interruptPlan()` in the square's body).
- A fast typist's tap never aborts the agent: `typing into the field turns Stop back into Send`, RED under NC-2.
- No question before Stop: `pressing the square while the session works interrupts without asking`, RED under NC-1.
- "Stopped" once, under the composer, never a toast: w34 both halves (RED under go-a, go-b); `INTERRUPT_SENT == "Stopped"`
  (RED under NC-2).
- Stop stays reachable with a draft in the field: the menu row, `Stop is offered only while the session works`
  (RED under NC-1 and NC-3), pressing `interruptPlan()` directly.
- Negative controls: `SessionStopOfflineTest.kt`, `SessionDetailPanelTest`'s offline case, `CtaButtonTest.kt`,
  `ComposerSendStateTest.kt` untouched and green in the same run.

Branch `refit/w3`: 00bb0902, aa2b04af, d2477292, plus this file's own commit. Not merged, not rebased (the
orchestrator rebases at merge; `main` has moved since 1a0e7b29), bead left open for the orchestrator.
