# S4b evidence — the remote-socket contract (PB-LIFE-7)

**Commit**: `d971525`. **Requirement**: PB-LIFE-7 (one, the whole slice). **Decision**: ADR-007 B15.

## Why this slice exists at all

It was created mid-implementation. An S4 re-reviewer restated an item S4 had recorded as an
*accepted residual*, and on verification it was exit-criterion-fatal on the flagship install path,
not a footnote. The requirement, the slice, and its place on the S19 exit path were added then
(141 requirements, 28 slices). This is the process working: the residual had already been written
down and signed off once.

## The defect

The gateway's supervision unit dialed ADR-007 D4's canonical `<stateDir>/remote.sock`
(`cmd/swarm/remote.go:90,262,318-321`), while the daemon opened a remote socket **only** when
`SWARM_DAEMON_REMOTE_SOCK` was set in its own environment — `RemoteSocketPath:
os.Getenv(daemon.EnvRemoteSocket)` with no default (`cmd/swarm/main.go:309`), documented "empty =>
remote control off". Two independent defaults that had to agree, and did not.

On a stock install: the daemon served nothing there, `swarm remote init` installed a unit aimed at
nothing, the gateway exited failure, and the supervisor respawned it every `ThrottleInterval`
**forever**. User-visible symptom: **"the phone pairs, then silence"** — the exit criterion
delivering its first step and nothing after it.

## Failing-first (GG-5)

```
TestDefaultInstall_GatewayIsServedOrTheOperatorIsTold/stock_install:_nothing_opted_in
  PB-LIFE-7: `swarm remote init` started the gateway against "/tmp/swcli.../remote.sock",
  but nothing is listening there (dial unix ...: connect: no such file or directory).
  The daemon assembled from this same environment listens on "".

TestDefaultInstall_NoThrottledRestartLoop
  restart 1/3 ... restart 2/3 ... restart 3/3 -- 3 identical restarts, and nothing between
  them changes.

TestRemoteSocket_OneDefinition/stock_install_(nothing_opted_in)
  the daemon listens on "" and the installed unit dials "/tmp/.../remote.sock".

TestHostStop_ClassifiesNothingByMessage (launchd, systemd)
  Stop() = ...Boot-out failed: 5: Input/output error for output "Boot-out failed: 5:
  Input/output error" but <nil> for "Boot-out failed: 3: No such process"; the exit status is
  the same in both. The outcome is being decided by the message.
```

Nothing is modelled where it could be real: the daemon's listen path comes from
`skeletonConfigFromEnv` (the function the `swarm daemon` role itself calls), "served" is a real
`net.Dial` against a real in-process `skeleton.Serve`, and the restart loop is walked with the
units' own `supervise.ExitCodeFor` + `ShouldRestart`.

## What shipped

1. **One definition.** `gatewaySocket` gained a provisioning gate; `skeletonConfigFromEnv` calls
   it. Both sides derive the path from one function. The RED author measured both permitted
   resolutions and recommended this one; option (b) — refuse loudly at `init` — was rejected
   because its detector is **unsound in scope**: `swarm remote init` cannot read the daemon's
   environment, and a dial failure cannot distinguish "remote is off" from "the daemon isn't
   running", a state `init` must tolerate by design.
2. **The upgrade ordering.** Review found the fix still produced the spin loop when the *running*
   daemon predates provisioning — reachable on a **binary upgrade**, where `init` is the very
   command an upgrading owner is told to run. `init`/`pair` now probe the running daemon and name
   `swarm daemon restart`. Gating `Ensure` on that probe was measured and rejected: it breaks the
   same tests that got option (b) rejected. So it warns and proceeds — the requirement asks that
   the operator be *told precisely what to do*, which this does; it does not prevent the interim
   restarts.
3. **Stale-socket reclaim, confined to sockets.** A crash-left socket would otherwise have stopped
   the daemon starting **at all** — worse than the bug. Unlink-if-stale matches the idiom
   `daemon.bindSocket` already uses, and the flock argument was verified to hold. Review then
   showed unconditional removal reached *past* that guarantee on an override path: a regular file
   was destroyed, and two daemons with different state dirs sharing one override path no longer
   contended — the second **silently stole the first's live socket**. Now gated on
   `os.Lstat` reporting a socket.
4. **0600 on the remote socket** (D4 specifies it; `net.Listen` was inheriting umask). Previously
   reachable only for opted-in operators; under B15 it is every provisioned machine.
5. **`Stop`'s prose classifier deleted.** It sniffed "no such"/"not find"/"not loaded"/"does not
   exist" — the defect class purged from `Ensure` in S4's own remediation — with zero coverage on
   its decision logic. Now a presence query decided on **exit status**.

