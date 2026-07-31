# Phase B slice S6 — transport resilience and TLS (PB-NET-2, 3, 4, 6, 7)

Client-side resilience over the existing relay WebSocket client. PB-NET-1 (facade integration)
and PB-NET-5 (the low-latency input path) are **not** in this slice — PB-NET-5 was split out to
S6b when the S6 test author noticed the manifest assigned it here while the brief excluded it.

## Shape: two send methods, not one

The load-bearing design decision. `SendLive` never queues and never replays; `SendOp` queues
bounded at 64 and refuses the 65th. That makes ADR-007 D7's "input is live-only, resolving to
delivery unknown / not sent" **structural** rather than a policy flag a later change could
invert. `Drain` reads one bounded page per call and returns `hasMore`, so hostile pagination
cannot wedge the transport by construction.

## A test that would have been vacuous

PB-NET-3 asserts no plaintext ever reaches the wire. RFC 6455 requires clients to XOR-mask
every frame, so a naive plaintext search over raw TCP bytes finds nothing **whether or not the
transport leaked**. The harness unmasks frames and carries a negative-control test that fails
if the oracle ever goes blind; the author validated the whole harness against the real relay
before writing any assertion.

## The review found a real security hole, with a working reproduction

**B1 — a release binary admitted CLEARTEXT via HTTP redirect.** `Security.resolve` inspected
only the URL string it was handed, but `coder/websocket` installs a `CheckRedirect` that
*follows* and rewrites `wss->https`. Nothing re-validated the new URL. Proven end to end, using
the strongest available policy with a pin set:

```
DOWNGRADE: DialSecure(wss://) followed a 302 to http://127.0.0.1:53016
and completed the relay-auth handshake in CLEARTEXT
```

A relay — or anyone who compromises one, or a CDN misconfiguration — answers the upgrade with
`302 -> ws://` and every subsequent byte is cleartext. Payloads stay E2E-sealed, so this is a
routing-metadata break rather than a content break, but metadata is exactly what PB-NET-2's
cleartext ban protects. It also falsified two doc comments, including the slice's own headline
claim that "every refusal happens here, before a socket is opened".

