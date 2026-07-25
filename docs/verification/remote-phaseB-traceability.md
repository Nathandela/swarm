# Phase B requirement traceability

**GENERATED — do not edit by hand.** Regenerate with
`python3 scripts/phaseb-traceability.py > docs/verification/remote-phaseB-traceability.md`.

The final audit validates against every REQUIREMENT, not every slice. This is the per-row
view: owner, whether that owner has shipped, and where the evidence is.

| | count |
|---|---|
| Requirements | 143 |
| Shipped | 98 |
| Remaining | 45 |
| **Shipped with NO evidence file** | **56** |

## Shipped slices with no evidence file

These 9 slices are implemented and gated, but their only durable record is a commit
message. That is not sufficient for a per-requirement audit. **Reconstruct each from its
commit and its tests, never from memory** — an evidence file written from recollection is a
plausible account of what was intended rather than of what shipped.

- **S5** — 3 requirements: PB-TOK-1, PB-TOK-2, PB-TOK-3
- **S8** — 9 requirements: PB-BIND-1, PB-BIND-2, PB-BIND-3, PB-BIND-4, PB-BIND-5, PB-BIND-6, PB-BIND-7, PB-SAS-1, PB-SAS-2
- **S9** — 1 requirement: PB-NET-1
- **S10** — 10 requirements: PB-KEY-10, PB-KEY-3, PB-KEY-4, PB-SYNC-1, PB-SYNC-2, PB-SYNC-3, PB-SYNC-4, PB-SYNC-5, PB-SYNC-6, PB-SYNC-8
- **S11** — 9 requirements: PB-INPUT-1, PB-INPUT-2, PB-INPUT-3, PB-INPUT-4, PB-INPUT-5, PB-INPUT-6, PB-TIME-1, PB-TIME-2, PB-TIME-3
- **S12** — 9 requirements: PB-PUSH-0, PB-PUSH-1, PB-PUSH-10, PB-PUSH-2, PB-PUSH-3, PB-PUSH-5, PB-PUSH-6, PB-PUSH-7, PB-PUSH-8
- **S13** — 13 requirements: PB-RUN-1, PB-RUN-2, PB-RUN-3, PB-RUN-4, PB-RUN-5, PB-TOK-4, PB-TOOL-1, PB-TOOL-2, PB-TOOL-3, PB-TOOL-4, PB-TOOL-5, PB-TOOL-6, PB-TOOL-7
- **S6b** — 1 requirement: PB-NET-5
- **S7b** — 1 requirement: PB-GW-2

## Every requirement

