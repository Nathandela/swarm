# ADR-013: Mirror capture architecture — structure arrives beside the PTY, and a phone answer is typed into the CLI's own dialog

**Status**: Accepted (owner-approved direction 2026-08-13, "that's exactly the direction"; the M1 items it records are built, tested and pushed)
**Date**: 2026-08-13
**Program**: [docs/specifications/mirror-program.md](../specifications/mirror-program.md) — the plan of record; this ADR records the decisions inside it, not the wave schedule.
**Companions**: [ADR-009-structured-chat-interaction.md](ADR-009-structured-chat-interaction.md) (the phone surface is a transcript, the grid is retired), [ADR-010-adapter-structured-capture.md](ADR-010-adapter-structured-capture.md) (the optional `InteractionSource` seam this feeds), [ADR-007-remote-access.md](ADR-007-remote-access.md) (D4/D7 signed-op and binding-tuple rules), [docs/specifications/interaction-schema.md](../specifications/interaction-schema.md) (the normative item fields).
**Evidence**: [mirror-m0.md](../verification/mirror-m0.md), [mirror-m1.md](../verification/mirror-m1.md), [channels-spike.md](../research/channels-spike.md).

> **SUPERSEDED ON ANDROID, 2026-08-30 — one conversation surface.** The 2026-08-14
> terminal-fallback amendment at the end of this ADR remains historical design and **wire compatibility**,
> not current Android navigation. Every production Android session opens in the
> normal transcript and pinned composer shell. Missing chat capability, offline state and ended
> sessions disable sending and explain why inline; they do not replace the shell with a terminal
> or status-card screen. Terminal view/control fields and verbs remain supported for rolling and
> non-Android consumers. No terminal text becomes an interaction item, no unavailable composer is
> inferred usable, and a durable history-gap marker is never removed by later sink recovery.

This ADR is written **after** the facts it records, on purpose. M0 settled the program's riskiest
assumption empirically, M1 shipped the approval path and M1.6 measured the successor technology
against a real session. Every claim below is either an owner ruling, a named test, or a quoted
capture — none of it is a forecast, except the one item marked as a gated intention in decision 1.

## Context

The industry verdict on this problem is unambiguous and consistent across three independent
post-mortems in the 2026-08-13 landscape survey (coder/agentapi, the Omnara pivot, the Happy
migration): **never scrape the terminal for structure.** Every project that built a phone or web
surface by diffing a VT grid ended up rewriting onto a native event source. This repo has its own
version of that finding recorded before the program started — spike S-A is PARTIAL (FAIL on overlay
transitions, DEGRADED on truncated tool output, `tool_input` never recovered at all), which is why
ADR-010 exists.

Three things were unknown when the program was planned, and all three were load-bearing:

1. **Does an owner TUI attach evict the phone?** Two sources in this repo asserted the opposite of
   each other (`internal/skeleton/sessiontap_test.go:4-8` vs `internal/tui/attach.go:65-70` and
   commit `6ac05db`). If eviction were real, every "both rooms are live" claim in the program would
   have been a lie, and the approval design would have had to serialize the two surfaces.
2. **What does a claude permission dialog accept?** The apply-by-keystroke path is only as safe as
   the key map is real; a guessed key runs something on the owner's machine.
3. **Is `claude/channel/permission` usable today?** If the relay were already shippable, injection
   would be wasted work.

M0 and M1 answered all three. What follows is what was decided given those answers.

## Decision

### 1. The PTY is sacred; structure arrives beside it, per CLI

The shim-owned PTY hosts the vendor's real TUI, byte-exact, always. **Nothing in this program reads
the terminal for structure and nothing in it alters how the terminal works.** Structure is captured
from each CLI's best native side-channel, normalized by an adapter `InteractionSource` (ADR-010)
into the existing item schema, and carried over the existing journal → gateway → relay → phone
pipeline. No new transport, no new plane.

