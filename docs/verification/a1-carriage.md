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

---

# Review finding R2 (HIGH) — §5's per-field caps and the IS-CAP-1 truncator

**Landed after the fact**, closing an adversarial review of 79a070d. Appended rather than folded
into the sections above, so the record of what W2 decided stays as W2 wrote it.

## The finding

Beyond `MaxItemBytes`, none of §5's caps existed anywhere in the tree — no `MaxTextBytes`,
`MaxSummaryBytes`, `MaxPromptLines`, `MaxSteps` or `MaxDecisions` constant, and no truncator. An
item over the cap was **dropped** at the append boundary, which IS-CAP-1 forbids: an over-cap
item is truncated at a rune boundary with `truncated`/`full_bytes` set and its body left
fetchable (IS-CAP-2). The worst case is the one that blocks the agent — an `approval_request`
at §5's own documented maxima is **11 697 bytes**, so it was refused and the owner was never
asked.

## What changed

`internal/skeleton/interaction.go` only, plus a one-line pointer in `internal/daemon`'s
`MaxItemBytes` comment (it described the upstream truncator as something that ought to exist;
it now names it). The §5 per-field caps live in `internal/skeleton` because that is where the
§3 kind fields are shaped — which is exactly where `daemon.MaxItemBytes`'s own comment already
said they belonged. They are **unexported**: no other package shapes a kind field, and an
exported constant nothing outside can reach is a new entry in B94's unreachable ledger. B94 is
unmoved at *540 exported symbols examined, 54 unreachable and all accounted for*.

The shaping seam gained one stage, `fitItem`, and `shapeItem` now returns the item **serialized**
— §5's caps are measured on the serialization and IS-CAP-1's pair is decided by it, so the two
belong together. `captureInteractions` lost its separate `json.Marshal` step; net, the pipeline
is one call shorter.

## Decisions recorded (points §5 leaves open)

1. **§5's per-field caps are NOT jointly sufficient, so there are two stages.** This is the
   finding's real content and it is not stated in the spec. An `approval_request` sitting exactly
   on the documented maxima — 40 prompt lines × 200 runes, a 256 B summary, 256 B action strings,
   8 decisions with 256 B labels — serializes to ~11.7 KiB against an 8 KiB `MaxItemBytes`. A
   `plan_update` at its maxima is worse: 64 × 200 B is 12.8 KiB of step text alone. So `capFields`
   applies §5's table per field, and if the item is *still* over, `clipStrings` lowers one ceiling
   across every string, halving until it fits.

2. **The fit stage uses a UNIFORM ceiling, not a priority order.** Something has to give and §5
   names no order in which fields should give it. A shed order would be this seam ruling on which
   half of a card matters — a judgement for the schema, not for the producer. The uniform ceiling
   makes the choice nobody's: it cuts the longest strings hardest, leaves short ones untouched,
   needs no per-kind knowledge, and so costs a ninth kind nothing (IS-COMPAT-3). Measured: the
   approval_request above ships at a 128-byte ceiling (7 316 B), the 64-step plan at 64 (6 176 B).

3. **`full_bytes` is measured on the untruncated serialization.** §2 says "byte length of the
   untruncated payload", so the item is marshalled **once with nothing clipped** and that length
   is kept. This is why `interactionFields` applies no cap of its own and capping is a separate
   in-place pass — an approximation (final length plus bytes dropped) would be wrong under JSON
   escaping, and `full_bytes` is the one number a consumer uses to decide whether to fetch the
   detail body.

4. **An item is NEVER dropped for size.** The fit is guaranteed: `clipStrings` reaches every
   non-enum string the builder can produce, including the ones §5's table never names (`tool`,
   `path`, `old_path`, `truncation_marker`, a decision's `id`). Those get no *per-field* cap on
   purpose — §5 does not give them one, and IS-TOOL-3 requires the truncation marker **verbatim**
   — so `MaxItemBytes` is the only bound they answer to, and the fit stage is where it is applied.
   `daemon.RecordInteractionRaw`'s refusal is left for genuinely malformed items.

