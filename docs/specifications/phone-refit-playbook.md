# Phone refit playbook: the contract for the 2026-08-27 session

Owner direction 2026-08-27, from eight screenshots of the shipped 0.8.0 phone app: the chat is
unusable (nothing sends, the header floats, the list bleeds through the composer), the copy is
paragraphs of jargon, the warm palette reads as brown, the layout is cramped, and the full-width red
Stop should not exist. Decisions taken the same day, after the interactive mock
(https://claude.ai/code/artifact/90c122aa-eff2-4e47-a4f1-6d9e8757ba99):

| Decision | Ruling |
|---|---|
| Frame | Pinned header and composer; the list is the only thing that scrolls, clipped to its box |
| Colours | **Slate**, for the whole phone app and the launcher icon |
| Spacing | Breathing: list gap 14dp, side 16dp, card 16x12dp, type up one rung |
| Stop | The send control becomes a square while the agent works. No full-width button, no confirm |
| Tool activity | One line per tool, in plain words, tap to open |
| Words | Rewritten: one short sentence, from the user's side, naming the computer |

Tracker: epic `agents-tracker-d45a`, seven children `d45a.1`-`d45a.7`.

This document is the **contract**: every wave below names its bead, the files it may touch, the tests
written before the code, the gates that move, and what "done" means. A fleet that finds the
contract wrong stops and says so; it does not widen the wave.

## 0. Where this sits

`docs/specifications/chat-surface-plan.md` (epic `agents-tracker-tbpm`, 2026-08-26) is the plan of
record for the conversation screen and stays so. This playbook is its next set of waves, and it
amends three of its rulings on the owner's word:

- **The drawing's string table is superseded** by the copy tables in W5. The owner's direction
  today is that the recorded copy is too long and too technical.
- **ADR-009 (Obsidian) D1, D3 and the colour half of D4 are superseded** by ADR-020 (W4). The
  material grammar, motion register, status semantics, type substitutions and contrast floors survive.
- **R2 (send becomes stop)** is kept exactly as the committee amended it on tbpm.5: the button is
  Stop only while the session works **and** the field is empty; a non-empty field means Send; the
  verb is read from the live field at press time; Stop stays reachable elsewhere.

Beads this playbook absorbs or depends on:

| Bead | Relation |
|---|---|
| `tbpm.10` (mid-turn sends, the quiescent-since-submit guard) | The guard shipped; W2.1 corrects the one input it miscounts |
| `tbpm.8` (four reasons the phone cannot type) | Its copy half lands in W2.2 and W5; its router arm is out of scope here and stays open |
| `tbpm.4` (the screen learns whether the agent is working) | W3.2 implements the committee's ruling: one source, the open turn |
| `ryuk` (R4: inline decision buttons half wired) | P0, open, **not in this playbook**; W6 does not depend on it |
| `s0ve` (menu one inset too low) | Re-measured on the handset after W1.1; likely closed by it |
| `m5p7`, `g2mr`, `t8u6` | Out of scope; listed in §10 |

## 1. Fleet protocol

Every wave runs the same way. None of this is optional.

1. **One bead per wave**, child of the epic created for this playbook, claimed before any file is
   touched. From a worktree, pass `--db /Users/Nathan/Code/swarm/.beads/embeddeddolt` to every
   `bd` call; a zero-issue `bd` is a broken query, never evidence of absence.
2. **One worktree per wave** under `.claude/worktrees/refit-w<n>`, branched from `main` at the
   moment the wave starts. The main checkout is shared with another live session, which may switch
   its branch at any time; it is never edited by a fleet, and the orchestrator merges from its own
   detached worktree (`.claude/worktrees/refit-main`, pushed as `HEAD:main`).
3. **TDD, evidenced.** The RED run is captured (test names and the failure text) into
   `docs/verification/phone-refit-w<n>.md` (one file per wave) before GREEN begins. A test is never
   edited to pass; if a test seems wrong the fleet stops and reports. Tests listed as "must stay
   untouched" are the negative controls of the wave.
4. **Stage by exact path.** `git add <file>` only. Never `-A`, `.`, a directory, or `commit -a`.
5. **Gate check and commit are separate tool calls.** Read the gate result, decide, then commit.
6. **Gates.** Go: `go build ./... && go vet ./... && golangci-lint run` (`export PATH="$HOME/go/bin:$PATH"`;
   version must match `.github/workflows/ci.yml`) and, because this session runs inside a
   swarm-supervised shell whose `SWARM_*` variables break the hook and daemon tests:
   `env -u SWARM_HOOK_CAPTURE -u SWARM_SESSION_ID -u SWARM_HOOK_SEQ_FILE -u SWARM_HOOK_TOKEN -u SWARM_DAEMON_SOCK -u SWARM_SHIM_HOOK_SOCK go test -race -count=1 -timeout 40m ./...`.
   A load-timing failure in an unrelated package is rerun once in isolation before it is red. Android:
   `. android/toolchain.env && cd android && ./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache`.
7. **The Gradle lane is serialised across all worktrees.** Before running Gradle, confirm
   `pgrep -f gradle-wrapper.jar` is empty from a script file (not a typed command, which self-matches);
   afterwards confirm every `app/build/test-results/testDebugUnitTest/*.xml` is newer than the run's
   recorded start and `app/libs/swarm.aar` did not move during the run. Never delete test-results.
   If the wave changes an exported facade field, rebuild the AAR (`android/build-aar.sh`) and rerun.
8. **Review before merge.** One adversarial review round per wave (three for W2, which touches the
   PTY byte path). The reviewer verifies each finding against the code before it is acted on.
9. **Merge order is fixed** (§9). Each wave rebases onto `main` before merge, reruns its gates, and
   is merged by the orchestrator, never by the fleet.
10. **Close.** Evidence section written, bead closed, `git push` green, worktree removed.

Global definition of done for the session: every wave's "Done when" is true; all four Go gates and
the Kotlin suite are green on `main`; `docs/verification/phone-refit-w<n>.md` exists for every wave with
RED evidence and one negative control per behavioural change; the owner has seen the eight screens
on the handset; versionCode 19 / 0.9.0 is on the Play internal track.

## 2. W1 Pin the frame

Bead: `agents-tracker-d45a.1`. Worktree `refit-w1`. Files: `PhoneActivity.kt`, `ui/screens/PhoneScaffoldView.kt`, their tests. Nothing under `docs/research/`, nothing in
`tokens.json`.

### W1.1 The top inset is the platform's, not a floor
- **Symptom.** `PhoneActivity.kt:229` `screenTopOrRealInset = maxOf(measured, 54dp)`; on this handset
  54dp is taller than the real status bar, so the header sits in dead space.
- **Current** (`PhoneActivity.kt:160-169`):
```kotlin
val screenTopPx = resources.getDimensionPixelSize(R.dimen.swarm_screen_top)
surface.root.setOnApplyWindowInsetsListener { view, insets ->
    val bars = insets.getInsets(WindowInsets.Type.systemBars())
    val ime = insets.getInsets(WindowInsets.Type.ime())
    val top = screenTopOrRealInset(bars.top, screenTopPx)
    view.setPadding(bars.left, top, bars.right, bottomInsetPx(bars.bottom, ime.bottom))
    insets
}
```
- **Target.** `view.setPadding(bars.left, bars.top, bars.right, bottomInsetPx(bars.bottom, ime.bottom))`;
  `screenTopOrRealInset` and its KDoc (`:210-230`) deleted. `R.dimen.swarm_screen_top` is **kept**:
  `s22b_spacing_test.go:99` and `DesignScaleResolutionTest.kt:61` read it from the design source.
- **Tests first.** `PhoneActivityInsetTest.kt`: delete `:25`, `:35`, `:45` (they pin the floor) and
  add `the top padding is the platform's own inset and nothing else`. Untouched:
  `ConversationScaffoldViewTest.kt:127` (IME max-not-sum), `DesignScaleResolutionTest.kt:58`.
- **Gates.** `tabbar_test.go:577,582` still match (`bars.bottom` padding regex). `s22b` unchanged.
- **Done when** `PhoneActivity.kt` names `swarm_screen_top` nowhere; `dimens.xml:52` unchanged;
  handset screenshot shows the header directly under the status bar.

### W1.2 Both scroll viewports clip
- **Symptom.** `PhoneScaffoldView.kt:191-192` and `:368-369` set `clipChildren = false` on the two
  `ScrollView`s, so scrolled rows paint over the header and under the footer.
- **Current** (`:363-371`):
```kotlin
isFillViewport = true
// A glowing dot is inflated past its own bounds and the tab badge overhangs its icon, so
// every container between them and the window has to be told not to clip. ...
clipChildren = false
clipToPadding = false
```
- **Target** (both sites): `clipChildren = true; clipToPadding = true`, comment replaced: the rows
  open their own bounds (`SessionRow.kt:74,128,172`, `TabBar.kt:112,155,193`), the bloom and the dot
  draw inside their own layer (`CtaButton.kt:318`, `StatusDot.kt:95`), and the badge lives in the
  bar, a sibling of the scroll. The root `LinearLayout`s (`:206`, `:384-385`) keep `clipChildren = false`.
- **Tests first.** `PhoneScaffoldViewTest.kt`: `the destination viewport clips what scrolls out of it`
  (CONTENT child `clipChildren == true`), `the badge still escapes because the bar holds it` (root
  `clipChildren == false`). `ConversationScaffoldViewTest.kt`: `the transcript viewport clips, so
  nothing draws over the pinned composer`. Untouched: `PhoneScaffoldViewTest.kt:150` (badge), `:254`
  (grain travels with content) — both must stay green as proof the overhang survives.
- **Gates.** None read these flags; confirm `h1_conversationscroll_test.go` does not pattern-match
  `clipChildren = false` before landing.
- **Done when** both `ScrollView`s clip, both roots do not, and the badge/grain tests are unchanged.

### W1.3 / W1.4 The bars' opacity is a token value (re-homed in W4.2)
- **Finding (fleet W1, 2026-08-27).** `android/gate/o3_material_test.go:148`
  (`TestPBDS5_EveryColourResourceIsSpentBySomethingThatDraws`) refuses any `<color>` nothing draws;
  `swarm_tabbar_background`'s only consumers are `Composer.kt:292` and `TabBar.kt:115`. The bars
  cannot stop spending it, and the Obsidian maquette cannot be edited.
- **Ruling.** With W1.2 nothing scrolls under either bar (both are siblings of the `ScrollView`), so
  the visible bleed is closed by W1.1 + W1.2 alone. The opacity is a token value and lands in W4.2:
  the Slate maquette carries `--p-tabbg: rgba(11,14,20,1)`, `swarm_tabbar_background` becomes
  `#FF0B0E14`, both bars keep spending it, `s23_kit_test.go:4139`'s alpha pin moves 0.88 → 1.0 under
  ADR-020, and `InboxChromeTest.kt:335-340`'s translucency assertion inverts. No o3 exemption, no
  dead row. W1's evidence file records the gate line and this ruling.

**IME note.** `bottomInsetPx` (`PhoneActivity.kt:254-255`, `maxOf(bars.bottom, ime.bottom)`) is
untouched; `ConversationScaffoldViewTest.kt:127-138` pins it through all four items.

## 3. W2 Make sending work

Bead: `agents-tracker-d45a.2`. Worktree `refit-w2`. Three review rounds (PTY byte path). Files: `internal/shimwire/`,
`internal/shim/server.go`, `internal/protocol/types.go`, `internal/skeleton/{sessiontap,chat,inject}.go`,
`internal/adapter/claude/interaction.go`, `ui/ErrorRouting.kt`, `PhoneSurface.kt` (one function), tests.

### W2.1 Daemon-authored control keys carry their provenance
- **Symptom.** Phone Stop (ESC) and Allow/Deny ("1"/"3") travel as `wire.TDataIn`
  (`chat.go:662`, `inject.go:120` → `sessiontap.go:366-371` → `fromdaemon.go:391-395`), so
  `ptyWriter.WriteInput` (`internal/shim/server.go:836-861`) counts them as typing and every later
  `composer_send` is refused `input_busy` until someone presses Enter at the machine.
- **Current** (`server.go:278-282`, `:877-881`):
```go
case wire.TDataIn:
    if !helloed { continue }
    _, _ = s.ptyIn.WriteInput(payload)
...
func (p *ptyWriter) submitMessage(text []byte, gap time.Duration) error {
    p.mu.Lock(); defer p.mu.Unlock()
    if p.sinceSubmit != 0 { return errInputBusy }
```
- **Target.** Provenance on the frame, mirroring the `SubmitTransaction` capability exactly:
```go
// internal/shimwire/shimwire.go
TypeControlInput = "control_input" // daemon-authored keys: interrupt, dialog answer
ControlInput bool   `json:"control_input,omitempty"` // hello reply capability
Keys         string `json:"keys,omitempty"`          // control_input payload
// internal/shim/server.go, beside TypeSubmit; hello reply gains ControlInput: true
case shimwire.TypeControlInput:
    _, _ = s.ptyIn.Write([]byte(ctrl.Keys)) // the non-counting write
// internal/protocol/types.go
type ControlInputWriter interface { ControlInput(keys []byte) error }
// internal/skeleton/sessiontap.go; chat.go:662 and inject.go:120 call ControlKeys instead of Input
func (s *tapSub) ControlKeys(p []byte) error {
    if s.mode != readWrite { return nil }
    if cw, ok := s.t.up.(protocol.ControlInputWriter); ok { return cw.ControlInput(p) }
    return s.t.up.Input(p) // old shim: today's behaviour, disclosed degrade
}
```
  Why not "the shim resets the counter on a known control byte": `server.go:817-822` and
  `chat.go:437-448` forbid the shim judging what a byte does to the input line, and an owner's Escape
  at the terminal is the identical byte. Provenance is a property of who sent the frame. The counter
  does **not** reset at turn end (an inference from the drawn screen; a half-typed draft outlives the
  turn). Residual risk, stated: an approval key that lands on an empty dialog dirties one character
  uncounted; bounded, rare, and preferable to a permanently poisoned session.
- **Tests first.** `internal/shim/controlinput_test.go`:
  `TestSubmitMessage_AfterInterruptKeys_IsAccepted`, `TestSubmitMessage_AfterApprovalKeys_IsAccepted`,
  `TestSubmitMessage_AfterTypedText_IsRefused` (negative control), `TestControlInput_ReachesThePTYByteExact`.
  `internal/skeleton/s0_controlkeys_test.go`: `TestPhoneStopThenSend_IsDelivered`. Must change:
  `shimwire_test.go:45,64,132` (enumerate the new type and fields). Untouched:
  `s0_writerserialise_test.go:176`, `s0_concurrentwriters_test.go:242`, `g4_ptybytesacred_test.go:165`,
  `s0_realclipty_test.go:313`.
- **Gates.** `shimwire.Version` stays 1 (capability-negotiated); `TestDecode_UnknownTypePreserved`
  proves an old shim tolerates the type.
- **Done when** Stop-then-send and Approve-then-send are delivered; an owner draft still refuses
  byte-for-byte; `internal/shim` and `internal/skeleton` green under `-race`.

### W2.2 Every machine reason gets plain words
- **Symptom.** `ErrorRouting.kt:504-511` maps three codes; the other fifteen reach the user as
  "Something failed in a way the app does not recognise."
- **Vocabulary** (`internal/protocol/schema/`): `stale_turn`, `interrupt_unsupported`, `unavailable`,
  `structured_unsupported`, `input_busy` (chat.go); `policy`, `kill_switch`, `rate_limit`,
  `stale_approval`, `not_authorized`, `invalid_field`, `op_not_implemented` (remote.go);
  `unknown_preset`, `stale_preset`, `outcome_unknown` (launchpreset.go); `capability_refused`,
  `stale_generation`, `stale_instance` (terminalcontrol.go).
- **Target.** Do **not** widen `toToken` (it decides state and remedy; `:490-500` forbids a per-verb
  fact borrowing a generic remedy). Add a copy-only sibling `MachineRefusalCodes.sentence` with one
  row per code and `sentenceFor(code)`; UNKNOWN is reached only by a code this build has never seen.
  The sentences (W5's rule: short, from the user's side):
```
stale_turn            Not sent. There's a new reply. Read it, then send again.
input_busy            Not sent. Finish typing on your computer first.
rate_limit            Too many requests. Wait a moment.
interrupt_unsupported This agent can't be stopped from the phone.
unavailable           Your computer no longer has this.
structured_unsupported Chat is off for this session.
policy                Your computer's rules don't allow this.
kill_switch           Remote control is off on your computer.
stale_approval        Already answered. Reload to see.
not_authorized        This phone isn't allowed to do that.
invalid_field         Your computer couldn't read that request.
op_not_implemented    Your computer can't do this yet.
unknown_preset        That setup is gone. Pick another.
stale_preset          That setup changed. Check it and confirm again.
outcome_unknown       Not sure it went through. Check before retrying.
capability_refused    This session doesn't allow that from the phone.
stale_generation      Your turn at this terminal ended. Take control again.
stale_instance        This session restarted. Open the new one.
```
  "your computer" becomes the machine label wherever the model has it (W5.2).
- **Tests first.** `MachineCodeRoutingTest.kt`: add `every daemon code this build ships has a
  sentence` (key set equals the 18 literals); `:63`, `:69` untouched (routing unchanged).
  `ErrorRoutingRefusalCopyTest.kt:165`: now asserts `unavailable`/`invalid_field` are absent from
  `toToken` and present in `sentence`. `mobile/ksvb5_refusalcopy_test.go`: add
  `TestRefusalSentences_CoverEverySchemaCode` (reads the schema constants by value); `:62` untouched
  (the machine's own words are never replaced, they sit in the detail cell).
- **The caller** (fleet W2 finding: the table had none). `ErrorRouter.routeMachineCode` returns
  `unknown.copy(message = sentenceFor(code))` when the code has a sentence and no `toToken` row —
  state and remedy stay UNKNOWN, words only; a code with no sentence returns `unknown` unchanged.
  `SessionDetailPanel.composerVerdictFor` prefers the routed message over
  `ComposerModel.noticeFor("REFUSED").copy` for an unmapped code, so `structured_unsupported` reads
  "Chat is off for this session." under the composer.
- **Done when** no refusal from a shipped daemon renders the UNKNOWN sentence; `toToken` still has
  three rows.

### W2.3 One refusal, said once
- **Symptom.** `PhoneSurface.kt:4493-4497` writes `composerRefusal` (drawn under the composer via
  `SessionDetailPanel.kt:916` → `:1097-1100` → `SessionDetailView.kt:529-532`) **and** calls
  `say(PressFeedback.ofRefusal(...))`, which writes the outcome line (`:473-478`) and a toast with the
  same sentence.
- **Target.** Delete the `say()` block; the composer notice is the single surface. **The notice
  path carried no detail cell** (fleet W2 finding: only `ComposerModel.noticeFor(state).copy` crossed
  `SessionDetail.composerRefusal` → `SessionDetailPanel.composerNotice` → `DetailTag.COMPOSER_NOTICE`),
  so one is built: `composerRefusalDetail` through `SessionScreens.kt`, `SessionDetailPanel.kt`,
  `SessionDetailView.kt` (a mono ink3 line directly under the notice, absent when empty) and
  `PhoneSurface.kt` (set with the verdict, cleared where `composerRefusal` is). About fifteen lines;
  those four files are W2.3's list.
- **Tests first.** RED: an `android/gate` test that `renderComposerVerdict`'s body contains no
  `say(` while `renderInterruptVerdict` still does (the control); `SessionDetailViewTest.kt`: `the
  machine's words are drawn under the composer notice and absent when empty`. Fences (already green,
  kept): `a refused send says its sentence exactly once across the view tree`.
  `SessionDetailComposerTest.kt:315` broadens: `DetailTag.OUTCOME` absent when
  `DetailTag.COMPOSER_NOTICE` present. `SessionDetailVerdictTest.kt:117` (refused Stop shows the
  reason) untouched: Stop is not a composer refusal and keeps `say()`.
- **Gates.** `r6_chat_ui_test.go:307-310` needs `composerVerdictFor(` and `clearsDraft` to survive
  (they do); re-read `:334` before landing (detail cell requirement).
- **Done when** a refused send renders one sentence and zero toasts; a refused Stop still toasts;
  the draft is retained on every refusal.

### W2.4 Claude's synthetic prompts are not messages
- **Symptom.** `internal/adapter/claude/interaction.go:145-152` shapes every non-empty
  `UserPromptSubmit` prompt as `KindUserMessage`; Claude Code fires that hook for its own envelopes.
- **Corpus** (opening tags on user-role entries across 1532 local transcripts): `system-reminder`,
  `teammate-message`, `task-notification`, `tool_use_error`, `persisted-output`,
  `local-command-caveat`, `command-name`, `local-command-stdout`, plus `title` and `svg` — the last
  two are pasted user content, which is why a bare "starts with `<`" rule is wrong.
- **Target.** A recorded allowlist and a closed-envelope predicate:
```go
var syntheticPromptTags = map[string]bool{
    "system-reminder": true, "task-notification": true, "teammate-message": true,
    "agent-message": true, "tool_use_error": true, "persisted-output": true,
    "command-name": true, "command-message": true, "local-command-caveat": true,
    "local-command-stdout": true, "local-command-stderr": true, "bash-input": true,
    "bash-stdout": true, "bash-stderr": true,
}
// isSyntheticPrompt: opens with a listed tag AND that tag is closed in the same prompt.
case "UserPromptSubmit":
    if b.Prompt == "" || isSyntheticPrompt(b.Prompt) { return nil }
```
- **Tests first** (`interaction_test.go`): `TestUserPromptSubmit_SyntheticEnvelopesShapeNothing`
  (table over the list), `TestIsSyntheticPrompt_GoldenTagListMatchesTheRecordedCorpus`,
  `TestUserPromptSubmit_ARealPromptContainingAngleBracketsIsKept` (`fix the <div> wrapper`,
  `<title>Foo</title> what does this render?`), `TestIsSyntheticPrompt_AnUnclosedEnvelopeIsKept`.
  `:232`/`:259` golden corpus tests change only if a recorded fixture opens with a listed tag; `:54`
  untouched.
- **Amended by round-1 review (2026-08-28).** Dropping the envelope outright also drops Claude's
  only turn-opening signal (`internal/skeleton/interaction.go:358` opens a turn on every
  `KindUserMessage`; the adapter sets `TurnRef` nowhere), so a session driven by a teammate message
  or task notification would open no turn and Stop would be refused. Ruling: keep the turn, drop the
  bubble. The adapter returns the envelope as a `KindUserMessage` with `Source: SourceSynthetic`
  (new constant, admitted by `Validate`); the daemon opens the turn for it but neither persists nor
  publishes it. Tests: each listed tag shapes one `SourceSynthetic` item; the skeleton opens a fresh
  turn and publishes no `user_message`.
- **Done when** each listed tag shapes one synthetic item that never reaches the wire, the next tool
  item carries a fresh turn id, both negative controls shape one `SourceOwner` item, and
  `Interactions` stays pure and total.

## 4. W3 One button

Bead: `agents-tracker-d45a.3`. Worktree `refit-w3`. Depends on W2.3 (merged first). Files: `PhoneSurface.kt`,
`ui/kit/Composer.kt`, `ui/kit/Kit.kt` (one const), `ui/kit/ConversationMenu.kt`,
`ui/screens/SessionDetailPanel.kt`, `ui/SessionScreens.kt`, two new drawables, tests.

### W3.1 The full-width Stop leaves the composer region
- **Current** (`PhoneSurface.kt:677-685`): `composerRegion` adds `stop`, `composer`, `composerShutDetail`.
- **Target.** `composerRegion` adds `composer` and `composerShutDetail`; `private val stop` (`:364`),
  `stop.text = panel.stopLabel` (`:3279`), `stop.enable` (`:4835`), `stopQuestion` (`:4440`),
  `STOP`/`STOP_CONFIRMATION`/`stopConfirmation` (`SessionDetailPanel.kt:330-340`) and
  `stopVisible`/`stopLabel` (`SessionScreens.kt:196`) are deleted. KDoc `:620-632` rewritten.
  **Stop stays separately reachable**: `ConversationMenu` gains a "Stop" row shown only while
  `composerWorking`, calling the same interrupt press as the square.
- **Tests first.** `PhoneSurfaceControlsTest.kt`: `the composer region is the bar and its notice`
  (`childCount == 2`). Untouched: `SessionStopOfflineTest.kt:48,67,75` (model-side `StopAction`),
  `SessionDetailPanelTest.kt:132`.
- **Gates.** `r6_chat_ui_test.go:271` requires `app.interrupt(`, `interruptOp = ` and
  `launchOutcome(interruptOp)` in production Kotlin: the press body moves, it does not die.

### W3.2 The send control is a 40dp square that reads the live field
- **Current** (`PhoneSurface.kt:489`): `private val send = actionButton("Send line", CtaKind.APPROVE)`.
- **Target.** `send` becomes an `ImageView` from a new kit factory `composerAction(context)`
  (40dp box as `KitMetrics.COMPOSER_ACTION_DP` beside `TAB_ICON_DP`; 48dp minimum touch height;
  tinted by the kit), built with `pressable` whose bound widens from `<V : TextView>` to `<V : View>`
  (`:4895`; the `Button` accessibility delegate already works on any View). The press plan:
```kotlin
val stopping = detailDrawn?.composerWorking == true && typed.text.isBlank() // LIVE field, at press
if (stopping) Press(SendPlane.COMMAND, verb = { app -> app.interrupt(target, turn) },
                    confirmation = "", settle = { rememberInterrupt(it) })
else          /* the existing composer_send body from :489-520, unchanged */
```
  The glyph and spoken label follow the same predicate on every draw and on every text change:
  square + "Stop" when working and blank, arrow + "Send" otherwise. **Working comes from one
  source**: `SessionDetailPanel` gains `val composerWorking: Boolean = transcript.latestTurnId.isNotEmpty()`,
  the fact `headerStateFor` (`:645-657`) and `composerPlaceholder` (`:1093-1095`) already read; the
  header word, the placeholder, the menu row and the square cannot disagree. It joins
  `sessionDetailRedraw`'s patch whitelist deliberately (tbpm.4's hazard).
  **Fence (W2 round-2 review, 2026-08-28).** `TranscriptScreen.openTurnOf`
  (`TranscriptPanel.kt:812-819`) already reads a turn as open from any item's `turn_id`, which is
  what makes a turn the daemon opened on Claude's own envelope (W2.4: no `user_message` is
  published) reach the square. Its comments (`:88-89`, `:807-808`) and every case in
  `TranscriptTurnAndAnchorTest.kt:65-115` say a `user_message` opens it, so a "tightening" to the
  comment would silently reproduce the round-1 defect. W3 adds the test `a turn the daemon opened
  on the CLI's own envelope reads open from its first tool item` (`[tool_run turn-c in_progress]`
  → `latestTurnId == "turn-c"`) and rewords both comments: a turn is open when the newest item
  carries a `turn_id` and is not a terminal `agent_message`, whether or not a `user_message` began it.
  Drawables, in `swarm_nav_back.xml`'s style (24dp, `@android:color/white`, kit-tinted):
  `swarm_send.xml` path `M12 19V5M5 12l7-7 7 7` (stroke 1.7); `swarm_stop.xml` path `M7 7h10v10H7z` (fill).
