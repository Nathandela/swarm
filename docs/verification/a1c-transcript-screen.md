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

---
---

# Appendix — INTEGRATION: the AAR rebuild, and joining the screen to the facade's own bytes

**Workpackage**: INTEGRATION, run after A-TRANSCRIPT, A-SHEET-AND-WELL and W-APPROVE had all
landed in this worktree.
**Its three jobs**: (1) rebuild the gomobile AAR so the Kotlin layer can see the new facade verbs;
(2) make the app compile and its unit tests pass against that AAR, fixing any signature mismatch
in favour of the facade; (3) write the integration test that proves slice I1 exit as far as this
environment allows.
**Files added**: `internal/skeleton/interaction_screen_golden_test.go`,
`internal/skeleton/testdata/i1-transcript-screen.golden.json`,
`android/gate/i1_screengolden_test.go`,
`android/app/src/test/kotlin/dev/swarm/phone/ui/screens/TranscriptScreenGoldenTest.kt`.
**File edited**: `android/app/build.gradle.kts` (one staging entry). No production code, Go or
Kotlin, was changed — see §6, which is itself a result.

---

## 1. The AAR was rebuilt, and the toolchain is present after all

**ONE** of the three preceding workpackages recorded the AAR as unbuildable here — A-SHEET-AND-WELL,
in those words: "no Java runtime on this session's PATH and no Android SDK on the machine". **That
was a PATH artefact, not a missing toolchain.** `android/toolchain.env` exists precisely because
"nothing Android is discoverable on this host by default"; sourcing it resolves everything:

> **Narrowed 2026-08-08 from "The three preceding workpackages all recorded..." at A-TRANSCRIPT's
> request, and checked against all three files rather than taken on report.** A-TRANSCRIPT never made
> the claim: it BUILT the AAR (`android/app/libs/` directory mtime 12:43 is that first write,
> arm64-v8a + x86_64, before it compiled anything — the file mtimes inside are this integration run's
> 13:40 overwrite), and reached it by passing `JAVA_HOME` and `ANDROID_HOME` inline on every gradle
> call rather than sourcing `toolchain.env`. Same fix, worse ergonomics; sourcing the file is the
> better habit. W-APPROVE made a DIFFERENT and correct claim — that the AAR is a gitignored local
> artifact so "the Kotlin binding does not exist until someone reruns that script" — which is about a
> missing artifact, not a missing toolchain, and is not corrected by this section. The error was
> A-SHEET-AND-WELL's alone; it is also corrected at the head of that file's own §6.

```
gomobile: /Users/Nathan/go/bin/gomobile
gobind:   /Users/Nathan/go/bin/gobind
java:     /usr/local/opt/openjdk@21/bin/java
javac:    /usr/local/opt/openjdk@21/bin/javac
go:       /usr/local/bin/go
ANDROID_HOME=/usr/local/share/android-commandlinetools               SDK platforms: present
ANDROID_NDK_HOME=.../ndk/27.2.12479018                               NDK dir: present
```

`android/build-aar.sh` then runs unmodified, exit 0, both pinned ABIs:

```
$ sh android/build-aar.sh
building .../android/app/libs/swarm.aar for arm64-v8a x86_64 (androidapi 21)
$ ls -la android/app/libs/
-rw-r--r--  1 Nathan  staff     56895 Aug  8 13:40 swarm-sources.jar
-rw-r--r--  1 Nathan  staff  11891471 Aug  8 13:40 swarm.aar
```

All three verbs the app half was written against are now bound, with the arity and types the
Kotlin passes:

```
$ unzip -p android/app/libs/swarm-sources.jar swarmmobile/App.java | grep -Ei 'approv|transcript'
73:  public native Op approve(String session, String itemID, String decisionID) throws Exception;
394: public native TranscriptPage pendingApprovals() throws Exception;
458: public native TranscriptPage readTranscript(String session, long from, long limit) throws Exception;
```

