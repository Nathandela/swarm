# Automatic reconnection and richer attention signals

Date: 2026-10-10. Scope: the terminal client's recovery and the existing session board's attention signals. Target release: v0.17.2, subject to rechecking the latest release before tagging.

Swarm should recover its view when the daemon connection disappears, and distinguish a permission request, a user question, and a failed turn. Recovery must preserve live agent sessions and unsent edits. Attention must come from the provider's structured events and pass through the existing status engine.

## Baseline and boundaries

Implementation starts from remote main 686383e, incorporating the newer Codex thread-ownership and Claude activity fixes. It already includes the Accounts feature's daemon reconnect seam. The existing implementation retries six times, can declare recovery after a failed roster read, does not close the replaced client, and does not consistently block board mutations while disconnected. Extend that implementation; do not build another reconnect subsystem.

Codex already supplies a per-session app-server feed. Its current adapter maps turn start/completion and command/file approvals statically. The app-server protocol also exposes thread status flags, blocking/nonblocking user-input requests, and failed turns. Dynamic event-body interpretation belongs in the stateless adapter; the engine remains the sole status writer.

The scope excludes previews, search/filtering, Stop controls, subagent browsing, archives, pins, history discovery, forks, billing, and other previously proposed additions. Existing PTYs, shim processes, controller leases, phone approval authority, and native CLIs keep their ownership rules.

## Recovery contract

1. A loss from the current event channel marks cached data stale immediately and starts one retry chain. A loss or event from a superseded channel is ignored.
2. Retry asynchronously at 1, 2, 4, then 8 seconds, capped at 8 seconds while the TUI remains open. A duplicate loss or obsolete timer cannot start another dial. Reconnection never restarts, kills, launches, or resumes an agent session. The production retry seam may use the existing singleton-safe EnsureDaemon path to revive a missing supervisor and reconnect its durable shims; it never restarts a responsive supervisor.
3. A replacement is usable only after hello, subscription, and a successful bounded List. A successful empty List is authoritative. A failed or timed-out List retains the existing rows, closes the candidate connection, and retries instead of claiming live data.
4. Subscribe before List and immediately drain/coalesce events into a bounded invalidation signal. The recovered connection's unversioned events are invalidation hints, never authoritative row payloads: maintain one bounded asynchronous List in flight and a dirty bit that schedules another List when events arrive during hydration. A stale queued event can therefore trigger a fresh read but cannot regress state or resurrect a deleted row. Continue this rule for the recovered connection, with generation guards and identity-preserving roster reconciliation. A completed local mutation invalidates any older in-flight List; authoritative Lists can restore an absent session without event tombstone suppression. A failed refresh marks the view stale and returns to reconnection. This intentionally trades a bounded roster read for avoiding a new protocol revision system.
5. Preserve selection, grouping/order, pending rename/tag text, launch/handoff drafts, and confirmation target identities. Refresh connection-dependent Accounts/options state. Any target removed from the new roster must not transfer its pending action to a neighboring row.
6. While stale, local navigation and draft editing remain available, but actions that require the daemon, attach, and remote mutations are disabled. Recovery never automatically replays an uncertain user mutation. The user must explicitly submit again.
7. Close failed candidates, stale successful candidates, and the displaced client. Quit cancels retry work and closes the current client; a candidate arriving after cancellation must close itself. Coordinate the pre-existing daemon-upgrade flow so a delayed reconnect cannot replace its newer client.
8. Show a persistent reconnecting/stale indication, then clear it only after full recovery. Reconnect errors must not expose raw transport/account secrets. Do not add configuration knobs.

## Attention contract

1. Preserve the four display groups and their ordering. Keep existing approval/request routing; this change observes and labels requests, it does not answer them.
2. Use existing interaction values for permission requests and user questions. Add the explicit `error` interaction value for a provider-reported failed turn/system error. A running idle session with this value belongs to Needs input. Process exit/loss remains Completed, and active/unknown-turn precedence remains unchanged.
3. Add an optional, pure event-body status extension beside the frozen Adapter interface. It returns normalized turn/interaction dimensions, observed wait categories, and an ownership flag. A claimed but malformed/unsupported event is a no-op rather than falling back to an unsafe static mapping. The core supplies the expected native thread identity, and the adapter rejects foreign/missing identity, duplicate JSON keys, excessive input/depth, and unknown status variants.
4. Codex normalization covers:
   - `thread/status/changed`: active with waitingOnApproval -> idle/permission; active with waitingOnUserInput -> idle/prompt; ordinary active -> active/none; idle -> idle/none; systemError -> idle/error; notLoaded -> no lifecycle inference.
   - `item/tool/requestUserInput`: a validated blocking request -> idle/prompt; explicit nonblocking requests do not pause the Working state. Omitted isBlocking uses Codex's compatibility default of blocking. The request remains answered through the native CLI.
   - `turn/completed`: completed/interrupted -> idle/none; failed -> idle/error; unknown/malformed terminal status -> no-op.
