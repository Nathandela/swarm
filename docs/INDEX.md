# Documentation Index

## Current architecture and boundaries

The terminal product is a Go + Bubble Tea client → per-user supervisor daemon → per-session PTY-owning shim → agent CLI. The daemon owns session metadata, orchestration, adapter coordination, and client protocol; shims own live PTYs and VT grids. The production adapter set is Claude Code, Codex, agy, Hermes Agent, and OpenCode, with capability differences recorded in adapter docs and verification. [OpenCode v0.19.0](verification/opencode-v0190-deployment.md) includes native worker lifecycle and controls; reliable unattended turn/attention status and full sustained qualification remain open ([parity evidence](verification/opencode-parity-2026-10-18.md), [qualification status](verification/opencode-minimum-version-2026-10-19.md)).

Android remote control is a separate product surface, not a client in the original terminal v1 scope. The owner-approved [remote product playbook](specifications/remote-control-product-playbook.md) is an implementation plan, and [ADR-027](adr/ADR-027-clean-remote-control-v2.md) accepts a clean replacement whose implementation and hosted/physical-device release gates remain open. Release CI builds Android artifacts; that does not claim an Android publication or production remote service.

The approved [system spec](specifications/system-spec.md), [build plan](specifications/build-plan.md), and [implementation goals](specifications/implementation-goals.md) preserve the original v1.0/v1.1 phase scopes and EARS/exit criteria. Later accepted amendments apply only where linked at the affected requirement or criterion; they do not erase historical evidence or imply that an open gate passed. Proposed ADRs remain proposals. For current operations and release status, use the evidence and runbooks linked below.

## Quick start
- [AGENTS.md](../AGENTS.md) — agent entry point (finalized)
- [README](../README.md) — project overview
- [Install](install.md) — Homebrew tap, `go install`, static binary download, upgrade/D-8 note (E13.3)

## Operations
- [Provider logins](operations/provider-logins.md) — manage native Claude Code and Codex logins and provider-wide safe switches
- [Pilot a Swarm conversation](operations/pilot.md) — caller-scoped `swarm --pilot` guidance and commands
- [Remote-control operator runbook](operations/operator-runbook.md) — provision, pair, revoke and diagnose the one Cloudflare relay-v2 Worker
- [Metadata disclosure](operations/metadata-disclosure.md) — the register of who observes what: the relay operator, the push provider, the gateway, a network observer, and (§5) a second model vendor, the one entry that receives session content rather than metadata (PB-OPS-3)
- [Physical-handset gate](operations/physical-handset-gate.md) — **every step UNRUN**: the playbook 11.2 matrix as 95 executable PH-* rows (rewritten 2026-08-15 for gateway push, multi-machine, presets)
- [Agent CLI refresh](ops/cli-session-refresh.md) — idle-session refresh after installed Claude/Codex upgrades, recovery, and operator controls
- [CLI refresh implementation plan](specifications/cli-session-refresh-plan.md) — reviewed lifecycle, observation limits, and acceptance criteria
- [Auto-upgrade runbook](ops/auto-upgrade.md) — the nightly `launchd` timer that upgrades the owner's machine and converges the daemon/gateway onto it, or touches nothing (ADR-020)

## The plan

- [OpenCode maturity implementation plan](specifications/opencode-maturity-plan.md) — dependency-ordered native lifecycle, observation, semantic messaging, controls, continuity, model selection and release qualification, reviewed through Swarm by DeepSeek and GLM
- [OpenCode minimum version and deployment status](verification/opencode-minimum-version-2026-10-19.md) — stable >=1.19.3 admission, connected checks, actual native recovery and sustained qualification boundaries
- [Swarm v0.19.0 OpenCode deployment](verification/opencode-v0190-deployment.md) — published release gates, Mac/VM installed artifact and daemon verification, native-worker continuity and remaining qualification
- [Managed OpenCode operations](ops/opencode-native.md) — native session monitoring, semantic receipts, exact request decisions and recovery limits

- [Reconnection and attention plan](specifications/reconnect-attention-plan.md) — Astra-reviewed terminal recovery and structured attention contracts
- [Reconnection and attention decision](adr/ADR-030-reconnection-and-attention.md) — additive error status and authoritative recovery semantics
- [Reconnection and attention verification](verification/reconnect-attention.md) — regression, race, compatibility, review and release evidence

