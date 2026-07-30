# Dial-bound evidence — the handshake is bounded at `dialConn`

**Requirement touched: PB-NET-7** (and PB-NET-4's reconnect schedule, which a parked dial
prevents from ever running). **This file is not a claim that PB-NET-7 is MET** — that verdict is
the committee's, and the enumeration fence's row states the rest of what the row owes. It is the
verification owed for the dial fix committed at `b806444`, which ADR-007 B122's addendum records
as adjudicated-at-the-boundary and names as this agent's to evidence.

| | |
|---|---|
| `go build ./...` | green |
| `go vet ./...` | green |
| `~/go/bin/golangci-lint run --max-same-issues=0 --max-issues-per-linter=0` | **0 findings** |
| `go test -race ./internal/remote/relay/` | green, 90.1 s |
| `go test ./mobile/conformance/` | green, 186.8 s |
| `go test ./internal/skeleton/` | green (§5, mutation 4) |

**Gated from an isolated `git archive` of the commit under test, not from the working tree.**
Four other agents were editing this worktree while these ran, and `internal/remotegw` did not
compile in it at the time. Every number below comes from a clean extraction of the named commit.

**Two failures elsewhere in the tree are NOT this change's**:
`TestPBNET4_TheGatewayRedialsTheRelayAfterTheLinkDrops` and
`TestPBNET4_TheGatewayBacksOffBetweenRedialsRatherThanHammering` in `cmd/swarm-remote` are
another agent's RED at `29ba43a`, which landed between this change's RED and GREEN. They fail
for a missing redial loop; a connect-phase deadline cannot remove a loop.

---

## 1. The defect

`dialConn` applied no deadline, and **all three** shipped callers supplied cancellation-only
contexts. So a relay that accepts the TCP connection and then stalls parks its caller for as
long as it holds the socket. It is the declared adversary and this costs it one file
descriptor; a half-open TCP after a WiFi → cellular handoff presents identically, so it is
reached benignly too.

| Caller | Context it dials with | What a stalled dial costs |
|---|---|---|
| `mobile/app.go` → `App.dial` | `context.WithCancel(context.Background())` | **Never enters backoff.** `App.run`'s reconnect schedule runs BETWEEN dial attempts, so a dial that never returns is a dial that never fails and PB-NET-4's backoff — itself implemented, fenced and mutation-proven — never runs once. |
| `cmd/swarm-remote` → `run` | `signal.NotifyContext(context.Background(), …)` | A sidecar that never starts and never says why. |
| `internal/skeleton` → `relayRendezvousFactory` | the pair_start handler's `context.WithCancel(context.Background())` (`internal/protocol/server.go`) | **Burns the owner connection's pairing SLOT.** |

**The third caller is the worst of the three, and I did not know it existed** — it was found by
the PB-NET-7 enumeration (`internal/verify/pbnet7_deadlines_test.go`) after the first two had
been fixed. Its dial runs at `pairing.go`'s `cfg.NewRendezvous(ctx, id)`, **before**
`pairCtx, cancelPair := context.WithTimeout(ctx, window)` — ADR-007 **B64**'s fix — so it sits
inside the pairing slot and outside the window B64 exists to close. The slot is already claimed
(`cc.pair = ps`) and is released on exactly two paths, `result` and `BeginPairing`'s error
return; a parked dial reaches neither. B64's own comment states the price: *"every later
pair_start on it is refused 'pairing already in progress'. There is no pair_cancel op; dropping
the owner connection was the only exit."* **B64 fixed the handshake's missing clock; the dial
that precedes the handshake is the one step of the ceremony B64 did not reach.**

**Three callers, three independent failures to bound, and only two were known when the fix was
written.** That is the argument for the boundary rather than a prediction about it: a per-caller
fix would have gone to the set we happened to know, and that set was demonstrably wrong.