Go `int` binds as Java `long`, which is why `readTranscript`'s `limit int` appears as `long` and
why `FacadeBridge.transcript(..., limit: Long)` is correct rather than a mismatch.

---

## 2. No signature mismatch existed — and a real compiler said so

The task anticipated fixing drift between what the UI called and what the facade exports. **There
was none.** Two independent checks, in increasing strength.

**A mechanical name-and-arity scan** of every `app.*` call site in production Kotlin against the
rebuilt `App.java`:

```
App bound methods (53): approve=True readTranscript=True pendingApprovals=True
unresolved call sites: 0
```

(The scan also flagged eight `item.optString(`/`item.optJSONObject(`-style hits; all are
`org.json.JSONObject` locals inside `ApprovalItem.kt`/`InteractionItem.kt`, not the bound
`TranscriptItem`. Recorded so the number is not read as eight real defects.)

**The Kotlin compiler**, from the `:app:testDebugUnitTest` invocation described in §5:

```
> Task :app:compileDebugKotlin FROM-CACHE
> Task :app:compileDebugUnitTestKotlin
> Task :app:processDebugUnitTestJavaRes
> Task :app:testDebugUnitTest
```

`compileDebugUnitTestKotlin` **completed**. The whole module — production Kotlin plus the entire
test source set, including the suites A-SHEET-AND-WELL had left mid-amendment and the new suite
below — compiles against the rebuilt AAR. That closes the largest unknown the three preceding
workpackages left, and closes it with a compiler rather than a source scan. What it does not do is
run the assertions; see §5.

---

## 3. The integration test, and the gap it actually closes

Slice I1's exit is "a real Claude Code session reads as chat, and the approval in it can be
answered". Two thirds of that was already proven **in Go**: `TestClaudeChainE2E_...` shows the real
producer's items reach the phone, and `TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval`
shows `swarmmobile.App.Approve` answers one over the whole shipped stack. Both stop at the facade.

**Everything above the facade was asserted against itself.** `TranscriptPanelTest` decides what a
`tool_run` reads as from a body its own author typed; `ApprovalSheetPanelTest` labels its buttons
from a `decisions[]` its own author typed. Nothing joined either to what the adapter and the
producer actually emit. The gap is structural rather than an oversight: `swarmmobile.App` is a
gomobile class over Android-ABI `.so` files, so **no Robolectric test can construct the facade** —
which is why `FacadeBridge` lifts its pure halves out of the instance in the first place.

So the crossing is **recorded**, and the two halves are joined by the recording.

### 3a. `internal/skeleton/interaction_screen_golden_test.go` — the recorder

Reuses the existing S19 rig, so nothing is faked: real recorded corpus → real
`internal/adapter/claude` → real producer → **separate gateway process** → real relay → real
durable phone core → real bound `App.ReadTranscript` / `App.PendingApprovals`. It then taps
`App.Approve` **with the id the card itself offered** (read off `decisions[]`, never a literal — a
hardcoded `"allow"` would pass while the app sent a token the CLI never offered), waits for the
resolution to fold, and pins both sides of it.

RED, verbatim, before the golden existed:

```
$ go test ./internal/skeleton/ -run TestI1_TheScreensBytesAreTheFacadesBytes -count=1 -v
=== RUN   TestI1_TheScreensBytesAreTheFacadesBytes
    interaction_screen_golden_test.go:187: no pinned crossing at
    testdata/i1-transcript-screen.golden.json: open testdata/i1-transcript-screen.golden.json:
    no such file or directory. The Android suite renders this file; without it the screen is
    asserted only against fixtures written on its own side
--- FAIL: TestI1_TheScreensBytesAreTheFacadesBytes (8.03s)
FAIL	github.com/Nathandela/swarm/internal/skeleton	9.019s
```

GREEN, and — the property that makes the golden worth having — **byte-stable across two
independent live runs of the full rig**:

