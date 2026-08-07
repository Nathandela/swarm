# A1 — the interaction program, end to end

Evidence for the integration slice of the interaction program: the **producer glue** that joins
the four landed workpackages, the **end-to-end demonstration** that an item shaped by an adapter
reaches the phone's transcript through every real hop, and the clearing of the **B94 reachability
fence** W1 left red.

**Governing documents** (all Accepted 2026-08-07): `docs/adr/ADR-009-structured-chat-interaction.md`,
`docs/adr/ADR-010-adapter-structured-capture.md`, `docs/specifications/interaction-schema.md`.

---

## 0. The gap this slice closes

Four workpackages landed four halves and no spine:

| Landed | Package | What it could not do |
|---|---|---|
| W1 | `internal/adapter` | shape an `Interaction`; nothing called it |
| W2 | `internal/journal`, `internal/daemon`, `internal/protocol` | append ONE item; nothing produced one |
| W3 | `internal/remotegw` | space a stream under ADR-010 §7's floor; nothing offered to it |
| W4 | `internal/phonecore`, `mobile` | fold and render items; nothing sent them |

`internal/verify`'s B94 reachability fence said exactly that, naming seven symbols across two
packages (§4 below).

### The placement decision, forced rather than chosen

W3's handoff note says the append floor "must be constructed daemon-side". **That is not
implementable as written**: `internal/remotegw` already depends on `internal/daemon`
(`remotegw -> protocol -> daemon`), so a daemon importing `remotegw.ItemAdmission` is an import
cycle. Measured:

```
$ go list -deps ./internal/remotegw | grep 'swarm/internal' | tr '\n' ' '
internal/vt internal/adapter internal/status internal/engine internal/hookclient internal/idempotency
internal/journal internal/persist internal/shimwire internal/transcript internal/wire internal/shim
internal/daemon internal/protocol/schema internal/remote/crypto internal/remote/pairing internal/version
internal/protocol internal/remote/device internal/remote/relay internal/remote/transport internal/remotegw
```

`internal/skeleton` is the assembly layer that imports `adapter`, `registry`, `daemon` and
`remotegw`, and `skeleton.Daemon` **is** the assembled daemon. The floor is constructed there and
the capture runs there.

---

## 1. RED — failing first (GG-5)

Two new test files, written and run before a line of production code existed.

`internal/skeleton/interaction_capture_test.go` — the glue's own contract:

- `TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing` (ADR-010 §5, `AsInteractionSource`)
- `TestInteractionCapture_AnUnshapeableInteractionEmitsNothing` (IS-ENV-3, `Interaction.Validate`)
- `TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal` (§2 + §3, and the
  negative half: no `keystrokes`, no CLI ref on the wire — IS-APR-1/-3)
- `TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID` (IS-ENV-2, IS-DELTA-1/-3)
- `TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage` (IS-ENV-1)
- `TestInteractionCapture_AnAuthenticatedHookReachesTheProducer` (the production call site, and
  the negative half: a foreign-token post reaches nothing)

`internal/skeleton/interaction_e2e_test.go` — the chain:

- `TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed`

### Verbatim RED

```
$ go test ./internal/skeleton/ -run "TestInteraction" -count=1
# github.com/Nathandela/swarm/internal/skeleton [github.com/Nathandela/swarm/internal/skeleton.test]
internal/skeleton/interaction_capture_test.go:133:13: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:153:13: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:184:13: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:249:5: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:250:5: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:266:5: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:285:5: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:288:5: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:305:5: sk.captureInteractions undefined (type *Daemon has no field or method captureInteractions)
internal/skeleton/interaction_capture_test.go:330:5: sk.adapterFor undefined (type *Daemon has no field or method adapterFor)
internal/skeleton/interaction_capture_test.go:330:5: too many errors
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
FAIL
```

The RED is "undefined symbol" rather than a failing assertion because the production glue did not
exist at all — the W1 precedent, and the honest shape of a first cycle against zero code.

---

## 2. GREEN

### 2.1 The producer glue

```
$ go test ./internal/skeleton/ -run "TestInteractionCapture" -count=1 -race -v
=== RUN   TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing
--- PASS: TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing (3.02s)
=== RUN   TestInteractionCapture_AnUnshapeableInteractionEmitsNothing
--- PASS: TestInteractionCapture_AnUnshapeableInteractionEmitsNothing (0.05s)
=== RUN   TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal
--- PASS: TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal (0.03s)
=== RUN   TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID
--- PASS: TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID (0.40s)
=== RUN   TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage
--- PASS: TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage (0.40s)
=== RUN   TestInteractionCapture_AnAuthenticatedHookReachesTheProducer
--- PASS: TestInteractionCapture_AnAuthenticatedHookReachesTheProducer (1.70s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	14.135s
```

