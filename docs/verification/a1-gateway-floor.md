# A1 / W3 — interaction items: the append floor and the approval wake

**Workpackage**: W3 of the interaction program (ADR-009, ADR-010,
`docs/specifications/interaction-schema.md`, all Accepted 2026-08-07).
**Scope**: ADR-010 §7 (the producer-side spacing floor and its lossless merges) and ADR-010 §4
(the approval push wake and its deferral). Both fence files are the ones the ADR names:
`internal/remotegw/append_budget_test.go` and `internal/remotegw/push_trigger_test.go`.
**Out of scope, by the workpackage**: the daemon wiring that drives the queue, the per-kind
item types and their §5 caps, the approval lifecycle (`approval_resolved`, retention
exemption), every phone-side consumer, and both event producers. Only
`internal/remotegw` was touched.

---

## What landed

| File | What |
|---|---|
| `internal/remotegw/itemadmission.go` (new) | `ItemAdmission` — one admission queue per target: one release per `DefaultAppendWindow`, oldest-first within a priority class, lossless merges. |
| `internal/remotegw/push.go` | ADR-010 §4(a) interaction wake eligibility, §4(b) deferral, `PushConfig.After` timer seam, `claimWindow` now returns the window's remainder. |
| `internal/remotegw/append_budget_test.go` | 7 new tests beside the existing peek case (the file is unchanged above them). |
| `internal/remotegw/push_trigger_test.go` | 6 new tests beside the existing PB-PUSH cases (the file is unchanged above them). |

No existing test was modified. No dependency was added. Nothing outside `internal/remotegw`
was touched.

---

## Decisions recorded (points the ADR and the schema leave open)

1. **The queue lives in `internal/remotegw`, but it is PRODUCER code.** ADR-010 §7 says the
   floor is "`CoalescingSink.Terminal`'s shape, moved upstream of the sink that may not use
   it", and names `remotegw.DefaultAppendWindow` as its unit. It is placed in this package
   because the window and the §6.0 budget are this package's, and because the workpackage is
   scoped to this package; it is wired by the daemon, ahead of the journal append, and is
   never installed in a sidecar sink. That does not weaken interaction-schema.md §10's "the
   gateway parses no item": nothing in the sidecar's own path (`RelaySink`,
   `CoalescingSink`, `PushNotifier`) reads an item, and this type is not reachable from one.
   Recorded in the type's doc comment. **Handoff**: the daemon-side wiring releases into a
   `func(session string, item json.RawMessage) error`, so `Daemon.RecordInteraction`'s
   `InteractionItem` argument needs a raw-payload sibling, or the queue's release callback
   needs to unmarshal — a W4/W5 decision, not made here.

2. **Merging is scoped to one `item_id`, and the record collapse is a field-wise UNION,
   later-wins.** IS-DELTA-3 says "the `file_change` records of one tool run" collapse, which
   read literally would merge records with different `path` values into one record that can
   only carry one path — lossy, and the task's binding constraint is *lossless*. So the
   leanest compliant reading is applied: records sharing a `(session, item_id)` inside one
   window merge; distinct `item_id`s never merge, they queue and are spaced. A `tool_run`
   open (`tool`, `action`) and its close (`output_excerpt`, `exit_code`) therefore become one
   record carrying all four, which is exactly §7's stated example, while two file changes
   keep their two records and both their paths.
   `TestItemAdmission_NeverConcatenatesAcrossItemIDsOrKinds` pins the second half.

3. **`truncated`/`full_bytes` are unioned like any other key on a collapse.** A merged record
   therefore reports the last clipped record's byte count, not the sum. The alternative is
   per-field arithmetic this seam cannot do — it does not know which §5 cap clipped what —
   and the pair's job ("something here was clipped") survives the imprecision. Named as a
   ponytail ceiling in `fold`.

