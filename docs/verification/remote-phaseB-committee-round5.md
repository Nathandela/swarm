# Phase B audit committee — round 5

**Convened** 2026-07-30 against the round-4-remediated tree. **Verdict: REVISE.**
**Split 3–1 on the closed test.** Unanimous that production is not ready.

## Members and emphases

| Member | Emphasis | Verdict |
|---|---|---|
| **GPT-5.6 sol (codex)**, external | Open | **Not a valid closed-test candidate**; production no |
| **Opus** | The crypto — untested in four rounds | Closed test yes; production no |
| **Sonnet** | Row-by-row audit | Closed test yes; production no |
| **Fable** | Compositions and fences | Closed test yes; production no |

## The headline: the crypto holds

Eighteen attacks written and **run**, not read. A ten-position mutation battery on a real frame left
only the one field documented as unauthenticated surviving. 20,000 seals produced 20,000 distinct
nonces. Five malformed key configurations refused by name. A live two-leg handshake through a
**tampering** relay leaked neither identity key nor any plaintext across 332 observed bytes. Grant
replay, relay-signed forgery, coordinate promotion and cross-device misdirection all refused.

The external reviewer independently mutated five mechanisms and every fence bit. **Both concluded:
no primitive-level forgery. The failure is at the callers.** A five-round-old gap is closed with a
null result — which is what a null result is for.

**And all thirteen round-4 fences are real**, reverted one at a time, each failing for the right
reason. The vacuous-fence replacement was verified end to end: under the deadline-deleted mutation
the **old** suite still passes while the **new** fences fail. **Round 4 replaced a fence that could
not fail rather than adding one beside it** — asserted twice in this record, now measured.

## Consensus

1. **A CRITICAL in the callers.** Nothing in the mailbox AAD binds **direction**; both legs shared a
   key and a replay bucket. The relay reflects one **observed** frame onto the other leg, the AEAD
   verifies, and the receiver's high-water advances — after which every genuine frame is stale. The
   frame is refused a layer later, but **the damage is the advance, not the rejection.** Reproduced
   independently by two members. **Remediation in flight, uncommitted.**

2. **Durable state is economically unbounded on a public relay.** Both remedies the committee has
   debated are now measured as insufficient — including its own recommendation, refuted by
   manufacturing 50 relationships from 100 minted identities.

3. **`PB-PAIR-4` is structural**, and the adversarial form is **deterministic**: a relay bit-flip in
   the decision frame produces a half-pair every attempt, then the machine refuses further pairing.
   **Relay-triggered lockout.** Not closable by timeout tuning.

4. **The count is not defensible as a single number.** It moved from 139 to **133 of 144** during the
   round, on evidence, every movement recorded.

## Divergence — recorded, not resolved

**The external reviewer dissents on the closed test** and its grounds are **tree hygiene, not
defects**: the critical fix is uncommitted, *"there is no single exact source snapshot for which all
gates are presently evidenced"*, and push cannot work from this build. **That objection is correct
and lands on the orchestrator** — gates were run repeatedly at moving HEADs with several agents'
uncommitted work in the tree, and reported green. Each run was honest about what it measured; **none
was a release candidate.**

It is the most *actionable* objection of the round rather than the most damning: both grounds are
answerable.

## What this round taught the committee

**A fifth instrument**, and the first to arrive with a mechanical tell and a fix shape:

> **The quantifier is dropped between the requirement and the fence.** When a requirement's subject
> is a channel or an external observer, the property is quantified over everything reaching it. When
> the fence's subject is a component, every other producer is unfenced **by construction**.
> *Tell:* compare the grammatical subject of the requirement with that of the fixture.
> *Fix:* enumerate the producers, so a new one **fails the enumeration** rather than slipping past.

**And a new class of count error.** Every movement in five rounds was a row that was **wrong**. Round
5 found one that was **absent** — nothing in the specification made the channel binding attest the
accept/decline exchange, so three green rows in that family could never have caught it. **The
denominator was incomplete, and re-deriving existing rows can never find that.**

## Blind spots

- **Coverage is 23 of 144 rows deep-derived (16%).** Whole families remain untouched by any deep
  pass. **Every tranche anyone has re-derived has produced a finding**, so the count should be read
  as provisional, not settled.
- **Nobody has checked what a stolen handset yields.** The device-loss properties are under
  examination now, using the new instrument.
- **No timing analysis, no constant-time review, no fuzzing proper.** The crypto pass was
  hand-chosen mutation positions.
- **No Android or gomobile attack surface work** by any member, in any round.

## Verdict: REVISE

**Closed test on a private, owner-operated relay: three members yes, one no.** The dissent is
answerable — commit the fix, evidence the gates on that exact commit, configure push. Two conditions
all members agree on: **do not expose the relay**, and **document that a corrupted final pairing
frame from any cause needs a desktop revoke to recover**, because testers will hit it.

**Production: unanimous no.** Blocking: the direction binding; aggregate relay admission rooted in
something unmintable; an acknowledged final pairing frame (which closes `PB-PAIR-4` and the newly
added `PB-SAS-4` together — **one protocol change, not two**); the biometric gate's three open
halves; the unwired app surfaces; `PB-E2E-5` on real hardware, which first requires making it
runnable; and a single immutable commit with every gate evidenced against it.

> **AMENDED 2026-07-31 (ADR-007 B133) — one blocker in this list is void and one has narrowed.**
> *"The biometric gate's three open halves"* is **VOID**: the gate is deleted, PB-SEC-2's subject
> has left the product, and nothing is owed on it. *"`PB-E2E-5` on real hardware"* stays blocking
> and **narrows by one item** — real biometrics leaves its scope with the feature; real camera,
> real FCM, Doze and hardware Keystore attestation stay in the gate and stay UNRUN. The other five
> blockers are unaffected, and no vote recorded here is re-cast.

## The trajectory, stated honestly

Rounds 1–4 each found more than the last. **Round 5 found less that is new and more that is
structural** — the crypto held, the fences held, the gateway family came back clean, and the
remaining blockers are increasingly *design* rather than *defect*. That is what progress looks like
here, and it is still not agreement.
