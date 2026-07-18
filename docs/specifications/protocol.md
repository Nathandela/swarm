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