4. **The queue is unbounded.** §7 says the floor merges rather than drops, and every bound
   worth having here is a drop policy — which would lose a tool run or an approval and needs
   an ADR, not a constant. What keeps it finite is the merging itself (prose folds to one
   pending item per `item_id`; a tool run's two records fold to one), which is §7's own
   arithmetic: the floor binds above roughly 3–4 tool calls/s machine-wide. Named as a
   ponytail ceiling on `release`.

5. **One ordered queue, not N per-session queues.** §7 says "releasing per-session queues
   oldest-first"; selection by (class, arrival order) over one list is identical in effect
   and a third of the bookkeeping.
   `TestItemAdmission_CeilingIsPerTargetAcrossSessionsAndKinds` asserts both halves: the
   ceiling is per target (six sessions share one budget, IS-DELTA-2a) and no session is
   starved.

6. **Every `interaction` record is wake-eligible, not only `approval_request`.** This is
   forced, not chosen. IS-LAYER-1 gives every item the coarse wire type `interaction` and
   puts `kind` inside the AEAD-covered payload; interaction-schema.md §10 forbids the gateway
   parsing an item. So the gateway cannot single out an approval without doing the one thing
   §10 forbids. The wake therefore fires on any `interaction` append — a superset of §4(a)'s
   requirement — with the existing 30 s per-session window bounding the cost.
   **Disclosed cost**: a session streaming prose can wake the phone once per 30 s even with
   no approval pending. Making that precise needs either a coarse wake-eligibility hint on
   the wire record (a `schema.JournalRecord` field, out of this workpackage) or gateway-side
   item parsing (forbidden). Cited in `maybeWake`.

7. **An interaction wake is charged to the `needs_input` preference category.** An approval
   is the agent blocked on its owner, which is what that category means; charging it to
   `finished` would put the one wake the owner is waiting on behind the preference for the
   one they are not. PB-PUSH-8's sender-side suppression is unchanged and pinned by
   `TestADR010_InteractionWakeIsStillSuppressedByPreference`.

8. **Rule (b)'s deferral applies to the INTERACTION wake only; a suppressed group-transition
   wake is still dropped.** ADR-010 §4(b) states the rule generally, but PB-PUSH-0's existing
   fence (`TestPBPUSH0_CoalescesRepeatTransitionsWithinTheWindow`) pins that a session
   flapping between push-worthy groups inside one window wakes the phone **once** — so
   deferring those wakes would require editing an existing fence, which this program forbids.
   The distinction is real and not a convenience: a group wake is redundant (same session,
   same roster state, re-read whole on the next connect), while an approval is a distinct
   request with an expiry that nothing later re-announces. Pinned deliberately by
   `TestADR010_SuppressedGroupTransitionIsStillDropped` so the scope is a test, not a memory.

9. **The deferred wake's `PushTrigger` call stays inside `maybeWake`, as a closure.**
   `internal/remote/relay/pbpush3_producers_test.go` enumerates every *function* that hands a
   payload to the push provider, so a new one fails by name and requires a ledger entry plus
   a channel test. The deferred wake is not a new producer — same seal, same empty plaintext,
   same constant 78 bytes (`PushWakeEnvelopeSize`) — so keeping its one call site inside the
   enumerated function keeps that ledger *accurate* rather than merely quiet, and no
   cross-package test is touched. Cited in `maybeWake`'s doc comment. Verified green below.

10. **The deferral timer is never cancelled.** At most one is armed at a time and it fires a
    content-free wake at most one window later, so a gateway shutting down leaves one pending
    78-byte send — cheaper than a lifecycle `PushNotifier` does not otherwise have. Named as
    a ponytail ceiling in `NewPushNotifier`.

---

## RED — verbatim, failing first (GG-5)

### Cycle 1 — the append floor (`append_budget_test.go`)

Tests written first, at a tree where the admission queue exists nowhere.

```
$ go test ./internal/remotegw/ -run 'TestItemAdmission' -count=1
# github.com/Nathandela/swarm/internal/remotegw [github.com/Nathandela/swarm/internal/remotegw.test]
internal/remotegw/append_budget_test.go:515:33: undefined: ItemAdmission
internal/remotegw/append_budget_test.go:524:30: undefined: ItemAdmission
internal/remotegw/append_budget_test.go:527:9: undefined: NewItemAdmission
internal/remotegw/append_budget_test.go:527:26: undefined: ItemAdmissionConfig
internal/remotegw/append_budget_test.go:797:9: undefined: NewItemAdmission
internal/remotegw/append_budget_test.go:797:26: undefined: ItemAdmissionConfig
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
FAIL
```

Undefined-only, which is this package's established RED convention (stated in
`push_trigger_test.go`'s own header: a do-nothing stub is how a previous RED author here
ended up with four tests passing vacuously). The anti-vacuity arm is the mutation section
below.

