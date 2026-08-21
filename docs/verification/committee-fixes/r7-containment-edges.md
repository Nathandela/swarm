# R7 containment edge paths (codex HIGH-7) — committee fix evidence

Task D of the final audit committee fix wave. The two shim-death paths were already verified
(Sonnet and Fable probes); this closes the three UNTESTED edges around the backend record.
Baseline: main at b688097. Date: 2026-08-21.

## The three edges

1. **Record-write failure left an unrecorded live backend.** After the backend became
   servable, a failure to write backend.json (internal/shim/shim.go) was only logged and the
   session carried on watching a live, account-authenticated app-server the daemon could never
   identify — backend.json is the daemon's ONLY means of finding an orphan backend
   (internal/shim/backend.go). Unrecorded backend + uncatchable SIGKILL of the shim = an
   orphan forever.
2. **Dead leader, surviving child.** reapOrphanBackend (internal/daemon/backend.go) killed the
   group only when the recorded leader pid was alive with a matching start time; a dead leader
   meant the record was removed WITHOUT touching the group. `codex app-server` is TWO pids, so
   the vendored child could survive in the leader's group, unrecorded forever.
3. **Readiness timeout with a live child.** A backend that never became servable within
   ReadyTimeout stayed alive (own group, unrecorded) until Run finalized — indefinitely for a
   long-lived agent — the same unrecorded-orphan hole as edge 1; and an early/stale record was
   never cleaned.

## TDD failing-first (RED)

New tests written before any implementation change:

- `internal/shim/r7_edge_containment_test.go`
  (TestR7Edge_ARecordWriteFailureKillsTheUnrecordedBackend,
  TestR7Edge_AReadinessTimeoutKillsTheBackendGroupAndCleansTheRecord)
- `internal/daemon/r7_edge_reaper_test.go`
  (TestR7Edge_ADeadLeadersSurvivingChildIsStillReapedByGroup,
  TestR7Edge_ARecycledLeaderPidIsNeverSignalled — the second is the over-reach GUARD and
  passes on the unmodified code by design)

RED run, daemon (`go test ./internal/daemon -run 'TestR7Edge_' -count=1`):

    --- FAIL: TestR7Edge_ADeadLeadersSurvivingChildIsStillReapedByGroup (4.25s)
        r7_edge_reaper_test.go:86: the surviving child (pid 24316) of the dead leader (pid 24315)
        outlived the reaper. `codex app-server` is TWO pids; a reaper that only acts when the
        LEADER is alive leaves the vendored binary running, authenticated and unrecorded, forever
    FAIL    github.com/Nathandela/swarm/internal/daemon    8.603s

RED run, shim (`go test ./internal/shim -run 'TestR7Edge_' -count=1`):

    2026/08/21 12:41:51 shim: record the session backend: rename .../backend.json.tmp729015878
        .../backend.json: file exists
    --- FAIL: TestR7Edge_ARecordWriteFailureKillsTheUnrecordedBackend (10.08s)
        r7_edge_containment_test.go:249: the backend survived a FAILURE TO WRITE backend.json. ...
    --- FAIL: TestR7Edge_AReadinessTimeoutKillsTheBackendGroupAndCleansTheRecord (10.05s)
        r7_edge_containment_test.go:319: the never-servable backend's LEADER survived the
        readiness timeout while the session ran on ...
    FAIL    github.com/Nathandela/swarm/internal/shim    21.521s

Both fail for the right reason (the process survives; the write really failed with a rename
error, standing in for disk-full/permission failure). The RED log also shows
"the session backend exited with 137; ending the session" fired when the test's CLEANUP killed
the leaked backend — proving the old code was also WATCHING the unrecorded backend, i.e. its
death would have taken the agent down.

## Implementation

- `internal/shim/backend.go` — new `containBackendFailure(b, sessionDir, grace)`: TERM to the
  backend's own group, one grace, then always the final synchronous group KILL (mirrors
  finishEscalation's discipline; reaps a TERM-ignoring member, ESRCH no-op on an empty group),
  joined on the backend's dedicated Wait goroutine; then scrubs backend.json (covers
  writeBackendInfo failing AFTER its rename, and stale prior-incarnation records).
- `internal/shim/shim.go` — both failure branches of the backend spawn path now call it:
  readiness failure (berr) and record-write failure (werr). Both then `srv.setBackendPgid(0)`
  so finalization — possibly hours later — never re-KILLs a fully-reaped, possibly-recycled
  pgid. `backendWatch` is set ONLY when the backend is serving AND recorded, so a
  deliberately-killed backend can never fire the backend-died-first edge. The write failure is
  surfaced in the log and, through the existing join, in exit.json's backend_exit.
