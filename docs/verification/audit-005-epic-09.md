# Audit 005 — Epic 9 (adapter contract + characterization harness)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model) + Opus (independent). agy quota-blocked.
**Verdict**: FIX REQUIRED. Reviewers diverged (codex 3 HIGH; Opus APPROVE + 1 MEDIUM); orchestrator adjudicated by reading code — codex substantially right, including a genuine contract-design flaw.

## The decisive finding — Detect() cannot be pure (contract flaw)
`Detect() (Detection, error)` is declared on the pure Adapter interface (adapter.go:29) but structurally must probe the host (LookPath, exec `--version`). This contradicts ADR-001 / build-plan Epic 9 ("adapters own no fds/disk/sockets — core owns all lifecycle") — the exact invariant Epic 9 exists to freeze. Opus conceded "Detect is inherently impure so it can't live in a pure harness" and rated it INFO; that concession IS the flaw — the E9.2 conformance suite cannot enforce purity on a method that can't be pure. Since Epic 9 is the anti-corruption boundary frozen BEFORE the real Claude/Codex adapters (Epic 11), this must be fixed now, not scattered as host I/O across every future adapter. Resolution: descriptor-based detection (Adapter provides pure Binary()/VersionArgs()/ParseVersion(); a CORE Detect(a, HostProber) does the fd/exec).

## Consensus / verified-real (FIX)
1. **Conformance suite has gaps** (codex HIGH, Opus MEDIUM on the grid axis): shell check is argv[0]-only, so `["/usr/bin/env","sh","-c",...]` or a shell as argv[1] passes (verified); extraction totality only ever fed a nil grid, so an extractor panicking on a non-nil grid passes (BOTH reviewers); the fd/purity/determinism + fuzz run against a fixed baseAdapter, not the adapter under test. Suite has teeth for 14 violators (Opus) but these axes are real holes.
2. **E9.2 boundary muddied + grep weak** (codex HIGH, Opus LOW): the contract package does os.ReadFile (fixture.go LoadFixture); the source-grep is scoped to refadapter and bans only 6 literal tokens (misses os.ReadFile/WriteFile/ReadDir/CreateTemp, ioutil, exec run, net.Dialer/ListenConfig). Move fixture I/O out of the pure contract package; expand the banned list AND update E9.2's enumerated list to match.
3. **swarm-char doesn't actually characterize** (codex HIGH): no scenario-input driving; HookPayloads never populated; the capability entry is synthesized from the hardcoded refadapter regardless of CLI; extraction fed a nil grid. It captures PTY output but can't produce the T-6 baseline. Add scenario driving + a hook-collection sink + capability from the actual adapter.
4. **GG-5 evidence** (codex MEDIUM): epic-09-evidence.md + committed red log absent — orchestrator writes at close.

## Cleared by BOTH (not re-litigated)
The vt Close() change is correct and race-free with NO shim regression — both reviewers verified against the pinned charm source (InputPipe always returns a non-nil *io.PipeWriter; Close serializes vs Feed under e.mu; io.Pipe synchronizes CloseWithError vs the drain's Read; shim Close runs after drainDone/acceptDone). LoadFixture version rejection + Validate complete. T-5 import-boundary check has teeth. 30s fuzz of both extractors clean.

## Disposition
One combined fix round (contract revision + implementation) since the frozen Adapter interface itself changes (Detect → descriptors) — the STOP-and-report path, resolved by the orchestrator revising the contract. Re-reviewed by codex before close.
