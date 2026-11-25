# Durable dispatcher delivery for ordinary Swarm workers

**Status:** Implementation proposal for Nathan, 20 November 2026. Baseline is Swarm 0.29.5 at `0d64879f`. This document proposes a change; the commands and behavior marked *proposed* are acceptance targets. Dispatchers use the existing Swarm default models. Implementation is authorized after independent plan validation.

## Outcome and constraints

A dispatcher starts a worker with its normal `swarm spawn`. The worker can ask its dispatcher for input or report completion from inside a running native turn. A receipt survives process restarts. The dispatcher wakes, answers in its own words, and can steer the same worker. A parked or replaced manager retains the discussion, worker, question and unsent result. Discord remains the destination of intentional manager publications. Raw worker chatter and board-status changes do not automatically post there.

Keep the VM user’s existing environment, provider permissions, project access and ordinary worker launch defaults. Parentage determines where a note belongs. It adds no repository allowlist or separate SDK spawn path. Keep enrolled-mode compatibility, the DISPATCHER section, DSP tags and naming. Preserve the existing room or owner-DM audience, historic supervisor binding and manual intervention semantics.

## What the spikes established

- Rehan’s worker began at 16:52:26.450 UTC. A review wake arrived 686 ms later. At 16:53:50 the manager correctly reported that the worker was still checking. A second wake at 17:01:19 remained prepared with no admission; native completion followed at 17:01:29. The ledger retained an unresolved retirement. The controller spike reproduced this sequence under uncertain retirement, but the historical retirement receipt is unknown.
- Immediate and delayed explicit notes from ordinary VM children both failed because the launch did not establish the enrolled worker bridge. Generic `swarm message` to the manager returned submitted but counted as external steering, paused the scheduler and produced no worker-message admission.
- In the routed-mailbox control, the dispatcher published both challenge markers while the worker remained inside one native turn. The control used a synthetic Discord sink.
- A real 0.29.3 → 0.29.5 supervisor replacement preserved the worker shim and incarnation. The first send returned the missing-incarnation error before allocating an operation ID. A retry 220 ms later submitted. The named live session’s roster identity likewise appeared when the one-second hint cache warmed. Both guidance prototypes were discovered by fresh dispatchers given a general discovery hint.

The 20 November dispatcher research report and evidence bundle are retained on the owner VM and were delivered privately to Nathan. The findings above distinguish native observations, controlled tests and inference.

## Component changes and implementation order

### 1. Resolve the target of an ordinary message authoritatively

In `cmd/swarm/message.go`, the current preflight reads a single roster snapshot. In `internal/protocol/server.go` and `internal/skeleton/native_owner_input_cache.go`, that snapshot is deliberately allowed to omit capability information while a background probe runs. Keep that fast rendering path.

For a send, add a negotiated owner-protocol lookup for a single namespaced target, reusing the daemon’s stored incarnation and physical shim identity checks in `internal/skeleton/instance.go`. Return the current instance, or a specific ended, replaced, unsupported or temporarily unavailable result. Keep execution-time adapter checks on semantic send. A swap between resolution and submission must reject the stale instance. Never substitute a terminal keystroke or silently retarget a successor. On an older daemon, do a bounded pre-submission refresh of the existing roster, then give an actionable compatibility/error result. An empty operation ID means no submit occurred; after a transport ambiguity use the original operation ID and receipt query. Preserve stdout compatibility. Ship and qualify this independently of Discord.

### 2. Bind the ordinary child to the logical discussion before it can send

Extend the existing supervisor-owned spawn/lineage path in `cmd/swarm/spawn.go`, `internal/skeleton`, `internal/daemon/launch.go` and the owner protocol schema. When the validated parent is a dispatcher, persist the owning endpoint, discussion ID, worker session/incarnation, and job/attempt association as part of the existing idempotent launch. Reuse stable identities already recorded by spawn and the discussion ledger. Bind before the child can run its first shell command. After a crash, a retry of the launch operation must reuse its child and repair any missing binding. Derive the target from the caller’s verified association; worker-supplied Discord channel or manager IDs cannot redirect delivery.

Keep `swarm teammate message` as the public worker command. Proposed additive examples:

