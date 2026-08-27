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
| `tag`              | string          | manual grouping label, carried on `set_tag`; empty clears it              |
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
| `body_version`     | int                 | R1 refusal-ops (`session_launch`/`composer_send`/`operation_status`/`turn_interrupt`/`terminal_control_begin`/`terminal_control_end`): the profile version the phone bound this op to (`RemoteProfileV1.accepted_body_versions`); there is no version `0` (Wave R1 skeleton, playbook §6.3) |
| `session_launch`   | `*SessionLaunchReq` | Wave R5 `session_launch` body: the phone's confirmed machine-authored preset selection (`preset_id`, `preset_revision`, `initial_prompt`, cosmetic `cols`/`rows`), bound into the device signature via `SessionLaunchContentHash` (ADR-007 B144(b)) |
| `presets`          | `[]LaunchPresetView` | Wave R5 `launch_presets` reply: exactly the machine-authored preset list — empty custody answers empty, never an invented default (ADR-007 B135) |
| `preset_policy_revision` | string        | Wave R5 `launch_presets` reply: the revision of the preset policy that produced `presets`, the staleness coordinate operators correlate `stale_preset` refusals against |
| `subject_operation_id` | string          | Wave R5 `operation_status`: the operation being ASKED ABOUT — distinct from the query's own `operation_id` exactly as `interaction_id` is (ADR-007 D7) |
| `operation_outcome` | `*OperationOutcomeView` | Wave R5 `operation_status` reply: `applied` (authoritative, with the session id), `outcome_unknown` (honest undecidability), or `unknown_operation` (no record; never invented) |
| `device_capability` | string              | Wave R5 `launch_presets` reply (round-2): the SIGNING device's own registry-pinned tier (`full`/`read_only`/`read_approve`) — the phone's only honest wire source for its tier-denied launch state; empty when the backend has no capability seam (absent fact, never invented) |
| `composer_send`    | `*ComposerSendReq`  | Wave R6 `composer_send` body (Mirror M2.4, IS-LIFE-5): the phone's structured message under the wire name `composer_send`, carrying `session` / `expected_turn` / `text`, bound into the device signature via `ComposerSendContentHash` so a gateway cannot re-point a valid signature at different text or a different turn (ADR-009 (8)) |
| `turn_interrupt`   | `*TurnInterruptReq` | Wave R6 `turn_interrupt` body, added by the review fix-pack (finding B7): the semantic Stop's subject under the wire name `turn_interrupt`, carrying `session` / `expected_turn`, bound into the device signature via `TurnInterruptContentHash` so a gateway cannot re-point a valid Stop at a different turn. `expected_turn` is REQUIRED and non-empty — the op was BODYLESS when it landed, and a Stop with no turn coordinate typed the cancel sequence into whatever turn was current on arrival, including one the owner had just started at the terminal |
| `interaction_history` | `*InteractionHistoryReq` | Wave R6 `interaction_history` body (Mirror M3.1, ADR-014): the unsigned paged read under the wire name `interaction_history`, carrying `session` / `before_item` / `limit`; the reply rides the existing `journal` carrier, strictly older than `before_item`, ascending by cursor |
| `interaction_detail` | `*InteractionDetailReq` | Wave R6 `interaction_detail` body (Mirror M3.3, IS-CAP-2): the unsigned detail read under the wire name `interaction_detail`, carrying `session` / `item_id`; the reply is exactly one `journal` record whose `item` is the FULL pre-truncation body, or the sealed `unavailable` refusal outside retention (IS-CAP-3) |
| `history_floor`    | bool                | Wave R6 `interaction_history` reply: nothing older than the returned page is retained, so the phone renders a retention floor instead of offering "load earlier" forever (Mirror M3.1) |
| `terminal_control_begin` | `*TerminalControlBeginReq` | Wave R8 `terminal_control_begin` body (ADR-017 T6): the SIGNED request that mints one non-transferable control generation over a `terminal_fallback` session, under the wire name `terminal_control_begin`, carrying `session` / `session_instance` / `profile` / `expires_at`, bound into the device signature via `TerminalControlBeginContentHash` so a gateway cannot re-point a valid begin at another session, another incarnation or another profile. The session instance is REQUIRED: a generation that binds no incarnation authorises raw bytes into the PTY that replaced the one the user was reading |
| `terminal_input`   | `*TerminalInputReq` | Wave R8 `terminal_input` body (ADR-017 T6): ONE unsigned raw-input frame under the wire name `terminal_input`, carrying `session` / `session_instance` / `control_generation` / `bytes`. It rides the E2EE frame's authenticated sender and sequence plus the CONFIRMED generation rather than its own signature — the sole exception to full-body signatures, held to exactly two body types with `terminal_control_keepalive`. Every frame re-evaluates the kill switch, device registration, the session capability record and generation liveness (T6-e); a refused frame is DROPPED and never buffered (T6-f) |
| `terminal_view`    | `*TerminalViewV1`   | Wave R8 (ADR-017 T4/T4-a/T8-a): the VERSIONED terminal snapshot, carried under the wire name `terminal_view` on the SAME `terminal_snapshot` op as the legacy `terminal` body. Both ride one frame: a gateway that predates it ignores the key and reads `terminal` exactly as before, and one that understands it prefers this body. It was added by the closing round of Wave R8, which found the versioned fields being MINTED by the render loop and DISCARDED before the wire — so a phone watching a session replaced under the same id read the new incarnation as a seamless continuation of the old screen |
| `control_generation` | string            | Wave R8 (ADR-017 T6): the minted control generation. It rides the `terminal_control_begin` REPLY and every `terminal_control_keepalive`. It is NOT a control lease — a generation and a lease have different lifetimes, different ceremonies and different authority, and the input path never reaches the lease plane (OPEN-C4) |

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
| `type`       | string            | `group_transition` \| `launched` \| `exited` \| `lost` \| `deleted` \| `presence` \| `roster` \| `interaction` \| `structured_gap` |
| `group`      | `status.Group`    | the server-derived display group; carried on `group_transition` and on a roster record, absent elsewhere |
| `agent`      | string            | the session's agent identity (`claude`, `codex`, …). Its ABSENCE IS MEANINGFUL: a record with no agent carries none, and `""` is never an agent by that name |
| `item`       | `json.RawMessage` | the interaction item object, carried ONLY when `type` is `interaction` — one unit of the phone's chat transcript (ADR-009, `interaction-schema.md` §1/§2, IS-LAYER-1). Opaque on the wire: the gateway forwards it and parses no item (§10), and the item's own `kind` discriminator stays inside it (IS-LAYER-2) |

