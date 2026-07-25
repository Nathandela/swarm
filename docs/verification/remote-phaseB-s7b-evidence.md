# S7b evidence — the gateway's bounded-age backstop (PB-GW-2)

**Commit**: `a0bd09d` — seven files, +503/-7. **Requirement**: 1 (PB-GW-2).
**Spec**: `3838b83` amended PB-GW-2 because its original wording named a seam that does not exist.
**Related**: `aa6f22e` recorded two latent hazards this check creates.

> **RECONSTRUCTED**, 2026-07-25, from the commit, the diff and the tests, re-run at HEAD.

## What it is for

The per-`(sender, epoch)` sequence replay guard cannot cover one window: **after a gateway restart
the in-memory high-water is gone**, and a fresh `crypto.MailboxReceiver` has `seen == false` and
**skips the staleness check entirely**. A relay that retained frames can re-inject one and the guard
has nothing to compare it against. A 10-minute bound on the **authenticated** `IssuedAt` closes that
window.

The value is §6.0's: well above the 60 s command TTL and any plausible delivery delay, well below
the 7-day retention cap.

## The requirement had to be amended, and that is the RED

PB-GW-2 v3.5.1 said "the inbound receiver enables the bounded-age check, which `NewMailboxReceiver`
leaves at `maxAge == 0`". **That was unimplementable verbatim**: `MailboxReceiver.maxAge` and `.now`
are unexported, `NewMailboxReceiver()` takes no arguments, there is no setter, and
`internal/remote/crypto` is **frozen** — the only code that sets `maxAge` lives inside the package,
in a test. S7's author had hit the same wall and recorded it.

So the tests pin the **property at the gateway seam**, not the field, and they were written to hold
under **both** admissible implementations: (A) an ADR adds a max-age constructor to `crypto`; (B) the
gateway authenticates the envelope itself via `crypto.OpenMailbox`, applies the bound, and only then
calls `recv.Accept`. **Route B shipped**, and `crypto` stayed frozen. The two places where the two
routes' error precedence differs are deliberately avoided in the assertions.

## The ordering IS the requirement

Both wrong orderings are named, and both have mutations the suite catches:

- **Bounding before the AEAD verifies** would apply the check to a timestamp **the untrusted relay
  supplied**, and a forgery would be reported as staleness.
  Fence: `TestS7bBoundedAge_AForgeryIsRefusedAsAForgeryNotAsStale`.
- **Bounding after `Accept`** would let the rejection **advance the replay high-water**, so a single
  retained frame at a high seq would permanently refuse everything the real phone sends next — *a
  failed replay converted into denial of typing*.
  Fence: `TestS7bBoundedAge_RejectionDoesNotPoisonTheStream`.

## The bound is one-sided on purpose

`IssuedAt` is AAD-covered, so the relay can only make frames **older**, never newer. A symmetric
window would refuse a fast-clocked handset's live traffic while preventing nothing the seq guard
does not already catch — replay by a fast phone is the seq guard's job.
`TestS7bBoundedAge_ToleratesTheSkewBudgetInBothDirections` pins the tolerance;
`TestS7bBoundedAge_DoesNotReplaceTheSeqReplayGuard` pins that the two mechanisms remain distinct.

**This asymmetry is deliberate and it is also the source of a downstream residual** — see below and
`remote-phaseB-s11-evidence.md`.

## Per requirement: what proves it

| Test | What it establishes |
|---|---|
| `TestS7bInboundMaxAge_IsTheBudgetedTenMinutes` | The constant is §6.0's value, named rather than left to implementer discretion |
| `TestS7bBoundedAge_RefusesARetainedFrameWhenTheHighWaterIsLost` | PB-GW-2's actual scenario: the seq guard is blind and the age bound is the only thing left |
| `TestS7bBoundedAge_Boundary` | The edge of the window, through an injected clock (`OpenMailboxFrameAt`) — without the seam the boundary could not be tested except by waiting ten minutes |
| `TestS7bBoundedAge_RejectionDoesNotPoisonTheStream` | A refused frame does not advance the high-water |
| `TestS7bBoundedAge_DoesNotReplaceTheSeqReplayGuard` | Both mechanisms still fire on their own cases |
| `TestS7bBoundedAge_ToleratesTheSkewBudgetInBothDirections` | The one-sidedness, asserted |
| `TestS7bBoundedAge_AForgeryIsRefusedAsAForgeryNotAsStale` | The ordering relative to the AEAD |
| `TestS7bBoundedAge_IsEnforcedOnTheProductionBridgePath` | **The check is on the path production takes** — `CommandBridge.handle`, not a sibling seam. `OpenMailboxFrame` keeps its exact signature and delegates with `time.Now()`, so the bound is on by default at the production choke point and no cross-package caller had to change |
| `TestS7bLiveTraffic_RealPhoneSealsPassTheEnabledBound`, `_TheWindowIsTenRealMinutes` (`internal/phonecore`) | The non-vacuity half, cross-package: **real phone seals, opened through the real gateway entry point, with the bound enabled** |

