# Obsidian phase O2 — token migration through the pipeline, and one open blocker

ADR: [ADR-009](../adr/ADR-009-obsidian-visual-direction.md) · plan:
[obsidian-migration-plan.md](../specifications/obsidian-migration-plan.md) phase O2 · 2026-08-07

**Status: the migration is complete and green. The contrast gate ADR-009 D8.1 asks for is
landed and RED, and it cannot be made green from inside this phase. The phase does not close
until an owner rules on the blocker in section 4.**

---

## 1. What flowed, and what decided it

The whole skin change is a value flow. 27 token values moved and 4 tokens were added, all
transcribed from the `:root` block of `docs/research/obsidian-maquette.html`, which ADR-009 D2
makes the normative design source. Nothing downstream was chosen by hand; every number below
landed because a gate demanded it after `internal/design/tokens.json` moved.

| Artifact | What moved | What made it move |
|---|---|---|
| `internal/design/tokens.json` | 27 values, +4 tokens, `skin` and `source` | the maquette's `:root` |
| `android/design-tokens.tsv` | +2 rows (sheet gradient stops); zero row edits | the join records the JOIN, not values |
| `values/colors.xml` | 17 colours repainted, 2 added (17 → 19) | PB-TOK-1 both directions |
| `values/dimens.xml` | radii 9/14/9 → 14/18/10 | PB-DS-4 |
| `values/type.xml` | 4 styles: weight 650 → 500, tracking → +0.01 / −0.015 | PB-DS-2, through `var()` resolution |
| `SwarmTheme.EXPECTED_DARK_COLORS` | 3 recorded literals | PB-TOK-1's third-copy fence |
| `Kit.kt` | `KEY_LIGHT_ALPHA` 0.045 → 0.10, `CTA_BLOOM_ALPHA` 0.20 → 0.22 | PB-DS-7's metric join |
| `Surfaces.kt` | key light `Color.WHITE` → `--p-ink` linen; well radius card → button | Robolectric appearance suite |
| `icon-candidates/solid-wedge.svg` | 2 colours; **geometry byte-for-byte unchanged** | the launcher-drawable gate |
| 5 derived colours | `#6D5220` → `#66553D` and four more | `Derivations()` resolving over new tokens |

Three coincidences were exposed by the migration and are recorded here because each had been
invisible while it held:

1. **The well's radius.** Substrate set `--p-card-r` and `--p-btn-r` to the same 9px and the CSS
   wrote a literal 9px, so three numbers agreed and nothing could say which the well spent. It
   spent the card's. The maquette settles it: `.well { border-radius: var(--p-btn-r) }`.
2. **The key light's RGB.** `Color.WHITE` was correct *and* literal-free while `--p-card-fx` was
   a translucent white. Obsidian's is linen, and a white key light over a warm ladder is exactly
   the cool contamination the direction removes.
3. **An alpha rounding.** A control computed its expected alpha with `.toInt()` (truncates) while
   `ColorMix` rounds. At 0.045 both give 11; at 0.10 the product is exactly 25.5 and they split.

## 2. Gate results

| Gate | Result |
|---|---|
| `go build ./...` | green |
| `go vet ./...` | green |
| `golangci-lint run` | green, no new findings |
| `go test ./...` | green **except** `TestADR009D8_EveryInkOnSurfacePairClearsItsAPCAFloor` (section 4) |
| `./gradlew --no-daemon test` | **green** — 970 debug + 970 release, 0 failures, 124 result XML files per variant |

Zero screen-code changes. Nothing under `ui/screens` was touched; the two `ui/kit` edits are
`origin:`-annotated theme metrics. The app renders warm by value flow alone.

## 3. The contrast gate, ADR-009 D8.1

`android/gate/obsidian_contrast_test.go` implements APCA (SAPC screen-polarity Lc) over the join
— a colour is only judged if it reaches the app through `tokens.json` → `design-tokens.tsv` →
`colors.xml` — plus WCAG 2.x for the non-text indicators. It reproduces the published APCA-W3
0.1.9 anchors: black on white **+106.04**, white on black **−107.88**, deliberately not each
other's negation.

**The WCAG half passes on Obsidian.** Every state indicator clears the 3:1 non-text minimum
against both surfaces it is ever drawn on:

| indicator | on `--p-bg` | on `--p-card` |
|---|---|---|
| `--p-att` | 8.73:1 | 8.22:1 |
| `--p-work` | 7.23:1 | 6.81:1 |
| `--p-ok` | 8.07:1 | 7.60:1 |
| `--p-ink3` | 3.74:1 | 3.52:1 |

