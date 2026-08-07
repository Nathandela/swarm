# Obsidian phase O6 — the pull-quote sheet, the haptics vocabulary, predictive back

Date: 2026-08-07
Phase: [obsidian-migration-plan.md](../specifications/obsidian-migration-plan.md) O6
Decision of record: [ADR-009](../adr/ADR-009-obsidian-visual-direction.md)

O6 is the phase that adds things a screenshot cannot show. Two of its three items produce no
pixels at all — a vibration leaves no visual trace, and a gesture preview exists only while a
thumb is on the glass — which is why both are fenced by Go gates over source as well as asserted
by Robolectric, and why section 6 below is longer than usual.

---

## 1. The three items, and what each actually changed

### Item 1 — the pull-quote approval sheet

**The whole deliverable is an ordering.** Substrate's `.sheet2` draws an `h4` question first with
the machine and project under it; ADR-009's maquette (frame 2) reverses the two and grows the
question into a pull-quote. Three parts before, three parts after — the plan's own clause is "no
new information, reordered hierarchy". The first assertion in `ApprovalSheetTest` is therefore
about **child order**, because a suite that only checked colours would pass on the old
arrangement.

**`sheetSurface` has a caller for the first time.** O3 built ADR-009 D4.4's recipe and recorded
that it had no screen — *"a recipe waiting for its screen, not a rendered surface"*
([obsidian-o3-evidence.md](obsidian-o3-evidence.md), §6 item 2). That gap is closed.

**One substitution the ladder forces, recorded in the §4 derivation row.** The maquette's question
is 19 px, drawn on a 300 px gallery phone. This app's type ladder is deliberately still
Substrate's (`s22b_designsource_test.go` records why re-pointing nineteen sizes at the maquette is
not a reskin's business), so "larger type at `--p-display-wt`" resolves to `Display.NavTitle` —
the only style in the scale above `Title.Sheet` at that weight. The test asserts both halves: the
style it takes, **and** that it is larger than the 15.5 sp Substrate's `h4` spent.

### Item 2 — the haptics vocabulary

Six signals, in one file, rendered twice from one set of beats:

| Signal | Rhythm | Why that shape |
|---|---|---|
| `NEEDS_YOU` | two CLICKs, 90 ms apart, full scale | the only signal that interrupts rather than confirms |
| `SENT` | one CLICK, 18 ms, 0.7 | answers a finger still on the glass |
| `COMPLETED` | one THUD, 90 ms, 0.5 | finished, nothing asked |
| `FAILED` | two LOW_TICKs, 140 ms apart | slower and lower than NEEDS_YOU: tempo and register, not intensity |
| `SHEET_SETTLE` | one THUD, 60 ms, 0.8 | D4.4's heaviest surface arriving |
| `SCROLL_TICK` | one TICK, 10 ms, 0.3 | per detent, so it has to be faint |

**The composition/waveform split is the DEVICE's, not the API level's.** `minSdk` is 33, so
`VibrationEffect.startComposition` and `VibratorManager` exist on every supported handset and
there is no version branch to write. What varies is whether the actuator implements the
primitives, which `areAllPrimitivesSupported` answers per device. `Rhythm` renders both forms from
the same beats, which is what makes the fallback the *same* signal rather than a different one
that happened to be available.

**The user's switch wins twice.** `HAPTIC_FEEDBACK_ENABLED` is read by the app, because it is the
toggle a person actually touches; and every vibration is attributed `USAGE_TOUCH`, because an
*unclassified* vibration is not what the platform's own touch-feedback suppression governs. Either
alone is a half-measure — the first is one OS release from being wrong, the second is a
suppression this app never verified.

### Item 3 — predictive back

The manifest opts in on `<application>`; `PhoneActivity`'s back callback grew the three progress
members beside the commit it already had; `Motion.predictiveBack` is the frame.

**It scales `contentHost`, not the window.** The tab bar and the status banner are chrome the
gesture is not leaving, so a preview that shrank the window would tell the user they were about to
exit the app — which is what back does on the inbox, and is the confusion this callback exists to
prevent.

**There is no animator, and that is deliberate.** The gesture is the clock; an animator would be a
second one racing the thumb. So the preview is a pure function of progress and the view's own box
— which is also what makes it assertable, because a gesture cannot be replayed in a JVM and a
frame can be asked for.

**`closeSessionDetail` clears the preview, and that is load-bearing.** A committed gesture leaves
`contentHost` at 90% and fully transparent, and the *inbox* is hosted in that same view — so
without it the back gesture succeeds and lands the user on an invisible list.

---

## 2. Gate results

