## Revised recommendation

**Legacy observation ships first; managed same-runtime v2 is a hard gate only for adopting v2 queue/idempotency features, not for the first deployable slice.** Round 2 makes this clearer: the shared session row does not prove shared runtime. Empty v2 history for ordinary TUI conversations with completed replies means v2's durable event store may not cover TUI-visible conversations at all. If that holds, v2 history/repair features are inert for real workers, and building the first slice on them would be untestable against actual Swarm behavior. Ship the legacy slice now; the in-flight TUI+v2 queue/drain test becomes the decisive gate for phase 4's durable-queue adoption — nothing more.

**Receipt taxonomy (sharpened by round 2):**

- **Admitted**: v2 prompt returns admittedSeq + input ID, or legacy admission followed by correlated native input observed. Durable receipt anchor exists.
- **Uncertain**: connection lost before admittedSeq was recorded, or admitted but no correlated completion. Never auto-replay; disclose.
- **Conflicting**: 409 different-text-same-ID. This is caller-side misuse, not transient — surface to owner, never rewrite or retry.
- **Failed**: explicit rejection or generation error event.
- Exact msgID+body retry returning same admittedSeq is genuine content-bound idempotency for the queue path only — adopt it, but scope the guarantee to queue delivery, not to creates or interrupts.

**New round-2 hazards the plan must absorb:**

1. Repeated create with changed agent returns 200 with ORIGINAL settings. Create idempotency silently absorbs changed payloads. Reconciliation must compare returned settings against desired launch metadata and fail explicitly on mismatch — a 200 is not conformance.
2. --port0 chose 4096, not ephemeral. This corrects a topology assumption: readiness discovery must parse the serve process's actually-reported listen address, never assume an assigned port, and must detect collisions across per-worker servers.
3. after=1 exclusive with one surviving event proves cursor mechanics but not gap semantics. One-event fixtures cannot validate replay completeness on non-contiguous sequences; keep gap detection gated until multi-event restart fixtures exist.

**On DeepSeek's three claims:**

- *Identity persistence before admission + input-correlated completion*: agree, and round 2 strengthens it — the empty-TUI-history finding means completion can only be trusted via runtime observation (TUI/PTY or live events), never via v2 history presence. Persist nativeID at create; correlate every completion to a specific admitted input.
- *Multi-pending requests early*: agree, unchanged from my round 1 correction; if not moved early, explicitly restrict the supervision pilot to one child.
- *Daemon-owned shared server under resource pressure*: reject. The shim-owned server is what makes worker survival independent of daemon crashes — the proven SIGKILL continuity depends on it. Daemon ownership reintroduces the exact single-point failure the architecture exists to prevent, and the daemon is itself the crash/restart entity. If resource pressure is real, measure it first; a serve process is lightweight. Pressure alone does not justify moving lifecycle ownership, and no version of "shared server" preserves shim survival semantics.

**Errors in my earlier review:**

1. I framed same-session/TUI compatibility as an open assumption; round 2 refutes the optimistic half — row sharing is proven but runtime/event sharing is now affirmatively doubted. I should have weighted the risk that v2 history could be empty for TUI sessions, which inverts the value of v2 history features.
2. I listed "port0 readiness" as a qualification item without flagging that readiness might misreport; the 4096 result means my acceptance framing implicitly assumed a trustworthy reported port.
3. My acceptance test "same-ID same-body retry → exactly one native effect" treated idempotency as unproven across the board; round 2 proves it for queue delivery, so that test should now be split: queue retry is a regression test, create-retry is a conformance test against changed-payload absorption.

**Preserved semantics:** explicit terminal send key/no-submit behavior stays untouched; admission via v2 queue is an additive owner-tier route, never replacing the terminal as source of truth.

ROUND2_COMPLETE