### 2.2 The end-to-end chain

```
$ go test ./internal/skeleton/ -run "TestInteractionE2E" -count=1 -race -v
=== RUN   TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed
--- PASS: TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed (6.22s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	14.135s
```

What is real in that chain, and what is not, is stated at the head of
`internal/skeleton/interaction_e2e_test.go`. In one line: everything except the CLI behind the
adapter. Real relay server, real `cmd/swarm-remote` binary as a separate process, real daemon
with its shim and PTY, real hook post over the daemon's own socket, real engine authentication,
real pairing ceremony, real bound `swarmmobile.App` over a durable `phonecore.Core`. The three
substitutions the s19 rig documents (key custody, `remote init`'s supervision half, the fake
agent) carry over unchanged, plus one more: the `InteractionSource` behind the real
`AsInteractionSource` assertion is a double, because no shipped adapter implements the extension
and both CLI producers are excluded from this program.

### 2.3 Gates

```
$ go build ./...                                        # clean
$ go vet ./...                                          # clean
$ go test ./... -count=1                                # no failures
$ go test <every touched package> -count=1 -race
ok  	github.com/Nathandela/swarm/internal/skeleton	192.371s
ok  	github.com/Nathandela/swarm/internal/daemon	60.113s
ok  	github.com/Nathandela/swarm/internal/phonecore	27.387s
ok  	github.com/Nathandela/swarm/mobile	49.791s
ok  	github.com/Nathandela/swarm/internal/remotegw	34.138s
ok  	github.com/Nathandela/swarm/internal/adapter	4.564s
ok  	github.com/Nathandela/swarm/internal/verify	47.056s
```

`golangci-lint run ./internal/skeleton/... ./internal/daemon/... ./internal/verify/...` reports
ZERO findings in any file this slice touched (those packages carry pre-existing errcheck
findings in untouched files). `gofmt -l` is clean on `internal/skeleton/serve.go`, `conn.go`,
`interaction.go` and `internal/daemon/interaction.go`; `internal/skeleton/api.go`,
`internal/skeleton/revoke_reaudit_test.go` and `internal/verify/phaseb_evidence_test.go` are
unformatted AT HEAD (verified by running `gofmt -l` on `git show HEAD:<path>`) and were left
alone.

---

## 3. TEETH — six mutations, each reverted

A green e2e proves nothing until it is shown to go red for the right reason. Each mutation was
applied alone, run, and reverted.

