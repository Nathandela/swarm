# ADR-012: Type ladder consolidation — phase 1 (safe merges) and phase 2 (the ruled ladder)

**Status**: Accepted. Phase 1 (T1–T5) took the two merges that moved no pixel. Phase 2 (P1–P8, below) implements the owner rulings of 2026-08-09 and is where every question phase 1 refused to answer is answered. **Amended 2026-08-27 by [ADR-020](ADR-020-slate-palette-and-breathing-scale.md) D3: P10 (ruling R9) shifts all five rungs one step up; the rung table under P10 is the machine-read one.**
**Date**: 2026-08-09 (phase 1), amended the same day with phase 2
**Filename**: still `...-phase-1.md`. The path is cited from `type.xml`, from `android/gate/s22b_type_test.go`, from the Robolectric suite and from three beads; renaming the file to match its contents would break four joins to save one word. The title above is the record of what it is.
**Amends**: [ADR-009](ADR-009-obsidian-visual-direction.md) D7's parenthetical "Type scale structure (19 styles, ...) is unchanged" — the count becomes 17. D7's decisions themselves are untouched: no size, weight, tracking, family or feature string moves here.
**Companions**: `android/app/src/main/res/values/type.xml` (the scale), `android/gate/s22b_type_test.go` (the join), `docs/research/remote-control-design-directions.html` (the design source), bead `agents-tracker-v6sa` (the maquette-vs-scale reconciliation this ADR routes its open questions to).

## Context

The field test of 0.3.0 reported "fonts not well mastered" (`agents-tracker-ksvb`). Counting the
scale explains the report structurally rather than as taste.

`res/values/type.xml` ships **19 named styles across 13 sizes**. Eleven of those thirteen sizes sit
inside one six-point band — 9.5, 10, 10.5, 11, 11.5, 12, 12.5, 13, 13.5, 14, 15.5 — with every
half-step occupied. A reader meets 10 sp and 10.5 sp on the same screen and cannot tell a hierarchy
from an inconsistency, because at that spacing there is no difference to tell. The inbox alone
renders eleven distinct styles.

Three findings fall out of the count, and only the first two can be acted on without deciding
something about how the app should look. This ADR takes those two and states the rest as
questions.

### The bundled family answers 400 and 500, and five mono styles ask for 600

ADR-009 D7 bundles two static faces of JetBrains Mono (`res/font/jetbrains_mono.xml`). A style
asking the family for 600 resolves to the nearest available face, which is the 500, with no
synthetic bolding — D7 recorded that as an accepted consequence of shipping two faces instead of a
variable one.

Five mono styles declare 600: `Mono.InlineStrong`, `Label.Section`, `Label.CardHead`, `Label.Live`
and `Mono.Agent`. All five render 500. The declarations are not wrong — each transcribes a CSS rule
in the design source that literally says `600`, and the type gate reads the number out of that rule
rather than out of a table in a test — so the declared weights stay. What the resolution does mean
is that **two styles whose only difference is 600-versus-500 on the mono family are the same
pixels**, and the scale carries one such pair.

### `Label.CardHead` and `Mono.Meta` are that pair

    .tcard .h      { font: 600 10.5px var(--p-mono); color: var(--p-ink3); ... }   -> Label.CardHead
    .sheet2 .ctx   { font: 500 10.5px var(--p-mono); color: var(--p-ink3); ... }   -> Mono.Meta

Same size, same family, same tracking (neither rule states any, so both are CSS `normal` = 0),
same line-height (neither states one), same ink token, and the same resolved face. Two names for
one rendered result is the defect the type gate's own duplicate-origin check exists to prevent,
arriving through the font family instead of through the citation.

The existing Robolectric assertion is the proof, and it needs no change to stay true:
`SettingsRowTest.the status label is hero and not ok` claims `statusLabel` against `.tcard .h`'s
size, tracking and fixed-pitch-ness. Those three are what `.sheet2 .ctx` also states, so the test
goes on asserting the design fact after the component moves onto the surviving style.

### `Label.StatusBar` is dead

