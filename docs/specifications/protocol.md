# Client ⇄ Daemon Protocol (Epic 6)

This is the normative, field-level specification of the **client ⇄ daemon control
surface** — the low-reversibility wire contract (ADR-002) that the TUI (Epic 7)
and the attach path (Epic 8) consume. It is versioned; CI diffs this document's
field set against the Go message structs in `internal/protocol/schema` (the GG-7 drift
check), so this file and the code move together.

Implementation: `internal/protocol` (`types.go`, `codec.go`, `client.go`,
`server.go`, `fromdaemon.go`).

## Protocol version

The protocol **version** is `1` (`protocol.Version`). The version is exchanged in
the hello handshake. A client and daemon that disagree on the version are
incompatible: the client's `Dial` fails with `ErrIncompatibleVersion`, whose
message names `swarm daemon restart` and states that the restart is safe and
loses no live sessions (D-8).

## Framing

Every message rides inside the shared G1 frame envelope (`internal/wire`): a
4-byte big-endian length, a 1-byte type tag, then the payload. The same envelope
carries the client socket and the daemon⇄shim socket (G1). Four frame types are
used:

| Frame type    | Direction        | Payload                                             |
| ------------- | ---------------- | --------------------------------------------------- |
| `TControl`    | both             | a JSON-encoded `Control` message (see below)        |
| `TSnapshot`   | daemon → client  | opaque snapshot bytes, one or more per attach (S10) |
| `TDataOut`    | daemon → client  | opaque live terminal-output bytes                   |
| `TDataIn`     | client → daemon  | opaque terminal-input bytes (controller only)       |

Control frames carry JSON; the three data-plane frame types carry opaque binary
and are never JSON-decoded. The planes demux purely by frame type. A control
payload larger than the envelope cap (`wire.MaxFrame`) is rejected before any
allocation. A malformed control payload is answered with an `error` op — the
server never crashes on bad input.

### Snapshot chunking (ADR-002 amendment)

A full grid snapshot can exceed `wire.MaxFrame` (with `maxDim = 1000`, a styled
snapshot is far over 1 MiB). The snapshot is therefore delivered as a **sequence
of one or more `TSnapshot` frames** carrying raw, ordered chunk bytes. The `lease`
control that precedes them carries `snapshot_len`, the snapshot's total byte
length; the client concatenates `TSnapshot` payloads until it has that many bytes,
and only then does it have the whole snapshot (which `Attachment.Snapshot()`
returns). A snapshot that fits in one frame is sent as a single raw `TSnapshot`
frame (the common case), so the ordering guarantee is unchanged: the `lease`, then
the snapshot (as chunks), then the live `TDataOut` stream, with no interleaving.

## The `Control` message

`Control` is the single JSON envelope for every control-plane message. Keys are
`snake_case`. Which fields are meaningful depends on `op`. Every message carries
`endpoint_id`; a session-scoped op additionally carries a namespaced `session_id`.

