# A1b — the Claude Code interaction producer

**Slice**: `internal/adapter/claude` implements `adapter.InteractionSource` per ADR-010 §5 —
the four `capture=raw` hook rows, shaped into interaction-schema.md §3 items, with `Decision`
returning the `PermissionRequest` hook reply body and spike S-C's carve-out declared at capture.

**Normative**: [ADR-009](../adr/ADR-009-structured-chat-interaction.md),
[ADR-010](../adr/ADR-010-adapter-structured-capture.md) §5 and its conformance obligations 3-5,
[interaction-schema.md](../specifications/interaction-schema.md) §3, §7.
**Ground truth**: [spike-SB.md](spike-SB.md) (the recorded payloads),
[spike-SC.md](spike-SC.md) (the hook reply envelope and the Bash-with-a-file-path carve-out).

**Nothing in this slice ran a CLI.** Every assertion is against bytes recorded on 2026-07-18.

---

## 1. The corpus, and its provenance

`internal/adapter/claude/testdata/interaction/` holds three fixtures, each a **byte-identical**
copy of a real S-B recording (`cmp` reports no difference; the runs are in §3 below):

| Fixture | What it grounds |
|---|---|
| `claude-bash-pretooluse-no-escalation.json` | a Bash call that never escalated — two prompts, one tool run, real `tool_response.stdout` |
| `claude-bash-permissionrequest-run1.json` | the ONE genuine Bash `PermissionRequest` S-B captured. `touch approval-test.txt` names a file path, so it **is** S-C's carve-out |
| `claude-edit-permissionrequest-run1.json` | a Read run, an Edit `PermissionRequest` (native), and the `structuredPatch` that becomes a `file_change` |

`PROVENANCE.md` sits beside them and carries the same table plus the honest list of what the
corpus does NOT contain. That list is load-bearing, because it is exactly what the producer
declines to shape: `PermissionDenied` (never observed in ten runs), a `Write` `PostToolUse`,
`Grep`/`Glob`/`WebFetch` `tool_input`, and any tool response carrying an exit code, a non-empty
`stderr`, or `interrupted: true`.

`claude-edit-permissionrequest-run2.json` was deliberately not copied: S-B records it as having
an identical field set to run1, so it is a second copy of the same shapes and no new coverage.

---

## 2. RED, verbatim

Five tests, written before `internal/adapter/claude/interaction.go` existed. The RED reason is
the right one — the adapter implements no `InteractionSource` and declares no capture row — and
not a compile error:

```
$ go test ./internal/adapter/claude/ -run 'TestInteractionSource_ClaudeImplementsTheOptionalExtension|TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows|TestGoldenCorpus_|TestDecision_ReturnsThePermissionRequestHookReplyBody|TestCarveOut_' -count=1 -v
=== RUN   TestInteractionSource_ClaudeImplementsTheOptionalExtension
    interaction_test.go:44: AsInteractionSource(claude) reports false; ADR-010 §5 makes Claude Code a native capture source, and without it the daemon falls back to deriving items from the sanitized grid (spike S-A: PARTIAL, and it never recovers tool_input at all)
--- FAIL: TestInteractionSource_ClaudeImplementsTheOptionalExtension (0.00s)
=== RUN   TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows
    interaction_test.go:65: capture=raw rows = map[], want map[PermissionRequest:true PostToolUse:true PreToolUse:true UserPromptSubmit:true] (ADR-010 §5 names UserPromptSubmit, PreToolUse, PostToolUse and PermissionRequest and no others)
--- FAIL: TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows (0.00s)
=== RUN   TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems
    interaction_test.go:160: claude is not an InteractionSource
--- FAIL: TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems (0.00s)
=== RUN   TestGoldenCorpus_PassesCheckInteractionFixture
=== RUN   TestGoldenCorpus_PassesCheckInteractionFixture/claude-bash-pretooluse-no-escalation.json
=== RUN   TestGoldenCorpus_PassesCheckInteractionFixture/claude-bash-permissionrequest-run1.json
=== RUN   TestGoldenCorpus_PassesCheckInteractionFixture/claude-edit-permissionrequest-run1.json
--- PASS: TestGoldenCorpus_PassesCheckInteractionFixture (0.01s)
    --- PASS: TestGoldenCorpus_PassesCheckInteractionFixture/claude-bash-pretooluse-no-escalation.json (0.01s)
    --- PASS: TestGoldenCorpus_PassesCheckInteractionFixture/claude-bash-permissionrequest-run1.json (0.00s)
    --- PASS: TestGoldenCorpus_PassesCheckInteractionFixture/claude-edit-permissionrequest-run1.json (0.00s)
=== RUN   TestDecision_ReturnsThePermissionRequestHookReplyBody
    interaction_test.go:204: claude is not an InteractionSource
--- FAIL: TestDecision_ReturnsThePermissionRequestHookReplyBody (0.00s)
=== RUN   TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt
    interaction_test.go:249: claude is not an InteractionSource
--- FAIL: TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	1.875s
```

### The one that PASSED in RED, and why that was a defect in the test

`TestGoldenCorpus_PassesCheckInteractionFixture` passed with no producer at all. That is not a
lucky green: `CheckInteractionFixture` opens with `src, ok := AsInteractionSource(a); if !ok {
return nil }`, so against a non-source it has **nothing to replay** and reports no violations.
The test was measuring nothing.

It was fixed **before** GREEN, by adding the guard that makes the vacuity impossible — which is
the sanctioned direction (strengthen the test, never weaken it to pass):

