# ADR-012: Type ladder consolidation, phase 1 — safe merges

**Status**: Accepted (safe half); the questions in "Open questions" are **Proposed** and belong to the owner
**Date**: 2026-08-09
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