| CLI | Structured channel | Status of this row |
|---|---|---|
| Claude Code | hooks — the five `capture=raw` rows ADR-010 ships (`UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `Stop`) — plus the transcript JSONL tail as **non-load-bearing enrichment** (thinking, full tool results, `plan_update`) | **decided and partly built**; the tail lands in M3.4 behind a tail-killed negative control: a format change degrades detail, never the feed |
| Codex | `codex app-server` as the session backend, the TUI attached with `--remote`, the daemon a **second JSON-RPC client of the same thread** | **GATED INTENTION, NOT A MADE DECISION.** It is what M4.1-M4.5 will build *if* the M4.0 gate passes against the installed binary. Named fallback if any leg fails: rollout-file tail, mirror-only, no steer, approvals stay on grid heuristics. The gate's result is recorded here either way |
| opencode | `opencode serve` + SSE `/event` (`message.part.updated` deltas, tool parts, `permission.updated`); the TUI is already a client of the same server; injection via `POST /session/:id/message` | decided; the approval response RPC pair is flagged unconfirmed by research and is M5.1's first job |
| AGY | probe for the Gemini-line hook set (per-chunk `AfterModel`) or an ACP surface; wire if present | undecided by construction — a timeboxed probe (M5.2), not a commitment |
| anything else | none | **status card, by owner ruling.** No pseudo-chat sliced from the grid, ever |

> **THE "anything else" ROW ABOVE IS AMENDED BY ADR-017 (2026-08-14):** its third column becomes "honest status card **plus** the sanitized terminal fallback"; *no pseudo-chat sliced from the grid, ever* is untouched and re-affirmed. See the amendment at the end of this file.

**The honest limit, stated once and not worked around**: interactive Claude Code exposes no
mid-generation assistant text by any mechanism — hooks carry final text at `Stop`, `stream-json` is
headless-only, and the transcript file lags. On Claude the feed is alive through tool cards and
running states (M0.2 made the running state render at all); token-live prose arrives first on
Codex, if M4.0 passes.

### 2. Co-presence is a proven fact, and the lease dies as UX

`TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive`
(`internal/skeleton/copresence_test.go`) drives the real assembly — two protocol Servers over one
`coreAPI`, a real shim session, a real paired device with a real Ed25519-signed `take_control` — in
both orders, with a vacuity control and a negative control that proves the harness can observe an
eviction when one happens. **It was GREEN on its first run and ships as a pin.** Under this repo's
TDD rule that green run is the finding, and it is quoted verbatim in mirror-m0.md.

The mechanism, confirmed by the code the test exercises: each Server keeps its own `s.leases` map,
so `Server.attach`'s supersede can only reach a controller on the *same* Server; `coreAPI.Attach`
and `coreAPI.TerminalTap` both go through `a.tap.subscribe(...)`, and the shared per-session tap
dials the shim once and refcounts. `hub.attach` — the true single-subscriber constraint — is
reached once per session, by the tap. **There is no slot to contend for.** Eviction is real only
within a tier.

Three consequences are decided here:

- **R3 (owner ruling): the lease survives as plumbing and dies as UX.** Reading never requires
  anything; sending is a signed op the daemon applies locally; the kill switch and device
  revocation keep their full authority. There is no visible take-control anywhere in the chat path,
  because there is nothing to take.
- **The injection primitive joins instead of stealing.** M1.2's write is one short-lived
  `readWrite` subscription on the same shared tap, not a fresh shim dial and not a new shim
  protocol. Co-presence is what makes that legal.
- `agents-tracker-nx44.8` (build a non-evicting observer role at the shim) was **closed as
  disproved**: the role already exists and ships. The remaining work is display wording
  (`agents-tracker-dwwv.3.1`, M2) — the attach chrome still says "took over from phone", which
  co-presence makes untrue.

### 3. Approvals: the held hook is rejected; a grid-gated keystroke is what ships; Channels is the designated successor

**Rejected: the held hook.** The audit of `cd648a7` proposed having `swarm hook` keep the
`PermissionRequest` connection open until the phone answers, with the daemon writing the decision
back on the held hook. It is technically sound — spike S-C measured that hook holding to within
~15 ms at 5 s, 30 s, 120 s and 300 s with no timeout or auto-deny — and it is **rejected for the
interactive path on co-presence grounds**: while a `PermissionRequest` hook is undecided, the CLI
has **not drawn its own dialog**. Holding it indefinitely hides the terminal prompt, which turns
the room with a human in it into the blind one and violates the program's central ruling. M0.1 is
what makes this reasoning empirical rather than stylistic — both rooms really are live at once, so
there is a co-presence to break.

**Shipped: the spec's own fallback as the primary path** (IS-LIFE-6 prompt-card apply, built in
M1.2):

1. The dialog appears in the terminal exactly as today; the hook returns without a decision.
2. The phone's Allow/Deny maps to that dialog's **recorded** keys, injected by the daemon into the
   PTY it already owns, after validating (a) the signed binding tuple is still unresolved and (b)
   the live VT grid still shows an answerable permission dialog.
3. Resolution is emitted **only by observation** — the status transition out of the waiting state —
   never by the tap. `ok` on the op means **APPLIED**, not **RESOLVED**.
4. A post-injection watchdog (5 s) surfaces a non-transitioning dialog as a `session_status` item,
   and resolves nothing.
5. First-answer-wins falls out by construction: both surfaces answer the same dialog.

Four properties of that design are decisions in their own right:

- **The key map is a recorded characterization fixture per Claude version, never an assumption.**
  M1.1 pressed each key bare on a live 2.1.231 dialog and read the answer off the **side effect**
  (marker files, edited file content) rather than the screen, because the collapsed card reads
  `Ran 1 shell command` either way: `1` and bare `\r` allow, `3` and `Esc` deny, identically in the
  Bash and Edit variants, digits absolute rather than relative to the selection. `\r` is recorded
  and deliberately **not returned** — it confirms whatever is selected, which is not a thing a
  remote tap may rely on. Option 2 is never sent by anything: in both variants it is a standing
  grant, which is not what one phone tap authorizes.
- **The recognizer refuses anything it does not recognize.** `RecognizePermissionDialog`
  (`internal/adapter/claude/permdialog.go`) is pure, total and I/O-free, so it stays inside the T-5
  adapter boundary while being callable from the daemon. It positively matches an option row
  labelled exactly `Yes` and one labelled exactly `No` in the engine's bottom-12-row region, in
  that order, plus a recorded box title found by walking up to the box's own top rule — anchored to
  the box so a scrolled-up earlier dialog cannot name the variant now on screen. Anything else is
  `false`, and a `false` refuses the injection rather than guessing. It is the **stricter reader of
  a screen the engine already classifies**, not a second opinion about it; the subset relation is
  pinned by `TestRecognizedDialog_IsPermissionToTheStatusEngineToo`, not asserted in prose.
- **The stray-keystroke race is narrowed to one tap round trip by the gate, and the gate is why the
  design is a gate at all.** Between the phone rendering a card and the tap arriving, the owner may
  have answered at the terminal. A key typed at a dismissed dialog lands in the composer as input
  the agent acts on. So the injection is refused (`no_dialog`) when the live grid no longer shows an
  answerable dialog, refused (`already_applied`) when an answer has already been typed and is
  awaiting observation — the hole the design itself opened, since "already resolved" no longer
  catches a re-delivered approve during the observation interval — and refused when the recognized
  dialog was raised by a **different tool** than the pending request's own (M1.8: the request's §7
  action must name the variant on screen, so a chained `A answered, B raised` pair cannot be
  answered with A's verdict). **The gate and the keystroke share one seeded view**: the subscription
  is seeded with the grid as of the moment it joined and writes through the same handle, so no
  SECOND DIAL can interleave between the read and the write.
  **What that does not close, stated plainly.** The seed is either the shim's snapshot fetched over
  the wire during the dial or the daemon's own mirror of a frame stream that arrives with transport
  latency, and `sub.Input` travels back over the same wire. So the judged screen is the screen as of
  the seed, not as of the write, and the terminal-answered-first race is narrowed to one tap round
  trip rather than eliminated — it is physically unclosable from the daemon side, which owns neither
  the glass nor the keyboard. The residual consequence is bounded by M1.1's own recording: the keys
  carry **no Enter**, so a digit that arrives after the dialog has gone lands in the composer
  un-submitted, where it is visible and deletable rather than acted on. Every refusal leaves the
  card PENDING — the daemon declined to type; it decided nothing.
- **A verdict outside allow/deny is refused, not guessed** (`unmappable_decision`). This reversed a
  prior test's conclusion on purpose: while the tap merely *recorded* an outcome, resolving `other`
  as allowed was defensible; once the answer is *applied*, typing the allow key would run something
  on the owner's machine on the strength of a guess. Same premise, opposite conclusion, because the
  consequence changed. The rewrite quotes the assertion it replaced (mirror-m1.md M1.2).

**Attribution.** The daemon records what it is about to type *before* it types it, so when the
dialog leaves it has first-hand knowledge of which key it pressed and the record carries
`allowed`/`denied`, `by: phone` and the phone's `operation_id`. The ordering is deliberate: the
status engine may already be mid-sample when the keys land. For a terminal-side answer the record
stays `answered_locally` / `by: owner`, and M1.3 established that this is the spec's honest answer
and not a gap: the `PermissionDenied` hook **empirically never fires on the interactive dialog
path** at 2.1.231 (four real captures, plus the dispatcher read out of the installed binary — it is
gated to `decisionReason.type === "classifier"` with `classifier === "auto-mode"`; a hook-originated
deny takes a third function again). No fixture was fabricated from the decompiled schema; the
shaping work is filed as `agents-tracker-hgyg` against the day a real body is captured.

**Codex needs none of this**, and this is a standing rule rather than a current state: its
approvals are native RPC, so **no keystroke is ever injected on a codex session** (M4.3).

**Channels is the designated successor.** M1.6 built a scratch stdio MCP sidecar declaring
`capabilities.experimental['claude/channel']` and drove it against real interactive sessions.
Result: the hidden flags are real and wired in 2.1.231; the sidecar receives
`permission_request`; the terminal dialog renders **simultaneously**, not sequentially; and a
sidecar `allow` proceeded the tool 14 ms later with **zero local keypresses**. The fourth
observation — the terminal-first race — was not reproduced inside the timebox and stays open.

Promotion from flagged spike to shipped path is gated on all seven criteria in
`docs/research/channels-spike.md` §4, of which four are hard blockers today: the research-preview
flag must be gone or the contract declared frozen; a `claude --version` >= 2.1.211 runtime floor
must be enforced before any relayed `description`/`input_preview` is trusted (both are
attacker-controlled tool text below that floor); first-answer-wins must be verified in the
**terminal-wins** direction and not only the sidecar-wins one — the docs describe a silent drop by
request-ID mismatch, and if the channel gets no signal at all, promotion inherits M1.2's
watchdog-and-reconcile pattern rather than a fire-and-forget verdict; and relay scope is narrower
than injection's by contract (project-trust and MCP-consent dialogs never relay), so M1.2's
injection stays as the fallback for exactly those dialogs even after the rest is promoted. Because
the promotion is a **config change and not a design change** — same signed op, same schema, same
card — the spike stays flagged and non-release until then. Re-check signal: the preview note
disappearing from `code.claude.com/docs/en/channels#research-preview`, or "channels" leaving the
experimental language in the CLI's release notes.

### 4. Schema rulings

- **R1 — keep the swarm interaction schema; import from ACP only the fixed tool-kind vocabulary**
  (`read | edit | delete | move | search | execute | think | fetch | other`) as an **additive**
  field. No rewrite: fold-by-ref increments, in-place item mutation and replay-through-the-same-path
  already exist in our schema and pipeline, so adopting ACP's item model wholesale would buy
  vocabulary at the price of the machinery. The field lands in M2.2 with its GG-7 row and per-arm
  recorded fixtures (including the new `Grep`/`Glob`/`WebFetch` arms); an adapter that maps nothing
  yields `other`, which is the vocabulary's own escape hatch and not a degradation.
- **R2 — hand-rolled minimal markdown renderer** (bold, italic, inline code, fenced blocks, lists,
  links), no new Android dependency. The reasoning is a dependency gate, not aesthetics:
  `verification-metadata.xml` churn is deliberately a **human-review** gate in this repo, so every
  added Android dependency spends owner attention that a six-feature renderer does not. Revisit only
  if the subset visibly falls short — tables are the likely gap.

## Non-goals

Deliberately excluded; a future agent should read an attempt at any of these as drift.

- No terminal rendering anywhere in the phone app (ADR-009's ruling stands), and no pseudo-chat
  sliced from the VT grid for a CLI with no structured source. The honest status card is the answer.

  > **AMENDED BY ADR-017 (2026-08-14):** the first clause is re-scoped to `structured_chat` sessions; the second clause and the no-scraping rule stand verbatim.

- No multi-tenant relay. The transport stays the existing sealed outbound-only design.
- No new shim protocol, wire field, op or error code for injection — M1.2 added none, and the GG-7
  drift check has nothing to diff (see Conformance below).
- The phone never authors a keystroke. It sends a signed decision id; the daemon types.
  `mobile/interaction_screencoverage_test.go`'s ban on the former is untouched and still correct.
- No visible take-control in the chat path (R3), and no lease gate on reading.

## Consequences

### Positive

- The terminal is never worse for the phone existing: the vendor TUI is byte-exact and the daemon
  only ever joins the tap that already tees to it.
- The approval card can no longer lie. Before M1.2 a tap dismissed the card on every surface while
  the CLI stayed blocked; resolution now requires an observation, so a stuck dialog surfaces as a
  status item instead of a silently-closed card.
- The apply path degrades safely in the one direction that matters: an unrecognized dialog, a
  moved-on screen, an unmappable verdict and an unreachable PTY are all **refusals that type
  nothing**, and each leaves the card pending for the terminal to answer.
- Promotion to Channels is a config change, and the recognizer/key-map fixtures are per-version, so
  a CLI update is characterized rather than silently mis-keyed.
- Co-presence being proven rather than assumed retired an entire planned subsystem (nx44.8) and
  removed the lease from the UX.

### Negative

- The Claude feed has no token-live prose and will not until a streaming source exists; the phone
  shows tool cards and running states between the user's message and the whole reply at `Stop`.
- The key map is a per-version characterization: a claude release that renumbers or relabels its
  dialog makes the recognizer refuse (safe) until a new capture is recorded (work). Recording it
  costs real session runs.
- The injection path is Claude-shaped by construction; every other CLI either gets a native
  mechanism or the status card. There is no generic apply.
- The observation interval is a real window in which a re-delivered approve must be refused rather
  than resolved, which is why `already_applied` exists — a refusal reason the phone can see and a
  user could reasonably find confusing.
- Codex's row in decision 1's table is a plan, not a fact, and the largest single unknown left in
  the program rides on one gate.

## Alternatives Considered

- **Hold the `PermissionRequest` hook until the phone answers.** Rejected on co-presence grounds
  (decision 3). It remains the right shape for a *headless* or auto-mode path, where there is no
  terminal room to blind — nothing here forecloses that.
- **Derive the transcript from the sanitized grid.** Rejected on evidence: spike S-A is PARTIAL and
  never recovers `tool_input` at all, and the three industry post-mortems agree. This is the
  decision the whole program is organized around.
- **Ship Channels now instead of injection.** Rejected: research preview, hidden flags, a documented
  "may change based on feedback" contract, and an unreproduced terminal-wins race. It is the
  successor, gated on §4's criteria, not the incumbent.
- **A new non-evicting subscriber role at the shim for the injection write.** Rejected as
  disproved-before-built: the shared session tap already tees, so injection is a subscription and
  not a protocol.
- **Adopt the ACP item model wholesale.** Rejected (R1): our schema already has the increment,
  mutation and replay semantics; only the tool-kind vocabulary was worth importing.
- **Add an Android markdown dependency.** Rejected (R2): the dependency-verification file is a
  human-review gate by design, and the needed subset is small.

## Conformance

**GG-7 cross-check on M1.2, performed while writing this ADR.** M1.2 changed the *semantics* of an
existing op (`approve`: validate-then-apply, `ok` means APPLIED, no `approval_resolved` on the op,
five refusal reasons of which four are new), which GG-7 obligates against
`docs/specifications/protocol.md`. **The obligation was met.** `protocol.md`'s `approve` section
(lines 369-423 at `791852d`) carries the validate-then-APPLY sequence, the full refusal table with
codes, the screen-gate rationale, and the sentence whose meaning changed. No row is missing and no
correction was needed here. The wire FIELD table is correctly untouched: no field, op or error code
was added — `git diff 24ef9b1..HEAD -- internal/wire` is empty across the whole M1 wave — so the CI
drift check against the `wire` package has nothing to diff. There is no per-op conformance TSV row
for `approve` (the repo carries none for any op), so protocol.md is the whole of the obligation. No
bead was filed, because there is no gap.

Obligations this ADR creates, all landing in later waves:

1. **M2.2** — the R1 `tool_kind` field is additive, carries its GG-7 protocol.md row, and every
   adapter arm that maps it has a recorded fixture; an unmapped tool yields `other`.
2. **M3.4** — the Claude transcript-tail enrichment ships with a tail-killed negative control: the
   feed is unaffected and only detail degrades.
3. **M4.0** — the Codex gate's outcome is recorded in this ADR by amendment, pass or fail, before
   any M4 item is built on it.
4. **Channels promotion** — no promotion without all seven criteria in channels-spike.md §4, and
   the terminal-wins race reproduced against a real session rather than read off the docs.

## Notes

On numbering: 013 is a single allocation, not one of the 007/008/009/010 twin pairs — those came
from parallel lines minting independently, and this one was reserved by mirror-program.md §2 before
it was written. `docs/specifications/mirror-program.md` M3.1 has likewise reserved **014** for paged
interaction history. The next free number is tracked in docs/adr/README.md (019 as of 2026-08-14, the Wave R1 records having spent 015-018).

## Historical amendment 2026-08-14 — honest status plus terminal fallback (ADR-017; superseded on Android 2026-08-30)

**Status**: Accepted (owner sign-off 2026-08-15; drafted 2026-08-14 from the owner-approved playbook).
**Source**: `docs/adr/ADR-017-terminal-fallback-capability.md`, which quotes each amended sentence of
this ADR verbatim (`ADR-017:12-13`); the direction is RC-D5
(`docs/specifications/remote-control-product-playbook.md:75`) and §3.1's closing instruction to
"replace 'status card only' with 'honest status plus terminal fallback' for incomplete providers,
while retaining the rule that terminal scraping never produces structured interaction items"
(`:118-120`).

**Current Android disposition (2026-08-30).** The following paragraphs continue to specify the
compatibility capability/wire behavior and to explain why terminal text is never structured chat.
Their three-destination Android UI is retired: production Android always uses the conversation
shell and expresses availability inline. Nothing in this disposition removes the legacy wire
fields or relaxes their sanitizer, authorization, horizon or session-instance fences.

**Decision 1's "anything else" row.** "| anything else | none | **status card, by owner ruling.** No
pseudo-chat sliced from the grid, ever |" (`:53`): the third column becomes **"honest status card plus
the ADR-017 terminal fallback"**. A provider with no complete structured source, whose capability
record says `terminal_fallback=true`, opens into the machine-sanitized snapshot presented as a
terminal, with an honest header naming the missing capability; a provider with `terminal_fallback=false`
keeps the status card. The status card is not deleted — it becomes one of three destinations rather
than the only alternative to chat.

**The second half of that row is untouched and re-affirmed: no pseudo-chat sliced from the grid,
ever.** Nothing here weakens the evidence the row was protecting — S-A is PARTIAL, FAIL on overlay
transitions, DEGRADED on truncated tool output, `tool_input` never recovered at all (`:20-21`) — and
**terminal scraping still never produces interaction items** (`:18-21`). No content is promoted from a
fallback surface into structured chat: not a scraped user message, not a parsed tool result, not a
heuristic status, not the last line of a completion. An adapter earns `structured_chat` only by
satisfying the complete-chat contract, and a session degraded mid-life keeps its accepted items
read-only at the exact boundary rather than backfilling from the grid (ADR-017 T10, `ADR-017:177`).

**Non-goals (`:219-220`).** "No terminal rendering anywhere in the phone app (ADR-009's ruling
stands)" is re-scoped to `structured_chat` sessions, exactly as ADR-009's own decision 1 is
(ADR-017 T1, `ADR-017:36`). The rest of that bullet — no pseudo-chat sliced from the VT grid for a CLI
with no structured source — stands verbatim, as does the ban on the phone authoring a keystroke and
R3's rule that there is no visible take-control in the chat path (`:81-84`): the control ceremony
ADR-017 introduces exists only on a fallback screen, which is never a structured session's screen.
What does not change at all is this ADR's own subject — the PTY stays byte-exact and untouched, and
the fallback reads the trusted renderer's output, downstream of the tap.

## Amendment 2026-08-15 — the M4.0 Codex gate ran and PASSED; decision 1's Codex row hardens from gated intention to decision

**Status**: Accepted (owner-signed program, playbook wave R1; evidence
`docs/verification/r1-codex-gate.md`, fixtures `docs/verification/r1-codex-fixtures/`).
**Amends**: decision 1's Codex table row ("GATED INTENTION, NOT A MADE DECISION") and Conformance
obligation 3, which required this amendment pass or fail.

Against the installed `codex-cli 0.147.0`, all five legs passed: app-server as a supervised child
on a unix socket; the real TUI attached and driving a thread; a second JSON-RPC client receiving
the same thread's items live (586 `item/agentMessage/delta` frames in one turn) without disturbing
TUI ownership; `turn/start`, `turn/steer` (same `turnId`, steer text honored), `turn/interrupt`
(`turn/completed` with `status: interrupted`), and an `item/fileChange/requestApproval` answered by
the observer over RPC — the TUI's dialog closed with **no keystroke ever sent to the terminal**,
which discharges the "no keystroke is ever injected on a codex session" rule empirically.

Two of the plan's mechanical assumptions were wrong and bind R7's implementation instead:
`--listen unix://PATH` serves a **WebSocket** endpoint (HTTP upgrade on `GET /rpc`, then JSON-RPC
as WS text frames — raw JSON-lines gets silence), and `codex app-server proxy --sock` is not the
bridge. `thread/resume` refuses until the thread's first turn exists, so the R7 shim should own
`thread/start` itself and hand the thread id to the TUI attach — which also matches RC-D1.
`turn/steer` carries a native `expectedTurnId`; the composer's expected-turn precondition rides it
rather than being reinvented. app-server writes nothing to stdout/stderr; supervision is by socket.

## Amendment 2026-08-20 — Wave R7 Codex topology: the app-server is a shim-owned CHILD with its own containment, the JSON-RPC client is DAEMON-owned, and the adapter stays a pure function of one frame

**Status**: Proposed (design, ahead of any R7 code; bead `agents-tracker-hggx.8`, Mirror M4.1-M4.5).
**Revision 2 (2026-08-20)**, after design review. What changed, so a reader of the rejected draft
does not have to diff it: the backend now has its **own** process group and its own
TERM->grace->KILL and its own `Wait` (§R7.2a — the claim that it "joins the agent's" was FALSE, and
is retracted below in as many words); a backend **pid and start-time** are recorded and liveness is a
checked fact rather than a successful dial, with a named orphan reaper (§R7.2c); the spawn-ordering
**go-ahead handshake** is specified as a `shim.Config` + control-socket change, because the preferred
cold start was not implementable without one (§R7.2e); the append arithmetic is redone against
`CoalescingSink`'s SHARED slot and the item floor is widened to 250 ms so a streaming Codex session
cannot freeze terminal peek (§R7.4); the composer-echo correlation is decided for the backend branch
(§R7.5); the capability dependency is restated as HONESTY, not safety, and the dead-backend
`structured_gap` question is answered (§R7.7); M4.5's typed status uses a direct engine seam instead
of `HandleCallback` (§R7.3); the app-server's own crash is covered (§R7.6); and the durability,
detached-session and per-session-process trades are stated as Consequences (§R7.10).

**Revision 3 (2026-08-20)**, after implementation review of R7 round 1. Four decisions that
revision 2 left open or got wrong, settled in code and recorded here:

1. **The daemon starts NO thread. It adopts the AGENT's.** Revision 2 framed the open question as
   "which `agent_args`" and said the handshake was the same either way. It is not: round 1 called
   `thread/start` from the daemon AND launched the agent with `--remote unix://SOCK` and nothing
   else, so the agent created its own thread and the two surfaces were never on one conversation.
   The topology is now: the daemon dials and completes `initialize`/`initialized` before the agent
   exists, sends the go-ahead, and learns the thread from the `thread/started` the server
   broadcasts to every attached client (RECORDED: `frame-samples.json`, where the R1 gate's
   observer received exactly this for a thread the TUI created). It then joins with
   `thread/resume`, which the schema documents as a rejoin of a running thread and which the gate
   exercised end to end. `agent_args` is `--remote unix://SOCK` and carries no thread id, so §R7.9's
   "which `agent_args`" question is CLOSED, and Q2 (`codex resume` under `--remote`) is no longer on
   R7's path at all. `codex resume --help` at 0.147.0 does list `--remote`, recorded offline.
2. ~~**`thread/resume`'s rollout race is retried, and a late join is an honest gap.** The RECORDED
   `no rollout found for thread id` is the ONE error retried; every other error fails immediately.
   A resume that needed retries succeeded only because a turn had already begun, so the daemon did
   not see that turn's opening: it emits `structured_gap` reason `backend_joined_late`. This is
   §R7.2e's own stated arm, now implemented rather than deferred to the Q3 measurement.~~
   **SUPERSEDED by revision 4 below (2026-08-20), which inverts it.** The inference
   "retried ⇒ a turn had already begun" is BACKWARDS. `no rollout found` is returned *because* no
   turn has begun; a resume that had to retry was waiting for the first turn and therefore missed
   NOTHING. The retry rule stands; the gap rule was factually false and is replaced.
3. **Turn operations name the CLI's OWN turn id.** `Interaction.TurnRef` (ADR-010's companion
   amendment) carries `params.turnId` machine-side, and `turn/steer`'s `expectedTurnId` and
   `turn/interrupt`'s `turnId` are that value. Round 1 sent the daemon's 26-character ULID against
   the server's UUIDv7 turn table: every mid-turn phone send was rejected by the precondition, and
   `turn/interrupt`'s honest `no active turn to interrupt` was swallowed as benign, so a Stop that
   stopped nothing reported success. A turn with no CLI-native id is REFUSED, never bridged.