| JSON key           | Go type         | Meaning                                                                   |
| ------------------ | --------------- | ------------------------------------------------------------------------- |
| `op`               | string          | the operation (see the op vocabulary below); always present              |
| `endpoint_id`      | string          | the connection's endpoint id (F-1); always present after the handshake    |
| `session_id`       | string          | namespaced session id `<endpoint_id>/<local>` for session-scoped ops      |
| `protocol_version` | int             | protocol version, carried on `hello`                                      |
| `build_version`    | string          | build version (`internal/version.Version`), carried on `hello` (E13.2)   |
| `capabilities`     | []string        | offered (client) / negotiated (daemon) capabilities, carried on `hello`   |
| `generation`       | uint64          | controller lease generation, carried on `lease` and `resize`             |
| `snapshot_len`     | int             | total snapshot byte length, carried on `lease` for chunk reassembly       |
| `cols`             | int             | terminal columns, carried on `resize` (and inside `launch`)               |
| `rows`             | int             | terminal rows, carried on `resize` (and inside `launch`)                  |
| `name`             | string          | new session label, carried on `rename`; sanitized + length-capped server-side (P2) |
| `launch`           | `*LaunchReq`    | the launch request, carried on `launch`                                   |
| `sessions`         | `[]SessionView` | the session roster, carried on the `list` reply                           |
| `session`          | `*SessionView`  | one session view, carried on the `launch` reply and on `event`            |
| `error`            | string          | human-readable error text, carried on `error`                            |
| `operation_id`     | string            | idempotency key of a remote mutating op (`<device_id>:<client-ULID>`, R-IDP) |
| `interaction_id`   | string            | the agent interaction being approved, distinct from `operation_id` (A6)      |
| `device_id`        | string            | pairing device id; never trusted alone, always paired with `device_sig` (A1) |
| `device_sig`       | string            | detached Ed25519 signature over the canonical op tuple (D4)                  |
| `cursor`           | uint64            | journal cursor, carried on `journal_read` / `journal_event` (R-PROT.3)       |
| `issued_at`        | time              | daemon-authoritative issue time (pointer; the key is omitted when zero)      |
| `expires_at`       | time              | daemon-authoritative expiry (pointer; the key is omitted when zero)          |
| `approve`          | `*ApproveReq`     | remote approval of an agent interaction (A6)                                 |
| `error_code`       | `ErrorCode`       | machine-readable refusal reason, carried alongside `error` (R-PROT.7)        |
| `journal`          | `[]JournalRecord` | journal records, carried on `journal_read` / `journal_event` (R-PROT.3)      |
| `roster`           | `[]JournalRecord` | live sessions as-of `cursor` on a `journal_read` snapshot (R-JRN.4)          |
| `full_resync`      | bool              | set when the caller's `cursor` fell below the retained journal floor (R-JRN.6) |
| `devices`          | `[]DeviceView`    | paired-device roster, carried on the `device_list` reply (R-DEV.1)           |
| `policy`           | `*PolicyView`     | remote launch policy (allowed cwd roots), carried on the `policy_query` reply (R-POL.3) |
| `target_device_id` | string            | device_revoke: the device to REVOKE, distinct from the caller `device_id` (A3.2) |
| `pairing`          | `*PairingControl` | owner-tier pairing payload, carried on `pair_start`/`pair_pending`/`pair_confirm`/`pair_result` (A3.3-a) |
| `ttl_seconds`      | int               | `take_control`: caller-requested control-session lifetime (seconds), clamped server-side (A5-b) |
| `gate_token`       | string            | `take_control`: one-shot gate token bound into the device signature via `content_hash` and made single-use (A5-c) |
| `remote_control`   | `*bool`           | `remote_set_control`: the DESIRED remote-control master state (true=on, false=manual off), owner-tier only (A4) |
| `terminal`         | `*TerminalSnapshot` | server-rendered terminal snapshot, carried on `terminal_snapshot` (A7 slice B) |
| `send_input`       | `*SendInputReq`     | `send_input`: one owner-tier steering message for `session_id`, owner-tier only (ADR-010 A2) |

The rows below `error` are the **remote-tier additive fields** (R-PROT.2/.3/.7,
amendments D.0-A1/A3/A6/A11): every one is `omitempty`, so a control message that
uses none of them serializes byte-identically to the pre-remote shape. The nested
`ApproveReq` (approval), `DeviceView` (paired device), `PolicyView` (launch
policy), and `PairingControl` (pairing payload) shapes are documented at the field
level in `internal/protocol` and are not repeated as wire tables here.
`JournalRecord` is the exception, below: it is the phone's whole view of the
machine, roster and transcript alike. `send_input`
(`ADR-010-inter-session-orchestration.md` A2 — cited by filename because two ADRs
carry the number 010, see `docs/adr/README.md`) is the one **owner-tier** addition
in that block and follows the same rule: `omitempty`, and its `SendInputReq` payload
is described in its op section below rather than as a second wire table.

## The `JournalRecord` message