## Accepted residuals

- **Later drift is unwarned.** The liveness probe runs only in `ensureGatewayRunning`, reached from
  `init`/`pair`. A machine that drifts afterwards — daemon restarted by something else, or an owner
  exporting `SWARM_DAEMON_REMOTE_SOCK` in a new shell after pairing — gets no warning until the
  next `init`/`pair`. `swarm remote status` is the natural home for a standing check; not
  authorised in this slice.
- **The ADR's own filed residual stands**: the CLI reads `SWARM_DAEMON_REMOTE_SOCK` from *its* env
  while the daemon reads its own, and `internal/daemon/client.go:174` spawns with
  `append(os.Environ(), …)`, so the daemon inherits it from whichever shell first auto-started it.
  Closing it needs the daemon to be the authority. Item 2 above reduces it to a warning rather
  than silence.
- **`gatewaySocket` treats any stat error as unprovisioned.** Probed benign on all three edges
  (empty identity, identity-is-a-directory, unreadable dir): the fail-closed loader reports each
  loudly, so a live daemon cannot end up silently without its remote tier.
- **`TestSkeletonConfigFromEnv_RemoteSocketEmptyByDefault` depends on ambient environment** (it
  never unsets the override). Pre-existing, and it FAILS loudly under a set override rather than
  passing vacuously — a self-announcing hazard, not a vacuous pass.
- **systemd `is-active` edges**: a `disable --now` that stops but fails to *disable*, and an
  "activating" unit, both read as "gone". Backstopped by the gateway exiting `ExitQuiescent` when
  it finds zero devices.
- **Pre-existing lint**: `internal/skeleton/sessiontap_test.go:80` (unused `inputCount`), verified
  pre-existing by linting a clean `git archive` of HEAD.

## Notes worth keeping

- **The suite could not catch a wholesale polarity inversion.** Inverting both arms of `Stop`'s
  decider left all three original tests green — they are direction-agnostic *by design*, so the
  implementation stays free to choose its probe command. `TestHostStop_DeciderPolarity` now carries
  the sense. The four tests are a **matched pair**: deleting either half reopens the hole.
- **`TestRemoteSocket_OneDefinition` structurally cannot see the upgrade ordering** — it compares
  two config *computations* taken at the same instant, not the running daemon, and reported
  agreement while the live daemon served nothing. Only the real dial catches it.
- **The ADR-007 D5 source-scan guard constrains comments too.** Naming `cmd/swarm-remote` in a
  comment in `internal/daemon` tripped `TestDaemonNeverSpawnsTheGateway`, because that file already
  calls `exec.Command`. The comment was reworded rather than the test touched.

## Gates

`go build ./...`, `go vet`, and `go test -count=1` green on `cmd/swarm`, `internal/remote/supervise`,
`internal/skeleton`, `internal/protocol`, `internal/daemon`; `-race` green on `supervise`;
`gofmt -l` clean; `golangci-lint` clean on the required packages. Re-verified independently before
commit.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** to emit the traceability
table's DERIVATION column: the verdict token `DERIVED` or `NOT DERIVED`, and -- for `DERIVED` --
the mutation that was made to fail, in the same row. `DERIVED` means somebody made the fence fail
on purpose and restored it; reading a fence is not deriving it.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-LIFE-7 | DERIVED | the pre-B15 second default restored at the daemon's end only -- `skeletonConfigFromEnv`'s `RemoteSocketPath: gatewaySocket(stateDir)` reverted to `os.Getenv(daemon.EnvRemoteSocket)` (`cmd/swarm/main.go`), leaving the unit still dialing D4's canonical path. Five tests fail and each reports the real symptom: `TestDefaultInstall_GatewayIsServedOrTheOperatorIsTold` (*"started the gateway against .../remote.sock, but nothing is listening there ... The daemon assembled from this same environment listens on \"\""*), `TestDefaultInstall_NoThrottledRestartLoop` (three identical restarts with nothing changing between them), `TestRemoteSocket_OneDefinition`, `TestRemoteInitThenPair_DoesNotLeaveTheGatewayDialingNothing` (*"The phone is paired and will be served nothing"*), and `TestRemoteInit_TellsTheOperatorWhenTheRunningDaemonServesNoRemoteSocket`, whose control subtest catches the opposite error -- a warning that fires on a correctly-served machine. The fence drives a real `net.Dial` against a real `skeleton.Serve` and walks the restart loop with the units' own `ExitCodeFor`/`ShouldRestart`, so it is measuring the install rather than a config computation |
