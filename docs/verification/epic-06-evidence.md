# Epic 6 — Evidence

**Epic**: Client protocol + daemon API (`agents-tracker-9k9`) — THE low-reversibility wire surface (ADR-002)
**Commits**: 7c09e6e (initial), 6b10576 (fixes r1), f2b63aa (re-attach rework), 982e1f8 (derived cap), c55664e (producer-side bounds).

## TDD evidence (GG-5)

Designer wrote 45 failing tests across 13 files first; red log in [epic-06-red/protocol-red.txt](epic-06-red/protocol-red.txt) (undefined-only, verified genuine by both reviewers). Every fix round wrote failing tests first.

## Criterion walk (E6.1 – E6.9)

| Criterion | Evidence |
|---|---|
| E6.1 frame codec (G1) | every control op round-trips; MaxFrame pre-alloc; unknown op → error, server survives; TDataIn opaque demux |
| E6.2 protocol.md | field-level doc; **bidirectional** drift test (struct↔doc field-set diff, GG-7 teeth) |
| E6.3 handshake | version + capability negotiation; unique endpoint ids; D-8 both sides (names `swarm daemon restart`, states safe; client SYNTHESIZES the text) |
| E6.4 lease/generation (S2) | monotonic generations; supersede RE-ATTACHES a fresh shim connection (reuses the shim's atomic S10 boundary — no daemon-side splice); stale-generation input/resize dropped (TOCTOU closed); detach/EOF releases lease+stream (L3); detach rejects gen-0 wildcard |
| E6.5 fan-out (S9/L1) | bounded per-client queues; wedged subscriber disconnected within bound outside the lock; FromDaemon disconnects a full-queue subscriber (never silently drops); registered before ok; ≤1 s delivery |
| E6.6 revalidation | every field re-validated server-side before DaemonAPI; FilterEnv server-side; bad ids/dims/oversized rejected |
| E6.7 attach ordering (S10) | exactly one snapshot before any live frame; snapshot CHUNKED across frames (a full grid exceeds MaxFrame) with bounded reassembly (cap derived from producer-bounded per-cell text + title; rejects negative/overshoot/oversized, no OOM) and attachment-state rollback on write failure |
| E6.8 namespacing (F-1/F-2) | endpoint_id + namespaced session id; foreign endpoint/session rejected; no UDS/fd fields |
| E6.9 groups server-side | SessionView.Group via status.Derive daemon-side; client never derives |

## Committee (audit-006) — four rounds, productive divergence

codex returned 7 HIGH + 5 MEDIUM; Opus (ran -race) returned 1 HIGH + 2 MEDIUM, clearing several of codex's HIGHs with interleaving traces. Orchestrator adjudicated by reading code. Both agreed on the stale-snapshot-on-supersede HIGH; codex uniquely surfaced the snapshot-exceeds-MaxFrame + deadlock, the unbounded attach pump, and the L1 event loss — all real.

Fix arc (each cross-model-verified by codex):
- r1 fixed 13 findings but the snapshot-chunking introduced 3 NEW HIGHs (unbounded reassembly, broken re-snapshot boundary, publication race).
- **Root fix**: supersede RE-ATTACHES via a fresh shim connection instead of splicing a snapshot into a reused stream — reusing the shim's proven atomic boundary eliminated the whole class of re-snapshot bugs. The frozen lease test was revised (authorized) to assert supersede semantics + L3 stream release rather than single-stream reuse.
- Final: the reassembly cap needed a sound per-cell byte bound — fixed producer-side (vt clamps per-cell text to 64 B, title to 256 B; also an N-6 hostile-input defense). **codex APPROVE.**

Synthesis: [audit-006-epic-06.md](audit-006-epic-06.md).

## Quality gates (GG-4)

**Whole module green under `-race`** (all 16 packages), gofmt + vet clean, vt fuzz clean. This is the first epic close where every package in the module passes together (Epics 0–6, 9 all implemented).
