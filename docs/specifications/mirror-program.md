# Mirror: the live chat session mirror

Program plan of record. Owner-approved direction 2026-08-13 ("that's exactly the direction").
Tracker: epic `agents-tracker-dwwv`, waves `dwwv.1` through `dwwv.6`.
Look-and-feel reference: the Mirror artifact (synchronized terminal/phone replay), published 2026-08-13.

Grounding: three-agent investigation of 2026-08-13 -- (a) OSS landscape survey (Happy/Happier,
Omnara, coder/agentapi post-mortem, opencode, the ACP ecosystem), (b) per-CLI event-source matrix
against current vendor docs, (c) line-cited audit of this repo at `cd648a7`. Claims below about
current code carry the audit's citations; wave RED phases re-verify them before building on them.

## 1. Target

The phone renders a session as the Claude Code Remote Control demo renders one:

- The full transcript, live: every user message, assistant prose (markdown), every tool call as a
  compact card with args, collapsed result, and a visible running state.
- History backfills when the screen opens; sub-second-to-few-second item latency while it is open.
- The composer is always available: send mid-turn ("add feedback"), stop, approve or deny -- and
  the terminal remains fully usable at the same time. Nobody takes control; both rooms are live.
- Approvals are first-answer-wins between terminal and phone; the losing surface resolves itself.

Non-goals, ruled by the owner 2026-08-13:

- No terminal rendering anywhere in the app (ADR-009 obsidian ruling stands). CLIs without a
  structured event source keep the status card. No pseudo-chat sliced from the VT grid.

  > **AMENDED BY ADR-017 (2026-08-14):** sentence one is re-scoped to `structured_chat` sessions; sentence two gains the capability-routed sanitized terminal fallback; sentence three stands verbatim.

- No multi-tenant relay. The transport stays the existing sealed outbound-only design.

Owner rulings adopted with the direction:

- **R1 (schema)**: keep the swarm interaction schema (`docs/specifications/interaction-schema.md`);
  import from ACP only the fixed tool-kind vocabulary
  (`read | edit | delete | move | search | execute | think | fetch | other`) as an additive field.
  No rewrite: fold-by-ref increments, in-place item mutation, and replay-through-the-same-path
  already exist in our schema and pipeline.
- **R2 (markdown)**: hand-rolled minimal renderer (bold, italic, inline code, fenced blocks,
  lists, links). No new Android dependency: `verification-metadata.xml` churn is deliberately a
  human-review gate. Revisit only if the subset visibly falls short (tables are the likely gap).
- **R3 (lease)**: the lease survives as plumbing and dies as UX. Reading never requires anything;
  sending is a signed op the daemon applies locally; the kill switch and device revocation keep
  their full authority. No visible take-control anywhere in the chat path.

## 2. Architecture (to be recorded as ADR-013 during M1)

The industry verdict is unambiguous (agentapi post-mortem, Omnara pivot, Happy migration): never
scrape the terminal for structure. Mirror keeps the two planes separate:

- **The PTY is sacred.** The shim-owned PTY hosts the vendor's real TUI, byte-exact, always.
  Nothing in this program touches how the terminal works.
- **Structure arrives beside it**, per CLI, from the best native side-channel, normalized by an
  adapter `InteractionSource` into the existing item schema, over the existing
  journal -> gateway -> relay -> phone pipeline.

| CLI | Structured channel | Fidelity |
|---|---|---|
| Claude Code | hooks (5 raw rows shipped) + transcript JSONL tail as non-load-bearing enrichment | tool cards live; prose whole-message at turn end (no interactive delta source exists) |
| Codex | `codex app-server` as the session backend; TUI attaches `--remote`; daemon is a second JSON-RPC client of the same thread | full: token deltas, tool begin/end with results, native approvals, native steer |
| opencode | `opencode serve` + SSE `/event`; the TUI is already just a client of the same server | full: part deltas, tool parts, observable permissions, native message injection |
| AGY | probe for the Gemini-line hook set (per-chunk `AfterModel`) or an ACP surface; wire if present | unknown until probed |
| anything else | none | status card (ruled) |

