# A1 / W4 — interaction items: phone-core ingest and the bound transcript surface

**Workpackage**: W4 of the interaction program (ADR-009, ADR-010,
`docs/specifications/interaction-schema.md`, all Accepted 2026-08-07).
**Scope**: the phone side of an interaction item — `commitReceive` accepting interaction
records off the existing bare-journal path (IS-LAYER-1), the fold that turns records into a
transcript (IS-ENV-2, IS-DELTA-1, IS-ST-1, IS-PLAN-1, IS-COMPAT-1/-2/-4), durable custody of
that transcript with the state-schema bump the repo's migration rule requires, the IS-LIFE-3
retention exemption for an unresolved `approval_request`, and the gomobile surface that lets a
Kotlin transcript screen list items and receive live updates.
**Out of scope, by the workpackage**: every producer (Claude Code, Codex), the daemon-side
admission queue (ADR-010 §7), the push-wake rules (ADR-010 §4), the `ApproveReq` wire body and
approval ANSWERING (IS-LIFE-4), and all Android/Kotlin.
**Files touched**: `internal/phonecore/` and `mobile/` only.

---

## Decisions recorded (points the schema and the ADRs leave open)

1. **The demux is a branch on the RECORD's type inside the existing kind-less mailbox case.**
   IS-LAYER-1 forbids a new mailbox `kind` and a new demux branch; it does not forbid reading
   the record's own `type` inside the branch that already handles every bare journal record.
   `MailboxRouter.apply` and `Core.foldContent` both route `rec.Type == "interaction"` to the
   transcript and leave every other record on the roster path untouched.

2. **An interaction record shapes the TRANSCRIPT and NOT the roster** (IS-SS-1). The kind-less
   branch used to call `sessionsWith` unconditionally, which would have marked a session
   `Present` on the triage screen off the back of a tool call, and grown the durable session
   list with an entry the machine never announced. `session_status` is the transcript's marker;
   `group_transition` stays the roster's.

3. **The journal read cursor still advances on an item** (`SessionCache.AdvanceCursor`). The
   cursor is what `Resync` resumes from. After ADR-009 items are the bulk of the stream, so
   leaving the cursor on roster events alone would have the phone ask for a range thousands of
   records behind — and IS-CAP-4 then cuts that oversized reseed at a floor, which is content
   the phone asked for and lost. Cited at the call site (IS-LAYER-3, IS-LAYER-4).

4. **A reseed MERGES the transcript; it REPLACES only the roster.** PB-SYNC-8's replace rule is
   about a SET whose absent members have ended. A transcript is a cursor-ordered log whose
   events half IS-CAP-4 may CUT at a floor to keep the repair inside one frame, so replacing
   would delete history the phone legitimately holds on every repair. The roster half is not
   read into the transcript at all: a roster record's cursor is deliberately zero (PB-SYNC-8)
   and cannot be ordered against an `approval_resolved` — which is IS-LIFE-3's own reason for
   putting the re-delivery in the events half.

5. **`Item.Cursor` is the FIRST record's cursor.** IS-LAYER-3 orders by journal cursor and
   IS-ENV-2 folds by `item_id`; the spec does not say which record's cursor an item then
   occupies. Taking the latest would move a streaming message to the end of the transcript on
   every increment. Recorded on the field.

6. **`Text` is the reconstruction, `Body` is the latest record verbatim.** IS-DELTA-1 makes a
   streamed `agent_message` a sequence of increments, so the latest record's `text` is the tail
   of the message and not the message. The core concatenates into `Text`; `Body` carries the
   raw item object so the client decodes the per-kind fields of §3 itself. That split is what
   makes an unknown kind or field free on the boundary (IS-COMPAT-1/-2) and is forced at the
   gomobile edge anyway — no bound map, no variant type, the `Snapshot.Text` precedent.

7. **The transcript is bounded at `MaxItemsPerSession = 200`, per session, with IS-LIFE-3's
   exemption.** The number is §5/IS-CAP-3's own proposed retention figure and is flagged
   PROPOSED AND UNRATIFIED in the code, exactly as §5's preamble says of every number in that
   section. It is reused rather than invented so the phone's retention and the daemon's
   detail-fetch retention cannot disagree. Bounded at all because the alternative is
   `MailboxRouter.rebind`'s recorded `OpOutcomes` residual ("never pruned") reproduced on a
   plane that grows per tool call. Per session so a busy session cannot evict a quiet one.

8. **The exemption LIFTS on resolution.** `Item.Resolved` is set when the matching
   `approval_resolved` folds (IS-LIFE-2) and is DURABLE with the item: a flag rebuilt in memory
   comes back clear after a process death and re-exempts a card answered hours ago. An
   exemption that never lifted is an unbounded transcript with extra steps.

