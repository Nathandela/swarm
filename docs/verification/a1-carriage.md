# A1 / W2 — interaction items: journal + wire carriage

**Workpackage**: W2 of the interaction program (ADR-009, ADR-010, `docs/specifications/interaction-schema.md`,
all Accepted 2026-08-07).
**Scope**: give an interaction item a durable home and a wire seat — the `interaction` journal
record type (IS-LAYER-1), the daemon-side append entry with the §2 envelope and its required
`ts`, §5's `MaxItemBytes` at that boundary, the additive `item` field on `schema.JournalRecord`,
its `protocol.md` row, and the single conversion hop that populates it.
**Out of scope, by the workpackage**: the eight kind types and their per-field caps, the
producer admission queue (ADR-010 §7 / IS-DELTA-2a), the push-wake rules (ADR-010 §4), the
approval lifecycle, and every phone-side consumer.

---

## Decisions recorded (points the schema leaves open)

1. **Write path — direct append, not the meta choke point, not under `writeMu`.**
   `Daemon.RecordInteraction` appends straight to `d.journal`, as a sibling of
   `RecordGatewayPresence`. Not through `saveMetaLocked`/`journalRecordFor`, because an item is
   not a meta transition — it is captured off an adapter event and correlates with no
   `persist.Meta` write, so the derivation switch has nothing to derive it from. Not under
   `writeMu`, because that mutex exists to keep `JournalReadFrom`'s roster snapshot consistent
   with the cursor it is taken at (R-JRN.4), and an interaction record is **roster-neutral**: it
   never writes `persist.Meta`, and `rosterSnapshotLocked` reads only `persist.Meta` and
   `d.sessions`. It is the `RecordGatewayPresence` argument with a session id attached. Cited in
   the function comment (IS-LAYER-1/-3/-4).

2. **`ts` is producer-owned, daemon-stamped only when unset.** §2 requires the machine instant
   for *this record* and forbids a consumer substituting arrival time (PB-APP-11). A producer
   that captured the event earlier passes its instant in; `RecordInteraction` stamps
   `time.Now().UTC()` only for a zero value, so the append time never silently replaces a known
   capture time.

3. **§3 kind fields ride flat beside the envelope.** §3 says its fields are "additional to the
   envelope", i.e. one flat object. `InteractionItem.Fields json.RawMessage` is merged in by
   `MarshalJSON`; a kind field colliding with an envelope key is an **error**, never an
   overwrite (a silently re-labelled `kind`/`item_id` would break IS-ENV-2's fold-by-item_id in
   a way no consumer could detect). `Fields` is raw rather than the eight typed kinds because
   this is the carriage seam: the kinds marshal into it, and IS-COMPAT-1/-2 make an unknown kind
   or field free here.

4. **Only `MaxItemBytes` is enforced at this boundary, and an over-cap item is refused.** It is
   the one §5 cap this seam can see; the per-field caps apply to kind fields that arrive here as
   an opaque blob and belong to the kind-shaping producer (with IS-CAP-1's rune-boundary
   truncation and the `truncated`/`full_bytes` pair). Clipping a serialized JSON object here
   would produce exactly the partial item IS-ENV-3 forbids, so the append is refused with an
   error instead. The number itself is flagged **proposed and unratified** in the code comment,
   as §5's own preamble requires.

5. **`kind` is checked for presence, not against the eight-value vocabulary.** The vocabulary
   belongs with the kind types (a different workpackage), and IS-COMPAT-1 already makes an
   unknown kind a consumer-side skip rather than a producer-side error.

6. **No new fence over `JournalRecord`.** GG-7's drift check reflects `Control`, `SessionView`,
   `LaunchReq` and `TerminalSnapshot` only, and interaction-schema.md §1 states in as many words
   that "no build can fail on a missing `item` … row" — the obligation is **procedural**. A test
   that made it fire would contradict a normative sentence of the spec. So the `protocol.md` row
   and the Go doc comment were written in the same change as the field, and the existing fence
   was run to prove it stayed green (below). The existing fence test was not touched.

---

## RED — failing first

`go test ./internal/journal/ ./internal/daemon/ ./internal/protocol/ ./internal/skeleton/`
run before any production change. Undefined-only, as intended:

```
# github.com/Nathandela/swarm/internal/journal [github.com/Nathandela/swarm/internal/journal.test]
internal/journal/interaction_test.go:41:56: undefined: TypeInteraction
internal/journal/interaction_test.go:63:17: undefined: TypeInteraction
internal/journal/interaction_test.go:64:57: undefined: TypeInteraction
internal/journal/interaction_test.go:109:18: undefined: TypeInteraction
FAIL	github.com/Nathandela/swarm/internal/journal [build failed]
# github.com/Nathandela/swarm/internal/daemon [github.com/Nathandela/swarm/internal/daemon.test]
internal/daemon/interaction_test.go:39:25: undefined: journal.TypeInteraction
internal/daemon/interaction_test.go:54:11: d.RecordInteraction undefined (type *Daemon has no field or method RecordInteraction)
internal/daemon/interaction_test.go:54:37: undefined: InteractionItem
internal/daemon/interaction_test.go:55:11: undefined: InteractionSchemaVersion
internal/daemon/interaction_test.go:86:26: undefined: InteractionSchemaVersion
internal/daemon/interaction_test.go:116:14: d.RecordInteraction undefined (type *Daemon has no field or method RecordInteraction)
internal/daemon/interaction_test.go:116:40: undefined: InteractionItem
internal/daemon/interaction_test.go:117:6: undefined: InteractionSchemaVersion
internal/daemon/interaction_test.go:144:8: undefined: InteractionItem
internal/daemon/interaction_test.go:146:12: undefined: InteractionItem
internal/daemon/interaction_test.go:146:12: too many errors
FAIL	github.com/Nathandela/swarm/internal/daemon [build failed]
# github.com/Nathandela/swarm/internal/protocol [github.com/Nathandela/swarm/internal/protocol.test]
internal/protocol/interaction_carriage_test.go:31:90: unknown field Item in struct literal of type JournalRecord
internal/protocol/interaction_carriage_test.go:39:28: back.Item undefined (type JournalRecord has no field or method Item)
internal/protocol/interaction_carriage_test.go:79:84: unknown field Item in struct literal of type JournalRecord
internal/protocol/interaction_carriage_test.go:91:36: got.Journal[0].Item undefined (type schema.JournalRecord has no field or method Item)
FAIL	github.com/Nathandela/swarm/internal/protocol [build failed]
# github.com/Nathandela/swarm/internal/skeleton [github.com/Nathandela/swarm/internal/skeleton.test]
internal/skeleton/interactionwire_test.go:26:22: undefined: journal.TypeInteraction
internal/skeleton/interactionwire_test.go:30:16: got.Item undefined (type protocol.JournalRecord has no field or method Item)
internal/skeleton/interactionwire_test.go:31:100: got.Item undefined (type protocol.JournalRecord has no field or method Item)
internal/skeleton/interactionwire_test.go:34:79: undefined: journal.TypeInteraction
internal/skeleton/interactionwire_test.go:48:9: got.Item undefined (type protocol.JournalRecord has no field or method Item)
internal/skeleton/interactionwire_test.go:49:92: got.Item undefined (type protocol.JournalRecord has no field or method Item)
FAIL	github.com/Nathandela/swarm/internal/skeleton [build failed]
FAIL
```

## GREEN

Same tests, after the production change. `-race` on `internal/daemon` (it spawns goroutines).

```
$ go test ./internal/journal/ -run 'Interaction' -v
=== RUN   TestInteractionRecordCarriesTheItemInItsPayload
--- PASS: TestInteractionRecordCarriesTheItemInItsPayload (0.08s)
=== RUN   TestInteractionTypeDidNotBumpTheSchema
--- PASS: TestInteractionTypeDidNotBumpTheSchema (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/journal	0.859s

$ go test ./internal/daemon/ -race -run 'Interaction' -v
=== RUN   TestDaemon_RecordInteractionAppendsBareInteractionRecord
--- PASS: TestDaemon_RecordInteractionAppendsBareInteractionRecord (0.05s)
=== RUN   TestDaemon_RecordInteractionKeepsAProducerSuppliedTS
--- PASS: TestDaemon_RecordInteractionKeepsAProducerSuppliedTS (0.03s)
=== RUN   TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope
--- PASS: TestDaemon_RecordInteractionEmitsNothingForAnIncompleteEnvelope (0.02s)
=== RUN   TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap
--- PASS: TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap (0.02s)
=== RUN   TestInteractionItem_KindFieldsMayNotCollideWithTheEnvelope
--- PASS: TestInteractionItem_KindFieldsMayNotCollideWithTheEnvelope (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/daemon	(cached)

$ go test ./internal/protocol/ -run 'JournalRecord|JournalEvent|ProtocolMD' -v
=== RUN   TestJournalRecordCarriesTheInteractionItem
--- PASS: TestJournalRecordCarriesTheInteractionItem (0.01s)
=== RUN   TestJournalRecordWithoutAnItemEncodesUnchanged
--- PASS: TestJournalRecordWithoutAnItemEncodesUnchanged (0.00s)
=== RUN   TestJournalEventControlCarriesTheItem
--- PASS: TestJournalEventControlCarriesTheItem (0.00s)
=== RUN   TestProtocolMDBidi_FieldSetMatchesStructs
--- PASS: TestProtocolMDBidi_FieldSetMatchesStructs (0.00s)
=== RUN   TestProtocolMD_ExistsAndDocumentsEveryField
--- PASS: TestProtocolMD_ExistsAndDocumentsEveryField (0.00s)
=== RUN   TestProtocolMD_DocumentsEveryOp
--- PASS: TestProtocolMD_DocumentsEveryOp (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/protocol	1.986s

$ go test ./internal/skeleton/ -run 'WireJournalRecord' -v
=== RUN   TestWireJournalRecordCarriesAgent
--- PASS: TestWireJournalRecordCarriesAgent (0.00s)
=== RUN   TestWireJournalRecordInventsNoAgent
--- PASS: TestWireJournalRecordInventsNoAgent (0.00s)
=== RUN   TestWireJournalRecordCarriesTheInteractionItem
--- PASS: TestWireJournalRecordCarriesTheInteractionItem (0.00s)
=== RUN   TestWireJournalRecordCarriesNoOtherPayload
--- PASS: TestWireJournalRecordCarriesNoOtherPayload (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	(cached)
```

