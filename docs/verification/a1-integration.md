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

  **CORRECTED 2026-08-07 (a1b, the Claude Code producer).** The corpus half came true — the
  corpus is `internal/adapter/claude/testdata/interaction` and `CheckInteractionFixture` is
  called over it — but **the prediction about the row was wrong, and the row stays**. It was
  measured, not argued: deleting it fails B94 with `internal/adapter.CheckInteractionFixture`
  as the only finding (verbatim in `a1b-claude-producer.md` §4). The mistake was about the
  instrument, not the corpus. B94 loads with `Tests: false` and roots at `cmd/...` `main()`
  plus the gomobile facade, so **no caller in a `_test.go` file can ever make a symbol
  reachable**, and the bidirectional arm fires on production reachability alone. A conformance
  harness has no production caller by construction — which is exactly the reason its neighbours
  `Conformance` and `CheckConformance` carry, and the row now carries it too. The only way this
  row could have died is a production call site, and inventing one for a function that replays
  recorded fixtures is the move the fence's own header warns against.

---

## 7. Review finding R5 — one validation, on the shipped path

Adversarial review of 79a070d, reported as quality and not fixed there.

### The finding

`InteractionItem.validate` carries FIVE refusals, but the entry the shipped producer releases
into — `RecordInteractionRaw`, reached from `skeleton.initInteractionsLocked`'s
`ItemAdmission.Append` closure — re-implemented only THREE of them inline (IS-ENV-3's `v`,
`item_id`, `kind`). The other two lived on the typed `RecordInteraction`, which **no production
caller reaches**:

- §2's **required `ts`** — and worse, the typed path could not fire it either, because it
  *stamps* `ts` before validating it, so that refusal was unreachable from anywhere;
- §2's **`full_bytes` only alongside `truncated`**.

So ~35 lines of `internal/daemon/interaction.go` were reachable only from tests, and two schema
rules were enforced on nothing that ships. Ponytail: one path, not two.

### The resolution, and why this branch

The review offered two: wire the typed validation into the shipped raw path, or delete the typed
form and move its refusals. **The first, and there is now exactly one refusal set.**

`RecordInteractionRaw` **decodes** the offered bytes into an `InteractionItem` to READ them, calls
`it.validate()`, and appends the **original bytes**. Decoding to inspect costs the byte-exactness
nothing — only *re-encoding* would, and that is the distinction §5 decision 2 was reaching for.
IS-APR-2 still holds: an unmerged item is journalled exactly as the producer serialized it.

`RecordInteraction` keeps only what a typed constructor owes — the `ts` default a caller may
legitimately leave to the daemon — and delegates. It is not a second write path and no longer
carries a second copy of the rules. **It still has no production caller**, which is honest: the
shipped producer serializes its own items because the floor merges BYTES, not structs (IS-DELTA-3).
Keeping it typed is what stops the next caller from hand-rolling the envelope; keeping it
*validating* is what created this finding, and that half is gone.

**This supersedes §5 decision 2's "it re-applies both refusals".** It applies all five, and it is
the only place any of them are applied.

Net diff: `internal/daemon/interaction.go`, +23/−14, of which the code change is −8 lines of
duplicated inline checks and +5 that call the one validation. No production change in
`internal/skeleton` was needed.

### 7.1 RED — failing first (GG-5)

Two new test files, run before the production edit. The RED is a FAILING ASSERTION, not an
undefined symbol: the seam existed and shipped an item it should have refused.

```
$ go test ./internal/daemon/ -run "TestDaemon_RecordInteractionRaw" -count=1 -v
=== RUN   TestDaemon_RecordInteractionRawRefusesAnItemWithNoTS
    interaction_r5_test.go:40: RecordInteractionRaw accepted an item with no `ts`; §2 makes it required and the enclosing wire record carries none to substitute (PB-APP-11)
    interaction_r5_test.go:44: 1 interaction record(s) appended for a `ts`-less item; want none
--- FAIL: TestDaemon_RecordInteractionRawRefusesAnItemWithNoTS (0.04s)
=== RUN   TestDaemon_RecordInteractionRawRefusesFullBytesWithoutTruncated
    interaction_r5_test.go:61: RecordInteractionRaw accepted `full_bytes` on an item that is not `truncated`; §2 carries the two together, and alone it reports a clip that never happened
    interaction_r5_test.go:65: 1 interaction record(s) appended for an unpaired `full_bytes`; want none
--- FAIL: TestDaemon_RecordInteractionRawRefusesFullBytesWithoutTruncated (0.03s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/daemon	3.616s
FAIL
```

The end-to-end half, on the path the producer actually releases through
(`ItemAdmission.Offer` → `Append` → `RecordInteractionRaw` → journal). It is offered at the floor
rather than shaped through `captureInteractions` because `shapeItem` always stamps `ts` — which is
precisely why the refusal behind it had never been exercised, and the floor asserts nothing about
an item beyond `item_id` and `kind`:

```
$ go test ./internal/skeleton/ -run "TestInteractionR5" -count=1 -v
=== RUN   TestInteractionR5_TheShippedReleasePathRefusesAnItemWithNoTS
    interaction_r5_test.go:29: the append floor released a `ts`-less item into the journal without complaint; §2 makes `ts` required and the wire journal record carries none to substitute (PB-APP-11)
    interaction_r5_test.go:33: the journal holds 1 interaction record(s) for a `ts`-less item: [map[item_id:01JBQ4Z0X9M6T7NPKV2RQF8SJD kind:agent_message text:hi v:1]]; want none (IS-ENV-3: emit nothing rather than a partial item)
--- FAIL: TestInteractionR5_TheShippedReleasePathRefusesAnItemWithNoTS (2.13s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	3.137s
FAIL
```

That RED output IS the "before" state of the finding: a `ts`-less item reached the journal, and
the map printed on the third line is the partial item a phone would have had to date by arrival.

### 7.2 GREEN

The five pre-existing carriage tests pass unchanged — the consolidation neither weakened IS-ENV-3
nor touched a test to make it pass:

```
$ go test ./internal/daemon/ -run "TestDaemon_RecordInteraction|TestInteractionItem" -count=1 -race -v
=== RUN   TestDaemon_RecordInteractionRawRefusesAnItemWithNoTS
--- PASS: TestDaemon_RecordInteractionRawRefusesAnItemWithNoTS (0.04s)
=== RUN   TestDaemon_RecordInteractionRawRefusesFullBytesWithoutTruncated
--- PASS: TestDaemon_RecordInteractionRawRefusesFullBytesWithoutTruncated (0.03s)
=== RUN   TestDaemon_RecordInteractionAppendsBareInteractionRecord
--- PASS: TestDaemon_RecordInteractionAppendsBareInteractionRecord (0.04s)
=== RUN   TestDaemon_RecordInteractionKeepsAProducerSuppliedTS
--- PASS: TestDaemon_RecordInteractionKeepsAProducerSuppliedTS (0.02s)
=== RUN   TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope
--- PASS: TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope (0.02s)
=== RUN   TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap
--- PASS: TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap (0.02s)
=== RUN   TestInteractionItem_KindFieldsMayNotCollideWithTheEnvelope
--- PASS: TestInteractionItem_KindFieldsMayNotCollideWithTheEnvelope (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/daemon	4.747s
```