Every field but `cursor`, `session_id` and `type` is `omitempty`, so a record type
that predates one of them serializes byte-identically to what earlier builds wrote.

`structured_gap` is the daemon-authored capability-degrade event of ADR-017 T2 rule 2
(`internal/journal.TypeStructuredGap`, `internal/daemon.StructuredGapEvent{TS, Reason}`).
Its wire carriage on `JournalRecord` is not yet defined: emission is presently a stub
(`Daemon.EmitStructuredGap` returns `ErrStructuredGapUnimplemented`) pending the
spool-boundary detection that would trigger it, so no `structured_gap` record reaches the
wire yet. The type value is reserved here so a future emitting slice adds no new entry to
this vocabulary, only a carriage field.

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
| `tag`           | string          | user-assigned grouping label; empty/absent means untagged            |
| `cwd`           | string          | the session's working directory                               |
| `status`        | `status.Status` | the three raw dimensions (process, turn, interaction)         |
| `group`         | `status.Group`  | the daemon-computed display group (E6.9)                      |
| `group_entered_at` | time          | when the session entered `group`; used for newest-first ordering within that group |
| `last_activity` | time            | timestamp of the session's last activity                      |
| `created_at`    | time            | session creation timestamp                                    |
| `summary`       | string          | V-4 one-line last-output summary                              |
| `spawned_from`  | string          | local id of the session that spawned this one; absent when none (ADR-010 D4) |
| `spawn_intent`  | string          | how the spawn was meant: `handoff` or `delegate`; absent when none |
| `remote_controlled` | bool        | a paired device currently holds this session's controller lease (R1.3.7); absent when false |
| `remote_activity_at` | `*time.Time` | when a paired device last delivered a message to this session, carried only while that instant is inside the daemon's activity horizon; absent when no message is in the window. The board row's words -- `phone sent 09:41` -- are drawn from this instant, so the row states an event and never a presence claim (conversation surface, Wave G item G.2) |
| `supervision`   | string          | the persisted supervision mode of a handoff child: `passive`, `manual` or `none` (ADR-010 Amendment 3 C1); absent when none |
| `supervision_pending` | bool      | an attention event of this handoff child awaits its source; live supervisor state, sampled like `remote_controlled` (ADR-010 Amendment 3 C5); absent when false |
| `capabilities`  | `*SessionCapabilities` | daemon-authored per-session capability record (ADR-017 T2), absent on an older daemon or a session not yet stamped -- see "The `SessionCapabilities` record" below. Shares its wire name with `Control.capabilities` (the hello negotiated-capability list); the two are unrelated fields of unrelated messages that happen to share a name, and the GG-7 drift check treats the key as documented once it has one row |

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
| `supervision`    | string              | optional supervision mode, one of `passive`, `manual` or `none` (ADR-010 Amendment 3 C1); refused unless `spawn_intent` is `handoff` |

Two option keys are reserved for resume orchestration. `resume_from` names an
ended/lost swarm source session and creates a new row linked through
`Meta.ResumedFrom`. `resume_conversation_id` adopts a provider-native conversation
that is not yet represented in swarm. The latter is accepted only on the owner
tier when `external-resume` was negotiated during `hello`; it is refused with
`capability_refused` otherwise and with `policy` on the remote tier. The assembly
validates the canonical identity, composes the adapter's resume argv, persists the
identity with the launch, and reuses an existing row with the same provider and
identity. The two resume keys are mutually exclusive.

A third option key is reserved for the hands-off handoff (ADR-010 Amendment 4).
`handoff_from` carries the NAMESPACED id of the SOURCE session whose conversation
the new session is told to go and read; the source is never signalled, stopped or
asked to cooperate. It is accepted only on the owner tier when `hands-off-handoff`
was negotiated during `hello`; it is refused with `capability_refused` otherwise
and with `policy` on the remote tier, in both cases with no daemon side effect.
`handoff_from`, `resume_from` and `resume_conversation_id` are three different
answers to where a session comes from, so all three are mutually exclusive;
pairing `handoff_from` with either resume key is refused `invalid_field` naming
both keys, and the resume pair's own exclusion is the one stated above. Unlike
the two resume keys, `handoff_from` distinguishes PRESENT-BUT-EMPTY from ABSENT
and fails closed on the former: an empty value is refused `invalid_field`, while
a key that was never set is an ordinary launch requiring no capability. ADR-010
Amendment 4 E7 is the reason -- no refusal in this flow may degrade to a bare,
context-free launch, and reading an empty source id as "absent" would reach one
past the capability gate. The assembly
resolves the source, its conversation identity and its transcript path, and
composes the successor's prompt daemon-side; nothing about the handoff reaches
`SessionView` or any other roster, list or event message.

