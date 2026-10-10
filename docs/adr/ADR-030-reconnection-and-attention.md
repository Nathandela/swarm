# ADR-030: Automatic terminal recovery and structured attention

- Status: Accepted
- Date: 2026-10-10
- Amends: system-spec.md status vocabulary and V-5; adds V-7. The four display groups and frozen Adapter interface stay intact.
- Affects: terminal client, app-server admission, Codex adapter, backend request bookkeeping, status engine.

## Context

The terminal client stopped retrying after six failed connections. It could accept a replacement with a failed roster read and retained obsolete clients. Codex provides structured waiting and failure events, while Swarm's static event mapping could not distinguish questions from approvals or successful completion from failure.

The existing event protocol has no revision tying a queued session payload to a List snapshot. Replaying events buffered before hydration can regress a newer roster. Native approval and question requests may overlap, and delayed request resolutions must not clear a newer wait or provider failure.

## Decision

The terminal retries automatically at 1, 2, 4, then 8 seconds, with an 8-second cap until closed. A replacement must complete subscription and a bounded successful List. Cached rows remain visibly stale and daemon actions are disabled until then; drafts and selected identities survive. Existing singleton-safe daemon startup may revive a missing supervisor without restarting agents. Recovery never replays a user mutation.

Subscribe before hydration and continuously drain/coalesce recovered events into bounded invalidation hints. One asynchronous authoritative List runs at a time; another follows if invalidated during the read. Generation guards reject superseded connections and mutation replies. A completed local mutation invalidates an older in-flight List. Authoritative Lists can restore a previously absent session. Shared lifetime ownership closes failed, superseded and displaced clients, including candidates completing after quit. Upgrade recovery uses this same path. Accounts and Options reload their new connection while retaining unsent policy edits.

An optional pure `adapter.TypedStatusSource` interprets event bodies without changing the frozen Adapter interface. It returns normalized dimensions and active wait categories. Claimed malformed/unsupported events are no-ops. The transport preserves original frames for strict duplicate-key, case-alias, depth, size and thread-ownership validation; ContextGuard keeps its calibrated projection under the same feed fence.

Codex thread status, blocking user-input requests, validated approval requests and terminal turn status distinguish permission, prompt and failure. Backend request IDs correlate waits, with approval taking display precedence over a concurrent question. Native snapshots retain both wait categories. Turn/healthy/error boundaries retire obsolete waits; unknown, reused-item or old-turn resolutions cannot clear current attention. Questions remain answerable through the native CLI.

Add `interaction=error` for provider-reported turn/thread failure. Running idle error belongs to Needs input; active/unknown turn and exited/lost process keep their existing precedence. Grid fallback cannot erase a provider failure; an explicit healthy event/new turn clears it. The board labels Needs input as `approval`, `question` or `error`, with the generic label retained for other signals. Label changes within Needs input also trigger the existing expiring banner.

The protocol and persisted status carry interaction as a string and display group separately. Compatibility tests cover persisted reload, version-1 owner List/Subscribe and the encrypted phone status path. No protocol, interaction-item, persistence or shim version changes are needed.

## Consequences

### Positive

The view recovers through supervisor/socket replacement without losing agent identity or unsent edits. Questions and provider failures become visible without guessing from terminal output or changing approval authority.

### Negative

Recovered connections perform a bounded roster read after coalesced events. An unresponsive or unavailable daemon keeps the cached view stale. A mutation with an uncertain outcome is not automatically retried; the refreshed roster determines what landed.

## Alternatives considered

Replaying queued events cannot establish freshness without a new protocol revision contract. Adding event revisions would extend the requested change across every producer and consumer. Terminal-text error detection cannot reliably distinguish failed tools from failed turns. Extending the mandatory Adapter interface would force unrelated providers to implement a Codex-specific capability.