9. **`StateSchemaVersion` 9 → 10, with the v10 byte-literal fixture pinned.** `State.Items`
   lives INSIDE `content_purgeable` (content tier, purgeable — a decrypted machine-sealed
   cache, the same class as `Sessions`/`Snapshots` and the most revealing of the three), so the
   top-level tag set is unchanged and `TestStateSchemaVersion_IsPinnedToTheDurableFieldSet`
   alone would not have noticed the field. `TestStateStore_PinnedSealedFixturesStillLoad` is
   what caught it, and the new literal is what answers it. IS-COMPAT-3 is untouched: this
   version stamps a file only this build writes and reads, not `journal.SchemaVersion` and not
   the item's own `v`.

10. **Approval ANSWERING is not exposed, deliberately.** IS-LIFE-4's decision travels as the
    signed `ActionApprove` with an `ApproveReq` wire body no slice has built, and IS-APR-2
    requires the phone to echo `content_hash`/`expires_at` verbatim rather than compute them —
    so a verb shipped now could only send something the daemon refuses, or be tempted into the
    blind keystroke ADR-007 D7 and IS-LIFE-6 both forbid the phone from authoring. The pending
    card is exposed READ ONLY, and `TestInteraction_TheFacadeCannotAnswerAnApproval` pins the
    absence.

11. **IS-DELTA-4 ("incomplete from join") is NOT built.** The build brief lists it as
    consumer-side and explicitly not this program's build target, and its literal reading —
    "first record for an item_id carries in_progress" — is true of EVERY streamed item, so it
    cannot be implemented as written without marking all of them. It needs a rule that
    distinguishes a mid-turn join from an ordinary first increment; that is a schema question,
    not an implementation one. Recorded as an open point rather than guessed at.

---

## RED → GREEN

### Cycle 1 — `internal/phonecore` (the fold, durability, retention)

Test file written first: `internal/phonecore/interaction_test.go`. Verbatim RED, at
`907d6dc` with zero production code for this workpackage:

```
$ go test ./internal/phonecore/
# github.com/Nathandela/swarm/internal/phonecore [github.com/Nathandela/swarm/internal/phonecore.test]
internal/phonecore/interaction_test.go:43:15: undefined: RecordTypeInteraction
internal/phonecore/interaction_test.go:97:13: r.Items undefined (type *MailboxRouter has no field or method Items)
internal/phonecore/interaction_test.go:116:29: st.Items undefined (type State has no field or method Items)
internal/phonecore/interaction_test.go:119:79: st.Items undefined (type State has no field or method Items)
internal/phonecore/interaction_test.go:158:13: r.Items undefined (type *MailboxRouter has no field or method Items)
internal/phonecore/interaction_test.go:185:13: r.Items undefined (type *MailboxRouter has no field or method Items)
internal/phonecore/interaction_test.go:234:12: r.Items undefined (type *MailboxRouter has no field or method Items)
internal/phonecore/interaction_test.go:253:13: r.Items undefined (type *MailboxRouter has no field or method Items)
internal/phonecore/interaction_test.go:284:14: undefined: Item
internal/phonecore/interaction_test.go:285:23: r.Items undefined (type *MailboxRouter has no field or method Items)
internal/phonecore/interaction_test.go:285:23: too many errors
FAIL	github.com/Nathandela/swarm/internal/phonecore [build failed]
FAIL
```

### Cycle 1b — the THREE shipped fences that fired on the durable field

After `State.Items` landed, before any fixture was touched. This is the repo's migration rule
working exactly as designed, and it is recorded because the fences — not this workpackage —
are what forced the version bump and the new pinned literal:

```
$ go test ./internal/phonecore/
--- FAIL: TestS15_TheAtRestInventoryIsCompleteAndMeasuredFromTheBytes (0.05s)
    s15_statetier_test.go:457: PB-STATE-9: State.Items has no row in the at-rest inventory. Every field must be assigned a tier or recorded as deliberately unassigned; a field nobody classified is how decrypted content reached disk in the clear in the first place
--- FAIL: TestState_EveryResumeCriticalFieldSurvivesARestart (0.01s)
    state_test.go:134: fullState() leaves Items at its zero value; PB-STATE-1 enumerates every resume-critical field, so the fixture must set it
--- FAIL: TestStateSchemaVersion_IsPinnedToTheDurableFieldSet (0.00s)
    state_test.go:567: StateSchemaVersion is 10 and stateFixtures pins no literal for it (it pins [1 4 5 6 7 8 9]). PB-STATE-5's forward-migration path is only mechanical if every shipped version keeps a byte-literal that must go on loading, so raising the version means pinning the blob the new version writes
FAIL
FAIL	github.com/Nathandela/swarm/internal/phonecore	9.918s
```

Each was answered by giving the fence what it demanded, never by weakening it:

- the at-rest inventory gained a row — `{field: "Items", tier: "content", ...}` — and a
  sentinel (`s15ItemText`) that the test MEASURES in the state directory, so the claim "the
  transcript is sealed under the content KEK and readable nowhere in the clear" is a
  measurement of the bytes on disk rather than an assertion;
