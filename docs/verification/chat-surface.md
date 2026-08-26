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

- `internal/shim/server.go` — `ptyWriter` counts input bytes written since the last line-running
  byte (`WriteInput`, `countLocked`). Emulator replies do not count: the shim answering the
  agent's own queries is not somebody typing.
- `internal/shim/server.go` — `submitMessage` checks the count is zero, writes the text, waits
  `submitframe.Gap`, writes the CR, all under one `mu` hold, or returns `errInputBusy` having
  written nothing. A partial write (text in, CR failed) counts the text as dirty rather than
  pretending the line is clean.
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

It never characterizes the CLI's input region. ADR-017's amendment obligation asked for
`expected_input_revision`, whose enforcement would require exactly that — and the reasoning for
why that is unreachable still stands. What changed is the question: the shim can say whether
**anybody has written to this PTY since the last submit**, which is a fact about the PTY rather
than a claim about what the agent has drawn on it. The revision never crosses the wire; only the
predicate does. The ADR-017 amendment block records the substitution rather than letting a
different mechanism quietly satisfy a named obligation.

It errs safe: a draft typed and deleted back to empty still refuses. False refusal was chosen over
prompt corruption.

### Residual, disclosed

A shim that predates the transaction answers `ErrSubmitUnsupported` and the daemon degrades to the
two unlocked writes — reachable only between a daemon upgrade and the shim restart that replaces
it. The merge is otherwise exclusively a property of the keystroke branch: the backend arm never
touches the PTY, and the only `ComposerKeys` implementor in the tree is Claude.

### Owed before this is called complete

The committee's list for this slice, not yet written:

- A real Claude PTY test (not the fake agent) parking an owner draft between the check and the
  write.
- Concurrent owner Enter, phone send, and two distinct phone sends in one test.
- Turn closure or start between `expected_turn` validation and delivery, for both sinks.

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
