# Private OpenCode TUI bridge

This package targets the public OpenCode 1.19.3 TUI plugin API. It runs inside the
normal standalone TUI and uses that TUI's authenticated native SDK. The host owns
launch configuration, durable operation records, controller leases, and the PTY
input fence. The bridge does not replace those controls.

## Binding and reads

The host writes the flat version-1 `Binding` record as a regular, singly linked
0600 file in its physically resolved, owned 0700 session directory. The socket
must be in that same directory and have a fresh generation-specific pathname.
The plugin refuses existing sockets. Do not replace a live socket: Node's own
listener cleanup can unlink its pathname. The bridge token authenticates this
local channel only; it is not a provider or native HTTP credential.

The plugin creates the supplied native root ID or reads the exact saved root on
resume, validates directory and creation time, and navigates to it. `/ready`
reports the resulting identity. The host persists that identity before `/ack`.
A failed resume never creates another native session.

`/snapshot` preserves native root/history/request/child JSON and the opaque
pagination cursor. Children are direct, parent-validated children. Statuses and
pending request arrays remain native global collections; consumers must scope
them. `/events` starts with `swarm.bridge.connected` carrying the complete
identity, then relays native event envelopes. There is no replay cursor. A gap,
disconnection, or malformed stream requires a fresh snapshot. Streams are
bounded to 128 queued events and 2 MiB; an event is at most 1 MiB.

## Mutation boundary

All writes require the acknowledged binding, exact current root route and
working directory, supported native version, no modal, a successful current
native status read, and a verified target. New prompts additionally require an
idle root with no pending request in its verified ancestry subtree. An unresolved or cyclic request ancestry refuses dispatch. Reads and
request bodies are bounded. The final route and request-liveness checks and SDK
invocation share one JavaScript callback. This is a check at dispatch; the native
API has no atomic expected-view or expected-turn admission fence.

`/initial` forwards the genuine native Prompt only for an initial launch with no
other enabled external plugin, a mounted visible/enabled same-root empty
composer, and the host's input embargo still held. The wrapper preserves native
reactive props, refs, submit callbacks, and the right-hand slot. Public APIs do
not expose the live model-picker readiness or composer slot ownership. Normal
owner configuration remains the host's responsibility; unsupported profiles
must be reported, not silently replaced. CLI initial text must be withheld.

Prompt submission callbacks are not admission receipts. `/initial` observes a
new matching native user row and records its actual agent/model/variant. A
repeated operation reads that live-process receipt; it never submits again.
`GET /initial` reconciles late rows without resubmission. Lost-process or absent
receipts do not authorize replay of a host operation already marked started.

`/dispatch` accepts `prompt`, `permission_reply`, `question_reply`, and
`question_reject`. Prompts explicitly reuse the authoritative native session
agent/model/variant (`native_session_selection`); missing metadata refuses.
This is not the unsent TUI picker. The native session stores its resolved model
selection before writing each native user message. No configuration default is
guessed and no history-page search decides selection.

Before a new prompt write, an exact native message lookup must return HTTP 404
for the supplied caller message ID. Existing messages, transport errors, and
other statuses refuse. Native prompt 204 alone remains uncertain: admission
requires the exact newly observed user row with matching text and tuple. The
caller message ID is retained on uncertain receipts; only `status: admitted`
means admission was proved. The native API still has no atomic create-if-absent
or expected-view fence; the host must use unique deterministic IDs and custody.

Request replies require an exact current pending ID, exact target session, and
verified root ancestry. They do not require idle: answering a blocked worker is
the purpose of these actions. Questions preserve answer order, allow empty
arrays for skipped items, enforce single/multiple selections, and honor explicit
`custom: false` (omission allows custom answers). An awaited native HTTP 200 with
boolean `true`, or an exact matching native replied/rejected event after
invocation, confirms processing. Disappearance from the pending list does not.

Permission choices retain native semantics, exposed in `nativeEffect`:

| Reply | Native effect |
| --- | --- |
| `once` | Resolve the selected request. |
| `reject` | Reject the selected request and other pending permissions in that native session. |
| `always` | Add native in-memory approval patterns and resolve matching pending permissions in that session; those rules also affect later requests. |

The bridge retains at most 128 live operation records and 2 MiB of request
payloads. It evicts only terminal records; unresolved records remain until
processing is proved or the plugin ends. Full unresolved capacity refuses new
writes. Repeating an owned operation with its unchanged body reads its receipt;
a changed body conflicts. `GET /receipt?operationID=...` also reads/reconciles
without submitting. Prompt reconciliation uses the exact native message GET,
so a message moving outside the latest history page does not prevent recovery.
Late reply events reconcile request receipts. A missing or evicted receipt never
authorizes replay; durable host custody remains authoritative. The bridge's
`requestHash` is its own normalized content digest, separate from host custody
hashes and the initial-prompt text hash.

An SDK promise which ignores abort keeps the mutation lane occupied until it
settles. The host retains uncertain operations and never blindly replays them.
Queue/steer and Stop with an expected turn remain unsupported. Mock hierarchy
and multiple-request tests do not qualify the full native interaction source;
that requires separate native integration evidence.

Pinned primary source: [request effects](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/permission/index.ts#L109),
[awaited reply handler](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/server/routes/instance/httpapi/handlers/permission.ts#L16),
[question effects](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/question/index.ts#L114),
and [native session selection](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/session/prompt.ts#L613).

## Verification

`node --test internal/opencodebridge/tui.test.mjs` uses the standard Node test
runner, a mock public native API, and real private Unix sockets. It tests binding
custody, route drift, host disconnect, native read failures, admission evidence,
prompt forwarding, bounded event queues, and request ancestry. These tests do
not call a provider. `go test -race ./internal/opencodebridge` verifies the host
client and stream cancellation. Actual native TUI qualification is a separate
integration gate; mocked admission does not establish provider execution.
