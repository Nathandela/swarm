# Epic 10 — Evidence

**Epic**: Status detection engine (`agents-tracker-8lr`) — authenticated status (S6), never-confidently-wrong (S7)
**Commits**: 9c9642d (engine), 8844af3 (fixes r1 + linux build fix), 32d24ae (per-dimension sequence).

## TDD evidence (GG-5)

Designer wrote the failing suites first; red log in [epic-10-red/engine-red.txt](epic-10-red/engine-red.txt) (undefined-only, verified genuine by both reviewers). Fix rounds wrote failing tests first.

## Criterion walk (E10.1 – E10.8)

| Criterion | Evidence |
|---|---|
| E10.1 hook transport (G4) | `swarm hook <event>` builds an authenticated Callback from env (token + socket + a per-session sequence-counter file) and posts it; launch injects all four POST-FilterEnv (crypto/rand token, socket, session id, seq-file path) |
| E10.2 S6 negative auth (G5) | tokenless/foreign/replayed/post-EndSession/unregistered all no-op (error + no emit) — both reviewers traced airtight, under one mutex, fail-closed at uint64 max |
| E10.3 staleness guard (S7) | active ∧ no-output ∧ no-CPU past threshold → unknown; a sample error → not-proven-busy → unknown (never confidently active) |
| E10.4 precedence | a fresher typed signal beats the heuristic; the heuristic never overrides a fresher typed signal (both directions) |
| E10.5 idempotence | per-dimension sequence high-water: exact replay rejected, stale same-dimension rejected (no regression), out-of-order disjoint-dimension concurrent hooks BOTH accepted |
| E10.6 CPU sampling | cgo-free (proc_info syscall darwin, /proc linux, x/sys/unix); pure parsers unit-tested on BOTH platforms in normal CI + a macOS CI job + real-process integration |
| E10.7 fan-out (V-2/L1 engine half) | Emit synchronous on the triggering event, run OUTSIDE the mutex (per-session ordered) so a wedged subscriber can't stall the engine |
| E10.8 heuristic + poll (T-3/T-4) | grid heuristic (animation-position spinner→active, prompt+cursor→idle, else unknown), CLI-agnostic, deterministic; engine.Run drives the low-frequency staleness poll; no busy-poll |

## Committee (audit-007) — strong convergence

Both reviewers cleared the engine CORE (auth, staleness, precedence, CPU sampler, determinism, no-TOCTOU). Both flagged the sequence-source bug (a spawn-env seq is constant across per-event hooks → only callback #1 accepted). Fix arc:
- Sequence → per-session flock'd counter file. codex re-review then found a subtler HIGH: the counter allocates collision-free but concurrent hooks release the lock before posting, so out-of-order delivery drops a legitimate lower sequence. My proposed flat anti-replay window CONTRADICTED the frozen no-regression tests; the implementer instead built **per-dimension sequence high-water** — tolerates concurrent disjoint-dimension reordering, rejects replay, preserves per-dimension no-regression. **codex APPROVE.**
- Pure-engine fixes: payload vocabulary validation, Emit-outside-mutex, heuristic animation-position tightening, launch-side token injection, engine.Run poll loop — all codex-confirmed.
- **Also fixed a critical cross-platform bug**: `syscall.Getsid` (darwin-only, from the Epic 5 shim-session work) broke the linux build of cmd/swarm → `golang.org/x/sys/unix.Getsid`.

Synthesis: [audit-007-epic-10.md](audit-007-epic-10.md).

## Explicit deferral to Epic 8 (both reviewers concurred)

The full daemon assembly — runDaemon constructing the engine + RegisterSession + starting engine.Run; the daemon accept loop routing hook callbacks → HandleCallback; engine.Emit → Epic 6 fan-out to subscribers — is the walking-skeleton composition (Epic 8 already assembles protocol.Server into the daemon). Recorded on the Epic 8 bead. Epic 10 delivers the engine + hook + injection + Run + Emit-half as tested units.

## Quality gates (GG-4)

gofmt · build (incl. GOOS=linux) · vet · `go test ./internal/engine/ ./internal/hookclient/ ./internal/daemon/ ./cmd/swarm/ -race -count=3` green; engine `-tags integration` (real CPU sampler) green on darwin.
