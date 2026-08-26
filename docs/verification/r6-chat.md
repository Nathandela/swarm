# Wave R6 — Claude complete chat: GREEN evidence (Mirror M2/M3 remaining scope)

**Date**: 2026-08-20 (GREEN round 4 — amended again after review round 3)
**Bead**: agents-tracker-hggx.7
**RED evidence**: [r6-red/chat-red.txt](r6-red/chat-red.txt) — the slice's own RED (2026-08-16),
the fix-pack's (2026-08-19, lines 312-740: the Go lane's ten captures and the Android lane's two
gradle RED runs), review round 2's seven captures (2026-08-20), and **review round 3's
mutation-and-injection record (2026-08-20, the last section of the file), which also carries the
one CORRECTION this pass makes to an earlier capture's wording — stated there rather than by
editing the capture.**
The spool foundation's evidence is spool-red.txt / fixpack-red.txt beside it and is untouched by
this slice.

**How to read this file.** Round 1 of this document claimed rows GREEN that a phone could not
reach: `interaction_history` and `interaction_detail` were listed under CAN with no gateway arm
in the tree, and M2.1/M2.2 read "GREEN" with no production Kotlin calling either renderer. Three
independent adversarial reviewers found those and eleven more. This rewrite is therefore written
to one rule: **every sentence below names the file:line or the test that proves it, and where the
code contradicted a claim the CLAIM was changed.** Nothing was weakened in the code or in a test
to make a sentence here true; the claims this file has had to retract or weaken are listed under
"Claims retracted" with the evidence that forced each — **six from the fix-pack's round, six more
from review round 2, and four from review round 3, sixteen in all.** The disclosures are load-bearing — an
evidence-deletion incident is on record in this wave family (a disclosure was deleted and
replaced with its inverse), so a disclosure here may be sharpened and may never be removed.

## What review round 2 found, in one paragraph

The fix-pack's own reviewer confirmed B1-B13 fixed **by mutation** — reverting each fix and
watching the fence catch it — and then found four more, all of one shape: **a layer that existed
and had never been exercised.** No R6 verb's machine answer reached the screen, because
`VerbDispatch.press` settles on the FACADE CALL and all four verbs return the instant the
envelope is appended; so the composer said "Sent" and erased the draft on local sealing, the
daemon's `stale_turn` had no route onto `ErrorState.STALE_TURN` at all, and neither M3 read ever
folded — `adoptInteractionRead` runs inside `App.Outcome` and nothing called it. Underneath that,
three more things could not have worked even once claimed: the phone sent the CLOSED turn's id,
so an ordinary reply to a finished turn was refused `stale_turn` 100% of the time; the detail
reply could not fold at all (no cursor, terminal item) and forcing it produced a garble; and a
history page was evicted by the very trim it was paged in behind. Every one is fixed below,
failing-first, with the probe frozen as a permanent test. **CAN rows 1, 2, 7 and 8 of the
previous round were false when they were written** — see "Claims retracted".

## What review round 3 found, in one paragraph

Round 3 read the fixed tree and found **one blocker, three majors and the documents lagging the
code.** The blocker is the shape this wave keeps producing: B5's page-on-item-boundary fix was
CORRECT and held by nothing on the path that serves it — all three of its tests call
`historyPageStart` directly, so the CALL SITE could be reverted with the suite green. The majors:
the link-honesty guard was defeated by four one-character label edits (a trailing space, a
trailing dot, a zero-width space, an uppercase scheme), each proven by an executed probe; the two
M3 reads were the only machine-answering verbs on this surface that threw the machine's own
sentence away, so tapping a clipped card whose body the daemon had EVICTED read "Something failed
in a way the app does not recognise. Try again" — advice for a retry that can never work, under
an offer that stayed tappable forever; and the suite was not deterministically green, with two
load-sensitive gates recorded while there were three. **And this file, the ADR and the renderer
all claimed markdown links were TAPPABLE. They are not, and never were** — nothing in production
Kotlin builds a `URLSpan`, a `ClickableSpan`, a movement method or `autoLink`. Links stay
text-only by ruling this round, so what the scheme allowlist and the label-honesty rule protect is
what a reader SEES AND COULD RETYPE, which is a smaller claim than the one that was written, and
the true one. Every one is discharged below, each fix mutation-proven, each flake fixed by
injection or recorded open.

## Per-row status

Status vocabulary: **GREEN** = reachable by a user on a real screen through the real wire and
daemon, with a permanent test. **GREEN (layer)** = that layer is proven and the row names what is
not. **PARKED** = deliberately not built this wave, with a reason.