## Failing-first evidence (GG-5)

The test file's header states the contract as **undefined symbols -> compile-fail RED**:
`InboundMaxAge` and `OpenMailboxFrameAt` do not exist at `a0bd09d^`.

**PB-GW-6's closure was verified before enabling anything**, not assumed. Enabling a bounded-age
check while any producer left `IssuedAt` unset would compute an age of ~56 years and reject **every
legitimate keystroke** — one of the six orchestration errors the progress doc records as caught by
an agent pushing back with proof. The verification is written into the test header: every
phone->machine seal stamps `IssuedAt` from the wall clock (five functions in `phonecore/input.go`
and `command.go`, landed in S7 `0ac4fb9`), and every real producer — `phonesim` and the Android
binding — goes through those five functions.

**The blast radius was MEASURED, not estimated.** Enabling the bound turned **29 existing tests
across 13 files** in `internal/remotegw` red, every one with the same cause: their sealing helpers
left `IssuedAt` unset, so those fixtures were ~56 years old. `internal/phonecore`,
`internal/skeleton`, `internal/phonesim` and `mobile` were measured **green** under an enabled
bound — which is PB-GW-6 doing its job.

**Five fixture header literals gained `IssuedAt`, and leaving one unstamped would have been the
harmful choice.** Three test files assert that a retained frame is refused **at the replay guard**;
unstamped, they would have been refused by the **age backstop** instead — so PB-GW-1's guard would
have gone untested under a name that claimed to test it. **No assertion text changed.** This is the
most instructive thing in the slice: a fixture that lags the real producer silently relocates which
mechanism a test is actually exercising.

**The review constructed seven mutations and all seven fired.**

## Gates, re-run at HEAD 2026-07-25

`go test -count=1 -run 'S7B|S7b' ./internal/remotegw/ ./internal/phonecore/` — **10 tests, all
PASS**, `ok internal/remotegw 0.96 s`, `ok internal/phonecore 0.70 s`.

## Accepted residuals

- **PB-GW-2 is inbound-only; the phone's receiver still runs `maxAge == 0`.** A real asymmetry,
  deliberately out of S7b's scope. Closing it is **blocked on the PB-TIME-2 reply-seal gap** —
  `SealControlReply` stamped no `IssuedAt` — so the two must be done together or the phone bricks on
  its own command replies. S11 closed the reply-seal half (`TestS11ReplyStamp_SealControlReplyStampsIssuedAt`);
  the phone-side bound is still not enabled.
- **And the asymmetry created a third clock wall.** Because the phone-side check *is* enabled in
  `phonecore/snapshot.go` against `InboundMaxAge` on the **phone's** clock, **a phone more than 10
  minutes FAST goes silently deaf to the entire machine->phone plane** — every reply, journal record
  and snapshot returns `ErrStaleAge`, `mobile/relay.go` swallows it with no stale mark and no event,
  and `ConnectionState()` still reads "online". Not a regression: it follows from this slice's
  deliberate one-sidedness. Recorded in the S11 evidence and the progress doc as the **third** clock
  wall (30 s surfaced, 60 s refused opaquely, 10 min deaf) and the only one that was undocumented.
- **The offline op queue is safe only by accident of design, and nothing pins it.** Found by this
  slice's implementer and recorded before it can bite: now that PB-GW-2 enforces a 10-minute inbound
  age bound, a phone backgrounded past ten minutes would have **every queued mutating op refused as
  stale** if the queue held *sealed* envelopes. It does not — `OpQueue` stores unsealed `QueuedOp`
  and seals at replay time, so `IssuedAt` is stamped **on send, not on enqueue**. That ordering is
  what makes offline replay work at all under the bound, **and no test asserts it**. A future
  refactor that sealed at enqueue would silently brick offline replay, and the failure would look
  exactly like the PB-GW-6 brick this slice was created to avoid. **Wants a test pinning
  seal-at-send**, against S7/PB-STATE.
  *(Compounding note: the progress doc separately records that `OpQueue.Enqueue` has zero production
  callers, so nothing fills the queue today either — the hazard is latent in both directions.)*
- **`crypto` stayed frozen**, which is why route B was taken. If a later ADR ever adds a max-age
  constructor to `crypto`, the gateway's own `OpenMailbox`-then-bound-then-`Accept` sequence becomes
  redundant and should be retired deliberately rather than left as a second, divergent enforcement
  point.