**The APCA half fails, on Obsidian and on shipped Substrate alike.** Negative Lc is light ink on
a dark ground, which is the correct polarity for every pair but the two champagne fills.

| pair | Substrate | Obsidian | floor | verdict |
|---|---|---|---|---|
| `--p-ink` × 4 surfaces | −103.0 … −102.3 | −100.0 … −98.7 | 75 | **pass both** |
| `--p-ink2` × 4 surfaces | −41.9 … −41.1 | −49.7 … −48.4 | 75 | fail both (Obsidian better by ~8) |
| `--p-ink3` × 4 surfaces | −22.9 … −22.1 | −25.6 … −24.2 | 60 | fail both (Obsidian better by ~3) |
| `--p-hero-ink` on hero / cta-bg | +64.6 | +58.8 | 75 | fail both (Obsidian worse) |
| `--p-err` on `--p-bg` | −47.3 | −40.6 | 75 | fail both (Obsidian worse) |
| `--p-hero` on `--p-bg` | −63.8 | −57.7 | 75 | fail both (Obsidian worse) |

Twelve of sixteen text pairs fail. **This is not an Obsidian regression** — the same twelve fail
on the palette that is live on the internal track today, and Obsidian is *better* on eight of
them. It is a property of both palettes that nothing in this repository had ever measured, which
is precisely what ADR-009 D8.1 predicted the gate would be for.

## 4. BLOCKER — owner decision required