> AMENDED BY ADR-007 B144 (2026-08-15): `LaunchReq` above is the owner-tier form's request — free
> `cwd`, `options`, `env`. B144's preset model arrives with the R1/R5 skeleton as a **separate**
> remote-tier op, `session_launch(machine, operation_id, profile, preset_id, preset_revision,
> initial_prompt?, expires_at)`: on the remote tier a preset id, resolved daemon-side against a
> signed preset revision, replaces free `cwd`/`options`/`env` — never argv or environment supplied
> by the phone. This paragraph is a pointer, not a field addition; `session_launch`'s
> refusal-only skeleton (its op name, the shared mutating-op device-auth fields, and
> `body_version`) is documented below under "Control-op vocabulary"; the preset-body table
> (`profile`, `preset_id`, `preset_revision`, `initial_prompt`, `expires_at`) lands with the
> commit that implements the real handler (GG-7).

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

## The `TerminalViewV1` message

`TerminalViewV1` is ADR-017 T4's read path: one **full coalesced snapshot** of a
`terminal_fallback` session's sanitized screen, carried in `Control.terminal_view` on a
`terminal_snapshot` op beside the legacy `TerminalSnapshot` body. There is no patch
language and no delta — a slow observer drops superseded snapshots and receives the newest
COMPLETE revision, which is what the gateway's coalescer already does and what T4 makes a
wire contract. A watch grants NO INPUT AUTHORITY; nothing in this body carries or implies
one.

**Why both `view_epoch` and `revision` (amendment T4-a).** The daemon's render loop is PER
INVOCATION and the gateway's watcher re-runs it after every transport hiccup with a fresh
emulator. A bare revision restarted at 1 while the phone holds revision N makes the phone's
only sane rule — "drop anything not strictly greater" — discard every snapshot of the second
run, with no error on either side: the user reads a plausible, frozen screen. So the epoch is
minted per render-loop start and the revision is monotonic WITHIN it, and the phone's rule is:
differing epoch = hard reset that discards prior state; same epoch = strictly greater revision
only.

**On the sealed mailbox these fields are additive.** The phone's `terminal_snapshot` plaintext
carries `session` / `lines` / `cols` / `rows` unchanged and the five below as `omitempty`
siblings (`rendered_at` as a pointer, because a zero time is not omitted), so a frame carrying
none of them serializes byte-identically to the shape that wire has always had. A machine that
predates the closing round sends none, and the phone reads zero — which means "this machine
does not version its views", never a fabricated epoch.

| JSON key           | Go type   | Meaning                                                                 |
| ------------------ | --------- | ----------------------------------------------------------------------- |
| `session`          | string    | namespaced session id the view is for                                    |
| `session_instance` | string    | which INCARNATION this screen belongs to (T8-a); how a phone tells a replacement from a continuation |
| `view_epoch`       | uint64    | minted per render-loop START, process-globally increasing (T4-a)         |
| `revision`         | uint64    | strictly increasing WITHIN one epoch                                     |
| `reset`            | bool      | true on the FIRST snapshot of every epoch, on every path; the phone adopts a marked frame UNCONDITIONALLY, because the epoch counter is per daemon PROCESS and a restarted daemon re-mints epoch 1 under a phone holding a higher revision (ADR-017 D0) |
| `cols`             | int       | grid width the view was rendered at                                      |
| `rows`             | int       | grid height the view was rendered at                                     |
| `lines`            | []string  | machine-sanitized rows, one string per grid row (`vt.SnapText`)          |
| `rendered_at`      | time.Time | the MACHINE's clock: T4-b's staleness is derived from the snapshot's own age, never from arrival time — a replayed backlog arrives all at once and a held relay delivers old content at a new instant |

## The `SessionCapabilities` record

`SessionCapabilities` is the daemon-authored, per-session-instance capability record of
ADR-017 T2 / playbook §6.2, carried nested under `SessionView.capabilities` (documented
above). It is authored once at session launch and is immutable for the life of the
instance; the only mutation path afterward is the degrade-only `SetStructuredChat`, which
also cannot be observed on the wire as anything but a fresh `SessionView` row.

This table's header reads **`Field`**, not `JSON key`, for the reason `JournalRecord`'s
does (see that section): `SessionCapabilities` is not one of the four types GG-7's
bidirectional check reflects, so a `JSON key`-headed table here would fail the check's
reverse direction. Keeping it in step with `internal/protocol/schema.SessionCapabilities`
is a procedural obligation carried by that type's Go doc comments.

| Field               | Go type | Meaning                                                              |
| -------------------- | ------- | --------------------------------------------------------------------- |
| `provider`           | string  | adapter identity: `claude`, `codex`, `opencode`, `agy`, ...            |
| `provider_version`    | string  | the DETECTED version of the installed CLI, never a configured/assumed one |
| `adapter_revision`    | string  | the revision of the Swarm adapter that produced the record            |
| `structured_chat`     | bool    | true only when every T3 complete-chat row passes against `provider_version` |
| `terminal_fallback`   | bool    | whether the sanitized `TerminalViewV1` surface may be offered at all  |
| `interrupt`           | bool    | whether a semantic interrupt reaches the session's current turn       |

## The `ReconcileRecord` message

`ReconcileRecord` is the machine → phone reconcile record (PB-SYNC-7): the wire carrier of
the three rollback authorities PB-STATE-4 names, plus the sealed `RemoteProfileV1`
(ADR-017 T5, below). It rides the existing machine → phone sealed mailbox stream as an
ordinary plaintext with a `"kind":"reconcile"` discriminator (`internal/remotegw` seals
it, `internal/phonecore` demuxes it) -- no new mailbox frame kind, `internal/remote/crypto`
untouched. No field carries `omitempty`: a legitimately-zero or -empty authority must stay
distinguishable on the wire from a producer that never published the field.

This table's header reads **`Field`**, not `JSON key`, for `JournalRecord`'s reason:
`ReconcileRecord` is not one of the four GG-7-reflected types.