> **THE "anything else" ROW ABOVE IS AMENDED BY ADR-017 (2026-08-14):** its fidelity column becomes "honest status card **plus** capability-routed sanitized terminal fallback"; no pseudo-chat sliced from the grid, ever.

**Inbound (phone -> machine)** is always a signed device op the daemon applies locally: composer
text and approval keys are injected into the PTY the daemon already owns (Claude), or delivered as
native RPC (`turn/steer`, approval replies) where the CLI has one (Codex, opencode). The phone
input keystroke plane and its lease remain as plumbing only.

**Honest limit, stated once**: interactive Claude Code exposes no mid-generation assistant text by
any mechanism (hooks carry final text at `Stop`; stream-json is headless-only; the transcript file
lags). On Claude the feed feels alive through tool cards and running states; token-live prose
arrives first on Codex.

## 3. The M1 design decision, explicitly

The audit suggested making approvals work by having `swarm hook` hold the `PermissionRequest`
connection until the phone answers, with the daemon writing the decision back on the held hook.
**Mirror rejects the held hook for the interactive path, on co-presence grounds**: while a
PermissionRequest hook is undecided, the CLI has not shown its own dialog -- holding indefinitely
hides the terminal prompt, which violates the program's central ruling.

Instead, M1 ships the spec's own fallback as the primary path (IS-LIFE-6 prompt-card apply):

1. The dialog appears in the terminal exactly as today (the hook returns without a decision).
2. The phone card's Allow/Deny maps to the dialog's accepted keys, injected by the daemon into
   the PTY it owns -- after validating (a) the approval tuple is still unresolved and (b) the live
   VT grid still shows the permission dialog **this request raised** (the status engine already
   classifies needs-approval; the recognized variant must be the one the request's own action
   names -- M1.8). This gate NARROWS the stray-keystroke race to one tap round trip: if the
   terminal answered a beat earlier, the injection is refused rather than typed into the composer.
   It does not close it -- the judged grid is the daemon's mirror and the keystroke travels back
   over the same wire -- and the residue is bounded by the recorded keys carrying no Enter, so a
   late digit sits in the composer un-submitted rather than acting as an answer (ADR-013 §3).
3. Resolution is emitted only by observation (subsequent hooks / grid transition), never by the
   tap -- the card cannot lie. A post-injection watchdog surfaces a non-transitioning dialog.
4. First-answer-wins falls out by construction: both surfaces answer the same dialog.

The exact key sequences are a recorded characterization fixture per Claude version (M1.1), not an
assumption. The Channels permission relay (`claude/channel/permission`, first-answer-wins by
contract) is the designated successor once it leaves research preview; M1 carries a timeboxed,
flagged spike so promotion is a config change, not a design change. Codex needs none of this: its
approvals are native RPC.

## 4. Waves

Execution style per wave: sequential TDD pipeline (sonnet/opus subagents), RED evidence quoted in
commits, adversarial review then independent verify pass, GG-4 gates green, evidence file under
`docs/verification/`, release cut and field-tested by the owner before the next wave starts.

### M0 -- Ground truth (`dwwv.1`, small)

| # | Item | Where | Test / evidence |
|---|---|---|---|
| M0.1 | Co-presence empirical test: owner TUI attach + phone `take_control` at once; do both streams live? | `internal/skeleton/serve.go:283-312`, `sessiontap.go`; stale comment `internal/tui/attach.go:65-70` | integration test `TestCoPresence_OwnerAttachAndRemoteLease_BothStreamsLive`; re-scope or retire `nx44.8` on the answer |
| M0.2 | Render the running state: `status` crosses the wire and nothing reads it | `TranscriptPanel.kt` `blockFor`; populated at `FacadeBridge.kt:120` | Robolectric: in-progress `tool_run` pulses, completed does not |
| M0.3 | Version-skew guard: unknown gateway action yields a sealed refusal, never a hanging card | `internal/remotegw/command_loop.go` default arm | `TestCommandLoop_UnknownAction_SealsRefusal`; then close `joyi` |
| M0.4 | Hygiene: done at filing (ejc7 closed; 791j/cptp/0t3o/ztlb superseded into waves) | tracker | -- |