| Requirement | Slice | Status | Evidence |
|---|---|---|---|
| PB-APP-1 | S16 | pending | — |
| PB-APP-2 | S16 | pending | — |
| PB-APP-3 | S16 | pending | — |
| PB-APP-4 | S16 | pending | — |
| PB-APP-5 | S16 | pending | — |
| PB-APP-6 | S16 | pending | — |
| PB-APP-7 | S16 | pending | — |
| PB-APP-8 | S16 | pending | — |
| PB-APP-9 | S16 | pending | — |
| PB-APP-10 | S16 | pending | — |
| PB-BIND-0 | S1 | shipped | `docs/verification/remote-phaseB-s1-evidence.md` |
| PB-BIND-1 | S8 | shipped | **none — commit message only** |
| PB-BIND-2 | S8 | shipped | **none — commit message only** |
| PB-BIND-3 | S8 | shipped | **none — commit message only** |
| PB-BIND-4 | S8 | shipped | **none — commit message only** |
| PB-BIND-5 | S8 | shipped | **none — commit message only** |
| PB-BIND-6 | S8 | shipped | **none — commit message only** |
| PB-BIND-7 | S8 | shipped | **none — commit message only** |
| PB-DOC-1 | S0 | shipped | `docs/adr/ADR-007-remote-access.md` |
| PB-DOC-2 | S20 | pending | — |
| PB-DOC-3 | S20 | pending | — |
| PB-DOC-4 | S20 | pending | — |
| PB-DOC-5 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-DOC-7 | S20 | pending | — |
| PB-E2E-1 | S19 | pending | — |
| PB-E2E-2 | S19 | pending | — |
| PB-E2E-3 | S19 | pending | — |
| PB-E2E-4 | S19 | pending | — |
| PB-E2E-5 | S21 | pending | — |
| PB-GW-1 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-2 | S7b | shipped | **none — commit message only** |
| PB-GW-3 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-4 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-5 | S2 | shipped | `docs/verification/remote-phaseB-s2-evidence.md` |
| PB-GW-6 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-GW-7 | S2b | shipped | `docs/verification/remote-phaseB-s2b-evidence.md` |
| PB-GW-8 | S2b | shipped | `docs/verification/remote-phaseB-s2b-evidence.md` |
| PB-INPUT-1 | S11 | shipped | **none — commit message only** |
| PB-INPUT-2 | S11 | shipped | **none — commit message only** |
| PB-INPUT-3 | S11 | shipped | **none — commit message only** |
| PB-INPUT-4 | S11 | shipped | **none — commit message only** |
| PB-INPUT-5 | S11 | shipped | **none — commit message only** |
| PB-INPUT-6 | S11 | shipped | **none — commit message only** |
| PB-KEY-1 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-2 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-3 | S10 | shipped | **none — commit message only** |
| PB-KEY-4 | S10 | shipped | **none — commit message only** |
| PB-KEY-5 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-6 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-7 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-8 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-KEY-9 | S14a | shipped | `docs/verification/remote-phaseB-s14a-evidence.md` |
| PB-KEY-10 | S10 | shipped | **none — commit message only** |
| PB-LIFE-1 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-2 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-3 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-4 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-5 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-6 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-LIFE-7 | S4b | shipped | `docs/verification/remote-phaseB-s4b-evidence.md` |
| PB-NET-1 | S9 | shipped | **none — commit message only** |
| PB-NET-2 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-3 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-4 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-5 | S6b | shipped | **none — commit message only** |
| PB-NET-6 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-NET-7 | S6 | shipped | `docs/verification/remote-phaseB-s6-evidence.md` |
| PB-OPS-1 | S20 | pending | — |
| PB-OPS-2 | S20 | pending | — |
| PB-OPS-3 | S20 | pending | — |
| PB-OPS-4 | S4 | shipped | `docs/verification/remote-phaseB-s4-evidence.md` |
| PB-OPS-5 | S20 | pending | — |
| PB-PAIR-1 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PAIR-2 | S16 | pending | — |
| PB-PAIR-3 | S16 | pending | — |
| PB-PAIR-4 | S16 | pending | — |
| PB-PAIR-5 | S16 | pending | — |
| PB-PAIR-6 | S16 | pending | — |
| PB-PAIR-7 | S3 | shipped | `docs/verification/remote-phaseB-s3-evidence.md` |
| PB-PUSH-0 | S12 | shipped | **none — commit message only** |
| PB-PUSH-1 | S12 | shipped | **none — commit message only** |
| PB-PUSH-2 | S12 | shipped | **none — commit message only** |
| PB-PUSH-3 | S12 | shipped | **none — commit message only** |
| PB-PUSH-4 | S17 | pending | — |
| PB-PUSH-5 | S12 | shipped | **none — commit message only** |
| PB-PUSH-6 | S12 | shipped | **none — commit message only** |
| PB-PUSH-7 | S12 | shipped | **none — commit message only** |
| PB-PUSH-8 | S12 | shipped | **none — commit message only** |
| PB-PUSH-9 | S17 | pending | — |
| PB-PUSH-10 | S12 | shipped | **none — commit message only** |
| PB-RUN-1 | S13 | shipped | **none — commit message only** |
| PB-RUN-2 | S13 | shipped | **none — commit message only** |
| PB-RUN-3 | S13 | shipped | **none — commit message only** |
| PB-RUN-4 | S13 | shipped | **none — commit message only** |
| PB-RUN-5 | S13 | shipped | **none — commit message only** |
| PB-SAS-1 | S8 | shipped | **none — commit message only** |
| PB-SAS-2 | S8 | shipped | **none — commit message only** |
| PB-SAS-3 | S16 | pending | — |
| PB-SEC-1 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-SEC-2 | S14 | shipped | `docs/verification/remote-phaseB-s14-evidence.md` |
| PB-SEC-3 | S18 | pending | — |
| PB-SEC-4 | S18 | pending | — |
| PB-SEC-5 | S18 | pending | — |
| PB-SEC-6 | S18 | pending | — |
| PB-SEC-7 | S18 | pending | — |
| PB-SEC-8 | S18 | pending | — |
| PB-SEC-10 | S15 | pending | — |
| PB-SEC-11 | S18 | pending | — |
| PB-SEC-12 | S18 | pending | — |
| PB-SEC-13 | S18 | pending | — |
| PB-SEC-14 | S18 | pending | — |
| PB-STATE-1 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-2 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-3 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-4 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-5 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-6 | S15 | pending | — |
| PB-STATE-7 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-8 | S7 | shipped | `docs/verification/remote-phaseB-s7-evidence.md` |
| PB-STATE-9 | S15 | pending | — |
| PB-STATE-10 | S18b | pending | — |
| PB-SYNC-1 | S10 | shipped | **none — commit message only** |
| PB-SYNC-2 | S10 | shipped | **none — commit message only** |
| PB-SYNC-3 | S10 | shipped | **none — commit message only** |
| PB-SYNC-4 | S10 | shipped | **none — commit message only** |
| PB-SYNC-5 | S10 | shipped | **none — commit message only** |
| PB-SYNC-6 | S10 | shipped | **none — commit message only** |
| PB-SYNC-7 | S1b | shipped | `docs/verification/remote-phaseB-s1b-evidence.md` |
| PB-SYNC-8 | S10 | shipped | **none — commit message only** |
| PB-TIME-1 | S11 | shipped | **none — commit message only** |
| PB-TIME-2 | S11 | shipped | **none — commit message only** |
| PB-TIME-3 | S11 | shipped | **none — commit message only** |
| PB-TOK-1 | S5 | shipped | **none — commit message only** |
| PB-TOK-2 | S5 | shipped | **none — commit message only** |
| PB-TOK-3 | S5 | shipped | **none — commit message only** |
| PB-TOK-4 | S13 | shipped | **none — commit message only** |
| PB-TOOL-1 | S13 | shipped | **none — commit message only** |
| PB-TOOL-2 | S13 | shipped | **none — commit message only** |
| PB-TOOL-3 | S13 | shipped | **none — commit message only** |
| PB-TOOL-4 | S13 | shipped | **none — commit message only** |
| PB-TOOL-5 | S13 | shipped | **none — commit message only** |
| PB-TOOL-6 | S13 | shipped | **none — commit message only** |
| PB-TOOL-7 | S13 | shipped | **none — commit message only** |
