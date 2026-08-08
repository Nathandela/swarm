# A1c / A-TRANSCRIPT — the chat transcript screen, in Obsidian

**Workpackage**: A-TRANSCRIPT of the interaction program — slice I1's exit on the handset, "a real
Claude Code session READS AS CHAT".
**Normative**: `docs/adr/ADR-009-structured-chat-interaction.md` (1) — the phone's only session
surface is a structured chat transcript, no terminal emulation and no raw grid anywhere;
`docs/adr/ADR-009-obsidian-visual-direction.md` — how it is drawn;
`docs/specifications/interaction-schema.md` §2–§9 — the item shapes and the IS-\* rules.
**Scope**: three new files under `android/app/src/main/kotlin/dev/swarm/phone/ui/**`, one added
method on `ui/FacadeBridge.kt`, and two new unit-test suites.
**Out of scope, by the workpackage**: the approval SHEET and the terminal well's deletion
(A-SHEET-AND-WELL owns `ApprovalSheetPanel.kt`, `ApprovalSheetView.kt`, `ui/ApprovalItem.kt`, the
`PeekPanel`/`peekHost`/`TerminalPeek` path); `PhoneSurface.kt`'s hosting of this screen, reassigned
to the same agent because it is the same edit region as the peek deletion; `android/gate/`,
including this screen's `s24ScreenComponents` entry; `android/unbound-verbs.tsv`, including the
`App.ReadTranscript` row this screen's wiring retires; every Go package. **Nothing outside the
six files in the table below was edited by this workpackage.**

---

## What was built

| File | What it is |
|---|---|
| `ui/InteractionItem.kt` | The Kotlin shape of `swarmmobile.TranscriptItem`, and the §3 decode. Shared with the approval card. |
| `ui/screens/TranscriptPanel.kt` | `TranscriptScreen.of(items) -> TranscriptPanel`: what each of §3's kinds SAYS. Pure, no Android. |
| `ui/screens/TranscriptView.kt` | `transcriptView(context, panel, onApproval)`: the composition, out of five existing kit factories. |
| `ui/FacadeBridge.kt` | One method, `transcript(sessionId, afterCursor, limit)`, over `App.ReadTranscript`. |
| `ui/screens/TranscriptPanelTest.kt` | The model suite: 21 tests over the kinds and §9's compatibility rules. |
| `ui/screens/TranscriptViewTest.kt` | The view suite: 7 tests over the parts, their order, and the one tap. |

---

## Decisions recorded

1. **Oldest first, which reverses this screen's own previous rule.** `SessionDetailScreen` sorted the
   journal newest-first, and `ActivityPanelScreen` still does, because a log is read from the top. A
   conversation is not a log: newest-first puts the agent's answer above the question it answers.
   `App.ReadTranscript` already walks the fold in ascending cursor order (IS-LAYER-3), so the screen
   keeps the wire's order rather than imposing one — which is also what lets a later page append
   instead of interleave.

2. **The wire's vocabulary is never translated.** `modify`, `in_progress`, `allowed`, `exited`,
   `running` are printed as the machine sent them. `TriageInboxScreen` states the rule for its groups
   and `ApprovalSheetPanel` for its question: a table turning wire tokens into English must fail
   loudly on a value it does not know, and a machine that adds one would take the screen down at the
   moment it is being read (ADR-007 B135's defect class). The screen writes only what no wire field
   exists for — the separators, the heading, the empty copy, the two marks, and the word "You".

3. **Only the user's side is attributed.** An `agent_message` is bare prose with no emphasis; a
   `user_message` carries "You" as its marked span. Prefixing every agent line would label nine rows
   in ten, and `.ln b` is an inline IDENTIFIER — marking a span of an agent's sentence claims one
   where there is none. Nothing on the item carries the agent's NAME, so no block reads "Claude".

4. **A row that would say nothing says what it is.** An item whose payload cannot be read (IS-ENV-3)
   and an item of a kind this build does not know (IS-COMPAT-1) both render one neutral row carrying
   the kind verbatim. `phonecore.ItemStore` already refuses an unknown kind before one reaches
   Kotlin, which is exactly why the screen must not be the second place that assumes so: the cost of
   being wrong is not a missing row but an exception on the looper `PhoneEvents` posts a redraw to on
   every interaction event, which is the process.

5. **`truncated` and `degraded` are marked INSIDE the sentence, not beside it.** A notice qualifies a
   whole section — PB-APP-8's stale line qualifies the transcript, PB-INPUT-1's qualifies what was
   typed — and these two qualify one row each. A per-row notice column would be a component the kit
   does not have and a fourth thing to keep aligned at the 1.3x font scale PB-DS-12 requires.

6. **A resolved approval stays in the transcript and stops being answerable** (IS-LIFE-2). It is part
   of what was said, so removing it would edit the conversation; and the guarantee that every request
   reaches exactly one resolution is bought precisely so a stale card dismisses on every surface.

7. **The approval block is a pointer, not the decision.** ADR-009-obsidian-visual-direction D4.4
   reserves the sheet's material — the one vertical gradient, the 0.22-alpha edge — for "the moment
   of decision". The transcript's job is to say a decision is waiting and get the reader to it, so
   the block is an ordinary row that can be tapped and carries no second copy of the question and no
   buttons. `onApproval = null` draws the block and no control at all, which is
   `navHeaderDrill(back = null)`'s ruling and the defect it was written against (agents-tracker-2yb:
   "the chevron therefore looks like a control and does not act").