Exit: evidence file `mirror-m0.md`; the co-presence answer re-scopes the rest of the program's
riskiest assumption. M0.2 ships with the M1 app release unless the owner wants a 0.4.3.

### M1 -- The approve moment (`dwwv.2`, medium)

| # | Item | Where | Test / evidence |
|---|---|---|---|
| M1.1 | Characterization fixture: the permission dialog's grid signature and accepted keys (allow, deny), version-stamped | `internal/adapter/claude/testdata/` | recorded fixture; refusal to proceed on unrecognized dialog |
| M1.2 | Apply-by-injection: `handleApprove` validates tuple + grid state, injects mapped keys into the PTY, stops emitting `approval_resolved` on tap (authorized test rewrite, quote old assertions) | `internal/skeleton/approval.go`, daemon-local injection API | races both directions: terminal-first tap refused; phone-first dialog consumed |
| M1.3 | Terminal-side attribution: verify resolution among the five daemon paths. CORRECTED IN-WAVE: the `PermissionDenied` hook empirically never fires on the interactive dialog path (claude 2.1.231, four real captures + binary analysis -- it is gated to auto-mode classifier denials), so no capture row was added; `answered_locally / by: owner` is IS-RES-1's honest answer for a terminal deny. Conditional shaping work filed as `agents-tracker-hgyg` | `approval.go` paths 151/262/295/326/463; evidence in `mirror-m1.md` M1.3 | `approval_resolved` carries `by: owner` vs `by: phone` correctly |
| M1.4 | Approval sheet lives in session detail (today it navigates out to the inbox); inbox entry point keeps working | `PhoneSurface.kt:1966-1969`, sheet parents `:899,1813` | Robolectric: answering from the transcript, no navigation |
| M1.5 | Approval FCM wake deep-links to the card | `internal/remotegw/push.go`, Android intent routing | handset test |
| M1.6 | Channels spike (timeboxed, flagged, non-release): `claude/channel/permission` relay | new sidecar, off by default | measurement note + promotion criteria in ADR-013 |
| M1.7 | ADR-013 written (architecture + the section 3 decision); release machine v0.10.0, app 0.5.0 | `docs/adr/ADR-013-*.md` | `mirror-m1.md`; `180i`'s final step passes: a real prompt answered from the handset, agent proceeds |

### M2 -- Chat feel (`dwwv.3`, large; mostly Kotlin)

| # | Item | Where | Test / evidence |
|---|---|---|---|
| M2.1 | Markdown subset renderer (R2): pure `String -> blocks`, escaping-safe | `ui/kit/Markdown.kt` | exhaustive unit tests incl. injection controls |
| M2.2 | Tool cards: kit component, kind glyphs (R1 vocabulary as additive `tool_kind` schema field + GG-7 row; adapter mapping incl. new `Grep`/`Glob`/`WebFetch` arms with recorded fixtures), expand/collapse, timestamps, turn separators (`TSUnixMs`/`TurnID` finally mapped) | `ui/kit/ToolCard.kt`, `claude/interaction.go:242-254`, `mobile/types.go:260-273`, `FacadeBridge.kt` | card states; separator on `turn_id` change; absorbs `5z2u`'s rulings |
| M2.3 | Incremental transcript render keyed by `item_id`: append/mutate in place, stick-to-bottom, scroll preserved (closes `esed`) | `PhoneSurface.kt` `sessionDetailRedraw` + cursor use at `:1853` | Robolectric: scroll survives an item burst; status flips mutate, not rebuild |
| M2.4 | Structured composer send (IS-LIFE-5): signed op `(session, expected_turn, text)`, gateway arm, daemon precondition + local PTY injection, `stale_turn` refusal surfaced gently; lease leaves the UX (R3): composer gated on `online` only, Take control leaves session detail, Stop becomes a signed interrupt op; `source: phone` via injection-time correlation; paste + IME (closes `570d`, `76j`, `16o`) | `internal/protocol`, `internal/remotegw`, `mobile/commands.go`, `Composer.kt`, `SessionScreens.kt:275`, `unbound-verbs.tsv` | protocol conformance + GG-7; race test: turn advances between render and tap |
| M2.5 | Working shimmer + composer states from existing session status ("Message" / "Add feedback...") | phone status dims, `Composer.kt` | state-driven placeholder test |
| M2.6 | `MailboxWait` long-poll replaces the 500 ms sleep; fallback to poll against older relays | `mobile/relay.go:667-687`, `mobile/app.go:33` | measured felt echo sub-300 ms on the handset |
| M2.7 | Increment rendering ready: fold-by-ref grows text in place with a caret while `streaming` | `phonecore/interaction.go`, `TranscriptPanel.kt` | fed by synthetic increments until M4's producer |

