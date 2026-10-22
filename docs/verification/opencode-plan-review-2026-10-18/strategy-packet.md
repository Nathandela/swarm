# OpenCode maturity strategy for independent review

You are reviewing an implementation strategy, not implementing it. Read only this packet. Do not edit files, execute shell commands, inspect user configuration, browse, or infer unstated API guarantees. Give specific objections and a corrected implementation order. You are one of two reviewers: DeepSeek V4.1 Flash and GLM-5.3 Flash. The human wants to see how each model reasons about the strategy.

## User outcome

Swarm should give OpenCode maturity comparable to its existing Claude Code and Codex integrations. Priorities: long-running autonomous workers; agents messaging and supervising one another across CLI providers; reliable completion, permission and error monitoring; crash/restart continuity; usable model selection rather than typing IDs. Also cover structured phone conversation/control, native subagents, history/handoffs, safe upgrade, and honest capability rollout. A model picker is being implemented independently now.

## Existing system and measured facts

Swarm0.18.2 is Go/Bubble Tea. A per-user daemon owns the roster/engine/protocol; one independent shim per worker owns its PTY, so worker processes survive terminal close and daemon crash. Adapters are pure strategy descriptors and cannot own I/O/processes. Daemon/shim own lifecycle; assembly in internal/skeleton wires protocols. Codex has a shim-owned app-server with Unix socket/WebSocket JSON-RPC and attached TUI. Claude uses authenticated hooks plus checked PTY input. Existing local CLI verbs are spawn, handoff, ls, watch, send, peek, pilot. Local trust is same-user owner-tier, not strong per-agent isolation. No new distributed broker is wanted.

OpenCode1.19.3 currently launches standalone TUI with model/agent/prompt flags. Its adapter has only prompt-marker and busy 'esc interrupt' grid heuristics, no reliable settled idle, request or native error feed. It captures native ses_ ID from the exit-screen 'opencode -s ...' command. No OpenCode native backend, structured interaction capture, structured phone composer/approval/interrupt, model enumeration, native transcript handoff-source support or auto CLI refresh exists.

Measured through real Swarm: two OpenCode workers exchanged actual user messages and exact assistant replies through swarm send, including after daemon SIGKILL. Their actual PIDs/start identities survived two daemon crashes. Yet a pilot watcher timed out during successful replies and the roster stayed active/working. A native permission modal had Interaction none. A send to that modal returned0 but no corresponding native user prompt appeared. A finite passive child exited but its idle-looking OpenCode parent remained active, so notification stayed pending. Repeated unchanged grid snapshots refresh lastSignalAt every500ms; the generic30s stale downgrade is not a route to dependable idle.

One original OpenCode process ran631s, then its own SIGKILL left Swarm native ID empty; Swarm resume refused, although native history survived. A graceful stop of another process captured native ID; resume used the same native conversation/history and generated a new answer. Two separate shared bugs were confirmed: generated manual watch command puts flags after ID and parser rejects it; CLI supervised handoff inherits original launch root rather than actual source worktree.

Existing status is Process x Turn x Interaction; grouping currently prioritizes active/unknown over permission interaction. Existing interaction fold closes a turn on terminal agent messages, but a native completed assistant message may be an intermediate tool-call step. Existing approvals implementation has one current request slot per Swarm session. Existing journal/live capture is bounded and native CLI owns durable history (ADR033); do not reintroduce full durable transcript archives. Some identity maps currently grow until session end. Existing composer operations have durable operation IDs and outcome tracking. Direct-input uncertainty deliberately blocks automatic actions until known status progress. Passive notifications contain daemon-authored IDs/state only, not untrusted child prose.

Current baselines are not perfect: installed Codex0.163.0 is outside Swarm ContextGuard's old characterized0.150/0.151 range; Claude has no Swarm ContextGuard. External reattach command is Claude-only. These must not be presented as universal parity blockers.

## Native API evidence and a new qualification question

Tested legacy1.19.3 HTTP APIs: /global/health, create/get/list session and explicit parentID children, /event plus /global/event, /session/status, /session/{id}/message, prompt_async. SSE emits busy/idle/error and native permission.asked/replied. /session/status omits idle keys. On one error path, final assistant completion followed the first idle event. HTTP204 is admission only, not successful generation. The exact reply text and terminal error were checked separately.

Permission once was verified end-to-end: ask for harmless sleep1 -> GET /permission matched request -> POST /permission/{id}/reply once returned200 -> replied event/pending gone -> tool exit0 -> final assistant reply. Sending tools:{bash:true} unexpectedly promoted permission to allow; preserve owner policy by omitting such override fields. always/reject/questions/abort races/compaction/SSE replay were not qualified.

IMPORTANT new discovery: the pinned local /doc ALSO advertises a newer /api/session family. It is schema-only pending live qualification. It advertises:

- POST /api/session/{id}/prompt: durable input admission with optional caller id /^msg_/, prompt:{text,files?,agents?}, delivery:'steer'|'queue', resume:boolean. Response carries admittedSeq, input id, sessionID, delivery, timestamps and optional promotedSeq; conflict409 is possible.
- GET /api/session/{id}/history?after&limit: finite public durable events plus hasMore.
- GET /api/session/{id}/event?after: replay durable session events then follow; event durable metadata may be optional and public sequences need not be contiguous.
- /wait, /interrupt, projected paged messages, permission/question routes, context and compact routes.
- Interrupt/wait schemas show no expected-turn guard. /active is process-owned foreground drains, so an unrelated server may falsely look idle.

