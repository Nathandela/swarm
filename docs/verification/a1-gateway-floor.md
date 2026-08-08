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

---

## Review addendum 2 — R3: the combined ceiling was doubled

Found by the adversarial review of `79a070d`. **The defect**: this workpackage added a second
admission point without connecting it to the first. `ItemAdmission` releases up to one item
per `DefaultAppendWindow` (8/s); `CoalescingSink` admits terminal snapshots at one per
`DefaultAppendWindow` (8/s); and an item release *becomes* a journal record, which the sink
forwards immediately and never coalesces (R-GW.5). Two independent 8/s admissions against
**one** target is 16/s, against a relay that caps that target at `MailboxAppendPerMin: 600`
(10/s) on a tumbling minute. The overrun is not merely late: a quota-refused append burns an
outbound seq (PB-GW-7) and the manufactured gap stales journal **and** terminal (PB-SYNC-1).
IS-DELTA-2a names exactly this and exempts nobody — *"admission SHALL be bounded per target
and SHALL govern every kind … IS-DELTA-3 orders the queue; it exempts nobody from it."*

Measured at RED against the relay's real quota model: **2344 appends offered over 150 s
(15.6/s), 676 of them refused, 2 gaps at the phone, and 338 of 1172 interaction records lost
to the refusals.**

### Where the shared budget had to live, and why it is not one object

`ItemAdmission` runs in the **daemon** process (`skeleton.Daemon.initInteractionsLocked`
releases into `daemon.RecordInteractionRaw`); `CoalescingSink` runs in the **gateway sidecar**
(`cmd/swarm-remote` → `remotegw.NewService`, which reaches the daemon over
`cfg.DaemonSocket`). They cannot share a budget *object*: there is a process boundary between
them, and threading a budget across it would be a new IPC for a rate counter.

They can and now do share a **slot**, because every machine→phone append on the
journal/terminal stream passes through exactly one place — `CoalescingSink`. An item release
arrives there as `Event`, so charging `Event` to the same slot a snapshot release consumes is
what makes the two streams share one per-target ceiling. Nothing needed threading through
construction, so `internal/skeleton` was not touched.

### The fix (`internal/remotegw/coalesce.go`, +5 behavioural lines)

`c.last` (when the slot was last consumed) becomes `c.nextFree` (when the slot is next free),
and every append goes through one `debitLocked`:

```go
func (c *CoalescingSink) debitLocked(now time.Time) {
	if now.After(c.nextFree) {
		c.nextFree = now
	}
	c.nextFree = c.nextFree.Add(c.window)
}
```

The charge is unconditional; the **wait** is not. `Event`, `Snapshot` and `Reseed` still
forward immediately (R-GW.5) and debit; `release` (the terminal path) both waits for the slot
and debits. Spending from `now` on every append is precisely what let the two streams
interleave at 2x — a journal record landing 1 ms after a snapshot released reset the clock the
snapshot had just paid for, so each stream saw a free slot every window and the target saw
two. A slot in the past means the stream is idle, so this stays IS-DELTA-2's **spacing floor**
and never becomes a batching delay.

### The arbitration, decided and cited

**The terminal is the stream that yields; the journal never waits.** This is forced, not
preferred: R-GW.5 forbids delaying or dropping a journal record at the gateway, so at a
saturated ceiling the only stream that *can* yield is the one that may be coalesced. It is
also the direction the ADRs already committed to — ADR-009 (2): *"no snapshot frames are
appended to a phone … the machine→phone append budget in (7) is spent by the journal alone"*,
and (7): *"with no snapshot appends, the transcript inherits the whole of what the peek used
to spend"* (interaction-schema.md §6 repeats it). The peek survives on the transition slice's
terms only, which is what the terminal well being deleted at the end of slice I1 means.

**Yielding is not dying**, and the fence says so: the stash is latest-wins per session, so a
held frame is superseded rather than lost and ships on the first idle wake after the
transcript goes quiet. `ItemAdmission`'s own priority rules are untouched — `approval_request`
still heads the queue and still waits at most one window, pinned by the two existing tests
that measure it.

**The debt is unclamped, deliberately** (ponytail comment on `debitLocked`). A burst of
journal records pushes the terminal's next release out one window each, and that is honest
arithmetic rather than a bug to cap: those appends really were spent out of
`MailboxAppendPerMin`, and the terminal is the only stream that can pay them back. What bounds
it in practice is the producer's floor holding the journal side to one release per window
machine-wide; assertion (e) below is what would break if that stopped being true.

