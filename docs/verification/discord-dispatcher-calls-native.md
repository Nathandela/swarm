# Dispatcher call native admission and publication

The local fixtures exercise the existing native discussion ledger with typed call inputs, the Discord connector against a mock REST service, and the VM manager prompt. They ran with Go 1.26.5 for darwin/amd64 on an arm64 macOS host.

- The personal fixture resolves the established owner DM, pins its historical supervisor, retries the same turn with calls disabled, rejects a changed body or generation, and leaves the Discord text spool empty.
- The team fixture checks the configured participant list, the linked discussion's effective channel permissions, archived-thread behavior, and loss of private-thread membership.
- The native fixture checks that an ended call cannot produce a queued native write. A known no-effect draft refusal requires authority again, including when the refusal is reconciled after a lost acknowledgement. Uncertainty preserves the sealed operation and the existing text path preserves its FIFO behavior.
- Canonical publication fixtures check the exact body digest and attempt. Two independent attempts resolve to the correct admissions when their completions arrive in reverse order. The speech path refuses an attempt with conflicting later steering.

Existing VM shell delegation uses a process-scoped dispatcher credential. That credential proves the parent manager and worker attempt, but carries no native turn identity. A later shell worker result with only that proof stays eligible for the canonical written outbox. Calls direct new delegation through the existing native manager tool, which can record the exact launch or resume grant against the input. Neither observing a currently active native turn nor matching a job ID establishes that a prior shell command came from that turn. Attributing shell-launched workers to live calls remains open until the launch carries an independently verified native call coordinate.

The VM manager MCP fixture uses its production tool list and controller. It checks that a native call admission can launch and read an exact worker while existing text admissions still publish normally. The read requires the original voice grant or the exact verified worker wake. Removal of that grant prevents further reads. After a personal Discord reconnect, pending DM repair blocks live call admission alongside text ingress. Existing canonical delivery keeps its post-cutover audience checks.

These fixtures use synthetic input and no Discord or speech credentials. Actual personal and team calls, provider tool adoption, audible stop, and human end-to-end acceptance remain open.

## Hosted native personal canary

`TestHostedTeammateLifecycle` with `SWARM_TEAMMATE_TEST_CALL=1` and `SWARM_TEAMMATE_TEST_PERSONAL=1` uses an isolated Codex home supplied through `SWARM_TEAMMATE_TEST_NATIVE_HOME`, a trusted disposable workspace, and a candidate binary supplied through `SWARM_TEAMMATE_TEST_BINARY`. It uses synthetic coordinates and a local publication sink.

On macOS with Codex 0.170.0 and a candidate built from the integration worktree at `f5fa46a`, one complete run passed in 21.23 seconds. The typed written seed settled in 8 seconds. A read-only voice-origin turn returned the random marker in 6 seconds from the same native manager and instance. The exact voice submission was retried before and after native cutover. The bound Codex rollout contained one matching user input, the canonical publication resolved to that call admission, and the connector observed two pre-admission checks. No worker launched. The candidate used the real Codex account, with no Discord or speech-service access.

The earlier marker run also returned the exact value, but active native history paging was unavailable. The passing run counts only real user message records in the separately bound isolated rollout. It excludes Codex completion telemetry describing the same input.

A fresh native-personal run in VM project-access mode passed on the clean `a8a7bd7` candidate after merging the upstream continuity and attachment-recovery changes. The run took 18.73 seconds, with an 8-second written seed and a 5-second typed call reply from the same manager. It counted one actual native user input, one canonical publication, and two pre-admission checks. It used the same disposable profile and removed its temporary authentication file after the run.
