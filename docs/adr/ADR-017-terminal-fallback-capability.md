# ADR-017: Capability-driven terminal fallback — the sanitized terminal returns, for the sessions that cannot be chat, and for nothing else

**Status**: Accepted (owner sign-off 2026-08-15; drafted 2026-08-14 from the owner-approved playbook)
**Date**: 2026-08-14

**Source of the direction**: [docs/specifications/remote-control-product-playbook.md](../specifications/remote-control-product-playbook.md) — RC-D4 and RC-D5 (lines 74-75), §3.1 (lines 103-120), §4.5 (lines 248-262), §4.7 (lines 276-293), §6.2 (lines 384-399), §6.3 (lines 401-472), §7 (lines 578-598), and the Wave R8 exit (lines 812-827). The playbook is the plan; this ADR is the rule.

> **SUPERSEDED ON ANDROID, 2026-08-30 — one conversation surface for every session.**
> The terminal-surface portions of T1/T2/T4/T6 and their later amendments remain the historical
> design and the machine/wire compatibility contract, but they no longer authorize a production Android terminal-fallback or
> status-card route. Opening any session on Android always enters the normal transcript and pinned
> composer-shaped shell. When chat is unavailable, the shell stays in place and shows the precise
> inline reason: `NO_CHAT`, `OFFLINE`, and `ENDED` disable input and its action; `AVAILABLE` is
> sendable. A history tear remains a transcript warning and does not by itself select another screen
> or prove that a live message sink disappeared. `TerminalViewV1`, terminal watch/control verbs, and
> capability fields remain specified for rolling wire compatibility and non-Android consumers, but
> no production Android route issues a terminal watch, renders the grid, or enters terminal control.
> This amendment changes only Android routing/rendering. It does not infer capability from transcript
> contents, authorize a composer the machine has refused, promote terminal text into interaction
> items, or weaken any authorization, sanitization, session-instance, or Stop rule below.

**Amends, narrowly and by quotation**:

- **ADR-009-structured-chat-interaction.md decision 1, line 53** — "**No terminal emulation and no raw grid anywhere in the app.**" That sentence is **re-scoped, not repealed**: it becomes the rule for `structured_chat` sessions, which keep ADR-009 exactly as written. See ruling T1.
- **ADR-009-structured-chat-interaction.md decision 2, lines 68-69** — "Both are machine-side, so **no snapshot frames are appended to a phone**". That clause alone is **re-scoped, not repealed**: snapshot frames are appended only to a `terminal_fallback` surface, under `TerminalViewV1`, and never to a structured one. Everything else in decision 2 stands verbatim and is re-affirmed — hostile-PTY sanitization stays on the trusted side, the phone core stays thin, no VT emulator crosses the gomobile boundary, raw PTY bytes never reach the phone, `internal/daemon/terminalrender.go` remains the security choke point. See ruling T4.
- **ADR-013-mirror-capture-architecture.md decision 1, table line 53** — "| anything else | none | **status card, by owner ruling.** No pseudo-chat sliced from the grid, ever |". The third column becomes "honest status card **plus sanitized terminal fallback**"; the second half of the sentence — *no pseudo-chat sliced from the grid, ever* — is **untouched and re-affirmed** (T1, T10).
- **ADR-013 Non-goals, lines 219-220** — "No terminal rendering anywhere in the phone app (ADR-009's ruling stands), and no pseudo-chat sliced from the VT grid for a CLI with no structured source. The honest status card is the answer." The first clause is amended to the T1 scope; the second clause stands verbatim.
- **docs/specifications/mirror-program.md — four rows, not one.** Playbook §3.1 directs the amendment of "ADR-013 **and the Mirror program**" as a whole (`playbook:118-120`), and Wave R1's deliverable and exit forbid a surviving copy of the replaced behavior anywhere: "no current normative/runbook source may retain the replaced behavior" (`playbook:691-692`), "the current docs have one non-contradictory direction" (`playbook:706`). Amending the program's non-goals while leaving its decision table and its AGY row saying status-card-only would leave exactly the second, contradictory source of truth that R1 exists to prevent. All four rows move together:
  - **§1 Non-goals, lines 25-26** — "No terminal rendering anywhere in the app (ADR-009 obsidian ruling stands). CLIs without a structured event source keep the status card. No pseudo-chat sliced from the VT grid." Sentence one is re-scoped to the T1 boundary; sentence two gains the capability-routed fallback destination; sentence three is untouched and re-affirmed (T10).
  - **§2 architecture table, line 63** — "| anything else | none | status card (ruled) |". This is the twin of `ADR-013:53` and takes the identical treatment: the fidelity column becomes "honest status card **plus capability-routed sanitized terminal fallback**", and *no pseudo-chat sliced from the grid, ever* remains the rule the row is protecting (T1, T10).
  - **M5.2, line 189** — "AGY probe (timebox): Gemini-line hooks (`AfterModel` per chunk) or ACP surface; wire if present, else the status card stands". Its closing clause — *else the status card stands* — is the AGY row this ADR routes to fallback, and the playbook rules on that exact provider: "Advertise `structured_chat=false`, `terminal_fallback=true` until the complete-chat table passes" (`playbook:649`). It becomes "else AGY advertises `structured_chat=false`, `terminal_fallback=true`". The row's own note — "no pseudo-chat, ruled" — is untouched (T10).
  - **M5.5, line 192** ("Status-card polish for non-chat sessions; composer hidden there") and the **M5 exit, lines 196-197** ("Exit: every session in the inbox opens into a live chat or an honest status card -- nothing in between") gain the third destination: **live chat, capability-routed terminal fallback, or an honest status card -- nothing in between.** "Composer hidden there" stays true and becomes structural: a fallback session has no structured composer to hide, because it has no `MessageSink` (T2).