| Field                 | Go type            | Meaning                                                              |
| ---------------------- | ------------------- | ----------------------------------------------------------------------- |
| `machine`              | string              | endpoint id the authorities belong to                                 |
| `epoch_id`              | uint32              | epoch the content key (and both seq buckets) belong to                |
| `inbound_high_water`    | uint64              | the gateway's durable inbound accepted high-water (PB-GW-1)           |
| `journal_ceiling`       | uint64              | highest seq issued on the shared journal/terminal bucket               |
| `reply_ceiling`         | uint64              | highest seq issued on the command-reply bucket                        |
| `grant_epoch`           | uint32              | the daemon's grant-issuance epoch                                     |
| `grant_seq`             | uint64              | the daemon's grant-issuance seq                                       |
| `issued_at`             | int64               | unix millis, the same value the envelope header carries               |
| `profile`               | `RemoteProfileV1`   | the machine's sealed remote semantic profile (ADR-017 T5), below      |

## The `RemoteProfileV1` record

The asynchronous E2EE mailbox has no local `hello`, so `RemoteProfileV1` is the sealed,
machine-authored substitute: it names the accepted action/body versions, the
interaction-schema version, the `TerminalView` version and the session-capability record
version the machine currently accepts. It is nested, verbatim, under `ReconcileRecord.profile`
above -- one shared struct further R1 (and later) decision records may also add fields to,
with the profile *version* as the compatibility unit (ADR-017 T5). No field carries
`omitempty`, for the same reason as `ReconcileRecord`'s own fields.

This table's header reads **`Field`**, not `JSON key`, for `JournalRecord`'s reason:
`RemoteProfileV1` is not one of the four GG-7-reflected types.

| Field                        | Go type          | Meaning                                                        |
| ----------------------------- | ----------------- | ------------------------------------------------------------------ |
| `version`                     | int               | the profile version -- the compatibility unit companion ADRs add fields under |
| `accepted_actions`            | []string          | semantic op names the machine currently accepts                |
| `accepted_body_versions`      | map[string]int    | per-action accepted body version                                |
| `interaction_schema_version`  | int               | the `interaction-schema.md` version the machine speaks          |
| `terminal_view_version`       | int               | the `TerminalViewV1` version (ADR-017 T4) the machine speaks     |
| `capability_record_version`   | int               | the `SessionCapabilities` record version (ADR-017 T2) the machine speaks |
| `relay_tls_policy`            | string            | ADR-016 W1: `webpki` or `pinned_spki`, the machine's authoritative relay TLS policy; independent of the pin field below, never derived from it |
| `relay_host`                  | string            | ADR-016 W1/W4: the hostname the machine itself dials, checked by the phone's migration probe against the relay URL it already holds |
| `relay_spki_pin`              | []byte            | ADR-016 W1/W9: the SHA-256 SPKI pin, retained and republished across policy changes; consulted only when the effective policy is `pinned_spki` (ADR-016 W3) |

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

The owner CLI offers `external-resume` only for provider-session adoption. Its
absence is a compatibility boundary, not a best-effort downgrade: a reattach
client must refuse before discovery or launch rather than let an older daemon
interpret the reserved option as a fresh session.

The owner CLI offers `hands-off-handoff` only when it is about to send a
`handoff_from` launch, and its absence is the same kind of compatibility boundary
for the same reason, stated more sharply: an older daemon does not know the option
key and would silently ignore it, launching a context-free agent into the user's
checkout. The client must refuse rather than launch. The capability is advertised
on both tiers because `serverCaps` is tier-independent, but the option itself is
refused `policy` on the remote tier, so negotiating it there grants nothing.

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

> AMENDED BY ADR-007 B144 (2026-08-15): the allowed-cwd-root policy above stands today; B144's
> preset model — `swarm remote init`-published presets carrying provider, allowed
> workspace/worktree root and a fixed environment policy — arrives with the R1/R5 skeleton and is
> what `launch_presets` will expose. `policy_query` is not superseded by it, since a preset is
> resolved and authorized daemon-side, not chosen freely against this policy read.

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
daemon replies with `error` and forwards nothing. A `supervision` outside the closed
vocabulary, or a non-empty one without `spawn_intent` `handoff`, is refused
`invalid_field` before any daemon side effect (ADR-010 Amendment 3 C1); on the remote
tier any non-empty `supervision` is refused `policy`, since a mode makes `spawned_from`
actionable and the launch content hash does not bind lineage.

> AMENDED BY ADR-007 B144 (2026-08-15): `launch` above is owner-tier only. Phone launch is a
> supported RCE-class action in the first complete product (B144, RC-D9): the remote-tier
> counterpart is `session_launch`, a preset-based op arriving with the R1/R5 skeleton, sharing
> ADR-017 T9's six-state delivery vocabulary (`draft`/`pending`/`sent`/`refused`/`uncertain`/
> `outcome_unknown`) with `composer_send` rather than this op's plain `ok`/`error` reply. The
> Wave R1 refusal-only skeleton of both ops (this commit) answers plain `error`/
> `op_not_implemented` in the meantime — see the `session_launch` / `composer_send` / ...
> section below; T9's six-state vocabulary lands with the real handler.

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

### `set_tag`

