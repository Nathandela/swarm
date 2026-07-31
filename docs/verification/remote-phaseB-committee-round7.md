# Phase B audit committee — round 7

> **AMENDED 2026-07-31 (ADR-007 B133) — one of the objections recorded below no longer has a
> subject.** GPT-5.6 sol's NO cites two grounds: *"The dial handshake is unbounded"* and
> *"PB-SEC-2 is still not cryptographically enforced"*. **The first stands.** The second is now
> **VOID rather than open**: the trust boundary has moved to the wire, all phone-side user
> authentication is removed with its code, and a requirement whose subject has left the product
> cannot be met, failed, or reopened by a later slice.
>
> **This does not convert the verdict.** No committee vote is re-cast here, and the round's REVISE
> stands as recorded; what changes is only that a reader must not go looking for open work behind
> the PB-SEC-2 clause. Everything else in this round — the gateway redial defect, the unbounded
> dial handshake, staleness by silence, the enumeration residuals — is untouched by B133.
>
> The counts below (**131 of 144, 11 NOT MET**) are the round's own measurement and are left as
> they were taken. The current numbers are in `docs/verification/remote-phaseB-traceability.md`,
> which now records PB-SEC-2 as void inside the NOT-MET bucket because that report has a single
> override dict — the honest reading of its count is *n* not met plus one void.

**Convened** 2026-07-30 against the round-6-remediated tree. **Verdict: REVISE.**
**Closed test: 1 YES, 3 NO. Production: unanimous NO.** Seven rounds, seven REVISE verdicts.

## Members and verdicts

| Member | Emphasis | Closed test | What it uniquely contributed |
|---|---|---|---|
| **Composition** | Do round 7's fixes compose? | **YES** | Two agents' PB-PAIR-4 fences, written 23 min apart and blind to each other, both catch the same reverted ordering |
| **Adversary** | Be the relay; measure | **NO** | The gateway dials once per process and never redials — a WiFi blip permanently ends remote control |
| **Denominator** | Unexamined green rows | **YES, one repair first** | Staleness by silence: the cheapest attack in the system, forbidden by no row |
| **GPT-5.6 sol**, external | Open | **NO** | The dial handshake is unbounded; PB-SEC-2 is still not cryptographically enforced |

**The single YES was cast before the gateway-reconnect evidence existed**, and its author's own
production objection anticipated the class.

## The headline: the count fell, because reviewers were pointed at GREEN rows

**131 of 144. 11 NOT MET. 2 hardware-deferred.** Round 7 opened at 134.

**Eight `PB-BIND` rows — all green, never examined in six rounds — produced four findings**, including
a second reverse-bound key-crossing interface passing both custody fences, and "drop-oldest" stated in
three places and enforced by none. **Eighteen `PB-SYNC`/`PB-STATE` rows produced three more**, though
15 of 18 came back genuinely clean.

**That is seven consecutive re-derived tranches producing findings, seven for seven.** The remaining
~70 green rows are **unexamined, not known-good.**

## Consensus

1. **Four CRITICALs, all the same shape: nothing bounds or observes a dead link.** Non-wait exchanges
   (fixed round 6), `MailboxWait` (fixed), the **dial handshake** (unbounded, fix in flight), and the
   **gateway's absent reconnect** (fix in flight). Each was found by probing one site at a time.
2. **The dominant defect class is the dropped quantifier.** `PB-NET-4`, `-5`, `-6`, `-7` each had more
   clauses than the defect reported against them, and **the unfenced clause was never the one anyone
   had complained about.**
3. **Two requirements that SHOULD exist and do not.** The gateway's recovery mechanism, and a bound on
   the age of what the phone presents as live. **Neither is a row falsely marked met.**
4. **Production is not close**, unanimously.

## The finding that changes the method

**Staleness by silence.** Every staleness mechanism keys on a **gap**, and a gap is observable only
when a *later* seq arrives. So the relay's cheapest move is to **withhold the tail and keep answering
polls**. No gap, nothing stale, the poll succeeds — and `Presence()` asks **the relay** whether the
machine is alive. The phone shows arbitrarily old state as live, indefinitely.

**Section 6.0 already specifies the fix** — *"5 min without a successful poll"* — and `grep` returns
**zero hits outside the requirements file.** The staleness decision has **no clock input at any
layer**, verified. `IssuedAt` is AAD-covered on every inbound frame and consumed for nothing.

> **And nothing checks section 6.0's budget table for an owner or a fence.** Requirement IDs are
> checked against the index; **budget rows are checked against nothing.** A binding number that is
> never implemented needs no committee agreement to ignore — the opposite of what section 6.0 says
> about itself.

## What this round taught the committee

**Two new instruments**, both about the *auditor* rather than the artifact:

> **Instrument 8 — the mutation moves a constant the test transcribes.** It proves the test reads the
> constant, never that production uses it. **Mutate the connection, not the value.** I accepted such a
> proof this round while demanding mutation proofs from everyone else.

> **Instrument 9 — an enumeration cannot find an absent mechanism.** *"What is missing is a recovery
> mechanism, not a timeout."* I commissioned a deadline enumeration and would have read it green as
> closing the class. It would have closed **the class I had been probing, not the class that was
> there.**

**And the method critique the committee converged on independently:** the specification is rigorous
about clauses it has written down and **has no instrument for mechanisms it never named.** Both missing
requirements were found by asking *"what does the adversary do that raises no flag anywhere"* — neither
came from re-deriving a row.

**Adopted for round 8: an adversary-capability enumeration** — order, timing, withholding, retention,
duplication, sizes, connection lifetime — **mapped against rows**, rather than more rows.

## Blind spots

- **~70 green rows still unexamined**, and the per-tranche finding rate has not fallen.
- **The stolen handset is mostly HARDWARE-BLOCKED**, reported per property, with nothing written that
  pretends to cover real biometrics, Keystore attestation, or locked-device push.
- **`PB-E2E-2`/`PB-E2E-5` cannot run**: the build deliberately omits the Firebase plugin, so the
  physical gate cannot receive real FCM, and its runbook contradicts two current requirements.
- **`PB-STATE-10`** was read and not mutated — counted **unexamined, not clean.**

## Verdict: REVISE

**Closed test: NO**, on the gateway reconnect specifically. The failure mode is the worst available for
early testers — the phone reconnects and reports online while the machine is silently and permanently
dead, and **nothing is in any log, because all three components that store the degraded state have zero
readers.** Testers would report *"it worked and then it stopped."* Fix dispatched; it is small, and the
phone already has the loop the gateway lacks.

**Production: unanimous NO**, unchanged in kind and sharper in detail.

## The trajectory, stated honestly

**Round 7 found more than round 6, and the count went down.** That is not regression — it is the first
round in which reviewers were pointed at rows that were **green**, and at the threat model rather than
the specification. Both moves paid immediately.

The uncomfortable symmetry with round 6 holds: **four of that round's nine false rows were closed by my
own adjudications; this round's two most instructive findings are instruments aimed at my own
verification method.** I accepted a mutation proof that proved the wrong proposition, and commissioned
an enumeration that could not have found the defect that prompted it. **Both were caught by reviewers I
had told to assume the brief was wrong.**