```
$ go test ./internal/skeleton/ -run TestI1_... -count=1 -update-screen-golden
ok  	github.com/Nathandela/swarm/internal/skeleton	9.744s
$ go test ./internal/skeleton/ -run TestI1_... -count=1     # fresh rig, fresh ULIDs, diffed
ok  	github.com/Nathandela/swarm/internal/skeleton	9.422s
```

Stability took two passes to earn. The first golden leaked two per-run values inside the
`approval_resolved` body — `interaction_id` (the request's real ULID) and `operation_id` (the
phone's own per-tap mint) — which the first normalizer missed because it only walked the envelope.
Both are now pinned, and `interaction_id` is **mapped through the same numbering rather than
blanked**, so the golden shows the resolution pointing at the card above it, which is the
relationship IS-LIFE-2 is about:

```
"Body": "{\"by\":\"phone\",\"decision\":\"allowed\",\"interaction_id\":\"item-2\",
          \"item_id\":\"item-7\",\"kind\":\"approval_resolved\",
          \"operation_id\":\"00000000000000000000000000000000\", ...}"
```

**What is normalized, and why it weakens nothing.** Six values differ per run: the session id, the
item and turn ULIDs, the cursors, the capture instants, and the approval's ADR-007 D7 binding
tuple (`content_hash`, `expires_at`, `agent_instance`). The Android decode reads **none** of them —
between them `InteractionItem.fields()` and `ApprovalItem.of` read `tool`, `action`,
`output_excerpt`, `truncation_marker`, `change`, `path`, `old_path`, `added`, `removed`,
`diff_excerpt`, `steps`, `decision`, `by`, `process`, `turn`, `interaction`, `note`, `summary`,
`decisions`, `mode` and `prompt_lines`. Every key is **kept** and only the unstable values are held
still, so the golden is the real body with the clock stopped.

The recorder also asserts the screen's read-set **where it is produced**, which the Android side
structurally cannot: the app's decode is total by construction (IS-COMPAT-1/-2 — an unreadable body
costs a row and nothing else), so a producer that stopped sending `body` would render six neutral
rows saying only their kind **and every Robolectric assertion would still pass**.

### 3b. What the recording turned out to contain

Two facts worth the record, because both would have been guessed wrong:

- **The real dialog's buttons are `Yes` and `No`**, ids `allow`/`deny` — not the maquette's
  `Allow`/`Deny`. A phone-side table over that vocabulary would have mislabelled the one surface in
  the app that must not editorialise. §3.5 keeping the ids the CLI's own is doing real work.
- **The approval sits third from the top, above the two tool runs it was blocking.** The recorded
  turn raised the permission at `PreToolUse`, so the fold's ascending-cursor order puts the question
  before the work. `TranscriptScreen` keeping the wire's order is what makes the phone show the
  conversation the machine actually had.

### 3c. `TranscriptScreenGoldenTest.kt` — the renderer

Six tests over the recorded bytes: the whole turn as one ordered list (one assertion, not six —
reading is sequential, and a per-row assertion passes over a shuffled transcript); the two mono
wells and the absence of a third where the `Edit` run carried no `output_excerpt`; the approval row
being the tappable one and reporting the id the facade accepted; the sheet's question, literal and
`Yes`/`No` buttons with the pressed id compared against the golden's own `decision_id`; the
answered card staying in the conversation while ceasing to be a decision, with `allowed · phone` as
the last row; and the card model carrying **no part of the binding tuple** — asserted after first
asserting the recording still contains all three, so the check cannot go vacuous.

### 3d. `android/gate/i1_screengolden_test.go` — the join that stops the recording going vacuous

Because Robolectric cannot construct the facade, the suite builds its `InteractionItem`s **by hand**
from the golden. That hand mapping is a second spelling of `FacadeBridge.transcript`, and nothing in
either toolchain compares them — so a getter dropped from the bridge would leave the screen suite
green over a field the app no longer reads. This is PB-APP-8's repair channels and PB-PAIR-5's
terminal states again, and the remedy is the same one
`android/gate/pbapp8_repairchannels_test.go` uses: set-compare the two spellings, read from source,
fail in either direction.

