# OpenCode 1.19.3 terminal qualification

Pinned source: `e3c4eb242585dcd77be1a867391b7c25fd47ce52`.

A successful `message.updated` with `finish: stop` is insufficient. The native loop continues when the assistant has ordinary tool parts. `providerExecuted` parts and error-state parts marked `metadata.interrupted` do not require continuation. Swarm therefore reads the exact assistant ID and qualifies success only from a complete native `{info,parts}` row with positive `time.completed` and no continuing tool. Errors remain correlated to their exact native parent. Neither idle status nor text matching proves input completion.

The ordering evidence is:

- [prompt.ts lines 96–99 and 1099–1125](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/session/prompt.ts#L1099): native continuation guard and interrupted-tool predicate.
- [processor.ts lines 550–610 and 657–695](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/session/processor.ts#L550): sequential stream processing; cleanup persists remaining text/reasoning/tool states before setting and publishing `time.completed`.
- [session.ts lines 629–643](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/session/session.ts#L629): part and message writes await event publication.
- [schema/v1/session.ts lines 503, 596 and 612](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/schema/src/v1/session.ts#L503): both update events are durable within the session aggregate.
- [core/event.ts lines 320–355 and 379–389](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/core/src/event.ts#L320): database projectors and durable event commit complete before listeners are notified.
- [core/session/projector.ts lines 260–273 and 310–323](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/core/src/session/projector.ts#L260): message and part projections write their database tables inside those transactions.
- [message-v2.ts lines 519–532](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/session/message-v2.ts#L519): exact message read returns the persisted message and its full parts.

Consequently, the final completed row read can establish ordinary-tool absence even when SSE part events are delayed or reordered. This is specific to the pinned native writer; it is not an atomic view-at-admission fence.

Compaction remains a distinct limitation. [compaction.ts lines 465–555](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/session/compaction.ts#L465) creates fresh replay or synthetic continuation user IDs without an original-input ID link. Its `session.compacted` event carries only the session ID, and `tail_start_id` is a retained-context boundary. Swarm preserves exact outcomes on those fresh IDs and leaves an earlier owned input unresolved. A queued manual input or identical copied text cannot transfer ownership. The regression exercises both synthetic continuation and ordinary copied replay with another manual input present.

Validation: retained RED reproduces bare-event premature completion and stop-with-tool history misclassification. Focused state/decoder race tests use complete-row fixtures explicitly for successful outcomes. Actual native stop-with-tool runtime qualification is a separate required fixture gate.
