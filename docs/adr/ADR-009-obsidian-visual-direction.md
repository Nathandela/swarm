# ADR-009: Obsidian visual direction — warm material, one champagne accent, one earned specular moment

Status: Accepted (owner decision, 2026-08-07)
Date: 2026-08-07

## Context

Substrate (d1) was chosen as the phone app's single v1 skin by ADR-007-remote-access B3 and
PB-TOK-2: a cool tinted near-black ladder, hairline-only depth, one phosphor-green hero. It is
built, shipped to the internal track, and pinned end to end — 31 tokens in
`internal/design/tokens.json`, joined to Android resources by `android/design-tokens.tsv`,
drift-guarded in both directions by the `android/gate/` suite.

On 2026-08-07 the owner commissioned a design exploration beyond Substrate ("dev ultra premium:
ultra clean, perfected, modern, elegant, cutting edge"). Three research streams fed it: a
distillation of the owner's design-research library (`docs_personal/research/design/`, notably
the eight-paper black-glass series), a field study of the premium dev-tool canon (Linear,
Raycast, Warp, Vercel Geist, Family, M3 Expressive motion tokens), and an inventory of the
current app. Six directions were drafted with same-content mockups
(artifact: `swarm — six premium directions`). **The owner chose Obsidian.**

Obsidian, in one sentence: quiet-luxury dark glass — a warm near-black that reads as volcanic
material rather than screen, every surface defined by a single light source catching its top
edge, one champagne accent that means "you", and exactly one moment of motion: a specular
highlight that travels a card's top edge once, at the moment the session asks for its human.

The research constraints this direction is built against (each is load-bearing below):
near-black never #000 and elevation in discrete lightness steps, because OLED panels render
near-black at ~6-bit precision and auto-dim static accents 20-40% within minutes; accents
desaturated 15-25% for dark; motion on black amplified ~80:1, so at most one moving element per
viewport; premium codes null each other when stacked, so material is the single carrier of
luxury here and type/motion stay plain; and a premium signifier detected as decoration inverts
into cheapness, so every effect must be traceable to the interface's own geometry and light
(one light direction, grain as microstructure, no free-floating glows).

## Decision

### D1. The skin is Obsidian. The regime is unchanged.

Obsidian replaces Substrate as the app's single dark-mode skin. This supersedes
ADR-007-remote-access **B3's choice of direction only**. Everything structural that B3's regime
built is deliberately kept: the one-origin token pipeline
(`tokens.json` → `design-tokens.tsv` → `values/*.xml`), the bidirectional gates, the 2dp
spacing scale (PB-DS-1), the Group→token binding mechanism (PB-TOK-8), the ban on visual
literals outside the theme (PB-DS-11), programmatic Views, the screen inventory and UX flows,
and dark-only until Phase C. A skin change that also changed the regime would be two decisions
wearing one ADR.

### D2. The design source is a complete checked-in maquette.

Substrate's tokens were transcribed from
`docs/research/remote-control-design-directions.html`; the transcription-vs-invention
discipline in PB-TOK exists because of that. Obsidian gets the same arrangement:
**`docs/research/obsidian-maquette.html`** is the normative design source — every screen the
app has, every kit component, and the reworked mark, drawn at token fidelity, carrying a
machine-readable `:root` token block. `tokens.json.source` moves to it. No value below ships
until it exists in the maquette; the maquette is reviewed by the owner before any code phase
(obsidian-migration-plan.md, phase O1).

### D3. The normative token set.

31 existing tokens, 27 of them changing value; 4 new. `skin: "substrate"` → `"obsidian"`.

