# Sustained qualification queue

The final implementation is frozen at `c5d2923214df0db247c0f6714f7d158f5a6caf1a`.
At 2026-10-18 19:45 UTC, a private sequential **1h → 24h → 72h** workload queue
started. These are running/planned checks, not completed qualification.

The queue reuses the checked-in native workload with a frozen binary and fixture
copy. It checks their hashes and the pinned native binary before each stage.
Each stage must finish successfully **and** meet its requested elapsed duration
before the next starts. Any failed resource, receipt, history, recovery or cleanup
assertion stops the queue; unknown writes are never posted again.

| Stage | Requested elapsed time | Between-input interval | Maximum inputs | Crash/rejoin cadence |
| --- | ---: | ---: | ---: | ---: |
| 1h | 3,600 s | 10 s | 500 | every 20 inputs |
| 24h | 86,400 s | 120 s | 2,000 | every 20 inputs |
| 72h | 259,200 s | 180 s | 2,000 | every 20 inputs |

The real OpenCode 1.19.3 TUI executes scripted localhost tools, requests and native
child work through Swarm. No external model requests or owner-profile changes
are made. Declared Swarm budgets are 256 MiB RSS per daemon/shim, 128 descriptors
per daemon/shim, 256 MiB state/log disk, 30 s dispatch and 15 s rejoin. Native
OpenCode RSS/FDs/SQLite growth is reported separately. Goroutine/private-queue
peaks and physical sleep/wake are not established by these assertions.

Live evidence:

- Queue status: `/private/tmp/swarm-opencode-implementation-20261018/final-sustained/status.json`
- Frozen queue/fixture/logs: `/private/tmp/swarm-opencode-implementation-20261018/final-sustained/`
- Stage directories: `/private/tmp/oc-soak-1h-20261018`, `/private/tmp/oc-soak-24h-20261018`, `/private/tmp/oc-soak-72h-20261018`
- Each active stage writes `heartbeat.json` and `metrics.jsonl`; completion writes `result.json` and `finished.json`.

The initial queue PID was `30986`; the initial 1h workload PID was `31068`.
The queue accepts SIGTERM and forwards it to its current workload so its owned
Swarm worker can clean up, then prevents later stages. Verify the current PID in
the status file before signalling; these are session-time identities.

A read-only Swarm roster for the active 1h stage can be inspected with:

```sh
SWARM_DAEMON_STATE=/private/tmp/oc-soak-1h-20261018/s \
SWARM_DAEMON_SOCK=/private/tmp/oc-soak-1h-20261018/s/d.sock \
SWARM_DAEMON_LOCK=/private/tmp/oc-soak-1h-20261018/s/d.lock \
SWARM_DAEMON_LOG=/private/tmp/oc-soak-1h-20261018/s/d.log \
/private/tmp/swarm-opencode-implementation-20261018/bin/swarm-final-c5d29232 ls --json
```

Read the queue status first and select the active stage directory. These private
workers use the scripted fixture provider, not the free DeepSeek/GLM workers.
No installed Swarm or Android artifact was replaced. Beads
`agents-tracker-vyhy.9` remains open until actual duration and release criteria are
demonstrated; the queue does not publish or modify the tracker automatically.
