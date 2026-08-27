# Auto-upgrade plan: the machine keeps pace with the phone

**Bead**: agents-tracker-njan. **Status**: revision 5 after three audit rounds, 2026-08-27. **Decision record**: [ADR-020](../adr/ADR-020-unattended-daemon-restart.md).

Revision 4 moves the restart out of the cask and into the nightly timer. Revision 3 put it in the cask postflight, and a postflight runs only on the night a new cask is installed: a deferral there (sessions working at lid-open, the common case on a laptop) was permanent. The timer runs every night, and a "versions already match" rule makes the call idempotent. The cask, the goreleaser config and the Ruby are untouched. Revision 5 applies the third round's findings as edits: the timer is installed only once the linked binary understands `--unattended` (a 0.13.0 `daemon restart` ignores its flags), the gateway restarts in place with `kickstart -k` instead of a bootout-bootstrap pair, and a gateway restart failure fails the run. Section 7 keeps the ledger.

## 1. The problem, stated from the failure

Google Play updates the phone on its own. Nothing updates the machine. A phone newer than the daemon does not error, it waits: `composer_send` expects an `operation_id` on the echo that an older daemon never stamps, so the bubble says `sending` forever. The user sees a broken app; the cause is on a machine they are not sitting at.

2026-08-27 showed the whole gap end to end on the owner's machine:

1. `brew upgrade --cask swarm` failed with "already a Binary at /usr/local/bin/swarm" because the previous version had been hand-copied there, and brew rolled back by purging its own record.
2. After a correct `brew install`, the gateway (launchd, `KeepAlive`) and the daemon kept running their old inodes. Both needed a restart by hand: `launchctl kickstart -k gui/501/com.swarm.remote` and `swarm daemon restart`.
3. The v0.13.0 Release workflow (run 33090840783) failed in the android gate on a transient `sum.golang.org` stream error, so the release was published by a local `goreleaser` run that lacked the tap token; the cask was pushed by hand.

What exists today: the TUI restarts a daemon older than itself on open (bead 5jl, `internal/tui/tui.go:372`, and only on skew); `swarm daemon restart` stops the daemon by pidfile and signal and spawns a replacement (`internal/daemon/client.go:224`, `:281`); shims survive either; the hello reply carries the daemon's build version (`internal/protocol/client.go:93-109`); every status change is persisted to the session's `meta.json` (`internal/daemon/daemon.go:638`, `internal/persist/persist.go:55`); the gateway's unit is stamped with the stable linked path and a Stop-then-Ensure restart exists (`cmd/swarm/remote.go:336-356`, `:1375-1376`). The gateway has no version logic and never spawns a daemon; only `swarm` client commands do (`cmd/swarm/main.go:266`).

## 2. Decision to record (ADR-020)

Two things, and only the first is a decision change:

1. **D-8's unattended form.** A restart requested by nobody at a keyboard spawns the replacement from the environment the daemon SAVED when it last started interactively, never from the caller's; it refuses when nothing is saved; it defers while any session is working or waiting on the user; and it moves the gateway only after the daemon, both or neither. Today `Restart` spawns from the caller's `os.Environ()` (`client.go:178`), which is correct only because the caller has always been the owner's shell.
2. The owner's machine upgrades unattended on a launchd timer through the documented install path (Homebrew cask), and the same timer converges the running processes onto the installed binary. That is operations, recorded in `docs/ops/auto-upgrade.md`, not in the ADR.

The relay is out of scope (hand-run until SH6's VPS cutover; its skew is SH4's).

## 3. Two layers

### L1. The timer: fetch, then converge

