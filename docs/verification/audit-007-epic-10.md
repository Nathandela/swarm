# Audit 007 — Epic 10 (status detection engine)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model) + Opus (independent, ran -race incl. -tags integration). agy quota-blocked.
**Verdict**: FIX REQUIRED. Strong convergence: the engine CORE is correct (both cleared S6 auth, S7 staleness/precedence, CPU sampler, heuristic determinism, no-TOCTOU, fail-closed sequence). The issues are at the transport/wiring seam + a real sequence-source bug.

## Consensus — FIX (Epic 10)
1. **Monotonic sequence source is broken** (codex HIGH, Opus HIGH): hookclient reads SWARM_HOOK_SEQ from spawn-time env, which is CONSTANT across per-event `swarm hook` invocations → the engine accepts callback #1 and rejects ALL subsequent (seq <= lastSeq). G5 is in Epic 10's scope; the env-var contract cannot satisfy it. Fix: a per-session monotonic counter each hook invocation atomically increments (counter file), not env.
2. **applyPayload stores unvalidated status strings** (both): an authenticated-but-malformed payload yields out-of-vocabulary Turn/Interaction. Fix: validate against the status vocabulary; reject without advancing the sequence or emitting.
3. **Emit invoked under e.mu** (both): a wedged subscriber stalls the whole engine, contradicting L1/P-3. Fix: call Emit OUTSIDE the lock.
4. **E10.6 two-platform verification missing** (codex HIGH): only the integration-tagged CPU test; no deterministic parser UNIT tests; CI doesn't run the integration tag; macOS only builds. Fix: add /proc + taskinfo parser unit tests (deterministic fixtures) in normal CI + a macOS CI job exercising the darwin sampler.
5. **Heuristic false-actives** (Opus LOW): a markdown "| " row trips the ASCII-spinner branch; any braille rune anywhere → active. Fix: tighten to trailing-animation context (still CLI-agnostic; per-CLI refinement is Epic 11).

## Load-bearing new work (Epic 10)
6. **Launch-side injection** (E10.1/G4): the launch path must inject, POST-FilterEnv, the per-session random token + SWARM_DAEMON_SOCK + SWARM_SESSION_ID + the sequence-counter path, and register the session with the engine (token+pid+sources). Only the READER half exists today.
7. **engine.Run(ctx, pollInterval)**: the staleness/fallback poll must actually run — a driver goroutine that periodically Ticks. PollInterval is stored but has no caller.

## Explicit deferral to Epic 8 (walking skeleton = end-to-end assembly) — BOTH reviewers concur it's defensible IF acknowledged
The full daemon assembly — runDaemon constructing+running the engine, the daemon socket receiving hook-callback connections → HandleCallback, engine.Emit → Epic 6 fan-out to subscribers, engine.Run started — is the walking-skeleton wiring (Epic 8 already assembles protocol.Server into the daemon). Recorded on the Epic 8 bead; noted in Epic 10 evidence. Epic 10 delivers the mechanism (engine + hook + injection + Run + Emit→persist half) as tested units; Epic 8 assembles the client-facing serving + fan-out.

## Cleared by both (not re-litigated)
S6/G5 auth airtight (tokenless/foreign/replay/post-end/unregistered all no-op, under one mutex, fail-closed at uint64 max); S7 staleness conjunction + sampler-error→unknown + precedence both directions; idempotence; darwin proc_taskinfo 96-byte layout + syscall signature (Opus ran unsafe.Sizeof); linux /proc parse robustness; cgo-free CGO_ENABLED=0; determinism/no-busy-poll/single-writer G6; the integration CPU test actually ran on darwin (busy>idle).
