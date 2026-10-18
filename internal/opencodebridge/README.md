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

`/snapshot` preserves the native root/history/request JSON and opaque history
pagination cursor. Status and pending request arrays remain native global
collections. `descendants` contains only verified currently active/requested
descendants and their necessary ancestors, ordered parent before child.
`children` is the direct-child subset of that current scope. Neither field
claims a complete historical tree. `hierarchyScope` is
`current_activity_requests`; `hierarchyComplete: true` means every candidate in
that current activity/request snapshot was classified. Classification is bounded
to 128 candidates, 32 native ancestry reads, and depth eight; unknown ancestry,
cycles, malformed statuses, or exhausted bounds reject the snapshot.

Historical idle children are not enumerated on the readiness path. New prompts
refuse currently busy/retrying descendants as well as related pending requests.
A bounded snapshot is not atomic; consumers subscribe before reading it and
reconcile overlapping root/descendant events. `/events` starts with
`swarm.bridge.connected` carrying the complete identity, then relays native event
envelopes. There is no replay cursor. A gap, disconnection, or malformed stream
requires a fresh snapshot. Streams are bounded to 128 queued events and 2 MiB;
an event is at most 1 MiB.

`GET /message?messageID=...` reads one exact raw native message after rechecking
the root's directory and creation time. Route drift does not block this read.
An actual native 404 is exposed as HTTP 404 with `native_message_missing`;
transport, binding, and other native failures are not proof of absence. This
endpoint permits durable host reconciliation after the plugin's live receipt
cache is lost, without submitting or rewriting native history.

## Mutation boundary

All writes require the acknowledged binding, exact current root route and
working directory, supported native version, no modal, a successful current
native status read, and a verified target. New prompts additionally require an
idle current root/subtree with no related pending request. An unresolved or cyclic request ancestry refuses dispatch. Reads and
request bodies are bounded. The final route and request-liveness checks and SDK
invocation share one JavaScript callback. This is a check at dispatch; the native
API has no atomic expected-view or expected-turn admission fence.

A clean managed profile forwards the genuine native Prompt for all sessions,
including blank launches and resumes. Immediately before every semantic prompt
write, the bridge rechecks the mounted visible/enabled same-root native ref and
requires both its input and parts to be empty. A PTY Enter/reset is not evidence
of an empty native composer. `draftGuardSupported` is true only while this actual
empty-draft guard is usable. Profiles with other enabled external plugins keep
their existing composer; reads remain available but semantic prompting is
unqualified. Request replies do not require an enabled composer because native
pending questions/permissions can hide or disable it.

`/initial` submits through that genuine native Prompt only for an initial launch
with no other enabled external plugin and the host's input embargo still held. The wrapper preserves native
reactive props, refs, submit callbacks, and the right-hand slot. Public APIs do
not expose the live model-picker readiness, composer slot ownership, or hidden
normal/shell mode. Semantic text dispatch is an explicit SDK chat prompt; it
does not submit/clear the composer or claim its mode/picker. Initial forwarding
relies on the fresh launch composer and held startup embargo. Normal
owner configuration remains the host's responsibility; unsupported profiles
must be reported, not silently replaced. CLI initial text must be withheld.

Optional `expectedModel` (`provider/model`, retaining any slashes in the model ID)
and `expectedAgent` bind the host's requested launch choices. Before touching the
composer, the bridge checks a fresh `config.providers` catalogue and `app.agents`
list using the native TUI's active-model and visible-primary-agent rules. An
unavailable choice refuses before native submission instead of allowing native
CLI fallback. Blank choices preserve defaults. Create-mode initial submission
also requires empty native history. Changed choices conflict with an existing
initial operation. These checks establish availability, not an unsent picker
read; the observed native tuple is still audited after admission.

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

Binding-valid preflight refusals return correlated rejected receipts; they do
not become ambiguous transport failures after the host has persisted Begin.
Errors after native invocation remain uncertain unless processing is proved.

An SDK promise which ignores abort keeps the bridge mutation lane occupied until
it settles. The host retains uncertain semantic operations and never blindly
replays or lets later semantic automation overtake them. After the bounded call
ends, manual owner input intentionally remains possible for inspection or
explicit intervention. This is not an indefinite keyboard embargo or an atomic
native expected-turn fence.
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

Launch-choice preflight follows pinned OpenCode 1.19.3
[`sync.tsx`](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/tui/src/context/sync.tsx#L458),
[`local.tsx`](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/tui/src/context/local.tsx#L64), and the
[`config providers handler`](https://github.com/anomalyco/opencode/blob/e3c4eb242585dcd77be1a867391b7c25fd47ce52/packages/opencode/src/server/routes/instance/httpapi/handlers/config.ts#L24).

P7 host decisions supply private `expectedRequest` (full observed native JSON) and
`requestExpiresAtMS` together. The bridge compares the fresh pending row structurally,
including unknown metadata, binds that proof into the operation cache, and checks
expiry again in the final route/invoke callback. The public host requires these
proofs; direct private SDK fixture calls may omit both. Native SDK reply methods
have no atomic expected-content argument, so this is a dispatch precondition.

A bound, acknowledged initial preflight refusal is retained as `status: rejected`,
`nativeEffect: none`, with the exact operation/text hash and no native message or
selection tuple. `GET /initial` can recover it after a lost daemon response; it
never searches later owner input for a rejected attempt. The host must persist
that terminal refusal and never resubmit it. Native configured agents can return
`hidden: null`; the visible-primary check accepts null, omitted, or false only.

OpenCode 1.19.3 embeds Bun 1.3.14, whose HTTP response does not emit `close`
when an SSE peer disconnects. The listener releases stream capacity and queued
bytes on request abort/error and socket close as well as response close/error.
Actual native reconnect qualification retains one live stream while repeatedly
closing eight peers; disconnected daemon observers cannot consume the four slots.
