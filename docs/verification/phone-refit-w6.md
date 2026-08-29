# Phone refit W6: Chat rows (verification evidence)

Bead `agents-tracker-d45a.6`. Contract: `docs/specifications/phone-refit-playbook.md` §7
(W6.1, W6.2). Worktree `refit-w6`, branch `refit/w6`, branched from `main` at `1a0e7b29` and rebased by the
orchestrator onto `main` at `49759ed7` before the review round (the rebased SHAs are the ones in
the commit list).
Each item below records the RED run (tests written first, exact failure text), the GREEN run,
and one negative control per behavioural change (the fix perturbed back, the test shown
failing, the file restored).

## W6.1 A tool row is a verb and one grey line

### Go: `agent` joins the wire vocabulary; `Task` classifies as `agent`

Tests written first: `internal/adapter/interaction_test.go`
(`TestInteractionValidate_AcceptsEverySection7ActionType`, new; `TestInteractionValidate`
untouched), `internal/adapter/claude/w6_agent_test.go`
(`TestActionFor_ClassifiesEachToolByName`, new). The R6 table (`r6_searchfetch_test.go`) is
fixture-driven and no Task corpus is recorded, so the Task row lives in a direct `actionFor`
table rather than in a fabricated fixture.

#### RED

```
$ go test -race -count=1 -run 'TestInteractionValidate_AcceptsEverySection7ActionType|TestActionFor_ClassifiesEachToolByName' ./internal/adapter/ ./internal/adapter/claude/
    interaction_test.go:353: action.type "agent" is in §7's vocabulary and was rejected: action.type "agent" is not one of [read edit write search execute fetch other] (interaction-schema.md §3/§4)
--- FAIL: TestInteractionValidate_AcceptsEverySection7ActionType (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter	1.024s
    w6_agent_test.go:33: actionFor("Task").Type = "other", want "agent"
--- FAIL: TestActionFor_ClassifiesEachToolByName (0.01s)
    --- FAIL: TestActionFor_ClassifiesEachToolByName/Task (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	1.490s
exit=1
```

#### GREEN

```
$ go test -race -count=1 ./internal/adapter/...
ok  	github.com/Nathandela/swarm/internal/adapter	2.337s
ok  	github.com/Nathandela/swarm/internal/adapter/agy	3.917s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	3.130s
ok  	github.com/Nathandela/swarm/internal/adapter/codex	4.232s
ok  	github.com/Nathandela/swarm/internal/adapter/detect	10.867s
ok  	github.com/Nathandela/swarm/internal/adapter/fixtureio	4.126s
ok  	github.com/Nathandela/swarm/internal/adapter/opencode	3.749s
ok  	github.com/Nathandela/swarm/internal/adapter/refadapter	3.686s
ok  	github.com/Nathandela/swarm/internal/adapter/registry	2.048s
$ go build ./... && go vet ./... && golangci-lint run
0 issues.
```

`docs/specifications/interaction-schema.md` §7's `type` row gains `agent` in the same commit,
so the sealed vocabulary the spec states is the one the oneOf enforces.

#### Negative controls

The `agent` oneOf entry removed (`internal/adapter/interaction.go`), then restored:

```
    interaction_test.go:353: action.type "agent" is in §7's vocabulary and was rejected: action.type "agent" is not one of [read edit write search execute fetch other] (interaction-schema.md §3/§4)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter	0.822s
```

The `Task` arm perturbed back to `other` (`internal/adapter/claude/interaction.go`), then restored:

```
    --- FAIL: TestActionFor_ClassifiesEachToolByName/Task (0.00s)
        w6_agent_test.go:33: actionFor("Task").Type = "other", want "agent"
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	0.818s
```

GREEN after both restores:

```
ok  	github.com/Nathandela/swarm/internal/adapter	1.806s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	1.985s
```

### Kotlin: the verb, the grey line, the glyph, the pill