Release: app 0.5.x. Exit: a Claude session on the handset reads like the artifact's replay, minus
token streaming.

### M3 -- Depth (`dwwv.4`, medium)

| # | Item | Where | Test / evidence |
|---|---|---|---|
| M3.1 | ADR-014 paged interaction history (amends PB-SYNC-3): per-session read op `(session, before_item, limit)`, daemon session-item index, phone "load earlier"; plus IS-CAP-4's byte-aware reseed bound (`internal/daemon/interaction.go:36-39`) | daemon, gateway, phonecore window above `MaxItemsPerSession` | paging conformance; reseed bound negative control |
| M3.2 | Auto-backfill on session-detail open, throttled | `mobile/app.go` | cold-open shows history without tapping Repair |
| M3.3 | Detail-on-demand (IS-CAP-2): daemon retains full pre-truncation payloads in a 64 MiB LRU side store at capture time; unsigned read `(session, item_id)`; tap-to-expand fetch | `internal/skeleton` `fitItem` path, new store | full output of a truncated card retrieved on handset |
| M3.4 | Claude enrichment via transcript JSONL tail, non-load-bearing: thinking (`agent_message` + additive `thought` flag), full tool results into the detail store, `plan_update` from `TodoWrite` (shaping exists `skeleton/interaction.go:430-437`), resume id-rewrite trap handled | `internal/adapter/claude` | tail-killed control: feed unaffected, only detail degrades |
| M3.5 | `stop_reason` populated; IS-SS-1 marker decision recorded | `claude/interaction.go:183-209` | -- |

Release: app 0.6.0, machine v0.10.x. Exit: cold-open a week-old session, page its history, tap any
card for the whole output.

### M4 -- Codex full depth (`dwwv.5`, large; gated)

**M4.0 gate (timebox 1 day, may run during M2)**: against the installed binary --
`codex app-server --listen unix://` + `codex --remote unix://` in a PTY; verify the TUI drives the
thread, a second JSON-RPC client receives `item/*` live, `turn/steer` works, and an RPC approval
answer closes the TUI dialog. Fallback if any leg fails: rollout-file tail (mirror-only; no steer;
approvals stay on grid heuristics) and revisit upstream. The result is recorded in ADR-013 either
way.

| # | Item | Notes |
|---|---|---|
| M4.1 | Shim topology: app-server as shim-owned child, socket in session state dir, TUI attaches `--remote`; lifecycle + upgrade semantics | shims survive daemon restarts; app-server dies with its shim |
| M4.2 | Codex `InteractionSource`: `userMessage` -> `user_message`; `agentMessage` deltas -> `agent_message` increments folded by ref, **batched 200 ms at the adapter from day one** (the 8 appends/s gateway ceiling is real); `commandExecution` -> `tool_run` kind `execute` with args + results; turn lifecycle -> `turn_id` + status | first true token streaming through M2.7's renderer |
| M4.3 | Native approvals: phone Approve answers the server-initiated `approvalRequest` via RPC; first-answer-wins is server-side | no keystroke injection on codex, ever |
| M4.4 | `composer_send` -> `turn/steer` (mid-turn) or `turn/start` (idle); interrupt -> `turn/interrupt` | per-CLI inbound dispatch behind one op |
| M4.5 | Typed events replace the grid heuristic for codex status | pays the D1 debt deferred since Epic 14 |

