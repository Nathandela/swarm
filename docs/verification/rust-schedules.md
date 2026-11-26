# Rust scheduling evidence

The historical storage fixture uses Go v0.29.22 at `f26648d69086e329132ac7849954d2f29a895387`. The Rust catalog retains the Go `schedules/state.db` layout, Bolt buckets and indexes, occurrence IDs, JSON records, request hashing and cross-process lock. The pinned Rust Bolt engine uses compatibility mode. The fixture contains a job, revision, template, manual occurrence, bound attempt, completion receipt and release/projection timestamps. Rust reads the original bytes, preserves the receipt and attempt, appends a calendar claim, and reopens the catalog. A separate Go reader reopened a Rust-edited copy. [Current reference](../specifications/rust-migration/reference-checks.md)

`scripts/verification/schedule_catalog.py` passed against both the historical Go binary and the Rust binary in separate temporary state roots. The Rust tests exercise DST gaps and folds, Lord Howe's half-hour shift, cron day combination, a century leap gap, clock rewind, host timezone travel, competing claims, revision fencing, stale manual retries, draft/paused state, private files and timer rollback. The worktree and execution fixture retains original changes on verified quota fallback, creates separate overlap worktrees, reconciles a persisted launch intent, freezes uncertain prompt delivery, rejects stale credentials and separately verifies the final message and projection after restart. With 1,200 terminal occurrences the active index stays empty. Twenty combined idle claim and active-index reads took 3.2 ms on the development macOS arm64 host.

The cold timer fixture installs only test definitions through an isolated manager shim, disables external networking, and runs the generated argument vector and daemon paths with the fixture provider missing from the timer's PATH. A Rust-owned supervisor restores the private saved launch environment. The fake Codex uses the managed App Server WebSocket protocol and a fresh thread without a seeded rollout. It exercises an exact native opening turn, separate receipt socket, MCP bridge, a pending first receipt, matching final assistant history, automation tag and verified workspace release. Authority is applied to both the dedicated App Server configuration and its exact native turn. The native turn fields were checked against a schema generated from an installed Codex using a disposable home.

The macOS arm reproduced EPERM from an exact worker group containing only its exited, unreaped leader. The shim now accepts this case only with its owned child exit proof and a positive census of exactly that PID. Failed inspection or additional members preserve uncertainty. The App Server group also has to be signalled and joined before the shim records its exit. [XNU process group census](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/proc_info.c)

```sh
cargo test --locked -p swarm
cargo clippy --locked --all-targets -- -D warnings
cargo build --locked -p swarm
go -C "$GO_SCHEDULE_FIXTURE_SOURCE" build -o /tmp/swarm-schedule-codex-fixture ./cmd/swarm-fake-codex
python3 scripts/verification/schedule_catalog.py "$PWD/target/debug/swarm"
python3 scripts/verification/parity_schedule.py \
  --candidate "$PWD/target/debug/swarm" --provider /tmp/swarm-schedule-codex-fixture
```

`GO_SCHEDULE_FIXTURE_SOURCE` is a private checkout of `6a673aca36d41ac7c14e79e851a32bbc9dad18a9`, which contains the schedule-aware fake Codex. The native Claude and agy routes have separate evidence. [Claude schedule](rust-claude-schedule.md) · [agy schedule fixture](../../scripts/verification/rust_agy_schedule.py)

## Native user-manager calendar firing

Rust `17de672c` used its generated timer arguments, four state paths and minute cadence with a private calendar job on each native host. Both native managers fired twice, without a manual start of the tick. The first firing cold-started the owner and bound one automatic Codex occurrence. The second delivered the exact opening turn and scoped receipt. Explicit follow-up ticks verified the separate final assistant item, automation projection and workspace release.

On macOS 27.2.1 (`26B131`) arm64, Aqua launchd ran at 13:26 and 13:27 UTC and reported run counts of one and two. The fixture substituted the label in the generated plist. The 60 minute entries, arguments, environment and `AbandonProcessGroup` value came from the renderer. On Ubuntu 22.04.5 x86_64, systemd 249 ran at 13:25 and 13:26 UTC. The fixture registered runtime-only names, used the exact service definition, and retargeted the generated timer's `Unit` to its private service. The `OnCalendar`, `Persistent=true` and `AccuracySec=1s` values stayed in effect. The native manager created the job's persistence stamp.

