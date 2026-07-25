# S11 evidence — input and lease semantics (PB-INPUT-1..6, PB-TIME-1..3)

**Commits**: `582676e` (slice, committed jointly with S14a — they share
`internal/phonecore/core.go` and could not be separated), `c85c210` (round-3 remediation),
`3d84d53` (round-4 remediation). Adjacent: `4495a0e` (spec, PB-TIME-1), `9173106` (residuals),
`8b31d98` (spec, PB-STATE-2).
**Requirements**: 9. **Decisions**: ADR-007 D7 (input is live-only), B7 (the inbound wait).

> **RECONSTRUCTED**, 2026-07-25, from the three commits, their diffs and their tests. All 68
> S11-owned tests were re-run at HEAD; results are at the bottom.

## What the slice is for

S6b delivered a low-latency transport for input. S11 is the **semantics** on top of it: what a
keystroke is allowed to do, when it may be sent, what happens to it when it cannot be, and what the
phone is allowed to believe about the clock.

Before it, the phone had **no notion of a lease at all**. `mobile.SendInput` sealed and appended a
keystroke with no check of any kind, so PB-INPUT-2's "input is suppressed until a new lease is
visibly confirmed" had nothing to suppress against. PB-SYNC-7 (S1b) had supplied the
*confirmation* half — `remotegw/lease_confirm.go` seals an `OpLease` carrying the daemon-granted
generation — and nothing consumed it.

## What shipped

- **`InputCoalescer`** (`internal/phonecore/coalesce.go`) — a **leading-edge** window: emit on the
  first byte of a burst, then hold for `InputFrameInterval = 125 ms`, with `MaxInputPayload = 4096`.
  Leading edge, not trailing, and the reasoning is pinned in the test header: S6b measured live
  typing at p50 31 ms phone->PTY against a 150 ms budget, and a purely trailing coalescer would add
  a flat 125 ms to the single most latency-visible keystroke there is — the first one after a pause.
- **`LeaseState`** (`internal/phonecore/lease.go`) — per-session, **never durable**, severance
  notices, and a `Require` gate every input path passes through.
- **`SkewMonitor`** (`internal/phonecore/skew.go`) — an RTT-bracketed offset measurement fed only
  from **authenticated** frames, reporting skew only when the *whole* bracket is outside ±30 s.
