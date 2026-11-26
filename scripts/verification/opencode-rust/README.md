# Installed OpenCode qualification for Rust Swarm

Both probes require installed OpenCode 1.19.3 and a Rust Swarm binary built from the revision being qualified. The probes verify the owner endpoint derived from a fresh state root before issuing commands. They use private test profiles and leave their scratch records for inspection.

## Linux

The runner needs Python 3, bubblewrap with working user namespaces, and the installed native binary at `$HOME/.local/bin/opencode`. Stage the compiled Linux binary and the three probe files in a new RAM directory. It mounts selected system dependencies read-only and runs Swarm, the TUI, and the fixture in one private PID and network namespace.

```sh
run_root=$(mktemp -d /dev/shm/oc.XXXXXXXX)
cp /path/to/linux/swarm "$run_root/swarm"
cp scripts/verification/opencode-rust/{launch.py,loopback.py,run-linux.sh} "$run_root/"
sh "$run_root/run-linux.sh" "$run_root" qualification
```

The probe verifies that IPv4 and IPv6 external egress are unavailable and that loopback works before starting the daemon. It supplies scripted model responses for the initial prompt, semantic input, and native permission and question requests. It exercises receipt recovery across daemon restart, exact operation replay, request-content hashing, rejected grouped answers, draft fencing, history pagination, and exact-root resume. The final JSON line reports the completed assertions and number of fixture model calls.

## macOS

Run with the installed OpenCode on `PATH` and an absolute Rust Swarm binary path. The runner starts with a clean environment and applies a macOS sandbox-exec profile to test daemons and their workers.

```sh
scripts/verification/opencode-rust/run-macos.sh "$PWD/target/debug/swarm"
```

The macOS probe exercises the no-model worker lifecycle with outbound IP denied. It checks native binding, SSE-backed idle, history and request reads, daemon rejoin, worker stop and resume. The resume assertion compares both native creation identity and backend incarnation.

For pinned JavaScript fixtures and Rust unit and owner tests, run:

```sh
node --test internal/opencodebridge/tui.test.mjs
cargo test --locked --all-targets
```

## Sustained native development runs

Freeze the installed OpenCode version, the Rust Swarm executable, and the
runner files before starting. Use the exact source SHA used to build Swarm.
The default queue runs 1h, 24h and 72h sequentially on fresh roots. Each stage
must demonstrate real elapsed time and pass cleanup before the next starts.
Use `--stage 1h` to run one stage separately. The queue checks every stage's
arguments and scratch capacity before spawning a worker.

On macOS, copy the runtime files and the release binary to a private short
path. The runner wraps the installed native binary with a direct exec that
sets the run-local temporary directory. Go and Rust filter this environment
variable at launch. The wrapper lets Bun use scratch under the write guard.
Results hash the wrapper and the actual native binary separately.

```sh
test_root=$(mktemp -d /private/tmp/oc.XXXXXX)
cp scripts/verification/opencode-rust/{launch,loopback,processes,soak,queue}.py "$test_root/"
cp scripts/verification/opencode-rust/run-macos-loopback.sh "$test_root/"
cp /path/to/verified/macos/swarm "$test_root/swarm"
caffeinate -i python3 -B "$test_root/queue.py" "$test_root" "$test_root/swarm" "$SOURCE_SHA"
```

On the designated native Linux host, stage the matching executable and the
same Python files in a new `/dev/shm/oc.XXXXXXXX` directory. Add `run-linux.sh`
in place of the macOS runner. Each Linux stage runs in fresh bubblewrap PID
and network namespaces with a read-only bind of the installed OpenCode.

```sh
python3 -B "$test_root/queue.py" "$test_root" "$test_root/swarm" "$SOURCE_SHA"
```

The queue records `status.json` and a log per stage. Each active stage writes
an atomic `heartbeat.json` and append-only `metrics.jsonl`. The final
`result.json` includes typed decisions, per-operation admitted and terminal
receipts, exact replay, one native user turn per input, the original provider
and shim PID and birth identities across daemon SIGKILL, elapsed seconds,
and verified cleanup. Swarm caps are 256 MiB RSS and 128 descriptors per role,
256 MiB state and log disk, 30 seconds for dispatch and 15 for rejoin. Total
disk use is capped at 512 MiB per stage. Native OpenCode measurements are
separate. Fresh native execution, simulator inference and final-release
qualification are distinct observations. Qualify the final source and
executable hashes again before release.

To stop, send SIGTERM to the queue only after checking its PID and birth from
`status.json`. The queue stops its current stage and prevents later stages.
Retain private evidence in the run root and copy only scrubbed observations
for publication.

## Mixed 24-hour development workload

Use a new queue root. Stage the five native probe files above and `mixed.py`.
Place the offline `scripts/verification/parity/claude` executable at
`$test_root/bin/claude` and an architecture-matched build of
`./cmd/swarm-fake-codex` at `$test_root/bin/codex`.

Add `--mixed` to the queue command. This runs one 24-hour stage. It keeps
the native OpenCode and a Claude fixture PTY alive on the same daemon,
observes attach, resize and exact echoed input, and checks both sessions
after SIGKILL and rejoin. On real minute boundaries, the scheduler claims
one occurrence in a separate workspace, starts a Codex fixture turn,
observes its proved completion, releases the workspace and deletes the
stopped fixture session while retaining schedule history. The private
Discord control receiver verifies the installation-bound publication and
coalesces exact retry. It uses a Unix endpoint and synthetic receipts.

Every sample measures per-role RSS, kernel CPU, threads and descriptors.
The mixed runner also records read/write bytes. The scheduler's tick
driver emits real wall-clock ticks without installing an OS timer. The
native scheduler parity gate independently checks timer installation,
cold environment restoration and invocation of the generated unit.
Protected Discord Gateway and call checks are separate release gates.

For a shorter calibration, invoke the platform runner directly with
`--mixed --duration 90 --interval 3 --restart-every 4 --source "$SOURCE_SHA"`.
This exercises at least one real minute boundary and reports actual
cleanup without satisfying the 24-hour gate.

For the prompt preflight fault trial, also stage `digest_pause.py` and
`processes.py`. A normal short loopback run with `--digest-pause old`
verifies the original transport failure and one explicit exact-input
retry. A build with the read-only retry uses `--digest-pause retry`.
Use `--digest-pause exhaust` to suspend the fixture across both
preflight deadlines and check the terminal no-effect behavior.

The test pauses only the TUI whose PID and birth match the private
worker record. It checks the actual OpenCode database and fixture model
requests independently of the Swarm receipt before accepting replay.
