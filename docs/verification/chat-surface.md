# Conversation surface — verification evidence

Plan of record: `docs/specifications/chat-surface-plan.md`. Epic: `agents-tracker-tbpm`.
One section per slice; no slice closes without its section.

---

## Slice 0 — serialise the writer (`agents-tracker-bzfe`)

**Claim.** A message from the phone is delivered as *one message* — its text and the carriage
return that runs it cross the PTY's only serialized writer under a single hold of its lock — or it
is refused having written nothing. Neither a second phone send nor the owner's own half-typed line
can land between the two halves.

### RED, and it reproduced both defects

`internal/skeleton/s0_writerserialise_test.go`, run against `main` at `b6bb0db` before any
implementation existed:

```
--- FAIL: TestSlice0_TwoPhoneSendsAreNotMergedIntoOneSubmit (3.50s)
    the session's stdin saw ["alphabravo" ""], want ["alpha" "bravo"].
--- FAIL: TestSlice0_AnOwnerDraftIsNeverMergedWithAPhoneSend (0.33s)
    phone send against a dirty input line = code "" err <nil>, want "input_busy".
```

The first line is the defect the audit committee ranked above the whole redesign, reproduced
verbatim: **two messages went in, one submitted concatenation and one empty submit came out.** It
needs no concurrency luck — the second send is issued inside the first's `submitframe.Gap`, which
`injectComposerText` holds open by design between the text and the CR.

The second is B13, disclosed in `skeleton/chat.go` in prose since Wave R6 and stated here as a
test for the first time.

Both failures are behavioural, not undefined-symbol: `schema.CodeInputBusy` was added before the
run so the tests could fail on what the code *does*, not on what it lacks.

### GREEN

- `internal/shim/inputline.go`, `server.go` — `ptyWriter` conservatively tracks the owner's
  logical line from characterized character insertion/deletion, complete horizontal/home/end
  navigation, line kill and submit operations. A known draft deleted back to empty becomes clean;
  provider-owned word/Meta keys, history/completion, an in-progress paste, or a lone/incomplete
  escape sequence remains busy. Emulator replies and provenance-carrying daemon control keys do
  not mutate this tracker.
- `internal/shim/server.go` — `submitMessage` requires the tracked line to be provably clean,
  writes the text, waits `submitframe.Gap`, writes the CR, all under one `mu` hold, or returns
  `errInputBusy` having written nothing. A partial transaction (text in, CR failed) records the
  text as dirty rather than pretending the line is clean.
- `internal/shimwire/shimwire.go` — `TypeSubmit` / `TypeSubmitResult`, the stable
  `RefusedInputBusy` token, and the `SubmitTransaction` hello capability.
- `internal/protocol/fromdaemon.go` — `shimStream.Submit`, one transaction in flight per stream,
  answered on the same connection; `ErrSubmitUnsupported` for a shim that predates it.
- `internal/protocol/types.go` — `MessageSubmitter`, an optional interface rather than a sixth
  method on `SessionStream`, so no test double grows a method it has no PTY to implement.
- `internal/skeleton/sessiontap.go` — `tapSub.Submit`, mode-gated like `Input`.
- `internal/skeleton/chat.go` — `injectComposerText` prefers the transaction and falls through to
  the old two writes only on `ErrSubmitUnsupported`; `composerSend` maps `ErrInputBusy` to
  `CodeInputBusy`.

```
go test ./internal/skeleton/ -run 'TestSlice0_' -count=1
ok  	github.com/Nathandela/swarm/internal/skeleton	4.746s
```

### Gates

| Gate | Result |
|---|---|
| `go build ./...` | clean |
| `go test ./...` | green, one pre-existing filed flake: `TestE2E_ReplayProductionPath_AgyOpencode` (`agents-tracker-oqx0`, "3s busy-hold bound misses by ~70ms under CPU contention") — passes in isolation, and it was filed before this work |
| `go test -race ./internal/skeleton ./internal/shim ./internal/protocol ./internal/shimwire` | green (399s / 93s / 24s / 3s) |
| `go vet ./...` | clean |
| `golangci-lint run` (v2, `~/go/bin`) | `0 issues.` |

### What this deliberately does not claim

It never derives the CLI's input region from the rendered grid and does not claim a provider-wide
`expected_input_revision`. What changed is the question: under the PTY's only serialized writer,
the shim can conservatively determine whether the owner's logical line is **provably clean** from
operations whose effects are characterized. Provider-dependent word/Meta bindings, history,
completion and lone/incomplete escape sequences are unknown and therefore busy. The revision
never crosses the wire; only this conservative predicate does. The ADR-017 amendment records the
substitution rather than letting a different mechanism quietly satisfy a named obligation.