**And the third site shows why the caller is the wrong place even when it is remembered.** The
ctx `relayRendezvousFactory` receives has **dual duty** — it bounds the dial *and* owns the
connection's lifetime, through the `go func(){ <-ctx.Done(); _ = conn.Close() }()` watcher three
lines below the dial — so a caller-side `defer cancel()` there closes the connection it just
returned. The boundary bound has no such hazard at any of the three sites.

**Third instance of one shape.** B94 bounded the non-wait exchanges, B115 bounded `MailboxWait`
at the gateway, and the dial — which happens BEFORE either, and without which neither bound is
ever reached — was never bounded at all.

## 2. RED — `b9367d7`, behavioural, not a compile failure

`internal/remote/relay/dialdeadline_test.go` stands up a peer that accepts and then stops at a
chosen stage. No existing fixture stalls DURING the handshake: `silentRelay`
(`calldeadline_test.go`) stalls after it completes.

```
--- FAIL: .../TLS_handshake (60.01s)
    DialSecure is STILL PARKED after 1m0s ... stalls at the TLS handshake
--- FAIL: .../HTTP_response (60.00s)
--- FAIL: .../websocket_upgrade (60.01s)
--- PASS: .../relay-auth_exchange (10.06s)
    DialSecure returned after 10.049166541s with relay: the relay did not answer
    within the call deadline: context deadline exceeded
--- FAIL: TestDialDeadline_EveryExportedDialInheritsTheBound/DialRaw (60.01s)
--- FAIL: .../DialRawSecure (60.01s)
--- FAIL: .../Dial (60.01s)
--- FAIL: .../DialSecure (60.01s)
```

**The one PASS is the measurement, not an omission.** `DefaultCallTimeout` already covered the
relay-auth exchange; every stage BEFORE it was unbounded, which is why two previous fixes to
this shape left it live.

And the user-visible half, over the real facade
(`mobile/conformance/pbnet4_stalleddial_test.go`):

```
--- FAIL: TestPBNET4_AStalledDialStillReachesTheReconnectSchedule (90.20s)
    only 1 dial attempt(s) reached a relay that accepts connections and answers
    nothing, after 1m30s.
```

The tolerances in both files are literals that transcribe no production constant (ADR-007
B113), so they fail a widened bound as readily as a missing one.

## 3. GREEN — `b806444`

`dialConn` wraps the websocket handshake in `relay.DefaultDialTimeout`, so the TCP handshake,
the TLS handshake, the HTTP response and the websocket upgrade all end on a deadline this side
declares. Every dial funnels through `dialConn`, so `DialRaw`, `DialRawSecure`, `Dial` and
`DialSecure` inherit it — and so does the next dial path added.

All nine assertions green; the four connect-phase stalls return at ~10.0 s and the relay-auth
stall at ~10.04 s, so the whole dial is bounded end to end.

## 4. The number, and what is still the committee's

**10 s is §6.0's, reused rather than re-derived.** The connect phase IS one non-wait
request/reply — an HTTP GET carrying the upgrade, answered by a 101 — and the budget table binds
*"Non-wait request timeout | 10 s"* to PB-NET-7. Minting a second, local dial budget is what
B99 refused.

**The composition corroborates it, and `TestDialDeadline_TheWholeDialFitsTheRelaysOwnPreAuthWindow`
pins that**: connect 10 s + two auth exchanges at `DefaultCallTimeout` = 30 s, which is exactly
`Config.HandshakeTimeout` — the same window the relay bounds from its own side
(`preAuthDeadline`: *"CUMULATIVE time-to-authenticate, anchored at accept time"*), one of the
Phase A constants §6.0's preamble names as the values its table is chosen to be consistent with.
**Only the number is borrowed from the adversary, never its enforcement** — B112 recorded that
exact error, measured losing by 2.8x.

