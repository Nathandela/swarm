# Committee fix evidence: relay wait negotiation, reply-stream integrity, ack batching

Final-audit committee findings addressed (TASK A - RELAY WAIT): Opus H1, M1, M2, M3;
codex quota HIGH. Also resolves bead agents-tracker-zphd (an upgraded relay is
re-evaluated for free on reconnect).

Base: main at b688097. Date: 2026-08-21.

## What changed

1. **Capability negotiation replaces the blind probe** (M1, M2, M3).
   - `internal/remote/relay/server.go`: `serverCaps` now registers `"wait"`, so r_hello
     advertises the bounded mailbox_wait.
   - `internal/remote/relay/client.go`: `(*Client).Hello` surfaces the existing conn-level
     r_hello on an authenticated client.
   - `mobile/relay.go`: `App.run` calls `negotiateWaitSupport` on every successful dial,
     BEFORE the client is published and before `pollPresence` starts (closing the M3
     entry race: the hello is the connection's only in-flight exchange). The verdict is
     stored per connection: a hello without `"wait"` selects the poll outright (no probe,
     no dark window, no stray MsgError), and every reconnect re-derives it (zphd).
   - The timeout demotion survives ONLY as defense in depth for a relay that advertises
     `"wait"` and never answers a wait: it now demotes THIS connection to the poll
     in-place (`App.drain` runs the modes sequentially on one goroutine), instead of
     poisoning a process-wide sticky verdict. `waitUnknown` renamed `waitAdvertised`.

2. **Pump defense: an unsolicited frame cannot poison the reply stream** (H1).
   - `internal/remote/relay/client.go`: `Conn.pending` became an atomic written under
     `mu` (raised BEFORE the request write reaches the socket); the pump drops, with a
     log line, any clean non-wait frame that arrives while zero replies are owed.
     A stray that lands while an exchange IS in flight costs that one exchange a bounded
     error, and the stream re-synchronises instead of shifting one question back forever.
     Decode failures still forward (they end the connection and the reader learns why).

3. **AckBatcher in the phone wait drain** (codex quota HIGH).
   - `mobile/relay.go` `drainWait` constructs `transport.NewAckBatcher` (the same one the
     gateway command loop uses, `internal/remote/transport/follow.go`), records the
     committed high-water per page, and joins the batcher goroutine on exit. Metered acks
     are bounded at `MaxDrainAcksPerSec` (1/s) instead of one per delivered page; the
     poll fallback keeps its inline `flushAcks` (its ack doubles as its link probe).
   - The inline-ack "failed flush ends the generation" early exit is gone; the silence
     bound is now carried by the parked wait's own `waitTimeout` (see 4).

4. **Silent-relay fence honesty** (task item 4).
   - `mobile/conformance/silentrelay_test.go`: `silentBound` (40 s) now documents that it
     bounds the OUTBOUND verbs only (two 10 s `DefaultCallTimeout`s + margin). The
     connection-state test gets its own `silentStateBound` (60 s) composed from the bound
     that actually holds: `waitTimeout` (35 s) + demoted-poll `DefaultCallTimeout` (10 s)
     = 45 s worst case + margin. The old comment claimed 40 s absorbed two deadlines --
     true at 10 s, false at 35 s; the state test had been passing by timing accident.
   - `mobile/relay.go` drainWait's comment block no longer claims a timeout is the only
     possible evidence (hello/caps answers the question now); `internal/remote/transport/
     doc.go` and `mobile/pbnet6_drainreaders_test.go` doc strings updated to the
     caps-selected fallback and to the sequential (never concurrent) mode handoff.

## New fences

| Fence | File |
|---|---|
| r_hello advertises `"wait"` | `internal/remote/relay/committee_wait_test.go` `TestCommittee_HelloAdvertisesTheWaitCapability` |
| Unsolicited frames never shift reply pairing (H1 shape: old relay refusing a probed wait while an exchange is in flight; plus the idle-time stray) | `internal/remote/relay/committee_wait_test.go` `TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing` |
| Wait verdict is per connection; upgraded relay re-gains the wait tail (zphd); a non-advertising relay is NEVER probed | `mobile/committee_wait_test.go` `TestCommittee_WaitSupportIsRenegotiatedPerConnection` |
| Quiet-then-burst at 8 frames/s: acks ride the 1/s batcher and metered ops extrapolate under OpsPerMin (600) | `mobile/committee_wait_test.go` `TestCommittee_TheWaitDrainAcksOffTheDeliveryPath` |
| STRENGTHENED (not weakened): old-relay fallback selected by hello caps, zero mailbox_wait on the wire, zero dark window (delivery bound 15 s << one 35 s waitTimeout; the test no longer shortens waitTimeout) | `mobile/r9_waitfallback_test.go` `TestR9_AnOldRelayWithoutTheWaitCapabilitySelectsThePollFallback` |

`mobile/conformance/r9_waittail_test.go` (wait tail + zero-redelivery reconnect) is
untouched and green under the new semantics.

## TDD red (failing-first), verbatim

`go test ./internal/remote/relay/ -run 'TestCommittee_' -count=1` at b688097 + tests only:

```
--- FAIL: TestCommittee_HelloAdvertisesTheWaitCapability (0.11s)
    committee_wait_test.go:49: r_hello agreed caps = [mailbox], want "wait" among them: the relay serves mailbox_wait but never registered it as a capability, so a client cannot learn of the op except by probing it blind -- which against an old relay desynchronises the reply stream (H1)
--- FAIL: TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing (0.21s)
    committee_wait_test.go:208: MailboxAppend after an idle-time stray = (0, relay: bad_request), want (1, nil).
        A frame nobody is owed was queued as a pending reply, and the next exchange consumed it as its own answer -- the reply stream is shifted by one from here on (H1)
FAIL
```

`go test ./mobile/ -run 'TestCommittee_' -count=1`:

```
--- FAIL: TestCommittee_WaitSupportIsRenegotiatedPerConnection (17.36s)
    committee_wait_test.go:365: the phone never negotiated hello with the old relay; wait support must be derived from the advertised capability set, not probed blind
    committee_wait_test.go:369: the phone sent 2 mailbox_wait ops to a relay whose hello did not advertise "wait"; ... desynchronises the reply stream (H1)
    committee_wait_test.go:391: no mailbox_wait crossed the proxy after the relay upgrade: the poll verdict stuck to the process, so the upgraded relay's wait tail is never used until the app is killed (bead agents-tracker-zphd)
--- FAIL: TestCommittee_TheWaitDrainAcksOffTheDeliveryPath (8.27s)
    committee_wait_test.go:455: the drain issued 20 mailbox_ack ops in 6.1s (one per delivered page); batched acking (transport.AckBatcher) is bounded by 8 in this window. ...
FAIL
```

`go test ./mobile/ -run 'TestR9_AnOldRelayWithoutTheWaitCapabilitySelectsThePollFallback' -count=1`:

```
--- FAIL: TestR9_AnOldRelayWithoutTheWaitCapabilitySelectsThePollFallback (15.22s)
    r9_waitfallback_test.go:347: the journal event never reached the phone within 15 s; a hello that does not advertise "wait" must drop the drain straight into the 500 ms compatibility poll -- with no blind-probe dark window ahead of delivery -- and delivery must survive it
FAIL
```

Each red is the defect the committee named, not a syntax error: the blind 35 s probe, the
process-sticky verdict, the shifted reply stream, the per-page acks.

## Mutation proofs (cp backup -> mutate -> fence fails -> cp restore -> cmp byte-identical)

| # | Mutant | Fence that failed | Restore |
|---|---|---|---|
| 1 | `"wait"` removed from `serverCaps` | `TestCommittee_HelloAdvertisesTheWaitCapability`: `agreed caps = [mailbox]` | `cmp` OK |
| 2 | pump drop branch deleted (client.go) | `TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing`: idle stray consumed as append #1's reply | `cmp` OK |
| 3 | `run()` skips renegotiation once unsupported (process-sticky) | `TestCommittee_WaitSupportIsRenegotiatedPerConnection`: no mailbox_wait after the upgrade | `cmp` OK |
| 4 | `acks.Record` replaced by inline `flushAcks` per page | `TestCommittee_TheWaitDrainAcksOffTheDeliveryPath`: 20 acks in 6.2 s vs bound 8 | `cmp` OK |
| 5 | `negotiateWaitSupport` ignores caps, always `waitAdvertised` (blind-probe world) | `TestR9_AnOldRelayWithoutTheWaitCapabilitySelectsThePollFallback`: delivery missed the 15 s no-dark-window bound | `cmp` OK |

All restores verified byte-identical with `cmp` against the pre-mutation backup.

## Gates (final tree)

- `go build ./...` -- OK
- `go vet ./...` -- OK
- `golangci-lint run` -- 0 issues
- `go test -race -count=1`:
  - `internal/remote/relay` -- ok (100.2 s)
  - `internal/remote/transport` -- ok (5.5 s)
  - `mobile` -- ok (42.6 s)
  - `mobile/conformance` -- ok (223.3 s, includes both silent-relay fences at the new bounds)
- No test-spawned processes left behind (`ps -axo pid,ppid` shows no orphaned relay/test
  binaries with ppid 1).

## Residuals

- The MACHINE hop's gateway (`internal/remotegw` CommandBridge) still issues
  `MailboxWait` without consulting hello caps; it is machine-side, retries through its
  own bounded loop, and was outside this task's footprint. If a pre-wait relay must be
  supported there too, the same `Client.Hello` seam now exists for it.
  [RESOLVED, ROUND 4: the gateway now negotiates per connection and falls back to the
  MailboxRead poll -- see the round-4 section; bead agents-tracker-10ar.]
- A stray frame that arrives while a reply IS legitimately owed remains indistinguishable
  from that reply (in-order stream, no correlation ids on ordinary exchanges): the cost
  is one bounded wrong-error on that single exchange, after which the pump's
  pending-count drop re-synchronises the stream. Eliminating even that would need
  request ids on every op -- a wire change the committee did not ask for.
- ADR-007's r_hello prose does not yet enumerate `"wait"` in the capability list; doc-only,
  left to the docs owner (the playbook/spec files were being edited by another agent).
- `drainPoll` (compatibility fallback only) keeps its inline one-per-page acks; against
  an old relay at 500 ms cadence the op rate is bounded by the poll interval itself.

## Round 3: the drop rule's own race (codex round-2 finding 3; Opus round-2 findings 4, 5, nit 6)

Base: main at 5856a4e. Date: 2026-08-21.

### The reproduced defect

`go test ./internal/remote/relay -run 'TestCommittee_(HelloAdvertisesTheWaitCapability|AnUnsolicitedFrameDoesNotShiftReplyPairing)$' -count=50` at 5856a4e FAILED (orchestrator-reproduced; re-reproduced here):

```
--- FAIL: TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing (0.21s)
    committee_wait_test.go:238: MailboxAppend #3 = (2, <nil>), want (3, nil).
```

The round-2 drop rule sampled `c.pending.Load() == 0` and then performed a potentially
BLOCKING send into `c.frames` (capacity 1), while roundtrip independently decremented
`pending` when it consumed a frame. Check and enqueue were not atomic, so the check
could go stale between them.

### The interleavings, written down

Notation: the peer answers append #2 with a stale MsgError (the parked wait's refusal,
H1 shape) immediately followed by the real MsgOK{cursor 2}, back to back on the
in-order stream. "W" is a request write, "P" the pump, "C" the consuming roundtrip.