```
$ go test ./internal/skeleton/ -run "TestInteraction" -count=1 -race -v
--- PASS: TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing (2.49s)
--- PASS: TestInteractionCapture_AnUnshapeableInteractionEmitsNothing (0.07s)
--- PASS: TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal (0.03s)
--- PASS: TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID (0.41s)
--- PASS: TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage (0.41s)
--- PASS: TestInteractionCapture_AnAuthenticatedHookReachesTheProducer (1.71s)
--- PASS: TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed (6.70s)
--- PASS: TestInteractionR5_TheShippedReleasePathRefusesAnItemWithNoTS (0.03s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	14.212s
```

### 7.3 TEETH — two mutations, each reverted

| # | Mutation | Result |
|---|---|---|
| 1 | `RecordInteractionRaw` appends the DECODED item (`json.Marshal(it)`) instead of the offered bytes — the one regression this change newly makes possible | FAIL: `interaction_capture_test.go:205: item has no string "summary"` — the §3 kind fields ride in `Fields json:"-"` and vanish on a round trip, so the existing capture test already has teeth against it |
| 2 | the single `it.validate()` call is removed | FAIL, 4 tests: both new R5 refusals, the skeleton end-to-end one, AND the pre-existing `TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope` naming all three of `no v` / `no item_id` / `no kind` |

Mutation 2 is the one that matters: it proves the SINGLE validation now carries the three
IS-ENV-3 refusals the deleted inline copy used to carry, so consolidating did not quietly drop
them on the way.

### 7.4 Gates

```
$ go build ./...                                        # clean
$ go vet ./...                                          # clean
$ gofmt -l internal/daemon/interaction.go internal/daemon/interaction_r5_test.go internal/skeleton/interaction_r5_test.go   # clean
$ go test ./internal/daemon/  -count=1 -race            ok   43.534s
$ go test ./internal/skeleton/ -count=1 -race           ok  187.593s
$ go test ./internal/protocol/... ./internal/journal/... ./internal/remotegw/... ./internal/adapter/... -count=1   all ok
$ go test ./internal/verify/ -count=1                   ok    8.629s
```

The `protocol.md` drift fences are untouched and green, along with the item-carriage fences that
ride the same file:

```
$ go test ./internal/protocol/ -run "TestProtocolMD|TestJournalRecordCarries|TestJournalRecordWithout|TestJournalEventControl" -count=1 -v
--- PASS: TestJournalRecordCarriesTheInteractionItem (0.01s)
--- PASS: TestJournalRecordWithoutAnItemEncodesUnchanged (0.00s)
--- PASS: TestJournalEventControlCarriesTheItem (0.00s)
--- PASS: TestProtocolMDBidi_FieldSetMatchesStructs (0.01s)
--- PASS: TestProtocolMD_ExistsAndDocumentsEveryField (0.00s)
--- PASS: TestProtocolMD_DocumentsEveryOp (0.00s)
ok  	github.com/Nathandela/swarm/internal/protocol	0.695s
```

B94 is unmoved, as it must be — no exported symbol was added or removed:

```
$ go test ./internal/verify/ -run TestB94_EveryExportedSymbolIsReachableFromProduction -count=1 -v
    phaseb_reachability_test.go:310: B94: 540 exported symbols examined, 54 unreachable and all accounted for
--- PASS: TestB94_EveryExportedSymbolIsReachableFromProduction (3.20s)
```

`golangci-lint run ./internal/daemon/... ./internal/skeleton/...` reports ZERO findings in
`interaction.go`, `internal/daemon/interaction_r5_test.go` or
`internal/skeleton/interaction_r5_test.go`; the packages' pre-existing errcheck/staticcheck
findings are all in files this change does not touch.

**One honest note on `go test ./... -count=1`:** it reports `FAIL internal/phonecore`
(`r1_replayfold_test.go`, three tests). That is a *different* review finding (R1) being worked
concurrently in this same worktree and is RED by design at the moment of this run; it is
untracked test code plus in-flight edits to `internal/phonecore/{interaction,state}.go`, and this
slice touches neither. Every other package in the module passes.

### 7.5 What is deliberately still open

- **`daemon.RecordInteraction` still has no production caller.** That is the branch the review
  preferred, taken knowingly: the typed form is now a 6-line constructor that delegates, its
  refusals are the shipped path's refusals, and there is no second copy of any rule. If a later
  slice decides the constructor earns nothing, deleting it is a self-contained change that moves
  four carriage tests onto `RecordInteractionRaw` — but doing it *here* would have meant rewriting
  tests that are green, which is the wrong trade for the same end state.
- **The `ts` refusal is a producer-bug backstop, not a validator.** It cannot tell a *wrong*
  instant from a right one — only an absent one. Nothing on the machine checks that an item's `ts`
  is plausible relative to its cursor, and IS-LAYER-3 makes the cursor the ordering authority
  anyway, so this is a floor, not a clock.

---

## 8. Review finding R4 — the approval lifecycle's back half

Adversarial review of 79a070d. Unlike R5 this was not a quality note: it is §6's own list of
what A1a did not build, read back as a defect. Three deliverables, all daemon-side.

**This section SUPERSEDES the first three bullets of §6** (IS-LIFE-4, IS-LIFE-2's resolver,
IS-ST-2's sweep — all three now built). Appended rather than edited in place, on §7's precedent:
the record of what a slice knew at the time is worth more than a tidy document. §6's remaining
bullets stand unchanged.

### The finding

A1a ships an `approval_request` to the phone and re-delivers it across a repair. Nothing else
about the lifecycle exists:

- **IS-LIFE-4 is not built.** The item carries no `agent_instance`, no `content_hash` and no
  `expires_at` (§3.5), so the phone renders a card it CANNOT ANSWER — IS-APR-2 forbids it
  computing either value, so a card that arrives without them has nothing to echo. There is
  also no daemon-side validation, because there was no stored tuple to validate against.
- **IS-LIFE-2's resolver is not built.** Nothing emits `approval_resolved` on any path, so a
  card can only be dismissed by a resolution nobody produces — and the phone's IS-LIFE-3
  retention exemption, which lifts on `Resolved`, therefore never lifts. The card is both
  unanswerable and unevictable.
- **IS-ST-2's orphan sweep is not built.** No pass closes `in_progress` items `failed` on
  instance death, so a transcript keeps a spinning card for an agent that is gone.

### 8.1 What landed, and the decision behind each

**(a) IS-LIFE-4 — the tuple on the item, and the validation.**

`schema.ApproveReq` gains one field, `decision` — "the chosen decision id" IS-LIFE-4 names,
in the CLI's OWN vocabulary (§3.5: spike-SB captured Codex offering
`accept | acceptWithExecpolicyAmendment | cancel`). It is deliberately UNSIGNED, and the Go
doc comment carries IS-LIFE-4's reason verbatim: `ContentHash` is the signed tuple's one
content slot and D7 spends it on the interaction content, which the phone echoes verbatim and
so cannot fold a choice into.