Both tests removed their private registrations, including the Linux persistence stamp, and found zero remaining fixture processes. The existing production labels were absent before and after the tests. This proves native calendar execution with synthetic inference. Production-name installer registration, login persistence, authenticated inference and a complete day of scheduled operation still require evidence.

| Host | Rust executable SHA-256 | Calendar log SHA-256 |
| --- | --- | --- |
| macOS arm64 | `7d85efedc2fde7a86b25cebb3f1544c015ac4283ee6642b21e5774ff7c4e6eb1` | `4d381c3b24c09872d73e948619d4577608f9a83be561a0fcd7c16acccece34d9` |
| Linux x86_64 | `8c447594bfbea5d52c0350398d753a35e04036c996a9f40df1c2f4065e8895d5` | `0d93dd6658fc3d7f5b1b25ee023ca873a3b7ab8b9035d8308dfbff52e64158df` |

## Enable a paused automation from the board

On a paused job, `p` opens the saved definition. Escape leaves its state and revision untouched. `Ctrl+S` checks the proposed definition, provider and workspace against the current owner. If the timer is already active, the owner saves the edited revision and active state in one catalog transaction. Otherwise the board offers a separate timer confirmation. `Ctrl+D` saves the draft while paused. The CLI's explicit `schedule resume JOB` remains available.

The isolated PTY witness compares the paused editor and cancel against the released Go v0.29.24 binary. The Rust arm also checks unavailable and held preflight, timer confirmation and cancel, a stale revision, and successful activation under an already armed fixture timer. An older dashboard’s direct paused-to-active owner request is refused. The affirmative confirmation arms the fixture timer, and a second installation changes the job revision during timer startup to verify the board reports that the timer is active and the save failed. No job launches in these tests. The fixture uses a disposable `launchctl` or `systemctl` and a version-only Claude stub.

The macOS Rust log SHA-256 is `ca11ded5d4e31f9cf3a31c247a62945ea04e1de037341c8339694b50d3de10e8`. The paired native Linux log SHA-256 is `10a0e95e0acc068e991bda93598571cad00d7c737f57689a38ec675855ffd2c3`.

The broader board fixture initially restarted the owner after it could see a row that still had work to commit. Both the prior and current binary could reopen that row as `queued` with a rejected attempt. The fixture now waits for the exact `exhausted` receipt and verifies that the same ID appears after restart. Three fresh runs passed on macOS arm64 and three on Linux x86_64, with no survivors. The raw failed reproduction SHA-256 is `85f8e3598d128f782798fcffdd1b5802c54804c3d59c28f1052e9e4e66514ee7`. The corrected log SHA-256 is `60c80c873583c2f853cb1d7ea34db6c88782feb6dfba623297dd5cf69efbf99f` on macOS and `150c7abc26a040683a56e39f795df4bbde066339473e90286e67f423aeba859b` on Linux.

Explicit Reconcile now handles that interruption point. If the occurrence is still queued and every prior attempt recorded a definitive refusal, it advances only to the next unused route or records exhaustion. The original occurrence and rejected attempt are preserved. Unknown or dispatching attempts stay pending. The store-reopen test checks fallback with a distinct operation, exhaustion, both uncertain controls and read-only status. Replacing the predicate with the old empty-attempt rule reproduced the stuck queued state.

## Archive confirmation and list navigation

Signed Go v0.29.24 and Rust matched all 6,720 styled cells in six paired archive frames on each native host, including terminal modes and cursor positions at 80×24 and 40×8. Tab selected the next job from the list and Escape returned from job detail to the list. Opening the archive confirmation, pressing Enter or `n`, and cancelling with Escape left the catalog bytes unchanged.

Rust accepts archive approval only after a readable confirmation for the captured job has been flushed at the current terminal size. A single write containing `xy`, or keyboard and mouse confirmation after a shrink to 24×4, left the catalog byte-identical. After a 40×8 repaint, `y` archived exactly the captured job. The broader raw PTY workflow retained its history. Both systems reported zero survivors. The paired receipt SHA-256 is `6f5bf6732bc66a779316d8ef2084f17c48a04e6b874e56b2506baae1c6993443` on macOS and `b6e24c3e577f4ea0af0b5d21742640bab9271b5ca7bb727fdb1f86df927c015a` on Linux.

```sh
python3 -Bu scripts/verification/parity/automation_enable.py GO_V0_29_24 --reference-go
python3 -Bu scripts/verification/parity/automation_enable.py target/debug/swarm
```