Do NOT assume these endpoints share the ordinary TUI's exact sessions, provide content-bound idempotency, retained history, atomic replay-to-follow, or cancellation fencing until tested. First qualify same-session/TUI compatibility, same-ID same/different-body retries before/after restart, after cursors/gaps, and legacy/native bridging. If these guarantees hold, use them instead of inventing a duplicate Swarm messaging log. If not, clearly bound the legacy fallback.

## Proposed architecture and order

0. Finish native model/agent discovery using bounded non-billable CLI catalog/config calls, reuse existing editable Suggest picker and keep manual fallback. Fix the independent watch argument and worktree cwd bugs.

1. Qualification gate: characterize pinned1.19.3 v2 durable API, native versus legacy sessions/TUI behavior, endpoint auth, port0 readiness, attach/resume, startup ownership, process death, history continuity and version changes. Save fixtures + real acceptance tests, not only source scans.

2. Provisional topology: one shim-owned authenticated OpenCode serve process per Swarm worker, with an attached TUI. Keep Codex backend as-is behind a small typed endpoint dispatch; avoid a broad provider framework. Shim records exact backend PID/start/endpoint and contains process group; daemon reconnects without owning server lifetime. Loopback only, no mdns/CORS wildcard. Generate endpoint auth secret in private per-session custody; never argv/log/phone/journal. Native history stays in user's profile. Persist exact nativeID + launch token + server incarnation/cursor safety state, not whole history.

The daemon should create/bind the native session and persist its ID before useful work, open and reconcile event stream, then attach TUI and admit initial prompt once. Native create may accept launch metadata for ambiguous-create recovery. Attach CLI supports session/dir/auth but NOT standalone --model/--agent/--prompt, so set exact session defaults through native API rather than blindly append argv. Existing shim fallback starts standalone after20s without backend_attach; managed OpenCode MUST NOT silently create a second conversation after a native session was allocated. Fail explicitly or reattach exact known session. Rollback must keep native history and refuse incompatible managed metadata safely.

3. Authoritative lifecycle + observation as first production vertical slice: use qualified v2 durable events/history or subscribe-buffer-snapshot-reconcile legacy with honest gap semantics. Root session + relevant native children aggregate into running/idle/retry/attention/error; permission/question must remain visible even if other work continues. Track native inputs/turns separately from assistant tool-step messages. Detect TUI session switch/new/fork so Swarm never writes old hidden thread while terminal shows another. Bound response/frame/queue/mapping memory and HTTP timeouts, reconnect with backoff and jitter; a broken structured feed disables autonomous send/composer while sacred PTY remains usable.

4. Semantic messaging + cross-CLI supervision: preserve explicit terminal send key/no-submit behavior. Add or reuse a narrow owner-tier semantic send route for pilot/agents with operationID -> native input/message ID -> admitted/running/completed/failed/uncertain receipt. Use native durable queue/idempotency only if qualification proves it. Never automatically replay an uncertain side effect after timeout. Do not promise exactly-once model execution. Completion monitoring must follow correlated native input/reply, not any idle or unrelated terminal event. Preserve same-user trust and treat child/model text as untrusted. Recheck passive-supervisor safety at the actual native write boundary.

5. Structured capture/history + approvals/questions + native stop: map existing journal item schema; bounded paging/delta coalescing/stable refs, native history repair, explicit irreparable gap. Preserve all simultaneous pending request IDs across children rather than silently superseding one. Bind approval to session instance/native request; forward exact once/always/reject scope and verify native resolution, including reconnect/replay and stale replies. Model questions are distinct from permission approval. Session-wide interrupt without expected-turn can hit a newer turn; qualify native fencing, otherwise disclose scope and refuse falsely precise turn-stop claims. Owner typing, tools, native children and phone events must coexist.

6. Continuity/operations: provider-specific ses_ validation (current generic resume paths assume UUID), exact native history/export resolver with anchored filesystem/API and on-demand bounded access, graceful/abrupt resume and handoff FROM/TO OpenCode, preserve actual ProviderCwd/model/agent, names/badges/subagent links. Then safe idle CLI refresh, environment compatibility, compaction observations/continuation with evidence and version fences. No silent credential import or blanket provider-key environment passthrough.

7. Staged acceptance: isolated opt-in native status/identity pilot, then message/supervision, then structured phone/control, then normal default for qualified versions; existing standalone terminal path remains explicit fallback. Full three-provider cross-send/monitor/handoff matrix, open modal/queued/busy recipients, native children, provider errors/retry/quota, duplicate/cancel races, daemon/server/shim kill, auth/port reuse, slow consumers/network cuts/retention gaps, worktrees, upgrade/rollback,24h soak with bounded memory/disk and periodic tasks, then multi-day unattended, macOS/Linux and phone. No new scheduled GitHub Actions; repository CI is release-tag only.

## Required review format

Give about700-1100 words. Start with your verdict and5 highest-impact corrections. Separate proven facts in this packet, assumptions requiring qualification, and your proposals. Include:

1. Best transport/topology choice and conditions that would change it.
2. The smallest first deployable vertical slice and dependencies.
3. Concrete failure/interleaving cases this order misses.
4. Exact acceptance tests for long-running messaging, monitoring and recovery.
5. What to remove/defer to avoid overbuilding while still reaching comparable maturity.

Do not claim you ran tests or browsed. Do not identify yourself from an inferred model name; actual provider metadata will be checked externally. End with REVIEW_COMPLETE.