**Companions** (Wave R1, drafted concurrently; referenced by number and title only): ADR-015 push-gateway split, ADR-016 Web-PKI relay TLS, ADR-018 multi-machine pairings. Standing companions: [ADR-007-remote-access.md](ADR-007-remote-access.md) (D7, the 2026-07-24 Decision 1 keystroke transport, Decision G), [ADR-009-structured-chat-interaction.md](ADR-009-structured-chat-interaction.md), [ADR-010-adapter-structured-capture.md](ADR-010-adapter-structured-capture.md) (the `InteractionSource` seam this extends), [ADR-011-multi-device-epochs.md](ADR-011-multi-device-epochs.md) (M9's sender-bound lease, which the control generation inherits), [ADR-013-mirror-capture-architecture.md](ADR-013-mirror-capture-architecture.md), [docs/specifications/interaction-schema.md](../specifications/interaction-schema.md) (the normative item fields; IS-LIFE-5 is amended by obligation, not rewritten here).

## Context

ADR-009 retired the terminal peek on 2026-08-07 and gave two reasons, both of which survive this ADR. The first is a product judgement: "a character grid is the *machine's* view of an agent, shrunk onto a handset. The thing the owner needs from a phone is not a smaller terminal" (`ADR-009-structured-chat-interaction.md:41-43`). The second is a security property that got *stronger* by the deletion: "The A7 'no control-sequence injection at the phone' property gets stronger, not weaker. It was structural because the phone displayed pre-sanitized text; it is now structural because the phone has no grid to inject into" (`:198-200`).

ADR-013 then closed the remaining hole in the same direction. Its decision-1 table gives "anything else" the status card "by owner ruling" (`ADR-013-mirror-capture-architecture.md:53`), because the alternative — deriving a transcript from the grid — is disproved rather than disliked: spike S-A is PARTIAL, FAIL on overlay transitions, DEGRADED on truncated tool output, and "`tool_input` never recovered at all" (`ADR-013:20-22`), agreeing with three independent industry post-mortems (`:16-19`).

What has changed is not the evidence. It is the product's shape. RC-D4 makes Claude Code and Codex the first complete structured providers and leaves OpenCode and AGY launchable (`playbook:74`); RC-D5 says terminal fallback "exists only for providers without complete structured chat" and is "sanitized, read-only by default" with raw control "an explicit temporary action" (`playbook:75`). Between those two decisions sits the honest observation that a status card is *honest* and *useless*: a developer who launched an OpenCode session from Swarm can see that it is running and can do nothing about it, on a phone that already receives every byte the machine has already sanitized for exactly this purpose. `internal/daemon/terminalrender.go:11-20` still runs, still calls `vt.SnapText`, and still describes itself as "the SECURITY choke point"; `internal/vt/render.go:134-143` still strips "C0 incl. LF/CR, DEL, C1, bidi, zero-width" and says so as a rule, not an implementation note. ADR-009 decision 2 deliberately kept that renderer alive with two machine-side consumers and struck only "the phone *rendering a grid to a human*" (`ADR-009:71`).

So the question this ADR answers is narrow: **which sessions may show a human that already-sanitized text, and under what authority may a human type back into one.** Everything ADR-009 and ADR-013 decided about *structure* — where it comes from, what it may be derived from, what an approval is — is out of scope here and is re-affirmed below so that nobody reads this document as permission to reopen it.

One more thing has changed since ADR-009: the number that would govern a control window no longer has a live justification. `internal/phonecore/lease.go:48-57` explains `TakeControlTTL = 15 * time.Minute` as clearing PB-INPUT-5's ">= 60 s sustained-typing criterion" while staying "under the cap" (`MaxControlSessionTTL = 30 * time.Minute`, `lease.go:62`), and then records that "§6.0's 60 s biometric freshness was the third [wall], and it is withdrawn with the requirement that owned it" (`lease.go:54-56`, ADR-007 B133). Read plainly: the surviving rationale justifies a **range** — more than a minute, less than thirty — and not the value. Fifteen minutes is currently an unexplained point inside it. Ruling T7 gives it a reason.

## Decision

### T1. The no-grid rule is re-scoped to `structured_chat`, not repealed

Every session instance has exactly one phone surface, selected by its capability record (T2), and the selection is the daemon's, never the user's:

- A **`structured_chat`** session keeps ADR-009 **exactly**: transcript of interaction items, tool cards, approval cards, composer, no terminal surface, no `terminal_watch`, no visible take-control ceremony (ADR-013 decision 1's R3, `:81-84`). ADR-009's line 53 is the rule here, unamended and unqualified.
- A **`terminal_fallback`** session may render `TerminalViewV1` (T4): the machine-sanitized snapshot the trusted renderer already produces, presented as what it is — a terminal — with an honest header naming provider, detected version, and the missing capability that cost it structured chat (`playbook:280`).
- A session that is neither — a provider with no adapter and no PTY-worth-showing, or one whose `terminal_fallback` capability is false — keeps the honest status card. ADR-013's status card is not deleted; it is now one of three destinations rather than the only alternative to chat.

**What the fallback is still not.** Raw PTY bytes never reach the phone. ANSI, OSC and any other control sequence never reach the phone. No VT parser crosses the gomobile boundary and none is written for Android. ADR-009 decision 2 (`ADR-009:61-71`) survives **except the single clause re-scoped in T4** — "no snapshot frames are appended to a phone" (`ADR-009:68-69`) — including its statement that `internal/daemon/terminalrender.go` "remains the security choke point"; this ADR adds a third machine-side consumer of that renderer alongside decision 2's two, and changes nothing about what the renderer emits or where it runs. The A7 property changes its ground — it was structural-by-absence for a phone with no grid, and returns to being structural-by-sanitization for a fallback screen, which is exactly what it was before ADR-009 and is what `vt.SnapText` is written to guarantee (`internal/vt/render.go:134-143`: "no terminal control sequence can escape, and no Unicode bidi/zero-width rune can visually spoof what is displayed"). Stating that honestly is the point: the property is not weakened, but its *proof obligation moves back onto the sanitizer*, and Wave R8's adversarial ANSI/OSC/Unicode fixtures (`playbook:822`) are that obligation, not a nice-to-have.

### T2. The per-session capability record is the router, with fenced live-chat transitions

An adapter publishes one authoritative capability record at session launch (`playbook:394-399`). It carries, at minimum:

| Field | Content |
|---|---|
| `provider` | Adapter identity — `claude`, `codex`, `opencode`, `agy`, … |
| `provider_version` | The **detected** version of the installed CLI, not a configured or assumed one |
| `adapter_revision` | The revision of the Swarm adapter that produced the record |
| `probe_result` | The outcome of any runtime probe the adapter ran at launch, recorded even when it passed |
| `structured_chat` | True only when every mandatory row of T3 passes against `provider_version` |
| `terminal_fallback` | Whether the sanitized view may be offered at all |
| `interrupt`, `steer`, `approvals`, `history` | The individually testable seams of `playbook:386-392` |

Four rules bind:

1. **Launch facts are immutable per session instance.** Provider identity/version, adapter revision, interrupt support, and terminal authority are fixed for the life of the instance. An adapter upgrade still takes effect only on a newly launched/resumed instance. The only runtime-changing field is `structured_chat`, under rule 2 below; Android keeps the same conversation surface while that field changes.
2. **A visible history tear withdraws chat until the exact live sink is re-proven.** `structured_gap` is the shim/daemon spool boundary of `playbook:375-376`: an unrecoverable gap emits an exact boundary, temporarily publishes `{structured_chat:false, terminal_fallback:false, terminal_control:false}`, and forbids a fabricated completion. Unrecoverable describes the missing history, not the surviving channel. After the boundary is durable, a later clean drain may adopt the retained sequence space and continue folding retained/future events behind the marker. A fresh machine-local proof of the exact current session instance's message sink may then publish `{true,false,false}` through a cursor-ordered `capability_transition`; the marker remains permanently visible and the missing range is never invented.
3. **The phone renders from the record and infers nothing.** "The phone renders from that record; it never infers support from whether a transcript happens to be empty" (`playbook:396-397`). An empty transcript is an empty transcript.
4. **No route to the fallback from a healthy structured session.** Wave R8's exit is explicit: "Claude and Codex expose no route to it when their structured capabilities are healthy" (`playbook:826-827`). There is no power-user escape hatch, no long-press, no debug toggle in a release build. RC-D5 is a routing rule, not a default the user may override.

`session_capabilities(machine, session_instance)` is daemon-authored state (`playbook:423`), not a phone-side heuristic and not an adapter call the phone can make.

> **2026-08-30 recovery fence.** The recovery in rule 2 is not an adapter upgrade and cannot
> promote a provider that was authored without structured chat. The proof binds the current
> session instance, the latest gap generation, and a freshly initialized backend or shim submit
> transaction in the current daemon process. Session replacement or any newer gap invalidates it
> before capability publication. The phone accepts the complete validated transition only for an
> already-known row with the same instance and unchanged launch facts; a transition can toggle only
> `structured_chat` and can never grant a terminal route or terminal control. This narrowly
> supersedes older "never in place upward" wording for recovery of an originally structured
> session; it does not restore a complete-history claim or change the Android screen kind.

### T3. The complete-chat capability contract is the bar for `structured_chat=true`

A provider may advertise `structured_chat=true` only when **all** of these pass against a recorded provider version (`playbook:578-598`, adopted here normatively):

| Capability | Mandatory behavior |
|---|---|
| User messages | Terminal- and phone-originated prompts enter one ordered stream with stable ids and source attribution. |
| Assistant messages | Final prose is captured without terminal scraping. Streaming deltas are optional, but their availability is declared honestly. |
| Tool lifecycle | Start and terminal outcome are represented; missing full output is marked truncated/degraded, never invented. |
| Turn lifecycle | Idle, active, waiting, completed, interrupted, and failed states are provider-authored or adapter-proven. |
| Composer | Idle start and, where supported, mid-turn steer have explicit outcomes and expected-turn enforcement. Unsupported steering is refused with legible UX. |
| Interrupt | A semantic interruption reaches the current turn and is observed. |
| Approvals | Pending requests re-deliver after reconnect; resolutions dismiss on every surface; first answer wins. |
| History | Cold-open reconstructs the retained conversation in order and exposes an honest retention floor. |
| Version skew | Unknown provider versions fail to the terminal fallback or a status-only refusal, not optimistic structured mode. |
| Survival | Daemon restart preserves every structured event accepted by the shim/backend. A proven gap is marked at its exact boundary and degrades the session; retained/future capture resumes only behind that boundary, so the missing range is never silently bridged. |

Two clarifications that are decisions, not commentary. **Claude may satisfy complete chat without token-live assistant deltas** (`playbook:596-597`), which is ADR-013's "honest limit, stated once and not worked around" (`ADR-013:57-61`) surviving intact rather than being quietly repaired. And the *Version skew* row is where fallback earns its existence: it converts an unknown Claude or Codex build from an optimistic guess into a routed, labelled, read-only terminal — which is strictly more useful than ADR-013's status card and strictly less dangerous than guessing a dialog key (`ADR-013:120-136`).

### T4. `TerminalViewV1` — versioned full coalesced snapshots, and no invented patch language

Terminal observation for a fallback session uses a versioned `TerminalViewV1` (`playbook:453-458`):

- **Full coalesced snapshots**, each carrying a **monotonically increasing revision**, the session instance, rows/columns, UTF-8 text, and a reset/resync marker. "Prefer full coalesced snapshots over an invented patch language in v1."
- **Size, line and rate bounds are declared in the remote profile** (T5), so a phone knows the ceiling it is rendering under rather than discovering it.
- **A slow observer drops superseded snapshots and receives the newest complete revision.** This is not new machinery: `internal/remotegw/coalesce.go:167-180` already holds one newest snapshot per session, latest-wins, releasing when the shared append slot frees, and its own comment records that "being coalesced is never an error" (`coalesce.go:171`). `TerminalViewV1` makes that behavior a wire contract with a revision the phone can reason about instead of an emergent property of the sink.
- **The trusted machine renderer strips control sequences and supplies replacement glyphs for invalid Unicode** (`playbook:457-458`). The stripping half already exists and is the T1 choke point; the replacement-glyph half is new work on the machine side and must not be simulated on the phone.
- **Watch grants no input authority** (`playbook:458`). `terminal_watch` / `terminal_unwatch` are offered only when `terminal_fallback=true` (`playbook:424`), and a granted watch is a read.

The existing `TerminalSnapshot` / `terminal_watch` bodies are **not deleted**. They remain reachable only under the legacy remote profile (`playbook:953-954`); the new fallback uses the versioned view and control bodies. ADR-009 decision 2's sentence — quoted whole, including the clause this ADR reverses: "Both are machine-side, so **no snapshot frames are appended to a phone**: `TerminalSnapshot` and `terminal_watch` stay on the wire unchanged — no protocol change, nothing deleted — but no phone surface issues a watch" (`ADR-009:68-70`) — is amended in exactly one clause: a **fallback** phone surface issues a watch and is appended snapshot frames, under `TerminalViewV1`, and no structured surface ever does. The wire half of the sentence is not amended at all; `TerminalSnapshot` and `terminal_watch` still stay on the wire unchanged.

### T5. `RemoteProfileV1` is what routes all of this, and it is sealed, not negotiated

The asynchronous E2EE mailbox has no local `hello`, so the machine sends a sealed `RemoteProfileV1` during reconciliation naming accepted action/body versions, interaction-schema version, **`TerminalView` version**, and **session-capability record version** (`playbook:403-410`). Four concepts stay distinct and must not be collapsed into one another: local transport-protocol features, paired-device authorization tier, remote semantic profile, per-session provider capabilities.

Every durable semantic mutation binds the selected profile and, where relevant, the session instance, and signs a canonical hash of its **full body**: "symmetric sealing alone is not command authorization" (`playbook:407-409`). Unknown actions, profiles, body versions, session instances or capabilities receive **sealed stable refusals** (`playbook:450-451`) — an old client degrades legibly and never receives a malformed screen.

**The field list above is this ADR's contribution to the profile, not the whole of it.** `RemoteProfileV1` is one shared wire struct that companion R1 decision records may also add fields to, and later waves will add more. **Companion ADRs may add profile fields; the profile *version* is the compatibility unit, and no ADR owns the struct.** Two consequences follow and are stated so that two R1 changes do not race on one struct: a field addition is a profile-version decision, taken once across the R1 set rather than independently per ADR; and each ADR that adds a field carries its **own** GG-7 field-table obligation against `docs/specifications/protocol.md` in its own commit, so the obligations compose instead of one discharging another's. The `TerminalView` version and the session-capability record version are the two fields this ADR owns. **The current instance of that rule inside R1 is ADR-016**, which adds three fields to the same struct — `relay_tls_policy`, `relay_host` and the pin set (`ADR-016:194`) — and acknowledges this ruling where it adds them (`ADR-016:55`): those three and this ADR's two land in one profile version selected once for the set, and ADR-016 carries its own GG-7 field-table obligation rather than riding this one. Any later R1 or post-R1 record that adds a field joins the same version decision on the same terms.

### T6. Read-only by default; control is an explicit, visually persistent, temporary, non-transferable generation

The fallback opens **read-only** and is useful there — "It remains useful for monitoring without any lease" (`playbook:282`). Entering control is one signed semantic operation:

- **The affordance is explicit *and* visually persistent, and a confirmation dialog alone does not satisfy this.** "**Control terminal** is explicit, visually persistent, and capped by the existing signed 15-minute horizon" (`playbook:283-284`). All three properties bind independently, and the middle one is the easiest to lose: a sheet that grants control and then disappears is explicit and not persistent, and it leaves a user typing into a live generation they have to *remember* they opened. So for the whole life of a generation the fallback screen continuously displays that control is live, its remaining horizon, and a release control in the same view — no drawer, no menu, no second navigation step. This is the one place where the fallback deliberately restores the ceremony ADR-013's R3 removed from the chat path (`ADR-013:81-84`), and it restores it for the reason R3 removed it: R3 struck the ceremony because there was nothing to take, and here there is.
- **`terminal_control_begin(machine, session_instance, operation_id, profile, expires_at)`** is bound to the selected remote profile, the paired device's command signing key, and the fallback session instance. It **mints one non-transferable generation bound to that sender** (`playbook:460-462`).
- **`terminal_input(machine, session_instance, control_generation, bytes)`** and **`terminal_control_keepalive(...)`** are **not individually signed**. They ride the E2EE frame's authenticated sender/sequence plus that device-bound confirmed generation. **This is the sole exception to full-body signatures in the remote protocol**, and it is deliberately the same exception that already exists: ADR-007's 2026-07-24 Decision 1 rules that live input frames "travel as **sealed mailbox envelopes** … carry a **monotonic mailbox sequence number**", that the gateway seq-gates them with `crypto.MailboxReceiver`, and that "Keystrokes are NOT individually signed — they ride the lease per D7" (`ADR-007-remote-access.md:411-422`, the ruling's own heading at `:411`: "**Decision 1 — keystroke transport: sealed + sequence-gated, riding the control lease**"). The generation is what the lease was; the authority is no stronger and no weaker than the plane that ships today.
- **It is never a bearer token another device can use** (`playbook:464-465`). When ADR-011 lands, M9's rule — the lease is bound to the `SenderKeyID` that opened it, and an input frame from another sender is dropped fail-closed (`ADR-011-multi-device-epochs.md:65`) — applies to the control generation verbatim and without a second decision. Until then the single-device registry is what makes "the sender" unambiguous, and that is stated rather than assumed.
- **Only the active fallback screen may send raw input** (`playbook:285`). This is a routing rule in its own right and not merely a consequence of the severance list: the sender of a `terminal_input` frame must be the screen currently foregrounded and displaying that session's fallback view. A background task, a notification action, a widget, an aggregate-inbox row, or any code path that is not the live screen has no route to `terminal_input` — not a refused one, none — and the app therefore has no place to hold a byte on the way to one. T8's leave-screen and backgrounding triggers are what enforce this rule at the daemon; this bullet is what forbids the app from building a second producer in the first place.
- **Wrong profile, wrong sender, wrong generation, wrong instance, replayed sequence, read-only authorization tier, background-triggered end, revocation, kill switch and expiry all fail closed** (`playbook:469-471`).
- **`terminal_input` is not an approval verb and must never become one.** An approval answered from a fallback screen still travels as the signed `ActionApprove` of IS-LIFE-4, or the button is not shown. The facade ban in `mobile/interaction_screencoverage_test.go:100-137` — no `approvekeystroke`, `answerprompt`, `typeapproval`, … because "the phone must never author the approving keystroke" — is **untouched and still correct**, and this ADR adds no verb that would satisfy it by another name.

Interrupt follows the same discipline: the fallback shows Stop only when the capability record says `interrupt=true`, and otherwise hides or refuses it with "This provider version has no safe remote interrupt." It never guesses a control key (`playbook:272-274`).

### T7. The 15-minute horizon, and the reason it is fifteen

The daemon-clock deadline is **no later than a signed 15-minute horizon**. The daemon may grant less. There is no silent renewal, and no keepalive extends the signed horizon (`playbook:284`, `:467-469`).

The number is inherited from `phonecore.TakeControlTTL` (`internal/phonecore/lease.go:57`), and the rationale recorded beside it no longer selects it. That comment justifies a range — above PB-INPUT-5's ">= 60 s sustained-typing criterion", below `MaxControlSessionTTL = 30 * time.Minute` (`lease.go:48-62`) — and its third wall, "§6.0's 60 s biometric freshness", is recorded in the same comment as "withdrawn with the requirement that owned it" (`lease.go:54-56`, ADR-007 B133). **So the horizon is re-derived here rather than re-cited.**

The horizon's job under this ADR is one thing only: **it bounds the window during which raw bytes are accepted on frame authentication plus a confirmed generation rather than on a per-body signature** (T6). It must be:

- **long enough that the ceremony is not the experience.** A user diagnosing a stuck OpenCode run types in bursts separated by reading; a horizon measured in single minutes turns a legitimate ten-minute debugging session into four re-authorizations, and re-authorization fatigue is how a confirmation stops being read. The same floor that produced the original number — sustained typing must not be interrupted by the signature expiring — still binds: a 60 s floor (`lease.go:51`) against a 900 s horizon, more than an order of magnitude of headroom.
- **short enough that authority which cannot be revoked in real time is bounded.** Every fast severance path (T8) requires the daemon to be reachable, the transport to be alive, or the app to be running. The horizon is the wall that holds when **none** of those fire: a phone that is off, out of coverage or in an attacker's hands after the transport dropped. Fifteen minutes is the maximum age of a generation that no live signal can reach, and it is half `MaxControlSessionTTL`, so a control session can never outlive the control-session cap on the strength of its horizon alone.
- **the number already implemented**, so adopting it costs no migration, mints no second constant, and leaves one 15-minute wall in the system rather than two nearly-equal ones.

`lease.go`'s comment is amended in the same change that implements this ruling, so the constant's stated reason is this ADR and not a withdrawn requirement.

### T8. Severance is a list, not a timeout, and no byte is ever queued

The generation ends on **every** one of these (`playbook:288-293`, extending ADR-009 (6), `ADR-009:115-121`):

| Trigger | Mechanism |
|---|---|
| Leaving the fallback screen | Local input stops immediately; best-effort signed `terminal_control_end`; the daemon ends on receipt |
| App backgrounding | Same, and it is a **first-class trigger** in its own right — ADR-009 (6) named it precisely so the guarantee does not rest on the transport drop that happens to follow (`ADR-009:128-141`) |
| Transport loss | Missing-keepalive deadline, below |
| Horizon expiry | Daemon-clock, T7, never extended |
| Kill switch | `Server.SeverAllRemoteControl` (`internal/protocol/server.go:1612`), synchronous at the daemon |
| Device revocation | Synchronous at the daemon; ADR-011 M5's rotation applies unchanged when it lands |
| Session replacement / instance change | Synchronous; a generation is bound to one session instance and dies with it |

One citation in that table is a correction and is flagged rather than left to be discovered: **`SeverAllRemoteControl` is at `internal/protocol/server.go:1612` today**, and this row supersedes ADR-009 (6)'s `internal/protocol/server.go:1482` for the same symbol (`ADR-009:115-121`), which the file has since moved past. It is flagged here so that a reader comparing the two ADRs does not conclude they name different mechanisms: same function, same authority, same B133 status as "the only surviving mitigation" — only the line number is stale.

The liveness rule: **the active screen sends a keepalive at most every ten seconds; a missing keepalive severs the generation within 30 seconds** (`playbook:288-291`, `:467-469`). "Within 30 seconds" is a ceiling on the daemon's own detection, not a promise about the network.

**No queued bytes, ever.** Input is live-only, "the app does not buffer or replay it" (`playbook:285`), which is D7's live-only rule as amended by B43 and re-affirmed by ADR-009 (5) — "no offline queue, ever, and B43's reasoning for why one is *unbuildable* from these commands is not reopened" (`ADR-009:103-104`) — and by ADR-011's invariant list ("Live-only input (D7 as amended by B43). Multi-device adds no queue.", `ADR-011:89`). The playbook prices this as a release budget: "Automatically replayed uncertain messages or raw input | zero" (`playbook:863`).

**Owner typing remains possible throughout.** Decision G's concurrent owner-and-phone control is untouched (`ADR-007` Decision G; `ADR-011:88`), and ADR-013's co-presence finding — proven by `TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive`, `internal/skeleton/copresence_test.go` (`ADR-013:65-70`) — is why both surfaces stay live. The UX must warn that simultaneous typing can interleave (`playbook:286-287`). It must not "fix" this by evicting the terminal.

### T9. Six composer delivery states

ADR-009 (6) gives the composer a visible `pending → sent → refused` state and rules that "A send that cannot get a lease is shown refused, not silently swallowed" (`ADR-009:123-126`). That three-state vocabulary is **extended, not replaced**, to the six states of `playbook:248-262`:

| State | Meaning | Allowed transition |
|---|---|---|
| `draft` | Local text only. | `pending` or deletion |
| `pending` | The app is online and is acquiring/using the provider's message primitive. | `sent`, `refused`, `uncertain`, or `outcome_unknown` |
| `sent` | The daemon accepted the operation against the expected turn and attributed the injected/native message. | Terminal item acknowledgement may fold into the same item |
| `refused` | The daemon definitively rejected it — stale turn, revoked device, offline target, unsupported action. | User edits or retries as a new operation |
| `uncertain` | Connectivity disappeared after send began and no authoritative outcome has arrived yet. | `operation_status` reconciliation changes it to `sent`, `refused`, or `outcome_unknown` |
| `outcome_unknown` | A crash crossed an irreversible provider side-effect boundary and Swarm cannot prove whether the provider accepted the text. | **Never auto-retry.** The user inspects the transcript and deliberately creates a new operation |

Three rules ride with the table. **An uncertain, unknown, or offline draft is never automatically replayed** — this is the same prohibition as T8's, one layer up. **Live input must not become an unseen queue merely to make the UI look successful** (`playbook:259-260`), which is PB-INPUT-2's "invisible acquisition is not invisible suppression" (`ADR-009:123`) restated for the failure directions ADR-009 did not enumerate. And **there is one active turn per session instance**: the first accepted idle-start wins, and a mid-turn send is accepted only when the provider advertises native steering, otherwise receiving a stable refusal "instead of waiting invisibly" (`playbook:260-262`).

The same vocabulary governs `session_launch` — "Launch uses the same `pending`, `sent`, `refused`, `uncertain`, and `outcome_unknown` vocabulary as composer operations" (`playbook:223-225`) — so a user meets one delivery model across the product rather than two. **`session_launch` itself is B144's decision, not this one** (`ADR-007-remote-access.md` B144(b)): B144 lifts D8's Phase-2 deferral and owns the op's shape — `launch_presets`, the signed preset revision and the `stale_preset` refusal. This ruling constrains only its *delivery vocabulary*, and the two records are read together: B144 says what a launch is and what may authorize it; T9 says how its outcome is named on the phone.

**Amendment obligation, named here and discharged in `interaction-schema.md`, not in this ADR.** IS-LIFE-5 (`docs/specifications/interaction-schema.md:389-398`) currently defines the composer-send op as carrying `(session, expected_turn, text)`. It must be amended, in the commit that implements `composer_send`, to add: (a) **`expected_input_revision`**, mandatory for Claude, whose enforcement is the shim-wide input transaction of `playbook:437-445` — acquire the one lock every owner/remote input writer shares, snapshot and check the characterized input region is empty and the revision still matches, then write text-plus-submit framing without releasing the lock, refusing a pre-existing or changed terminal draft; (b) the **one-active-turn rule** of the paragraph above, since IS-LIFE-5's `expected_turn` names a turn but does not today rule on who may start one; and (c) the six delivery states as the normative extension of its lifecycle. Those are schema shapes and belong in the schema. This ADR only makes them owed, exactly as ADR-009 made its own obligations owed rather than inlining them (`ADR-009:217-251`). GG-7 applies to every new op and field: `internal/protocol/protocolmd_test.go` and `protocolmd_bidi_test.go` fail the build when `docs/specifications/protocol.md`'s field table and the `wire` structs disagree, so `protocol.md` moves in the same commit.

> **DISCHARGE OF THE AMENDMENT OBLIGATION — Wave R6, the commit that implements `composer_send` (review fix-pack, finding B13). PARTIAL, and this is the accounting of which part.**
>
> The obligation above says the amendment lands "in the commit that implements `composer_send`". That commit is this one, so the obligation is answered here rather than left to accrue silently.
>
> **(b) is DISCHARGED.** IS-LIFE-5's `expected_turn` is enforced daemon-side against the daemon's own turn state, and a send rendered against a turn that has moved on is refused `stale_turn` having typed nothing. The R6 review fix-pack extended the same rule to `turn_interrupt` (finding B7), which had no turn coordinate at all and typed the cancel sequence into whichever turn was current on arrival — so "one active turn per session instance" now governs BOTH ops that can act on a turn, which is what the rule was for.
>
> **Later amendment — message FIFO, strict Stop.** The R6 sentence above records the behavior
> that shipped then. `composer_send.expected_turn` is now advisory signed context: the daemon
> serializes accepted messages per session and dispatches each against current provider state.
> `turn_interrupt.expected_turn` remains required and strict. The one-active-turn obligation is
> preserved by reserving the native id returned by `turn/start` so an immediate second message
> steers that turn rather than starting another.
>
> **(c) is DISCHARGED on the wire and PARTLY on the phone.** The refusal vocabulary is coded per verb (`stale_turn`, `interrupt_unsupported`, `structured_unsupported`, `unavailable`), and the ops resolve — visible success and visible refusal — rather than being fire-and-forget. `outcome_unknown` and `uncertain` ride the existing `operation_status` reconciliation.
>
> **(a) is NOT DISCHARGED, and the composer ships with the gap open.** `expected_input_revision` is absent, and so is the enforcement it names: the shim-wide input transaction of `playbook:437-445`. `internal/skeleton`'s `injectComposerText` writes text-plus-CR through the session tap with NO check that the terminal's input region is empty and NO lock shared with the owner's own keystrokes. **The user-visible consequence, stated plainly: if the owner has a half-typed line in the CLI's composer when a phone send lands, the phone's text is APPENDED to it and the CR submits the concatenation — a message nobody wrote.** That is the same harm the adjacent "refused, never truncated" rule protects against for the other half of one message.
>
> **Why it is not discharged, rather than why it was missed.** Two halves, two different reasons:
>
> - *The transaction* requires `internal/shim`, which owns the PTY writer. Only the writer's owner can make "read the input revision, refuse if it moved, write" atomic against the owner's own keystrokes; anything the daemon does above that seam is a check with a race under it. `internal/shim` is outside Wave R6's scope, and a half-transaction that LOOKED atomic would be worse than none.
> - *The weaker half* — gate the send on the input region being empty, without a transaction — was measured and is not reachable either. Nothing characterizes the input region: the adapter seams are `ApprovalApplier`, `TurnInterrupter`, `InteractionSource` and `HostProber`, and none of them says where the composer is or whether it holds a draft. Deriving "the composer is empty" from the raw grid would be exactly the never-guess move IS-TOOL-2 forbids one layer down and that `interruptTurn` already refuses to make for the cancel key. Trading a disclosed gap for an undisclosed wrong answer is not an improvement.
>
> The obligation therefore **stands open**, with its blocker named: it closes when `internal/shim` grows the shared input transaction and an adapter seam characterizes the input region. `docs/verification/r6-chat.md`'s CANNOT YET states the consequence in the user's own terms, and `internal/skeleton/chat.go`'s `injectComposerText` carries the same disclosure at the site.

> **AMENDMENT — (a) IS NOW DISCHARGED, BY A DIFFERENT AND WEAKER MECHANISM (2026-08-26, conversation surface Slice 0, `agents-tracker-bzfe`).**
>
> **What changed the answer was the question, not the difficulty.** The paragraph above is correct that nothing characterizes the CLI's input region, and correct that deriving it from the grid would be the never-guess move IS-TOOL-2 forbids. Both halves of that reasoning stand. What it did not notice is that **the transaction does not need the input region at all.**
>
> The shim owns `ptyWriter`, the PTY master's only serialized writer, with a mutex it already takes on every write. It can therefore answer one question absolutely: **has anybody written to this PTY since the last submit.** That is a fact about the PTY, not a claim about what the agent has drawn on it, and it needs no adapter seam, no characterization and no heuristic.
>
> `shimwire.TypeSubmit` carries one whole message. The shim checks that count is zero, writes the text, waits `submitframe.Gap`, writes the carriage return — **all under one hold of that same lock** — or refuses having written nothing. Holding across the gap is the point: while it elapses nothing else may reach this PTY, which is exactly what stops the owner's keystroke, or a second phone send, landing between a message's two halves. `internal/skeleton/supervision.go:16` already records the same shape for the passive supervisor.
>
> **What is discharged, precisely.** The obligation's operative clause — "acquire the one lock every owner/remote input writer shares … then write text-plus-submit framing without releasing the lock, refusing a pre-existing or changed terminal draft" — is met. **`expected_input_revision` itself is NOT added to IS-LIFE-5 and is now not needed:** the revision never leaves the shim, because only the predicate has to cross. Refusals surface as `CodeInputBusy` (`schema/chat.go`). **The phone's rendering of that code is a separate obligation and it was not discharged by this transaction** — a fact recorded here because the sentence this replaces asserted it. `input_busy` existed in exactly one place in the tree, `internal/protocol/schema/chat.go`, and was absent from `MachineRefusalCodes.toToken` (`ui/ErrorRouting.kt`), so it fell through to `ErrorState.UNKNOWN` and the reader was shown the generic "Your message was refused and not delivered." The refusal the whole slice exists to produce could not be said. The mapping, carrying the drawing's own sentence — "Not sent — the terminal's input line was not empty." — lands in the conversation-surface wave as plan row 0.6.
>
> **It errs safe by construction.** A draft typed and deleted back to empty still refuses — the counter measures bytes written, not what survives on the line. False refusal was chosen over prompt corruption deliberately.
>
> **What remains open, and it is smaller than what closed.** A shim predating the transaction answers `ErrSubmitUnsupported`, and the daemon degrades to the two unlocked writes — reachable only between a daemon upgrade and the shim restart that replaces it, and disclosed at the site. The merge is also exclusively a property of the keystroke branch: `resolveMessageSink`'s backend arm never touches the PTY, and the only `ComposerKeys` implementor in the tree is Claude, so the class has a known exit the day Claude gains a structured sink.
>
> **Why this was found now.** The R1 ruling of the conversation surface (`docs/specifications/chat-surface-plan.md`) makes both surfaces live at all times, which turns this from a disclosed edge into the ordinary case — and the audit committee then found the *worse* half nobody had stated: two phone sends racing each other produce one submitted concatenation and one empty submit, with no owner involved at all. `internal/skeleton/s0_writerserialise_test.go` reproduces both and is the failing-first evidence.

### T10. Nothing is promoted from the fallback into structured chat

This is the ruling that keeps the two amendments above narrow, and it has no exceptions:

- **No content may be promoted from the terminal fallback into structured chat** (`playbook:115-116`). Not a scraped user message, not a parsed tool result, not a heuristic status, not "just the last line" of a completion.
- **Terminal scraping never produces interaction items.** ADR-013's rule stands verbatim (`playbook:119-120`; `ADR-013:41-45`), on the evidence that produced it (S-A PARTIAL, `tool_input` never recovered — `ADR-013:20-22`), and the playbook lists "Terminal-derived pseudo-chat" as an explicit non-goal of the first complete product (`playbook:1025`).
- **An adapter earns `structured_chat` by satisfying T3, and by no other route.** Partial structured sources "may improve status cards internally but do not unlock chat" (`playbook:652`).
- **A session degraded by `structured_gap` keeps its already-accepted items read-only at the exact boundary and does not backfill from the grid** (`playbook:376-380`). The durable hook stream may continue folding retained and future events behind that explicit boundary; this neither backfills the missing range nor restores a claim of complete history.

## What does NOT change

Listed so that no implementer "fixes" one of these on the way past, and so that no future reader mistakes this ADR's scope for a general re-opening of the phone surface.

- **The PTY is sacred.** ADR-013 decision 1: the shim-owned PTY hosts the vendor's real TUI, byte-exact, always; "nothing in this program reads the terminal for structure and nothing in it alters how the terminal works" (`ADR-013:39-45`). The fallback reads the *renderer's* output, which is downstream of the tap and touches neither the PTY nor the TUI.
- **No raw PTY bytes, ANSI or OSC ever reach the phone**, and there is no on-phone VT parser. ADR-009 decision 2 survives except the single clause re-scoped in T4 — "no snapshot frames are appended to a phone" (`ADR-009:68-69`) — and everything else in it stands (`ADR-009:61-71`); `internal/daemon/terminalrender.go` and `internal/vt.SnapText` remain the choke point and the sanitizer.
- **No pseudo-chat sliced from the VT grid**, for any provider, in any mode (T10).
- **The phone never authors an approving keystroke.** ADR-007 D7, IS-LIFE-6, ADR-013's non-goal — "The phone never authors a keystroke. It sends a signed decision id; the daemon types." (`ADR-013:227-228`), and the facade ban at `mobile/interaction_screencoverage_test.go:100-137` are untouched.
- **No visible take-control in the chat path**, and no lease gate on reading. ADR-013's R3 (`:81-84`) is unchanged; the control ceremony introduced here exists **only** on a `terminal_fallback` screen, which by T2 is never a structured session's screen.
- **The relay stays untrusted, ciphertext-only, self-host, and no smarter** (ADR-009 (9), `ADR-011:87`). `TerminalViewV1` is journal-adjacent payload to it, nothing more.
- **Live-only input, no offline queue** (D7/B43), and the two-phase `prepared → executing → applied | refused | outcome_unknown` discipline for every durable semantic op (`playbook:430-435`).
- **PB-SYNC-1's bucket model, the envelope format, the AAD field set and the nonce discipline** — this ADR adds ops and bodies, and touches no crypto surface.
- **The append budget** — `<= 8 appends/s` combined per target (`ADR-009:156-165`). A fallback session's snapshot stream spends from the same budget the peek used to spend, under the same coalescing sink; T4's drop-superseded rule is what keeps it inside.

## Consequences

### Positive

- OpenCode and AGY become genuinely usable from the phone without a single line of terminal-derived structure, which is the outcome RC-D4 and RC-D5 were written to buy (`playbook:74-75`).
- Version skew stops being a cliff. ADR-013's recognizer refuses an unknown Claude dialog and leaves the card pending (`ADR-013:128-136`, `:252-254`); with T3's *Version skew* row the session now degrades to a labelled read-only terminal instead of to a status card, so an uncharacterized CLI update costs fidelity rather than reach.
- The capability record turns three previously implicit questions — which surface, which verbs, which explanation — into one daemon-authored object that the phone renders and never guesses (T2), and makes "why is there no composer here" answerable in the UI.
- The 15-minute constant acquires a stated reason for the first time since B133, and the code comment stops citing a withdrawn requirement (T7).
- The unsigned-frame exception does not grow. It is the same exception ADR-007's 2026-07-24 Decision 1 already made, re-bound from a lease to a generation, and it inherits ADR-011 M9's sender binding without a second ruling (T6).
- The six delivery states give launch and composer one vocabulary, so `outcome_unknown` is a state the user has already met by the time a crash produces one.

### Negative, stated plainly

- **A terminal surface returns to the Android app**, one week after ADR-009 deleted one and recorded the discarded peek UI as "real work thrown away" (`ADR-009:206-207`). That is a real reversal of a recent deletion, and the honest accounting is that ADR-009 deleted the peek as *the* session surface for *every* provider, while this ADR restores it as *a* session surface for *some* — a narrower thing, on a routing rule that did not exist on 2026-08-07, but it is still a grid on a handset, and the Android work ADR-009 discarded is partly re-earned rather than recovered.
- **The A7 no-injection property returns to resting on the sanitizer.** Under ADR-009 it was structural-by-absence; under T1 it is structural-by-`SnapText`. That is a weaker kind of proof — one function instead of one missing screen — and it puts Wave R8's adversarial ANSI/OSC/Unicode fixture work on the critical path rather than in the nice-to-have column (`playbook:822`).
- **A second input plane exists again.** `terminal_input` is raw bytes authorized by a generation rather than by a per-body signature. It is bounded (fallback sessions only, live-only, 15 minutes, 30-second severance, no queue) and it is not new authority — but it is a second thing to reason about in every security review, forever, and reviewers will have to be told each time that it is the same exception and not a new one.
- **The immutability rule has a visible cost.** An OpenCode session launched before its adapter is upgraded stays in fallback for its whole life. That is the correct trade against a surface silently changing mode under a user's thumb (`playbook:291-293`), and it will still read as a bug to whoever meets it first.
- **Three destinations instead of two.** The M5 exit's "live chat or an honest status card -- nothing in between" (`mirror-program.md:196-197`) was a clean promise; "chat, fallback, or status card" is a promise with a routing table behind it, and the routing table has to be legible in the UI or the honesty is lost.
- **The status card does not go away**, so the fallback adds a surface without retiring one. Providers with no PTY worth showing, and sessions where `terminal_fallback=false`, still need it maintained.

## Alternatives Considered

- **Keep ADR-013's status card as the only answer for incomplete providers.** Rejected on product grounds, and only on product grounds — the ruling was correct for the question it was asked (`ADR-013:53`). What changed is that RC-D4 keeps OpenCode and AGY launchable in the shipped product (`playbook:74`), which makes "you may start it and then only watch a card" the experience of a supported path rather than of an edge case. Everything the ruling was *protecting* — no pseudo-chat, no scraping, no invented items — is preserved by T10 rather than traded away.
- **Let the fallback feed the structured transcript, via ADR-009 decision 2's generic fallback adapter.** Rejected on evidence, twice recorded: S-A is PARTIAL with an unrecovered `tool_input` (`ADR-013:20-22`), ADR-009 already loaded that adapter with two unfinished production rules and a FAIL condition it had to finish before shipping (`ADR-009:209-215`), and the playbook forbids terminal-derived pseudo-chat outright (`playbook:1025`). Showing a terminal *as a terminal* is honest; showing it as chat is the failure mode three post-mortems named.
- **Send raw bytes and render them with a VT emulator on the phone.** Rejected on ADR-007 Decision 2's rationale, reaffirmed in full by ADR-009 (2): hostile-PTY sanitization stays on the trusted side, no VT emulator crosses the gomobile boundary, and raw PTY bytes never reach the phone (`ADR-009:61-71`). It would also import the VT dependency that raised this repo's Go floor into the mobile artifact.
- **A patch/delta terminal protocol instead of full coalesced snapshots.** Rejected for v1 (`playbook:455`): a patch language is a second thing to get right adversarially, and the existing sink already implements latest-wins coalescing with a released newest snapshot (`internal/remotegw/coalesce.go:167-180`). Full snapshots plus a monotonic revision make "the phone is behind" a legible state rather than a corrupted one.
- **Sign every `terminal_input` frame.** Rejected as the playbook rejects it (`playbook:462-465`) and as ADR-007's 2026-07-24 Decision 1 already rejected the equivalent ("per-keystroke MAC rejected", `ADR-007:412`). A per-frame signature would buy attribution the AEAD sender and the device-bound generation already provide, at a per-keystroke cost on the latency path PB-INPUT-5 budgets. The exception is held to exactly two body types and one live generation, which is what keeps it an exception.
- **Make the fallback available to Claude and Codex as a power-user escape hatch.** Rejected: RC-D5 says a structured provider "never exposes a terminal or visible lease in its normal phone experience" (`playbook:75`), Wave R8's exit tests for the absence of the route (`playbook:826-827`), and an escape hatch is exactly how a fallback outlives its replacement — the failure ADR-009 (3) named when it dated the peek's deletion to one slice (`ADR-009:75-83`).
- **Let an adapter upgrade promote a live fallback session to structured chat.** Rejected: `playbook:291-293`. A surface that changes kind mid-session invalidates whatever the user was reading, and a promotion path is a route by which a scraped history could be presented as structured history — which T10 forbids at the content layer and this forbids at the lifecycle layer.
- **A shorter control horizon (60 s or 5 min) with silent renewal.** Rejected: silent renewal makes the signed horizon decorative, and T7's floor is the sustained-typing criterion the original constant was chosen against (`lease.go:48-57`). The daemon may still grant less than 15 minutes, which is the safe half of this alternative without its dishonest half.

## Amendment — 2026-08-20 (Wave R8 scoping review)

Wave R8's scoping review found that six of this ADR's rulings are unenforceable as written against the code they land on, and that two questions listed below as undecided are in fact answerable from the tree. This amendment answers the two and closes the six. It **adds no new destination and reverses no ruling**; every clause below is a fail-closed narrowing.

**T2-a. An absent capability record is the status card, and both verbs are refused.** T2 rule 3 says the phone renders from the record and infers nothing. It did not say what "no record" means, and no record is the common case: sessions launched before this ADR ships, resumed sessions, sessions re-adopted by a daemon-restart reconcile, and sessions started from the TUI all reach the phone with `capabilities` absent (`schema.go:244-249` makes that absence wire-distinguishable on purpose). **A session whose record is absent gets the honest status card; `terminal_watch` is refused; `terminal_control_begin` is refused.** "No record" and `terminal_fallback=false` take one code path, not two. Every session-creation path in the daemon authors a record, and a path that does not is a defect the fail-closed default contains rather than a fallback the phone improvises.

**T2-b. `structured_chat && terminal_fallback` is an invalid record and is rejected wherever it appears.** Today the pair is consistent only by construction (`internal/skeleton/capability.go:375-376` sets `terminal_fallback = !structured`) and by the one degrade path (`internal/protocol/schema/capability.go:29-38`). Nothing forbids the inconsistent record, and a gate that tests one of the two booleans enforces T2 rule 4 only for as long as the derivation stays right. **The record carries a validity rule — the two booleans are mutually exclusive — checked where the record is authored, where it is decoded off the wire, and where it is decoded on the phone; and every gate is written over both booleans (`terminal_fallback && !structured_chat`).** This converts "no route to the fallback from a healthy structured session" from a property of the derivation into a property a malformed, stale or attacker-supplied record cannot violate.

**T2-c. The capability gate binds the legacy terminal path too.** T4 keeps `TerminalSnapshot`/`terminal_watch` alive "only under the legacy remote profile". The production profile ships as a zero value, so "the legacy profile" is presently indistinguishable from "any profile"; and the legacy path carries no session-scoped gate at all — `internal/remotegw/command_loop.go:612-621` routes the watch straight to the watcher without reaching the device authenticator, and `handleTerminalSubscribe` gates only the kill switch, the remote-gateway capability and the presence of a tapper. A downlevel or compromised app that merely asks therefore peeks a healthy Claude session. **The session capability gate applies to `terminal_subscribe`/`terminal_watch` unconditionally and regardless of profile, scoped to the remote tier exactly as the kill-switch gate already is, so the owner's view of the owner's machine is untouched.**

**T4-a. Snapshots carry a view epoch as well as a revision, and every epoch's first snapshot carries the reset marker.** A revision alone is not sufficient, because the render loop is per invocation and the gateway's supervised watcher re-runs it after every transport hiccup, re-seeding a fresh emulator each time. A counter restarted at 1 while the phone holds revision N makes the phone's "drop anything not greater" rule discard every subsequent snapshot, and the user is left staring at a plausible, wrong, frozen screen. **Each snapshot carries a `view_epoch`, minted per render-loop start and changed by any re-seed or session-instance change, and a `revision` strictly increasing within that epoch. The phone's rule is: a differing epoch is a hard reset that discards prior state; within one epoch, only a strictly greater revision is accepted. `reset` is true on the first snapshot of every epoch on every path, including the path where the initial snapshot fails to decode and nothing is pushed. Exactly one producer per session may publish into the coalescer's per-session slot.**

**T4-b. A watch has a horizon, and staleness is shown.** The slow-observer guarantee is a property of a slow *sink*, not of an *absent* observer: the watcher ends a watch only on an explicit unwatch or a gateway close, so a phone that goes offline mid-watch leaves the machine rendering, sealing and appending full screens indefinitely against the shared append budget, building a backlog the phone then replays. **A watch is renewed on the same discipline as the control keepalive and expires without renewal; transport loss unwatches; the liveness predicate the render loop already polls every tick is widened from the kill switch alone to kill switch, capability and watch liveness; the phone skips a replayed backlog to the newest revision without rendering the intermediates; and the fallback screen displays a staleness indicator derived from the snapshot's own age, so "the machine went quiet" is never rendered as "the terminal is idle".**

**T4-c. Sanitization additions, and one property the machine cannot supply.** The trusted renderer additionally drops U+2028 and U+2029 — a line-separator spoof that splits one grid row into two on the phone with no control byte present — along with U+00AD, U+180E, U+2060-U+2064, U+FFF9-U+FFFB and the U+E0000-U+E007F tag block, clamps combining-mark depth per cell, and produces replacement glyphs for invalid Unicode as an explicit, tested behavior rather than as a side effect of rune iteration. **One half of the A7 property cannot be supplied machine-side at all:** implicit bidi from strongly-RTL characters reorders a line with no control character present. The fallback body therefore lays out each row under a forced LTR paragraph direction. That is a layout attribute, not terminal emulation; it crosses no boundary this ADR draws, and without it the ADR's stated "no Unicode bidi rune can visually spoof what is displayed" is false. **The fallback body also never routes a snapshot line through a markdown, annotated-string or link-detection pipeline: a terminal line is literal monospace text, and re-interpreting it is a phishing surface no machine-side sanitizer can see.**

**T5-a. Zero-valued profile fields fail closed, and this is the last wave that publishes into an empty field.** No `RemoteProfileV1` field carries `omitempty`, and the production profile is presently constructed with three of its fields set and the rest zero. **The phone reads `terminal_view_version == 0` as "no fallback exists", `capability_record_version == 0` as "record untrusted", and any zero bound as "clamp to a conservative built-in" — never as "unlimited".** Wave R8 is the first wave to publish a non-zero profile version; the "no deployed reader to break" argument that lets R8's own field additions join version 1 rather than bump it **expires with R8**, and the next record that adds a field inherits a real compatibility decision.

**T6-a. The authorization tier for `terminal_control_begin` is `device.ActionControl`.** This was listed below as undecided; the code already answers it. `internal/skeleton/deviceauth.go:19-27` maps `ActionTerminalControlBegin` and `ActionTerminalControlEnd` to `device.ActionControl` alongside launch, kill and take_control. That mapping is ratified rather than re-derived: entering control over a real terminal is at least as consequential as taking the control lease, and a read-only device is refused.

**T6-b. A session degraded by `structured_gap` may watch, and may not control.** This was listed below as undecided. Control authority is granted only where `terminal_fallback` was **authored true at launch**, never where it was derived by degradation. `SetStructuredChat` forces `terminal_fallback` true to give the user something to look at, not to hand them a keyboard; a degraded Claude session still has a live TUI whose input region is uncharacterized — the gap this ADR discloses under `expected_input_revision` — so raw bytes there can concatenate onto an owner's half-typed line; and refusing is reversible where granting is not. **The mechanism is a distinct daemon-authored `terminal_control` field on the capability record, not a phone-side derivation from `terminal_fallback`.** Two fences bind it: the degrade path never touches `terminal_control`, so a later "consistency fix" mirroring the `terminal_fallback = true` line cannot silently invert this ruling; and the record-merge path preserves `terminal_control=false`, so a reconcile after a degrade cannot re-grant control. **A degrade is machine-local in origin** — a proven hook-spool gap, or backend loss — and no remote-reachable path induces one; a phone-inducible degrade would be a privilege escalation whose payoff is a live peek onto a Claude session's terminal.

**T6-c. The keepalive is bound to the live foreground screen exactly as input is, and the daemon expires an idle generation on its own clock.** "Only the active fallback screen may send raw input" is unenforceable if a background coroutine, a scheduled job or a service-hosted timer may hold the generation open for the full horizon with no screen displaying it. **`terminal_control_keepalive` is emitted only by the same live foreground composition that owns `terminal_input` — same routing rule, same fence — and the daemon's expiry fires on an idle generation with no inbound frames at all, never driven off frame arrival.** The phone-side rule is the app's contract; the daemon-side timer is what holds when the app does not.

**T6-d. The phone has no resize authority over a fallback session.** This ADR grants none; the silence is now closed rather than reversed. The fallback renders the machine's geometry under the existing clamp. A phone-driven resize mutates the owner's live TUI, which is the worst instance of the interleaving harm this ADR requires the UX to warn about. The resize/input race is tested in its dangerous direction instead: an owner-initiated resize racing a phone byte in flight and racing a snapshot revision.

**T6-e. Authority is re-evaluated per frame and per emission, not only at the severance triggers.** Matching the discipline already in the tree, where the peek gate is re-checked before every emission and the liveness predicate on every render tick: **every `terminal_input` frame re-evaluates kill switch, device registration, capability record and generation liveness, and every snapshot emission re-evaluates the capability gate,** so a session degraded, revoked or killed mid-stream stops within a tick rather than at whichever trigger the phone next happens to send.

**T6-f. On severance, held bytes are dropped and never flushed.** The keystroke path holds bytes for a coalescing window by design, and the natural implementation of "release control" flushes them — which converts live-only input into a short offline queue and defeats T7's no-queue rule at the one buffer that actually exists. **On any severance trigger the paced-but-unsent bytes are discarded, the composing text is discarded rather than submitted, and each is recorded on the undelivered ledger as undelivered rather than replayable.**

**T8-a. Everything bound to "the session instance" binds to a minted identifier.** This ADR binds the capability record, the control generation and every snapshot to a session instance, and makes session replacement a synchronous severance trigger; the repository has no such identifier, only a session id that survives a shim restart, a resume and a daemon restart. **A per-incarnation session-instance identifier is minted at shim spawn and persisted in the session's own directory beside its capability record; it is carried in the record, in every snapshot and in every control body; a generation whose instance no longer matches is refused; and the watcher's supervised reconnect across a replacement surfaces to the phone as an epoch reset with a changed instance, never as a seamless continuation.**

**T8-b. Backgrounding severs directly.** ADR-009 already obliged this and the phone core still records the opposite — that backgrounding severs by way of the disconnect it forces. That answer is by-consequence and depends on a connectivity choice that could be revisited. **Backgrounding is a severance trigger in its own right, independent of transport.** The test that pins the by-consequence answer is amended in the same change as a strengthening, which is the only shape such an edit is allowed to take.

**Gate note.** The Kotlin gates that ban the retired peek are stated over legacy symbol names, so a new verb spelled differently would clear them while the intent — no phone surface issues a watch — is routed around. **The ban widens to any watch-shaped verb and to the terminal variant of the mono well, with a single-file allowlist naming exactly the one fallback screen, plus the additions this wave owes: no structured or chat screen may name the fallback render path, and that path is unreachable without a capability read. The retired peek symbols stay banned by name, and the net assertion count rises.**

## Amendment — 2026-08-20 (Wave R8 GREEN round 3)

Round 3's review found that three of the amendment clauses above are stated over a mechanism
the code could not supply, and that one of them was unreachable over the transport the product
actually uses. These clauses close that gap. Each is a narrowing or a relocation of an existing
binding; **none widens what a phone may do.**

**T6-g. A control generation is bound to the SIGNING DEVICE, the session, its instance and the
profile — and NOT to a connection.** The implementation stored generations on the daemon
connection that minted them. `remotegw.Gateway.ForwardCommand` dials a **fresh daemon
connection per command** and closes it on the reply, so a generation minted by a signed
`terminal_control_begin` was gone before the first `terminal_input` arrived: measured on the
assembled remote-tier server, `code="stale_generation"` with **zero bytes at the PTY**. It
failed closed, so it was never an exploit; it made T6 unreachable by the only path the product
has. **Generations live in a server-wide registry keyed by the minted generation id.** What
authorises an unsigned frame is what T6 already said authorises it: the E2EE seal's own
authenticated sender, possession of the unguessable 128-bit generation the server minted and
returned only to an authenticated, device-signed begin, and T6-e's per-frame re-evaluation of
the kill switch, the **signing device's** continued registration, the capability record, the
session instance and both walls. The connection identity was never one of those controls.

Two consequences are stated rather than left implicit. **The generation is a bearer secret**,
and the reply carrying it crosses the gateway — which is already a trusted component on this
path, because it forwards the input frames themselves, so this adds no new trusted party.
And **`terminal_control_end` releases by (session, signing device)** rather than by connection:
a release arrives on a fresh connection like every other command, so "this connection's
generation" named nothing the product could produce, and every release answered
`stale_generation` while the phone was told its control had not been given back.

**T8-c. The severance of a terminal generation carries the same race fence as the lease's.**
`Server.severControl` bumps a generation counter **before** snapshotting, so a `take_control`
publishing after that snapshot re-checks and fails closed rather than escaping the sever; its
own comment says so. The terminal plane had neither the bump nor the re-check, so a
`terminal_control_begin` landing between the snapshot and the sweep escaped the sever outright
— and a survivor is live again the moment the kill switch goes back on, which is verbatim the
resume defect T8's synchronous severance exists to prevent. **A terminal sever bumps its own
counter before it sweeps, and a begin re-checks that counter under the registry lock when it
publishes.**

**T8-d. The session instance binds an OBSERVABLE incarnation.** T8-a says the identifier is
"minted at shim spawn" and that a replacement mints a new one. The implementation minted only
when a session had none, and the repository has exactly one shim-spawn path, which always
mints a fresh session id — so the instance was the session id under another name, and T8-a's
replacement clause had no production path at all. **The instance is persisted together with
the incarnation it was minted for (the shim's pid): a matching pid is an adoption, a differing
pid is a replacement and re-mints, and an UNKNOWN pid adopts** — because a side-file written
before this rule carries none, and reading that as a replacement would show every session on
the machine an epoch reset for no shim restart.

**T4-d. A watch that ends without the phone asking blanks the phone's copy, and a live screen
renews on its own clock.** T4-b's horizon is enforced from the gateway: an expiry cancels the
peek's context, so the daemon emits nothing, the sink's blanking path never runs, and the phone
keeps its last grid — with no staleness signal, because the stream-stale flag is set by desync
events and the machine heartbeat keeps arriving. That is exactly "the machine went quiet
rendered as the terminal is idle", introduced by the horizon T4-b adds. **A reaped watch and a
transport-loss unwatch publish an empty snapshot for each session they end; an explicit unwatch
does not, because that is the phone saying it stopped looking.** And because an idle fallback
screen on an idle session produces no redraw inside the horizon, **the renewal is driven by a
clock the live foreground composition owns and tears down, which renews and never watches.**
T6-c's ban is on a background emitter holding **raw input authority** open with no screen
displaying it; a watch grants no input authority, and a timer the composition cancels is the
composition.

**T4-e. The sanitizer's drop set is a Unicode PROPERTY, not an enumeration.** T4-c listed rune
ranges. The list was reviewed twice and still leaked, measurably: U+206A-U+206F, U+1D173-U+1D17A
and the four Hangul fillers all survived it. An enumeration makes the default ALLOW, which
inverts this ADR's own "when in doubt refuse rather than render". **Every rune in `Cf`, `Zl`,
`Zp` or `Other_Default_Ignorable_Code_Point` is removed. Those the terminal gave no cell to are
DROPPED; those it laid out — today the default-ignorable Letters, i.e. the Hangul fillers — are
REPLACED BY A BLANK, because dropping them would shift every column to their right against a
grid that did not move.** The split is decided by measurement against the emulator and re-run
as a test, so a future Unicode release that contradicts it fails rather than silently eating a
column.

**T2-d. The router's answer, not the record's field, is what crosses to the UI.** T2 rule 3
says the phone renders from the record. It does not say WHICH read: the facade handed the
platform layer the record's raw `terminal_control` boolean, so a valid record on a machine
publishing `terminal_view_version == 0` — every machine deployed before this ADR — routed to
the status card while the UI was told it had a keyboard. **Every capability the UI reads is the
router's conclusion over the record AND the machine's published profile, never a record field
read directly**, which is the rule the composer predicate already stated and the terminal
predicate now shares.

## Amendment — 2026-08-20 (Wave R8 CLOSING round: the wave lands as the READ HALF, and the control half is parked)

This amendment is written after the closing review of Wave R8. It changes what the wave
CLAIMS more than it changes what the wave DOES, and that is the point: the previous rounds'
evidence described a control plane that no product path can execute.

**C0. R8 SHIPS THE READ HALF. THE CONTROL HALF IS PARKED as its own slice, with
preconditions.** `protocol.TerminalInputSink` (`internal/protocol/remote_terminal.go`) has NO
production implementation — grep outside tests returns the declaration and the type assertion
and nothing else, and `internal/skeleton.coreAPI`, which is what is passed as `srv.d`, has no
`TerminalInput` method — so `handleTerminalInput` takes the `op_not_implemented` arm for every
frame the product can produce. There is also no screen affordance: no take-control control is
reachable in the shipped app. **The wave's stated exit — "launched and safely monitored AND
CONTROLLED from the fallback" — is UNMET on the control half**, and every claim in the R8
evidence and in the rounds above that says or implies otherwise is superseded by this
paragraph.

The positive corollary is true and load-bearing and is stated in the same breath: **the
raw-input attack surface in the shipped product is currently ZERO.** Nothing can type into a
terminal from a phone, because there is nothing on the machine that would accept it.

The protocol-side control work — the signed begin, the generation registry, the horizon, the
keepalive clock, the per-frame re-checks — is reviewed, correct and KEPT. It is kept as an
UNREACHABLE export, and B94's reachability ledger must describe it as exactly that. An
unreachable export that a ledger claims is wired is the fence rot B94 exists to prevent.

**C1. PRECONDITION FOR RESUMING THE PARKED SLICE: raw input is BEARER-AUTHORISED and T6 says
otherwise.** `terminal_input` and `terminal_control_keepalive` carry no device identity:
`SealTerminalInputEnvelope` (`internal/phonecore/command.go`) builds a `DeviceCommandAuth`
with no `DeviceID`, so `forwardControl` sets `DeviceID: ""`, and `liveTerminalGeneration`
never compares the SENDER of a frame to `gen.deviceID`. Since the epoch `ContentKey` is
per-machine and granted to every paired device, and the begin reply is sealed to one shared
`ReplyTarget`, **a paired READ-ONLY device could read a control-tier device's generation id
and type under it.** It is moot today because no sink exists. It is NOT moot the moment one
does. **No control sink may be wired until the generation is bound to the sending device's
identity and that binding is checked per frame.** This is a precondition, not a
recommendation.

**C2. T8 is AMENDED: the trigger table's "Transport loss" and "Session replacement" rows were
false as written.** Round 3 correctly moved generations to a SERVER-WIDE registry, because the
gateway dials a fresh daemon connection per command and a connection-scoped generation is one
no phone could ever use. That move removed the connection binding that made a dropped
transport sever immediately. The table is corrected to:

| Trigger | Mechanism, as of the closing round |
|---|---|
| Leaving the fallback screen | Unchanged: local input stops, best-effort signed `terminal_control_end`, daemon ends on receipt |
| App backgrounding | Unchanged, and DIRECT (T8-b): `App.EnterBackground`, reached from `PhoneActivity.onPause` via `PhoneSurface.release` |
| Transport loss | **There is no persistent phone→daemon connection on the control plane to lose.** The connection that mints a generation is closed before the first byte could ever be typed. The phone's liveness is the MISSING-KEEPALIVE clock (`TerminalKeepaliveTTL`, swept on the server's own ticker) plus T8-b's phone-side severance. "Disconnect severs synchronously at the daemon" is withdrawn as unbuildable under the per-command-connection gateway, not merely unimplemented |
| Horizon expiry | Unchanged: daemon clock, T7, never extended |
| Kill switch | Unchanged: synchronous at the daemon |
| Device revocation | Unchanged: synchronous at the daemon |
| **Session kill / session delete** | **Synchronous at the daemon** (`Server.severTerminalControlForSession`, called from `handleKill` and `handleDelete`). New in the closing round; it had no trigger at all before |
| Session replacement / instance change | **On the server's own clock**, in the T6-c sweep (`severReplacedTerminalGenerations`): a generation whose bound incarnation is no longer the one the session's capability record names is dropped. It is NOT severance at the INSTANT of replacement, because the daemon has no notification seam that tells the protocol server an incarnation was re-minted. **Building that seam is a precondition of the parked control slice**, alongside C1 |

"Refused on the next frame" is not severance and is not accepted as one anywhere in this
table: the case severance exists for is a phone that will never send another frame.

**C3. `TerminalViewV1` IS ON THE WIRE, and until the closing round it was not.** T4 and T4-a
were implemented as a producer and never as a message: `RenderTerminalView` minted the epoch,
the revision and the reset marker, and its ONE caller — `RenderTerminal` — discarded all three
and passed instance `""`. No producer and no consumer of `view_epoch` existed on any wire
path. The consequence is a correctness defect of the READ half, which is the half that ships:
**a phone watching a session REPLACED under the same id saw the new incarnation as a seamless
continuation**, which is exactly what T4-a and T8-a exist to prevent.

The fields now cross end to end. `Control.terminal_view` carries `TerminalViewV1` on the SAME
`terminal_snapshot` op as the legacy `terminal` body, so nothing negotiates and no
`RemoteProfileV1` version moves (ADR-016's profile-version coordination is parked; racing that
struct to ship an epoch would have been the larger change, not the smaller one). The sealed
mailbox plaintext gains the five fields as `omitempty` SIBLINGS of the frozen
`TerminalSnapshot` keys, so a frame carrying none of them is byte-identical to the shape that
wire has always had. `docs/specifications/protocol.md` documents the body at field level and
`TerminalViewV1` is now reflected by the GG-7 drift check — it was not, which is why the
check could not have caught a fully documented type that never reached a wire.

**C4. T4-b's staleness indicator was INERT, and a screen that silently freezes is the worst
failure mode this surface has.** Three things were true together: `TerminalGrid.ageMs` was
hardcoded to `0L` because no machine-authored render time was on the wire; `streamStale` is a
SEQUENCE-GAP flag that by construction does not fire when a machine simply stops sending; and
`PhoneSurface.reconcileTerminalWatch` opened a watch only when the DISPLAYED SESSION CHANGED,
renewing unconditionally otherwise — while `TerminalWatcher.Renew` is a documented no-op for a
session with no live watch. So ONE lapsed 60-second horizon ended the stream PERMANENTLY for
that screen, the phone renewed into nothing forever, and the user read a frozen grid labelled
fresh. Both halves are fixed: the age is derived from `rendered_at` (zero means UNKNOWN, never
"just now"), and a lapsed watch is RE-ESTABLISHED rather than renewed harder.

**C5. The routing fence was EVADABLE BY THIS WAVE'S OWN INDIRECTION.** The closing review
appended `bridge.terminalFallbackBinding(id).watch()` to a structured chat screen and every R8
gate stayed green: the bans are stated over the SHAPE OF A FACADE CALL SITE, round 3 moved
those call sites behind `TerminalFallbackBinding`, whose verbs are named `watch`/`unwatch`/
`renew`, and the binding was handed to any caller for any session id with no capability read
anywhere on the path. That is this wave's own finding 8 — "renaming the verb is evasion" —
reopened by the fix for it. **The answer is structural and a ban list is only its second
half**: the binding's constructor is PRIVATE, its one factory performs the capability read and
answers NULL for a session the machine did not route to the fallback, and `.watch()` on a
structured session therefore has no receiver rather than a rule forbidding it. The reviewer's
exact probe is a permanent test, applied to a synthetic mutant of the real screen through the
SAME predicate the real scan uses.

**C6. Degrade-on-read may not launder an invalid record into a more privileged valid one
(T2-b).** `lookupCapabilitiesLocked` applied `SetStructuredChat(false)` — which FORCES
`terminal_fallback = true` — to whatever the disk held, with no validity check. The invalid
`{structured_chat:true, terminal_fallback:false, terminal_control:true}` came back as the
valid `{false, true, true}`, granting `AllowsTerminalControl()`: a transform running from LESS
VALID to MORE AUTHORITY. The degrade is no longer applied to a record whose ROUTING BOOLEANS
are already inconsistent — both of Validate's boolean clauses, checked as Validate writes
them. (The first fix guarded only the control-without-fallback clause, so the
mutual-exclusion shapes `{structured_chat:true, terminal_fallback:true, ...}` still
laundered into grants; closing round 2 found it and the guard and its fence now cover all
three invalid shapes.) It deliberately does not apply Validate's session-instance clause at
this seam: that clause is a T8-a fact the transform cannot launder, it is already enforced
fail-closed by `AllowsTerminalWatch` on every read, and refusing on it here would make every
record written before instances existed unreadable — a behaviour change this finding does not
ask for and a standing test pins against.

**C7. A per-emission gate needs its own fence.** Deleting the emission-callback capability
re-check left `./internal/protocol/` green INCLUDING the test named for it, because that test
is satisfied by the PER-TICK clause alone. The two are separable at exactly one moment and the
fence now lives in it: the render loop's FIRST emission happens at loop start, before the
ticker has fired once, so a record withdrawn between the subscribe gate and that emission is
caught by the emission re-check and by nothing else.

**C8. THE STALENESS FIELDS MUST SURVIVE THE FACADE, and until the closing round nothing said
so.** C3 put `rendered_at` and `session_instance` on the wire and C4 made the screen derive its
age from them, and both were fenced — but the seam BETWEEN them was not. `App.Peek` builds the
`swarmmobile.Snapshot` the phone actually reads, and no Go test asserted it copied either field
across; `mobile/types.go` declared them and the Kotlin gate asserted the SCREEN READS
`renderedAtMillis`, so a `Peek` that dropped them left `./mobile/...` green. This is C7's defect
class one package over: two fences for one property, either of which can pass while the property
is false.

It is not cosmetic. A dropped `RenderedAtMillis` is a zero, `ageOf` reads zero as UNKNOWN, and
`watchLapsed(0)` is FALSE FOREVER — so **both halves of C4, the honest staleness indicator and
the re-establishment of a lapsed watch, are dead together**, silently, with every other fence in
this wave still green. The rule is therefore stated as a ruling and not as an implementation
note: **any field the fallback screen derives a safety property from must be fenced at the
facade seam it crosses, behaviourally, and not only where it is produced and where it is read.**
`mobile/r8r4_snapshotidentity_test.go` is that fence, and it pins the other direction too — a
machine that sends no render time yields ZERO, never the phone's own clock, because substituting
it reports an arbitrarily old screen as rendered just now.

## Amendment — 2026-08-21 (Wave R8 CLOSING round 2)

The closing round's own review found three defects in the READ half — the half C0 says ships.
Two are correctness defects a user meets, one is an evidence defect. All three are closed in the
tree that carries this amendment; none of them widens the wave's scope and none of them touches
the parked control half.

**D0. THE PHONE'S HARD RESET READS `reset`, AND NOT THE EPOCH ALONE.** T4-a states the ordering
rule as "a differing epoch is a hard reset; within one epoch, only a strictly greater revision is
accepted", and it also states that `reset` is "true on the first snapshot of every epoch on every
path". The daemon sent the marker and the phone decoded it, and **`SnapshotCache.Apply` compared
only (epoch, revision) and never read it**. That is not a redundancy: `viewEpochSeq`
(`internal/daemon/terminalview.go`) is a bare process-global counter, so it **restarts at 1 in
every daemon process**, and sessions surviving a daemon crash, restart or upgrade is a designed
property of this system. A phone holding `{epoch 1, revision 40}` therefore discarded a restarted
daemon's `{epoch 1, revision 1, reset}` *and every revision after it*, and the user read a
plausible, frozen, pre-restart terminal — the same failure T4-a exists to name, in the variant
where the epoch collides. **The rule is amended to: a frame the machine marked `reset` is adopted
unconditionally; otherwise the epoch/revision rule above is unchanged.** Reading the marker is
what makes the counter's process-locality harmless, and it is deliberately the only way a lower
revision may win, so the reorder rule is not weakened for any frame the machine did not mark.

**D1. THE REAP BLANK AND THE LAPSE DETECTOR MUST ANSWER THE SAME QUESTION.** Round 3 made a
reaped watch BLANK the phone's copy (T4-b) and round 4 gave the screen a lapse detector so it
would RE-WATCH rather than renew into a watch that no longer exists — and the detector was
written over the snapshot's AGE while the blank carries **no `rendered_at` at all**. Zero is
UNKNOWN on this phone by ruling (C4: a machine predating the closing round sends no render time
and must not be reported as rendered just now), so the detector answered NOT LAPSED for the one
frame that proves the watch is over: **round 3's blank actively defeated round 4's detector**,
and a user with the app in the foreground and the UI thread descheduled past the horizon sat on a
permanently blank terminal. **The screen's lapse rule is amended to read both evidences: the
machine SAYING it stopped — a view with no geometry, which is what `BlankTerminal` publishes and
what no live view ever carries — and the phone INFERRING it from a screen older than the machine's
horizon.** The blank is identified by its geometry and never by its age, because stamping a render
time on it would make the blank look FRESH, which is the same disagreement with the sign flipped.
A snapshot that has never arrived is a facade REFUSAL and not a blank, so the arrival window is
not a lapse and no redraw tears down a peek that is still opening.

**D2. A SEVERANCE TRIGGER IS FENCED AT THE HANDLER, NEVER AT THE HELPER.** C2's table row for
session kill / session delete was true of the code and false of its evidence: the round-4 test
called `severTerminalControlForSession` directly, so removing all four production call sites in
`handleKill`/`handleDelete` left `./internal/protocol/` green — the fence-survives-its-own-removal
class this wave raised as its own finding and claimed to have closed. **Every trigger in C2's
table must be driven through the op that fires it, to a reply, with the registry read after.**
Kill and delete are now fenced that way on both handler branches (the `IdempotentExecutor` branch
and the plain one). The control plane is still parked under C0/C1 and this escape was inert in the
shipped product; it is fenced because a trigger table is a claim about this code, and a claim no
test can lose is not evidence.


## Notes

**Numbering.** When Wave R1 was allocated, `docs/adr/README.md` recorded ADR-015 as the next free number, with 014 reserved by `mirror-program.md` M3.1 for paged interaction history. Wave R1 mints four together — 015 push-gateway split, 016 Web-PKI relay TLS, this one, and 018 multi-machine pairings — and 014 stays reserved. This is a **single** allocation of 017, not one of the 007/008/009/010 twin pairs; cite this file by name when both 009s are in scope, per the README's own instruction. The README's step 4 — "Add a row to the table above in the same commit" (`docs/adr/README.md:44`) — binds this file like any other: its index row lands in the commit that adds it. Step 1 was corrected by the same R1 change that landed these four files and now reads "the next FREE one is ADR-019" (`README:41`), so the numbering instruction is no longer the drift this Note was written to warn about.

**On reversing a deletion.** ADR-009's closing note asks that the next agent who finds `PeekPanel.kt` deleted "meets a ruling instead of a gap", so that "nobody restores a grid to the phone believing they are fixing a regression" (`ADR-009:373-376`). This ADR is written in the same spirit and against the same risk from the opposite direction: the grid returns **by ruling, for one routed class of session, under a capability record the daemon authors**, and any restoration wider than that — a peek on a Claude session, a user-selectable terminal, a scraped item, a queued byte — is drift, not this decision being implemented.

**What this ADR does not decide (replacing the previous paragraph).** It does not specify the interaction-schema shapes it obliges (T9), the push-gateway split (ADR-015), the relay TLS policy (ADR-016), or the multi-machine pairing model (ADR-018). The two owner questions previously listed here — the authorization tier for `terminal_control_begin`, and whether a `structured_gap`-degraded session may enter control — are answered above (T6-a, T6-b) and are no longer open. Two obligations remain deliberately undecided: the full T3 mandatory-row gate including the version-skew row, which is tracked separately and until it lands leaves an unrecognised Claude or Codex build opening as structured chat rather than as a labelled read-only terminal; and whether a notification may deep-link to a **read-only** fallback, which T6's routing rule constrains only for `terminal_input`.

**This list said three until 2026-08-26.** The struck entry was "`expected_input_revision` and the shim-wide input transaction, which need an adapter seam that characterizes the input region" — which the amendment at `:195-210` had already discharged, by establishing that the transaction needs no such seam: the shim can answer *has anybody written to this PTY since the last submit* absolutely, because it owns the only writer, and only that predicate has to cross. A note listing an obligation as open while the decision above closes it is a live contradiction in the same document, and it is repaired here rather than left for a reader to adjudicate. `expected_input_revision` itself is not added and is not needed; it is not deferred, it is unnecessary.