**Fixed** by re-running `resolve` on every hop via `CheckRedirect`, and by always building an
HTTP client (the cleartext path previously fell back to `http.DefaultClient`, which is how the
library's redirect policy applied at all). https->https redirects still work and still hit the
pin, since `VerifyPeerCertificate` lives on the Transport.

RED, verbatim:
```
--- FAIL: TestTLS_RedirectToCleartextIsRefused
    DialSecure(wss://) followed a 302 to cleartext and returned a Client: the session runs unencrypted
```

**B2 — §6.0's anti-burst clause was unimplemented.** The spec says the 64-op reconnect drain
"must not be issued as one burst", because the relay's limiter is a **tumbling** minute window.
`flush()` was a tight loop issuing all 64 appends back to back. The existing test drove exactly
that burst and asserted only that each op arrives once, so the requirement was both
unimplemented and untested. Fixed with `FlushPacing = 125ms` (8/s, matching the §6.0 input
budget) through the existing `Sleep` seam; a cancelled session aborts and leaves the rest queued.

**N1 — `Drain` double-delivered under concurrency**, reproduced 3/3: two concurrent drains
delivered 10 items for a 5-item mailbox, because `Cursor()` -> `SetCursor()` was an unguarded
read-modify-write while every other `Session` method was mutex-safe. On a handset a foreground
drain plus a push-wake drain is exactly that shape. Fixed with a `drainMu`.

## Release-proofing the cleartext carve-out

`ws://` requires **all three** of: explicit `AllowLoopbackCleartext`, `testing.Testing()`, and
a **loopback IP literal** (never a hostname, so `localhost` cannot be repointed by DNS). A
build tag cannot satisfy both halves, because `go test ./...` passes no tags — hence a runtime
discriminator. The test compiles and runs the exact release program and asserts it is still
refused. Measured cost of importing `testing` into a production package: **0 flags registered**
(registration lives in `testing.Init()`, which only the generated test main calls) and
**+79,776 bytes** (+2.9%).

## Harness repairs (a third agent)

Two helpers were provably broken and repaired without touching any assertion:
- `selfSignedDER` obtained its "different" certificate from `httptest`, which serves **one
  hardcoded certificate for every server in the process** — verified directly: two independent
  servers yield byte-identical 844-byte DER. So the wrong-pin certificate *was* the pinned one
  and the test was unwinnable by any implementation. Now mints a real second certificate.
- `waitState` returned immediately when already in the awaited state, so it never waited for
  the reconnect its doc promised. One test was provably unreachable: it demanded a second
  `auth_init` ~25us after `Cut()` returned, while a real reconnect measures 4-9ms (instrumented:
  peer-close observable at ~120us via OS netpoll — not something an implementation can outrun).

The reviewer confirmed by mutation that the repaired suite has teeth: no reconnect fails 5
tests; `SendLive` falling back to the queue fails the D7 test; a drop-oldest queue is caught;
removing `ErrStuckPage` is caught; declaring Android as system-trust-roots is caught.

## The Android trust-root decision stands, with a corrected rationale

Android is declared pinning-only, enforced by returning `ErrPinRequired` rather than living in
a comment. The original justification was wrong: Go **does** read Android's system store
(`root_linux.go` lists `/system/etc/security/cacerts`, and `GOOS=android` implies the linux
tag). The accurate and still-sufficient reason: Android 14+ moved the CA store to the Conscrypt
APEX, which is not in Go's search path, so the pool is stale-or-empty on modern handsets, and
user-installed/enterprise CAs are never picked up.

**Operational consequence, now PB-OPS-5**: the pin is a leaf DER, so every Let's Encrypt
renewal (60-90 days) breaks the handset. An SPKI-hash pin survives renewal at the same security
level.

## Recorded residuals

- **One extra frame from a hostile relay shifts every subsequent reply by one, permanently and
  silently.** `Conn.pending` discards replies owed to abandoned requests, which fixes the
  timeout desync, but a *duplicate* frame is undetectable — proven: after one duplicate, a
  drain returned `0 items, hasMore=false, err=nil` against a mailbox holding one. This hazard
  predates S6 (the old inline reader had it too) but is now the load-bearing assumption of a
  deliberate design change, in a system whose threat model says the relay is untrusted. The
  real fix is S6b's request-id correlation.
- **Pumping `Dial` (as well as `DialSecure`) is untested** — reverting it passes both suites.
  It alters `relay.Dial`'s semantics for its one production caller (read errors surface as a
  generic closed error; a malformed frame now kills the connection).
- **`SendOp` loses an op if the connection dies mid-request** — the caller sees an error, not a
  silent drop, but the docstring's "held until the link returns" does not cover that window.
  The correct answer is the definitive-refusal vs delivery-unknown split PB-GW-7 already
  demands; blind re-queueing is wrong because the relay commits before replying.
- **"Never a hostname" is asserted in a comment, not a test** — a mutant accepting `localhost`
  passes the suite.
- **`ErrPinRequired` is never executed on any test platform**, because the trust-root source is
  read from `runtime.GOOS` directly rather than through a seam.
- **`Drain`'s 10s budget spans read + caller work + ack**, so a slow callback can fail the ack
  after the cursor already committed (self-healing, but the caller sees an error for a drain
  that succeeded).
- **`Close()` deadlocks if an `OnState` callback calls `Close()`.**
- **A test writes into the source tree** (builds a release-check program under the package dir);
  a SIGKILL leaves it behind and `go test ./...` requires a writable checkout.

## Gates

```
go test ./internal/remote/transport/ ./internal/remote/relay/ -count=1     ok / ok
transport suite stability                                                  3/3 green
go test -race ./internal/remote/transport/ ./internal/remote/relay/        ok / ok
go build ./... && go vet (scoped)                                          clean
```

