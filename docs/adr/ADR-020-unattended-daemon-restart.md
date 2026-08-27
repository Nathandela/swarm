# ADR-020: Unattended daemon restart spawns from the saved environment, or touches nothing

- Status: Proposed (owner sign-off pending)
- Date: 2026-08-27
- Source: [docs/specifications/auto-upgrade-plan.md](../specifications/auto-upgrade-plan.md) section 2 item 1, section 3 L2
- Amends: D-8's restart (`system-spec.md:69`) as implemented at `internal/daemon/client.go:224` (`Restart`'s doc comment) — the interactive path there is unchanged; this ADR adds only the unattended form.
- Affects: `internal/daemon` (client.go, policyenv.go, daemon.go, singleton.go, launch.go), `internal/persist/env.go`, `internal/persist/persist.go`, `internal/remote/supervise`, `cmd/swarm/main.go`

## Context

Google Play updates the phone on its own. Nothing updates the machine. A phone
newer than the daemon does not error, it waits: `composer_send` expects an
`operation_id` on the echo that an older daemon never stamps, so the bubble
says "sending" forever. The user sees a broken app; the cause is on a machine
they are not sitting at.

2026-08-27 showed the whole gap end to end on the owner's machine: `brew
upgrade --cask swarm` failed with "already a Binary at /usr/local/bin/swarm"
because the previous version had been hand-copied there, and brew rolled back
by purging its own record; after a correct `brew install`, the gateway
(launchd, `KeepAlive`) and the daemon kept running their old inodes and both
needed a restart by hand (`launchctl kickstart -k gui/501/com.swarm.remote`
and `swarm daemon restart`); and the release that should have automated the
Homebrew push instead failed in CI and was published by a local `goreleaser`
run.

### The environment trap

`swarm daemon restart` (`internal/daemon/client.go:224`, `Restart`'s doc
comment: "stop the running daemon ... then spawn a fresh one that reconnects
them") spawns the replacement from the caller's `os.Environ()`
(`internal/daemon/client.go:178`, inside `defaultSpawnDaemon`). That is
correct only because the caller has always been the owner's interactive
shell.

Phone-launched sessions carry `ClientEnv: nil`
(`internal/daemon/launchpreset.go:172`), and `PolicyEnv(nil)` resolves to the
daemon's OWN process environment, allowlist-filtered through the S-2 list
(`internal/daemon/policyenv.go:48`; the allowlist itself is
`internal/persist/env.go:23-35` — PATH, HOME, SHELL, TERM, the locale family,
the venv/conda vars, and the two provider credentials by exact name). A
daemon spawned unattended under a launchd timer inherits launchd's own
`PATH=/usr/bin:/bin:/usr/sbin:/sbin` and no keys, and from then on every
phone-launched agent either fails to find `claude`/`codex` at all or loses
ADR-006's billing-inheritance rule. The TUI's existing auto-restart (bead
5jl, `internal/tui/tui.go:372`) cannot repair this: it fires on a build
version skew, and after an unattended restart the versions already match.

## Decision

D-8's restart gains an unattended form. A restart requested by nobody at a
keyboard (`swarm daemon restart --unattended`) spawns the replacement from
the environment the daemon SAVED when it last started interactively, never
from the caller's; it refuses when nothing is saved; it defers while any
session is working or waiting on the user; and it moves the gateway only
after the daemon — both or neither. `swarm daemon restart` without the flag,
and the TUI's 5jl path, are unchanged: caller environment, which is the
owner's shell.

**The saved file.** At every start the daemon writes `<state>/daemon.env`:
`persist.FilterEnv(os.Environ())`, mode 0600 inside the 0700 state dir — the
same allowlisted set the daemon already writes into every session's
`meta.json` (`internal/persist/persist.go:53`, `:189`). An interactive start
refreshes it; a start from the file rewrites the same content.

**The five rules.** `--unattended` decides in this order, spawning only at
rule 4, and exits 0 only when the running processes match the installed
binary:

0. **No daemon.** The lock is free (`acquireLock`/`releaseLock`,
   `internal/daemon/singleton.go:20-45`; the pidfile is best-effort,
   `internal/daemon/daemon.go:288`). Any error other than `ErrAlreadyRunning`
   is also treated as "no daemon." Exit **0**, nothing done.
1. **Already converged.** `protocol.Dial` (bounded — a `DialTimeout` and a
   hello read deadline of 10s each, `internal/protocol/client.go:50`, `:65`)
   and the hello reply's `BuildVersion` equals this binary's own version.
   Exit **0**, nothing done — this is what makes the nightly call idempotent.
   `errors.Is(err, protocol.ErrIncompatibleVersion)`
   (`internal/protocol/client.go:81-86`) or a different build version means
   not converged; continue. The lock held but the dial failing for any other
   reason (a daemon wedged in `Close`, or caught between lock and listen):
   exit **2**, deferred, the reason logged.
2. **A session is working or waiting on the user.** Read every session's
   persisted `meta.json` status and derive its group
   (`internal/status/status.go:66-77`); `GroupWorking` or `GroupNeedsInput`
   means exit **2**, deferred, naming the session; Completed and
   ReadyForReview hold nothing in flight. This reads disk, not the daemon, so
   it runs against a daemon of any protocol version and can never itself
   spawn. `TurnUnknown` derives to Working (every launch starts there,
   `internal/daemon/launch.go:293`), so a hung session defers every night
   until it is ended.
3. **No `daemon.env` saved, or an empty one.** Exit **3**, the reason on
   stderr, nothing done — the daemon keeps running. Empty is refused for the
   same reason as absent (an environment with no PATH and no HOME is not one
   to spawn from), with a distinct reason line so the log tells the two apart.
4. **Otherwise:** today's stop-and-spawn, with the spawn's environment taken
   from `daemon.env` plus the `SWARM_DAEMON_*` variables, and `DaemonBin`
   this executable exactly as invoked and UNRESOLVED
   (`cmd/swarm/main.go:527`, `internal/daemon/launch.go:562` — the daemon
   already spawns every shim from its own executable path this way);
   confirm the replacement is reachable, as `Restart` does today; THEN
   restart the gateway IN PLACE with a new `Restart()` on the supervisor
   (`launchctl kickstart -k` on launchd, `systemctl restart` on systemd) —
   the daemon and the gateway move together, both or neither, never one
   without the other. `ErrNotInstalled` (no `swarm remote init` ever ran, or
   the gateway is run another way) is benign: logged, exit **0**, on
   `restartGatewayForDelivery`'s precedent (`cmd/swarm/remote.go:1364-1368`).
   Exit **1**, gateway untouched, on a failed spawn, an unreachable
   replacement, or a lock held with no pidfile to signal. Exit **1** after a
   successful daemon restart on any gateway-restart error other than
   `ErrNotInstalled`, so a new daemon paired with a dead gateway is never
   reported as success.

The owner's machine upgrading unattended on a launchd timer, and that same
timer converging the running processes, is operations — recorded in
[docs/ops/auto-upgrade.md](../ops/auto-upgrade.md), not decided here.

## Consequences

### Positive

- Phone-launched sessions keep a real PATH and ADR-006's billing-inheritance
  credentials across an unattended restart, because the spawn draws on the
  daemon's own saved environment rather than launchd's bare one.
- The call is safe to run every night regardless of outcome: idempotent when
  nothing changed (rule 1), and a refusal rather than a guess when the
  daemon has never been started interactively (rule 3) or a session is busy
  (rule 2).
- The daemon and the gateway can never diverge from an unattended run: rule
  4 moves both or neither.

### Negative

- **A restart loses what the daemon holds only in memory**: pending
  approvals (`internal/skeleton/interaction.go:108`), a composer send
  accepted but not yet echoed, and the turn bookkeeping the phone steers by
  (`internal/skeleton/serve.go:154-166`, `:183-192`). Rule 2 refuses while
  any session is working or waiting, but a turn or approval that begins
  between that read and the stop is lost: the phone's answer is refused and
  the user re-answers at the terminal.
- **A hung session (`TurnUnknown`) defers the converge every night** until
  it is ended, because rule 2 cannot distinguish "stuck" from "working."
- **A deferred night is a mixed-version night.** The old daemon spawns every
  new session from the linked path (`cmd/swarm/main.go:527`,
  `internal/daemon/launch.go:562`), so once brew relinks the binary it execs
  the NEW shim while the daemon itself is still old — tolerable only while
  `shimwire.Version` stays pinned at 1 (checked at
  `internal/daemon/shimclient.go:59`) and the launch config's missing-field
  compat holds (`cmd/swarm/main.go:608-612`).
- **The saved environment is only as fresh as the last interactive start.**
  Rotated keys reach phone-launched sessions only after a
  `swarm daemon restart` run from a terminal.
- **The saved environment can be degraded, not just stale.** `daemon.env` is
  rewritten on every start from that daemon's own environment. The timer never
  starts a daemon (rule 0 exits instead), but D-1 does: any other `swarm`
  client command run from a scrubbed context (a cron'd `swarm ls`) auto-starts
  a daemon from launchd's environment and overwrites a good file with a
  three-line one (system PATH, HOME, SHELL; no keys), which rule 3 does not
  catch and every later converge reproduces while logging `converged`. Named,
  not fixed: "poorer environment" and "interactive start" have no definitions
  that survive a scripted `swarm ls` from the owner's own shell. The
  operator rule is in `docs/ops/auto-upgrade.md`: nothing but this timer runs
  `swarm` from launchd or cron.
- **At lid-open the timer and the TUI's 5jl restart can race**: both do a
  stop-and-spawn, the daemon lock serializes them, and one exits 1. If the
  loser is the timer's converge, the gateway stays old against a
  TUI-spawned new daemon until the next night; if the loser is the TUI, it
  banners and the owner retries.

## Alternatives Considered

**A daemon-side re-exec op.** An older daemon cannot answer a new op, so the
fallback would have to spawn from the caller anyway; exec after teardown has
no supervisor left to confirm it; and `protocol.Server.Close` waits on the
per-connection WaitGroup the op handler itself runs in
(`internal/protocol/server.go:446`, `:494`, `:1150`), so the daemon would
have deadlocked on its own caller.

**The cask postflight.** A postflight runs once, on install, so a deferral
(the common case — a session working at lid-open) is never retried; and
Homebrew's cask DSL makes `system_command` must-succeed
(`cask/dsl/base.rb:28-30` in the `homebrew-cask` source, not this
repository), so a refusal there would have purged the install rather than
merely deferring.

**Leaving the daemon to the TUI's next open.** That open never happens on
the path where the phone is the only client — which is exactly the gap this
decision exists to close.

**Baking PATH and keys into the timer's plist.** Goes stale on credential
rotation and duplicates the S-2 allowlist (`internal/persist/env.go:23-35`)
in a file nobody tests.