- **Tests first.** `ComposerTest.kt`: `the composer action is a 40dp square`, `the square keeps the
  48dp touch floor`. `PhoneSurfaceControlsTest.kt`: `the one composer control speaks Send when idle
  and Stop when the session works and the field is empty`, `typing into the field turns Stop back
  into Send`. `SessionDetailPanelTest.kt`: `composerWorking is the open turn, the same fact the
  header reads`. Must change: `PhoneSurfaceControlsTest.kt:86` ("Send line" leaves
  `requiredControls`; `PbE2E2PairAndTypeTest` re-pointed at the content description in the same
  commit), `PhoneLaunchSurfaceTest.kt:268` (prose). Untouched: `CtaButtonTest.kt`, `ComposerSendStateTest.kt`.
- **Gates.** `r6_chat_ui_test.go:270-271` (both verbs, both latches, both `launchOutcome`),
  `resxml_test.go:30` (well-formed vectors, no `--` in comments), `s22b_spacing_test.go:255` (no new dimen).
- **Done when** one control sends and stops; `app.interrupt(` and `app.composerSend(` both reachable
  from it; a fast typist's tap never aborts the agent (proven by the "typing turns Stop into Send" test).

### W3.3 No confirmation before Stop
- **Target.** The square's press carries `confirmation = ""` and no `ask`; `stopQuestion` and
  `STOP_CONFIRMATION` deleted (W3.1). `StopAction.CONFIRM` stays a model arm (`SessionScreens.kt:232`,
  pinned by `SessionStopOfflineTest.kt:67`); the square resolves it directly.