`JournalRecord` is one wire-facing journal event, carried in `Control.journal`
(`journal_read` / `journal_event`) and in `Control.roster` (the snapshot half of a
`journal_read`, R-JRN.4). It mirrors the daemon journal's record fields the phone
needs; the daemon-internal payload is not carried, with the single exception of
`item`.

| Field        | Go type           | Meaning                                                                 |
| ------------ | ----------------- | ----------------------------------------------------------------------- |
| `cursor`     | uint64            | the record's monotonic journal cursor; ordering is this and nothing else. Deliberately unset (`0`) on a roster record, which is a set member and not a point in the stream (PB-SYNC-8) |
| `session_id` | string            | namespaced session id the record is about; absent on a session-neutral record (`presence`) |
| `type`       | string            | `group_transition` \| `launched` \| `exited` \| `lost` \| `deleted` \| `presence` \| `roster` \| `interaction` |
| `group`      | `status.Group`    | the server-derived display group; carried on `group_transition` and on a roster record, absent elsewhere |
| `agent`      | string            | the session's agent identity (`claude`, `codex`, …). Its ABSENCE IS MEANINGFUL: a record with no agent carries none, and `""` is never an agent by that name |
| `item`       | `json.RawMessage` | the interaction item object, carried ONLY when `type` is `interaction` — one unit of the phone's chat transcript (ADR-009, `interaction-schema.md` §1/§2, IS-LAYER-1). Opaque on the wire: the gateway forwards it and parses no item (§10), and the item's own `kind` discriminator stays inside it (IS-LAYER-2) |

Every field but `cursor`, `session_id` and `type` is `omitempty`, so a record type
that predates one of them serializes byte-identically to what earlier builds wrote.

This table's header column reads **`Field`**, not `JSON key`, and that is
deliberate rather than a style slip: GG-7's bidirectional drift check
(`internal/protocol/protocolmd_bidi_test.go`) collects the rows of every table
headed `JSON key` and asserts set equality against the json tags of the four
reflected wire types — `Control`, `SessionView`, `LaunchReq` and
`TerminalSnapshot`. `JournalRecord` is not one of them (`interaction-schema.md` §1
says so and relies on it), so rows like `type` and `item` — real fields of a type
the check does not reflect — would fail its reverse direction. Keeping this table
in step with `internal/protocol/schema.JournalRecord` is therefore a **procedural**
obligation, carried by those fields' Go doc comments; changing the header would not
extend the check to this type, only break it.

## The `SessionView` message

`SessionView` is one general-view row (V-4), stamped for the receiving client. The
status **group** is precomputed daemon-side via `status.Derive` (E6.9); the client
displays it and never re-derives it. The three raw status dimensions travel
alongside the group.

| JSON key        | Go type         | Meaning                                                       |
| --------------- | --------------- | ------------------------------------------------------------- |
| `endpoint_id`   | string          | the receiving connection's endpoint id                        |
| `id`            | string          | namespaced session id `<endpoint_id>/<local>`                 |
| `agent`         | string          | agent type (e.g. `claude`, `codex`)                           |
| `name`          | string          | user-provided session label; empty/absent falls back to `agent` (P2) |
| `cwd`           | string          | the session's working directory                               |
| `status`        | `status.Status` | the three raw dimensions (process, turn, interaction)         |
| `group`         | `status.Group`  | the daemon-computed display group (E6.9)                      |
| `last_activity` | time            | timestamp of the session's last activity                      |
| `created_at`    | time            | session creation timestamp                                    |
| `summary`       | string          | V-4 one-line last-output summary                              |
| `spawned_from`  | string          | local id of the session that spawned this one; absent when none (ADR-010 D4) |
| `spawn_intent`  | string          | how the spawn was meant: `handoff` or `delegate`; absent when none |
| `remote_controlled` | bool        | a paired device currently holds this session's controller lease (R1.3.7); absent when false |

## The `LaunchReq` message

