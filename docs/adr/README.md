# ADR Index

Architectural Decision Records for swarm. Each ADR captures the *why* behind a design choice so later agents don't "fix" an intentional decision back into the problem it solved (see [docs/governance/MANIFESTO.md](../governance/MANIFESTO.md), Axiom 2).

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [001](ADR-001-per-session-shim-processes.md) | Per-session shim processes own the PTYs — sessions survive daemon crash/upgrade | Accepted | 2026-07-16 |
| [002](ADR-002-protocol-control-data-split.md) | Control/data plane split and in-shim VT emulation | Accepted | 2026-07-16 |
| [003](ADR-003-persistence-schema.md) | Per-session metadata as source of truth; roster is a rebuildable index | Accepted | 2026-07-16 |
| [004](ADR-004-security-baseline.md) | v1 security baseline: filesystem permissions, argv-only spawning, server-side revalidation | Accepted | 2026-07-16 |
| [005](ADR-005-vt-emulator-library.md) | VT emulator library — `github.com/charmbracelet/x/vt` (E2.1 risk gate) | Accepted | 2026-07-17 |
| [006](ADR-006-field-test-ux-revisions.md) | Field-test UX revisions — detach key Ctrl+q, full-screen chrome, auth inheritance | Accepted | 2026-07-18 |
| [007](ADR-007-inconclusive-heuristic-preserves-status.md) | An inconclusive grid heuristic preserves the committed status (per-adapter grid signatures) | Accepted | 2026-07-18 |
| [007](ADR-007-remote-access.md) | Remote access — identity, pairing, two-scheme crypto, relay trust, journal, launch authority | Accepted, open gates (2026-08-15) | 2026-07-18 |
| [008](ADR-008-status-events-latest-state-coalescing.md) | Status events are level-triggered latest-state snapshots (coalescing permitted) | Accepted | 2026-07-18 |
| [008](ADR-008-go-toolchain-floor-1-25.md) | Go toolchain floor moves to 1.25 (gomobile tool directive) | Accepted | 2026-07-22 |
| [009](ADR-009-obsidian-visual-direction.md) | Obsidian visual direction — warm material, champagne accent, one specular moment | Accepted | 2026-08-07 |
| [009](ADR-009-structured-chat-interaction.md) | The phone surface is a structured chat transcript — the terminal grid is retired | Accepted (amended 2026-08-14, signed off 2026-08-15) | 2026-08-07 |
| [010](ADR-010-inter-session-orchestration.md) | Inter-session orchestration — agent-initiated spawn, handoff, observation, and steering via local CLI verbs | Accepted (amended 2026-08-07, 2026-08-18, 2026-08-26) | 2026-08-07 |
| [010](ADR-010-adapter-structured-capture.md) | Structured interaction capture is an optional, additive extension of the frozen adapter contract | Accepted | 2026-08-07 |
| [011](ADR-011-multi-device-epochs.md) | Multi-device epochs — per-device sender ids, per-device inbound keys, per-device seq spaces | Accepted (amended 2026-08-14, signed off 2026-08-15) | 2026-08-07 |
| [012](ADR-012-type-ladder-consolidation-phase-1.md) | Type ladder consolidation, phase 1 — safe merges | Accepted | 2026-08-09 |
| [013](ADR-013-mirror-capture-architecture.md) | Mirror capture architecture — structure beside the PTY; the phone's answer typed into the CLI's own dialog | Accepted (amended 2026-08-14, signed off 2026-08-15) | 2026-08-13 |
| [014](ADR-014-paged-interaction-history.md) | Paged interaction history — the transcript's past read on demand, by item identity, with an honest floor | Accepted | 2026-08-19 |
| [015](ADR-015-push-gateway-split.md) | The push gateway splits from the relay — Swarm operates the FCM sender, `swarm-remote` submits the wake, Android is the first client | Accepted | 2026-08-14 |
| [016](ADR-016-web-pki-relay-tls.md) | Web PKI is the default relay TLS policy — the pin becomes an expert policy with an authenticated rotation | Accepted | 2026-08-14 |
| [017](ADR-017-terminal-fallback-capability.md) | Capability-driven terminal fallback — the sanitized terminal returns, for the sessions that cannot be chat, and for nothing else. **Amended 2026-08-20 (Wave R8):** T2-a/T2-b/T2-c, T4-a/T4-b/T4-c, T5-a, T6-a..T6-f, T8-a/T8-b and the gate note — every one a fail-closed narrowing, and the two owner questions it left open are answered. **Amended again 2026-08-20 (Wave R8 CLOSING round), C0-C7: the wave lands as the READ HALF ONLY.** The control half — `terminal_input`, the generation/keepalive plane, any take-control affordance — is PARKED with written preconditions (C1: raw input is bearer-authorised); T8's transport-loss row is WITHDRAWN as unbuildable and its replacement row is a sweep, not a synchronous sever. C8 adds the facade-seam rule for the staleness fields. The raw-input attack surface in the shipped product is ZERO | Accepted | 2026-08-14 |
| [018](ADR-018-multi-machine-pairings.md) | Multi-machine pairings — one phone, N independent single-device relationships | Accepted | 2026-08-14 |
| [019](ADR-019-boundary-aware-detach-recognition.md) | Detach recognition becomes boundary-aware — the solo-read test (D4) is superseded | Accepted | 2026-08-26 |
| [020](ADR-020-unattended-daemon-restart.md) | Unattended daemon restart spawns from the saved environment, or touches nothing | Proposed (owner sign-off pending) | 2026-08-27 |
| [021](ADR-021-live-session-name-adoption.md) | A session's name follows the name its CLI shows, newest wins | Accepted | 2026-08-28 |

Numbers 007, 008, 009 and 010 are each carried by TWO documents: parallel lines minted them
independently before merging (007/008 the main and remote-control lines, 2026-08-02; 009/010 the
Obsidian design line and the interaction-program line, 2026-08-08). All four pairs stay as
published — each is cited by number across docs/ and the codebase (the remote ADR by number and
letter, B31..B144; the interaction ADRs by number from the item schema, the evidence files and Go
doc comments), so renumbering would break far more than the collision confuses. Disambiguate by
filename, and cite the filename rather than the bare number whenever both lines are in scope —
which they are for any phone-surface work, since the transcript screen is governed by
ADR-009-structured-chat-interaction.md and drawn in the language of ADR-009-obsidian-visual-direction.md.

## Adding a new ADR

1. Next sequential number (the next FREE one is ADR-022: 013 was minted for the Mirror capture architecture, `docs/specifications/mirror-program.md` M3.1 has reserved 014 for paged interaction history and that reservation still stands, 015-018 were allocated together by Wave R1 for the four playbook §3 amendments, 019 was minted for boundary-aware detach recognition, 020 for the unattended-restart environment decision, and 021 for live session name adoption).
2. File name: `docs/adr/ADR-NNN-kebab-case-title.md`.
3. Template sections: `Status` (Proposed / Accepted / Deprecated / Superseded by ADR-XXX), `Date`, `Context`, `Decision`, `Consequences` (Positive/Negative), and `Alternatives Considered` where relevant.
4. Add a row to the table above in the same commit.
5. Per implementation-goals.md (Orchestration protocol, step 6): if an epic discovers the spec or plan is wrong, the fix goes through an ADR — never silent criterion drift.