**protocol.md's posture is kept exactly as it was.** `ApproveReq` is not a wire table there and
does not become one: the `approve` row on the `Control` field table already exists and is
unchanged, `RemoteCommand` and its bodies are documented at the field level in prose in
`internal/protocol`, and GG-7's drift check reflects `Control`, `SessionView`, `LaunchReq` and
`TerminalSnapshot` only. interaction-schema.md §1 says so in as many words ("no build can fail
on a missing `item` or approve row … the obligation is PROCEDURAL, carried by the Go field's
doc comment"), and that comment is now written. All three drift fences are untouched and green.

The producer stamps §3.5's three daemon-authoritative fields on a pending `approval_request`
and stores the binding tuple (`skeleton.openApprovalLocked`). `skeleton.approveInteraction` is
the validation: it checks `machine`, `session`, the agent instance `{shim_pid,
shim_start_time}`, `interaction_id`, `content_hash`, the echoed `expires_at` and the daemon's
OWN clock, and finally that the decision id is one the card actually offered — refusing with
`invalid_field` or `stale_approval` from D10's taxonomy, and applying nothing.

**THE CONTENT HASH'S CANONICALIZATION, which the schema leaves to the daemon and which
therefore has to be stated here.** `content_hash` is SHA-256 over **the shipped bytes with its
own slot zeroed**. The item is serialized with a 64-character placeholder in the slot, fitted
under §5's caps, hashed, and the placeholder replaced with the digest — same width, so the cap
still holds and nothing is re-serialized. Three properties make it the right form rather than a
trick: a hash cannot cover itself, so *some* exclusion is forced; the form is re-derivable by
anyone holding the item (the test does exactly that); and it obeys R2's rule TRUNCATE-THEN-HASH,
so the bytes hashed are the bytes the card renders and an approve echoed off a truncated card
still matches. `content_hash` is excluded from the fit ceiling for the same reason the §3
enums are — half a digest is not a smaller item, it is a permanently unanswerable card.

`approvalTTL = 120 s` is spike-SC's shorter measured CLI hold (Codex's app-server, verified to
120 s with no timeout or auto-deny; Claude Code's `PermissionRequest` to 300 s). The daemon's
window must sit INSIDE the CLI's own, or the daemon accepts an approve the CLI stopped waiting
for. It also leaves ADR-010 §4's own arithmetic intact: 120 − 30 s of push-wake deferral = 90 s,
still above spike-SC's 60 s one-tap floor. PROPOSED AND UNRATIFIED on §5's terms.

**(b) IS-LIFE-2 — the resolver, five paths, one record shape.**

`resolveApprovalLocked` is the ONE place a resolution is authored. It is a no-op when nothing
is pending, which is what makes "exactly one" hold under a race between two paths (an expiry
ticking while a withdrawal arrives resolves once; the loser finds nothing).

| Path | Trigger | `by` |
|---|---|---|
| `superseded` | a newer `approval_request` for the same session | `agent` |
| `cancelled` | a further record for the same CLI request id carrying a TERMINAL status — the CLI withdrawing the prompt, as the capture side sees it | `agent` |
| `cancelled` | the agent instance died with the request unresolved (the IS-ST-2 sweep) | `agent` |
| `expired` | the daemon's own window passed — swept on the append floor's existing 125 ms ticker, and re-checked inline on an arriving approve so the ≤ 1-tick gap is not a hole | `daemon` |
| `answered_locally` | the session's `status.Interaction` LEAVES the waiting state with no remote decision recorded | `owner` |
| `allowed` | a validated approve | `phone` |

**The target is the SESSION, and that is the schema's model rather than a convenience.**
IS-LIFE-3 rules out the roster half for re-delivery partly because a roster record "cannot hold
two pending approvals for one session". So a second request for one session is not a second
card, it is a supersession — which is exactly what `superseded` names.

**`answered_locally` fires on the TRANSITION, not on the state.** A resolver keyed on "the
session is not waiting" dismisses a live card the moment the session reports anything at all,
including the status emit that races the capture. That is the negative control
`TestApprovalResolved_AnApprovalStillWaitingIsNotResolvedByAnyStatusEmit`, and mutation 3 below
shows it has teeth.

**(c) IS-ST-2 — the orphan sweep.**

`sweepSessionInteractions` runs from `endSession`, the one hook fired for a shim exit, a lost
session and a delete alike. It resolves a pending approval first (IS-LIFE-2 is unconditional —
an unanswerable card that is also unevictable is the worse of the two failures), closes every
item still `in_progress` with `failed`, and emits the terminal `session_status` AFTER them,
which is the order IS-ST-2 states.

It emits NOTHING for a session with no open items and no pending approval. The terminal
`session_status` is emitted here as the marker IS-ST-2 orders the failures against; a
`session_status` on every session end regardless is IS-SS-1's transcript marker, a different
rule that no slice has built, and emitting one unconditionally would put a record on the journal
for every session that ever ran with no transcript at all.

### 8.2 RED — failing first (GG-5)

Written in two files for a reason: the behavioural half runs entirely against production entry
points that ALREADY EXIST (`captureInteractions`, `emitStatus`, `endSession`) and reads the
JOURNAL, so each failure names a missing RULE rather than a missing symbol. Had it shared a file
with the white-box half, the whole package would have failed to build and the behavioural RED
would have been masked by a compile error.

`internal/skeleton/approval_r4_test.go` — behavioural, run against unchanged production code:

```
$ go test ./internal/skeleton/ -run 'TestApprovalRequest_|TestApprovalResolved_|TestOrphanSweep_' -count=1
--- FAIL: TestApprovalRequest_ShipsTheD7BindingTupleAndTheDaemonAuthoritativeExpiry (3.49s)
    approval_r4_test.go:95: the approval_request carries no `agent_instance` object: map[action:map[path:src/main.rs type:write] decisions:[map[id:accept label:Allow] map[id:cancel label:Deny]] item_id:01KZEFBME94MKK6W231E8AYTJ7 kind:approval_request mode:card status:in_progress summary:write src/main.rs ts:2026-08-07T16:02:05.384991Z v:1]. §3.5 makes it the ADR-007 D7 instance binding {shim_pid, shim_start_time}, and without it an approve cannot be refused for naming a DIFFERENT agent than the one that asked
--- FAIL: TestApprovalRequest_TheContentHashCoversTheItemAsShipped (0.04s)
    approval_r4_test.go:156: content_hash = ""; want a 64-character SHA-256 (§3.5)
--- FAIL: TestApprovalResolved_ANewerRequestSupersedesTheOlderOne (10.03s)
    approval_r4_test.go:189: no approval_resolved for "01KZEFBMGXARVM7AKDGX0HJ08V" ever reached the journal for s-sup. IS-LIFE-2: EVERY approval_request reaches exactly one approval_resolved -- including when it is cancelled, superseded, expired or answered at the machine -- and that guarantee is the whole of what makes a stale card dismiss on every surface. Items seen: [map[... item_id:01KZEFBMGXARVM7AKDGX0HJ08V kind:approval_request ... summary:first ...] map[... kind:approval_request ... summary:second ...]]
--- FAIL: TestApprovalResolved_ACLIWithdrawalCancelsTheRequest (10.03s)
    approval_r4_test.go:214: no approval_resolved for "01KZEFBYADME1YHCFK7AMQMVB2" ever reached the journal for s-cancel. ... Items seen: [map[... status:in_progress ...] map[... status:declined ...]]
--- FAIL: TestApprovalResolved_TheDesktopAnsweringResolvesLocally (10.04s)
    approval_r4_test.go:241: no approval_resolved for "01KZEFC8401Q9AQJS36RJ3ZM8D" ever reached the journal for s-local. ...
--- FAIL: TestOrphanSweep_InstanceDeathClosesEveryOpenItemBeforeTheTerminalSessionStatus (10.41s)
    approval_r4_test.go:332: the sweep never completed. Want a `failed` record for BOTH open items map[01KZEFCJJJD3W3EGR95KAZZZEP:tool_run 01KZEFCJJT4Y3DPTZEBQV322EC:approval_request], one approval_resolved (IS-LIFE-2 is unconditional -- an unresolved request whose agent is gone still resolves, or the phone's IS-LIFE-3 exemption never lifts), and a terminal session_status LAST (IS-ST-2 orders the failures before it).
        Journal: [map[... kind:tool_run status:in_progress ...] map[... kind:approval_request status:in_progress ...]]
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	45.639s
```

Six failures, one pass: `TestApprovalResolved_AnApprovalStillWaitingIsNotResolvedByAnyStatusEmit`
is the negative control and passes vacuously on a system that resolves nothing at all. Its teeth
are mutation 3.

`internal/skeleton/approval_validate_r4_test.go` — the white-box half. The arriving approve has
no wire route (`opForAction` refuses one: "approve is not a daemon remote op (D6/D7)"), so the
entry point IS the seam under test, and it necessarily names symbols this slice adds:

```
$ go test ./internal/skeleton/ -run 'TestApprove' -count=1
# github.com/Nathandela/swarm/internal/skeleton [github.com/Nathandela/swarm/internal/skeleton.test]
internal/skeleton/approval_validate_r4_test.go:51:3: unknown field Decision in struct literal of type protocol.ApproveReq
internal/skeleton/approval_validate_r4_test.go:77:18: sk.approveInteraction undefined (type *Daemon has no field or method approveInteraction)
internal/skeleton/approval_validate_r4_test.go:137:78: r.Decision undefined (type *protocol.ApproveReq has no field or method Decision)
internal/skeleton/approval_validate_r4_test.go:138:63: r.Decision undefined (type *protocol.ApproveReq has no field or method Decision)
internal/skeleton/approval_validate_r4_test.go:150:20: sk.approveInteraction undefined (type *Daemon has no field or method approveInteraction)
internal/skeleton/approval_validate_r4_test.go:185:11: sk.approvals undefined (type *Daemon has no field or method approvals)
internal/skeleton/approval_validate_r4_test.go:194:18: sk.approveInteraction undefined (type *Daemon has no field or method approveInteraction)
internal/skeleton/approval_validate_r4_test.go:226:3: unknown field Decision in struct literal of type protocol.ApproveReq
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
```

**`internal/skeleton/approval_e2e_r4_test.go` was authored before the implementation but first
RUN after it, so its failing-first is evidenced by REMOVING the rule rather than by the original
order — recorded plainly rather than dressed up.** That is mutation 2 below, and the failure is
the one the file exists for:

```
--- FAIL: TestInteractionE2E_AResolvedApprovalDismissesThePhoneCard (52.35s)
    approval_e2e_r4_test.go:83: timed out after 45s: the approval_resolved reached the phone's transcript
```

### 8.3 GREEN

```
$ go test ./internal/skeleton/ -run 'TestApprovalRequest_|TestApprovalResolved_|TestOrphanSweep_|TestApprove' -count=1 -v
--- PASS: TestApprovalRequest_ShipsTheD7BindingTupleAndTheDaemonAuthoritativeExpiry (4.15s)
--- PASS: TestApprovalRequest_TheContentHashCoversTheItemAsShipped (0.03s)
--- PASS: TestApprovalResolved_ANewerRequestSupersedesTheOlderOne (0.40s)
--- PASS: TestApprovalResolved_ACLIWithdrawalCancelsTheRequest (0.40s)
--- PASS: TestApprovalResolved_TheDesktopAnsweringResolvesLocally (0.28s)
--- PASS: TestApprovalResolved_AnApprovalStillWaitingIsNotResolvedByAnyStatusEmit (0.52s)
--- PASS: TestOrphanSweep_InstanceDeathClosesEveryOpenItemBeforeTheTerminalSessionStatus (1.30s)
--- PASS: TestApprove_AValidApproveIsAcceptedAndResolvesTheCard (0.41s)
--- PASS: TestApprove_AStaleOrMismatchedApproveIsRefusedWithACodeAndAppliesNothing (0.68s)
    --- PASS: .../a_foreign_machine
    --- PASS: .../an_unknown_interaction
    --- PASS: .../a_different_agent_instance
    --- PASS: .../a_rewritten_content_hash
    --- PASS: .../a_pushed-out_expiry
    --- PASS: .../a_decision_the_card_never_offered
    --- PASS: .../no_decision_at_all
--- PASS: TestApprove_AnApproveAfterTheDaemonWindowIsRefusedEvenWhenEveryFieldMatches (0.43s)
--- PASS: TestApproveReq_CarriesTheChosenDecisionOnTheWire (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	10.853s

$ go test ./internal/skeleton/ -run TestInteractionE2E_AResolvedApprovalDismissesThePhoneCard -count=1 -v
--- PASS: TestInteractionE2E_AResolvedApprovalDismissesThePhoneCard (8.08s)
```

The end-to-end case is the join W4 could not make: the machine's own resolver, through the
append floor, the journal, a real gateway process, a real relay and the durable phone core, to
`PendingApprovals` emptying — while the resolved `approval_request` STAYS in the transcript,
because resolving lifts IS-LIFE-3's retention exemption and does not delete history.

### 8.4 A weak assertion of my own, declared

`TestApprove_AnApproveAfterTheDaemonWindowIsRefusedEvenWhenEveryFieldMatches` **passed under
mutation 5** (the daemon-clock check removed) in its first form, and the reason is worth
recording: it wound back only the DAEMON's stored expiry, which made the phone's echoed copy
disagree — so the approve was refused by the ECHO check and the clock check was never reached.
The test passed for the wrong reason.

Strengthened to wind the window back on BOTH sides, which is what a card minted 121 s ago and
tapped now actually looks like: the phone echoes what it received, and what it received is now
in the past. Re-run against unchanged production code under the same mutation, it fails
correctly (`an approve past the daemon's own window was accepted`). No pre-existing test was
touched; this is my own new test being made honest.

A residual race is stated rather than papered over: the 125 ms expiry sweep could in principle
resolve the approval in the ~microsecond gap between the test's unlock and
`approveInteraction`'s lock, in which case the refusal comes from the "no approval pending" arm
instead. The assertions hold either way; only the mutation's ability to be caught would be lost,
and only on that ~1e-5 of runs.

### 8.5 TEETH — eight mutations, each reverted

| # | Mutation | Result |
|---|---|---|
| 1 | `openApprovalLocked` no longer supersedes its predecessor | FAIL: `TestApprovalResolved_ANewerRequestSupersedesTheOlderOne` — and its dump shows the tuple now shipping (`agent_instance`, `content_hash`, `expires_at` all present on both cards) |
| 2 | the CLI-withdrawal arm in `shapeItem` is removed | FAIL, both `TestApprovalResolved_ACLIWithdrawalCancelsTheRequest` AND the end-to-end case (`timed out after 45s: the approval_resolved reached the phone's transcript`) — this is the e2e's failing-first |
| 3 | `answered_locally` fires on the STATE (`if !awaitingInput(cur)`) rather than the transition | FAIL: the negative control alone — `an approval_resolved landed for a request still waiting: map[by:owner decision:answered_locally …]`. The control isolates exactly the failure it exists for |
| 4 | the hash is taken over a DIFFERENT canonicalization (slot removed rather than zeroed) | FAIL: `TestApprovalRequest_TheContentHashCoversTheItemAsShipped`, with both digests printed — the canonicalization is pinned exactly, not merely "some hash is present" |
| 5 | the daemon-clock expiry check is removed, trusting the phone's echoed value | FAIL (after 8.4's strengthening): `an approve past the daemon's own window was accepted` |
| 6 | the terminal `session_status` is emitted FIRST instead of last | FAIL: `TestOrphanSweep_…` — IS-ST-2's ordering is asserted, not assumed |
| 7 | the decision-membership check is removed | FAIL, two rows: `a decision the card never offered was ACCEPTED`, and `no decision at all` degrades to `stale_approval` — the second shows the two codes are distinguished, not merely non-empty |
| 8 | the sweep closes items but resolves no pending approval | FAIL: `TestOrphanSweep_…` — IS-LIFE-2 is unconditional and the sweep is one of its paths, not an exception to it |