| Token | Substrate | Obsidian | Note |
|---|---|---|---|
| `--p-bg` | `#08090a` | `#0e0b08` | Warm near-black ground. Never #000 (OLED smear/banding). |
| `--p-card` | `#0f1011` | `#171310` | One lightness step up. Steps are hand-tuned for the 6-bit near-black regime; each adjacent pair must survive an OLED at 30% brightness as two distinct surfaces. |
| `--p-elev` | `#141516` | `#1f1a13` | The only step above card. Elevation stays a lighter surface, never a shadow. |
| `--p-well` | `#060708` | `#090705` | Recessed: command well, terminal peek ground. |
| `--p-hair` | `#23252a` | `#2e271d` | Linen-warm hairline, never cool white. |
| `--p-ink` | `#f7f8f8` | `#f6f3ec` | Linen ink. Off-white on purpose: pure white halates for the 30-50% of adults with astigmatism. |
| `--p-ink2` | `#8a8f98` | `#a69d8e` | Secondary. |
| `--p-ink3` | `#62666d` | `#746b5d` | Tertiary; also Completed group and offline dot (PB-TOK-8 mapping unchanged). |
| `--p-hero` | `#53ce7c` | `#c9a876` | THE identity change: phosphor green → champagne, desaturated ~20% per the dark-accent rule. Champagne means "you / needs you / live". |
| `--p-hero-ink` | `#06150c` | `#1a1206` | Ink on champagne fills. |
| `--p-att` | `#f1a10d` | `#c9a876` | NeedsInput group. **Value-aliases `--p-hero` deliberately** — in Obsidian the accent IS the needs-you signal. Keeps its own row and resource exactly as `--p-cta-bg` already does; the join must not deduplicate by value. |
| `--p-work` | `#00c2d7` | `#6fa7a4` | Working group: desaturated patina teal — the machine's one cool note in a warm world. |
| `--p-ok` | `#4cc38a` | `#8caf8e` | ReadyForReview group: desaturated sage. Green stays on review, not done (ADR-007 B134 d1). |
| `--p-err` | `#ff6369` | `#d96a62` | Error/deny: terracotta. |
| `--p-cta-bg` | `#53ce7c` | `#c9a876` | Still value-aliases hero, still its own row. |
| `--p-cta-ink` | `#06150c` | `#1a1206` | |
| `--p-tabbg` | `rgba(8,9,10,0.88)` | `rgba(14,11,8,0.88)` | |
| `--p-font` | (platform sans) | unchanged | |
| `--p-mono` | (platform mono) | unchanged at O2; O5 prepends bundled JetBrains Mono | Fixes the recorded box-drawing fallback defect and unlocks tnum/zero/calt. |
| `--p-display-wt` | `650` | `500` | Narrow weight band; size and colour carry hierarchy, weight does not shout. |
| `--p-display-tr` | `-0.025em` | `-0.015em` | |
| `--p-body-tr` | `-0.008em` | `0.01em` | Positive tracking on light-on-dark body, per the reading-ergonomics guidance. |
| `--p-card-r` | `9px` | `14px` | Slab radius. |
| `--p-sheet-r` | `14px` | `18px` | |
| `--p-btn-r` | `9px` | `10px` | |
| `--p-chip-r` | `8px` | `8px` | unchanged |
| `--p-dot-r` | `4px` | `4px` | unchanged; the dot remains a true circle. |
| `--p-card-fx` | `inset 0 1px 0 rgba(255,255,255,0.045)` | `inset 0 1px 0 rgba(246,243,236,0.10)` | The key-light warms and strengthens: one light source, top edge, linen-toned. |
| `--p-cta-fx` | `0 0 18px rgba(83,206,124,0.20)` | `0 0 18px rgba(201,168,118,0.22)` | Champagne bloom, mechanism unchanged. |
| `--p-grain` | `0.05` | `0.04` | Re-rastered warm-neutral 140x140, still checked in, still SOFT_LIGHT. |
| `--p-workbar` | `linear-gradient(90deg, #00c2d7, transparent 85%)` | `linear-gradient(90deg, #6fa7a4, transparent 85%)` | Android impl keeps RGB in the transparent stop (PB-DS-5 rule survives). |
| `--p-lit-fx` | — | `inset 0 1px 0 rgba(246,243,236,0.22)` | NEW (effect). The promoted card: a NeedsInput slab carries the stronger key-light. |
| `--p-sweep-fx` | — | `sweep 500ms rgba(255,252,244,0.30)` | NEW (effect). The specular sweep, D5. |
| `--p-sheet-hi` | — | `#231c12` | NEW (color). Approval-sheet gradient top. The sheet is the one heavy object in the app. |
| `--p-sheet-lo` | — | `#16110b` | NEW (color). Gradient bottom. |

Colour-typed tokens go 17 → 19; the count-pinned gates update RED-first (plan, O2).

**Resolved standing gap:** the focus ring, undefined since PB-DS-7 flagged its `#e2a33b`
documentation-chrome origin, is decided: **2dp `--p-hero` champagne**, the same "you" semantics
as everything else the accent touches.

**Terminal peek:** `terminal_peek.fg` stays bound to `--p-hero` and therefore becomes
champagne — an amber-phosphor terminal instead of a green-phosphor one. Both are historically
real CRT registers; the indexical grounding survives the warmth shift intact.

