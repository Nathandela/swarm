# ADR-021: Slate palette and the breathing scale — a cool ladder, wider steps, one rung up

Status: Accepted (owner decision, 2026-08-27)
Date: 2026-08-27
Supersedes: [ADR-009-obsidian-visual-direction.md](ADR-009-obsidian-visual-direction.md) D1, the
values in D3, and the colour half of D4. Everything else in ADR-009 stands; section "What survives"
below lists it by decision number.
Companions: `docs/research/slate-maquette.html` (the design source), `internal/design/tokens.json`
(the origin), `docs/specifications/phone-refit-playbook.md` section 5 (the wave that executes this),
[ADR-012](ADR-012-type-ladder-consolidation-phase-1.md) P10 (the rung table this amends), bead
`agents-tracker-d45a.4`.

## Context

The owner reviewed eight screenshots of the shipped 0.8.0 phone app on 2026-08-27 and ruled on
colour, density and type in the same session as the frame, sending and copy rulings the playbook
records: the warm Obsidian palette reads as brown on a real panel, the layout is cramped, and the
type is one rung too small to read at a glance. An interactive mock was compared against the
shipped app and Slate was chosen for the whole phone app and for the launcher mark.

Nothing about the pipeline was in question. ADR-009 built a one-origin token regime (a normative
maquette, `tokens.json` transcribing its `:root`, `design-tokens.tsv` joining tokens to Android
resources, drift gates in both directions, a measured contrast gate, a spacing ledger and a ruled
type ladder) and it did what it was built to do: this direction change is a value flow through
that regime, with exactly the friction it was designed to have. What the owner ruled on is the
palette, the density and the ladder. What this ADR decides is those three, and the mark.

## Decision

### D1. The skin is Slate. The regime is unchanged.

Slate replaces Obsidian as the app's single dark-mode skin. `skin: "obsidian"` becomes `"slate"`
and `tokens.json.source` moves to **`docs/research/slate-maquette.html`**, a new file rather than a
second block in the Obsidian one: `internal/design/tokens_test.go` takes the first `:root` a
source declares, so two blocks in one file would let the JSON match whichever came first. The new
file is a copy of the Obsidian maquette with the 35 tokens moved, the slab given its breathing (D2)
and the accent's own literals moved with it; every selector and both block markers the gates read
are unchanged, so the readers that took a geometry or a `var()` binding from the Obsidian maquette
take it from the Slate one without a row change. The Obsidian maquette stays checked in as the
record of the direction this one supersedes.

The 35-token set is the same set: nothing is added, nothing removed, so `htmlTokenCount` and
`colourTokenCount` hold. Values:

| Token | Obsidian | Slate | Note |
|---|---|---|---|
| `--p-bg` | `#0e0b08` | `#0b0e14` | Cool near-black ground. Never #000, as before. |
| `--p-card` | `#171310` | `#131824` | One lightness step up. |
| `--p-elev` | `#1f1a13` | `#1b2334` | The only step above card; elevation stays a lighter surface, never a shadow. |
| `--p-well` | `#090705` | `#080a10` | Recessed. |
| `--p-hair` | `#2e271d` | `#262e3f` | Cool hairline. |
| `--p-ink` | `#f6f3ec` | `#eef2f8` | Off-white, cool. Still not pure white. |
| `--p-ink2` | `#a69d8e` | `#9aa6ba` | Secondary. |
| `--p-ink3` | `#746b5d` | `#66718a` | Tertiary; still Completed and the offline dot (PB-TOK-8 mapping unchanged). |
| `--p-hero` | `#c9a876` | `#8eb4e6` | The identity change: champagne to slate blue. Still "you / needs you / live". |
| `--p-hero-ink` | `#1a1206` | `#0b1524` | Ink on slate fills. |
| `--p-att` | `#c9a876` | `#8eb4e6` | Still value-aliases `--p-hero` and still keeps its own row. |
| `--p-work` | `#6fa7a4` | `#6fc3bc` | Working: a brighter teal on the cool ladder. |
| `--p-ok` | `#8caf8e` | `#8cc49a` | ReadyForReview: sage, brightened. Green stays on review, not done. |
| `--p-err` | `#d96a62` | `#e5736b` | Error and deny. |
| `--p-cta-bg` | `#c9a876` | `#8eb4e6` | Still value-aliases hero, still its own row. |
| `--p-cta-ink` | `#1a1206` | `#0b1524` | |
| `--p-tabbg` | `rgba(14,11,8,0.88)` | `rgba(11,14,20,1)` | The tab bar and the composer bar are opaque: the ground's own value at full alpha. See below. |
| `--p-font`, `--p-mono`, `--p-display-wt`, `--p-display-tr`, `--p-body-tr` | | unchanged | |
| `--p-card-r` | `14px` | `16px` | Slab radius, one step softer on the wider slab. |
| `--p-sheet-r` | `18px` | `20px` | |
| `--p-btn-r` | `10px` | `12px` | |
| `--p-chip-r` | `8px` | `10px` | |
| `--p-dot-r` | `4px` | `4px` | unchanged; the dot is still a circle and still has no radius resource. |
| `--p-card-fx` | `inset 0 1px 0 rgba(246,243,236,0.10)` | `inset 0 1px 0 rgba(238,242,248,0.08)` | The key light is `--p-ink` at a quieter alpha; one light source, top edge. |
| `--p-lit-fx` | `inset 0 1px 0 rgba(246,243,236,0.22)` | `inset 0 1px 0 rgba(238,242,248,0.18)` | The promoted slab's stronger edge, same ratio to the resting one in spirit, not in arithmetic. |
| `--p-cta-fx` | `0 0 18px rgba(201,168,118,0.22)` | `0 0 18px rgba(142,180,230,0.22)` | Slate bloom, mechanism and alpha unchanged. |
| `--p-grain` | `0.04` | `0.04` | unchanged. |
| `--p-workbar` | `linear-gradient(90deg, #6fa7a4, transparent 85%)` | `linear-gradient(90deg, #6fc3bc, transparent 85%)` | The opaque stop follows `--p-work`. |
| `--p-sweep-fx` | `sweep 500ms rgba(255,252,244,0.30)` | unchanged | See D5 below: the register survives and it holds the peak. |
| `--p-sheet-hi` | `#231c12` | `#1b2334` | Approval-sheet gradient top; value-aliases `--p-elev` in Slate and keeps its own row, as `--p-att` does with `--p-hero`. |
| `--p-sheet-lo` | `#16110b` | `#10141d` | Gradient bottom. |

**The tab bar and the composer bar are opaque, and the opacity lives in the token.** ADR-009 D4.5
already banned the blur the 88% was paired with, and on a handset a translucency with nothing behind
it read as a bar you see the list through. W1 pins the bars but cannot stop spending
`swarm_tabbar_background` (`o3_material_test.go` refuses a colour resource nothing draws), so the
alpha moved to `1` in the origin, `swarm_tabbar_background` became `#FF0B0E14`, and the two bars keep
spending the resource untouched. `--p-tabbg` value-aliases `--p-bg` from here on and keeps its own
row and resource, exactly as `--p-att` and `--p-cta-bg` alias `--p-hero`.

**`--p-sweep-fx` keeps its peak, and that is a decision rather than an omission.** The playbook's
token list proposed `rgba(238,242,248,0.30)`. The sweep's peak is stated twice on purpose: in the
palette as an effect token and in ADR-009 D5's motion register, and `MotionTest` holds the two to
agree. D5 survives this ADR whole, `Motion.kt`'s tint constants are joined to the token by the O4
gate, and neither file is in this wave. So the specular highlight stays the warm-white glint it was:
at 30% over a slate card the warmth is not perceptible, and a peak that moved with the palette would
have to move the register and the motion constants with it, in a wave that is about colour. If the
owner wants a cool peak it is a one-token, one-row, one-constant change with its own record.

