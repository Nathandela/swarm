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