### D4. Material rules — glass without blur.

Obsidian's "glass" is simulated by composition, never by `RenderEffect` backdrop blur:

1. **Surfaces are gradients of linen over ground** (flattened to the solid ladder above for
   OLED predictability), with the 1dp top-edge key-light as the single light source. No
   surface may carry a second light direction.
2. **No drop shadows** (unchanged from Substrate); elevation is the ladder.
3. **Grain at 4%** SOFT_LIGHT, pre-rendered raster, checked in.
4. **The approval sheet** is the only vertical gradient (`--p-sheet-hi` → `--p-sheet-lo`) and
   the only surface with the strong 0.22-alpha top edge besides promoted slabs — the heaviest
   material in the app, reserved for the moment of decision.
5. **Backdrop blur is banned**, not deferred: it is the single most expensive effect on
   mid-tier hardware (cost scales with radius squared; measured 13x battery drain in
   comparable systems), it is invisible over a near-black ground anyway ("backdrop blur on a
   solid dark background produces a black rectangle"), and banning it keeps every API level
   identical. If a future skin wants real refraction it writes its own ADR.

#### Amendment to D4.3 (2026-08-08, agents-tracker-ksvb.3): the grain is content-anchored.

D4.3 states the grain's value, its blend and its provenance and says nothing about where the
overlay is attached. The 0.3.0 field test named what that silence shipped: **the grain was a
foreground on the screen scaffold's ROOT**, which does not scroll, while the destination inside
it does. A tiled noise field pinned to the window with the type sliding under it re-modulates
every glyph's antialiasing on every scroll frame — and at this app's 9.5–11 sp body sizes the
antialiasing ramp is most of a stroke, so the text visibly shimmers while the page moves. It is
invisible in a screenshot, which is why every gate and every verification image passed it.

**The grain is now attached to each part that moves, and to each part that does not, separately.**
In `PhoneScaffoldView` that is three overlays instead of one: the scrolled child (the destination),
the banner slot, and the tab bar. The tile, the opacity and the blend are unchanged — this
amendment moves no number and touches no token. What changes is that a glyph and the noise over it
now travel together, so the modulation each stroke receives is constant instead of per-frame.

**The QR exemption is unaffected and stays structural.** Row 21 exempts the pairing symbol because
4% soft-light noise on a 29-module code is a scan risk; `pairOnlyView` replaces the whole scaffold
rather than being hosted inside it, so every site named above is still inside the paired app.

### D5. Motion register — amends PB-DS-8.

PB-DS-8's decision "no decorative animation; only navigation affordances move, 350ms on
cubic-bezier(0.32, 0.72, 0, 1)" is amended, not discarded. The new register, implemented as
named constants in `ui/kit/Motion.kt` with `origin: ADR-009` annotations:

| Constant | Value | Rule |
|---|---|---|
| Entrance | 200ms, `cubic-bezier(0.22, 1, 0.36, 1)` | transform+opacity only; max travel **4dp** (larger travel visibly bounces on a dark ground). |
| Navigation (sheet, banner) | 300ms, same curve | replaces 350ms / (0.32, 0.72, 0, 1). |
| Toggle | 150ms | unchanged. |
| Streaming caret | 900ms period | unchanged; liveness signal, not decoration. |
| **Specular sweep** | 500ms one-shot, peak `rgba(255,252,244,0.30)`, skew -25deg | NEW named exception, see below. |
| Press feedback | <= 120ms to first visible response | slower reads as latency; audited, not animated. |

**The sweep is the one new exception to "no decorative animation", and it is not decoration:**
it fires exactly once, at the moment a session's Group becomes `NeedsInput`, along the top edge
of that session's slab. It is an attention signal in the same class as the caret's liveness —
Substrate's own rule "nothing glows unless it is alive" extends to "nothing sweeps unless it
just started asking". Constraints, each testable: one-shot (never loops); at most one sweep
animating per viewport (newest wins, others complete instantly); constructed only inside the
kit; collapses to nothing (not to a shorter sweep) under reduced motion. Field-register motion
(ambient loops, auroras, drifting gradients) remains banned.

### D6. Status semantics in a warm world.

The four-Group binding mechanism (PB-TOK-8) is untouched: `NeedsInput --p-att`,
`Working --p-work`, `ReadyForReview --p-ok`, `Completed --p-ink3`, machine-readably joined.
What changes is the meaning carried: champagne = the human (needs you, CTA, focus, live
counter, brand), patina teal = the machine working, sage = ready for review, warm grey =
receded. The four values remain pairwise distinct, so the "four distinct hues" property of
PB-TOK-8 holds; that `--p-att` now equals `--p-hero` is a *pairing* across sets, identical in
kind to the existing CTA alias, and the join keeps every row separate so a future skin can
break either alias in one line.

#### OPEN CONFLICT (2026-08-07, recorded by the fix pass — NOT resolved here)

**D2 and D1/D6 disagree about the status dot's glow, and the disagreement is real rather than a
transcription slip.** Both authorities are this ADR's, so no implementing session can pick one.

- **What D1/D6 keep.** The glow shares are a `color-mix()` the ORIGINAL design source writes:
  `docs/research/remote-control-design-directions.html:78-79` gives `.pdot.att` 70% of `--p-att`
  over transparent and `.pdot.wrk` 55% of `--p-work`. `docs/design/substrate-components.md:279`
  records them, `internal/design.Derivations()` computes them, and `Kit.groupGlow` spends them.
  D1 keeps the derivation mechanism unchanged and D6 keeps the Group binding unchanged; the app
  therefore glows NeedsInput at 70% and Working at 55% today.
- **What D2's maquette draws.** `.sdot.att { box-shadow: 0 0 9px rgba(201,168,118,0.5) }` — a flat
  50% literal, not a `color-mix()` — and **no `box-shadow` at all** on `.sdot.work`, `.sdot.ok` or
  `.sdot.done`. Its component-sheet legend annotates glow for NeedsInput only ("glow 9dp").

So the shipped app glows the Working dot, which the maquette never draws, at a share the maquette
does not state; and glows the NeedsInput dot at 70% where the maquette writes 50%.

**Three readings, and choosing between them is a design call:**

1. The maquette **under-drew** an effect it meant to keep — the working dot's glow is a liveness
   signal ("nothing glows unless it is alive") and dropping it would make a running agent look
   like a resolved one. The 0.5 is then a hand-typed approximation of the kept 0.70.
2. The maquette **retired** the working glow deliberately, and 50% is the new NeedsInput share.
   The dot's motion budget on near-black is real, and one glowing mark per viewport is a stronger
   reading of "at most one moving element" than two.
3. Both survive with the derivation re-pointed: the mechanism stays (D1), the SITES and SHARES
   move to the maquette (D2), which is the disposition `internal/design/derive_test.go` already
   flags for three of the four derivations as "ADR-009 D4's work, phase O3".

**Nothing changes until the owner picks one.** The app keeps the derivation, because that is what
D1 says is kept and it is the shipped behaviour; this note exists so the divergence is a recorded
question rather than a discovery waiting for the O7 device pass.

### D7. Typography.

Platform sans stays for UI text (no bundled display face — the narrow weight band 400/500 and
the tracking changes in D3 are the whole typographic move; a bundled grotesque is a separate
decision for a later ADR if ever). JetBrains Mono is bundled for the mono roles in phase O5,
which is already the recorded upgrade path for the box-drawing fallback defect; `tnum` +
`zero` + `calt` enabled wherever machine data renders. Type scale structure (19 styles, sp
units, colour-free styles) is unchanged.

**The count moved on 2026-08-09; the sentence stays as published (see
[ADR-012](ADR-012-type-ladder-consolidation-phase-1.md)).** "19 styles" is the record of what D7
did not change and it was true when D7 was written. ADR-012 takes the scale to **17**: it merges
`Label.CardHead` into `Mono.Meta`, which this decision's own two-face bundle made pixel-identical
by resolving 600 to 500, and deletes the dead `Label.StatusBar`. Everything else in the structure
sentence still holds — sp units, colour-free styles, and no size, weight, tracking, family or
feature string in this decision moves.

#### Amendment (2026-08-07, executed by phase O5)

The paragraph above names a font and a phase. This records what was actually bundled, where the
decision lands in the pipeline, and the one prediction in D3 that turned out to be unexecutable.

**What is bundled.** Two static faces from the `JetBrains/JetBrainsMono` v2.304 release, under
OFL-1.1, with the licence and authors files checked in beside them at `docs/design/fonts/`:

| release file | app resource | weight | sha256 |
|---|---|---|---|
| `JetBrainsMono-Regular.ttf` | `res/font/jetbrains_mono_regular.ttf` | 400 | `a0bf60ef0f83c5ed4d7a75d45838548b1f6873372dfac88f71804491898d138f` |
| `JetBrainsMono-Medium.ttf` | `res/font/jetbrains_mono_medium.ttf` | 500 | `31c92d01a8a08528b718a43addf0ad3df0af2ca4b7b3290a452f70f358e14d3d` |

joined by `res/font/jetbrains_mono.xml`, the family the styles name. **`--p-mono` substitutes to
`@font/jetbrains_mono`.**

**Two static faces, not the variable file, and not three.** The type scale asks the mono family
for 400, 500 and 600. Two static faces answer 400 and 500 exactly and resolve 600 to the nearest
(500, no synthetic bolding — the gap is under the 300-unit threshold that triggers it, and both
faces are 600/1000 em on every glyph, so nothing about the advance grid moves). The variable
`JetBrainsMono[wght].ttf` would cost roughly 60% more bytes to render one weight nobody asked for
at full fidelity. Bundling a third static face to serve 600 literally would cost another 273 KB
for a weight difference at 10 sp.

**The features.** Every mono style declares `android:fontFeatureSettings` = `tnum, zero, calt`,
and no sans style does. `tnum` gives digits one advance, so a live counter ticking 9 to 10 does
not reflow the line it sits in; `zero` slashes the zero, which is the one glyph pair a person
reading a session id has to disambiguate; `calt` is on by default in the family and is stated
anyway, because Android's `fontFeatureSettings` is a full override rather than an addition — two
features named without it would silently switch the family's contextual alternates off.

**THE TOKEN VALUE DOES NOT MOVE, and D3's row for `--p-mono` is corrected here.** That row
predicted "O5 prepends bundled JetBrains Mono" to the token's value in `internal/design/
tokens.json`. **It is unexecutable as written.** D2 makes the maquette the normative design
source, and `internal/design/tokens_test.go` joins `tokens.json` to the maquette's `:root` in
both directions with no exception mechanism — so prepending a family name to the JSON is a drift
failure, and editing the owner-signed maquette to satisfy a gate is the tail wagging the dog.

The bundling belongs one layer down and always did. PB-DS-3 exists precisely because the token
states a stack the platform cannot supply (`SF Mono`); the SUBSTITUTION table is what says what
Android renders for it, and ADR-007 B134 is the record of that decision. So `--p-mono` keeps its
maquette value, `android/gate/s22b_type_test.go`'s `s22bFontSubstitution` moves from `monospace`
to `@font/jetbrains_mono`, and the change flows JSON (unchanged) → substitution → `type.xml` →
resolved typeface, gated at every hop. ADR-007 B134 decision 2 carries the matching supersession
note; its sans half, `sans-serif` with zero bundled assets, is untouched.

**The defect is measured dead.** `MonoBoxDrawingTest` asserted the residual for as long as it was
real — box drawing resolving through fallback at 0.71em against the family's own 0.60em, an 18%
mismatch in the one place the app draws a frame. It now asserts the equality on the same
measurement at the same size, per character and across the string, and keeps the old inequality
as a control against `Typeface.MONOSPACE`: the platform family must still show the mismatch, or
the equality is passing for a reason nothing in this repository caused.

**The APK cost, measured rather than estimated.** 547,760 bytes of TTF on disk. The debug APK
goes **35,165,498 → 35,574,834 bytes: +409,336, or +1.16%** (`assembleDebug`, same machine, same
AAR, immediately before and after). The three `res/font/` entries account for 257,994 of that
compressed (`jetbrains_mono_regular.ttf` 273,900 → 128,157; `jetbrains_mono_medium.ttf` 273,860 →
129,597; `jetbrains_mono.xml` 540 → 240); the remaining ~151 KB is packaging overhead this
measurement does not decompose, and the honest number to quote is the whole-file delta.

**No subsetting.** The peek renders whatever an agent TUI emits, so a subset chosen against
today's screens is a tofu bug waiting for tomorrow's — and tofu is the failure mode this whole
decision exists to avoid. Revisit only if the release AAB's font contribution is ever the binding
constraint; at 1.16% of a debug APK dominated by an 11.8 MB native library, it is not.

#### Amendment (2026-08-08, agents-tracker-ksvb.3): `line-height: 1` transcribes as silence.

A transcription correction, not a design change: no size, weight, tracking or family moves, and
the design source is untouched.

The design states two label rules with a `/1` in the `font` shorthand — `.acts2 button`
(`600 13.5px/1`) and `.chip` (`600 11px/1`). The type join transcribed both by the same arithmetic
it applies to every other multiplier, `line-height x size`, and wrote `android:lineHeight` equal to
the text size into `Label.Button` and `Label.Chip`.

**That arithmetic is wrong for this one value.** CSS `line-height: 1` on a single-line label means
*no extra leading*. `android:lineHeight` is not a leading — it sets the line box's **absolute
height**, and a font's natural line box is taller than its em. Asking for a box exactly one em tall
makes the platform subtract the difference as a negative `lineSpacingExtra`, so the box shrinks
around the text: the CTA's label sat low inside its own button and the filter chip's descenders
clipped. Every other `/N` in the design (1.4, 1.45, 1.5, 1.55, 1.6) is larger than the natural box
and transcribes correctly; 1 is the only one that does not.

**The Android form of `/1` is to declare nothing**, which is also how the join already treats a
rule that states no line-height at all. Both readers of the join now say so, in both directions —
`android/gate/s22b_type_test.go` (`s22bNoExtraLeading`, with its own negative control) and the
Robolectric resolution test through `TypeScale.Spec.lineHeightPx`. The two `android:lineHeight`
items are removed.

What the readers asserted before this amendment:

```go
case spec.LineHeight != 0 && !declared:
    fault("PB-DS-2: %s declares no android:lineHeight; the design says %g x %gpx = %gsp",
        where, spec.LineHeight, spec.SizePx, spec.LineHeight*spec.SizePx)
```

```kotlin
val lineHeightPx: Float? get() = lineHeightMultiplier?.let { it * sizePx }
```

```xml
<item name="android:lineHeight">13.5sp</item>   <!-- Label.Button -->
<item name="android:lineHeight">11sp</item>     <!-- Label.Chip   -->
```

### D8. Quality gates added by this direction.

1. **A contrast gate** (`android/gate/` — new, RED-first): computes APCA lightness contrast for
   every ink-on-surface pair the join can derive (ink/ink2/ink3 x bg/card/elev/well, hero-ink
   on hero, err and hero as text on bg) and fails below Lc 75 for body-size roles, Lc 60 for
   large/display roles; non-text state indicators hold WCAG >= 3:1 against their adjacent
   surface. WCAG 2.x alone is known to false-pass ~49% of dark pairs, which is why APCA leads
   and WCAG certifies. The gate reads token values through the join, so it guards every future
   skin, not just this one.

   #### Amendment (2026-08-07, measured calibration)

   **The two floors above — Lc 75 for body, Lc 60 for large — were written before anything in
   this repository had ever measured a contrast number.** Phase O2 built the gate they ask for
   and it failed **twelve of sixteen** text pairs, on Obsidian **and on the Substrate palette
   that is live on the internal track today**. A threshold that fails the shipped product is not
   a finding about the shipped product; it is an unmeasured number, and this amendment replaces
   it with floors calibrated to what was measured, per role. **The inks do not move.** The
   maquette is the owner-signed ground truth (D2) and the palette it states is kept whole.

   The measurement, as phase O2 reported it (negative Lc is light ink on a dark ground, which is
   the correct polarity for every pair but the two champagne fills):

   ```
     --p-ink   x 4 surfaces:  Substrate -103.0..-102.3 | Obsidian -100.0..-98.7 | floor 75 | PASS both
     --p-ink2  x 4 surfaces:  -41.9..-41.1 | -49.7..-48.4 | 75 | fail both (Obsidian better by ~8)
     --p-ink3  x 4 surfaces:  -22.9..-22.1 | -25.6..-24.2 | 60 | fail both (Obsidian better by ~3)
     --p-hero-ink on hero/cta: +64.6 | +58.8 | 75 | fail both (Obsidian worse)
     --p-err on --p-bg:        -47.3 | -40.6 | 75 | fail both (Obsidian worse)
     --p-hero on --p-bg:       -63.8 | -57.7 | 75 | fail both (Obsidian worse)
   ```

   **THE CEILING FACT, which decides this.** A later verification swept every possible ink over
   the champagne fill `#c9a876` and found the **maximum reachable `|Lc|` on it is 59.73** (pure
   black; pure white reaches −49.05). The CTA carries its label on that fill. So the original
   two-floor model was **unsatisfiable by construction** for any palette with a mid-luminance
   accent fill carrying a label — no value of `--p-hero-ink`, in this skin or any future one,
   could ever have cleared Lc 75, and the pair misses even the large floor of 60 by 0.3. The
   defect was in this ADR's text, not in the palette.

   **The three options, adjudicated.**

   - **Re-light the maquette's inks and re-transcribe** — *rejected*. It compresses the
     owner-signed luminance hierarchy (a secondary ink at Lc 75 sits close to a primary at
     Lc 100, and the receding tertiary stops receding), and it does nothing for the two
     champagne pairs, which no ink can fix.
   - **Leave the gate permanently red as a quantified statement** — *rejected*. A forever-red
     gate teaches red-blindness; the next real regression arrives into a suite already failing.
   - **Per-role floors — a refined version of APCA's own conformance ladder, mapped to this
     app's actual type roles** — **chosen**, and written out below.

   **APCA's conformance ladder** (the standard's own rungs, not this app's):

   | rung | what APCA says it is for |
   |---|---|
   | Lc 90 | preferred for body prose |
   | Lc 75 | minimum for body text |
   | Lc 60 | content text and headlines |
   | Lc 45 | large-and-bold, and non-content text (button labels, placeholders, spot-read metadata) |
   | Lc 30 | absolute minimum for any text |

   **The app-role mapping, which is where the floors come from.** Each floor is the rung the role
   sits on, raised to the measured value where Obsidian already exceeds it — a floor set below
   what the palette achieves is a floor that permits a regression:

   | token / pair | app role | rung | **floor** | note |
   |---|---|---|---|---|
   | `--p-ink` on bg/card/elev/well | primary body prose | 90 | **90** | measures −98.7…−100.0; exceeds its own rung |
   | `--p-ink2` on the four surfaces | spot-read supplementary status text | 45 | **45** | the decision-carrying text in this app renders in `--p-ink` (sheet headline, well) or `--p-hero` (lit need line), **never** in ink2 |
   | `--p-ink3` on the four surfaces | incidental / de-emphasized | — | **24** | **see the named deviation below** |
   | `--p-hero-ink` on `--p-hero`, `--p-cta-bg` | CTA label, 14sp/500 | 45 | **55** | ceiling on this fill is 59.73; 55 is the achievable floor, not a comfortable one |
   | `--p-hero` as text on `--p-bg` | LIVE counter, links | 45 | **50** | |
   | `--p-err` as text on `--p-bg` | deny / revoke labels | 45 | **38** | **WATCH ITEM** — below its rung |
   | the four Group indicators + presence | non-text | — | **WCAG ≥ 3.0:1** | unchanged, already passing on Obsidian |

   **THE DEVIATION, named plainly rather than buried: `--p-ink3`'s floor of 24 sits below APCA's
   Lc 30 absolute minimum for text.** It is accepted on exactly two conditions, both standing
   rules from here on:

   1. **`--p-ink3` is never the sole carrier of required information.** It is the section label
      over a group whose rows state the same thing, the Completed group that has already
      resolved, and the offline presence dot that is also a shape change. If a screen ever needs
      ink3 to say something the user must read, the screen is wrong, not the floor.
   2. **The O7 device glance pass is the empirical backstop** for it, on a real panel at real
      brightness.

   **`--p-err` at 38 is an EXPLICIT WATCH ITEM.** The O7 device pass must confirm deny/revoke
   legibility. If it fails on device the **token lightens** — the ladder rule, D3 — and the floor
   does not move.

   **THIS AMENDMENT IS AN OWNER QUESTION AND IS MARKED OPEN.** It was written by the session that
   built the gate, against an ADR whose status line reads "Accepted (owner decision)", and two of
   the floors it sets are below the rungs the table above quotes. The ceiling argument stands on
   its own and needs nothing from the owner: |Lc| on `#c9a876` cannot exceed 59.73 for any ink, so
   the original Lc 75 was unsatisfiable by construction for the CTA label. That argument does
   **not** extend to `--p-ink2`, `--p-ink3` or `--p-err`, whose floors were set at what this
   palette happens to measure — so the gate can catch a regression from today's values and cannot
   catch a legibility failure, because after the calibration no pair fails. That is a defensible
   position and it is a **quality-bar decision the owner signed and only the owner can lower**.
   The O7 device glance pass is the empirical input it should be decided on. Until then this
   amendment is in force (a permanently red gate teaches red-blindness, which is the alternative
   this rejected) and it is recorded as unadjudicated rather than as settled.

   **Obsidian improves the hierarchy inks over Substrate** and the gate now records it as a
   requirement rather than a coincidence: ink2 goes −41.8 → −49.6 and ink3 −22.8 → −25.5, so the
   45 floor *fails the shipped Substrate palette* and passes Obsidian. The gate is therefore
   proof that this migration is an accessibility improvement, not merely a repaint. Obsidian is
   **worse** on the three accent pairs (hero-ink, hero-as-text, err-as-text), which is why those
   three are the watched ones.

   **The two new effect tokens keep no TSV row, and that is correct.** `--p-lit-fx` and
   `--p-sweep-fx` are typed `effect`, the one kind with no `res/values` converter, exactly like
   the four effect tokens that preceded them; they live in `tokens.json` alone and the TSV header
   records the absence. Adding rows would break the join gate. Blessed.
