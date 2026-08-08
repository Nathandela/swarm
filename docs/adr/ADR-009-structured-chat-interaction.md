# ADR-009: The phone surface is a structured chat transcript — the terminal grid is retired

**Status**: Accepted (owner sign-off 2026-08-07)
**Date**: 2026-08-07
**Amends**: ADR-007-remote-access.md — the 2026-07-23 amendment's item 2 (line 295-297), item 4's
**binding screen set** (line 309-313) and item 4's phasing clause (line 318-320); the 2026-07-24
amendment's **Decision 2** (line 437-445), narrowed in role, not withdrawn, and that amendment's
**GG-7 clause** (line 450-453), narrowed by (8). **Settles** the open question B133 left unanswered
(line 7923-7934). **Leaves intact**: D7 (line 62-66) in full — including its rule that an approve
carries its own signature and is *never translated into a blind keystroke*, which (4) now honours on
the fallback path too — B43's withdrawal of the offline queue, the 2026-07-24 **Decision 1**
keystroke transport (line 399-420), Decision G (line 474-486), and B133's trust boundary and SAS
ruling.

**Companions** (drafted alongside, not restated here): `docs/adr/ADR-010-adapter-structured-capture.md`
(the adapter-contract extension S-B requires), `docs/adr/ADR-011-multi-device-epochs.md` (multi-device
epoch handling, decided now and implemented post-v1), and `docs/specifications/interaction-schema.md`
(the normative item schema).

## Context

ADR-007 chose the terminal peek as v1's phone UX on 2026-07-23, and the reason it gave was honest at
the time: the chat transcript was *speculative*. Item 2 deferred the composer to "Phase 2 (gated on
spike S-A)", and item 4 listed "chat transcript S-A, approval sheets S-B/S-C" among the screens
"explicitly out of v1". Those clauses were written as bets on three unfinished spikes.

All three have now run, and they discharge the gate:

- **S-A** (`docs/verification/spike-SA.md:96`): **PARTIAL** — chat-shaped text derives cleanly from
  VT-diff for ordinary multi-turn conversation in both CLIs, cross-version-stable, gated by two
  mechanically-checkable rules for the two observed failure modes (overlay transition, truncated
  tool output). Not a blocker; a specification.
- **S-B** (`docs/verification/spike-SB.md:7`): the real nested permission payloads, `tool_input`
  included, **are capturable**. The only obstacle is process: extending the frozen adapter boundary
  is ADR-level work. ADR-010 is that work.
- **S-C** (`docs/verification/spike-SC.md:97`, `:190`): a Claude Code `PermissionRequest` hook holds
  a decision for **300 s** with no timeout, auto-deny or error (every wait within ~15 ms of the
  staged delay); Codex's `requestApproval` holds for **120 s** measured. Minutes-later remote
  approval is architecturally available — the C3 audit worry is retired.

So the bet resolved in favour of the surface ADR-007 deferred. And the peek's own premise reads
worse under scrutiny than it did in July: a character grid is the *machine's* view of an agent,
shrunk onto a handset. The thing the owner needs from a phone is not a smaller terminal. It is the
two questions a terminal makes them reconstruct by eye — what is the agent doing, and does it need
me to say yes.

## Decision

**1. The phone's only session surface is a structured chat transcript.** A transcript of interaction
items — user and agent messages, tool-run cards, file-change diffs, plan updates — plus tappable
approval cards. **No terminal emulation and no raw grid anywhere in the app.** ADR-007's item 2 is
inverted: the chat transcript *is* the v1 input UX, and the terminal-peek screen is retired as a
user-facing surface. Item 4's phasing clause is retired **for its first two entries only** (chat
transcript, approval sheets); voice, quiet hours, activity-feed depth and Live Activities stay out
of v1 exactly as written. Item 4's *binding screen set* is restated in the same move — terminal peek
struck, chat transcript, approval card and prompt card in its place — so that "exactly these
screens" names the set that ships (see the obligations below).

