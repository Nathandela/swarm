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
native status read, and no pending request in the root's verified ancestry
subtree. An unresolved or cyclic request ancestry refuses dispatch. Reads and
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

`/dispatch` currently accepts semantic text prompts. It explicitly reuses the
latest committed native user agent/model/variant from bounded history and labels
that selection basis. It does not claim the current picker or draft. Native 204
alone is uncertain: admission requires a newly observed exact message ID,
matching text and tuple. A call whose promise ignores abort keeps the mutation
lane occupied until it settles. The host must retain uncertain custody records
and must not blindly replay them. Permission/question mutations, queue/steer,
and Stop with an expected turn are outside this vertical.

## Verification

`node --test internal/opencodebridge/tui.test.mjs` uses the standard Node test
runner, a mock public native API, and real private Unix sockets. It tests binding
custody, route drift, host disconnect, native read failures, admission evidence,
prompt forwarding, bounded event queues, and request ancestry. These tests do
not call a provider. `go test -race ./internal/opencodebridge` verifies the host
client and stream cancellation. Actual native TUI qualification is a separate
integration gate; mocked admission does not establish provider execution.
