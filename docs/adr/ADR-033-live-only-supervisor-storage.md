# ADR-033: Live-only supervisor storage

## Status

Accepted by owner direction 2026-10-16; implementation and rollout gates open.

## Date

2026-10-16

## Context

The two-host investigation found agent-generated work under supervisor session
state as the dominant disk consumer, alongside redundant terminal and structured
discussion captures. Explicit discussion deletion also retained hidden older
attempts. The owner chose a live-only supervisor, relying on the coding-agent
CLIs for discussion traceability, and rejected treating a global quota/garbage
collection system as the main solution.

## Decision

Swarm supervises live processes and stores compact identity, recovery, authority
and delivery state. It does not keep a second durable generic discussion archive.
Generic interaction history becomes a bounded RAM window with truthful unavailable
history boundaries. Keep small durable lifecycle/control records and monotonic
stream coordinates; an interrupted migration must never reset them.

New shims retain only bounded identity-discovery output required by adapters,
rather than raw terminal archives or final screens. Live attach uses their VT
screen. Agent working directories stay outside supervisor state, including on
native resume; optional worktrees remain project-owned and explicitly tracked.

Explicit discussion deletion removes the validated attempt family and owned
worktrees, including hidden predecessors, as amended in ADR-024. Existing large
workspaces require writer/ownership checks and preservation of unique work before
cleanup. Provider history, external projects and existing phone copies remain
outside automatic host cleanup.

Accepted-operation receipts, replay guards, uncertain-effect holds, account
ownership, unfolded hook custody, and exact pending outbound envelopes remain
durable. Historical display records never establish current approval authority.
Current pending approvals must survive ordinary in-process history eviction;
after supervisor restart fresh provider evidence is required.

Operation-count pressure refuses new acceptance before effects occur; it never
evicts unexpired guards or their exact outcomes. Existing retries keep their
reserved slots. TTL expiry remains longer than the signed-command validity
horizon, and repeated transitions compact the existing log.

This supersedes the full raw-transcript/final-screen persistence requirements in
ADR-003 and Epic 3, and the durable generic interaction-history retention/read
assumptions in ADR-007 remote access, ADR-009 structured chat, ADR-013 and ADR-014,
only to the extent described above. Live attach continuity, native resume,
authorization, at-most-once operations and outstanding custody remain binding.

The [implementation plan](../specifications/lightweight-storage-plan.md) records
the complete work, migration and acceptance gates.

## Consequences

Positive: lower retained disk usage, fewer duplicate sensitive payloads, smaller
restart replay, and an explicit boundary between supervisor records and agent
work. No new provider-history integration or global quota service is needed.

Negative: host conversation details may disappear after restart or RAM eviction.
Old shims and old wrong-cwd native threads need a deliberate transition; deploying
the new binary alone does not reclaim their existing files. Delivery custody can
still write conversation-bearing sealed envelopes until its existing retirement
boundary.

## Alternatives Considered

Keep a complete mirror; retain a bounded durable recent mirror; lazily fetch native
history; build a hybrid backfill/cache; isolate an optional archive; enforce broad
filesystem quotas. These tradeoffs are compared in the research survey. The owner
selected live-only supervision for the current host-storage program.