`LaunchReq` is a client's request to launch a session, carried in `Control.launch`.
Every field is **re-validated server-side** (E6.6) before it reaches the daemon:
the agent name must be non-empty and bounded; `cwd` must be an existing directory
(L-3); each option value is length-capped; `cols`/`rows` must be in range; and the
`env` is passed through the launch-environment allowlist (S-6) so injection vectors
and unrelated secrets are dropped.

| JSON key         | Go type             | Meaning                                                    |
| ---------------- | ------------------- | ---------------------------------------------------------- |
| `agent`          | string              | agent type to launch                                       |
| `name`           | string              | optional session label; sanitized + length-capped server-side (P2) |
| `cwd`            | string              | working directory (must exist and be a directory)          |
| `options`        | map[string]string   | declarative adapter options (each value length-capped)     |
| `env`            | []string            | `KEY=VALUE` launch env (allowlist-filtered server-side)    |
| `cols`           | int                 | initial terminal columns                                   |
| `rows`           | int                 | initial terminal rows                                      |
| `initial_prompt` | string              | optional initial prompt text                               |
| `worktree`       | bool                | opt into launch-time git-worktree isolation (Epic 12)      |
| `spawned_from`   | string              | optional local id of the spawning session, carried verbatim into meta (ADR-010 D4) |
| `spawn_intent`   | string              | optional spawn intent, one of `handoff` or `delegate`; refused without a `spawned_from` |

## The `TerminalSnapshot` message

`TerminalSnapshot` is one **server-rendered, sanitized terminal snapshot** (A7
renderer slice B), carried in `Control.terminal` on a `terminal_snapshot` op. The
daemon renders the session's VT grid to plain text — every control byte already
stripped — so only sanitized text crosses the daemon → gateway socket and the raw
hostile PTY bytes never reach the network-facing sidecar. The phone displays
`lines` as-is.

| JSON key    | Go type    | Meaning                                            |
| ----------- | ---------- | -------------------------------------------------- |
| `session`   | string     | namespaced session id the snapshot is for          |
| `lines`     | []string   | sanitized plain-text grid rows, top to bottom      |
| `cols`      | int        | grid width the snapshot was rendered at            |
| `rows`      | int        | grid height the snapshot was rendered at           |

## Control-op vocabulary

All op values are lowercase snake_case strings.

### `hello`

Handshake. The client sends `hello` with `protocol_version`, its own
`build_version`, and its offered `capabilities`. The daemon replies with `hello`
carrying the assigned unique `endpoint_id`, its `protocol_version`, its own
`build_version`, and the negotiated `capabilities` (the intersection of the
client's offer and the daemon's support). On a `protocol_version` mismatch the
daemon replies with `error` naming `swarm daemon restart` (D-8). `build_version`
is ADDITIVE and never fatal to the handshake: a client whose `build_version`
differs from the daemon's (e.g. the daemon is still running an older build
after an upgrade) can surface that and suggest `swarm daemon restart` even when
`protocol_version` still matches (E13.2).

### `list`

The client sends `list`. The daemon replies with `list` carrying `sessions`, one
stamped `SessionView` per session, each with its precomputed `group`.

### `device_list`

Remote-tier control-plane read (slice A3.1, R-DEV.1). The client sends
`device_list`; the daemon replies with `device_list` carrying `devices`, the
paired-device roster. Non-mutating: gated purely by the negotiated `pairing`
capability and a `DeviceLister` backend (no `requireRemoteAuthz` choke point). An
unnegotiated capability or an unsupporting backend replies `error`.

### `policy_query`

Remote-tier control-plane read (slice A3.1, R-POL.3). The client sends
`policy_query`; the daemon replies with `policy_query` carrying `policy`, the
machine's configured remote launch policy (allowed cwd roots). Non-mutating:
gated purely by the negotiated `policy` capability and a `PolicyDescriber`
backend (no `requireRemoteAuthz` choke point). An unnegotiated capability or an
unsupporting backend replies `error`.