2. **A sweep gate**: the four sweep constraints in D5, asserted over kit source (construction
   site, one-shot, single-concurrency, reduced-motion collapse).
3. **The existing pins move deliberately**: the skin-name pin, the 17-colour count (→ 19), and
   the design-source banner test are rewritten RED-first quoting their old assertions, per the
   house test-rewrite rule.

## Consequences

### Positive

- A visual identity no competitor occupies (warmth as premium; every dev tool on the canon
  list is cool-toned), rated evergreen by the taxonomy the research library provides —
  deliberately not a trend purchase.
- The champagne accent unifies five meanings (needs-you, CTA, focus, live, brand) that
  Substrate split across green and amber — one fewer colour to teach, and the accent's
  scarcity is what makes it read.
- The whole migration flows through the existing pipeline; zero new mechanism, zero new
  dependencies, no Compose, no blur APIs, identical behaviour across API levels.
- The standing focus-ring hole (PB-DS-7) closes.
- The contrast gate turns accessibility from a review item into a build failure, permanently.

### Negative / accepted risks

- **Brand divergence window**: the store icon, feature graphic and screenshots are phosphor
  green until phase O7 re-renders the Solid Wedge in champagne on warm ground. Internal-track
  users see a green icon launch a warm app during the migration. Accepted; the closed track is
  the audience.