8. **No new visual language.** Five factories that already exist do all of the drawing:
   `sectionLabel`, `sessionList`, `activityRow`, `monoWell`, `emptyState`. `monoWell` is called
   WITHOUT `terminal = true` — that variant is the escape-filtered VT snapshot's ink and ADR-009 (1)
   leaves no grid anywhere in the app; what the transcript puts in a mono block is a tool's output
   excerpt and a producer-rendered unified diff, which are column-aligned text a body-copy layout
   would silently re-wrap.

9. **No new update loop.** `PhoneEvents.onEvent` already posts a redraw for every event kind,
   including the `interaction` event `mobile/relay.go` emits, and that event is documented as a WAKE
   rather than a delivery — the body is re-read through `ReadTranscript`. The host therefore re-reads
   the page inside its existing render path and nothing new is invented.

10. **Read from cursor 0, not from the tail.** An item updated in place keeps its FIRST record's
    cursor (IS-LAYER-3), so paging past the tail would miss every update to a streaming message.
    `App.ReadTranscript`'s own doc comment says the same thing from the Go side.

## Recorded absences

- **No timestamp.** `activityRow` has an optional time cell and the item carries the machine's own
  `ts` — the first pairing in this app where the cell has a real value. It is left unspent because
  what a transcript time READS as (absolute, relative, per-turn) is a ruling nobody has made, and
  PB-APP-11's clock rule is the reason to make it deliberately rather than in passing.
- **No turn boundary.** `turn_id` groups items into a turn (IS-ENV-1) and a chat could rule a
  separator off it. There is no divider component in the kit, and adding one is a design decision.
- **No `incomplete from join` elision.** IS-DELTA-4 asks a client holding no earlier record for an
  `in_progress` item to render a leading elision. The core folds items and does not report whether it
  joined mid-item, so there is nothing to key on; drawing it from `status == in_progress` alone would
  elide the front of every message still being streamed.
- **No detail fetch.** IS-CAP-2's "view full output" is an unsigned read this facade does not export.
  A truncated item says it is an excerpt and offers nothing it cannot do.

---

## TDD record

Both suites run under Robolectric. The model suite needs it despite being a pure function over the
item: `TranscriptItem.Body` crosses the gomobile boundary as the item's JSON *as a raw string*
(gomobile binds no map or variant type — `mobile/types.go`), so the per-kind decoding is the
client's, and the client's JSON reader is `org.json`, which the unit-test `android.jar` stubs to
throw on every method.

### RED

Both suites were written before any of the three production files existed. The RED below was
**re-taken** at the end, by moving the three files out of the tree and building again, because
the first run was taken while the concurrent workpackage's tree was mid-edit and failed on
*its* symbols before reaching either suite — which is evidence of nothing.

