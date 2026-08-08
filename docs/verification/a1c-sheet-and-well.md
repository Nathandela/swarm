# A1c — the approval sheet says what the item carries, and the terminal well is deleted

Date: 2026-08-08
Slice: I1 exit, phone half (workpackage A-SHEET-AND-WELL)
Decisions of record:
[ADR-009-structured-chat-interaction.md](../adr/ADR-009-structured-chat-interaction.md) (1)(2)(3)(4),
[ADR-009-obsidian-visual-direction.md](../adr/ADR-009-obsidian-visual-direction.md) (D4.4, D6),
[interaction-schema.md](../specifications/interaction-schema.md) §3.5, §7, IS-APR-1..4, IS-LIFE-2/-4/-6

Two halves of one decision. Half 1 fills the three gaps `ApprovalSheetPanel.kt` recorded as refusals
because the wire carried nothing to fill them. Half 2 deletes the plain-text terminal well, which
ADR-009 (3) dates to this slice's exit: *"a fallback surface that outlives its replacement stops
being a fallback and becomes the design."*

Neither half could ship alone. A run that deleted the grid and left the sheet unable to state a
question would have removed a surface and put nothing in its place.

---

## 1. What changed, and what it replaced

### Half 1 — the sheet

`ui/screens/ApprovalSheetPanel.kt` recorded three refusals under *"The gap between this and the
maquette, stated rather than filled in"*. All three are now facts on the wire, and each refusal is
**replaced by what it turned into** rather than deleted — a refusal left standing over a wire that
has moved reads to the next agent as a rule (ADR-009's own Notes section says exactly this).

| The maquette's frame 2 | What the sheet drew before | What it draws now |
|---|---|---|
| `Claude wants to push the release commit to main.` | `swarmmobile.Session.Need` — the journal record TYPE, i.e. `needs_input` | §3.5's `summary`, the adapter's own one-line headline |
| `$ git push origin main` | the daemon-rendered terminal snapshot, verbatim | §3.5's `action` (§7's structured literal), or IS-APR-3's `prompt_lines` on the fallback |
| `[ Allow ] [ Deny ]` | nothing: *"THERE IS NO APPROVE VERB"* | one `ctaButton` per `decisions[].label`, answered by `App.Approve(session, itemID, decisionID)` |

