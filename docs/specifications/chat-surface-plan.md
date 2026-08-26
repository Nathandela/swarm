# The conversation surface: plan of record

Owner-approved direction 2026-08-26, from three screenshots and one verdict: *"the flow to access a
discussion from swarm is a disaster."* Tracker: epic `agents-tracker-tbpm`, twelve children, plus the
independent P0 `agents-tracker-bzfe`.

Owner-facing artifacts, and which is authoritative for what:

- **The drawing** — https://claude.ai/code/artifact/4f2f2277-de36-4242-a73a-b80c86f1ecee — is
  authoritative for **components, states and copy**. A state not drawn there is a state the screen
  may not enter; a string not tabled there is not on screen.
- **The flow document** — https://claude.ai/code/artifact/7830b3ff-65bb-4710-8fff-448335b944b7 —
  carries the argument, the audit committee's findings and the rulings. It is authoritative for
  **why**, and for the list of what is deferred.

Grounding: four read-only traces of the shipped code (phone screen inventory, control/lease trace,
terminal-side inventory, reference-pattern survey) and an adversarial audit committee — codex
(gpt-5.6-sol), Opus and Sonnet reported; **Fable could not sit (out of usage credits) and its
design-honesty lens is uncovered**; agy was auto-denied headless. Every claim below about current
code carries a citation, and each wave's RED phase re-verifies its own citations before building on
them.

---

## 1. What is being fixed

The session screen is a document, not a conversation. Measured on a Pixel-class viewport:
**~414 dp of fixed chrome on a healthy session** — nav header, `Conversation` label, lease notice,
Take control, Stop, Kill, composer, tab bar — before one message is drawn, and 640-720 dp in the
torn state the owner photographed, leaving ~150 dp for the conversation. The three stacked CTAs are
160 dp; the composer is 64.

Three defects sit under that:

1. **The ceremony is theatre.** `composer_send` is lease-free at every layer — no lease reference in
   the gateway arm, in `handleComposerSend`, or in `skeleton/chat.go`. The daemon's lease gates
   exactly `forwardInput` and `forwardResize` (`internal/protocol/server.go:957-1000`), and every
   verb behind those is in `android/unbound-verbs.tsv` with zero Kotlin callers since R6 replaced
   them with `composer_send`. The composer is greyed by **one line**:
   `PhoneSurface.kt:1827 setKeyboardEnabled(lease.keyboardEnabled)`, where
   `SessionScreens.kt:294` defines `keyboardEnabled = leaseHeld && online`.
2. **The screen contradicts itself.** `"Read-only -- take control to type."`
   (`SessionDetailPanel.kt:641`) is drawn on `!leaseHeld` with no capability input;
   `"This session's structured record broke..."` (`:248-249`) is drawn on `!structuredChat`. The
   conditions share no term, so both render together — the default state of every status-card
   session, because the Kotlin router has only two arms (`PhoneSurface.kt:2227-2245`).
3. **Two sends interleave.** `skeleton/chat.go` unlocks `itemMu` at `:184` before
   `deliverComposerText` at `:186`, and `tapSub.Input` takes no lock at all
   (`internal/skeleton/sessiontap.go:366-370`). Two sends passing the same `expected_turn` produce
   `text_A · text_B · CR · CR`: one submitted concatenation and one empty submit. Filed as
   `agents-tracker-bzfe`.

---

## 2. Rulings adopted

**Owner, first round (2026-08-26).**

- **R1 Control model — no activation.** A live session is typeable from phone and terminal
  simultaneously, always. "Take control" leaves the product. No terminal-side enable feature.
- **R2 Destructive.** The composer carries Stop; Kill moves into a header overflow menu behind its
  existing confirmation. Neither costs vertical space in the reading flow.
- **R3 Tool runs.** Collapsed by default.
- **R4 Approvals.** The question is a message in the stream carrying its own buttons.

**Owner, second round, after the committee reported.**

- **R5 Draw everything first, build once.** No Kotlin until the drawing is signed. The single
  exception is `bzfe`, which is a live defect on its own merits.
