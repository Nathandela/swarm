# GREEN evidence: Wave R7 round 3 -- the two BLOCKING and four MEDIUM review findings, fixed and mutation-proved

- **Date:** 2026-08-20 (UTC; per-command timestamps in `r7-green-round3.txt`)
- **Role:** GREEN (implementer), round 3. Rounds 1 and 2 are `README.md` and `README-round2.md`
  beside this file; RED is `docs/verification/r7-red/` (`r7-red-round3.txt` for this round)
- **Bead:** `agents-tracker-hggx.8`
- **Design:** ADR-013 Amendment 2026-08-20 (§R7.2c and §R7.10 amended this round), ADR-010
  Amendment 2026-08-20

## What a reader CAN and CANNOT conclude

**CAN.** Both BLOCKING findings are fixed and every fix is proved by MUTATION -- the production
line is changed, the named test fails, the failure message is printed in `r7-green-round3.txt`,
the change is reverted. **Eight of eight mutations fire.** The wave's machine-side topology is
now driven end to end by ONE test that assembles daemon -> shim -> app-server -> agent out of
production code, which is what round 2 had nowhere in the tree and what let its single most
load-bearing line be deleted with the whole `internal/skeleton` package still green.

**CANNOT.** *The wave's exit criterion is still not demonstrated.* No test has driven a real
Codex TUI and the phone against one live thread; the two probes that would are behind
`//go:build realcli` and were not run this round. The topology test proves the WIRING with a
fake `codex` in both its modes -- it proves the agent is launched as a client of the socket the
shim served and that the daemon adopts the thread that server announces, and it proves nothing
about the real CLI's own behaviour. No Android work is in this slice, and no Codex-shaped
Android test exists.

**And one thing a reader must NOT over-read about a phone-answered approval.** See "Disclosed,
not fixed" below: when the owner answers at the terminal inside the phone's send window, the
journal records the PHONE's decision by:phone and the phone is told its approve FAILED. Exactly
one decision is ever applied and it is the server's -- the safety property holds -- but the
RECORD is wrong in that window and the protocol offers nothing better.

## The findings and their fixes

| # | Finding | Fix | Production file:line |
|---|---|---|---|
| B1 | the daemon's decision to send the shim the agent arguments (`--remote unix://SOCK`) was fenced by NOTHING; changing it to `nil` left the whole package green | no production change was needed -- the line was correct -- but the test that makes it *unremovable* now exists, and it assembles the whole topology out of production code | fences `internal/skeleton/backendconnect.go:187` |
| B2a | a shim killed while the daemon is UP left its backend alive forever (`handleShimExit` reaped nothing, and reconcile never revisits a session no longer persisted RUNNING) | `handleShimExit` calls the reaper on the path that is reached while this daemon is up | `internal/daemon/reconcile.go:187` |
| B2b | the reconcile call site that WAS shipped was fenced by nothing (both reaper tests called the helper directly) | a test that drives `swarm daemon restart` over a dead shim and never mentions the reaper | fences `internal/daemon/reconcile.go:78` |
| M1 | a daemon that rejoined MID-TURN held no turn, so the phone could queue a SECOND concurrent turn and Stop was impossible | `turnIDLocked` adopts a turn from any frame naming a native turn id it has not already closed; a `closedTurns` guard keeps closure the daemon's own decision | `internal/skeleton/interaction.go:344` |
| M2 | the terminal-wins approval race writes a fact about the owner the daemon cannot know | DISCLOSED, not fixed (the protocol gives no mechanism) -- ADR-013 §R7.10 item 6 and the CANNOT section above | `docs/adr/ADR-013-mirror-capture-architecture.md:1053` |
| M3 | the retry rule was fenced by a whole-file grep for `"no rollout found"`, satisfiable by `isMissingRollout`'s own declaration | `subscribeThread` takes the `backendConn` interface so its rule can be driven by BEHAVIOUR; the grep is scoped to the function body and asserts the CALL | `internal/skeleton/backendconnect.go:290`, `internal/skeleton/r7fix_test.go:375` (round 4 note: `subscribeThread` no longer exists -- it split into `resumeThreadOnce` and `subscribeSessionThread`; the scoped grep moved with it and the behavioural fences grew, see `README-round4.md`) |
| M4 | `endSession` dropped the registration but never closed the connection | `backendConn` requires `Close`, and `forgetBackend` closes -- covering both callers | `internal/skeleton/backend.go:86`, `:215` |
| LOW | two evidence claims did not survive a diff against the code; `mirror-program.md` M4.2 had no pointer to the recorded deviation | corrected below; the spec row now names ADR-013 §R7.4 | `docs/specifications/mirror-program.md:177` |

## The assembled topology test, and why it is the headline

`internal/skeleton/r7r3_topology_test.go:119`
`TestR7R3_TheAgentIsLaunchedAsACLIENTOfTheAppServerTheShimSpawned`

Every link is production code:

