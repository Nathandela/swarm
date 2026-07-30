# Phase B requirement traceability

**GENERATED — do not edit by hand.** Regenerate with
`python3 scripts/phaseb-traceability.py > docs/verification/remote-phaseB-traceability.md`.

The final audit validates against every REQUIREMENT, not every slice. This is the per-row
view: owner, whether that owner has shipped, and where the evidence is.

**READ THE TWO COUNTS BELOW DIFFERENTLY — they have different provenance.** *Shipped* is
the orchestrator ASSERTING that a slice landed and gated; it is maintained by hand in
`scripts/phaseb-traceability.py` and no code checks it, so it is exactly as reliable as
that bookkeeping. *Evidenced* is MEASURED: the evidence file is on disk. A requirement
counted as shipped but not evidenced has no durable record an auditor can read, and the
gap between the two numbers is the honest size of what is asserted rather than shown.

**AND *EVIDENCED* MEANS THE FILE EXISTS, NOT THAT IT IS CURRENT** (ADR-007 B67). An
evidence file is a claim about the commit that produced it. Files that declare themselves
partly superseded are listed below and their claims must be read against that notice, not
against HEAD.

| | count |
|---|---|
| Requirements | 144 |
| Shipped (asserted by hand) | 133 |
| Evidenced (measured on disk) | 133 |
| **NOT MET (slice shipped, requirement invalidated later)** | **9** |
| Remaining | 2 |
| **Shipped with NO evidence file** | **0** |

## Evidence files carrying a dated correction, amendment or withdrawal

These count as *evidenced* above and mostly ARE — the flag says the file contains a
dated correction, not that it is untrustworthy. **Read the correction before citing
the passage it touches.** Two of these (S17, S18) were genuinely overtaken by a later
decision and carry a superseding banner; the rest carry honest inline corrections,
which is the record working rather than failing. See ADR-007 B67(1) and B79.

- **S10** — cited for 10 requirements: PB-KEY-10, PB-KEY-3, PB-KEY-4, PB-SYNC-1, PB-SYNC-2, PB-SYNC-3, PB-SYNC-4, PB-SYNC-5, PB-SYNC-6, PB-SYNC-8
- **S14** — cited for 8 requirements: PB-KEY-1, PB-KEY-2, PB-KEY-5, PB-KEY-6, PB-KEY-7, PB-KEY-8, PB-SEC-1, PB-SEC-2
- **S17** — cited for 2 requirements: PB-PUSH-4, PB-PUSH-9
- **S18** — cited for 10 requirements: PB-SEC-11, PB-SEC-12, PB-SEC-13, PB-SEC-14, PB-SEC-3, PB-SEC-4, PB-SEC-5, PB-SEC-6, PB-SEC-7, PB-SEC-8
- **S20** — cited for 8 requirements: PB-DOC-2, PB-DOC-3, PB-DOC-4, PB-DOC-7, PB-OPS-1, PB-OPS-2, PB-OPS-3, PB-OPS-5
- **S18b** — cited for 1 requirement: PB-STATE-10

## Every requirement