- **The gateway half** — `SealControlReply` now stamps `IssuedAt` (PB-TIME-2's mirrored trap), and
  a lease-death notice (`lease_sever.go`) is sealed to the phone when the daemon drops a lease.

## Per requirement: what proves it

| Requirement | What proves it |
|---|---|
| PB-INPUT-1 (live-only; a disconnect resolves as explicit "delivery unknown / not sent") | `TestS11Coalescer_AbandonReportsDeliveryUnknownAndNeverReplays`, `_AFailedFrameIsReportedNotResent`, `_ResizeIsLiveOnlyToo`; end-to-end `TestS11R_ADisconnectSeversTheLeaseAndNeverDeliversAKeystrokeLate`; the pull surface is `App.UndeliveredInputs()` |
| PB-INPUT-2 (lease lifecycle across every event; no keystroke without a confirmed generation) | `TestS11Lease_InputIsRefusedWithoutAConfirmedLease` (**with its mutation control**: the same call must SUCCEED once a real confirmation lands, or a gate that refuses everything satisfies half the requirement while making typing impossible — the shape PB-GW-6 shipped and S7b had to fence), `_AGenerationOfZeroIsNotAConfirmation`, `_ARefusalIsNotAConfirmationAndCarriesItsReason`, `_EveryLifecycleEventSeversTheLease`, `_ASeveredLeaseIsNotResurrectedByAStaleConfirmation`, `_ASupersededGenerationsSeveranceDoesNotKillTheLiveLease`, `_IsNeverDurable`, `_TheInboundPathFeedsTheLeaseState`; wiring by `TestS11Wiring_InputIsGatedOnTheConfirmedLease` |
| PB-INPUT-3 (TTL expiry mid-use has defined UX; the three-way 60 s collision) | `TestS11TTL_ByOpClassResolvesTheThreeWayCollision`, `TestS11Lease_SurvivesWellPastSixtySeconds`, `_ExpiryIsDistinctFromAbsenceAndFromSilentLoss`, `_TheMachinesExpiryWinsOverThePhonesSignedHorizon`; `TestS11Wiring_TheSignedTTLIsChosenByOpClass` |
| PB-INPUT-4 (retry keyed on stable codes, never blind resend) | `TestS11Retry_EveryErrorClassMapsToItsPolicy`, `_LiveOnlyWinsOverTheUnderlyingRelayCode`, `_IsKeyedOnTheSentinelNotOnTheMessage`, `_ThePoliciesAreDistinct`. **See the residual — the table is fully tested and has no production caller** |
| PB-INPUT-5 (coalesce under the relay quota; sustained, not burst) | `TestS11Coalescer_SustainedAutorepeatStaysUnderTheRelayQuota` (60 s at autorepeat rate on an **injected** clock), `TestS11Budget_CoalescingConstantsAreTheBudgetedValues`, `TestS11Coalescer_FirstKeystrokeOfABurstIsNotDelayed`; wiring by `TestS11Wiring_InputGoesThroughTheCoalescer` |
| PB-INPUT-6 (ordering + flush at every boundary; paste and IME stated) | `_PreservesByteOrderAcrossFrames`, `_FlushesBeforeResize`, `_FlushEmptiesEveryBoundary`, `_OversizePayloadFlushesEarlyInOrder`, `_PasteIsAtomicAndNeverInterleaved`; end-to-end `TestS11R_PasteIsAtomicThroughTheFacade` |
| PB-TIME-1 (skew detected, bounded ±30 s, surfaced distinctly) | `TestS11Skew_BoundIsTheBudgetedThirtySeconds`, `_TheErrorIsDistinctAndLegible`, `_CheckMirrorsTheLastMeasurement`; the user-facing half `TestS11R_TheSkewVerdictReachesTheUser` and `TestS11R3_AConstantClockSkewRaisesOneEventNotOnePerReply` |
| PB-TIME-2 (every security-relevant timestamp; the mirrored reply-seal trap) | `TestS11ReplyStamp_SealControlReplyStampsIssuedAt` (gateway), `TestS11ReplySeal_StampsANonZeroIssuedAt`, `_CoversEveryMachineToPhoneFrameKind`, `TestS11InboundMaxAge_IsTheBudgetedTenMinutes`, `TestS11ReplyAge_RealMachineSealsPassTheEnabledBound`, `_Boundary`, `_ToleratesTheSkewBudgetAndIsOneSided`, `_RejectionDoesNotPoisonTheStream`, `_IsEnforcedOnTheDurableAcceptPath`, `_AForgeryIsRefusedAsAForgeryNotAsStale` |
| PB-TIME-3 (a skew *protocol*: authenticated exchange, RTT allowance, monotonic/wall split, offline behaviour) | `TestS11Skew_BoundaryAtTwentyNineThirtyAndThirtyOne` (the requirement's literal ±29/30/31 boundary), `_RelayDelayIsNotMistakenForSkew`, `_RealSkewIsStillCaughtThroughASlowRelay`, `_ABackwardClockStepIsDiscardedNotMeasured`, `_AnUncorrelatedStampIsIgnored`, `_OfflineIsNotSkewed`, `_TheMonitorIsFedOnlyFromAuthenticatedFrames` (the requirement's "the relay cannot influence the phone's notion of machine time") |

## Failing-first evidence (GG-5)

**Every S11 test file opens with `FAILING-FIRST (TDD RED, GG-5)` and then states the contract it
freezes as a list of symbols that did not exist.** The RED was therefore a **compile failure of the
test binary**, which is the strongest form available and is reproducible from the diff: none of
`ErrNoLease`, `ErrLeaseExpired`, `Lease`, `LeaseState`, `NewLeaseState`, `Core.Leases`,
`CommandTTL`, `TakeControlTTL`, `CommandTTLFor`, `InputFrameInterval`, `MaxInputPayload`,
`Undelivered`, `InputCoalescer` and its ten methods, `MaxClockSkew`, `ErrClockSkew`, `Skew`,
`SkewMonitor` and its five methods, or `Core.SkewMonitor` exists at `582676e^`.

The three remediation rounds have behavioural failing-first evidence instead, recorded per round
below.

## The protocol decision worth keeping: why the skew check is what it is

PB-TIME-3 forbids both available shortcuts — the relay may not be the authority (untrusted) and an
unauthenticated wall clock may not be either. The mechanism S11 chose was **already in the tree**:
every machine->phone envelope carries an **AAD-covered** `IssuedAt`. A relay that alters it breaks
the AEAD, so an *opened* frame's `IssuedAt` is machine-authenticated by construction — no new verb,
no new exchange, no new signature.

The RTT allowance is the classic one-way-delay bracket: with the phone's send at T1, the machine's
stamp Tm and the receive at T2, `Tm - T2 <= offset <= Tm - T1` (width = RTT). Skew is reported only
when the **whole bracket** lies outside ±30 s. That is what stops a slow relay from being mistaken
for a wrong clock: an untrusted relay can add delay, and delay *widens* the bracket — it does not
move it far enough to manufacture a verdict. Both directions are asserted
(`_RelayDelayIsNotMistakenForSkew` and `_RealSkewIsStillCaughtThroughASlowRelay`).

**Monotonic vs wall, stated rather than assumed**: the RTT is a duration between two readings of
one clock and is measured monotonically; the offset is a difference between two *wall* clocks and
cannot be. A wall-clock step between T1 and T2 — an NTP correction, the exact event the requirement
is about — must not be folded in as RTT, so a sample with **negative** RTT is discarded. Zero is
not discarded: an injected clock produces it, and it is the tightest possible bracket.

**And the one-sidedness of PB-GW-2 deliberately does NOT transfer.** The gateway's bounded-age
check is one-sided because it is an *anti-replay backstop* whose only adversary can make frames
older but never newer. Neither reason holds here: this is a *measurement*, and a phone 45 s **fast**
is exactly as broken as one 45 s slow. The skew bound is symmetric, and the tests assert both signs.

## The four review rounds — and the rounds ARE the evidence

Every round found at least one real defect in work that had already been reviewed. The pattern is
the argument that the process works, so each is recorded with what it found and what fixed it.

### Round 1/2 (fixed before `582676e` was committed) — four blockers

| # | Finding | Why it survived |
|---|---|---|
| **Reply-seq inversion** | Two producers left unsynchronised on the reply seq bucket — **117 of 500** concurrent appends arrived out of order. A supersede collides them *by construction*, so either the new lease's grant or the severance notice was dropped | Single-producer testing |
| **A keystroke rode a 5-second reconnect wait and was DELIVERED on the new link, returning success** — a direct violation of ADR-007 D7's live-only rule | **The fence asserted on a transport method the facade never calls.** This is the project's standing class (v), and S11 is where it was first named |
| **A relay disconnect severed no lease**, so after a gateway restart the phone typed against a lease the new gateway did not hold | Nothing tested the composition |
| **The clock-skew refusal was self-latching**: the check ran *before* the only thing that records a measurement, so a user who fixed their clock stayed locked out until an app restart | The remedy was gated behind the thing it remedies |

The last one was resolved by **removing the phone-side gate entirely** — "the phone explains, the
daemon enforces". That is a behaviour change, and it forced a spec correction (`4495a0e`): §6.0's
one-line budget row said "reject and surface" without saying *who* rejects, contradicting PB-TIME-1's
own text. Refusing locally at ±30 s was considered and **rejected on merits**: the daemon's accept
band is skew ∈ [-59 min, +60 s] for ordinary commands and [-45 min, +15 min] for `take_control`, so
a phone 45 s out is outside the budget yet **fully served** — refusing locally would refuse commands
the machine would have honoured.

### Round 3 (`c85c210`) — three blockers

**B-1: the daemon-restart lease brick.** The daemon's lease *generation* counter lives only in
memory — its own comment says "monotonic for the Server's lifetime and never resets". A restart
resets it to 1 while the phone had recorded a severance at generation N and refuses anything at or
below N. **The first daemon restart of any session left the keyboard dead and Take Control doing
nothing visible**, and it fired even at N=1. Both arrival orders broke: the reverse order accepted
the grant and then let the late severance kill it.

Fixed by keying the lease on the `take_control` **OperationID** rather than on generation ordering.
The reasoning is worth keeping: the generation is a *within-lifetime ordinal* being asked to serve
as a *lease identity*; the operation id already **is** that identity, and the gateway already
carries it on both the grant and the death notice. Generation stays as the tiebreak within one
lifetime and the fallback when an id is absent. No change to `internal/protocol`.

The fix depended on **one ordering change** that is easy to lose: `sealSignedCommand` recorded the
request *after* the append, so a grant arriving while the append was in flight would find nothing
recorded and fall back to the floor — reintroducing the dead keyboard through a race.
Fenced by `TestS11Lease_ADaemonRestartDoesNotBrickTheKeyboard`,
`_APreRestartSeveranceDoesNotKillThePostRestartLease`,
`_ALateDuplicateOfTheRecoveryGrantCannotResurrectTheLease`.

**B-2: silent mid-line keystroke loss.** The phone allocated a seq, sealed, and appended with
nothing spanning the three steps, so the caller goroutine and the app's own drain timer inverted at
roughly **9%**. The gateway then dropped **both** frames per inversion — the high one as a gap, the
low one as stale — while `MailboxAppend` had returned success, so the undelivered ledger recorded
nothing.

**B-3: a fence a one-line move defeats.** The guard meant to keep the reconnect wait off the
keystroke path asserted on a **spelling** in three function bodies and never followed what it
resolved to. Two mutations pass it, both named in the replacement's header:
`liveSendContext() { return a.resolveSend(a.awaitConn) }` puts the five-second wait back one level
down while every body still says `liveSendContext`; and `Paste` switching to `a.sendContext()`
escapes entirely, because `Paste` appears in none of the three round-1 guards.

The replacement (`TestS11R3_NoLiveInputPathCanReachTheWaitingResolver`) walks an **intra-package
reference graph** and identifies resolvers by result type. **Edges are references, not calls** —
load-bearing, because the connection policy is passed as a *function value*, exactly the shape a
call-only walker is blind to. It carries a guard-on-the-guard
(`TestS11R3_TheCallGraphResolvesThroughIndirection`), since a silently broken walker makes every
"cannot reach" assertion vacuous.

Also fixed: a clock-skew dedupe keyed on a **rendered message** carrying nanosecond jitter, so it
deduped nothing and every reply raised an event. Now keyed on the verdict — the binary fact the
user cares about (`TestS11R3_AConstantClockSkewRaisesOneEventNotOnePerReply`).

### Round 4 (`3d84d53`) — two of round 3's fixes were scoped wrong and the third had a hole

**F-1: the ordering lock covered a slice of the bucket, not the bucket.** `phonecore.Sequencer`
numbers **commands and input frames from ONE counter per epoch** — they share a single
`MailboxReceiver` key, because `SenderKeyID` stays zero — so *every command author is a producer on
the phone->machine bucket*. Round 3's `inputMu` spanned allocate-seal-append in `sendInputFrame`
**only**, while `sealSignedCommand` and `unsignedCommand` drew their seq and appended with nothing
held. The inversion survived the fix.

**And the consequence got worse.** `routeInput` returns nil for a `Gap` frame (an inversion between
two keystrokes loses **both**), but `routeCommand` **executes** a `Gap` frame — so an inversion
involving a command loses exactly the **low** frame. When that is the command, the op is gone with
no signal on either side: `MailboxAppend` returned nil, so the phone shows an op in flight forever —
*a `take_control` that never confirms and leaves the keyboard dead, or a `kill` that never runs.*

The field was renamed `inputMu -> bucketMu` and taken at all three append sites. The round-3 test
file had asserted in its own header that the bucket has "two producers in production"; **that claim
was false, and correcting it at both places it was written is recorded in the commit as the reason
the defect survived a round.**

`TestS11R4_ACommandRacingTypistsNeverReachesTheRelayOutOfSequence` is the producer pair round 3
never drove: three typists against one screen opening and closing its terminal peek, sealing
**80 peek commands** (40 iterations × `{terminal_watch, terminal_unwatch}`). Round 4 measured
**2 of those 80 vanishing** between a successful `MailboxAppend` and the machine.

> The 80 is arithmetic from the test itself (`peeks, len(s11r4CommandActions) = 40, 2`) and is
> checkable. **The "2 of 80" measurement is not preserved anywhere in the repository** — it is
> recorded in the progress doc and the round-4 brief. The durable artefact is the assertion, which
> fails on any loss at all.

**F-2: a foreign detach clobbered a freshly authored `take_control`.** The guard above `severLocked`
was `e.live && namesAnotherLease`, so for a **non-live** entry the attribution test was never
consulted and `severLocked` cleared `e.op` unconditionally. The natural recovery sequence lost the
user's second tap: transport drops (the phone severs itself), the user re-taps because they are
typing into a void, the old lease's in-flight death notice wipes the fresh request, and the
restarted daemon's grant falls back to the generation floor. **Take Control does nothing visible.**
`severNoticedLocked` now clears the request only when the notice is attributable — ids equal, or no
id at all. Fenced both ways:
`TestS11Lease_AForeignDetachDoesNotClobberAFreshlyAuthoredRequest` and
`_AnAttributableDetachStillClearsTheRequest`.

**F-3: the reachability fence never queried from the ROOTS.** It queried only from the resolvers
the roots name, so an `awaitConn()` fallback written **directly inside `SendInput`** passed every
fence in the tree *and* both behavioural disconnect tests — those refuse on the lease gate, before
the resolver is consulted. The walker had already recorded the edge. The fence now asserts
whole-root for every live-input path, with `ReleaseControl` exempted **by name and reason** rather
than by a second list that can drift.

**The round-4 mutation evidence, verbatim from the commit**: "the fence hole is shown by inserting
the decoy, observing the whole `./mobile/...` suite stay green, then failing the strengthened
fence."

**And a recurrence fence, because the same defect had now been found twice.**
`TestS11R4_EveryBucketAppendAllocatesItsSeqUnderTheBucketLock` reads the **source**: every facade
append site must draw `NextCommand`/`NextInput` under `bucketMu`, and in that order. Its header
states why a behavioural test is not enough: both times, the test that caught the inversion had to
drive the exact producer pair involved, and both times the pair that was *not* driven stayed
broken. The property is not "these two producers are serialised" — it is "every append on this
bucket draws its seq inside the section that appends it", which is a statement about the source.

**Measured cost of the critical section**: ~1.9 µs per keystroke frame and ~13.4 µs per 4 KiB paste
(round 3); 2.2 µs for the command sealer, input half 1.5 µs (round 4) — against PB-NET-5's 150 ms
budget, and only when the producers collide. Pinned by
`internal/phonecore/s11r4_sealcost_test.go`.

## Gates, re-run at HEAD 2026-07-25

`go test -count=1 -run 'TestS11' ./internal/phonecore/ ./internal/remotegw/
./internal/remote/transport/ ./mobile/ ./mobile/conformance/` — **68 tests, all PASS, five packages
ok, zero failures.**

| Package | S11 tests | Result |
|---|---|---|
| `internal/phonecore` | 48 (coalescer 11, lease 13+3, reply-age 9, skew 10, severance 2) | ok 0.95 s |
| `internal/remotegw` | 8 (reply stamp, six severance, reply-seq order) | ok 1.26 s |
| `internal/remote/transport` | 4 (retry policy table) | ok 1.24 s |
| `mobile` | 11 (5 wiring, 2 reachability fence, bucket-lock fence, 3 live-send) | ok 3.59 s |
| `mobile/conformance` | 6 (input order, skew report, command order, disconnect, retake, paste) | ok 27.5 s |

The behavioural concurrency tests are the slow ones and they ran green under concurrent agent load:
`TestS11R4_ACommandRacingTypistsNeverReachesTheRelayOutOfSequence` 7.85 s,
`TestS11R3_ConcurrentInputNeverReachesTheRelayOutOfSequence` 6.25 s.

> S11's own gates were green across phonecore, remotegw, transport, mobile and conformance
> **immediately before S14a's change landed**, and the progress doc records those as the real
> state of S11. The re-run above supersedes that: they are green at HEAD, after S14a, S14, S10 and
> S12.

## Accepted residuals, each with its owner

- **PB-INPUT-4 is enforced nowhere in production.** `transport.RetryFor` **and**
  `transport.SendLive` both have **zero production callers** — re-verified at HEAD: the only
  references outside `_test.go` files are their own declarations and one doc comment. The facade
  appends through `relay.Client.MailboxAppend` directly. So the retry table is fully tested and
  drives nothing, and `TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing`
  (`transport/s6b_input_test.go`) — *the very fence whose blindness let the B-2 defect ship* —
  remains a fence on a path production does not take. Round 4 neither retired nor re-pointed it.
  **Not currently harmful**: "never blind resend" is trivially satisfied because nothing resends,
  and D7 is structurally enforced by `sendCoalesced` never re-buffering a failed frame. The hazard
  is the next slice adding a resend without consulting the table.
  **Owner: whoever next touches the transport send path — delete the fence or re-point it.**
- **The clock verdict never clears and has no pull surface.** `mobile/relay.go`'s
  `if !changed || msg == "" { return }` emits nothing on the transition back to healthy, and the
  golden has no clock verb. A screen opened after the event — or after the user fixes the clock —
  cannot learn the current verdict. This is the same latch the round-1 fix removed from the command
  path, re-created one layer up in the UI, and it is inconsistent with this round's own
  `UndeliveredInputs()`, added expressly as "the matching pull surface for a screen that opens
  afterwards". **Owner: S16.**
- **A phone more than 10 minutes FAST goes silently deaf to the entire machine->phone plane.**
  `snapshot.go` is one-sided against `InboundMaxAge` on the *phone's* clock, so every reply, journal
  record and snapshot returns `ErrStaleAge`; `mobile/relay.go` swallows it with no stale mark, no
  event, and `ConnectionState()` still reads "online". The skew feature cannot fire, because it
  needs an **opened** reply. Not a regression — it follows from PB-TIME-2/S7b's deliberate
  one-sidedness. **This is the third clock wall** (30 s surfaced, 60 s refused opaquely, 10 min
  deaf) and the only one that was undocumented. **Blocked on** the PB-TIME-2 reply-seal gap; the two
  must land together.
- **The daemon's `OpLease` grant carries no `ExpiresAt`.** `internal/protocol/server.go` encodes
  Op/EndpointID/SessionID/Generation/SnapshotLen, while the deadline the daemon actually enforces is
  the earliest of three bounds. The phone therefore **cannot observe the authoritative expiry**.
  S11 takes the machine's value when a confirmation carries one and otherwise falls back to the
  horizon the phone signed — an upper bound on the truth, so the phone can only ever believe the
  lease ends *later* than it does, never earlier, and the severance notice (not a countdown) stays
  the authority. Changing a daemon reply was outside S11's fence, so the precedence is pinned by
  `TestS11Lease_TheMachinesExpiryWinsOverThePhonesSignedHorizon` to stop a later slice wiring the
  real value through and having it silently ignored. **Wants a follow-up.**
- **PB-TIME-1's distinct explanation never reaches the outcome plane.** `server.go` replies "device
  command not authorized" and discards `authorizeCommand`'s specific "command expired", so the
  distinct reason reaches the user only on the phone **event** plane. Closing it needs a
  distinguishable expiry code from the daemon, which is **not authorised in Phase B** (`4495a0e`).
- **`replyMu` is held across a relay append with a 10 s ceiling** (`lease_confirm.go`). A wedged
  relay lets one severance notice head-of-line-block every lease confirmation and command reply for
  up to 10 s. Correct given the ordering the reply-seq fix requires, and not on the keystroke path,
  but new in this slice. **Watch it if reply latency ever matters.**
- **The undelivered-input ledger is unbounded.** `coalesce.go` appends forever, `Undelivered()` is a
  read and not a drain, and the facade has no clear verb. A minute of autorepeat against a dead
  lease retains roughly 1800 entries, and `UndeliveredInputs()` copies the whole slice per call.
  **Owner: S16** — and S16's RED tests for exactly this are in the working tree now.
- **The reachability walker resolves `a.foo` against the receiver's own identifier**, so aliasing the
  receiver (`self := a; self.awaitConn()`) and method expressions (`(*App).awaitConn(a)`) evade edge
  recording. Recorded in the fence's own comment rather than chased: an *accidental* variant of
  either also loses the connection reference and trips the "cannot reach conn" non-vacuity
  assertion, so only a deliberate decoy evades both — and a fence's job is to stop regressions, not
  authors.
- **PB-NET-5's latency harness does not measure the shipped input path**, so nothing times the
  facade layer where the coalescer, the lease gate and the bucket lock now live. Found by the round-3
  implementer while proving its lock had not blown the budget. The facade's added cost was measured
  separately (above) and is negligible; **the gap is coverage, not a known regression. Owner: S19.**
  See `remote-phaseB-s6b-evidence.md`.
- **The lease is deliberately never persisted.** A lease is a live daemon connection; a lease
  restored from disk is by construction a lease the machine does not hold — the "assume the lease"
  failure PB-INPUT-2 forbids. So process death is fenced by asserting the lease is **absent from
  durable state** (`TestS11Lease_IsNeverDurable`), not by asserting it is cleared on load.
