## Verdict

The strategy is directionally right and unusually honest about uncertainty, but its spine rests on one unqualified assumption: that the pinned 1.19.3 `/api/session` family shares the attached TUI's exact session, history, and identity. If it does not, step 3's "authoritative lifecycle" collapses back to the proven-unreliable prompt-marker/esc-grid fallback, and the plan has no trustworthy middle ground. The proposed topology (shim-owned serve + attached TUI + daemon reconnect) is the correct shape, but the ordering defers several cheap correctness fixes and the crash-continuity defect too late, and it treats "idle" as closer to completion than the evidence allows. Do the durable-API qualification as the true first gate; keep every downstream slice behind it.

## Five highest-impact corrections

1. **Qualify `/api/session` same-session/TUI binding before committing to it as authoritative.** The packet itself warns these may not share the ordinary TUI's sessions and may lack idempotency, retained history, atomic replay-to-follow, or cancellation fencing. Until a same-ID retry, an after-cursor gap, and a legacy/native bridge are tested, step 3 must ship behind a feature fence with honest gap semantics, not assume v2 works.

2. **Define completion by correlated native input→terminal reply, never by idle.** Evidence: `/session/status` omits idle keys, a final completion followed the first idle on an error path, an assistant message can be an intermediate tool-call step, and 500ms grid refreshes keep `lastSignalAt` live. Idle is necessary, not sufficient; the generic 30s stale downgrade is a fallback alarm, not a completion signal.

3. **Fix the proven shared bugs and the permission-grouping defect inside the first slice, not step 6.** The worktree-cwd bug corrupts every supervised handoff's actual source root; the watch argument bug breaks the manual watch the plan leans on; and grouping that prioritizes active/unknown over permission is why the modal showed `Interaction none`. These are prerequisites for trustworthy monitoring, not cleanup.

4. **Move crash-window identity persistence into slice 2/3.** The 631s SIGKILL leaving native ID empty (resume refused though history survived) is a continuity defect. Create/bind the native session and persist its exact ID (and launch/caller token) before admission, and make resume resolve a missing ID via session list + launch token instead of refusing. "Admit initial prompt once" is right; it must be fenced by that persisted ID.

5. **Collapse the one-request-slot approval model to multi-pending early.** "One current request per Swarm session" silently supersedes concurrent children's permissions. Preserve all simultaneous pending IDs in slice 3 so a busy recipient's modal cannot hide a second request.

## Proven / assumption / proposal separation

**Proven in-packet:** worker PID/start identity survived two daemon SIGKILLs; cross-send produced exact replies; pilot watcher timed out during successful replies and roster stayed active; native permission modal yielded `Interaction none`; send to modal returned 0 with no native prompt; idle-looking parent masked an exited child; grid refreshes reset `lastSignalAt`; graceful stop captured native ID and reused history; the two shared CLI bugs; one end-to-end permission round-trip; `tools:{bash:true}` widened policy.

**Assumptions needing qualification:** that v2 endpoints mirror TUI sessions; same-ID idempotency; retained history and `hasMore` bounds; atomic replay-to-follow; after-cursor/gap behavior; interrupt/wait fencing (schemas show no expected-turn guard); `/active` reflecting this process, not another server.

**My proposals:** a single liveness/correlation invariant independent of grid heuristics; deterministic pre-admission session identity; feature-fenced v2 with explicit legacy fallback; multi-pending approvals in slice 3; typed dispatch only.

## 1. Best transport/topology

Shim-owned loopback, authenticated `opencode serve` per worker with attached TUI, daemon reconnecting without owning lifetime — as proposed — **if and only if** v2 events/history bind to that TUI's session. Change it if: serve+TUI cannot share the exact session/history; the TUI's session switch/fork fights the server; per-worker serve cost (LSP/children) is prohibitive, in which case consolidate to one daemon-owned server with per-worker sessions; or v2 is absent on the pinned version, in which case either pin a version that has it or run legacy subscribe-buffer-snapshot-reconcile with explicit gap semantics and autonomous send disabled on gaps. Auth secret via env, never argv (TUI process listings).

## 2. Smallest first slice

One worker: managed serve+TUI, create/persist native session ID before admission, subscribe v2 events+history, correlate one admitted prompt to one terminal reply, expose read-only native status (running/idle/retry/error/permission) in `ls`/`watch`. Explicitly excluded: semantic send automation, structured capture, approvals write path, phone, model mutations beyond catalog. Dependencies: same-session qualification, auth, port-0 readiness, process-group containment, crash-window persistence.

## 3. Failure/interleaving cases the order misses

Compaction during the lifecycle slice (deferred to 6) causing false completion; reconnect replay double-counting turns; two children where one's idle masks another's permission; same-ID different-body retry executing twice; `promotedSeq` (steer promotion) ambiguous turn attribution; out-of-band TUI approval needing status clear; stale server on a reused port answering; SIGKILL inside the admission window; slow-consumer lag indistinguishable from a broken feed; non-contiguous public sequences across `/history` cursors; and the worktree-root mismatch on cross-worktree handoff.

## 4. Acceptance tests

**Messaging:** K workers × M scripted messages over hours; assert each native input ID maps to exactly one terminal reply, no duplicate execution, bounded memory/disk; inject daemon SIGKILL and serve SIGKILL mid-run. **Monitoring:** inject permission, question, error, retry/quota; assert each stays visible while other work continues and clears only on native resolution; assert no false idle with an active child. **Recovery:** SIGKILL serve at random; daemon reconnects, history intact, same session resumed, no second conversation. **Idempotency/races:** same opID same body → one execution; same ID different body → defined outcome; interrupt mid-turn → fencing proven or scope disclosed. **Upgrade/rollback:** version change refuses incompatible metadata, preserves history; worktree handoff asserts actual source cwd.

## 5. Remove/defer

Defer phone composer, structured capture, approvals write path, and the full cross-CLI matrix until liveness and receipts are proven. Delete the supersede-one-slot approval behavior now. Keep typed dispatch; build no provider framework. Do not reintroduce transcript archives (ADR033). Defer provider-key passthrough, auto CLI refresh, and compaction continuation until evidence exists. Fix the CLI bugs before any handoff milestone.

REVIEW_COMPLETE