- `fullState()` gained a non-zero `Items`, so the round-trip test covers it;
- `StateSchemaVersion` went to 10 and `stateV10Fixture` was pinned — generated by writing
  `fullState()` through the fixture's pinned KEK, then deleted the generator.

GREEN, whole package, race-enabled for the new tests:

```
$ go test ./internal/phonecore/
ok  	github.com/Nathandela/swarm/internal/phonecore	8.633s

$ go test -race -count=1 -run 'TestInteraction' ./internal/phonecore/ -v
--- PASS: TestInteraction_FoldsIntoTheTranscriptAndNotTheRoster (0.05s)
--- PASS: TestInteraction_TheJournalReadCursorStillAdvances (0.01s)
--- PASS: TestInteraction_AgentMessageRecordsConcatenateByItemID (0.01s)
--- PASS: TestInteraction_NoRecordLandsAfterATerminalStatus (0.00s)
--- PASS: TestInteraction_UnusableItemsAreSkippedWithoutStalingTheStream (0.01s)
--- PASS: TestInteraction_ANewerItemSchemaIsDegradedNotDropped (0.01s)
--- PASS: TestInteraction_PlanUpdateKeepsOnlyTheHighestRevision (0.01s)
--- PASS: TestInteraction_UnresolvedApprovalSurvivesRetentionTrim (0.21s)
--- PASS: TestInteraction_ReseedMergesTheTranscriptAndRedeliversUnresolvedApprovals (0.01s)
--- PASS: TestInteraction_TranscriptSurvivesARestart (0.01s)
--- PASS: TestInteraction_ThePurgeDestroysTheTranscript (0.00s)
ok  	github.com/Nathandela/swarm/internal/phonecore	2.536s
```

### Cycle 2 — `mobile` (the bound surface)

Test file written first: `mobile/interaction_test.go`. Verbatim RED:

```
$ go test ./mobile/
# github.com/Nathandela/swarm/mobile [github.com/Nathandela/swarm/mobile.test]
mobile/interaction_test.go:75:17: a.ReadTranscript undefined (type *App has no field or method ReadTranscript)
mobile/interaction_test.go:124:17: a.PendingApprovals undefined (type *App has no field or method PendingApprovals)
mobile/interaction_test.go:139:16: a.PendingApprovals undefined (type *App has no field or method PendingApprovals)
FAIL	github.com/Nathandela/swarm/mobile [build failed]
FAIL
```

### Cycle 2b — the TWO shipped surface fences

After the verbs landed, before the table or the golden was touched:

```
$ go test ./mobile/
--- FAIL: TestPBBIND3_NoUntracedEntryPoint (0.58s)
    coverage_test.go:200: PB-BIND-3: 6 exported entry point(s) appear in no screen_coverage.tsv row:
        	App.PendingApprovals
        	App.ReadTranscript
        	TranscriptPage.At
        	TranscriptPage.Count
        	TranscriptPage.NextCursor
        	TranscriptPage.Stale
--- FAIL: TestPBBIND7_ExportedSurfaceMatchesTheGolden (0.68s)
    golden_test.go:54: PB-BIND-7: the exported surface drifted from the pinned contract.
        REMOVED (breaks the Android app):
        	(none)
        ADDED (new API, must be traced in screen_coverage.tsv):
        	[13 TranscriptItem fields, 2 App methods, 4 TranscriptPage methods, 2 types]
        If the change is intended and reviewed, re-run with -update-surface and justify the diff in the slice evidence.
```

Answered by the fences' own documented growth protocol, the one S16 and S17 already used:
two rows in `mobile/screen_coverage.tsv`, two elements appended to `requiredScreenElements`
(the list is hard-coded so deleting a row cannot make a requirement vanish, which means it
must GROW when the product does — its own comment says so), a new
`mobile/interaction_screencoverage_test.go` hard-coding the same two elements so the two lists
keep meeting in the middle, and `-update-surface` for the golden. **The golden diff is
additive: `REMOVED (none)`.** Nothing the Android app compiles against changed.

GREEN:

```
$ go test ./mobile/ -run 'TestPBBIND3|TestInteraction'
ok  	github.com/Nathandela/swarm/mobile	7.407s
```

### Cycle 2c — a THIRD fence, one package outside this workpackage's two

The repo-wide run then found the bound-verb ledger, which is the control that exists because a
gomobile verb compiles and tests green with no caller at all:

```
$ go test ./...
--- FAIL: TestBoundVerbs_EveryBoundVerbIsCalledFromProductionKotlinOrLedgered (0.08s)
    boundverbledger_test.go:499: swarmmobile.App.PendingApprovals has NO production-Kotlin caller and no row in android/unbound-verbs.tsv.
    boundverbledger_test.go:499: swarmmobile.App.ReadTranscript has NO production-Kotlin caller and no row in android/unbound-verbs.tsv.
FAIL	github.com/Nathandela/swarm/android/gate	9.713s
```

