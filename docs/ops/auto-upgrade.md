# Auto-upgrade runbook — the nightly timer on the owner's machine

A `launchd` user-agent timer (`com.swarm.upgrade`) runs `brew update` and then
`brew upgrade --cask swarm` every night at 04:00 (the explicit update matters: brew's own
auto-update only fires when the last one is more than 24 hours old, which a daily timer can
narrowly miss), then always runs `swarm daemon restart --unattended` regardless of whether brew
found anything new, so a restart deferred one night still happens the next. `--unattended` converges the
running daemon and gateway onto whatever binary is now installed, spawning the replacement from
the environment the daemon saved the last time it started from a terminal — or it touches nothing
at all if the processes already match or a session makes it unsafe to disturb them. Google Play
already updates the phone on its own; this timer is the machine-side half of keeping pace with it.

See [ADR-020](../adr/ADR-020-unattended-daemon-restart.md) for why the restart works this way, and
[docs/specifications/auto-upgrade-plan.md](../specifications/auto-upgrade-plan.md) for the full
design.

## Precondition

**Install the timer only once `swarm version` on this machine reports 0.13.2 or later.** A 0.13.0 or 0.13.1
`swarm daemon restart` ignores every argument (`cmd/swarm/main.go:490-492`) and always runs the
full stop-and-spawn from the CALLER's environment — so a timer installed against a 0.13.0 or 0.13.1 binary
would restart the daemon from launchd's bare environment (no PATH beyond the system default, no
provider keys) every single night, not from a saved one. Do the one-time hop below first.

**Nothing but this timer runs `swarm` from launchd or cron.** The daemon rewrites `daemon.env`
from its own environment at every start, and any `swarm` client command auto-starts a daemon when
none is running (D-1). A cron'd `swarm ls` on a morning the daemon happened to be down would start
one from cron's environment and overwrite a good `daemon.env` with a three-line one (system PATH,
HOME, SHELL, no provider keys); every later nightly converge would then spawn from that file and
log `converged`. If that ever happens, `swarm daemon restart` from a terminal rewrites the file.

## The one-time hop to 0.13.2

Run these from a terminal, in order, exactly once:

```bash
brew upgrade --cask swarm
swarm daemon restart
launchctl kickstart -k gui/$(id -u)/com.swarm.remote
swarm version
```

The `swarm daemon restart` here is what saves `daemon.env` for the first time — every unattended
restart after this one spawns from what it captures. Confirm `swarm version` reports 0.13.2 (or
later) before continuing.

## Installing the timer

```bash
sed -e "s|@HOME@|$HOME|g" -e "s|@PREFIX@|$(brew --prefix)|g" packaging/launchd/com.swarm.upgrade.plist \
  > ~/Library/LaunchAgents/com.swarm.upgrade.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.swarm.upgrade.plist
```

Dry proof, run once right after installing:

```bash
launchctl kickstart gui/$(id -u)/com.swarm.upgrade
```

Then read `~/.local/state/swarm/upgrade.log`. Because nothing changed since the hop above, it
should show brew reporting `swarm` already up to date and `--unattended` exiting 0 at rule 1
(already converged) — the idempotent no-op path, proof the plumbing works before the timer is
trusted to run unattended overnight.

## Reading the log

Both brew's output and `--unattended`'s own messages land in the same
`~/.local/state/swarm/upgrade.log`. What the exit status of
`swarm daemon restart --unattended` means, most recent run at the bottom of the file:

- **0 — converged, or nothing to do.** Either no daemon was running, the daemon already matched
  the installed binary, or the restart (daemon and, if installed, gateway) completed. No action
  needed.
- **1 — failed.** A spawn failed, the replacement never became reachable, the daemon's lock was
  held with no pidfile to signal, or the daemon restarted but the gateway restart then failed.
  Check the log line for which; the daemon may be down until the owner's next `swarm` command or
  the next night's timer. Investigate before the next quiet hour.
- **2 — deferred.** Either a session was working or waiting on the user (the log names it), or the
  daemon was wedged (holding its lock but not answering a dial). Nothing was touched; it retries
  the next night. A session stuck in this state (an unresponsive turn) defers every night until it
  is ended.
- **3 — refused, no usable saved environment.** No `daemon.env` has ever been written, or the file
  exists but is empty (the log line says which), so there is nothing safe to spawn from. Run `swarm daemon restart` from a terminal once — that both fixes this and
  captures a fresh `daemon.env` — then the timer converges normally from then on.

**A 04:00 run missed while the machine was asleep runs at the next wake instead** (`launchd`
coalesces a missed `StartCalendarInterval`), which on a laptop closed overnight is often when the
owner opens the lid and starts working — the exit codes above are what make that safe.

## Pausing or removing the timer

```bash
launchctl bootout gui/$(id -u)/com.swarm.upgrade
rm ~/Library/LaunchAgents/com.swarm.upgrade.plist
```

The `bootout` alone stops it from running again; delete the plist too for a clean removal.

## Draining before a shimwire version bump

If a release bumps `internal/shimwire.Version`, do NOT let the timer carry it unattended: end
every running session, `launchctl bootout gui/$(id -u)/com.swarm.upgrade`, upgrade by hand (the
one-time hop above), restart, then reinstall the timer (`launchctl bootstrap`).

Why: `internal/daemon/shimclient.go:59` rejects a reconnecting shim whose wire version does not
match the daemon's, so every session running under the old shim would drop off the board the
moment the new daemon comes up — the agents keep running, but swarm can no longer see or attach to
them. Ending sessions first means there is nothing left to drop.

## Recovery: brew reports "already a Binary at /usr/local/bin/swarm"

This means a previous version was hand-copied into place outside brew's own bookkeeping, and
`brew upgrade` will not touch it. Move both hand-copied binaries aside, then install fresh:

```bash
mv /usr/local/bin/swarm /usr/local/bin/swarm.bak
mv /usr/local/bin/swarm-remote /usr/local/bin/swarm-remote.bak
brew install --cask swarm
```

`brew install`, not `brew upgrade` — there is no prior cask record for brew to upgrade from. Then
run the one-time hop above.

## What is actually running

```bash
swarm version
lsof -p $(pgrep -f 'swarm daemon') | grep txt
lsof -p $(pgrep -f 'swarm-remote') | grep txt
launchctl print gui/$(id -u)/com.swarm.remote
```

`swarm version` reports the CLI's own build. The two `lsof` lines show which binary's inode the
running daemon and gateway processes actually have open (their `txt` mapping) — the fact that
matters after an upgrade, since a process keeps running its old inode until it is restarted even
after `brew` relinks the path. `launchctl print` shows the gateway unit's last exit status and
whether it is currently loaded.
