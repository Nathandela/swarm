# Documentation Index

## Quick start
- [AGENTS.md](../AGENTS.md) — agent entry point (finalized in Epic 0)
- [README](../README.md) — project overview

## The plan
- [System specification](specifications/system-spec.md) — EARS requirements, diagrams, scenario table (Gate 2-approved)
- [Build plan](specifications/build-plan.md) — 15 ordered epics, contracts, gap resolutions, implementation guidelines (Gate 3-approved)
- [System invariants](invariants/system-invariants.md) — 12 safety + 3 liveness properties, each test-bound

## Design reference
- [UI preview](design/ui-preview.html) — **canonical visual reference**: interactive screen mockups (keyboard-drivable), flow, architecture, lifecycle, test strategy. Live copy: https://claude.ai/code/artifact/2959c9c2-1ab9-4ab1-ba35-e32d845ba0b7

## Decisions
- [ADR-001](adr/ADR-001-per-session-shim-processes.md) — per-session shim processes own the PTYs
- [ADR-002](adr/ADR-002-protocol-control-data-split.md) — control/data plane split, in-shim VT emulation
- [ADR-003](adr/ADR-003-persistence-schema.md) — per-session metadata as source of truth
- [ADR-004](adr/ADR-004-security-baseline.md) — v1 security baseline

## Process traces
- [Audit committee report 001](verification/audit-001-system-spec.md) — the adversarial review that shaped spec Draft 2
- [Landscape research](research/agent_view_landscape.md) — Agent View internals, cross-CLI managers, mobile remotes
- [DESIGN.md](../DESIGN.md) — original design brief (historical)