- **Tests first.** `PhoneSurfaceControlsTest.kt`: `pressing the square while the session works
  interrupts without asking` (`ShadowAlertDialog.getLatestAlertDialog() == null`).
- **Gates.** `4lta_offlinestop_test.go` reads `StopAction`/`NOT_SENT`, not the dialog; `r6:312`
  (`renderInterruptVerdict` → `interruptNoticeFor(`) untouched.

### W3.4 "Stopped", once, under the composer
- **Current** (`SessionScreens.kt:263-277`): `INTERRUPT_SENT = "Interrupt sent"`, delivered as a toast.
- **Target.** `INTERRUPT_SENT = "Stopped"` (the KDoc keeps its honesty argument: this is the sealing,
  not the agent's answer; refusals still arrive through `renderInterruptVerdict`). Drawn as one
  centred `noticeLine()` child of `composerRegion`, cleared on the next `drawComposerRegion`; no toast.
- **Tests first.** `PhoneSurfaceControlsTest.kt`: `a stop says Stopped once, under the composer, and
  not in a toast` (`ShadowToast.shownToastCount() == 0`). `SessionScreensTest.kt`: expected value updated.
- **Gates.** Check `PressFeedbackAuditTest.kt:85` / `o6_haptics_test.go` do not require a non-empty
  `confirmation` on every `Press`.

## 5. W4 Slate and breathing

Bead: `agents-tracker-d45a.4`. Worktree `refit-w4`. Files: `internal/design/tokens.json`, `internal/design/tokens_test.go`,
new `docs/research/slate-maquette.html`, `android/app/src/main/res/values/{colors,type}.xml`,
`docs/design/icon-candidates/solid-wedge.svg`, `docs/adr/ADR-020-*.md`, `docs/adr/README.md`,
`docs/adr/ADR-012-*.md` (amendment section), `android/gate/{s22b_*,obsidian_contrast_test.go,s22b_designsource_test.go}`,
`ui/kit/{SessionRow,ActivityRow,MessageBubble,Kit}.kt`, `theme/SwarmTheme.kt`, two 512px PNGs, tests.
`android/design-tokens.tsv`: comment lines only. `dimens.xml`: comment lines only.

### W4.1 The token origin points at a Slate maquette
- **Current** (`tokens.json:1-5`): `"source": "docs/research/obsidian-maquette.html", "skin": "obsidian"`.
  `tokens_test.go:41-46,66-67` pin the path, `htmlTokenCount = 35`, and `skinSelector{"obsidian": ":root"}`.
- **Target.** A **new file** `docs/research/slate-maquette.html` (not a second block: `parseSkinTokens`
  takes the first `:root` match) with exactly 35 `--p-*` declarations in one `:root` block; `tokens.json`
  `source`/`skin` → slate; `tokens_test.go` path, ref and selector → slate. Values:
```
--p-bg #0b0e14  --p-card #131824  --p-elev #1b2334  --p-well #080a10  --p-hair #262e3f
--p-ink #eef2f8 --p-ink2 #9aa6ba  --p-ink3 #66718a
--p-hero #8eb4e6  --p-hero-ink #0b1524  --p-att #8eb4e6  --p-cta-bg #8eb4e6  --p-cta-ink #0b1524
--p-work #6fc3bc  --p-ok #8cc49a  --p-err #e5736b
--p-sheet-hi #1b2334  --p-sheet-lo #10141d  --p-tabbg rgba(11,14,20,1)   (opaque; see W1.3/W1.4 ruling)
--p-card-fx inset 0 1px 0 rgba(238,242,248,0.08)   --p-lit-fx inset 0 1px 0 rgba(238,242,248,0.18)
--p-cta-fx 0 0 18px rgba(142,180,230,0.22)         --p-workbar linear-gradient(90deg, #6fc3bc, transparent 85%)
--p-sweep-fx sweep 500ms rgba(238,242,248,0.30)    --p-grain 0.04
--p-card-r 16px  --p-sheet-r 20px  --p-btn-r 12px  --p-chip-r 10px  --p-dot-r 4px
--p-font, --p-mono, --p-display-wt, --p-display-tr, --p-body-tr: unchanged
```
- **Tests first.** `tokens_test.go:279` `TestChosenSkinIsObsidianAndPinnedDark` → `...Slate...`, with
  the file's AUTHORIZED REWRITE comment citing ADR-020; negative controls `:224`, `:239` retargeted to
  Slate literals. `htmlTokenCount` and `colourTokenCount` do not move.
- **Done when** `go test ./internal/design/` green including `TestTheDriftCheckCanActuallyFail`;
  `kinds` unchanged.

### W4.2 The join follows without a row change
- `colors.xml:39-61,72-73`: nineteen hex values move (`swarm_background #FF0B0E14`, `swarm_surface_card
  #FF131824`, `..._elevated #FF1B2334`, `..._well #FF080A10`, `swarm_hairline #FF262E3F`,
  `swarm_text_primary #FFEEF2F8`, `..._secondary #FF9AA6BA`, `..._tertiary #FF66718A`, `swarm_hero
  #FF8EB4E6`, `swarm_hero_ink #FF0B1524`, `swarm_state_attention #FF8EB4E6`, `..._working #FF6FC3BC`,
  `..._ok #FF8CC49A`, `..._error #FFE5736B`, `swarm_cta_background #FF8EB4E6`, `swarm_cta_ink
  #FF0B1524`, `swarm_sheet_gradient_top #FF1B2334`, `..._bottom #FF10141D`, `swarm_tabbar_background
  #FF0B0E14`, opaque). `s16_tokens_test.go` needs no code change; its `colourTokenCount = 17` is a floor.
- **Done when** `go test ./android/gate/ -run 'PBTOK1|PBTOK5'` green with a comment-only TSV diff.

### W4.3 Derivations recompute themselves
- `derive.go:283-313` holds three live derivations (`attention-row-border`, `deny-fill`,
  `needs-input-dot-glow`); `Derivation.Resolve(tokens)` takes the map, so nothing is edited. Verify
  by running `-run PBTOK7` that no Slate literal collides with a derivation output (Slate deny-fill is
  `#21E5736B`) and that the three `Site:` selectors exist in the new maquette.

### W4.4 Contrast is measured, not asserted
- Floors (`obsidian_contrast_test.go:82-104`) are byte-identical after the wave: primary 90,
  supplementary 45, incidental 24, cta-label 55, accent-text 50, error-text 38, indicators WCAG 3.0.
  Computed on `--p-bg`: ink 98.8, ink2 53.3, ink3 27.5, hero-ink-on-hero 60.9, hero 60.0, err 45.2 —
  all pass. The tight pair is `--p-ink3` on `--p-elev` (the lightest ground); the gate prints it and it
  must be ≥ 24.0. File and identifiers renamed `slate_contrast_test.go` / `slate*` / `TestADR020_*`,
  citations kept as history; the retired-floor ceiling proof (`:885`) re-derived on `#8eb4e6`.
- **Done when** all 16 ink pairs and 10 indicator pairs print green over the Slate origin.

### W4.5 Spacing: the maquette moves, the components follow
- The ten-step 2dp grid and the frame constants are unchanged; components spend a different step.
  Maquette slab `margin: 0 16px 14px; padding: 12px 16px` (was `0 14px 10px` / `13px 15px`).
  `SessionRow.kt:167-180` `sessionList` gap `space_8` → `space_14`, side `space_12` → `space_16`;
  `ActivityRow.kt:92-97` `12/10/12/10` → `16/12/16/12`; `MessageBubble.kt:78-81` `12/8/12/8` →
  `16/12/16/12`, `:88` `topMargin` → `space_14`. `ScreenColumn.kt` unchanged.
- **Tests first.** `s22b_spacing_test.go`: ledger rows `space_12 {12,13}` → `{12}`, `space_14 {14,15}`
  → `{14}` only if no selector still declares 13/15; `wantMovers` re-derived and recorded;
  `s22b_designsource_test.go:78,88-89` path and block markers → slate; ratified exceptions register
  checked. `KitDensityTest.kt:160-174` claims updated to the new steps.
- **Widened (fleet W4 finding, ruled 2026-08-27).** The kit call sites are joined by
  `android/gate/s23_kit_test.go` (`s23Spacing` rows for `.prows {padding: 0 12px; gap: 8px}` and the
  `s23DerivedSpacing` rows) and by `docs/design/substrate-components.md` rows #14 and #26 to the
  older Substrate values, not to the maquette's `.slab`. Both are in W4.5's list: the rows are
  re-pointed to `slate-maquette.html`'s `.slab` under an AUTHORIZED REWRITE note citing ADR-020 D2,
  and the claims in `InboxRowTest`, `ActivityRowTest`, `MessageBubbleTest`, `KitDensityTest:299`
  follow. No exemptions; a row that cannot be re-pointed to a declared design value stops the wave.
- **Also ruled.** `--p-sweep-fx` keeps `rgba(255,252,244,0.30)` (ADR-009 D5 survives, sweep tint
  included; `Motion.kt`/`o4_sweep_test.go` untouched). `dimens.xml`'s four `swarm_radius_*` values
  follow the radius tokens (PB-DS-4), so its diff is not comment-only. `build.gradle.kts`'s staging
  line and `DesignScale.kt` point at `slate-maquette.html`.
- **Done when** `-run PBDS1` green with worst drift ≤ 1dp and `dimens.xml` diff comment-only.

### W4.6 Type: five rungs, all shifted
- Ladder: **display 24 / title 15 / body 14 / code 12.5 / micro 11** (gaps 9 / 1 / 1.5 / 1.5, all
  ≥ 1sp as `TestPBDS2_TheLadderIsTheFiveRuledRungs` requires). The authority is ADR-012's rung table,
  amended by an ADR-020 section (not by editing R1's text); Move cells use a minted ruling `R9`
  (`s22bRungMoveRe` accepts `(R<digit>)`). `type.xml` sizes follow the table; every `lineHeight` is
  recomputed as multiplier × rung (`Body.Message` 1.45×14 = 20.3sp, `Mono.Code` 1.5×12.5 = 18.75sp,
  `Mono.Meta` 1.6×11 = 17.6sp, and so on). `Display.SAS` 34sp is off the ladder and does not move.
- **Done when** `-run PBDS2` green including `TestPBDS2_TheRungReadersRefusePerturbedInput`.

### W4.7 The launcher mark is repainted, not redrawn
- `ic_launcher_foreground.xml` and `mipmap-anydpi-v26/*.xml` hold `@color/swarm_hero` and
  `@color/swarm_background` and are **unchanged**. `docs/design/icon-candidates/solid-wedge.svg`
  literals `#0E0B08` → `#0B0E14`, `#C9A876` → `#8EB4E6` (twice) and its token comment, in the same
  commit as `tokens.json`: `appicon_test.go:616,665` compare the SVG literal to the resolved token.
  `docs/ops/play-assets/play-store-icon-512.png` and `docs/design/store-assets/icon-512.png` are
  re-rendered by hand from the Slate SVG; `docs/verification/appicon-evidence.md` XOR re-run.
- **Done when** `-run Icon` green with zero edits under `res/`; no `C9A876`/`0E0B08` outside quoted history.

### W4.8 ADR-020 and the Kotlin literals
- `SwarmTheme.kt:40-44` `EXPECTED_DARK_COLORS` → `0xFF0B0E14, 0xFFEEF2F8, 0xFF9AA6BA`;
  `Kit.kt:400` `KEY_LIGHT_ALPHA 0.10f` → `0.08f`; `:420` `LIT_KEY_LIGHT_ALPHA 0.22f` → `0.18f`;
  prose quoting `rgba(246,243,236,…)` → `rgba(238,242,248,…)`; `Toggle.kt:29` / `ToggleTest.kt:214`
  ceiling on `#c9a876` re-measured on `#8eb4e6`. `ThemeTokenOriginTest` needs no value edit.
- `docs/adr/ADR-020-slate-palette-and-breathing-scale.md`: Status Accepted, Date 2026-08-27; Context
  (the pipeline is sound, the palette and density are what the owner ruled on); Decision D1 skin =
  slate, D2 breathing by spending steps not adding them, D3 the five rungs shift, D4 the mark is
  repainted; what ADR-009 this supersedes (D1, D3 values, colour half of D4) and what survives (D2,
  D4 material grammar, D5, D6, D7, D8 floors); Consequences (tight `--p-ink3`/`--p-elev` pair,
  ceiling proof re-derived, verification screenshots stale); Alternatives (recolour without
  re-typesetting; second block in the Obsidian file; 4dp grid). `docs/adr/README.md`: row after 019;
  the "next FREE" note becomes 021.
- **Done when** the Kotlin suite is green including `ThemeNightModeTest` and `ThemeTokenOriginTest`,
  and `grep -rn "c9a876\|C9A876\|0e0b08\|0E0B08"` over `android/ internal/ docs/design/` returns only
  quoted history.

## 6. W5 Words everywhere

Bead: `agents-tracker-d45a.5`. Worktree `refit-w5`. **Runs last** (it touches every screen file). Files: the
nineteen constant-holders in the anchor table, `docs/design/conversation-drawing.html`, the copy
tests named in W5.4. No structural change: every rewrite replaces a literal in place; no constant is
renamed, no `when` arm added, no map key changed.

**The copy gate.** `scripts/check-conversation-copy.py` byte-compares the owner-signed sheet
`docs/design/conversation-drawing.html` against fifteen bound Kotlin literals (`bubble.pending`,
`bubble.refused`, `bubble.stale`, `composer.{ended,nochat,offline,torn}`, `decision.pill`,
`decision.settled.owner`, `decision.unknown`, `earlier`, `empty`, `kill.confirm`, `menu.a11y`,
`sync`), and `internal/verify/conversation_copy_test.go` proves the checker can fail. **The sheet
changes in the same commit as the Kotlin.** The sheet's other rows are superseded by the tables
below on the owner's direction and are updated to match.

### W5.1 The rule and the anchors
One short sentence, from the user's side of the screen, naming the computer by its name where the
model has it and "your computer" where it does not; verbs on buttons; commands in a well, never in
a sentence; no "event stream", "structured record", "input line", "relay", "ceremony", "plane".

| File (`android/app/src/main/kotlin/dev/swarm/phone/`) | Anchor | Shape |
|---|---|---|
| `ui/screens/SessionDetailPanel.kt` | `:432 STALE_NOTICE` (+ `:328,330,352,402,414,417,423,458,696,704,719-740`) | `private const val` |
| `ui/screens/TranscriptPanel.kt` | `:549 HEADING`, `:560 EMPTY`, `:1330-1332 GAP_*`, `:1354` | const |
| `ui/kit/Composer.kt` | `:141-158 ComposerShut`, `:251-268 noticeFor`, `:275 placeholderFor` | `when` arms |
| `ui/ErrorRouting.kt` | `:252-387 byToken` (20 arms) | map entries |
| `ui/ConnectionUi.kt` | `:48-100`, `:150-157`, `:176-180`, `:252-255` | `when` arms |
| `ui/SyncStatus.kt` | `:104-152` | consts |
| `ui/SessionScreens.kt` | `:259 NOT_SENT_NOTICE`, `INTERRUPT_SENT` (W3.4) | consts |
| `ui/screens/TerminalFallbackScreen.kt` | `:125-152`, `:224`, `:238`, `:265` | consts |
| `ui/TriageInbox.kt` | `:103 staleNotice` | const |
| `ui/screens/ActivityPanel.kt` | `:138 SECTION`, `:149 EMPTY`, `:158 STALE_NOTICE` | consts |
| `ui/SettingsScreen.kt` | `:178`, `:202-208`, `:271-278`, `:415`, `:428-459` | consts |
| `ui/MachineAndLaunch.kt` | `:156`, `:161`, `:174-179` | functions |
| `ui/screens/PairedMachineRow.kt` | `:77`, `:87`, `:104-109` | consts |
| `ui/screens/MachinesPanelScreen.kt` | `:47-138`, `:218-227` | consts |
| `ui/screens/LaunchPresetScreen.kt` | `:234-241`, `:250-270`, `:279-290` | `when` arms |
| `ui/screens/PairOnlyScreen.kt` | `:108`, `:112`, `:149`, `:179-218` | consts |
| `ui/PairingUi.kt` | `:152`, `:192-195`, `:444-504` (17 arms) | `when` arms |
| `ui/screens/PairingPanel.kt` | `:197`, `:268`, `:279`, `:299` | consts |
| `ui/screens/TriageInboxScreen.kt` | `:164-169`, `:186-191`, `:218-238` | maps |

### W5.2 Naming the computer
Already in reach, interpolate directly: `SessionDetailPanel` (`detail.machineLabel`, `:996`),
`SettingsPanel` (`:446-447, 470`), `MachinesPanelScreen` (`row.displayName`), `PairedMachineRow`
(`confirmationFor(machine)`), `TriageInboxScreen` (`machineNames`). One-line plumbing:
`ComposerModel.noticeFor(code: String, machine: String = "")` with `machine.ifEmpty { "your computer" }`
in each arm; no call site breaks. Out of reach and **must not** grow a parameter: `ErrorRouting.byToken`,
`ConnectionUi`, `SyncStatus`, `PairingUi`/`PairingPanel` (there is no name before pairing),
`TerminalFallbackScreen` — these say "your computer".
- **Tests first.** `ComposerTest.kt`: `noticeFor names the machine when one is known`, `noticeFor
  falls back to your computer`.

### W5.3 Chrome removed
- `TranscriptView.kt:264` no longer adds the "Conversation" section label; `TranscriptPanel.HEADING`
  and the `heading` field go; `TranscriptTag.SECTION_LABEL` and the empty-state branch (`:269-272`) stay.
- `ActivityPanel.kt:138 SECTION = "Journal"` goes; the single section has no heading (W7.4 adds day headings).
- `STOP_CONFIRMATION` (`SessionDetailPanel.kt:339`) goes with W3; `KILL_CONFIRMATION` (`:352`) stays,
  shortened.
- **Tests first.** `TranscriptViewTest.kt:129` → `assertNull(kitFind(SECTION_LABEL))`; constructor
  calls at `TranscriptViewTest.kt:76`, `TranscriptViewDecisionTest.kt:81` drop `heading`;
  `ActivityPanelTest.kt:55` keeps its count, drops the heading text.

### W5.4 The tables

Conversation:

| Now | Proposed | Where |
|---|---|---|
| Some records are missing: the event stream from your machine had a gap that has not been repaired, so this is not a complete log of the session. [Repair this record] | Some messages are missing. **Reload** | SessionDetailPanel.kt:433, 696 |
| Load earlier messages | Show earlier | SessionDetailPanel.kt:328 |
| CONVERSATION | (removed) | TranscriptPanel.kt:549 |
| No messages for this session have reached this phone yet. | No messages yet. | TranscriptPanel.kt:560 |
| records missing · repair | Missing messages · Reload | TranscriptPanel.kt:1330 |
| Not sent — the terminal's input line was not empty. | Not sent. Finish typing on {machine} first. | Composer.kt:261, ErrorRouting.kt:357 |
| Not sent — the conversation moved on. Read the latest turn and send again. | Not sent. There's a new reply. Read it, then send again. | Composer.kt:256, ErrorRouting.kt:343 |
| Your message was refused and not delivered. | Not sent. Try again. | Composer.kt:265 |
| Something failed in a way the app does not recognise. Try again, and report it if it keeps happening. | Something went wrong. Try again. | ErrorRouting.kt:255 |
| This agent reports no chat surface / You can watch it here, and type at your machine. | Chat is off for this session. / Reply on {machine}. | Composer.kt:154 |
| Not connected to your machine / Messages are never held — nothing is sent when the link returns. | Not connected. / Reconnect to send. | Composer.kt:142 |
| This session's record has a gap / Still typeable at your machine. | Chat is paused here. / You can still type on {machine}. | Composer.kt:148 |
| Message / Add feedback... | Message / Add a note while it works | Composer.kt:275 |
| Your machine did not send more of this conversation. | Couldn't load more. | SessionDetailPanel.kt:414 |
| This session's structured record broke, so it can no longer be typed into from the phone. It is still running on your machine, where you can still type at it. | Chat is off for this session. Reply on {machine}. | SessionDetailPanel.kt:318 |
| Interrupt what this session is doing? This sends Ctrl-C, the same key you would press at the terminal. | (removed, W3.3) | SessionDetailPanel.kt:340 |
| End this session? The agent stops and the session is gone; this cannot be undone. | End this session? This can't be undone. | SessionDetailPanel.kt:353 |
| Something you typed did not reach your machine. Input is live-only: none of it was held, and nothing is sent when the connection comes back. [Clear this record] | 1 message didn't get through. **Dismiss** (N messages …) | SessionDetailPanel.kt:704-736 |
| This phone is holding as much of this conversation as it can. Anything earlier is still on your machine. | That's all this phone can show. Older messages are on {machine}. | SessionDetailPanel.kt:403 |
| Your machine did not send the whole of this message / no longer keeps the whole of this message, so what is shown is all of it | Couldn't load the full message. / This is all that's left of this message. | SessionDetailPanel.kt:417, 424 |
| This was clipped. Tap to fetch the whole of it from your machine. | Tap to see the full message. | TranscriptView.kt:637 |
| This version of swarm cannot show this question. Answer it at your machine, or update the app. | Update the app to answer here. | TranscriptPanel.kt:1354 |
| Decision needed / Your machine could not apply this answer | Needs your answer / Couldn't send your answer. Try again. | SessionDetailPanel.kt:458, ApprovalSheetPanel.kt:158 |
| Your machine did not end this session / did not stop this turn | Couldn't end the session. / Couldn't stop. | SessionDetailPanel.kt:370, 387 |
| Stop did not reach your machine and was not held for later. Try again once the connection is back. | Couldn't stop. You're offline. | SessionScreens.kt:259 |
| Not connected to your machine. / Connecting to your machine. / Connected to your machine. / Lost the link to your machine; reconnecting. | Offline / Connecting… / Connected / Reconnecting… | ConnectionUi.kt:49-67 |
| This view may be missing events. It repairs itself when the link recovers. | Some updates may be missing. | ConnectionUi.kt:178 |
| Repairing the $stream view; the gap clears when the repair arrives. | Catching up… | ConnectionUi.kt:179 |
| Not heard from your machine yet. / for 3h. | Not seen yet / Last seen 3 h ago | ConnectionUi.kt:254-255 |
| SYNCING / QUIET / BROKEN · HEARD / READING / VIEWS · Repair now / Go to Pairing | Syncing… / Last seen {age} / Offline · Last heard / Link / Up to date · Reload / Pair again | SyncStatus.kt:104-152 |
| working · MacBookPro (mono, lower case) | Working · MacBookPro | SessionDetailPanel.kt:515-519 |
| Send line / Stop | arrow icon / square icon (spoken "Send", "Stop") | W3 |

Other screens:

| Now | Proposed | Where |
|---|---|---|
| This list may be incomplete: some of your machine's activity has not arrived yet. | Some updates haven't arrived yet. | TriageInbox.kt:103 |
| Some entries are missing: the event stream from your machine had a gap that has not been repaired, so this is not a complete history. | Some entries are missing. **Reload** | ActivityPanel.kt:158 |
| No activity has reached this phone yet. | No activity yet. | ActivityPanel.kt:149 |
| JOURNAL | (removed) | ActivityPanel.kt:138 |
| Paired with {m} / Ends this pairing first, then re-pairs. / Replace this computer | {m} / (no sublabel) / Replace this computer | PairingPanel.kt:330, PairedMachineRow.kt:60,77 |
| Replace {m}? This ends the pairing and destroys this phone's keys; pairing again needs a new code from the computer. | Replace {m}? You'll need a new code to pair again. | PairedMachineRow.kt:104 |
| Saved on this phone. It takes effect once your machine confirms it. | Saved. Waiting for {m} to confirm. | SettingsScreen.kt:178 |
| Notifications are turned off for this app, so these switches change nothing yet. Turn one on and Android will ask for permission. | Allow notifications to use these. | SettingsScreen.kt:203 |
| Notifications are blocked for this app, so these switches change nothing yet. … | Notifications are blocked. **Open settings** | SettingsScreen.kt:207 |
| Android is not showing notifications from this app … / Android has this app's alert category switched off … | Notifications are off in Android. **Open settings** / Alerts are off in Android. **Open settings** | SettingsScreen.kt:272-278 |
| Your machine did not save this change. | Couldn't save. Try again. | SettingsScreen.kt:415 |
| This phone has no system screen for that, so it cannot be changed from here. | Can't be changed on this phone. | SettingsScreen.kt:429 |
| This phone could not update its push registration, so what it receives may not match these switches until it reconnects. | Notifications may lag until the phone reconnects. | SettingsScreen.kt:442 |
| Battery saver can delay these notifications. | Battery saver can delay these. | SettingsScreen.kt:404 |
| Needs your decision / Approvals and blocked prompts · Task done / Completions and failures | Needs your answer / Approvals and questions · Task done / Finished and failed sessions | SettingsPanel.kt:353-360 |
| The {stream} view has a gap. / The a, b and c views have gaps. | Some updates are missing. | SettingsPanel.kt:584-585 |
| Remote control is switched off at your machine, so it will refuse anything this phone asks it to change. Only the machine's owner can switch it back on. | Remote control is off on {m}. (well:) swarm remote on | MachineAndLaunch.kt:175 |
| {freshness} The relay reports "online", which is the relay's word and not your machine's. | Online · last seen {age} | MachineAndLaunch.kt:156 |
| Computers / Add, switch, or forget a computer | Computers / (no sublabel) | MachinesPanelScreen.kt:44-47 |
| Add computer needs the machine id; nothing was sent. / is still running; nothing was sent. | Enter the computer's id first. / Still adding… | MachinesPanelScreen.kt:59, 119 |
| Forget this computer? This phone deletes its pairing keys and cached sessions for it. The computer itself is untouched, and other computers are unaffected. | Forget {m}? You can pair it again later. | MachinesPanelScreen.kt:71 |
| No computers are paired yet. Pair this phone with a computer first; Computers fills in from the first pairing. | No computers yet. Pair one first. | MachinesPanelScreen.kt:80 |
| Adding a computer registers it on this phone. That computer still needs its own pairing ceremony … | You'll pair with it next. | MachinesPanelScreen.kt:95 |
| Add this computer now? This phone briefly disconnects … | Add {m}? The app reconnects for a moment. | MachinesPanelScreen.kt:109 |
| Selected {m}. This phone's live session has not moved to it yet. | Now viewing {m}. | MachinesPanelScreen.kt:138 |
| {fault}. Forget this computer or pair it again; other computers are unaffected. | Can't open {m}. Forget it or pair again. | MachinesPanelScreen.kt:218 |
| Only {cap} computers stay connected at once; the rest are parked and shown stale. | Up to {cap} computers stay connected. Others pause. | MachinesPanelScreen.kt:227 |
| This machine is unreachable right now, so nothing can be sent to it. | {m} is offline. | LaunchPresetScreen.kt:236 |
| Remote control is switched off on the machine. Turn it on at the terminal with `swarm remote on`. | Remote control is off on {m}. (well:) swarm remote on | LaunchPresetScreen.kt:237 |
| This phone is paired without launch permission. Launching needs the full tier, granted at pairing. | This phone can't start sessions. Pair again with full access. | LaunchPresetScreen.kt:238 |
| The machine has not authored any launch presets yet. Create one at its terminal with `swarm remote presets add`. | No presets yet. On {m}: (well:) swarm remote presets add | LaunchPresetScreen.kt:239 |
| This phone has not asked the machine what it offers yet. Fetch the presets to begin. | Tap Fetch to see presets. | LaunchPresetScreen.kt:240 |
| The session was created on the machine and is in your session list. / The launch is on its way … | Started. / Starting… | LaunchPresetScreen.kt:252-253 |
| This preset changed on the machine after you picked it. … / The machine does not have this preset any more. … | That setup changed. Check it and confirm again. / That setup is gone. Pick another. | LaunchPresetScreen.kt:254-255 |
| The machine refused: remote control is switched off there. / … not authorized … / Nothing was sent: this machine is unreachable. … | Remote control is off on {m}. / This phone can't start sessions. / {m} is offline. | LaunchPresetScreen.kt:256-258 |
| The machine could not prove what happened to this launch. Check the session list first: confirming again sends a new launch and may create a second session. | Not sure it started. Check the Inbox before trying again. | LaunchPresetScreen.kt:265 |
| The machine refused this launch. / … refused the preset fetch … | Couldn't start. / Couldn't load presets. | LaunchPresetScreen.kt:267, 279-289 |
| Sessions, machines and activity all come from the machine this phone is paired with. There is nothing else here until then. | Your sessions come from the computer you pair with. | PairOnlyScreen.kt:108 |
| This phone's key was destroyed and cannot be recovered. Your machine still has this device registered and … | This phone needs to pair again. First, on your computer: (well:) swarm remote revoke {device} | PairOnlyScreen.kt:149 |
| This phone has unpaired itself, and your machine has not confirmed … | Unpaired. If pairing is refused, on your computer: (well:) swarm remote devices | PairOnlyScreen.kt:179-196 |
| This phone could not destroy the key material it had stored, so it is still on this device. | Couldn't clear this phone's keys. | PairOnlyScreen.kt:217 |
| Check these six against the ones on your machine's screen. | Same six on your computer? | PairingUi.kt:152 |
| This is an address on your local network. Confirm it is your own machine before joining. / This phone will connect to this relay and to nothing else. … | Local address. Make sure it's your computer. / Connects only to this address. Check it matches your computer. | PairingUi.kt:192-195 |
| The destination changed after it was shown to you, so nothing was joined. Scan the code again. | The address changed. Scan again. | PairingUi.kt:462 |
| Your machine declined this device. Approve it there, then pair again. | Your computer said no. Approve it there, then try again. | PairingUi.kt:469 |
| The symbols did not match, so someone may be sitting between this phone and your machine. Nothing was joined. Pair again on a network you trust. | Symbols didn't match. Try again on a network you trust. | PairingUi.kt:472 |
| Your machine did not answer in time. Check it is awake and online, then pair again. | No answer. Check your computer is awake, then try again. | PairingUi.kt:476 |
| That code has expired. Ask your machine for a new one. | The code expired. Get a new one. | PairingUi.kt:479 |
| That code belongs to a different machine from the one this phone is paired with. … | That code is for a different computer. | PairingUi.kt:486 |
| Too many pairing attempts from here. Wait a minute, then ask your machine for a new code and try again. | Too many tries. Wait a minute, then get a new code. | PairingUi.kt:491 |
| This phone could not reach your relay. If the relay runs on your home network, connect this phone to that WiFi, then pair again. | Couldn't reach your computer. On home WiFi? Join it, then try again. | PairingUi.kt:498 |
| The pairing did not finish and nothing was joined. Ask your machine for a new code and try again. | Pairing didn't finish. Get a new code and try again. | PairingUi.kt:502 |
| This app cannot use the camera, so it cannot scan a code. Turn the camera on in Settings, or paste the code your machine printed. | Camera is off. Allow it in Settings, or enter the code. | PairingPanel.kt:268 |
| This device has no camera, so it cannot scan a code. Paste the code your machine printed. | No camera. Enter the code instead. | PairingPanel.kt:279 |
| This phone does not know your relay yet. Enter its address once; it is remembered after this pairing. | Enter your computer's address once. It's remembered. | PairingPanel.kt:299 |
| This pairing was interrupted before it finished. Nothing was joined. | Pairing was interrupted. | PairingPanel.kt:197 |
| The camera did not start. Close any other app using it and try again, or enter the code instead. | Camera didn't start. Close other camera apps, or enter the code. | PairingUi.kt:295 |
| No chat here: this build of {p} does not support {c}. / … has no structured plane. | Chat is off for this session. | TerminalFallbackScreen.kt:125, 131 |
| Someone may be typing at this terminal too. Simultaneous typing can interleave. | Someone else may be typing here. | TerminalFallbackScreen.kt:143 |
| Read only. This session's machine did not grant terminal control. | Read only. | TerminalFallbackScreen.kt:152 |
| Last update {n}s ago -- this screen may be out of date. / Not hearing from this machine. This screen is whatever it last sent, not what is there now. | Updated {n} s ago / {m} is offline. This may be out of date. | TerminalFallbackScreen.kt:224, 238 |
| Control ended. Nothing you type reaches this terminal. | Your turn ended. | TerminalFallbackScreen.kt:265 |
| That message is no longer in the history this phone is holding. | That message is gone from this phone. | PhoneSurface.kt:5244 |

### W5.5 Test churn, bounded
A test is updated to the new sentence **only where the test's subject is the sentence** (its name
says so): `ErrorRoutingRefusalCopyTest`, `ConnectionUiStaleNoticeTest`, `PairOnlyPurgeNoticeTest`,
`PairOnlyRevokeNoticeTest`, `PairingCameraCopyTest`, `SyncStatusTest`, `DecisionPillTest`,
`ActivityPanelTest` (stale), `GapDividerTest`, `EarlierChipTest`, `PairedMachineRowTest`,
`MachinesPanelScreenTest`. Where the sentence is a fixture (`KitDensityTest`, `PressFeedbackAuditTest`,
`ScreenAirSweepTest`, `MotionTest`, `VerbDispatchRound3Test`, `PhoneLaunchSurfaceTest`,
`SettingsSurfaceReplaceTest`) the literal is swapped and nothing else moves. No `android/gate` test
asserts UI copy (verified).
- **Done when** `python3 scripts/check-conversation-copy.py .` exits 0, `go test ./internal/verify/
  -run ConversationCopy` passes, the Kotlin suite is green, and `grep -rn "your machine\|the machine\|structured\|event stream\|input line" android/app/src/main` returns only KDoc.

## 7. W6 Chat rows

Bead: `agents-tracker-d45a.6`. Worktree `refit-w6`. Files: `ui/kit/ToolCard.kt`, `ui/screens/TranscriptPanel.kt`,
`ui/screens/TranscriptView.kt`, `internal/adapter/interaction.go` (one `oneOf` entry),
`internal/adapter/claude/interaction.go` (one `case`), tests. Independent of `ryuk`.

### W6.1 A tool row is a verb and one grey line
- **Current** (`ToolCard.kt:30-41`): glyph map `read R, edit E, write W, search /, execute $, fetch @,
  other ?`. (`TranscriptPanel.kt:921-924`): `line = phrase(fields.tool, fields.target)` — for
  `execute` the target is the whole command. The wire vocabulary is sealed at
  `internal/adapter/interaction.go:423` (`read edit write search execute fetch other`); the Claude
  adapter (`claude/interaction.go:250-265`) sends `Task` as `other`.
- **Target.** Add `"agent"` to the `oneOf`, a `case "Task": return "agent"` arm, and `"agent" to "A"`
  in the glyph map (`internal/skeleton/interaction.go:734` already passes `tool_kind` unclipped).
  In `TranscriptPanel.kt`, replacing `:923-924`:
```kotlin
line = verbFor(item.toolKind, fields.target),
emphasis = "",
secondary = firstLine(fields.output.ifEmpty { fields.target }),
// beside phrase() at :1281
private fun verbFor(kind: String, target: String): String = when (kind) {
    "execute" -> "Ran a command"
    "read"    -> phrase("Read", basename(target))
    "edit"    -> phrase("Edited", basename(target))
    "write"   -> phrase("Wrote", basename(target))
    "search"  -> "Searched"
    "fetch"   -> phrase("Fetched", hostOf(target))
    "agent"   -> "Started a helper agent"
    else      -> "Used a tool"
}
```
  For `execute`, `secondary` is the command's first line. `TranscriptView` draws `secondary` as one
  `TextView`, `maxLines = 1`, `ellipsize = END`, `swarm_text_tertiary`. The expanded card is unchanged:
  `card.wellVisible` (`:929`) still gates the well and `overflowOf` still routes past
  `OPEN_IN_PLACE_LINES = 20`.
- **Tests first.** `ToolCardTest.kt:41,52` vocabulary extended with `agent`. `TranscriptPanelTest.kt`:
  `every tool kind gets a verb and other is never a question mark` (`Read main.rs`; `Used a tool`).
  `TranscriptViewTest.kt`: `the secondary line is one line and ellipsised`. Goldens:
  `TranscriptScreenGoldenTest.kt:246-247` become `Read edit-target3.txt` / `Edited …`; `:245`, `:249`,
  `:278`, `:295` unchanged. Go: `internal/adapter/claude` table test gains the `Task → agent` row;
  the `oneOf` test enumerates `agent`.
- **Done when** every kind maps to a verb, `?` never renders, the golden test changes exactly two
  expectations, and the well still opens past 20 lines.

### W6.2 The decision card — pill copy only
The pill becomes "Needs your answer" (W5's table; a bound drawing row). The card's question is
already `block.line`. **A machine-nominated "preferred" choice is not in this session**: IS-APR-4
keeps polarity off the item and `interaction_chain_e2e_test.go:430-457` guards it; a render hint
that a reviewer cannot distinguish from a verdict needs a written owner ruling first (§10).

## 8. W7 Inbox, Activity, Settings, Computers

Bead: `agents-tracker-d45a.7`. Worktree `refit-w7`. Files: `ui/screens/{TriageInboxScreen,TriageInboxView,ActivityPanel,
ActivityPanelView,SettingsPanelView,MachinesPanelView}.kt`, `ui/kit/NavHeader.kt` (one parameter),
`ui/kit/ConversationMenu.kt` (reuse), `mobile/types.go`, `mobile/app.go` (two additive fields),
`PhoneSurface.kt` (Activity tap wiring), tests. `SettingsPanel.kt` and `MachinesPanelScreen.kt`
(models) are **not** edited.

### W7.1 Every Inbox row's second line says state and age
- **Current.** `SessionRow.kt:140-150` always adds the need line; `TriageInboxScreen.kt:251-255`
  passes an empty or unknown wire token through, so a row can carry a blank line. There is **no time
  on the wire**: `mobile/types.go:67-117 Session` has no stamp.
- **Target.** Go, additive: `Session.LastActivityUnixMs int64` — the **machine's** stamp from
  `persist.Meta.LastActivity` (`persist.go:61`), set in `mobile/app.go` beside `Destination`
  (`:1022-1030`); 0 is absent. Kotlin (`TriageInboxScreen.kt:310`):
```kotlin
need = listOfNotNull(
    needCopy(row.need, row.group),
    ageOf(row.lastActivityUnixMs, nowUnixMs),                 // "" when 0
    if (scope == null) machineNames[machineOf(row.id)] else null,
).filter { it.isNotEmpty() }.joinToString(" · ")
```
- **Tests first.** `TriageInboxScreenTest.kt`: `the need line is the state word and the age`
  (`Working · 4m`), `an absent stamp draws no age rather than the epoch`, `the All scope appends the
  machine`. `NeedVocabularyTest.kt` keeps its table (now against `needCopy`). Go: a zero
  `LastActivity` crosses as 0. The AAR is rebuilt and the Kotlin suite rerun against it.
- **Done when** `KitTag.NEED` never renders a raw token or an empty string; twins show different ages.

### W7.2 Empty sections collapse, except Needs you
- **Current** (`TriageInboxView.kt:145-171`): every section draws its label and an empty caption.
- **Target.** One guard: `if (section.rows.isEmpty() && section.group != BLOCKED) return@forEach`;
  the comment at `:146-150` rewritten. The model still emits four sections.
- **Tests first.** `TriageInboxViewTest.kt`: `an empty non-blocked section draws no heading at all`,
  `an empty needs-you still says Nothing waiting`. `TriageInboxScreenTest.kt:161` (four sections in
  the model) stays; `:135` retitled to the needs-you case.

### W7.3 New session stays where it is
`navHeader` has no trailing slot (`NavHeader.kt:36-40`), the kit has no sheet, and
`PhoneSurface.kt:2760-2765` locates `approvalSlot()` by `indexOfChild(launchHost)`. A "+" sheet is
real machinery; it is **not in this session** (§10). What lands: the launch panel keeps its place
under the list, with W5's words and W7.2's shorter list above it.

### W7.4 Activity: by day, with a time, tappable
- **Current** (`ActivityPanel.kt:189-197`): one mono line `session · type · group`; `JournalEntry`
  (`mobile/types.go:223-230`) has no timestamp; `ActivityPanelTest.kt:82` pins that a cursor is never
  shown as a time.
- **Target.** Go, additive: `JournalEntry.TSUnixMs int64` from the daemon's own record stamp (pattern
  at `types.go:310`); 0 is absent. Kotlin: `ActivityPanel.of` groups into one section per day
  (`Today` / `Yesterday` / date), newest first; zero-stamp rows fall into one trailing untitled section;
  `entryFor` prepends `ToolCard.timestampLabel(row.tsUnixMs)` (returns "" for 0). `body` becomes
  `session · word` where `word` is the W5 vocabulary (`started`, `finished`, `needs you`, `connection
  lost`). `ActivityPanelView.kt:129` rows get `setOnClickListener { onSelectSession(entry.sessionId) }`,
  wired to `::selectSession` (`PhoneSurface.kt:2772`).
- **Tests first.** `ActivityPanelTest.kt`: `rows are grouped by day newest day first`, `an absent
  stamp renders no time and no day heading`; `:82` stays green untouched; `:55` retitled `one section
  per day`. `ActivityPanelViewTest.kt`: `a row opens its session`.
- **Done when** `cursor` appears nowhere on screen; an all-zero page renders as today.

### W7.5 Settings: computer first, destructive last
- **Current.** Order is the view's (`SettingsPanelView.kt:236-338`): Pairing (with Replace) →
  Connection → Computers row → Notifications. The model emits one section (`SettingsPanel.kt:450-455`).
- **Target** (view only). (1) Computer card: name, presence dot, one status line (`Online · synced
  2 min ago`), chevron → Computers (the separate Computers row `:316-322` folds into it). (2) Remote
  control one-liner + `swarm remote on` well, only when off. (3) Notifications, two switches, W5
  sublabels, "Battery saver can delay these." (4) "Replace this computer" last, unchanged control
  (`SettingsTag.REPLACE`).
- **Tests first.** `SettingsPanelViewTest.kt`: `the section order is computer, remote access,
  notifications, replace` (tag sequence). `SettingsPanelMachinesEntryTest.kt` assertions move onto
  the card's chevron. `SettingsPanelConnectionViewTest.kt` counts one status line.
  `SettingsPanelScreenTest.kt:55` stays green untouched (the proof this is view-only);
  `SettingsSurfaceReplaceTest.kt` untouched.

### W7.6 Computers: Add behind the header, Forget behind the row
- **Current** (`MachinesPanelView.kt:136-184`): the add form and its CTA are unconditional column
  children; `Forget this computer` is every row's trailing control.
- **Target.** `navHeader` gains `trailing: View? = null` (default keeps `ActivityPanelView.kt:101`,
  `SettingsPanelView.kt:236`, `MachinesPanelView.kt:100` compiling); Computers passes
  `ctaButton("Add", CtaKind.MORE)` which toggles `addForm` visibility (the form stays surface-owned,
  `:75-77`); `MachinesTag.ADD` moves inside the form. Each row's trailing becomes an overflow opening
  `conversationMenu` with one item, `FORGET_LABEL`; `FORGET_CONFIRM` and `filterTouchesWhenObscured`
  (`:133`) unchanged. `brokenNotice` (`:159-163`) and `ADD_LIMITS` (`:181-184`) fold into the form.
- **Tests first.** `NavHeaderTest`: `a trailing action is drawn after the status slot`, `a null
  trailing draws nothing`. `MachinesPanelViewTest.kt`: `the add form is hidden until the header
  action is pressed`, `forget is not on the row, it is in the row menu`. `MachinesPanelViewRound2/3Test`
  position assertions change; destructiveness assertions do not. `MachinesPanelScreenTest.kt`
  (model) untouched.
- **Done when** a healthy two-computer panel draws two rows and nothing below them;
  `MachinesPanelScreenTest` passes untouched.

## 9. Merge order and release

```
Fleet 1 (parallel):  W1 frame   |  W2 sending   |  W4 Slate
                     merge W1 -> merge W2 -> merge W4 (each rebased, gates rerun)
Fleet 2 (parallel):  W3 button (after W2)  |  W6 chat rows  |  W7 screens
                     merge W3 -> merge W6 -> merge W7
Sequential:          W5 words (touches every screen file; last, to avoid conflicts)
Release:             versionCode 19 / versionName 0.9.0, AAR rebuilt, Kotlin suite rerun against it,
                     bundleRelease, swarm-publish --dry-run then internal track, owner handset pass
```

W1, W2 and W4 are disjoint by file and merge in order of readiness. W3 and W7 both edit `PhoneSurface.kt` in different regions;
conflicts are resolved by the orchestrator at merge, never by a fleet editing outside its list.

## 10. Out of scope, with the reason

- **Grouped tool rows** ("Ran 6 commands"): needs a machine-authored sensitivity field
  (chat-surface-plan §10). Per-row one-liners only.
- **Rotation survival** (`t8u6`), **display cutout** (`g2mr`), **history-tear vs lost-sink** (`m5p7`),
  **push deep-link**: unchanged, still open.
- **The router's status-card arm** (`tbpm.8` first fix): a session with no structured record still
  lands on the chat screen; only its words change here.
- **A phone-side "yes" heuristic for approvals**: forbidden (IS-APR-4). The only honest route is an
  additive, non-verdict render hint on the wire (`Decision.Preferred`, set by the Claude adapter,
  carried as one key at `internal/skeleton/interaction.go:533`, drawn as `CtaKind.APPROVE` in
  `TranscriptView.kt:534`). **Owner decision pending**; filed as its own bead, not in this session.
- **New session as a sheet from a "+" in the Inbox header**: no sheet component exists and
  `approvalSlot()` is located by `indexOfChild(launchHost)`. **Owner decision pending**; filed as its
  own bead. W7.6's `navHeader.trailing` parameter is the safe first piece and lands regardless.