| Row | Status | The load-bearing artifacts |
|---|---|---|
| M2.1 markdown subset | **GREEN (model + view)** — italic disclosed | `ui/kit/Markdown.kt` (pure `String -> blocks`, http/https allowlist, no HTML pass) and it is now **painted on the row**: `TranscriptPanel.kt:406` (`Markdown.parse`), `TranscriptView.kt:259` (`markdownBody`). `MarkdownTest.kt` incl. injection controls; label/target honesty (B11) at `Markdown.kt`'s `linkAt`/`shownFor`/`hostOf`. **ROUND 3 (F2/F3).** The honesty rule was defeated by four one-character label edits, each an executed probe: `URL_SHAPED` is anchored and requires the host be followed by `[:/?#]` or end-of-input, so a trailing space, a trailing dot, a zero-width space or an uppercase `HTTPS://` made `hostOf(label)` answer null and the LABEL was shown verbatim — the spoof B11 closed, reopened four ways. The label is now NORMALIZED before the host test (`normalizedForHostTest`: invisible characters anywhere, then whitespace, then a trailing FQDN dot) and both the regex and the target's scheme allowlist are case-folded (RFC 3986). Four evasion tests plus an anti-vacuity control that an HONEST label carrying the same decorations is left exactly as written — all five fail under the reverted normalization, the control does not. **AND NO LINK HERE IS TAPPABLE, WHICH THIS ROW USED TO IMPLY IT WAS**: `Span.linkHref` is read only by `styleRun`'s type role, and no production Kotlin builds a `URLSpan`/`ClickableSpan`/movement method/`autoLink`. Links stay text-only by ruling — outbound navigation from adversarially-sourced agent output is new scope this wave does not open — so the allowlist and the honesty rule protect what a reader READS AND COULD RETYPE, not a tap. The KDoc, the test name and the RED record were corrected to say so. **Italic has no register in this design** (Substrate declares no italic face, the kit gate forbids `Typeface.`): its words render, its markers are consumed, nothing distinguishes it — disclosed in `Markdown.kt`'s class KDoc (`:63-68`)` |
| M2.2 tool cards | **GREEN (model + wire + adapter + view)** — default-open, no separator rule | flat `tool_kind` journalled (`skeleton/interaction.go`, schema row §3.3), `Item.ToolKind`/`TranscriptItem.ToolKind` folded and bound, `InteractionItem.kt` +5 fields, `FacadeBridge.kt` maps them, `ui/kit/ToolCard.kt` + `ToolCardTest.kt`; REAL Grep/Glob/WebFetch fixtures (below) drive `claude.actionFor`'s new arms. **On screen**: `ToolCard.modelFor` at `TranscriptPanel.kt:434`, glyph as its own cell via `activityRow(glyph=)` (`ui/kit/ActivityRow.kt:80`), expand/collapse by row tap. Two design residuals, argued at `TranscriptPanel.kt:292-300,191-196`: **open is the default**, because the recorded Claude crossing pins a tool run's captured output as a drawn block and a collapsed default would silently stop drawing it; and the **turn separator is spent as the head-of-turn timestamp, not a rule** — this kit has no divider component |
| M2.3 incremental transcript | **GREEN (model + view)** | `ui/screens/TranscriptIncremental.kt` + `TranscriptIncrementalTest.kt` (reconcile by item_id, stick-to-bottom, anchor). `sessionDetailRedraw` now **mutates in place**: `SessionDetailView.kt:600` (`reconcileBlocks`), `:633` (`stickToBottom`), reading "was the reader at the bottom" off the enclosing `ScrollView` BEFORE mutating. B12's model gaps closed: `Append`/`Insert` carries `index` + `tail` and `Remove` exists (`TranscriptIncremental.kt:65-74`), so a front insertion — which is exactly what "load earlier" produces — is expressible. Residual: empty↔non-empty transitions still recompose the block, because row 8's empty state is not a row container |
| M2.4 composer_send | **GREEN (wire + daemon + facade + view)** | `schema.ComposerSendReq` + `ComposerSendContentHash`, `handleComposerSend` (authz-first, body-hash bound, refused-not-truncated), gateway body gate, daemon `composerSend` (expected_turn precondition incl. THE RACE, PTY injection with submit framing, injection-time `source: phone` + `operation_id` correlation). **The send control now calls it**: `PhoneSurface.kt:490` `app.composerSend(target, turn, line)` on the COMMAND plane; draft survives every refusal; `stale_turn` gets `ComposerModel`'s gentle copy through a new `Press.refused` hook. **B1's blocker is closed**: `Gateway.ForwardCommand` never copied `rc.ComposerSend`, so a real phone send arrived at the daemon bodyless — `internal/remotegw/gateway.go:376-379` now carries it, and the fence at `r6fix_forwardassembly_test.go:73` `TestR6Fix_EveryRemoteCommandBodyReachesTheDaemonControl` is REFLECTIVE (it walks `protocol.RemoteCommand` for every pointer-to-struct body and fails naming any that did not arrive), so the next body cannot be forgotten the way this one and R5's `PolicyEnv` were. **B3's blocker is closed**: `requireStructuredComposer` (`skeleton/chat.go:185`) refuses `structured_unsupported` on a degraded session rather than typing into a session whose transcript could never show the message. **ROUND 2 closed two blockers that made every word above unreachable from a finger.** (a) The press discarded the `Op`, so `settle` fired on LOCAL SEALING: `SendState.SENT` and `typed.text.clear()` ran before the machine had seen the message, under a comment claiming the opposite, and the daemon's `stale_turn` could not route at all (nothing mapped the wire code onto `ErrorState.STALE_TURN`, so `SendState.STALE_TURN` and its gentle copy were dead code with an exhaustive suite over them). The verdict is now a value — `SessionDetailScreen.composerVerdictFor` + `ErrorRouter.routeMachineCode` — claimed per draw by operation id (`renderComposerVerdict`), and the draft is spent ONLY on acceptance. (b) The phone sent the CLOSED turn's id (`items.lastOrNull()?.turnId`), so a reply to a finished turn was refused `stale_turn` **100% of the time**; `TranscriptScreen.openTurnOf` now mirrors IS-ENV-1's own rule and answers the EMPTY string for an idle session, which is what protocol.md says the daemon matches |
| M2.4 semantic interrupt | **GREEN (wire + daemon + facade + view)** | `adapter.TurnInterrupter` (claude proves ESC on the S-B capture's own "esc to interrupt" hint; opencode proves none), `InterruptTurn` types the declared bytes or refuses `interrupt_unsupported` typing NOTHING, capability record derives `Interrupt` from the SAME seam (ADR-017 T2 r3). **B7's blocker is closed**: the op was BODYLESS, so a Stop rendered against turn A typed the cancel key into turn B — in the §8.1 script, the turn the OWNER just started. It now carries `TurnInterruptReq{session, expected_turn}` bound through the tuple's EXISTING content slot (`TurnInterruptContentHash`) — no new crypto — with `expected_turn` REQUIRED non-empty, which also closes the idle-ESC case by construction. On screen: `PhoneSurface.kt:372` `app.interrupt(target, turn)`, the turn the panel was drawn against. **ROUND 2**: the press kept no operation, so `interrupt_unsupported` and `stale_turn` reached nobody — a refused Stop and a Stop that worked drew the identical screen. `renderInterruptVerdict` claims it by id and `SessionDetailScreen.interruptNoticeFor` says what is true of the TURN, with the machine's words in the mono detail cell (`killNoticeFor`'s own split). And the turn it names is now the OPEN one — see the composer row |
| M2.4 approvals | GREEN (landed in M1, unchanged) | this slice adds the M2.3 pin that a resolution is an in-place mutation (`TranscriptIncrementalTest.anApprovalResolutionIsAnInPlaceMutationToo`) |
| M2.5 composer states | **GREEN (model + view)** — ABSENT arm inferred | `ComposerModel.availabilityFor` (online-only; ABSENT for `structured_chat=false`), `stateLabel`, `placeholderFor` ("Message"/"Add feedback..."). On screen at `SessionDetailPanel.kt:770-785`: placeholder status-driven off `TranscriptBlock.running`, send state and refusal as separate notice slots, ABSENT removes the composer and says why. **Disclosed at the site (`SessionDetailPanel.kt:763-769`): `structuredChat` is read off the TRANSCRIPT (`transcript.structureTorn`, i.e. the presence of a `structured_gap` element), not off a published capability** — there is no capability read on this facade |
| M3.1 history paging | **GREEN (wire + daemon + gateway + facade + view)** | `interaction_history` unsigned read; strictly-older/ascending/limit/honest-floor conformance; ADR-014 authored and **amended ten times** (A1-A5 by the fix-pack and its evidence pass, A6-A9 by review round 2, A10 by review round 3). **B6's blocker is closed**: ADR-014 stated as accepted decision that the reads "are gateway-routed" and there was no arm — `command_loop.go:955` and `protocol.ActionInteractionHistory` now exist, with the body gate. **B5's blocker is closed**: the page was trimmed by RAW RECORD while the phone pages by ITEM, so a page could deliver a message's tail with its head missing and make the head permanently unreachable — `historyPageStart` (`skeleton/chat.go`) pages on item boundaries. **ROUND 3 (F1) fenced it where it is SERVED**: all three of B5's tests call the helper directly on a hand-built slice, so reverting the CALL SITE to the pre-fix record trim left the suite GREEN — the rule could be deleted from the served path unnoticed. `TestR6R3_TheServedHistoryPageNeverBeginsInTheMiddleOfAnItem` (`internal/skeleton/r6r3_chat_test.go`) drives `InteractionHistory` over a real journal whose middle item is an `agent_message` grown across three records under one item_id, and asserts the served page begins at that item's FIRST record; the same mutation now FAILS (ADR-014 amendment A10). On screen: "Load earlier" above the conversation (`PhoneSurface.kt:532`), dropped once `App.HistoryFloor` is true (`FacadeBridge.kt:208`). **ROUND 2 closed three blockers, all below the wire.** (a) The press discarded the `Op` and `adoptInteractionRead` runs only inside `App.Outcome`, so no page ever folded and the control never disappeared — `rememberHistoryRead` + `renderReadVerdicts` claim it, and the cold-open backfill claims its own read too (silently, which is its documented decision). (b) At the retention bound the page was evicted by the trim it was paged in behind (probed: 200 held, 50-record page, 200 held, none surviving) — `ItemStore.ApplyPage` holds a page in a second bounded region the live trim neither counts nor evicts, refuses a page it cannot hold WHOLE, and `App.HistoryAtCapacity` is how the screen says so (ADR-014 A8). (c) `before_item` could be the phone-minted `structured_gap:<ts>` id, which the daemon can never match (`invalid_field`, permanently) — the anchor now skips tears (ADR-014 A7). The end-to-end fence is `mobile/conformance/r6r2_chatoutcome_test.go`, the first test anywhere that drives a claimed reply from the relay into `App.ReadTranscript`. **ROUND 3 (F4)**: a refused page now says `SessionDetailScreen.historyReadNoticeFor`'s own sentence with the machine's words beside it, instead of the generic routed remedy — see the M3.3 row for the whole of that finding |
| M3.2 open policy | **GREEN (model + call site) / PARKED (the read it needs does not exist)** | `SessionDetailOpen.plan` (cold-open backfill, throttled on a MONOTONIC clock) is called on every detail draw at `PhoneSurface.kt:2379` via `dispatch.enqueue` (not `press` — nobody tapped). **And there it stops, on purpose**: `plan` answers true for a session this phone holds NO items for, and that is exactly the session the facade cannot page — `App.LoadEarlierInteractions` requires a non-empty `before_item` (`mobile/interactionread.go:56-58`) and there is no anchorless "newest page" read. Written at the call site, `PhoneSurface.kt:2383-2401` |
| M3.3 detail-on-demand | **GREEN (wire + daemon + gateway + facade + view)** | capture-time retention in `fitItem`'s own untruncated pass (no second marshal), 64 MiB oldest-first store, `detail: true` only when true, `interaction_detail` answers the WHOLE body or `unavailable` with zero records. Gateway arm at `command_loop.go:968`. On screen: a tappable notice under any clipped card the machine still retains (`TranscriptView.kt:275` under the tag at `:107`) → `App.LoadInteractionDetail` (`PhoneSurface.kt:2444`), handed to `VerbDispatch.press` so it disables while crossing. **ROUND 2**: the tap could not have worked. The press discarded the operation (so nothing claimed the reply), and the reply CANNOT travel the fold — it carries no cursor and the tapped card is `completed`+`truncated`, so `ItemStore.Apply` dropped it; forcing a cursor above the high water reaches the `agent_message` branch instead, which CONCATENATES and yields "HEAD...HEAD AND THE WHOLE REST OF IT", a garble presented as the whole of it (the ambiguity IS-CAP-3 forbids). `ItemStore.ApplyDetail` replaces the clipped body where the card stands; `rememberDetailRead`/`renderReadVerdicts` claim the answer, including the `unavailable` refusal (ADR-014 A9). **ROUND 3 (F4) corrects what that refusal SAID, and this row's own previous wording**: it did not reach "nobody" — it reached somebody, unnamed. Both M3 reads passed their wire code to `ErrorRouter.routeMachineCode` and handed the result to the ONE-ARG `PressFeedback.ofRefusal`, which sets no detail — so the daemon's own sentence was dropped, and `unavailable`/`invalid_field`, absent from `MachineRefusalCodes.toToken` on purpose (they are one-verb facts), fell to `ErrorState.UNKNOWN`: "Something failed in a way the app does not recognise. Try again, and report it if it keeps happening." A user tapping a clipped card the daemon had EVICTED was told to retry what retrying can never fix, and `offersDetail` — derived from fields journalled at CAPTURE time — left the offer tappable forever. Now: `SessionDetailScreen.detailReadNoticeFor`/`detailReadDetailFor` say the verb's own sentence with the machine's words in the detail cell (the shape the composer, Stop, kill and approval already used), `unavailable`/`invalid_field` are NAMED in `MachineRefusalCodes` (and still deliberately out of `toToken`), and a TERMINAL refusal withdraws the offer (`detailReadIsTerminal` → `TranscriptScreen.of(withoutDetail=)`) so nobody is invited to retry an eviction. **The CLOSING review then caught this fix's own fence being unscoped**: `TestR6R3_TheTwoM3ReadsSayWhatTheMachineSaid` grepped the whole app module, where each symbol matched its own declaration, so all four call sites could be gutted with every gate in both lanes green. The checks are now scoped to `renderReadVerdicts` and `detailPanel` and the same mutation FAILS seven ways — a test-only change; the production code was correct throughout |
| M3 deep-links | **GREEN (in-app landing) / PARKED (notification tap, and it cannot be built here)** | `ItemStore.Resolve` by id (survives cursor-moving reconciliation, honest miss), `DeepLinkAnchor.resolveById` wired at `PhoneSurface.kt:2578` with a NAMED miss ("no longer in the history this phone is holding") rather than a silent return. The notification tap is parked and **not buildable in this wave**: `PhoneActivity` reads nothing off its Intent (PB-SEC-11, enforced byte-for-byte by `PhoneActivityWindowTest`) and `WakeNotifications` carries no destination by design |
| ADR-017 structured tear | **GREEN (daemon + wire + phone + view)** | **B4's blocker is closed on both paths.** The daemon's history scan kept only `interaction` records, so every `journal.TypeStructuredGap` was dropped and each page spanned a proven capability tear CONTIGUOUSLY (`skeleton/chat.go:397-408` now keeps it; `api.go:617` now carries its payload, which was being dropped so the phone got a typed record with an empty body); and `ItemStore.applyLocked` dropped the same gap on the live path (`phonecore/interaction.go:71-81,329-412` (`applyStructuredGapLocked`) — `KindStructuredGap`, identity keyed on the emission instant so a reconciliation re-delivery folds to one row). On screen the gap is a FIRST-CLASS row: `notice(ERROR)` with its own tag, its own three-fact sentence and the daemon's reason verbatim (`TranscriptPanel.kt:512`, `TranscriptView.kt:191` (`notice(..., NoticeKind.ERROR)` tagged `transcript.gap`)) |

## The recorded fixtures (M2.2's real-recording obligation)

`claude-{grep,glob,webfetch}-pretooluse.json` are REAL captures: the actual `swarm-char` binary
(unmodified), real PTY, real hook sink, against real `claude` **2.1.214** — the same version as
the corpus' S-B three — in `-p` print mode with an inline `--settings` denying Bash (so the model
exercises the three tools) and `--setting-sources ""`. Version honesty: the machine-installed
claude 2.1.235 (and 2.1.233/234, and 2.1.214 under this account's default server config) no
longer offers Grep/Glob to the model AT ALL — verified live: `ToolSearch` reports no such
deferred tool and every probe fell back to Bash — so the recording pinned `ENABLE_TOOL_SEARCH=false`
on the 2.1.214 binary (npm `@anthropic-ai/claude-code[-darwin-arm64]@2.1.214`), where both tools
are served. Sources of truth live in `docs/verification/fixtures/r6-chat/` (with the recording
relay + settings beside them); the corpus copies are sha256-held to them by `provenance_test.go`,
rows added to PROVENANCE.md (`copied`). Recorded keys: Grep/Glob `tool_input.pattern`, WebFetch
`tool_input.url` — the arms read exactly these.

## Supersessions executed

**At RED (pre-recorded, quoted at every rewrite site):**

1. `internal/protocol/r1_refusalops_test.go` — composer_send row `CodeNotImplemented ->
   CodeInvalidField` (the real handler's structural refusal of the same bodyless frame);
   turn_interrupt row keeps the VALUE `CodeNotImplemented` but now asserts the real handler's
   seam-absent refusal. Both quoted in the row comments; every choke-point ordering assertion
   inherited unchanged.
2. `mobile/commands.go (*App).Interrupt` — the input-plane ride retired with its dissolved premise
   quoted in the replacement doc; `interruptByte` deleted. Conformance rewrites (each quoting what
   it replaced): `s16_appverbs_test.go`'s three PB-APP-3 tests now pin the signed op (no 0x03, no
   lease, offline refusal LEGIBLE with **no** undelivered-input residue and **no** replay on
   reconnect); `conformance_test.go`'s walk presses Stop bare and awaits the turn_interrupt
   command; PhoneSurface's Stop press moved `SendPlane.LIVE -> COMMAND`.
3. `internal/skeleton/capability.go` — the "interrupt is left at its zero value" note rewritten
   with the original quoted; `deriveSessionCapabilities` now derives `Interrupt` from
   `adapter.AsTurnInterrupter` (r6_interruptapply_test.go:114 is the pin).

**By the fix-pack** (chat-red.txt:493-532, each carrying its reason at its own site): the
turn_interrupt refusal-ops row's `wantCode` (finding B7 dissolved its "the op has no body"
premise; the seam-absent answer it used to measure is STILL measured, by
`r6_turninterrupt_test.go`); `TestR6ComposerRoute_TurnInterruptStaysBodylessAndForwards ->
...TurnInterruptCarriesItsTurnAndForwards` (the old name asserted the DEFECT; its load-bearing
assertion is unchanged); `r6_historydetail_test.go` connections now negotiate `CapJournal` (no
assertion about a reply's CONTENT changed); the B7 seam/frame signatures; and
`App.Interrupt(session) -> App.Interrupt(session, expectedTurn)`, which forced the Kotlin call
site at `PhoneSurface.kt:372`.

Two shared TEST RIG capability gaps were fixed, no assertion touched: `approval_inject_test.go`'s
`gridScript` now ends with four bare `ask` steps (the fake CLI went deaf after its first report,
so the composer race test's accepted second send could never be observed landing), and
`android/gate/s25_mainthread_test.go`'s `s25PressOpeners` gained `enqueue(` — a third genuinely
dispatched shape the fence did not know, so a correctly dispatched read was being reported as a
main-thread defect.

## Goldens and ledgers

- `mobile/testdata/exported_surface.golden` — the R6 verbs (`App.ComposerSend`, `App.Interrupt`
  on its new signature, `App.LoadEarlierInteractions`, `App.LoadInteractionDetail`,
  `App.HistoryFloor`, `ErrClassStaleTurn`, `TranscriptItem.Source`, `TranscriptItem.ToolKind`),
  traced in `mobile/screen_coverage.tsv`. **Round 2 adds exactly one method**,
  `App.HistoryAtCapacity(string) (bool, error)` — the phone's own end of the conversation, kept
  apart from `App.HistoryFloor`'s machine sentence (ADR-014 A8) — re-pinned with
  `-update-surface` and traced in the same `transcript.history` row, which round 2 also amended
  because three of its claims were false as written (they are quoted and corrected in the row).
- `internal/skeleton/testdata/i1-transcript-screen.golden.json` — the REAL stack delivers
  `tool_kind` in tool_run bodies and the five bound fields; `TranscriptScreenGoldenTest.itemFrom`
  reads all five, so the two-way field join (`i1_screengolden_test.go`) holds.
- `android/app/libs/swarm.aar` REBUILT from the current `mobile/` via `android/build-aar.sh` —
  mtimes recorded in Gates below. **Round 3 rebuilt it again** (`mobile/relay.go` gained the
  reconnect-delay observation seam) and **added NO exported symbol**: the seam is unexported, so
  `mobile/testdata/exported_surface.golden` is byte-unchanged by this round, no `-update-surface`
  run was needed, `android/unbound-verbs.tsv` and `mobile/screen_coverage.tsv` are untouched by
  it, and B94's ledger neither gained nor lost a row.
- `android/unbound-verbs.tsv`: **−`App.ComposerSend`** (it now has a caller),
  **+`App.SendInput`** — the first row there for a verb REPLACED rather than a screen not yet
  built, with the consequence disclosed in the row: PB-INPUT-1's undelivered-input ledger can no
  longer be filled from this surface, so its notice and Clear control are drawn over a ledger
  nothing writes to.
- GG-7: +1 field row, `turn_interrupt -> *TurnInterruptReq` (`protocol.md:104`), Meaning cell
  written with "wire name".

## The §8.1 exit demonstration — step by step

The playbook's Claude exit demonstration is a PHYSICAL run on a handset and remains the wave's
exit; this table says, for each of its nine steps, what automated stand-in exists — or names the
step as owner-physical-only, which is a gap and not a pass.

| §8.1 step | Automated stand-in | Verdict |
|---|---|---|
| 1. start through Swarm | R5's launch suites (r5-launch.md), unchanged | stand-in |
| 2. type from terminal | `r6_composerapply_test.go` owner-typed attribution (stays `owner`); **bounded** by `r6fix_chat_test.go`'s TTL pins — see the retraction below | stand-in, **bounded** |
| 3. continue from phone while tools run | the composer chain, all five layers **plus round 2's sixth, the ANSWER coming back**: `r6_composersend_test.go` (wire) → `r6fix_forwardassembly_test.go` (the REAL gateway assembly, B1) → `r6_composerroute_test.go` (gateway route) → `r6_composerapply_test.go` + `r6fix_chat_test.go` (daemon: idle, THE RACE, closed turn, degraded session) → `r6_composerverbs_test.go`/`r6_chatverbs_test.go` (phone) → `android/gate/r6_chat_ui_test.go` `TestR6_TheChatKitIsReachedFromProductionKotlin` (a real screen calls it) → `TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface` (the screen CLAIMS the machine's answer, which nothing did before round 2) + `SessionDetailVerdictTest` (what the claim means) + `mobile/conformance/r6r2_chatoutcome_test.go` (a claimed reply reaching `App.ReadTranscript` over the real relay) | stand-in, **with a disclosed hole**: §8.1 step 3 also requires "an empty characterized input region" and `expected_input_revision`, and neither exists — see CANNOT YET (i) |
| 4. approve from either side | M1's suite (mirror-m1.md, approval_inject/approve_roundtrip), unchanged; in-place resolution pinned by `TranscriptIncrementalTest` | stand-in |
| 5. background the app | S17 push suites (`mobile/conformance/s17_pushwake_test.go`, `internal/phonecore/s17_wakereplay_test.go`), unchanged | stand-in |
| 6. receive a real wake | delivery is covered by the S17 suites; **wake TIMING on a real handset is owner-physical-only** | **gap, disclosed** |
| 7. reopen into the exact card | `r6_deeplink_test.go` (resolve by id across a cursor-moving reconciliation) + `SessionDetailOpenTest` (land by id or `NotRetained`) + the wired landing at `PhoneSurface.kt:2578`. **Only the IN-APP tap**: the notification tap cannot land on an item at all (PB-SEC-11) | stand-in for the in-app half; **the notification half is a gap** |
| 8. switch networks | none — **owner-physical-only**. No automated stand-in exists or is claimed | **gap, disclosed** |
| 9. finish from terminal without session replacement | owner-typed attribution above + the spool/co-presence evidence on file (`internal/skeleton/copresence_test.go`, the r6 spool suites) | stand-in |

The playbook's §8.1 *build* list (its numbered 1-6) stands as: **1** delivered (M2 rows above);
**2** delivered by the spool foundation slice (its own evidence file); **3** delivered EXCEPT the
empty-input-region check and `expected_input_revision` — see CANNOT YET (i); **4** delivered
(M3.1/M3.3), with M3.4's JSONL enrichment parked; **5** unchanged from M1 (grid-gated injection,
version-gated, refusal never a guess); **6** untouched, correctly (Channels stays out).

## CAN / CANNOT YET / PARKED

### CAN — a user does this on a real screen, through the real gateway and daemon

1. **Send a message from the session-detail composer** into a live Claude session — including
   the ORDINARY case, a reply typed after the agent has finished — gated on the turn the
   composer was drawn against, and see it attributed `phone` in the transcript.
   (`PhoneSurface.kt:490` → `App.ComposerSend` → `forwardControl` → `handleComposerSend` →
   `composerSend` → PTY.) **This row was FALSE in the previous round in two independent ways**
   (see "Claims retracted" 7 and 8): the phone named the turn the daemon had just closed, so an
   idle session refused every send; and the screen reported the send as delivered on local
   sealing whatever the machine went on to answer.
2. **Be refused `stale_turn` gently, with the draft retained**, when the conversation moved
   between render and tap — the refusal never claims the text was sent. **Also FALSE in the
   previous round, in both halves** (retraction 8): nothing routed the daemon's wire code onto
   `ErrorState.STALE_TURN`, and the draft was cleared before any refusal could arrive.
3. **Stop a running turn as a signed op bound to the turn the Stop was drawn against**, with
   visible success or refusal, including the honest `interrupt_unsupported` on a provider with no
   recorded cancel key.
4. **Read agent prose as markdown** — bold, inline code, fenced code in the mono well, and
   **tool activity as cards** with a kind glyph, a head-of-turn timestamp, and expand/collapse.
   **Links are TEXT and are not tappable, and this row said otherwise for three rounds**
   (retraction 13). What the renderer promises is that a link's visible text cannot LIE about its
   href — the scheme allowlist and the label-honesty rule protect the URL a reader reads and
   might type into a browser by hand, which is what a phishing URL needs and the whole of what is
   defended here. It is NOT protection against a click hijack, because there is no click:
   `Span.linkHref` is read only by the type-role pass, and no production Kotlin builds a
   `URLSpan`, a `ClickableSpan`, a movement method or `autoLink`. Round 3 also closed four
   one-character evasions of the honesty rule (M2.1 row) — which that same fact makes a
   read-and-retype spoof rather than a click hijack, and the severity is written down here rather
   than left to be inferred.
5. **Watch the transcript mutate in place** as items arrive and statuses flip, with scroll
   position preserved and stick-to-bottom only on a tail insert.
6. **See a proven capability tear as its own row**, with the daemon's reason verbatim, and find
   the composer REMOVED with a named reason rather than accepting a message the transcript could
   never show.
7. **Page a session's history** with "Load earlier", on item boundaries, with an honest floor that
   removes the control rather than offering it forever — and an equally honest SECOND stop when
   this phone can hold no more of the conversation (`App.HistoryAtCapacity`), which is a
   different sentence and never the same control silently disappearing. **FALSE in the previous
   round** (retraction 9): no page ever folded, because nothing claimed the read's answer.
8. **Tap a clipped card for its whole body**, or get the named `unavailable` refusal carrying no
   partial body — **in the machine's own words, under the screen's own sentence, with the offer
   withdrawn** so an evicted body is not offered for tapping again. **FALSE in the previous round**
   (retraction 10): the answer was never claimed, and the reply could not have folded if it had
   been. **AND HALF FALSE AGAIN AFTER THAT** (retraction 15): the refusal was claimed but rendered
   as `ErrorState.UNKNOWN`'s "Something failed in a way the app does not recognise. Try again",
   with the daemon's sentence dropped and the offer left standing.
9. **Land on the exact item in-app** after a daemon restart moved every cursor, or read a named
   miss.

### CANNOT YET — stated as the user experiences it

i. **CLOSED 2026-08-26 — see the note at the end of this file.** The entry is kept verbatim below
   because it is the record of what shipped in R6, and because what closed it did not do what
   this entry says was required: it asks a strictly weaker question the shim can answer without
   characterizing anything. The audit that closed it also found the worse half nobody had stated
   here — two phone sends racing each other produce one submitted concatenation and one empty
   submit, with no owner draft involved at all (`agents-tracker-bzfe`).

   **A phone send can be merged with the owner's half-typed terminal draft, and the CR submits
   the concatenation** — a message nobody wrote. `injectComposerText` (`skeleton/chat.go:227`)
   writes text + CR through the tap with NO check that the input region is empty and NO input
   transaction. This is §8.1 step 3's own requirement and ADR-017:175's amendment obligation
   (`expected_input_revision`), and it is **not discharged in code**: enforcement needs a
   shim-wide input transaction because only the shim owns the PTY writer, and `internal/shim` is
   out of this wave's scope. The half that could in principle have been discharged here — gate
   the send on the input region being empty — was **measured and is also unreachable**: no
   adapter seam characterizes the input region (`ApprovalApplier`, `TurnInterrupter`,
   `InteractionSource`, `HostProber` — none), and deriving it from the raw grid is the
   never-guess move IS-TOOL-2 forbids. ADR-017:177-197 now carries the obligation's accounting
   in three parts: **(b)** the one-active-turn rule is DISCHARGED (and B7 extended it to
   `turn_interrupt`, which had no turn coordinate at all), **(c)** the delivery vocabulary is
   discharged on the wire and partly on the phone, and **(a)** `expected_input_revision` is NOT
   discharged and ships open, with its blocker named. The same disclosure is at the code site
   (`skeleton/chat.go:203-226`) and in protocol.md's own paragraph. **Review round 3 (F9) re-read
   all three and left every one of them untouched, deliberately and by ruling**: the obligation is
   discharged IN WRITING with its blocker named, `internal/shim` is still out of this wave's
   scope, and the honest act is to leave the gap disclosed and parked rather than to build half a
   transaction that looks atomic.
ii. **Open a session this phone has never held an item for and see its history.** The cold-open
   backfill fires, and there is nothing it can ask: paging is strictly BEFORE a named item id
   (IS-ENV-2) and no anchorless "newest page" read exists. The user gets the transcript's empty
   state — which says the conversation has not reached this phone, not that the agent said
   nothing — plus PB-SYNC-1's Repair control. One facade verb from closed.
iii. **A paged read is live-only, not persisted.** Records a page delivers fold into the live
   `ItemStore` and are not written to the durable snapshot, so after a process death the screen
   asks again. Deliberate (ADR-014's "Deferred, disclosed"): persisting them would let one "load
   earlier" walk the phone past `MaxItemsPerSession` and evict the LIVE tail. **This sentence
   was half true and pointed the wrong way when it was written** (retraction 11): nothing wrote
   the records to the transcript SNAPSHOT, and `Core.RecordOutcome` persisted the WHOLE reply —
   records included — into `OpOutcomes`, which this codebase's own comment records as never
   pruned. So every page wrote up to `limit` full item bodies, and every detail read the FULL
   pre-truncation body, into the phone's durable state file permanently, with none of the
   benefit. A durable outcome is now a verdict and carries no records
   (`phonecore/core.go`, `TestR6R2_ADurableOutcomeIsAVerdictAndNotAPayload`), and the sentence
   above is true in both halves as of this round.
iv. **Phone attribution is bounded, not infallible.** The `UserPromptSubmit` hook carries no
   injection id, so the daemon correlates BY TEXT; probed, an owner-typed `yes` was stamped
   `source: phone` with the phone's `operation_id` while a phone send of `yes` was pending. The
   correlation now expires after `skeleton.pendingSendTTL` (10 s). What is promised: a `phone`
   attribution is backed by an injection watched inside that window. What is not: that two
   identical prompts inside one window are told apart.
v. **The composer's ABSENT arm is inferred from the transcript, not from a published
   capability.** `registerSessionCapabilities` / `deriveSessionCapabilities` have **no production
   caller** (`skeleton/capability.go:228-237`), so no live session has a capability record and
   ADR-017 T2 rule 3's "the phone renders from the capability record" has no record to render
   from. The screen therefore keys ABSENT on a `structured_gap` ELEMENT
   (`SessionDetailPanel.kt:770-773`), and the daemon's gate keys on the durable degrade MARKER
   (`chat.go:186`) rather than on record-absence — refusing on absence would refuse every send,
   which is feature-off dressed as fail-closed. **Consequence for the user**: a session whose gap
   the 200-item retention bound has evicted still shows a composer, and the send is refused
   `structured_unsupported` by the machine — late, but never silent.
vi. **Markdown italic renders as undifferentiated prose.** Its markers are consumed and its words
   are shown; this design system declares no italic face and the kit gate forbids choosing one.
vii. **A notification tap cannot land on an item.** `PhoneActivity` reads nothing off its Intent
   (PB-SEC-11) and `WakeNotifications` carries no destination. Only the in-app tap lands, and it
   now names its miss.
viii. **PB-INPUT-1's undelivered-input notice has no producer.** `App.SendInput` lost its last
   caller to the signed composer op; the notice and its Clear control on session detail are drawn
   over a ledger nothing now writes to. Ledgered with the full argument in
   `android/unbound-verbs.tsv:58`.
ix. **Tool cards open by default** and there is **no drawn turn separator** — the turn boundary is
   spent as the head-of-turn timestamp. Both are argued design choices, not oversights, at
   `TranscriptPanel.kt:292-300,191-196`.
x. **The §8.1 physical run has not happened.** Wake TIMING and network switching (steps 6 and 8)
   have no automated stand-in and none is claimed; the wave's exit is the owner's handset run.
xi. **This phone holds a bounded amount of history, and says so rather than pretending.** A
   "load earlier" page is held in a second bounded region beside the live window
   (`phonecore.MaxBackfillPerSession`); a page that does not fit is refused WHOLE — half a page
   is a hole in a conversation with nothing marking it — and the control is then dropped with
   the phone's own sentence rather than the machine's floor. What a user therefore CANNOT do:
   read a very long conversation all the way back on the handset. What they are never shown is
   the beginning of a conversation that goes further back, silently.
xii. **A history page's NEW edge is not an item boundary** (ADR-014 A6). The page begins on an
   item boundary and ENDS at the boundary cursor, so an item whose first record precedes
   `before_item` while its later increments follow it is delivered head-only. **THE LIVE CASE,
   named here because stating the asymmetry without it reads as theoretical** (round 3, F7): a
   phone `composer_send` echo opens a `user_message` INSIDE a growing `agent_message`, so the
   older item's increments continue past the newer item's first record — the ordinary shape of a
   reply typed while the agent is still writing. The consumer's
   own rule keeps it honest rather than corrupt — `applyLocked` drops the trailing increments
   instead of concatenating them in the wrong place — so what a reader sees is a message that
   stops early, not one with a hole in the middle. Recorded rather than fixed: closing it
   changes what "at most `limit`" bounds, and minting that inside a review round is how three
   of the fix-pack's own findings got in.

xiii. **No markdown link is tappable, and that is a decision rather than an omission.** A link
   renders in the inline identifier register and goes nowhere; opening a URL from adversarially
   sourced agent output is a new attack surface (a chosen destination, a chosen moment, on a
   phone that is already authenticated to things), and this wave does not open it. What a user
   CAN do is read a URL they can trust to be the one the link points at, and type it themselves.
   Every claim to the contrary — the renderer's KDoc, a test name, this file's CAN row 4 and the
   RED record's B11 narrative — was corrected in round 3 rather than made true.

xiv. **A clipped card whose whole body the machine has evicted is asked once, told plainly, and
   then stops offering.** The offer cannot be right in advance: `truncated` and `detail` are
   journalled when the item is CAPTURED, and the daemon's detail store is bounded and
   oldest-first, so between capture and tap the body may be gone. The phone learns it from the
   refusal and remembers it for that item, in memory only — the machine's retention is its own to
   change, and asking again on a later visit is a cheap question with a true answer.

### PARKED — deliberately not built this wave

- **M2.6**: `MailboxWait` long-poll replacing the 500 ms sleep, and the measured sub-300 ms felt
  echo on the handset.
- **M2.7**: the caret affordance while `streaming` (the fold-by-ref growth exists; the caret does
  not).
- **M3.4 / M3.5**: Claude transcript-JSONL enrichment (thinking, full tool results, `plan_update`)
  and `stop_reason`.
- **IS-CAP-4's byte-aware reseed bound** (`internal/daemon/interaction.go:36-39`): pre-existing,
  named by M3.1, not built; `Gateway.Resync` still seals every record above the phone's cursor.
  One paging channel less load-bearing than it was, still open.
- **The daemon session-item index**: the journal scan is the implementation until measurement
  says otherwise (ADR-014 §2); the ratified op shape is index-agnostic.
- **The capability-publication slice**: wiring `deriveSessionCapabilities` into session launch, so
  ADR-017 T2 rule 3 has a record to render from and the composer gate can tighten its absent-record
  arm to a refusal. Invisible to the B94 reachability ledger because both symbols are unexported.
- ~~**`expected_input_revision` + the shim-wide input transaction** (CANNOT YET (i)):
  `internal/shim` is out of scope for this wave.~~ **CLOSED 2026-08-26** by conversation-surface
  Slice 0 (`agents-tracker-bzfe`), and by a weaker mechanism than this entry assumed: the shim
  refuses a message when anything has been written to the PTY since the last submit, and writes
  text and carriage return under one hold of its own writer lock. `expected_input_revision` was
  not added and is not needed — the predicate crosses, the revision does not. See the amendment
  block under ADR-017 §T-obligation and `internal/skeleton/s0_writerserialise_test.go`.
- **An anchorless "newest page" read** (CANNOT YET (ii)).
- **The notification deep link** (CANNOT YET (vii)) — parked and not buildable under PB-SEC-11 as
  it stands.

## Claims retracted by this rewrite, and the evidence that forced each

1. **"M3.1/M3.3 GREEN (wire + daemon)", listed under CAN.** Retracted at review time: there was
   no gateway arm, no `ActionInteractionHistory` constant, and a phone-issued read fell to
   `opForAction`'s default and was refused `unsupported command action`. The fix-pack BUILT the
   route rather than deferring it (ADR-014 amendment A2), so the row is GREEN again — but on
   different evidence than round 1 asserted, and it spent the interval being false.
2. **"M2.1 GREEN (model)" and "M2.2 GREEN (model + wire + adapter)" with no residual named.**
   Retracted: nothing in production Kotlin referenced `Markdown`, `ToolCard`,
   `TranscriptIncremental`, `ComposerModel`, `SessionDetailOpen` or `DeepLinkAnchor` — the single
   grep hit was a comment. The Android lane built the views, and
   `TestR6_TheChatKitIsReachedFromProductionKotlin` is now the permanent regression fence against
   the same claim being made again on the same absent evidence.
3. **"an owner-typed prompt keeps `owner` and does not consume the correlation", unqualified.**
   Retracted: probed both directions; an owner-typed `yes` was stamped `phone`. Replaced by the
   bounded claim in CANNOT YET (iv) — here and at protocol.md:695-707.
4. **"Gates … clean" as a promise ("re-run before close").** Retracted: two gates were RED at the
   time it was written (`internal/remotegw/r1_refusalops_test.go:114,216` and
   `internal/phonecore/issuedat_test.go:154`), and a Gates section that promises rather than
   records cannot be checked. Both are fixed; the section below is a record of runs, and it
   carries one gate that is NOT clean.
5. **protocol.md: "A session whose `structured_chat` capability is ABSENT … is refused
   `structured_unsupported`."** Weakened by THIS pass, at protocol.md:710-724, because the
   handler does not keep it: a session with no capability record at all is NOT refused
   (`skeleton/chat.go:185-193` gates on the durable degrade marker and on a record that exists
   and says false). Refusing on record-absence would today refuse EVERY send, since nothing
   publishes records — feature-off dressed as fail-closed. The paragraph now says what the code
   does and names the residual.
6. **ADR-014: "the Kotlin affordance … is now the ONLY deferred half."** Overtaken within the
   same round — the affordance landed. Wiring it exposed a gap the ADR had never written down,
   so amendment **A5** was added by this pass: paging is always relative to a named `item_id`,
   therefore **a phone holding nothing for a session has no id to name and no page it can ask
   for**, and M3.2's stated exit ("cold-open shows history without tapping Repair") is not met.
   That is CANNOT YET (ii), and it is now in the ADR as well as here.

7. **CAN row 1, "send a message from the composer", unqualified.** Retracted: the phone sent the
   CLOSED turn's id. `TranscriptPanel.of` took `items.lastOrNull()?.turnId`, and the daemon's
   `turnIDLocked` reads the open turn, stamps it onto the terminal `agent_message` and THEN
   deletes it — so once a turn ends the last item carries turn A while `d.turnIDs[session]` is
   `""`, and `composerSend` refuses `stale_turn` unless they match. protocol.md:690 states the
   other half ("an idle session is matched by the EMPTY expected_turn") and nothing on the phone
   ever produced the empty value. The ordinary path — the agent finishes, you read the answer,
   you reply from the phone — was refused every time, and re-reading the transcript (the
   refusal's own stated remedy) could not change it. Fixed by mirroring IS-ENV-1 in
   `TranscriptScreen.openTurnOf`; RED at `TranscriptTurnAndAnchorTest` (three failures captured
   against the pre-fix model, chat-red.txt round-2 capture 7).
8. **CAN row 2, "be refused stale_turn gently, with the draft retained".** Retracted in BOTH
   halves. `VerbDispatch.press` settles on the FACADE CALL and `App.ComposerSend` returns the
   instant the envelope is appended, so `settle` set `SendState.SENT` and ran
   `typed.text.clear()` on local sealing — a refused send was shown as sent with the user's
   words erased, under a comment reading "THE FIELD IS EMPTIED ONLY ON THE MACHINE'S
   ACCEPTANCE". And `Press.refused` could only fire for facade-LOCAL errors, so the daemon's
   `stale_turn` never routed: nothing mapped the wire code onto `ErrorState.STALE_TURN`, making
   `SendState.STALE_TURN` and `ComposerModel.noticeFor("STALE_TURN")` dead code —
   `SessionDetailComposerTest` constructed that state BY HAND, which is finding B8's own defect
   one layer in. Fixed by `composerVerdictFor` + `routeMachineCode` + `renderComposerVerdict`.
9. **CAN row 7, "page a session's history".** Retracted: no page ever folded.
   `adoptInteractionRead` is called only from `App.Outcome`, the press discarded the `Op`, and
   `bridge.launchOutcome` had eleven call sites and none for these verbs — so "Load earlier"
   reached the machine, the machine answered, and the answer sat in the reply cache while the
   control went on being offered. Underneath that, at the retention bound the page was evicted
   by the trim it was paged in behind (probed: 200 held, 50-record page applied, 200 held, none
   of the 50 surviving). Fixed by claiming the op and by `ItemStore.ApplyPage`'s second bounded
   region; the honest stop is `App.HistoryAtCapacity` (CANNOT YET xi).
10. **CAN row 8, "tap a clipped card for its whole body", and M3.3's "GREEN (wire + daemon +
   gateway + facade + view)".** Retracted: the press discarded the operation AND the reply could
   not fold if it had been claimed — no cursor, terminal item, so `ItemStore.Apply` dropped it;
   forcing a cursor reaches the concatenating branch and yields a garble presented as the whole
   of it. Fixed by `ItemStore.ApplyDetail` and by claiming the answer.
11. **CANNOT YET (iii), "records a page delivers are NOT written to the durable snapshot".**
   Weakened: true of the snapshot, false of the phone. `Core.RecordOutcome` persisted the whole
   reply into `OpOutcomes`, which nothing prunes — so the durable pressure was worse than what
   the disclosure said it was avoiding, with none of the benefit. See (iii) as it now reads.
12. **The Gates section recorded one whole-repo run as "exit 0, 60 packages ok, zero FAIL"
   without noting that the suite is load-sensitive.** Weakened: the reviewer's first whole-repo
   run of that session FAILED on `cmd/swarm` `TestRunShim_LaunchesAgentPersistsAndLeadsSession`
   (role_test.go:112, "shim pid never became its own session leader"), isolated re-runs went
   3/3 green, and a second whole-repo run was exit 0. There are now TWO known load-sensitive
   gates, and both are recorded below rather than one.

### Round 3's four

13. **"links whose label cannot lie about their target", read as a defence against a TAP** — CAN
   row 4, the M2.1 row's "tappable", `Markdown.kt`'s `linkAt` KDoc ("the reader still gets a
   tappable link"), the RED record's B11 narrative, and a test NAMED "…is still not tappable".
   Retracted: **no markdown link on this surface is tappable and none ever was.** `Span.linkHref`
   is consumed by exactly one thing, `styleRun`'s type-role pass; a grep of
   `android/app/src/main/kotlin` for `URLSpan|ClickableSpan|setMovementMethod|LinkMovementMethod|
   linksClickable|autoLink` returns nothing. Every claim is corrected in place (the test keeps its
   assertions and is renamed to what it actually asserts; the RED capture is preserved verbatim
   with a dated correction beside it), and the consequence is written down rather than implied:
   the guard protects a URL a reader READS AND COULD RETYPE, so finding F2's evasions are a
   read-and-retype spoof and NOT a click hijack — a lower severity, stated, not left to be
   inferred. Links stay text-only by ruling this round.
14. **"B5's blocker is closed", as a fact about the SERVED page.** Weakened and then re-earned:
   the fix was correct and no test drove the path that uses it. Reverting the call site in
   `interactionHistory` to the pre-fix record trim left `go test -run TestR6 ./internal/skeleton/`
   at exit 0. The rule is now fenced where it is served, and the same mutation fails; ADR-014
   carries it as amendment A10. **The general lesson is recorded rather than the instance**: a
   test that drives a helper the production path merely happens to call is not a fence on the
   production path, and this file's own round-2 report had noted "my first RED capture was
   vacuous — it reverted the call site, not the function" without closing the gap it had just
   exposed.
   **CORRECTED IN PLACE 2026-08-20T03:43Z (final-fence pass; the paragraph above is kept as
   written).** "The general lesson is recorded rather than the instance" was the right
   intention and it did not hold: the lesson was recorded and then the SWEEP THAT RECORDED IT
   missed two more instances of its own defect class, both in `android/gate/r6_chat_ui_test.go`
   and one of them **one function above** the fence it had just corrected. Three were missed in
   total, not one — F4's own fence (found by the closing review, fixed 03:09Z),
   `TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface`'s four semantic symbols, and
   `TestR6_TheChatKitIsReachedFromProductionKotlin`'s `markdownBody`. The second is the
   expensive one: gutting `renderComposerVerdict` and `renderInterruptVerdict` restored this
   wave's round-2 headline blocker — a REFUSED send shown as "Sent" with the user's typed words
   erased — with `go test ./android/gate/` at exit 0. **The real lesson, and it is mechanical
   rather than moral**: a lesson written as prose is not a fence, and the sweep it prescribes
   has to be executed symbol by symbol with the mutation run each time. The rule that survives
   is checkable — a symbol check must be unable to match its own declaration — and it is now
   written at every reach-check site in the five gate files this wave touched, with the full
   45-check sweep table in `r6-red/chat-red.txt`.
15. **CAN row 8 and the M3.3 row: the `unavailable` refusal "also reached nobody before".**
   Retracted: it reached somebody, unnamed. Both M3 reads routed their wire code through
   `ErrorRouter.routeMachineCode` into the detail-less `ofRefusal` overload, so the daemon's
   sentence was discarded and two codes deliberately absent from `MachineRefusalCodes.toToken`
   answered `ErrorState.UNKNOWN`'s "Something failed in a way the app does not recognise. Try
   again, and report it if it keeps happening" — retry advice for an eviction, under an offer
   that stayed tappable. Fixed with a verb-specific sentence, the machine's words in the detail
   cell, and the offer withdrawn on a terminal refusal.
16. **The Gates section recorded TWO load-sensitive rows.** Weakened: there were at least three,
   and two of the three are now FIXED BY INJECTION rather than recorded. `cmd/swarm`'s
   `TestRunShim_LaunchesAgentPersistsAndLeadsSession` and `mobile/conformance`'s PB-NET-4 run-loop
   fence were both reproduced under deliberate starvation, fixed in the RIG (never in an
   assertion), and re-run green under the identical injection. The third,
   `internal/skeleton`'s `TestApprove_AStaleOrMismatchedApproveIsRefusedWithACodeAndAppliesNothing`,
   would not reproduce in ten more starved runs and is recorded open, unfixed, below.

Also corrected, in the documents rather than here: ADR-014 §1's "daemon-side gating lives behind
the seams" (amendment A1 — both handlers honored NEITHER the negotiated `journal` capability nor
the remote kill switch, while the `journal_read` precedent they cite honors both), ADR-014 §2's
record-trimmed page (A3), the rule ADR-014 should have written about carrying the tear in a page
(A4), and protocol.md's sale of `turn_interrupt`'s bodylessness as a virtue (B7). Each is amended
IN PLACE with the original sentence quoted, never edited away. **Round 2 added four more
amendments to ADR-014** — A6 (the item-boundary rule is asymmetric and the ADR implied it was
not), A7 (a `structured_gap` id is not a `before_item`), A8 (a page must survive the reader's
phone) and A9 (the detail reply is not a delta and could not fold) — and struck the header
sentence "This ADR ratifies those shapes verbatim; nothing is amended", which sat directly above
a status line announcing amendments. **Round 3 added A10** (A3's item-boundary rule is right, is
served, and was held by no test on the path that serves it), re-read section 1 against the code
and left it, and left A6 standing with the live case that produces it named in CANNOT YET (xii)
as well as in the ADR. Round 3 checked the two self-contradictions the reviewer named as still
open — the struck "nothing is amended" header and "three claims did not survive" over five
amendments — and found both already corrected in place by round 2 (the header sentence is struck
with its reason, and the preamble states three failed claims plus two missing rules explicitly);
the preamble's arithmetic is extended here to cover A10 rather than rewritten.

## Gates — a record, not a promise

Round 1 wrote "Recorded at the bottom of this file's producing session; re-run before close",
and two gates were RED while it said so. This section is therefore a RECORD: every row below is
a run this document's own session executed, with its start/end in UTC, its exit code, and its
counts. `PATH=$HOME/go/bin`, `golangci-lint has version 2.12.2 built with go1.26.5`.

**Round 3's runs are the rows below.** Round 2's rows (2026-08-20T00:39-00:57Z) are superseded
and not repeated: this pass changed `internal/skeleton`, `cmd/swarm`, `mobile`,
`mobile/conformance`, `android/gate` and four production Kotlin files, so a gate recorded before
those edits proves nothing about the tree that ships. **Two of the three known load-sensitive
gates are FIXED this round, by injection, in the RIG and never in an assertion; the third is
recorded open.** See "The gates that are NOT clean" below — and
[r6-red/chat-red.txt](r6-red/chat-red.txt)'s round-3 section for every mutation and injection run
behind these numbers.

### Go (2026-08-20)

| Gate | Window (UTC) | Exit | Result |
|---|---|---|---|
| `go build ./...` | 01:54:09 → 01:54:11 | 0 | — |
| `go vet ./...` | 01:54:11 → 01:54:12 | 0 | — |
| `golangci-lint run` (v2.12.2) | 01:54:12 → 01:54:17 | 0 | one issue, FIXED, then **0 issues** at 01:54:40 (`QF1001` on the new fence's growth assertion — De Morgan; the assertion is unchanged in meaning) |
| `go test -race -count=1` on internal/protocol{,/schema}, internal/remotegw, internal/phonecore, internal/skeleton, mobile, mobile/conformance, internal/verify, internal/adapter/..., android/gate, **cmd/swarm** | 02:22:37 → 02:27:17 | 0 | **19/19 packages `ok`** (skeleton 278.0s, mobile/conformance 203.8s, android/gate 58.7s, cmd/swarm 44.9s, mobile 40.6s, phonecore 38.2s, remotegw 34.3s, verify 32.1s, protocol 22.5s, the nine adapters 1.8-10.1s). **cmd/swarm is on this list for the first time**: round 3 changed its rig, so it is gated here rather than only inside the whole-repo run |
| whole-repo `go test ./...` | 02:27:26 → 02:32:03 | 0 | **60 packages `ok`, zero `FAIL`** (5 with no test files). One run, and it is not a claim that the suite is deterministic — see the open row below |
| GG-7 (`TestProtocolMD_*`, incl. the bidi field-set check) | inside the protocol package run above | 0 | `ok` — **no field row and no protocol.md change this round**; round 3 touched no wire shape |
| `TestB94_EveryExportedSymbolIsReachableFromProduction` | inside the verify package run above | 0 | `ok` — **no exported symbol added or removed**: round 3's only production Go addition is `mobile`'s unexported `reconnectDelayObserver`, and `mobile/testdata/exported_surface.golden` is unchanged (no `-update-surface` run was needed or made) |
| re-run AFTER this pass's last document edits (`./internal/verify/ ./android/gate/ ./internal/protocol/`) | 02:33:24 → 02:34:25 | 0 | `ok`/`ok`/`ok` (-race) — the rows above were recorded before this Gates section and the ADR/RED prose were finished, so every document-reading gate is re-recorded after them |
| and once more, after the `file:line` citations in this file were re-pointed at the code as it now stands | 02:35:51 → 02:36:52 | 0 | `ok`/`ok`/`ok` (-race). Round 3 moved four production Kotlin files, so several of this file's citations had drifted; the ones into `Markdown.kt`, `PhoneSurface.kt`, `TranscriptPanel.kt` and `SessionDetailPanel.kt` were re-read and corrected. The only edit after this run is this cell |
| **CLOSING REVIEW, test-only**: build / vet / `golangci-lint` | 03:08:07 → 03:08:13 | 0 | `0 issues`. The closing review found F4's own fence unscoped — its six symbol checks matched their own DECLARATIONS in `SessionDetailPanel.kt`/`TranscriptPanel.kt`, so gutting all four call sites in `PhoneSurface.kt` left both lanes green (reproduced here, gate `ok` at 03:06Z). **Fence gap, not a live defect**: the shipped code was correct and NO production line changed. **CORRECTED IN PLACE 2026-08-20T03:43Z (final-fence pass; the sentence above is the capture as written): F4's fence was ONE OF THREE, not the only one.** The sweep that fixed it missed the sibling one function up in the same file — `TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface`, whose four semantic symbols matched `SessionDetailPanel.kt:586,:213,:489` and `FacadeBridge.kt:229` — and `TestR6_TheChatKitIsReachedFromProductionKotlin`'s `markdownBody`, which matched `ui/kit/Markdown.kt:402`. All three are the SAME defect class, all three in `r6_chat_ui_test.go`, all three now scoped and mutation-proved red |
| **CLOSING REVIEW** `go test -race -count=1 ./android/gate/ ./internal/verify/ ./internal/protocol/` | 03:08:17 → 03:09:25 | 0 | `ok`/`ok`/`ok` (65.0s / 23.6s / 17.1s) — the owned packages this edit touches, plus B94 and the GG-7 bidi check. The six positive checks now read `r6ReadVerdictsBody` (renderReadVerdicts alone) and the last of them is `detailRefused.add(`; a seventh reads the new `r6DetailPanelBody` for `withoutDetail = detailRefused` on the transcript draw. **The identical gutting mutation now FAILS with seven messages, one per deleted line** (chat-red.txt's F1 sweep correction). No Kotlin changed, so the 02:16:40Z Android run above still stands; `PhoneSurface.kt` is byte-identical before and after the mutation (md5 `2ab5e05…f6e596`) |
| **FINAL FENCE, test-only + prose**: `go build ./...` / `go vet ./...` / `golangci-lint run` (v2.12.2) | 03:50:41 → 03:50:47 | 0 / 0 / 0 | `0 issues`. The pass that swept the closing review's OWN sweep: two more instances of its defect class, both in `android/gate/r6_chat_ui_test.go` — `TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface`'s four semantic symbols and `TestR6_TheChatKitIsReachedFromProductionKotlin`'s `markdownBody`. **Test-only and prose-only; NO production line changed in either lane** |
| **FINAL FENCE** `go test -race -count=1 ./android/gate/ ./internal/verify/` | 03:50:47 → 03:51:45 | 0 | `ok`/`ok` (58.8s / 22.0s) — the one package this pass edits, plus B94. **Both new scopings are mutation-proved red**: gutting `renderComposerVerdict` + `renderInterruptVerdict` (which restores round 2's headline blocker — a REFUSED send shown as "Sent" with the draft erased) took the whole gate package from **exit 0 to exit 1 with three messages, one per deleted line** (03:48:58 → 03:49:07Z; the pre-fix form re-probed on the same mutated tree at 03:50:01 → 03:50:04Z passes on all four symbols), and deleting `markdownBody(`'s only call site in `TranscriptView.kt` took `TestR6_TheChatKitIsReachedFromProductionKotlin` from pass to FAIL (03:49:17 → 03:49:18Z) where the old module-wide form passed on `Markdown.kt:402`'s declaration alone (re-probed 03:50:30 → 03:50:33Z). **No Kotlin changed** — `PhoneSurface.kt` sha1 `545f22f770a98b0d4649fac6151293c2386829b8` and `TranscriptView.kt` sha1 `aff22d63d19f5ff71265da6cc092e3128ed88449` are byte-identical before and after both mutations — so the 02:16:40Z Android run above still stands |

**The authoritative Go run: the orchestrator's pre-commit audit.** The rows above record how the
wave was built, gate by gate, on trees that no longer exist. The run this commit stands on is a
single clean sweep over the FINAL tree, taken after the last document edit in this file, with
nothing running beside it:

| Gate | Window (UTC) | Exit | Result |
|---|---|---|---|
| `go build ./...` | 04:09:27 → 04:09:29 | **0** | — |
| `go vet ./...` | 04:09:29 → 04:09:31 | **0** | — |
| `golangci-lint run` (v2.12.2, the version pinned in ci.yml) | 04:09:31 → 04:09:32 | **0** | **`0 issues.`** |
| `go test -race -count=1` over internal/protocol/..., internal/skeleton, internal/remotegw, internal/phonecore, internal/adapter/..., mobile/..., internal/verify, android/gate | 04:09:32 → 04:14:12 | **0** | every package `ok` — GG-7's bidi check inside protocol, `TestB94` inside verify, the newly-scoped fences inside android/gate |
| whole-repo `go test -count=1 ./...` (what CI runs) | 04:14:12 → 04:19:44 | **0** | zero `FAIL` |

A leaked `daemon.test` process from an R5 fault test had been running **3 days 7 hours** and was
consuming a core throughout the rounds above; the orchestrator killed it before this run, so
these timings are the first taken on an unloaded machine. That also means the two load-sensitive
rows below were characterised under MORE contention than this run had, not less — the direction
that makes their fixes harder to trust too little, not too much.

An earlier `-race` run of the owned set (02:08:26 → 02:13:06) is superseded by the row above and
is recorded because it FAILED and is the reason a Kotlin file changed after it:
`android/gate`'s `TestPBDS7_EveryNumberInTheKitIsAccountedFor` refused F2's first implementation
— sixteen `'\uXXXX'` char literals, every one containing digits the kit's numeric accounting
cannot see. The list was replaced by the property it was enumerating (Unicode category Cf, via
`Character.getType`), which covers more and spells no literal. Seventeen packages were `ok` in
that run; the gate was the eighteenth and it was right.

### Android (2026-08-20)

Run from a SCRIPT FILE (`/tmp/r6r3/android.sh`) and never a bare command line, because
`pgrep -f gradle-wrapper.jar` self-matches from one and hangs forever.
`JAVA_HOME=/usr/local/opt/openjdk@21/...`, `ANDROID_HOME=/usr/local/share/android-commandlinetools`,
`./gradlew --no-daemon --rerun-tasks --no-build-cache`. **No test-results directory was deleted at
any point.**

| Fact | Value |
|---|---|
| AAR before this pass | `app/libs/swarm.aar` 2026-08-20T**00:39:33Z**, 12 524 115 B (round 2's) |
| AAR REBUILT from the current `mobile/` (`android/build-aar.sh`) | 2026-08-20T01:54:46Z → **01:55:03Z**, exit 0, 12 525 352 B — **required**: round 3 added the reconnect-delay observation seam to `mobile/relay.go` |
| final run (`:app:testDebugUnitTest :app:lint :app:assembleDebug`) recorded start | **2026-08-20T02:16:40Z** |
| `BUILD SUCCESSFUL` | 4m 13s, **59 actionable tasks, 59 executed** |
| `GRADLE_EXIT` | **0** |
| recorded end | **2026-08-20T02:20:55Z** |
| AAR after the run | 2026-08-20T**01:55:03Z**, 12 525 352 B — **unchanged across the run**, so no dependency was swapped under it |
| ~~AAR as a reader will find it~~ **CORRECTED IN PLACE 2026-08-20T03:43Z** (final-fence pass; the two rows above are the capture as taken and are kept verbatim) | `app/libs/swarm.aar` is **2026-08-20T02:42:20Z**, 12 525 352 B. The two rows above say 01:55:03Z, so **a reader reproducing them with `stat` today gets a different answer and the rows are unreproducible as written**. What is true: the run recorded above started 02:16:40Z and ended 02:20:55Z, so it consumed the 01:55:03Z AAR, and the 02:42:20Z touch is **after that run finished** — a later rebuild from unchanged `mobile/` sources (the size is identical to the byte). It does NOT invalidate the run; it does mean the AAR on disk is not the artifact the recorded run read, and the honest statement is the mtime, not the inference |
| ~~`mobile/` newer than the AAR~~ **DISCLOSED 2026-08-20T03:43Z**, same pass | `mobile/relay.go` now carries mtime **2026-08-20T03:30:58Z**, LATER than the 02:42:20Z AAR, so **mtime alone no longer proves the AAR is built from the current `mobile/`**. Its content is the round-3 reconnect-delay observation seam this file already records (`reconnectDelayObserver atomic.Pointer[...]` plus the `run` loop's publish) and nothing else, so the touch is a restore rather than a change — but that is read from the diff, not from the timestamp, and the timestamp is what a reader checks. Stated rather than left for a reader to trip over |
| result XMLs | `testDebugUnitTest/`: **173 files, mtimes 2026-08-20T02:20:52Z and 02:20:53Z** — after the recorded start, so **the run is this session's** |
| totals | **tests=1382, failures=0, errors=0, skipped=0** (up from 1373: +9 from round 3 — four link-evasion cases, their anti-vacuity control, three read-refusal cases and the withdrawn-offer case) |
| `:app:lint` | inside the same run, **BUILD SUCCESSFUL, nothing reported** |
| `:app:assembleDebug` | inside the same run, `app-debug.apk` written 2026-08-20T02:18Z, 37 524 212 B |
| ~~newest production Kotlin~~ **CORRECTED IN PLACE 2026-08-20T04:15Z** (orchestrator pre-commit audit; the capture as taken is kept below, struck rather than deleted) | The row as recorded read: "`Markdown.kt` 2026-08-20T**02:16:06Z** (the restore after the F2 mutation re-run), then `TranscriptPanel.kt`/`SessionDetailPanel.kt` 02:03:26Z, `PhoneSurface.kt` 01:52:50Z, `ErrorRouting.kt` 01:50:41Z — every one BEFORE the 02:16:40Z start. Newest `mobile/` source: `relay.go` 01:41:59Z, before the 01:54:46Z AAR rebuild. Nothing changed under either." **It is not reproducible today.** `Markdown.kt`, `TranscriptPanel.kt`, `SessionDetailPanel.kt`, `internal/skeleton/chat.go` and `mobile/relay.go` all carry mtime **2026-08-20T03:30:58Z** — exactly five files sharing one second, i.e. one bulk restore after the round-3 mutation proofs, 70 minutes AFTER the 02:20:55Z run ended. So the run above consumed sources a reader can no longer identify by timestamp, and the inference "every one BEFORE the start" no longer holds |

### The authoritative Android run: the orchestrator's pre-commit audit

The rows above are kept as the record of how the wave was built, but **the run this commit
stands on is the orchestrator's own**, taken because the corrected row above left the earlier
run's inputs unidentifiable by timestamp. Rather than reason about whether a restore was
content-identical, the lane was simply re-run from a rebuilt artifact, so every input provably
predates it. Script: `r6_audit_android.sh` (a script FILE — `pgrep -f gradle-wrapper.jar`
self-matches when typed on a command line and the guard then never clears).

| what | value |
|---|---|
| lane guard | `LANE_BUSY=0` — no other gradle build active |
| recorded start | **2026-08-20T04:02:03Z** |
| AAR before | 2026-08-20T02:42:20Z, 12 525 352 B |
| AAR **rebuilt from the current `mobile/`** | `./android/build-aar.sh` → **2026-08-20T04:05:31Z**, 12 525 352 B, exit 0 — so the artifact under test is newer than every source, and the identical size across a rebuild from the restored tree is itself evidence the restore changed no bytes |
| gradle | `./gradlew --no-daemon --rerun-tasks --no-build-cache :app:testDebugUnitTest` → **BUILD SUCCESSFUL in 3m 33s, 32 actionable tasks, 32 executed**, `GRADLE_EXIT=0` |
| recorded end | **2026-08-20T04:09:05Z** |
| result XMLs | **173 files**, and `XML_OLDER_THAN_START=0` — not one result predates the recorded start, so every XML counted was written by THIS run |
| totals | **tests=1382, failures=0, errors=0, skipped=0** |

The count matching the earlier run's 1382 exactly is the evidence that the 03:30:58Z restore
was content-identical — but it is stated as a consequence of the re-run, not offered in place
of one.

The two measurement traps the previous rounds recorded still apply and were applied again: a
naive sweep of `app/build/test-results/**` also counts `testReleaseUnitTest/` XMLs from an older
task and reports a larger number, so only the 173 XMLs `:app:testDebugUnitTest` wrote are
counted; and `stat -f '%Sm' -t '...Z'` prints LOCAL time with a `Z` suffix that is a lie, so
every timestamp here was recomputed with `date -u -r <file>`.

**Two further gradle runs this round were RED ON PURPOSE** and are the mutation evidence for
F2 and F4, recorded in full in chat-red.txt: 01:59:41Z → 02:03:14Z (four mutations at once,
1382 tests, 7 failed, each attributable to the test it fails by name, every control green) and
02:14:16Z → 02:16:06Z (F2 re-proven over the final Cf-category implementation, 13 tests, 4
failed). Both restored the tree afterwards, verified by content check, and the clean run above
is on the restored tree.

### The gates that are NOT clean, and are recorded open

**There were THREE known load-sensitive tests, not two — and the previous round recorded two.**
Round 3 reproduced two of them by injection, fixed the RIG (never an assertion) and re-ran the
identical injection green. The third would not reproduce and is recorded open, unfixed.

**INJECTION, the same for all three**: `GOMAXPROCS=1` with `6 x hw.ncpu` busy loops on this host.
A milder one (`GOMAXPROCS=2`, `2 x hw.ncpu`) reproduced nothing — the flakes need real starvation,
which is why "it passed on my machine" was never evidence about them.

**(1) FIXED — `cmd/swarm` `TestRunShim_LaunchesAgentPersistsAndLeadsSession`** (role_test.go:112,
"shim pid never became its own session leader (getsid != pid) - setsid was not guaranteed").
Reproduced **6 of 6** under injection (01:32:33Z → 01:34:35Z). The rig polled `getsid` for a fixed
3 s and reported a setsid failure if the answer had not arrived — a bounded wait treated as a sync
point, measuring PROCESS STARTUP LATENCY. It now waits on the REAL CONDITION: `runShim` calls
`ensureSession()` before `shim.Run`, and `shim.Run` binds its control socket as its first act, so
the socket existing proves setsid already happened — an ordering that lives in the production code
instead of in a timeout. **6 of 6 green under the identical injection** (01:35:27Z → 01:37:45Z).

**(2) FIXED — `mobile/conformance` `TestPBNET4_TheRealRunLoopGrowsReAuthenticatesAndResetsItsBackoff`**
(pbnet4_flappingrelay_test.go:251). Reproduced **1 of 4** under injection (01:38:01Z → 01:38:37Z):
`gap #1 ... was 1.200393917s, want within [800ms, 1.2s]` — 393 microseconds over a ceiling that IS
the largest legal schedule. The rig timed dial ARRIVALS at an HTTP tap, which is the scheduled
delay plus transit the rig does not control, and held both edges of a ±20% band against it.
**The band moved to where the quantity exists**: `mobile/relay.go` gained an observation seam on
the delay `App.run` schedules (unexported, nil in production, the `internal/shim`
`testHookAfterSignalArm` pattern), and `mobile/pbnet4_rundelay_test.go` drives the REAL run loop
(`NewApp` + `Start` against a 503 endpoint) holding each SCHEDULED delay to section 6.0's band
with **no tolerance at all** — plus a one-sided elapsed check, because observing `rb.next()`'s
answer alone would not catch `time.After(250 * time.Millisecond)` beside a correct computation
(proven: that mutation fails it). The conformance fence keeps the half a tap can honestly prove —
the FLOOR, which is load-immune because an arrival gap is the scheduled delay plus non-negative
transit, and a ceiling with a NAMED 1 s transit allowance. **6 of 6 green under the identical
injection** (01:42:39Z → 01:43:57Z), and the new in-process fence 6 of 6 green under it too
(01:44:04Z → 01:44:24Z). Net: the property is fenced more tightly than before, in the one place it
can be measured exactly.

**(3) OPEN — `internal/skeleton`
`TestApprove_AStaleOrMismatchedApproveIsRefusedWithACodeAndAppliesNothing`.** Failed 1 of 3 clean
`-race` package runs at review time and has not been reproduced since. Fourteen passes were on
record; round 3 added **ten more under the injection above** (01:44:35Z → 01:45:43Z, `-race`,
`-count=10`, exit 0) plus this session's full package runs. **Twenty-four-plus passes bound the
rate; they do not disprove it, and this file does not claim the gate is clean.** Nothing was
changed to "fix" it, deliberately and by the house rule: a flake is proven by injection before it
is called fixed, and a rig edit whose effect nobody can measure produces a green run
indistinguishable from these plus a claim with no evidence under it. What was read, so the next
agent starts further along, is unchanged and stands at chat-red.txt:553-597 (the `launchFake`
60 s idle nothing bounds the test against; the trailing 500 ms sleep before the "no
`approval_resolved`" sweep, which under load can only make the test pass more easily).

The skeleton package run is ~276-285 s. No test in this slice added a wall-clock sleep to it:
B9's TTL uses the established `Config.ItemClock` seam, round 2's addition reads a constant, and
round 3's addition (`r6r3_chat_test.go`) drives the append floor on the same pinned clock and
runs in ~3 s.
