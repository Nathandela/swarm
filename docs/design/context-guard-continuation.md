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

## Why the provider queue was rejected (adversarial review, 2026-09-01)

The obvious design — enqueue via `thread/queue/add` while the compaction runs
and let the provider auto-start it — has an unrevokable window: once queued,
the message WILL become a turn when the compaction ends, and nothing the
daemon does can reliably prevent that. A human attaching or pressing Stop
during the ~20-second compaction still receives the surprise turn (a Stop
even cancels the compaction, going idle and draining the queue faster than
any revoke could land). Experiment 2's elegance is exactly the hazard.

## Implemented design (ADR-023 amendment 2)

The continuation is an ordinary `turn/start`, sent only when the outcome is
verifiable and every gate can be re-checked at the last instant:

1. **Arm.** The guard's own compact arms a one-shot, in-memory continuation
   at the write boundary. A compaction the daemon did not write never arms.
2. **Send at `latched` only.** While the cycle is in flight the attempt
   waits (a turn mid-compaction would cancel it — gate evidence). At latched
   the worker forfeits to an attendant human, waits out folded-status lag
   (latched is stable; status edges wake the worker) inside a two-minute
   freshness window -- beyond it the moment has passed and the attempt is
   forfeited -- then sends from the
   composer lane's head with the dispatch's own revalidation: unattended,
   quiet, Stop barrier unchanged, no uncertain composer outcome, backend
   identity current, worker not stopping. Any failed check forfeits.
3. **At-most-once, in memory.** The armed flag is consumed by the single
   attempt; failure or ambiguity forfeits (a stalled task the owner can
   nudge beats a duplicated surprise turn); every hold transition disarms
   immediately; a crash forfeits structurally — recovery maps the cycle to a
   hold, which can never re-observe the arming edge — so a durable marker
   would add nothing.
4. **The prompt.** Fixed daemon-authored text, proven in the experiments:
   "This session's context was automatically compacted by swarm to keep the
   task focused. Continue the task exactly where you left off. If the task
   was already complete and you were waiting for review, say so briefly and
   do not start new work."
5. **Product contract.** Integral to automatic dispatch in v1 — no separate
   setting; observe-only and unsupported guards never send anything. A
   `continue_after_compact` toggle remains a documented future option.
6. **Out of scope for v1, documented options:** launch-time
   `-c compact_prompt=...` for owners who want every compaction (manual and
   automatic) framed as mid-task maintenance; PostCompact managed hooks;
   `thread/goal/set` as a compaction-surviving task pin.

## Safety notes

- The continuation is a normal message, not a destructive action; every
  dangerous ordering is in WHEN it is sent, and the send happens only at
  latched behind the same lane and revalidation as the dispatch itself.
- Accepted residual: a manual/native compaction racing the daemon's own
  write can merge into one latch and receive the continuation — but the
  manual compactor is attached or phone-active and forfeits via the
  unattended rule in every reachable case; a foreign API client on the same
  socket is outside the product's threat model, as with the dispatch itself.
- Version fence unchanged: continuation ships only where automatic dispatch
  ships (exact live-gated allowlist), and this document's provider findings
  are part of what a new version's live-gate rerun must re-verify.