```sh
swarm teammate message --kind needs-input --id "$note_id" --text "Which environment should I inspect?"
swarm teammate reply --id "$note_id" --wait 90s
swarm teammate message --kind complete --id "$result_id" --text "Checked staging; the fixture returned 17."
```

Keep untyped notes and existing enrolled clients working. Return the generated ID when omitted, including on an uncertain result where the client already minted one. Add receipt lookup by that ID. Store a queued receipt only after the note is durable. Replaying the same ID and payload returns the same receipt; a different payload for that ID is a conflict. The unit of deduplication includes the sender’s incarnation and attempt. Waiting for a reply is optional. A worker may end its turn and receive a subsequent native instruction, or read the durable reply by question ID while still running. Implement reply retrieval for both qualified providers and test each separately.

### 3. Deliver and acknowledge notes independently of status

Use the owning supervisor as ingress for ordinary children. Persist accepted notes with the worker binding so submissions survive connector downtime. A small pending-note reader feeds the existing discussion event/wake/admission path in `internal/voice/discussion_message.go` and `internal/teammatemanager/runtime.go`. Reuse the existing store and reconcile loop; introduce no new service or polling agent. Keep enrolled bridge ingress, but share one normalization, deduplication and wake operation after verifying the sender. Do not fabricate an enrolled launch action for an ordinary child.

For the supervisor-to-connector handoff, use at-least-once delivery keyed by note ID. Commit the correlated event/wake in the discussion store before acknowledging ingestion to the supervisor. Commit the worker reply before reporting it available. Preserve order within each worker attempt, including input followed rapidly by completion. Distinguish queued, admitted to a manager turn, answered, and publication delivered. A worker completion note is a claim; record supporting output or native proof before publishing a verified result. If proof is unavailable, the manager can report precisely what the worker said and what remains unverified.

Use the existing audience checks before publication. A changed audience, revoked installation or genuine manual takeover can hold delivery with a visible reason. Normal worker notes must never increment external-input counters. Keep pending notes through downtime; bound payloads and backlog with explicit retryable errors before acceptance. Prune settled records under the existing retention policy only after their replay horizon. Never discard an accepted pending note to make room.

### 4. Make retirement and wake admission race-safe

In `internal/voice/discussion.go`, `discussion_vm.go`, `discussion_recovery.go` and `internal/skeleton/manager_retire.go`, distinguish a board review hint, an outstanding job, native turn completion and a verified job result. Ready-for-review can prompt inspection. An exported progress message must not settle the job. Retain the idle grace period. Permit a manager to park with a long-running child only when its independent mailbox and wake reader are active.

| Timing | Required behavior |
|---|---|
| Note arrives during a manager turn | Persist immediately, then admit when native input is safe. Preserve human and worker provenance separately. |
| Note arrives before retirement is claimed | Recheck pending work under the existing store transaction/generation fence; cancel the pending park and drain. |
| Retirement already submitted or receipt uncertain | Query the same retirement operation. Atomically cancel an unclaimed intent at the supervisor, fencing even a delayed original RPC. Once claimed, re-drive the same operation against the exact shim until exact exit is proved. A live shim may still have a pending signal. If exact exit is proved, finish parking, then wake the discussion. While physical outcome is unknown, persist the note, retry reconciliation with bounded backoff, expose oldest pending age and reason, and avoid a competing launch. |
| Discussion already parked or connector restarts | Pending ingress must invalidate the dormant shortcut even without a new human message. Resume the recorded manager; on proved unrecoverable binding use the established recovery path and retained transcript/checkpoint. Fence generations so only one successor consumes the wake. |
| Crash after native admission or Discord send but before local acknowledgement | Reconcile the recorded native operation and existing publication ID before replay. If an external delivery cannot be proved or safely deduplicated, mark it uncertain rather than posting a second result. |

Expose note ID, worker attempt, admission/source, retirement phase and publication receipt in existing status/doctor surfaces. Distinguish “awaiting worker evidence,” “queued while dispatcher is retiring,” and “manual intervention.” A prepared wake must progress or expose a reason visible to the operator.

### 5. Make dispatcher guidance discoverable