### `device_revoke`

Remote-tier control-plane MUTATING op (slice A3.2): removes a paired device from
the daemon's device registry. The client sends `device_revoke` with
`target_device_id` (the device to remove), plus the usual mutating-op device-auth
fields (`operation_id`, `device_id`, `device_sig`, `expires_at`) — `device_id` here
is the CALLER (the signer), and `target_device_id` is the resource: it is what
`requireRemoteAuthz` binds the caller's signature to, so a device can revoke a
*different* device, not only itself. Goes through the same `requireRemoteAuthz`
choke point as `kill`/`delete` (kill switch, `operation_id`, device signature,
capability — `device_revoke` maps to the `ActionControl` capability class, so it
requires a CapFull device). The daemon replies `ok` (or `error`). Revoking the
last paired device is not a distinct code path: `RemoteControlEnabled` already
derives from the registry's device count, so it flips remote control off as a side
effect. Known gaps (future slices): this only removes the daemon-side registry
entry, not any relay-side registration/mailbox; and there is no separate admin
capability tier yet — any CapFull device can revoke any other.

### `remote_set_control`

Owner-tier MUTATING op (A4) — the durable manual kill switch behind `swarm remote
off`/`on`. The client sends `remote_set_control` with `remote_control` (the desired
master state: `false` = manual off, `true` = on). It is **owner-tier only**: a
remote-tier connection is refused `not_authorized` BEFORE the backend is consulted
(mirroring `pair_start`), so a remote device can never re-enable a switch its owner
turned off. On the owner tier it requires the negotiated `pairing` capability, then
durably flips the override via `RemoteControlSetter.SetRemoteControl` (persisted to
`remote-state.json`), and replies `ok` (or `error`). Manual off **wins over device
presence**: with the override set, `RemoteControlEnabled` reports false even while a
device is paired, so `off` severs remote control at the daemon choke point —
`requireRemoteAuthz` refuses every remote mutating op, `controlGateOpen` drops live
input, and an established terminal peek is blanked. `on` clears the override,
returning to the device-derived value.

### `pair_start` / `pair_pending` / `pair_confirm` / `pair_result`

Owner-tier pairing ops (slice A3.3-a, ADR-007 amendment "Pairing host: Option A").
This slice freezes the wire shape only — the four op names and the `pairing`
field's `PairingControl` payload — with no handlers and no pairing logic wired up
yet; a later slice adds the handlers and the `PairingHost` bridge against this
frozen contract.

### `launch`

The client sends `launch` with a `launch` request. After server-side revalidation
the daemon launches the session and replies with `launch` carrying the new
`session` view (whose `id` is the namespaced session id). On a rejected field the
daemon replies with `error` and forwards nothing.

### `kill`

The client sends `kill` with a `session_id`. The daemon terminates the session's
process group and replies with `ok` (or `error`).

### `delete`

The client sends `delete` with a `session_id`. The daemon removes the session
(killing it first if running) and replies with `ok` (or `error`).

### `rename`

The client sends `rename` with a `session_id` and the new `name`. The daemon
**re-validates** the label server-side (the same sanitizer `launch` uses — control
characters stripped, capped to the label rune limit), updates the session meta,
persists it, and broadcasts a roster `event` so every client converges; it replies
with `ok` (or `error`). A label is cosmetic, so a hostile or over-long value is
sanitized rather than rejected. An **older daemon** that predates this op replies
with `error` ("unknown op"), which the client surfaces (skew-safe) rather than
crashing.

### `attach`

The client sends `attach` with a `session_id`. The daemon grants the exclusive
controller lease, replying with `lease` (carrying the new `generation` and
`snapshot_len`), then the snapshot as one or more `TSnapshot` chunk frames, then
the live `TDataOut` stream (S10). A second concurrent attach **supersedes** the
first: it wins a strictly higher `generation` and **re-attaches** — it releases
the prior controller and its upstream connection and opens a **fresh** connection
to the session's shim, whose atomic snapshot-then-stream gives the new controller
the shim's CURRENT grid (no daemon-side re-snapshot of a live stream). The prior
controller's live stream ends (its frames channel closes) — see `detach`. A slow
or wedged controller is evicted within a bound so a supersede/detach never blocks
on it (S9); a supersede whose fresh attach fails is a clean error, never a stale
screen.