### 8.6 Gates

```
$ go build ./...                                                   # clean
$ go vet ./...                                                     # clean
$ gofmt -l internal/skeleton/approval.go internal/skeleton/approval_r4_test.go \
         internal/skeleton/approval_validate_r4_test.go internal/skeleton/approval_e2e_r4_test.go \
         internal/skeleton/interaction.go internal/skeleton/serve.go                     # clean
$ go test ./internal/skeleton/ -count=1 -race                      ok  193.868s
$ go test ./internal/daemon/ ./internal/remotegw/ ./internal/phonecore/ \
          ./internal/protocol/... ./internal/verify/ -count=1 -race                       all ok
$ go test ./... -count=1                                           all ok (no FAIL, module-wide)
```

The three `protocol.md` drift fences are untouched and green — no `Control` field moved and no
op was added, which is what keeps `ApproveReq`'s posture as it was:

```
--- PASS: TestProtocolMDBidi_FieldSetMatchesStructs (0.01s)
--- PASS: TestProtocolMD_ExistsAndDocumentsEveryField (0.00s)
--- PASS: TestProtocolMD_DocumentsEveryOp (0.00s)
```

B94 is unmoved — every symbol this slice adds is unexported, deliberately: an exported
`ApproveInteraction` with no production entry point would be a new row in the unreachable ledger,
and the future caller (`coreAPI`) lives in this package anyway.