4. **A phone-answered approval is retired by the server's own broadcast.** The pending-request
   table is split: answering consumes the ANSWERABILITY entry (so a request is answered once), and
   only `serverRequest/resolved` consumes the id->ref mapping. Round 1 consumed both when the phone
   answered, so the broadcast found nothing and the owner's card stayed live until the IS-LIFE-2
   expiry sweep. The gate recorded the server broadcasting to the ANSWERING client, which is what
   makes retirement-by-observation available on this ordering and not only the terminal-answered one.

Also corrected: an agent `exec` failure now fires the backend's group kill before `Run` returns
(§R7.2a's leak, reachable on the one exit that did not pass through finalization), and `Serve`
gained `connectBackendsForRunning` beside `startHookDrainsForRunning` so a daemon restart rejoins a
live backend or emits an honest `structured_gap` instead of tearing the structured plane silently.

**Revision 4 (2026-08-20)**, after implementation review of R7 round 3. One root cause, three
rulings. Revision 3's item 2 is struck above and replaced here; §R7.2e's "one measurement is owed"
paragraph and §R7.7's case list are amended in place below.

**The root cause.** The R1 gate RECORDED that `thread/resume` answers `no rollout found for thread
id` until the thread's FIRST TURN starts (`r1-codex-gate.md:112-115`). This topology has the daemon
join the thread at session LAUNCH, which is before any turn exists. Three mechanisms then compounded
that into a broken ordinary path: a retry set `late=true` and the join emitted `structured_gap`
reason `backend_joined_late` **on the success path**; the phone reads ANY gap as "no message sink"
(`TranscriptPanel.kt:331` → `SessionDetailPanel.kt:770-772` → `Composer.kt:89-91`), so the composer
disappeared; and the backend was registered only AFTER the resume succeeded, so until then
`resolveMessageSink` found no backend and refused `structured_unsupported` — the phone could not
start the very turn that would create the rollout. On an ordinary fresh launch the wave's exit
criterion was structurally unreachable, and an owner who thought for 45 s got a PERMANENTLY degraded
session while the app-server was perfectly healthy.

1. **A join that missed nothing is not a gap.** `structured_gap` means "the transcript has a tear
   that must never be silently bridged" (ADR-017). On a fresh launch there is no rollout because NO
   TURN HAS HAPPENED, so a late join has missed NOTHING and a gap there is factually false.
   **"Retried at least once" is no longer the test for lateness; "there is history I could not
   read" is.** The FIRST resume attempt answers it exactly: a rollout that ALREADY EXISTS proves the
   thread has already run at least one turn, and a client receives a thread's items only after it
   resumes — so those turns are unread history. That is the `codex resume`-shaped session, and it
   gets `structured_gap` reason **`backend_prior_history`** and **no durable degrade**: the tear is
   in the HISTORY while the channel is healthy. Round 3's rule was exactly inverted — it gapped the
   fresh session and stayed silent on this one.
2. **The message sink is the CONNECTION, not the subscription.** The backend is registered as soon
   as the connection is initialized and usable for `turn/start`; the thread SUBSCRIPTION is a
   separate, possibly-pending concern that gates nothing. This breaks the deadlock by construction
   and is the correct layering: the ability to SEND does not depend on having read history.
   *Ordering:* a turn started before the subscription is live is still captured, because the
   subscription retries and `thread/resume` on a RUNNING thread is a rejoin that delivers that
   thread's live item stream (RECORDED: the R1 observer joined a turn already in flight and received
   its 97 frames). The residual window is ~~one retry interval~~ **one retry interval at first and
   up to the 5 s backoff ceiling thereafter (corrected in place 2026-08-20)**, and any item whose opening deltas fall
   inside it is completed by `item/completed`, which carries the item's FULL text — so nothing is
   permanently lost. Fenced by
   `TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists`.

   > **The residual this ruling INTRODUCES, disclosed 2026-08-20 (pre-commit correction).** The
   > ordering note above covers item CAPTURE only. It does not cover TURN DISPATCH, and there the
   > same window has a second consequence that was not stated when the ruling was written.
   >
   > *The window.* It opens when `registerBackend` runs (`backendconnect.go:227`) and closes only
   > once the subscription has landed **and** a frame naming the native turn has been shaped into
   > the daemon's own turn state (`turnIDLocked`'s rejoin arm, `interaction.go:354`). Inside it the
   > daemon holds a live message sink and **no turn id at all**.
   >
   > *The consequence.* `d.turnIDs[local]` is empty, so a phone `composer_send` carrying
   > `expected_turn: ""` MATCHES the precondition and `deliverComposerText` takes the `turn/start`
   > branch — **even when the terminal has already started a turn this daemon has not yet
   > observed.** By this ADR's own rule (`chat.go:247`) a `turn/start` dispatched mid-turn queues a
   > SECOND turn, so the owner's terminal question and the phone's message become two conversations
   > on one thread. The corpus does not record the server's response to `turn/start` against a
   > thread with an active turn (`errors-observed.json` holds three errors, none of them this), so
   > whether it queues or refuses is UNPROBED; on either branch the phone was told the send
   > succeeded.
   >
   > *What widens it.* Ruling 3's backoff. Retries start at 100 ms but double to a 5 s ceiling, so a
   > session whose owner has been thinking sits at the ceiling and a terminal-started turn can go
   > unobserved for **up to about 5 s** plus the time for its first shaped frame to arrive.
   >
   > *What the user would observe.* They start a turn at the terminal, send from the phone within
   > that window, and the agent answers the phone's message as a separate turn after the terminal's
   > rather than steering the one in flight.
   >
   > *Not narrowed, and why.* The only turn-naming frame that could reach an unsubscribed client is
   > `turn/started`, and (a) the recorded corpus shows it only AFTER `thread/resume`, so there is no
   > evidence it arrives pre-subscription at all, and (b) the Codex adapter shapes no interaction
   > for it by design (`adapter/codex/interaction.go:140-148`), so reading it would mean sourcing a
   > turn at the pump — which IS-ENV-1 reserves to the daemon and which would change `expected_turn`
   > on an already-shipped wire. A synchronous `thread/resume` at send time cannot answer the
   > question either: success proves only that a rollout EXISTS, which is equally true of an idle
   > thread whose first turn has completed. `thread/read` is the only read that could name the
   > active turn and its practical `itemsView` is **Q4**, unsettled. Narrowing this correctly needs
   > a probe, not a patch; reverting Ruling 2 reinstates the deadlock and is not an option.