5. A new turn or explicit healthy status clears an earlier error. Grid fallback alone must not clear a provider failure after the typed freshness timeout; keep that error until explicit provider recovery or process termination. Register valid outstanding requests by their connection-owned request IDs, keep approval precedence over a concurrent question, and ignore nonblocking questions for status. Resolve and clear only a known current wait. An old resolution for a reused item reference cannot delete the replacement's answerability map. Clear outstanding waits at an observed turn boundary/error, so late resolutions cannot erase an error or affect a new turn.
6. The board renders `approval`, `question`, or `error` inside the existing status column when the session is in Needs input. Its transient attention banner uses the same label and also updates when that label changes within Needs input. Generic/older providers keep `needs input`; no text is synthesized from terminal bytes.
7. Failed command tools alone do not mark the whole session failed; only provider turn/thread failure does. All other adapters retain their existing behavior, including authenticated Claude hooks.

## Change map

| Area | Expected files and responsibilities |
|---|---|
| TUI recovery | internal/tui/reconnect.go and tui.go: retry lifecycle, complete recovery, resource cleanup, stale action guards; dedicated reconnect tests |
| Production client | cmd/swarm/main.go: reconnect dial and final-model cleanup; existing daemon upgrade/recovery tests |
| Adapter boundary | internal/adapter: optional pure status normalizer and conformance tests, without changing the frozen method set |
| Codex mapping | internal/adapter/codex: pure structured-event normalization and protocol-derived fixtures |
| Transport | internal/appserver and backendconnect.go: retain original JSON envelopes for strict adapter validation; preserve the existing ContextGuard projection under the same ownership fence |
| Feed assembly | internal/skeleton/backend.go: invoke normalization with the correct session/feed/thread identity and keep engine authority |
| Shared status | internal/status and internal/engine: admit error interaction, retain precedence, validate dimensions |
| Board presentation | internal/tui/general.go: attention labels/banners and width-safe rendering |
| Compatibility | protocol/persistence round trips and phone consumers: retain existing display groups, optional provider behavior, no wire/shim version bump unless tests establish a need |
| Documentation | ADR explaining the status-vocabulary extension and recovery contract; specification/index/README updates; verification evidence |
| Release | existing protected-main/PR and tag-triggered CI + signed GoReleaser publication; verify the next patch release assets and Homebrew tap |

## Validation before implementation

An Astra subagent reviews this plan against current main and the Codex protocol source. It must identify missing callers, race conditions, misleading states, compatibility risks, and unnecessary complexity. Resolve material findings in the plan before implementation. Record its final assessment in the verification document.

## Test sequence and acceptance

Write regression tests before each implementation slice and retain the failing-first output. Do not change existing tests merely to accommodate failures; legitimate changed contracts are explained by the ADR and new acceptance tests.

Recovery tests exercise loss on the board and form screens, repeated failures beyond six attempts, successful empty hydration, failed/timed-out hydration, subscription failure, nil candidate/channel, stale timers/results/channel events, duplicate loss, fresh capability loading, preserved selected identity/drafts, deleted confirmation targets, upgrade/reconnect overlap, candidate closure, quit during every recovery stage, and no mutation replay. Delay old name/tag/status/deleted-row events until after hydration and flood more than 256 events while List is blocked. Drive the real owner protocol server through socket disappearance/replacement for an integration test; confirm sessions remain intact and later events reach the same model.

Attention tests exercise every listed status variant and flag, approval versus question labels, both flags together, blocking default versus explicit nonblocking input, failed versus interrupted/completed turns, error recovery beyond typed freshness, same-group banners, unknown/superseded/reused-item resolutions, concurrent approval/question waits, unknown fields/variants, malformed/duplicate/deep/oversized JSON, foreign thread, replaced feed/session instance, invalid dimensions, process-exit precedence, and unchanged other-provider behavior. Integration tests drive app-server frames through the actual adapter/engine/daemon/roster path. A negative control must demonstrate that the old mapping misses the new question/error state.

## Astra plan validation

Astra reviewed the plan and current main on 2026-10-10. Its assessment was "Plan can proceed after these changes." The amendments above address all six findings: authoritative roster refresh instead of replay, request-ID correlation, shared cancellation and upgrade ownership, every stale-action entry point, deterministic ambiguous attention, and explicit error/banner lifetime. Compatibility review found Android already treats interaction as a string and groups separately; round-trip coverage remains required. The final implementation receives a separate independent review.

Run focused unit/integration tests and repeated race tests on changed concurrent packages, then the repository's required Go build, vet, lint, and full race suite. Use the pinned Go 1.25.0/linter versions and existing CI for platform-specific, Android, packaging, fuzz, and release gates. Inspect failing checks rather than rerunning to conceal a flake.

An independent Astra implementation review checks the final diff and test evidence before merging. Recheck remote main, integrate any concurrent changes, and rerun affected checks. Merge only after required CI succeeds. Recheck the latest stable tag, apply exactly one patch increment, and publish through the existing release workflow. Confirm the release workflow succeeded, signed checksums/compatibility assets exist, and Homebrew points to the new version. Activation on this machine uses the existing supported upgrade/restart path and must not terminate live sessions.