**2. The machine-side renderer survives; its role as the phone's display does not.** ADR-007's
Decision 2 is narrowed, not withdrawn. Every word of its rationale still holds and is reaffirmed:
hostile-PTY sanitization stays on the trusted side, the phone-core stays thin, no VT emulator
crosses the gomobile boundary, and raw PTY bytes never reach the phone. `internal/daemon/
terminalrender.go` remains the security choke point, and interaction items are produced on the
machine adjacent to it. The renderer keeps exactly two consumers: the **generic fallback adapter**
(which shapes the sanitized snapshot into transcript form for CLIs with no native event stream) and
the **prompt-card fallback** in (4). Both are machine-side, so **no snapshot frames are appended to
a phone**: `TerminalSnapshot` and `terminal_watch` stay on the wire unchanged — no protocol change,
nothing deleted — but no phone surface issues a watch, and the machine→phone append budget in (7) is
spent by the journal alone. What is deleted is the phone *rendering a grid to a human*.

**3. The terminal well is deleted at the end of slice I1.** I1 is this program's first
implementation slice. The I-numbering is the interaction program's own and is deliberately not the
`remote-v1-roadmap.md` Phase-A task list, whose `A1` is a different, already-shipped daemon task
(`remote-v1-roadmap.md:45-46`, `:84-85`). I1's exit is the chat transcript rendering real
interaction items on the handset. On that exit, the plain-text terminal well goes with the same
commit — `PhoneSurface.kt`'s `peekHost` / `PeekPanel` path and the screens under it. This is a dated
obligation and not an aspiration: a fallback surface that outlives its replacement stops being a
fallback and becomes the design. One slice of two surfaces is the accepted cost of not shipping a
broken transcript; two slices is a decision nobody made.

**4. The approval fallback is a prompt card, never a grid.** S-C found a carve-out the hook cannot
resolve: a Bash command referencing a specific file path trips a separate interactive confirmation
that the hook's `allow` does not answer (`spike-SC.md:108-129`). ADR-007's answer to that class was
deep-link-to-peek plus a take-control keyboard. **That answer is replaced.** The phone renders the
sanitized prompt region as a *card* whose buttons carry the same signed `ActionApprove` op every
other approval card uses. The keystroke mapping is data the daemon holds, not a payload the phone
authors: the daemon validates the binding tuple, `content_hash` and its own expiry, and only then
does the **machine-side adapter** inject the mapped keystroke into the PTY. No take-control lease is
involved, and D7's "a stale or mismatched approve is rejected daemon-side and never translated into
a blind keystroke" therefore holds on the fallback path exactly as it holds on the hook path — the
carve-out is in *how the decision is applied*, never in *how it is authorized*. Nothing on the
approval path rides the unsigned keystroke plane. Rejected alternative: keep the deep-link-to-peek
keyboard as the fallback. It reinstates the grid this ADR deletes, and it asks the owner to type
blind into a viewport too small to show what they are answering — the worst moment to hand someone
the least legible surface. The roadmap's B3 "terminal peek + take-control keyboard" screen
(`docs/research/remote-v1-roadmap.md:255-256`) is superseded by the transcript, the approval card
and the prompt card.

**5. Raw input stays exactly as decided, as the substrate.** D7's live-only rule stands: no offline
queue, ever, and B43's reasoning for why one is *unbuildable* from these commands is not reopened.
The 2026-07-24 Decision 1 keystroke transport — sealed mailbox envelopes under the epoch
`ContentKey`, gateway-side seq gate, riding the lease rather than per-keystroke signatures — is
unchanged. What changes is what the phone *authors* on it. It no longer authors raw keystrokes:
there is no keyboard left to type them, prompt-card decisions are signed `ActionApprove` ops per (4),
and a **composer send** is a distinct remote-tier op carrying `(session, expected_turn, text)` per
(8) — lease-authorized and live-only like the keystrokes it replaces, but shaped so the daemon can
read the precondition it must enforce. Structured items are the durable, replayable surface; input
verbs remain live-only. Decision G (concurrent owner + phone
control, and the A4/TUI remote-lease indicator follow-up) is untouched.

**6. Lease severance: backgrounding is a first-class trigger.** This settles B133's open question.
A lease ends on: an explicit **Release** control, **app backgrounding**, transport loss, a
**machine-sealed severance notice** (device revoke, kill switch, `swarm remote off` —
`SeverAllRemoteControl`, `internal/protocol/server.go:1482`, which B133 made the only surviving
mitigation and which this list must not be read as dropping), and the two time walls that already
exist (the device-signed `ExpiresAt` and `MaxControlSessionTTL`). The chat composer acquires the
lease per send under the hood and extends nothing beyond these.

**Invisible acquisition is not invisible suppression** (PB-INPUT-2). The composer removes the
*ceremony* of taking control, never the confirmation: the send item carries a visible
pending → sent → refused state, and nothing leaves the phone until the lease generation is
confirmed. A send that cannot get a lease is shown refused, not silently swallowed.