`Label.StatusBar` transcribes `.ptime`, the simulated iOS status-bar clock the mock draws at the top
of its phone frame. This app draws no status bar — the system does, and
`docs/design/substrate-components.md` classifies that surface as platform-owned, in its own words:
"The system draws it... No token applies, and pretending one does would be an invention." The style
has had zero call sites since it landed. `docs/verification/remote-phaseB-traceability.md` records
the objection as live and open, tracked by `agents-tracker-b2s`; this is where it closes.

## Decision

### T1. `Label.CardHead` merges into `Mono.Meta`

`TextAppearance.Swarm.Label.CardHead` is deleted from `type.xml`. Its one call site,
`ui/kit/SettingsRow.kt`'s `statusLabel`, spends `TextAppearance.Swarm.Mono.Meta`.

**No rendered pixel moves.** That is the whole authorization for the change and it is asserted
rather than claimed: `android/gate/s22b_type_test.go` now resolves both rules out of the design
source, resolves each declared weight through `res/font/jetbrains_mono.xml`'s own weight table, and
fails if the two rules stop rendering identically. If the design ever moves `.tcard .h` off
`.sheet2 .ctx`'s numbers, the merge fails and has to be re-argued rather than silently becoming a
size change nobody reviewed.

`.tcard .h` therefore joins the small set of design rules this app deliberately does not implement.
It is not excluded from the gate and not hidden: it is listed, with the rule it merged into, and
the gate holds that list to the same standard the doc-chrome exclusion is held to.

### T2. `Label.StatusBar` is deleted

`TextAppearance.Swarm.Label.StatusBar` is deleted from `type.xml`. `.ptime` joins the same
deliberately-unimplemented list, for the reason the derivation document already states: the status
bar is platform-owned and this app never draws one. Zero call sites move, because there were none.

This closes the PB-DS-2 objection recorded at `docs/specifications/remote-phaseB-requirements.md`
and `docs/verification/remote-phaseB-traceability.md` (bead `agents-tracker-b2s`).

### T3. `Mono.Fine` stays

`Mono.Fine` (`.sheet2 .bind`, 9.5/500 mono with a 1.6 leading) also has zero call sites today, and
is **not** deleted. It is reserved: the approval sheet's binding line — the line that says which
request a decision binds to — is specified and not yet built (beads `agents-tracker-1my5`,
`agents-tracker-joyi`). Deleting a style that a queued screen is specified to spend would trade one
kind of drift for another.

The distinction between T2 and T3 is not "unused versus used". It is **whether anything is going to
use it**: `.ptime` has no future call site because the app has no status bar to draw, and
`.sheet2 .bind` has a named one waiting.

### T4. The five 600-weight mono declarations stay as declared

Recorded, changed nothing. Each of the five transcribes a design-source rule that states `600`, and
the type gate reads the weight out of that rule; editing the declaration to `500` to match what
renders would be a number typed into a resource file against its own design fact, which is the
exact failure the join exists to catch. What renders is 500 today because D7 bundles two faces. If
that is wrong, the fix is a third face (D7 priced it: 273 KB) or a design change — both owner
decisions, neither one an XML edit.

### T5. The scale after this phase

**17 styles across 12 sizes.** Eight of the seventeen are mono. Ten sizes still sit inside the same
six-point band; this phase removes one occupant of it (13 sp, `Label.StatusBar`) and no more,
because every remaining half-step is occupied by a style with call sites.

## Open questions — routed to the owner, decided nowhere in this ADR

> **ALL FIVE WERE RULED ON 2026-08-09** (specimens: `https://claude.ai/code/artifact/cf7206b3-787c-43d7-b275-a46fa7e8320b`; recorded on beads `agents-tracker-v6sa` and `agents-tracker-oonj`, implemented under `agents-tracker-nx44.9`). They are left below **as asked**, because a question edited after it is answered stops being the record of what was uncertain. Question 1 is answered by P1, question 2 by P9 (ruling R2: section headers move to sans), question 3 by P3, question 4 by P4 and question 5 by P5.

None of these is a merge. Each one changes what somebody sees, so each is an owner ruling, and this
ADR states them so they are adjudicated together rather than one at a time by whoever next edits a
screen. They belong with `agents-tracker-v6sa`, which already holds the maquette-versus-scale half
of the same argument.