Tests written first: `ToolCardTest.kt` (`everyKindInTheVocabularyHasItsOwnGlyph`'s vocabulary
gains `agent`), `TranscriptPanelTest.kt` (new
`every tool kind gets a verb and other is never a question mark`; the `item()` fixture gains a
`toolKind` parameter, which the flat field the verb reads off requires), `TranscriptScreenGoldenTest.kt`
(exactly two expectations, the two `BLOCK` rows), `TranscriptChatRenderTest.kt` (one expectation),
`SessionDetailPanelTest.kt` (new `the decision pill asks for the reader's answer`, W6.2).

`TranscriptBlock.secondary` (a data field defaulting to `""`, nothing filling it) was added before
this run so the panel test compiles: one missing field fails the whole Kotlin test compilation and
would have hidden every other class's failure text behind a compile error. Its behaviour is what
the run below is RED on.

#### Changed assertions (team-lead ruling 2: the wave's "must change" set; only expected values move)

| Test | Before | After |
|---|---|---|
| TranscriptPanelTest `a tool run reads as what the tool did, from the structured action` | line `Read src/main.rs`, emphasis `src/main.rs` | line `Read main.rs`, emphasis null |
| TranscriptPanelTest `each action type is read from its own field rather than from the tool name` | `Grep TODO` / `Bash go test ./...` | `Searched` / `Ran a command` |
| TranscriptPanelTest `an unclassified call falls back to the tool and guesses nothing` | `WebFetch` | `Used a tool` |
| TranscriptChatRenderTest `a tool run draws the flat tool_kind glyph as its own cell` | `Read /tmp/x` | `Read x` |
| TranscriptScreenGoldenTest `the recorded Claude Code turn reads as a conversation` (:246-247) | `Read /Users/Nathan/spike-sb-work/edit-target3.txt` / `Edit /Users/Nathan/spike-sb-work/edit-target3.txt` | `Read edit-target3.txt` / `Edited edit-target3.txt` |

#### RED (run 1: lane start 2026-08-28T05:38:39Z, `--tests` on the five classes)

```
BUILD FAILED in 3m 49s
gradle exit=1
tests=67 failures=8 errors=0 skipped=0

ToolCardTest > everyKindInTheVocabularyHasItsOwnGlyph
    java.lang.AssertionError: distinct kinds draw distinct glyphs; a shared glyph makes two different acts look alike expected:<8> but was:<7>
SessionDetailPanelTest > the decision pill asks for the reader's answer
    org.junit.ComparisonFailure: expected:<[Needs your answer]> but was:<[Decision needed]>
TranscriptChatRenderTest > a tool run draws the flat tool_kind glyph as its own cell
    org.junit.ComparisonFailure: the glyph was spliced into the sentence, which rewrites the line the recorded crossing pins byte for byte expected:<Read []x> but was:<Read [/tmp/]x>
TranscriptPanelTest > a tool run reads as what the tool did, from the structured action
    org.junit.ComparisonFailure: expected:<Read []main.rs> but was:<Read [src/]main.rs>
TranscriptPanelTest > each action type is read from its own field rather than from the tool name
    org.junit.ComparisonFailure: expected:<[Searched]> but was:<[Grep TODO]>
TranscriptPanelTest > every tool kind gets a verb and other is never a question mark
    org.junit.ComparisonFailure: expected:<Read []main.rs> but was:<Read [src/]main.rs>
TranscriptPanelTest > an unclassified call falls back to the tool and guesses nothing
    org.junit.ComparisonFailure: expected:<[Used a tool]> but was:<[WebFetch]>
TranscriptScreenGoldenTest > the recorded Claude Code turn reads as a conversation
    java.lang.AssertionError: expected:<[(transcript.bubble, Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt), (transcript.approval, Edit /Users/Nathan/spike-sb-work/edit-target3.txt), (transcript.block, Read edit-target3.txt), (transcript.block, Edited edit-target3.txt), (transcript.filechange, modify · /Users/Nathan/spike-sb-work/edit-target3.txt · +1 -1), (transcript.block, Done. Changed 'line two' to 'line TWO EDITED' in edit-target3.txt.)]> but was:<[(transcript.bubble, Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt), (transcript.approval, Edit /Users/Nathan/spike-sb-work/edit-target3.txt), (transcript.block, ...
```