The client sends `set_tag` with a `session_id` and the new `tag`. The daemon
sanitizes the tag as a single-line cosmetic label, persists it in session meta,
and broadcasts a roster `event` so every client converges. An empty (or
whitespace-only) tag clears the assignment. Older daemons reply with `error`, which the client surfaces.

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
- **The daemon validates before any effect, and then APPLIES the answer.** ADR-007 D7 forbids
  translating an approve into a *blind* keystroke; Mirror M1.2 (mirror-program.md section 3)
  is what makes it a sighted one. The stored binding tuple (agent instance, content hash,
  daemon-authoritative expiry) and the offered decision set are checked first, and only then
  does the daemon read the session's LIVE screen and — if the session's adapter still
  recognizes a permission dialog it holds a **recorded** key map for — write that dialog's own
  keys into the session's PTY through the shared session tap. The card is left pending on
  every refusal below.

  | refused because | code |
  |---|---|
  | the binding tuple, the expiry echo, or the daemon's own window does not match | `stale_approval` |
  | an answer has already been typed for this request and is awaiting observation | `stale_approval` |
  | the live screen no longer shows an answerable dialog (the terminal answered first) | `stale_approval` |
  | the decision was never offered by the request | `invalid_field` |
  | the decision's adapter-classified verdict is neither allow nor deny, so no key answers it | `invalid_field` |
  | this session's CLI is not answered by keystroke at all, or its PTY is unreachable | *(no code)* |

  The screen gate is the safety property, not a formality: between the phone rendering its
  card and the tap arriving, the owner may have answered at the terminal, and a key typed at
  a dismissed dialog lands in the composer as input the agent acts on.