### What is still NOT counted (disclosed, out of R3's scope)

- **Command replies** append straight to the relay from `lease_confirm.go:93`
  (`Mailbox.MailboxAppend(ctx, ReplyTarget, env)`), bypassing the sink. They are
  phone-command-driven (one per command), not a sustained producer.
- **`RelaySink.Replay()`** at service start (`service.go:319`) re-appends the outbox's pending
  entries directly. One-shot per restart.
- **`RelaySink.Reconcile()`** rides *inside* `Snapshot` (`relaysink.go:172`), so one debited
  `Snapshot` can be two relay appends. Per reconnect, not per window.

Each is a distinct seam with its own rate story; folding them in would widen this fix past the
finding and past the packages it names.

### RED — verbatim, failing first (GG-5)

```
$ go test ./internal/remotegw/ -run 'TestAppendBudget_ItemReleasesAndSnapshotsShareOneCeiling' -count=1 -v
=== RUN   TestAppendBudget_ItemReleasesAndSnapshotsShareOneCeiling
    append_budget_test.go:1038: 1668 appends over 2m30s = 11.1/s against ONE target with a transcript and a peek pressing simultaneously, over the §6.0 ceiling of 1202 (<= 8/s COMBINED across journal and terminal): the item floor and the terminal coalescer are two INDEPENDENT 125ms admissions, i.e. 2x the ceiling -- admission is bounded PER TARGET and exempts nobody (IS-DELTA-2a)
    append_budget_test.go:1047: the relay refused 676 of 2344 appends (338 item offers and 338 snapshots saw the error) over 2m30s: two independent admissions against one MailboxAppendPerMin=600 target overrun it (PB-GW-7)
    append_budget_test.go:1110: the phone saw 2 GAPS with no relay failure: a refused append burns an outbound seq and the gap stales journal AND terminal (PB-GW-7, PB-SYNC-1)
    append_budget_test.go:1119: interaction record cursor=301 reached the phone 0 times, want exactly 1 (834 of 1172 releases arrived): a journal record is never coalesced, deferred or dropped (R-GW.5)
--- FAIL: TestAppendBudget_ItemReleasesAndSnapshotsShareOneCeiling (0.18s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/remotegw	1.306s
FAIL
```

### What the new case measures