- **R6 A sent bubble is pending until the agent's own transcript echoes it back.**
- **R7 A pending decision wears a badge on the inbox and is answered inside the conversation.** One
  renderer. The lost ability to answer from the triage surface is accepted.

**Owner, third round, closing the drawing's open questions.**

- **R8 Expanded tool output opens in place up to a bound, then on its own screen.**
- **R9 A file change is a chip in the flow; its diff opens on its own screen.**
- **R10 No `+` button.** The composer is a field and its send/stop control.
- **R11 The terminal repairs land in this wave.**

**Committee corrections that overrode the proposal as written.**

- **R2 is amended before it is built.** "Send becomes Stop" would leave a user able to type feedback
  mid-turn and unable to submit it — and continuing from the phone while tools run is the
  demonstration this programme exits on (playbook §8.1). The rule is: **empty composer while working
  shows Stop; non-empty shows Send with Stop still reachable**, and which fires is read from the
  live field at press time, never from state.
- **R3 splits.** Flipping the default on the per-item cards that already ship is the readability
  win. Cross-item grouping is **deferred** — see §9.
- **R4's buttons are the CLI's.** `interaction-schema.md:146-155` and `:259-266` permit one to eight
  CLI-defined labels; IS-APR-4 keeps the verdict machine-side and
  `internal/skeleton/interaction_chain_e2e_test.go` fails the build if a verdict rides along.
  "Allow/Deny" is precisely what this surface may not author.
- **The lease has three consumers, not one.** The board marker, a grid-tap skip
  (`internal/skeleton/serve.go:617`), and — load-bearing — `anyControlled` (`serve.go:613-620`),
  the gate on ADR-010 Amendment 3 C3, *"the supervisor never types into a session someone is
  driving"* (`internal/skeleton/supervision.go:9-10`). Deleting the remote lease naively lets the
  passive supervisor type into a session the phone is chatting in.

---

## 3. Slice 0 — serialise the writer (`agents-tracker-bzfe`)

**Ships first, independently of the drawing.** Ungating the composer is what makes a person send
three messages in a row, so this cannot land after.

| # | Item | Where | Test / evidence |
|---|---|---|---|
| 0.1 | RED: two concurrent `composer_send` calls on one session produce one concatenated submit and one empty submit | `internal/skeleton/` new test | must fail against `main` for the right reason, and the failure recorded |
| 0.2 | RED: an owner draft parked in the PTY between the phone's check and its write survives byte-exact, and the send is refused | `internal/skeleton/` new test | the B13 case (`chat.go:337-345`), stated as a test rather than a comment |
| 0.3 | GREEN: quiescence guard | `internal/shim/server.go:798-811` | `ptyWriter` is already **the** single serialised writer with a mutex on every write. Add a byte counter under that same lock, reset on a forwarded submit byte |
| 0.4 | GREEN: one shim op holding the lock across the whole message | `internal/shim/` | under one `ptyWriter.mu` hold: check `sinceSubmit == 0`, write text, wait `submitframe.Gap`, write CR, reset — or refuse having written nothing. Precedent for the held gap: `supervision.go:16` |
| 0.5 | GREEN: route the phone's send through the daemon's serialised entry point | `internal/skeleton/chat.go`, `internal/protocol/sendinput.go:115-127` | `Server.sendMessage` already takes `attachMu` then `inMu` for a whole message (`:214`, contract at `:167-195`); the phone path uses `tap.subscribe` instead |
| 0.6 | The refusal reaches the phone as its own outcome | `internal/protocol/remote_chat.go`, `mobile/` | drawn in the drawing as `bubble.refused`, copy: *"Not sent — the terminal's input line was not empty."* |

**What this deliberately does not do.** It never characterises the CLI's input region — the thing
`chat.go:345-357` rightly refuses to guess, and the thing ADR-017:175's `expected_input_revision`
would require. It claims only that *nobody has written to this PTY since the last submit*, a fact the
shim owns absolutely because it is the only writer. It errs safe: typed-then-deleted-to-empty still
refuses. Roughly 30 lines; the revision never crosses the wire, only the predicate.