**Interleaving, recorded honestly.** The first GREEN run for cycle 1 could not be taken when
it was due: a concurrent agent was mid-edit in `internal/phonecore`, which
`internal/remotegw`'s *test* package imports, so the package would not compile for reasons
outside this workpackage:

```
$ go test ./internal/remotegw/ -run 'TestItemAdmission' -count=1
# github.com/Nathandela/swarm/internal/phonecore
internal/phonecore/snapshot.go:349:24: st.Items undefined (type State has no field or method Items)
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
FAIL
```

`go build ./internal/remotegw/` (production code only, which does not import phonecore) exited
0 at that point. Cycle 2's tests were authored while that cleared, so the two REDs are
recorded before either implementation and the first GREEN covers both.

### Cycle 2 — the approval wake (`push_trigger_test.go`)

```
$ go test ./internal/remotegw/ -run 'TestItemAdmission|TestADR010' -count=1
# github.com/Nathandela/swarm/internal/remotegw [github.com/Nathandela/swarm/internal/remotegw.test]
internal/remotegw/push_trigger_test.go:1180:3: unknown field After in struct literal of type PushConfig
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
FAIL
```

---

## GREEN

```
$ go test ./internal/remotegw/ -run 'TestItemAdmission|TestADR010' -count=1 -race -v
=== RUN   TestItemAdmission_IsASpacingFloorNotABatchingDelay
--- PASS: TestItemAdmission_IsASpacingFloorNotABatchingDelay (0.01s)
=== RUN   TestItemAdmission_AgentMessageMergesByTextConcatenation
--- PASS: TestItemAdmission_AgentMessageMergesByTextConcatenation (0.00s)
=== RUN   TestItemAdmission_NeverConcatenatesAcrossItemIDsOrKinds
--- PASS: TestItemAdmission_NeverConcatenatesAcrossItemIDsOrKinds (0.00s)
=== RUN   TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord
--- PASS: TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord (0.00s)
=== RUN   TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged
--- PASS: TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged (0.00s)
=== RUN   TestItemAdmission_CeilingIsPerTargetAcrossSessionsAndKinds
--- PASS: TestItemAdmission_CeilingIsPerTargetAcrossSessionsAndKinds (0.19s)
=== RUN   TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget
--- PASS: TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget (4.40s)
=== RUN   TestADR010_InteractionAppendWakesWithoutAGroup
--- PASS: TestADR010_InteractionAppendWakesWithoutAGroup (0.00s)
=== RUN   TestADR010_SecondApprovalInsideOneTurnStillWakes
--- PASS: TestADR010_SecondApprovalInsideOneTurnStillWakes (0.00s)
=== RUN   TestADR010_SuppressedInteractionWakeIsDeferredNotDropped
--- PASS: TestADR010_SuppressedInteractionWakeIsDeferredNotDropped (0.00s)
=== RUN   TestADR010_OneDeferredWakeServesEveryPendingRequest
--- PASS: TestADR010_OneDeferredWakeServesEveryPendingRequest (0.00s)
=== RUN   TestADR010_SuppressedGroupTransitionIsStillDropped
--- PASS: TestADR010_SuppressedGroupTransitionIsStillDropped (0.00s)
=== RUN   TestADR010_InteractionWakeIsStillSuppressedByPreference
--- PASS: TestADR010_InteractionWakeIsStillSuppressedByPreference (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/remotegw	6.873s
```

Whole package, race-enabled, nothing else disturbed:

```
$ go test ./internal/remotegw/ -count=1 -race
ok  	github.com/Nathandela/swarm/internal/remotegw	26.373s
```

Cross-package fences that could plausibly fire on this change, all green and none touched:

```
$ go test ./internal/remote/relay/ -run 'TestPBPUSH3' -count=1     # the push producer ledger
ok  	github.com/Nathandela/swarm/internal/remote/relay	2.176s

$ go test ./internal/protocol/... ./internal/skeleton/ -count=1    # GG-7 drift + the s19 e2e
ok  	github.com/Nathandela/swarm/internal/protocol	11.009s
ok  	github.com/Nathandela/swarm/internal/protocol/schema	1.006s
ok  	github.com/Nathandela/swarm/internal/skeleton	189.097s

$ go build ./... && go vet ./internal/remotegw/
(exit 0, no output)
```

`golangci-lint run ./internal/remotegw/` reports 27 pre-existing `errcheck` findings, all in
files this workpackage did not touch (`gateway.go`, `inboundstate.go`, and six test files) or
in `append_budget_test.go` above line 405, i.e. inside the pre-existing peek case. The new
production files (`itemadmission.go`, `push.go`) contribute **zero** findings.

---

## Teeth — mutation runs (anti-vacuity)

Each mutation was applied to the production code, run, and reverted; the package is green
again after all four (`go test ./internal/remotegw/ -count=1 -race` → ok, 31.865s).

**M1 — the interaction wake branch disabled** (`if false && rec.Type == recordTypeInteraction`):

```
--- FAIL: TestADR010_InteractionAppendWakesWithoutAGroup (0.00s)
    push_trigger_test.go:1205: push count for an interaction append with no Group: got 0, want 1 (ADR-010 §4(a))
--- FAIL: TestADR010_SecondApprovalInsideOneTurnStillWakes (0.00s)
    push_trigger_test.go:1231: push count for a second approval inside one turn: got 1, want 2 -- the session never left the permission group, so there is no transition to ride (ADR-010 §4(a))
--- FAIL: TestADR010_SuppressedInteractionWakeIsDeferredNotDropped (0.00s)
    push_trigger_test.go:1243: first approval: got 0 pushes, want 1
--- FAIL: TestADR010_OneDeferredWakeServesEveryPendingRequest (0.00s)
    push_trigger_test.go:1283: three sessions' first approvals produced 0 pushes, want 3 (the window is PER SESSION)
```

**M2 — the deferral disabled, i.e. today's drop** (`if false && deferrable && ...`):

```
--- FAIL: TestADR010_SuppressedInteractionWakeIsDeferredNotDropped (0.00s)
    push_trigger_test.go:1252: 0 deferral timers scheduled, want exactly 1
--- FAIL: TestADR010_OneDeferredWakeServesEveryPendingRequest (0.00s)
    push_trigger_test.go:1293: 0 deferral timers scheduled for three pending sessions, want exactly 1: ONE deferred wake serves every request pending at that moment (ADR-010 §4(b))
```

M2 is the important one: the wake still *eventually* happens in M1's absence only by
accident of a status transition, whereas M2 is the exact defect ADR-010 §4(b) names — a
suppressed wake that is dropped rather than deferred — and it is caught by the timer
assertion, not by a push count that a later unrelated record would have satisfied.

**M3 — `approval_request` loses its head-of-queue class** (`classApproval` → `classOther`):

```
--- FAIL: TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged (0.00s)
    append_budget_test.go:705: the first record released after the approval was offered is a "tool_run" (tr0), want approval_request: every kind other than agent_message takes the head of the queue, approval_request first of all (IS-DELTA-3)
```

Honesty note: the sustained-transcript fence did **not** fail under M3. Its tool runs drain
faster than they arrive (≈3/s offered against an 8/s floor), so at the instant the approval is
offered there is rarely a same-class backlog to sit behind. The head-of-queue rule's teeth are
therefore the focused test, which builds the backlog deliberately; the fence measures rate and
losslessness.

**M4 — record collapse replaces instead of unions** (`p.merged = extra`):

```
--- FAIL: TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord (0.00s)
    append_budget_test.go:656: collapsed tool = "", want Bash: the OPEN's fields must survive the collapse -- a card with no tool name is the open record silently dropped
    append_budget_test.go:667: collapsed item lost `action`: a collapse is a UNION of the two records, not a replacement (§7)
--- FAIL: TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget (0.63s)
    append_budget_test.go:937: tool run tr-1960 came back without its tool name ("") or its exit code (true): a collapse is a lossless UNION of the open and the close, never a replacement (ADR-010 §7)
```