`TestAppendBudget_ItemReleasesAndSnapshotsShareOneCeiling` is added **beside** the peek case
and the transcript case, reusing their machinery (`vclock`, `quotaAppender` modelling the
relay's real tumbling-minute `MailboxAppendPerMin`, a real `RelaySink`, a real
`CoalescingSink`, the phone's real `crypto.MailboxReceiver`). It runs both producers against
**one target simultaneously** for 150 s of virtual time at the real 16 ms render debounce — a
live peek at ~62 snapshots/s and a transcript streaming prose through `ItemAdmission` into
`sink.Event` — and asserts:

- **(a)** combined appends ≤ `elapsed/DefaultAppendWindow + 2` (the §6.0 ceiling, IS-DELTA-2a);
- **(b)** zero quota refusals, zero errors surfaced to either producer;
- **(c)** zero gaps when the accepted stream is replayed into the phone's receiver;
- **(d)** every item the floor released reached the phone **exactly once** — the ceiling is
  never paid for by delaying or dropping a journal record (R-GW.5);
- **(e)** the peek's newest grid ships on an idle wake once the transcript goes quiet — the
  terminal yields, it does not die (PB-APP-4).

Measured at GREEN: **1200 appends over 150 s = 8.0/s**, against 2344 offered.

### Teeth — mutation runs (anti-vacuity)

| # | Mutation | Result |
|---|---|---|
| 1 | `debitLocked` spends from `now` (`c.nextFree = now.Add(c.window)`) — the pre-fix two-budget behaviour | new case FAILS with all four RED assertions, byte-identical to the RED above |
| 2 | `Event` forwards without debiting (the journal is uncharged) | new case FAILS **and** the pre-existing `TestRelaySink_SustainedPeekStaysUnderAppendBudget` FAILS (`1321 appends over 2m30s = 8.8/s`) — the charge on `Event` was load-bearing before this fix and still is |
| 3 | `Event` debits **twice** (debt accrues faster than it repays) | (a)–(d) stay green and only **(e)** fires: `the newest grid the phone would show is "frame 0", want "the last grid"` — the anti-starvation assertion isolates exactly the failure it exists for |

All three were reverted; the diff above is the final state.

### GREEN

```
$ go test ./internal/remotegw/ -run 'TestAppendBudget|TestItemAdmission|TestRelaySink_SustainedPeek|TestCoalescingSink|TestGatewayRunTerminal' -count=1 -race -v
--- PASS: TestRelaySink_SustainedPeekStaysUnderAppendBudget (0.10s)
--- PASS: TestGatewayRunTerminal_CoalescedPeekShowsLatestGrid (0.27s)
--- PASS: TestItemAdmission_IsASpacingFloorNotABatchingDelay (0.00s)
--- PASS: TestItemAdmission_AgentMessageMergesByTextConcatenation (0.00s)
--- PASS: TestItemAdmission_NeverConcatenatesAcrossItemIDsOrKinds (0.00s)
--- PASS: TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord (0.00s)
--- PASS: TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged (0.00s)
--- PASS: TestItemAdmission_CeilingIsPerTargetAcrossSessionsAndKinds (0.16s)
--- PASS: TestItemAdmission_SustainedTranscriptStaysUnderAppendBudget (2.79s)
--- PASS: TestAppendBudget_ItemReleasesAndSnapshotsShareOneCeiling (0.81s)
--- PASS: TestCoalescingSink_StashIsPerSession (0.00s)
--- PASS: TestCoalescingSink_TeardownBlankSurvivesConcurrentSession (0.00s)
--- PASS: TestCoalescingSink_MultiSessionStaysUnderCombinedBudget (0.03s)
--- PASS: TestGatewayRunTerminal_BlanksPhoneOnDaemonEnd (0.00s)
--- PASS: TestGatewayRunTerminal_SubscribesAndForwards (0.13s)
PASS
ok  	github.com/Nathandela/swarm/internal/remotegw	6.481s
```

Note the three pre-existing budget fences above — the sustained peek, the multi-session
combined budget and the coalesced-grid case — pass **unmodified**. With terminal traffic
alone, `nextFree` and the old `last + window` are the same condition, which is why keying the
budget off accrued debt changed no single-stream behaviour.

### Gates

`go build ./...` and `go vet ./...` clean. `gofmt -l` clean on both touched files
(`terminal_watcher.go`'s pre-existing formatting hit is untouched and unrelated).
`go test -count=1 -race`: `internal/remotegw` ok 27.0s, `internal/skeleton` ok 189.6s,
`cmd/swarm-remote` ok 34.5s, `internal/protocol` ok 18.9s, `internal/protocol/schema` ok 2.2s,
`internal/verify` ok 40.2s, `internal/phonesim` ok 3.6s. The protocol.md drift fences
(`TestProtocolMD_ExistsAndDocumentsEveryField`, `TestProtocolMD_DocumentsEveryOp`,
`TestProtocolMDBidi_FieldSetMatchesStructs`) are untouched and green — no wire field or op
moved. `golangci-lint run internal/remotegw/...` reports nothing in either touched file (its
two hits in `append_budget_test.go` are at lines 333/339, inside the pre-existing peek case).
No exported symbol was added or removed, so B94's reachability ledger is unmoved. No existing
test was modified.

---

# Re-review of R3, and one defect found in the append floor while doing it

## R3's closure holds — re-measured with a THIRD stream pressing

R3's own fence drives items and terminal snapshots at once. The re-review added the stream it does
not: a roster `Snapshot` once a second, on the same target, alongside a peek at the 16 ms render
debounce and a transcript streaming prose at the same rate, for 150 s of virtual time across two
tumbling relay minutes (scratch harness, not kept — it is the R3 fence with one `sink.Snapshot`
call added to the loop).

```
1173 appends over 2m30s = 7.82/s (refused=0 snapErr=0 offerErr=0 termErr=0)
```

7.82/s against §6.0's 8/s combined ceiling, with ZERO refusals against `MailboxAppendPerMin: 600`.
The slot arithmetic in `debitLocked` holds when a third producer joins, which is the property that
matters: the charge is unconditional and per APPEND, so a new stream costs the target a slot
rather than buying itself a budget.

## Defect found — an `approval_request` IS collapsed by the floor (IS-DELTA-3)

Pre-existing at 79a070d, in `internal/remotegw/itemadmission.go`, and not introduced by any of the
five remediations — but R4 is what made it bite, because R4 put a real `content_hash` on the item.

**What was wrong.** `pendingItem.fold` applies ADR-010 §7's record collapse to EVERY kind that is
not `agent_message`. IS-DELTA-3 scopes that collapse to two kinds and says so twice:

> `tool_run` and `file_change` are subject instead to ADR-010 §7's **record collapse** ... **Every
> remaining kind SHALL keep its own record** ... *Never merged* and *never delayed* are different
> guarantees, and only the first is compatible with the ceiling: an `approval_request` waits at
> most one window, at the front.

So two records of ONE `approval_request` landing inside one window — the CLI withdrawing its
prompt, or re-announcing it — were re-marshalled into one object by a field-wise union.

**Why it matters now.** §3.5's `content_hash` is SHA-256 over the item AS SHIPPED with its own slot
zeroed, and `daemon.RecordInteractionRaw`'s own contract states the premise the digest rests on:
"it forwards an UNMERGED item byte-exact — which is what keeps an approval_request's bytes the
bytes the daemon hashed (IS-APR-2)". A union falsifies that premise: the shipped card carries a
digest that does not name it, nothing holding the item can re-derive the hash, and IS-APR-2 forbids
the phone recomputing one. Measured end to end through the shipped producer, a merged card came out
carrying `9e356aa0…` while SHA-256 over its own bytes is `00ab14b8…`.

The existing fence states the rule in as many words — `TestItemAdmission_ApprovalRequestHeadsThe`
`QueueAndIsNeverMerged`: "an approval_request is NEVER merged — its bytes are the content the
daemon hashed (IS-APR-2)" — but only ever offers the item ONCE, so `fold` is never reached from it.

**The fix** (`itemadmission.go`, +12 lines of which 9 are comment): an `approval_request`'s queue
key gets a unique suffix, so a second record for one request takes its OWN place in the queue
instead of folding. Nothing else changes — it still heads the queue by class, still waits at most
one window, still counts against the per-target ceiling. Scoping it to the one kind rather than to
IS-DELTA-3's literal reading (which would also stop collapsing `plan_update`, `session_status` and
`approval_resolved`) is deliberate: those three collapse harmlessly under later-wins and cost the
ceiling nothing, and widening the rule is a schema reading, not a bug fix.

### RED — verbatim, failing first

```
$ go test ./internal/remotegw/ -run 'TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed' -count=1 -v
=== RUN   TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed
    itemadmission_rr_test.go:60: the floor released 1 approval_request record(s) for two offered; want 2. IS-DELTA-3 scopes record collapse to tool_run and file_change and says every remaining kind KEEPS ITS OWN RECORD, and again for this one: an approval_request is never merged, only delayed at most one window. Released: [{"content_hash":"beef","decisions":[{"id":"accept","label":"Allow"}],"expires_at":"2026-08-07T12:02:00Z","item_id":"ap1","kind":"approval_request","mode":"card","status":"declined","summary":"Bash: rm -rf build","ts":"2026-08-07T12:00:00Z","v":1}]
--- FAIL: TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed (0.01s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/remotegw	0.934s
```

Note what the released record is: the withdrawal's `status:"declined"` sitting on top of the
pending request's `content_hash`, `expires_at` and `decisions`. One card, two records' worth of
state, and a digest naming neither.

### GREEN

```
$ go test ./internal/remotegw/ -run 'TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed' -count=1 -v
--- PASS: TestItemAdmission_TwoRecordsOfOneApprovalRequestAreNeverCollapsed (0.01s)
ok  	github.com/Nathandela/swarm/internal/remotegw	0.855s

$ go test ./internal/remotegw/ -count=1 -race
ok  	github.com/Nathandela/swarm/internal/remotegw	35.753s
```

Every pre-existing budget and admission fence passes unmodified, including
`TestItemAdmission_ApprovalRequestHeadsTheQueueAndIsNeverMerged` (which now tests what it says) and
the three ceiling fences (an approval that takes two slots instead of one is still charged, so the
per-target arithmetic is unchanged).

### Teeth — one mutation, reverted

Widening the unique key to EVERY kind (`if kind != ""`) fails
`TestItemAdmission_AgentMessageMergesByTextConcatenation` and
`TestItemAdmission_ToolRunOpenAndCloseCollapseToOneRecord` — which is what proves the scoping is
load-bearing rather than a blanket disabling of the collapse.

---

# Owner ruling 2026-08-07 — `MaxItemBytes` raised to 16 KiB, and the merge drop closed

**Ruling** (Nathan, 2026-08-07): *"`MaxItemBytes` is RAISED so the section-5 field maxima jointly
fit and the merge path can never produce an over-cap item — pick the exact value by arithmetic."*
Recorded as ADR-009's **Amendment 2026-08-07**, which carries the decision and the alternatives;
this section carries the measurement, the RED and the teeth.

**Why the evidence lands here and not only in `a1-carriage.md`.** The defect was *recorded* in
`a1-carriage.md`'s re-review of R2, but it is manufactured by the **append floor** — the merge in
`remotegw.ItemAdmission` is what creates an item no single record could be — and this is the
floor's evidence file. `a1-carriage.md`'s open-defect section carries a closing note pointing here.

## The arithmetic, measured through the shipped producer

A temporary harness (`internal/skeleton/zz_measure_test.go`, **not kept**) serialized each §5
worst case through `skeleton.serializeItem`, the same function `fitItem` measures on:

```
approval_request maxima (no D7 tuple)           11736 B  (11.46 KiB)
approval_request maxima (+ D7 tuple)            11930 B  (11.65 KiB)
approval_request maxima (+tuple,+trunc pair)    11967 B  (11.69 KiB)
plan_update maxima (state=in_progress)          15166 B  (14.81 KiB)
plan_update maxima (+trunc pair)                15203 B  (14.85 KiB)   <-- worst single item
tool_run maxima (output_excerpt at MaxText)      5415 B  (5.29 KiB)
file_change maxima (diff_excerpt at MaxText)     5372 B  (5.25 KiB)
agent_message union of 1 x MaxTextBytes          4334 B  (4.23 KiB)
agent_message union of 2 x MaxTextBytes          8430 B  (8.23 KiB)
agent_message union of 3 x MaxTextBytes         12526 B  (12.23 KiB)
agent_message union of 4 x MaxTextBytes         16622 B  (16.23 KiB)   <-- still over 16 KiB
FLOOR 2-way merged agent_message (as shipped)    8405 B  (8.21 KiB)   <-- worst sanctioned merge
```

`max(15 203, 8 405) = 15 203 B`; the next power-friendly bound is **16 KiB = 16 384 B**. 8 KiB was
measurably insufficient (both cases over it) and 32 KiB buys nothing the arithmetic asks for.
Headroom at 16 KiB: 1 181 B on the worst single item, 7 979 B on the worst sanctioned merge.

**`plan_update`, not `approval_request`, is the binding case.** The ruling cited the approval at
~11 697 B; the measurement says a 64-step plan is 3.2 KiB larger, because §5's step maxima
(64 × 200 B) carry 64 JSON objects of structure with them. Verifying rather than assuming is what
the ruling asked for, and it moved the binding constraint.

**Two things the raise does NOT close, disclosed rather than absorbed:**

1. **An unbounded fold.** `ItemAdmission.concatText` merges *every* increment pending for one
   `item_id` in a window, not two. Four at `MaxTextBytes` is 16 622 B — still refused, still
   dropped. Reaching it needs ~16 KiB of prose in 125 ms (≈131 KB/s), far above any observed CLI
   token rate, and no adapter streams increments today. Open point, carried in the amendment.
2. **Bytes as §5 counts them ≠ serialized bytes.** `prompt_lines` is capped in *runes*;
   `tool` / `path` / `old_path` / `truncation_marker` / `decisions[].id` carry no per-field cap at
   all; JSON escaping expands a byte-capped field by up to 6×. So `fitItem`'s stage 2 is **kept
   unchanged** and IS-CAP-5 says so normatively.
   `TestApprovalRequest_AtTheMaximaTheHashStillNamesTheBytesItShipped` (4-byte prompt runes,
   32 000 B of prompt) is the case that still drives it.

## RED — verbatim, failing first (GG-5)

New fences in `internal/skeleton/interaction_cap_test.go`, against the unraised cap. The
`steps[1..63]` lines are elided (identical to `steps[0]`); nothing else is.

```
$ go test ./internal/skeleton/ -run 'TestInteractionCap_' -count=1 -v
=== RUN   TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped
2026/08/07 21:19:50 interaction: append floor release failed (0 item(s) still held): interaction: item is 8368 bytes, over the 8192-byte cap (interaction-schema.md §5)
    interaction_cap_test.go:68: no agent_message item reached the journal for session s-cap-merge after 10s. IS-DELTA-2 merges two pending increments for one item_id into ONE lossless append; §5's MaxItemBytes must admit that union, or the floor dequeues the item and the append boundary refuses it -- and the agent's text is gone with nothing marked damaged
--- FAIL: TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped (12.36s)
=== RUN   TestInteractionCap_APlanUpdateAtTheDocumentedMaximaFitsWhole
    interaction_cap_test.go:108: a plan_update sitting exactly on §5's maxima reports truncated = true; the maxima are JOINTLY bounded by MaxItemBytes, so nothing on them is clipped and §2 sets the flag only when a field WAS
    interaction_cap_test.go:119: journalled steps[0].text is 64 bytes; §5 allows a step 200 B and the item cap must admit all 64 of them
    [... steps[1] .. steps[63], identical ...]
--- FAIL: TestInteractionCap_APlanUpdateAtTheDocumentedMaximaFitsWhole (0.05s)
=== RUN   TestInteractionCap_TheItemCapAdmitsEveryDocumentedFieldMaximum
    interaction_cap_test.go:137: MaxItemBytes = 8192, under the 15203-byte plan_update §5's own maxima describe; the field maxima must be JOINTLY bounded by the item cap (ADR-009 Amendment 1)
    interaction_cap_test.go:141: MaxItemBytes = 8192, under the 8405-byte union of two MaxTextBytes increments; IS-DELTA-2's merge is lossless, so an item cap below it drops the agent's text
--- FAIL: TestInteractionCap_TheItemCapAdmitsEveryDocumentedFieldMaximum (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	13.481s
```

The first RED is the confirmed defect reproducing itself on the shipped path: the producer offered
8 192 bytes of the agent's message and the transcript held **none of it**, with one log line as
the only trace.

## GREEN

```
$ go test ./internal/skeleton/ -run 'TestInteractionCap_' -count=1 -v
--- PASS: TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped (2.23s)
--- PASS: TestInteractionCap_APlanUpdateAtTheDocumentedMaximaFitsWhole (0.03s)
--- PASS: TestInteractionCap_TheItemCapAdmitsEveryDocumentedFieldMaximum (0.00s)
ok  	github.com/Nathandela/swarm/internal/skeleton	3.336s
```

## What changed

| File | What |
|---|---|
| `docs/adr/ADR-009-structured-chat-interaction.md` | Amendment 2026-08-07: the ruling, the arithmetic table, the disclosed cost, the four alternatives and why each was rejected. |
| `docs/specifications/interaction-schema.md` | §5's `MaxItemBytes` row is 16 KiB and marked **ratified** (the per-field numbers stay proposed); new **IS-CAP-5** makes the joint bound normative and requires a future per-field raise to re-derive the item cap. |
| `internal/daemon/interaction.go` | `MaxItemBytes = 16 << 10`; the comment no longer says PROPOSED AND UNRATIFIED and carries the two failures the old number caused. |
| `internal/skeleton/interaction.go` | `fitItem`'s doc re-derived: stage 2 no longer fires on §5's maxima and is kept for the three cases the byte table does not bound. The per-field constants' comment now says only the item cap was ratified. `itemUnclippedFields`' headroom note re-measured. |
| `internal/skeleton/interaction_cap_test.go` (new) | The three fences above. |

## Three existing tests updated — the sanctioned fixture-follows-spec pattern, declared

The ruling anticipated this: *"keep every existing cap test green without modifying it — if one
hardcodes 8 KiB as a spec value, that is the fixture-follows-spec update pattern, record it
explicitly."* Three tests encoded the old number. All three are recorded here in full; **no
assertion was weakened to make an implementation pass**, and every other cap fence
(`TestInteractionR2_*`, eight of them) is green **unmodified**. Tests that read the production
constant instead of spelling the literal — `assertFitsItemCap` (`interaction_r2_test.go:138`) and
`TestDaemon_RecordInteractionRefusesAnItemOverTheByteCap` (`daemon/interaction_test.go:166`) —
tracked the raise with no edit at all, which is why they are not in this list.

**1. `interaction_rr_test.go:59` — a literal.**

```go
-	if len(raw) > 8<<10 {
-		t.Fatalf("the shipped item is %d bytes, over §5's 8 KiB MaxItemBytes", len(raw))
+	if len(raw) > specMaxItemBytes {
+		t.Fatalf("the shipped item is %d bytes, over §5's %d-byte MaxItemBytes", len(raw), specMaxItemBytes)
```

`specMaxItemBytes = 16 << 10` was added to `interaction_r2_test.go`'s spec-literal block, which is
that file's own stated convention ("EVERY NUMBER BELOW IS SPELLED AS THE SPEC'S OWN LITERAL, not
as a production constant"), so the number is pinned in one place and a wrong constant still fails.
The test itself is otherwise untouched and still asserts the multi-byte-rune case truncates and
the digest still names the shipped bytes.

**2. `interaction_r2_test.go` — `AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped`,
renamed to `…IsShippedWholeNotDropped`.** This is the one whose *premise* the ruling inverts, so it
is declared rather than quietly edited:

```go
-	assertTruncationPair(t, item, payload)
+	if _, clipped := item["truncated"]; clipped {
+		t.Errorf("an approval_request on §5's own per-field maxima reports truncated = %v; ...")
+	}
```

R2 wrote it against an 8 KiB cap, where an approval on §5's maxima was over the item cap and
IS-CAP-1's truncator was what saved it from being dropped. Under the raise nothing on those maxima
is over a per-field cap, so nothing is clipped and the card ships **whole** — a strictly stronger
outcome, and `truncated` must now be **absent**, because §2 sets it only when a field *was*
clipped and an item claiming a clip that did not happen makes every consumer render IS-DELTA-4's
elision on a complete card. Everything else the test asserts (the card survives as a card: kind,
summary, mode, 8 decisions with ids and labels intact, non-empty prompt lines, fits the item cap)
is unchanged and still passes. Two stale prose comments naming "the 8 KiB MaxItemBytes" were
corrected in the same file; neither is load-bearing for a pass.

**3. `internal/adapter/interaction_test.go:242` — a literal inside a probe classifier.**

```go
-		case len(p.Raw) > 8<<10: // larger than interaction-schema.md §5's MaxItemBytes
+		case len(p.Raw) > 16<<10: // larger than interaction-schema.md §5's MaxItemBytes
```

`TestCheckConformance_InteractionsTotalityIsProbed` walks the probe battery and asserts an
OVERSIZED body is among the shapes fed to `Interactions`. The only oversized probe is
`oversizedBody` at **64 KiB**, which is over both numbers, so `sawOversized` fires either way and
the assertion's outcome is unchanged — the edit keeps the comment's claim true, nothing more.
It is recorded here because the rule is that every edit to a pre-existing test is declared, not
only the ones that change an outcome.

## Teeth — two mutations, each reverted

| Mutation | Result |
|---|---|
| `concatText` returns only the first increment (a lossy merge that still *arrives*) | **FAILS** `TwoMaxTextIncrementsMergeAndAreNotDropped`: "the merged agent_message carries 4096 bytes of text; want 8192". The fence measures losslessness, not arrival — raising the cap without a working merge does not pass it. |
| `maxSteps` 64 → 32 | **FAILS** `APlanUpdateAtTheDocumentedMaximaFitsWhole` on both halves (`truncated = true`, "journalled steps holds 32 entries"). The fence pins §5's number, not the producer's. |

The RED above is itself the third mutation, run backwards: reverting `MaxItemBytes` to `8 << 10`
fails all three new fences.

## Gates

```
$ go build ./...                                  OK
$ go vet ./...                                    OK
$ go test ./internal/daemon/ -count=1 -race       ok   54.298s
$ go test ./internal/remotegw/ -count=1 -race     ok   33.972s
$ go test ./internal/protocol/ -count=1 -race     ok   19.534s   (GG-7 drift fences green)
$ go test ./internal/verify/ -count=1 -race       ok   35.104s   (B94 reachability green)
$ go test ./internal/adapter/... -count=1 -race   ok   (9 packages)
```

`internal/skeleton -race` is recorded with the verdict evidence in `a1-integration.md`, since both
rulings land in that package and one run covers them.
