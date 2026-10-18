# Actual Linux ARM64 OpenCode qualification — 2026-10-18

The short native integration gate passed on Swarm `6b37068795b9efcb78947fee8f1a513d4c42093f`, using actual OpenCode **1.19.3** under ordinary UID/GID **1000** in a disposable Debian 13.3 ARM64 container. The Linux kernel was `6.10.14-linuxkit`; Go was `1.26.0`. This is native Linux runtime evidence, separate from the earlier Go application test gate.

The [official `opencode-ai` metadata](https://registry.npmjs.org/opencode-ai/1.19.3) names `opencode-linux-arm64@1.19.3` as its platform dependency. The downloaded [platform package](https://registry.npmjs.org/opencode-linux-arm64/1.19.3) matched its published SHA-512 `dist.integrity` before extraction and execution. The [sanitized evidence](native-linux-arm64.json) records exact source/archive, Swarm/native binary, script, metadata and package hashes; image digest; installed container dependency versions; command arguments; declared budgets; and cleanup.

The unchanged repository loopback harness completed three root inputs, one native child, exact admission/outcome correlation, duplicate-operation reconciliation, concurrent permission/question requests and public owner decisions. Provider `stop` responses containing ordinary tools remained nonterminal until the later native response settled the input. Two daemon SIGKILL/rejoin cycles preserved the exact native conversation and did not add inputs. Nine scripted localhost provider requests ran; **zero external model requests** ran. Workload time was 19.208 seconds, or 23.403 seconds including cleanup.

| Measurement | Observed | Declared gate |
| --- | ---: | ---: |
| Maximum semantic/decision dispatch | 0.132 s | 30 s |
| Maximum daemon rejoin | 0.256 s | 15 s |
| Maximum sampled daemon / shim RSS | 25,784 / 22,264 KiB | 256 MiB each |
| Maximum sampled daemon / shim FDs | 17 / 13 | 128 each |
| Maximum sampled Swarm disk | 165,419 bytes | 256 MiB |

There were eight resource checkpoints. Native OpenCode's separate maximum sampled RSS was 1,093,104 KiB and its maximum sampled FD count was 76; those are measurements, not native resource-budget claims.

Only the project archive, verified native binary and existing named Go caches entered the container. No host credentials or installed profiles were mounted or modified. Dependencies were installed inside the disposable container. Owner cleanup acknowledged termination and every owned shim had an exit record. One exited shim remained as a zombie under the container's simple PID 1; removing the disposable container completed process cleanup. The shared Go caches were preserved.

This qualifies the declared Linux ARM64 container and source snapshot. Linux amd64, physical Linux hosts, external model behavior, hosted relay/physical Android transport, later phone-history changes, and elapsed 24h/72h workloads remain separate gates. Sampled resources do not establish between-sample peaks, long-term bounds, or goroutine/queue counts.