Backgrounding is named as its own trigger rather than left to the disconnect it happens to force.
`internal/phonecore/lease.go:218-225` currently answers by consequence — "BACKGROUNDING IS NOT
ITSELF A TRIGGER … backgrounding DISCONNECTS the phone (ADR-007 B16) and the transport loss is what
severs." **Stated without gloss: that is not wrong today.** B16 decided PB-RUN-3 by dropping the
connection on every background state and shipping no foreground service in v1, so backgrounding does
sever, and naming the trigger changes no observable v1 behaviour. It is required for two other
reasons. First, B133 demanded a *behaviour decision* and expressly forbade the clause being dropped
while the comment was reworded — an answer that holds by consequence is not the answer it asked for.
Second, that consequence is derived from a connectivity choice B16 itself scopes to v1 and prices
against live alternatives (`dataSync`'s ~6 h/day force-stop, `specialUse`'s Play-review dependency);
if the foreground-service question is ever reopened, a severance guarantee resting on it would
change silently. Under (1) the composer acquires the lease *invisibly*, which is exactly when a
guarantee must not be an emergent property of transport. Naming the trigger costs one call at the
backgrounding hook and makes the rule true independently of B16.

This is scope-limiting, not authentication. B133 removed every phone-side user gate and nothing here
restores one; there is no freshness to lapse and none is reintroduced.

**7. Interaction items ride the existing E2EE journal.** The item `kind` lives inside the journal
record's `payload` — i.e. inside the AEAD-covered plaintext — and an interaction record is sealed as
a **bare journal record**. The mailbox-frame `kind` value set is **unchanged** and gains no member
(`interaction-schema.md` IS-LAYER-1/-2): two different fields are called `kind`, and adding a value
to the outer one would add a demux branch nothing expects. PB-SYNC-1 is explicit and measured: a
direction or kind tag "may live in neither `SenderKeyID` nor `EpochID`, because both ARE the bucket
key" — a value in either forks the buckets per tag or collides two streams, and the collision was
observed costing typing for the life of the epoch. Ordering rides the existing journal cursor.
Payloads are size-capped: excerpt inline, detail on demand.

**Admission is per target, not per item.** §6.0's binding ceiling is <= 8 appends/s sustained across
journal **and** terminal combined for one target (`remote-phaseB-requirements.md:372`), and the
gateway may not coalesce a journal record — R-GW.5, `internal/remotegw/coalesce.go:112-120`, "a
journal record is never coalesced, deferred or dropped". Admission is therefore producer-side or
nowhere, and the producer's window is per **target**, across every concurrently streaming session —
not per `item_id`, which N sessions multiply straight past the ceiling. `interaction-schema.md`
IS-DELTA-2 is read under that ceiling. Overrunning it is not a latency bug: quota-refused appends
burn outbound seqs and manufacture gaps (PB-GW-7), and PB-SYNC-1 then conservatively stales journal
*and* terminal on any shared-bucket gap. (2) is what makes the budget affordable — with no snapshot
appends, the transcript inherits the whole of what the peek used to spend. The normative shapes are
`docs/specifications/interaction-schema.md`, not this ADR.

**8. Approval lifecycle.** The D7 binding tuple is unchanged (`machine`, `session`,
agent-instance `{shim_pid, shim_start_time}`, `interaction_id`, `content_hash`, `expires_at`;
`operation_id` distinct from `interaction_id`; daemon-authoritative expiry, phone countdowns
display-only). It is unchanged because **every** approval decision — card and prompt card alike —
travels as the signed `ActionApprove` op (`interaction-schema.md` IS-LIFE-4), which is what gives
the daemon an object to validate the tuple against; (4) is a carve-out in application, not in
authorization.

Three mechanics are adopted from the Codex source audit and are now required: pending approval
requests are **re-delivered to a reconnecting client**; an `approval_resolved` event fires even when
a request is **cancelled or superseded**, so stale cards dismiss on every surface; and
composer/steering input carries an **`expected_turn` precondition**, which kills the race between
what the phone rendered and what the user tapped. The daemon is the enforcer, and it holds no
`ContentKey` and reads an already-ordered raw byte stream — so `expected_turn` cannot ride raw
`TDataIn`, and the composer send is the control op of (5) instead. This **narrows** the 2026-07-24
Decision 1 GG-7 clause ("the input channel adds NO new GG-7-covered `Control` fields",
`ADR-007-remote-access.md:450-453`): the composer-send op and its `expected_turn` field take a
`protocol.md` field-table row in the commit that adds them. The narrowing is confined to this one
op; keystroke framing, resize and `take_control` keep the clause as written.

