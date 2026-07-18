# ADR-008: Status events are level-triggered latest-state snapshots (coalescing permitted)

**Status**: Accepted
**Date**: 2026-07-18

## Context

The v0.5 committee review of the single-producer roster fan-out (commit 5c2d698,
fixing the stale-rename race codex raised as HIGH-2) surfaced a follow-on
objection: with the roster poller as the sole snapshot producer, two status
commits landing inside one sampling window coalesce — the poller samples only
the final state, so a committed intermediate state (`A → B → A'`: `B`) may never
become a subscriber event. codex read P-3 ("WHEN a session's status dimensions
change, the daemon SHALL push an event") and L1 ("a status-dimension change
reaches every still-connected subscriber within 1 s") as requiring **edge
delivery** — one event per change — and named two exits: an ordered/versioned
mutation stream, or an explicit contract change to latest-state coalescing.

Three facts decide it:

1. **Edge-completeness was never the implemented contract.** Before 5c2d698 the
   direct `emitStatus` path did send one event per engine commit — but under a
   full outbound queue that event was dropped (P-3's own bounded-queue clause),
   and the repair path (the poller) only ever delivered *current* state. Rename
   changes always rode the coalescing poller. Every version of the system has
   guaranteed convergence-to-latest with bounded latency, not per-change replay.

2. **No consumer wants edges.** Events carry full-state snapshots. The TUI board
   renders current state; the V-5 transition banners are derived *client-side*
   by diffing consecutive received snapshots. Delivering an intermediate state
   that already ended would paint wrong (stale-at-arrival) data on the board for
   a frame. No consumer replays history; nothing keys off "an event happened".

3. **The lost states are not human-observable.** Losing `B` requires two engine
   commits inside one nudged sample interval (microseconds to low milliseconds
   — the nudge channel wakes the poller immediately). Engine commits are paced
   by output evaluation and ticks (hundreds of ms); renames are human-paced. A
   state that brief has no product meaning on a monitoring board.

## Decision

**Status/rename subscriber events are level-triggered latest-state snapshots.
Consecutive commits MAY coalesce; the daemon guarantees that the latest
committed state reaches every still-connected subscriber within 1 s (or the
subscriber is disconnected for slowness).** P-3 and L1 are amended to state
this explicitly. An ordered/versioned mutation stream is rejected as
over-engineering: it buys per-change replay that no consumer uses, at the cost
of versioning, buffering, and replay machinery on the hot fan-out path.

Pinned by: `TestFanout_RenameNeverRevertedByConcurrentStatus` (convergence to
latest under a concurrent commit storm, at the real subscribe surface) and
`TestE2E_L1Composite_SignalReachesRenderedTUIWithin1sUnderLoad` (the 1 s
latest-state latency bound, hook to rendered TUI, under PTY load).

## Consequences

- A status held for less than one sampling window may never be delivered as an
  event. Consequently a V-5 banner fires only for states that persist long
  enough to be sampled — which is exactly the set worth interrupting a human
  for (both banner-worthy states are human-paced waits, so in practice they
  always persist to delivery). This is the accepted residual; V-5, E7.2, and
  the matrix V-5 row are amended to "as observed in the delivered status
  stream" in the same change.
- Any future feature that needs true edge semantics server-side (e.g. a
  notification/webhook on every transition, or V2 remote clients replaying
  history) reopens this decision and needs the versioned mutation stream P-3
  no longer requires. That feature, not the fan-out, carries that cost.
- The single-producer fan-out (sole snapshot producer + nudge) stands: the
  stale-rename race stays structurally impossible, and the coalescing it
  introduces is now the documented contract rather than an accidental property.
