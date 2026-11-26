# Documentation Index

- [Rust migration roadmap](specifications/rust-migration/README.md) — parallel-branch design, two pinned Go references, retained workflows, Discord calls, local parity qualification and cutover.

## Implementation and release evidence

| Work | Reference |
|---|---|
| Active scope, exclusions and milestone definitions | [Migration plan](specifications/rust-migration/README.md) |
| Source-level architecture and verification contract | [Rust design](specifications/rust-migration/rust-design.md) · [Differential qualification](specifications/rust-migration/qualification.md) |
| Latest observed behavior and open release gates | [Native fleet ledger](verification/rust-migration.md) |
| Installed preview builds, signing and checkout isolation | [Local release runbook](operations/rust-preview-release.md) |
| Working agreements and source navigation | [Agent instructions](../AGENTS.md) · [Codebase manifesto](governance/MANIFESTO.md) |

## Operator and contributor guides

| Need | Reference |
|---|---|
| Launch and navigation | [Launch-form verification](verification/rust-launch-form-2026-11-26.md) · [Operator trials](specifications/rust-migration/operator-trials.md) |
| Sessions across a Go–Rust transition | [Live terminal adoption](verification/rust-live-go-shim.md) · [Stopped-state rollback](verification/rust-stopped-go-state.md) |
| Scheduling and Pilot | [Scheduling](verification/rust-schedules.md) · [Pilot](verification/rust-pilot.md) |
| Diagnostics and cleanup | [Storage qualification](verification/rust-storage-cleanup.md) |
| Discord and calls | [Operator notes](ops/discord.md) · [Personal call verification](verification/rust-discord-personal-calls.md) |
| Local test harness | [Two-binary terminal comparisons](../scripts/verification/parity/README.md) |
| Branch scope and source retirement | [Retired surface checks](verification/rust-retirement.md) |
| Architecture decisions and system requirements | [Decision index](adr/README.md) · [Requirements](specifications/system-spec.md) · [Invariants](invariants/system-invariants.md) |

## Go reference

The roadmap uses the public Go release and the separately pinned Discord-call revision. The reference builder checks out exact commits into separate temporary directories. The published Go documentation and historical test evidence can be read at their original revision. [Go reference index](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/INDEX.md) · [Build the reference executables](../scripts/verification/build_rust_references.py)