```
$ go test ./internal/adapter/claude/ -run 'TestGoldenCorpus_PassesCheckInteractionFixture' -count=1 -v
=== RUN   TestGoldenCorpus_PassesCheckInteractionFixture
    interaction_test.go:189: claude is not an InteractionSource, so CheckInteractionFixture has nothing to replay and would pass vacuously
--- FAIL: TestGoldenCorpus_PassesCheckInteractionFixture (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/claude	0.868s
```

---

## 3. GREEN

```
$ go test ./internal/adapter/claude/ -run '<the same five>' -count=1 -v
--- PASS: TestInteractionSource_ClaudeImplementsTheOptionalExtension (0.00s)
--- PASS: TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows (0.00s)
--- PASS: TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems (0.01s)
    --- PASS: .../claude-bash-pretooluse-no-escalation.json (0.01s)
    --- PASS: .../claude-bash-permissionrequest-run1.json (0.00s)
    --- PASS: .../claude-edit-permissionrequest-run1.json (0.00s)
--- PASS: TestGoldenCorpus_PassesCheckInteractionFixture (0.00s)
    --- PASS: .../claude-bash-pretooluse-no-escalation.json (0.00s)
    --- PASS: .../claude-bash-permissionrequest-run1.json (0.00s)
    --- PASS: .../claude-edit-permissionrequest-run1.json (0.00s)
--- PASS: TestDecision_ReturnsThePermissionRequestHookReplyBody (0.00s)
--- PASS: TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.692s
```

Corpus copies proven identical to their S-B sources:

```
$ cmp docs/verification/fixtures/spike-sb/claude-bash-permissionrequest-run1.json internal/adapter/claude/testdata/interaction/claude-bash-permissionrequest-run1.json && echo IDENTICAL-1
IDENTICAL-1
$ cmp docs/verification/fixtures/spike-sb/claude-bash-pretooluse-no-escalation.json internal/adapter/claude/testdata/interaction/claude-bash-pretooluse-no-escalation.json && echo IDENTICAL-2
IDENTICAL-2
$ cmp docs/verification/fixtures/spike-sb/claude-edit-permissionrequest-run1.json internal/adapter/claude/testdata/interaction/claude-edit-permissionrequest-run1.json && echo IDENTICAL-3
IDENTICAL-3
```

The whole frozen conformance suite still passes against the now-capturing adapter — obligations
1 (purity/totality over the garbage battery), 3 (declaration half) and 5 (the carve-out) run
inside `CheckConformance`, which `TestConformance` has always called:

```
$ go test ./internal/adapter/... -count=1
ok  	github.com/Nathandela/swarm/internal/adapter	1.838s
ok  	github.com/Nathandela/swarm/internal/adapter/agy	1.326s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.815s
ok  	github.com/Nathandela/swarm/internal/adapter/codex	1.688s
ok  	github.com/Nathandela/swarm/internal/adapter/detect	9.092s
ok  	github.com/Nathandela/swarm/internal/adapter/fixtureio	1.949s
ok  	github.com/Nathandela/swarm/internal/adapter/opencode	2.345s
ok  	github.com/Nathandela/swarm/internal/adapter/refadapter	2.628s
ok  	github.com/Nathandela/swarm/internal/adapter/registry	1.586s
```

---

## 4. The `b94Allowed` row — the task said delete it; measurement says it stays

`a1-integration.md` predicted: *"that row should be deleted the day one [a corpus] does (the
allowlist is bidirectional and will demand it)."* The corpus now exists and
`CheckInteractionFixture` is called over it. **The row was deleted, and the fence failed:**

```
$ go test ./internal/verify/ -run TestB94_EveryExportedSymbolIsReachableFromProduction -count=1 -v
=== RUN   TestB94_EveryExportedSymbolIsReachableFromProduction
    phaseb_reachability_test.go:343:
        1 package(s) export symbols NO PRODUCTION ENTRY POINT CAN REACH.

          internal/adapter -- 1 unreachable exported symbol(s):
              internal/adapter.CheckInteractionFixture

        Each symbol is either (a) dead and to be DELETED, or (b) legitimately
        unreferenced, in which case add it to b94Allowed WITH A STATED REASON.
        Do not widen the root set to make this pass: a root set that reaches everything
        is how PB-NET-4 read met for five rounds.

--- FAIL: TestB94_EveryExportedSymbolIsReachableFromProduction (2.79s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/verify	3.577s
```

**The prediction was wrong about the instrument, not about the corpus.** B94 loads with
`Tests: false` (`phaseb_reachability_test.go:193`) and roots the RTA walk at `cmd/...` `main()`
plus the gomobile facade. A caller in a `_test.go` file is therefore invisible to it and **can
never** clear a symbol, and the bidirectional stale-exemption arm fires on production
reachability alone. A conformance harness has no production caller by construction — which is
precisely the reason its two neighbours `Conformance` and `CheckConformance` are permanently
allowlisted.

The only thing that could have retired this row is a **production** call site, and inventing one
for a function that replays recorded fixtures is the exact move the fence's own header warns
against (*"Do not widen the root set to make this pass"*).

So the row **stays**, with its reason rewritten from a placeholder-awaiting-a-caller into the
permanent one it actually is, and a comment above it recording that deletion was tried and
measured. `a1-integration.md`'s bullet carries a `CORRECTED 2026-08-07` note pointing here.

```
$ go test ./internal/verify/ -run TestB94_EveryExportedSymbolIsReachableFromProduction -count=1 -v
    phaseb_reachability_test.go:319: B94: 541 exported symbols examined, 54 unreachable and all accounted for
--- PASS: TestB94_EveryExportedSymbolIsReachableFromProduction (3.86s)
```

