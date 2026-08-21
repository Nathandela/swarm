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
- The quarantine is a timing bias (three orders of magnitude of margin on loopback,
  measured), not a proof; interleaving (b) explains why no client-side proof exists.
