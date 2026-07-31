# Phase B slice S2b — gateway outbound durability (PB-GW-7, PB-GW-8)

## PB-GW-7: the machine->phone numbers did not close (exit-criterion-fatal)

`renderDebounceWindow = 16ms` lets a live terminal peek emit ~62 snapshots/s, while the relay
caps appends at `MailboxAppendPerMin: 600` (10/s) per target on a **tumbling** minute window.
`RelaySink` allocated the seq *before* the append and returned on error, so every quota-refused
snapshot **permanently burned an outbound seq** — manufacturing gaps that PB-SYNC-1 must charge
to *both* journal and terminal, exhausting the resync budget. Journal, terminal and command
replies share one sink and one target, so a peek starved the journal too.

Measured in RED over a 2m30s peek: **7724 of 9524 appends refused**, 12.0 appends/s against a
budget of 8, **120 of 149 journal records lost**, and the phone saw manufactured gaps with no
relay failure at all. Without this, "types into a real session" has no live tail.

## PB-GW-8: the outbound journal cursor was not durable

`Gateway.cursor` was a bare in-memory `uint64` that nothing persisted or seeded, while **two
comments called it durable**. Every restart re-read from cursor 0 and re-appended the entire
journal at fresh seqs into the same capped mailbox — the fourth instance of a comment presuming
durability that did not exist.

## The forbidden remedy, and why the tests forbid it by construction

"A failed append never consumes a seq" is unsafe: the relay commits the item **before** replying
and `MailboxAppend` errors when the *response* read fails, so failure is not always pre-commit.
Reusing seq N for different plaintext means the phone accepts whichever seq-N envelope lands
first and stale-drops the other — silent journal/snapshot loss or reordering.

The test author proved the split is the only answer **by mutation**: patching to the forbidden
remedy makes `TestRelaySink_DefinitiveRefusalDoesNotBurnASeq` pass and
`TestRelaySink_DeliveryUnknownRetryIsVerbatimNotReSealed` fail with *"seq 2 carries TWO
DIFFERENT sealed envelopes"*. No blanket policy passes both.

Shipped: `AppendRefused` => the seq is reused (no gap). `AppendUnknown` => burned, or the
**identical sealed envelope** re-appended. Never new plaintext at a live seq.

> **SUPERSEDED by ADR-007 B127 (round 8).** The `AppendRefused` half of that split was
> **withdrawn and the classifier deleted**. It rested on the relay's own error code being
> evidence about the relay's own storage; a relay that STORES and then answers
> `quota_exceeded` got the seq reissued for different plaintext — the same silent loss this
> section calls forbidden, reached through the one door left open for it. The shipped rule is
> now uniform: **a seq handed to the appender is spent**, and reuse survives only where the
> frame provably never crossed the process boundary. The mutation argument recorded above
> still holds and now has a third arm, fenced in
> `internal/remotegw/relay_authored_refusal_test.go`.

## The brief was half wrong about sentinels, and the tests pin the conservative answer

The relay's error codes are only half distinguishable: `decodeError` maps `codeToErr` only, so
`bad_request`, `auth_failed` and `unsupported` decode to a bare error **indistinguishable by
`errors.Is` from a transport failure**. `ClassifyAppend` was therefore sentinel-only (quota,
not-authorized, revoked — all verified pre-commit in the relay handler) and conservatively
returned unknown otherwise. String-sniffing is explicitly forbidden by test, and that
prohibition outlives the classifier: B127 deleted `ClassifyAppend`, and
`TestAppendFailure_NoUnsentinelledErrorImpersonatesARefusal` carries the rule forward.
"All verified pre-commit in the relay handler" is where the defect hid — it is a property of
the HONEST relay's handler, and the relay is the adversary.

## Review found a blocking defect the tests could not see

**B1 — the coalescing stash was a single slot, not keyed by session.** One `RunTerminal` runs
per session, so N concurrent peeks shared one slot: session B's snapshot discarded session A's
held-back final frame, and **A's peek was stuck on a stale grid forever** — exactly what the
coalescing test forbids, which passed only because it drove a single session. The same clobber
broke the teardown blank between its append and its flush.

Writing the multi-session RED found **two further defects the review had not named**: with the
old forcing `Flush`, each idle-waking peek forced an append, so four sessions already ran at
**9.0/s (over the combined budget)**, and **three of four sessions were starved outright** —
whichever peek stashed last won every slot.

Fixed with per-session latest-wins over a FIFO queue and a **slot-obeying** `Flush`, so N
sessions do not buy N budgets. Single-session behaviour is bit-identical to before.

Behavioural note: because `Flush` no longer forces, a teardown blank still held when
`RunTerminal` returns ships on the next free slot rather than instantly — delayed, never lost.
In the common kill-switch case the peek has been idle past the window, so the blank releases on
that very call; if the peek resumes first, the fresh grid replaces it, which is the correct
outcome (no blank flash before the new frame).

## PB-GW-8 closed end to end

`Reserve` before the append, `Commit` after the ack, `Replay()` re-appending pending entries
**verbatim** oldest-first, `DeliveredCursor()` seeding the gateway on construction. Outbox
format is versioned JSON at `<stateDir>/remote/outbound-journal.outbox` using the existing
temp+fsync+rename+dir-fsync idiom, fail-closed on malformed or wrong-version.

