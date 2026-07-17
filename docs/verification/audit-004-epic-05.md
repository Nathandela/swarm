# Audit 004 — Epic 5 (daemon core)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model) + Opus (independent). agy quota-blocked.
**Verdict**: FIX REQUIRED. The reviewers diverged sharply and the divergence was productive.

## Divergence (the committee working)
codex returned CRITICAL + 6 HIGH; Opus returned 1 blocking MEDIUM + 2 LOW and explicitly CLEARED (with tests) the three areas codex flagged hardest. Orchestrator adjudicated by reading the code:
- **S11 crash window** (codex CRITICAL): Opus correct — `waitShimServing` runs BEFORE `probe(phaseSpawned)`, no defer cleanup, phaseReserved/Spawned/Confirmed crash tests pass. The core mechanism WORKS. Downgraded. BUT codex's residual sub-point is real: the post-spawn `processStartTime` error is discarded (launch.go:121), persisting ShimStartTime=0 → a later reconcile marks a live shim lost. Both reviewers flagged this (Opus #3 LOW). FIX.
- **S3 signal-time recheck** (codex HIGH #2): Opus correct — pollMonitor re-verifies (PID, start-time) every 100ms; reconnect gated on the real G2 hello. Downgraded to a cheap pre-signal recheck hardening.
- **S12 singleton**: both CLEAR.

## Consensus / verified-real (FIX)
1. **Restart PID-reuse** (codex#3 HIGH, opus#1 blocking): pidfile stores only PID; stopRunningDaemon SIGTERMs it with no start-time check → after crash+PID-reuse, `swarm daemon restart` signals an unrelated process. The exact S3 bug in the daemon's own pidfile.
2. **Launch identity error discarded** (codex#1 residual, opus#3): launch.go:121 drops the processStartTime error.
3. **Delete/exit resurrection race** (codex#4): verified — Delete's registry removal is not atomic with the exit handler's side-file merge; a concurrent handleShimExit can saveMeta after store.Delete, recreating the session dir.
4. **reconcile swallows errors** (codex#7): persist.Scan failure and merge/lost-write errors discarded → daemon can serve a blind/incomplete registry.
5. **D-1 spawner cold-start** (codex#6): defaultSpawnDaemon opens the log before creating the state dir → ENOENT on a truly cold start. Plus D-8 skew text omits the "restart is safe" statement.

## Test fidelity (STRENGTHEN)
6. Real subprocess `kill -9` for E5.8/S1 (codex#5) — abandon() models fd-drop but not reparenting/fd-inheritance/mid-write; the daemon role is now wired, so a real kill-9 test is feasible and much stronger.
7. Concurrent two-process singleton (codex#10). 8. WireVersion skew check + rejection test (codex#8). 9. Perm sweep: chmod existing log 0600, broaden test (codex#9). Real Restart() path test (opus#2, codex).

## Deferred (noted on beads)
Production `swarm list`/EnsureDaemon client caller → Epic 6/7 (client layer). Daemon serving loop + real kill-9 testability land now.

## Cleared by both (not re-litigated)
G6 sole-writer, linux /proc field-22 parse robustness, darwin kinfo_proc, zombie/reaped-PID safety, lowercase ids + FilterEnv on shim env, R-3/S-7 cap (>= no off-by-one), bare `swarm daemon` satisfies TestDispatch.