Three derived colours move by value flow and no blend changed (PB-TOK-7): `attention-row-border`
`#66553D` to `#4B5E7B`, `deny-fill` `#21D96A62` to `#21E5736B`, `needs-input-dot-glow` `#80C9A876`
to `#808EB4E6`. None collides with a Slate token literal.

### D2. Breathing: the components spend wider steps of the same scale.

The ten-step 2dp grid (PB-DS-1) and the three frame constants are unchanged; no step is added and
none is removed. What changes is which step the list and its cards spend. In the maquette the slab
goes from `margin: 0 14px 10px; padding: 13px 15px` to **`margin: 0 16px 14px; padding: 12px
16px`**: list gap 14dp, side inset 16dp, card padding 16x12dp. The spacing ledger follows by
arithmetic rather than by edit: the design no longer declares 15px anywhere, so `swarm_space_14`
absorbs `{14}` alone, and the movers go from seven to six, all still by 1dp. `dimens.xml`'s ten
steps are byte-identical.

A 4dp grid was considered and rejected again, for PB-DS-1's original reason: it loosens everything
by ten percent whether or not a component wanted air. Breathing is a decision per component, and
the slab is the component the owner pointed at.

### D3. The five rungs shift one step up.

ADR-012 phase 2's ladder was **10 / 11.5 / 12.5 / 14 / 22**. It becomes **micro 11 / code 12.5 /
body 14 / title 15 / display 24**: gaps 1.5 / 1.5 / 1 / 9, every one at least the point ADR-012
requires between rungs. The authority stays ADR-012's rung table, amended by a P10 section under a
minted ruling **R9**, and every `Move` cell of the amended table cites R9 because every style moves
relative to the design px the table still holds to. `type.xml` follows the table; the five
`android:lineHeight` values are recomputed as multiplier times rung (`Body.Message` 1.45 x 14 =
20.3sp, `Body.Secondary` 1.4 x 14 = 19.6sp, `Mono.Code` 1.5 x 12.5 = 18.75sp, `Mono.CodeSmall` 1.55 x
12.5 = 19.375sp, `Mono.Fine` 1.6 x 11 = 17.6sp). `Display.SAS` at 34sp is off the ladder and does
not move. Weight, tracking, family and font features are the cited rules' and do not move.

### D4. The launcher mark is repainted, not redrawn.

`docs/design/icon-candidates/solid-wedge.svg` keeps every coordinate, the 8.5 stroke, the mitred
joins, the butt caps and the cursor bar; its two colours move with the tokens (`#0E0B08` to
`#0B0E14`, `#C9A876` to `#8EB4E6`) in the same commit as `tokens.json`, because the launcher gate
compares the SVG literal to the resolved token. `ic_launcher_foreground.xml` and the two
`mipmap-anydpi-v26` files reference `@color/swarm_hero` and `@color/swarm_background` and are
untouched. The two 512px store PNGs are re-rendered from the Slate SVG at the framing the
appicon evidence recorded (`viewBox 15.54 18.37 72 72`, the mark's own bounding box) and the XOR
check is re-run; they had still been the Substrate phosphor-green render, so this also closes
ADR-009's "brand divergence window".

## What ADR-009 this supersedes, and what survives

Superseded: **D1** (the skin), **D3**'s values (the table above replaces them; the token set, its
kinds and its count are D3's still), and the **colour half of D4** (linen key-light, champagne
bloom, warm sheet stops).

Survives, by number: **D2** (a complete checked-in maquette is the design source; the file changes,
the arrangement does not), **D4**'s material grammar (single top-edge light, no drop shadows, grain
at 4% content-anchored, one vertical gradient on the sheet, backdrop blur banned), **D5** (the
motion register, the sweep's peak included), **D6** (status semantics and the four-Group binding;
the values are Slate's, the pairing att=hero and the pairwise distinctness are kept), **D7** (type
substitutions, the bundled mono family, font features, `/1` as silence), and **D8**'s gates with
their floors byte-identical (primary 90, supplementary 45, incidental 24, cta-label 55,
accent-text 50, error-text 38, indicators WCAG 3.0).