Pre-existing load flake recorded, with evidence it is independent of this slice:
`TestRelay_SweepLoopPresenceSilentPushNoManualCall` (a 5ms sweep ticker against a 3s bound)
passes 3/3 isolated under `-race` and **still reproduces with all three new S6 tests excluded**.

## Per-requirement evidence (PB-E2E-3)

Added in S19. The traceability table cites this file for **PB-NET-2, PB-NET-3, PB-NET-4, PB-NET-6
and PB-NET-7**, and until now it named only the first two outside the range form in its title, so
three shipped rows cited a document that never mentioned them. Reconstructed from the tests
below, every one of which is in `internal/remote/transport` and can be run.

### PB-NET-4 — resilience, with the numbers stated

Each clause of the requirement has its own test, and the numeric clauses are asserted against
§6.0's table rather than against the implementation's own constants:

- **Bounded exponential backoff with a stated ceiling**: `TestBackoffBudgetIsTheCommitteeBudget`
  (initial 500 ms, factor 2, ceiling 30 s — read from the budget, so a drifted constant fails)
  and `TestBackoffScheduleDoublesToACeiling`.
- **Jitter**: `TestReconnectDelaysStayWithinTheJitterBand` (+/-20%).
- **Connection state surfaced**: `TestConnectionStateIsSurfaced`.
- **Re-auth after reconnect**: `TestReAuthenticatesAfterReconnect` — a second `auth_init` after a
  cut, which is what makes the reconnect a new authenticated session rather than a resumed one.
- **Input and resize are never queued or replayed** (ADR-007 D7):
  `TestLiveFramesAreNeverQueuedAndNeverReplayed`. This is the clause the two-send-method design
  above makes structural: `SendLive` has no queue to fall back to.
- **The idempotent-op queue's bound and drop signal**: `TestIdempotentOpQueueIsBoundedAndRejectsNew`
  — 64 ops, the 65th refused with an error, never a silent drop and never drop-oldest.
- **The reconnect drain is paced**: `TestReconnectDrainIsPacedNotABurst`, added by the review as
  B2 above; §6.0's anti-burst clause was unimplemented and the pre-existing test drove exactly the
  burst it forbade.

The reviewer's mutation run is what makes these non-vacuous: no reconnect fails 5 tests, `SendLive`
falling back to the queue fails the D7 test, and a drop-oldest queue is caught.

### PB-NET-6 — the Phase A relay-adversary properties, across process restarts