**(a) Pump-ahead — the round-2 TOCTOU, the reproduced failure.**

1. W: append #2 raises the count to 1, writes.
2. P: reads the stray MsgError; count == 1, enqueue (channel now FULL).
3. P: reads MsgOK{2}; samples count — still 1, C has not run — check passes, P
   commits to a blocking send and PARKS on the full channel.
4. C: consumes the stray as append #2's answer, decrements to 0, returns the
   bounded casualty.
5. P's parked send completes: MsgOK{2} enters the queue while the count is 0 —
   append #3 consumes it as its own answer (cursor 2), and every later exchange
   answers the question before it, for the life of the connection.

Fix: the accounting is pump-owned. `owed` is raised by roundtrip before its write
(under `owedMu`) and lowered ONLY by the pump, in the same critical section as the
check and a non-blocking claim on the queue slot. When the slot is taken, the blocking
send is entered with `owed` already lowered, and the pump is one goroutine, so the next
frame it reads is judged against a truthful count. In (a), step 3 now finds owed == 0
(the stray's enqueue lowered it at step 2) and DROPS MsgOK{2} deterministically.

**(b) Consumer-ahead — the complementary window the suggested design does not close.**

1. W: append #2 raises owed to 1, writes.
2. P: reads the stray MsgError; owed 1 -> 0, enqueue.
3. C: consumes the stray, returns the casualty; the caller immediately issues
   append #3: owed 0 -> 1, write. (MsgOK{2} is STILL IN FLIGHT — two separate
   TCP segments; the pump is parked in a netpoll wait.)