(541 examined against the 540 in `a1-integration.md`. The extra one is **not** from this slice:
`Interactions` and `Decision` are methods on the unexported `claudeAdapter`, so B94 never takes
them as subjects, and `New()` already existed. It comes from other work in this shared worktree,
see §7.)

---

## 5. What the producer shapes, and the evidence under each choice

| Row | Item(s) | Key fields, and where they were observed |
|---|---|---|
| `UserPromptSubmit` | `user_message` | `prompt`; `source: owner` |
| `PreToolUse` | `tool_run` `in_progress` | `tool_name`, `tool_use_id` (the fold key), §7 action from `tool_input` |
| `PostToolUse` | `tool_run` `completed` (+ `file_change`) | same `tool_use_id` so the pair collapses (IS-DELTA-3); `structuredPatch` renders §3.4 |
| `PermissionRequest` | `approval_request` `in_progress` | §7 action, `mode` decided at capture, decisions + verdicts |

### Six judgement calls, recorded rather than absorbed

**(a) The decisions are `allow`/`deny`, NOT `permission_suggestions`.** The brief asked for
"decisions[] from the recorded permission_suggestions vocabulary". The corpus does not support
it, in both directions at once, so the producer offers Claude Code's own two hook `behavior`
values instead:

- *No native reply applies a suggestion.* S-C proved exactly one reply body,
  `{"behavior":"allow"}`. Offering `setMode` on the `card` path would force `Decision` to return
  a body no capture ever showed — invented bytes written onto a hook that is holding a live tool
  call. Conformance forbids the cowardly middle too: `checkShapedItems` requires `mode: card` to
  report native for **every** decision id, so a suggestion could not simply be offered and left
  unanswerable.
- *No keystroke reaches one either.* The recorded Bash dialog settles it. That body offered
  **two** suggestions (`addDirectories`, `setMode`) while its dialog rendered **one** middle
  entry — decoded from that same fixture's `pty_capture`:

  ```
  Do you want to proceed?
  ❯ 1. Yes
    2. Yes, and always allow access to spike-sb-work/ from this project
    3. No

  Esc to cancel · Tab to amend · ctrl+e to explain
  ```

  Suggestion index is not menu position, so any keystroke derived from it presses the wrong
  button. Carrying suggestions is additive the day a capture shows how to answer one.

**(b) The prompt-card keystrokes are `1` and ESC, not `1` and `3`.** Read off the dialog above.
Both are **position-independent**; the numbered "No" is not — its number is the menu length, and
the hook body does not determine the menu length (that is the same 2-suggestions/3-entries fact).
A `3` hard-coded here would press "No" on this dialog and something else on the next one.

**(c) The carve-out test is crude and over-inclusive, on purpose.** `tool_name == "Bash" &&
strings.ContainsAny(command, "/.<>")`. The two error directions are not symmetric: a false
`prompt_card` costs one extra tap on a path that always works, while a false `card` renders a
one-tap card and leaves the session wedged behind a dialog nobody is at the machine to answer.
S-C's own probe command `kill -0 99999` — chosen precisely because it has no file-path argument —
still classifies native, which is what stops this collapsing into "always prompt_card".

**AMENDED 2026-08-07 by the adversarial review: the test is also UNDER-inclusive, and that is the
direction the paragraph above says must not happen.** Driven through the shipped shaper, one
`PermissionRequest` body per command:

```
"echo x > file.txt"              -> prompt_card
"rm -f file.txt"                 -> prompt_card
"sed -i s/a/b/ file.txt"         -> prompt_card
"mv a.txt b.txt"                 -> prompt_card
"touch Makefile"                 -> card          <-- names a file path; S-C's class
"rm README"                      -> card          <-- same
"chmod +x run"                   -> card          <-- same
"cat LICENSE"                    -> card          <-- same
"mkdir build"                    -> card          <-- same
"kill -0 99999"                  -> card          (correct: S-C's own native probe)
"echo hello. world"              -> prompt_card   (the accepted over-inclusion)
```

A bare relative filename carries none of `/.<>`, and no character test can close that — any bare
word is a possible path. The two complete answers are to send **every** Bash `PermissionRequest`
to the prompt card, which ADR-010 §5's "Bash-with-a-file-path is the measured carve-out" does not
say, or to widen the ADR; both are owner rulings rather than producer choices, so the review
recorded it instead of changing behaviour. **Latent today**: nothing in production calls
`Decision` or applies a prompt card at all (a1-integration.md §8.7 — there is no wire route for
the approve), so `mode` reaches the phone as a rendering hint with no apply path behind either
value. It becomes live the day the apply slice lands, which is the day it must be ruled on.
Marked `ponytail:` at the `pathish` constant.

**(d) The approval `ref` is synthesized, and encodes the mode.** Claude Code's
`PermissionRequest` body carries **no** per-request id: it has `session_id`, `prompt_id` and
`tool_name`, and the `tool_use_id` that `PreToolUse` carries is absent. Two things follow.
`Decision` is handed nothing but the ref and this adapter is stateless, so the ref must say
whether the request was the carve-out — hence
`permission-request:{card|prompt-card}:{tool}:{receivedAtMs}`. And the capture instant is in it
because the daemon folds records under one `item_id` **by ref** (`itemIDLocked`): a ref that
repeated would fold a second approval into an already-resolved one and violate IS-ST-1.
`ReceivedAtMs` is populated in production (`skeleton/interaction.go:167`), not just in fixtures.

