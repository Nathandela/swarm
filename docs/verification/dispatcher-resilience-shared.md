# Office-Swarm shared-thread qualification

## Observed on 2026-11-21

The Office VM ran Swarm 0.29.9. Its personal connector reported connected, zero pending admissions, one active DM manager and an existing uncertain job. A recursive check of registered Office connector state found only Office-Swarm. The Mac also had a production personal enrollment. Neither installation was designated as a separate shared test bot.

The Office bot created a public qualification thread under the authorized server's general channel. A subsequent GET verified its guild, parent, type and unarchived state. The parent permissions included View Channel, Read Message History and Send Messages in Threads. The application lacked both Message Content intent flags. The server connector requests that intent. No volunteer had been confirmed and no challenge had been posted at preflight. The thread link and identities belong in the private trial record.

The reported Discord-only stall, relieved by native terminal input, is a separate incident from manual closure and replacement. A queued notice, marker on disk, native final or worker mailbox acknowledgement alone cannot establish that Discord received the substantive answer.

## Before opening the trial

1. Confirm a human sender, the thread, the exact binary revision and who is observing. Use a separately designated test bot if available. Reusing Office-Swarm requires Nathan's agreed DM interruption window. Confirm the bot has Message Content enabled before the window. Discord supports concurrent sessions and limits IDENTIFY starts per five seconds. Both modes here skip legacy command registration, but honoring the agreed window still governs use of the production identity. [Discord Gateway](https://docs.discord.com/developers/events/gateway#sharding)
2. Save a UTC timestamp, release/commit, binary hash, service identity, production connector selection, installation ID, discussion boundary, outstanding attempts and manager/worker instance IDs in a private durable trial directory. Record `external_input` and any draft or uncertain action. Leave these jobs with their original owners.
3. Provision a separate candidate supervisor and connector. The following commands assume a private, durable absolute `trial` path short enough for Unix sockets, an empty enrolled project, an authenticated manager CLI, and existing shell variables for the candidate executable, bot token **file path**, guild ID and thread ID. For a public thread, confirm that access to the test account and its enrolled workspace is appropriate for any human participant able to post there. The teammate mode admits every human participant. Use a separate OS account where account-level isolation is required.

```sh
umask 077
mkdir -p "$trial/s" "$trial/d" "$trial/project"
export SWARM_DAEMON_STATE="$trial/s"
export SWARM_DAEMON_SOCK="$trial/s/daemon.sock"
export SWARM_DAEMON_LOCK="$trial/s/daemon.lock"
export SWARM_DAEMON_LOG="$trial/s/daemon.log"
"$candidate" discord connect --mode teammate --manager codex \
  --project-access enrolled --state-dir "$trial/d" --workdir "$trial/project" \
  --token-file "$token_file" --guild "$guild" --channel "$thread"
```

Check that the saved config pins the candidate's socket and supervisor state, that its sole channel is the thread ID, and that production still selects its original connector. This rehearsal uses the enrolled project policy. Record separately any behavior that requires production's VM project access. Omit `--install` because it targets the ordinary user's service.

## Cutover and challenge

1. For an authorized Office identity switch, record a fresh production snapshot and stop only the connector with `systemctl --user stop swarm-discord.service`. Its supervisors and workers remain running. With a dedicated test identity, leave the personal service running.
2. Run `"$candidate" discord doctor --state-dir "$trial/d" --channel "$thread"`. Require the correct identity, Gateway, channel permissions and usable authenticated native CLI. Space Gateway IDENTIFY starts at least five seconds apart if the app reports maximum concurrency one. Start `"$candidate" discord run --state-dir "$trial/d"` under its own tracked process or uniquely named transient unit. Check candidate status with the explicit state directory. Require connected teammate mode, a distinct installation and the expected native supervisor.
3. Give the volunteer the thread link and ask them to select the actual bot mention and send once, **“What is 17 times 19? Reply in one sentence with the product and the word cedar.”** Record the human author, source ID and UTC time. Observe without native input, retries or a second opener. Verify one substantive bot message in that thread with 323 and cedar. Count notices separately. Record elapsed time, publication and Discord IDs, manager identity, input-counter delta and any second answer throughout the agreed observation interval. Preserve a timeout as a failure.
4. For the restart trial, use a new human-authored task and a fresh marker. Ask the dispatcher to delegate a bounded arithmetic/report task that runs long enough to observe its worker. Confirm an active native child and record its attempt, session, instance and PID **before** restarting the candidate component under test. Name the component in the record. For a supervisor restart use only the verified candidate supervisor, retain the shim/worker and reconnect the candidate to its saved socket. A connector-only restart exercises a different boundary. Require the original worker and attempt after restart, no concurrent second manager, no native nudge or duplicate launch, and one substantive Discord result on the designated thread. Capture observed receipts before and after. If the worker finishes before the restart, the trial cannot qualify the active-worker case.
5. Repeat the Discord-only and active-worker trials with fresh markers after installing the published patch. Keep pre-release and post-release observations separate, including which binary served the connector and supervisor. Record the personal DM trials in their own discussion scope.