The golden's other expectations (`:245` approval, `:249` file change, `:278`, `:295`) are
unchanged and pass in the same run; `TranscriptViewTest` is untouched at this point.

#### Run 2 (lane start 2026-08-28T06:49:45Z): model GREEN, view RED, question-mark RED

The model GREEN (ToolCard `agent`, `verbFor`, `secondaryFor`, the pill) was applied, and two
more tests written first: `TranscriptViewTest` `the secondary line is one line and ellipsised`
(the `block()` fixture gains `secondary`; `KitTag.ACTIVITY_SECONDARY` was added ahead of the
view so the test compiles and fails on the missing VIEW rather than on the missing name) and
`ToolCardTest` `noKindDrawsAQuestionMark` (team-lead ruling 4: the Done-when is literal, `?`
never renders, so `other` gets a glyph of its own).

```
BUILD FAILED in 3m 28s
gradle exit=1
aar unchanged
xml total=6 stale=0
tests=83 failures=3 errors=0 skipped=0

ToolCardTest > noKindDrawsAQuestionMark
    java.lang.AssertionError: kind other draws a question mark. Actual: ?
TranscriptPanelTest > an unreadable body renders as a neutral row and never crashes
    org.junit.ComparisonFailure: expected:<[tool_run]> but was:<[Used a tool]>
TranscriptViewTest > the secondary line is one line and ellipsised
    java.lang.IllegalArgumentException: the component carries no view tagged `activity secondary`; every assertion about it would compare nulls
```

The first and third are the REDs this run was for. The second is a REGRESSION the model
GREEN introduced and the existing test caught: an item with no readable body (and no
`tool_kind`) must stay the neutral `tool_run` row, and `verbFor("")` said "Used a tool". The
test is untouched; the fix is `verbFor`'s `"" -> ""` arm (no kind is no fact,
interaction-schema.md §2's `tool_kind` row), and the new panel test gains the same claim as
its last assertion, for which this run is the RED. The other five RED-1 failures are GREEN
here (the two golden rows, the chat-render line, the three panel lines, the pill).

#### Run 3 (lane start 2026-08-28T06:57:56Z): view GREEN attempt, one construction bug