**Proved non-vacuous by mutation.** Replacing `resolved = o.getBoolean("Resolved")` with
`resolved = false` in the suite:

```
--- FAIL: TestI1_TheScreenSuiteReadsTheFieldsTheBridgeReads (0.01s)
    slice I1: android/.../FacadeBridge.kt reads [Resolved] off the facade and the recorded-bytes
    suite renders none of them. A field the app reads in production and the golden suite does not
    is a field whose rendering is asserted nowhere, which is what the recording was written to end.
      bridge: [Body Cursor Degraded ItemID Kind Resolved SessionID Status Text Truncated]
      suite:  [Body Cursor Degraded ItemID Kind SessionID Status Text Truncated]
```

Both sets are the real ten fields, so the comparison measures what it claims to. The mutation was
reverted and the gate re-run green.

---

## 4. Gates

```
$ go build ./...                                          BUILD OK
$ go vet ./internal/skeleton/ ./android/gate/ ./mobile/    VET OK
$ gofmt -l internal/skeleton/interaction_screen_golden_test.go android/gate/i1_screengolden_test.go
                                                          (empty — both clean)
$ go test ./android/gate/ -count=1
ok  	github.com/Nathandela/swarm/android/gate	7.440s
```

The **whole** `android/gate` suite is green with the two new join tests in it. `gofmt -l` over the
three touched packages does list six files — `internal/skeleton/revoke_reaudit_test.go` and
`android/gate/{il7u_tokenrevert,o6_haptics,o6_predictiveback,pbapp11_freshness,w6o3_terminalpaironly}_test.go`
— **none touched by this workpackage**; they are pre-existing, and are named here so the count is
not mistaken for new debt.

Confirmed empirically on the way past: `i1_sheetandwell_test.go`'s "a screen must not parse JSON"
fence is scoped to the main source set, so the new suite's use of `org.json` does not trip it.

---

## 5. NOT VERIFIED BY THIS WORKPACKAGE: the Robolectric assertions

**No Robolectric result is claimed here, and §2's compile signal must not be read as one.** The run
that produced it was killed at the test phase, by another agent, before a single assertion ran:

```
> Task :app:testDebugUnitTest
Daemon is stopping immediately stop command received
FAILURE: Gradle build daemon has been stopped: stop command received
gradle exit status: 1
MISSING app/build/test-results/testDebugUnitTest
```

The coordinator then took the gradle lane exclusively and this workpackage stood down from it. The
`:app:testDebugUnitTest` numbers belong in that agent's record and are not invented here. **A green
claim requires a nonzero test count read out of the JUnit XML**, per the house rule that
`./gradlew | tail` exits 0 when Gradle exits 1.

### Two serialization traps, recorded because both cost real time

1. **`scripts/o2-gradle-run.sh` issues `./gradlew --stop` as its FIRST action.** With two or more
   agents driving it, every new run kills the one in progress, and the steady state is that nobody
   ever completes one. It also leaves `test-results/` **absent**, which looks like a build that ran
   nothing rather than one that was shot. This is what killed the run in §2.
2. **`until ! pgrep -f "gradle-wrapper.jar"; do sleep 10; done` never exits.** The waiting bash
   process's own command line contains the pattern, so `pgrep -f` matches the waiter itself. Two
   agents sat in it for 15+ minutes. It is the same trap that script's own header documents for
   `pgrep -x java` ("an IDLE Gradle daemon is also a java process"), in a new disguise. Match
   `org.gradle.launcher.daemon` instead.

---

## 6. What was NOT needed, which is itself a result

- **No production code changed, Go or Kotlin.** The instruction was to fix the app side to the
  facade on any disagreement. There was no disagreement: the app half had been written against the
  Go half's real signatures, and they met.