**9. The relay is untouched.** Self-host only, untrusted, ciphertext-only. Nothing here makes it
smarter; ack and replay semantics stay endpoint-to-endpoint, and the B133 trust boundary — wire
adversary, phone trusted, SAS as the sole human-in-the-loop step — is unchanged.

## Consequences

### Positive

- The gate ADR-007 set is discharged with evidence rather than waived: item 2 deferred chat *on
  S-A*, and S-A now returns a specification instead of a question.
- The A7 "no control-sequence injection at the phone" property gets stronger, not weaker. It was
  structural because the phone displayed pre-sanitized text; it is now structural because the phone
  has no grid to inject into.
- One approval mechanism covers both CLIs at their measured limits (S-C), with a prompt card for the
  carve-out — and the fallback is a card, so the "no grid" invariant has no exception.

### Negative, stated plainly

- The peek UI already built on Android is discarded. That is real work thrown away, and the honest
  accounting is that it was built against a clause this ADR reverses.
- Slice I1 ships two session surfaces at once. Bounded by (3), and the bound is the mitigation.
- The generic fallback adapter inherits S-A's two unfinished production rules — overlay-transition
  and truncated-tool-output (`spike-SA.md:98-99`). **Both must be finished before the fallback
  ships**; the transcript must never claim to hold tool output it only saw a truncation marker for.
- S-A's own FAIL condition still applies: if content scrolls past the 40-row viewport inside one
  quiescence window, the fallback needs journal+snapshot as ground truth with grid-diff as a live-
  tail optimization only (`spike-SA.md:101`). That is a fallback-adapter constraint, not a
  transcript-wide one — the native adapters do not derive from the grid at all.

### Spec-amendment obligations, firing when implementation lands

These are enumerated so they cannot be discovered late as drift (implementation-goals.md GG-7,
orchestration protocol step 6):

- **ADR-007 item 4's binding screen set** (line 309-313) and `docs/research/remote-control-design.md`
  §8 name the eight screens the exported surface "must feed **exactly**", terminal peek among them.
  Amending only the phasing clause would leave the retired screen bound. The set is restated as:
  pairing/onboarding, triage inbox, session detail, **chat transcript**, machines, approval card,
  **prompt card**, activity feed, settings — terminal peek struck.
