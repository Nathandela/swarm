# ADR-013: Mirror capture architecture — structure arrives beside the PTY, and a phone answer is typed into the CLI's own dialog

**Status**: Accepted (owner-approved direction 2026-08-13, "that's exactly the direction"; the M1 items it records are built, tested and pushed)
**Date**: 2026-08-13
**Program**: [docs/specifications/mirror-program.md](../specifications/mirror-program.md) — the plan of record; this ADR records the decisions inside it, not the wave schedule.
**Companions**: [ADR-009-structured-chat-interaction.md](ADR-009-structured-chat-interaction.md) (the phone surface is a transcript, the grid is retired), [ADR-010-adapter-structured-capture.md](ADR-010-adapter-structured-capture.md) (the optional `InteractionSource` seam this feeds), [ADR-007-remote-access.md](ADR-007-remote-access.md) (D4/D7 signed-op and binding-tuple rules), [docs/specifications/interaction-schema.md](../specifications/interaction-schema.md) (the normative item fields).
**Evidence**: [mirror-m0.md](../verification/mirror-m0.md), [mirror-m1.md](../verification/mirror-m1.md), [channels-spike.md](../research/channels-spike.md).

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
- **The stray-keystroke race is closed by the gate, and the gate is why the design is a gate at
  all.** Between the phone rendering a card and the tap arriving, the owner may have answered at
  the terminal. A key typed at a dismissed dialog lands in the composer as input the agent acts on.
  So the injection is refused (`no_dialog`) when the live grid no longer shows an answerable
  dialog, and refused (`already_applied`) when an answer has already been typed and is awaiting
  observation — the hole the design itself opened, since "already resolved" no longer catches a
  re-delivered approve during the observation interval. **The gate and the keystroke share one
  seeded view**: the subscription is seeded with the grid as of the moment it joined and writes
  through the same handle, so the screen the recognizer judged is the screen the keys are typed at,
  and no second dial can slip a repaint between them. Every refusal leaves the card PENDING — the
  daemon declined to type; it decided nothing.
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
interaction history, so the next free number is 015 once that one lands.