A second `attach` on the **same connection** auto-detaches the first (its lease is
released) before the new lease is granted, so one connection never holds two
leases.

### `take_control` / `take_control_end`

Remote-tier interactive control (slice A5). The owner tier uses `attach`; the remote
tier has no unsigned `attach` and instead requires a signed `take_control`.

- **`take_control`** is a signed, MUTATING op that runs through the same
  `requireRemoteAuthz` choke point as `kill`/`delete` (kill switch first, then
  `operation_id`, then the `device_id`/`device_sig`/`expires_at` signature over the
  canonical tuple) and, only once authorized, establishes a controller lease through
  the same `attach` path (replying with `lease`). The control-session lifetime binds to
  the EARLIEST of the device-signed `expires_at`, `now + server-max` (30 min), and — when
  the caller sets the optional `ttl_seconds` hint — `now + ttl_seconds`. Because
  `expires_at` is covered by the device signature, the session can never outlive what the
  device signed; `ttl_seconds` (unsigned) can only shorten it further (R7). While that
  control session is live, remote `TDataIn` input frames and
  `resize` reach the session's shim; they are gated on every keystroke by four
  conditions — the kill switch is still on, the control session exists, it has not
  expired (lazy, on the server clock), and it still targets the connection's current
  lease generation — and dropped otherwise.

  **Input routing + best-effort delivery (A7).** Each sealed input frame carries its
  target namespaced `session` INSIDE the AEAD-encrypted body, and the gateway routes it
  by that sealed id — never by mutable focus state — so the untrusted relay cannot drop a
  `take_control` and steer the following keystrokes onto another session's live lease.
  The phone stamps commands AND input frames from ONE monotonic sequence (they share a
  single machine `MailboxReceiver`), and the gateway opens each with EXACTLY ONE
  `Accept`; a replayed/reordered/duplicate frame is rejected as a stale sequence, and a
  frame that follows a sequence GAP (a preceding frame the relay dropped or reordered) is
  DROPPED, not routed. The input plane is therefore **best-effort under relay
  misbehavior**: an in-order relay never sets a gap, but a dropped/reordered frame may
  cost a keystroke — fail-closed, since dropping a keystroke is strictly safer than
  misrouting it.
- **`take_control_end`** is the caller-scoped teardown of one's OWN control session:
  it carries the `session_id` and lease `generation` (mirroring `detach`; no device
  signature), clears the control session, and releases the lease — shutting the input
  gate.

### `approve`

The phone's answer to one pending `approval_request` (interaction-schema.md IS-LIFE-4).
It is a signed, MUTATING remote-tier op and rides `requireRemoteAuthz` exactly like
`kill`/`device_revoke`/`take_control` (ADR-007 D4: no remote-class mutating op executes
without a valid device signature). Its capability class is `ActionApprove`, so a
`read_approve` device may answer a card without being able to control anything.

The body rides in `Control.approve` (`ApproveReq`: `session`, `agent_instance`,
`interaction_id`, `content_hash`, `expires_at`, `decision`), reconstructed by the gateway
from the sealed `RemoteCommand.approve` the phone appended. Three rules make it what it is:

- **The signed tuple's `content_hash` IS the interaction's own** — ADR-007 D7 spends the
  one content slot on the interaction content, and the phone echoes `content_hash` and
  `expires_at` **verbatim** (IS-APR-2). The daemon decodes the hash from the wire body and
  authorizes against it, so a relay or gateway that swaps it breaks the signature rather
  than redirecting the approval. A `content_hash` that is not 32 bytes of hex is refused
  `invalid_field`, and `approve.session` must equal `session_id` or the frame is refused
  before authorization.
