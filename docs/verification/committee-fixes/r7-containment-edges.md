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

## Round 3 — the two NEW containment interleavings (codex round-2 finding 4)

Audit-committee round-3 fix wave. The round-2 edge fixes above hold; these are two further
interleavings around them. Baseline: main at 5856a4e. Date: 2026-08-21.

### The two races

1. **A Kill/Delete during backend startup was LOST.** The shim's signal plane starts before
   either contained process group exists (internal/shim/shim.go: startPlanes runs before
   startBackend; the agent spawns only after the go-ahead), and killGroups rightly never
   signals a zero pgid (kill(-0, sig) signals the shim's OWN group) — but nothing REMEMBERED
   the request and nothing replayed it. With production grace (2 s) far shorter than the
   startup stages (ReadyTimeout / GoAheadTimeout, up to 20 s each), the escalation worker's
   KILL also fired into the same empty pgids. Net effect: the backend and then the agent
   SPAWN AFTER their termination was requested and survive it indefinitely, on a session the
   daemon believes it killed.
2. **markLost reaped nothing.** Kill's pre-signal identity recheck over an already-dead shim
   calls markLost (internal/daemon/lifecycle.go), which finalized the session LOST with no
   backend sweep. Once persisted LOST, the monitor's handleShimExit takes its
   already-terminal early return (internal/daemon/reconcile.go) BEFORE its own
   reapOrphanBackend call — a SIGKILLed shim writes no exit.json — and reconcile's orphan
   arm only visits sessions persisted RUNNING, so no restart revisits it either: the orphan
   backend survives indefinitely.

### TDD failing-first (RED)

New tests written before any implementation change (the only production edit preceding RED
is the nil-in-production test seam `testHookBeforeBackendSpawn` in shim.go, mirroring the
existing `testHookAfterSignalArm` precedent):

- `internal/shim/r7r3_startupkill_test.go`
  (TestR7R3StartupKill_ATerminationObservedBeforeAGroupExistsIsReplayed — unit, both
  setters, including the escalated-KILL-on-replay part against a TERM-ignoring leader;
  TestR7R3StartupKill_AKillDuringBackendStartupLeavesNoSurvivors — through shim.Run on the
  exact interleaving: the kill verb observed via onSignal while BOTH pgids are zero, backend
  and idle agent both born after it)
- `internal/daemon/r7r3_marklost_test.go`
  (TestR7R3MarkLost_AKillObservingADeadShimStillReapsTheBackend — drives d.Kill, the real
  production caller of markLost, against a session whose recorded shim is dead and whose
  recorded backend is a live group)

RED run, shim (`go test ./internal/shim -run 'TestR7R3StartupKill' -count=1`):

    --- FAIL: TestR7R3StartupKill_ATerminationObservedBeforeAGroupExistsIsReplayed (10.09s)
        r7r3_startupkill_test.go:76: the backend group was created AFTER the kill was observed
        and survived it ...
        r7r3_startupkill_test.go:91: the agent group recorded AFTER the whole TERM->grace->KILL
        escalation ran into empty pgids survived ...
    --- FAIL: TestR7R3StartupKill_AKillDuringBackendStartupLeavesNoSurvivors (15.12s)
        r7r3_startupkill_test.go:143: shim.Run is still running 15s after its session was
        KILLED ... the backend and the idle agent both spawned AFTER termination was requested
        and survived it
    FAIL    github.com/Nathandela/swarm/internal/shim    26.562s

RED run, daemon (`go test ./internal/daemon -run 'TestR7R3MarkLost' -count=1`):

    --- FAIL: TestR7R3MarkLost_AKillObservingADeadShimStillReapsTheBackend (10.12s)
        r7r3_marklost_test.go:81: the backend survived Kill's markLost path ...
        r7r3_marklost_test.go:86: backend.json survived the markLost reap ...
    FAIL    github.com/Nathandela/swarm/internal/daemon    12.714s

All three fail for the right reason: the processes survive a termination the system accepted.

### Implementation

- `internal/shim/server.go` — new `pendingSig` under the existing pgidMu: every kill records
  the strongest termination signal observed so far (SIGKILL is sticky over SIGTERM, so a
  group born after the one-shot escalation worker already fired gets the escalated KILL, not
  the stale TERM); `setAgentPgid`/`setBackendPgid` REPLAY it the moment a group is recorded.
  The record-read and the pgid-write share one pgidMu critical section, so no interleaving
  with a concurrent killGroups can miss: whichever runs second sees the other's effect. The
  `pgid > 0` guard keeps the deliberate `setBackendPgid(0)` after containBackendFailure, and
  kill(-0), impossible. Every termination entry funnels through killGroups (onSignal serves
  both the socket TypeSignal verb and the OS-signal handler; the escalation worker and
  finishEscalation call it too), so the memory cannot be bypassed.
- `internal/shim/shim.go` — the nil-in-production seam `testHookBeforeBackendSpawn`, invoked
  after startPlanes and immediately before startBackend; no production behavior change.
- `internal/daemon/lifecycle.go` — markLost now runs `d.reapOrphanBackend(id)` before its
  finalize, making it the THIRD death path with the same sweep handleShimExit and
  reconcile's orphan arm already have (reap-then-finalize, mirroring handleShimExit). A
  no-op for any session with no backend record.

### GREEN

    ok      github.com/Nathandela/swarm/internal/shim    1.780s   (TestR7R3StartupKill)
    ok      github.com/Nathandela/swarm/internal/daemon  2.943s   (TestR7R3MarkLost)

The lost-kill integration case drops from a 15 s hang to a session fully finalized in under
2 s, with the backend joined dead before Run returns.

### Mutation proofs (cp backup, run, cmp-verified restore — all five)

| # | Mutation (applied to the IMPLEMENTED code) | Test that must fail | Result |
|---|---|---|---|
| M1 | killGroups' remembering deleted (pendingSig never set) in shim/server.go | both TestR7R3StartupKill tests | FAIL (both, verbatim the RED failures); restore `cmp` byte-identical |
| M2 | setBackendPgid's replay deleted in shim/server.go | TestR7R3StartupKill_ATerminationObserved... part 1 | FAIL at r7r3_startupkill_test.go:76; restore `cmp` byte-identical |
| M3 | setAgentPgid's replay deleted in shim/server.go | TestR7R3StartupKill_ATerminationObserved... part 2 | FAIL at r7r3_startupkill_test.go:91; restore `cmp` byte-identical |
| M4 | KILL made non-sticky (`if s.pendingSig == 0` only: first signal wins) in shim/server.go | TestR7R3StartupKill_ATerminationObserved... part 2 | FAIL at r7r3_startupkill_test.go:104 (stale TERM replayed at a TERM-ignoring leader); restore `cmp` byte-identical |
| M5 | `d.reapOrphanBackend(id)` deleted from markLost in daemon/lifecycle.go | TestR7R3MarkLost_AKillObservingADeadShimStillReapsTheBackend | FAIL at r7r3_marklost_test.go:81 and :86; restore `cmp` byte-identical |

M4 initially SURVIVED, exposing a fixture race, not an implementation gap: the replayed TERM
could land before the sh leader had armed `trap '' TERM`, killing it and vacuously passing.
The fixture now blocks on a readiness byte the script writes only after the trap is armed
(r7r3Group); with that, M4 fails deterministically. No assertion was weakened.

### Gates

    go build ./...                            -> OK
    go vet ./...                              -> OK
    PATH=$HOME/go/bin:$PATH golangci-lint run -> 0 issues.
    go test -race -count=1 ./internal/shim    -> ok (92.2s)
    go test -race -count=1 ./internal/daemon  -> ok (52.5s)

### Process hygiene

Fixtures are the existing test-binary re-execs (r7BackendBind, modeIdle, r7Squatter) and
/bin/sh//bin/sleep — never swarm-remote or the real codex CLI. Every spawned group is
registered in t.Cleanup with an explicit kill(-pgid) and a Wait-based reap (kill(pid, 0) on
a zombie succeeds, so only the Wait proves death); the integration test's failure path first
kills the backend group so the backend-died edge contains the idle agent and Run returns
before the test exits. After the full -race runs, the six shims leaked by the PRE-EXISTING
d.Launch-style R7 daemon tests (the round-2 residual, out of footprint) were reaped by
explicit pid via SIGTERM to their armed handlers (11895/11899/11905/11908/11911/11931);
older ppid==1 stragglers from other sessions' runs were left untouched.

### Residuals

- On a killed backend session, the shim still waits out GoAheadTimeout before spawning (and
  instantly reaping) the agent, so Run's exit after a startup-window kill can lag by up to
  that bound. Containment is complete either way — replay guarantees no group survives — and
  shortening the wait would mean threading a cancel into waitBackendGoAhead for a latency-only
  gain; left out as beyond this finding's scope.
- The momentary birth-then-replay of a group after a kill (rather than suppressing the spawn
  outright) is the deliberate lean variant the finding allows; the replayed signal is issued
  under the same pgidMu ordering that makes it race-free.

## Round 4 addendum: the reap is one critical section (r7-check finding 1)

markLost (Kill's pre-signal identity recheck, run on an RPC goroutine) and
handleShimExit (the shim monitor) both call reapOrphanBackend(id) for the same session
with nothing serializing them (internal/daemon/backend.go). The sequence is
read-validate-kill-remove over shared on-disk state: two interleaved runs can both read
backend.json and both validate the recorded (pid, start-time); the first kills the
group and removes the record, and a pid recycled inside that window hands the second
one's already-validated signal to a stranger's group. Astronomically narrow -- it needs
a full pid recycle inside a microsecond window -- but it is the same TOCTOU class the
identity check exists to close.

Fix: `reapMu sync.Mutex` on Daemon (a leaf lock beside tombMu; reaps happen only on
session death, so daemon-wide is the simple shape) locked for the whole of
reapOrphanBackend.

### TDD failing-first (RED)

New test `internal/daemon/r7r4_reapmutex_test.go`
(TestR7R4_ConcurrentShimDeathPathsSerializeTheReap): hooks the procStartTimeFn seam --
backendAliveAt is its only reader and reapOrphanBackend is backendAliveAt's only caller,
so the hook sits inside the reaper's validate step -- counts concurrent entrants for the
recorded backend pid while holding the validate open 150ms, and reports the identity
unreadable so both runs take the arm that signals nothing. It then drives the two REAL
death paths concurrently: d.Kill (dead recorded shim, so the markLost path) against
d.handleShimExit. RED on the pre-fix code (`go test ./internal/daemon -run
'TestR7R4_' -race -count=1`):

    r7r4_reapmutex_test.go:112: reapOrphanBackend entered its identity validation
    1 time(s) while another reap of the same session was still inside its own
    read-validate-kill-remove sequence. ...
    --- FAIL: TestR7R4_ConcurrentShimDeathPathsSerializeTheReap (0.28s)

GREEN with the mutex: `ok github.com/Nathandela/swarm/internal/daemon 6.263s`.

### Mutation

Backed up internal/daemon/backend.go to /tmp/backend.go.bak, deleted the
`d.reapMu.Lock()/defer d.reapMu.Unlock()` pair, re-ran:

    --- FAIL: TestR7R4_ConcurrentShimDeathPathsSerializeTheReap (0.27s)

Restore: `cmp` identical (RESTORE-BYTE-VERIFIED), re-run: ok (4.137s).

### Accepted blast radius: markLost's false identity mismatch now costs the backend

shimIdentityMatches (internal/daemon/lifecycle.go) treats a processStartTime ERROR the
same as a mismatch, so a transient start-time read failure for a shim that is actually
LIVE sends Kill down the markLost path. Before round 3 that mis-cost was the LOST label
alone (and a shim left unsignalled); since markLost gained its reap, the same false
negative also reaps the session's backend out from under the live shim. Recorded as
ACCEPTED, because:

- A start-time read error for a live pid is procfs/sysctl failing on a running process
  -- not a state the daemon can distinguish from the recycled-pid case it exists to
  guard, and one the platform makes essentially unobservable in practice.
- The alternative (skipping the reap when the mismatch might be transient) reopens the
  orphan-forever hole round-2 codex finding 4 closed: once markLost persists LOST,
  handleShimExit early-returns before ITS reap and reconcile never revisits a
  non-running session, so an unreaped backend here is unreaped forever.
- The session is finalized LOST either way; a backend deliberately left alive behind a
  LOST session is exactly the unaccounted, account-authenticated survivor class R7
  exists to eliminate. The reap also re-validates the BACKEND's own recorded identity
  before signalling, so even on this path it kills only what backend.json provably
  names.