```
    phaseb_reachability_test.go:310: B94: 540 exported symbols examined, 54 unreachable and all accounted for
--- PASS: TestB94_EveryExportedSymbolIsReachableFromProduction (3.54s)
```

`golangci-lint run internal/skeleton/... internal/protocol/schema/...` reports ZERO findings in
`approval.go`, the three new test files, `interaction.go` or `schema.go`; the packages'
pre-existing errcheck/staticcheck findings are all in files this change does not touch.
`gofmt -l` does flag `internal/protocol/schema/schema.go`, and the misalignment is PRE-EXISTING
(`PairingControl.ShortCode`, present at HEAD, verified by formatting `git show HEAD:` output).
It was left alone rather than swept into this diff.

### 8.7 What is deliberately still open — and the one that needs an owner ruling

- **`denied` IS NOT REACHABLE FROM THE PHONE PATH, and this is the finding's one genuine gap.**
  > **CLOSED 2026-08-07 by owner ruling** — the first of the two closures this bullet named was
  > taken: `adapter.DecisionChoice` gains an additive `Verdict` field. See "Owner ruling
  > 2026-08-07 — the decision verdict" at the foot of this file.
  §3.6's `allowed`/`denied` split needs a NORMALIZED verdict for a decision id drawn from the
  CLI's OWN vocabulary — §3.5 says so explicitly, and spike-SB captured Codex offering
  `accept | acceptWithExecpolicyAmendment | cancel`, where the third is a refusal that travels
  as the same signed `ActionApprove`. `adapter.DecisionChoice` carries `{ID, Label}` and no
  verdict bit, and `internal/adapter` is outside this task's touch list, so the daemon cannot
  classify `cancel` as a refusal WITHOUT GUESSING at a CLI's vocabulary — which is exactly the
  posture IS-TOOL-2 forbids for the same reason. A validated approve therefore resolves as
  `allowed`. The bit belongs to the slice that APPLIES the decision (ADR-010 §4): it writes the
  CLI's reply and reads the outcome, and it calls the same resolver with `denied`. **Today the
  gap is unreachable rather than wrong** — no adapter implements `InteractionSource`, so no card
  with decisions on it can be tapped — but it is a gap, and closing it is one of: an additive
  verdict field on `adapter.DecisionChoice`, or a §3.5 amendment saying which side normalizes.
  **This needs an owner ruling.**
