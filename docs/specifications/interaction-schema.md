# Interaction items — normative schema

**Spec ID**: 0002
**Status**: Draft — binds on ADR-009 acceptance
**Author**: Nathan Delacrétaz (with Claude)
**Created**: 2026-08-07
**Binds**: the machine-side item producer (daemon + per-CLI adapters), the gateway's
machine→phone seal, `internal/phonecore`'s journal path, and every phone transcript surface.
**Decided by**: [ADR-009](../adr/ADR-009-structured-chat-interaction.md) (the pivot to a
structured chat transcript). The adapter-side capture that feeds this schema is
[ADR-010](../adr/ADR-010-adapter-structured-capture.md); multi-device epoch handling is
[ADR-011](../adr/ADR-011-multi-device-epochs.md). This document defines the **payload**;
those documents own the decisions, and neither is restated here.
**Evidence base**: `docs/verification/spike-SA.md` (VT-diff derivation limits),
`spike-SB.md` (real hook/JSON-RPC payload shapes), `spike-SC.md` (approval hold windows).

An **interaction item** is one durable, replayable unit of a conversation: a message, a tool
run, a file change, an approval, a plan revision, a status marker. The phone renders a
transcript of items and nothing else.

> AMENDED BY ADR-017 (2026-08-15): the no-grid rule is re-scoped to `structured_chat` sessions, not repealed. For a `structured_chat` session there is no terminal emulation and no raw grid on the phone (ADR-009), unamended. A `terminal_fallback` session (ADR-017 T1-T4 — incomplete providers, plus a version-skewed Claude or Codex build per T3's *Version skew* row; never a healthy `structured_chat` session, T2 rule 4) may render the machine-sanitized `TerminalViewV1` snapshot instead — never raw PTY bytes, never a VT parser on the phone — and never promotes anything it shows into this transcript (ADR-017 T10).

Items are produced on the machine, adjacent to the sanitization choke point
in `internal/daemon/terminalrender.go`; raw PTY bytes never reach the phone.

---

## 1. Where an item sits — three discriminator levels

Confusing these three is the failure mode this section exists to prevent. PB-SYNC-1 forbids a
kind tag in `SenderKeyID` or `EpochID` (both **are** the seq bucket key, and a measured
collision cost typing for the life of an epoch). Nothing here goes near either field.

| Level | Discriminator | Values | Where it lives |
|---|---|---|---|
| 1. Mailbox frame family | `kind` on the sealed plaintext | `terminal_snapshot`, `command_reply`, `epoch_grant`, `reconcile`, `journal_reseed`, `push`, or **absent** | `internal/phonecore/snapshot.go` — **UNCHANGED by this spec** |
| 2. Journal record type | `type` on `journal.Record` | the existing set **plus `interaction`** (new) | `internal/journal/journal.go` |
| 3. Item kind | `kind` on the item | the eight kinds of §3 | inside `Record.Payload`, i.e. inside the AEAD-covered plaintext |

- **IS-LAYER-1** (Ubiquitous) An interaction item SHALL travel as a `journal.Record` whose
  `type` is `interaction` and whose `payload` is the item object. Such a record SHALL be sealed
  as a **bare journal record** — mailbox `kind` absent — and SHALL be routed by the existing
  journal path with no new demux branch.
- **IS-LAYER-2** (Ubiquitous) The item `kind` SHALL live only inside `payload`. No value
  defined by this spec SHALL be written to `SenderKeyID`, `EpochID`, or any other header field
  covered by `Bucket` (PB-SYNC-1, ADR-007 B84/B85).
- **IS-LAYER-3** (Ubiquitous) Ordering SHALL be the journal `cursor`, ascending. Items carry
  **no private sequence number**; two items are ordered iff their cursors are. For successive
  records of one streamed item, cursor order **is** delta order.
- **IS-LAYER-4** (Ubiquitous) Because items are journal records, they inherit the journal's
  repair channel: a gap in the shared seq bucket stales `journal`, and only a contiguous
  roster+events reseed clears it (PB-SYNC-1/-2/-3). No new repair channel is created.

Wire carriage requires one additive field on `schema.JournalRecord`: `item`, populated only when
`type == "interaction"`, because that wire type today carries no payload. §8 requires two more —
the approve body on `schema.RemoteCommand` (IS-LIFE-4) and ADR-009's composer-send op with its
`expected_turn` field (IS-LIFE-5).

**Only the third is machine-checked.** GG-7's drift check reflects `Control`, `SessionView`,
`LaunchReq` and `TerminalSnapshot` only (`internal/protocol/protocolmd_test.go`), and
`protocol.md` documents `JournalRecord` and `RemoteCommand` at the field level in
`internal/protocol` rather than as wire tables — so no build can fail on a missing `item` or
approve row. The composer-send op *is* covered, because it is a `Control` op and field, and it is
the one new `protocol.md` row ADR-009 books. For the other two the obligation is **procedural**,
carried by the Go field's doc comment; it is written here so nobody mistakes it for a fence that
fires.

---

## 2. The item envelope

Keys are `snake_case`, matching the control surface. Fields the enclosing **wire** record
(`schema.JournalRecord`) already carries — `session_id` and `cursor` — are **never** duplicated on
the item. `ts` and the journal's `schema_version` are *not* on that record; they exist only on the
on-disk `journal.Record`. So the item carries its own `ts`, and its own `v` is the only schema
version a consumer needs.

| Field | Type | Req. | Meaning |
|---|---|---|---|
| `v` | int | yes | item schema version; `1` for this document. Distinct from `journal.SchemaVersion`. |
| `item_id` | string | yes | ULID, unique within the session, stable across every record of the item. Daemon-minted. |
| `ts` | time | yes | machine-supplied instant for **this record**. The wire journal record carries none; a consumer SHALL NOT substitute arrival time (the PB-APP-11 clock rule). |
| `turn_id` | string | no | groups items into one turn. Empty for items outside a turn (e.g. a launch-time `session_status`). |
| `kind` | string | yes | one of the eight kinds (§3). |
| `status` | string | no | `in_progress` \| `completed` \| `failed` \| `declined` (§4). Absent means not applicable to the kind. |
| `truncated` | bool | no | set when any field of this item was clipped to a §5 cap. |
| `full_bytes` | int | no | byte length of the untruncated payload, carried only with `truncated`. |
| `detail` | bool | no | set when a full body is fetchable on demand (§5). |

- **IS-ENV-1** (Ubiquitous) A turn SHALL open on a `user_message` and close when its
  `agent_message` reaches **any** terminal status (§4) — `completed` with `stop_reason:
  end_turn`, `declined` with `interrupted`, `failed` with `error`. Items emitted between them
  SHALL carry that `turn_id`. A turn is never left open on a non-`completed` outcome:
  IS-LIFE-5's `expected_turn` needs a current turn that is defined on every ending, and IS-ST-2
  covers the instance-death case.
- **IS-ENV-2** (Ubiquitous) Every record of a streamed item SHALL repeat the same `item_id`.
  A consumer SHALL fold records by `item_id`, not by position.
- **IS-ENV-3** (Unwanted) IF a producer would emit an item lacking `v`, `item_id` or `kind`,
  THEN it SHALL emit nothing rather than a partial item; a consumer SHALL skip such a record
  and SHALL still advance its cursor (§8).

---

## 3. The eight kinds

Fields below are additional to the envelope. Every string field is capped per §5.

### 3.1 `user_message`

| Field | Type | Meaning |
|---|---|---|
| `text` | string | what the human sent |
| `source` | string | `phone` \| `owner` (typed at the machine) \| `derived` (reconstructed by the fallback adapter) |

### 3.2 `agent_message`

| Field | Type | Meaning |
|---|---|---|
| `text` | string | the increment appended by **this record** (delta semantics, §6) |
| `stop_reason` | string | on the terminal record only: `end_turn` \| `interrupted` \| `error` |

### 3.3 `tool_run`

| Field | Type | Meaning |
|---|---|---|
| `tool` | string | adapter-reported tool name (`Read`, `Bash`, `Edit`, …) |
| `action` | object | the structured summary of §7 — what makes a card read `Read src/main.rs` |
| `output_excerpt` | string | leading bytes of the tool's output |
| `truncation_marker` | string | the CLI's own truncation text, verbatim (§7) |
| `exit_code` | int | for `execute` actions |

### 3.4 `file_change`

| Field | Type | Meaning |
|---|---|---|
| `path` | string | repo-relative where derivable, else absolute |
| `change` | string | `create` \| `modify` \| `delete` \| `rename` |
| `old_path` | string | previous path, on `rename` only |
| `diff_excerpt` | string | **unified diff text**. Producers normalize: Claude's `Edit` hook body is `old_string`/`new_string`, not a diff (spike-SB), and the producer renders it — consumers never see the raw pair. |
| `added`, `removed` | int | line counts of the whole change, not of the excerpt |

- **IS-FC-1** (Ubiquitous) A `file_change` SHALL describe a change that **has been applied**. A
  proposed-but-unapplied edit is an `approval_request`, never a `file_change`.

### 3.5 `approval_request`

| Field | Type | Meaning |
|---|---|---|
| `agent_instance` | object | `{shim_pid, shim_start_time}` — the ADR-007 D7 instance binding |
| `content_hash` | string | daemon-computed SHA-256 over the daemon's byte-exact canonicalization |
| `expires_at` | time | **daemon-authoritative**; a phone countdown is display-only |
| `summary` | string | one line for the card headline |
| `action` | object | §7, so an approval card reads like a tool card |
| `decisions` | []object | adapter-supplied available decisions, `{id, label}`. The ids are the CLI's **own** vocabulary, not a normalized one: spike-SB captured Codex's `availableDecisions` as `accept` \| `acceptWithExecpolicyAmendment` \| `cancel`, and Claude Code's `PermissionRequest` as a numbered dialog plus `permission_suggestions` (`addDirectories`, `setMode`). Each also carries a machine-side **verdict** (IS-APR-4), which is not a wire field. |
| `mode` | string | `card` (adapter applies the decision) \| `prompt_card` (IS-LIFE-6 fallback) |
| `prompt_lines` | []string | `prompt_card` only: the sanitized prompt region, as text |

- **IS-APR-1** (Ubiquitous) The item's `item_id` **is** the `interaction_id` of ADR-007 D7.
  There SHALL be exactly one such id. `operation_id` is a separate, phone-minted idempotency
  key on the enclosing `Control` and SHALL NEVER equal it.
- **IS-APR-2** (Ubiquitous) A phone SHALL echo `content_hash` and `expires_at` **verbatim** as
  received; it SHALL NOT compute or adjust either. The daemon recomputes and rejects a stale or
  mismatched approve, and never translates one into a blind keystroke (ADR-007 D7).
- **IS-APR-3** (Ubiquitous) `prompt_lines` SHALL be sanitized text produced by the same
  machine-side path as a terminal snapshot. It is a **card**, never a grid: no cursor, no
  styling, no addressability. The decision→keystroke mapping SHALL NOT be carried on the item: it
  is machine-side data — adapter-produced at capture, daemon-held (ADR-009 (4), ADR-010 §4) — and
  IS-LIFE-6 forbids the phone authoring the keystroke, so a field for it would only invite the
  implementation those rules exist to prevent. The card labels its buttons from
  `decisions[].label`.
- **IS-APR-4** (Ubiquitous) Every decision an `approval_request` offers SHALL carry a **verdict**
  — `allow` \| `deny` \| `other` — supplied by the adapter **at capture** from its own CLI
  vocabulary, and an adapter that offers a decision without one is **non-conformant** (owner
  ruling 2026-08-07; ADR-010's conformance obligations). It is the ONE normalized thing about a
  decision, and it exists because the ids deliberately are not: nothing downstream can read
  `cancel` as a refusal without guessing at a vocabulary it does not own, which is the posture
  IS-TOOL-2 forbids for the same reason. `other` is that rule's escape hatch — a decision the
  adapter can place neither way is declared unclassified rather than guessed at.
  The verdict is **machine-side and is NOT a field on the item**, on the `keystrokes` precedent:
  the card labels its buttons from `decisions[].label` and no phone surface switches on polarity,
  so putting it on the wire would only create a second place for the two to disagree. A phone
  need for it later is an additive field and a schema change, not an unused field shipped ahead
  of its consumer.

### 3.6 `approval_resolved`

| Field | Type | Meaning |
|---|---|---|
| `interaction_id` | string | the `approval_request`'s `item_id` |
| `decision` | string | `allowed` \| `denied` \| `cancelled` \| `superseded` \| `expired` \| `answered_locally` |
| `by` | string | `phone` \| `owner` \| `daemon` (expiry) \| `agent` (cancel/supersede) |
| `operation_id` | string | echoed when a phone `ActionApprove` drove the resolution |

- **IS-RES-1** (Ubiquitous) `allowed` and `denied` SHALL be classified from the **verdict** of the
  chosen decision (IS-APR-4), never from its id and never from the CLI's later behaviour. Only a
  `deny` verdict resolves `denied`. An `other` verdict resolves `allowed`, which here asserts no
  more than "answered from the phone, and not refused" — §3.6 has no third value for a remote
  answer, and manufacturing a refusal from an unclassified tap would be the guess IS-APR-4 exists
  to remove. The four remaining values are daemon-observed and carry no verdict.

### 3.7 `plan_update`

| Field | Type | Meaning |
|---|---|---|
| `revision` | int | monotonic per session |
| `steps` | []object | `{text, state}`, `state` ∈ `pending` \| `in_progress` \| `completed` \| `cancelled` |

- **IS-PLAN-1** (Ubiquitous) A `plan_update` is **latest-state, not incremental** (the ADR-008
  status-coalescing posture). A consumer SHALL keep only the highest `revision` per session and
  SHALL discard a lower one that arrives late.

### 3.8 `session_status`

| Field | Type | Meaning |
|---|---|---|
| `process` | string | `running` \| `exited` \| `lost` |
| `turn` | string | `active` \| `idle` \| `unknown` |
| `interaction` | string | `none` \| `prompt` \| `permission` \| `unknown` |
| `group` | string | the server-derived display group |
| `exit_code` | int | on `exited` |
| `note` | string | a neutral transcript marker, e.g. the spike-SA overlay-transition placeholder |

- **IS-SS-1** (Ubiquitous) `session_status` is the **transcript's** marker; `group_transition`
  remains the **roster's**. A client renders the roster from the latter and the transcript from
  the former. The overlap is intended: `session_status` additionally carries the `interaction`
  dimension, which the roster record drops.

---

## 4. Statuses

| Status | Meaning | Terminal? |
|---|---|---|
| `in_progress` | the item is open; more records will follow under this `item_id` | no |
| `completed` | finished as intended | yes |
| `failed` | attempted and errored | yes |
| `declined` | refused — by the human, by a policy, or by an expiry | yes |

- **IS-ST-1** (Ubiquitous) An `item_id` SHALL reach at most one terminal status, and SHALL emit
  no further record after it.
- **IS-ST-2** (Unwanted) IF a session's agent instance dies with items still `in_progress`,
  THEN the daemon SHALL close each with `failed` before the session's terminal
  `session_status`. A transcript SHALL NOT be left with a permanently spinning card.

---

## 5. Size caps, excerpts, and detail on demand

`MaxItemBytes` is **ratified** by the owner ruling of 2026-08-07, recorded as
[ADR-009's 2026-08-07 amendment](../adr/ADR-009-structured-chat-interaction.md#amendment-2026-08-07--maxitembytes-is-raised-to-16-kib-so-5s-own-maxima-fit-inside-it),
which carries the arithmetic. The **per-field** numbers below remain **proposed by this document
and unratified** — the amendment derived the item cap from them as they stand rather than
ratifying them. What would ratify one: a measured slice, or a further owner ruling recorded in
ADR-009. They are floors chosen well under the relay's per-envelope admission cap —
`relay.MaxFrame` is 1 MiB, but the check runs on the **base64-expanded** envelope
(`internal/remote/relay/server.go:1028`), so roughly 768 KiB of plaintext — not measured optima.

| Cap | Default | Applies to |
|---|---|---|
| `MaxItemBytes` | 16 KiB | the item's serialized JSON payload |
| `MaxTextBytes` | 4 KiB | `text`, `output_excerpt`, `diff_excerpt` |
| `MaxSummaryBytes` | 256 B | `summary`, each `action` string field, each `decisions[].label` |
| `MaxPromptLines` | 40 lines × 200 runes | `prompt_lines` |
| `MaxSteps` | 64 steps × 200 B | `plan_update.steps` |
| `MaxDecisions` | 8 | `decisions` |

- **IS-CAP-1** (Ubiquitous) Truncation SHALL be at a UTF-8 rune boundary, never mid-rune, and
  SHALL set `truncated` and `full_bytes`.
- **IS-CAP-2** (Ubiquitous) A truncated item that has a retrievable full body SHALL set
  `detail`. Detail is fetched by an **unsigned read** carrying `(session_id, item_id)`, on the
  `ActionTerminalWatch` precedent: gateway-routed, not forwarded to the device authenticator,
  gated daemon-side by capability plus the kill switch. It SHALL NOT introduce a device-signed
  action — PB-SYNC-5's `actionClass` is a closed fail-closed switch and capability is pinned at
  enrollment.
- **IS-CAP-3** (Unwanted) IF the requested detail is outside the daemon's bounded retention
  (proposed: the most recent 200 items per session, or 24 h, whichever binds first), THEN the
  daemon SHALL answer `unavailable`. It SHALL NOT return a partial body presented as whole.
- **IS-CAP-4** (Ubiquitous) A `journal_reseed` SHALL stay **one frame**. PB-SYNC-3 commits the
  repair atomically with its matching transport watermark and N frames cannot be — a death
  between frames leaves half a snapshot under a watermark claiming the whole
  (`internal/remotegw/relaysink.go`, `Reseed`). A reseed that would exceed the frame cap SHALL
  bound its **content** instead: cap the events half at a record count that fits, and carry the
  cut as an explicit floor the phone renders per IS-DELTA-4 ("incomplete from join"). Unresolved
  `approval_request` records SHALL survive that cap **by construction** (IS-LIFE-3). Paging the
  reseed would amend PB-SYNC-3 and needs an ADR, not a bullet in a payload schema.
- **IS-CAP-5** (Ubiquitous) The per-field caps above SHALL be **jointly bounded** by
  `MaxItemBytes`: an item carrying every field of its kind at that field's documented maximum
  SHALL serialize within `MaxItemBytes`, and so SHALL the one merge §6 sanctions — IS-DELTA-2's
  lossless concatenation of two `agent_message` increments already clipped to `MaxTextBytes`. A
  ruling that raises a per-field cap SHALL re-derive `MaxItemBytes` in the same ruling; the
  relation, not either number alone, is what ADR-009 Amendment 1 ratified. The joint bound covers
  only what the table measures in **bytes**, and a producer SHALL still clip to fit under IS-CAP-1
  rather than assume it: `prompt_lines` is capped in *runes* (40 × 200 four-byte runes is 32 000
  bytes), several §3 strings carry no per-field cap at all (`tool`, `path`, `old_path`,
  `truncation_marker`, `decisions[].id` — IS-TOOL-3 requires the marker verbatim), and JSON
  escaping can expand a byte-capped field by up to 6×.

---

## 6. Delta semantics for streamed `agent_message`

The gateway's machine→phone budget is ≤ 8 appends/s combined across journal and terminal
(`remote-phaseB-requirements.md` §6.0, ADR-007 B7), enforced by
`remotegw.DefaultAppendWindow = 125 ms`. Journal records are **never**
coalesced at the gateway (R-GW.5) — so the coalescing must happen at the producer, before the
append. With the terminal well deleted and no phone surface issuing a `terminal_watch`, no snapshot
frames are appended at all (ADR-009 (2)), so the transcript inherits the whole of the budget the
peek used to spend.

> AMENDED BY ADR-017 (2026-08-15): re-scoped, not repealed — for a `structured_chat` session
> this paragraph still holds exactly as written. A `terminal_fallback` session's `TerminalViewV1`
> stream spends from the same combined budget the peek used to spend, under the same coalescing
> sink (ADR-017 "What does NOT change," append budget), so on that session the transcript no
> longer has the whole budget to itself.

- **IS-DELTA-1** (Ubiquitous) An `agent_message` record's `text` SHALL be the increment appended
  since the previous record of that `item_id`. A consumer reconstructs by concatenation in
  cursor order.
- **IS-DELTA-2** (Ubiquitous) The producer SHALL merge pending increments for one `item_id` into
  at most one journal append per `DefaultAppendWindow`. The merge is lossless text
  concatenation. The window is a **spacing floor**, not a batching delay: an increment arriving
  more than one window after the last append for that session is admitted at once — subject to
  IS-DELTA-2a's per-target slot, which is what actually binds.
- **IS-DELTA-2a** (Ubiquitous) Admission SHALL be bounded **per target** and SHALL govern **every**
  kind: the producer SHALL hold total appends for one target, across all sessions and all kinds,
  under §6.0's combined ceiling. A per-`item_id` window alone does not bind — N concurrently
  streaming sessions multiply past it — and an overrun is not merely late: a quota-refused append
  burns an outbound seq (PB-GW-7) and the resulting gap stales journal and terminal alike
  (PB-SYNC-1). This is the governing rule. IS-DELTA-3 orders the queue; it exempts nobody from it.
- **IS-DELTA-3** (Ubiquitous) `agent_message` is the only kind merged by **text concatenation**;
  no other kind SHALL EVER have its content concatenated with another's. `tool_run` and
  `file_change` are subject instead to ADR-010 §7's **record collapse**, which merges whole
  records and never text: a `tool_run` open and its close inside one window become one record, as
  do the `file_change` records of one tool run. Every remaining kind SHALL keep its own record,
  and every kind other than `agent_message` SHALL take the head of the admission queue ahead of
  pending `agent_message` text — `approval_request` first of all.
  *Never merged* and *never delayed* are different guarantees, and only the first is compatible
  with the ceiling: an `approval_request` waits at most one window, at the front, never behind a
  backlog of prose.
- **IS-DELTA-4** (Unwanted) IF a consumer's first record for an `item_id` carries `in_progress`
  and it holds no earlier record for that id — a client that joined mid-turn, or repaired from a
  reseed whose floor cut the item — THEN it SHALL render the item as **incomplete from join**
  (an explicit leading elision) and MAY repair it via §5's detail fetch. It SHALL NOT present a
  partial body as the whole message.

---

## 7. `tool_run` summarization

Adopted from the Codex `commandActions` audit: a card reads well only if the machine, not the
phone, decides what the tool *did*.

| Field | Type | Meaning |
|---|---|---|
| `type` | string | `read` \| `edit` \| `write` \| `search` \| `execute` \| `fetch` \| `other` |
| `path` | string | file-scoped target, for `read`/`edit`/`write` |
| `query` | string | the pattern, for `search` |
| `command` | string | the argv rendering, for `execute` |

- **IS-TOOL-1** (Ubiquitous) `action` SHALL be produced machine-side by the per-CLI adapter. A
  phone SHALL NOT parse `tool` or raw arguments to infer an action.
- **IS-TOOL-2** (Unwanted) IF the adapter cannot classify the call, THEN `type` SHALL be `other`
  and the card falls back to `tool`. An unclassified call is never guessed at.
- **IS-TOOL-3** (Unwanted) IF a rendered line matches a per-CLI truncation marker (spike-SA's
  truncated-tool-output rule), THEN `truncation_marker` SHALL carry that text **verbatim**, the
  client SHALL show it as-is, and the item SHALL NOT claim to hold the underlying output. A
  "view full output" affordance defers to §5's detail fetch — never to VT-diff reconstruction.

---

## 8. Approval lifecycle

- **IS-LIFE-1** (Event) WHEN an adapter captures a pending permission, the daemon SHALL journal
  an `approval_request` carrying the immutable binding tuple of §3.5, and SHALL send a push
  wake. Expiry is daemon-authoritative.
- **IS-LIFE-2** (Ubiquitous) Every `approval_request` SHALL reach exactly one
  `approval_resolved` — **including** when it is cancelled, superseded, expired, or answered at
  the machine. A stale card SHALL dismiss on every surface, which is what that guarantee buys.
- **IS-LIFE-3** (Event) WHEN a client reconnects, unresolved `approval_request` items SHALL be
  re-delivered **in the reseed's events half, at their own cursors**. They SHALL NOT ride the
  roster half: a roster record is a set member keyed by `SessionID` whose `Cursor` is deliberately
  unset (`internal/daemon/journal.go`; PB-SYNC-8 makes a zero roster cursor a fixture rule), so it
  can neither be ordered against its own `approval_resolved` (IS-LAYER-3, IS-LIFE-2) nor hold two
  pending approvals for one session. Re-delivery is bought by **retention** instead: the daemon
  SHALL exempt an unresolved `approval_request` from journal trimming and from IS-CAP-4's reseed
  floor until its `approval_resolved` is journalled. No new reseed half, no new frame contract.
- **IS-LIFE-4** (Ubiquitous) A phone approval SHALL travel as the existing signed `ActionApprove`
  command, validated daemon-side against the binding tuple and expiry before the adapter applies
  it. This spec adds **no new signed action** — but it does add wire fields (§1). Today's sealed
  `schema.RemoteCommand` carries only `DeviceCommandAuth` plus `PushPrefs` / `Launch` /
  `GateToken` / `TTLSeconds` / `ResyncCursor`, so it needs the `ApproveReq` body (`agent_instance`,
  `interaction_id`, `content_hash`, `expires_at`) the gateway reconstructs `Control.Approve` from,
  plus the chosen `decision` id. The **decision itself is unsigned**: `ContentHash` is the signed
  tuple's one content slot and D7 spends it on the interaction content, which the phone echoes
  verbatim (IS-APR-2) and so cannot fold a choice into. It rides inside the epoch-sealed frame —
  unforgeable by the relay, alterable only by the gateway, which is the documented D4/D5 owner-uid
  residual (ADR-007) and not a new one.
- **IS-LIFE-5** (Ubiquitous) A phone-authored message SHALL travel as ADR-009's **composer-send**
  op — a remote-tier control op carrying `(session, expected_turn, text)` — not as raw `TDataIn`.
  `expected_turn` names the `turn_id` the composer was rendered against, and the daemon SHALL
  refuse a send whose value differs from the session's current turn, with a code from D10's error
  taxonomy. The carrier is not incidental: `TDataIn` is fire-and-forget with no reply
  (`internal/remotegw/lease.go`), an unroutable input frame is dropped silently
  (`command_loop.go`), and the daemon holds no `ContentKey` and reads an already-ordered byte
  stream — so neither the precondition nor its refusal can ride the keystroke plane (ADR-009 (5),
  (8)). This closes the render-vs-tap race: a tap that lands after the turn moved on is refused,
  never misapplied.
- **IS-LIFE-6** (Ubiquitous) WHERE an approval is hook-unsafe (spike-SC's Bash-with-file-path
  carve-out) the request SHALL use `mode: prompt_card`. The decision SHALL still travel as the
  signed `ActionApprove` of IS-LIFE-4; only its **application** differs — after the daemon validates
  the binding tuple, `content_hash` and its daemon-authoritative `expires_at`, the machine-side
  adapter SHALL inject the mapped keystroke into the PTY. The phone SHALL NOT author the approving
  keystroke, and this bullet SHALL NOT be read as overriding IS-LIFE-4 (ADR-007 D7: an approve is
  "never translated into a blind keystroke"; ADR-009 (4)).

---

## 9. Compatibility

- **IS-COMPAT-1** (Ubiquitous) An unknown item `kind` SHALL be skipped and SHALL still advance
  the consumer's cursor. Skipping SHALL NOT mark a stream stale — an unknown kind is not a gap.
- **IS-COMPAT-2** (Ubiquitous) Unknown fields SHALL be ignored, never fatal.
- **IS-COMPAT-3** (Ubiquitous) Adding a kind or an optional field SHALL NOT bump `v` and SHALL
  NOT bump `journal.SchemaVersion` — the precedent is the `Agent` field, which was added without
  a bump precisely so older readers keep the journal instead of rejecting every line.
- **IS-COMPAT-4** (Unwanted) IF a consumer sees an item `v` higher than it supports, THEN it
  SHALL render what it understands and mark the item degraded. It SHALL NOT drop the transcript
  and SHALL NOT error the connection.

---

## 10. What this specification does NOT change

Stated so no implementation slice reads a payload schema as licence to move a boundary.

- **Transport and crypto.** No new envelope, no new mailbox `kind`, no new seq bucket, no new
  signed action. `SenderKeyID` stays zero inbound and `EpochID` stays the epoch — both remain
  `Bucket` key material and nothing here writes to either. Frozen crypto/wire/on-disk formats
  are untouched (ADR-007 B133).
- **Rate budgets.** §6.0's ≤ 8 appends/s combined and the relay's `MailboxAppendPerMin: 600`
  are reused as-is. §6 spends the budget differently; it does not raise it.
- **Live-only input.** ADR-007 D7 as amended by B43 stands: no offline queue, ever. Items are
  the durable, replayable surface; the composer send, `resize` and the discrete ops stay
  live-only, and a disconnect still resolves as "delivery unknown / not sent". What changes is
  only what the phone *authors*: it stops sending raw keystrokes and sends IS-LIFE-5's
  composer-send op over the same lease-authorized transport (ADR-009 (5)).
- **The sanitization boundary.** The VT emulator and all sanitization stay machine-side. Items
  are produced adjacent to `internal/daemon/terminalrender.go`; raw PTY bytes never reach the
  phone, and `prompt_lines` is sanitized text, not a grid.
- **The relay.** Still self-hosted, untrusted, ciphertext-only. Nothing here makes it smarter;
  ack/replay semantics stay endpoint-to-endpoint.
- **Gateway topology.** Rendering and item production stay out of the sidecar's address space
  (ADR-007 D5); the gateway seals and forwards, and parses no item.
- **Single device.** `SenderKeyID = 0` on the wire is unchanged. Multi-device is ADR-011,
  decided separately and implemented post-v1.

  > AMENDED BY ADR-018 (2026-08-15): "single device" names the phone-per-machine axis (ADR-011's N devices on one machine), not the machine axis. Multi-machine — one phone, N independent machine pairings — is ADR-018 and is in scope for the first complete remote product; each pairing is its own `SenderKeyID = 0` relationship, and none of them is a second device against any single pairing's epoch.