- **`ok` means APPLIED, not RESOLVED.** The daemon journals no `approval_resolved` on the op.
  §3.6's record lands when the daemon OBSERVES the session leave the waiting state
  (IS-LIFE-2's existing paths), and is attributed `by: phone` echoing the phone's
  `operation_id` because the daemon typed that answer itself. A dialog still on screen a few
  seconds after the keys were written is surfaced as a `session_status` item and never as a
  resolution: a card may not claim an outcome nobody observed.

`operation_id` is the phone-minted idempotency key of the OP and is never equal to
`interaction_id`, which names the interaction (IS-APR-1). No replay dedup is needed: a
re-delivered approve finds the approval already answered or already resolved, and is refused
`stale_approval` either way.

### `session_launch` / `composer_send` / `operation_status` / `turn_interrupt` / `terminal_control_begin` / `terminal_control_end`

> AMENDED BY WAVE R5 (2026-08-16, ADR-007 B144(b)): `session_launch` and
> `operation_status` now have REAL handlers and no longer answer `op_not_implemented`;
> see "`session_launch` / `launch_presets` / `operation_status` (Wave R5)" below. The
> choke-point ordering this section pins (authz first, then `body_version`, then the
> op-specific reply) is inherited by the real handlers unchanged.
>
> AMENDED BY WAVE R6 (2026-08-19, Mirror M2.4, ADR-009 (8)): `composer_send` and
> `turn_interrupt` now have REAL handlers too — see "`composer_send` / `turn_interrupt`
> / `interaction_history` / `interaction_detail` (Wave R6)" below, with the same
> inherited choke-point ordering. Only `terminal_control_begin` and
> `terminal_control_end` remain refusal-only exactly as described here.
>
> AMENDED BY WAVE R8 (2026-08-20, ADR-017 T6): `terminal_control_begin` and
> `terminal_control_end` now have REAL handlers as well, so the refusal-only dispatch this
> section described has **no ops left and is deleted from `server.go`**. Every one of the
> six inherited its ordering unchanged -- authz first, then `body_version`, then the
> op-specific reply -- and `r1_refusalops_test.go` still drives all six through those
> assertions; what changed, op by op, is the CODE each answers a malformed frame with:
> `terminal_control_begin` is refused `invalid_field` (a bodyless begin binds no
> incarnation) and `terminal_control_end` is refused `stale_generation` (there is no
> generation on this connection to end). `op_not_implemented` itself is NOT retired: it is
> still what a daemon WITHOUT the seam answers, which is a different fact from "this build
> does not know the op".
>
> R8 also lands the two UNSIGNED frame kinds ADR-017 T6 pairs with them,
> **`terminal_input`** and **`terminal_control_keepalive`**. They carry no device signature
> and reach no device authenticator: what authorises them is the E2EE frame's own
> authenticated sender and sequence PLUS the confirmed control generation, re-evaluated on
> EVERY frame alongside the kill switch, the device's continued registration and the
> session's capability record (T6-e). That is the sole exception to full-body signatures in
> this protocol and is deliberately the SAME exception ADR-007's 2026-07-24 Decision 1
> already carries; it is held to exactly two body types and one live generation, which is
> what keeps it an exception rather than a policy. A refused frame is DROPPED and never
> buffered (T6-f), so a phone can never turn live-only input into a short offline queue.
>
> And **`terminal_subscribe` gains a SESSION-SCOPED capability gate** (amendment T2-c),
> after the kill switch and before any tap opens, remote tier only: a session whose
> daemon-authored capability record does not permit a terminal view is refused
> `capability_refused`, and the gate is re-read before EVERY emission so a session
> degraded, revoked or replaced mid-stream stops within a tick.

The Wave R1 "refusal-ops" skeleton (playbook §6.3, ADR-017 T5, ADR-007 B144): six signed
remote-tier ops landing as **refusal-only** daemon handlers ahead of their real business
logic. Five are MUTATING (`session_launch`, `composer_send`, `turn_interrupt`,
`terminal_control_begin`, `terminal_control_end`); `operation_status` is a READ, on
`push_prefs`'s own precedent — it cannot start, stop or type into anything, so a read-only
paired device may still poll the status of its own pending operation.

Each carries the usual mutating-op device-auth fields (`operation_id`, `device_id`,
`device_sig`, `expires_at`) plus `body_version`, and runs through the SAME
`requireRemoteAuthz` choke point as `kill`/`delete`/`launch`/`approve` (kill switch,
`operation_id`, device signature, capability) **before** any op-specific reply — a forged
signature or a missing device field is refused `not_authorized`/`invalid_field` and never
reaches the refusal below. `session_launch` and `operation_status` name no session instance
yet and sign over `OperationSessionSentinel` (`"@op"`, `LaunchSessionSentinel`'s sibling)
rather than a `session_id`; the other four sign over the `session_id` they target.

Once authorized, the daemon checks `body_version` against the one version this machine
currently accepts (`schema.CurrentProfileVersion`, shared across the whole R1 companion
set, `RemoteProfileV1` above): a mismatch — including an absent (`0`) `body_version`, which
is never treated as an implicit "version 1" — is refused `invalid_field` naming the
accepted version. Only once **both** hold does the daemon reply `error` with
`op_not_implemented`, naming the op in its `error` text: a name this build recognises
(mapped in `actionClass`, `internal/skeleton/deviceauth.go`) but has not yet built a real
handler for — distinguishable from the plain "unknown op" `error` an unrecognised action
still gets.

The gateway forwards all six to the daemon **unchanged** (`opForAction`, Op == Action,
mirroring `kill`/`delete`/`approve`/`push_prefs`) rather than refusing any of them locally:
only the daemon holds the device registry `requireRemoteAuthz` authorizes against, and the
gateway is a blind conduit.

`terminal_input` and `terminal_control_keepalive` (ADR-017 T4/T6) are **deliberately
excluded** from this vocabulary and from `actionClass`: they ride only the E2EE frame's own
authenticated sender/sequence and a confirmed control generation, never a per-frame
signature — the same exception the existing lease input frame already takes. Both stay on
the generic unmapped-action `error`, never `op_not_implemented`.

Each op's own body (`composer_send`'s text, `session_launch`'s preset id, ...) and its real
handler are a later slice's amendment obligation (GG-7 applies again when they land); this
skeleton carries only the one field every one of the six needs to refuse a version mismatch
honestly, and `body_version` is not yet bound into the device signature via `content_hash`.
Also outstanding: `SessionCapabilities` (ADR-017 T2, `RemoteProfileV1` above) already has a
producer (`internal/skeleton/capability.go`) but no daemon-side consumer, so today's
`op_not_implemented` refusal for `terminal_control_begin`/`terminal_control_end` runs
without the T2 per-session capability gate (`terminal_fallback`) and `turn_interrupt`
without T6's `interrupt` gate — wiring that lookup in belongs to each op's own real handler.

### `session_launch` / `launch_presets` / `operation_status` (Wave R5)

The phone remote-launch vocabulary (ADR-007 B144(b), D8 restrictions retained in full;
playbook "Wave R5"). All three are session-less and sign over `OperationSessionSentinel`.

**`launch_presets`** is the signed READ of the machine-authored preset list: the reply
carries `presets` (`LaunchPresetView`: opaque `id`, `display_name`, `agent`, canonical
symlink-resolved `root`, allowlisted `options`, `worktree` default, content-bound
`revision`) plus `preset_policy_revision` and `device_capability` (the signer's own
registry-pinned tier, stated by the machine so the phone can render its tier-denied
launch state honestly; empty when the backend exposes no capability seam). Empty custody
answers an empty list — never an invented default (ADR-007 B135). Presets are
MACHINE-AUTHORED (`swarm remote presets add/list/edit/remove`); nothing in one ever comes
from a phone.

**`session_launch`** resolves ONLY a machine-authored preset at the signed revision. The
body (`Control.session_launch`) is bound into the device signature via
`SessionLaunchContentHash(preset_id, preset_revision, initial_prompt)`, so a gateway
cannot re-point a valid signature at a different preset or prompt. After the shared
authz + `body_version` gates, refusals fire in order and all BEFORE any argv composition:
missing body → `invalid_field`; an id this machine never authored → **`unknown_preset`**
(remedy: re-list); a right id at a changed revision → **`stale_preset`** (playbook:447-448
— never silently launching different policy; remedy: re-confirm); a preset carrying a
hard-forbidden option value → `policy` (the SAME hard-coded remote denylist free-form
launch rides, R-POL.4); a root outside the machine-configured allowed roots → `policy`
(the SAME `LaunchPolicy` seam, R-POL.3, fail-closed absent). The composed spec comes from
the resolved preset alone — resolved root as cwd, preset options copied, NO client env
ever — and carries the signed `operation_id` so the daemon's EXISTING two-phase
idempotent reservation engages: a network retry of the same `operation_id` converges on
the one session and never spawns a second process. Success replies op `session_launch`
with the launched `session` view; both the applied launch and every semantic refusal are
recorded on the D10 activity log.

The agent ENVIRONMENT of that spec is daemon policy, which is ADR-007 D8's other half:
the phone supplies none, and the daemon fills it from its OWN process environment through
the same normative allowlist a local launch is filtered by (`persist.FilterEnv` — PATH,
HOME, SHELL, TERM, the locale family, venv/conda, the two provider credentials; everything
else dropped). The daemon process is the user's machine environment, so ADR-006's
billing-inheritance rule holds one level up, and there is no configuration surface for a
phone to reach.

One reply is neither success nor refusal: **`outcome_unknown`** (ADR-017 T9's delivery
vocabulary, the same state `operation_status` reports). It is the answer when the signed
`operation_id` is already IN FLIGHT under another driver — a redelivery racing its
original — and its outcome cannot be decided yet: the daemon will neither claim the
in-flight reservation as an applied session (it can still roll back) nor drive a second
process for it. The phone renders it as undecidable and sends the user to the session list
rather than reporting either a created session or a refusal.

**`operation_status`** is the reconciliation READ: `subject_operation_id` names the
asked-about op and is BOUND into the device signature via
`OperationStatusContentHash(subject_operation_id)` (round-2: unbound, a compromised
gateway could re-point a valid signature at another operation id and read back that
operation's namespaced session id — and the op is READ class, so any paired tier could).
The reply's `operation_outcome` is `applied` (authoritative, with the
session id), `outcome_unknown` (a launch that died mid-flight and cannot be proven —
never silent), or `unknown_operation` (no record; never invented). The read has no side
effect and never authorizes a retry (playbook:449): re-sending the SAME signed
`session_launch` operation id is the one re-driver.

### `composer_send` / `turn_interrupt` / `interaction_history` / `interaction_detail` (Wave R6)

The complete-chat vocabulary (Mirror M2.4 / M3.1 / M3.3, ADR-009 (5)/(8), ADR-014,
interaction-schema.md IS-LIFE-5 and IS-CAP-2/-3).

**`composer_send`** is the phone's structured message into one session. The body
(`Control.composer_send`: `session`, `expected_turn`, `text`) is bound into the device
signature via `ComposerSendContentHash(session, expected_turn, text)`, recomputed
daemon-side from the forwarded body, so a gateway cannot alter the text or re-point
`expected_turn` under a valid signature. After the shared authz + `body_version` gates,
structural refusals fire `invalid_field` in order: missing body; a body `session` that is
not the signed `session_id` (the approve collision rule); empty `text`; text past the
`send_input` path's own 4096-byte bound (refused, never truncated — a clipped send submits
a different message than the one the signature covered). The daemon then checks
`expected_turn` against the session's CURRENT turn (IS-ENV-1's own state): a send rendered
against a turn that has moved on — a newer `user_message` opened a new turn, or the turn
closed on a terminal `agent_message` — is refused **`stale_turn`** and types NOTHING; an
idle session is matched by the EMPTY `expected_turn`. An accepted send is written into the
session's PTY through the daemon's own input path with the r3p submit-boundary framing
(the CR that runs the message never shares a write with it), and the daemon remembers the
injection so the NEXT captured `UserPromptSubmit` echoing that text journals with
`source: "phone"` and the op's `operation_id`.

