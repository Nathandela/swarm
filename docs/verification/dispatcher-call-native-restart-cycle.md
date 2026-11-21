# Dispatcher call native interruption rehearsal

The uninterrupted `TestHostedTeammateLifecycle` worker canary passed in 99.51 seconds on the clean `43d006c0` supervisor candidate with Codex CLI 0.170.0. Two native workers returned the two random markers in reverse order. The existing canary kept the connector runtime and supervisor open throughout.

## Interruption and acceptance checks

The opt-in worker canary waits for a marker file written by the first worker and checks its running shim before closing the real `teammatemanager.Runtime`. Its result file remains withheld. The reopened runtime uses the same durable directory. Both paths verify the supervisor endpoint and original worker session, instance, attempt, shim PID and start time. Connector-only recovery also checks that the original manager and its native conversation remain available.

- `SWARM_TEAMMATE_TEST_INTERRUPTION=connector` closes and reopens the connector runtime with the same supervisor process.
- `SWARM_TEAMMATE_TEST_INTERRUPTION=restart` also terminates the supervisor with SIGTERM, waits for it to exit, and starts the same binary on the original isolated state and socket. The replacement supervisor must have a different PID.

The second worker starts in a synthetic replacement call. It must publish its marker before the first worker is released. Each worker must have a distinct completed native turn tied to the exact canonical publication for its enrolled job and attempt. Swapping attempts or forging the digest must fail publication validation. The original admission must still be at cutover and reject replacement call coordinates for the same operation. Replaying the worker admissions must leave one native input and one exact marked result per worker.

The hosted sink resolves the canonical receipt back to its original voice admission and models call ID and generation matching. It accepts the second result for the replacement call and rejects the first there. The separate synthetic `TestCallGatewayRestartsOnlyOnceAfterDAVEChange` fixture exercises the production playback path and checks that an old-generation result stays written while the replacement speech lane receives none.

The first hosted connector attempt timed out on a fixture assertion that counted every job status row. The retained ledger contained the exact enrolled attempt and a VM observer row for the same worker session. The assertion now checks the exact enrolled identity and physical supervisor roster, alongside native input and canonical publication counts.

## Native observations

The baseline and first interruption runs used the same clean supervisor binary from `43d006c0` (SHA-256 `4d3cc2bb135d29c9ee830b2184d746d9d01fb417484e7bc5e054004ef9627832`) and separately compiled hosted test binaries.

| Run | Observation |
| --- | --- |
| Uninterrupted baseline | Passed in 99.51 seconds. The second result preceded the first. |
| Connector close and reopen | Reattached the original manager conversation and waiting worker with the same supervisor PID, rejected replacement coordinates for the original admission, and launched the second worker. That worker’s Codex tool host timed out opening an execution session for the marker file. The canary timed out waiting for a native completion and canonical result. |
| Supervisor restart and connector reopen | Verified a different supervisor PID on the same endpoint. Native reconciliation returned an uncertain input outcome. The retained ledger recorded custody loss and a paused scheduler. The original enrolled worker and cutover admission persisted. The run did not reach replacement-call submission or publication. |

The deterministic call admission, reverse-attempt provenance, and synthetic Gateway playback fixtures passed. Repository build and vet passed, and scoped teammate-manager lint reported zero issues. The teammate-manager and voice focused race checks passed. Broader Discord race attempts hit a temporary-file mode mismatch under a restrictive umask and unrelated call-media timeouts.

## What the retained restart ledger establishes

The current voice ledger from the failed run (transaction 57) contains three completed, settled admissions, zero wakes and one worker still enrolled. The conversation recorded custody loss and a paused scheduler with no retirement request. The original manager proof ledger (transaction 26) retained its owner and 16 replay guards. The replacement supervisor recorded a typed custody rejection when it attempted to rejoin the original manager.

The previous canary waited for the original manager to be active before it submitted the second request. At that point the waiting worker had no result, and there was no unsettled admission or active wake to trigger replacement. The production controller intentionally requires pending work before creating a replacement intent.

The revised restart probe verifies typed quarantine and the original worker and cutover, then submits the fresh second admission while the first worker result is still withheld. It requires a ready replacement with distinct physical session and instance, an exact retired receipt for the original instance and checkpoint, and the original enrolled worker attempt. It counts matching native inputs across the distinct manager rollouts and checks both canonical results. The connector-only control still requires the same manager. The revised hosted probe has not yet produced a native result.

## Reproduction

Build `./cmd/swarm` and compile `./internal/teammatemanager` to a standalone test binary. Use an owner-only disposable Codex home, an authorized native test login and a trusted disposable working directory. Set `SWARM_TEAMMATE_TEST_BINARY`, `SWARM_TEAMMATE_TEST_NATIVE_HOME`, and `SWARM_TEAMMATE_TEST_MANAGER_DIR` to their absolute paths. Set `SWARM_TEAMMATE_TEST_CALL`, `SWARM_TEAMMATE_TEST_CALL_WORKERS`, `SWARM_TEAMMATE_TEST_VM`, and `SWARM_TEAMMATE_TEST_PERSONAL` to `1`. Run the test binary with `-test.run '^TestHostedTeammateLifecycle$' -test.v -test.timeout 11m` once per interruption mode. An empty interruption value runs the uninterrupted control.

This rehearsal uses synthetic Discord and voice coordinates, a local publication sink, and a disposable supervisor, workspace, and native profile. Actual Discord rooms and speech, abrupt supervisor loss, dispatcher process death, and live listener experience remain outside the evidence.