**(e) `summary` is the literal action, not Bash's `description`.** The CLI supplies a
model-written `description` ("Create empty approval-test.txt file") and it reads better than
"Bash touch approval-test.txt". It is still the wrong field for this card: an approval is the one
surface where a human authorizes the thing itself, and the agent's prose about its own intent is
not the thing. The card shows what runs.

**(f) `source: owner` on every `user_message`.** The adapter cannot see who typed it, but it does
not need to today: D7/B43 freezes remote input to live keystrokes, so the phone authors no
prompt and everything this hook reports was typed at the machine.

### Deliberately not shaped, each with its reason

- **`agent_message`.** ADR-010 §5 enumerates four capture rows and `Stop` is not among them,
  even though its body carries `last_assistant_message`. Staying inside the ADR; the transcript's
  agent prose is a follow-up, not a silent addition. **Recorded as an open point.**
- **§7's `search` and `fetch`.** No `Grep`/`Glob`/`WebFetch` body was ever captured, so their
  argument key would be a guess. IS-TOOL-2's `other` is the sanctioned answer.
- **`exit_code`, `stderr`, and a `failed` status.** No recorded response carries an exit code;
  every `stderr` was empty; `interrupted` was only ever `false`. Mapping `interrupted` to
  `failed` rather than `declined` is a judgement no evidence supports, and IS-ST-2's sweep
  already closes items that never complete.
- **`change: delete` / `rename`.** No Claude Code tool emits a `structuredPatch` for either.
  `create` **is** derived, because it is read off the hunks' own unambiguous unified-diff
  arithmetic (every hunk `oldLines == 0` means nothing existed before) rather than off a guess
  about a tool.
- **`prompt_lines`.** IS-APR-3 requires them to come from the same machine-side sanitizer as a
  terminal snapshot. The adapter sees a hook body, never a grid, so it leaves them empty and the
  daemon fills them.

---

## 6. Teeth — five mutations, each applied, each caught, each reverted

Every one was run for real; the lines below are the fences' own output.

**1. The carve-out classifier stops seeing a file path** (`pathish` `"/.<>"` -> `"/"`), so
`touch approval-test.txt` would ship a one-tap card onto a request the hook cannot resolve:

```
--- FAIL: TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems/claude-bash-permissionrequest-run1.json
        interaction_test.go:174: item 2 mismatch:
--- FAIL: TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt
    interaction_test.go:270: mode = "card", want prompt_card: `touch approval-test.txt` names a file path, which is exactly S-C's carve-out
```

**2. The deny keystroke is hard-coded to the menu number** (`"\x1b"` -> `"3"`):

```
--- FAIL: TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt
    interaction_test.go:283: keystrokes = map["allow":"1" "deny":"3"], want allow=1 and deny=ESC (the recorded dialog's own affordances)
```

**3. The `hookSpecificOutput` wrapper is dropped** — the exact shape S-C found the CLI silently
ignores while falling through to a keystroke:

```
--- FAIL: TestDecision_ReturnsThePermissionRequestHookReplyBody
    interaction_test.go:232: reply hookEventName = "", want PermissionRequest; S-C proved a bare top-level decision is silently ignored
```

**4. `PostToolUse` stops declaring `capture`**, so its body would be flattened before the shaper
sees it. Caught twice — by the declaration test and, independently, by ADR-010's own conformance
obligation replaying the corpus:

```
--- FAIL: TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows
    interaction_test.go:65: capture=raw rows = map[PermissionRequest:true PreToolUse:true UserPromptSubmit:true], want map[PermissionRequest:true PostToolUse:true PreToolUse:true UserPromptSubmit:true] ...
--- FAIL: TestGoldenCorpus_PassesCheckInteractionFixture/claude-bash-pretooluse-no-escalation.json
    interaction_test.go:195: permission-request-bash hook_payloads[2]: event "PostToolUse" shapes 1 item(s) but declares no capture=raw row, so its body is flattened before shaping
```

**5. The `file_change` carries the tool run's ref** (`fc.Ref = b.ToolUseID`), which would fold a
change and a run under one `item_id` in `itemIDLocked`:

```
--- FAIL: TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems/claude-edit-permissionrequest-run1.json
        interaction_test.go:174: item 6 mismatch:
```

A sixth is not a mutation but the measurement behind §4: deleting the `b94Allowed` row.

---

## 7. Concurrency note (resolved; not caused by this slice, and not acted on by it)

Mid-slice, a concurrent agent's scrub left this shared worktree momentarily at bare HEAD, with
the two owner rulings of 2026-08-07 and their ADR/schema amendments sitting in a stash and
orphaned untracked test files that no longer compiled. This slice did **not** pop, apply or drop
that stash — a stash belongs to whoever created it, and this agent has no business reverting
nineteen files of someone else's work — and reported it instead.

**Resolved by the orchestrator**: the tree was fully recovered via `apply`, and is HEALTHY and
AUTHORITATIVE. The remaining `stash@{0}` entry (`rc-lint-baseline`) is a **stale near-duplicate**
that the live tree is now AHEAD of, in `a1-integration.md` and
`phaseb_reachability_test.go` — precisely the two tracked files this slice edits. Its disposal is
the orchestrator's; nothing here should pop it.

Recorded because it is a real operational hazard of several agents sharing one worktree, and
because it dates the transcripts: everything above was taken before the interruption, and §8
re-runs the whole slice against the recovered tree so the evidence rests on the live state.

---

## 8. Final verification, on the recovered tree