1. the REAL core plans the backend from the session's own adapter -- `planSessionBackend` ->
   `adapter.ResolveBackend` (obligations 9a/9c) -> the codex adapter's recorded argv -- resolving
   the program by the core's OWN `PATH` search, so nothing in the test tells the daemon where the
   backend is;
2. the REAL shim binary spawns the backend in its own process group and blocks for the go-ahead;
3. the REAL `internal/appserver` client performs the RECORDED WebSocket upgrade over the REAL
   UDS and completes `initialize`/`initialized`;
4. the REAL `shimwire` control socket carries `backend_attach`;
5. the REAL shim appends the arguments and execs the agent;
6. the agent attaches, and only THEN does the server announce `thread/started`, which the REAL
   pump adopts and the REAL `thread/resume` joins.

The only doubles are the two ends that would otherwise cost the owner money (hard rule 10):
`cmd/swarm-fake-codex` is one binary in the two modes the R1 gate recorded, and it is resolved
because the built file is literally named `codex`.

Its three assertions are three independent witnesses: `backend.json` names a LIVE pid and the
socket inside the session dir; the AGENT's own argv, as it printed it on its PTY into the
session transcript, carries `--remote unix://<that socket>`; and the daemon ends up holding the
thread id the server announced -- which the server announces only when the agent attaches.

## Mutation verdicts -- EIGHT of eight FIRE

| # | Mutation applied to production code | Test that must fail | Verdict |
|---|---|---|---|
| MUT-1 | `SendBackendAttach(id, ch.AgentArgs)` -> `SendBackendAttach(id, nil)` (the review's own probe) | `TestR7R3_TheAgentIsLaunchedAsACLIENTOfTheAppServerTheShimSpawned` | **FIRES** -- and the log shows the structural failure verbatim: `session ... never announced a thread within 20s` |
| MUT-2 | delete `d.reapOrphanBackend(id)` from `handleShimExit` | `TestR7R3Reaper_AShimKILLEDWhileTheDaemonIsUPHasItsBackendREAPED` | **FIRES** |
| MUT-3 | delete `d.reapOrphanBackend(m.ID)` from `reconcileRunning` | `TestR7R3Reaper_ARESTARTOverADeadShimREAPSTheOrphanThroughRECONCILE` | **FIRES** -- and both round-2 reaper tests still PASS in the same run, which is exactly why they were not fences |
| MUT-4 | remove the mid-turn adoption arm from `turnIDLocked` | `TestR7R3_ADaemonThatJoinsMIDTURNHoldsTheRunningTurnRatherThanReadingAsIdle` | **FIRES** |
| MUT-5 | remove the `closedTurns` guard from the adoption arm | `TestR7R3_AFrameOfAnALREADYCLOSEDTurnDoesNotReopenIt` | **FIRES** |
| MUT-6 | drop the `Close` from `forgetBackend` | `TestR7R3_EndingASessionCLOSESItsBackendConnection` | **FIRES** |
| MUT-7 | delete `if !isMissingRollout(err) { return late, err }` from `subscribeThread` (round 4: the equivalent mutation is M4 in `README-round4.md`) | `TestR7R3_ATransportFaultOnTheThreadJoinFAILSFASTRatherThanRetryingTheWholeWindow` **and** the re-scoped `TestR7Fix_TheThreadJoinRetriesOnlyTheRecordedRolloutRace...` | **FIRES** (both; under the round-2 whole-file grep the second would NOT have) |
| MUT-8 | `killGroups` never signals the backend group (`backend > 0` -> `backend < 0`) | `TestR7ShimBackend_ATermIgnoringBackendIsDEADAfterRunReturns` | **FIRES** (with eight siblings) -- see the LOW correction below |

MUT-4 and MUT-5 were re-run against the FINAL form of `turnIDLocked` after a lint-driven
refactor (`QF1001`), so the recorded verdicts are for the code that shipped, not for an
intermediate.

## Two flaky tests were fixed, and one of them was executing the REAL codex binary

The gate run caught two failures that round 2's gate did not, both pre-existing and both
independently reproduced with this round's changes REVERTED (2 of 3 runs failed without them).
They are recorded here rather than quietly repaired, because one of them is a hard-rule breach.

1. **`internal/daemon/r7_backendlaunch_test.go`'s `r7BackendSpec` named
   `/usr/local/bin/codex`** -- so four CI-facing tests that launch REAL shims exec'd the REAL
   installed CLI against the owner's real account. Hard rule 7: CI-facing tests are
   fixture-driven. It was also the flake's cause, and the flake was self-inflicted: `r7Squatter`
   BINDS the very socket the shim told its backend to serve, so the shim's readiness poll (which
   can only ask the socket -- `codex app-server` writes nothing to either stream) saw the
   squatter's listener, declared the backend servable, and wrote `backend.json` naming the
   process it had spawned, racing the test's own write. PROBED: file pid 38518, squatter pid
   38519. The program is now a path that cannot start, so nothing is written and the test's own
   file is the only one there is. No assertion changed.