The rule is: if a pair fails, the **token** moves, never the threshold (ADR-009 D3 declares the
ladder the tunable; the plan's O2.4 says the same in as many words). That fix is **not available
from inside this phase**, and the reason is structural rather than a matter of nerve:

- moving `--p-ink2`, `--p-ink3`, `--p-err` or `--p-hero-ink` in `tokens.json` breaks the
  drift-guard against the maquette's `:root`, which is a hard failure by design;
- and the maquette is the **signed** design source, so editing it to match is the one thing this
  phase is forbidden to do.

So the token cannot move without the maquette moving, and the maquette is an owner artifact.

### 4a. Two of the twelve are not the palette at all — the floor is unreachable

A later pass measured the thing the first one assumed: a surface has a **contrast ceiling**, the
largest `|Lc|` any ink can reach on it, and `apcaCeiling` in the gate computes it.

```
apcaCeiling(--p-hero #c9a876) = 59.7          body floor 75
```

The best ink that can exist on the champagne fill is pure black at `Lc 59.7`; pure white gives
`-49.1`. So `--p-hero-ink` on `--p-hero` and on `--p-cta-bg` fail the body floor for **every value
`--p-hero-ink` could ever take** — Obsidian's `#1a1206` at 58.8, Substrate's `#06150c` at 64.6, and
every colour nobody has drawn. They miss even the *large* floor of 60, by 0.3. Any surface whose
own luminance sits in the middle has a low ceiling; that is a property of APCA, not of this skin.

Two consequences, and the second is the one that matters:

1. **"Move the token" is not a remedy for those two pairs.** Option (A) below cannot fix them.
   The gate now says so in its own failure text rather than repeating the generic advice, so the
   next reader is not sent on a search with no answer in it.
2. **ADR-009 D8.1's two-rung model is what is failing there.** A design system with an accent fill
   that carries a label cannot satisfy a flat Lc 75 for it, ever. That is a defect in the ADR's
   text, and it is an owner's line to change.

The other ten failures are unaffected: their grounds are near-black, their ceilings are 106–108,
and an ink that fails on one of them really is an ink that has to move.

### 4b. The ways out

Three, in the order they preserve the ADR's intent:

**(A) Re-light the maquette's inks, then re-transcribe.** The honest fix for the ten pairs it can
reach. Against `#0e0b08` a neutral ink first clears Lc 60 at `#b2b2b2`, Lc 75 at `#cccccc` and
Lc 90 at `#e4e4e4`; `--p-ink2` is `#a69d8e` and `--p-ink3` is `#746b5d`, so both travel a long way
up the ladder. That is a visible design change to a signed artifact and therefore the owner's
call, not this phase's, and it would compress the ink hierarchy the direction depends on — a
secondary ink at Lc 75 sits close to a primary at Lc 100. **It does nothing for the two champagne
pairs** (section 4a).

**(B) Amend ADR-009 D8.1 with APCA's own conformance ladder instead of two rungs.** The current
text assigns floors to sizes and this gate assigns roles to *ink tokens*, which is an
approximation: `--p-ink3` carries `Label.Section` at 10.5sp and `Mono.Agent` at 10sp — small text,
not large — so calling it "large-only" is generous to it rather than strict. APCA itself states
more rungs than two: Lc 90 preferred for body prose, 75 minimum for body, 60 for content text and
headlines, 45 for large-and-bold and for non-content text (button labels, placeholders, spot-read
metadata), 30 absolute. Under that ladder the champagne CTA label at 59.7 clears a 45 floor as a
large, bold, non-prose role and stops being an unsatisfiable requirement, while `--p-ink2` at 49.6
and `--p-ink3` at 25.5 still fail — so the gate stays honest and stays red on the inks that carry
prose. This is the only option that addresses section 4a, and it buys an owner's signature for the
thresholds instead of a subagent's.

**(C) Close O2 with the gate red.** What this evidence file currently records. The migration is
complete and every other gate is green; the contrast gate stands as a permanent, quantified,
un-ignorable statement about the palette.

**Nothing here lowers a floor and nothing here edits the maquette.** Both were available and both
were refused.

## 5. Not done in this phase

- **Device screenshot set** (the plan's O2.5 asks for the PB-E2E-2 shots in Obsidian). No handset
  is reachable from this environment; the Robolectric suite is what stands behind the appearance
  claims above. Carry to O3, which re-opens the same screens.
- **The `--p-lit-fx` and `--p-sweep-fx` effects have no consumer yet**, by design: they are typed
  `effect`, which has no `res/values` primitive, so they deliberately have no join row. O3 and O4
  give them Kotlin homes with their own gates (ADR-009 D8.2).
- **Three of the maquette's paddings were off the PB-DS-1 scale, and the orchestrator corrected
  them** (`38046c1`): `.sheet` 20px → 18px, `.empty` 26/30px → 24px. Found by this phase, fixed in
  the design source rather than by loosening the scale, which is the order the regime requires.

## 6. The spacing readers now read the maquette; two things deliberately still do not

With the maquette on the scale, `TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale` was retargeted
at it (RED first, then the table and the ledger rewritten quoting what they replaced). The
outcome is a stronger record than Substrate could give:

| | Substrate artifact | Obsidian maquette |
|---|---|---|
| distinct spacing values | 14 | 17 |
| movers | 6, plus a 7th (26→24) that lived in an artifact the gate never read | **7, all by 1dp** |
| worst drift | 1dp | 1dp |
| steps absorbing nothing | `swarm_space_6`, `swarm_space_24` — justified only by screens not yet built | **none** |

Both promissory steps are now spent by a design that exists: 6px on the badge, the chip gap, the
field label and the stale notice; 24px on the nav, the drill header, the tab bar's bottom inset
and the empty state. The one new literal, 3px, is an equidistant tie and is recorded as one — it
is the activity row's body gap and the maquette's two other sub-label gaps are both 2px, so it
joins them.

The reader also gained an **at-rule strip and a nesting assertion**, because it needed one. The
rule regexp is flat; fed the maquette's `@keyframes sweep { 0% { … } }` in the middle of the kit
block it matched the inner block and walked out of phase, silently attributing `.empty { padding }`
to a value three rules away. That is how the first reading of this block reported spacings the file
does not contain. At-rules are now removed whole and residual nesting is a hard failure.

**Three readers stay on the older directions artifact, each for a stated reason** (recorded in the
gate at `s22bMaquetteRelPath`, not only here):

| reader | why the maquette cannot serve it |
|---|---|
| the three frame constants (54/76/74) | handset geometry, not skin values — ADR-009 D1 keeps them by name. The maquette draws a 300px gallery phone with no OS chrome and no fixed-height bar, so it states none of the three. |
| the type ladder (PB-DS-2, 19 styles) | the maquette's sizes are a redraw (nav title 22px against 27px). Re-pointing type.xml's origins moves nineteen font sizes; ADR-009 D3 changes weight and tracking and **no size**, and the plan gives O5 the verification of the styles against the maquette. A type-scale change inside a token migration is the defect this regime exists to prevent. |
| the four tab glyphs (PB-DS-6) | the maquette's tab bar is four labels and draws no `<svg>`. ADR-009 moves material, not geometry; the paths are unchanged. |

The type ladder is the one worth a bead: the maquette specifies sizes the app does not yet render,
and O5 is where that is reconciled.