5. **`approval_request`: TRUNCATE, THEN HASH.** IS-APR-2 makes the phone echo `content_hash`
   verbatim and forbids it computing one, and ADR-007 D7 makes the daemon recompute and reject a
   mismatch. A hash taken over the *pre-truncation* content would therefore name a body no
   surface holds: the rendered card could not reproduce it, and every approve from a truncated
   card would be refused as stale. The rule is written on `interactionFields` beside the existing
   note on why `content_hash`/`expires_at`/`agent_instance` are deliberately absent until
   IS-LIFE-4's `ApproveReq` lands — which is the change that must obey it. Nothing hashes an item
   today, so this is a stated rule and not a tested one, and it is flagged as such.

6. **Closed vocabularies are excluded from the fit ceiling — and the guard cannot fire today.**
   `mode`, `change`, `source`, `stop_reason`, `action.type` and `steps[].state` are never clipped:
   half an enum is not a smaller item, it is an invalid one, and unlike an unknown *kind* (which
   IS-COMPAT-1 lets a consumer skip) a mangled enum renders a wrong card. Disclosed plainly:
   **deleting this guard fails no test** (mutation 3 below). The ceiling only falls while the item
   exceeds 8 KiB, and §5's count caps bound the residue — the widest item is 64 steps, which fits
   at a 64-byte ceiling, while the longest enum is `in_progress` at 11 — so the ceiling never
   reaches an enum's length. It is kept because that headroom is arithmetic over numbers §5 marks
   **proposed and unratified**: a ruling that raises `MaxSteps` far enough starts shipping `pen`
   for `pending`, silently. Six map entries buy the invariant by construction instead of by a
   calculation nobody will redo when the number moves. The ponytail comment says exactly this.

7. **The caps are flagged proposed and unratified**, on the same terms §5's preamble sets and
   `daemon.MaxItemBytes` already carries. Nothing here ratifies a number.

## Disclosed, not fixed — the clipped bytes are gone, not parked

