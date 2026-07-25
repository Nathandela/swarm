# S6b evidence — the low-latency input path (PB-NET-5)

**Commits**: `c22eb36` (slice), `44fb224` (review fixes), `7562ad6` (fixup — three missed
`PollInterval` references). **Requirement**: 1 (PB-NET-5). **Decision**: ADR-007 **B7**, with a
"**B7 AS BUILT**" amendment recording the mechanism and correcting three of its own overstatements.
**Related**: `3e83f82` recorded a Phase A finding this slice's review surfaced;
`d426170` recorded that this harness does not measure the shipped input path.

> **RECONSTRUCTED**, 2026-07-25, from the three commits, their diffs and their tests, re-run at
> HEAD. **Read the two sections on what the harness does and does not measure** — PB-NET-5 is the
> requirement whose evidence is easiest to over-read.

## What it replaced

The gateway's **500 ms command-IN poll**, which ADR-007 itself calls "unusable for live typing",
plus a phone hop that head-of-line-blocked on `Conn.roundtrip` holding `c.mu` across a
write-then-blocking-read. §4.5 had already proved that a naive long-poll blocks the keystroke path
and that a second connection is not available.

**Both hops** were changed: a bounded server-side wait with request-id correlation and concurrent
dispatch.

**`ServiceConfig.PollInterval` is DELETED rather than tuned**, and a test asserts the field itself
is gone (`TestS6B_GatewayExposesNoFixedCommandPollCadence`). The reason is exactly the shape this
project keeps catching: **a phone-side-only fix passes a naive reading of the exit criterion while
typing stays 500 ms-gated.** Deleting the field is what makes that unavailable.

## What proves PB-NET-5

The requirement has a mechanism clause and a latency clause, and they are evidenced differently.

### The mechanism — always-run tests, all green at HEAD

| Package | Tests | What they establish |
|---|---|---|
| `internal/remote/relay` | 11 | The protocol change: the wait ceiling **is** §6.0's budget; an append completes while a wait is outstanding; a wait on one client does not block another connection; a second concurrent wait is **refused, not queued**; cancellation frees the slot; the ceiling returns an empty page; a wait **meters exactly one op per call, not per item**; concurrent dials do not bypass the per-source conn cap; an unauthenticated conn cannot park in a wait to evade the handshake deadline; **takeover** and **revoke** each sever an outstanding wait |
| `internal/remote/transport` | 6 | The phone hop's live tail: the drain budget arithmetic fits the relay ops window; sustained typing stays inside the drain budget; follow is not truncated by the non-wait request timeout; a keystroke completes while following; a keystroke never survives a disconnect while following (ADR-007 D7); follow stops cleanly and leaks no goroutine |
| `internal/remotegw` | 4 | The gateway hop: no fixed command poll cadence exists; the command loop **waits instead of polling**; input latency is not poll-gated; the drain stays inside the budget |

`go test -count=1 -run 'TestS6B' ./internal/remote/relay/ ./internal/remote/transport/
./internal/remotegw/` at HEAD 2026-07-25: **21 tests, all PASS**, three packages ok
(5.8 s / 10.0 s / 8.1 s). That includes the two entries on the load-sensitive list —
`TestS6B_GatewayInputLatencyIsNotPollGated` and `TestS6B_KeystrokeCompletesWhileFollowing` — which
passed under concurrent agent load on this run.

### The latency clause — and it is gated OFF by default

`internal/skeleton/s6b_input_latency_test.go` `TestS6B_InputLatencyPhoneTypeToPTYWrite` is the
harness for §6.0's p50 <= 150 ms / p95 <= 400 ms / p99 <= 800 ms over n >= 200. **It is gated on
`SWARM_S6B_LATENCY=1` and skips in every ordinary run**, including `go test ./...`. Confirmed at
HEAD:

```
--- SKIP: TestS6B_InputLatencyPhoneTypeToPTYWrite (0.00s)
    PB-NET-5(b) latency harness: set SWARM_S6B_LATENCY=1 to run
```

The gating choice is **deliberate and reasoned**, and the reasoning is worth keeping: §6.0 describes
a *benchmark* (median of 3 runs, n >= 200 each, 20-sample warm-up discarded), not a unit test. It is
gated by an **env variable rather than a build tag** because GG-4's four gates all run untagged — "a
build-tagged file is never compiled, never vetted and never linted by the gate, so it rots into a
place where the hardest requirement in the phase can quietly die." An env gate keeps the file
compiled and vetted on every run and costs one `t.Skip`.

**The consequence for an auditor is the part to internalise**: a green `go test ./...` says nothing
about PB-NET-5's numbers. What it says is that the 21 mechanism tests pass. The numbers must be
produced deliberately.

**Re-measured at HEAD, 2026-07-25**, with `SWARM_S6B_LATENCY=1`, 3 runs × 200 samples after a
20-sample warm-up, 125 ms pacing — **PASS**, 123.8 s:

```
environment: GOOS=darwin GOARCH=amd64 cpu="Apple M1" NumCPU=8 go=go1.26.1 translated=YES-rosetta2
run 0 (n=200): p50=36.44ms  p95=91.21ms  p99=113.59ms
run 1 (n=200): p50=50.96ms  p95=82.70ms  p99= 99.19ms
run 2 (n=200): p50=33.10ms  p95=62.66ms  p99=114.72ms
median of 3:   p50=36.44ms/150ms (24%)  p95=82.70ms/400ms (21%)  p99=113.59ms/800ms (14%)
```

The commit recorded **p50 486 ms -> 31 ms, 21% of budget** (p95 11%, p99 7%) at the time it landed.
The re-run above is looser on the tails — the box was carrying concurrent agent load, and S11 has
since added the lease gate, the coalescer and the bucket lock to `phonecore`'s half of the path —
but every percentile is comfortably inside budget and the *shape* of the result (the 500 ms poll is
gone) is unchanged. The 486 ms "before" figure is not reproducible at HEAD: `PollInterval` no longer
exists to restore.

Note the harness records the environment as §6.0 requires, **including the translation status from
an in-process probe**: `translated=YES-rosetta2`. That line is the one that makes runs on this host,
on native arm64, and on CI's x86 Linux comparable at all.

### What the harness measures, precisely

The full production chain on one machine: `phonesim` (over the real `phonecore`) seals an input
frame under the epoch content key and appends it to the machine's mailbox on a **real in-process
relay**; the gateway's command-IN path opens it, **persists the inbound checkpoint** (PB-GW-3 puts
that fsync *before* the PTY write), and rides it down the `take_control` lease conn to the daemon,
which writes it to the session's PTY. Arrival is observed on a **read-only shared session tap** —
the shim's own output pipe, no render debounce in the path — by looking for the unique marker the
keystroke carries.

**Two §6.0 harness rules are structural preconditions checked before any measurement**, not
comments:

1. **The measured path must carry no fixed command-IN poll cadence.** At the time the file was
   written, `ServiceConfig.PollInterval` defaulted to 500 ms and the phonesim harness tuned it to
   20 ms — *a test-only value that would make the harness certify a path production never runs*.
   **That precondition failing is this file's RED.**
2. **The gateway must use a real file-backed `InboundState`.** S2 measured the per-keystroke fsync
   at 13-15 ms on this host, ~10% of the p50 budget, and it sits on the keystroke path. Measuring
   with the in-memory store measures a fiction. Asserted by requiring the checkpoint file to exist
   and be non-empty after the run.

## Failing-first evidence (GG-5)

- **Precondition 1 above is the literal RED**: the harness could not run at all against a tree with
  `PollInterval` in it, and the fix was to delete the field rather than tune it.
- **The review found a blocking defect the tests could not reach**, and the disclosure around it is
  the most instructive part of the slice. The wait goroutine read `sc.rid` **unlocked** while the
  request loop wrote it, and **re-authenticating an already-authenticated socket under a different
  key is a legal frame sequence** — so the wait slot was orphaned permanently: the routing id could
  never park a wait again. The rid is now captured **at registration**, under the lock.
  **The implementer could not reproduce the race-detector hit and said so**: bolt's metalock
  incidentally orders the two accesses today, an ordering that would vanish if the read moved off
  the store path. It shipped a **deterministic test asserting the sharper consequence** instead of
  a flaky race test. `internal/remote/relay/wait_reauth_test.go`.
- **A guard that could not fail, found and fixed in `44fb224`.** The pre-auth fence was standing
  class (i): deleting the `if !sc.authed` check so a pre-auth wait really does park **still
  PASSED**, because `HandshakeTimeout` tears the connection down regardless and the client's blocked
  read returns a websocket close inside the window. Both assertions passed while the property in the
  test's own name — *refused INLINE, never parked* — went unfenced, and a refactor moving the auth
  check after `registerWait` would have shipped green. It now asserts `ErrNotAuthorized`
  specifically, **mutation-verified to fire** (returns EOF under the mutation), with `wait.go`
  restored byte-identical.
- **`7562ad6` is a small but pointed record**: S6b deleted `ServiceConfig.PollInterval` but the
  slice was committed **by explicit pathspec**, leaving three `internal/skeleton` e2e test files
  behind. `go build` passes because test files are not built, **so the gap was invisible to the
  build gate while `go vet ./...` and `go test ./...` both failed at HEAD.** Found by the S11
  implementer, which noticed the files were already modified in the worktree and correctly left them
  alone rather than absorbing another slice's change.

## Three corrections `44fb224` made to ADR-007 B7's own arithmetic

Recorded because they would otherwise have been inherited on trust:

1. **The one-reply-per-request floor dropped its own precondition.** At 8 frames/s the gap is 125 ms
   and L is 150 ms, so `125 < 150` and one request returning at t=125 ms delivers **both** items.
   The floor is `min(R, 1/L) = 6.67 req/s`, **not 8**, and the queueing figure is 167 ms, which §6.0
   already stated. The conclusion is unchanged — 6.67 > 3 still needs the streaming subscribe the
   ADR rejected — but the gap is **2.2x, not 2.7x**.
2. **The token bucket caps the sustained average only asymptotically.** It starts full, so a window
   can carry 360 reads/min — inside `OpsPerMin`, so the quota death cannot occur.