Add one canonical short guide owned by the CLI, proposed as `swarm dispatcher-guide`, listed by `swarm --help` and `swarm teammate --help`. Point a small provider-discoverable skill and the manager’s startup instructions to that guide. Keep operator-authored standing instructions. The guide asks for an explicit needs-input and completion note, describes stable IDs and how the worker receives its answer, and explains why a status hint or submitted receipt alone cannot prove completion. The dispatcher writes its own acknowledgements, uses one worker for follow-ups, and publishes when it has an answer, a real question or a material blocker.

## Regression and end-to-end release gates

| Layer | Cases and passing evidence |
|---|---|
| CLI and owner protocol | A cold first send succeeds on an eligible fresh and retained target. Repeat after TTL expiry, with a slow hint probe, wrong endpoint, same name/different session, replaced shim, ended target, older peer and unavailable backend. Verify one native effect, stable receipt, and no fallback keystroke. |
| Launch and binding | Send from the first worker command; retry the same spawn after acknowledgement loss; crash before/after binding and before first shim input. Verify one child and one association. Test unrelated worker, forged destination, stale incarnation, historical endpoint and resumed worker. |
| Store and reconciliation | Submit both kinds with unchanged worker status; duplicate ID, conflicting reuse, concurrent notes, restart between every durable handoff, full backlog, late completion from superseded attempt, and duplicate publication acknowledgement. Assert ledger counts and keyed receipts. |
| Retirement and conversation | Use a controllable clock. Exercise immediately before/after grace, already parked, uncertain retirement with eventual exact exit, same still-running manager, connector offline, restarted supervisor, manual pause and manager replacement. Repeat the historical Rehan ordering with a final wake minutes later. Confirm the job’s result is published once, or a real unresolved reason is visible. |
| Native delivery and guidance discovery | Use fresh profiles, Swarm defaults, ordinary CLI workers and the release candidate. Exercise each supported worker reply mode. Run immediate and delayed notices, a held worker turn, and an idle interval longer than retire grace. Require actual note-specific admissions, the correct job/attempt, exact random markers, and observed worker receipt of the answer. No second human prompt. Test both skill discovery and CLI help from clean sessions without giving the file path or subcommand. |
| Product acceptance | First use an isolated connector and synthetic Discord sink. Then use Nathan’s private Office-Swarm DM for a canary. Verify concise manager-authored acknowledgement, one visible DSP child, a worker question, a dispatcher answer, final evidence in the same DM, and continuity through restart. Check actual DM message IDs separately from sink assertions. Never test delivery in the shared channel as a side effect of this rollout. |

Record timings for spawn→first accepted note, queued→admitted, admitted→reply/publication, retirement reconciliation and duplicate suppression. Keep provenance and job IDs in test evidence. Gate release on deterministic correctness and repeated native runs across the retirement window; compare latency to baseline before setting a production threshold. Run project build, vet, lint, test/race and existing release-tag CI.

## Migration, canary and rollback

1. Add compatible readers, optional protocol capabilities and inspection first. Inventory connected supervisors, old-manager bindings, existing worker identities and pending operations. Snapshot connector state and record shim identities. Test downgrade readability on copies.
2. Enable ordinary-child binding and mailbox ingress for new workers in an isolated place. Reconstruct existing bindings lazily only where stored dispatcher lineage, endpoint and incarnation prove an unambiguous match. Existing shims cannot acquire a new environment variable in place; use the daemon-resolved route when identity can be proved. If proof is missing, retain the worker/history and expose a degraded status. Do not replay old summaries as fresh completion events.
3. Deploy compatible connector and daemon before advertising explicit notifications. Mixed-version peers report their supported behavior. Keep status observation and enrolled mode until migration tests pass. Verify new notes arriving during upgrade on both home and historical supervisors.
4. After the release gates, canary notification delivery in the Office-Swarm DM. Check surviving sessions and confirm that an immediate needs-input note and a delayed completion note reach the same discussion across retirement and restart. Verify worker receipt of the answer, one publication per result, and Discord message IDs before broadening rollout.
5. **Rollback drill:** Stop admitting new mailbox-enabled launches; keep a compatible consumer running until accepted notes, pending replies, retirement intents and ambiguous publications reconcile. Preserve receipts, endpoint state, shims and history. Restore an older binary only after compatibility checks prove it can safely read the active state or the new queues are fully drained and fenced. Never restore a stale snapshot over post-snapshot user work. If rollback cannot safely drain, leave the compatible reader running and surface the pending count.

