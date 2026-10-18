## Verdict

Legacy observation ships first, but only as a **narrow, read-only attention/status slice**. Managed same-runtime v2 is a **hard gate on managed ownership and every autonomous write path**, not a gate on observation. This is not either/or: a hard gate that blocks all progress would freeze on an unproven dependency, and the new fact that real ordinary TUI conversations carry **empty v2 history despite completed replies** proves row-sharing is not runtime-sharing. Do not let rollback to legacy observation silently become a rollback to legacy *send*.

## Error in my earlier review

I offered "consolidate to one daemon-owned server with per-worker sessions" as a fallback. Retract it: it violates the shim-owned lifecycle invariant and reintroduces exactly the coupling the architecture is built to avoid. I also treated same-session *row* binding as near-sufficient once qualified; the empty-TUI-history fact shows qualification must be **runtime/event-level**, not row-level.

## State taxonomy (revised)

- **Admitted:** native durable input accepted — `admittedSeq`/input ID returned. Content-bound idempotency is now proven for the exact `msgID`+body retry returning the same `admittedSeq 1`.
- **Uncertain:** no receipt, feed gap, timeout before any native acknowledgment, or after-cursor discontinuity. Never auto-replay.
- **Conflicting:** same ID, different body → 409; or a replay/restart producing a different outcome than the recorded one.
- **Failed:** native error event or terminal error — distinct from 409.
- **Completed:** correlated terminal assistant reply, never idle.

Admission ≠ generation: 200/`admittedSeq` is not success. Label verbs accordingly.

## Assess GLM critically

- **Narrow legacy status/attention slice while v2 qualifies — accept,** with one fence: it must surface process/turn plus permission/question/error attention and must **not claim settled idle** or drive autonomous send. Repeated grid refresh resetting `lastSignalAt` makes legacy idle untrustworthy as completion.
- **Multi-pending before concurrent supervision — accept;** already my correction.
- **Permission policy preserved by tests, not conventions — accept and strengthen.** The `tools:{bash:true}` widening must be a test-enforced invariant asserting no agent-supplied override can expand owner policy.
- **Broad semantic-send changes to existing verbs — reject.** Overloading terminal send conflates admission with execution and risks breaking the explicit key/no-submit semantics. Keep existing send keys/no-submit behavior byte-for-byte; add a **separate owner-tier semantic route** that emits the taxonomy above. Do not relabel admission/conflict inside existing verbs.

## Order

1. Ship legacy observation: process/turn + attention events, multi-pending preserved, permission invariant tested, no idle claims, no writes.
2. Fix worktree-cwd and watch-arg bugs; keep them prerequisites for any handoff.
3. Hard-gate the **managed same-runtime v2 path** on the underway native-TUI + v2 queue/drain compatibility test, plus after-cursor exclusivity (`after=1` exclusive is a good anchor) and restart-survival for the *managed* session. Only then enable create/persist-before-admit, authoritative lifecycle, and the separate semantic send route.
4. Then capture/approvals/phone, then cross-CLI matrix.

## Port correction

`--port0` choosing 4096 is **not ephemeral**. Do not assume kernel-assigned free ports. Bind, read back the actual port, tie it to the server incarnation token, and guard collision with a stale or unrelated server (4096 is a plausible prior occupant). Readiness must key on the incarnation, not merely a listening socket.

REVIEW_COMPLETE
