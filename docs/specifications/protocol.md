# Client ⇄ Daemon Protocol (Epic 6)

This is the normative, field-level specification of the **client ⇄ daemon control
surface** — the low-reversibility wire contract (ADR-002) that the TUI (Epic 7)
and the attach path (Epic 8) consume. It is versioned; CI diffs this document's
field set against the Go message structs in `internal/protocol` (the GG-7 drift
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

The rows below `error` are the **remote-tier additive fields** (R-PROT.2/.3/.7,
amendments D.0-A1/A3/A6/A11): every one is `omitempty`, so a control message that
uses none of them serializes byte-identically to the pre-remote shape. The nested
`ApproveReq` (approval), `JournalRecord` (journal event), `DeviceView` (paired
device), `PolicyView` (launch policy), and `PairingControl` (pairing payload)
shapes are documented at the field level in `internal/protocol` and are not
repeated as wire tables here.

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
| `cwd`           | string          | the session's working directory                               |
| `status`        | `status.Status` | the three raw dimensions (process, turn, interaction)         |
| `group`         | `status.Group`  | the daemon-computed display group (E6.9)                      |
| `last_activity` | time            | timestamp of the session's last activity                      |
| `created_at`    | time            | session creation timestamp                                    |
| `summary`       | string          | V-4 one-line last-output summary                              |

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
| `cwd`            | string              | working directory (must exist and be a directory)          |
| `options`        | map[string]string   | declarative adapter options (each value length-capped)     |
| `env`            | []string            | `KEY=VALUE` launch env (allowlist-filtered server-side)    |
| `cols`           | int                 | initial terminal columns                                   |
| `rows`           | int                 | initial terminal rows                                      |
| `initial_prompt` | string              | optional initial prompt text                               |
| `worktree`       | bool                | opt into launch-time git-worktree isolation (Epic 12)      |

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
  the same `attach` path (replying with `lease`). It carries an optional `ttl_seconds`
  (the requested control-session lifetime, clamped to a server maximum and defaulted
  when absent). While that control session is live, remote `TDataIn` input frames and
  `resize` reach the session's shim; they are gated on every keystroke by four
  conditions — the kill switch is still on, the control session exists, it has not
  expired (lazy, on the server clock), and it still targets the connection's current
  lease generation — and dropped otherwise.
- **`take_control_end`** is the caller-scoped teardown of one's OWN control session:
  it carries the `session_id` and lease `generation` (mirroring `detach`; no device
  signature), clears the control session, and releases the lease — shutting the input
  gate.

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
subscribers (S9). A live status change reaches a healthy subscriber within one
second (L1).

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