- **The `decision` is deliberately UNSIGNED** (IS-LIFE-4). It rides inside the
  epoch-sealed frame — unforgeable by the relay, alterable only by the gateway, which is
  the documented D4/D5 owner-uid residual and not a new one.
- **The daemon validates before any effect** and never translates an approve into a blind
  keystroke (ADR-007 D7). The stored binding tuple (agent instance, content hash,
  daemon-authoritative expiry) and the offered decision set are checked; a mismatch is
  `stale_approval`, a decision the request never offered is `invalid_field`, and the card
  is left pending in that case. On success exactly one `approval_resolved` is journalled
  (IS-LIFE-2), attributed `by: phone` and echoing the phone's `operation_id`.

`operation_id` is the phone-minted idempotency key of the OP and is never equal to
`interaction_id`, which names the interaction (IS-APR-1). No replay dedup is needed: a
re-delivered approve finds the approval already resolved and is refused `stale_approval`.

### `terminal_subscribe` / `terminal_snapshot`

Terminal peek (A7 renderer slice B), mirroring the
`journal_subscribe`/`journal_event` streaming pair. Unlike `take_control`, the peek
is **read-only** and works BEFORE any control session exists (no lease, no signed
op).

**Authorization is per tier** (ADR-010 A3). A **remote-tier** peek keeps its full
gate: the remote-control kill switch (checked at subscribe time, on every render tick
and before every emission — `swarm remote off` blanks an established peek), plus the
negotiated `remote-gateway` capability. An **owner-tier** connection on the main
socket needs neither: ADR-004's v1 trust model already grants any same-user process on
that socket full daemon power, and the switch is the remote tier's master override, so
it must never blank the owner's own view of the owner's own machine. This is a
relaxation of authorization ONLY — the render path, the read-only tap and the
sanitization are identical on both tiers, and a backend that implements no terminal tap
still refuses on both.

- **`terminal_subscribe`** requests the server-rendered terminal snapshot stream for
  a `session_id`. The daemon renders that session's VT grid off a persistent
  read-only fan-out tap and streams `terminal_snapshot` frames as the grid changes.
- **`terminal_snapshot`** is daemon → client. It carries a `terminal`
  (`TerminalSnapshot`): the namespaced `session`, the sanitized plain-text `lines`
  (every control byte stripped daemon-side), and the `cols`/`rows` the grid was
  rendered at. The VT emulator and the raw hostile PTY bytes stay off the
  network-facing sidecar — only this sanitized text crosses the daemon → gateway
  socket.

### `send_input`

**Owner-tier only** one-shot steering write (ADR-010 Amendment 1 A2), behind
`swarm send`. The daemon writes ONE message into `session_id`'s shim through the same
input funnel every lease write uses. It never takes, bumps or supersedes the attach
lease — that is why it is a distinct op rather than a local control session, which
would kick an attached human off mid-keystroke. It is **refused `not_authorized` on the
remote tier**, before the session is resolved and before any authorization is consulted
(mirroring `attach`): the remote tier keeps its own signed `take_control` lane.

The `send_input` payload (`SendInputReq`) carries `text`, `submit` and `key`, and names
**exactly one mode** per request:

- **text mode** — `text` is the message. With `submit` (the default of `swarm send`) the
  daemon appends the CR that runs it; `--no-submit` leaves the text sitting in the
  session's input box. `text` is capped at 4096 bytes, the bound the input path already
  imposes on a single PTY write.
- **key mode** — `key` is ONE name from the closed vocabulary `enter` (CR), `esc`
  (`0x1b`), `ctrl-c` (`0x03`), `tab` (`0x09`), `up` (`ESC [ A`) and `down` (`ESC [ B`).
  An unknown name is refused, never guessed at.

Both set, neither set, an unknown key, or over-long text are refused `invalid_field`
with nothing written. A session that is not running is refused with a plain error
carrying **no** `error_code`: the closed code vocabulary has no fit for "the target
cannot receive input", and nothing is written either way.