Run from the worktree root, after the last commit of this phase.

```
$ go build ./...                                   clean
$ go vet ./...                                     clean
$ go test ./...                                    all packages ok
$ golangci-lint run                                no output — no findings, new or otherwise
$ (cd android/gate && go test ./...)               ok  github.com/Nathandela/swarm/android/gate  6.177s
$ (cd android && ./gradlew --no-daemon test)       BUILD SUCCESSFUL in 5m 36s
```

**One flake, named rather than hidden.** The first full `go test ./...` of the session reported
`TestFirstPaintGate_RealDaemon_FiftySessions_P95` at 147.2 ms against a 100 ms budget. It is a
wall-clock performance gate and it ran while this machine was still finishing a Gradle build; it
passes on its own (`ok ... 14.450s`) and the whole-tree re-run was clean. Nothing in this phase
touches Go production code — the only Go files it adds are `android/gate/*_test.go` — so the
package is untouched by it either way.

---

## 3. The Android suite, counted rather than inferred

Counted from the testsuite XML files, not from an exit code:

| | before O6 | after O6 |
|---|---|---|
| testsuite XML files | 126 | 130 |
| tests | 1015 | 1044 |
| failures / errors | 0 / 0 | 0 / 0 |

`1015 + 9 + 8 + 7 + 5 = 1044`, and `126 + 4 = 130`. The arithmetic is written out because
"the suite is green" is a claim an exit code makes about a run that may have executed nothing.

The three new suites:

| Suite | tests | what it is for |
|---|---|---|
| `HapticsTest` | 9 | the rhythm table, and the two suppression paths |
| `PredictiveBackTest` | 8 | the frame at rest, mid-drag, at the threshold, cancelled, and under reduced motion |
| `ApprovalSheetTest` | 7 | the ordering first, then the type, the ink and the material |
| `ApprovalSheetPanelTest` | 5 | what the three lines say, and which of them the wire does not carry |

---

## 4. The RED→GREEN ledger

| RED | GREEN |
|---|---|
| `RED O6.2: the plan names six signals and nothing in the app vibrates` | `GREEN O6.2: six rhythms, one construction site, and the user's switch wins` |
| `RED O6.3: the back gesture commits, and shows nothing on the way` | `GREEN O6.3: the drill-down answers the thumb while the thumb is still there` |
| `RED O6.1: the sheet material has had no screen since O3 built it` | `GREEN O6.1: the question is the headline, and the command keeps the well` |

Every RED commit body quotes the failing output. The two Kotlin REDs fail at
`compileDebugUnitTestKotlin` with `Unresolved reference` — missing implementation, which is the
right reason; the two Go REDs fail with the assertion messages the gates carry.

### Three corrections made on the way to green, each recorded rather than smoothed over

1. **`o6Signals`' regexp was unanchored** and read the `S` of `enum class Signal {` as a seventh
   constant, so the gate reported a vocabulary of seven against a plan of six. It is anchored on
   the line now, `guidedControlValue`'s shape.
2. **`HapticsTest`'s composition assertion got stronger, not weaker.** It counted how many
   primitives a Robolectric shadow recorded, which was 0 through the `VibrationAttributes`
   overload; it now compares *both* effects against ones built in the test from `Haptics.RHYTHMS`
   through the platform's own builders. A dropped delay, a scale on the wrong beat or a fallback
   built from other numbers all fail it now, and none of them failed the count.
3. **`PREDICTIVE_BACK_MARGIN_DP = 8f` was refused by `TestPBDS1_NoRawPixelPaddingSurvives`**, and
   the gate was right. The margin is a **length** and the other two numbers are **ratios**: the 2dp
   ladder has an 8dp step, and an 8 typed in Kotlin is that step copied out of the ladder, free to
   drift from it. It is `Motion.predictiveBackMarginPx` over `R.dimen.swarm_space_8` now, and the
   gate's join runs plan → `dimens.xml` → `Motion.kt` in three hops that can each be wrong alone.

---

## 5. What is now fenced that was not

**`android/gate/o6_haptics_test.go` — the new gate the plan asks for**, widened by one word from
its own sentence. The plan says "no `VibrationEffect` constructed outside Haptics.kt"; the fence
refuses the whole vocabulary `(?i)vibrat` — `Vibrator`, `VibratorManager`, `VibrationEffect`,
`VibrationAttributes`, a bare `vibrate(` — because a fence that knows one name is one somebody
walks past by using another. It also refuses `performHapticFeedback` / `HapticFeedbackConstants`
everywhere, the platform's *other* buzz path, which the vocabulary rule cannot reach because it is
not spelled like the hardware.