`packaging/launchd/com.swarm.upgrade.plist` (a template with two tokens, beside the tracked `packaging/homebrew/swarm.rb`; `dist/` is goreleaser's gitignored output):

```
Label                 com.swarm.upgrade
ProgramArguments      ["/bin/sh", "-c", "@PREFIX@/bin/brew upgrade --cask swarm; @PREFIX@/bin/swarm daemon restart --unattended"]
                      (three array elements; a single element would make launchd exec a file literally named "/bin/sh -c ...")
StartCalendarInterval Hour 4, Minute 0
EnvironmentVariables  HOMEBREW_NO_INSTALL_CLEANUP=1   (cleanup exited 1 on an unrelated ghostscript file tonight, masking a successful install)
                      HOMEBREW_NO_ENV_HINTS=1
StandardOutPath       @HOME@/.local/state/swarm/upgrade.log
StandardErrorPath     @HOME@/.local/state/swarm/upgrade.log
```

`;` not `&&`: the converge step runs whether or not brew found anything, so a restart deferred one night happens the next, and a binary upgraded by hand is converged too. Both streams go to the log because brew prints progress on stdout; the job's exit status is `--unattended`'s. Install, one accept-mode command on the owner's machine, documented in `docs/ops/auto-upgrade.md`:

```
sed -e "s|@HOME@|$HOME|g" -e "s|@PREFIX@|$(brew --prefix)|g" packaging/launchd/com.swarm.upgrade.plist \
  > ~/Library/LaunchAgents/com.swarm.upgrade.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.swarm.upgrade.plist
```

**04:00 is a preference, not a guarantee.** launchd runs a `StartCalendarInterval` job missed during sleep at the next wake, coalesced. On a laptop closed overnight the job runs when the lid opens, which is when the owner starts working and when the phone may be mid-approval. The converge step is therefore written to be safe at any hour and to try again tomorrow; the timer only makes the quiet hour likely.

A launchd user agent carries HOME, USER, SHELL and TMPDIR (checked on the running gateway with `ps eww`), so brew's trust file and the daemon's default state dir resolve; what it lacks is the owner's PATH and credentials, which is L2's subject.

Why brew and not a self-updater: brew is the documented install path; `brew upgrade` runs `brew update` first, so the tap's new cask is fetched; the cask is trusted by NAME (`~/.homebrew/trust.json` lists `nathandela/swarm/swarm`), so new releases stay trusted with no prompt. Why the timer and not the cask postflight: a postflight runs once, on install; `brew upgrade` skips a cask that is current (`Library/Homebrew/cask/upgrade.rb:39-70`), so nothing would ever retry a deferral. Why a template and not a unit rendered by `swarm remote init`: `internal/remote/supervise` is platform-symmetric; a brew timer is macOS-only. Why not `brew autoupdate`: it upgrades every formula and cask on the machine.

### L2. `swarm daemon restart --unattended`: converge, or touch nothing

**Why the environment is the whole design.** Phone-launched sessions carry `ClientEnv: nil` (`internal/daemon/launchpreset.go:172`) and `PolicyEnv(nil)` resolves to the daemon's own environment through the S-2 allowlist (`internal/daemon/policyenv.go:48`; `internal/persist/env.go:23-35`: PATH, HOME, SHELL, TERM, locale, venv and conda, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`). The shim process itself gets the daemon's raw environment (`internal/daemon/launch.go:563`). A daemon spawned under a launchd timer inherits launchd's `PATH=/usr/bin:/bin:/usr/sbin:/sbin` and no keys, and from then on every phone-launched agent fails to find `claude` or loses ADR-006's billing inheritance. The TUI would never repair it: 5jl fires on version skew, and the versions would match.

**The mechanism: existing machinery plus one file and one flag.**

- At start the daemon writes `<state>/daemon.env`: `persist.FilterEnv(os.Environ())`, mode 0600 in the 0700 state dir. This is the same allowlisted set the daemon already writes to every session's `meta.json` (`persist.go:53`, `:189`), so it is not a new exposure class. An interactive start refreshes it; a start from the file rewrites the same content.
- `swarm daemon restart --unattended` decides in this order, spawns only at rule 4, and exits 0 only when the processes match the installed binary:
  0. **No daemon**: the lock is free (`acquireLock`, released at once; the pidfile is best-effort, `daemon.go:288`). Any error other than `ErrAlreadyRunning`, including a missing state dir, is also "no daemon". Exit 0, nothing done. The next `swarm` command spawns one from the owner's shell (D-1).
  1. **Already converged**: `protocol.Dial` (bounded: `DialTimeout` and a hello read deadline of 10 s each, `internal/protocol/client.go:50`, `:65`) and the hello reply's `BuildVersion` equals this binary's `version.Version`. Exit 0, nothing done. This is what makes the nightly call idempotent. `errors.Is(err, protocol.ErrIncompatibleVersion)` (a `ProtocolVersion` bump, `:81-86`) or a different build version means not converged; continue. The lock held but the dial failing for any other reason (a daemon wedged in `Close`, or between lock and listen): exit 2, `deferred`, the reason logged.
  2. **A session is working or waiting on the user**: read every session's persisted `meta.json` status and derive its group (`internal/status/status.go:70-84`); `GroupWorking` or `GroupNeedsInput` means exit 2, `deferred`, naming the session; Completed and ReadyForReview hold nothing in flight. This reads disk, not the daemon, so it works against a daemon of any protocol version and can never spawn. `TurnUnknown` derives to Working (every launch starts there, `launch.go:293`, and a turn quiet past the staleness window returns there), so a hung session defers every night until it is ended; the log says which.
  3. **No `daemon.env` saved**: exit 3, the reason on stderr, nothing done; the daemon keeps running.
  4. Otherwise: today's stop-and-spawn, with the spawn's environment taken from `daemon.env` plus the `SWARM_DAEMON_*` variables and `DaemonBin` this executable exactly as invoked (see below); confirm the replacement is reachable as `Restart` does today; THEN restart the gateway IN PLACE with a new `Restart()` on the supervisor (`launchctl kickstart -k` on launchd, `systemctl restart` on systemd), the call the owner made by hand in section 1. Not Stop-then-Ensure: that pair exists for a restamped unit (`remote.go:1682-1685`) and opens a booted-out window in which a failed bootstrap leaves no gateway until morning. `ErrNotInstalled` (a machine where `swarm remote init` never ran, or the gateway is run another way) is benign, logged, exit 0, on `restartGatewayForDelivery`'s precedent (`remote.go:1364-1368`). Exit 1, gateway untouched, on: a failed spawn; an unreachable replacement; a lock held with no pidfile to signal (`stopRunningDaemon` returns silently, `client.go:281-284`, and `waitLockFree` then times out). Exit 1 after a successful daemon restart on: any gateway restart error other than `ErrNotInstalled`, so a new daemon with a dead gateway is never reported as success.
- `swarm daemon restart` without the flag, and the TUI's 5jl path, are unchanged: caller environment, which is the owner's shell.

**The linked path, not the resolved one.** The daemon spawns every shim from its own executable path (`cmd/swarm/main.go:527`, `launch.go:562`), and `os.Executable` does not resolve symlinks on darwin. A daemon started from the Caskroom path would pin every later session launch to a directory the NEXT upgrade purges (`cask/installer.rb:793-805`), so on a deferred night it could launch nothing. The timer invokes `@PREFIX@/bin/swarm`, and `DaemonBin` passes that through unresolved, exactly as an interactive start does today (`main.go:227`, `:233`).

**The 0.13 to 0.14 hop is manual, once, and the timer waits for it.** A 0.13.0 `swarm daemon restart` ignores every argument (`cmd/swarm/main.go:490-492`) and runs the full stop-and-spawn from the caller's environment, so a timer installed while the linked binary is 0.13.0 would restart the owner's daemon under launchd's PATH on its first run and every night after. The timer is therefore installed only once `swarm version` on the machine reports 0.14.0 or later: the owner upgrades to 0.14.0 by hand, restarts both processes from a terminal (the 0.14.0 daemon saves `daemon.env`), then installs the timer. Every hop after that is the timer's. The first fully unattended proof is 0.15.0.

Alternatives considered and rejected: a daemon-side re-exec op (revision 1): an older daemon cannot answer a new op, the fallback spawned from the caller, exec after teardown had no supervisor, and `protocol.Server.Close` waits on the per-connection WaitGroup the op handler runs in (`internal/protocol/server.go:446`, `:494`, `:1150`), so the daemon would have deadlocked on its own caller. The cask postflight (revisions 2 and 3): runs once per install, so a deferral is never retried, and the cask DSL's `system_command` is must-succeed (`cask/dsl/base.rb:28-30`), so a refusal would have purged the install. Leaving the daemon to the TUI's next open: that open never happens on the path where the phone is the only client. Baking PATH and keys into the timer's plist: goes stale on rotation and duplicates the S-2 allowlist in a file nobody tests.

### Phone: nothing to build here

Play auto-updates. The remaining gap is the phone TELLING the user about skew instead of waiting: "your machine runs 0.12.4, this app needs 0.13.0". One acceptance line added to SH4 (agents-tracker-8k5s), not work in this plan.

## 4. Tests first (GG-5), and what each fences

| Test | Fences |
|---|---|
| `internal/remote/supervise/upgrade_unit_test.go`: the RAW template (no substitution) parses as a plist via the existing `launchdExec` walk; label `com.swarm.upgrade`; `ProgramArguments` has exactly three elements, `/bin/sh`, `-c`, and a line containing `@PREFIX@/bin/brew upgrade --cask swarm` then `@PREFIX@/bin/swarm daemon restart --unattended`, joined by `;` not `&&`; both log paths start with `@HOME@`; `HOMEBREW_NO_INSTALL_CLEANUP` present | The template is what the install command substitutes, launchd gets a program not a sentence, and converge runs even when brew found nothing |
| `internal/remote/supervise`: `Restart()` renders `launchctl kickstart -k gui/<uid>/<label>` on launchd and `systemctl --user restart <unit>` on systemd, and reports `ErrNotInstalled` when the unit is absent | The in-place restart, on both platforms |
| `internal/daemon`: a started daemon writes `daemon.env` equal to `FilterEnv(os.Environ())`, mode 0600 | The saved set is exactly the S-2 allowlist |
| `internal/daemon`: `--unattended` with a live idle daemon of another build and a saved env spawns with the SAVED env. The saved file carries `LC_SWARM_PROBE=saved` (allowlisted under `LC_*`), the caller's env carries `LC_SWARM_PROBE=caller`; the fake spawn records what it was given. Negative control: revert the spawn to `os.Environ()` and the test must fail | The invariant L2 exists for, and proof the test discriminates |
| `internal/daemon`: `--unattended` with the lock free, then with no state dir at all: fake spawn never called, exit 0 both times | Rule 0, both arms |
| `internal/daemon`: `--unattended` against a daemon answering the hello with this build's version: no stop, no spawn, exit 0; against a lock held by a process that never listens: exit 2 within the dial bound | Rule 1, idempotence and the wedged arm |
| `internal/daemon`: `--unattended` with a `meta.json` in `GroupWorking`, then one in `GroupNeedsInput`, then one with `TurnUnknown`: no stop, no spawn, exit 2 naming the session; with only Completed and ReadyForReview: proceeds | Rule 2's predicate, all arms, read from disk |
| `internal/daemon`: `--unattended` against a daemon that refuses the hello, with idle sessions on disk: the check spawns nothing (fake spawn count 0 before rule 4), then rule 4 spawns once from the saved env | A protocol bump converges without a dial |
| `internal/daemon`: `--unattended` with no saved env: the daemon keeps running, exit 3 | Rule 3, the 0.13 to 0.14 hop |
| `internal/daemon`: the fake gateway supervisor sees `Restart` only AFTER the replacement is reachable; on a failed spawn it sees nothing and the exit is 1; when `Restart` returns `ErrNotInstalled` the exit is 0; when it returns any other error the exit is 1 | Both or neither, and a dead gateway is never reported as success |
| `cmd/swarm`: the unattended path passes the caller's executable UNRESOLVED as `DaemonBin` | The linked path property |
| `internal/shimwire`: `Version` is pinned at 1; the constant's comment names the drain procedure | A bump becomes a deliberate act (section 6) |

Negative controls per the repo norm: perturb in memory (Go gates over text) or in a scratch worktree; never in place.

## 5. Rollout and evidence

1. Land the template, `daemon.env`, `--unattended`, the supervisor's `Restart()`, the shimwire pin, ADR-020, the README next-free fix, the SH4 acceptance line and `docs/ops/auto-upgrade.md` on main behind green gates. No change to `.goreleaser.yaml` or the cask.
2. Release 0.14.0 by tag push and let `release.yml` publish (it holds `HOMEBREW_TAP_GITHUB_TOKEN`; the local goreleaser run that published 0.13.0 was the wrong path). If the android gate fails on a network transient, rerun the job.
3. On the owner's machine, in this order and no other: `brew upgrade --cask swarm` to 0.14.0; `swarm daemon restart` and `launchctl kickstart -k gui/501/com.swarm.remote` from a terminal (the 0.14.0 daemon saves `daemon.env`); `swarm version` reports 0.14.0; THEN install the timer (accept mode). Dry proof: `launchctl kickstart gui/501/com.swarm.upgrade`; `upgrade.log` shows brew reporting swarm up to date and `--unattended` exiting 0 at rule 1. Installing the timer before the binary is 0.14.0 is the failure this plan exists to prevent (section 3, the hop).
4. First unattended proof at 0.15.0, in `docs/verification/auto-upgrade.md`, every line an artifact a reviewer can open: `upgrade.log` with the run's actual time (not 04:00 if the machine slept) and the exit status; `swarm version`; the gateway's and daemon's `txt` inode from `lsof`, both 0.15.0; `swarm ls` before and after, identical; and for the environment, `LC_SWARM_PROBE` exported in the shell that last started the daemon interactively, then present in the `meta.json` of a session launched from the phone AFTER the unattended restart, the "after" shown by the session's launch time against the new daemon's start time in the pidfile (a session launched before the restart carries the probe too, so the timestamp pair is what discriminates). "Credentials" in that evidence means the S-2 allowlist, not Bedrock or Vertex variables.

## 6. Risks named, not hidden

- **A bad release lands unattended** on the machine that runs the owner's sessions; casks do not pin versions; rollback is manual. The timer makes the quiet hour likely, not certain.
- **Between brew's upgrade and the converge, or on any deferred night, the gateway can move alone**: its unit execs the linked path, so a crash relaunches it on the new binary against the old daemon (KeepAlive, `SuccessfulExit false`). Against a daemon of another protocol version the gateway idles without serving the phone (`internal/remotegw/command_loop.go:648`, `:800-802`): that idle is section 1's forever-`sending` bubble, bounded now by the next night's converge instead of by the owner noticing.
- **A restart loses what the daemon holds only in memory**: pending approvals (`internal/skeleton/interaction.go:108`), a composer send accepted but not yet echoed, and the turn bookkeeping the phone steers by (`internal/skeleton/serve.go:154-166`, `:183-192`). Rule 2 refuses while any session is working or waiting, but a turn or approval that begins between that read and the stop is lost: the phone's answer is refused and the user re-answers at the terminal. Named, not solved.
- **A failed rule-4 spawn leaves the machine with an old gateway and no daemon** until the owner's next `swarm` command (D-1) or the next night's timer. Overnight, that is the phone dark until morning. Exit 1 says so in the log.
- **A deferred night is a mixed-version night.** The old daemon spawns every new session from the linked path (`main.go:527`, `launch.go:562`), so after brew relinks it execs the NEW shim binary while it is itself old; the converge fixes it the next quiet night. Tolerable while `shimwire.Version` is pinned and the launch config's missing-field compat holds (`main.go:608-612`); named because it is a mode nobody tests. Inside `brew upgrade` itself there is a seconds-long window between the old link's removal and the new one's creation in which a launch execs nothing.
- **At lid-open the timer and the TUI's 5jl restart can race**: both stop-and-spawn, the lock serialises them, one exits 1. If the loser is the converge, the gateway stays old against a TUI-spawned new daemon until the next night; if the loser is the TUI, it banners and the owner retries. One night of skew at worst, and the log names it.
- **The gateway restart kills it before it can send lease severance** (`internal/remotegw/leasemanager.go:148-166` emits it only while the gateway lives), so a phone holding an input lease silently needs a fresh `take_control`. Rule 2 makes a held lease unlikely at restart time, not impossible.
- **A `shimwire.Version` bump turns the unattended upgrade into every running session dropped from the board** while the agents keep running (`internal/daemon/shimclient.go:59`). It has been 1 since Epic 4. The pin test makes a bump deliberate; the drain procedure (end sessions, unload the timer, upgrade) is the owner's to run that night.
- **Every gateway restart reseeds the phone's mailbox.** By morning those frames are older than `InboundMaxAge` (10 min, `internal/remotegw/mailbox_in.go:19`) and the phone stalls until fresh traffic: agents-tracker-y86g, P1, already filed. Auto-upgrade makes it fire once per release night; it does not create it.
- **A hung session (TurnUnknown) defers the converge every night** until it is ended. The log names it; the inbox shows it as working.
- **The saved environment is as fresh as the last interactive start.** Rotated keys reach phone-launched sessions only after a `swarm daemon restart` from a terminal.
- **`XDG_STATE_HOME` users**: launchd does not carry it, so the timer's client resolves the default state dir (`internal/persist/persist.go:334`), finds the lock free, and does nothing (rule 0). Not the owner's setup; named for the day it is someone's.
- A cask pushed before the release assets finish uploading fails one night and succeeds the next.
- Out of scope: the relay; Linux (systemd timer without brew); `go install` users (they can run the same `--unattended` from their own timer).

## 7. Audit ledger

**Round 1 (revision 1 to 2)**, three reviewers. Changed the plan: the re-exec op could not serve the first hop and its fallback spawned from the caller (honesty, CONFIRMED); the persisted allowlisted environment is the smaller mechanism and closes the fallback gap (minimality); `dist/` is gitignored (both); 04:00 runs at wake on a laptop (honesty); approvals are memory-only (honesty, `interaction.go:108`); the shim wire version gate (honesty, `shimclient.go:59`); test row 2 contradicted itself (minimality); "N1" is a code comment label, not an invariant (honesty); evidence must be artifacts (honesty).

**Round 2 (revision 2 to 3)**, ops reviewer. Changed the plan: the cask DSL's `system_command` is `run!` and a raise purges the install (CONFIRMED, `dsl/base.rb:28-30`, `installer.rb:361-377`); the unconditional kickstart manufactured the skew (CONFIRMED, `command_loop.go:648`); rule 2's read went through `EnsureDaemon` and would itself spawn (CONFIRMED, `client.go:127-129`); rule 2 ignored `GroupWorking` (CONFIRMED, `status.go:70-74`); the symlink rationale was inverted (CONFIRMED, `executable_darwin.go`; `main.go:527`); lease severance is lost on kill (CONFIRMED); the re-exec op would also have deadlocked (`server.go:446`).

**Round 3 (revision 3 to 4)**, three reviewers. Changed the plan: a postflight deferral is never retried because `brew upgrade` skips a current cask (honesty, CONFIRMED, `upgrade.rb:39-70`), so the converge moved to the timer with an idempotence rule on the hello's `BuildVersion`; busy-ness is read from persisted `meta.json`, so no rule needs a dial and a protocol bump has no special case (honesty, CONFIRMED, `daemon.go:638`); rule 0 tests the lock, not the best-effort pidfile (honesty, CONFIRMED, `daemon.go:288`); `TurnUnknown` derives to Working (honesty, CONFIRMED); a failed spawn was uncovered (ops, CONFIRMED by construction); the leasemanager path was wrong (honesty: `internal/remotegw`, not `internal/skeleton`); the 0.13 to 0.14 paragraph now says "2 or 3" (honesty); the evidence pairs the probe with timestamps (honesty); the shimwire pin no longer couples to a doc path (minimality); `HOMEBREW_PREFIX`, `.success?` and the Ruby were verified (ops) and then made moot by the move to the timer.

**Round 4 (revision 4 to 5)**, ops and honesty against revision 4. Changed the plan: a timer installed under a 0.13.0 binary would restart the daemon from launchd's environment every night, because that binary's `daemon restart` ignores its flags (honesty, CONFIRMED, `main.go:490-492`), so the rollout installs the timer only after the manual hop; the gateway restarts in place with `kickstart -k` rather than a bootout-bootstrap pair meant for restamped units (honesty, PLAUSIBLE, `remote.go:1682-1685`, adopted because it also removes the booted-out window); a gateway restart failure after a successful daemon restart now fails the run (ops, CONFIRMED, `supervisor.go:106`); `ErrNotInstalled` is benign (ops and honesty); `ProgramArguments` is three elements (ops); rule 0 treats any non-`ErrAlreadyRunning` error as "no daemon" (ops); rule 1 names `protocol.ErrIncompatibleVersion` (ops); a lock held with no pidfile is a named exit-1 cause (honesty); the mixed-version deferred night and the relink window (ops); the lid-open race with 5jl (honesty). Verified and kept: brew under `/bin/sh -c` from a gui agent with the default PATH (ops); the timer job may bootout and bootstrap another gui job of the same uid (ops, `supervisor.go:173`); `protocol.Dial` is bounded and cannot spawn (both); disk is never staler than the daemon's roster because memory updates only after a successful `Save` (honesty, `daemon.go:640-641`); the lock is a kernel flock released before the socket in `Close` (honesty, `singleton.go:20-45`).

**Not adopted, with reasons.** Minimality's "a refused dial proceeds without the busy check": rule 2 now reads disk, so the check runs on a protocol night too, which is what that finding wanted and ops' objection (a live terminal turn dropped) also wanted. Ops' "retry a failed spawn once": a spawn failure does not fix itself; the timer retries tomorrow and D-1 covers the day. Honesty's "have the daemon write its pid into the lock file": a change to the singleton for a rare case; named as an exit-1 cause instead, revisit if the log ever shows it.