- `internal/daemon/backend.go` — reapOrphanBackend now has three arms:
  - leader alive + identity match: kill(-pgid, SIGKILL) (unchanged);
  - leader pid alive but identity mismatched/unreadable: NEVER signalled (POSIX only recycles
    a pid once its old group has no members, so nothing of ours can remain; the
    unreadable-start-time residual is logged loudly), record still dropped;
  - leader gone: probe kill(-pgid, 0). Success means surviving members that are provably ours
    (a group id cannot be reused while members remain, so with the leader dead this can never
    be pid reuse) — kill the group. ESRCH means empty. Anything else (EPERM) means the group
    cannot be validated: logged LOUDLY, never signalled, and the record is still removed —
    an unprovable record can never become provable, and keeping it would pin every future
    reconcile on the same dead-end kill.

## GREEN

    --- PASS: TestR7Edge_ADeadLeadersSurvivingChildIsStillReapedByGroup (1.36s)
    --- PASS: TestR7Edge_ARecycledLeaderPidIsNeverSignalled (0.68s)
    ok      github.com/Nathandela/swarm/internal/daemon    4.826s
    --- PASS: TestR7Edge_ARecordWriteFailureKillsTheUnrecordedBackend (2.67s)
    --- PASS: TestR7Edge_AReadinessTimeoutKillsTheBackendGroupAndCleansTheRecord (1.11s)
    ok      github.com/Nathandela/swarm/internal/shim    5.216s

Whole R7 families re-run green: `go test ./internal/shim -run TestR7` ok (9.86s),
`go test ./internal/daemon -run TestR7` ok (6.74s). No existing assertion weakened; the
pre-existing never-servable fence (agent still launches, no backend.json) still holds.

## Mutation proofs (cp backup, run, cmp-verified restore — all four)

| # | Mutation (applied to the IMPLEMENTED code) | Test that must fail | Result |
|---|---|---|---|
| M1 | write-failure branch reverted to old behavior (log only, `backendWatch = backend`) in shim.go | TestR7Edge_ARecordWriteFailureKillsTheUnrecordedBackend | FAIL (backend survived / watched death ended the session); restore `cmp` byte-identical |
| M2 | `containBackendFailure` disabled on the berr branch (`if false && backend != nil`) in shim.go | TestR7Edge_AReadinessTimeoutKillsTheBackendGroupAndCleansTheRecord | FAIL at r7_edge_containment_test.go:319 (leader survived); restore `cmp` byte-identical |
| M3 | dead-leader arm's `syscall.Kill(-pgid, SIGKILL)` deleted in daemon/backend.go | TestR7Edge_ADeadLeadersSurvivingChildIsStillReapedByGroup | FAIL at r7_edge_reaper_test.go:82 (child survived); restore `cmp` byte-identical |
| M4 | alive-but-mismatched guard arm dropped (`case false && pidAlive(...)`) in daemon/backend.go | TestR7Edge_ARecycledLeaderPidIsNeverSignalled | FAIL at r7_edge_reaper_test.go:123 (stranger's group signalled); restore `cmp` byte-identical |

Each restore was performed with `cp` from the backup and verified with `cmp` (reported
`RESTORED-BYTE-IDENTICAL`). The RED runs above additionally prove the delete-the-call mutant
for M1–M3 against the original code.

## Gates

    go build ./...                      -> OK
    go vet ./...                        -> OK
    golangci-lint run                   -> 0 issues.
    go test -race -count=1 ./internal/shim    -> ok (91.6s)
    go test -race -count=1 ./internal/daemon  -> ok (56.0s)

## Process hygiene

All fixtures are test-binary re-execs and /bin/sh//bin/sleep — never swarm-remote or the real
codex CLI. Every spawned pid is registered in t.Cleanup with explicit kill. New daemon tests
use launchAnnounce (killTree cleanup) rather than the bare d.Launch pattern. After each run,
`ps -axo pid,ppid,pgid,command` was swept for ppid==1 stragglers; the shims/agents leaked by
the PRE-EXISTING d.Launch-style R7 tests during the full-suite runs were reaped by explicit
pid (24297/24314/24335/24336 after RED; 29985–30021 plus agent children after the -race run).

## Residuals

- The reaper's EPERM ("cannot validate") arm is implemented and documented but not exercised
  by a test: synthesizing a same-user group with an unsignalable member is not portably
  possible from a unit test. It refuses to signal and removes the record, matching the task's
  instruction.
- The pre-existing shim/agent leak in the OLD daemon R7 tests (d.Launch without killTree
  cleanup) is out of this task's footprint; noted for a follow-up issue.
- containBackendFailure blocks the (already-failed) spawn path for at most one GraceTimeout
  when a backend ignores TERM — bounded, and only on the failure path.