It errs safe: a known draft deleted back to empty becomes clean; a real draft or uncharacterized
editor state refuses. False refusal under uncertainty is chosen over prompt corruption.

### Residual, disclosed

A shim that predates the transaction answers `ErrSubmitUnsupported` and the daemon degrades to the
two unlocked writes — reachable only between a daemon upgrade and the shim restart that replaces
it. The merge is otherwise exclusively a property of the keystroke branch: the backend arm never
touches the PTY, and the only `ComposerKeys` implementor in the tree is Claude.

### Closing gates now present

- `s0_realclipty_test.go` covers a real Claude PTY retaining a genuine owner draft and, in the
  opt-in billable arm, accepting after the owner deletes a known draft.
- `s0_concurrentwriters_test.go` covers concurrent owner Enter, phone send and distinct phone
  sends without splitting or concatenation.
- `s0_turnmoved_test.go` covers turn closure/start between `expected_turn` validation and delivery
  for both sinks.
- Plan row 0.6 is mapped through `MachineRefusalCodes.toToken`: `input_busy` retains the draft and
  surfaces the phone remedy rather than falling through to `ErrorState.UNKNOWN`.

---

## Wave C — the kit gains five components (`agents-tracker-tbpm.2`)

**Claim.** Every visual element the conversation needs exists as a kit factory with a derivation a
reviewer can check, and the bidirectional registry gate proves neither half can outlive the other.