Release: machine v0.11.0. Exit: the demo moment on a Codex session, prose streaming token-live.

### M5 -- opencode, AGY, last mile (`dwwv.6`, medium)

| # | Item | Notes |
|---|---|---|
| M5.1 | opencode source: shim controls the serve port; daemon subscribes SSE `/event` (`message.part.updated` deltas + tool parts, `permission.updated`); injection `POST /session/:id/message`; verify the approval response RPC pair (research flagged it unconfirmed) | same delta batching as codex |
| M5.2 | AGY probe (timebox): Gemini-line hooks (`AfterModel` per chunk) or ACP surface; wire if present, else the status card stands | no pseudo-chat, ruled |
| M5.3 | Presence suppression: the daemon knows TUI attach state -- suppress FCM interaction wakes for sessions with a live focused owner attach | better than the presence-file idea |
| M5.4 | Phone-presence in the TUI: throttled foreground ping, "phone is watching" in attach chrome | completes the co-presence story both ways |
| M5.5 | Status-card polish for non-chat sessions; composer hidden there | honest, not empty |

> **M5.2 AND M5.5 ARE AMENDED BY ADR-017 (2026-08-14):** M5.2's "else the status card stands" becomes "else AGY advertises `structured_chat=false`, `terminal_fallback=true`" (its "no pseudo-chat, ruled" note is untouched); M5.5's "composer hidden there" becomes structural — a fallback session has no structured composer, because it has no message sink.

Release: machine v0.12.0, app 0.7.0. Exit: every session in the inbox opens into a live chat or an
honest status card -- nothing in between.

> **THE M5 EXIT ABOVE IS AMENDED BY ADR-017 (2026-08-14):** it gains a third destination — a live chat, a capability-routed terminal fallback, or an honest status card, nothing in between.

## 5. Release train

| Wave | Machine | App | Field test |
|---|---|---|---|
| M0 | -- | (folds into 0.5.0) | co-presence evidence only |
| M1 | v0.10.0 | 0.5.0 | answer a real prompt from the couch |
| M2 | v0.10.x | 0.5.x | a Claude session feels like the demo |
| M3 | v0.10.x | 0.6.0 | history + detail |
| M4 | v0.11.0 | 0.6.x | Codex streams |
| M5 | v0.12.0 | 0.7.0 | everything or an honest card |

## 6. Risks and their gates

- **Codex remote-attach doc/reality gap**: M4.0 is a hard gate with a named fallback; nothing
  before M4 depends on its outcome.
- **Claude transcript tail** is internal-format and lagging: enrichment-only by design; a format
  change degrades detail, never the feed (M3.4 carries a tail-killed negative control).
- **Stray-keystroke race on approvals**: narrowed to one tap round trip by the grid-state gate +
  tuple check + watchdog (section 3); the characterization fixture refuses unknown dialogs
  outright, and the recorded keys carry no Enter, so the residue is an un-submitted digit in the
  composer rather than a submitted answer.
- **Throughput ceiling** (8 appends/s per target): adapter-side delta batching ships with the
  first streaming producer; measured before any further valve is added.
- **Channels is a research preview**: spike stays flagged and non-release until the preview gate
  drops; promotion criteria live in ADR-013.

## 7. Existing beads absorbed

`791j` -> M1, `cptp` -> M2, `0t3o` -> M3, `ztlb` -> M4/M5 (superseded 2026-08-13); `ejc7` closed
stale; `esed`, `570d`, `76j`, `16o`, `5z2u` close inside M2; `joyi` closes with M0.3; `nx44.8`
re-scoped by M0.1's evidence; `180i` is M1's exit criterion.
