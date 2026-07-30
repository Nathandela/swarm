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
| Shipped (asserted by hand) | 134 |
| Evidenced (measured on disk) | 134 |
| **NOT MET (slice shipped, requirement invalidated later)** | **8** |
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
| PB-NET-3 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-4 | S6 | **NOT MET** | REASON REPLACED 2026-07-31; the pre-fix text described a defect now closed, which is fossil evidence in the audit's own artifact. Section 6.0's backoff IS implemented and fenced over a REAL FLAPPING RELAY: dial-gap growth in non-overlapping bands, a second auth_init, and the connection EVENT sequence, each mutation-proven by reverting the CALL SITE rather than a constant. Re-auth and no-replay independently mutation-verified. WHAT REMAINS: production jitter can be disabled and every assertion still passes -- a frac source returning 0 sits inside the +/-20% band on every sample, and the flapping test accepts the exact unjittered 500ms/1s/2s bases. B113 one level in: the RANGE is fenced, the connection to actual randomness is not. Nothing observes production reaching the 30s ceiling (ADR-007 B118) |
| PB-NET-5 | S6b | **NOT MET** | DECOMPOSES, and I refuted it wrongly once already (B98) by checking the numeric clause and not the quantifier. The criterion clause (p50 150ms phone Type -> PTY write) and the drop-the-gateway-poll clause ARE fenced on live code. But the requirement says BOTH HOPS, and the PHONE hop's fence is transport/s6b_input_test.go -- all six tests driving the dead Session.Follow, which is the only phone-side MailboxWait caller in the tree and has zero production callers. The shipped phone does not follow, it POLLS: mobile/app.go pollInterval = 500ms. So what shipped is a GATEWAY-SIDE-ONLY fix -- the exact mirror of the phone-side-only fix this requirement's own text warns would fake the criterion. UNFENCED, not disproven: the shipped poll plausibly avoids head-of-line blocking by a cruder mechanism, but nothing measures it, and the echo direction it gates is outside the numeric criterion by construction (ADR-007 B100) |
| PB-NET-6 | S6 | **NOT MET** | THE REPLACEMENT FENCE CANNOT FAIL ON THE DEFECT IT IS NAMED FOR. mobile/pbnet6_drainreaders_test.go pins CALL SITES by AST scan, not concurrency: mutating App.run to launch two genuinely concurrent drains on one connection -- verbatim the defect the fence's own error message describes -- leaves it PASSING. And concurrent-drain is not a clause this requirement names; it names seq gating, replay/reorder/dup rejection, the mailbox cap and hostile-pagination termination. THE ROW IS CLOSER THAN THIS REASON SUGGESTS: all four named clauses have live subjects (relay/abuse_test.go for the cap, skeleton's adversarial replay test, phonecore/processdeath_test.go for restart, and conformance/drain_test.go's non-advancing-page test which IS hostile-pagination termination in its shipped form, currently attributed to PB-SYNC-6). Nobody has assembled that union under this row, and none of those four has been mutated (ADR-007 B112) |
| PB-NET-7 | S6 | **NOT MET** | THE ENUMERATION RESIDUAL IS CLOSED; the row's remaining question is a NEW clause found by closing it. The residual B109 named, then discharged with an argument B112 measured losing by 2.8x, then B115 restated unchanged, is now fenced: internal/verify/pbnet7_deadlines_test.go DERIVES which relay operations relay bounds for itself (reaches roundtrip on a *Client, so the connection is guaranteed pumped) versus which oblige their caller, fences the four structural facts that partition rests on, and then walks EVERY production call into the caller-bounded set with a typed SSA+RTA backward dataflow, tracing each context to its origin through parameters, phis, closure free-vars and helper returns and requiring context.WithTimeout/WithDeadline. No allowlist: the rule decides every site, so there is nothing to rot. Mutation-proven three ways with reverts checksummed -- B112's CRITICAL restored verbatim fails naming command_loop.go:333 and the five-hop chain to os/signal.NotifyContext; a NEW unbounded call site fails by name; hoisting callTimeout out of dialConn's pumped branch fails FACT 3. RECORDED AGAINST THE FENCE ITSELF (ADR-007 B122, instrument 9): its FIRST version PASSED B112's mutation, because remotegw reaches the relay through its own Mailbox INTERFACE and a static-callee matcher saw no call at all -- a smaller enumeration that looked complete. Fixed by resolving dispatch, plus an anti-vacuity guard that fails if zero sites are reached through an interface. WHAT IS OWED: running it found the DIAL unbounded at three shipped call sites (45s probe against a TCP-accepting, HTTP-silent peer), including one that parks BEFORE B64's pairing window exists; that fix is another agent's and is now committed at b806444 -- DefaultDialTimeout at the dialConn boundary, ADJUDICATED there rather than at the callers because an unbounded dial is an OMISSION and not a contract (unlike MailboxWait, where no single bound is correct for all callers), and because context.WithTimeout composes so a caller wanting less still gets less. Its own verification is r7-fix-dial's, not this row's evidence yet; one precondition it rests on -- that a dial deadline does not become the CONNECTION's deadline, a property of coder/websocket and net/http rather than of this repo -- is fenced here by relay.TestPBNET7_ADialDeadlineDoesNotOutliveTheHandshake. THE VERDICT IS THE COMMITTEE'S, deliberately: B109 and B115 both declined to close this row on their own work, and this fence's author is a worse party still |
| PB-OPS-1 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-2 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-3 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-4 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-OPS-5 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-PAIR-1 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PAIR-2 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-3 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-4 | S16 | **NOT MET** | REASON REPLACED 2026-07-31; the half-pair defect is CLOSED and the pre-fix text is fossil. The phone commits durably BEFORE it acknowledges, the machine enrols only on that acknowledgement, and the persisted write fsyncs the file, renames, and fsyncs the directory. Two fences by two agents 23 minutes apart both go RED on the reverted ordering, one by SIGKILLing a genuine second process. WHAT REMAINS: the row requires process death AT EVERY named transition, and the enumeration declares Noise msg2 'unreachable' -- the kill test logs that and RETURNS WITHOUT KILLING ANYTHING. The enumeration passes, proving only that the no-op row is named. Either kill in that window or amend the criterion to state the bounding-state equivalence explicitly (ADR-007 B117) |
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
| PB-SEC-2 | S14 | **NOT MET** | REASON REPLACED 2026-07-31; the lifecycle defect is CLOSED and the pre-fix text is fossil. The timed tier has the per-use tier's prompt lifecycle, and two live BYPASSES found by mutating the production connection are fenced: a button that stops calling its gate, and observers never installed. WHAT REMAINS is the row's FIRST clause, untouched by round 7: 'cryptographically enforced, not cosmetic ... Keystore-enforced unwrap/sign authorization rather than a UI boolean'. The timed tier reads an IN-MEMORY LEDGER TIMESTAMP and invokes the action when fresh; SendInput accepts no proof; resolveSend reuses an already-resident content key without consulting Keystore. The authorized biometric operation is not cryptographically bound to the Go action -- the clause the requirement wrote 'rather than a UI boolean' to forbid (ADR-007 B117) |
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