- **Green association is spent.** Phosphor green carried "terminal" indexically; champagne
  carries "instrument warmth" instead. The amber-phosphor terminal peek keeps the CRT
  grounding. If field feedback reads the app as less "terminal", the response is the mono
  typography work of O5, not a colour retreat.
- **Near-black warm ladder is riskier on cheap panels** than Substrate's cooler one: the
  bg→card step (9/8/8 RGB) sits near the 6-bit floor. Mitigated by the hand-tuning rule in D3
  and the dimmed-state device check in the plan's QA protocol (O7); if a panel crushes the
  step, card lightens one notch — the ladder is the tunable, the direction is not.
- **27 token values changing at once re-opens visual QA on every screen.** That is what the
  maquette (D2) and the phased plan exist to absorb.

## Alternatives considered

- **Stay Substrate, sharpen it.** The null option. Rejected by the owner after the six-way
  exploration: Substrate is competent Linear-school work, and that school is the uniform of
  the category — it cannot read as "designed for me" because everyone owns it.
- **Blockwork** (transcript-as-blocks): strongest structural idea, but it changes the wire
  contract (typed block segmentation from the Go core), which is a product-architecture ADR,
  not a skin. Explicitly out of scope here; nothing in Obsidian forecloses it later.
- **Press / Vitrine / Blueprint / Daylight**: documented with mockups in the exploration
  artifact; not chosen. One Press idea is adopted into the plan as a UX quality item (the
  blocking question typeset as the approval sheet's headline — it fits any skin); Daylight's
  inversion is parked with light mode in Phase C.

## Amendments ledger (executed by plan phase O0)

- ADR-007-remote-access **B3**: superseded in part — direction Substrate→Obsidian; regime kept.
- **PB-TOK-2** decision cell: skin name and design-source pointer.
- **PB-DS-5**: effect implementations re-parameterised (key-light alpha, grain 4%, champagne
  bloom, workbar hue); two effects added (lit key-light, sweep); mechanisms unchanged.
- **PB-DS-8**: motion decision replaced by D5's register; the "no decorative animation"
  principle survives with one named, gated exception.
- **PB-DS-7** table: focus ring row resolves from "undefined" to `--p-hero` 2dp; scrim/grab
  handle derivations re-point at Obsidian values.
- **PB-TOK-5 / PB-TOK-8**: counts (17→19) and the att=hero alias note.