The view GREEN (`activityRow`'s optional `secondary`, `rowFor` passing it, `other -> T`) and
the `"" -> ""` arm were applied. `go test ./android/gate/` passed on the kit change
(`ok ... 106.539s`), but the Kotlin run failed on every row that carried a secondary:

```
BUILD FAILED in 3m 17s
tests=83 failures=8 errors=0 skipped=0
TranscriptChatRenderTest > a tool run draws the flat tool_kind glyph as its own cell
    java.lang.IllegalArgumentException: base aligned child index out of range (0, 0)
```

(the same exception under all eight). `baselineAlignedChildIndex = 0` was set on the vertical
stack before its children were added, and the platform checks the index against the count it
has. Moved after the two `addView` calls.

#### GREEN (run 4, lane start 2026-08-28T07:46:33Z, the same six classes)

```
gradle exit=0
aar unchanged
xml total=6 stale=0
tests=83 failures=0 errors=0 skipped=0
```

Every RED above is GREEN here, the untouched tests of the six classes with them: `?` renders
for no kind, every kind reads as a verb, the grey line is one ellipsised `TextView` in
`swarm_text_tertiary`, an unreadable body is still the neutral row, and the pill reads
"Needs your answer".

#### Negative controls (Kotlin)

Each control perturbs the committed tree (`220fdcd9`, `b089efa5`), runs the lane, and restores
every main source with `git checkout -- android/app/src/main/kotlin` (the runner records
`modified main files now: 0`). Control A batches five ORTHOGONAL perturbations, each aimed at
a different test, so one lane run attributes all five by its failure text; control B is the
one rule those five cannot isolate (the execute grey line), run alone.

Control A (lane start 2026-08-28T07:54:20Z): `agent -> A` removed and `other -> ?` restored in
`ToolCard.kt`; `line = phrase(fields.tool, fields.target)` restored in `TranscriptPanel.kt`;
`rowFor` stops passing `secondary` in `TranscriptView.kt`; `DECISION_PILL = "Decision needed"`
in `SessionDetailPanel.kt`.

```
BUILD FAILED in 2m 50s
tests=83 failures=10 errors=0 skipped=0
ToolCardTest > everyKindInTheVocabularyHasItsOwnGlyph
    java.lang.AssertionError: distinct kinds draw distinct glyphs; a shared glyph makes two different acts look alike expected:<8> but was:<7>
ToolCardTest > noKindDrawsAQuestionMark
    java.lang.AssertionError: kind agent draws a question mark. Actual: ?
SessionDetailPanelTest > the decision pill asks for the reader's answer
    org.junit.ComparisonFailure: expected:<[Needs your answer]> but was:<[Decision needed]>
TranscriptChatRenderTest > a tool run draws the flat tool_kind glyph as its own cell
    org.junit.ComparisonFailure: the glyph was spliced into the sentence, which rewrites the line the recorded crossing pins byte for byte expected:<Read []x> but was:<Read [/tmp/]x>
TranscriptPanelTest > a tool run reads as what the tool did, from the structured action
    org.junit.ComparisonFailure: expected:<Read []main.rs> but was:<Read [src/]main.rs>
TranscriptPanelTest > each action type is read from its own field rather than from the tool name
    org.junit.ComparisonFailure: expected:<[Searched]> but was:<[Grep TODO]>
TranscriptPanelTest > every tool kind gets a verb and other is never a question mark
    org.junit.ComparisonFailure: expected:<Read []main.rs> but was:<Read [src/]main.rs>
TranscriptPanelTest > an unclassified call falls back to the tool and guesses nothing
    org.junit.ComparisonFailure: expected:<[Used a tool]> but was:<[WebFetch]>
TranscriptScreenGoldenTest > the recorded Claude Code turn reads as a conversation
    java.lang.AssertionError: expected:<[(transcript.bubble, Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt), (transcript.approval, Edit /Users/Nathan/spike-sb-work/edit-target3.txt), (transcript.block, Read edit-target3.t
TranscriptViewTest > the secondary line is one line and ellipsised
    java.lang.IllegalArgumentException: the component carries no view tagged `activity secondary`; every assertion about it would compare nulls
```

| Perturbation | Test that caught it |
|---|---|
| agent glyph removed | ToolCardTest `everyKindInTheVocabularyHasItsOwnGlyph` (8 kinds, 7 glyphs) |
| other -> ? | ToolCardTest `noKindDrawsAQuestionMark` |
| verb line reverted | TranscriptPanelTest x4, TranscriptScreenGoldenTest, TranscriptChatRenderTest |
| view drops the secondary | TranscriptViewTest `the secondary line is one line and ellipsised` |
| pill reverted | SessionDetailPanelTest `the decision pill asks for the reader's answer` |

Control B (lane start 2026-08-28T08:13:28Z, `--tests TranscriptPanelTest` alone): `secondaryFor`
perturbed so execute's grey line falls to the output like every other kind
(`firstLine(fields.output.ifEmpty { fields.target })` for every kind), then restored:

```
BUILD FAILED in 3m 25s
tests=23 failures=1 errors=0 skipped=0
TranscriptPanelTest > every tool kind gets a verb and other is never a question mark
    org.junit.ComparisonFailure: the grey line under a command is the command's first line, never its output expected:<[go test ./...]> but was:<[ok  swarm]>
restored; modified main files now: 0
```

The other 22 tests of the class pass in the same run: the perturbation reaches only the one
rule it targets (team-lead ruling 3: execute -> the command's first line, always).

### The well past the bound (untouched, the wave's negative control)

`TranscriptOverflowTest.kt` is byte-identical to `main` at `1a0e7b29`
(`git diff 1a0e7b29 HEAD -- .../TranscriptOverflowTest.kt` is empty); its
`output past the bound keeps a head and offers the rest on its own screen` still pins
`OPEN_IN_PLACE_LINES = 20` and `overflowOf`'s route, and `card.wellVisible` (`TranscriptPanel.kt:939-947`)
still gates the well. The wave touched neither; the full-suite run below carries the class GREEN.

## Gates on the finished branch

### Kotlin (full suite, lane start 2026-08-28T09:56:15Z)

```
$ . android/toolchain.env && cd android && ./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache
gradle exit=0
aar unchanged (mtime 1787894315)
xml total=202 stale=0
SUMMARY tests=1599 failures=0 errors=0 skipped=0
lane done 2026-08-28T10:01:14Z
```

`TranscriptOverflowTest`: `tests="14" failures="0"` in the same run (the well past the bound).

### Go

```
$ go build ./... && go vet ./... && golangci-lint run
0 issues.
exit=0
```

```
$ env -u SWARM_HOOK_CAPTURE -u SWARM_SESSION_ID -u SWARM_HOOK_SEQ_FILE -u SWARM_HOOK_TOKEN -u SWARM_DAEMON_SOCK -u SWARM_SHIM_HOOK_SOCK go test -race -count=1 -timeout 40m ./...
(start 2026-08-28T11:51:01Z, exit 1 at 11:59:54Z: 62 packages ok, 7 with no test files, 2 FAIL)
--- FAIL: TestE2E_ReplayProductionPath_AgyOpencode (10.36s)
    replay_e2e_test.go:403: agy: idle observed only 2.875566709s after the first active sample (want >= 3s: ...)
FAIL	github.com/Nathandela/swarm/internal/e2e	33.398s
--- FAIL: TestS18_ARevokeCarriesTheSIGNEDTargetAndNotTheSigner (2.54s)
    s18_sec6_adversarial_test.go:325: gateway service did not stop within 2s of cancel
FAIL	github.com/Nathandela/swarm/internal/skeleton	434.072s
```

Both are wall-clock deadlines in packages the wave does not touch (W6's Go diff is
`internal/adapter` and `internal/adapter/claude`, both `ok` in the same run), and the run shared
the machine with another fleet's gate; per the fleet protocol (§1 item 6) each is rerun once in
isolation before it is red:

```
$ env -u ... go test -race -count=1 -timeout 40m ./internal/e2e/
ok  	github.com/Nathandela/swarm/internal/e2e	24.428s
$ env -u ... go test -race -count=1 -timeout 40m ./internal/skeleton/
ok  	github.com/Nathandela/swarm/internal/skeleton	435.048s
```

All four Go gates green; the Kotlin suite green.

## Commits on `refit/w6` (rebased onto `main` at `49759ed7`)

| SHA | Subject |
|---|---|
| `d82736b3` | Add the agent tool kind: Claude's Task tool classifies as agent |
| `2118cab3` | Rename the decision pill: Needs your answer |
| `81c7aa43` | Read tool rows as a verb over one grey line |
| `bd5134db` | Record W6 evidence: RED, GREEN, controls, gates, changed assertions |
| `795e5f80` | The oneOf test reads section 7's vocabulary from the schema (review fix 2) |
| `695483fd` | Route the grey line's style through Kit.appearance (review fix 1) |
| `2ab35944` | Say Needs your answer on the drawn stages and in the prose (review fix 3) |
| `51b79a8a` | The grey line is the first line that says something (review fix 4) |

(Before the rebase the first three were `ff3c65f8`, `b089efa5`, `220fdcd9` on `1a0e7b29`.)

`git merge-tree --write-tree main refit/w6` is clean: `main`'s W4 changes to `ActivityRow.kt`
(the padding, lines 21-29 and 91-100) do not overlap W6's hunks (the `secondary` parameter and
the body cell, lines 71+ and 123+), so the rebase at merge is trivial.

## Review round (2026-08-28)

One adversarial round (fleet protocol §1 item 8): one BLOCKING finding, two SHOULD-FIX, one
NOTE ruled in; one NOTE filed as a bead and not touched (a pre-R6 record with a readable body
but no `tool_kind` now reads `tool_run`). Each fix below: the defect, the RED text, the GREEN,
and the control that shows the fix is what the test bites on.

### The rebase

The orchestrator rebased `refit/w6` onto `main` at `49759ed7` (tree identical to the merge-tree
recorded above, no conflicts) and pushed it as `bd5134db`; the wave's commits became
`d82736b3`, `2118cab3`, `81c7aa43`, `bd5134db`. The worktree was reset to it and
`android/app/libs/swarm.aar` copied from `refit-main` (main's mobile facade changed in W7).

### Fix 1 (BLOCKING): the grey line's style bypassed Kit.appearance

Defect: `ActivityRow.kt:165` applied `TextAppearance_Swarm_Mono_Meta` with a bare
`setTextAppearance`, which main's W8.1 fence forbids in `ui/kit` (every kit style goes through
`Kit.appearance`, which also applies the style's leading). Mono.Meta declares no lineHeight, so
no golden moves.

RED, on the rebased tree before the fix:

```
$ go test -count=1 ./android/gate -run TestW8
--- FAIL: TestW8_EveryKitStyleIsAppliedThroughKitAppearance (0.03s)
    w8_leading_test.go:70: W8.1: 1 bare setTextAppearance( call(s) in ui/kit outside Kit.appearance. Each applies the style's size, weight, family and tracking and drops its leading; route it through Kit.appearance(this, style):
        	ActivityRow.kt:165: setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
FAIL	github.com/Nathandela/swarm/android/gate	0.917s
```

GREEN, `Kit.appearance(this, R.style.TextAppearance_Swarm_Mono_Meta)`:

```
$ go test -count=1 ./android/gate -run TestW8
ok  	github.com/Nathandela/swarm/android/gate	0.722s
```

Kotlin lane on `ActivityRowTest` + `TranscriptViewTest` (lane start 2026-08-28T13:20:59Z):

```
gradle exit=0
aar unchanged (mtime 1787923203)
xml total=2 stale=0
SUMMARY tests=27 failures=0 errors=0 skipped=0
```

### Fix 2 (SHOULD-FIX): the oneOf test read a copied list, not the schema

Defect: `TestInteractionValidate_AcceptsEverySection7ActionType` iterated a literal list, so a
member added to interaction-schema.md §7 but not to the oneOf went uncaught. The test now reads
the backticked tokens off the schema's `type` row (the line starting "| `type` |"; the field's
own name dropped, a repeat kept once), the way `skeleton/r6_toolkind_test.go` reads the same
file, and fails loudly when the row is missing or names nothing.

RED, proof it bites: a `probe` token added to the tracked schema row (restored afterwards with
`git checkout`), the new test:

```
$ go test -count=1 -run TestInteractionValidate_AcceptsEverySection7ActionType ./internal/adapter/
--- FAIL: TestInteractionValidate_AcceptsEverySection7ActionType (0.01s)
    interaction_test.go:358: action.type "probe" is in §7's vocabulary and was rejected: action.type "probe" is not one of [read edit write search execute fetch agent other] (interaction-schema.md §3/§4)
FAIL	github.com/Nathandela/swarm/internal/adapter	1.930s
```

Control (the defect, shown): the OLD literal-list test (`bd5134db`'s) against the same probed
schema row passes, which is exactly the hole:

```
--- PASS: TestInteractionValidate_AcceptsEverySection7ActionType (0.00s)
ok  	github.com/Nathandela/swarm/internal/adapter	1.388s
```

GREEN, schema restored (`git status` clean on it), the new test:

```
--- PASS: TestInteractionValidate_AcceptsEverySection7ActionType (0.01s)
ok  	github.com/Nathandela/swarm/internal/adapter	1.749s
```

### Fix 3 (SHOULD-FIX + prose): the drawn stages and the prose still said the old copy

No test exists for figures and prose; the evidence is the grep, before and after, plus the
copy checker and its Go harness.

Before (`grep -rn "Decision needed" docs android/app/src/main`):

```
docs/design/conversation-drawing.html:313:              <div class="pill">Decision needed ↓</div>
docs/design/conversation-drawing.html:610:          <div class="stage"><div class="pill">Decision needed ↓</div></div>
docs/design/substrate-components.md:279:| 32 | Decision pill | ... the copy table records `Decision needed`, and a string not on that sheet is not on the screen ...
docs/specifications/phone-refit-playbook.md:639:| Decision needed / Your machine could not apply this answer | Needs your answer / ... |
docs/specifications/chat-surface-plan.md:198:| `decisionPill` | *Decision needed* — the only persistent affordance in the flow |
docs/verification/remote-phaseA-a7-review.md:95:... **Decision needed (ADR):**
docs/verification/phone-refit-w6.md:116, :207, :218 (this file's RED text and control A)
android/app/src/main/kotlin/dev/swarm/phone/ui/kit/DecisionPill.kt:34: * `Decision needed`; a string not on that sheet is not on the screen. ...
android/app/src/main/kotlin/dev/swarm/phone/PhoneSurface.kt:636:     * The drawing's one persistent affordance: *Decision needed*, above the composer, while the
android/app/src/main/kotlin/dev/swarm/phone/ui/screens/TranscriptPanel.kt:134:     * `decision.pill` -- *Decision needed* -- "appears only while an unanswered decision is off
```

Changed: the two drawn stages (`:313`, `:610`, the arrow kept -- it is not copy),
substrate-components.md (`:279`; the review cited `:277`, the row is at `:279` on this tree),
chat-surface-plan.md `:198`, the three KDocs, and interaction-schema.md §7 IS-TOOL-2 ("the card
falls back to `tool`" -> the phone says "Used a tool" and never the tool name).

After:

```
$ grep -rn "Decision needed" android/app/src/main
(empty; exit 1)
$ grep -rn "Decision needed" docs
docs/specifications/phone-refit-playbook.md:639   (W5's table: the "Now" column records the old literal by construction)
docs/verification/remote-phaseA-a7-review.md:95   ("Decision needed (ADR)": an unrelated sense, not the pill)
docs/verification/phone-refit-w6.md:116, :207, :218   (this file: the wave's RED text and control A, quoted)
```

Those three are left as they are: a "Now" column, a different phrase, and quoted failure text.

```
$ python3 scripts/check-conversation-copy.py .
20 binding(s) checked across 15 of 28 tabled row(s), ONE DIRECTION.
checker exit=0
$ go test -count=1 ./internal/verify/ -run CopySheet
ok  	github.com/Nathandela/swarm/internal/verify	3.416s
```

(The harness's tests are named `TestCopySheet_*`; `-run ConversationCopy` selects none of them.)

Control: reverting the commit brings every line of the "before" grep back; there is no test to
go red, which is the finding's own premise.

### Fix 4 (NOTE ruled in): the grey line took a blank first line

Defect: `firstLine` took the first line even when blank, so an `output_excerpt` opening with
"\n" drew no grey line. The rule is now the first line that says something: blank lines
skipped, the line's own edges trimmed.

Test first, `TranscriptPanelTest` `the grey line skips a blank first line` (output
`"\n\nok  swarm\n"` -> `ok  swarm`; command `"\n  go test ./...\n"` -> `go test ./...`).

RED (lane start 2026-08-28T13:29:36Z, `--tests TranscriptPanelTest`):

```
gradle exit=1
SUMMARY tests=24 failures=1 errors=0 skipped=0
FAILURE dev.swarm.phone.ui.screens.TranscriptPanelTest > the grey line skips a blank first line
    org.junit.ComparisonFailure: expected:<[ok  swarm]> but was:<[]>
```

The other 23 tests of the class pass in the same run, so the existing grey-line rules
(`every tool kind gets a verb and other is never a question mark`) are untouched by the new
claim.

GREEN (lane start 2026-08-28T14:21:58Z, `--tests TranscriptPanelTest`), `firstLine` the first
non-blank line, trimmed:

```
gradle exit=0
aar unchanged (mtime 1787923203)
xml total=1 stale=0
SUMMARY tests=24 failures=0 errors=0 skipped=0
```

### Negative controls (each fix reverted in turn, its gate or test red, then restored)

| Fix | Reverted to | What went red |
|---|---|---|
| 1 | `bd5134db`'s `ActivityRow.kt` (bare `setTextAppearance`), after the commit | `go test ./android/gate -run TestW8`: `--- FAIL: TestW8_EveryKitStyleIsAppliedThroughKitAppearance` / `ActivityRow.kt:165: setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)`; restored, `git status` clean |
| 2 | `bd5134db`'s literal-list test, with `probe` in the schema row | nothing: the old test PASSES the probe (the defect); the new test fails on it (`action.type "probe" ... was rejected`) |
| 3 | the commit itself | no test exists (the finding's premise): `grep -rn "Decision needed" android/app/src/main` finds the three KDocs again and the drawing's two stages return |
| 4 | `firstLine` taking the first line even when blank (the pre-fix tree) | TranscriptPanelTest `the grey line skips a blank first line`: `expected:<[ok  swarm]> but was:<[]>` (the RED run above) |

### Changed assertions (additions of this round)

| Test | Before | After |
|---|---|---|
| TranscriptPanelTest `the grey line skips a blank first line` | (new) | output `"\n\nok  swarm\n"` -> secondary `ok  swarm`; command `"\n  go test ./...\n"` -> `go test ./...` |
| `TestInteractionValidate_AcceptsEverySection7ActionType` | iterated `[read edit write search execute fetch agent other]` literally | iterates the backticked tokens of interaction-schema.md's `type` row; `t.Fatal` when the row is missing or names nothing |

No existing assertion changed its expected value in this round.

### Gates on the four-fix tree

Go (start 2026-08-28T14:22:40Z, `firstLine` already applied on disk):

```
$ go build ./... && go vet ./... && golangci-lint run
0 issues.
static exit=0
$ env -u ... go test -race -count=1 ./internal/adapter/... ./internal/verify/... ./android/gate/
ok  	github.com/Nathandela/swarm/internal/adapter	3.584s
ok  	github.com/Nathandela/swarm/internal/adapter/agy	3.801s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	4.551s
ok  	github.com/Nathandela/swarm/internal/adapter/codex	4.828s
ok  	github.com/Nathandela/swarm/internal/adapter/detect	12.285s
ok  	github.com/Nathandela/swarm/internal/adapter/fixtureio	2.034s
ok  	github.com/Nathandela/swarm/internal/adapter/opencode	3.187s
ok  	github.com/Nathandela/swarm/internal/adapter/refadapter	5.025s
ok  	github.com/Nathandela/swarm/internal/adapter/registry	3.074s
ok  	github.com/Nathandela/swarm/internal/verify	48.007s
ok  	github.com/Nathandela/swarm/android/gate	123.955s
test exit=0
```

Kotlin, the seven classes (the six W6 classes and `ActivityRowTest`), lane start
2026-08-28T15:22:51Z, on the tree with all four fixes committed:

```
$ ./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache --tests ...ToolCardTest --tests ...ActivityRowTest --tests ...SessionDetailPanelTest --tests ...TranscriptChatRenderTest --tests ...TranscriptPanelTest --tests ...TranscriptScreenGoldenTest --tests ...TranscriptViewTest
gradle exit=0
aar unchanged (mtime 1787923203)
xml total=7 stale=0
SUMMARY tests=96 failures=0 errors=0 skipped=0
lane done 2026-08-28T15:25:37Z
```

The full race suite and the full Kotlin suite run at merge, by the orchestrator.