## Rollback

Stop the exact candidate connector process or its unique transient unit. The regular `discord disconnect` command looks up the user's fixed service even with an explicit state directory, so it can stop the production connector. Preserve the candidate ledger, status and exit evidence. Confirm which candidate workers are still active before retiring its isolated supervisor.

When Office-Swarm was paused, run `systemctl --user start swarm-discord.service`. Its original unit selects the saved personal state. Verify personal connectivity, installation and supervisor identity, discussion boundary, manager/worker continuity and repair results against the snapshot using an ordinary production shell. Count any deferred messages and reconcile by Discord ID. Leave ambiguous effects recorded as uncertain. Keep real conversation IDs and contents in the private evidence record.

## First live candidate window, 2026-11-21

The owner enabled Message Content and authorized a brief pause with rollback. The Linux amd64 candidate identified its clean source as `6d2b6fac`. Its local and remote SHA-256 was `482cae2e2b142ea1a6d9fc256cf946bfe0076254d35f7d10785e480bd8e212f6`. It used a separate supervisor, an empty enrolled workspace and the dedicated thread as its sole allowed channel.

The first cutover attempt restored the production connector immediately when the isolated supervisor socket was absent. After starting that supervisor, the second attempt passed all seven doctor checks, including Gateway intents, thread permissions, supervisor capabilities, Codex 0.171.0 and native login. At 09:25:26 UTC the shared connector reported connected and the thread challenge was relayed to the owner.

From 09:25:56 through 09:32:35 UTC, 76 observations recorded the candidate status and thread history. The thread remained empty, including a final REST read after rollback. No human request arrived. This window establishes shared connector readiness and rollback. Discord-only response latency, duplicate count and original-worker survival remain unmeasured in this live thread.

At 09:32:36 UTC the independent systemd timer stopped the candidate and restored the production personal connector. Its service reported success. Production status confirmed the original installation, discussion, manager instance and worker attempt. Pending admissions were zero before and after, and the earlier uncertain job retained that state. The isolated supervisor was stopped after its roster returned empty.

## Live human trials, 2026-11-21

**Discord-only wake.** In a second window the candidate reported connected at 10:05:26 UTC. At 10:09:44.200 a human mentioned the bot in the designated thread and asked for the product of 17 and 19 in one sentence containing “cedar.” The bot replied “Seventeen groups of 19 cedar trees total 323 trees.” at 10:10:14.975, a latency of 30.775 s. Across 132 observations through the timed rollback at 10:13:18, the thread contained one human request, one substantive reply and zero notices or duplicate answers. The isolated native manager recorded zero external input. The original personal service reconnected with the same manager and worker identities and zero pending admissions.

**Supervisor restart with a live worker.** The candidate reconnected at 10:14:26 for a separate, twelve-minute trial in the same enrolled thread. A fresh release filename was absent. At 10:21:42.943 the human posted the worker instruction without a mention. The bot accepted it, sent a reconnect gap notice and acknowledged that it would assign one worker. At 10:22:11.320 the human repeated the same instruction with a mention. Record those as two distinct source messages. They requested the same work and yielded one child.

At 10:22:46.266 the candidate had one running manager and one live child in the enrolled project. The pre-fault capture bound the child session, physical instance and job attempt to a live shim PID and its kernel process start time. The operator restarted only the candidate supervisor. At 10:23:08.606 the new supervisor had a different PID and had rejoined the same worker, instance, attempt and shim process. Only then did the operator create the release file. No native input was sent.

The original worker reported that it saw the file and computed 713. Its completion-note attempts and subsequent request for help received stale bridge errors. The dispatcher replaced the original manager and posted a continuity notice. At 10:24:16.510 it published “larch 713 (23 × 31).” The outbox tied that message to the recovery-only native worker-proof admission and original job/attempt. Both REST and Gateway confirmed the same Discord message. This was 67.904 s after observed rejoin. The worker continued to appear as uncertain in native job status.

Across 201 observations and a final REST read after rollback, the thread held one substantive answer per task. The worker trial added one progress acknowledgement and two system notices. At most one manager was running at any sampled time, and the roster contained exactly one distinct worker. The independent rollback restored Office's personal connector at 10:26:17 with its original manager, unchanged prior job and zero pending admissions. The operator then closed the completed, idle trial sessions and stopped the empty candidate supervisor. These measurements apply to the pre-release shared connector and its enrolled-project policy.