`detail` (§2, IS-CAP-2) is deliberately **left unset** on a truncated item, and that is the
honest state rather than an oversight: IS-CAP-2 requires it only for an item "that has a
retrievable full body", and there is no detail fetch — no unsigned `(session_id, item_id)` read,
no bounded retention (IS-CAP-3's proposed 200 items / 24 h), nowhere the untruncated content is
kept. Setting the flag would promise a fetch that answers nothing.

The consequence, stated plainly because the fix changes it: before this change an over-cap item
was **dropped whole**; after it, the item arrives and the clipped bytes are **discarded at the
producer**. `full_bytes` tells a consumer how much was cut, and IS-DELTA-4's "incomplete from
join" elision renders it, but nothing can recover it. That is strictly better than dropping the
card — an approval the owner can answer beats one they never see — and it is not the end state
§5 describes. Closing it is IS-CAP-2 + IS-CAP-3: retention plus the detail read, which is its own
slice and needs the daemon to hold the untruncated body somewhere this seam currently does not.

## RED — failing first

The new tests are in `internal/skeleton/interaction_r2_test.go`. **Every §5 number is spelled as
the spec's own literal, not as a production constant**, so they pin §5's table rather than the
implementation, and so the RED is **behavioural** — a field arrives uncapped, or the item never
arrives at all — rather than a compile error against a symbol that does not exist yet.

`go test ./internal/skeleton/ -run 'TestInteractionR2_' -count=1 -v`, run with
`internal/skeleton/interaction.go` and `internal/daemon/interaction.go` at their pre-change state:

```
=== RUN   TestInteractionR2_AgentMessageTextIsClippedAtMaxTextBytes
2026/08/07 17:29:38 interaction: PostToolUse was refused by the append floor: interaction: item is 12425 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:147: the producer journalled NO interaction record for session "s-r2-text". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_AgentMessageTextIsClippedAtMaxTextBytes (8.56s)
=== RUN   TestInteractionR2_ToolRunOutputExcerptIsClippedAtMaxTextBytes
2026/08/07 17:29:43 interaction: PostToolUse was refused by the append floor: interaction: item is 12485 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:165: the producer journalled NO interaction record for session "s-r2-out". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_ToolRunOutputExcerptIsClippedAtMaxTextBytes (5.03s)
=== RUN   TestInteractionR2_FileChangeDiffExcerptIsClippedAtMaxTextBytes
2026/08/07 17:29:48 interaction: PostToolUse was refused by the append floor: interaction: item is 28874 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:180: the producer journalled NO interaction record for session "s-r2-diff". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_FileChangeDiffExcerptIsClippedAtMaxTextBytes (5.04s)
=== RUN   TestInteractionR2_SummaryActionAndDecisionLabelsAreClippedAtMaxSummaryBytes
2026/08/07 17:29:53 interaction: PostToolUse was refused by the append floor: interaction: item is 27580 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:204: the producer journalled NO interaction record for session "s-r2-sum". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_SummaryActionAndDecisionLabelsAreClippedAtMaxSummaryBytes (5.07s)
=== RUN   TestInteractionR2_PromptLinesAreCappedInLineCount
    interaction_r2_test.go:269: journalled prompt_lines holds 160 lines for a 160-line prompt; §5 caps MaxPromptLines at 40 lines, exactly
    interaction_r2_test.go:277: a clipped item carries truncated = <nil>; IS-CAP-1 says truncation SHALL set it, and without it a consumer renders a cut body as the whole message (IS-DELTA-4)
    interaction_r2_test.go:277: full_bytes = <nil> with a 2840-byte payload; §2 carries the byte length of the UNTRUNCATED payload, so it must exceed what shipped
--- FAIL: TestInteractionR2_PromptLinesAreCappedInLineCount (0.03s)
=== RUN   TestInteractionR2_PromptLinesAreCappedInRunesNotBytes
2026/08/07 17:29:58 interaction: PostToolUse was refused by the append floor: interaction: item is 8310 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:292: the producer journalled NO interaction record for session "s-r2-prompt". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_PromptLinesAreCappedInRunesNotBytes (5.02s)
=== RUN   TestInteractionR2_PlanStepsAreCappedInCountAndBytes
2026/08/07 17:30:03 interaction: PostToolUse was refused by the append floor: interaction: item is 159485 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:322: the producer journalled NO interaction record for session "s-r2-plan". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_PlanStepsAreCappedInCountAndBytes (5.04s)
=== RUN   TestInteractionR2_AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped
2026/08/07 17:30:09 interaction: PostToolUse was refused by the append floor: interaction: item is 11697 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_r2_test.go:377: the producer journalled NO interaction record for session "s-r2-max". An item that exceeds an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and `full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never dropped at the append boundary
--- FAIL: TestInteractionR2_AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped (5.19s)
=== RUN   TestInteractionR2_ByteCapTruncationLandsOnARuneBoundary
    interaction_r2_test.go:442: journalled text is 4225 bytes; want 4095 -- §5 caps `text` at MaxTextBytes = 4096 and IS-CAP-1 backs the cut up off the two-byte rune straddling it
--- FAIL: TestInteractionR2_ByteCapTruncationLandsOnARuneBoundary (0.03s)
=== RUN   TestInteractionR2_AnItemUnderEveryCapIsShippedVerbatim
--- PASS: TestInteractionR2_AnItemUnderEveryCapIsShippedVerbatim (0.02s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	40.349s
FAIL
```

Two things worth reading off that output. The `approval_request` at §5's own maxima was
**11 697 bytes and never reached the journal** — the finding, reproduced exactly. And the control
(`AnItemUnderEveryCapIsShippedVerbatim`) passes in RED, which is what makes the other nine
failures about caps rather than about the harness.

The rune-boundary case is the one that failed on an assertion rather than on a drop: 4 225 bytes
shipped where 4 095 was required. `4225 = 4096 + 129` — the untouched body — and the *129* is
what the truncator has to cut without splitting the two-byte rune sitting across the boundary.

## GREEN

```
=== RUN   TestInteractionR2_AgentMessageTextIsClippedAtMaxTextBytes
--- PASS: TestInteractionR2_AgentMessageTextIsClippedAtMaxTextBytes (3.26s)
=== RUN   TestInteractionR2_ToolRunOutputExcerptIsClippedAtMaxTextBytes
--- PASS: TestInteractionR2_ToolRunOutputExcerptIsClippedAtMaxTextBytes (0.03s)
=== RUN   TestInteractionR2_FileChangeDiffExcerptIsClippedAtMaxTextBytes
--- PASS: TestInteractionR2_FileChangeDiffExcerptIsClippedAtMaxTextBytes (0.04s)
=== RUN   TestInteractionR2_SummaryActionAndDecisionLabelsAreClippedAtMaxSummaryBytes
--- PASS: TestInteractionR2_SummaryActionAndDecisionLabelsAreClippedAtMaxSummaryBytes (0.03s)
=== RUN   TestInteractionR2_PromptLinesAreCappedInLineCount
--- PASS: TestInteractionR2_PromptLinesAreCappedInLineCount (0.03s)
=== RUN   TestInteractionR2_PromptLinesAreCappedInRunesNotBytes
--- PASS: TestInteractionR2_PromptLinesAreCappedInRunesNotBytes (0.02s)
=== RUN   TestInteractionR2_PlanStepsAreCappedInCountAndBytes
--- PASS: TestInteractionR2_PlanStepsAreCappedInCountAndBytes (0.04s)
=== RUN   TestInteractionR2_AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped
--- PASS: TestInteractionR2_AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped (0.04s)
=== RUN   TestInteractionR2_ByteCapTruncationLandsOnARuneBoundary
--- PASS: TestInteractionR2_ByteCapTruncationLandsOnARuneBoundary (0.03s)
=== RUN   TestInteractionR2_AnItemUnderEveryCapIsShippedVerbatim
--- PASS: TestInteractionR2_AnItemUnderEveryCapIsShippedVerbatim (0.02s)
PASS
ok  	github.com/Nathandela/swarm/internal/skeleton	4.701s
```

## A weak assertion of my own, found by a mutation and strengthened — declared

The first draft of these tests asserted **upper bounds only** (`len(text) > 4096` fails). Mutation
5 below — capping `prompt_lines` in *bytes* where §5 counts *runes* — passed that suite cleanly:
a byte cap yields 100 runes on a two-byte-per-rune line, and 100 is comfortably "no more than
200". The cap would have silently halved every non-ASCII prompt the owner is asked to approve.

The assertions were **strengthened to exact counts** wherever nothing downstream can legitimately
cut further, and the RED above is the strengthened suite re-run against the unchanged production
code. Two places keep an upper bound, for a stated reason rather than for convenience:
`plan_update`'s step text and the maxima `approval_request`'s strings, because in both the fit
stage legitimately cuts below §5's per-field number (decision 1) — there they assert instead that
the field is **non-empty**, that the counts are exact, and that the closed vocabularies survive.

`TestInteractionR2_PromptLinesAreCappedInCountAndInRunes` was split into two cases in the course
of this, because the single case could not hold both properties exactly: at 160 lines × 400 runes
the fit stage runs and the per-line rune count is no longer 200. The count case now uses short
lines and the rune case ten long ones, so each isolates its cap. No pre-existing test was touched.

## Teeth — six mutations, each reverted

Run against the strengthened suite. The point of each is that it breaks exactly one clause.

| # | Mutation | Result |
|---|---|---|
| 1 | `clampBytes` cuts at `s[:max]` with no rune backup | **FAILS** `ByteCapTruncationLandsOnARuneBoundary` only — and instructively: the text arrives **4 098** bytes, not 4 096, because `encoding/json` replaced the split rune with U+FFFD. The phone would have rendered a replacement character the machine never saw. |
| 2 | the uniform-ceiling fit stage removed; §5's per-field caps alone | **FAILS** `PlanStepsAreCappedInCountAndBytes` and `AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped`, both by the item being dropped again. This is decision 1 proved rather than asserted: §5's per-field caps really are not jointly sufficient. |
| 3 | `itemEnumFields` emptied; the ceiling clips enums too | **PASSES — no test fails.** Disclosed, with the arithmetic, in decision 6. The guard is deliberate headroom against §5's numbers being ratified upward, not a tested behaviour. |
| 4 | `full_bytes` measured on the item **as shipped** instead of untruncated | **FAILS all nine** clipping cases (`full_bytes = 4250 with a 4268-byte payload`). |
| 5 | `prompt_lines` capped in bytes, not runes | **FAILS** `PromptLinesAreCappedInRunesNotBytes`: *"holds 100 runes (200 bytes); §5 caps a line at 200 RUNES"*. This is the mutation that caught the weak assertion above; it fires only against the strengthened test. |
| 6 | `capFields` still applied but its return dropped, so `truncated`/`full_bytes` are set only on a `MaxItemBytes` overrun | **FAILS** `PromptLinesAreCappedInLineCount` and `ByteCapTruncationLandsOnARuneBoundary` — the two cases where a §5 per-field cap binds but the item was never over the item cap. IS-CAP-1 ties the pair to *any* truncation, not to the last-resort one. |

## Gates

```
go build ./...   clean
go vet ./...     clean
gofmt -l         clean on internal/skeleton/interaction.go, internal/skeleton/interaction_r2_test.go,
                 internal/daemon/interaction.go

ok  github.com/Nathandela/swarm/internal/skeleton    191.674s   (-race, whole package)
ok  github.com/Nathandela/swarm/internal/daemon       64.850s   (-race)
ok  github.com/Nathandela/swarm/internal/remotegw     30.144s   (-race)
ok  github.com/Nathandela/swarm/internal/protocol     20.504s
ok  github.com/Nathandela/swarm/internal/protocol/schema  1.104s
ok  github.com/Nathandela/swarm/internal/journal      40.877s
ok  github.com/Nathandela/swarm/internal/adapter (and all eight sub-packages)
ok  github.com/Nathandela/swarm/internal/phonecore    17.967s
ok  github.com/Nathandela/swarm/internal/verify       14.867s
```

The three `protocol.md` drift fences are untouched and green —
`TestProtocolMDBidi_FieldSetMatchesStructs`, `TestProtocolMD_ExistsAndDocumentsEveryField`,
`TestProtocolMD_DocumentsEveryOp`. No wire field moved: the caps change what a producer *puts*
in `item`, never the carriage. `golangci-lint run` reports nothing in either touched file (the
package's 27 pre-existing errcheck/staticcheck findings sit in fourteen other files). B94 is
unmoved at 540/54 — no exported symbol was added or removed. Not committed.


---

# Re-review of R2 — its single-item claim holds; its headline claim does NOT hold on the MERGE path

## What was re-verified and holds

An `approval_request` on §5's documented maxima was driven through the shipped chain
(`internal/skeleton/interaction_rr_test.go`), with the one input that exercises R2's two hardest
rules together: 40 prompt lines of 200 **four-byte** runes — 32 000 bytes of prompt against an
8 KiB item cap, so the uniform ceiling halves several times and every cut lands INSIDE a multi-byte
rune.

Measured: **7 430 bytes shipped**, ceiling settled at 128 bytes, 32 runes surviving per line, all 8
decisions with their ids and labels, the summary, the mode and the action intact, `truncated` and
`full_bytes` both set — and **no U+FFFD anywhere in the payload**, which is the observable proof of
IS-CAP-1's rune boundary (encoding/json substitutes the replacement character for a split rune, so
a byte-wise clamp shows up as a character the machine never saw). The §3.5 `content_hash` still
re-derives exactly from the shipped bytes with its own slot zeroed, so R2's TRUNCATE-THEN-HASH
ordering and R4's digest agree under truncation.

`§5's per-field caps are not jointly sufficient` and the uniform-ceiling resolution are confirmed
as the right shape for a single item. The four disclosed open points stand as written.

## CONFIRMED DEFECT — the merged item is DROPPED, silently, and the text is lost

> **CLOSED 2026-08-07 by owner ruling** — the fourth resolution below ("raise or drop
> `MaxItemBytes` for merged items") was taken. `MaxItemBytes` is now 16 KiB, derived by arithmetic
> from §5's own worst cases, and the 8 405-byte two-increment union lands in the journal
> un-dropped. The decision and its alternatives are ADR-009's **Amendment 2026-08-07**; the
> measurement, the RED and the teeth are in `a1-gateway-floor.md`'s section of the same name. The
> spec defect this section identified — "§5's own maxima exceed §5's own item cap" — is closed by
> the same ruling and made a rule by the new **IS-CAP-5**.
>
> **What is NOT closed**, and is carried forward as an open point: the fold is *unbounded*, not
> two-wide. Four increments at `MaxTextBytes` inside one window still serialize to 16 622 B and
> are still refused and dropped. No item cap fixes that — it needs a rule about the merge itself,
> which the ruling did not make. The hazard note at the foot of this section stands unchanged for
> whoever writes it.

The section below is left as written, as the record of what was found.


R2's headline claim is "AN ITEM IS NEVER DROPPED FOR SIZE" (`fitItem`'s own doc comment). That is
true of an item the producer shapes. It is **false of the item the producer's own append floor
creates**, which is the only item that reaches the journal when the floor binds.

**The reproduction**, through the shipped path, with §5's OWN numbers and nothing exotic:

```
$ // two agent_message increments for one item_id, each exactly MaxTextBytes (4 KiB),
$ // offered inside ONE DefaultAppendWindow so ItemAdmission merges them
2026/08/07 18:59:28 interaction: append floor release failed (0 item(s) still held):
    interaction: item is 8368 bytes, over the 8192-byte cap (interaction-schema.md §5)

  the transcript holds 0 bytes of agent_message text; the producer offered 8192.
```

**The mechanism.** §5 sets `MaxTextBytes` at 4 KiB and `MaxItemBytes` at 8 KiB, i.e. exactly
`MaxItemBytes / 2`. IS-DELTA-2 requires the producer to merge pending increments for one `item_id`
into at most one append per window and says "the merge is **lossless text concatenation**".
`ItemAdmission.concatText` performs it faithfully. The result — two capped increments plus a ~176
byte envelope — is 8 368 bytes, which `daemon.RecordInteractionRaw` refuses. By then the item has
already been dequeued by `release`, so the bytes are gone: the caller sees an error it can only
log, and **8 KiB of the agent's message never reaches the transcript, with nothing on any surface
marked damaged**. Any two increments totalling more than ~8 016 bytes of text in one 125 ms window
reproduce it.

**Scope, precisely.** `agent_message` only. The record collapse of `tool_run` and `file_change` is
a field-wise union rather than a concatenation, so its size is roughly the max of the two records
and not their sum (a `tool_run` open carries `tool`/`action`, its close carries the 4 KiB
`output_excerpt`: union ≈ 4.3 KiB). It is the concatenation, and only the concatenation, that adds.

**Why it is recorded rather than fixed here.** Every available fix trades one normative rule
against another, which is a ruling and not a bug fix:

- bound the merged text in `concatText` — cheapest, but directly contradicts IS-DELTA-2's
  "lossless";
- refuse the fold when it would overrun and let the increment take its own slot — lossless, and
  keeps IS-DELTA-2a's per-target ceiling since the record still queues, but it changes the floor's
  contract (one item may now cost two appends) and needs the cap plumbed into `internal/remotegw`,
  which deliberately does not link the daemon;
- truncate the merged item with `truncated`/`full_bytes` under IS-CAP-1 — R2's own answer for a
  single item, but it makes the FLOOR a truncator, which is the opposite of "lossless";
- raise or drop `MaxItemBytes` for merged items — defensible (the number is proposed and
  unratified, and 8 KiB is a self-imposed floor three orders of magnitude under the relay's
  ~768 KiB envelope), but it is a fence with tests and an owner ruling.

**This is the same spec defect R2 already disclosed, in the form R2 did not consider.** R2's own
open point says "§5's own maxima exceed §5's own item cap is a defect in the table … the real fix
is an ADR-009 amendment". `MaxTextBytes = MaxItemBytes / 2` is that defect again: no per-record cap
can fix it, because the overrun is created by the merge, not by any one record. The amendment that
ratifies §5's numbers must state the relationship between `MaxTextBytes`, the merge window and
`MaxItemBytes`, or name which of the four behaviours above is the intended one.

**Reachability today.** Latent, on the same terms as every other finding in this program: no
adapter implements `InteractionSource`, so nothing produces an `agent_message` increment at all. It
becomes live with the first streaming adapter, and it is silent when it does — which is why it is
recorded at the top of the owed list rather than in the residuals.

**A hazard for whoever implements the second option.** "Refuse the fold and let the increment take
its own slot" is the most attractive of the four (lossless, and still one append per window per
item so IS-DELTA-2's letter holds), but it has a trap: `ItemAdmission.pending` is keyed by
`session\x00item_id`, so a THIRD increment would fold into the FIRST held entry rather than the
second, and the two would release as `1+3` then `2`. IS-DELTA-1 reconstructs by concatenation in
CURSOR order, so that ships the message scrambled — a worse failure than the drop it replaces. The
split has to re-home the full entry under a frozen key (updating `a.order` in place) and leave the
live key pointing at the newest record. That, not the size check, is the part to test first.