| Requirement | Slice | Status | Evidence |
|---|---|---|---|
| PB-APP-1 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-2 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-3 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-4 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-5 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-6 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-7 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-8 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-9 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-APP-10 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-BIND-0 | S1 | shipped | `docs/verification/remote-phaseB-s1-evidence.md` |
| PB-BIND-1 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-BIND-2 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-BIND-3 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-BIND-4 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-BIND-5 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-BIND-6 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-BIND-7 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-DOC-1 | S0 | shipped | `docs/adr/ADR-007-remote-access.md` |
| PB-DOC-2 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-DOC-3 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-DOC-4 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-DOC-5 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-DOC-7 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-E2E-1 | S19 | shipped | `docs/verification/remote-phaseB-s19-evidence.md` |
| PB-E2E-2 | S21 | pending | — |
| PB-E2E-3 | S19 | **NOT MET** | DEFINED DOWN by my own restatement (B93). It claimed RED-first is evidenced by a committed failing state, and its three cited exemplars contain ZERO lines of actual failing output -- verified, grep returns 0 on all three. They carry PROSE NARRATING failures, which is exactly what the restatement claimed to replace. And 26 slices landed implementation and tests in one commit, not the 4 the row names (ADR-007 B94) |
| PB-E2E-4 | S19 | shipped | `docs/verification/remote-phaseB-s19-evidence.md` |
| PB-E2E-5 | S21 | pending | — |
| PB-GW-1 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-2 | S7b | shipped | `docs/verification/remote-phaseB-s7b-evidence.md` |
| PB-GW-3 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-4 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-5 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-6 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-GW-7 | S2b | shipped | `docs/verification/remote-phaseB-s2b-evidence.md` |
| PB-GW-8 | S2b | shipped | `docs/verification/remote-phaseB-s2b-evidence.md` |
| PB-INPUT-1 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-INPUT-2 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-INPUT-3 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-INPUT-4 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-INPUT-5 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-INPUT-6 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-KEY-1 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-2 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-3 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-KEY-4 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-KEY-5 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-6 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-7 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-8 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-9 | S14a | shipped | `docs/verification/remote-phaseB-s14a-evidence.md` |
| PB-KEY-10 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-LIFE-1 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-2 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-3 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-4 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-5 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-6 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-7 | S4b | shipped | `docs/verification/remote-phaseB-s4b-evidence.md` |
| PB-NET-1 | S9 | shipped | `docs/verification/remote-phaseB-s9-evidence.md` |
| PB-NET-2 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-3 | S6 | **NOT MET** | UNFENCED, NOT DISPROVEN -- and the distinction is the point. Every fence for it lives in internal/remote/transport, which has no production caller: opaque_test.go taps the wire of the DEAD Session, and the structural arm reflects over transport's own types, saying nothing about relay.Client or mobile. The property itself (a sealed payload's plaintext never reaches the wire) appears TRUE of the shipped phone -- sendInputFrame seals before it appends and there is no raw-append path -- but that is read, not measured. The nearest live candidate fences transport POLICY (ws vs wss), not payload plaintext. Needs a wire tap over the SHIPPED path (ADR-007 B98) |
| PB-NET-4 | S6 | **NOT MET** | marked met by MY OWN adjudication (B90), which asserted the resilience half is "implemented and fenced". Section 6.0's backoff numbers -- initial 500ms, factor 2, ceiling 30s, jitter +/-20% -- exist ONLY in internal/remote/transport, which has zero production callers. Shipped reconnects are fixed-delay with no growth, no ceiling and no jitter; setting the shipped delay to 3h leaves every fence passing (ADR-007 B94) |
| PB-NET-5 | S6b | **NOT MET** | DECOMPOSES, and I refuted it wrongly once already (B98) by checking the numeric clause and not the quantifier. The criterion clause (p50 150ms phone Type -> PTY write) and the drop-the-gateway-poll clause ARE fenced on live code. But the requirement says BOTH HOPS, and the PHONE hop's fence is transport/s6b_input_test.go -- all six tests driving the dead Session.Follow, which is the only phone-side MailboxWait caller in the tree and has zero production callers. The shipped phone does not follow, it POLLS: mobile/app.go pollInterval = 500ms. So what shipped is a GATEWAY-SIDE-ONLY fix -- the exact mirror of the phone-side-only fix this requirement's own text warns would fake the criterion. UNFENCED, not disproven: the shipped poll plausibly avoids head-of-line blocking by a cruder mechanism, but nothing measures it, and the echo direction it gates is outside the numeric criterion by construction (ADR-007 B100) |
| PB-NET-6 | S6 | **NOT MET** | DECOMPOSES, and one clause has no live subject at all. Replay-refused-across-restart and durable-cursor-survives-restart do have live equivalents in phonecore (ErrStaleSeq, RelayCursor). But HOSTILE PAGINATION TERMINATES is fenced only by ErrStuckPage, which exists nowhere but the dead package; the shipped App.drain substitutes a weaker progress-conditioned throttle that is fenced as no termination property anywhere. Deleting the dead code without writing that fence converts a misaimed fence into no fence (ADR-007 B98) |
| PB-NET-7 | S6 | **NOT MET** | EVERY NAMED CLAUSE IS NOW FENCED OVER LIVE CODE, and ONE QUANTIFIER RESIDUAL keeps it red pending round 7 -- flagged for the committee rather than adjudicated here, because closing rows on my own partial reads is how four of this round's nine false rows happened. Bounded at the committee's 10 s, pinned by a test transcribing the value from section 6.0's table rather than from the constant (2ae1386). Cancellation honoured on BOTH the generic request and the dial, restored after the dead-package deletion took them -- mutation shows a 300 ms context taking 30.009 s without it (056f1af). No goroutine leak across 12 connect/disconnect cycles, the only such assertion in the remote stack (2ae1386). Calls after close: typed refusal on both arms, plus Close idempotency, which a shared fixture had been exercising on every run while nothing named it (2727f0d). THE RESIDUAL: the row says 'timeouts EVERYWHERE' and nothing ENUMERATES the call paths -- the bound sits on roundtrip, which every non-wait call takes, and MailboxWait bypasses it by construction under the relay's own 25 s ceiling. Sound by ARGUMENT, unfenced -- residual 4.23's shape, and the committee has refused that argument twice this round (ADR-007 B99, B105, B109) |
| PB-OPS-1 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-2 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-3 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-4 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-OPS-5 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-PAIR-1 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PAIR-2 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-3 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-4 | S16 | **NOT MET** | the acknowledgement means "the acceptance frame arrived", NOT "the phone durably committed". The device sends the ack in remote/pairing/pairing.go and only later does mobile/pairing.go call app.pin; the machine enrolls on the ack. Process death or a pin failure (full disk, read-only dir, Keystore refusal) in that window leaves the MACHINE ENROLLED and remote control live while the phone holds no durable pin. The send site's own comment enumerates the OPPOSITE residual (phone pins, machine claims nothing) and calls it harmless -- it never considers this orientation. Mutation: forcing App.pin to always error leaves every PBPAIR4-named test passing (ADR-007 B96) |
| PB-PAIR-5 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-6 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-7 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PUSH-0 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-1 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-2 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-3 | S12 | **NOT MET** | the fence asserts SIZE; the requirement asks for a SCHEMA. The presence sweep emits 78 RANDOM bytes, and the relay holds no key it could seal a real envelope with -- by two-tier design. A provider that PARSES rather than measures still separates a sweep from a wake, because a genuine wake's envelope header is cleartext. The project's OWN disclosure document says so in as many words -- 'Still open: the sweep is separable by SHAPE, just not by SIZE' -- while this row read shipped. Mutation: a plaintext payload leaves every PB-PUSH-3 test passing (ADR-007 B96) |
| PB-PUSH-4 | S17 | shipped | `docs/verification/remote-phaseB-s17-evidence.md` |
| PB-PUSH-5 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-6 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-7 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-8 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-9 | S17 | shipped | `docs/verification/remote-phaseB-s17-evidence.md` |
| PB-PUSH-10 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-RUN-1 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-RUN-2 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-RUN-3 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-RUN-4 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-RUN-5 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-SAS-1 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-SAS-2 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-SAS-3 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-SAS-4 | S8 | shipped | `docs/verification/remote-phaseB-s8-evidence.md` |
| PB-SEC-1 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-SEC-2 | S14 | **NOT MET** | the per-prompt identity fix landed on PerUseGate and NOT on the timed tier, so the class was never closed. PhoneSurface.reauthorizeTimedTier shows confirmForContent with NO ticket registered; the ledger entry is created inside the callback by grantTimedTier. An invalidation that clears the ledger therefore has nothing to clear for a prompt that is ON SCREEN, and a queued late success mints a fresh authorization AFTER invalidation. promptForContent has the same shape. Mutation: replacing the freshness decision with `if (true)` leaves both the Go gate and the Android unit suite green (ADR-007 B96) |
| PB-SEC-3 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-4 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-5 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-6 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-7 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-8 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-10 | S15 | shipped | `docs/verification/remote-phaseB-s15-evidence.md` |
| PB-SEC-11 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-12 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-13 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-SEC-14 | S18 | shipped | `docs/verification/remote-phaseB-s18-evidence.md` |
| PB-STATE-1 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-2 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-3 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-4 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-5 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-6 | S15 | shipped | `docs/verification/remote-phaseB-s15-evidence.md` |
| PB-STATE-7 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-8 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-9 | S15 | shipped | `docs/verification/remote-phaseB-s15-evidence.md` |
| PB-STATE-10 | S18b | shipped | `docs/verification/remote-phaseB-s18b-evidence.md` |
| PB-SYNC-1 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-SYNC-2 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-SYNC-3 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-SYNC-4 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-SYNC-5 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-SYNC-6 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-SYNC-7 | S1b | shipped | `docs/verification/remote-phaseB-s1b-evidence.md` |
| PB-SYNC-8 | S10 | shipped | `docs/verification/remote-phaseB-s10-evidence.md` |
| PB-TIME-1 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-TIME-2 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-TIME-3 | S11 | shipped | `docs/verification/remote-phaseB-s11-evidence.md` |
| PB-TOK-1 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-TOK-2 | S5 | shipped | `docs/verification/remote-phaseB-s5-evidence.md` |
| PB-TOK-3 | S5 | shipped | `docs/verification/remote-phaseB-s5-evidence.md` |
| PB-TOK-4 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-1 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-2 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-3 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-4 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-5 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-6 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
| PB-TOOL-7 | S13 | shipped | `docs/verification/remote-phaseB-s13-evidence.md` |