1. **The six-point band.** Ten sizes between 9.5 and 15.5 sp, every half-step occupied. Which of
   them are one size? A 0.5 sp step is not a hierarchy anybody perceives, and the app currently
   spends eight of them.

2. **The sans/mono role boundary.** Eight of the seventeen styles are mono, and they include roles
   that are not machine data by any reading: `Label.Section` sets every section header, and
   `Mono.Meta` now also sets a settings row's status word. ADR-009 D7's rule is "wherever machine
   data renders" — a section header is a label the design wrote, not a value the machine produced.
   This is half of why the app reads as "extensive code" rather than as an app that shows code.

3. **Nav title 27 sp against the maquette's 22 px.** Substrate's `.pnav .big` is 27 px and the
   maquette's `.nav .t` is 22 px. ADR-009 D3 changed weight and tracking and no size, and O5 ruled
   maquette sizes illustrative rather than normative — so the 27 stands by default rather than by
   decision. `agents-tracker-v6sa` is the bead.

4. **Drill title 15.5 sp against top-level 27 sp.** One tap into a drill-down, the screen title
   drops from `Display.NavTitle` 27 to `Title.Sheet` 15.5 — a 43 percent drop for a navigation
   step. The maquette draws 22 and 16, which is the same shape of drop; the question is whether the
   app wants it at this magnitude.

5. **The approval sheet's question.** It renders `Display.NavTitle` (27 sp, a screen-title style)
   while `Title.Sheet` — whose origin `.sheet2 h4` is literally the design's sheet-heading rule —
   is spent only on drill-down headers. `substrate-components.md` argues the choice from the
   ladder's contents ("the only style at `--p-display-wt` above `Title.Sheet`'s 15.5"), which is a
   consequence of the ladder rather than a decision about the sheet.

## Consequences

**Positive.**

- Two names for one rendered result become one name. A future skin change to `.sheet2 .ctx` now
  moves everything that rendered at 10.5/mono, instead of moving half of it.
- The PB-DS-2 objection that has been open since 2026-08-01 (`Label.StatusBar` ships unused)
  closes, and closes by deletion, which is what the design document's own classification implies.
- The merge's premise is now machine-checked. "Pixel-identical" was an argument in a bead comment;
  it is an assertion in the gate that fails if the design stops agreeing.

**Negative.**

- The scale is still a ramp. 17 styles across 12 sizes with ten of them inside six points is the
  same structural finding the field test reported; this phase removed the two occupants that cost
  nothing and left every occupant that costs a decision. The open questions above are the actual
  fix, and they are not this phase's to make.