**Scope exception, recorded rather than worked around.** This workpackage's brief says
`internal/phonecore` and `mobile/` only, and all Android/Kotlin work is out of scope. The fence
offers exactly two answers — call the verb from production Kotlin, or ledger it with a reason —
and the first IS the out-of-scope one. So two rows were added to `android/unbound-verbs.tsv`,
which is a checked-in data file recording a fact about the surface this workpackage added, not
Kotlin work: `App.ReadTranscript` and `App.PendingApprovals`, each traced to its
`mobile/screen_coverage.tsv` element and each stating what screen does not exist yet. The
alternative was leaving `./...` red, which is worse and would have hidden the next real failure.
`go test ./android/gate/` is green after the rows.

### Gates

```
$ go build ./...   # clean
$ go vet ./...     # clean
$ go test -race -count=1 -run 'TestTranscript|TestInteraction' ./mobile/ -v
--- PASS: TestTranscript_ItemsCrossTheBoundAsAPage (0.02s)
--- PASS: TestTranscript_PendingApprovalsAreExposedReadOnly (0.00s)
--- PASS: TestTranscript_AnItemRaisesItsOwnEventAndLeavesTheRosterAlone (0.00s)
--- PASS: TestInteraction_TheTranscriptVerbsAreTracedToFacadeMethods (0.30s)
--- PASS: TestInteraction_TheFacadeCannotAnswerAnApproval (0.27s)
ok  	github.com/Nathandela/swarm/mobile	2.780s

$ go test ./internal/phonecore/ ./mobile/ ./android/gate/ ./internal/skeleton/
ok  	github.com/Nathandela/swarm/internal/phonecore	11.193s
ok  	github.com/Nathandela/swarm/mobile	22.787s
ok  	github.com/Nathandela/swarm/android/gate	10.451s
ok  	github.com/Nathandela/swarm/internal/skeleton	183.949s

$ go test ./...
# 56 packages ok. ONE failure, and it names no symbol from this workpackage:
#   internal/verify TestB94_EveryExportedSymbolIsReachableFromProduction
#     internal/remotegw.{NewItemAdmission,ItemAdmission.Offer/Flush/Pending}   -- the ADR-010 §7 slice
#     internal/adapter.{AsInteractionSource,CheckInteractionFixture,Interaction.Validate} -- the W1 slice,
#       which recorded leaving this fence deliberately red in docs/verification/a1-adapter-contract.md
# Nothing from internal/phonecore or mobile is listed: the transcript store is constructed by
# MailboxRouter and its verbs are reachable from the bound facade.

$ golangci-lint run ./internal/phonecore/... ./mobile/...
# 5 issues, ALL PRE-EXISTING and none in code this workpackage wrote or moved:
#   internal/phonecore/deps_allowlist_test.go:91  errcheck f.Close
#   internal/phonecore/state.go:1514,1537         errcheck os.Remove / d.Close (persistState, untouched)
#   mobile/bind_test.go:149                       errcheck zr.Close
#   mobile/conformance/pbnet3_wire_test.go:245    govet inline reflect.Ptr
```

---

## What was built

### `internal/phonecore/interaction.go` (new)

- `RecordTypeInteraction`, `ItemSchemaVersion`, `MaxItemsPerSession`, the eight `Kind*` and
  four `Status*` constants of §3/§4.