**RECORDED, NOT DECIDED HERE.** §6.0 has no row for a dial or a connect, so *"the upgrade is one
non-wait request"* is a **reading of an existing row, not a row**. It is the narrowest reading
available — it mints nothing and composes onto a constant already in the table — but a committee
that wanted a distinct dial budget would be **setting** it, not confirming this. That is the one
open question this change leaves.

## 5. Mutation proofs — the connection, then the value

Both run in an isolated extraction; the file is restored from the commit and checksummed after.

**Mutation 1 — unwire the bound from the dial** (`websocket.Dial(dctx, …)` → `websocket.Dial(ctx, …)`,
the two bound lines removed). `DefaultDialTimeout` keeps its value and stays defined, so the
value-pin cannot be what fails:

```
--- PASS: .../relay-auth_exchange (10.06s)
--- FAIL: .../HTTP_response (60.01s)
--- FAIL: .../websocket_upgrade (60.01s)
--- FAIL: .../TLS_handshake (60.01s)
--- PASS: TestDialDeadline_TheWholeDialFitsTheRelaysOwnPreAuthWindow (0.00s)
--- FAIL: TestDialDeadline_EveryExportedDialInheritsTheBound/{Dial,DialRaw,
          DialRawSecure,DialSecure} (60.01s each)
```

**The value-pin passing here is the point** (B113): it proves the behavioural fences, not the
transcribed number, are what catch a removed bound.

**Mutation 2 — widen the value to `3 * time.Hour`, bound left wired.** The value-pin fails,
naming the arithmetic:

```
--- FAIL: TestDialDeadline_TheWholeDialFitsTheRelaysOwnPreAuthWindow (0.00s)
    the whole dial is bounded at 3h0m20s (connect 3h0m0s + two auth exchanges at
    10s each), but the relay's own pre-auth window is 30s.
```

**Mutation 3 — the same unwiring, measured end to end at the facade,** after the committee
reverted the caller-side bounds so `App.dial` declares no deadline of its own:

```
bound wired    ok   mobile/conformance  11.6s
bound unwired  --- FAIL: TestPBNET4_AStalledDialStillReachesTheReconnectSchedule (90.16s)
                   only 1 dial attempt(s) ... after 1m30s
```

**Mutation 4 — the same unwiring, at the THIRD caller, measured at the daemon.**
`internal/skeleton/pbnet7_stalleddial_test.go` drives two `pair_start`s over the owner-tier
wire against a relay that accepts TCP and answers nothing, using the **real**
`relayRendezvousFactory` under `relay.MachineSecurity()` — substituting an in-memory rendezvous
would measure nothing, since the whole defect lives inside that closure:

```
bound wired    --- PASS (22.09s)   both pair_starts answered; the second was ACCEPTED
bound unwired  --- FAIL (62.02s)   no answer to pair_start within 1m0s
```

The assertion is the **slot**, not the dial: the second `pair_start` on the same connection must
not be refused *"pairing already in progress"*. A fence proving only "the dial returns" does not
show that consequence, which is why it is asserted where an operator would see it.

Every mutation was reverted in the same command and `internal/remote/relay/client.go`
re-checksummed to `f431ecc…` afterwards.

## 6. What this change does NOT cover

- **`auth.Sign` is deliberately outside the bound.** It sits between the two auth exchanges and
  is the caller's closure; on a hardware-gated custody it refuses rather than blocks (B18(a)),
  but a network deadline over a custody decision would be the wrong instrument either way.
- **The bound is a ceiling, not a budget for every caller.** `context.WithTimeout` takes the
  earlier deadline, so a caller that needs less still gets less, for free.
- **The precondition that a dial deadline does not become the CONNECTION's deadline** is a
  property of `coder/websocket` and `net/http`, not of this repository. It is fenced separately
  by `relay.TestPBNET7_ADialDeadlineDoesNotOutliveTheHandshake`, and the implementation comment
  names why it holds (a 101 leaves `net/http` holding `errCallerOwnsConn` and cancelling the
  request that produced it).