Every command below was run after the §7 recovery, so this is the state the slice actually
leaves behind.

```
$ go build ./...                                    BUILD OK
$ go vet ./...                                      VET OK (no output)
$ gofmt -l internal/adapter/claude/ internal/verify/phaseb_reachability_test.go
                                                    (empty — clean)
$ go test ./... -p 1                                EXIT=0, 57 packages ok, 0 FAIL
$ go test ./internal/adapter/... -race -count=1     all 9 packages ok
ok  	github.com/Nathandela/swarm/internal/adapter/claude	3.145s
ok  	github.com/Nathandela/swarm/internal/adapter	2.378s
$ go test ./internal/verify/ -run TestB94_... -count=1 -v
    phaseb_reachability_test.go:319: B94: 541 exported symbols examined, 54 unreachable and all accounted for
--- PASS: TestB94_EveryExportedSymbolIsReachableFromProduction (3.20s)
```

**Two runs are recorded honestly rather than only the green one.** An earlier
`go test ./... -p 1` exited 1 on a single failure — `mobile/conformance`'s
`TestPBBIND6_SlowCallbackDoesNotStallTheCoreAndOverflowIsObservable`, *"timed out after 5s: no
overflow was ever surfaced"* — and a later attempt was SIGKILLed at 39 packages in. Both are the
shared machine, not this slice: several agents were running full suites concurrently (one
reported `internal/skeleton` alone taking 190 s against its usual few seconds), and a test with a
5 s deadline is the first thing to break under that. It passes in isolation —
`ok github.com/Nathandela/swarm/mobile/conformance 15.943s` — and this slice touches nothing
under `mobile/` or the phone core. The `EXIT=0` line above is a clean run of the whole suite once
the machine quietened, with zero `FAIL` lines.

Similarly, one `-race` run of `internal/adapter/...` failed with `PostToolUse ... declares no
capture=raw row` while another agent had `claude.go` open mid-write; the source on disk was
correct before and after, and the re-run is the `ok` above. It is the same text mutation 4
produces deliberately (§6), which is the fence behaving correctly on a torn read, not a flake in
the fence.

`golangci-lint run ./internal/adapter/... ./internal/verify/...` reports 9 issues (4 errcheck,
5 staticcheck), and **none of them is in a file this slice wrote or edited** — the flagged files
are `adapter/adapter_test.go`, `adapter/interaction_test.go`, `adapter/agy/{agy.go,engine_test.go,helpers_test.go}`,
`adapter/codex/helpers_test.go` and `adapter/detect/modelprobe.go`, all pre-existing. Baseline
unchanged.

One pre-existing gofmt finding, `internal/verify/phaseb_evidence_test.go`, is untouched by this
slice and left alone. `phaseb_reachability_test.go` needed `gofmt -w` because the comment added
above the allowlist row split a map-literal alignment group; that is this slice's own change and
is formatted.

---

## 9. The chain test — the real producer, over the whole stack, to the screen

`internal/skeleton/interaction_e2e_test.go` proved the CARRIAGE with a scripted double in the
adapter seat, and said so in its own header: *"the one hop this does not prove is the producer's
own content: a real CLI's hook body shaped by a real adapter into real items. That is the Claude
Code / Codex producer slice, excluded from this program by the task, and it is exactly the seam
the double occupies."* That producer is §1–§6 above. This section closes the sentence.

**New file**: `internal/skeleton/interaction_chain_e2e_test.go`,
`TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone`. No production
file was changed by this section — the whole chain already existed; what did not exist was a run
of it with the shipped shaper in the loop.

### The hops, and which are real

| hop | what runs |
|---|---|
| corpus | REAL RECORDED BYTES — `internal/adapter/claude/testdata/interaction/*.json`, read from the producer's own directory and **not copied**. A second copy would drift from the corpus the golden table in §5 is written against. |
| shaper | REAL — `claude.New()`'s `InteractionSource`, parsing its own recorded hook bodies. |
| producer | REAL — `Validate` → §2 envelope → the D7 tuple and the content seal → §5's caps → ADR-010 §7's `ItemAdmission` floor → journal. |
| gateway | REAL, and a **separate process** (`cmd/swarm-remote`), as s19 runs it. |
| relay | REAL `internal/remote/relay.Server` over a real localhost WebSocket. |
| phone | REAL `swarmmobile.App` over a durable `phonecore.Core`, paired through the production ceremony. |

It reuses the s19 rig on `interaction_e2e_test.go`'s terms and does not touch `TestPBE2E1`.

**Where it enters, and why.** At `captureInteractions` — the daemon's production capture entry
point, the one `serveHookInteractions` itself calls — with the recorded `HookPayload`. It does
**not** enter at `hookclient.Post`, because the CLI's own body does not survive that hop today.
That is §10, and it is the headline finding of this section rather than a footnote.

### RED, verbatim

The test was first written in its natural form: replay each recorded `HookPayload` exactly as
recorded, `received_at_ms` included. Every transcript assertion passed — the five items, the
folded tool runs, the file change, the card, the shared turn — and the approval leg failed:

```
$ go test ./internal/skeleton/ -run TestClaudeChainE2E_... -count=1 -v
=== RUN   TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone
2026/08/07 23:10:50 skeleton: grid-tap snapshot failed for session f25jewvwwgg5n3qy (1 total): dial unix /tmp/s193705621525/f25jewvwwgg5n3qy/shim.sock: connect: no such file or directory
    interaction_chain_e2e_test.go:207: PendingApprovals holds 0 card(s) ; want exactly the unresolved "01KZF110RQYQYERNZSYVJHN0P1"
--- FAIL: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (7.75s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	8.777s
FAIL
```

