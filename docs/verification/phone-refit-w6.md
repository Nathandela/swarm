# Phone refit W6: Chat rows (verification evidence)

Bead `agents-tracker-d45a.6`. Contract: `docs/specifications/phone-refit-playbook.md` §7
(W6.1, W6.2). Worktree `refit-w6`, branch `refit/w6`, branched from `main` at `1a0e7b29`.
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

## Commits on `refit/w6` (on `main` at `1a0e7b29`; not rebased, per the fleet protocol)

| SHA | Subject |
|---|---|
| `ff3c65f8` | Add the agent tool kind: Claude's Task tool classifies as agent |
| `b089efa5` | Rename the decision pill: Needs your answer |
| `220fdcd9` | Read tool rows as a verb over one grey line |

`git merge-tree --write-tree main refit/w6` is clean: `main`'s W4 changes to `ActivityRow.kt`
(the padding, lines 21-29 and 91-100) do not overlap W6's hunks (the `secondary` parameter and
the body cell, lines 71+ and 123+), so the rebase at merge is trivial.