Its controls:

- **positive**, on synthetic trees: seven plausible ways to raise a buzz elsewhere, each caught;
- **negative**, in memory on a copy of the real source: `SHEET_SETTLE` removed and `SCROLL_TICK`
  renamed, fed to the same reader the real assertion calls;
- **discrimination**: the exemption is a PATH, so `ui/fx/Haptics.kt` — a second vocabulary with
  its own six rhythms and its own answer about the user's setting — is caught, while the real file
  is not;
- **anti-vacuity**: the fence fails loudly if `Haptics.kt` does not exist, because a fence around
  an empty room reports a clean tree.

**`android/gate/o6_predictiveback_test.go`** joins the manifest attribute to the Kotlin that needs
it. Either half alone is silent: without the attribute the callbacks are dead code with a green
test suite, and the attribute cannot see whether anything implements them. Its reader strips XML
comments first — measured, not anticipated: the comment *explaining* the attribute sits above
`<application>`, and the first version found the word in the prose and reported a correctly-placed
attribute as misplaced.

**`s23_kit_test.go`** gained two entries. `Haptics.kt` is *claimed* on Motion.kt's terms (its
numbers are milliseconds and intensities, not surfaces and paddings, so the metric join has
nothing to join to); `approvalSheet` is a full `s23Inbox` row citing the new §4 derivation.

---

## 6. What this evidence does NOT establish

Stated plainly, because a partial recorded as complete is worse than a partial.

1. **No hand-feel pass on device.** The plan's O6 exit asks for one and this session had no
   handset. Everything about the six rhythms is verified as *data* and as *what was asked of the
   vibrator*; whether `COMPLETED`'s soft thud and `SHEET_SETTLE`'s thud are actually
   distinguishable by a thumb is a question only a device answers, and the same goes for whether
   `SCROLL_TICK` at 0.3 is felt at all on a given actuator. **This is the single biggest gap in
   this phase.** It belongs to O7's device protocol.
2. **No device pass on the predictive-back preview either.** Robolectric can be asked what one
   frame looks like; it cannot show the gesture. Whether 90% + a 35% crossfade reads as "you are
   about to leave this session" on a real edge swipe is O7's.
3. **Four of the six signals are wired to nothing, and this is a product gap rather than an
   oversight.** `SENT` and `FAILED` fire in `PhoneSurface.press()` — the one seam where the phone
   has decided what a press means and has not yet asked the machine anything, which is the plan's
   "fired locally on tap, never on server ack". `NEEDS_YOU` fires in `drawInbox` on the same
   promotion set D5's sweep uses. The other three have no event in this app:
   - `COMPLETED` — there is no completed-transition detector. `TriageInboxScreen.promotions`
     computes transitions *into* `needs_input` only, and generalising it is a model change, not a
     wiring one.
   - `SHEET_SETTLE` — the approval sheet has no opener (see item 4).
   - `SCROLL_TICK` — nothing in the app listens to scroll. `phoneScaffoldView` owns the one
     `ScrollView` and reports no detents.
4. **The approval sheet has no caller, and the blocker is the protocol rather than the skin.**
   `mobile/app.go` exports no approve, no deny and no answer verb; `android/unbound-verbs.tsv` has
   no row for one, because there is no facade surface to ledger. The way a blocked session is
   resolved from this phone today is take-control plus send-line. So the composition and the model
   ship, the actions slot is passed nothing in production, and this is the same disposition O3
   gave `sheetSurface` for the same reason. **Two further wire gaps are recorded in
   `ApprovalSheetPanel`'s KDoc**: `Session.Need` is a journal record *type*, not the sentence the
   maquette draws, and nothing carries the literal command (the well shows the daemon-rendered
   snapshot, which is where the command is actually printed).
5. **The predictive-back choreography is partial, by the plan's own permission.** The platform
   also pivots the preview away from `BackEventCompat.swipeEdge` and translates it, and cross-fades
   the *incoming* screen up. This app's nav is one `FrameLayout` whose content is replaced
   (`PhoneSurface.hostContent`), not a stack of two live views — so there is no incoming view on
   screen to fade up, and a translation without one reads as the drill-down sliding off to nowhere.
   Recorded in `Motion.predictiveBack`'s own KDoc, at the call it would go in.
6. **No screenshot diff.** Same reason as items 1 and 2, and the sheet is the only item of the
   three that would appear in one at all.

---

## 7. Independent re-verification (fix pass, same day)

