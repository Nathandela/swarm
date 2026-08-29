# Documentation Index

## Quick start
- [AGENTS.md](../AGENTS.md) — agent entry point (finalized)
- [README](../README.md) — project overview
- [Install](install.md) — Homebrew tap, `go install`, static binary download, upgrade/D-8 note (E13.3)

## Operations
- [Relay runbook](operations/relay-runbook.md) — standing up a TLS-terminated relay for the handset demonstration, and the certificate-renewal hazard (PB-OPS-1, PB-OPS-5)
- [Operator runbook](operations/operator-runbook.md) — install, pair, revoke, kill switch, device loss, push configuration (PB-OPS-2)
- [Metadata disclosure](operations/metadata-disclosure.md) — the register of who observes what: the relay operator, the push provider, the gateway, a network observer, and (§5) a second model vendor, the one entry that receives session content rather than metadata (PB-OPS-3)
- [Physical-handset gate](operations/physical-handset-gate.md) — **every step UNRUN**: the playbook 11.2 matrix as 95 executable PH-* rows (rewritten 2026-08-15 for gateway push, multi-machine, presets)
- [Auto-upgrade runbook](ops/auto-upgrade.md) — the nightly `launchd` timer that upgrades the owner's machine and converges the daemon/gateway onto it, or touches nothing (ADR-020)

## The plan
- [System specification](specifications/system-spec.md) — EARS requirements, diagrams, scenario table (Gate 2-approved)
- [Build plan](specifications/build-plan.md) — 15 ordered epics, contracts, gap resolutions, implementation guidelines (Gate 3-approved)
- [Implementation goals](specifications/implementation-goals.md) — per-epic exit criteria, global goals, orchestration protocol (post audit-002)
- [Remote Control implementation playbook](specifications/remote-control-product-playbook.md) — owner-approved self-host-first phone/terminal product contract, architecture, delivery waves, and release gates
- [System invariants](invariants/system-invariants.md) — 12 safety + 3 liveness properties, each test-bound
- [Push gateway API](specifications/push-gateway-api.md) — the ADR-015 gateway contract: five operations, WakeV1, wake obligations, relay+gateway collusion threat model (Draft, binds at R3)
- [Interaction schema](specifications/interaction-schema.md) — normative interaction-item payload: eight kinds, delta semantics, approval lifecycle (Draft, binds on ADR-009 acceptance)
- [Conversation surface plan](specifications/chat-surface-plan.md) — owner-approved 2026-08-26: the phone session screen becomes a chat, the lease leaves the UX, controls leave the reading path. Waves, deferrals with reasons, and the ADRs that move. Owner drawing (components, states, copy): https://claude.ai/code/artifact/4f2f2277-de36-4242-a73a-b80c86f1ecee · flow and audit findings: https://claude.ai/code/artifact/7830b3ff-65bb-4710-8fff-448335b944b7
- [Phone refit playbook](specifications/phone-refit-playbook.md) — owner-directed 2026-08-27, the contract for seven waves: pinned frame, sending works, one button, Slate palette (ADR-021), rewritten words, one-line tool rows, the other screens. Per wave: files allowed, tests first, gates, done-when. Interactive mock: https://claude.ai/code/artifact/90c122aa-eff2-4e47-a4f1-6d9e8757ba99

## Design reference
- [Brandbook v1](design/swarm-illustration-direction/index.html) — **canonical visual identity**: Night Orchestra artwork, logo, app icon, Slate palette, and README asset system
- [UI preview](design/ui-preview.html) — **canonical visual reference** (TUI): interactive screen mockups (keyboard-drivable), flow, architecture, lifecycle, test strategy. Live copy: https://claude.ai/code/artifact/2959c9c2-1ab9-4ab1-ba35-e32d845ba0b7
- [Slate maquette](research/slate-maquette.html) — **the phone app's design-token source** (ADR-021): every screen, every kit component, the re-rendered mark, and the normative `:root` token block
- [Hermes adapter contract](design/hermes-adapter.md) — classic-CLI argv, status, identity/resume, platform boundary, and deferred structured transports

## Decisions
- [ADR index](adr/README.md) — all decisions, status, and the convention for adding new ones
- [ADR-001](adr/ADR-001-per-session-shim-processes.md) — per-session shim processes own the PTYs
- [ADR-002](adr/ADR-002-protocol-control-data-split.md) — control/data plane split, in-shim VT emulation
- [ADR-003](adr/ADR-003-persistence-schema.md) — per-session metadata as source of truth
- [ADR-004](adr/ADR-004-security-baseline.md) — v1 security baseline
- [ADR-005](adr/ADR-005-vt-emulator-library.md) — VT emulator library (charmbracelet/x/vt)
- [ADR-009](adr/ADR-009-structured-chat-interaction.md) — the phone surface is a structured chat transcript, the terminal grid is retired
- [ADR-010](adr/ADR-010-adapter-structured-capture.md) — structured interaction capture as an optional, additive adapter-contract extension
- [ADR-011](adr/ADR-011-multi-device-epochs.md) — multi-device epochs: per-device sender ids, inbound keys, seq spaces
- [ADR-013](adr/ADR-013-mirror-capture-architecture.md) — Mirror capture architecture: structure beside the sacred PTY, the held hook rejected on co-presence grounds, the phone's answer typed into the CLI's own dialog
- [ADR-020](adr/ADR-020-unattended-daemon-restart.md) — unattended daemon restart spawns from the saved environment, or touches nothing
- [ADR-022](adr/ADR-022-live-session-name-adoption.md) — a session's name follows the name its CLI shows (Claude's `~/.claude/sessions` registry), newest wins

## Governance
- [docs/governance/](governance/) — the agentic-codebase-manifesto, vendored verbatim ([provenance](governance/PROVENANCE.md))

## Process traces
- [Audit committee report 001](verification/audit-001-system-spec.md) — the adversarial review that shaped spec Draft 2
- [Audit committee report 002](verification/audit-002-implementation-goals.md) — the review that shaped implementation-goals.md Draft 2
- [Landscape research](research/agent_view_landscape.md) — Agent View internals, cross-CLI managers, mobile remotes
- [Hermes adapter evidence](verification/hermes-adapter-evidence.md) — characterization provenance, retained PTY fixtures, TDD records, acceptance gates, and known upstream limitations
- [DESIGN.md](../DESIGN.md) — original design brief (historical)