**Framing and the gap are the daemon's job** (`internal/submitframe`, bead
`agents-tracker-r3p`). A text message is at most TWO PTY writes: the text verbatim in
one write — embedded newlines are content, which the CLI's paste heuristic renders as a
multi-line draft — then, with `submit`, the CR that runs it in a write of its own, so a
PTY write never mixes the message with the byte that submits it (which Claude Code's TUI
reads as an unsubmitted paste). ~150 ms must elapse between those two writes, and the
**daemon sleeps it** while holding the session's input serialization: the shim must
never sleep, because its PTY writer lock is shared with the VT emulator's DSR/CPR reply
pump. A message sleeps at most once, so that hold is bounded by the gap plus two writes
however long or newline-heavy the text is.

That serialization is held across both frames, so the message is atomic against
**owner-tier** lease input — a concurrent owner-tier controller's keystrokes land wholly
before the message or wholly after it, never between the text and its CR (invariant S2).
The scope is deliberate: the owner-tier and remote-tier servers are distinct values with
distinct per-session serializations over one shared tap, so a remote `take_control`
controller CAN interleave. That is accepted for the personal single-owner model — a
remote take-control is the human deliberately grabbing the session (ADR-010 A2).

If the CR write fails after the text has landed, the refusal says so distinctly ("text
delivered, submit not sent"): the message is half-delivered, and `swarm peek` plus
`swarm send --key enter` recovers it. A bare refusal would be indistinguishable from one
that wrote nothing.

### `detach`

Two directions share this op:
- **client → daemon**: the controller sends `detach` with `session_id` and its
  `generation` to release the lease; the daemon validates the `generation` against
  the current lease (a delayed old-generation detach is ignored, so it cannot
  release a lease held by a later controller), then stops the stream and closes the
  single upstream pipe (1→0, L3).
- **daemon → client**: the daemon sends `detach` to a controller whose lease has
  ended (supersede or orderly release), signalling that its live frame stream is
  closing.

### `resize`

The controller sends `resize` with `session_id`, its `generation`, and `cols`/
`rows`. The daemon honors it only under the current generation and only when the
dimensions are in range; a stale or out-of-range resize is dropped server-side
(S2/P-5/P-6). Input authority is likewise bound to the connection's generation:
`TDataIn` frames carry no per-frame generation, and a superseded connection's
input is dropped.

### `subscribe`

The client sends `subscribe`. The daemon replies with `ok`, then streams `event`
messages as session status changes. A subscriber that stops reading is
disconnected within a bound; it never blocks the daemon's event loop or other
subscribers (S9). Events are latest-state snapshots: consecutive changes may
coalesce, and after any change the latest committed state reaches a healthy
subscriber within one second (L1, ADR-008).

### `event`

Daemon → client. Carries a `session` view (stamped for the receiving endpoint,
with the precomputed `group`) describing a session whose status just changed.

### `lease`

Daemon → client. The reply to `attach`, carrying the granted `generation` for the
controller lease. Generations are monotonic per session for the daemon's lifetime
and are never reused.

### `ok`

Daemon → client. A generic success acknowledgement (e.g. for `kill`, `delete`,
`subscribe`).

### `error`

Daemon → client. A failure reply carrying human-readable `error` text. Used for an
unknown op, a failed handshake, a rejected field, a foreign endpoint/namespace, or
any op the daemon refuses. Receiving it never tears down the connection: the
server survives to serve the next request.

## Namespacing (F-1 / F-2)

Every applicable message carries an `endpoint_id`, and every session-scoped op
carries a `session_id` namespaced as `<endpoint_id>/<local>`. A message addressed
to a foreign endpoint, or a session id whose namespace belongs to a different
endpoint, is rejected before the daemon is touched. No message field references a
transport-specific construct (a socket path, socket address, peer credential, or
file-descriptor handoff), so a future non-UDS transport can reuse these schemas
unchanged (F-2).