New: `ui/screens/ApprovalSheetView.kt` — the sheet's **first composition**. `kit/ApprovalSheet.kt`
shipped in phase O6 with its well and its actions as slots and nothing to fill them; that phase was
recorded PARTIAL for precisely that reason (*"it is built, tested, and has no production caller … A
protocol decision has to land before item 1 can be closed"*). The kit factory is **unchanged** — this
slice adds no line to it.

The prompt-card mode takes the **same** composition: ADR-009 (4) is *"a card, never a grid"*, and a
`prompt_card` differs only in where the well's literal came from.

### Half 2 — the well

| Deleted | Why |
|---|---|
| `ui/screens/PeekPanel.kt`, `PeekPanelView.kt` | ADR-009 (3) names them: *"`PhoneSurface.kt`'s `peekHost` / `PeekPanel` path and the screens under it"* |
| `PeekPanelViewTest.kt`, `PeekPanelScreenTest.kt`, `PeekPanelLeaseVerdictTest.kt` | the suites of a deleted screen |
| `SessionDetailView.kt`'s `monoWell(terminal = true)` | (1): *"no terminal emulation and no raw grid anywhere in the app"*. The same grid was drawn twice; both go. |
| `SessionDetail.snapshotText` / `.snapshotStale` / `.hasSnapshotCard` / `.snapshotStaleNotice`, `TerminalPeek` | nothing renders a grid, so nothing carries one. `TerminalPeek` became `SessionLease` — its three lease properties survive, its four grid properties do not. |
| `PhoneSurface.watch` / `.unwatch` and their call sites | (2): *"no phone surface issues a watch"*, so §6's append budget is spent by the journal alone |
| `SessionDetailPanel.transcript: TranscriptSection` (the journal-record log) | (1): the session surface is the **item** transcript. The four kit components moved whole to `TranscriptView.kt`. |

**Kept, deliberately.** `kit/MonoWell.kt` — the workpackage's own instruction was to check for another
caller, and there are several: the pairing command line, the transcript's tool output and file diffs,
and now the approval sheet's literal. §2's rule is *"one component for every mono block in the app"*.
What has no caller left is the `terminal = true` variant, and `TestADR009_NoScreenRendersTheTerminalVariantOfTheWell`
is what keeps it that way.

**Kept, on the coordinator's ruling.** Take control and the send-line keyboard. ADR-009 (5) keeps raw
input *"exactly as decided, as the substrate"*; only the grid is retired. Both were the peek's, so
they were **re-homed onto the session detail** — the screen a session is read on now — together with
PB-INPUT-2's lease sentence, word for word. Deleting a working capability and re-adding it when the
composer lands would have left the phone unable to type for a whole slice, which no decision asked for.

---

## 2. The instruction this slice REFUSED, and why

The workpackage asked for decision buttons *"coloured by verdict (allow|deny|other)"*. **That is not
buildable and would be wrong if it were.** IS-APR-4:

> The verdict is **machine-side and is NOT a field on the item** … the card labels its buttons from
> `decisions[].label` and no phone surface switches on polarity, so putting it on the wire would only
> create a second place for the two to disagree.

The producer emits `{id, label}` and nothing else (`internal/skeleton/interaction.go:422`), and
`internal/skeleton/interaction_chain_e2e_test.go:415-421` fails the build if a `verdict` ever appears
beside them — a fence whose comment records that emitting one *"left the whole suite green until this
check existed"*. So a phone that painted `.a2-ok` on a label would be asserting a grant the daemon
never told it about.

Every decision is therefore `CtaKind.MORE`, the one CTA variant that asserts nothing. This is also
what `kit/ApprovalSheet.kt` already ruled about width — *"A sheet whose Allow is wider than its Deny
has decided for the user, and this is the one surface in the app where it must not"* — and it satisfies
the Obsidian direction's scarce-accent rule without a second competing accent on the sheet.

**Escalated and ruled on** by the coordinator before implementation: *"REFUSE MY INSTRUCTION, your
reading stands."* `DenyChip` stays unused on this sheet.

Two phone-side fences now hold the line: `TestADR009_TheItemIsDecodedOutsideTheScreen` fails if the
decoder reads a verdict, and `TestADR009_TheSheetPaintsNoPolarityItCannotKnow` fails on
`CtaKind.APPROVE`, `CtaKind.DENY` or `denyChip(` in the sheet's composition.

---

## 3. RED, verbatim

New fence: `android/gate/i1_sheetandwell_test.go`. Written before any implementation, run from the
worktree root:

```
$ go test -C android/gate -run 'TestADR009_|TestPBDS6_EveryClaimedScreenExists' ./...
--- FAIL: TestADR009_TheApprovalSheetReadsTheApprovalRequestItem (0.01s)
    i1_sheetandwell_test.go:63: ADR-009: dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt still records "THERE IS NO APPROVE VERB". The wire now carries the prompt, the action and the decisions (interaction-schema.md §3.5), so that paragraph documents a gap this slice filled -- and a stale refusal reads to the next agent as a rule.
    i1_sheetandwell_test.go:63: ADR-009: dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt still records "Nothing on this wire carries the literal". The wire now carries the prompt, the action and the decisions (interaction-schema.md §3.5), so that paragraph documents a gap this slice filled -- and a stale refusal reads to the next agent as a rule.
    i1_sheetandwell_test.go:63: ADR-009: dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt still records "`swarmmobile.Session.Need` is". The wire now carries the prompt, the action and the decisions (interaction-schema.md §3.5), so that paragraph documents a gap this slice filled -- and a stale refusal reads to the next agent as a rule.
    i1_sheetandwell_test.go:75: ADR-009: dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt declares no `val actions:`. The sheet's three contents are the item's summary, its action's literal and its decisions' labels.
    i1_sheetandwell_test.go:84: ADR-009: dev/swarm/phone/ui/screens/ApprovalSheetPanel.kt still takes its question from `row.need`, the verbatim journal record type. §3.5's `summary` is the adapter's own one-line headline and is what a blocking question reads as.
--- FAIL: TestADR009_TheItemIsDecodedOutsideTheScreen (0.00s)
    i1_sheetandwell_test.go:97: ADR-009: cannot read dev/swarm/phone/ui/ApprovalItem.kt: open /Users/Nathan/Code/swarm/.claude/worktrees/interaction-program/android/app/src/main/kotlin/dev/swarm/phone/ui/ApprovalItem.kt: no such file or directory
--- FAIL: TestADR009_TheSheetPaintsNoPolarityItCannotKnow (0.00s)
    i1_sheetandwell_test.go:126: ADR-009: cannot read dev/swarm/phone/ui/screens/ApprovalSheetView.kt: open /Users/Nathan/Code/swarm/.claude/worktrees/interaction-program/android/app/src/main/kotlin/dev/swarm/phone/ui/screens/ApprovalSheetView.kt: no such file or directory
--- FAIL: TestADR009_TheTerminalWellIsDeletedAtI1Exit (0.06s)
    i1_sheetandwell_test.go:166: ADR-009 (3): dev/swarm/phone/ui/screens/PeekPanel.kt is still in the app. The peek screen is deleted with the well, not merely left uncomposed -- a screen nothing reaches is how the grid comes back.
    i1_sheetandwell_test.go:166: ADR-009 (3): dev/swarm/phone/ui/screens/PeekPanelView.kt is still in the app. The peek screen is deleted with the well, not merely left uncomposed -- a screen nothing reaches is how the grid comes back.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: calls terminalPeek. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: calls terminalUnwatch. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: calls terminalWatch. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: names `PeekPanelScreen`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: names `PeekPanel`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: names `peekHost`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/PhoneSurface.kt: names `peekPanelView`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/FacadeBridge.kt: names `TerminalPeek`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/SessionScreens.kt: names `TerminalPeek`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/screens/PeekPanel.kt: names `PeekPanelScreen`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/screens/PeekPanel.kt: names `PeekPanel`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/screens/PeekPanel.kt: names `TerminalPeek`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/screens/PeekPanelView.kt: names `PeekPanel`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
    i1_sheetandwell_test.go:188: ADR-009: dev/swarm/phone/ui/screens/PeekPanelView.kt: names `peekPanelView`. (1) leaves no raw grid anywhere in the app and (2) leaves no phone surface issuing a terminal_watch; the well's hosting path goes with the well.
--- FAIL: TestADR009_NoScreenRendersTheTerminalVariantOfTheWell (0.01s)
    i1_sheetandwell_test.go:205: ADR-009 (1): dev/swarm/phone/ui/screens/SessionDetailView.kt prints the daemon-rendered grid in the terminal well. There is no terminal emulation and no raw grid anywhere in the app; the transcript is the session surface.
    i1_sheetandwell_test.go:205: ADR-009 (1): dev/swarm/phone/ui/screens/PeekPanelView.kt prints the daemon-rendered grid in the terminal well. There is no terminal emulation and no raw grid anywhere in the app; the transcript is the session surface.
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	0.959s
FAIL
```

`TestPBDS6_EveryClaimedScreenExists` passed on this run and **that is the correct RED for it**: every
claim in `s24ScreenComponents` still named a file that existed. It went red the moment `PeekPanelView.kt`
was deleted, which is the whole reason it was written — see §4.

---

## 4. The amended assertions — before and after

Four fences asserted things this decision removes. **None was deleted.** Each is amended, in place,
with the reason recorded beside it. The rule followed throughout: *never drop an assertion about a
capability that still exists; move it to where the capability now lives.*

### 4.1 `s24_screens_test.go` — the WELL rows (`s24ScreenComponents`)

**Before.** Two screens were required to call `monoWell`:

```go
"dev/swarm/phone/ui/screens/SessionDetailView.kt": {
    "navHeaderDrill": "C2.1 `.navhead` -- the chevron and the session it names, per §4",
    "monoWell":       "C2.2 `.term` -- the daemon-rendered grid, reused from C3",
    "sectionLabel":   "C2.3 `.plabel` -- the heading over the session's own journal",
    "activityRow":    "C2.3 -- one record, derivation row 14 reused from the activity feed",
    "sessionList":    "C2.3 `.prows` -- the rows' container, carrying the gap and side padding",
    "emptyState":     "derivation row 8 -- a heading over no records is a section that lies",
},
"dev/swarm/phone/ui/screens/PeekPanelView.kt": {
    "navHeaderDrill": "C3.1 `.navhead` -- the session title, per §4, with no destination",
    "monoWell":       "C3.2 `.term` -- the escape-filtered VT snapshot, in `terminal_peek.fg`",
    "readOnlyNote":   "C3.3 `.ro-note` -- derivation row 22, and PB-INPUT-2's lease sentence",
},
```

**After.** The `PeekPanelView.kt` entry is gone with its file. `SessionDetailView.kt` keeps
`navHeaderDrill` alone — its `monoWell` requirement is **void, not unmet**, and its four journal
components **moved whole** to `TranscriptView.kt`, which is now claimed. `ApprovalSheetView.kt` is
claimed with `monoWell` on it, so the reuse rule the WELL row existed to enforce is still enforced —
on the one screen that still renders a machine-authored literal.

```go
"dev/swarm/phone/ui/screens/SessionDetailView.kt": {
    "navHeaderDrill": "C2.1 `.navhead` -- the chevron and the session it names, per §4",
},
"dev/swarm/phone/ui/screens/TranscriptView.kt": {
    "sectionLabel": "...", "sessionList": "...", "activityRow": "...",
    "monoWell":     "a tool's output and a file's diff -- §2's one factory for every mono block",
    "emptyState":   "...",
},
"dev/swarm/phone/ui/screens/ApprovalSheetView.kt": {
    "approvalSheet": "D4.4's sheet -- the heaviest material in the app, for the moment of decision",
    "monoWell":      "the literal the decision is about, §5's one mono block spent again",
    "ctaButton":     "one per `decisions[].label`, every one `.a2-more` -- IS-APR-4 keeps polarity machine-side",
},
```

**And the map gained the direction it never had.** `s24ScreenComponents` is checked by iterating the
screens that *exist*, so a claim for a deleted file is silently skipped — the same vacuous pass its own
comment records about `SessionDetailView.kt` (*"the screen passed because nothing was asked of it,
which reads identical to passing"*), arrived at from the other side. `TestPBDS6_EveryClaimedScreenExists`
closes it, and it is what caught the stale `PeekPanelView.kt` claim in this very slice.

### 4.2 `pbapp6_pbinput2_surface_test.go` — PB-INPUT-2 survives its screen

The requirement's content is unchanged; only its host moved. `FacadeBridge.terminalPeek` →
`sessionLease`, `TerminalPeek.keyboardEnabled` / `.showsTakeControl` → `SessionLease.*`. The two
assertions still hold: the lease may not be a boolean literal at the call site, and production Kotlin
must *read* `keyboardEnabled` and one of `showsTakeControl`/`showsRelease`. The `&& online` argument
is quoted verbatim in the amended file, because that clause is why the model exists at all.

### 4.3 `0qe7_snapshotstale_test.go` — a stale mark comes from the stream it is about

Before: `detailPanel` had to read `grid.stale` off the same `terminalPeek` read the grid came on, so a
frozen grid could not be drawn under a healthy journal's verdict. The grid is gone, so the *second
stream* is gone — and the rule survives its subject: the stale mark must be the transcript read's own
(`chat.stale`, the journal mark on the handle that carried the items, IS-LAYER-1/-4) rather than a
second `journal(...)` read that can disagree with it.

### 4.4 `s18_sec4_windowsecurity_test.go` + `window-security.tsv` — B65's answer follows its screen

`window-security.tsv`'s `terminal_peek` row is deleted with the screen. Its **argument is not**: it
was the row that carried the specific cost of allowing screenshots on session content, and that
content is now on the session detail. So the `session_detail` row absorbs the peek's paragraph
verbatim (*"the layers below survive device loss … a screenshot needs an adversary already executing on
the unlocked handset"*), and `TestPBSEC4_ThePolicyStillCoversTheTwoScreensTheRequirementNamed` now
requires a row for `session detail` where it required one for `terminal peek`. Dropping the role would
have dropped B65's answer along with the question.

### 4.5 `s25_mainthread_test.go` — a ledger row that left with its debt

`s25RenderPathExemptions` held two rows: `PhoneSurface.watch` and `.unwatch`, both
`agents-tracker-jx1x` (a waiting verb reached from the looper). Neither was fixed — **the call sites
were deleted** by ADR-009 (2). Recorded in the file in as many words, because a row leaving this way
looks identical to a row being paid off, and only one of the two is an improvement in the code.

### 4.6 The Kotlin suites — every assertion MOVED, DELETED or KEPT, by name

The shape change reached six Kotlin suites. This is the whole ledger, because the failure mode an
amendment invites is that a deleted assertion and a weakened one look identical in a diff. **Nothing
below was changed to make a test pass**; each row is either a subject the ADR retires, or a subject
that survived and needed a new home.

**DELETED — the subject no longer exists anywhere in the app.** ADR-009 (1)/(3) leave "no terminal
emulation and no raw grid anywhere", so an assertion about a grid has nothing to be about:

| Was | In |
| --- | --- |
| `the grid is the daemon-rendered text verbatim` | `SessionScreensTest.TerminalPeekTest` |
| `a stale grid is banner-marked and the keyboard stays available` | `SessionScreensTest.TerminalPeekTest` |
| `a fresh snapshot is the grid and nothing else` | `PeekPanelScreenTest` |
| `a stale snapshot is the grid, with the model's own banner beside it` | `PeekPanelScreenTest` |
| `a stale snapshot does not shut the keyboard` | `PeekPanelScreenTest` |
| `the note is C3's recorded first line` | `PeekPanelScreenTest` |
| `the peek names no destination, because it is not a drill-down` | `PeekPanelScreenTest` |
| `the peek is composed of the kit components C3 names` | `PeekPanelViewTest` |
| `the header is the drill-down header, and it offers no back control` | `PeekPanelViewTest` |
| `the composition is in C3's order` | `PeekPanelViewTest` |
| `the stale banner is a notice above the well, not a line inside the grid` | `PeekPanelViewTest` |
| `a fresh snapshot draws no stale notice at all` | `PeekPanelViewTest` |
| `what this slice has not recomposed is hosted under the panel` | `PeekPanelViewTest` |
| `the snapshot carries its own stale mark and not the journal's verdict` | `SessionDetailPanelTest` |
| `the snapshot card is absent when no frame has arrived, not blank` | `SessionDetailPanelTest` |
| `no snapshot means no card at all` | `SessionDetailViewTest` |
| `a stale snapshot is marked beside its card, not inside the grid` | `SessionDetailViewTest` |
| `the answer carries no grid, and claims nothing about one` | `FacadeBridgeTest` |

The last row is the one worth naming twice: it asserted `FacadeBridge.noFrameYet`, the fabricated
empty-grid fallback. The *interpretation* it depended on is untouched — `isAwaitingFirstFrame` now
absorbs the same not-found refusal for `pendingApproval` — and that predicate's four tests are
unchanged, so what went is the fabricator, not the rule.

**MOVED — the subject survives, so the assertion does.** ADR-009 (5) keeps the input substrate
"exactly as decided", PB-INPUT-2 is untouched, and the peek held the lease copy only because the peek
was where the keyboard was. `leaseNoticeFor` moved from `PeekPanelScreen` to `SessionDetailScreen`
intact, and its coverage followed:

| Assertion | From | To |
| --- | --- | --- |
| `take control is offered exactly while the machine has not confirmed one` | `PeekPanelScreenTest` | `SessionDetailPanelTest` |
| `the two lease sentences go with the two lease states and not the other way round` | `PeekPanelScreenTest` | `SessionDetailPanelTest` |
| `the keyboard follows both of the model's clauses, not just the lease` | `PeekPanelScreenTest` | `SessionScreensTest.SessionLeaseTest` |
| `take control is on screen exactly while the model offers it` | `PeekPanelViewTest` | `SessionDetailViewTest` |
| `the control on screen is the one the surface supplied` | `PeekPanelViewTest` | `SessionDetailViewTest` |
| `a control re-composed after a redraw is not refused for having a parent` | `PeekPanelViewTest` | `SessionDetailViewTest` |
| `the lease sentence on screen is the one the model chose for that state` | `PeekPanelViewTest` | `SessionDetailViewTest` |
| all five of `PeekPanelLeaseVerdictTest` | `PeekPanelLeaseVerdictTest` | `SessionDetailLeaseVerdictTest` (new file) |
| `the lease is acquired and released explicitly` | `SessionScreensTest.TerminalPeekTest` | `SessionScreensTest.SessionLeaseTest` |
| `the transcript is the session's own journal, newest first, verbatim` | `SessionDetailPanelTest` | `TranscriptPanelTest` — `the conversation is oldest first, in the order it was said` |
| `a session with no records says so rather than showing an empty area` | `SessionDetailPanelTest` | `TranscriptPanelTest` |
| `the transcript renders one row per record, newest first, in the wire's words` | `SessionDetailViewTest` | `TranscriptViewTest` — `every block is drawn, in the order the panel put them in` |
| `a session with no records draws its copy, not an empty area` | `SessionDetailViewTest` | `TranscriptViewTest` |

The last four rows are the ones ADR-009 (1) deliberately **changes the content of** while keeping the
property: the old pair asserted journal records NEWEST first, the new pair asserts interaction items
OLDEST first, because a conversation is read in the order it was said. That is the ruling landing, not
an assertion weakening — and it is stated here because a reader comparing the two suites would
otherwise see an ordering flip with no record of who authorised it.

**ADDED — properties that did not exist before this slice could be got wrong:**

| Assertion | In |
| --- | --- |
| `the conversation the screen is handed is the conversation it carries` (`assertSame` — no second fold) | `SessionDetailPanelTest` |
| `an approval block tapped on this screen reaches the caller with its item id` | `SessionDetailViewTest` |
| `a screen given no destination for an approval draws the block anyway` | `SessionDetailViewTest` |

The last two are the seam neither neighbouring suite can see: `TranscriptViewTest` hands
`transcriptView` a listener directly and proves the block calls it, which says nothing about whether
the session detail passed one **down**. A screen that dropped the argument renders an approval that
looks identical and does nothing — the dead-chevron defect (`agents-tracker-2yb`) one surface over.

**Two stale citations fixed, no assertion changed.** `PhoneSurfaceEventPathGuardTest`'s two failure
messages named `FacadeBridge.terminalPeek` as the exemplar guard, and `ScaffoldPairAgainTest`'s named
`PeekPanelScreen` as the record for why a dead control is worse than a gap. Both name deleted code as
live authority; both now name the surviving one (`FacadeBridge.pendingApproval`, `transcriptView`'s
`onApproval`). The `guarded(...)` scans themselves are untouched — they assert `.journal(` and
`.triageInbox(`, both of which still exist.

---

## 5. GREEN

```
$ go test -C android/gate -v -run 'TestADR009_|TestPBDS6_EveryClaimedScreenExists' ./...
--- PASS: TestADR009_TheApprovalSheetReadsTheApprovalRequestItem (0.00s)
--- PASS: TestADR009_TheItemIsDecodedOutsideTheScreen (0.00s)
--- PASS: TestADR009_TheSheetPaintsNoPolarityItCannotKnow (0.00s)
--- PASS: TestADR009_TheTerminalWellIsDeletedAtI1Exit (0.07s)
--- PASS: TestADR009_NoScreenRendersTheTerminalVariantOfTheWell (0.01s)
--- PASS: TestPBDS6_EveryClaimedScreenExists (0.00s)
PASS
ok  	github.com/Nathandela/swarm/android/gate	0.816s

$ go test -C android/gate ./...
ok  	github.com/Nathandela/swarm/android/gate	7.250s

$ go build ./...                                   clean
$ go vet ./android/gate/                           clean
```

The whole `android/gate` suite is green, including the four amended fences above and the eleven other
files that assert over the same sources.

### 5.1 The Robolectric suite — RUN, and proved to have actually run

```
$ . ./android/toolchain.env
$ ./android/gradlew -p ./android --no-daemon --rerun-tasks --no-build-cache :app:testDebugUnitTest

> Task :app:compileDebugKotlin
> Task :app:compileDebugUnitTestKotlin
> Task :app:testDebugUnitTest
BUILD SUCCESSFUL in 4m 1s
31 actionable tasks: 31 executed
```

**130 suites, 1056 tests, 0 failures, 0 errors, 0 skipped.**

`agents-tracker-4ok` is why that sentence is not the whole of the evidence: after a build collision
`compileDebugUnitTestKotlin` can restore FROM-CACHE with an empty output, `testDebugUnitTest` then
reports `NO-SOURCE`, and the run exits 0 having executed nothing. Three things were checked so that
`NO-SOURCE` could not read as success here:

1. **The tasks executed.** `31 actionable tasks: 31 executed` — no `FROM-CACHE`, no `UP-TO-DATE` on
   either compile task or the test task. `--rerun-tasks --no-build-cache` is what forces it.
2. **The count is nonzero and was parsed from the XML**, not from Gradle's exit code: every
   `TEST-*.xml`'s `tests`/`failures`/`errors` attributes summed independently.
3. **No result is stale.** Every one of the 130 XMLs has mtime `13:42:48`, and the newest `.kt` edit
   in the module is `13:31:17` — so all 130 postdate the source they are about. Zero stale suites were
   found, which matters because a leftover XML from a dead run is a result for source that no longer
   exists.

**THE RUN IS COMPROMISED, AND THE CHECKS BELOW DID NOT CATCH IT.** `android/app/libs/swarm.aar` has
mtime **13:40:29** — INTEGRATION's gomobile rebuild — and this build ran from roughly 13:38:50 to
13:42:50. **A dependency this build compiles against was replaced about ninety seconds after it
started.** Gradle snapshots a task's inputs when that task begins, so `compileDebugKotlin` and the
test runtime classpath did not necessarily see the same AAR, and nothing in the three checks below
looks at dependencies at all: they compare result XMLs against *source*, which is one input of
several. The freshness discipline was applied on one axis and the run was broken on another.

What this does and does not invalidate, kept separate because the difference is the honest part.
Every suite named below is pure-Kotlin — screen models and Robolectric view tests — and none of them
constructs a `swarmmobile` type at runtime (`FacadeBridgeTest`'s own KDoc records why: the gomobile
class "CANNOT BE CONSTRUCTED on the unit-test JVM"). So the *assertions* are very unlikely to have
been affected. But "very unlikely" is not what a verification document is for, and the run cannot be
claimed as coherent. **It is superseded**: see §5.3.

**THE RUN IS ALSO A SNAPSHOT AT 13:42, AND ONE SUITE LANDED AFTER IT.**
`ui/screens/TranscriptScreenGoldenTest.kt` has mtime 13:56 — fourteen minutes later, from the
INTEGRATION workpackage — so the 130 XMLs above **do not include it** and "1056" is not a count of
the module as it now stands. That is the same freshness argument as check 3, pointed the other way:
results newer than their source proves nothing about source newer than the results. INTEGRATION has
since run it in an exclusive lane (`TranscriptPanelTest` 21, `TranscriptScreenGoldenTest` 6,
`TranscriptViewTest` 7 — 34 tests, 0 failures, all XMLs written by that run after clearing the
directory), so the suite is covered; it is covered by a **different run**, and this section does not
claim it.

The safe idiom from that issue was followed exactly: **no deletion** of `app/build/test-results` (that
is what kills a concurrent run and was misattributed to memory pressure twice), and a live-build guard
before starting.

The guard used is `pgrep -f gradle-wrapper.jar` plus `pgrep -f GradleWorkerMain`, rather than the
`pgrep -x java` that `-4ok` prescribes. **This was arrived at independently and then found to be
already recorded**, which is worth saying plainly rather than presenting as a finding:
`agents-tracker-6qi` states it in as many words — "KotlinCompileDaemon lingers 7200s with gradle paths
in its command line so both `pgrep -x java` and `pgrep -f gradle` report busy when nothing builds. The
only reliable distinguisher is `pgrep -f gradle-wrapper.jar` = active build." The reasoning here
(daemons persist by design and write nothing to `test-results`, so `-x java` never clears on this host
and becomes a permanent refusal) is a rederivation of that note, not an addition to it. The lesson is
about the ledger rather than the guard: the answer was already written down, and two sessions spent
effort re-finding it.

The suites this slice touched, all green:

| Suite | Tests |
| --- | --- |
| `ui.screens.SessionDetailViewTest` | 14 |
| `ui.screens.TranscriptPanelTest` | 21 |
| `ui.screens.SessionDetailPanelTest` | 8 |
| `ui.screens.ApprovalSheetPanelTest` | 8 |
| `ui.screens.TranscriptViewTest` | 7 |
| `ui.kit.ApprovalSheetTest` | 7 |
| `ui.SessionDetailTest` | 6 |
| `ui.screens.SessionDetailLeaseVerdictTest` | 5 |
| `ui.screens.SessionDetailKillVerdictTest` | 5 |
| `ui.SessionStopOfflineTest` | 5 |
| `ui.screens.ScaffoldPairAgainTest` | 5 |
| `PhoneSurfaceEventPathGuardTest` | 5 |
| `ui.FacadeBridgeTest` | 4 |
| `ui.SessionLeaseTest` | 2 |

### 5.3 The authoritative run — exclusive lane, settled AAR, whole module

§5.1 is superseded by this. Taken at 14:33:57 local in a lane confirmed empty, with
`test-results/testDebugUnitTest` removed first — a deletion that is safe **only** because the lane was
empty, which is the distinction `agents-tracker-6qi` draws (deleting it out from under a concurrent
run is what destroys both).

```
AAR mtime before: 13:40:29
run started 12:33:57Z
> Task :app:policyTestResources
> Task :app:compileDebugKotlin
> Task :app:compileDebugUnitTestKotlin
> Task :app:testDebugUnitTest
BUILD SUCCESSFUL in 4m 9s
31 actionable tasks: 31 executed
run ended 12:38:07Z
AAR mtime after:  13:40:29
```

**131 suites, 1062 tests, 0 failures, 0 errors, 0 skipped.**

It closes the three defects in §5.1, each by construction rather than by assertion:

1. **The AAR did not move.** Printed before and after: `13:40:29` both times, roughly fifty minutes
   stale by the time the run began. This is the check §5.1 lacked entirely — it compared results
   against *source* and never looked at dependencies.
2. **Nothing is a snapshot.** 131 suites rather than 130: `TranscriptScreenGoldenTest` is included,
   and 1062 is exactly 1056 + its 6. Every suite in the module ran.
3. **Nothing can be stale.** The directory was empty when the run started, so every XML in it was
   written by this run — a stronger guarantee than the mtime comparison, which can only detect
   staleness it thinks to look for.

`policyTestResources` **executed** rather than reporting `UP-TO-DATE`, which is the signal the
integration appendix's §7.2 warns about: had it been cached, the golden JSON would not be on the
unit-test classpath and `TranscriptScreenGoldenTest`'s 6 assertions would have been passing over
nothing. `--rerun-tasks` is what forces it; the hash comparison in that appendix is the other way to
prove it, and the two agree.

### 5.2 The RED that is MISSING, named rather than skipped

**The nine assertions §4.6 lists as MOVED or ADDED were green on their first execution, and GG-5 asks
for a failing-first run.** For the MOVED ones the original RED is real but historical — it was earned
in the peek suites, against the peek. For the three ADDED ones there is no RED at all.

Three one-line negative controls were prepared and applied (`transcript = transcript.copy()` to break
the pass-through; `transcriptView(..., null)` to break the approval tap; dropping `verdict` from
`leaseNoticeFor` to break the verdict suite). **The run was abandoned and the perturbations reverted
byte-for-byte, because five other agents' Gradle launchers were live at that moment.** Perturbing
shared production source while four other sessions are compiling it would have fed them a defect that
is not in the code and invited them to act on it — a worse outcome than a missing RED. The
perturbations are reverted (`grep` confirms no marker survives and all three original lines are back).

**That last clause originally read "the script is ready to run when the build lane is free", and the
condition was wrong** — but so was its first correction. This paragraph has now been wrong twice, and
both versions are recorded because the second error is the instructive one:

1. *"when the build lane is free"* — the lane was necessary but incidental.
2. *"what made it safe was the clean tree"* — better, and still wrong. It licenses perturbing shared
   source whenever `git status` is empty.

The project's standing answer, recorded 2026-08-03 and found only after both of the above were
written, is that a destructive negative control **must not touch the shared working tree at all** —
perturb in memory inside the test, or use a scratch worktree. **§5.2.2 is that correction and
supersedes this paragraph.** Declining to run these controls on a dirty tree was right; the reason was
neither the lane nor the revert path, but that in-place perturbation of a shared checkout is the wrong
method whatever the tree looks like.

What can be said WITHOUT that run is structural, and is weaker on purpose: none of the three added
assertions can pass vacuously. `kitRequire(TranscriptTag.APPROVAL)` throws when the block is absent;
`assertEquals(1, allTagged(DetailTag.TAKE_CONTROL).size)` fails on both zero and two;
`assertSame` fails on any object that is not the one handed in; and the tap test compares against a
`var` initialised to `null`, so a listener that is never called leaves it null. That rules out the
vacuous-pass class. It does not replace watching each one fail for its own reason.

#### 5.2.1 The run was taken. RESOLVED — recorded by A-TRANSCRIPT, 2026-08-08

The three prepared controls were applied and run by the neighbouring workpackage once the lane was
free and the tree was clean at `f81975f`. **The clean tree is what made it safe**, and it is the
condition this section should have named rather than "the lane is free": with every path committed,
`git checkout --` is a byte-exact revert that needs no discipline to get right, so the perturbation
cannot outlive the run even if the run is killed. The script reverted from a shell `trap ... EXIT`
for that reason, and the trap was exercised for real — the first attempt died on `exit 127` (the
wrapper is `android/gradlew`, not `./gradlew` from the repo root) and reverted anyway.

Applied together, one line each:

| Perturbation | Site |
|---|---|
| `transcript = transcript` → `transcript = transcript.copy()` | `SessionDetailPanel.kt:250` |
| `leaseNoticeFor(lease.showsRelease, verdict)` → `leaseNoticeFor(lease.showsRelease)` | `SessionDetailPanel.kt:254` |
| `transcriptView(context, panel.transcript, onApproval)` → `…, null)` | `SessionDetailView.kt:223` |

```
SessionDetailPanelTest       > the conversation the screen is handed is the conversation it carries          FAILED
SessionDetailViewTest        > an approval block tapped on this screen reaches the caller with its item id    FAILED
SessionDetailLeaseVerdictTest> a refused take control says the machine refused it, in the machine's own words FAILED
SessionDetailLeaseVerdictTest> a severed lease is reported as ended and not as a refusal                      FAILED

SessionDetailPanelTest.xml        tests="8"  failures="1"
SessionDetailViewTest.xml         tests="14" failures="1"
SessionDetailLeaseVerdictTest.xml tests="5"  failures="2"
```

Reverted, then the same three suites re-run on the restored tree: **27 tests, 0 failures**,
`git status` clean at the same `f81975f`. Same suites, same counts, four failures and zero — so the
failures are the perturbations and nothing else.

**Three perturbations produced FOUR failures, and the extra one is the useful part.** Dropping
`verdict` collapses two distinct branches at once — `verdict.result == ENDED` and `verdict.refused`
— and the suite reports them separately, in the words that make them different remedies: *"the
screen dropped the machine's reason for refusing control, so the user is left to guess between a
kill switch, a revoked device and a policy"*, and *"PB-INPUT-2: a lease that died is not visibly
reported at all, so the phone types into a void with the keyboard shut and no sentence saying why"*.
A single failure there would have meant the two clauses were not independently asserted.

The pass-through control is the one that reads as pedantic and is not: its message is *"the session
detail rebuilt the transcript it was handed, so the conversation on this screen is a second fold of
the same items — and only one of the two is the one `TranscriptScreen` decided"*. `.copy()` produces
an EQUAL object, so an `assertEquals` would have stayed green through it. `assertSame` is doing the
work.

#### 5.2.2 The METHOD was wrong, and the project had already said so

**A standing team norm forbids what §5.2.1 did, and neither workpackage checked for one.**
`bd memories "destructive negative controls"` returns
`team-norm-for-shared-worktree-agent-fleets-learned`, recorded **2026-08-03**, five days before this
slice:

> DESTRUCTIVE NEGATIVE CONTROLS MUST NEVER TOUCH THE SHARED WORKING TREE. Every gate in this repo
> demands a negative control (prove the check can fail), and the obvious method — perturb the source
> file in place, run the test, copy it back — is actively hazardous when several agents share one
> checkout.

It names the cost in a previous session: an agent perturbed `PairingSurface.kt:192` in place, and
during that window another agent's grep and full Gradle run "saw a source state that existed in NO
COMMIT", which burned a verification pass and then a reconciliation effort. It gives the correct
methods in preference order: **(1) perturb IN MEMORY inside the test** — `strings.Replace` on the
source text, never a write, which `android/gate/guidedpairing_test.go:143` already does and four
other gate files besides; **(2) a scratch git worktree or clone**; **(3) never in place.**

§5.2.1 used method (3). The clean tree and the `trap ... EXIT` made it *recoverable*, and the hazard
did not materialise — but recoverable is not the test the norm sets, and "the tree was clean" is not
one of the three answers. **So the conclusion §5.2 reached — that the clean tree is the enabling
condition — is wrong as guidance**, and it is wrong in the direction that invites repetition: a
future reader would take it as licence to perturb shared source whenever `git status` is empty. The
enabling condition the project actually recorded is *not touching the shared tree at all*.

What this does **not** change: the four failures in §5.2.1 are real, and what they establish about the
assertions stands. A wrong method that happened to run cleanly still produced correct evidence. The
correction is to the method and to the rule extracted from it, not to the result.

**This is the fifth thing in one session that was already written down** — after `6qi`'s lane guard,
`6qi`'s two false-failure signatures, and `180i`. It is the only one of the five where the standing
guidance would have changed what we DID rather than merely saved us the trouble of rederiving it.

**What this closes and what it does not.** GG-5 asks for a failing-first run, and this is not that —
it is a failing-after run, which demonstrates that the assertions BITE but not that they were
written before the code. For the three ADDED assertions the distinction is real and §5.2 above
should keep saying so. What is now gone is the weaker worry, that nine green-on-first-execution
assertions might be green because they check nothing.

---

## 6. What is NOT verified here, stated plainly

**CORRECTION — an earlier revision of this section claimed the Robolectric suite "could not be run"
because "this machine has no Android SDK".** That was WRONG, and it was wrong in the exact way
`android/toolchain.env` predicts in its own header: "nothing Android is discoverable on this host by
default: no `ANDROID_*` variable is exported, nothing Android is on PATH, `/usr/libexec/java_home`
reports no JVM ... A fresh shell that has not sourced this file concludes there is no Android
toolchain at all, which is wrong and has already cost three readers." This slice was reader four. The
toolchain is present and pinned — JDK 21.0.12 at `/usr/local/opt/openjdk@21`, SDK at
`/usr/local/share/android-commandlinetools` with `android-36` installed — and reachable with one
line:

```
. ./android/toolchain.env
```

The check that produced the false claim (`java -version`, `ls ~/Library/Android/sdk`) tested an
unsourced shell, which is a fact about the shell and not about the machine. **No verification claim in
this document may rest on "the tooling is unavailable" again without sourcing that file first.** The
run is recorded in §5.1.

Consequences, each named rather than absorbed:

1. ~~`ApprovalSheetPanelTest.kt` is rewritten but unrun.~~ **RUN AND GREEN** — 8 tests, §5.1. Amended
   assertion-for-assertion against the new model (summary rather than `need`, the action's literal
   rather than the snapshot, `decisions[].label` in wire order, the prompt-card mode taking the same
   sheet, and the two ids an answer has to name).
2. ~~The Kotlin suites are amended but unrun.~~ **COMPILED AND GREEN** — `compileDebugUnitTestKotlin`
   executed clean and all six ran (§4.6 is the amendment ledger, §5.1 the counts):
   `ui/SessionScreensTest.kt`, `ui/SessionStopOfflineTest.kt`, `ui/FacadeBridgeTest.kt`,
   `ui/screens/SessionDetailPanelTest.kt`, `ui/screens/SessionDetailViewTest.kt`, and the new
   `ui/screens/SessionDetailLeaseVerdictTest.kt`. The static scan that stood in for a compile before
   the run (every `.kt` under `android/app/src`, comments and string literals stripped, for
   `noFrameYet`, `TerminalPeek`, `PeekPanel*`, `snapshotText`, `snapshotStale`, `hasSnapshot*`,
   `DetailTag.SNAPSHOT*`/`.SECTION_LABEL`/`.ROW`/`.EMPTY`, `SessionDetail.journal`,
   `terminalWatch`/`terminalUnwatch`/`terminalPeek`, `PeekTag` — zero hits) agreed with the compiler,
   which is worth one line: it was a sound proxy for THIS class of breakage and would still have
   missed a type error inside a lambda. It is not a substitute and was not treated as one.
   `androidTest/PhoneScreenDriver.kt` names `TerminalPeek.keyboardEnabled` in **prose only** and is
   instrumented (never built by the unit-test task); it is left alone deliberately.
3. **The composition depends on a sibling workpackage.** `PhoneSurface.detailPanel` calls
   `bridge.transcript(...)` and `TranscriptScreen.of(...)`, and `sessionDetailView` places
   `transcriptView(...)`. Those three are A-TRANSCRIPT's files at the signatures it published; the
   `s24ScreenComponents` row for `TranscriptView.kt` is the claim over them.
4. **The approval sheet has a caller now, but no screenshot.** `PhoneSurface.approvalHost` composes it
   where `peekHost` used to sit, and the transcript's approval block navigates to it
   (`onApproval` → `openApproval`). O6's "no production caller" gap is closed in source; that it draws
   correctly on a handset is unverified on this machine.

---

## 7. Files touched

**Deleted:** `ui/screens/PeekPanel.kt`, `ui/screens/PeekPanelView.kt`,
`test/.../PeekPanelViewTest.kt`, `PeekPanelScreenTest.kt`, `PeekPanelLeaseVerdictTest.kt`.

**Added:** `android/gate/i1_sheetandwell_test.go`, `ui/screens/ApprovalSheetView.kt`,
`ui/ApprovalItem.kt`, `test/.../ui/screens/SessionDetailLeaseVerdictTest.kt`.

**Amended:** `ui/screens/ApprovalSheetPanel.kt`, `ui/screens/SessionDetailPanel.kt`,
`ui/screens/SessionDetailView.kt`, `ui/SessionScreens.kt`, `ui/FacadeBridge.kt`, `PhoneSurface.kt`,
`test/.../ApprovalSheetPanelTest.kt`, `test/.../ui/SessionScreensTest.kt`,
`test/.../ui/SessionStopOfflineTest.kt`, `test/.../ui/FacadeBridgeTest.kt`,
`test/.../ui/screens/SessionDetailPanelTest.kt`, `test/.../ui/screens/SessionDetailViewTest.kt`,
`test/.../PhoneSurfaceEventPathGuardTest.kt` (citation only),
`test/.../ui/screens/ScaffoldPairAgainTest.kt` (citation only), `android/gate/s24_screens_test.go`,
`android/gate/pbapp6_pbinput2_surface_test.go`, `android/gate/0qe7_snapshotstale_test.go`,
`android/gate/s18_sec4_windowsecurity_test.go`, `android/gate/s25_mainthread_test.go`,
`android/window-security.tsv`, `android/unbound-verbs.tsv`.

**Ledger rows** (`android/unbound-verbs.tsv`): `App.Peek`, `App.TerminalWatch` and
`App.TerminalUnwatch` are now unbound — the first rows in that file that are a **retired screen**
rather than a missing one.
