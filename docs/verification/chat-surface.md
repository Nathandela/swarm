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
