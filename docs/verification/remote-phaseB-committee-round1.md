# Phase B — audit committee, round 1

**Verdict: REVISE. No member signs off. The count moved 142 -> 136 DURING the audit.**

Four reviewers, deliberately different assignments so they could not converge on the same easy
findings. A fifth (Gemini) was convened twice and returned nothing both times — blocked on filesystem
permissions before reading anything — and was replaced rather than reported as a fourth voice.

## Consensus (2+ members, treat as real)

1. **The trust-on-first-use residual is anonymously reachable.** Two members derived it independently
   by **different routes**: one via a passive observer of a cleartext dial reading the relay-auth
   pubkey from `auth_init`; one via pairing msg3 carrying the device's key *before* the SAS check, so
   a rejected SAS still leaks it. A third found msg2 leaking the **machine's** key before the desktop
   confirm exists. Three doors to one room. **ADR-007 B37, then B38.**
2. **Transport security has no production caller** (PB-NET-2). Unanimous where examined.
3. **The lock purge and the freshness gate are absent, not merely unverified** (PB-KEY-7, PB-SEC-2),
   and one evidence file asserted the opposite in a sentence the index counted as evidence.

   > **AMENDED 2026-07-31 (ADR-007 B133).** The **freshness gate** half of this finding is VOID:
   > there is no timed tier, no per-use tier and no gate, and PB-SEC-2's subject has left the
   > product. The **lock purge** half survives NARROWED — PB-KEY-7's purge mechanism is shipped and
   > its trigger moved from screen lock to revoke or unpair. The finding's real lesson is untouched
   > and is why round 1 mattered: an evidence file asserted the opposite of the code, and the index
   > counted the assertion.
4. **The traceability count is not readiness evidence** — evidence is measured per *slice*, and the
   strengthened fence is a substring check. Both members who examined it said so unprompted.

## Divergence, and where one caught what the others missed

- **Only one member measured the composition.** The others found B27 and B34 separately and judged
  each defensible; one asked what they do *together* and found an unauthenticated permanent DoS. That
  is the finding of the audit, and it is a question no single-residual review asks.
- **One member falsified my remedy within two hours of my writing it**, with a negative control — the
  `unless` clause in B37 is false because the disclosure survives `wss://`.
- **One withdrew a finding after checking it** (PB-KEY-4, initially reported as a second
  invalidated-by-another-fix instance, retracted when rotation was found to work through the
  bootstrap path). That withdrawal raises my confidence in its other findings more than another
  finding would have.
- **One returned a substantive negative**: it enumerated every Kotlin interface, every Go interface,
  every nilable gateway seam and the Android manifest, and found nothing further. A clean negative
  from an exhaustive read is a result; I have recorded it as one.

## Blind spots — asked by nobody, including me

- **Nothing asks what two accepted residuals do in combination.** Every residual here was judged
  against the threat model; none against each other. Three composing pairs are now known
  (B27xB34, B29xB27, §2.2xPB-STATE-10's fail-fast) and all three were invisible to single-residual
  review.
- **Nothing re-derives a requirement when a *different* requirement's fix changes its mechanism.**
  Two confirmed instances (PB-KEY-7 killed by PB-KEY-10's fix; PB-NET-4 killed by the connection
  model). Both read `shipped` for the rest of the phase.
- **Nothing checks that an amendment's new criterion is met on the surface it moved to.** PB-PAIR-5
  is the clean case: I retired one state and substituted another, and the app still declares the
  retired one.

## Per-member signal

- **GPT-5.6 sol (external)** — the exploit chain, end to end, as a *composition*: `ws://` -> observed
  pubkey -> self-pair -> revoke. Also the sharpest verdict line: "'every gap is recorded' is not
  enough when the traceability table simultaneously calls those gaps shipped."
- **Fable** — the deepest measurements, with overlay tests and negative controls: the QR-photographer
  path that survives TLS, 18 metered calls against a limit of 4 via re-auth, 200 minted victims
  persisting after disconnect, and a paired phone permanently banning its own machine.
- **Opus** — the requirement-level failures the others' focus could not reach: PB-PAIR-5's unrendered
  state, PB-NET-4's unreachable queue, four more uncalled bound symbols, and the gobind
  acronym-mangling flaw in the ledger built to catch exactly that class.
- **Sonnet** — the apparatus: which checkers can actually fail (several genuinely can), the false
  sentence inside a counted evidence file, and the PB-SEC-11 tension that makes the obvious lock-purge
  fix illegal.

## What held up

`golangci-lint` at 0 and the Go suite green were reproduced independently by two members. The
manifest checker's 13 mutation controls, the coverage fence's negative control, and B28's
fail-closed record versioning were each verified as genuinely capable of failing. B30's `Save`/`Mutate`
split, B24's banner-scoped ban clear and B32's token-deletion durability were confirmed implemented
as argued. No second blind-adopt path was found.

## Verdict

**REVISE — Phase B is not production-ready.** One critical unauthenticated remote DoS with three
disclosure paths, six requirements marked shipped that are not met, and an exit-criteria set that
does not establish readiness. **Round 2 is required**, and every member must re-audit against the
composition question rather than the requirement list, because the requirement list is what missed
all of this.