Rows 28-32 of `docs/design/substrate-components.md`: Conversation menu, Gap divider, Earlier chip,
File change row, Decision pill. Six factories, because `overflowControl` and `conversationMenu` are
separate controls in one file (textField / composerBar's precedent on row 9).

### RED, in both directions

The gate is the test. Run both times as
`go test ./android/gate/ -run 'TestPBDS6|TestPBDS7|TestPBDS12'`, against a baseline of
`ok github.com/Nathandela/swarm/android/gate 10.633s`.

**Direction 1 — the registry entry added before the factory exists:**

```
--- FAIL: TestPBDS6_EveryInboxComponentIsAKitFactory (0.02s)
    s23_kit_test.go:869: PB-DS-6: the kit has no ConversationMenu.kt, which is where overflowControl() lives.
    s23_kit_test.go:869: PB-DS-6: the kit has no GapDivider.kt, which is where gapDivider() lives.
--- FAIL: TestPBDS6_EveryKitFileIsClaimedByAFence (0.01s)
    s23_kit_test.go:980: PB-DS-6: s23OwnedFiles in this file claims dev/swarm/phone/ui/kit/ConversationMenu.kt
    and no such file exists. A claim over nothing makes the forward direction above pass by having less to
    check: the scans skip the missing name, report no fault, and the inventory goes on describing a kit that
    is not there.
--- FAIL: TestPBDS12_EveryStatedTouchTargetIsSpentByItsComponent (0.02s)
    s23_kit_test.go:2766: PB-DS-12: `#28 Conversation menu` is not a row in
    docs/design/substrate-components.md. A floor assigned to a row nobody can find is a floor nobody can check.
```

**Direction 2 — the factory added before the derivation row exists:**

```
--- FAIL: TestPBDS7_EveryDerivationCitationResolvesToARow (0.01s)
    s23_kit_test.go:1022: PB-DS-7: FileChangeRow.kt cites `#31 File change row`, and no such row exists in
    docs/design/substrate-components.md. Either the row was renamed and the component now paints to a
    specification nobody can find, or the citation was written from memory.
```

**GREEN** once rows 28-32 landed: `TestPBDS6_*` (three), `TestPBDS7_*`, `TestPBDS12_*` (three) all pass,
`ok github.com/Nathandela/swarm/android/gate 0.967s`.

### GG-5 deviation, disclosed

**The Kotlin layer of this wave did not go test-first**, and the lane that built it said so without
being asked. The designated RED was the Go bidirectional gate, run in both directions above; the five
component suites were written *after* their components, and `Composer.kt` was edited before its
tests. Against the pre-change tree those five suites do not fail on an assertion — they fail to
compile (`Unresolved reference: conversationMenu`), which is the compile-RED on frozen symbols this
repo already uses, but it is a weaker instrument than a failing assertion and it is recorded as such
rather than counted as one. The four `Composer.kt` assertions do have predicted failure messages and
they are in the lane report; they are predictions, not observations.

This is written down because GG-5 says the failing-first run must be *evidenced*, and an unevidenced
half of a wave that reports green is exactly the shape a reader cannot see.

### Two existing assertions reversed, neither quietly

- `ComposerSendStateTest.aSendWalksPendingSentAndShowsEachState` pinned
  `stateLabel(SENT).isNotEmpty()` — inside the same method whose own failure message calls that
  rendering a lie. Reversed; the property it protected (every state tellable apart) survives, now
  carried by the bubble's surface (derivation row 26) rather than by a word.
- `staleTurnGetsItsOwnGentleNotice` carried
  `assertFalse(stale.copy.lowercase().contains("sent"))` — a substring guard that any negation
  defeats, and the tabled copy is a negation ("Not sent — ..."). Replaced with
  `startsWith("Not sent")`, which is strictly stronger: it cannot be met by copy claiming delivery,
  while the old one could be met by copy saying "delivered".

### Copy is extracted, never retyped

Both strings the lane landed were pulled out of the drawing's `bubble.refused` and `bubble.stale`
rows and asserted present in the source, rather than typed from reading. This is now the rule for
this wave: retyping copy is how the tree came to hold **five** different sentences for the single
fact that a turn moved on (`Composer.kt` twice, `ErrorRouting.kt:306`, and the sheet's own), which is
what the audit found and what the convergence pass fixes.

### One string left the product

`Composer.kt`'s ENDED detail — "Its conversation is kept; there is nothing to type into." — was on
screen and on no copy sheet. It is deleted rather than tabled: the other three shut reasons all end
in a remedy at the machine, and an ended session has nothing on the other side to type at, so a
second line can only restate the first.

---

## Wave D — the model says what the wire says (`agents-tracker-tbpm.3`, `.4`)

**Claim.** The transcript model carries the facts the drawing needs, in the words the wire actually
supplies, and refuses to invent the ones it does not.

### R8 — expanded output opens in place to a bound, then on its own screen

Bound is **20 lines**, derived rather than picked: mono code at 11.5 sp is a ~17 dp line box, so 20
lines is ~340 dp against ~650 dp of reading height — half the reading area, with conversation
visible either side. Ten would route an ordinary `go test` failure to another screen; forty rebuilds
the wall of text collapse exists to end. Head, never tail.

The label is two strings and not one template, and the reason is the interesting half: a body **one
line** over the bound is the commonest body that is over it at all, so a plural-only template gets
the likeliest case wrong. `Open in full · 1 more line` is tabled beside `· N more lines`, and a
second test guards against fixing it in the other direction.

The test file restates the bound as its own literal rather than importing the model's constant. A
test that reads the number out of the thing it tests asserts that 20 equals 20.

### R9 — a file change is a chip

The unified diff was drawn inline and unconditionally. The block now carries verb, path and counts,
and routes the diff to its own screen. Never grouped: a count of files is not a record of what
changed.

### The tear says a word, not a paragraph

`records missing · repair`, and the daemon's spool reason is no longer drawn at the reader. The
counter-argument is recorded beside the argument it answers, in the file that made it.

### What the wire does not have, found by building against it

Three corrections to the **owner-signed drawing**, each because the sheet named something the wire
cannot supply:

1. **`<label>` is not on the wire at all.** §3.6 carries `interaction_id`, `decision`, `by` and
   `operation_id` — no label — and it is most unobtainable on exactly the path the sheet described.
   The row had already been corrected once today to key on `by` and *still* carried `<label>`.
2. **`answered_locally` prints no verdict token.** It resolves precisely when the daemon did **not**
   type the answer; `approval_inject_test.go:536-543` records that the daemon observes only that the
   session's interaction dimension left the waiting state, never which button was pressed. A token
   there dresses a non-verdict as a decision.
3. **`timed out` is not a status.** The vocabulary is four values —
   `in_progress | completed | failed | declined` (`interaction-schema.md:85`) — and `timed out`
   appears nowhere in the tree. The sheet tabled it under a caption reading "the wire's own status,
   never a word of ours". Struck from the sheet and from this plan's row D.4, which carried the same
   invention.

Three resolutions that are **not** answers — `cancelled`, `superseded`, `expired` — gained their own
state, because IS-LIFE-2 guarantees every request reaches one of six ends and the sheet had a
sentence for three of them.

### Two rulings that moved the drawing rather than the code

- **No `Dismiss` on a decision this build cannot render.** The sheet drew one. "Dismiss" implies the
  question goes away; it does not — it is still blocking the agent at the machine, and a reader who
  taps it believes they closed something they did not. The tabled sentence is the way out.
- **`+N -M` and `old -> new` stay ASCII.** The sheet tabled U+2212 and U+2192; the shipping code has
  always drawn the ASCII forms. An absent glyph in the mono face is tofu on a handset, so the
  unproven claim moved and the proven code did not.

### No time inside a settled sentence

The block already draws a timestamp at its turn boundary. A second one inside the sentence is one
fact in two voices — the rule that had to be made after finding **five** different wordings of "the
turn moved on" in one tree (`Composer.kt` twice, `ErrorRouting.kt:306`, a state label, and the
sheet's own).

### GG-5, stated exactly

No Gradle was run in this lane and there is no `kotlinc` on this machine, so every claim about
compilation and about RED messages is **a reading of the source, not an execution**. Four assertions
are the exception and are genuine failing assertions against today's tree — the file-change well,
both tear assertions, and the two resolution-line ones. Everything else would surface as an
unresolved reference, which is compile-RED and is not test-first. It is not called test-first here.

The one execution in the lane: the regenerated
`internal/skeleton/testdata/i1-transcript-screen.golden.json` was parsed and the `OperationID` key
confirmed present on all 14 recorded items across both the `items` and `approvals` arrays. That
check was worth running rather than assuming — `getString` throws on an absent key, so one
un-regenerated item would have taken the whole suite down instead of failing one assertion.

---

## The bridge, and a gate that had to be made honest twice

**Claim.** The facts the phone needs reach the screen, the refusal this programme exists to produce
can be said out loud, and the sheet that signs the copy is checked by a machine rather than by
intention.

### Three facts that stopped one hop short of the screen

- **`operationId`.** `internal/phonecore/interaction.go` and `mobile/types.go` carried it with the
  R6 argument written out in full; `FacadeBridge.kt` mapped `detail`, `toolKind`, `turnId`,
  `tsUnixMs` and `source` and not this — under a comment saying those fields are mapped there *"so
  they cross the boundary instead of dying one hop short of the screen"*. The comment described the
  fix and the field was not in the list beneath it. Without it the phone cannot match an agent's
  echo to the message it sent, so **owner ruling R6 had no mechanism at all**, and the composer
  rendered the daemon's "bytes written into a PTY" as the word **Sent**.
- **`interactionId`.** `ItemFields` decoded `interaction` — §3.8's `session_status` dimension, an
  unrelated fact — and not `interaction_id`, which *is* the request's `item_id`. Pairing a
  resolution to the decision card it resolves was a wire fact the phone received and discarded. The
  two names sit one underscore apart and a reader who assumes the second is a typed-up version of
  the first will pair the wrong things plausibly; the decoder test now runs in both directions so
  neither key may fill the other's field.
- **`input_busy`.** Present in exactly one place in the tree and absent from
  `MachineRefusalCodes.toToken`, so it fell to `ErrorState.UNKNOWN` and the reader was shown *"Your
  message was refused and not delivered."* It has its own state, class and remedy now
  (`ErrClassInputBusy` / `swarm/input-busy` / `INPUT_BUSY` / `wait_and_retry` — waiting, not
  refreshing, because the terminal's line clears the moment whoever is typing presses enter).

The fence that would have caught the first two is widened in the same commit: `r6_chat_ui_test.go`
now looks for `operationId`, `interactionId` and `getOperationID`, with the reason recorded in the
file. A review found it by reading a comment against a list; the loop finds it by running.

### `source` is dead, and it stays dead on the record

`InteractionItem.source` appears exactly twice in production Kotlin — the declaration and the
bridge's write. **Zero reads.** That is D.10's shape one wave later and it was argued for wiring.

It is not wired, and the reason is the drawing: `agentProse` rules attribution out on purpose
(*"No attribution line. On a screen with one agent and one person, a name on every message is
noise; the side of the screen already says who spoke"*), and every `user_message` is the owner's
whichever keyboard it was typed at. Wiring it would be entering a state the sheet does not draw,
which R5 forbids. The distinction the surface actually needs — did *this phone* send it — is
carried by `operationId`.

It is not deleted either, and that reason is mechanical: the I1 join fails in both directions, so
dropping `getSource()` from the bridge drops `Source` out of the recording and M2.4's daemon-stamped
attribution stops being asserted anywhere. **An unasserted field is worse than a visible dead one.**
D.10's defect was a field computed and read by nothing with nobody able to say why; this one is
dated, argued, and has a named condition for becoming live (a second paired handset, ADR-018).

### The copy gate, including the two times it was not honest

Section 03 of the drawing said *"The gate checks copy as recorded text."* No test read the sheet.
The proof was a sentence shipping in `Composer.kt` for the ENDED composer that appeared on no row,
with nothing failing.

1. **Retracted.** The sheet was reworded to say copy is checked by review. True, and weak.
2. **Built.** `scripts/check-conversation-copy.py` + `internal/verify/conversation_copy_test.go`,
   following `check-phaseb-manifest.py`'s precedent exactly, including its stated reason: *an exit-0
   check is indistinguishable from a check that cannot fail.* Four negative controls — a retyped en
   dash, a sentence deleted from the code, a row deleted from the sheet, and a sheet whose table
   cannot be parsed at all — each mutating a **copy** of the inputs in a temp dir, never the shared
   working tree, since several agents were compiling those files concurrently.
3. **Overclaimed, and caught by the lane whose script it was.** The restored sentence said "checked
   in both directions" over a check covering **3 of 27 rows in one direction**. That is this wave's
   own defect one layer up. Widened to 13 rows / 18 bindings by sweeping the sheet against
   production Kotlin; the checker now prints `N binding(s) checked across the rows whose screens are built tabled row(s),
   ONE DIRECTION` on every run, and the sheet says "in part" with the missing half described.

**The widening exposed a vacuity nobody had spotted.** The check matched whole files including
KDoc — and these files carry long comments that quote the very sentences being checked, because the
argument for a piece of copy sits directly above it. **Four bindings were passing on comments
alone.** Comments are stripped before comparison now, deliberately over-eagerly, so the stripper can
only ever remove more than a perfect parser would and never manufacture a pass.

**And then it earned itself.** `Composer.kt:145` shipped

```
"Messages are never held - nothing is sent when the link returns."
```

with an ASCII hyphen where the sheet signs an em dash. It renders almost identically, reads
identically, and is a different string — the near-miss no reviewer catches — on the one sentence the
honesty review singled out as the fact a user cannot deduce from a greyed-out field.

**The reverse direction is not built**: every shipped string being *on* the sheet. It is the half
that catches a rival wording being added tomorrow, and it would have passed all four of this tree's
competing stale-turn sentences, because one of them was correct. Filed as its own task with the
method recorded, rather than implied by a green gate.

---

## Wave G — the terminal stops claiming a takeover (`agents-tracker-tbpm.9`)

**Claim.** The terminal reports what the daemon observes and nothing more, and the fact ADR-010
Amendment 3 C3 depends on survives the deletion of the lease it used to read.

### The prerequisite the plan sequenced late

`anyControlled` — "the supervisor never types into a session someone is driving" — read leases, and
`composer_send` has never taken one. Deleting take-control naively would have made that gate answer
false for every phone, so the passive supervisor would type notifications into a live conversation.
The replacement fact had to exist first.

### RED, then GREEN

`internal/skeleton/s0b_phonepresence_test.go`, five tests. The daemon records when a remote message
was **delivered**, ages it out after `phoneActiveHorizon` (2 min), and `anyControlled` consults it.
A refused send records nothing.

**Negative control** (scratch git worktree at `HEAD`, never the shared tree): removing the
`phoneRecentlyActive` clause from `anyControlled` and the `notePhoneActivity` calls from `chat.go`
fails four of the five tests, and they fail **separately**, each in its own words. The fifth —
"a refused send records nothing" — correctly stays green, because it asserts an absence.

### What the terminal says now

| Before | After | Why |
|---|---|---|
| `phone control` | `phone` | Control was a lease. The marker's own presence carries the recency claim: it appears when a phone sends and disappears when the horizon passes. |
| `took over from phone` | *(deleted)* | Asserted an eviction M0.1 disproved, and was sampled once before the dial, so it could not have stayed true even if it had been. |
| — | *(nothing yet)* | A live in-attach indicator needs a side channel `attach.Session` does not have. A row that says nothing beats a row that says something untrue. |

Each pinned assertion is classified DELETED or MOVED in `hintrow_takeover_test.go`'s header.

Two further comments describing a build that no longer ships were corrected: the schema field still
explained that an attach and a phone lease are mutually exclusive "so no live in-attach indicator is
possible", and `chat.go` still said no screen calls `availabilityFor`.

### Owed before this is called complete

This section carried no such list, and the asymmetry with Slice 0's was itself a finding — a wave
with no owed list reads as finished.

- **The marker ships as a bare `phone`; the drawing and plan G.2 both signed `phone sent HH:mm`.**
  The table above argues the short form ("the marker's own presence carries the recency claim") and
  that is true of the mechanism and invisible to the reader: a bare noun standing beside
  `supervisor pending` / `supervisor gone` reads as a *condition* — a phone is on this session —
  which is precisely what G.5 and the drawing's "it is not `phone is here`" rule out. Worse,
  `internal/skeleton/phonepresence.go` asserts the signed wording three times (`:22`, `:34`, `:51`)
  as though it shipped. The instant is already recorded — `phoneActivityAt` returns an instant
  rather than a boolean for exactly this reason — and is spent by nothing but the boolean.
- **G.3 (`phone sent 2 messages · 09:41`) is unbuilt** and was disclosed only by its absence from
  the table.

---

## The echo carries its id (`agents-tracker-tbpm.4`, part)

Owner ruling R6 needs a fact, not a timer. The daemon already stamps `operation_id` onto the prompt
the CLI echoes back; the phone's fold carried `source` across and dropped the id, so a phone with two
sends in flight could see that one was echoed but not which. Matching on text was never an option —
`chat.go` records a probed mis-attribution where an owner-typed "yes" was claimed by a pending phone
send of "yes".

The exported-surface gate caught the new field, as designed; golden regenerated, field traced in
`screen_coverage.tsv`.

---

## Four reasons the composer is shut (`agents-tracker-tbpm.8`)

The defect the owner photographed: `"Read-only -- take control to type."` drawn on `!leaseHeld` with
no capability input, and `"This session's structured record broke..."` drawn on `!structuredChat`,
on the same screen, contradicting each other — the default state of every status-card session.

`ComposerAvailability` gains `TORN`, `NO_CHAT` and `ENDED` beside `OFFLINE`, each with its own
sentence. All four were derivable with no wire change: `structureTorn` was already computed from the
daemon's own `structured_gap` element and read by nothing.

**Ordering is part of the contract:** a permanent reason outranks a transient one, because a session
that is both offline and torn would otherwise be told "not connected", implying the composer returns
when the link does. It never will.

**Offline keeps its composer.** Absent and disabled are different answers, and which one a state gets
turns on whether the session has a message sink at all. An existing test made that argument and was
right; the first pass here contradicted it and the drawing both.

Two assertions MOVED: `ABSENT`'s two call sites become `NO_CHAT` and `TORN`.

---

## R1 — the composer needs no lease (`agents-tracker-tbpm.7`, part)

One line was greying a field over a message the machine would have accepted:
`keyboardEnabled = leaseHeld && online`. PB-INPUT-2's reasoning is still correct about the plane it
was written for — the raw keystroke plane really is lease-gated — and this app has no raw keystroke
plane. `online` stays, and is not ceremony: input is live-only and never queued.

One assertion MOVED with half of it DELETED: "enabled only while the lease is held" becomes
"whenever the link is up, lease or no lease". The lease half has no subject left.

---

## R3 — a tool run is closed until the reader asks (`agents-tracker-tbpm.3`, phase 1)

The reversal owes an argument back, and the load-bearing word in the old one was **silently**. A
closed card that says nothing does hide the record; one that names its own worst outcome does not.
`TranscriptBlock.mark` carries the wire's own word — `failed`, `declined` — or `clipped`, with the
failure outranking the clip. A successful whole card is unmarked: a mark on every row means nothing.

Two facts would otherwise have gone invisible with the default: a failed run would have read like a
successful one, and a clipped body would have lost its fetch offer, which is drawn only under an open
card. A file change is never folded.

The parameter inverted with the decision — what a reader spends is now the OPEN.

**Six tests MOVED.** The golden test, which is the recorded real Claude turn and the evidence the old
default's argument rested on, is split in two and asserts **both** states, so neither rendering is
weakened and the reversal is recorded where the old argument lived.

### Gates for the phone waves

| Gate | Result |
|---|---|
| Kotlin unit suite | 1442 tests, 0 failures, every result file newer than the run's start, `swarm.aar` unmoved across the run |
| `android/gate` | green — including `TestPBBIND7_TheBuiltAARExportsThePinnedFacadeSurface`, which caught a stale AAR the Kotlin suite had been compiling against |
| `go build` / `go vet` / `golangci-lint` | clean / clean / `0 issues.` |
