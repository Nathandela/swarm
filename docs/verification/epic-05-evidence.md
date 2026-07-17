# Epic 5 — Evidence

**Epic**: Daemon core — lifecycle, registry, orchestration (`agents-tracker-s95`) — the kill-9-survival lifecycle authority (S1)
**Commits**: fa99757 (core), fd5a83c (fixes r1), 9afa219 (fixes r2), ce1b8ab (N2-residual), 0087161 (N2 containment), df3ac2f (signal-handler join).

## TDD evidence (GG-5)

Designer wrote 11 failing test files first; red log (undefined-only) in [epic-05-red/daemon-red.txt](epic-05-red/daemon-red.txt), confirmed genuine by both reviewers. Every fix round wrote failing tests first (verified red-to-green by reverting each production fix).

## Criterion walk (E5.1 – E5.11)

| Criterion | Evidence |
|---|---|
| E5.1 flock singleton (S12) | flock-before-bind, stale-socket reclaim under lock, ErrAlreadyRunning; strengthened to a TWO-REAL-PROCESS race (exactly one wins) |
| E5.2 rebuild + reconnect + lost (S3) | registry from meta scan; (PID, process-start-time) identity reconnect via real G2 hello with WireVersion check; mismatch/reaped → lost, zero signals |
| E5.3 reconnect-before-lost | onMetaSave observer proves a live shim is never transiently persisted lost |
| E5.4 two-phase launch (S11) | crash injection at each phase leaves no orphan/phantom; waitShimServing before phaseSpawned; identity saved BEFORE agent spawn (agent-exists ⟹ reconnectable); un-trackable shim killed, never ShimStartTime=0 |
| E5.5 side-file merge (G6) | exit.json/final-snapshot → meta; daemon sole writer (shim-never-writes-meta test) |
| E5.6 kill/delete/cap (R-3, S-7) | delete kills-then-removes with a tombstone against exit-handler resurrection; configurable cap rejects before spawn; pre-signal (PID,start-time) identity recheck |
| E5.7 perms (D-6) | state dir 0700, socket/lock 0600, daemon log chmod 0600 on every open |
| E5.8 kill-9 survival (S1, L2) | **REAL subprocess SIGKILL**: a genuine daemon process running production Open/Launch is killed -9; every agent survives (reparent + fd independence); a fresh daemon reconnects and drives all |
| E5.9 D-1 auto-start | EnsureDaemon dial→spawn detached→backoff→idempotent; spawner creates state dir before opening log. (Production `swarm list` caller deferred to Epic 6/7 — client layer.) |
| E5.10 version-skew smoke | WireVersion compared; skewed shim → lost, not adopted |
| E5.11 restart (D-8) | pidfile stores PID+start-time; Restart verifies before SIGTERM, waits for the flock to free, Dial-confirms the replacement before reporting success; skew error states restart is safe |

## Committee (audit-004) — productive divergence

codex returned CRITICAL+6 HIGH; Opus returned 1 blocking MEDIUM+2 LOW. Orchestrator adjudicated by reading code: Opus was right that the S11 crash-window and S3 signal-recheck CORE MECHANISMS work and are tested (codex's CRITICAL/HIGH downgraded); codex was right on the residuals Opus under-rated (restart PID safety, discarded launch identity error, delete/exit resurrection race, reconcile error-swallowing). Full synthesis: [audit-004-epic-05.md](audit-004-epic-05.md).

Four fix rounds followed, each cross-model-verified by codex:
- r1/r2: all 10 findings → OK. Then codex found 2 NEW HIGHs the fixes introduced (Restart false-success; launch cleanup orphaning the agent).
- N2 took three iterations because each fix exposed a subtler race; resolved at the root: **the shim now installs a signal handler (armed before it spawns the agent) that kills the agent group on any catchable termination**, so daemon cleanup just SIGTERMs the shim — no socket-timing dependency, and it hardens shutdown/restart too. The handler is joined before finalization (no post-finalization signal to a reused pgid).
- **Final codex verdict: APPROVE N2.** Only residual: an uncatchable SIGKILL of the shim itself (documented, error-surfaced).

Note: the containment handler was an additive, review-driven improvement to the Epic 4 shim (internal/shim); the Epic 4 suite stays green. Recorded on bead agents-tracker-a7d for Epic 14 to assert the arm-before-spawn ordering.

## Quality gates (GG-4)

gofmt · build · vet · GOOS=linux build · `go test ./internal/shim/ ./internal/daemon/ ./cmd/swarm/ -race -count=3` all green (real-subprocess kill-9 + two-process singleton included).