## Launch scope and review acceptance

Use ordinary `swarm spawn` on the dispatcher's existing supervisor. The shim owns the native CLI and PTY as in the system specification (P-5 and attachment contract). Importing unrelated processes is outside this change. Keep Swarm's default models, launch arguments, environment and VM-user rights. No launch-policy decision is needed.

* Reuse the VM manager's existing pre-submission launch operation ID. Reserve the binding durably before native exec. Roster observation joins that exact record. Retry before reservation, after reservation, before exec and after commit with a lost acknowledgement. Verify one child and one job attempt.
* Authenticate the spawning dispatcher and calling worker against supervisor-attested session/incarnation evidence. An environment parent ID is a routing hint. Bind scoped credentials to the original launch endpoint, parent and job attempt. Authorize successors by logical discussion generation and keep the original parent for transcript proof. Exercise a sibling presenting the correct IDs of another worker on both write and read. This addresses accidental or spoofed routing within the existing VM-user trust boundary. Preserve a degraded route for legacy workers without adequate evidence.
* Persist attempt sequence and the note's sender-scoped key independently of expiring native semantic operation IDs. Check accepted key/payload before freshness gates on retry. Accepted notes remain ingestible after worker exit. Drive pending discovery from the connector outside dormant controllers, with the original audience reader. Test a two-hour outage including lost receipt, deduplication and retrieval of the durable reply by both providers.
* Persist answers for same-question lookup by either provider. Preserve existing native steering and enrolled behavior. Test observed answer bytes inside a held worker turn, then delayed completion after manager replacement. Reject superseded attempts explicitly and fence admission and publication across a change of generation.
* Exercise delayed first retirement RPC versus cancellation, and restart after the supervisor claims retirement but before signalling. Re-drive only the same claimed operation and physical shim. Test unresolved admission and publication with visible receipts and no duplicate sends.

## Patch plan for Discord wait notices

Baseline is `v0.29.21`. The candidate is `v0.29.22`. In a retained 72-hour sample, four personal DM requests got the generic wait notice even though a substantive reply followed within 44–78 seconds of the request. One Claudio request got the notice around 46 seconds and substantive progress around 187 seconds. A replay with the existing first observation makes the four personal notices ineligible on time alone. The Claudio example still crosses the threshold. Its worker and source would also have to qualify under the new rules.

### Discord experience

| What Swarm can verify | What Discord should show |
|---|---|
| Answer, real progress or a question is ready | Publish the dispatcher’s own message through the existing route. |
| A verified linked worker is still pending after two quiet minutes | At most one routine update for the open stretch of work in the discussion. Use “I’m waiting for the worker’s result.” |
| The ledger shows only admission, manager activity, a wake awaiting delivery or an unknown worker | Keep the wait internal. Escalate a confirmed blocker or the existing recovery deadline through its current route. |
| Another reply-eligible request arrives in the same discussion while the first remains open | Preserve both admissions and their answers. Apply the same routine notice budget to the overlapping requests. |
| A worker or dispatcher needs a decision | Use the existing question flow as soon as it is ready. |

Use the same policy in a personal DM and a shared discussion. Keep the eligible admission as the notice source and send only within its bound discussion. In a busy shared thread, reference the originating message where its authenticated Discord route supports a same-channel reply. Suppress reply pings. A starter in the parent channel can use the existing thread context.

### Implementation sequence

