# Epic 4 — Evidence

**Epic**: Shim process (`agents-tracker-b2l`) — the security-critical PTY-owning survivor (ADR-001)
**Commits**: 19bae14 (wire/shimwire/vt-replies + red shim suite), bac19f8 (shim engine), 25d9c8c (review fixes r1), + final fix round (escalation-join + guaranteed setsid).

## TDD evidence (GG-5)

Designer wrote the full failing suite first (49 Test/Fuzz funcs across wire/shimwire/vt/shim); red log at [epic-04-red/shim-red.txt](epic-04-red/shim-red.txt) — undefined-symbol-only failures, confirmed genuine by both reviewers. Leaf packages (wire, shimwire, vt-replies) implemented first, then the shim engine against its suite.

## Criterion walk (E4.1 – E4.6)

| Criterion | Evidence |
|---|---|
| E4.1 argv/setsid/env/cwd/pgid (S4, S-6) | injection-free spawn (metachar argv verbatim, no shell, sentinel file never created); env == cfg.Env not inherited (differential marker absent) + TERM injection; cwd; agent in own process group; `swarm shim` role setsids (guaranteed, verified by SID==PID) |
| E4.2 G2 socket | hello/hello handshake, version-mismatch reply+close, attach, resize→emulator+PTY winsize; operations gated behind hello |
| E4.3 snapshot-then-stream (S10) | barrier-synchronized boundary test (exactly-once across snapshot∪stream) + active-load test now requiring strict sequence contiguity under zero-drop |
| E4.4 TERM→grace→KILL (S5) | whole-group kill incl. same-group child; TERM-ignoring child killed at grace; cooperative exit within grace; escalation worker joined before Run returns (no post-Run stray signal) |
| E4.5 exit side-files (G3) | final-snapshot.bin decodes; exit.json code/signal correct (0/7/SIGKILL); snapshot-before-exit.json ordering + parent-dir fsync; exit.json withheld if snapshot failed; transcript Flush(timed)-before-Close(timed) |
| E4.6 daemon-death survival (S1, S9) | drains with no consumer; wedged consumer → FramesDropped climbs, grid authoritative; 30s bounded-memory soak (heap growth ~0.02 MiB) gated behind -short |

Emulator query-reply carry-forward: DSR replies piped back to the PTY via a bounded non-blocking reply pump (a flood of queries cannot wedge the drain — S9).

## Real bugs the process caught

1. **CRITICAL (codex)**: TERM→KILL escalation cancelled on leader reap, letting a TERM-ignoring child survive and hang Run on PTY EOF. Fixed: escalate on group-not-empty with a bounded EOF wait + final group KILL.
2. **Test helper bug (impl)**: bare `select{}` park tripped Go's deadlock detector once output drained; fixed to a blocking stdin read (also models real CLIs).
3. **Reply-writer S9 hole (both reviewers)**: synchronous reply write could block the drain; fixed with the async pump.
4. **Vacuous drop test (impl self-caught)**: original flood under-stressed the queue → 0 drops; rewritten to poll the real counter.
5. **Escalation goroutine outliving Run (codex re-review)**: fixed by tracking+joining the worker before Run returns.

## Review outcomes (protocol step 5 — cross-model required)

- **Opus (independent)**: FIX REQUIRED → resolved. Cleared spawn security, S10 atomicity, exit-report timeout, vt race-freedom with explicit probes.
- **codex GPT-5.6 sol (cross-model)**: FIX REQUIRED (1 CRITICAL + HIGHs) → fix round → delta re-review 10/12 OK → final round on the last 2 HIGHs (escalation-join, guaranteed setsid) → [final verdict recorded at close].
- Committee synthesis: [audit-003-epic-04.md](audit-003-epic-04.md).

## Quality gates (GG-4)

gofmt · build · vet · `go test ./... -race -count=1` (incl. 30s soak) green; shim + vt `-race -count=3` stable; wire fuzz clean.

## Accepted dispositions

- Resize out-of-range → ignore (retain last valid dims); codex confirmed safer than clamp given no resize-error response in the protocol.
- `syscall.Umask` is process-global; assumes one-shim-per-process (the deployment model) — commented in code.