- `docs/design/substrate-components.md` still names `Label.CardHead` at three places (row 15, the
  approval-card variant, and §7's move table). That prose is bookkeeping rather than a design
  change, and it is deliberately left for the owner's adjudication pass so that this phase edits no
  design source at all.
- ADR-009 D7's "(19 styles, ...)" parenthetical is now stale as a count. It is left as published —
  it is the record of what D7 did not change — and this ADR is the pointer.

## Alternatives considered

**Change the five 600 declarations to 500 so the file says what renders.** Rejected: the gate reads
each weight out of the CSS rule the style cites, so this would be a number typed into a resource
file in disagreement with its own design source. The declaration is a transcription; what it
resolves to at render time is the font family's business, and D7 already recorded that.

**Delete `Mono.Fine` as well, since it also has zero call sites.** Rejected under T3: a specified,
queued call site is a different thing from no call site.

**Point `Label.CardHead` at `.sheet2 .ctx` instead of deleting it.** Rejected: that produces two
styles claiming one design rule, which the type gate already refuses by name ("two names for one
design fact drift apart on the first edit").

**Do the whole consolidation in one pass.** Rejected as the scope ruling for this bead: every
remaining merge changes a rendered size, and a phase that mixes "no pixel moves" with "these
pixels move" is one review where the second half is the one nobody separated out.

---

# Phase 2 — the ruled ladder

**Status**: Accepted
**Date**: 2026-08-09
**Decides**: the owner rulings R1, R2, R3, R4 and R5 of 2026-08-09 (specimens
`https://claude.ai/code/artifact/cf7206b3-787c-43d7-b275-a46fa7e8320b`, recorded on bead
`agents-tracker-v6sa`; implemented under `agents-tracker-nx44.9`). R2 (section headers move to
sans) amends this ADR's own question 2 and lands here, in P9. R6 (spacing exceptions) lands in its
own gate record; R7–R8 (contrast floors, dot glow) amend ADR-009.
**Amends**: phase 1's T5 ("17 styles across 12 sizes") — the style count is unchanged and the SIZE
count becomes six. Phase 1's decisions T1–T4 are untouched.

## Context

Phase 1 took the two merges that moved no pixel and stated the rest as questions, because "every
remaining merge changes a rendered size". Those questions were put to the owner as rendered
specimens rather than as prose, and the owner ruled all of them. This section is the record of
what was ruled and the join that holds the app to it.

Nothing here is re-argued. Where this text gives a reason, the reason is the ruling's own.

### P1 — R1: the ladder is five rungs

The scale keeps its seventeen named styles and spends them across **five sizes** instead of
twelve. The rungs, and what the ruling names each one for:

| Rung | sp | What it sets |
| --- | --- | --- |
| micro | 10 | tabs, counters, agent names, section labels, meta |
| code | 11.5 | terminal text, chips |
| body | 12.5 | all running text |
| title | 14 | row titles, buttons, sheet headings |
| display | 22 | screen titles |

**A style's size is now read out of this ADR and everything else about it out of the design
source.** That is the shape of the change and it is the only thing about phase 2 that is
structural: a rung is a decision this app made about its own hierarchy, so it cannot be read out
of a CSS rule, and the alternative — typing sixteen sp values into `type.xml` — is the failure the
whole type gate exists to prevent. Weight, tracking, family, font features and the leading
MULTIPLIER still come from the rule each style cites, unchanged and unchangeable here.

### The rung table

> **SUPERSEDED 2026-08-27 by P10 below (ADR-020 D3, ruling R9).** The rows are struck through so
> that the two machine readers (`android/gate/s22b_type_test.go`, the Robolectric suite's
> `TypeScale`) take P10's table and not this one: both refuse a style that stands on two rungs, so
> the retired table has to stop parsing as rows. Every cell is otherwise as R1 ruled it, because
> this is the record of what R1 ruled.

Machine-read. `android/gate/s22b_type_test.go` and the Robolectric suite both parse it; the ADR is
staged onto the unit-test classpath for that second reader, beside `type.xml` and the design
source, so the text half and the resolved half cannot be reading two different tables.

**Design px** is the size the cited CSS rule states, and the gate resolves the rule and fails if
the two disagree — so a design that moves a rule leaves this table visibly stale rather than
quietly wrong. **Move** is `sp − design px`, checked arithmetically, and **every non-zero move
names the ruling that authorized it**: a size that moved without a citation is a size somebody
chose.

| Ladder style | Origin | Design px | Rung | sp | Move |
| --- | --- | --- | --- | --- | --- |
| ~~`Display.NavTitle`~~ | `.pnav .big` | 27 | display | 22 | -5 (R3) |
| ~~`Title.Sheet`~~ | `.sheet2 h4` | 15.5 | title | 14 | -1.5 (R1) |
| ~~`Title.Row`~~ | `.prow .pj` | 14 | title | 14 | 0 |
| ~~`Label.Button`~~ | `.acts2 button` | 13.5 | title | 14 | +0.5 (R1) |
| ~~`Body.Message`~~ | `.m2` | 12.5 | body | 12.5 | 0 |
| ~~`Body.Secondary`~~ | `.prow .ln` | 12 | body | 12.5 | +0.5 (R1) |
| ~~`Mono.InlineStrong`~~ | `.prow .ln b` | 11.5 | code | 11.5 | 0 |
| ~~`Mono.Code`~~ | `.sheet2 .cmd` | 11.5 | code | 11.5 | 0 |
| ~~`Label.Chip`~~ | `.chip` | 11 | code | 11.5 | +0.5 (R1) |
| ~~`Mono.CodeSmall`~~ | `.tcard .b` | 11 | code | 11.5 | +0.5 (R1) |
| ~~`Label.Section`~~ | `.plabel` | 10.5 | micro | 10 | -0.5 (R1) |
| ~~`Mono.Meta`~~ | `.sheet2 .ctx` | 10.5 | micro | 10 | -0.5 (R1) |
| ~~`Label.Live`~~ | `.pnav .live` | 10 | micro | 10 | 0 |
| ~~`Mono.Agent`~~ | `.prow .ag` | 10 | micro | 10 | 0 |
| ~~`Label.Tab`~~ | `.ptabs div` | 9.5 | micro | 10 | +0.5 (R1) |
| ~~`Mono.Fine`~~ | `.sheet2 .bind` | 9.5 | micro | 10 | +0.5 (R1) |

Sixteen rows for the sixteen styles that cite `origin:`. `Display.SAS` has none and is the one
style outside the ladder — see P7.

### P2 — no style is deleted, and no call site of a surviving style moves

The ruling consolidates SIZES, not names. Every role keeps its own style, because a role is what
the design source distinguishes and the app still transcribes every one of those distinctions:
`.prow .ag` and `.sheet2 .ctx` remain two rules, with two weights and two names, that this app now
renders at one size.

This is why phase 2 adds nothing to `s22bUnimplementedRules`. That register means "the design
draws this rule and the app deliberately does not spend it", and after the consolidation the app
spends all sixteen — at a ruled rung rather than at the rule's own px. Retiring a style here would
put a false statement in the register to describe a size change.

### P3 — R3: the display rung is 22, not 27

`Display.NavTitle` moves from Substrate's 27 to the signed maquette's 22. The ruling's reason:
"it's the signed design, and the nav row is about to gain the status pill; 22 shares the row more
gracefully."

This is the one move in the table that is not a merge — it is a size change, ruled on its own
specimen — which is why its Move cell cites R3 and not R1.

### P4 — R4: drill-down titles take the display rung

`navHeaderDrill` spends `Display.NavTitle` where it spent `Title.Sheet`. The ruling: "A screen is
a screen; depth is shown by the back chevron, not by shrinking the name."

The 43 percent drop phase 1 recorded as question 4 is gone: both headers now set their title at
22, and what distinguishes a drill-down from a root screen is the back control, the three-step
padding and the absence of a live counter — three differences that survive, so `NavHeaderDrill.kt`
remains a separate component for the reason it always was.

`Title.Sheet` is thereby **freed** and has no call site. It is not deleted, for phase 1's T3
reason exactly: the design's own sheet-heading rule now has no impostor sitting on it, and the
sheets that will spend it are specified (`agents-tracker-1my5`, `agents-tracker-joyi`). It sits at
the title rung, which is where a sheet heading belongs.

### P5 — R5: the approval sheet's question keeps the display size, and its citation is renamed

Ratified as it renders. The question stays at the display rung — "the question is the moment's
headline; the display size is right; only its style NAME is wrong" — so R5 changes no pixel beyond
P3's global 27 → 22.

What changes is the **reason written next to it**. The citation used to argue from the ladder's
contents: `Display.NavTitle` is "the only style in the scale at `--p-display-wt` above
`Title.Sheet`'s 15.5". That was a consequence of the ladder, not a decision about the sheet, and it
stopped being true the moment P1 moved `Title.Sheet` to 14. The citation now states the decision:
**the approval sheet's question takes the DISPLAY RUNG, deliberately, because the blocking question
is a screen-level headline** — the same rung the screen title and the drill title take, for the
same reason.

The style keeps the name `Display.NavTitle`, because the thing it transcribes is still `.pnav
.big`, and a style is named for the rule it descends from everywhere else in this file. The rung
is what the three roles share and the rung has a name of its own now.

### P6 — leading is recomputed on the rung

`android:lineHeight` is an absolute dimension, so it is the design's multiplier times the size the
text actually renders at. Five styles declare one, and three of the five move:

| Style | Multiplier | Was | Is |
| --- | --- | --- | --- |
| `Body.Message` | 1.45 | 18.125sp | 18.125sp |
| `Body.Secondary` | 1.4 | 16.8sp | 17.5sp |
| `Mono.Code` | 1.5 | 17.25sp | 17.25sp |
| `Mono.CodeSmall` | 1.55 | 17.05sp | 17.825sp |
| `Mono.Fine` | 1.6 | 15.2sp | 16sp |

Both gates recompute the product rather than trusting it, which is what they already did; the only
change is which size they multiply.

### P7 — what phase 2 leaves alone, and the one thing it discloses

**`Display.SAS` is not on the ladder.** 34 sp, `derived:` from
`docs/design/substrate-components.md` §7, and it sets the four verification emoji a person compares
against their Mac's. It is a specimen to be matched, not text to be read in a hierarchy, and the
ruling's five rungs are about hierarchy. The file therefore carries six sizes: five rungs and the
SAS. The gate asserts exactly that split rather than counting to six.

**`Mono.Fine` moves although it has no call site.** It is 9.5 today and the ruling retires 9.5; a
reserved style left off the ladder would put the half-step back the day its screen is built.

**DISCLOSED: the micro rung now carries a render-identical pair.** `Mono.Agent` (`.prow .ag`,
600) and `Mono.Meta` (`.sheet2 .ctx`, 500) are both mono, both at 10, both untracked, and ADR-009
D7's two-face bundle resolves 600 to the 500 face — so they render the same pixels, which is
exactly the condition under which phase 1's T1 merged `Label.CardHead` into `Mono.Meta`.

They are **not** merged here, and the difference from T1 is where the identity comes from. T1's
pair was identical **in the design**: two CSS rules stating the same size, same tracking, same
family. This pair is identical only **after** a rung this app chose; the design still draws them at
10.5 and 10. Deleting one would record "the design draws `.prow .ag` and this app does not spend
it", which is false — and it would also decide, in a resource file, which of two roles is the real
one. Whether the app wants one name for both is a naming decision that belongs with R2's
sans/mono boundary, and it is left open rather than taken here.

### P8 — two fix-pass placements, ratified

Both were taken by the wave-2 fixer, disclosed rather than buried, and are ratified by this record
so the next reader meets a decision instead of an artefact.

1. **The kill-switch panel sits LAST in Settings' `CONNECTION` section.** A control that severs
   every session qualifies everything above it; a reader meets what the connection does before
   meeting the way to end it, and a destructive control placed first reads as the section's
   subject.
2. **Derivation row 15 (settings row) was amended for `statusLabel`'s retirement.** The
   status-text trailing form never acquired a production caller and its only candidate row is one
   `SettingsPanelScreen` deliberately does not build; the row keeps its ink rule verbatim, because
   that is the design record a future spend has to obey, and records that the FORM does not ship.

### P9 — R2: section headers move to sans, and the mono boundary is restated

R2 answers phase 1's own question 2 (the sans/mono role boundary). `Label.Section` — the group
heading that sets NEEDS YOU, WORKING, PAIRING, NOTIFICATIONS — moves from mono to sans-serif caps.
The ruling's own reason: "The rule becomes crisp and defensible: mono = data the machine produced
(agent names, code, ids, timestamps), sans = the app speaking. Headers are the app speaking."

`Label.Section`'s cited properties move, and its size does not — R1 already put it on the micro
rung and R2 touches nothing there:

| Property | Was (`.plabel`, mono) | Is (R2, sans) |
| --- | --- | --- |
| `android:fontFamily` | `@font/jetbrains_mono` | `sans-serif` |
| `android:fontFeatureSettings` | `tnum, zero, calt` | none — sans carries nothing, the rule every other sans style in this file already holds |
| `android:letterSpacing` | 0.09 (`.plabel`'s own) | 0.11 |

Weight (600) does not move; `.plabel`'s own citation states it and R2 does not touch it.

**THE 0.11 TRACKING IS THE RULING'S SPECIMEN, NOT `.plabel`'S AND NOT INVENTED.** The owner's
rendered comparison draws the choice as `.secsans { font-size: 10.5px; font-weight: 600;
letter-spacing: 0.11em; text-transform: uppercase; color: var(--ink3); }` beside Substrate's own
mono precedent, `.secmono` at 0.09em, so the wider tracking is read directly against the value it
replaces rather than chosen after the fact. A sans face carrying a mono tracking sits tighter than
spaced caps read on a proportional face; the specimen's 0.11em is the record of the value the
ruling actually looked at.

**THE MONO BOUNDARY, RESTATED AS THE RULED RULE.** ADR-009 D7's own words were "wherever machine
data renders"; R2 makes that the standing rule for every style in this file, mono or sans, from
here on: **mono is for data the machine produced — agent names, code, ids, timestamps — and
nothing else.** A label the design wrote, however small or however uppercase, is the app speaking
and takes the sans family. Phase 1's question 2 named two violations. `Label.Section` was the
live one and R2 fixes it. The other was `Label.CardHead`'s role — the settings row's status word,
merged into `Mono.Meta` by T1 for pixel-identity and then retired without ever acquiring a
production caller (P8.2's own record, `agents-tracker-2pnu` F5) — and it has no call site left to
move. Should that role ever return, this is the boundary it returns under: a status word is the
app speaking, not data the machine produced, so it would not come back onto `Mono.Meta`.

**`Mono.Meta` ITSELF DOES NOT MOVE.** Its three live call sites — `ActivityRow.kt`'s timestamp
cell, `Notice.kt`'s `noticeDetail` (the machine's own raw reason, spliced under a notice line),
`ApprovalSheet.kt`'s `contextLine` (the project, the agent and the machine — who is asking) — are
each an instance of the ruled rule's own list: a timestamp, a diagnostic identifier, an agent
name. `.tcard .h`'s own demo content in the shared block is a file path (`Edit ·
internal/attach/attach.go`), which is why T1's merge into `Mono.Meta` was already safe on the
boundary R2 states as well as on the pixels T1 argued: both rules are machine data, and R2 leaves
that pair exactly where phase 1 put it.

**The gate.** `android/gate/s22b_type_test.go` reads `Label.Section`'s family and tracking from a
small RULED table rather than from `.plabel`'s own citation — the same shape R1's rung table takes
for size: a role's VOICE is a decision about this app's own boundary, not a fact the cited CSS
rule alone can state once the rule and the ruling disagree. The table has one row today, and every
entry is checked against this section's own text (the `### P9 — R2:` heading above) so a family
that moved without a citation behind it fails exactly as an uncited size move does in the rung
table.


### P10 — R9: the five rungs shift one step up (ADR-020 D3, 2026-08-27)

Owner ruling of 2026-08-27, recorded in `docs/specifications/phone-refit-playbook.md` section 5
and decided by [ADR-020](ADR-020-slate-palette-and-breathing-scale.md) D3: on the handset the
ladder R1 ruled reads one rung too small, so every rung moves up and the ladder keeps its shape.
R1's structure is untouched — five rungs, sizes read out of this record and everything else out of
the cited rule — and R1's table above is superseded in its numbers only.

| Rung | R1 sp | R9 sp | What it sets |
| --- | --- | --- | --- |
| micro | 10 | 11 | tabs, counters, agent names, section labels, meta |
| code | 11.5 | 12.5 | terminal text, chips |
| body | 12.5 | 14 | all running text |
| title | 14 | 15 | row titles, buttons, sheet headings |
| display | 22 | 24 | screen titles |

Gaps 1.5 / 1.5 / 1 / 9: every rung is at least the point apart that R1's finding requires. R3's
display rung and R4's drill-title placement move with it (24 for both headers and the approval
sheet's question); P6's leadings are recomputed on the new rungs by both gates, as before.

**The rung table, as amended.** Machine-read by the same two readers, under the same rules: the
`Design px` column is still the cited rule's and is held to the Substrate artifact; `Move` is `sp −
design px`; and every row now moves, so every row cites R9 — the one ruling that authorized the
whole shift, in place of R1 and R3.

| Ladder style | Origin | Design px | Rung | sp | Move |
| --- | --- | --- | --- | --- | --- |
| `Display.NavTitle` | `.pnav .big` | 27 | display | 24 | -3 (R9) |
| `Title.Sheet` | `.sheet2 h4` | 15.5 | title | 15 | -0.5 (R9) |
| `Title.Row` | `.prow .pj` | 14 | title | 15 | +1 (R9) |
| `Label.Button` | `.acts2 button` | 13.5 | title | 15 | +1.5 (R9) |
| `Body.Message` | `.m2` | 12.5 | body | 14 | +1.5 (R9) |
| `Body.Secondary` | `.prow .ln` | 12 | body | 14 | +2 (R9) |
| `Mono.InlineStrong` | `.prow .ln b` | 11.5 | code | 12.5 | +1 (R9) |
| `Mono.Code` | `.sheet2 .cmd` | 11.5 | code | 12.5 | +1 (R9) |
| `Label.Chip` | `.chip` | 11 | code | 12.5 | +1.5 (R9) |
| `Mono.CodeSmall` | `.tcard .b` | 11 | code | 12.5 | +1.5 (R9) |
| `Label.Section` | `.plabel` | 10.5 | micro | 11 | +0.5 (R9) |
| `Mono.Meta` | `.sheet2 .ctx` | 10.5 | micro | 11 | +0.5 (R9) |
| `Label.Live` | `.pnav .live` | 10 | micro | 11 | +1 (R9) |
| `Mono.Agent` | `.prow .ag` | 10 | micro | 11 | +1 (R9) |
| `Label.Tab` | `.ptabs div` | 9.5 | micro | 11 | +1.5 (R9) |
| `Mono.Fine` | `.sheet2 .bind` | 9.5 | micro | 11 | +1.5 (R9) |

Sixteen rows, as before; `Display.SAS` stays off the ladder at 34 sp (P7). The leadings that
follow: `Body.Message` 1.45 × 14 = 20.3sp, `Body.Secondary` 1.4 × 14 = 19.6sp, `Mono.Code` 1.5 ×
12.5 = 18.75sp, `Mono.CodeSmall` 1.55 × 12.5 = 19.375sp, `Mono.Fine` 1.6 × 11 = 17.6sp.

P7's disclosed pair (`Mono.Agent` and `Mono.Meta`, render-identical on the micro rung) is
unchanged by this: both move to 11 together, and the naming question stays open where P7 left it.

## Consequences

**Positive.**

- The finding the field test reported structurally — eleven sizes inside a six-point band — is
  gone. Five rungs, each at least 1.5 sp from its neighbour, is a hierarchy a reader can perceive.
- The two nav headers and the approval sheet now state one rung instead of three sizes, so the
  "how big is a screen title" question has one answer.
- Sizes did not stop being machine-checked; they changed which machine-read record they are checked
  against. `type.xml` still cannot carry a number nobody wrote down.

**Negative.**

- `type.xml` now joins TWO records instead of one, and a reader has to hold both: the design source
  for what a style looks like, this ADR for how big it is. The gate reads both and fails on either,
  which is the mitigation, but it is genuinely one more file in the chain.
- The micro rung's render-identical pair, above. It is a defect by phase 1's own standard and it
  is being carried deliberately, with the reason written down.
- `Title.Sheet` has zero call sites again, one phase after phase 1 argued that a specified future
  call site is what distinguishes T3 from T2. The argument still holds and it is now doing more
  work.

## Alternatives considered

**Put the sp values in `type.xml` and let the gate read them from there.** Rejected: that is
`EXPECTED_DARK_COLORS` again — a test certifying that the app renders whatever the resource file
says. The ruling is a decision and a decision has a record; the record is what the gate reads.

**Add a `rung:` field to each style's citation comment.** Rejected as a third place for the same
fact. The rung table is keyed by style name, the citation is keyed by CSS rule, and a style
declaring its own rung would let the file disagree with the ADR in a way the ADR could not see
without checking the file it is the authority for.

**Merge `Mono.Agent` and `Mono.Meta`.** Rejected under P7: it is a naming decision the ruling did
not make, and the register it would have to be recorded in says something false about the design.

**Keep 27 and move only the merges.** Rejected: R3 ruled the size, and the nav row's coming status
pill is the reason the ruling gave.