The implementer flagged that `main.go` did not map the provisioned outbox into the service
config, so the production service still ran in-memory — the reviewer closed it with a test that
asserts **durability** (reserve, commit, reopen from the real path) rather than non-nil, because
`OpenOutbox("")` cannot fail and a dropped mapping would otherwise be silently satisfied by the
in-memory fallback.

## Recorded residuals

- **Command replies are outside the wrapper.** They append directly to the same target and
  quota, so the budget is "combined" only for journal + terminal. 8/s leaves ~120/min of
  headroom for replies. Matches the spec's letter; recorded.
- **`Snapshot` charges N+1 appends to one slot** (1 reconcile + N roster records), so a
  reconnect loop against a severing daemon can exceed the budget unthrottled.
- **`RelaySink.Err()` has no production caller** — replay failures are stashed but unreachable
  and unlogged. A persistently failing outbox surfaces indirectly via `Reserve` propagating
  through the reconnect loop.
- **`terminalIdleWake` shortens the terminal read deadline from 1s to 125ms**, making a
  pre-existing mid-frame desync ~8x more frequent. Bounded: the watcher re-dials and
  re-subscribes, so a transient peek blip, no corruption.
- **Out-of-order verbatim replay can manufacture a gap** (a delivery-unknown record replayed
  after a later successful frame). Not silent loss — the phone already saw `Gap: true`, so
  PB-SYNC-1 resyncs.
- **Roster records are deliberately exempt from outbox dedup**: they are current state re-sent
  at overlapping cursors, and suppressing them would leave a restarted phone with no roster.

## Gates

```
go test ./internal/remotegw/ ./cmd/swarm-remote/ -count=1        ok / ok
go test -race ./internal/remotegw/ ./internal/skeleton/          ok / ok
go build ./... && go vet (scoped)                                clean
```
Real-time tests verified at `-count=15` and `-race -count=5` with no flakiness. The pinned
tests are strictly stronger than required: both wrong answers are asserted against in each
delivery-unknown test, the refusal test drives a real relay through a real post-commit TCP cut
and verifies the item landed, and the budget test replays the accepted stream through a real
`crypto.MailboxReceiver` checking gaps, journal completeness and a liveness floor.

## Derivation

**MACHINE-READABLE** (ADR-007 B129). `DERIVED` means the fence was made to FAIL ON PURPOSE and
restored; a `DERIVED` row naming no mutation is malformed and counted NOT DERIVED. Every mutation
is to the CONNECTION in production code (B113); all reverted, package green at HEAD.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-GW-7 | DERIVED | four, all caught, and the two that matter most are B125's ninth axis. (a) **the refusal-lie restored** — `s.reuse = seq` added back after a failed `appendLocked` -> 12 subtests of `TestRelaySink_ARelayAuthoredRefusalNeverReissuesASeq` plus all 6 of `..._TheRelayCannotCauseLOSSWithoutCausingAGAP` fail; (b) **the known-false remedy** — reissue only when the error carries `ErrQuotaExceeded`/`ErrNotAuthorized`/`ErrRevoked`, i.e. the relay's own code trusted as evidence about the relay's own storage -> the SAME 18 fail, so the classifier exception cannot be smuggled back; (c) the admission window deleted from `CoalescingSink.release` -> `TestRelaySink_SustainedPeekStaysUnderAppendBudget`, `TestGatewayRunTerminal_CoalescedPeekShowsLatestGrid`, `TestCoalescingSink_MultiSessionStaysUnderCombinedBudget` fail; (d) all three pre-append `s.reuse = seq` assignments removed, so a frame that provably never left the process burns a seq -> `TestRelaySink_ASeqIsReissuedWhenTheFrameNeverLeftTheProcess` fails. `NewCoalescingSink` production-reachable at `internal/remotegw/service.go:196` |
| PB-GW-8 | DERIVED | four, all caught, and they separate the two halves the requirement says a local cursor write alone cannot cover. (a) `New`'s `CursorSource` seeding disabled -> `TestGateway_SeedsResumePointFromDurableCursor` fails ALONE — `TestGateway_RestartDoesNotReAppendDeliveredJournalRecords` survives it, because `RelaySink.Event` carries its own `resumed` guard, which is defence in depth rather than a redundant fence; (b) `NewRelaySink` no longer seeds `resumed: outbox.Cursor()` -> `TestGateway_RestartDoesNotReAppendDeliveredJournalRecords` fails; (c) a delivery-unknown record recovered by RE-SEALING at a fresh seq instead of replaying the reserved bytes verbatim -> `TestRelaySink_DeliveryUnknownRetryIsVerbatimNotReSealed` and `..._ARefusedOutboxFrameKeepsItsSeqThroughTheReservation` fail; (d) `outbox.Reserve` moved from BEFORE the append to after it -> those two plus `TestRelaySink_DeliveryUnknownNeverReusesASeqForDifferentPlaintext` (which drives a REAL relay behind a TCP proxy cut after commit, before the reply) and `..._OutboxReplayIsIdempotent` fail |