```
$ mv ui/InteractionItem.kt ui/screens/TranscriptPanel.kt ui/screens/TranscriptView.kt <aside>
$ ./gradlew :app:testDebugUnitTest --tests '…TranscriptPanelTest' --tests '…TranscriptViewTest'

e: …/PhoneSurface.kt:58:35 Unresolved reference 'TranscriptScreen'.
e: …/PhoneSurface.kt:1631:37 Unresolved reference 'stale'.
e: …/PhoneSurface.kt:1637:13 Unresolved reference 'TranscriptScreen'.
e: …/PhoneSurface.kt:1637:38 Unresolved reference 'items'.
e: …/ui/FacadeBridge.kt:106:72 Unresolved reference 'TranscriptPageView'.
e: …/ui/FacadeBridge.kt:108:16 Unresolved reference 'TranscriptPageView'.
e: …/ui/FacadeBridge.kt:109:44 Cannot infer type for type parameter 'R'. Specify it explicitly.
e: …/ui/FacadeBridge.kt:111:17 Unresolved reference 'InteractionItem'.
e: …/ui/screens/SessionDetailPanel.kt:64:21 Unresolved reference 'TranscriptPanel'.
e: …/ui/screens/SessionDetailPanel.kt:244:21 Unresolved reference 'TranscriptPanel'.
e: …/ui/screens/SessionDetailView.kt:222:12 Inapplicable candidate(s): fun addView(p0: View!): Unit
e: …/ui/screens/SessionDetailView.kt:223:9 Unresolved reference 'transcriptView'.
e: …/ui/screens/SessionDetailView.kt:223:63 Cannot infer type for type parameter 'T'. Specify it explicitly.
e: …/ui/screens/SessionDetailView.kt:223:69 Cannot infer type for type parameter 'T'. Specify it explicitly.
e: …/ui/screens/SessionDetailView.kt:223:71 Unresolved reference 'tag'.

BUILD FAILED in 9s
```

**STATED PLAINLY: THIS IS THE MAIN SOURCE SET FAILING, NOT THE TEST SOURCE SET.** Gradle
compiles main before test, so with the screen absent the suites never get the chance to run and
their own `Unresolved reference 'TranscriptScreen'` is never printed. The absence is
demonstrated instead at every **call site**, which is the stronger half of the same fact and
answers the question `TestPBAPP3_TheSessionDetailIsReachedFromTheApp` exists to ask: the host
already composes this screen, so removing it takes the app down rather than leaving a screen
nothing reaches. What the RED does *not* prove is that the suites BITE; the negative controls
below are that proof, and they are why this section is not the whole record.

### GREEN

```
$ ./gradlew :app:testDebugUnitTest --tests '…TranscriptPanelTest' --tests '…TranscriptViewTest'
BUILD SUCCESSFUL in 49s

TEST-dev.swarm.phone.ui.screens.TranscriptPanelTest.xml: tests="21" skipped="0" failures="0" errors="0"
TEST-dev.swarm.phone.ui.screens.TranscriptViewTest.xml:  tests="7"  skipped="0" failures="0" errors="0"
```

**One honest note about how this run was obtained.** Five test files that construct
`SessionDetail` — `FacadeBridgeTest`, `SessionScreensTest`, `SessionStopOfflineTest`,
`SessionDetailPanelTest`, `SessionDetailViewTest` — were still on the pre-ADR-009 shape while
the concurrent workpackage was mid-flight, and Gradle compiles the whole test source set before
running any test. They were **moved aside for the duration of this run and restored byte for
byte** (`git diff` over the five is empty); not a line in any of them was edited, and their
repair belongs to the workpackage that changed `SessionDetail`. The three production files were
verified `diff`-identical to the bytes this run used, before and after the controls below.

### Negative controls

Every assertion in the GREEN run is of the form "nothing was found wrong", and a suite that
understood nothing would report exactly that. Three one-line perturbations of the production
code, applied together and reverted immediately, each targeting a different rule:

| Perturbation | Rule it breaks |
|---|---|
| `items.map` → `items.asReversed().map` | oldest-first (IS-LAYER-3, and the reason a conversation is not a log) |
| `approval = !item.resolved` → `approval = true` | IS-LIFE-2 — a stale card must dismiss on every surface |
| `fields()`'s `try/catch` removed | IS-ENV-3 / IS-COMPAT-2 — an unreadable item must not throw on the looper |

```
TranscriptPanelTest > an answered approval stops being a decision FAILED
TranscriptPanelTest > the conversation is oldest first, in the order it was said FAILED
TranscriptPanelTest > an unreadable body renders as a neutral row and never crashes FAILED
28 tests completed, 3 failed
```