2. **`internal/appserver/r7_wsupgrade_test.go:180`** compared the server's upgrade counter
   immediately after `Dial` returned, but the counter is incremented AFTER `websocket.Accept`
   has already written the 101 -- a race against the server's own goroutine. It now waits for
   the count to become non-zero with a bound. Exactly as strong: a client that skips the upgrade
   leaves it at 0 forever, which is the failure the test exists for.

## Disclosed, not fixed: the terminal-wins approval race (review MEDIUM 2)

`ServerRequestResolvedNotification` carries `{threadId, requestId}` and **no decision and no
answerer**. The daemon marks a phone answer applied before its RPC leaves, and attributes any
subsequent resolution to the phone while that mark is set. If the owner answers AT THE TERMINAL
inside that window: the journal records the phone's decision, by the phone, with the phone's
`operation_id`, for an answer the server took from the keyboard -- and because the same
broadcast retires the request id, the phone's own RPC then finds nothing and the phone is
returned an error. **History says the phone answered it; the phone was told it failed.**
Narrowing the window (marking applied only after the reply) shrinks it but cannot close it, and
no field exists that would let the daemon tell the two cases apart. Written into ADR-013 §R7.10
as consequence 6.

## Corrections to earlier evidence (review LOW)

1. **Round 2's WIRED list was stated by reference** ("everything round 1's README lists as
   wired, MINUS the one line corrected"), and round 1's list says `joinSessionBackend` "runs
   initialize/initialized/thread/start" -- which round 2's own BLOCKING-3 fix REMOVED. The
   accurate statement, as of this round: `joinSessionBackend` dials, runs
   `initialize`/`initialized`, sends the go-ahead, WAITS for the agent's `thread/started`, and
   joins that thread with `thread/resume`. **It starts no thread of its own, and
   `TestR7Fix_NoProductionPathEverCallsThreadStart` fences that.** Nothing in this round's
   evidence is stated by reference to an earlier round's list.
2. **Round 1's mutation table named
   `TestR7ShimBackend_ATermIgnoringBackendIsDEADAfterRunReturns` as the fence for backend
   containment, and the round-2 reviewer reported it did NOT fail under their `killGroups`
   mutation.** Re-measured this round under the mutation form recorded above as MUT-8
   (`backend > 0` -> `backend < 0`, which keeps the function compiling -- deleting the block
   outright does not, since `backend` becomes unused): the named test DOES fail, at 30.67 s,
   along with eight siblings. The row is therefore correct under this mutation form; the
   reviewer's observation stands for whatever form they applied, and the property is fenced
   either way by nine tests. The most DIRECT of them remains the named one.
3. **`mirror-program.md` M4.2** said "batched 200 ms at the adapter" with no pointer to where
   the deviation is recorded. It now names ADR-013 §R7.4 and states that the fold ships at the
   PRODUCER EDGE, with the reason. The decision-change rule was already satisfied; the reader of
   the spec was not.

## Wired vs parked, honestly

**WIRED (production-reachable this round, unchanged from round 2 except where named):**
`planSessionBackend` (core `BackendPlanner`), the shim-owned backend with its own process group
and the go-ahead handshake, `backend_attach` over `shimwire`, `appserver.Dial` + the RECORDED
upgrade, the pump (200 ms fold, thread adoption, typed events via `Engine.ApplyTypedEvent`),
`turn/start` / `turn/steer` / `turn/interrupt`, native approvals by RPC, the three §R7.7
lifecycle cases, `connectBackendsForRunning` on restart, `DefaultItemWindow`, **and now: the
orphan reaper on BOTH shim-death paths, connection close on session end, and mid-turn turn
adoption.**

**PARKED (named, with the gate each is behind):**

- **The exit criterion itself.** One real Codex thread driven from the terminal and the phone at
  once. Behind `realcli`; not run this round.
- **The two ADR-013 measurement obligations** (rollout-to-resume; `thread/read{includeTurns}`
  losslessness, Q4). Written, not taken. Q4 is what decides whether the mid-turn adoption arm is
  load-bearing or belt-and-braces.
- **`registerSessionCapabilities` still has no production caller**, so `structured_chat` is not
  derived per session instance from a capability record; the composer's availability is read off
  the transcript. Unchanged by this round and still the capability slice's obligation.
- **No Android work**, and no Codex-shaped Android test.
- **`turn/steer`'s reply `itemId`** remains NOT RECORDED; the composer echo rides
  `clientUserMessageId` and does not depend on it.

## Gate

`r7-green-round3.txt` carries the full output with timestamps and exit codes: `go build ./...`,
`go vet ./...`, `golangci-lint run` (v2.12.2), `go test -race -count=1` over the owned packages
plus `internal/verify` (the B94 reachability ledger) and the GG-7 `protocol.md` bidi test.

**B94 delta: zero rows.** No exported symbol was added without a production caller and no ledger
row became reachable; `internal/verify` is green with these changes in the tree.
**GG-7 delta: none.** No wire field, op or struct changed, so `protocol.md` is untouched.
