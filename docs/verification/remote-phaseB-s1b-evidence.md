# Phase B slice S1b — the reconciliation frame (PB-SYNC-7)

## Why this slice exists

PB-STATE-4 requires the phone to reconcile against per-coordinate authorities on reconnect and
fail closed until it can. But the phone's entire inbound plaintext set was journal record,
terminal snapshot, `command_reply` and epoch grant — **none carrying any authority**, and no
action, facade method or ADR item introduced one. Read literally, "fail closed" became
**permanent**: take_control, launch and kill refused forever, so the exit criterion's
"launches" and "types" both fail. This slice supplies the missing carrier.

## Design (ADR-007 amendment B6)

The gateway seals a reconcile record onto the **existing** machine->phone outbound stream. No
new signed device action, so PB-SYNC-5's closed `actionClass` switch is never touched — a
phone-initiated signed reconcile would have needed a new `Action*` constant whose only fitting
class (`ActionControl`) would make a *read repair* require the control tier, with capability
pinned at enrollment and never read from the wire.

Seal tier: epoch content key. Seq bucket: the shared journal/terminal bucket, so
`journal_ceiling` **is** the frame's own envelope seq and its arrival is the reseed for that
bucket. Bootstrap: `RelaySink.Snapshot`, already once-per-(re)connection.

## Two schema subtleties that are the point

1. **No field may be `omitempty`.** With it, "this authority is legitimately 0" and "the
   producer never published it" are the *same bytes*, and the phone would raise no high-water
   while believing it reconciled. The pinned test asserts the literal full-zero JSON string, so
   adding `omitempty` anywhere changes the bytes and fails.
2. **The ceilings are the highest ISSUED seq, never the persisted RESERVATION ceiling.** The
   durable seq source reserves in blocks, which sits above the last frame actually sent — a
   phone seeded there would stale-drop a whole block of live frames. `journal_ceiling` is set
   to the record's own envelope seq inside the lock, so it is self-certifying and structurally
   cannot be the reservation ceiling.

## Review: the flagged risk cleared, two real defects found

The riskiest change was `clientConn.opID` — stashing the request's operation id on the
connection and echoing it, rather than touching ~100 reply call sites. Correctness rests on
`serve()` being strictly sequential and on **no background goroutine** using the reply helpers.
The reviewer verified exhaustively rather than trusting the comment: all 67 `reply*` call sites
resolved to enclosing functions, all reachable only from `handleControl`'s synchronous chain;
all 14 `go` statements in the package traced, each using `writeControl`/`writeFrame*` directly;
the decode-failure path confirmed to clear the id; plus a `-race` run on `protocol` that was
not in the gate list. **Cleared.**

Two defects it did find, both fixed TDD-first:

**F-1 — a lease "grant" could be sealed carrying generation 0.** `confirmLease` re-queried the
generation *after* `Begin` returned, and `LeaseManager.Generation` returns 0 when the conn is
absent — which the watcher removes the moment the conn dies. So: `Begin` succeeds, the daemon
severs the lease (kill switch, revoke, session exit), the watcher fires, and the phone receives
a **positive confirmation naming a generation that does not exist** — exactly what PB-INPUT-2's
"no keystroke without a confirmed current lease generation" forbids, and directly contradicting
the slice's own refusal test, which treats generation 0 as the marker for "nothing was granted".
Fixed: the reply now starts as a refusal and only upgrades to a grant when a real generation
exists.

**F-2 — the refusal path swallowed its own seal error.** If the refusal itself failed to append,
the phone got neither the lease nor the refusal, and the fault was invisible — the file's own
header comment says silence is the worse half. Fixed by joining the seal error.

RED for both, verbatim:
```
--- FAIL: TestCommandBridge_SeveredLeaseIsRefusedNotConfirmedAtGenerationZero
    confirmation op = "lease"; want "error" -- a grant at generation 0 names a lease that
    does not exist, and PB-INPUT-2 forbids gating keystrokes on it
--- FAIL: TestCommandBridge_FailedRefusalSealSurfacesLocally
    PollOnce err = ...; want it to ALSO wrap the failed seal -- the phone got neither the
    lease nor the refusal, and that must not be silent
```

A deliberate asymmetry, now pinned by assertion: the severed-lease refusal seals but does not
fail the item, because `Begin` already consumed the take_control and dialed a real lease conn —
failing it would hold back the inbound high-water and invite a replay of a command that already
ran. The `beginErr != nil` path still fails the item.

## Recorded residuals

- **`sealAtSeq` runs a caller-supplied closure under `s.mu`**; a future `build` calling back into
  a locking method self-deadlocks. Now documented in the doc comment.
- **`authorities()` is sampled before the seq is allocated**, so `InboundHighWater` can be
  stale-low by frames consumed in the window. Self-heals on the next reconcile, and `SeedFrom`
  is monotonic so it cannot rewind the phone. Note for the wiring slice.
- **`SeqSource.Issued()`'s interface doc contradicts the concrete implementation** post-restart
  (prose only; the pinned test has the correct two-sided contract).
- **take_control now skips consume when the reply seal fails**, matching the pre-existing
  kill/delete/launch pattern — but for this class it is a behaviour change. Under restart +
  retaining relay + append failure the frame replays, the daemon's single-use claim refuses it,
  and the phone is sealed an error for an op whose lease is actually live.
- **`ReplyCache` is unbounded**; `TakeFor` makes selective draining possible, so deliberately
  retained unattributable replies can accumulate. Needs a bound or a drain policy.
- **The `opID` invariant has no enforcement** — only a comment. A future background `replyError`
  would break correlation silently.
- **The reconcile arm does not validate `Machine`/`EpochID`** against the router. Currently
  defended by the per-epoch content key and seq ordering; worth an explicit check when the
  authorities are applied.

## CROSS-SLICE BRICK RISK

Production wiring is deliberately NOT in this slice: `service.go` still constructs `RelaySink`
with nil `Authorities`/`Machine`, so the bootstrap is inert and the record is never published.
The phone-side seams have zero production callers. **If a later slice wires the phone-side
`RequireReconciled()` gate and nobody wires `RelayConfig.Authorities`/`Machine`, the phone
refuses every mutating op forever and nothing in the tree fails** — the exact permanent brick
this slice exists to prevent, re-created at the seam. Both halves must land together; see
`remote-phaseB-progress.md`.

## Gates

```
go test ./internal/protocol/... ./internal/phonecore/ ./internal/remotegw/ -count=1   all ok
go test -race ./internal/remotegw/ ./internal/skeleton/ -count=1                      ok
go test -race ./internal/protocol/... -count=1  [extra, opID check]                   ok
go build ./... && go vet (scoped)                                                     clean
```
Pinned tests byte-for-byte unmodified (mtimes verified: tests 02:28-02:34, implementation
03:14-03:22).