`internal/remote/transport/restart_test.go`: `TestReplayIsRefusedAcrossAProcessRestart` (seq
gating survives a restart — the clause v1's single-process criterion could not see),
`TestDurableCursorSurvivesProcessRestart`, `TestHostilePaginationTerminates` (a relay that pages
forever is refused with `ErrStuckPage` rather than wedging the transport),
`TestMailboxCapSurfacesACleanRefusal`, `TestRelayAdversaryPropertiesHoldThroughTheSession` (the
replay/reorder/dup set through the real client), and `TestConcurrentDrainsDeliverEachItemOnce`,
which is the N1 defect found in review: two concurrent drains delivered 10 items for a 5-item
mailbox, the exact shape a foreground drain plus a push-wake drain takes on a handset.

**The requirement's second half is NOT S6's and is not claimed here.** "plus the PB-STATE-2
restart case" is `internal/phonecore`'s process-death test, which landed in S7; see
`remote-phaseB-s7-evidence.md`. S6 covers the transport across a restart of the transport's own
process.

### PB-NET-7 — hygiene

**CORRECTION, 2026-07-30 (ADR-007 B105). Everything in the paragraph below cites a file that no
longer exists.** `internal/remote/transport/hygiene_test.go` was deleted with its package (`b43b03f`)
because nothing in production called any of it — **including these six tests, which is why this row
was NOT MET while this evidence read complete.** The paragraph is kept, struck, because it is the
clearest statement in this repository of what a complete fence over dead code looks like.

**Live evidence as of `2ae1386`:** the 10 s budget and every-call-times-out clauses are
`internal/remote/relay/calldeadline_test.go` and `calldeadline_boundary_test.go` (the budget pin
transcribes 10 s from section 6.0's table, not from the constant); calls-after-close is
`calldeadline_boundary_test.go`'s `ErrConnClosed` arms; cancellation on the **wait** path is
`s6b_wait_test.go`; and the goroutine-leak assertion is `mobile/conformance/pbnet7_leak_test.go`,
**now the only one in the remote stack.**

**Still owed, and the reason this row stays NOT MET:** `TestContextCancellationIsHonoured` and
`TestDialHonoursCallerContext` covered cancellation on the **generic request** and on the **dial**.
Neither has a named live equivalent. **The deletion removed them and nothing replaced them.**

~~Superseded:~~ `internal/remote/transport/hygiene_test.go`, one test per clause:
`TestNonWaitRequestTimeoutIsTheCommitteeBudget` and `TestEveryCallTimesOutAgainstASilentRelay`
(timeouts everywhere, at §6.0's 10 s), `TestContextCancellationIsHonoured` and
`TestDialHonoursCallerContext` (cancellation honoured on both the request and the dial),
`TestCallsAfterCloseFailCleanly`, and `TestNoGoroutineLeakAcrossConnectDisconnectCycles` — the
acceptance criterion's leak assertion over repeated connect/disconnect. The `-race` half of the
criterion is the gate block above.

**Its one honest exception is already recorded in the residuals**: `Close()` deadlocks if an
`OnState` callback calls `Close()`. That is a hygiene defect inside PB-NET-7's own subject
matter, so it is named here rather than left in a list a reader of this row would not reach.

---

## PB-NET-8 — the machine hop's recovery mechanism (added 2026-07-31, ADR-007 B120)

**The requirement is new; the code that satisfies it landed in round 7.** That order is the
finding, not an accident of bookkeeping: the mechanism was absent for the whole project and
**no row required it**, so seven rounds of re-deriving existing rows could not find it. It took
an adversarial member cutting a live connection and measuring what happened.

**What was measured before the fix** (ADR-007 B120/F1), against a real relay, a real `Service`
and a real client, with only a proxy cut:

```
premise: phone received 1 item(s) over the live link
Service.Run was STILL RUNNING 8s after its only relay connection died, and nothing redialled
post-cut: phone still sees 1 item(s) -- nothing delivered after the cut
```

`cmd/swarm-remote/main.go` dialled **once per process**. `Service.Run` had no relay reconnect —
`runJournal` reconnects to the **daemon** — and `relay.Client.Done()`/`Err()`, which exist
precisely to notice a drop without issuing a request, had **zero production callers**. The
process therefore never exited, so `KeepAlive{SuccessfulExit:false}` and `Restart=on-failure`
(PB-LIFE-1/-5) never fired: **a supervision policy written against exit cannot restart a
zombie.** The phone reconnected, reported `online`, and nothing appeared in any log.

**What satisfies it now**: `Service.Run` watches the relay connection through the `LinkWatcher`
seam and returns `ErrRelayGone`, distinct from a revoke and from a cancelled parent;
`cmd/swarm-remote`'s `run` loop redials on §6.0's backoff, resets it on `Service.Progressed()`
(traffic crossed the link) rather than on a successful dial, treats a cancelled parent as the
shutdown it is, and rebuilds each generation while carrying the durable coordinates across. The
`relayConn` interface pins the liveness half at **compile** time, because a client that quietly
stopped satisfying `LinkWatcher` would not fail the build — it would fail to reconnect,
silently, in the field.

**Fences**: `internal/remotegw/pbnet4_relaylink_test.go` (Run ends when the link dies),
`cmd/swarm-remote/pbnet4_reconnect_test.go` (the redial, the backoff between redials, and the
cancelled-context path). Both name PB-NET-8 as well as PB-NET-4.

**What this row does NOT claim**: B120's F1b — the relay dropping exactly one reply so the
client's `pending` counter never returns to zero, leaving the gateway able to RECEIVE and never
to SEND — is a separate condition with its own residual. A dead LINK is now recovered; a link
that is up and unusable is not this row.