- [Clean v2 remote-control plan](specifications/remote-scale-to-zero-plan.md) — owner-directed zero-user replacement: no backward compatibility, direct on-demand push, legacy cleanup, safety gates and conditional cost budget; not deployed
- [Scale-to-zero verification](verification/remote-scale-to-zero/README.md) — reproducible local runtime/emulator/interop tests and explicit unrun production gates
- [Private mobile repository decision](research/mobile-private-repository.md) — source-access/IP benefits, APK/history limits, split/CI costs and release-continuity gates
- [Lightweight storage strategies](research/swarm-lightweight-storage-strategies.md) — native history ownership, bounded caches, workspace growth, quota boundaries, and recovery trade-offs; research, not an implementation decision
- [Disk growth audit 2026-10-24](research/disk-growth-audit-2026-10-24.md) — fresh Mac/VM measurements, tested growth mechanisms, independent Astra challenges and storage architecture tradeoffs; no production cleanup
- [Lightweight storage fixing plan](specifications/lightweight-storage-plan.md) — owner-selected live-only supervisor, workspace boundaries, deletion, legacy cleanup, and two-host acceptance

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
- [Mobile Interface Field Guide v1](design/mobile-interface-direction/index.html) — interactive before/after proposal across 12 Android frame families, with Signal Field, Night Atlas, and Constellation options for each
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
- [ADR-023](adr/ADR-023-context-guard-auto-compaction.md) — opt-in, exact, provenance-bound ContextGuard with crash-safe at-most-once semantics and provider rollout gates
- [ADR-025](adr/ADR-025-launch-gate-answered-by-the-daemon.md) — the daemon answers Claude Code's folder-trust launch gate itself, grid-gated and once per session; an unanswered gate reads as needs_input
- [ADR-034](adr/ADR-034-provider-wide-native-logins.md) — one native login per provider, provider-wide safe switching, and owner-local credential custody

## Governance
- [docs/governance/](governance/) — the agentic-codebase-manifesto, vendored verbatim ([provenance](governance/PROVENANCE.md))

## Process traces
- [Cleanup follow-up](verification/cleanup-followup-2026-10-21/README.md) — documentation reconciliation, platform custody/contracts, Android read ownership, audit retention and explicit phone recovery
- [Repository tightening audit and verification](verification/repository-tightening-2026-10-21/audit.md) — ten Luna audit/implementation/review lanes, confirmed fixes, retired Android code, complete shortcut ledger, and remaining platform/policy debt
- [OpenCode strategy model reviews](verification/opencode-plan-review-2026-10-18.md) — actual DeepSeek V4.1 Flash and GLM-5.3 Flash critiques, two-round observations, selected evidence, native API runtime qualification and model selector verification
- [OpenCode implementation evidence](verification/opencode-implementation-2026-10-18.md) — built-in free-model Swarm workers, test-first changes, cross-review corrections, native client contracts and qualification limits
- [OpenCode parity and live verification](verification/opencode-parity-2026-10-18.md) — current adapter and Claude/Codex comparison, two-worker communication, daemon/provider crash and resume tests, native HTTP/SSE/permission probes, and unattended orchestration gaps
- [Lightweight storage verification](verification/lightweight-storage.md) — live-only journal/tail, workspace prevention, crash/pressure tests, and remaining lifetime-growth work
- [Native Linux storage gates](verification/lightweight-storage-linux.md) — complete normal/race suites, CPU/latency gates, fixture corrections, skipped coverage, and test-artifact retirement
- [v0.18 storage rollout](verification/lightweight-storage-rollout.md) — release gates, both-host deployment, legacy-journal recovery, executed cleanup, runtime storage checks and retained work
- [Storage cleanup inventory](verification/storage-cleanup-inventory.md) — measured Mac/VM state, live-writer limits, missing recovery destination, and explicit cleanup boundaries
- [Discussion deletion](verification/discussion-deletion.md) — remove retained attempts and owned worktrees, retry failed cleanup, and preserve lifecycle ordering
- [Provider login qualification](verification/provider-logins.md) — native feasibility evidence and open integrated release gates
- [Codex minimum version and migration readiness](verification/codex-minimum-version.md) — forward version admission, installed native sign-in checks and current bulk-migration limits
- [Phase A verification](verification/phase-a.md) — Codex model scan, backend/shim resilience, resume history and journal retention (v0.17.8)
- [v0.17.9 verification](verification/v0179.md) — CLI refresh retry bounds, board elapsed, SubagentStop ordering and status persist retry
- [v0.17.10 verification](verification/v0180.md) — Codex out-of-messages attention detection
- [Audit committee report 001](verification/audit-001-system-spec.md) — the adversarial review that shaped spec Draft 2
- [Audit committee report 002](verification/audit-002-implementation-goals.md) — the review that shaped implementation-goals.md Draft 2
- [Landscape research](research/agent_view_landscape.md) — Agent View internals, cross-CLI managers, mobile remotes
- [Hermes adapter evidence](verification/hermes-adapter-evidence.md) — characterization provenance, retained PTY fixtures, TDD records, acceptance gates, and known upstream limitations
- [ContextGuard evidence](verification/context-guard.md) — implementation map, Codex 0.150.1 characterization, safety gates, test commands, and observe-only rollout status
- [DESIGN.md](../DESIGN.md) — original design brief (historical)