- **PB-INPUT-2** (`remote-phaseB-requirements.md:500`) is satisfied, not narrowed: (6) keeps the
  visible confirmation and removes only the ceremony. If its wording ("input is suppressed until a
  new lease is visibly confirmed") is read as demanding a visible *acquisition*, the amendment
  restates it as a per-send confirmed state. The fence in `internal/phonecore/s11_lease_test.go`
  keeps asserting a non-empty reason on every severance.
- **system-spec D-9** (line 70) describes the journal as "session lifecycle and status-group
  transitions". Interaction items add a kind to that journal; D-9 must be amended in the slice that
  writes the first item, not after.
- **system-spec F-2** (line 143-145) is unchanged — B133's threat model survives this ADR intact.
  Say so in the amendment rather than leaving the reader to infer it.
- **P-5 / Decision G** unchanged; no exclusivity claim is restored.
- **GG-7 protocol drift** is machine-checked, not procedural: `internal/protocol/protocolmd_test.go`
  and `protocolmd_bidi_test.go` fail the build when `docs/specifications/protocol.md`'s field table
  and the `wire` structs disagree. `interaction_id`, `approve` / `ApproveReq`, `expires_at` and
  `terminal` already exist and need no new row. The **composer-send op and its `expected_turn`
  field** are the one new row this ADR creates (8), and they land in `protocol.md` in the same
  commit. The item `kind` discriminator is **not** a `Control` field — it is AEAD plaintext per (7) —
  and adding it to the wire table would be the wrong fix for a red test.
- **`internal/phonecore/lease.go:218-225`** (the `Sever` doc comment) and the backgrounding case in
  `internal/phonecore/s11_lease_test.go:218-224` encode the answer (6) replaces. Both change, and
  the test must assert that the backgrounding hook severs *directly* rather than that a transport
  loss does it. This is a behaviour change with a test change attached, which is the only shape it
  is allowed to take.
- **ADR index rows** in `docs/adr/README.md` for all three companions (009, 010, **011**), same
  commit as this file, plus the stale "next is ADR-009" instruction moved to ADR-012.

## Amendment 2026-08-07 — `MaxItemBytes` is raised to 16 KiB, so §5's own maxima fit inside it

**Status**: Accepted (owner ruling, Nathan, 2026-08-07). **Amends**: this ADR's hand-back of §5's
numbers to `docs/specifications/interaction-schema.md`, which left them "proposed and unratified".
This ratifies **one** of them — the whole-item cap — and leaves the per-field numbers proposed.
**Also cited as "ADR-009 Amendment 1"** (it is this ADR's first), which is how the code comments
and fences name it — ADR-010 carries a same-dated amendment and the number disambiguates.

### What was wrong

An 8 KiB `MaxItemBytes` bounded neither of the two things a whole-item cap exists to bound, and
both failures were confirmed on the shipped path rather than argued:

1. **§5's own per-field maxima did not fit jointly inside it.** An item on the table's own numbers
   was over the table's own item cap, so the producer's fit stage had to cut fields that were
   already legal. Recorded as R2's open point in `docs/verification/a1-carriage.md`.
2. **The one merge §6 sanctions overran it.** IS-DELTA-2 folds pending `agent_message` increments
   for one `item_id` into one append and calls the merge "lossless text concatenation". With
   `MaxTextBytes = MaxItemBytes / 2`, two increments already clipped to `MaxTextBytes` produce an
   item the append boundary refuses — and by then the floor has dequeued it, so the text is
   **dropped silently**: logged once, nothing marked damaged on any surface. Confirmed in
   `a1-carriage.md`'s re-review of R2 and reproduced verbatim in `a1-gateway-floor.md`'s RED for
   this amendment (`interaction: item is 8368 bytes, over the 8192-byte cap`).

### The arithmetic (measured, not estimated)

Every figure is the serialized item as `internal/skeleton.fitItem` produces it, measured through
the shipped producer (harness in `a1-gateway-floor.md`; it is not kept).

| Case | Serialized | Note |
|---|---|---|
| `approval_request` at §5's maxima | 11 736 B | 40 × 200-rune prompt lines, 256 B summary, 4 × 256 B action strings, 8 decisions × 256 B labels |
| …plus §3.5's D7 tuple | 11 930 B | `agent_instance`, `expires_at`, `content_hash` |
| …plus the `truncated`/`full_bytes` pair | 11 967 B | the worst-case approval |
| `plan_update` at §5's maxima | 15 166 B | 64 steps × 200 B, longest step state (`in_progress`) |
| …plus the truncation pair | **15 203 B** | **the worst single item §5's table can describe** |
| `tool_run` at §5's maxima | 5 415 B | the record-collapse union of an open and its close is ≈ this, not the sum |
| `file_change` at §5's maxima | 5 372 B | |
| merge union, 2 × `MaxTextBytes` | 8 368 B untruncated / **8 405 B** with the truncation pair | **the worst sanctioned merge**; the floor's re-marshalled bytes |
| merge union, 3 × `MaxTextBytes` | 12 526 B | fits |
| merge union, 4 × `MaxTextBytes` | 16 622 B | does **not** fit — see the residual below |

The binding constraint is `max(15 203, 8 405) = 15 203 B`. The next power-friendly bound above it
is **16 KiB = 16 384 B**, leaving 1 181 B of headroom on the worst single item and 7 979 B on the
worst sanctioned merge. 8 KiB was verified insufficient and 32 KiB buys nothing the arithmetic
asks for, so:

> **`MaxItemBytes` = 16 KiB (16 384 bytes).**

### The consequence, stated honestly

**The per-item wire budget doubles against an unchanged rate budget.** §6.0's combined ≤ 8
appends/s per target and the relay's `MailboxAppendPerMin: 600` are untouched (§10 says this
schema spends the budget differently and does not raise it). A target saturating the append slot
with maximal items now moves up to 128 KiB/s of plaintext instead of 64 KiB/s. That is accepted:
the cap is still ~48× under the relay's ~768 KiB per-envelope plaintext admission limit, the
budget that actually binds is the append *count*, and a cap that drops an agent's message to save
bandwidth nobody is short of is the wrong trade.

**A second consequence, added by the adversarial review of 2026-08-07.** The ~48× headroom is per
*single append*. IS-CAP-4's `journal_reseed` is the one frame that **aggregates** records, so the
number of interaction records a reseed can carry before it exceeds that same admission limit is
**halved** — roughly 96 maximal items to roughly 48. IS-CAP-4 already requires the events half to
be bounded at "a record count that fits" and **nothing implements that bound**:
`remotegw.Gateway.Resync` seals every record above the phone's cursor into one frame, over a
journal that production opens with unbounded retention (`journal.Open`, `Options{}`). The gap is
therefore pre-existing and orthogonal to this ruling, which makes it twice as easy to reach rather
than creating it. Carried as an open point against IS-CAP-4, not against this amendment.

**The joint bound is over bytes as §5 counts them, and three things still exceed it.** The fit
stage (`skeleton.fitItem`'s stage 2) is therefore **kept unchanged**, and IS-CAP-5 says so
normatively: `prompt_lines` is capped in *runes* (40 × 200 four-byte runes is 32 000 B); `tool`,
`path`, `old_path`, `truncation_marker` and `decisions[].id` carry no per-field cap at all
(IS-TOOL-3 requires the marker verbatim); and JSON escaping can expand a byte-capped field by up
to 6×. No finite item cap removes the need to clip.

**Residual, not closed by this ruling: an unbounded fold.** `ItemAdmission.concatText` merges
*every* increment pending for one `item_id` in a window, not two. Four increments at
`MaxTextBytes` inside one 125 ms window serialize to 16 622 B and are still refused and dropped.
Reaching it takes ~16 KiB of agent prose in 125 ms (≈ 131 KB/s), which is far above any observed
CLI token rate, and no adapter streams increments at all today — but it is the same defect in a
narrower window, and closing it needs a rule this amendment does not make (a bounded merge would
contradict IS-DELTA-2's "lossless"; splitting the fold across two appends changes the floor's
one-append-per-window contract). Recorded as an open point in `a1-gateway-floor.md`.

### The alternatives, and why this one

The re-review recorded four resolutions (`a1-carriage.md`, "CONFIRMED DEFECT, NOT FIXED"). Each is
rejected here on the record:

- **Bound the merged text in `concatText`** — cheapest, and directly contradicts IS-DELTA-2's
  "the merge is lossless text concatenation". Rejected: it silently truncates the agent's message
  at a seam that has no `truncated`/`full_bytes` to set honestly.
- **Refuse the fold and let the increment take its own slot** — lossless, but it changes the
  floor's contract (one item may cost two appends), needs the cap plumbed into `internal/remotegw`
  which deliberately does not link the daemon, and carries the re-homing trap the re-review
  documented (a third increment folds into the *first* held entry, shipping the message scrambled
  — worse than the drop it replaces).
- **Truncate the merged item under IS-CAP-1** — R2's answer for a single item, but it makes the
  *floor* a truncator, which is the opposite of lossless, and the floor cannot say which §5 cap
  bound.
- **Raise the item cap** — chosen. It is the only one of the four that fixes the *cause*: the
  overrun was manufactured by `MaxTextBytes = MaxItemBytes / 2`, a relation between two numbers of
  which one was never measured. It changes no normative rule, needs no new code path, and it also
  closes the first failure above, which none of the other three touch.

### What binds now

- `interaction-schema.md` §5's table (`MaxItemBytes` = 16 KiB) and **IS-CAP-5**, which makes the
  joint bound a rule rather than an observation and requires any future ruling that raises a
  per-field cap to re-derive the item cap in the same ruling.
- `internal/daemon.MaxItemBytes` = `16 << 10`, its comment no longer marked PROPOSED.
- Three fences in `internal/skeleton/interaction_cap_test.go`: the merge union lands un-dropped,
  a `plan_update` at the documented maxima ships whole, and the item cap admits both worst cases
  (the arithmetic itself, so a later per-field raise fails a test rather than a transcript).

## Notes

ADR-007 chose the terminal peek honestly, and under the information it had it chose correctly: three
spikes were open, and shipping a chat view that silently dropped an agent's output would have been
worse than shipping a small grid that never lies about what it shows. The spikes closed the other
way. This is recorded as a decision, with its date and its evidence, so the next agent to read
`PeekPanel.kt` and find it deleted meets a ruling instead of a gap — and so nobody restores a grid
to the phone believing they are fixing a regression.
