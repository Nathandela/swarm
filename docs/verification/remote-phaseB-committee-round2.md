# Phase B — audit committee, round 2

**Verdict: REVISE. No member signs off. Round 2 found MORE than round 1, and found it in the FIXES.**

## The one-sentence result

Round 1 found that two residuals, each defensible alone, were critical together. **Round 2 found that
the fixes compose the same way** — and that one fix deleted the only recovery path for the hole it was
adjacent to.

## Consensus

1. **The remediation is real where it was checked.** All five round-1 claims bite under adversarial
   mutation, verified independently: five dial sites reverted individually, consent verification
   bypassed, the memory drop skipped, the sealed key destroyed, a routed state removed, the
   destructive ack restored. The manifest checker's negative controls — which round 1 found had
   **never run** — now run and fail against the real files.
2. **The loopback carve-out held** under a thorough attack by two members: userinfo confusion,
   decimal/hex/octal literals, IPv4-mapped IPv6, hostname resolution, redirects, and whether the
   websocket library replaces the caller's redirect policy. **Recorded as a clean negative.**
3. **The handset pin IS consulted at the dial site.** My stated worry that the defect class had been
   reintroduced was unfounded.
4. **B44 does not worsen B40** — checked independently by two members rather than inherited.

## Divergence, and what one member caught that three missed

- **Only one member asked what the DELETIONS depended on.** That produced B49: the ban is global,
  only the banner lifts it, and the consent fix removed the counterparty-side lift — so every revoke
  is now unrecoverable by either party. **Three members audited the same code and did not ask that
  question.**
- **Two members reached the pairing-dial circularity independently**, by different routes, and one
  deferred to the other's analysis as better rather than defending its own.
- **One member audited a different tree than the others** — the working tree was dirty mid-edit — and
  **said so first**, then showed which half of its objection survived the difference. That disclosure
  is why its headline is trustworthy.

## Blind spots — still unasked by anyone, including me

- **Nothing checks that a fix does not delete something another defect's remedy depends on.** B49 is
  the second instance of exactly this (B24 -> B25 was the first, eleven entries earlier). I recorded
  that lesson and then repeated it.
- **Nothing re-derives a REMEDY when the code changes.** B40's recorded fix was falsified by a
  symmetry that predates it; B47's is inseparable from B22's ban-lift. Both would have tested green.
- **Nothing reads prose.** A fourth invalidated instance was found in an *evidence file* describing
  behaviour a later fix deliberately reversed, and two committed artifacts still state a bootstrap
  premise that is false.

## Per-member signal

- **Composition reviewer** — the headline (B49), B40's falsified remedy, B47's inseparability from
  B22, self-consent leaving B39 untouched, the sticky-pin composition, and the ninth uncalled-symbol
  instance. The most findings and the most severe.
- **Threat reviewer** — measured B46 end to end with a vacuity control, falsified the reachability
  bound in three joined pieces, found the fifth "names a mechanism" entry, and **corrected its own
  round-1 claim**.
- **Verification reviewer** — mutation-tested every remediation claim, caught itself about to report a
  false gap and corrected before reporting, and found the stale-evidence instance.
- **External reviewer** — produced the consent-ordering defect as an executable test rather than an
  argument. **Its run terminated early on its host's content filter; its report is partial and is
  recorded as partial.**

## Verdict

**REVISE.** One critical unrecoverable-revoke defect with two entry points, two requirements newly
NOT MET, three falsified remedies, and a ninth uncalled-symbol instance. **Round 3 is required.**

The instruction for round 3 writes itself from the blind spots: **audit what each fix DELETED or made
unreachable, and re-derive every recorded remedy against the current code.** Both classes test green
by construction.
