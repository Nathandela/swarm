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

### D7. Typography.

Platform sans stays for UI text (no bundled display face — the narrow weight band 400/500 and
the tracking changes in D3 are the whole typographic move; a bundled grotesque is a separate
decision for a later ADR if ever). JetBrains Mono is bundled for the mono roles in phase O5,
which is already the recorded upgrade path for the box-drawing fallback defect; `tnum` +
`zero` + `calt` enabled wherever machine data renders. Type scale structure (19 styles, sp
units, colour-free styles) is unchanged.

### D8. Quality gates added by this direction.

1. **A contrast gate** (`android/gate/` — new, RED-first): computes APCA lightness contrast for
   every ink-on-surface pair the join can derive (ink/ink2/ink3 x bg/card/elev/well, hero-ink
   on hero, err and hero as text on bg) and fails below Lc 75 for body-size roles, Lc 60 for
   large/display roles; non-text state indicators hold WCAG >= 3:1 against their adjacent
   surface. WCAG 2.x alone is known to false-pass ~49% of dark pairs, which is why APCA leads
   and WCAG certifies. The gate reads token values through the join, so it guards every future
   skin, not just this one.
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