- **The approve has no wire route.** `opForAction` refuses one ("approve is not a daemon remote
  op (D6/D7)"), so `approveInteraction` is reachable only from tests today. That is the task's
  boundary rather than an omission — routing it means a new `Op`, a new `protocol.md` row and a
  gateway arm, all outside the touch list — but it is what makes the validation live.
- **Applying the decision is not built**, by the task's own scope: the adapter's
  `DecisionAction` is never written back to the CLI's pending hook. The lifecycle stops at
  validated-and-recorded.
- **`session_status` is emitted ONLY by the orphan sweep.** IS-SS-1's general transcript marker —
  a `session_status` for every meaningful transition, not merely the terminal one — is a
  different rule and is not built.
- **The `cancelled` signal is inferred, not sourced.** A terminal record for the CLI's own
  request id is what the daemon reads as a withdrawal; an adapter that withdraws a prompt
  SILENTLY (emitting nothing) leaves the request to expire instead. That is a 120 s stale card,
  not a permanent one, so it degrades rather than breaks — but a first-class withdrawal signal
  would be an additive `Interaction` field.
- **The expiry sweep is per-tick, not per-deadline.** A request expires within one 125 ms window
  of its deadline. That reuses the append floor's existing ticker rather than adding a timer per
  approval, and 125 ms against a 120 s window is not a number anybody can observe.

---

# Re-review of R4 and R5 (adversarial, against the closures)

## R5 — verified on the shipped path

The five refusals were driven at `daemon.RecordInteractionRaw` — the entry the floor releases into
— rather than at the typed constructor:

```
$ // an item with no ts, offered as bytes
interaction: item has no "ts" (§2: required, and the wire record carries none to substitute)
$ // full_bytes without truncated, offered as bytes
interaction: "full_bytes" is carried only with "truncated" (§2)
```

Both refusals are real on the shipped path, which is the whole of the finding. **R5's closure
holds.**

## R4 — the lifecycle, verified path by path

| IS-LIFE-2 path | Driven by | Result |
|---|---|---|
| `superseded` | a second request for the session | emits, `by: agent` |
| `cancelled` | a terminal record for the pending CLI ref; and the orphan sweep | emits, `by: agent` |
| `expired` | **`sweepExpiredApprovals`, newly covered** (see below) | emits, `by: daemon` |
| `answered_locally` | the transition OUT of permission/prompt | emits, `by: owner` |
| `allowed` | a validated approve | emits, `by: phone`, `operation_id` echoed |
| `denied` | — | unreachable, as R4 disclosed; needs an owner ruling |

The stale-approve matrix was re-driven and one row added that it did not carry (`ShimPID`, where
the shipped matrix mutates `ShimStartTime` only) plus an approve routed at a different local
session on the right machine. Both refuse. Expiry against the daemon's own clock refuses even when
every echoed field matches. The IS-LIFE-3 retention exemption lifting is covered end to end by
`TestInteractionE2E_AResolvedApprovalDismissesThePhoneCard`, which passes.

### Newly covered — the `expired` arm through the ticker

`sweepExpiredApprovals` had NO test. `approvalTTL` is a bare constant with no clock seam, so the
shipped 120 s window cannot be waited out, and the inline re-check inside `approveInteraction` is a
different code path. `expired` is the resolution for the card NOBODY answers — the one case where
the phone's IS-LIFE-3 exemption would otherwise never lift, leaving the item unanswerable AND
unevictable. `interaction_rr_test.go`'s
`TestApprovalResolved_TheDaemonWindowLapsingResolvesACardNobodyAnswered` winds the daemon's own
stored window back and drives the sweep, then drives it twice more to pin the exactly-one
guarantee under the ticker's own repetition. It passes against the shipped code — coverage added,
not a defect found.

### Newly covered — truncate-then-hash where §5's caps actually BIND

R4's hash fence uses a small item, so truncate-then-hash is asserted where nothing truncates; R2's
maxima cases are ASCII, so IS-CAP-1's rune boundary is asserted where every cut is a byte boundary
anyway. `TestApprovalRequest_AtTheMaximaTheHashStillNamesTheBytesItShipped` drives both at once —
an `approval_request` on §5's documented maxima whose 40 prompt lines are 200 FOUR-BYTE runes each
(32 000 bytes of prompt against an 8 KiB item cap), so the uniform ceiling halves several times and
every cut lands inside a multi-byte rune. Measured: **7 430 bytes shipped, 128-byte ceiling, 32
runes per line surviving, no U+FFFD anywhere in the payload, and the digest re-derives exactly**.
R2's rune-boundary clamp and R4's truncate-then-hash ordering both hold under the one input that
exercises them together.

## Defect found and fixed — a request superseded by its OWN re-announcement (IS-LIFE-2, IS-LIFE-3)

**What was wrong.** `openApprovalLocked` identified "a second request" by the SESSION alone:

```go
out := d.resolveApprovalLocked(session, resolveSuperseded, byAgent, "")
```

A CLI that re-announces its still-pending permission — the same adapter `Ref`, therefore the same
minted `item_id`, which is exactly what `Ref` is for — made the daemon supersede the very card it
was re-opening. This is ordinary CLI behaviour, not an exotic input: spike-SB captured Claude
Code's `Notification` firing beside `PermissionRequest`, and any adapter shaping the outstanding
permission off a second hook produces it.

**Two rules broke at once.**

- **IS-LIFE-2** ("every `approval_request` SHALL reach EXACTLY ONE `approval_resolved`"): the
  spurious `superseded` is the request's first resolution, and its real one — allowed, cancelled,
  expired — is its second.
- **IS-LIFE-3's retention exemption, on the phone.** `ItemStore.resolveLocked` marks the request
  `Resolved` off that record, which drops it out of `PendingApprovals()` and lifts the trimming
  exemption. The owner's card vanishes from the one surface that shows it while the CLI is still
  blocked and the daemon still holds the request pending — the precise failure the exemption exists
  to prevent, arrived at from the other direction.

**The fix** (`approval.go`, +8 lines of which 7 are comment): supersede only when the pending
request is a DIFFERENT item. The binding tuple is still restamped from the newer record, because
the phone folds the newer record over the older one and therefore echoes the newer hash and expiry
(IS-APR-2) — so the daemon's stored tuple and the card's rendered one stay the same object.

### RED — verbatim, failing first

```
$ go test ./internal/skeleton/ -run 'TestApprovalResolved_ARequestIsNotSupersededByItsOwnReAnnouncement' -count=1 -v
=== RUN   TestApprovalResolved_ARequestIsNotSupersededByItsOwnReAnnouncement
    approval_rr_test.go:52: the still-pending request 01KZEJGS15A4093SB70KV43H9J was resolved by its OWN re-announcement: map[by:agent decision:superseded interaction_id:01KZEJGS15A4093SB70KV43H9J item_id:01KZEJGS1W1PKDGXZ5T5Q1CT1R kind:approval_resolved ts:2026-08-07T16:57:19.676868Z v:1].
        A second record for the SAME item_id is the same request, not a second one -- IS-LIFE-3's "one pending approval per session" is what `superseded` names, and superseding an item with itself breaks IS-LIFE-2's exactly-one guarantee AND lifts the phone's IS-LIFE-3 retention exemption for a card the CLI is still blocked on
--- FAIL: TestApprovalResolved_ARequestIsNotSupersededByItsOwnReAnnouncement (2.81s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	3.908s
```

Note the record: `interaction_id` equals the item that is being re-opened.

### GREEN

```
$ go test ./internal/skeleton/ -run 'TestApprovalResolved_' -count=1 -v
--- PASS: TestApprovalResolved_ANewerRequestSupersedesTheOlderOne (2.84s)
--- PASS: TestApprovalResolved_ACLIWithdrawalCancelsTheRequest (0.52s)
--- PASS: TestApprovalResolved_TheDesktopAnsweringResolvesLocally (0.16s)
--- PASS: TestApprovalResolved_AnApprovalStillWaitingIsNotResolvedByAnyStatusEmit (0.54s)
--- PASS: TestApprovalResolved_ARequestIsNotSupersededByItsOwnReAnnouncement (0.64s)
ok  	github.com/Nathandela/swarm/internal/skeleton	6.290s

$ go test ./internal/skeleton/ -count=1 -race
ok  	github.com/Nathandela/swarm/internal/skeleton	199.923s
```

### Teeth — two mutations, each reverted

1. Restoring the unconditional supersede (`ap.itemID != ""`) fails only the new case — the
   genuine-supersede fence stays green, so the two are distinguishable and the guard is not
   over-scoped.
2. Skipping the supersede ALWAYS (`if false`) fails
   `TestApprovalResolved_ANewerRequestSupersedesTheOlderOne` — so the guard did not quietly delete
   the rule it narrows.

**R4's closure holds otherwise**, and its four disclosed open points (`denied` unreachable, the
content-hash canonicalization needing sign-off, no wire route for the approve, `session_status`
only from the sweep) are all still accurate as written.

---

# Owner ruling 2026-08-07 — the decision verdict

**Ruling** (Nathan, 2026-08-07): *"`adapter.DecisionChoice` gains a `Verdict` field
(allow | deny | other) the adapter sets at capture from its own CLI vocabulary; the daemon
classifies `approval_resolved` allowed/denied from the chosen decision `Verdict`; conformance
obliges every `approval_request` decision to carry a valid verdict."*

This closes §8.7's first open point — the one that bullet said "needs an owner ruling" — by the
first of the two closures it named. Recorded normatively as ADR-010's **Amendment 2026-08-07**
(conformance obligation 6) and interaction-schema.md's **IS-APR-4** (§3.5) and **IS-RES-1** (§3.6).

## The shape of the answer, and why each half sits where it does

The old code resolved every validated approve as `allowed` and said so in its own comment. It was
right to refuse to guess: §3.5 keeps decision **ids** the CLI's own on purpose, so `cancel` is a
refusal only because Codex says it is. The fix is not to teach the daemon a vocabulary — it is to
have the party that already knows attach one normalized bit at capture, exactly as `Mode` is
attached at capture because that is where the spike-SC carve-out is decidable.

**Three decisions, recorded:**

1. **The verdict is MACHINE-SIDE — no wire field.** It rides `adapter.Interaction` into the daemon
   and stops there, on the `Keystrokes` precedent (IS-APR-3). The card labels its buttons from
   `decisions[].label` and no phone surface switches on polarity, so a wire field would be a second
   place for the two to disagree, shipped ahead of any consumer. A phone that later wants it (to
   style a destructive button) is an additive §3.5 field and a schema change.

