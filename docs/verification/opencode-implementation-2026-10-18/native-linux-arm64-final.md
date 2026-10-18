# Final integrated Linux ARM64 native qualification — 2026-10-18

The actual native integration gate passed on integrated Swarm `c5d2923214df0db247c0f6714f7d158f5a6caf1a`, using OpenCode **1.19.3**, ordinary UID/GID **1000**, Debian 13.3 ARM64, Linux `6.10.14-linuxkit` and Go `1.26.0`. This refreshes the earlier [6b370687 platform qualification](native-linux-arm64.md) with the integrated recovery, history and phone-consumer source snapshot.

The official [parent package metadata](https://registry.npmjs.org/opencode-ai/1.19.3) names the [Linux ARM64 platform package](https://registry.npmjs.org/opencode-linux-arm64/1.19.3). Its archive matched the published SHA-512 integrity before extraction. The [sanitized record](native-linux-arm64-final.json) includes source/archive, Swarm/native/script/package hashes, pinned image, dependency versions, arguments and cleanup.

The unchanged repository harness completed three root inputs, one native child, concurrent permission/question decisions, duplicate-operation reconciliation and two daemon SIGKILL/rejoins without adding native inputs. Exact native readiness and version checks passed. A `stop` response containing ordinary tools remained nonterminal until a later native response completed the same input. Nine scripted localhost provider requests ran, with zero external requests. The native history read returned imported native rows, an opaque continuation cursor, and the same earlier page through the exact `BeforeItem` anchor; pages did not overlap.

| Measurement | Observed | Declared gate |
| --- | ---: | ---: |
| Maximum semantic/decision dispatch | 0.224 s | 30 s |
| Maximum daemon rejoin | 0.366 s | 15 s |
| Maximum sampled daemon / shim RSS | 24,116 / 23,380 KiB | 256 MiB each |
| Maximum sampled daemon / shim FDs | 17 / 13 | 128 each |
| Maximum sampled Swarm disk | 165,835 bytes | 256 MiB |

The workload took 19.487 seconds, or 23.568 seconds including fixture cleanup, with eight resource checkpoints. Native OpenCode separately reached sampled RSS 1,043,860 KiB and 88 FDs; these are measurements without a native resource-budget claim.

Only the exact git archive, verified native executable and named Go caches entered the disposable container. No host credentials or profiles were mounted or changed. Owner termination was acknowledged and all owned shim exit files existed. Before container removal there were no live UID 1000 processes; an ended Go tool process and shim remained as zombies under the simple container PID 1. The container was removed and its absence verified; shared caches were preserved.

This is a short Linux ARM64 container qualification. It does not establish Linux amd64/physical-host behavior, external model quality, physical phone transport, elapsed 24h/72h reliability, between-sample resource peaks, or goroutine/queue bounds.