3. **A healthy backend is never permanently degraded.** `markSessionDegraded` is ONE-WAY and DURABLE
   and is reserved for a PROVEN structural gap. "No turn started within 45 s" proves only that the
   owner is thinking, so it no longer degrades anything: the subscription retries **for the life of
   the session**, bounded by the session's own backend registration (dropped by `endSession`, by
   `noteBackendLost`, and by the loop's own hard-failure arm) with backoff doubling from 100 ms to a
   5 s ceiling. A real loss still degrades honestly — an app-server that dies mid-session, and a
   resume that fails for any reason OTHER than the recorded rollout race, both tear and say so.

Also corrected in this revision: a frame that CANNOT reorder an open agentMessage fold no longer
flushes it (§R7.4's arithmetic was measured against a pure delta stream that never occurs; at the
RECORDED mix of ~1 lifecycle frame per 5 deltas the pump offered 9 records/s, above the 8 appends/s
ceiling); every arm of `joinSessionBackend` and both arms of `watchSessionBackend` now have
behavioural fences at the real call site, against a real WebSocket app-server, which is the absence
that let both blockers survive three rounds.

**One residual, stated rather than fixed.** The phone treats ANY `structured_gap` as "no message
sink" and hides the composer. After the three rulings above no gap reaches it on the ordinary fresh
launch, so the practical harm is gone; the conflation itself is real and wrong in general, and is
filed as a follow-up rather than changed here — the R6 Kotlin is reviewed and these blockers are
fixable daemon-side.

**Where M4.2's "BATCHED 200 ms AT THE ADAPTER" actually lands.** Mirror M4.2's own words say the
agentMessage deltas are batched *at the adapter*. They are not, and cannot be:
`InteractionSource.Interactions` is required to be pure, total and stateless and `internal/adapter`
is grep-fenced against every fd/socket/exec token, so a connection, a correlation table and a
200 ms timer are three kinds of state a stateless strategy object may not hold. The batcher sits at
the daemon's PRODUCER EDGE instead (`internal/skeleton/backend.go`), between the client's frame
stream and `captureInteractions` — which is where the row's intent lands, because the coalescing
still happens BEFORE AN ITEM EXISTS. This deviation is recorded here rather than only in a file
header so a reader of mirror-program.md M4.2 does not go looking for a batcher in the adapter.

**Amends**: decision 1's Codex row (which the 2026-08-15 amendment hardened from gated intention to
decision, but which names a *channel* and not an *owner*), and decision 3's "Codex needs none of
this" paragraph (`:175-176`), which is now given its positive form: what Codex *does* instead.
**Companion**: the amendment of the same date to
[ADR-010-adapter-structured-capture.md](ADR-010-adapter-structured-capture.md), which is where the
one new optional adapter extension belongs — extending the frozen boundary is ADR-level work
"regardless of whether the change is additive" (`ADR-010:16`), and that pre-commitment is ADR-010's,
not this file's.
**Evidence**: `docs/verification/r1-codex-gate.md`, `docs/verification/r1-codex-fixtures/`. Every
frame shape cited below is a recorded file in that directory. Where the recording is silent this
amendment says so in as many words and names the offline command or the realcli measurement that
settles it; nothing here is inferred from an unrecorded frame.

### R7.1 The binding question, and why the obvious answers are wrong

A token-live Codex backend needs somebody to open a unix socket, perform an HTTP/1.1 WebSocket
upgrade on `GET /rpc`, speak JSON-RPC in both directions, hold request/response correlation, and
push item events at the daemon. ADR-001 gives every session's lifecycle to a shim; E9.2 and ADR-010
§3 give the adapter descriptors and pure functions and **no fd**. Neither of the two obvious homes
survives contact:

- **Not the adapter.** `InteractionSource.Interactions(p HookPayload) []Interaction`
  (`internal/adapter/interaction.go:258`) is required to be PURE and TOTAL, and `internal/adapter`
  is grep-fenced against every fd/socket/exec token (`TestContractPackage_NoIOInSource`,
  `boundary_test.go:23-30`). A connection, a correlation table and a 200 ms timer are three kinds of
  state a stateless strategy object may not hold. Read literally, mirror-program.md M4.2's "batched
  200 ms **at the adapter**" is not implementable; §R7.4 below is what that sentence means once it
  is made legal.
- **Not, wholesale, the shim.** The shim is the PTY plane, and decision 1 of this ADR makes that
  plane sacred. A WebSocket JSON-RPC client is an unbounded parser of attacker-influenced bytes
  (tool output, model prose, MCP catalogues — the R1 gate excluded a 3.8 MB `app/list/updated` frame
  for exactly that reason) and its crash would take the PTY, the emulator and the transcript with
  it. `internal/shim/hooksocket.go:44-52` already had to bound a DRAIN body for this reason, in as
  many words: "permitting hundreds of megabytes of allocation inside the shim, the process that owns
  the PTY (ADR-013's sacred plane)".

### R7.2 Decision: the process is the shim's, the socket is the daemon's, and they are different things

**(a) The app-server is a shim-owned CHILD with its OWN process group and its OWN containment.**
`internal/shim.Config` (`shim.go:42`) gains a backend triple — the program plan to run, the socket
path it must serve, and a readiness bound — and `Run` (`shim.go:97`) starts it *before*
`pty.StartWithSize`, because `codex --remote unix://PATH` cannot attach to a socket that does not
exist yet.

**The rejected draft claimed the backend "joins the agent's existing TERM->grace->KILL containment
(`shim.go:139-146`)". That claim was false and is retracted.** Every kill on that path is
`syscall.Kill(-s.pgid, ...)` (`internal/shim/server.go:291,293,309,331`) and `s.pgid` is the AGENT's
group — documented at `server.go:59` as "agent process-group id (== agent pid; it leads its own
group)" and pinned by `internal/shim/spawn_test.go:175-191`, which asserts the agent's pgid equals
its own pid AND differs from the shim's. A sibling `exec.Cmd` started by the shim inherits the
SHIM's group, so all four of those kills miss it, and `cmd.Wait()` (`shim.go:228`) waits the agent
only, so it is never reaped either. `Run` would have returned leaving a live app-server behind.

The backend therefore gets containment of its own, and it is a `shim.Config` + `shim.Run` +
`shim/server` change, not an implementation detail:

1. **Its own group.** The backend is started with `SysProcAttr{Setpgid: true}`, so its pid leads its
   own group and the node launcher plus the vendored rust binary the R1 gate recorded as two pids
   (`r1-codex-gate.md:66-72`) both go with it.
2. **Its own escalation.** `server` gains `backendPgid int` (0 == none). `onSignal`, the escalation
   worker, and `finishEscalation` each signal BOTH groups — agent first, backend second, one
   `syscall.Kill(-backendPgid, ...)` beside each existing `syscall.Kill(-pgid, ...)`. The grace
   window is shared; there is no second timer.
3. **Its own Wait.** A dedicated goroutine holds `backendCmd.Wait()`. It is JOINED at finalization
   *after* `finishEscalation` has issued the final group KILL, which is what guarantees the join
   returns rather than blocking `Run` behind a backend that ignores TERM. This mirrors the
   escalation worker's own cancel-and-join discipline.
4. **Its own arm-before-spawn.** The signal handler is already armed before the agent spawns
   (`shim.go:126-131`); the backend is spawned *inside* that armed window, so a SIGTERM arriving
   between backend spawn and agent spawn contains the backend too.

Fence, by mutation: delete the `-backendPgid` kill from `finishEscalation` and a permanent test that
spawns a TERM-ignoring backend under a shim and asserts the pid is gone after `Run` returns must
fail. Fence 2: delete `Setpgid` and the same test must fail, because a backend in the shim's group
is signalled by the daemon's own containment of the shim only, which is exactly the accident this
rule refuses to rely on.