**Scope note for the amendment.** The merge is exclusively a property of the keystroke sink
(`resolveMessageSink`'s second branch, `chat.go:218-239`), and the only `ComposerKeys` implementor in
the tree is Claude (`internal/adapter/claude/interaction.go:405`). The backend branch (`:249-284`)
never touches the PTY and has no merge problem. Record that R1's residual risk is scoped to
keystroke-sink providers and has a known exit.

**Gate on R1:** until 0.3-0.5 are green, either ungate only providers with a structured sink, or
hold R1. Both reviewers said so independently; adopted.

---

## 4. Wave A — the design system owes rows before the kit owes code (`tbpm.1` in part)

`android/gate/s23_kit_test.go` holds a registry of every kit factory with either an `Origin:`
(a selector in the design source) or a `Derived:` (a row in `docs/design/substrate-components.md`),
plus a `Why:`. It is checked **in both directions** —
`TestPBDS6_EveryKitFactoryIsAnInboxComponent` and `TestPBDS7_EveryDerivationCitationResolvesToARow`.

**`docs/research/obsidian-maquette.html` is owner-signed and is never edited.** New components take
`Derived:` rows in `substrate-components.md`, which is the derivation document written for exactly
this case.

| # | Item | Where |
|---|---|---|
| A.1 | Derivation sections for the new components: `messageBubble`, `conversationHeader`, `conversationMenu`, `fileChangeRow`, `earlierChip`, `decisionPill`, `gapDivider` | `docs/design/substrate-components.md` |
| A.2 | Registry entries with `Derived:` and `Why:` for each | `android/gate/s23_kit_test.go` |
| A.3 | RED first: add the registry entry before the factory exists and watch the gate fail for the missing factory; add the factory before the row and watch it fail for the missing row | the bidirectional check is the test |

Every derivation spends tokens only — `internal/design/tokens.json`, 31 tokens, nothing else — and
states its arithmetic so a reviewer can check it.

---

## 5. Wave B — hosting and insets (`tbpm.1`)

**Blocks everything below it.** The conversation is not a `Destination` today: it is content swapped
inside `INBOX`, and `Destination.forLabel` (`PhoneScaffoldView.kt:110-114`) **throws** on any label
outside the three-entry enum. There is no back stack; back is a hand-rolled boolean union
(`PhoneSurface.kt:798-821`). `phoneScaffoldView` (`:140-234`) wraps whatever content it is handed in
one `ScrollView` with the tab bar as the last non-scrolling child, and offers no branch.

**Decision, and it is the lazy one that holds:** a **second top-level composition inside the existing
Activity**, not a second Activity and not a navigation library. `PhoneActivity` already hosts exactly
one view (`setContentView(surface.root)`, `:134`); the surface gains a second root it can swap to,
and the hand-rolled back handling gains one more case — the same shape it already has for three
sub-states.

| # | Item | Where | Test / evidence |
|---|---|---|---|
| B.1 | The conversation composition: status strip, content that owns its own scroll, pinned composer, **no tab bar** | `ui/screens/PhoneScaffoldView.kt` (a second composition) | Robolectric: the strip is present, the bar is not, and the content's scroll is not the scaffold's |
| B.2 | **Keep `ScaffoldTag.STATUS`.** Its own KDoc (`:46-61`) argues against dropping it: a warning that belongs to one destination is a warning the others do not have | same | a test that the offline strip is reachable *on the conversation*, which is the screen where a person types |
| B.3 | IME insets | `PhoneActivity.kt:160-168` | the platform already dispatches them (targetSdk 35 forces edge-to-edge); the app reads `systemBars()` only. Read `ime()` in the **same** listener and take `maxOf(bars.bottom, ime.bottom)`, mirroring `screenTopOrRealInset` at `:228-229` |
| B.4 | Keyboard-slide tracking | `PhoneActivity.kt` | `WindowInsetsAnimationCompat` — genuinely new, and **optional to the first landing**. Jump is acceptable; drift is not |
| B.5 | Narrow the two universal assertions, deliberately, in the same commit | `PhoneScaffoldViewTest.kt:281-295`, `PhoneSurfaceNavigationTest.kt:77-90`, `:93-108` | both are universal-quantifier tests over *whatever* is on screen, written to catch exactly this. They are scoped to the three tab destinations, with the amendment naming why |
| B.6 | Re-derive the composer and chip placement math for a bar-less container | `s23_kit_test.go`, `KitDensityTest.kt` | `tabbar_height` currently sites the composer's bottom |

**No `android/gate/*.go` gate forbids a bar-less screen or a pinned composer** — verified. The
fences here are the Kotlin estate, and they are narrowed on the record.

---

## 6. Wave C — kit components (`tbpm.2`), parallel with Wave D

Gated by `android/gate/s24_screens_test.go`, which fails the build on an `R.color`, `R.dimen`,
`R.style`, `setTextAppearance`, `setPadding` or `background =` inside `ui/screens/`. Every visual
element is a kit factory; screens compose and arrange, and nothing else. `Gravity` and
`layoutParams` are explicitly the allowed half, so a screen may right-align a bubble — the bubble's
own surface may not be authored there.

| Component | Notes |
|---|---|
| `messageBubble` | right-aligned, elevated surface, card radius with one squared corner. Three states: pending, settled, refused |
| `conversationHeader` | back, name, machine, state dot, overflow. Reuses `presenceDot` |
| `conversationMenu` | Session details · Load earlier · Kill session. **No terminal-view route** (ADR-017:60-65 forbids it on a structured session) |
| `fileChangeRow` | the tool row's shape carrying change verb, path and `+N −M` |
| `earlierChip` | pill at the head of the list |
| `decisionPill` | *Decision needed* — the only persistent affordance in the flow |
| `gapDivider` | the error notice reduced to a rule with a word on it |
| `toolCard` | **no visual change**; the closed state gains a status mark and a clipped mark |
| `workingBar` | reused as-is, at the tail of the list |
| `composerBar` | extended with the stop affordance |

Every target ≥48 dp; every privileged control keeps `filterTouchesWhenObscured` (PB-SEC-12 — Send is
privileged); the decision announces itself to a screen reader and reads in order with its message.

---

## 7. Wave D — models (`tbpm.3`, `tbpm.4`), parallel with Wave C

Pure JVM, no views, testable alone.

| # | Item | Where | Rule |
|---|---|---|---|
| D.1 | One source for *working* | `TranscriptPanel.kt:71-90` already computes the open turn correctly | **Not** `blocks.any { it.running }` (`SessionDetailPanel.kt:786-790`), which misses an agent that is only thinking and leaves a missed completion working forever. The header word, the working line, the placeholder and Stop all read the open turn |
| D.2 | The patch path must survive it | `SessionDetailView.kt:541-558` | the redraw patches only when the diff is confined to the transcript and three whitelisted fields; anything else is a full rebuild that loses scroll position (`:700-703`). Either the field joins the whitelist deliberately, or the header does not carry it. **Decide in the RED phase, not in review** |
| D.3 | Collapse by default | `TranscriptPanel.kt:305` (`collapsed` set), `PhoneSurface.kt:2393` | the mechanism ships; the default flips. `TranscriptPanel.kt:290-296` argues for open-by-default in writing — the counter-argument (a closed line that names its worst outcome hides nothing actionable) is recorded **in that file**, beside the argument it answers |
| D.4 | The closed line carries what collapse would otherwise hide | `ToolCard.kt` | status (`failed` / `declined` / `timed out`, the wire's own words) and `clipped`, because `offersDetail = expanded && truncated && detail` (`ToolCard.kt:61`) would make truncation invisible by default |
| D.5 | The in-place bound and the overflow (R8) | `TranscriptPanel.kt` | open in place to the bound; past it, head plus *Open in full · N more lines* |
| D.6 | File change becomes a chip (R9) | `TranscriptPanel.kt:465` | today the unified diff is drawn unconditionally. The row carries verb, path and counts; the diff opens on its own screen. **Never grouped** |
| D.7 | Send state machine (R6) | `ui/kit/Composer.kt`, `mobile/` | `pending` until the agent's own transcript echoes the prompt back — the fact `stampComposerEchoLocked` (`chat.go:393-399`) already detects. `settled` has no label; `refused` keeps the words and offers the retry |
| D.8 | Four refusal reasons, four sentences | `SessionDetailPanel.kt:247-249` | today one sentence accuses the record of breaking while covering *no record authored*, *record inconsistent* and *machine predates R8*. Copy is tabled in the drawing |
| D.9 | The router's missing arm (`tbpm.8`) | `PhoneSurface.kt:2227-2245` | a status-card session must stop being drawn as a chat. This is what makes the two contradicting sentences co-render |
| D.10 | Dead state, wired or removed | `Composer.kt` `OFFLINE`, `TranscriptPanel.kt:331` `structureTorn` | both computed and read by nothing; an offline session draws a composer identical to a live one |

---

## 8. Wave E — the screen (`tbpm.5`), then Wave F — the deletions (`tbpm.7`)

**E** composes Waves C and D into the conversation screen, migrates the decision card inline at its
item with its material intact (`tbpm.6`), and adds the two new screens: `outputScreen` and
`diffScreen`.

Decision rules, all from the committee: never collapse an unresolved decision; preserve the exact
literal and every label in wire order; lock all choices while one answer is in flight; resolve or
visibly mark the card when the terminal answers first; the pill when it is offscreen; suppress
stick-to-bottom past an unanswered one; never dead-end on a kind this build cannot render.

**F deletes, and only after E is the screen that ships,** so the app is never left with neither the
old affordances nor the new ones. The demolition bill, each item classified DELETED (subject gone) or
MOVED (subject survives, new home), is in `tbpm.7` with every pinning test named. Its heart is one
line: `PhoneSurface.kt:1827`.

**Before F deletes the lease as UX, `anyControlled` needs its replacement**, or the passive
supervisor loses its phone arm and starts typing into a live conversation.

---

## 9. Wave G — the terminal (`tbpm.9`, R11: in this wave)

| # | Item | Rule |
|---|---|---|
| G.1 | Delete `takeoverNote = "took over from phone"` (`internal/attach/attach.go:560`) | it claims an eviction `docs/verification/mirror-m0.md` M0.1 disproved, and it is sampled once before dialling so it could not be live even if it were true |
| G.2 | `phone control` becomes `phone sent HH:mm` (`internal/tui/general.go:794,:894`) | the old marker is lease-driven and the lease is going. The new one reports the fact we have |
| G.3 | The attach row reports the event: `phone sent 2 messages · 09:41` | swarm owns that row |
| G.4 | **Nothing is written into the terminal's own output** | prefixing changes the prompt the agent receives; display bytes corrupt the CLI's screen; ADR-017:203-210 makes the PTY byte-sacred. A test proves the attribution chrome writes zero bytes into the PTY |
| G.5 | It is **not** `phone is here` | presence needs begin/renew/end, expiry, session binding, transport-loss cleanup and multi-device aggregation. We do not name a fact we do not have |
| G.6 | Two stale sites, not one | `internal/protocol/schema/schema.go:253-259` still asserts a live in-attach indicator is impossible; `skeleton/chat.go:298` claims no screen calls `availabilityFor` while `SessionDetailPanel.kt:782` does |

---

## 10. Deferred, each with its reason

- **Cross-item tool grouping.** Needs stable group identity across item ids, member updates,
  expansion state, history-prepend behaviour and detail routing — today one `TranscriptBlock` owns
  one `itemId` (`TranscriptPanel.kt:138-164`) and updates reconcile by that identity. And
  *"destructive commands never collapse" is not implementable on the phone at all*:
  `interaction-schema.md:349-368` requires machine-side classification and forbids this side
  inferring what a command does. A machine-authored sensitivity field comes first, or `execute`
  actions are never aggregated. A count is also a claim about the machine made from what this phone
  happens to hold.
- **Day separators.** Not in this wave.
- **The inbox conversation row.** Last-message preview and time are not fields that exist —
  `mobile/types.go:63-117` omits both and `internal/protocol/server.go:2862-2865` publishes
  `Summary` empty. Cross-boundary work.
- **Terminal presence.** Needs the protocol in G.5. The *recently sent* marker ships instead.
- **Push deep-link.** Stays parked: PB-SEC-11 forbids the activity reading anything off its intent,
  and the wake envelope carries no session identity by design.

---

## 11. ADRs that move

| Document | Amendment |
|---|---|
| ADR-009 structured chat, clauses (5) and (6) | they say a chat send is **lease-authorised**, acquiring one per send, refused visibly if it cannot. None of it was implemented (`mobile/commands.go:387-398` never calls `Leases().Require`). R1 **ratifies an undisclosed drift**; it does not carry out a ruling |
| ADR-009 obsidian direction, D4.4 | the decision keeps its weight through the promoted-slab key light the same document already defines and exempts, drawn inline. One word, no new material |
| ADR-017 terminal fallback | mechanism unchanged. The four refusal reasons get four sentences. The amendment names its own dependency: R1 is safe partly because the phone's raw-keystroke path was never built (`TerminalInputSink`, `internal/protocol/remote_terminal.go:418-455`, answers `CodeNotImplemented`), and amendment C0 parks that slice rather than renouncing it |
| ADR-010 Amendment 3 C3 | the supervision gate reads the lease being deleted and needs a replacement source in the same wave |
| PB-DS-9 | *not* the lever. "One destination above one tab bar" is `PhoneScaffoldView`'s KDoc gloss (`:13`); the recorded requirement is about recomposing on the kit. The clauses actually touched are that the composition matches the recorded inventory and that every string is the recorded copy |

---

## 12. Verification and exit

Evidence file: `docs/verification/chat-surface.md`, and no wave closes without its section.

Every wave is TDD and the failing-first run is evidenced (implementation-goals.md GG-5). Gate tests
change **in the same commit** as the change that requires them, each classified DELETED or MOVED —
never silently dropped. All four quality gates green before the epic closes (GG-4): `go build ./...`,
`go test ./...` (`-race` where goroutines spawn), `go vet ./...`, `golangci-lint run` (it lives at
`~/go/bin`, not on the default PATH).

Tests the committee requires before this is called done:

- A real Claude PTY test parks an owner draft between the phone's check and its write: the send
  refuses and the draft survives byte-exact.
- Concurrent owner Enter, phone send, and two distinct phone sends: no concatenation, no duplicate
  submit, no misordered turn.
- Turn closure or start between `expected_turn` validation and delivery, for both sinks.
- Working composer with empty and non-empty drafts: Stop reachable, feedback sendable.
- Inline decision with one to eight long labels, a long literal, terminal-first resolution, a stale
  tap, reconnect, TalkBack, and the card offscreen.
- Tool cards with failure, decline, running, truncation, detail fetch, an approval boundary, a gap,
  an unknown kind, history prepend, and an incremental member update.
- IME open and close, rotation, process recreation, large font, gesture navigation, scrolled-up
  jump-to-latest, and load-earlier viewport preservation.
- A terminal test proving the attribution chrome writes zero bytes into the PTY.
- Android jank and memory measured at 200 live plus 200 backfilled items
  (`internal/phonecore/interaction.go:44-73`) — the transcript is an eager `LinearLayout` with no
  recycling.

**Exit:** a Claude session on the handset opens directly into a conversation, is typeable without any
control being pressed first, reads as a message stream, and says something true in every state the
drawing names. The owner's physical demonstration (`agents-tracker-11un`) is the exit criterion.

---

## 13. Standing risks

1. **IME is unbuilt from zero** on the app's one exported, security-fenced activity. The insets are
   already dispatched, so reading them is additive — but no soft-input mode is declared and this
   window has never been validated with a keyboard up. Budget cross-device work, not one call.
2. **The scaffold's own suite is written to catch this change.** Narrowing it is a named decision.
3. **R3 reverses a reasoned prior decision.** Answer the argument in the file where it lives.
4. **The working field may force a full rebuild** on every status flip, on the one screen whose
   purpose is continuous reading.
5. **Fable's design-honesty lens is uncovered.** Re-run against the drawing when credits return —
   per the standing owner instruction that Fable is a permanent committee member.
