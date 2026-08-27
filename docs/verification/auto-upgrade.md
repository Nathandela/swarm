# Auto-upgrade: rollout evidence

Plan: [docs/specifications/auto-upgrade-plan.md](../specifications/auto-upgrade-plan.md) section 5. Decision: [ADR-020](../adr/ADR-020-unattended-daemon-restart.md). Runbook: [docs/ops/auto-upgrade.md](../ops/auto-upgrade.md). Bead: agents-tracker-njan.

The failing-first and negative-control evidence for the CODE is in `docs/verification/auto-upgrade/` (one file per implementation lane, A to E). This file is the ROLLOUT artifact section 5 asks for: what happened on the owner's machine, quoted from the machine.

## Step 1: landed (2026-08-27)

Merged to main fast-forward at `f32652a3` after CI 33119379027 was green on that exact sha (13 of 13 jobs: android gate, lint, linux `go test ./...`, darwin tests, goreleaser dry run). Two earlier CI runs on the branch had one real failure each, both mine: the linked-path assertion was unscoped and failed on linux, where `os.Executable()` is kernel-resolved (fixed in `308c03a9`); and the `internal/converge` import of `supervise` tripped `TestDaemonNeverSpawnsTheGateway` (fixed in `b402635`). Every other red in those runs was a deadline under load averages of 20 to 60 and passed alone or on the rerun; the 2 s cancel class is filed as a flake bead.

## Step 2: released as v0.13.2 (2026-08-27 22:07Z)

`v0.13.1` had been tagged and released from main at `dedf1eab` (the options window) while this work was on its branch, so this is 0.13.2, tag on `ade4628b`. Release run 33120365499: gates reused from ci.yml, all green, `publish (goreleaser release)` green; release has 8 assets; the tap's cask reads `version "0.13.2"`. Published by CI with the tap token, not by a local goreleaser run.

## Step 3: the hop and the timer on the owner's machine (2026-08-28, 00:0x local)

`brew upgrade --cask swarm` first reported "the latest version is already installed" at 0.13.0: brew's auto-update had not run because the last one was under 24 hours old (`HOMEBREW_AUTO_UPDATE_SECS`). That is the defect commit `d80bffce` fixes by putting `brew update` at the head of the timer's chain; the plan and runbook no longer claim brew updates on its own. After an explicit `brew update`:

```
nathandela/swarm/swarm 0.13.0 -> 0.13.2
swarm 0.13.2 (go1.25.0)
```

The hop, run from a terminal with the six `SWARM_*` session variables unset:

```
swarm daemon restart            -> exit 0
launchctl kickstart -k gui/501/com.swarm.remote   -> exit 0
21213 /usr/local/bin/swarm daemon      -> /usr/local/Caskroom/swarm/0.13.2/swarm
21225 /usr/local/bin/swarm-remote      -> /usr/local/Caskroom/swarm/0.13.2/swarm-remote
daemon.env: 7 lines, mode 600; keys: SHELL TERM PATH CONDA_PREFIX LANG HOME CONDA_DEFAULT_ENV
sessions: 16 (unchanged across the restart)
```

No provider key in `daemon.env`: this machine authenticates Claude Code by subscription, which is the case section 5 names ("credentials means the S-2 allowlist"). The daemon's argv[0] is the linked path.

Timer installed from `packaging/launchd/com.swarm.upgrade.plist` at `d80bffce`, tokens substituted, `plutil -lint` OK, `launchctl bootstrap gui/501` exit 0.

Dry proof, `launchctl kickstart gui/501/com.swarm.upgrade`, then `~/.local/state/swarm/upgrade.log` verbatim:

```
==> Updating Homebrew...
Already up-to-date.
==> Downloading Homebrew API data
Warning: Not upgrading swarm, the latest version is already installed
converged: the daemon already runs this build (0.13.2), nothing to do
```

Daemon pid 21213 before and after: rule 1 touched nothing. This is the idempotent no-op path the runbook describes as the proof that the plumbing works before the timer is trusted overnight.

## Step 4: first unattended proof (pending, at 0.13.3)

Owed when the next release lands: `upgrade.log` with the run's actual time and exit status; `swarm version`; both processes' `txt` inode from `lsof`, both 0.13.3; `swarm ls` before and after; and a session launched from the phone after the unattended restart whose `meta.json` carries the environment the daemon saved (the `LC_SWARM_PROBE` discriminator, paired with the pidfile's start time). Not claimed here.