Readiness is the SOCKET and never the process streams: R1 leg 1 recorded that app-server's stdout
and stderr stay **empty for the entire session**, so a supervisor that watched them would watch
nothing (`r1-codex-gate.md:75-79`).

**(b) The socket lives in the session state dir**, `<session-dir>/codex.sock`, sibling of `hook.sock`
(`internal/daemon/launch.go:154`), and the *intent* to run one is persisted in the 0600
`shim-launch.json` under a new `backend_socket_path` key, on `HookSocketPath`'s exact
**unset-means-disabled** convention (`launch.go:123-125`, `SessionHookChannel`'s `:173-190`). An
absent key means "this session was launched without a backend" and is never a defect.

**(c) A servable socket is NOT a liveness fact, and the SIGKILL residual is worse here than for the
agent.** The agent's documented last-resort residual (an uncatchable SIGKILL of the shim) is bounded
in practice by the PTY: the master closes, the agent takes SIGHUP/EIO. The app-server has no PTY, no
controlling terminal, and writes to neither stream ever, so a SIGKILL of its shim leaves a process
authenticated to a real ChatGPT account alive indefinitely, still serving `<session-dir>/codex.sock`
— and still looking *healthy* to any rule whose readiness test is "the dial succeeded". A restarted
daemon would rediscover the path, redial, and report a session live whose agent died hours ago. That
is the one place this topology is qualitatively worse than the agent's residual, and it is closed by
recording a fact rather than probing a symptom:

- The shim writes `<session-dir>/backend.json` (0600) the moment the backend's socket is servable:
  `{"pid": N, "pgid": N, "started_at_ms": …, "socket_path": "…"}`. It is a shim-authored side-file
  beside `exit.json` and `final-snapshot.bin`, for the same reason those are: the daemon cannot know
  a pid it did not spawn.
- **Liveness is (pid alive AND its start-time matches AND the owning shim is live)**, checked with
  the same `procStartTimeFn` (PID, start-time) pair reconcile already matches shims by (S1/L2,
  `launch.go:415-425`). Two of the three are new facts; the third is the one that matters, because
  an app-server whose shim is gone is by construction an orphan: nothing will ever reap it and no
  agent is attached to it.
- **The orphan reaper is named**: `Daemon.reapOrphanBackend(id)`, called from **BOTH paths on which
  a shim's death becomes known**. For a session whose shim is not live, if `backend.json` names a
  live pid whose start-time matches, it issues one `syscall.Kill(-pgid, SIGKILL)` and removes the
  file. It never dials.
  - `reconcileRunning`'s orphan arm — a shim that died while NO daemon was watching, discovered at
    the next `swarm daemon restart`.
  - `handleShimExit` — a shim that died while THIS daemon was up, which is the more common half and
    which round-3 review found open (`superviseLaunched`/`pollMonitor` marked the session LOST and
    reaped nothing, and reconcile skips any session no longer persisted RUNNING, so no later restart
    revisited it either; PROBED: the recorded backend pid was still alive afterwards). It is a no-op
    on every ordinary end, because the shim's own TERM→grace→KILL contains its backend and `Run`
    joins it before returning — by the time the shim process is gone its backend is already reaped,
    and the reaper finds a dead pid and merely removes the stale record. It kills only in the case
    the shim could not act on: an uncatchable signal.
- **Reconcile never adopts a backend it did not prove live by pid.** A dial that succeeds against a
  pid that does not match `backend.json` is an unrelated process on a reused path and is refused.

Fence, by mutation: make the liveness check `dial(socketPath) == nil` and a permanent test that
leaves a live process serving a stale socket with no matching `backend.json` must fail.

**(d) The JSON-RPC client is DAEMON-owned, in a new core package `internal/appserver`.** It holds
the connection, the request-id correlation, the server-request table and nothing else; it imports
neither `internal/adapter` nor `internal/daemon`. **No new dependency is required**: the repo already
carries `github.com/coder/websocket v1.8.13` as a direct dependency and already dials a WebSocket
over a caller-supplied `http.Client` at `internal/remote/relay/client.go:220-234`, which is exactly
the shape a `net.Dial("unix", …)` transport plugs into. The **per-session pump is assembled in
`internal/skeleton`**, beside `HookDrainer` and the interaction producer, for their stated reason
verbatim: skeleton is the one package that already imports the adapter contract, the core daemon,
the protocol and the gateway (`internal/skeleton/interaction.go:6-13`, `hookdrain.go:20-24`), and
the pump is the only thing that touches all four of {client, adapter, engine, producer}.

**(e) The spawn-ordering handshake, which is the one NEW mechanism this topology needs.** The
rejected draft preferred "the daemon calls `thread/start` and owns the thread id before the TUI
attaches" and had no way to build it: the shim starts the backend and then immediately spawns the
agent, and the daemon is not in that loop — `launchConfirmTimeout` (`launch.go:109,449`) waits for
the shim's CONTROL socket, which is bound at `shim.go:116` *before* either. There is no edge the
daemon can act on. The stated fallback (the daemon connects before the shim spawns the agent) has
the same defect and is strictly worse, because leg 1 recorded the socket appearing "within ~3 s" and
that 3 s is the entire window a poller would have to win.

**Decision: the shim GATES the agent spawn on a daemon go-ahead, over the per-session control socket
that already exists.** Concretely:

| Step | Actor | What |
|---|---|---|
| 1 | shim | bind control socket (unchanged, `shim.go:117`) |
| 2 | shim | spawn backend in its own group; wait for the socket to be servable, bounded by `Config.BackendReadyTimeout` |
| 3 | shim | write `backend.json` (§R7.2c) |
| 4 | shim | **block** in `waitBackendGoAhead(Config.BackendGoAheadTimeout)` |
| 5 | daemon | `waitShimServing` returns; read `backend.json`; dial; `initialize`/`initialized` |
| 6 | daemon | optionally `thread/start` (see below); send `backend_attach{agent_args}` on the control socket |
| 7 | shim | append `agent_args` to the agent argv verbatim; `pty.StartWithSize` |

`backend_attach` is one new `shimwire` control verb, and `Config` gains two bounded timeouts. The
timeouts are what keep this from being a new way to hang: **a go-ahead that never arrives spawns the
agent anyway** at the deadline, degraded to the fallback below, and the shim logs which path it took.
A backend that never becomes servable is a launch failure for the backend only — the agent still
spawns, with no `--remote`, and the session runs exactly as a pre-R7 Codex session does, plus a
`structured_gap` (§R7.7).

**What the handshake buys, and what is still unrecorded.** It buys the guarantee that matters
unconditionally: the daemon is connected as a client **before the agent process exists**, so no
`thread/started` notification can be missed and the 15-17 s rollout race the gate hit never occurs at
cold start. Whether the daemon ALSO pre-creates the thread with `thread/start` and hands its id to
the TUI through `agent_args` depends on a flag that is **NOT RECORDED** — the gate ran
`codex --remote unix://SOCK` and the TUI created its own thread. That question now degrades cleanly
instead of changing the topology: with the flag, `agent_args` carries it and the daemon owns the
thread from birth with no `thread/resume` at all; without it, `agent_args` is empty and the daemon
learns the id from `thread/started` and joins with `thread/resume`.

**One measurement is owed before the no-flag path is relied on.** The rejected draft called the join
window "wide" on the basis of the recorded ~2.1 s from `turn/start` to first delta. That is the wrong
quantity. The one that binds is **rollout-to-resume**: how long after the thread's first turn starts
does `thread/resume` stop returning `no rollout found for thread id` (`errors-observed.json`)? The
gate's 15-17 s is boot-to-resume and does not answer it. R7 MUST measure rollout-to-resume under the
realcli gate (`//go:build realcli`, `SWARM_REALCLI=1`, isolated `CODEX_HOME`) and record the number,
before any claim that the first turn is joinable. ~~If it exceeds the first turn's duration, the
no-flag path emits a `structured_gap` for the first turn rather than pretending to have seen it.~~

> **Amended, revision 4 (2026-08-20).** The struck sentence made the gap a function of the WAIT, and
> that is the false rule round 3 shipped: the wait exists because no turn has started yet, so a join
> that waits misses nothing. The measurement is still owed — it bounds how much of a first turn's
> *opening* can fall outside the subscription — but it can no longer produce a gap by itself. What
> the window costs is bounded and recoverable instead: the subscription retries, `thread/resume` on a
> running thread delivers that thread's live stream, and any item whose opening deltas fell inside
> the window is completed by `item/completed`, which carries the item's FULL text. A gap is emitted
> only for history that CANNOT be recovered — a thread that had already run turns before the daemon
> joined (`backend_prior_history`, §R7.7).

**(f) Two clients, deliberately, and only two.** The daemon is a *second* client of the same thread —
which is precisely what R1 leg 3 proved is safe (97 frames delivered to an observer with the TUI
still fully functional, `r1-codex-gate.md:93-119`). The TUI is the first. Swarm opens no third.

### R7.3 How the events reach the InteractionSource seam without making the adapter impure

The pump calls the SAME function the hook path calls —
`Daemon.captureInteractions(sessionID, ad, adapter.HookPayload{…})`
(`internal/skeleton/interaction.go:187`) — with:

| Field | Value | Why |
|---|---|---|
| `Event` | the JSON-RPC **method** (`item/agentMessage/delta`, `turn/completed`, …) | the same slot `cb.Event` fills for a hook; the adapter dispatches on it |
| `Raw` | the **whole frame, verbatim as recorded** | makes `r1-codex-fixtures/frame-samples.json` literally the golden vector set, which is ADR-010's own stated benefit of reusing `HookPayload` (`ADR-010:59`) |
| `ReceivedAtMs` | the daemon's receipt instant of the **first** frame folded into this payload | timestamps are daemon-side (ADR-010 §3); a batched delta's honest capture instant is its earliest content's, per `shapeItem`'s own rule (`interaction.go:238-241`) |

`HookPayload`'s name is already documented as historical — "it carries any captured event body, hook
or JSON-RPC" (`ADR-010:59`). Nothing about the seam changes. The adapter gains a pure
`Interactions` arm per recorded method and returns normalized fields and nothing else; ids,
ordering, the turn, caps, redaction, hashing, expiry and transport stay exactly where ADR-010 §3 put
them. `Ref` is `params.itemId`, which is what folds successive `agentMessage` deltas under one
`item_id` (`itemIDLocked`, `interaction.go:307`) — the adapter is still the only party that sees the
CLI's own id, and the daemon still consumes it before the wire.

**Typed status (M4.5) does NOT ride `engine.HandleCallback`.** The rejected draft routed it there and
justified the reuse with a sequence-namespace claim that inverts the actual property: sharing one
counter between two allocators is safe only because `hookclient.nextSequence` takes `LOCK_EX`
(`internal/hookclient/hookclient.go:116-128`), not because the file is the same. And the consequence
of getting it wrong is a silent DROP, not a warning — `hookSeqDuplicate` discards the callback and
`ingestHookBytes` errors (`internal/skeleton/hookdrain.go:73-81`) — while `markHookSeqIngested`
fsyncs a durable seen-set per callback (`hookdrain.go:289-315`), which is tolerable at
turn-lifecycle rates and fatal if item frames ever reach it. More basically, the daemon does not need
to authenticate to itself: the token check, the durable replay set and the on-disk counter buy
nothing an in-process producer does not already have.

**Decision: a direct engine seam.** `Engine.ApplyTypedEvent(sessionID, event string, payload
map[string]string) error` performs exactly what `HandleCallback` does *after* the token check —
`deriveDims` -> `withChildrenHoldingTheTurn` -> `withoutPostStopReactivation` -> `applyTyped` ->
`commit` -> `emit` — with the sequence drawn from a per-session **in-memory** monotonic counter the
engine allocates under `e.mu`. `applyTyped`'s per-dimension high-water is retained (it is what
rejects a stale reorder and is real value); the fsync, the token and the durable seen-set are not.
Frames arrive in order on one WebSocket connection, which is what makes an in-memory counter
sufficient.