> ATTRIBUTION IS BOUNDED, NOT INFALLIBLE (amended by the Wave R6 review fix-pack, finding
> B9). This paragraph used to end "an owner-typed prompt keeps `owner` and does not consume
> the correlation", with no qualifier, and that is not something the mechanism can promise.
> The CLI's `UserPromptSubmit` hook is the only echo and carries no injection id, so the
> daemon correlates BY TEXT — and a probe showed both directions of the resulting collision:
> an owner-typed `yes` at the terminal was stamped `source: "phone"` carrying the phone's
> `operation_id`, because a phone send of `yes` was still pending. Short words are exactly
> the ones two parties type identically. The correlation is therefore TIME-BOUNDED: a
> pending injection expires after `skeleton.pendingSendTTL` (10 s, ~3 orders of magnitude
> over the local hook round trip) and a prompt arriving after it keeps the adapter's honest
> `owner`. What the daemon promises is that an attribution of `phone` is backed by an
> injection it watched WITHIN that window; what it cannot promise is that two identical
> prompts inside one window are told apart.

**`composer_send` is refused on a degraded session.** A session degraded by a proven
`structured_gap` (ADR-017 T2 rule 2), or holding a capability record that says
`structured_chat` is false, has NO structured composer, because it has no message sink — the
user's words would go into the PTY and the transcript could never show them, which is the gap
silently bridged. The refusal is **`structured_unsupported`**, `turn_interrupt`'s
`interrupt_unsupported` for the composer: the caller is fine, the capability is absent, and
nothing is typed.