4. P: reads MsgOK{2}; owed == 1 — indistinguishable from append #3's own answer —
   enqueue; append #3 gets (2, nil).

NO client-side accounting can close (b): whether a frame was received by the kernel
before or after the client's own write is not observable through a lagging reader, and
the wire carries no correlation ids on ordinary exchanges. What the client does control
is WHEN the next request is written. Every stray in the H1 family is an MsgError (a
pre-wait relay's refusal of an unknown op), so roundtrip now holds the exchange lock
for `strayQuarantine` (5 ms) after adopting an MsgError reply — the one moment a
violation may just have displaced a frame. For that whole window owed is provably 0
(this frame's decrement already happened; `mu` blocks any new writer), so whatever the
violation displaced arrives owed to nobody and is dropped. A genuine refusal pays 5 ms
on a path that already failed. Empirically (b) dominated on this machine: the fix
WITHOUT the quarantine failed the round-2 SHAPE 2 five out of five runs; with it,
200/200 green. Mutant 3 below pins the quarantine as load-bearing.

[CORRECTED, ROUND 4 -- "owed is provably 0 for the whole window" was NOT true of the
round-3 code this paragraph signed off (Opus F4/F8): an ABANDONED exchange's credit
stayed parked in `owed`, so a quarantine entered while such a credit was outstanding
ran against owed >= 1 and could admit the displaced frame mid-window. The claim becomes
true only under the round-4 ledger, where abandonment moves the credit out of `owed`
into the pump's `discard` and owed counts the live exchange alone -- and the round-4
free-drop rule supplies the second half: a frame arriving at owed == 0 spends no
discard credit either, so the quarantine cannot leak an abandoned credit onto the
displaced frame. The client.go quarantine comment now states exactly this.]

**(c) Abandoned reply, then a stray (the skip rewrite's interplay).**

1. W: append #1 raises owed to 1, writes; its reply is delayed; the caller times out
   and abandons — roundtrip increments its own `skip` (guarded by `mu`, driven by
   what roundtrip READS, never by the pump's counter).
2. P: the late reply arrives; owed 1 -> 0, enqueue. It sits in the queue.
3. W: append #2 raises owed to 1, writes; reads the late reply, `skip` 1 -> 0,
   discards it; reads its own reply (owed 1 -> 0 at the pump); adopts it. Correct.
   Had a stray stolen the late reply's slot at step 2, the late reply itself would
   have hit owed == 0 and been dropped — the stray and the abandoned reply
   annihilate, and no live exchange is touched.

   [CORRECTED, ROUND 4 — the annihilation claim above is FALSE of the round-3 code
   (Opus F3, reproduced 5/5). A stray at step 2 does not annihilate with the
   abandoned reply: it spends the abandoned credit still parked in `owed` and is
   ENQUEUED; append #2's `skip` then spends itself on that same stray; and the late
   reply arrives against append #2's own fresh credit and is adopted as its answer —
   wrong data, nil error. One abandonment had minted TWO independently-spendable
   credits, both consumed by one frame. Under the round-4 single ledger the truth is:
   the idle stray is an unsolicited FREE drop (owed is 0 — abandonment moved the
   credit into the pump's `discard`), the late reply is dropped by the discard spend
   inside append #2's window, and append #2 adopts its own reply. The stray and the
   abandoned reply still never touch a live exchange — but through one credit spent
   on one frame, not through an annihilation that never happened. Permanent probe:
   TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange.]

`TestCommitteeR3_ALateReplyToAnAbandonedCallNeverAnswersTheNextOne` pins (c) — the
first test to cover the abandoned-reply pairing at all (the calldeadline files bound
the deadline but never fed a LATE reply back).

### Deviation from the suggested design, justified

The suggested round-3 design tears the connection down when a frame arrives at
owed == 0. That is IRRECONCILABLE with the standing round-2 fence, which may not be
weakened: SHAPE 2 of `TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing`
requires append #3 to succeed with cursor 3 AFTER the stray — under teardown the
displaced MsgOK{2} kills the connection in interleaving (a) (append #3 gets
ErrConnClosed), and in interleaving (b) the teardown never fires at the right moment
anyway (the displaced frame is judged at owed == 1). Both windows fail the committee's
own acceptance test deterministically. The drop is retained instead, and it is now
provably safe: with the pump the only decrementer and the check/decrement/claim fused
under one lock, a frame judged against owed == 0 answers nothing that was ever
written, so dropping it can displace no real reply. Teardown would also hand any peer
able to volunteer one frame a free reconnect lever (a self-DoS amplifier), where the
drop is bounded, counted and logged.

FOR THE RECORD (round 4): the commit message of d15c269, which shipped this round,
states "a frame owed to nobody tears the connection down instead of guessing" — a
teardown this section argues against and the code deliberately does NOT do (it drops,
bounded, counted and logged); the commit message's sentence is false of the tree it
describes and this file is the corrected record.

### The other round-3 items

- **Opus finding 5 (peer-controlled log amplification):** the drop-path `log.Printf`
  is now `noteUnsolicitedDrop`: at most one line per second, carrying the running
  per-connection drop count so suppressed drops stay visible.
  `TestCommitteeR3_TheUnsolicitedDropLogIsRateLimited` (50 strays, at most 5 lines,
  and the append after the burst still gets cursor 1) was RED against the old code
  (50 lines) before the fix.
- **Opus nit 6 (caps drift):** the phone's advertised set is now the single var
  `helloRequestCaps` (mobile/relay.go), and
  `TestCommitteeR3_PhoneHelloCapsAreServedByTheRelay` (mobile) dials the SHIPPED
  relay and asserts every requested cap is granted — the cross-package fence between
  `helloRequestCaps` and relay's `serverCaps`.
- **Opus finding 4, recorded honestly:** r_hello runs pre-auth and unsigned, so a
  hostile relay (or any on-path party before TLS pinning applies) can strip "wait"
  from the agreed set and pin a phone to the 500 ms compatibility poll for the life
  of each connection — a denial-of-quality only: no authority is gained, no data is
  exposed, and every reconnect renegotiates.

### TDD red (failing-first), verbatim

New round-3 tests against the UNMODIFIED 5856a4e client:

- `TestCommitteeR3_AStrayMidExchangeThenABurstKeepsEveryPairing` -count=50: **40 of 50
  runs FAILED** (`burst MailboxAppend #3 = (2, <nil>), want (3, nil)`). The exact codex
  interleaving cannot be forced fully deterministically from outside the client — the
  race is between the pump goroutine's netpoll wakeup and the consumer's return path,
  and no test seam exists (adding one to production code was rejected as scaffolding)
  — so the counts carry the evidence, as the task allows. The stray and the displaced
  reply ARE written back to back (the adjacency the TOCTOU needs); post-fix the
  outcome is deterministic by construction (pump-owned count) and 200/200 green.
- `TestCommitteeR3_TheUnsolicitedDropLogIsRateLimited` -count=1: FAILED (`50 strays
  produced 50 log lines`).
- `TestCommitteeR3_ALateReplyToAnAbandonedCallNeverAnswersTheNextOne`: green pre-fix
  by design — it pins the pairing semantics the rewrite had to preserve, and reddens
  under mutant 5.

### Acceptance bar (re-runnable)

- `go test ./internal/remote/relay -run 'TestCommittee_(HelloAdvertisesTheWaitCapability|AnUnsolicitedFrameDoesNotShiftReplyPairing)$' -count=200` — **ok** (50.3 s)
- `go test ./internal/remote/relay -run 'TestCommitteeR3_' -count=200` — **ok** (225.6 s)
- `go test ./internal/remote/relay -race -run 'TestCommittee_(HelloAdvertisesTheWaitCapability|AnUnsolicitedFrameDoesNotShiftReplyPairing)$|TestCommitteeR3_' -count=50` — **ok** (74.0 s)

### Mutation proofs (cp backup -> mutate -> fence fails -> cp restore -> cmp byte-identical)

| # | Mutant | Fence that failed | Restore |
|---|---|---|---|
| 1 | whole fix reverted (round-2 client.go restored wholesale) | round-2 SHAPE 2 + R3 burst: 33 of 40 runs FAILED (-count=20 each) | `cmp` OK |
| 2 | the owed == 0 drop branch deleted from the pump | round-2 SHAPE 1: idle stray consumed as append #1's reply (`(0, relay: bad_request)`) | `cmp` OK |
| 3 | `strayQuarantine` zeroed (interleaving (b) reopened) | round-2 SHAPE 2 + R3 burst: 29 of 40 runs FAILED (-count=20 each) | `cmp` OK |
| 4 | rate-limit early-return deleted from `noteUnsolicitedDrop` | `TestCommitteeR3_TheUnsolicitedDropLogIsRateLimited` | `cmp` OK |
| 5 | abandoned-reply `c.skip++` deleted | `TestCommitteeR3_ALateReplyToAnAbandonedCallNeverAnswersTheNextOne`: late cursor 1 adopted as append #2's answer | `cmp` OK |
| 6 | `"bogus"` appended to `helloRequestCaps` | `TestCommitteeR3_PhoneHelloCapsAreServedByTheRelay` | `cmp` OK |

### Residuals (round 3)

- A stray that is NOT an MsgError (e.g. a duplicated MsgOK) consumed while a reply is
  legitimately owed still costs one bounded wrong-value casualty, and under sustained
  pipelined load over a slow link a displaced reply can lag beyond the quarantine and
  cost a second before the owed count re-synchronises the stream at the next gap. Full
  closure needs correlation ids on ordinary exchanges — a wire change the committee
  has twice declined to ask for. The SHIPPED trigger (the blind wait probe) remains
  eliminated by negotiation; everything here is defense in depth against a violating
  peer.

  [CORRECTED, ROUND 4 — "cost a second" understated the round-3 exposure and is
  restated exactly for the round-4 ledger (Opus F8). The true bound now: a non-MsgError
  stray adopted while a reply is owed costs that exchange one wrong-value casualty, and
  its displaced real reply costs AT MOST ONE MORE exchange — delivered as the next
  exchange's answer when it lands inside that window with no discard pending, spent
  against a pending discard credit when one is (each spend is one credit, never more),
  or free-dropped at the first owed == 0 gap, which is where the stream provably
  re-synchronises. Additionally, when an ABANDONED exchange's credit is outstanding, an
  idle-time stray leaves that credit un-spent (the free-drop rule the round-4 probe
  forces), and an honest straggler landing at idle leaks its credit the same way: the
  leaked credit converts at most one future exchange into a spurious timeout, after
  which abandonReply's suppressed re-mint retires it — bounded, never cascading
  (TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection).]
- The quarantine is a timing bias (three orders of magnitude of margin on loopback,
  measured), not a proof; interleaving (b) explains why no client-side proof exists.

## Round 4: one ledger for abandoned replies; CloseNow on the abandoning teardown; the gateway negotiates; load-tolerant PBBIND6 (Opus F3/F4/F6/F7/F8; codex round-3 blocker 1 / bead agents-tracker-10ar)

Base: main at d15c269 (uncommitted round-4 work tree). Date: 2026-08-21.

### F3: the two abandonment ledgers unified

`internal/remote/relay/client.go`. Round 3 left an abandoned exchange's reply owed in
TWO independent books -- the credit parked in `owed` (pump-side) and a roundtrip-side
`skip` -- and the two could spend on DIFFERENT frames; interleaving (c) above claimed
they annihilate, and the correction inserted there records why they do not. The round-4
design is the committee's: on abandon the credit moves from `owed` into a pump-side
`discard` counter (`abandonReply`, which first drains the queue in case the reply was
already routed); the pump judges every clean frame in one critical section -- owed == 0
is an unsolicited FREE drop spending nothing, otherwise pending `discard` is spent
BEFORE a frame may be delivered (in-order streams put every abandoned straggler ahead
of the live reply, which is what makes that spend the exact FIFO attribution);
`roundtrip.skip` is gone and roundtrip reads exactly one frame.

ONE DELIBERATE ADDITION TO THE PRESCRIPTION, argued in the `discard` field doc: the
probe's required outcome forces the idle-time free drop to leave discard credits
un-spent, so an HONEST straggler landing at idle leaks its credit, and unmitigated that
leak CASCADES (the eaten live reply times out, re-mints, eats the next -- an honest-relay
permanent wedge). `abandonReply` therefore suppresses the re-mint when a discard was
already spent inside the abandoning exchange's own window (`spent`): each leaked credit
costs at most ONE bounded spurious timeout and the connection recovers. Mutant 3 pins
the suppression as load-bearing.

[CORRECTED, ROUND 4 FIX WAVE -- the paragraph above originally continued "No
client-side rule does better", and the suppression it describes was UNCONDITIONAL on
`spent` alone. Both were wrong, and the unconditional form was a live defect in the
honest DOUBLE-SLOW world (two back-to-back exchanges timing out against one relay
stall, no idle arrival anywhere): there the credit spent inside the abandoning
exchange's window paid for its PREDECESSOR's straggler, its own reply was still
genuinely in flight, and suppressing the mint handed that reply to the successor as
wrong data with a nil error -- a one-back shift persisting through every back-to-back
successor until the first owed == 0 idle gap, so under a busy pipeline the run of
wrong answers was unbounded, not "at most ONE". That was also a regression against the
round-3 skip ledger, which in this exact world returned honest ErrTimeouts throughout.
A client-side rule DOES do better, because the two worlds are distinguishable by
evidence already held: the idle-leak world (where suppression is correct) necessarily
contains a clean-frame free drop at owed == 0 while a discard credit was outstanding,
and the double-slow world contains none. The suppression is now conditioned on exactly
that observation (`idleLeak` set at such a free drop, transferred onto the spending
window as `spentLeaked`, reset per window): the double-slow abandonment mints its
credit and the successor discards the straggler FIFO-exactly, while both round-4
probes keep their outcomes -- the idle-stray probe sets the flag but its live exchange
succeeds, and the no-wedge probe's suppression still fires off the leaked straggler's
own free drop. What remains genuinely undecidable without wire correlation ids is only
WHICH outstanding credit an idle drop leaked when several coexist (the taint is one
boolean per epoch, not one flag per credit), and hostile-stray vs honest-straggler at
the moment of the idle drop itself.
`TestCommitteeR4_AnHonestDoubleTimeoutKeepsFIFOAttributionForTheSuccessor` is the
permanent fence; fix-wave mutants A-C pin both directions.]

### F4/F8: the evidence corrections

Made inline above, each marked `[CORRECTED, ROUND 4 ...]`: interleaving (c)'s
annihilation claim (false of the round-3 code; the corrected mechanics stated); the
quarantine's "owed provably zero for the whole window" claim (true only under the
round-4 ledger; the client.go comment now derives it rather than asserting it); the
non-MsgError stray bound ("cost a second" restated as the exact per-stray and
per-leaked-credit bounds). The round-3 section also now carries the round-4 FOR THE
RECORD sentence: d15c269's commit message says "a frame owed to nobody tears the
connection down instead of guessing" -- a teardown that section argues against and the
code deliberately does not do; the drop-quarantine design is what shipped.

### F6: the abandoning teardown is CloseNow

`mobile/relay.go` App.run's generation teardown now calls the new
`relay.(*Client).CloseNow` instead of the graceful `Close`. Measured first (the round-2
CloseNow doc's "pays that timeout in full, every time" is NOT the whole truth): the
graceful close is cheap while the pump is parked in a read (cancelling the read
hard-closes the socket via the websocket library's timeout loop) and cheap on a dead
socket; it costs its full five seconds exactly when the pump has EXITED and the peer is
SILENT -- the state a malformed frame leaves behind on a link gone dark, and reachable
on the ordinary teardown path as a scheduling race besides. The redial after a dead
link, and Stop() on the facade's serial command lane (backgrounding), both ran through
that close. The graceful Close keeps its callers where the goodbye is the point: the
pairing probe's finished exchange, App.Close's machines manager and push gateway
(process exit), the gateway sidecar's own shutdown.

### Codex round-3 blocker 1 / bead agents-tracker-10ar: the gateway negotiates -- RESOLVED

`internal/remotegw/command_loop.go`. `CommandBridge.Run` now negotiates capabilities at
entry -- once per connection, since a Service is one relay generation -- through the
optional `CapabilityHello` seam (*relay.Client pinned; hello-less unit fakes keep the
wait their Mailbox implements). A hello that does not advertise "wait" selects
`runPoll`: MailboxRead at the 500 ms compatibility cadence (the transport doc's
documented fallback, the phone drainPoll's exact shape -- immediate next read only on
cursor PROGRESS), with a successful read counting as the link-progress evidence
`Progressed` resets the reconnect backoff on. The eternal refused-wait loop -- a blind
mailbox_wait whose in-order MsgError refusal the pump free-drops, so every wait ends as
a swallowed 35 s timeout and commands never flow -- is gone. `gatewayHelloCaps`
(mailbox, push, wait) is the machine-hop sibling of the phone's `helloRequestCaps`,
fenced against the shipped relay.

### F7: the PBBIND6 flood wait is load-tolerant

`mobile/conformance/harness_test.go` gains `eventuallyFor` (caller-declared ceiling;
`eventually` delegates at the old 5 s); `robustness_test.go`'s flood-arrival wait --
required context for every assertion after it -- now runs at a 60 s ceiling. The
assertion is unchanged and exact (the flood must FULLY arrive: NextCursor == emitted);
only the wall-clock allowance scales, and the early exit keeps healthy runs at their
old speed. Mutant 6 shows the ceiling still binds.

### New fences

| Fence | File |
|---|---|
| The committee's F3 probe, permanent: idle stray + abandoned late reply never answers a live exchange (want (2, nil)) | `internal/remote/relay/committee_r4_ledger_test.go` `TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange` |
| One idle-arriving honest straggler never wedges the connection (the suppressed re-mint; at most one bounded casualty) | same file, `TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection` |
| An honest double timeout never hands the abandoned reply to its successor (the conditioned suppression; fix wave) | same file, `TestCommitteeR4_AnHonestDoubleTimeoutKeepsFIFOAttributionForTheSuccessor` |
| Redial after a dead-pump link death is on the backoff schedule, not 5 s late | `mobile/committee_r4_closenow_test.go` `TestCommitteeR4_RedialDoesNotWaitForTheDeadConnectionsCloseHandshake` |
| Background (Stop) then immediate foreground: Stop bounded at 2.5 s on the serial lane | same file, `TestCommitteeR4_BackgroundForegroundResumeDoesNotPayTheCloseHandshake` |
| A non-advertising relay gets the MailboxRead poll: command delivered, ZERO mailbox_wait issued, hello consulted | `internal/remotegw/committee_r4_hello_test.go` `TestCommitteeR4_ANonAdvertisingRelayGetsThePollFallbackNotAnEternalRefusedWaitLoop` |
| An advertising relay keeps the wait tail, and the verdict came from a real hello | same file, `TestCommitteeR4_AnAdvertisingRelayKeepsTheWaitTail` |
| Every gatewayHelloCaps entry is granted by the shipped relay (drift fence; the fake hello intersects like the real one) | same file, `TestCommitteeR4_GatewayHelloCapsAreServedByTheShippedRelay` |

### TDD red (failing-first), verbatim, against the unmodified d15c269 tree + tests only

`go test ./internal/remote/relay/ -run 'TestCommitteeR4_' -count=1 -v`:

```
=== RUN   TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange
    committee_r4_ledger_test.go:85: live MailboxAppend = (1, <nil>), want (2, nil).
--- FAIL: TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange (1.54s)
=== RUN   TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection
--- PASS: TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection (1.12s)
```

The probe reproduces the committee's exact (1, <nil>) corruption. The no-wedge test is
GREEN pre-fix BY DESIGN, like round 3's late-reply test: it pins the recovery semantics
the round-3 skip already had and the round-4 rewrite must preserve; mutant 3 reddens it.

`go test ./mobile/ -run 'TestCommitteeR4_RedialDoesNotWaitForTheDeadConnectionsCloseHandshake' -count=6`
at d15c269: **2 of 6 runs FAILED** ("the redial arrived 5.42s / 5.44s after the link
died ..."). Whether the graceful close pays its five seconds on a given run depends on
whether the websocket timeout loop still holds a registered read/write context at close
time -- a scheduling race no test seam reaches from outside the library -- so the counts
carry the evidence (the round-3 precedent); an instrumented run showed the teardown
close itself at 5.004 s. Post-fix the outcome is deterministic by construction (CloseNow
does no network wait): 6/6 green, and 5/6 runs FAILED under mutant 4 (the revert).
The Stop/Start lifecycle bound is green pre-fix on a responsive loopback relay (the
close is cheap in the parked-pump state) and is kept as the permanent lane bound the
finding names.

`go test ./internal/remotegw/ -run 'TestCommitteeR4_' -count=1 -v` (with only the dead
`gatewayHelloCaps` var added so the package compiles):

```
--- FAIL: TestCommitteeR4_ANonAdvertisingRelayGetsThePollFallbackNotAnEternalRefusedWaitLoop (10.03s)
    committee_r4_hello_test.go:164: the command never reached the daemon through a relay that does not advertise "wait": the loop is parked in the eternal refused-wait cycle instead of the documented MailboxRead poll fallback (codex round-3 blocker 1, bead agents-tracker-10ar)
--- FAIL: TestCommitteeR4_AnAdvertisingRelayKeepsTheWaitTail (0.02s)
    committee_r4_hello_test.go:206: the bridge never negotiated hello with a wait-advertising relay; the verdict must come from the capability exchange, not from a default
--- PASS: TestCommitteeR4_GatewayHelloCapsAreServedByTheShippedRelay (0.08s)
```

F7 is a test-scaffolding change; its failing direction is the mutation proof (mutant 6).

### Mutation proofs (cp backup -> mutate -> fence fails -> cp restore -> cmp byte-identical)

| # | Mutant | Fence that failed | Restore |
|---|---|---|---|
| 1 | `c.discard++` deleted from abandonReply (abandon mints nothing) | R3 late-reply: `#2 = (1, <nil>), want (2, nil)`; R4 probe: `(1, <nil>), want (2, nil)` | `cmp` OK |
| 2 | idle drop spends discard (`owed == 0 && discard == 0` guards the free drop) | R4 probe: `(1, <nil>), want (2, nil)` -- the stray consumed the credit owed to the straggler | `cmp` OK |
| 3 | suppressed re-mint deleted (always mint on abandon) | R4 no-wedge: `#3 = (0, ErrTimeout), want (3, nil)` -- the leaked credit cascaded | `cmp` OK |
| 4 | mobile teardown reverted CloseNow -> Close | R4 redial fence: 5 of 6 runs failed (~5.4 s redial) | `cmp` OK |
| 5 | negotiateWait ignores caps (always the wait arm) | both gateway arms: no poll fallback; no hello consulted | `cmp` OK |
| 6 | robustness flood ceiling 60 s -> 1 ms | `TestPBBIND6_SlowCallback...`: timed out after 1ms | `cmp` OK |
| A (fix wave) | suppression made unconditional again (`spent > 0` without `spentLeaked`) | double-slow fence: `#3 = (2, <nil>), want (3, nil)` -- the predecessor's reply adopted with a nil error | `cmp` OK |
| B (fix wave) | suppressed re-mint deleted (always mint on abandon; re-run of mutant 3 against the conditioned form) | R4 no-wedge: `#3 = (0, ErrTimeout), want (3, nil)` -- the leaked credit cascaded | `cmp` OK |
| C (fix wave) | `idleLeak` never set (the free drop stays blind) | R4 no-wedge: `#3 = (0, ErrTimeout), want (3, nil)` -- suppression never licensed, cascade back | `cmp` OK |

Fix-wave TDD red, verbatim, against the pre-fix ledger (the failing-first evidence for
the double-slow fence):

```
--- FAIL: TestCommitteeR4_AnHonestDoubleTimeoutKeepsFIFOAttributionForTheSuccessor (1.55s)
    committee_r4_ledger_test.go:211: MailboxAppend #3 = (2, <nil>), want (3, nil).
        #2's abandonment suppressed its re-mint because a discard credit was spent inside its window -- but that credit was spent on #1's straggler, not on any idle leak, so #2's genuinely in-flight reply was left unprotected and adopted by its successor with a nil error (honest double-slow corner)
```

### Acceptance bar (re-runnable) and gates

- `go test ./internal/remote/relay -run 'TestCommittee_(HelloAdvertisesTheWaitCapability|AnUnsolicitedFrameDoesNotShiftReplyPairing)$|TestCommitteeR3_|TestCommitteeR4_' -count=200` -- **ok** (840.6 s)
- same selector `-race -count=50` -- **ok** (214.6 s)
- `go build ./...` -- OK
- `go vet ./...` -- OK (0 issues)
- `golangci-lint run` -- 0 issues
- `go test -race -count=1` on the touched packages -- internal/remote/relay ok (128.4 s); internal/remotegw ok (33.4 s, re-run ok 30.0 s after the poll-progress refinement); mobile ok (47.7 s); mobile/conformance ok (240.3 s)
- No test-spawned processes left behind -- one STALE `swarm shim` from an earlier e2e run (ppid 1, 1 h 26 m old, its /tmp workspace already deleted) was reaped; after the bar, no orphaned relay/test binaries remain

### Residuals (round 4)

- The free-drop-at-idle rule the probe forces has the price the discard doc states: an
  honest straggler landing while the connection is idle costs at most one spurious
  timeout of a later exchange (recovered by the suppressed re-mint, never cascading),
  where the round-3 skip handled that one shape exactly but paid for it with F3's
  corruption. The trade is deliberate and fenced from both sides (probe + no-wedge).
- [CLOSED, ROUND 4 FIX WAVE] The honest double-slow corner this bullet used to record
  as a bounded residual was neither bounded nor open-by-necessity: unconditional
  suppression left the abandoned reply uncredited and the one-back shift persisted
  until the first idle gap, and the idle-free-drop observation decides the corner
  client-side (see the F3 correction above). What remains is only the epoch
  granularity of the taint: with SEVERAL discard credits outstanding across one
  observed idle leak, suppression may still fire for a window whose spent credit was
  not the leaked one -- one boolean per epoch, closable only by correlation ids.
- The mobile redial fence's pre-fix red is probabilistic (2/6 at HEAD, 5/6 under the
  revert mutant); post-fix it is deterministic. The race lives inside the websocket
  library's close path, out of reach of a deterministic external seam.
- CloseNow means the relay sees an abrupt drop instead of a close frame on
  backgrounding; the relay's presence logic keys on connection death plus the
  silent-push bound, not on close-frame receipt, so no presence behavior changes.
- The gateway poll fallback keeps PollOnce's inline per-batch ack; at the 500 ms
  cadence the metered op rate is bounded by the interval itself (the same argument as
  the phone's drainPoll, recorded in round 1's residuals).

Bead agents-tracker-10ar resolved by this round.