**Single-writer is enforced, not assumed.** A session with a declared backend is registered with **no
hook token and no hook env injection** (`injectHookEnv`, `launch.go:499`, becomes conditional).
`HandleCallback` already refuses a callback whose token is empty or mismatched
(`engine.go:281-284`), so a backend session cannot have two typed producers competing for one
high-water namespace. This costs Codex nothing: its typed rows have never fired (the D1 debt,
`codex.go:39-45`), so there is no working hook path to lose. Fence, by mutation: mint a hook token
for a backend session and a permanent test asserting the two producers are mutually exclusive fails.

### R7.4 Where the 200 ms batching sits, and the append arithmetic redone against the SHARED slot

The batcher is a field of the **pump**, in `internal/skeleton`, between the client's frame channel
and `captureInteractions`. It is not in the adapter (purity), not in the gateway (too late — the
record already exists, and `ItemAdmission` is already there), and not in the shim (the PTY plane).
That is the only reading of M4.2's "at the adapter" that is legal, and it is faithful to the intent:
the coalescing happens **at the producer edge, before an item exists**, which is where the program
put it.

Rules, all fenceable by mutation:

1. Consecutive `item/agentMessage/delta` frames for one `(session, itemId)` are folded into ONE
   synthesized frame carrying the **concatenation** of their `delta` strings and the last frame's
   other fields — the same method name, the same shape as
   `r1-codex-fixtures/frame-samples.json`'s recorded delta frame. It is exactly the frame the server
   would have emitted had it chunked more coarsely; no shape is invented.
2. **200 ms flush, or earlier on an ordering boundary.** An open batch is flushed *before* any other
   frame for that session is emitted, so ordering is never disturbed. Session end flushes.
3. **Approvals bypass the batcher entirely.** `item/*/requestApproval` is emitted the instant it
   arrives, mirroring IS-DELTA-3's head-of-queue rule one layer up.

**The arithmetic. There are TWO 125 ms floors, and the rejected draft's numbers were right only at
N=1.** R1 leg 4 recorded **586 `item/agentMessage/delta` frames in one turn** over roughly 14 s —
about 42 frames/s (`r1-codex-gate.md:104-105,208-209`). Batching at 200 ms turns that into **<= 5
offered records/s for that session**. That much stands. What does not is what happens next:

- `remotegw.ItemAdmission` (daemon-side) releases at most one item record per `DefaultAppendWindow`
  = 125 ms, machine-wide, merging the surplus losslessly by text concatenation within one `item_id`.
- `remotegw.CoalescingSink` (gateway-side) is a **second, independent** floor of the same width, and
  it states in as many words that it "IS THE ONE PLACE THE COMBINED CEILING CAN BE ENFORCED", because
  an `ItemAdmission` release arrives there as a journal record and is "charged to the same slot as a
  snapshot" (`internal/remotegw/coalesce.go:36-45`).
- Journal records are **forwarded immediately and may never be coalesced or dropped** (R-GW.5,
  `coalesce.go:46-49`), but they still SPEND the slot. Terminal snapshots are held oldest-first
  behind it.

So at N=3 streaming Codex sessions the rejected draft's own worst case offers 15 records/s,
`ItemAdmission` releases 8/s, and those 8 consume **all eight** of `CoalescingSink`'s slots per
second, leaving the terminal plane exactly **zero**. "Nothing is dropped" is true of items and false
of the guarantee `DefaultAppendWindow` exists to protect (`coalesce.go:11-16`, PB-GW-7): a live peek
freezes on a stale grid for as long as the sessions stream. Even at N=1 the honest statement is 5/8
of the budget, not "the terminal plane can breathe" as a general claim.

**Decision: split the target budget explicitly instead of letting the two planes race for it.**
`ItemAdmission`'s floor widens to a new constant `DefaultItemWindow = 250 ms` (<= 4 item releases/s
machine-wide), leaving >= 4 snapshot slots/s for the terminal plane at every N. The adapter-edge
batch window stays a flat 200 ms — its job is to stop 42 frames/s from ever reaching a serialize, a
cap pass and a queue slot, not to enforce the ceiling — so no adaptive-in-N window is introduced and
no new state is added anywhere.

**Widening the item floor is safe precisely because the merge is lossless.** `ItemAdmission` merges
what it holds by text concatenation within one `item_id` (ADR-010 §7); a wider window merges MORE and
loses nothing. The cost is latency and only latency, and it is stated rather than left to be
discovered:

| Streaming sessions | First token to glass |
|---|---|
| N = 1 | <= 200 ms (batch) + <= 250 ms (item floor) + <= 125 ms (gateway slot) + transport |
| N sessions | <= 200 ms + N x 250 ms (the item floor queues) + <= 125 ms + transport |

The playbook's <= 300 ms p95 budget is measured from *accepted item* to *visible update* and so
excludes the first two terms, but a reader of this ADR should know the whole number. At N = 3 that is
roughly 1.1 s to first token, against a terminal peek that stays live at 4 snapshots/s.

**The owner ruling this asks for, stated as a knob and not as a hidden default.** `DefaultItemWindow`
is the single constant that trades multi-session token latency against terminal-peek liveness. 250 ms
splits the budget evenly. If the owner prefers token latency, lowering it toward 125 ms restores the
rejected draft's numbers and, at N >= 3, restores the frozen peek. R7 ships 250 ms and records the
choice; it does not ship a number computed as if the second floor were not there. Fence: the existing
`internal/remotegw/append_budget_test.go` gains an N-session case asserting that terminal snapshots
still reach the sink while item records are being released — mutate `DefaultItemWindow` back to 125 ms
and it must fail.

Claude is affected too, and the effect is small: `PreToolUse` + `PostToolUse` is two appends per tool
call, so the floor binds above roughly 2 tool calls/s machine-wide instead of 3, and merges there
rather than drops.

### R7.5 Inbound: one op, per-CLI dispatch, and no keystroke ever

**This is the correction of a live defect, not only new work.** `Daemon.composerSend`
(`internal/skeleton/chat.go:113`) resolves the session, checks `expected_turn`, and calls
`injectComposerText` (`chat.go:227`) — which writes the text and a CR into the PTY — for **every
provider, with no seam and no provider check anywhere on the path** (`protocol/remote_chat.go:108`,
`remotegw/command_loop.go:930`, `skeleton/chat.go:185`). A phone send to a Codex session today types
into the Codex TUI. That is the thing playbook §8.2 forbids in as many words ("No Codex semantic
operation is implemented by terminal keystroke injection") and it is reachable now, before any R7
code. R7 must close it, and must close it **structurally** rather than by naming `codex` in the
daemon:

- `composer_send` resolves a **message sink** per session instance: a live backend client ->
  `turn/start` when the daemon's turn is empty, `turn/steer` when it is not, carrying the native
  `expectedTurnId` (recorded: `turn-steer.json` returns the unchanged turn id, which is the
  built-in optimistic-concurrency guard R1 note 4 says to propagate rather than reinvent). No
  backend -> the adapter's keystroke seam, **if it proves one**. Neither -> refuse
  `structured_unsupported`, having typed nothing.
- The keystroke composer therefore becomes an **explicit optional adapter seam**, exactly like
  `TurnInterrupter` (`internal/adapter/interaction.go:319-338`), so that **absence is the refusal**.
  Today it is an unconditional `sub.Input([]byte(text))` with no seam to be absent from, which is
  precisely why it reaches Codex. The Codex adapter implements no such seam and never will, so the
  fallback is structurally unreachable for it — ADR-010 §5's posture doing the work a provider name
  would otherwise have to do.
- `turn_interrupt` takes the same two-branch shape: backend -> `turn/interrupt` (recorded:
  `turn-interrupt.json` returns `{}` and `turn-completed-interrupted.json` carries
  `"status": "interrupted"`); else `AsTurnInterrupter`; else `interrupt_unsupported`. An interrupt
  of an already-finished turn returns `{"code":-32600,"message":"no active turn to interrupt"}`
  (`errors-observed.json`) and is **benign**, not an error surface: the daemon's own `stale_turn`
  precondition (`chat.go:340-350`) already refuses that case before the RPC is sent.
- **Approvals (M4.3)** get the native branch `approveInteraction` does not have today: it falls
  straight through to `applyDecision` -> `dialogTap` -> `ApprovalKeys` -> PTY
  (`skeleton/approval.go:538`, `skeleton/inject.go:73-124`), and Codex is saved from being typed at
  only because `AsApprovalApplier` is false and the whole thing refuses `errNoApplier`. R7 branches
  on the backend first and calls `InteractionSource.Decision(ref, decisionID)` — which **has no
  production caller anywhere in the repo today**, a fact worth stating plainly — and writes its
  `DecisionAction.Reply` as the JSON-RPC response. Two properties fall out and both are recorded:
  the reply must go out on **the daemon's own connection with the id that connection received**
  (JSON-RPC ids are per-connection; the pending request is matched by `params.itemId`, which
  `approval-request.json` carries), and **resolution still arrives only by observation** — here as
  the server's own `serverRequest/resolved` broadcast, which is strictly better evidence than the
  grid observation decision 3 step 3 settles for on Claude. First-answer-wins is server-side, so the
  daemon guesses nothing when the owner answers at the terminal.

**Decision 3's Codex paragraph (`:175-176`) is re-affirmed and given its positive form**: no
keystroke is ever injected on a Codex session — not for an approval, not for a message, not for an
interrupt — and after R7 that is enforced by the absence of a seam rather than by the accident of an
unimplemented interface.

**The composer ECHO correlation is decided here too, because the backend branch can do better than
the mechanism it inherits.** Today `composerSend` records a `pendingSend` and
`stampComposerEchoLocked` claims the echo by **text** within a 10 s TTL (`chat.go:52-78,259+`) — a
mechanism whose own comment records a PROBED mis-attribution, an owner-typed "yes" stamped
`source=phone` with the phone's operation id. Claude cannot do better: its `UserPromptSubmit` hook is
the only echo and it carries no injection id. **The backend branch can, and must.**

- The Claude path is UNCHANGED. `stampComposerEchoLocked`, its TTL and its 8-deep FIFO stay exactly
  as they are.
- On a backend session the text path is **not reached at all**. A `pendingSend` is keyed by the RPC
  reply's own identifiers: `turn/start` returns the `turnId` (recorded), and the stamp is applied to
  the first `item/userMessage` observed on that turn.
- For `turn/steer`, whether the reply carries the steered message's own `itemId` is **NOT RECORDED**
  (`turn-steer.json` records only the unchanged turn id). Settle it offline with
  `codex app-server generate-ts --out <dir>`. If it does, the stamp is exact. If it does not, the
  fallback is the first `item/userMessage` observed on **that turn** after that steer, within
  `pendingSendTTL` — still scoped to a turn the daemon itself started, which is strictly narrower
  than machine-wide text matching, and it is recorded as a residual rather than claimed exact.
- What is NOT shipped: text correlation on Codex. R7 does not carry a known attribution defect onto
  a new provider.

**One recorded contradiction, flagged rather than resolved here.** ADR-010 §5 records Codex's
decision vocabulary from spike S-B as `accept | acceptWithExecpolicyAmendment | cancel`
(`ADR-010:70`); R1 leg 4 recorded `accept | acceptForSession | decline | cancel` against
codex-cli 0.147.0. These disagree, almost certainly by version. The vocabulary is per-version
characterization data and must be re-recorded, not chosen: settle it offline with
`codex app-server generate-json-schema --out <dir>`, and let the fixture be the source.

### R7.6 Lifecycle: daemon restart, the app-server's OWN crash, and CLI upgrade

**Daemon restart / upgrade.** Shims and their app-servers survive it (ADR-001). On reconcile the
daemon proves the backend live by pid (§R7.2c) before it dials, then `initialize`/`initialized`, and
rejoins the thread it recorded at launch. **A successful rejoin is not a proven gap** and MUST NOT
degrade the session: `markSessionDegraded` writes a one-way durable history boundary and withdraws
chat until the exact current sink is freshly re-proven. A rule that invented that boundary on every
daemon restart would permanently scar every live Codex transcript and unnecessarily withdraw its
composer on the first `swarm daemon restart` — the operation ADR-001 exists to make ordinary. A gap
is emitted only when the rejoin fails, or when the interval demonstrably cannot be backfilled.

**What backfills it is not fully recorded, and this is the largest open mechanical question in R7.**
`turn/completed` carried the turn's `items` in one recorded sample (`frame-samples.json`,
`itemsView: "summary"`) and an EMPTY array in another (`turn-completed-interrupted.json`,
`itemsView: "notLoaded"`), and `thread/started` advertises `historyMode: "paginated"`. So an
ordered, resumable item read plausibly exists (`thread/read` is in the client-request inventory) and
its shape was never captured. **Settle it offline, before writing the reconnect path**:
`codex app-server generate-json-schema --out <dir>` and `codex app-server generate-ts --out <dir>`
give `ThreadReadParams`, the `ItemsView` union and whether `itemsView` is client-selectable. If a
lossless backfill exists, reconnect is silent; if it does not, reconnect emits an honest
`structured_gap` and ADR-017 T2's degrade applies — never a silent bridge.