"Zero cards" does not say **why** the card went, and §3.6 has six ways for one to go. The
assertion was strengthened with a diagnostic (`resolutionsOn`, which lists `decision/by` for
every `approval_resolved` the phone holds) and re-run, which named the cause:

```
$ go test ./internal/skeleton/ -run TestClaudeChainE2E_... -count=1 -v
=== RUN   TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone
2026/08/07 23:11:41 skeleton: grid-tap snapshot failed for session ekfwbkshvzrgxbdk (1 total): dial unix /tmp/s194120450708/ekfwbkshvzrgxbdk/shim.sock: connect: no such file or directory
    interaction_chain_e2e_test.go:208: PendingApprovals holds 0 card(s) ; want exactly the unresolved "01KZF12JERPG2PS1VCQBDEEFNZ". The resolutions the phone already holds are [expired/daemon] -- a card resolved before anyone answered it is a request the owner never got to decide
--- FAIL: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (7.41s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	8.509s
FAIL
```

**`expired/daemon`.** The fixtures' `received_at_ms` is `1784379271002` — 2026-07-18, when the
S-B recording was taken. `shapeItem` uses `p.ReceivedAtMs` as the item's `ts` (deliberately: the
CAPTURE instant, not the append instant, PB-APP-11), and `openApprovalLocked` derives the
daemon-authoritative window from it as `it.TS.Add(approvalTTL)`. Replayed verbatim, the card was
born three weeks expired and the expiry sweep — which runs on the append floor's own 125 ms
ticker — resolved it before anything could answer.

**The defect is the REPLAY's, not production's, and the fix is in the test.** ADR-010 §3 makes
timestamps daemon-authoritative; a fixture's `received_at_ms` records when *the recording* was
taken and is not an instant at which *this* daemon captured anything. `replayClaudeCorpus` now
replays the **bodies verbatim** and stamps the daemon's **own** capture instant, which is exactly
what the carriage of §10 will supply in production. Nothing under `internal/` moved.

### GREEN, verbatim

```
$ go test ./internal/skeleton/ -run TestClaudeChainE2E_... -count=1 -v
=== RUN   TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone
2026/08/07 23:12:22 skeleton: grid-tap snapshot failed for session 4tvjipo6hjgafwc7 (1 total): dial unix /tmp/s192505030998/4tvjipo6hjgafwc7/shim.sock: connect: no such file or directory
--- PASS: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (8.85s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	9.925s

$ go test ./internal/skeleton/ -run TestClaudeChainE2E_... -count=1 -race -v
--- PASS: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (11.30s)
ok  	github.com/Nathandela/swarm/internal/skeleton	13.666s
```

(The `grid-tap snapshot failed` line is the fake agent's shim socket closing at teardown; it is
present in every s19-rig run in this package and is unrelated.)

### What it asserts, and why each is not decoration

**Leg 1 — one recorded Edit turn, whole.** Seven records, five items, all the way to the facade.

- the **prompt** the owner typed, verbatim across five hops;
- **two** `tool_run` items, not four — the open and the close carry one `tool_use_id`, the daemon
  folds them under one `item_id` (`itemIDLocked`) and the phone folds by `item_id` (IS-ENV-2,
  IS-DELTA-3). Both `completed`: an item left `in_progress` is a row that spins forever;
- the Read run's §7 `action` (`{type:read, path:…}`, produced machine-side per IS-TOOL-1) and its
  `output_excerpt` — the only thing on that row a human reads;
- the `file_change`: path, `modify`, +1/−1, and the hunk rendered from the recorded
  `structuredPatch` (`@@ -1,3 +1,3 @@ … -line two +line TWO EDITED …`);
- the card: summary from the LITERAL action, `mode: card`, and `decisions[]` carrying Claude
  Code's own `allow`/`deny` ids with the `Yes`/`No` labels IS-APR-3 makes the phone render;
- **one turn holds all five** (IS-ENV-1). A transcript whose rows carry no common turn cannot be
  grouped into the exchange that produced them, which is the whole of what makes it a transcript
  and not a log.

**Leg 2 — the allow verdict, phone to machine and back.** The approve is built from
`TranscriptItem.Body` — the card **as the phone received it** — because IS-APR-2 forbids the
phone computing or adjusting `content_hash` or `expires_at`. That is what makes it a round trip
rather than the daemon validating against itself. Then: `decision: allowed`, `by: phone`,
`operation_id: op-allow`; the card leaves `PendingApprovals`; the request is still **in** the
transcript and marked `Resolved` — IS-LIFE-3's retention exemption **lifting**, which is the half
only an end-to-end run can see, because the producer and the retention rule live in different
processes.

**Leg 3 — the deny verdict, on a second recorded request.** Deliberately the *other* fixture,
which is S-C's **carve-out**: `touch approval-test.txt` names a file path, so the card arrives
`mode: prompt_card`. `deny` resolves `denied`. The two legs differ **in exactly one string** —
the decision id — and `deny` carries nothing about polarity on the wire. The only thing that
makes one a refusal is the `Verdict` the ADAPTER attached at capture (owner ruling 2026-08-07),
so this leg is the end-to-end proof of that ruling.

Both cards are also checked for a `keystrokes` leak (IS-APR-3 keeps the map off the item,
IS-LIFE-6 keeps the phone out of the keyboard). §11 mutation 3 is why that check sits where it
does.

### One observation, recorded rather than asserted

