# ContextGuard continuation: compact, then keep working

Status: implemented 2026-09-01 (ADR-023 amendment 2) -- integral to automatic dispatch, no separate setting in v1. At-most-once is in-memory by design: crash recovery holds the cycle, which can never re-observe the arming edge, so a durable marker adds nothing.
Owner ask: an automatic compaction on a working session must not strand the
task — the compaction should present itself as mid-task context maintenance,
and the flow should continue afterwards as if nothing happened.

## What the provider offers (codex 0.151.0, app-server schema + live runs)

- `thread/compact/start` takes **`threadId` only**. There is no way to attach
  instructions or framing to the compaction request itself.
- `Config.compact_prompt` (string, default null) exists: it customizes the
  compaction summarizer's prompt. It is codex USER configuration — writable at
  launch via `-c compact_prompt=...` (the `check_for_update_on_startup`
  precedent) or via `config/value/write`, which edits the user's
  `config.toml`. ADR-023 C-4 keeps harness configuration user-owned, so Swarm
  does not set it silently; it remains a documented owner option.
- `thread/queue/add {threadId, clientUserMessageId, input:[{type:"text",text}]}`
  is codex's native per-thread message queue, with `list/delete/start`
  companions. PreCompact/PostCompact managed hooks also exist (user-side
  config; `hooks/list` is read-only), and `thread/goal/set` can pin an
  objective on the thread — both noted, neither needed for v1.

## The live experiments (real session, second app-server client)

All on a throwaway swarm codex session running a deliberately interruptible
3-part writing task ("write part N, stop, wait for instruction").

1. **Queue at idle drains instantly.** `thread/queue/add` on an idle thread
   became a running turn within ~20ms (`thread/queue/changed` →
   `turn/started`); `queue/list` immediately after showed an empty queue.
   Consequence: enqueueing a continuation BEFORE the compact is dispatched
   would race the compaction exactly like a `turn/start` — the gate-1 disease.
2. **Compaction runs as its own turn, and the queue defers behind it.**
   `thread/compact/start` → `turn/started` wrapping the `contextCompaction`
   item. A `queue/add` issued ~170ms later SAT in the queue for the whole
   ~21s compaction, then auto-ran **31ms** after `item/completed` — no client
   action, no `queue/start` needed. The provider natively serializes
   queue-behind-compaction and auto-continues.
3. **The continuation genuinely continues.** The queued prompt framed the
   compaction ("context was compacted by swarm to keep this session focused;
   continue the task exactly where you left off") and the agent wrote the next
   story part with full narrative continuity across the compaction boundary,
   twice (parts 2 and 3).
4. **`clientUserMessageId` does not dedup.** Two `queue/add` calls with the
   same id produced two distinct queued submissions and two turns. It is
   provenance, not an idempotency key: at-most-once for the continuation is
   the daemon's to own, exactly like the compact dispatch itself.

## Proposed design (ADR-023 amendment 2 candidate)

One new step in the automatic-dispatch lifecycle, owned by the same worker:

1. **When to enqueue.** On the guard's own transition into
   `provider_compacting` (the compaction turn is provably active, so the queue
   defers — experiment 2's safe window), the worker enqueues the continuation
   via `thread/queue/add` on the backend conn. Fallback: if the worker first
   observes `latched` (compaction already finished), enqueue then — at idle it
   starts immediately, which is equally correct. Never enqueue before the
   compact's own write (experiment 1).
2. **At-most-once.** A `continuation` marker rides the existing per-session
   sidecar next to the lifecycle state, recorded through the same
   write-boundary callback before the `queue/add` bytes leave. Ambiguity
   after the write is a skip, never a resend: a lost continuation costs one
   stalled task (the owner can nudge); a duplicated one costs a surprise turn.
3. **Attended check.** Skip the enqueue if anyone is at the controls by
   continuation time — the human continues the task themselves; a queued
   surprise turn is worse than none.
4. **The prompt.** Fixed daemon-authored text, proven in the experiments:
   "This session's context was automatically compacted by swarm to keep the
   task focused. Continue the task exactly where you left off. If the task
   was already complete and you were waiting for review, say so briefly and
   do not start new work." The last clause protects ready-for-review sessions
   that crossed the threshold after finishing.
5. **Product contract.** A third ContextGuard setting,
   `continue_after_compact`, additive to the CAS settings document; default
   ON whenever automatic dispatch is enabled (the owner's stated intent is
   that compaction must not strand the flow), owner-toggleable. Observe-only
   and unsupported guards never enqueue anything.
6. **Out of scope for v1, documented options:** launch-time
   `-c compact_prompt=...` for owners who want every compaction (manual and
   automatic) framed as mid-task maintenance; PostCompact managed hooks;
   `thread/goal/set` as a compaction-surviving task pin.

## Safety notes

- The continuation is a normal message, not a destructive action; the
  dangerous orderings are all in WHEN it is enqueued, and the safe window is
  provider-enforced once the compaction turn is running.
- The effect-window gate (`compactionInFlight`) already refuses daemon-typed
  input during the compaction; the continuation enqueue is the one exception
  and rides the provider's own queue precisely so it cannot interrupt.
- Version fence unchanged: continuation ships only where automatic dispatch
  ships (exact live-gated allowlist). `thread/queue/*` behavior above is part
  of what a new version's live-gate rerun must re-verify.