2. **The obligation is split between `Validate` and `CheckConformance`, deliberately.**
   `Interaction.Validate` rejects a verdict **outside the vocabulary** — a shape error, because the
   daemon switches on the value — but accepts an **absent** one. `CheckConformance` requires
   presence. The split is what keeps the two failure modes distinguishable: a decision with no
   verdict is not malformed, it is *incomplete*, and `Validate` is the daemon's own admission gate
   (IS-ENV-3 drops the whole item), so requiring presence there would delete an adapter's approval
   card rather than report the adapter. Completeness is what conformance exists to prove.
   "The measurement behind decision 2" below is that paragraph run as an experiment rather than
   argued.

3. **One branch, on `deny` — and `other` resolves `allowed`.** §3.6 has no third value for a
   remote answer. `denied` is an assertion that the owner REFUSED, so manufacturing one from a
   decision the adapter declared unclassifiable would be exactly the guess the verdict exists to
   remove; `allowed` here asserts only "answered from the phone, and not refused". This is the
   weaker half of the ruling and it is written into IS-RES-1 rather than left to the code, and
   fenced (`TestApprove_AnOtherVerdictDecisionResolvesAllowedNotDenied`) so the arm cannot flip in
   either direction unnoticed.

## RED — verbatim, failing first (GG-5)

**Cycle 1, undefined-only** — the field does not exist. This is the same RED shape the carriage and
daemon slices used for a new seam:

```
$ go test ./internal/adapter/ ./internal/skeleton/ -run 'TestInteractionValidate_|TestConformance_Requires|TestApprove_A.*Verdict' -count=1
# github.com/Nathandela/swarm/internal/adapter [github.com/Nathandela/swarm/internal/adapter.test]
internal/adapter/verdict_test.go:27:62: unknown field Verdict in struct literal of type DecisionChoice
internal/adapter/verdict_test.go:33:13: undefined: VerdictAllow
internal/adapter/verdict_test.go:33:27: undefined: VerdictDeny
internal/adapter/verdict_test.go:33:40: undefined: VerdictOther
internal/adapter/verdict_test.go:44:29: undefined: VerdictAllow
internal/adapter/verdict_test.go:44:43: undefined: VerdictDeny
internal/adapter/verdict_test.go:44:56: undefined: VerdictOther
internal/adapter/verdict_test.go:47:54: unknown field Verdict in struct literal of type DecisionChoice
FAIL	github.com/Nathandela/swarm/internal/adapter [build failed]
# github.com/Nathandela/swarm/internal/skeleton [github.com/Nathandela/swarm/internal/skeleton.test]
internal/skeleton/approval_verdict_test.go:28:34: unknown field Verdict in struct literal of type adapter.DecisionChoice
internal/skeleton/approval_verdict_test.go:28:51: undefined: adapter.VerdictAllow
internal/skeleton/approval_verdict_test.go:29:74: unknown field Verdict in struct literal of type adapter.DecisionChoice
internal/skeleton/approval_verdict_test.go:29:91: undefined: adapter.VerdictAllow
internal/skeleton/approval_verdict_test.go:30:33: unknown field Verdict in struct literal of type adapter.DecisionChoice
internal/skeleton/approval_verdict_test.go:30:50: undefined: adapter.VerdictDeny
internal/skeleton/approval_verdict_test.go:67:39: undefined: adapter.VerdictDeny
internal/skeleton/approval_verdict_test.go:90:62: undefined: adapter.VerdictAllow
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
FAIL
```

**Cycle 2, BEHAVIOURAL** — the field and its three constants exist and the stubs carry them, but
no rule reads either. This is the RED that matters, and it was taken as a separate step precisely
so a compile error could not stand in for one:

```
$ go test ./internal/adapter/ -run 'TestInteractionValidate_|TestConformance_Requires' -count=1 -v
=== RUN   TestInteractionValidate_RejectsAnUnknownDecisionVerdict
    verdict_test.go:31: Validate accepted decisions[0].verdict = "maybe"; it is a CLOSED vocabulary (allow | deny | other) and the daemon resolves §3.6's allowed/denied split off it
--- FAIL: TestInteractionValidate_RejectsAnUnknownDecisionVerdict (0.00s)
=== RUN   TestInteractionValidate_AcceptsEachDefinedVerdict
--- PASS: TestInteractionValidate_AcceptsEachDefinedVerdict (0.00s)
=== RUN   TestConformance_RequiresAVerdictOnEveryApprovalDecision
    verdict_test.go:65: an approval_request whose decision carries NO verdict was NOT flagged. The daemon classifies §3.6's allowed/denied off the chosen decision's verdict, so a decision without one resolves as `allowed` whatever the owner tapped
--- FAIL: TestConformance_RequiresAVerdictOnEveryApprovalDecision (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter	0.729s

$ go test ./internal/skeleton/ -run 'TestApprove_A.*VerdictDecisionResolves' -count=1 -v
=== RUN   TestApprove_ADenyVerdictDecisionResolvesDenied
    approval_verdict_test.go:65: decision = allowed; want "denied". The owner tapped "cancel", which the adapter classified deny at capture -- transcribing it as an approval records a grant the owner never gave (§3.6)
--- FAIL: TestApprove_ADenyVerdictDecisionResolvesDenied (4.18s)
=== RUN   TestApprove_AnAllowVerdictDecisionResolvesAllowed
--- PASS: TestApprove_AnAllowVerdictDecisionResolvesAllowed (0.42s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	5.629s
```

Note which two passed under RED, and why that is the point: the allow arm and the
accepts-each-verdict control were already satisfied by the old unconditional `allowed`, so only
the deny arm and the two rule checks are new information.

## GREEN

```
$ go test ./internal/adapter/ -run 'TestInteractionValidate|TestConformance' -count=1
ok  	github.com/Nathandela/swarm/internal/adapter

$ go test ./internal/skeleton/ -run 'TestApprove_A.*Verdict' -count=1 -v
--- PASS: TestApprove_ADenyVerdictDecisionResolvesDenied (3.34s)
--- PASS: TestApprove_AnAllowVerdictDecisionResolvesAllowed (0.40s)
--- PASS: TestApprove_AnOtherVerdictDecisionResolvesAllowedNotDenied (0.41s)
ok  	github.com/Nathandela/swarm/internal/skeleton	5.201s
```

Every pre-existing approval fence passes **unmodified**, including
`TestApprove_AValidApproveIsAcceptedAndResolvesTheCard` (whose fixture carries no verdicts, so it
exercises the default arm) and the seven-row refusal table, whose "a decision the card never
offered" and "no decision at all" rows now run against a map lookup instead of a slice scan.

## What changed

| File | What |
|---|---|
| `internal/adapter/interaction.go` | `VerdictAllow` / `VerdictDeny` / `VerdictOther`; `DecisionChoice.Verdict` with the machine-side ceiling stated on the field; `Validate` rejects a value outside the three (optional-empty). |
| `internal/adapter/conformance.go` | Obligation 6 inside `checkShapedItems`, so it runs from both `CheckConformance` and `CheckInteractionFixture` — every adapter already calling `Conformance(t, a)` gets it. |
| `internal/adapter/interaction_stubs_test.go` | `decisionWithoutVerdict` (new violator); the conformant `captureAdapter` and the three other single-defect approval violators carry verdicts, so each still breaks exactly one rule. |
| `internal/skeleton/approval.go` | `pendingApproval.decisions` is `map[id]verdict` — one lookup for both membership and polarity; the resolution classifies off it; `containsString` deleted (its only caller is gone). |
| `docs/specifications/interaction-schema.md` | IS-APR-4 (§3.5) and IS-RES-1 (§3.6); the `decisions` row notes the machine-side verdict. |
| `docs/adr/ADR-010-adapter-structured-capture.md` | Amendment 2026-08-07: the field, why the adapter owns it, and conformance obligation 6 with the Validate/Conformance split. |
| `internal/adapter/verdict_test.go`, `internal/skeleton/approval_verdict_test.go` (new) | Six fences: the vocabulary, the control, the obligation, and all three resolution arms. |