Mutation 2's failure output printed the phone's transcript in order:
`user_message,approval_request,tool_run,tool_run,tool_run,tool_run`. The `approval_request`
arrives **ahead of** tool runs that were captured before it — ADR-010 §7 gives it the head of the
queue (IS-DELTA-3: "never merged" and "never delayed" are different guarantees, and only the
first is compatible with the ceiling). The test therefore asserts **presence, folding, content
and turn**, and deliberately asserts **no cross-item ordering**: a test that pinned kind order
would be pinning the floor's priority as if it were transcript order, and would fail the day a
second approval reorders one window.

---

## 10. The carriage gap — the producer is not reachable from a real hook post

**This is the finding, and it is not hedged: the producer of §1–§6 shapes NOTHING in production
today.** Not because it is wrong — the chain test proves it right — but because the CLI's own
hook body never reaches it.

ADR-010 §6 specifies the carriage, and it is Accepted:

> `parseHookStdin` keeps the whole body, under the existing 1 MiB `hookStdinLimit`, only for
> events whose descriptor declares `capture=raw`; its string-flattening loop and its
> `turn`/`interaction` injection guard are untouched. **`engine.Callback` gains
> `Raw json.RawMessage` alongside `Payload`.**

Nothing implements it. `engine.Callback` has `Payload map[string]string` and no `Raw`;
`cmd/swarm`'s `parseHookStdin` (`main.go:673`) extracts **top-level STRING fields only**, so
`tool_input` and `tool_response` — objects — are dropped entirely; and `serveHookInteractions`
hands the shaper the callback **envelope** instead, which its own comment records as provisional
(*"belongs to the producer slices this program excludes, so until one lands this is the callback
envelope"*). A producer slice has now landed, so that sentence has expired.

**Measured, not read off the code.** A temporary probe (deleted after the measurement; it
replicated `parseHookStdin`'s flattening, which is in package `main` and not importable) ran the
recorded corpus through both paths:

```
$ go test ./internal/skeleton/ -run TestZZCarriageProbe -count=1 -v
    PreToolUse: recorded body -> 1 item(s); shipped hook post -> 0 item(s)
      flattened payload keys the envelope carries: [cwd tool_name tool_use_id session_id prompt_id permission_mode hook_event_name transcript_path]
      envelope the shaper actually sees: {"session_id":"s-1","token":"tok","sequence":1,"event":"PreToolUse","payload":{"cwd":"/Users/Nathan/spike-sb-work","hook_event_name":"PreToolUse","permission_mode":"default","prompt_id":"4d0825f3-c859-4643-8e57-9c7be9d0345b","session_id":"e8a4368a-7ec7-4c19-a46f-365d39099447","tool_name":"Read","tool_use_id":"toolu_01RKGYmCbHp7NkRPNoaZ6zrb","transcript_path":"/Users/Nathan/.claude/projects/-Users-Nathan-spike-sb-work/e8a4368a-7ec7-4c19-a46f-365d39099447.jsonl"}}
    PreToolUse: recorded body -> 1 item(s); shipped hook post -> 0 item(s)
    PermissionRequest: recorded body -> 1 item(s); shipped hook post -> 0 item(s)
      flattened payload keys the envelope carries: [hook_event_name tool_name session_id transcript_path permission_mode cwd prompt_id]
--- PASS: TestZZCarriageProbe (0.01s)
```

**Zero, on every capture row.** Two things are visible in those keys and both matter:
`tool_input` is absent from the flattened payload altogether (it is an object, so the flattener
drops it), and `tool_name` — which every one of the shaper's four rows guards on — survives only
*nested under `payload`*, where a top-level read finds nothing. So `Interactions` returns `nil`
and the transcript stays empty. A `PermissionRequest` in particular carries **no** `tool_use_id`
at all, so even if the nesting were fixed the request content is still gone.

**Why this slice did not build it.** It is not glue. It is a change to the hook callback wire, a
change to `cmd/swarm`'s hook role, and one design question ADR-010 §6 does not settle:
`swarm hook <event>` runs as a *child of the CLI* and knows the event name but **not the
adapter**, so it cannot itself decide which rows declare `capture=raw`. Closing that needs either
a new spawn-injected environment variable listing the capture rows (a fourth `hookclient.Env*`,
written by `internal/daemon/launch.go`) or a decision to carry the body on every row and gate it
daemon-side — which trades UDS bytes for simplicity and is a deviation from §6 as written.
Choosing between those inside a test task, and shipping it without its own failing-first cycle,
would be exactly the drift `docs/adr/README.md` forbids.

**NEEDS AN OWNER RULING / A SLICE OF ITS OWN.** The chain test is that slice's acceptance test,
and it is already green: wire the carriage, change `replayClaudeCorpus` to post over
`hookclient.Post`, and everything below the entry point is proven unchanged.

---

## 11. Teeth — five mutations, each applied for real, each reverted

Reverted from a **filesystem backup**, never `git checkout` — that is the process incident
recorded in the A1a handoff, where reverting mutations on uncommitted work destroyed the GREEN
edits in five production files.

| # | mutation | file | caught by | verdict |
|---|---|---|---|---|
| 1 | the verdict split never matches `deny` | `skeleton/approval.go` | leg 3's `denied` assertion | CAUGHT |
| 2 | `itemIDLocked` mints a fresh id per record (the fold breaks) | `skeleton/interaction.go` | leg 1's `tool_run` count | CAUGHT |
| 3 | `interactionFields` emits `keystrokes` on the item | `skeleton/interaction.go` | **nothing — see below** | MISSED, then caught |
| 4 | the S-C carve-out branch is disabled | `adapter/claude/interaction.go` | leg 3's `prompt_card` assertion | CAUGHT |
| 5 | `resolveLocked` stops marking the request resolved | `phonecore/interaction.go` | the exemption-lifting wait | CAUGHT |

**1 — the verdict is what makes a refusal a refusal.**

```
    interaction_chain_e2e_test.go:286: the resolution's decision = allowed; want `denied`. `deny` is the CLI's own id and carries nothing about polarity on the wire -- the ONLY thing that makes this a refusal rather than an approval is the Verdict the ADAPTER attached at capture, which is why the two legs of this test differ in exactly one string
```

**2 — the fold.** Note the transcript inside the message; it is the ordering observation of §9.

```
    interaction_chain_e2e_test.go:125: the phone holds 4 tool_run item(s); the recorded turn made TWO tool calls and emitted FOUR records for them. A count of 4 is the fold failing (IS-DELTA-3/IS-ENV-2); items: user_message,approval_request,tool_run,tool_run,tool_run,tool_run
```

**3 — MISSED, and it exposed a real defect in the test.** The keystroke-leak check was written
against the leg-1 card. That card is `mode: card`, and `claude`'s `approvalFrom` produces a
keystroke map **only** on the prompt-card path — so there was nothing there to leak and the check
could never fire. The suite stayed green with the leak live:

```
$ go test ./internal/skeleton/ -run TestClaudeChainE2E -count=1
ok  	github.com/Nathandela/swarm/internal/skeleton	11.101s
```

Fixed by **strengthening**, never weakening: the check was lifted into `assertNoKeystrokeLeak`
and called on **both** cards, with a comment recording that the prompt-card call is the one that
bites. Re-run against the same live mutation:

```
    interaction_chain_e2e_test.go:279: a prompt_card approval_request carried a `keystrokes` map to the phone: map[allow:1 deny:]. IS-APR-3 keeps it off the item and IS-LIFE-6 keeps the phone out of the keyboard
```

(`deny:` prints empty because its keystroke is the ESC byte `\x1b`.) This is the same class of
finding as §2's vacuous `CheckInteractionFixture` call: a check that cannot fail is not a check.

**4 — the carve-out.**

```
    interaction_chain_e2e_test.go:269: the Bash card's mode = card; want `prompt_card` -- S-C's carve-out is decided at capture, and a `card` here renders a one-tap button that silently leaves the session blocked behind a dialog nobody is at the machine to answer
```

**5 — the retention exemption.** It fails by TIMEOUT rather than by assertion, which is correct:
an exemption that never lifts is a card that never goes away, and there is nothing to compare
against — only something that fails to happen.

```
    interaction_chain_e2e_test.go:243: timed out after 45s: the answered card left the phone's pending set
--- FAIL: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (53.45s)
```

All four mutated files were verified byte-identical to their backups after reverting (`cmp` on
`skeleton/approval.go`, `skeleton/interaction.go`, `adapter/claude/interaction.go` and
`phonecore/interaction.go` — all IDENTICAL), and `grep -rn MUTATION internal/ cmd/ mobile/`
returns only pre-existing "MUTATION CONTROL" comments in other packages' tests.

**On the line numbers in the transcripts above.** They are verbatim from the run that produced
them, so they straddle the mutation-3 refactor: mutations 1 and 2 (and both REDs of §9) ran
against the file *before* `assertNoKeystrokeLeak` was extracted, mutations 3-caught, 4 and 5
after. In the file as it now stands the same assertions sit at 125, 214, 243, 269, 279 and 288 —
mutation 1's `:286` is the only number that moved, by two lines. `:279` is the *call site*, not
the helper body at 382: `assertNoKeystrokeLeak` calls `t.Helper()`, so a failure is reported
against its caller, which is the point of putting it there.

---

## 12. Final verification for §9–§11

```
$ go build ./...                                          BUILD OK
$ go vet ./...                                            VET OK (no output)
$ gofmt -l internal/skeleton/interaction_chain_e2e_test.go
                                                          (empty — clean)
$ go test ./... -p 1                                      EXIT=0, zero FAIL lines
ok  	github.com/Nathandela/swarm/internal/skeleton	191.481s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	0.783s
ok  	github.com/Nathandela/swarm/internal/phonecore	8.540s
ok  	github.com/Nathandela/swarm/mobile	20.012s
ok  	github.com/Nathandela/swarm/internal/verify	9.528s
(56 packages ok, 5 [no test files], 0 FAIL)

$ go test ./internal/skeleton/ -run TestClaudeChainE2E -count=1 -race -v   # on a quiet machine
--- PASS: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (11.60s)
ok  	github.com/Nathandela/swarm/internal/skeleton	14.143s
```

`golangci-lint run ./internal/skeleton/...` reports 27 issues (24 errcheck, 3 staticcheck) and
**none is in `interaction_chain_e2e_test.go`** (`grep -c` on the new file returns 0). The two
`gofmt -l` findings in that package, `internal/skeleton/api.go` and
`internal/skeleton/revoke_reaudit_test.go`, are both at HEAD and untouched by this slice.
Baseline unchanged.

**Files this section adds or edits:**

- `internal/skeleton/interaction_chain_e2e_test.go` — NEW, the chain test.
- `docs/verification/a1b-claude-producer.md` — this evidence (§9–§12).

No file under `internal/` outside that one test was modified. Nothing was committed, per the
task's do-not-commit rule; the working tree is the deliverable.