---

## What the fence actually measures

`TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget` is the transcript case ADR-010
§7 asks for, built beside the peek case and reusing its machinery (`vclock`,
`quotaAppender` modelling the relay's real tumbling-minute `MailboxAppendPerMin`, a real
`RelaySink`, a real `CoalescingSink`, and the phone's real `crypto.MailboxReceiver`). It
drives 150 s of virtual time — crossing the relay's tumbling minute boundary twice — with:

- three sessions streaming `agent_message` increments at the real 16 ms render debounce,
- a `tool_run` open + close every ~320 ms,
- one `approval_request` at t+45 s,

i.e. ~200 items/s offered, and asserts: zero quota refusals, appends under
`elapsed/DefaultAppendWindow + 2`, at least 4 appends/s (the floor spaces, it does not
silence), zero gaps at the phone, every session's prose reconstructing **byte-for-byte** by
cursor-order concatenation, every tool run arriving with both the open's `tool` and the
close's `exit_code`, the approval arriving exactly once and byte-identical, and the approval
released within one window of being offered.

---

## Review addendum — a deferred wake outlived the preference it was authorized under

Found by the quality/test-integrity review of this workpackage, fixed in place under the same
TDD discipline. It is a defect the deferral introduced, not a pre-existing one: rule (b) is
the only push this type emits with **no journal record driving it**, so it is the only wake
that can survive the preference it was authorized under. `maybeWake` reads `categoryEnabled`
before `claimWindow`, so an owner who switched `needs_input` off while a deferral was in
flight still got the wake up to a full window later — and PB-PUSH-8 requires a disabled toggle
to mean *no push sent, verified at the sender*, precisely because a sent-then-ignored push
still hands the provider token, timing and size.

### RED — verbatim, failing first (GG-5)

```
$ go test ./internal/remotegw/ -run 'TestADR010_ADeferredWakeHonoursAPreferenceFlippedMeanwhile' -count=1 -v
=== RUN   TestADR010_ADeferredWakeHonoursAPreferenceFlippedMeanwhile
    push_trigger_test.go:1362: the deferred wake fired for a category the owner had since disabled: got 2 pushes, want 1 (PB-PUSH-8: suppressed at the SENDER, or the provider still sees token, timing and size)
--- FAIL: TestADR010_ADeferredWakeHonoursAPreferenceFlippedMeanwhile (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/remotegw	2.534s
FAIL
```

### The fix

`push.go`'s deferred callback re-reads the preference at send. `claimDeferred` runs **first**
either way: a dropped wake must still release the single timer arm, or one preference flip
wedges the deferral path shut for every session that defers after it. That second half is
asserted in the same test (two timers over the run, not one), and it was the assertion that
caught the naive ordering while the fix was being written.

### GREEN

```
$ go test ./internal/remotegw/ -run 'TestADR010' -count=1 -race -v
--- PASS: TestADR010_InteractionAppendWakesWithoutAGroup (0.00s)
--- PASS: TestADR010_SecondApprovalInsideOneTurnStillWakes (0.00s)
--- PASS: TestADR010_SuppressedInteractionWakeIsDeferredNotDropped (0.00s)
--- PASS: TestADR010_OneDeferredWakeServesEveryPendingRequest (0.00s)
--- PASS: TestADR010_SuppressedGroupTransitionIsStillDropped (0.00s)
--- PASS: TestADR010_InteractionWakeIsStillSuppressedByPreference (0.00s)
--- PASS: TestADR010_ADeferredWakeHonoursAPreferenceFlippedMeanwhile (0.00s)
ok  	github.com/Nathandela/swarm/internal/remotegw	11.867s
```

The whole PB-PUSH-0/3/5/8/10 fence is green beside it (`-run 'TestADR010|TestPBPUSH'`), so
the re-check did not move any existing wake's behaviour: it fires only on the deferred path.
