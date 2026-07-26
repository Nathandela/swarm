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

| | count |
|---|---|
| Requirements | 143 |
| Shipped (asserted by hand) | 137 |
| Evidenced (measured on disk) | 137 |
| **NOT MET (slice shipped, requirement invalidated later)** | **5** |
| Remaining | 1 |
| **Shipped with NO evidence file** | **0** |

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
| PB-E2E-2 | S19 | **NOT MET** | ADR-007 B35 — the emulator smoke has never run -- its own evidence file disclaims it and no log or screenshot exists; counted shipped only because evidence is measured per SLICE, not per requirement (ADR-007 B38) |
| PB-E2E-3 | S19 | shipped | `docs/verification/remote-phaseB-s19-evidence.md` |
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
| PB-KEY-7 | S14 | **NOT MET** | ADR-007 B35 — no purge trigger exists, and wiring one as specified would brick the phone at the first screen lock (ADR-007 B35) |
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
| PB-NET-4 | S6 | **NOT MET — AWAITING AMENDMENT** | Reconnect/backoff/re-auth/state clauses are met (`docs/verification/remote-phaseB-s6-evidence.md`). The QUEUE clause cannot be met as written and is not a wiring gap: a queued op is a command signed for §6.0's 1-minute horizon that `internal/phonecore/opqueue.go` states is never re-signed on replay, and `internal/skeleton/deviceauth.go` refuses it as "command expired" — proven in `internal/skeleton/b42_offlinequeue_test.go`. Re-signing at drain needs PB-SEC-2's per-use biometric, i.e. the user present, which is a prompt and not a queue. Requires an amendment to PB-NET-4 and to ADR-007 D7's last sentence (ADR-007 B42) |
| PB-NET-5 | S6b | shipped | `docs/verification/remote-phaseB-s6b-evidence.md` |
| PB-NET-6 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-7 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-OPS-1 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-2 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-3 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-OPS-4 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-OPS-5 | S20 | shipped | `docs/verification/remote-phaseB-s20-evidence.md` |
| PB-PAIR-1 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PAIR-2 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-3 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-4 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-5 | S16 | shipped | `android/gate/pairingstates_test.go` — the app now declares DIFFERENT_MACHINE, RATE_LIMITED and FAILED, retires ALREADY_PAIRED, and `PairingSurface.stepOf` routes every state `mobile/pairing.go` can report; each has its own message. The gate compares the two alphabets in both directions, which is the control that was missing when B42 found the drift |
| PB-PAIR-6 | S16 | shipped | `docs/verification/remote-phaseB-s16-evidence.md` |
| PB-PAIR-7 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PUSH-0 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-1 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-2 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
| PB-PUSH-3 | S12 | shipped | `docs/verification/remote-phaseB-s12-evidence.md` |
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
| PB-SEC-1 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-SEC-2 | S14 | **NOT MET** | ADR-007 B35 — the freshness gate does nothing on the send path: the content key is unwrapped once at Resume and read from Go memory thereafter, so no re-authorization occurs per use (ADR-007 B36) |
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