1. Keep the 30-second first check in `internal/voice/discussion_reply.go`. Measure quiet from the first observation without an answer or from the first observation of a higher substantive progress sequence. Reuse `ReplyStallSince` for the quiet wait and preserve the current ten-minute recovery calculation. The normal first opportunity is around 150 seconds after admission. Repeated reads of queued questions and answers veto routine status outright. Keep one oldest due check per sync, with the existing bounded grant or receipt check. A request without suitable worker evidence advances on its regular cadence so a different due request can be considered. Add a two-request fairness test.
2. Keep the current broad worker signal for reply custody and parking. Use separate notice evidence for a non-reserved, non-ended, exact session, instance, job and attempt linked to the current admission. Terminal worker proof, a saved completion or an unanswered worker question takes precedence. Before sending, match the exact instance to a current supervisor roster entry reporting an active turn with no attention or interaction. A disconnected supervisor, a provisional alias, an ambiguous worker or an unknown turn supplies no basis for a routine update.
3. Reserve one routine notice budget in the same voice store transaction that saves the notice. Persist the budget in the existing checksummed store header with an optional field outside the record checksums and logical byte count. Reserve against the stable admission and revision publication ID. Start the next budget when all reply obligations close, including across restart, overlaps, edits, failed posts, and final/progress through a different request. Keep checking and delivering every request through the existing obligation path. The previous reader can open the header. Its writes can discard the budget, so the rollback gate requires the active budget to close before the previous writer resumes.
4. Keep immutable receipt authentication in `internal/voice/discussion_publication.go`. Add a send-only disposition that first authenticates exact source, hash, sequence, question and attachment metadata. Only a verified nonfinal monitor publication can become obsolete. Veto obsolete legacy copy, the wrong source revision, a withdrawn source, a closed obligation, a queued question or answer, newly available progress, or a no-longer-pending worker. Let transient failures hold the optional notice. Send genuine output first across the whole discussion and leave its current order intact. Limit the optional send time to keep it responsive to newly admitted work.
5. In the Discord connector, resolve the reference from the current ingress record or compacted ingress guard during exact source verification. Check installation, discussion, request, revision, actor and message. Use a same-channel reply reference without a ping. A source verified in the parent channel gets an unreferenced in-thread notice. Keep the stored destination route, body and publication digest stable. The durable transition to sending sets the boundary for an attempt already in progress.
6. In a connector store transaction, re-read the exact outbox row before settling obsolete status. With a zero attempt timestamp and all chunks prepared, decrement the outbox count and compact to the existing durable receipt without confirmed Discord IDs. A 403 or 404 can leave an attempted chunk prepared. Move every obsolete attempted unsent chunk to the existing uncertain state, including a chunk reset to prepared. Keep its nonce and accept late authenticated receipts for the existing 48-hour guard horizon. Track passive settlement separately from Discord proof using the existing settlement timestamp and chunk state. Stop POST retries after obsolescence, including inside the nonce window. Passive status must allow normal exports and parking. Schedule bounded compaction even for parked discussions and re-read the row inside the same transaction to preserve late receipts.
7. Gate a rollback on closing the discussion’s current notice budget, settling prepared obsolete notices, and aging every passive receipt past its original nonce window. A previous writer inside the window can retry an ordinary uncertain text post. Verify installed and running versions and use the live ledger rather than restoring a snapshot.

### Acceptance and release

| Gate | Passing evidence |
|---|---|
| Voice monitor | Before and at the quiet threshold, fast answer, wait without a linked worker, verified worker, worker end or completion wake, genuine progress after silence, immediate question, overlapping requests, input edits, and existing stall recovery. Exercise multiple held and eligible inputs with a controlled clock. |
| Discord connector and manager integration | Prepared status becomes obsolete before first POST, including an answer already present only in the voice ledger. Test across restart and duplicate export. Preserve identifier and receipt for attempted, uncertain, partial and already delivered records. Check shared route and source, disabled reply pings, unchanged normal exports, and continued publication of genuine output during wait reconciliation. |
| Fit with production | Replay the timings above. Exercise the release candidate with a temporary connector and synthetic sink for personal and shared routes. Assert actual emitted order, send count and resulting receipts alongside controller state. |
| Patch qualification | Independently review the plan, then the exact release candidate. Run build, vet, lint, the Go suite and race checks on changed concurrency paths. Use existing release-tag CI and confirm the signed artifact is for the reviewed commit. |
| Deployment | Start with a signed Office canary. Record daemon, connector and surviving shim identities, pending work and prior version. Verify the connected route, unchanged custody, and any organic delivery evidence. Qualify Claudio against its existing binding and state before rolling out there. Use the previous signed build for a compatible rollback without restoring a state snapshot over pending work. |

Track the implementation and evidence in `swarm-9qc`. Release qualification requires passing independent review and the automated gates. Record any portion of live acceptance still awaiting a natural Discord exchange.