3. **What actually restores batching under §6.0's workload is the bucket's forced delay**, not
   evidence of backlog. An un-spaced read returns on the *first* arrival, so it returns exactly one
   item every time and `!p.spaced` short-circuits the idle counter, so nothing accumulates. The rule
   is right and no workload latches the harmful regime — a burst that alternates re-enters batching
   immediately on any multi-item page — but **the trigger is the bucket**.

## Design points that will otherwise be re-derived

- **Concurrent dispatch is one goroutine, not a pool.** Only the wait handler leaves the request
  loop, which is the whole of the concurrency the decision needs (at most one wait per client), and
  it is what keeps the Phase A fences intact for free: per-source admission stays under one lock,
  `sc.authed` stays single-writer, and no ordinary op is reordered. **Every refusal is decided
  inline**, so nothing a refusal touches ever parks.
- **Takeover and revoke sever an outstanding wait.** Without that, the superseded connection — which
  issues no further requests — holds the one-per-client slot and **live typing is dead for up to
  25 s after every reconnect**.
- **Cancellation crosses the wire and is deliberately unmetered.** Releasing only the client's slot
  would leave the server's held until the ceiling, so the next wait would be refused and the
  cancellation would be a lie. It is not metered because a cancel strictly *releases* server state.
- **Acks are off the delivery path on both hops — a LATENCY requirement, not only a quota one.**
  `MailboxAck` measures p50 30.8 ms / **max 129.2 ms** on this host (one synchronous bolt fsync
  each), so a single inline ack can consume **86% of the entire 150 ms p50 budget**. Both hops ack
  from a separate goroutine at <= 1/s. Dropping an ack is safe: both hops advance a durable cursor
  before recording one.
- **The drain is adaptive**, per the amended §6.0: a flat 3 reads/s and a 150 ms p50 are **jointly
  infeasible**, so it starts spaced and drops the spacing after **two** consecutive spaced reads
  return no batch — two rather than one because a single slow append widens one gather window and
  would otherwise latch the regime.

## Accepted residuals

- **THE HARNESS DOES NOT MEASURE THE SHIPPED INPUT PATH.** `phonesim.Phone.Type` seals with
  `phonecore.SealInputData` and calls `relay.MailboxAppend` **directly** — it never enters
  `mobile/commands.go`. So this measurement covers phonecore's seal, the relay, the gateway, the
  daemon and the PTY, but **not the gomobile facade**, which is where the coalescer, the lease gate,
  `sendInputFrame` and S11's bucket ordering lock all live. Those are on the real app's path and on
  nothing this harness times.
  **The numbers are real for what they measure**, and the facade's added cost was measured separately
  and is negligible (~1.9 µs per keystroke frame, ~13.4 µs per 4 KiB paste; 2.2 µs for the command
  sealer). **The gap is coverage, not a known regression** — but it is standing class (v) applied to
  a performance budget, which is precisely the shape that let S11's B-2 defect ship.
  **PB-NET-5's evidence should be read as "phonecore -> PTY", not "phone -> PTY"**, and nobody should
  conclude from it that a facade-layer regression would have been caught. **Owner: S19**, whose exit
  demonstration is the natural place to time the real facade path end to end.
- **Every number here was taken through Rosetta.** `/usr/local/bin/go` is an x86_64 binary on an
  Apple M1, so measurements are **pessimistic** versus a native arm64 build. A budget that passes
  here passes natively with margin. **Never loosen a bound to "correct for" Rosetta.** The real
  hazard is a spurious FAIL pushing an implementer into over-engineering a mechanism that was
  already fast enough. The probe for this must be `sysctl.proc_translated` read **in-process** —
  `uname -m` lies inside a translated process, and a shell-level read lies in the other direction
  because the shell is native. Both traps have caught a reviewer here.
- **PHASE A: re-authenticating an already-authenticated relay socket under a different key is a
  legal frame sequence.** Neither `handleAuthInit` nor `handleAuthResp` checks `sc.authed`, so a
  client may authenticate as A and then re-authenticate as B on the same connection;
  `registerSession` rewrites `sc.rid` in place. **Phase B's exposure through this is fixed** (the
  wait-slot leak, above). The remaining Phase A surface is `s.sessions`, which the reviewer verified
  **does** self-heal because `registerSession` overwrites the key — so this is recorded, not urgent.
  Worth deciding deliberately whether re-auth on an authed connection should be refused outright: it
  is state a client can rewrite after passing the gate, and the next feature keyed on `sc.rid`
  inherits the same trap. **Not in Phase B's scope; do not fix it inside a Phase B slice without an
  ADR.**
- **`transport.SendLive` and `transport.RetryFor` have zero production callers** — the facade appends
  through `relay.Client.MailboxAppend` directly. So
  `TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing`, listed above as evidence for D7, is a
  fence on a path production does not take. Re-verified at HEAD. Not currently harmful; the hazard is
  the next slice adding a resend. **Owner: whoever next touches the transport send path.** See
  `remote-phaseB-s11-evidence.md`.