- `Item` — one FOLDED item: envelope fields, the reconstructed `Text`, `Body` (the latest
  record's item object verbatim), `Degraded`, `Resolved`, `Revision`.
- `ItemStore` — fold by `item_id` (IS-ENV-2), `agent_message` text concatenation (IS-DELTA-1),
  terminal-once (IS-ST-1), highest-revision-per-session for `plan_update` (IS-PLAN-1),
  skip-without-staling for a malformed/unknown item (IS-ENV-3, IS-COMPAT-1/-2),
  degrade-not-drop for a newer `v` (IS-COMPAT-4), resolution marking (IS-LIFE-2), and
  retention with the unresolved-approval exemption (IS-LIFE-3).
- `itemsWith` — the scratch-copy fold, so a failed durable commit leaves the live store
  untouched (the `sessionsWith` rule).

### Wiring

- `snapshot.go`: `MailboxRouter.items` + `Items()`, restored by `rebind`, folded in `apply`
  (kind-less branch → transcript on `type == "interaction"`; reseed events half merged).
- `core.go`: `foldContent` computes the durable `State.Items` on the same two paths.
- `journal.go`: `SessionCache.AdvanceCursor` (decision 3).
- `state.go`: `State.Items`, `StateSchemaVersion` 10, `purgeableContainer.Items`,
  `purgeableContainerOf`, `loadContentState`, `dropContentMaterial` (the purge destroys the
  transcript), `refuseUnreadableContentWrite` (a locked tier refuses to lose it silently),
  `clone`.

### `mobile/`

- `TranscriptItem` (flat, gomobile-bindable — no maps, no slices, no cross-package types),
  `TranscriptPage` handle with `Count`/`At`/`NextCursor`/`Stale`.
- `App.ReadTranscript(session, from, limit)` and `App.PendingApprovals()` (read-only).
- `Event{Kind: "interaction"}` raised per item on the `journal` stream, carrying the item KIND
  on `Message` and its status on `State`; gated by the existing `SubscribeJournal` toggle, so
  no new lifecycle verb. An interaction record deliberately does NOT feed `a.journal` or
  `a.needs` (IS-SS-1).

---

## Open points and residuals

1. **IS-DELTA-4 is unimplemented and unimplementable as written** (decision 11). It needs a
   rule that separates a mid-turn join from an ordinary first `in_progress` record.
2. **`MaxItemsPerSession = 200` is unratified**, as is every §5 number; flagged in the code.
3. **Detail fetch (IS-CAP-2/-3) has no surface.** `TranscriptItem.Detail` and `Truncated` cross
   the boundary so a client can SAY an item is clipped; the unsigned read that would fetch the
   full body is deferred with the gateway action plumbing.
4. **Approval answering is absent by design** (decision 10) and pinned absent by a test.
5. **Page semantics are tail-oriented.** An item UPDATED in place keeps its first record's
   cursor, so `ReadTranscript(from=NextCursor)` will not re-serve it; the "interaction" event is
   what prompts a re-read of the tail. Adequate for a chat screen, stated so nobody reads
   `NextCursor` as a change feed.
6. **No end-to-end test through a real relay+gateway+daemon.** The producer does not exist yet
   (W1 built the adapter contract, W2 the carriage), so the e2e is owed to the slice that
   lands a producer — `internal/skeleton/s19_e2e_test.go` is its shape.

---

## Review closure — R1: the fold had no replay guard (`StateSchemaVersion` 10 → 11)

**Finding (HIGH, confirmed by execution against `79a070d`).** `ItemStore.applyLocked` folded a
record into an existing item on `item_id` alone, so `next.Text = prev.Text + w.Text` ran for
EVERY record sharing that id whatever its cursor. A record delivered twice was folded twice: the
increment concatenated again and the item's fields re-collapsed to that record's values. Nothing
looked damaged — the transcript still reads as prose.

A re-delivered record is not an anomaly, it is the repair channel working as designed. IS-LAYER-4
gives items the journal's own repair, and IS-CAP-4 makes a reseed's events half a window the
DAEMON sizes to fit one frame — cut at a floor the phone does not choose, so overlap with what the
phone already holds is the normal case.

### Decisions recorded

12. **The guard is a per-ITEM high water, not a per-stream one.** IS-LAYER-3 is the clause the
    fold was missing: items carry no private sequence number, and "for successive records of one
    streamed item, cursor order IS delta order" — so a record that does not ADVANCE past what the
    item already absorbed is not a delta. It cannot be `SessionCache`'s single stream cursor,
    which is the shape the idiom is borrowed from: a repair legitimately re-delivers records the
    phone MISSED at cursors BELOW its highest folded one — that is what a repair is for — and a
    stream-wide high water would reject exactly those. Strictly-greater, unlike `SessionCache`'s
    tolerance of an equal cursor: that tolerance exists for a roster snapshot, whose records
    deliberately share one read cursor, and two records of one item at one cursor would be the
    same record.

13. **The high water is DURABLE with the item** (`Item.LastCursor`), for the argument already
    written on `Item.Resolved`: a guard rebuilt in memory comes back zero. The restart is not a
    hypothetical route to a repair, it is the ordinary one — the journal read cursor is
    memory-only (`SessionCache.restore` seeds no cursor), so the first resync after a process
    death asks from cursor ZERO (`mobile/app.go`: `unsignedResync(Sessions().Cursor())`) and the
    reseed answering it re-delivers the tail of the journal, which is precisely the records that
    built the restored transcript. Every item still `in_progress` at the SIGKILL is doubled; a
    terminal one was already safe, because IS-ST-1's guard refuses it.

14. **`StateSchemaVersion` 10 → 11, with the v11 byte-literal fixture pinned.** `LastCursor` sits
    one level deeper than v10's `items` — inside the array inside `content_purgeable` — so
    neither the top-level tag check nor `sealedTags` can see it, and
    `TestStateStore_PinnedSealedFixturesStillLoad` is again what catches it. The bump is answered
    the way v10's was: `fullState()` exercises the new field, the fence fires, the version moves
    and a literal for it is pinned. Not bumping would leave two builds both stamping `10` while
    writing different field sets, and the older one silently drops the guard on the next load.

### RED → GREEN

#### Cycle 3 — the guard (`internal/phonecore/r1_replayfold_test.go`)

Test file written first. Verbatim RED, with the fold unchanged from `79a070d` — the symbols all
exist, so this is a behavioural RED, not a compile-fail one:

```
$ go test -count=1 -run 'TestR1_' ./internal/phonecore/
--- FAIL: TestR1_ARepairedRecordIsNotFoldedTwice (0.02s)
    r1_replayfold_test.go:95: reconstructed text after the repair = "Let me read the Let me read the file."; want "Let me read the file.". The reseed re-delivered records this phone had already folded, and a record whose cursor does not ADVANCE past what the item absorbed is not a delta (IS-LAYER-3: cursor order IS delta order)
    r1_replayfold_test.go:105: durable transcript after the repair = [{SessionID:m1/s-alpha ItemID:itm-1 Cursor:10 Kind:agent_message Status:completed TurnID:t-1 TSUnixMs:1786096800000 Text:Let me read the Let me read the file. Truncated:false Detail:false Degraded:false Resolved:false Revision:0 Body:[123 34 118 34 58 49 44 34 105 116 101 109 95 105 100 34 58 34 105 116 109 45 49 34 44 34 116 115 34 58 34 50 48 50 54 45 48 56 45 48 55 84 49 48 58 48 48 58 48 48 90 34 44 34 116 117 114 110 95 105 100 34 58 34 116 45 49 34 44 34 107 105 110 100 34 58 34 97 103 101 110 116 95 109 101 115 115 97 103 101 34 44 34 115 116 97 116 117 115 34 58 34 99 111 109 112 108 101 116 101 100 34 44 34 116 101 120 116 34 58 34 102 105 108 101 46 34 125]}]; want the same single item the live store holds -- the repair commits with its watermark (PB-SYNC-3), so a doubled fold is doubled on disk too and no later repair can undo it
--- FAIL: TestR1_AReorderedRecordBehindTheItemsHighWaterIsRejected (0.00s)
    r1_replayfold_test.go:126: reconstructed text = "read the Let me "; want "read the " -- a record behind the item's high water is a reorder, and appending it renders the increments out of order
    r1_replayfold_test.go:131: item TurnID = "t-1"; want t-2 -- the rejected record must not re-collapse the item's FIELDS to its own older values either
--- FAIL: TestR1_TheFoldHighWaterSurvivesARestart (0.00s)
    r1_replayfold_test.go:169: reconstructed text after a restart and the repair that follows it = "Let me read the Let me read the file."; want "Let me read the file.". The per-item high water must be durable with the item -- a flag rebuilt in memory comes back clear, which is the same argument Item.Resolved is durable for
FAIL
FAIL	github.com/Nathandela/swarm/internal/phonecore	1.042s
FAIL
```

The three failures are the three shapes of the same defect: a repair that re-delivers what the
phone holds, a pair that arrives out of cursor order, and the same repair one process death later.

#### Cycle 3b — the TWO shipped fences that fired on the durable field

`Item.LastCursor` and the guard landed, then `fullState()` was given the new coordinate — the
fixture must exercise every field or the comparison stops covering what was added after it was
written. The migration rule then fired, twice, in the order it is designed to:

```
$ go test -count=1 -run 'TestStateStore_PinnedSealedFixturesStillLoad|TestStateSchemaVersion_IsPinnedToTheDurableFieldSet|TestState_EveryResumeCriticalFieldSurvivesARestart' ./internal/phonecore/
--- FAIL: TestStateStore_PinnedSealedFixturesStillLoad (0.05s)
    --- FAIL: TestStateStore_PinnedSealedFixturesStillLoad/v10 (0.01s)
        state_test.go:566: the pinned v10 fixture restored State.Items = []phonecore.Item{phonecore.Item{SessionID:"m1/s1", ItemID:"itm-1", Cursor:0x9, LastCursor:0x0, Kind:"agent_message", Status:"completed", TurnID:"turn-1", TSUnixMs:1753900000000, Text:"on it", Truncated:false, Detail:false, Degraded:false, Resolved:false, Revision:0, Body:json.RawMessage{0x7b, 0x22, 0x76, 0x22, 0x3a, 0x31, 0x2c, 0x22, 0x69, 0x74, 0x65, 0x6d, 0x5f, 0x69, 0x64, 0x22, 0x3a, 0x22, 0x69, 0x74, 0x6d, 0x2d, 0x31, 0x22, 0x2c, 0x22, 0x6b, 0x69, 0x6e, 0x64, 0x22, 0x3a, 0x22, 0x61, 0x67, 0x65, 0x6e, 0x74, 0x5f, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x22, 0x7d}}}; want []phonecore.Item{phonecore.Item{SessionID:"m1/s1", ItemID:"itm-1", Cursor:0x9, LastCursor:0xb, Kind:"agent_message", Status:"completed", TurnID:"turn-1", TSUnixMs:1753900000000, Text:"on it", Truncated:false, Detail:false, Degraded:false, Resolved:false, Revision:0, Body:json.RawMessage{0x7b, 0x22, 0x76, 0x22, 0x3a, 0x31, 0x2c, 0x22, 0x69, 0x74, 0x65, 0x6d, 0x5f, 0x69, 0x64, 0x22, 0x3a, 0x22, 0x69, 0x74, 0x6d, 0x2d, 0x31, 0x22, 0x2c, 0x22, 0x6b, 0x69, 0x6e, 0x64, 0x22, 0x3a, 0x22, 0x61, 0x67, 0x65, 0x6e, 0x74, 0x5f, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x22, 0x7d}}}. A coordinate the literal carries and this build no longer reads is a durable field dropped without a schema bump
FAIL
FAIL	github.com/Nathandela/swarm/internal/phonecore	1.335s
FAIL
```

`StateSchemaVersion` then went to 11, and the second fence demanded the literal for it:

```
$ go test -count=1 -run 'TestStateSchemaVersion_IsPinnedToTheDurableFieldSet|TestStateStore_PinnedSealedFixturesStillLoad' ./internal/phonecore/
--- FAIL: TestStateSchemaVersion_IsPinnedToTheDurableFieldSet (0.00s)
    state_test.go:587: StateSchemaVersion is 11 and stateFixtures pins no literal for it (it pins [1 4 5 6 7 8 9 10]). PB-STATE-5's forward-migration path is only mechanical if every shipped version keeps a byte-literal that must go on loading, so raising the version means pinning the blob the new version writes
FAIL
FAIL	github.com/Nathandela/swarm/internal/phonecore	1.093s
FAIL
```

Answered the way v10 was: `stateV11Fixture` generated by writing `fullState()` through the
fixture's pinned KEK, pinned as a byte literal, added to `stateFixtures`, and the generator
deleted. The v10 literal is untouched and still loads — `Items` is legitimately absent from it,
which is the version-aware half of that test doing its job.

#### GREEN

```
$ go test -count=1 ./internal/phonecore/
ok  	github.com/Nathandela/swarm/internal/phonecore	8.969s

$ go test -race -count=1 -run 'TestR1_|TestInteraction|TestState' ./internal/phonecore/ -v
--- PASS: TestInteraction_FoldsIntoTheTranscriptAndNotTheRoster (0.04s)
--- PASS: TestInteraction_TheJournalReadCursorStillAdvances (0.01s)
--- PASS: TestInteraction_AgentMessageRecordsConcatenateByItemID (0.00s)
--- PASS: TestInteraction_NoRecordLandsAfterATerminalStatus (0.00s)
--- PASS: TestInteraction_UnusableItemsAreSkippedWithoutStalingTheStream (0.00s)
--- PASS: TestInteraction_ANewerItemSchemaIsDegradedNotDropped (0.00s)
--- PASS: TestInteraction_PlanUpdateKeepsOnlyTheHighestRevision (0.01s)
--- PASS: TestInteraction_UnresolvedApprovalSurvivesRetentionTrim (0.22s)
--- PASS: TestInteraction_ReseedMergesTheTranscriptAndRedeliversUnresolvedApprovals (0.01s)
--- PASS: TestInteraction_TranscriptSurvivesARestart (0.01s)
--- PASS: TestInteraction_ThePurgeDestroysTheTranscript (0.00s)
--- PASS: TestR1_ARepairedRecordIsNotFoldedTwice (0.01s)
--- PASS: TestR1_AReorderedRecordBehindTheItemsHighWaterIsRejected (0.00s)
--- PASS: TestR1_TheFoldHighWaterSurvivesARestart (0.01s)
--- PASS: TestStateStore_PushPreferenceVersionSurvivesARestart (0.04s)
--- PASS: TestState_EveryResumeCriticalFieldSurvivesARestart (0.09s)
--- PASS: TestState_DeviceKeysSurviveARestart (0.04s)
--- PASS: TestState_GrantWatermarkRefusesAReplayedGrantAfterRestart (0.09s)
--- PASS: TestStateStore_PinnedSealedFixturesStillLoad (0.02s)
--- PASS: TestStateSchemaVersion_IsPinnedToTheDurableFieldSet (0.00s)
--- PASS: TestStateStore_PinnedV1FixtureStillLoads (0.00s)
--- PASS: TestStateStore_UnknownFutureSchemaFailsClosed (0.00s)
--- PASS: TestStateStore_CorruptFailsClosedButAForeignMachineIsMerelyEmpty (0.00s)
ok  	github.com/Nathandela/swarm/internal/phonecore	2.820s
```

#### Gates

```
$ go build ./...   # clean
$ go vet ./...     # clean
$ go test -count=1 ./mobile/ ./android/gate/
ok  	github.com/Nathandela/swarm/mobile	19.363s
ok  	github.com/Nathandela/swarm/android/gate	7.433s

$ go test -count=1 ./internal/protocol/... ./internal/verify/
ok  	github.com/Nathandela/swarm/internal/protocol	14.749s   # the protocol.md drift fences
ok  	github.com/Nathandela/swarm/internal/protocol/schema	1.000s
ok  	github.com/Nathandela/swarm/internal/verify	12.513s

$ golangci-lint run ./internal/phonecore/...
# 3 issues, ALL PRE-EXISTING and none in code this fix wrote or moved:
#   internal/phonecore/deps_allowlist_test.go:91  errcheck f.Close
#   internal/phonecore/state.go:1522,1545         errcheck os.Remove / d.Close (persistState, untouched)
```

The bound surface is unchanged: `LastCursor` is fold bookkeeping, not something a screen renders,
so `mobile.TranscriptItem` does not carry it and neither the golden nor the coverage fence moved.

### Residual

7. **A record missed INSIDE one item's own run stays missed.** If the phone folded cursors 10 and
   12 for an item and the repair returns 11, the guard drops it: IS-DELTA-1 reconstructs by
   concatenation in cursor order, and an item keeps a high water rather than a record of what it
   absorbed, so there is nowhere to put a late middle increment. Dropping it beats appending it in
   the wrong place. Rebuilding the item from the reseed instead would need the events half to be
   guaranteed WHOLE, which IS-CAP-4's cut is exactly the absence of. Carried as a `ponytail:`
   ceiling on the guard in `interaction.go`.

---

# Re-review of R1 (adversarial, against the closure rather than the finding)

The guard was re-verified BY EXECUTION, not by reading the claim: the three R1 cases were re-run,
and one further corruption vector was devised and driven.

## The new vector: repair through the LIVE stream, not through a reseed

R1's own restart case repairs through a `journal_reseed`. The vector it does not drive is the
gateway resuming from its durable PB-GW-8 delivered cursor: a phone that died between applying a
frame and that frame being acked receives those records again as ordinary `Event` frames at fresh
mailbox seqs. Nothing on such a frame marks it as a repeat — there is no reseed envelope, no
watermark, no floor — so the per-item high water is the only thing that can tell a re-send from a
delta (IS-LAYER-3: "for successive records of one streamed item, cursor order IS delta order").

`internal/phonecore/r1_liveredelivery_test.go`:
`TestR1_LiveRedeliveryStraddlingARestartIsNotFolded` folds cursors 10 and 11, restarts, then
re-sends 10 and 11 as live events at seqs 3 and 4 followed by the unseen 12. It PASSES against the
shipped guard — it is a fence added for coverage, not a red one. (Driven against the pre-fix code
it would have read `"Let me read the Let me read the file."`, the same doubling R1 names.)

```
$ go test ./internal/phonecore/ -run 'TestR1_' -count=1 -v
--- PASS: TestR1_LiveRedeliveryStraddlingARestartIsNotFolded (0.02s)
--- PASS: TestR1_ARepairedRecordIsNotFoldedTwice (0.00s)
--- PASS: TestR1_AReorderedRecordBehindTheItemsHighWaterIsRejected (0.00s)
--- PASS: TestR1_TheFoldHighWaterSurvivesARestart (0.00s)
ok  	github.com/Nathandela/swarm/internal/phonecore	0.891s

$ go test ./internal/phonecore/ -count=1 -race
ok  	github.com/Nathandela/swarm/internal/phonecore	29.833s
```

**R1's closure holds.** The `StateSchemaVersion` 10 → 11 bump is the right call and is confirmed:
the durable high water is what makes the guard survive the process death Android hands out
routinely, and the memory-only alternative leaves every `in_progress` item doubled on the first
repair after each launch.

### Residual 8 — the v10 → v11 UPGRADE window (measured, not fixed)

A blob stamped below 11 loads with every item's `LastCursor` at zero, so on the first launch after
an upgrade the guard reads zero and the first repair re-folds everything the restored transcript
was built from. Driven directly (v10 items, then the R1 reseed) the message came back as
`"Let me read the Let me read the file."` — the finding's own damage, once, per upgraded phone.

It is recorded rather than closed, for two reasons:

- **It is unreachable in the field.** No adapter implements `InteractionSource` anywhere in the
  tree (`grep -rn 'func (.*) Interactions('` over non-test code returns nothing), so no machine
  produces an item and no v10 blob shipped in 0.2.8 can carry one. The array is empty.
- **There is no complete cheap fix.** Seeding `LastCursor` from `Cursor` on restore closes it only
  for single-record items: the vulnerable class is exactly the multi-record `in_progress` item,
  and for that one the seed is a lower bound that still re-admits the middle records. The
  alternatives — dropping pre-v11 items on load and letting IS-DELTA-4's "incomplete from join"
  rebuild them, or carrying a per-item ledger of absorbed cursors — are both rulings, not
  one-liners. Neither should be taken on a path that cannot fire.

Closing it becomes worthwhile the moment the first `InteractionSource` adapter lands and BEFORE
that build ships, not before.