## Consequences

**Positive.**

- The palette, the density and the ladder move through the existing pipeline with zero new
  mechanism: a new maquette, one JSON, and every downstream file moved because a gate demanded it.
- Every ink role measures higher on Slate than on Obsidian except `--p-ink`, which measures
  slightly lower (Obsidian `-100.0..-98.7`, Slate `-98.9..-96.5` across the four grounds) and
  still clears the primary floor of 90 by 6.5 or more; the CTA label rises too (`+58.8` to
  `+60.9`). On `--p-bg`: ink `-98.8`, ink2 `-53.3`, ink3 `-27.5`, hero as text `-60.0`, err as
  text `-45.2`, hero-ink on hero `+60.9` (APCA Lc; negative is light-on-dark). The indicators sit
  at 3.6:1 to 9.6:1. The contrast gate is proof this is an accessibility improvement, not a
  repaint; the per-role Obsidian and Slate ranges are kept beside each floor in
  `android/gate/slate_contrast_test.go`.

**Negative / accepted.**

- **The tight pair is `--p-ink3` on `--p-elev`: Lc 25.2 against the 24 floor.** The tertiary ink
  on the lightest ground has 1.2 of headroom. The floor does not move (D8); if a panel pass ever
  finds it unreadable the token lightens, the ladder rule.
- **The ceiling proof is re-derived on `#8eb4e6`.** The maximum reachable |Lc| on the slate fill is
  **62.04** (pure black; white reaches 46.58). That is still below the retired body floor of 75, so
  the amendment's finding stands for the fill the CTA label actually sits on; it is above the
  retired large floor of 60 that the champagne fill missed by 0.3, so that half of the proof is
  historical and is quoted as such in `slate_contrast_test.go`. The CTA floor of 55 is reachable
  and the live pair measures 60.9.
- **The kit's spacing joins cite the slab, and one row is left behind on purpose.** Under the
  lead's W4.5 ruling the joins were re-pointed in the same wave (`98e19397`): `s23Spacing`'s
  `.prows` rows in `android/gate/s23_kit_test.go` read `.slab { margin }` out of the Slate
  maquette, `s23DerivedSpacing`'s rows 14 and 26 and `docs/design/substrate-components.md` rows
  14 and 26 state `space_12` x `space_16` with the slab named as the source, and `sessionList`,
  `activityRow` and `messageBubble` spend those steps. Row 31 (the file-change row, "row 14's
  activity row, verbatim") keeps `space_10` x `space_12` pending its own ruling, and
  `sessionRow`'s own `.prow` padding did not move.
- **Every verification screenshot under `docs/verification/` and `docs/design/store-assets/
  screenshots/` is stale** until the O7-style device pass is re-run on Slate.
- `docs/design/conversation-drawing.html` keeps an Obsidian `:root` block nothing reads; it is
  W5's file and is left as published for W5's brief. `docs/design/substrate-components.md`'s two
  live Obsidian figures -- the section 1.1 contrast note and row 23's focus-ring ratios -- were
  re-measured on Slate in W4 (`fb439c52`), with the Obsidian figures quoted as history.
- The Slate maquette's own font sizes are a redraw at the Obsidian ladder (the nav title is 22px
  where the app now renders 24sp). The type authority has been ADR-012's table since R1 and is
  unaffected; the discrepancy is the same one ADR-012 already records for the Obsidian file.

## Alternatives considered

- **Recolour without re-typesetting.** Rejected by the owner's own ruling: the type was one rung
  too small on the handset, and a palette change that left it would ship half the finding.
- **A second `:root` block in the Obsidian maquette, selected by skin name.** Rejected:
  `parseSkinTokens` takes the first match, both blocks would sit in one file with one set of kit
  rules drawn at one of them, and the owner-signed Obsidian file would be edited to host a
  direction it does not draw. A new file keeps each maquette a whole drawing at its own values.
- **A 4dp grid.** Rejected, D2.
- **Move the sweep peak with the palette.** Deferred, D1; a one-token change with its own record
  if wanted.
