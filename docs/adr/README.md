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

## Adding a new ADR

1. Next sequential number (the next is ADR-006).
2. File name: `docs/adr/ADR-NNN-kebab-case-title.md`.
3. Template sections: `Status` (Proposed / Accepted / Deprecated / Superseded by ADR-XXX), `Date`, `Context`, `Decision`, `Consequences` (Positive/Negative), and `Alternatives Considered` where relevant.
4. Add a row to the table above in the same commit.
5. Per implementation-goals.md (Orchestration protocol, step 6): if an epic discovers the spec or plan is wrong, the fix goes through an ADR — never silent criterion drift.