> SHARPENED, SAME REVIEW ROUND. This paragraph opened "A session whose `structured_chat`
> capability is absent, **or** has been degraded…", which reads as a promise the handler does
> not keep: a session with **no capability record at all** is NOT refused. That is deliberate
> and disclosed rather than an oversight — `registerSessionCapabilities` /
> `deriveSessionCapabilities` have no production caller yet (`internal/skeleton/capability.go`
> and `chat.go`'s gate both say so in as many words), so today NO live session has a record,
> and refusing on absence would refuse every composer send on the wire: feature-off dressed as
> fail-closed, hiding the very defect the gate exists to catch. The two facts the gate keys on
> are the two that are production-reachable: the durable degrade marker, and a record that
> exists and says false. When the capability-publication slice lands, the absent-record arm
> becomes reachable and should tighten to a refusal; docs/verification/r6-chat.md carries that
> as a named residual.

**`composer_send` can still be merged with the owner's draft, and is not yet fixed.** The
injection writes the text and the CR through the session tap with no check that the
terminal's input region is empty; if the owner has a half-typed line there when the send
lands, the phone's text is APPENDED and the CR submits the concatenation. ADR-017's
IS-LIFE-5 amendment obligation (`expected_input_revision`, enforced by a shim-wide input
transaction) is the fix, `internal/shim` is out of Wave R6's scope, and the gap is disclosed
in ADR-017's "Deferred, disclosed" and in docs/verification/r6-chat.md rather than papered
over.

**`turn_interrupt`** is the semantic Stop as a signed op. Its body
(`Control.turn_interrupt`: `session`, `expected_turn`) is bound into the device signature
via `TurnInterruptContentHash(session, expected_turn)`, recomputed daemon-side, so a gateway
cannot re-point a valid Stop at a different turn. After the shared authz + `body_version`
gates the structural refusals fire `invalid_field` in order: missing `session_id`; missing
body; a body `session` that is not the signed `session_id`; an EMPTY `expected_turn`. The
daemon then checks `expected_turn` against the session's CURRENT turn and refuses
**`stale_turn`** having typed nothing. Only then does it resolve the session's adapter seam
and either type that adapter's OWN declared cancel sequence, or refuse
**`interrupt_unsupported`** having typed nothing (ADR-017's honest degrade: "this provider
version has no safe remote interrupt" is a nameable state, never a guessed keystroke and
never a silent OK).

> AMENDED BY THE WAVE R6 REVIEW FIX-PACK (finding B7). This op was specified here as
> carrying "NO body — the signed tuple's `session_id` is its whole subject, so no new crypto
> appears anywhere on its path", and the bodylessness was sold as a virtue. It was a defect.
> `composer_send` carries `expected_turn` for a stated reason — a tap lands later than it was
> rendered — and Stop is tapped under exactly that race; probed, a Stop rendered against turn
> A returned `ok` and typed the cancel sequence into turn B, which in playbook §8.1 is the
> turn the OWNER just started from the terminal. The Claude adapter's own note records that
> the cancel key at an IDLE prompt clears the composer, so a late Stop wipes the terminal
> user's half-typed line. The signed tuple's session is the AUTHORIZATION subject; the turn
> is the OPERATIONAL one, and the op had none. The "no new crypto" half of the old sentence
> still holds exactly: the body rides the tuple's EXISTING content slot, `composer_send`'s
> own arrangement. `expected_turn` is REQUIRED and non-empty — there is deliberately no
> spelling of "interrupt whatever is running", which is what closes the idle case.

**`interaction_history`** and **`interaction_detail`** are UNSIGNED reads on the
`terminal_watch` precedent (IS-CAP-2): never forwarded to the device authenticator, no
device fields, and no new device-signed action — `actionClass` stays closed. They ARE
gateway-routed (`opForAction`, `ActionInteractionHistory` / `ActionInteractionDetail`), and
a read whose body was stripped in transit is refused at the gateway rather than forwarded
bodyless, the rule `launch` / `approve` / `session_launch` / `composer_send` already ride.
Daemon-side both require the negotiated `journal` capability AND honor the remote kill
switch, exactly as `journal_read` does — the same two gates, the same two refusals, on the
same plane.

> CORRECTED BY THE WAVE R6 REVIEW FIX-PACK (finding B2). The gate sentence above used to
> read "gating (capability, kill switch) is daemon-side, behind the seams", and the seams
> applied neither: with the kill switch OFF, `journal_read` refused while
> `interaction_history` served a `user_message`'s text verbatim and `interaction_detail`
> served a full pre-truncation output body; with NO capability negotiated, `journal_read`
> refused and `interaction_history` still served. Remote tier only — the owner tier shares
> the kill-switch-implementing core and is never gated.

`interaction_history` (`Control.interaction_history`: `session`, `before_item`,
`limit`) answers on the existing `journal` carrier — every record the named session's,
strictly older than `before_item`, ascending by cursor, at most `limit` — plus
`history_floor` when nothing older is retained; an unknown `before_item` or a
non-positive `limit` is refused `invalid_field`. The page also carries every
`structured_gap` boundary inside its range, beside the `interaction` records: a page that
omitted them would span a proven tear contiguously, which is ADR-017's silently-bridged gap.
`limit` bounds RECORDS but the page begins on an ITEM BOUNDARY — the window is the largest
suffix of whole items that fits, and one item too large to fit ships alone and over `limit`
rather than being cut. (Amended by the fix-pack, finding B5: the trim was by raw record over
a channel the phone pages by item id, so a multi-record `agent_message` could arrive as a
headless tail that the phone rendered as a whole message and that no later page could ever
return.) The boundary rule is ONE-SIDED, and review round 2 recorded that rather than leaving
it implied: the page BEGINS on an item boundary and ENDS at `before_item`'s first record's
cursor, so an item whose own later increments follow that cursor is delivered head-only. The
consumer's rule keeps that honest rather than corrupt — a record that does not strictly advance
its item's cursor is dropped, never concatenated in the wrong place — so what a reader sees is a
message that stops early. ADR-014 A6 carries the full accounting. `interaction_detail`
(`Control.interaction_detail`: `session`, `item_id`) answers exactly ONE `journal` record
whose `item` is the FULL pre-truncation body retained at capture time; outside the bounded
retention window it answers the sealed **`unavailable`** with no records at all (IS-CAP-3:
never a partial body presented as whole).

### `terminal_subscribe` / `terminal_snapshot`

Terminal peek (A7 renderer slice B), mirroring the
`journal_subscribe`/`journal_event` streaming pair. Unlike `take_control`, the peek
is **read-only** and works BEFORE any control session exists (no lease, no signed
op).

> AMENDED BY ADR-017 (2026-08-15): the new capability-routed fallback is gated by the ADR-017 T2
> per-session capability record — a `terminal_fallback` session may open `TerminalViewV1`'s
> streaming and control-generation ops (`terminal_control_begin`/`terminal_input`/
> `terminal_control_end`, ADR-017 T4/T6 — `terminal_control_begin`/`terminal_control_end` land
> as this commit's refusal-only skeleton, documented below under "Control-op vocabulary";
> `terminal_input` stays deliberately unmapped per T6, see that section; the watch/unwatch op's
> wire name lands with a later R1 skeleton commit, since ADR-017 itself uses `terminal_watch` for
> both this section's legacy body and the new fallback stream) only when its daemon-authored
> `terminal_fallback` capability is
> true, and every `structured_chat` session has no route to it at all (T2 rule 4). This section's
> `terminal_subscribe`/`terminal_snapshot` pair is NOT gated by the capability record — it stays
> on the wire unchanged and un-deleted, reachable only under the legacy remote profile (ADR-017
> T4).

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
(mirroring `attach`): the remote tier keeps its own signed `take_control` lane. The
daemon's supervision component authors its notifications to a source session through
this same serialized path (ADR-010 Amendment 3 C3), so they carry every guarantee and
bound below.

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
