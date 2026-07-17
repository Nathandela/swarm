# Audit 003 — Epic 4 (shim process)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model, required for security-critical epics) + Opus (independent). agy quota-blocked.
**Verdict**: FIX REQUIRED (both). No broken happy-path behavior — 22/22 tests green under -race — but a CRITICAL group-signal escalation bug plus robustness/coverage gaps in the security-critical core.

## Consensus (both reviewers)
- Emulator query-reply writer can block the PTY drain (agent-triggerable S9 hole): flood of DSR/DA queries with an agent not reading stdin → PTY input fills → reply write blocks → vt drain stalls → Feed blocks under hub.mu → drain stops. codex HIGH, Opus MEDIUM. FIX.
- `swarm shim` role still a stub; no shim-process setsid (E4.1 "Shim setsids", D-3). codex HIGH, Opus MEDIUM (offered Epic-5 waiver). Decision: IMPLEMENT the role now (it is Epic 4 scope) + setsid.
- Empty/nil Argv panics instead of clean setup error. Both LOW/MEDIUM. FIX.
- UDS mode set after bind (TOCTOU); socket left behind on shutdown. Both LOW. FIX (umask-around-bind + remove-on-close).

## codex-only (accepted)
- **CRITICAL**: TERM→KILL escalation cancels when the group LEADER is reaped, not when the group is empty — a TERM-ignoring child survives forever, and Run then blocks on PTY EOF indefinitely. Same root as codex #5 (natural exit waits on PTY EOF unconditionally; a descendant holding the slave hangs Run). FIX: escalate on group-not-empty; bound the post-exit EOF wait then KILL the group.
- Transcript Flush() has no timeout, so a wedged sink hangs before the timeout-protected Close ever runs (defeats the bead's Flush-before-Close-under-timeout contract). FIX.
- G3 persistence errors silently swallowed; Run returns nil even if snapshot write failed (breaks "exit.json presence implies complete snapshot"); no parent-dir fsync after rename. FIX.
- Resize accepts negative/huge cols/rows from an untrusted socket → emulator panic/OOM; no handshake-state gate (resize/input/signal accepted before hello). FIX (clamp + require hello first).
- E4.6 ≥30s soak + memory-bound not tested (only the drop path + grid authority). FIX (soak test).
- Active-load S10 test can't detect a dropped middle frame (N10,N12 passes). FIX (strengthen).

## Cleared by Opus (with what was tried) — NOT re-litigated
Spawn no-shell + env-verbatim + non-leak (non-vacuous); S10 boundary genuinely atomic under hub.mu; exit-report send bounded by 2s timeout (wedged client cannot stall exit); G3 ordering + return codes correct; post-reap KILL race sub-quantum/negligible; vt Close race-free; MaxFrame enforced before alloc; red log genuine.

## Disposition
One consolidated fix round: production fixes (internal/shim, internal/vt, cmd/swarm) to an Opus implementer with new tests; test-strengthening (active-load S10, soak) to the test designer. Re-reviewed by codex (delta) before close. Evidence: docs/verification/epic-04-evidence.md at close.