A second session re-ran every gate from scratch against the pushed tree rather than trusting
section 2, on the principle that a green reported by the party that made the change is a claim and
not a measurement. **Every number in section 2 and section 3 reproduced.** What follows is what
the re-run adds.

### 7.1 The gates, re-run

```
$ go build ./...                                   clean
$ go vet ./...                                     clean
$ go test ./...                                    all packages ok (no line outside `ok` / `no test files`)
$ golangci-lint run                                no output
$ (cd android/gate && go test ./...)               ok  github.com/Nathandela/swarm/android/gate  6.864s
$ (cd android && ./gradlew --no-daemon test)       BUILD SUCCESSFUL in 7m 19s
```

The Android suite, counted from the XML rather than from the exit code, and with a staleness check
so that a file left over from the previous run could not be counted as this run's:

| | testDebugUnitTest | testReleaseUnitTest |
|---|---|---|
| testsuite XML files | 130 | 130 |
| tests | 1044 | 1044 |
| failures / errors / skipped | 0 / 0 / 0 | 0 / 0 / 0 |
| files older than this run | 0 | 0 |

Both variants are counted because `./gradlew test` runs both and section 3 counted one — the same
suite, compiled twice, and the release variant is the one a release build would actually ship.

**Section 2's flake did not reproduce.** `TestFirstPaintGate_RealDaemon_FiftySessions_P95` passed
in the whole-tree run, with no Gradle build competing for the machine. That is consistent with what
section 2 said about it (a wall-clock budget measured under load) and is recorded here because a
flake nobody re-checks is indistinguishable from an intermittent defect.

### 7.2 What the re-run needed that nothing wrote down

The Gradle line in section 2 does not run in a clean shell, and this cost a build to find out.
Neither toolchain is on `PATH` on this machine and neither is discoverable from the repository:

```
JAVA_HOME=/usr/local/Cellar/openjdk@21/21.0.12/libexec/openjdk.jdk/Contents/Home
ANDROID_HOME=/usr/local/share/android-commandlinetools
```

There is no `android/local.properties` and no `/Library/Java/JavaVirtualMachines` entry, so a
session that assumes either fails at `:app:testDebugUnitTest` with "SDK location not found" — which
reads like a broken checkout and is a missing environment variable. Recorded here rather than
written into a file, because `local.properties` is machine-local by design and this is the only
place the fact is durable.

### 7.3 The one defect the re-verification found

`Motion.predictiveBackScale`'s KDoc linked `[PREDICTIVE_BACK_MARGIN_DP]`, a constant deleted by
this phase's own third correction (section 4). The link resolved to nothing. Fixed in
`Point the margin's KDoc at the function, not at the constant that was deleted`; the two other
occurrences of that name — `predictiveBackMarginPx`'s KDoc and
`android/gate/o6_predictiveback_test.go` — are prose about the constant's history and are correct
as they stand.

### 7.4 The deferrals in section 6, re-examined rather than re-copied

Each was checked against the source before being carried forward, because a deferral inherited
without checking is how a gap outlives the reason for it.

- **`SHEET_SETTLE`, `COMPLETED`, `SCROLL_TICK` remain unwired, and the events remain absent.**
  `Motion.bottomSheetEnter` and `Motion.pushBannerEnter` are kit recipes with no production caller
  — `grep` finds them in `MotionTest` and nowhere else — so there is no sheet-open animation end
  and no foreground push banner to hang a signal on. `TriageInboxScreen.promotions` still computes
  transitions into `needs_input` only. `phoneScaffoldView` still reports no scroll detents.
- **`NEEDS_YOU`'s call site is the only one available.** Push arrives at
  `SwarmMessagingService.onMessageReceived`, which raises a system notification and touches no
  view; there is no foreground banner path. `drawInbox`'s promotion set is the app's one
  "something just started asking you" event, and it does not re-fire per draw — `promotions` is a
  transition against `inboxDrawn`, which is what the early-return equality check preserves.
- **Predictive back covers the one drill-down there is.** The terminal peek is `peekHost`, a panel
  inside the Inbox tab's own column, not a destination with a back state — so "session detail,
  terminal peek" resolves to one screen in this app, and `detail` is it.
- **Back handling was not destabilised by the manifest opt-in.** `enableOnBackInvokedCallback`
  makes the platform stop dispatching to `Activity.onBackPressed`; nothing in this app overrides
  it, and the two `AlertDialog`s handle their own dismissal. Checked by `grep` over all production
  Kotlin, because this is the failure the opt-in is known for.

**What is still not established is what section 6 says is not established.** No hand-feel pass, no
device pass on the preview, no screenshot diff. Those need a handset and belong to O7.