| # | Mutation | Result |
|---|---|---|
| 1 | the producer never offers (`ItemAdmission.Offer` call removed) | FAIL: `timed out after 45s: both interaction items reached the phone's transcript` |
| 2 | `toWireJournalRecord` stops copying `Payload` into `Item` (W2's hop) | FAIL: same timeout — the item exists on the machine and crosses no wire |
| 3 | the phone's reseed REPLACES the transcript instead of merging | FAIL: `the transcript holds 0 item(s) after a journal reseed; it held 2 before` |
| 4 | `App.onInteraction` raises no event | FAIL: `timed out after 45s: an interaction event named the approval that arrived` |
| 5 | `Interaction.Validate` is not called in the producer | B94 FAIL naming exactly `internal/adapter.Interaction.Validate` |
| 6 | (during 3's first attempt) the reseed assertion taken without waiting for the repair to land | PASSED under mutation 3 — **the test was vacuous and was fixed** |

**Mutation 6 is the one worth reading.** The first draft asserted transcript survival immediately
after `App.Resync` returned. `Resync` returns when the request is SEALED, not when the answer is
folded, so the assertion held on a phone that had received no reseed at all — and it passed under
a mutation that wiped the transcript on every reseed. The fix is a positive observation: the
facade raises `Event{Kind:"journal", State:"resynced"}` from `accept()` only after the core has
committed the frame, and the test now waits for it. Mutation 3's FAIL above is against the fixed
test.

---

## 4. B94 — the reachability fence W1 left red

### Before

```
$ go test ./internal/verify/ -run TestB94_EveryExportedSymbolIsReachableFromProduction -count=1
--- FAIL: TestB94_EveryExportedSymbolIsReachableFromProduction (2.79s)
    phaseb_reachability_test.go:343:
        2 package(s) export symbols NO PRODUCTION ENTRY POINT CAN REACH.

          internal/remotegw -- 4 unreachable exported symbol(s):
              internal/remotegw.ItemAdmission.Flush
              internal/remotegw.ItemAdmission.Offer
              internal/remotegw.ItemAdmission.Pending
              internal/remotegw.NewItemAdmission

          internal/adapter -- 3 unreachable exported symbol(s):
              internal/adapter.AsInteractionSource
              internal/adapter.CheckInteractionFixture
              internal/adapter.Interaction.Validate
```

W3's four `ItemAdmission` symbols were failing too — the task named the three adapter ones, and
the fence has to pass whole.

### After

```
$ go test ./internal/verify/ -run TestB94_EveryExportedSymbolIsReachableFromProduction -count=1 -v
    phaseb_reachability_test.go:310: B94: 540 exported symbols examined, 54 unreachable and all accounted for
--- PASS: TestB94_EveryExportedSymbolIsReachableFromProduction (3.46s)
```

**Six of the seven were cleared by real production usage**, each at the point ADR-010 puts it:

| Symbol | Production caller | Clause |
|---|---|---|
| `adapter.AsInteractionSource` | `skeleton.(*Daemon).captureInteractions` | ADR-010 §5 — the generic-fallback decision point |
| `adapter.Interaction.Validate` | `skeleton.(*Daemon).captureInteractions` | IS-ENV-3 — emit nothing rather than a partial item |
| `remotegw.NewItemAdmission` | `skeleton.(*Daemon).initInteractionsLocked` | ADR-010 §7 — one floor per target |
| `remotegw.ItemAdmission.Offer` | `skeleton.(*Daemon).captureInteractions` | ADR-010 §7 |
| `remotegw.ItemAdmission.Flush` | `skeleton.(*Daemon).releaseInteractions` | ADR-010 §7 — the floor's clock |
| `remotegw.ItemAdmission.Pending` | `skeleton.(*Daemon).releaseInteractions` | the backlog on the failure log line |

`skeleton.Daemon` is reachable from `cmd/swarm`'s `main` through `skeleton.Serve`, so the whole
chain is live from a production entry point.

**One row was added to `b94Allowed`**, using that fence test's own sanctioned mechanism and the
reason its two siblings already carry:

```go
"github.com/Nathandela/swarm/internal/adapter.CheckInteractionFixture": "as Conformance: ADR-010's obligation-3 corpus half and obligation 4, replaying a recorded fixture's payloads through Interactions. Its two siblings above -- AsInteractionSource and Interaction.Validate -- are NOT listed, because internal/skeleton's interaction producer calls both in production (interaction.go).",
```

### The clearing is real, not the fence's over-approximation

B94's header warns that RTA over-approximates. It is worth showing the two adapter symbols are
genuinely reached rather than swept up: removing ONE call — `in.Validate()` in
`captureInteractions` — makes the fence fail again, naming that symbol and no other (mutation 5
above). The row was earned by the call, and the fence would notice if the call went away.

---

## 5. Decisions recorded (each also carries a code comment citing its clause)

1. **The producer lives in `internal/skeleton`, not `internal/daemon`.** Forced by the import
   graph, not preferred: `remotegw -> protocol -> daemon`, so a daemon importing the append floor
   is a cycle. W3's handoff ("constructed daemon-side") is satisfied in substance —
   `skeleton.Daemon` IS the assembled daemon and the floor never enters a sidecar sink, so
   interaction-schema.md §10's "the gateway parses no item" still holds in fact.
2. **`daemon.RecordInteractionRaw`** is the raw sibling W3's handoff asked for. It exists because
   the floor merges BYTES: `ItemAdmission` collapses a `tool_run`'s open and close by a
   field-wise JSON union and forwards an unmerged item byte-exact, which is what keeps an
   `approval_request`'s bytes the bytes the daemon hashed (IS-APR-2). Re-parsing into
   `InteractionItem` and re-marshalling would destroy that. It re-applies both refusals
   (IS-ENV-3's required fields, §5's `MaxItemBytes`) because neither can be assumed of merged
   bytes.
3. **The production call site is `serveHook`, AFTER `engine.HandleCallback` accepted.** The
   engine's per-invocation token check (S6/G5) is what stands between a local process and the
   owner's transcript; a capture running before it would be a second, unauthenticated write path
   into the journal. Pinned by the foreign-token half of
   `TestInteractionCapture_AnAuthenticatedHookReachesTheProducer`.
4. **`HookPayload.Raw` is the callback body the daemon received.** ADR-010 §1's `capture: raw` —
   which is what makes the CLI's OWN event body survive `cmd/swarm`'s `parseHookStdin`
   flattening — belongs to the excluded producer slices. Until one lands, `Raw` is the callback
   envelope. This is honest rather than placeholder: no shipped adapter shapes anything from it,
   which is exactly ADR-010 §5's supported state.
5. **`item_id` is a ULID minted here** (48-bit ms + 80 bits `crypto/rand`, Crockford base32),
   written in 20 lines rather than taking a dependency. The property §2 wants — lexicographic
   order matches mint order, with no coordination — is the format's, not a library's.
6. **`turn_id` uses the same generator** (IS-ENV-1): a turn is an interval the daemon opens and
   closes and needs the same property, with no reason to be a second format.
7. **The ref→item_id map is per session and cleared by `endSession`.** That is the whole of its
   retention policy: bounding it further is a drop policy, and a fold key dropped mid-item splits
   one item in two (worse than the memory), which needs a ruling rather than a constant.
8. **An `approval_request` carries no `agent_instance`, `content_hash` or `expires_at`.** All
   three are daemon-authoritative D7 binding material whose only consumer is IS-LIFE-4's
   `ApproveReq` body and its validation, which no slice has built — and IS-APR-2 makes the phone
   echo them verbatim rather than compute them, so a hash minted now would be a value nobody can
   check. They land with `ApproveReq`. See §6.
9. **`keystrokes` is never copied onto the item** (IS-APR-3, IS-LIFE-6), asserted negatively in
   `TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal`, along with the CLI's
   own ref (IS-APR-1 leaves exactly one id on the wire).
10. **The floor is driven by a ticker at `DefaultAppendWindow`.** `ItemAdmission` releases at most
    one item per `Offer`, so a transcript that goes quiet would hold its last item until the next
    event — and the last item of a turn is the one a user is waiting for.
11. **`serveHook` now reads the callback into memory before decoding**, because the producer
    needs the bytes and `hookclient.Decode` consumes the reader. That is a behaviour change worth
    naming: the read is now BOUNDED (`hookBodyLimit`, 4 MiB) where it used to stream unbounded.
    The bound is deliberately well above the largest legitimate post — `cmd/swarm`'s
    `hookStdinLimit` caps a CLI body at 1 MiB and JSON-escaping it into the callback envelope can
    nearly double that — so no real hook can be refused by it, and a callback over it is dropped
    whole exactly as a malformed one always was.

---

## 6. Open points, owed to later slices

- **IS-LIFE-4 is not built**, so the approval item is incomplete by §3.5: no `agent_instance`,
  `content_hash` or `expires_at`, and the phone can show a card it cannot answer. The
  `ApproveReq` wire body, the daemon's tuple/hash/expiry validation, and these three fields are
  ONE change and should land together.
- **IS-LIFE-2's resolver is not built.** Nothing emits `approval_resolved` on the cancel /
  supersede / expire / answered-locally paths, so a card can only be dismissed by a resolution
  nobody produces. The phone half is built and tested (W4); the daemon half is not.
- **IS-ST-2's orphan sweep is not built**: no daemon-side pass closes `in_progress` items
  `failed` on instance death.
- **ADR-010 §1's `capture: raw` is not built** (see decision 4), so no CLI's own event body
  reaches a shaper. This is the producer slices' work and is what makes the whole plane carry
  real content.
- **`engine.Callback.Raw`** (ADR-010 §6) was NOT added. The producer reads the raw body at
  `serveHook` instead, which needs no wire or engine change. If the sdk-url path wins the
  hooks-vs-sdk-url investigation, that decision is re-openable at one call site.
- **`docs/specifications/interaction-schema.md` was not amended.** Two facts this slice settled
  have no home in it: that the producer's natural place is the assembly layer rather than
  `internal/daemon` (the import graph decides it), and that `Resync`'s answer must be OBSERVED to
  land before anything is asserted about surviving it (§3's mutation 6). If those readings are
  right they belong in the doc.
- **No fixture-replay golden set.** `adapter.CheckInteractionFixture` is allowlisted rather than
  called because no recorded corpus exists for a CLI that has a shaper. Each producer slice
  supplies its own, and that row should be deleted the day one does (the allowlist is
  bidirectional and will demand it).