- **`android/unbound-verbs.tsv` needed no edit.** `App.Approve`'s row was already retired by
  A-SHEET-AND-WELL wiring the sheet's buttons to it, and `App.ReadTranscript`'s by A-TRANSCRIPT.
- **The five Kotlin suites A-SHEET-AND-WELL handed over as non-compiling now compile** — repaired
  by the parallel agent before this workpackage took the lane, and confirmed by
  `compileDebugUnitTestKotlin` completing over the whole test source set.

---

## 7. Open points

1. **The Robolectric suite has not been run by this workpackage** (§5). Everything above the facade
   is written and compiles; whether the assertions pass is the coordinator's run to report. The most
   likely red is a copy expectation of mine against `TranscriptScreen`'s joins rather than a facade
   problem, since the golden side is pinned by a live run of the real rig.
2. **`policyTestResources` must actually re-run for the new suite to see its recording.** The
   staging entry for `i1-transcript-screen.golden.json` is new; a cached `UP-TO-DATE` from before
   the edit would leave the file off the classpath. The suite then fails **loudly** ("is not on the
   unit-test classpath") rather than passing over nothing, on `Pin`'s precedent — so this cannot go
   silently wrong, but it can waste a triage.
3. **A HANDSET IS STILL NOT PROVEN, and this appendix does not move that.** Robolectric is a JVM
   sandbox: no glass, no compositor, no real `.so`. Slice I1's exit claim as written — a real session
   reads as chat *on a device* — remains PB-E2E-5's deferred physical gate. What is closed is
   narrower and was the actual hole: the screen is no longer asserted only against fixtures written
   on its own side.
4. **The golden is one turn, and it covers six of eight kinds.** `plan_update` and `session_status`
   have no recorded corpus behind them, so their rows are still asserted only against hand-written
   bodies. The recorder fails loudly if the corpus stops producing any of the five kinds it does
   cover, so coverage cannot decay silently — but widening it needs a new recording, not a new
   fixture.
5. **The recorded corpus carries an absolute path** (`/Users/Nathan/spike-sb-work/...`), stable
   because it is *recorded* rather than because this machine has that directory. It reaches the
   golden and therefore the Kotlin assertions verbatim. Worth knowing before anyone re-records the
   corpus on a different machine.

---

# Appendix — INTEGRATION run 2: the forced compile, and the first Robolectric assertions to execute

**Workpackage**: INTEGRATION (second agent on the same three jobs), run after the appendix above was
written. It **added no test and changed no production code**; its whole output is verification, plus
one deletion of its own duplicate work (§C).

This appendix exists to close §5 above, which correctly refused to claim a Robolectric result.

---

## A. The AAR rebuild, independently confirmed

Confirmed rather than restated: `android/build-aar.sh` runs unmodified in this environment once
`android/toolchain.env` is sourced, exit 0, both pinned ABIs. The artifact and its three verbs:

```
AAR_MTIME = 2026-08-08 13:40:29        android/app/libs/swarm.aar   (11 891 471 B)

$ unzip -p android/app/libs/swarm-sources.jar swarmmobile/App.java | grep -Ei 'approv|transcript'
 73: public native Op approve(String session, String itemID, String decisionID) throws Exception;
394: public native TranscriptPage pendingApprovals() throws Exception;
458: public native TranscriptPage readTranscript(String session, long from, long limit) throws Exception;
```

The three preceding workpackages each recorded the toolchain as absent. It is present; the header of
`android/toolchain.env` predicts that exact misreading and says it "has already cost three readers".
It has now cost five.

---

## B. THE COMPILE, FORCED — the evidence the Go source scans structurally cannot give

Every Android fence in this slice is a Go scan over Kotlin **source text**, so none of them can see a
type error inside a lambda, and none can see a facade signature that moved. Only a compiler can. The
difficulty is that gradle kept declining to be one:

| run | what it said about the compile | worth as evidence |
|---|---|---|
| 1 | `compileDebugKotlin FAILED` | measured another agent's uncommitted negative-control mutations, not the slice |
| 2 | `compileDebugKotlin UP-TO-DATE` | **none** — gradle saying it did not do the work |
| 3 | `--rerun-tasks` | **none** — dies in AGP's resource compiler on nine locales before any Kotlin is read |
| 4 | `compileDebugUnitTestKotlin FROM-CACHE` | **none** — a cache hit is not a compilation |

`UP-TO-DATE` and `FROM-CACHE` are the quiet version of the false green: the build succeeds and
nothing was checked. Forcing execution needs the output **deleted** and the cache **off** —
`--rerun-tasks` is the obvious lever and the wrong one:

```
$ rm -rf app/build/tmp/kotlin-classes app/build/test-results \
         app/build/tmp/testDebugUnitTest app/build/reports/tests
$ ./gradlew --no-daemon --no-build-cache :app:testDebugUnitTest

AAR_MTIME    = 2026-08-08 13:40:29
RUN_START    = 2026-08-08 14:14:43          <- 34 minutes AFTER the AAR: the ordering is provable
> Task :app:compileDebugKotlin              <- EXECUTED. no UP-TO-DATE, no FROM-CACHE
> Task :app:compileDebugUnitTestKotlin
kotlin error lines: 1
```

**That single `e:` is `e: Daemon compilation failed`, paired with `w: Failed to compile with Kotlin
daemon: java.lang.Exception` — the Kotlin daemon dying of host memory pressure (§D), not a rejected
source.** It fell back and `compileDebugUnitTestKotlin` completed. Every other diagnostic is a
pre-existing deprecation warning in `ui/kit` (`CtaButton`, `FocusRing`, `Grain`, `Haptics`, `Motion`,
`ScanReticle`, `StatusDot`, `Surfaces` ×4, `Toggle` — all `overrides a deprecated member`).

**Nothing about `approve`. Nothing about `PhoneSurface`. Nothing about the transcript.**

So: production Kotlin **plus the entire test source set** compiles against the rebuilt AAR, and
**there was no signature mismatch to fix.** `app.approve(panel.sessionId, panel.itemId, decision.id)`
(`PhoneSurface.kt:2007`) and `onApproval = ::openApproval` (`:1683`) are accepted by a real compiler.
Go `int` binding as Java `long` is why `FacadeBridge.transcript(..., limit: Long)` is correct.

---

## C. The integration test is NOT this workpackage's, and the duplicate was deleted

This agent independently built a second recorder/renderer pair — a `phonecore`-level fixture emitter,
a committed `interaction-screen-fixture.json`, and a Robolectric suite over it — in the same minutes
as §3's. **All three were deleted rather than kept**, and the reasoning is the point:

- §3a's recorder drives the **real bound facade** — `App.ReadTranscript`, `App.PendingApprovals`,
  `App.Approve` over the full rig with a separate gateway process. The deleted one folded through
  `phonecore` in-process, one struct-copy below the boundary.
- §3's Kotlin compares the pressed button's id against **the id the real `App.Approve` accepted**
  (`tap("pending").decision_id`). The deleted suite could only compare it against the id its own
  fixture offered — it never called the facade, so that assertion was circular where §3's is not.
- Normalizing the volatile fields **at emission** (`item-1`, `turn-1`, fixed `ts`, zeroed
  `content_hash`) beats scrubbing them at compare time, which is what the deleted one had to do.

**Two goldens of one crossing is worse than one**: they can disagree, and then neither is evidence.
Recorded here so nobody reinstates the weaker half from a transcript.

---

## D. §5 CLOSED: the assertions executed, and the golden suite is GREEN

The XML below was **snapshotted out of `app/build` the moment the task finished**, because the next
run's task-start cleanup deletes it — which is what destroyed four earlier attempts' evidence. The
three files are archived in-repo at **`docs/verification/i1-robolectric/`**, so this claim is
re-readable rather than resting on a transcript.

```
TranscriptPanelTest:        tests=21 failures=0 errors=0 skipped=0  ts=2026-08-08T12:22:20.380Z
TranscriptScreenGoldenTest: tests=6  failures=0 errors=0 skipped=0  ts=2026-08-08T12:22:33.523Z
TranscriptViewTest:         tests=7  failures=0 errors=0 skipped=0  ts=2026-08-08T12:22:35.528Z
TOTAL                       tests=34 failures=0 errors=0 skipped=0
```

(Timestamps are UTC; 12:22Z is 14:22 local.)

**`TranscriptScreenGoldenTest` 6/6 green is slice I1's app half passing against the machine's own
recorded bytes** — §3's whole claim, executed rather than merely compiled.

**Why these are provably not the stale corpse.** Run 4 above **deleted `app/build/test-results`
outright at 14:14:43**, so nothing in that tree can predate it. The full ordering:

```
13:40:29  AAR rebuilt
13:51:11  i1-transcript-screen.golden.json recorded
14:14:43  app/build/test-results DELETED
14:22:20  first assertion executes
14:22:35  last assertion executes
```

`TranscriptScreenGoldenTest` did not exist before today, so it cannot be a stale artifact of anything.

**Open point 7.2 is retired**: `i1-transcript-screen.golden.json` is confirmed staged into
`app/build/generated/policy-test-resources/` alongside `design-tokens.tsv` and the rest, so the suite
read the recording rather than failing loudly for want of it.

### What is still NOT claimed

- **The full-suite number.** A directory-wide aggregate read at ~14:22:30 gave
  `CLASSES=131 TESTS=1062 FAILURES=0`, and seconds later all but the three files above were gone. A
  131-class run and a 3-class run cannot both have completed inside 40 seconds, so **that 1062 is
  recorded as observed-but-unsubstantiated and is not a green.** Only the three snapshotted classes
  are stood behind here.
- **A handset.** Unchanged by this appendix, and §7.3 states it correctly.

---

## E. Corrections to the record, each of which cost a cycle today

1. **`NoSuchFileException: .../binary/in-progress-results-generic.bin` is an OOM signature, not a
   concurrent-Gradle signature.** §5's trap list attributes it to collision. It was tested: clearing
   `test-results/`, `tmp/testDebugUnitTest/` **and** `reports/tests/` and re-running produced the
   identical error, with `vm.swapusage: used = 8083.25M / 9216.00M`. It is the test **worker** dying
   before it can create its own results file. Diagnosing it as a clobber sends you to serialization,
   which does not fix it; the fix is fewer resident JVMs.
2. **`scripts/o2-gradle-run.sh` resolves `ROOT` from its own location.** Invoked from the main
   checkout it builds **the main checkout**, not the worktree. A run at 13:47:57 reported
   `BUILD SUCCESSFUL in 4m26s` having tested main while this worktree was the subject. Use the
   worktree's own `android/gradlew`.
3. **`until ! pgrep -f "gradle-wrapper.jar"; do sleep 10; done` can never exit** — the waiter's own
   command line contains the pattern, so `pgrep -f` matches itself. Three such waiters sat for
   25–40 minutes. Put the pattern in a **script file** and match with `ps | grep '[G]radle'`, so the
   waiter's argv is only a path.
4. **`--rerun-tasks` is not a way to force an AGP build to recompile** (§B table, run 3).
5. **A stale XML count reads exactly like a green.** `scripts/o2-gradle-run.sh` printed
   `testDebugUnitTest: 130 result files, 130 written in the last hour` for a build whose Kotlin
   compile had **failed**. A count is only evidence if the files are newer than the run that claims
   them — which is why every number in §D carries a timestamp and a deletion that precedes it.

**Five false greens surfaced in one day** on this one verification: the AAR-overwrite run, the
main-checkout run, the stale-130 count, `UP-TO-DATE`, and `FROM-CACHE`. Each looks identical to
success at the point a reader would stop.