### GG-7 drift fence, explicitly

Both directions pass with the `item` field and its `protocol.md` documentation landed in one
change: `TestProtocolMD_ExistsAndDocumentsEveryField` (forward, substring) and
`TestProtocolMDBidi_FieldSetMatchesStructs` (bidirectional, field-table rows vs struct tags).
Neither fence file was touched.

The fence never went RED for `item`, and that is the specified behaviour rather than a gap:
it reflects `Control`, `SessionView`, `LaunchReq` and `TerminalSnapshot` only, and
interaction-schema.md §1 records that "no build can fail on a missing `item` … row".

**One thing the bidirectional fence did catch**, and it is worth recording because it shaped
the doc: a `JournalRecord` table headed `JSON key` enlists its rows in the reverse check
against those four reflected structs, so the rows `type` and `item` — real fields of a type
the check does not reflect — failed it:

```
--- FAIL: TestProtocolMDBidi_FieldSetMatchesStructs (0.01s)
    protocolmd_bidi_test.go:40: protocol.md field-table row "item" has no matching struct json tag (GG-7 reverse drift)
    protocolmd_bidi_test.go:40: protocol.md field-table row "type" has no matching struct json tag (GG-7 reverse drift)
```

The table therefore heads its first column `Field`, and protocol.md says in the paragraph
below it exactly why, so a later reader does not "fix" the header and break the build. The
fence was not modified.

### Whole-package regression runs

```
ok  	github.com/Nathandela/swarm/internal/journal	(cached)
ok  	github.com/Nathandela/swarm/internal/protocol	9.832s
ok  	github.com/Nathandela/swarm/internal/protocol/schema	(cached)
ok  	github.com/Nathandela/swarm/internal/daemon	47.391s      (-race)
ok  	github.com/Nathandela/swarm/internal/skeleton	229.592s     (-race)
ok  	github.com/Nathandela/swarm/internal/remotegw	23.068s      (-race)
ok  	github.com/Nathandela/swarm/internal/phonecore	11.481s
ok  	github.com/Nathandela/swarm/mobile	21.641s
```

`go build ./...` and `go vet ./...` are clean. `golangci-lint run` over the touched packages
reports nothing in any file this workpackage added or changed (the repo's pre-existing
errcheck/staticcheck findings are untouched).

One environmental flake seen and re-run, recorded so it is not mistaken for a regression:
a `-count=1` pass of `internal/protocol` once failed in
`chunk_integration_test.go:84` with `daemon: shim for session … did not confirm serving`,
while another workpackage's `-race` suites were saturating the machine. It is a real shim
launch timing out under load, on a path this change does not touch; a re-run of that test
and of the whole package passed (`ok internal/protocol 26.780s`).

### One existing test line changed — declared

`internal/remotegw/relaysink_test.go:106` compared two `protocol.JournalRecord` values with
`!=`. A struct holding a slice is not comparable in Go, so the additive
`Item json.RawMessage` made that line a **compile error**, not a failing assertion — the
package could not build at all.

It was changed to `!reflect.DeepEqual(got, want[i])`, with a comment naming the reason. The
assertion is identical in strength (the whole record, field for field, against the one that
was sealed); nothing was weakened, removed or narrowed to make anything pass. The considered
alternative — a bespoke comparable raw-JSON type so the `==` would still compile — was
rejected as more machinery for no behavioural gain, and it would have left a `==` that
silently compares pointer identity rather than item content once items start flowing.