Three perturbations, three failures, each the intended test and no other. The implementation
was then restored from a pre-perturbation copy and re-verified `diff`-identical.

---

## Gates

```
$ go test ./android/gate/
ok  	github.com/Nathandela/swarm/android/gate	5.762s
```

The whole gate suite, green over the new screen. The ones that bear on it directly:

- **`TestPBDS6_TheScreenPackageIsFencedToComponentCallsPlusLayout`** — `TranscriptView.kt` names
  no `R.color`, `R.dimen`, `R.style`, `setTextAppearance`, `setTextColor`, `setPadding`,
  `background =`, `GradientDrawable`, `TypedValue` or `displayMetrics`.
- **`TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit`** / **`TestPBDS11_NoVisualConstantEnters
  TheAppExceptThroughTheTheme`** — no length, colour, radius or typeface enters the app through
  these three files.
- **`TestADR009_NoScreenRendersTheTerminalVariantOfTheWell`** and
  **`TestADR009_TheTerminalWellIsDeletedAtI1Exit`** (the neighbouring workpackage's fences) —
  the transcript reaches `monoWell` with the **default** `terminal = false`, and names none of
  `peekHost`, `peekPanelView`, `PeekPanelScreen`, `PeekPanel`, `TerminalPeek`,
  `.terminalWatch(`, `.terminalUnwatch(`, `.terminalPeek(`.
- **`TestBoundVerbs_TheLedgerCannotExcuseASymbolTheAppNowReaches`** — `App.ReadTranscript` now
  has a production caller, and its `android/unbound-verbs.tsv` row is gone. That row said the Go
  half landed first *on purpose* so "a screen written against a facade that already answers is
  written against the folded model rather than against a literal"; this is the screen it was
  waiting for. (The row was retired by the workpackage that owns that file.)

No Go production code was touched by this workpackage, so `go build` / `go vet` / `gofmt` /
`golangci-lint` have nothing to say about it that they did not say about the commit before.

---

## Open points

1. **The approval tap has a seam and not yet a destination.** `transcriptView`'s `onApproval` is
   nullable and the host passes what it passes; with null the block is drawn and is not
   clickable, so there is no dead control either way. The sheet on the far side of it is
   A-SHEET-AND-WELL's, and the wiring is one argument. Until a real lambda is passed, a user can
   *see* that an approval is waiting and must still answer it at the machine.
2. **No timestamp, no turn boundary, no `incomplete from join` elision, no detail fetch** — the
   four recorded absences above. Each is a ruling nobody has made rather than an oversight, and
   each is cheap to add once made.
3. **`nextCursor` is carried and unread.** The host reads from cursor 0 every render, for
   IS-LAYER-3's reason (an item updated in place keeps its first record's cursor). The field is
   on the page because dropping it is how `JournalPageView` lost the ability to advance; the day
   a transcript grows a "load older" affordance, it is already there. `JournalPageView` is in the
   same position and has been since S16.
4. **`status` is carried and unrendered.** §4's four statuses are on `InteractionItem` and no
   block reads them. A spinner on an `in_progress` `tool_run` is the obvious use and it needs a
   component the kit does not have; IS-ST-2 guarantees the machine closes every open item before
   a terminal `session_status`, so nothing spins forever in the meantime.
5. **The five stale test fixtures** listed under GREEN were red when this workpackage finished,
   from the concurrent `SessionDetail` shape change, and are that workpackage's to repair. The
   two transcript-content assertions inside them — the old journal-type log, newest-first — are
   the ones ADR-009 (1) deliberately changes; the same property is now asserted by
   `TranscriptPanelTest` ("the conversation is oldest first, in the order it was said") and
   `TranscriptViewTest` ("every block is drawn, in the order the panel put them in"), so the
   assertion moved rather than went away. Recorded here so the amendment is not mistaken for a
   weakening.
6. **A real Claude Code session has not been read on a handset by this workpackage.** Every
   assertion here is a unit test over the folded item; the end-to-end proof that the producer's
   items render as chat on a device is `docs/verification/a1c-approve-roundtrip.md`'s rig plus a
   manual run, and slice I1's exit claim should not be signed off without one.