**The app-server's OWN crash — the one lifecycle event this topology introduces, and which the
rejected draft did not cover.** Under `codex --remote unix://SOCK` the TUI is a CLIENT of the
app-server: R1 leg 2 recorded its whole boot handshake, model resolution and MCP boot going through
the server. So if the app-server dies mid-session the owner's terminal is **dead, not merely
unmirrored**, and a swarm-launched Codex session is strictly less reliable than a hand-run `codex`.
That is a regression on the terminal product bought with the phone product, and it is stated as one:

- **No restart, in R7.** A restarted app-server has no thread state and the TUI's connection is
  already broken, so a restart would buy the live session nothing while adding a supervision policy
  with no recorded behavior to test against.
- **The shim contains the session rather than leaving it hung.** If the backend exits before the
  agent, the shim immediately runs the agent's existing TERM->grace->KILL, so the owner gets a clean
  session end instead of a TUI wedged against a dead socket. This is the same escalation path, fired
  from a new edge (the backend's `Wait` returning first), which is why §R7.2a's dedicated `Wait`
  goroutine exists as a first-class thing and not as a reaper afterthought.
- **The exit is distinguishable.** `exit.json` gains a `backend_exit` field, so "the Codex backend
  died" is never reported as an unexplained agent exit. A `structured_gap` covers the un-backfillable
  tail before the session goes to exited.
- **This needs an owner ruling and R7 must not proceed as if it had one.** The accepted trade is: a
  swarm-launched Codex session has two failure points instead of one, in exchange for token-live
  chat and native approvals. The alternative if the owner refuses is not a different supervision
  policy — it is not using `--remote` at all, which is R7 not shipping. The ruling is therefore real
  and belongs to the owner, not to the implementer.

**Codex CLI upgrade under a live session.** The running app-server keeps running; only a new session
gets a new binary. `SessionCapabilities.ProviderVersion` is already per-session-instance
(`internal/protocol/schema/capability.go:13`). Frame-shape drift is caught the way ADR-010
obligation 4 already requires — per-version recorded fixtures — and degrades safely by construction:
an unrecognized method shapes **zero** interactions rather than a guess, and the grid heuristic is
still declared, so status falls back to what it does today instead of going dark.

### R7.7 Capabilities: an HONESTY dependency, not a safety gate; and what a dead backend says

The rejected draft said "R7 must not ship the composer branch without" the capability-publication
slice (`agents-tracker-hggx.2.1`). That is wrong in both directions and is restated here.

**On SAFETY it was over-stated.** Safety comes entirely from §R7.5's sink resolution: no backend and
no keystroke seam means REFUSE, having typed nothing. That is sufficient on its own, and
`requireStructuredComposer` cannot help at all — `registerSessionCapabilities` has **no production
caller** (`capability.go:88-100`, restated at length in `chat.go:185`'s own KDoc), so no live session
has a record and the only reachable arm is the durable degrade marker. **R7 may ship the composer
branch without the capability slice.**

**On HONESTY it was under-stated, and this is the real dependency.** The phone's composer
availability reads `structured_gap` off the transcript (`SessionDetailPanel.kt:763-772`:
`structuredChat = !transcript.structureTorn`), not a capability record. So a Codex session whose
backend is dead or was never connected still SHOWS a composer, the owner types, and the refusal
arrives *after* the tap. ADR-017 T2 wants that surfaced before it.

**Decision: yes, a dead-or-never-connected backend on a Codex session emits a `structured_gap`, and
the three cases are distinguished so a permanent history boundary is never invented for a
recoverable one.**

1. **Never connected.** The backend was declared in the launch config and its socket never became
   servable, or the daemon could not dial it at launch-confirm. This session has no proved structured
   plane: emit `structured_gap` with reason `backend_unavailable` at launch and withdraw chat
   durably. The composer is off before the first tap unless the exact current sink is later proved.
2. ~~**Transient (daemon restart, rejoin succeeds).** No gap, no degrade — §R7.6's rule, which exists
   precisely so `swarm daemon restart` does not permanently disarm every Codex composer.~~
   **Amended in place, 2026-08-20 (pre-commit correction).** The decision is UNCHANGED — no gap, no
   degrade — but the struck text left its cost unstated, and the implementation comment justifying
   it asserted something FALSE: that "this thread's earlier turns were captured by the daemon that
   launched it". They were not, for the daemon-downtime window. The agent keeps working against the
   surviving shim while no daemon is attached; those turns are recorded in the app-server's own
   rollout and are absent from the journal, because a client receives a thread's items only from
   the point it resumes. **A successful rejoin therefore SILENTLY BRIDGES every turn that ran while
   the daemon was down**, which is the one thing ADR-017 says a transcript may not do.
   **Amended again 2026-08-30.** The phone no longer derives send authority from
   `!transcript.structureTorn`: the durable marker and current sink authority are separate, and an
   exact current-instance proof may recover future sending without erasing the marker. The no-gap
   behavior still stands for a narrower evidence reason: a successful rejoin alone cannot establish
   that any turn occurred during downtime and therefore cannot name an exact missing boundary.
   *What would close the remaining history risk:* backfill the interval with
   `thread/read {includeTurns:true}` and gap only what the backfill cannot recover — **ADR-013 Q4**,
   open because the `itemsView` returned in practice is unrecorded and a `summary` view is lossy for
   a long turn. Implementation: `internal/skeleton/backendconnect.go`'s `rejoinSessionBackend`.
3. **Died mid-session.** §R7.6 ends the session; a `structured_gap` covers the tail so history is
   honest about what was not captured.

> **Amended, revision 4 (2026-08-20) — a fourth case, and one non-case.**
>
> 4. **Joined a thread that had ALREADY RUN TURNS.** The rollout file existed at the first
>    `thread/resume`, which proves a turn had already happened, and a client receives a thread's
>    items only after it resumes — so this daemon's transcript begins mid-conversation. Emit
>    `structured_gap` reason `backend_prior_history` and **do not degrade**: the tear is in the
>    history, the channel is healthy, and `markSessionDegraded` is reserved for a proven loss of the
>    plane itself.
>
> **NOT a case: a subscription that is still pending.** A fresh thread has no rollout because no
> turn has started. That is not a gap, not a degrade and not a failure — it is the ordinary state of
> every Codex session between launch and the owner's first message. The connection is registered as
> the message sink regardless (revision 4 ruling 2), so the composer works and the first turn is what
> ends the wait. Round 3 answered this state with `backend_joined_late` plus
> `markSessionDegraded`, which removed the composer from every ordinary healthy session — a
> ONE-WAY, DURABLE verdict founded on the owner having taken 45 s to think.

The capability derivation defect the rejected draft found is REAL and stands, and it is now the
capability slice's obligation rather than R7's precondition: `deriveSessionCapabilities`
(`capability.go:333-334`) derives `structured_chat` from `adapter.AsInteractionSource(a)` — a fact
about the **adapter TYPE**. The moment the Codex adapter implements `InteractionSource`, a
pre-upgrade Codex session with no backend at all would claim `structured_chat=true`. It must become
**seam AND live backend, per SESSION INSTANCE**. The same correction applies to `Interrupt`, derived
from `AsTurnInterrupter`, which would read `false` for a Codex session whose RPC interrupt works
perfectly. R7 lands the derivation change with its backend fact available; the publication of those
records stays with `agents-tracker-hggx.2.1`.

**M4.5 and the grid heuristic.** `internal/adapter/codex/codex.go:39-45` already declares
`turn/started`->active, `turn/completed`->idle and `item/commandExecution/requestApproval`->permission,
with the file's own header recording that the producer is deferred (the D1 debt). M4.5 pays that debt
by **building the producer** (§R7.3's `ApplyTypedEvent`), not by changing the mapping. **The heuristic
row STAYS**: `internal/engine` already ranks a fresh typed signal above the heuristic within
`StalenessThreshold` and *preserves* prior status on an inconclusive read (`engine.go:11-12,330-361`),
so the two sources cannot fight; `evaluateCodexGrid` is the T-3 fallback ADR-007 requires and the only
thing that keeps a pre-R7 session working; and
`TestSignalSources_DeclaresTypedEventsWithStatusMapping` (`codex_test.go:184`) fails outright if the
row is removed. **Two rows should be added, both from recorded frames**: `item/fileChange/requestApproval`
— the approval the gate actually captured (`approval-request.json`), while the adapter declares only
the `commandExecution` sibling — and `serverRequest/resolved`->interaction `none`, without which
`permission` sticks until `turn/completed`. Note that `protocol-methods.txt` inventories client
requests and server **notifications** and does **not** list the 8 server-to-client **requests** the
gate counted; the exact set of `*/requestApproval` methods must come from `generate-ts`, not from
guessing at siblings.

**Mid-session across the upgrade.** A session launched by the pre-R7 binary has argv `codex` with no
`--remote`, no backend child and no `backend_socket_path`. After the upgrade it keeps the grid
heuristic and behaves **exactly as it does today** — the discovery is file-driven and an absent key
means "no backend" (the `HookSocketPath` convention). What it does not get is structured chat, and
per the derivation fix above it will correctly say so.

### R7.8 The R6 phone: what works unchanged on a Codex session, and what does not

Traced through the shipped R6 code rather than assumed. **Nothing architectural is Claude-specific;
three concrete things are, and all three are copy or render decisions rather than plumbing.**

Works unchanged: the transcript renders by item **kind** with no provider switch anywhere
(`TranscriptPanel.kt:blockFor`); the approval sheet decodes decisions with **the CLI's own ids**, a
rule it states explicitly against Codex's own vocabulary (`ApprovalItem.kt:160-161`); history paging
and detail-on-demand read journal records and never a provider (`skeleton/chat.go:373,499`); the
composer's availability gate reads `structured_gap` off the transcript (`SessionDetailPanel.kt:763-772`,
`Composer.kt:89`); and `expected_turn` is produced by `TranscriptScreen.openTurnOf`, which mirrors the
daemon's `turnIDLocked` line for line (`TranscriptPanel.kt:339-353` vs `interaction.go:323`).

Does **not** work unchanged:

1. **The turn never closes if `turn/completed` cannot produce a terminal `agent_message`.** Both the
   daemon (`interaction.go:329-333`) and the phone close a turn **only** on a terminal
   `agent_message`. An interrupted Codex turn recorded `items: []` with `itemsView: "notLoaded"`
   (`turn-completed-interrupted.json`), so there may be no agent message to close it with — and a
   turn that never closes means `expected_turn` never goes empty and **every subsequent phone send is
   refused `stale_turn` forever**, which is exactly the R6 round-2 blocker that broke idle replies
   100% of the time. R7's rule: `turn/completed` is what closes the turn — it is the only frame that
   distinguishes completed/interrupted/failed — folded onto the turn's own agent message when
   `items` names it, else emitted as a terminal `agent_message` carrying `stop_reason` and no text.
   Whether the empty case can be removed entirely (by making `itemsView` `summary`) is the
   `generate-ts` question of §R7.6.
2. **A stop_reason-only `agent_message` has nothing to render.** The `AGENT_MESSAGE` arm draws
   markdown of `item.text` and nothing else; an interrupted turn must read as "Interrupted", which
   is new Kotlin.
3. **Mid-stream prose has no running mark, on purpose, and Codex is the first producer to need
   one.** `TranscriptPanel.kt:388-391` records the decision and its exact premise: "no adapter emits
   that today (`internal/adapter/claude/interaction.go` always closes an agent_message
   `StatusCompleted` in the same record that carries its text), so there is no wire value to drive a
   test off yet". Codex's `in_progress` increments make that premise false. The file itself defers
   the design question ("what it looks like on a SENTENCE rather than on a tool's single line"); R7
   is when it comes due.

Two copy strings also become false rather than merely incomplete: the Stop confirmation says the
interrupt is "the same key a person would press at the terminal"
(`SessionDetailPanel.kt:266`), and the approval sheet's KDoc and refusal copy are written around
`ok` meaning "the daemon TYPED the dialog's keys" and around `no_dialog`
(`ApprovalSheetPanel.kt:130-146`). On Codex no key is pressed, `ok` means the RPC reply was sent, and
`no_dialog` cannot occur.

### R7.9 What this amendment does NOT decide

Named so a later agent reads an assumption here as drift rather than as a decision:

- **Whether `item/commandExecution/outputDelta` is shaped at all.** The lean R7 answer is to open the
  `tool_run` at `item/started` and fill its `output_excerpt` at `item/completed`, matching what
  Claude does today (no regression, no accumulator, no new phone work) and dropping the delta frames.
  That answer is only correct if `item/completed` for a `commandExecution` actually carries the
  output — **the gate recorded `item/completed` for a `userMessage` only**. Settle with
  `generate-ts`'s `CommandExecutionItem` before choosing.
- ~~**Whether the TUI can be pointed at a daemon-created thread**, and therefore whether §R7.2e's
  `agent_args` carries a thread id or is empty.~~ **CLOSED by revision 3**: the daemon creates no
  thread, so there is nothing to point the TUI at. `agent_args` is `--remote unix://SOCK` and the
  daemon adopts the agent's thread from `thread/started`.
- **Whether a lossless post-outage backfill exists** (`thread/read`, `itemsView`), and therefore
  whether a daemon restart is ever a `structured_gap`.
- **The exact set of the 8 server-to-client request methods**, which `protocol-methods.txt` does not
  list.
- **Whether `turn/steer`'s reply carries the steered message's `itemId`** (§R7.5's echo correlation).
- **Codex's decision vocabulary at 0.147.0**, where ADR-010 §5 and the R1 gate disagree.
- **`file_change` shaping from `turn/diff/updated`.** The frame is recorded (a real unified diff) but
  IS-FC-1's applied-vs-proposed distinction against Codex's approval flow is not worked out here.
- **`thread/tokenUsage/updated`, `account/rateLimits/updated`, `turn/plan/updated`, reasoning
  deltas.** All recorded or inventoried, none shaped in R7.
- **opencode's equivalent (M5.1).** The backend-descriptor seam this amendment's companion adds is
  shaped so opencode's `serve` port fits it, but M5.1 is not decided here.
- **Any change to the Claude paths.** Decision 3's injection design, its key map, its gate and its
  watchdog are untouched; the only Claude-adjacent change is that the keystroke composer acquires an
  explicit seam it currently lacks, which is a fence around today's behavior, not a change to it.
  `stampComposerEchoLocked` is likewise untouched on Claude.

### R7.10 Consequences: what this topology costs, and what it forecloses

Stated as trades rather than left to be discovered. All three are accepted; none is free.

1. **The Codex transcript is LESS crash-durable than Claude's.** Claude's capture survives a daemon
   outage on disk: `HookChannel.SpoolPath` exists for exactly that, added in R6 because "the shim's
   hook server shuts down with the agent it reaped" (`internal/daemon/launch.go:143-148`). R7's
   frames exist only in the daemon's memory between the socket and `captureInteractions`, so a daemon
   outage loses every frame in flight, and §R7.6 is honest that whether `thread/read` can backfill is
   UNKNOWN. There is no durable spool on the JSON-RPC path in R7. If `generate-ts` shows a lossless
   `thread/read`, the gap is backfillable and this consequence softens; if it does not, a daemon
   outage on a Codex session is a `structured_gap`, which is honest but is a real difference from
   Claude. A shim-side spool is NOT the fix — it would put the JSON-RPC parse back in the PTY plane,
   which §R7.1 rules out.
2. **A detached or pre-existing `codex` can never be mirrored.** The backend is bound to shim launch,
   so attaching to a `codex` the owner started themselves stays heuristic forever. This is the right
   trade (swarm owns the session from process creation, RC-D1), and it means M5's exit criterion —
   "every session opens into a live chat or an honest status card, nothing in between" — acquires a
   third class: a Codex session that is neither, being heuristic-only but not degraded. M5 must name
   that class explicitly or R7 must emit a `structured_gap` for it. R7's answer: it is the case-1
   `structured_gap` of §R7.7, so the card is honest and the class is two, not three.
3. **One app-server per session, forever.** The socket is in the session dir and the process is a
   child of the shim, which is right for isolation and containment and costs **two processes per
   session** (R1 leg 1 recorded a node launcher plus a vendored rust binary) — twenty at ten
   sessions. It also forecloses ever sharing one app-server across sessions, which matters if
   anything about it turns out to be per-process rate-limited or billed. Accepted: isolation and a
   containment story that works are worth more than a process count at the scale this product runs
   at, and the seam (ADR-010's `BackendSpec.SocketPath` being CORE-supplied) is where a future
   sharing decision would land.
4. **Multi-session token latency regresses to buy terminal-peek liveness** (§R7.4): 200 ms + N x
   250 ms + 125 ms rather than 200 ms + 125 ms. `DefaultItemWindow` is the knob and the owner's.
5. **A swarm-launched Codex session has two failure points instead of one** (§R7.6). Owner ruling
   required.
6. **A terminal-answered approval that lands inside the phone's send window is RECORDED AS THE
   PHONE'S, and the phone is told its approve failed.** Added 2026-08-20 (round-3 review MEDIUM 2).
   §R7.5 says "first-answer-wins is server-side, so the daemon guesses nothing when the owner
   answers at the terminal". That is true of the EFFECT and false of the RECORD, and the difference
   is not fixable at this protocol. `ServerRequestResolvedNotification` carries `{threadId,
   requestId}` and **no decision and no answerer** — re-derived from the installed CLI's own
   bindings, and the recorded frame in `frame-samples.json` agrees — so the resolution broadcast
   cannot say who answered. The daemon marks a phone answer applied BEFORE the RPC leaves
   (`internal/skeleton/approval.go`), and the resolution handler attributes any subsequent
   resolution to `by: phone` with the phone's decision and `operation_id` whenever that mark is
   set. If the owner answers AT THE TERMINAL inside that window, the journal records the phone's
   decision, by the phone, for an answer the server took from the keyboard; and because the same
   broadcast retires the request id, the phone's own RPC then finds nothing to answer and the phone
   is returned an error. So history says the phone answered it while the phone was told it failed.
   ACCEPTED AND DISCLOSED rather than fixed: narrowing the window (marking applied only after the
   reply) shrinks it but cannot close it, since the server can resolve between the send and the
   reply, and no field exists that would let the daemon tell the two cases apart. The SAFETY
   property is unaffected — exactly one decision is ever applied, and it is the server's — and the
   evidence's CANNOT section states this in the same words.
7. **A daemon that joins a session MID-TURN adopts that turn without having seen its opening.**
   Added 2026-08-20 (round-3 review MEDIUM 1). `turnIDLocked` opens a turn on any frame naming a
   native turn id it has not already closed, not only on a `user_message`, because a daemon that
   held NO turn for a running one read as IDLE to everything downstream: the phone's composer send
   would carry an empty `expected_turn`, match, and take the `turn/start` branch — a SECOND
   concurrent turn on one thread — while Stop was impossible, since interrupt refuses an empty
   expected turn. The cost is that such a turn's earlier items are not in this transcript unless
   `thread/resume` replays them, which is Q4 and remains UNRECORDED; the adopted turn is therefore
   correct about what is RUNNING and silent about what it MISSED. If Q4 shows resume replays the
   running turn's items, the adoption arm becomes belt-and-braces rather than load-bearing.
