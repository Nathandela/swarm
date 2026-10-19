# Sustained qualification queue

## Overnight result (2026-10-19)

The final-source one-hour workload **passed**: 3,600.476 seconds, 224 inputs,
11 daemon crash recoveries and 893 scripted loopback requests. Declared Swarm
resource/latency gates and owned-worker cleanup passed; zero external model
requests. See [curated evidence](final-hour-2026-10-19.json).

The old queue is **stopped**, not running. Its 24h command failed argument
validation before any worker started because interval120 exceeded the fixture
maximum60. The 72h command would also have failed at interval180. No actual
24h/72h qualification occurred. The corrected plans use intervals60 with
2,000/6,000 iteration budgets, and every command is now preflighted before
starting a replacement queue.

## Corrected current queue (2026-10-19)

After max/default native smoke passed, a new frozen candidate queue started at
2026-10-19 18:05 UTC, production source `d86cb92d05085826a1facca2759b0e7e6b3ae183`.
All three commands passed argument preflight **before** the first worker launched.
The one-hour stage **passed**: 3,600.654 seconds including cleanup, 225 inputs,
11 daemon recoveries, 897 loopback requests and zero external calls. Configuration
was unchanged, declared budgets passed and all owned shims exited.
See [new-source evidence](minimum-hour-2026-10-19.json). The 24-hour stage is now
running; the 72-hour stage waits for its successful completion. Those durations
are not yet counted as passes.

| Stage | Duration | Interval | Maximum inputs |
| --- | ---: | ---: | ---: |
| 1h | 3,600 s | 10 s | 500 |
| 24h | 86,400 s | 60 s | 2,000 |
| 72h | 259,200 s | 60 s | 6,000 |

Each stage retains the same declared budgets below, restarts every 20 inputs,
requires actual elapsed completion and cleans up owned workers. Failure stops
later stages. The native/Swarm binaries and fixture hashes are checked before
each stage. The queue PID at startup is `19279`; initial worker PID `19283`.

Current evidence:

- Status: `/private/tmp/swarm-opencode-implementation-20261018/minimum-sustained/status.json`
- Queue, frozen fixture and logs: `/private/tmp/swarm-opencode-implementation-20261018/minimum-sustained/`
- Stage directories: `/private/tmp/oc-min-soak-1h-20261019`, `/private/tmp/oc-min-soak-24h-20261019`, `/private/tmp/oc-min-soak-72h-20261019`

Each active stage writes `heartbeat.json` and `metrics.jsonl`; completion writes
`finished.json` and `result.json`. SIGTERM to the verified current queue PID
forwards to its owned workload for cleanup and prevents later stages. A roster
read must use the active stage's four private `SWARM_DAEMON_*` paths and
`/private/tmp/swarm-opencode-implementation-20261018/bin/swarm-minimum-d86cb92d`,
following the command structure in the historical example below. The queue does
not modify installed binaries, native owner profiles, external model accounts,
remote publication or Beads records.

## Original queue (historical)

The final implementation is frozen at `c5d2923214df0db247c0f6714f7d158f5a6caf1a`.
At 2026-10-18 19:45 UTC, a private sequential **1h → 24h → 72h** workload queue
started. The results above supersede the original running/planned status.

The queue reuses the checked-in native workload with a frozen binary and fixture
copy. It checks their hashes and the pinned native binary before each stage.
Each stage must finish successfully **and** meet its requested elapsed duration
before the next starts. Any failed resource, receipt, history, recovery or cleanup
assertion stops the queue; unknown writes are never posted again.

| Stage | Requested elapsed time | Between-input interval | Maximum inputs | Crash/rejoin cadence |
| --- | ---: | ---: | ---: | ---: |
| 1h | 3,600 s | 10 s | 500 | every 20 inputs |
| 24h (invalid original command) | 86,400 s | 120 s | 2,000 | never launched |
| 72h (invalid original command) | 259,200 s | 180 s | 2,000 | never launched |

The real OpenCode 1.19.3 TUI executes scripted localhost tools, requests and native
child work through Swarm. No external model requests or owner-profile changes
are made. Declared Swarm budgets are 256 MiB RSS per daemon/shim, 128 descriptors
per daemon/shim, 256 MiB state/log disk, 30 s dispatch and 15 s rejoin. Native
OpenCode RSS/FDs/SQLite growth is reported separately. Goroutine/private-queue
peaks and physical sleep/wake are not established by these assertions.

Historical evidence:

- Queue status: `/private/tmp/swarm-opencode-implementation-20261018/final-sustained/status.json`
- Frozen queue/fixture/logs: `/private/tmp/swarm-opencode-implementation-20261018/final-sustained/`
- Stage directories: `/private/tmp/oc-soak-1h-20261018`, `/private/tmp/oc-soak-24h-20261018`, `/private/tmp/oc-soak-72h-20261018`
- Each active stage writes `heartbeat.json` and `metrics.jsonl`; completion writes `result.json` and `finished.json`.

The initial queue PID was `30986`; the initial 1h workload PID was `31068`.
The queue accepts SIGTERM and forwards it to its current workload so its owned
Swarm worker can clean up, then prevents later stages. Verify the current PID in
the status file before signalling; these are session-time identities.

While a stage is running, its read-only Swarm roster can be inspected with the
matching private paths (this original 1h stage has ended):

```sh
SWARM_DAEMON_STATE=/private/tmp/oc-soak-1h-20261018/s \
SWARM_DAEMON_SOCK=/private/tmp/oc-soak-1h-20261018/s/d.sock \
SWARM_DAEMON_LOCK=/private/tmp/oc-soak-1h-20261018/s/d.lock \
SWARM_DAEMON_LOG=/private/tmp/oc-soak-1h-20261018/s/d.log \
/private/tmp/swarm-opencode-implementation-20261018/bin/swarm-final-c5d29232 ls --json
```

Read the queue status first and select the active stage directory. These private
workers use the scripted fixture provider, not the free DeepSeek/GLM workers.
The queue itself replaces no installed artifact; the separate owner-authorized
[v0.19.0 rollout](../opencode-v0190-deployment.md) updated installed Swarm on the
Mac and Linux VM, without publishing an Android app. Beads
`agents-tracker-vyhy.9` remains open until actual duration and release criteria are
demonstrated; the queue does not publish or modify the tracker automatically.