## Teeth — three mutations, each reverted

| Mutation | Result |
|---|---|
| Classify `verdict != allow` as denied (so `other` counts as a refusal) | **FAILS** `TestApprove_AnOtherVerdictDecisionResolvesAllowedNotDenied`: "decision = denied; want allowed (IS-RES-1)". The deny and allow arms both stay green, so the three arms are genuinely distinguishable and the `other` ruling is fenced rather than incidental. |
| `if d.Verdict == ""` → `if false` in `checkShapedItems` | **FAILS** `TestConformance_RequiresAVerdictOnEveryApprovalDecision`; `TestConformance_AcceptsCapturingAdapter` stays green, so the obligation is not merely noisy. |
| Drop the `oneOf` verdict check from `Validate` | **FAILS** `TestInteractionValidate_RejectsAnUnknownDecisionVerdict`; the conformance test stays green, which is what proves the two halves are independent rather than one rule tested twice. |

### A fourth mutation, added by the adversarial review — MISSED, then fenced

Decision 1 above says the verdict is machine-side and reaches no wire item (IS-APR-4). Nothing
tested it. The mutation is one word in `skeleton.interactionFields`:

```go
-	decisions = append(decisions, map[string]string{"id": d.ID, "label": d.Label})
+	decisions = append(decisions, map[string]string{"id": d.ID, "label": d.Label, "verdict": d.Verdict})
```

`go test ./internal/skeleton/ ./internal/adapter/... ./internal/daemon/... -count=1` → **all
green**. The whole suite shipped the machine-side bit to the phone without a single failure — the
same class of hole `keystrokes` had before `assertNoKeystrokeLeak` existed
(a1b-claude-producer.md §11 mutation 3), on the field added by this very ruling.

Fixed by **strengthening**, never weakening: `assertDecisions` in
`internal/skeleton/interaction_chain_e2e_test.go` — already called on both cards the chain test
raises — now also requires each wire decision object to carry **exactly** `id` and `label`. It is
the right home for the same reason the keystroke check is: it reads the decision as the PHONE
received it, after the producer, the floor, the gateway, the relay and the facade.

RED, against the live mutation (both cards, native and prompt-card):

```
$ go test ./internal/skeleton/ -run TestClaudeChainE2E -count=1
--- FAIL: TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone (9.06s)
    interaction_chain_e2e_test.go:194: decision "allow" carried "verdict" to the phone: allow. §3.5 puts {id, label} on the wire and IS-APR-4 keeps the verdict MACHINE-SIDE -- the daemon classifies allowed/denied from it and no phone surface switches on polarity
    interaction_chain_e2e_test.go:194: decision "deny" carried "verdict" to the phone: deny. §3.5 puts {id, label} on the wire and IS-APR-4 keeps the verdict MACHINE-SIDE -- the daemon classifies allowed/denied from it and no phone surface switches on polarity
    interaction_chain_e2e_test.go:279: decision "allow" carried "verdict" to the phone: allow. §3.5 puts {id, label} on the wire and IS-APR-4 keeps the verdict MACHINE-SIDE -- the daemon classifies allowed/denied from it and no phone surface switches on polarity
    interaction_chain_e2e_test.go:279: decision "deny" carried "verdict" to the phone: deny. §3.5 puts {id, label} on the wire and IS-APR-4 keeps the verdict MACHINE-SIDE -- the daemon classifies allowed/denied from it and no phone surface switches on polarity
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	10.114s
```

GREEN, with the mutation reverted:

```
$ go test ./internal/skeleton/ -run TestClaudeChainE2E -count=1 -race
ok  	github.com/Nathandela/swarm/internal/skeleton	13.546s
```

**The measurement behind decision 2** (a fourth mutation, kept as a measurement rather than a
fence — the numbering above restarts for this ruling and does not continue R4's).
Making `Validate`'s verdict check
NON-optional (an absent verdict is a violation) turns the completeness obligation into an
admission gate: every approval fixture in `internal/skeleton` that predates this ruling —
`pendingApprovalInteraction` and its dependents — has its item dropped by `captureInteractions`
under IS-ENV-3, and **12 of the 15 approval tests fail** with no `approval_request` in the journal
at all (measured: `go test ./internal/skeleton/ -run TestApprov` → 12 `--- FAIL`, each a 10 s
`awaitItems` timeout). That is the concrete cost of putting it there, and it is why decision 2
above puts it in conformance instead. The three survivors are the ones that never capture a card.

## Gates

```
$ go build ./...                                  OK
$ go vet ./...                                    OK
$ gofmt -l (files touched)                        clean
$ go test ./internal/adapter/... -count=1 -race   ok  (9 packages)
$ go test ./internal/skeleton/  -count=1 -race    ok  196.455s
$ go test ./internal/daemon/    -count=1 -race    ok  54.298s
$ go test ./internal/remotegw/  -count=1 -race    ok  33.972s
$ go test ./internal/protocol/  -count=1 -race    ok  19.534s   (GG-7 drift fences green)
$ go test ./internal/verify/    -count=1 -race    ok  35.104s   (B94 reachability green)
$ go test ./... -count=1 -p 1                     exit 0, whole tree
$ golangci-lint run ./internal/{adapter,skeleton,daemon}/...
                                                  48 issues — IDENTICAL to the pre-change
                                                  baseline measured by stashing this work
                                                  (39 errcheck, 9 staticcheck, none in any
                                                  file touched here)
```

`-p 1` on the whole-tree run is not cosmetic: with packages in parallel,
`TestLaunch_InjectsHookEnvToAgent` fails on "daemon: another instance is already running" — a
pre-existing lock-path contention between packages that each stand up a daemon, unrelated to
either ruling (it passes alone, and the whole `internal/daemon` package passes alone with and
without `-race`). Recorded so the next reader does not attribute it here.

## What this ruling does NOT close

- **The approve still has no wire route** (§8.7, unchanged): `opForAction` refuses one, so
  `approveInteraction` is reachable only from tests. The verdict makes the *classification*
  correct; it does not make the path live.
- **Applying the decision is still not built.** `DecisionAction` is never written back to the
  CLI's pending hook, so `denied` is recorded in the transcript and not yet enacted on the agent.
  That was always the applying slice's work; what changes is that it no longer also owes the
  classification.
- **No adapter sets a verdict yet**, because no adapter implements `InteractionSource` at all. The
  conformance obligation is what makes the first one that does carry the bit — a compile-clean
  adapter that forgets it fails its own package's `Conformance(t, a)` call.

  > **OVERTAKEN 2026-08-07 (a1b)** — the first one landed the same day.
  > `internal/adapter/claude` implements `InteractionSource` and its `approval_request` carries
  > `Verdict` on both decisions; the obligation held it to that from the first compile. What is
  > still true is the reachability caveat, and it moved rather than closed: the shaper is not
  > reachable from a real hook post because ADR-010 §6's carriage is unimplemented
  > (`docs/verification/a1b-claude-producer.md` §10).
