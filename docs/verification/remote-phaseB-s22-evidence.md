# S22 evidence — the design system foundation

**Requirements owned: PB-TOK-5, PB-TOK-6, PB-TOK-7, PB-TOK-8, PB-DS-1, PB-DS-2, PB-DS-3,
PB-DS-4.** Eight, each with a section below. **PB-DS-5** is the slice's ninth row in the manifest
and is **not evidenced here**; §9 says why, and it is the reason this slice cannot close as
written.

**Decisions**: ADR-007 **B134** — all four. Decision 1 (the `ReadyForReview` rebinding) is
PB-TOK-8; decision 2 (the fonts) is PB-DS-3; decision 3 (no decorative animation) is S23's;
decision 4 (`minSdk 33`, elevation stays banned) underwrites PB-DS-2's weight 650 and PB-DS-5.

**Commits**: `99f131a` (the slice), `a323c87` and `af246df` (two gate defects found by
self-review after it).

| | |
|---|---|
| `go build ./...` | green |
| `go vet ./...` | green |
| `go test ./internal/design/` | green — 8 tests for PB-TOK-6/-7 |
| `go test ./android/gate/ -run 'TestPBTOK1\|TestPBTOK[5-8]\|TestPBDS[1-4]'` | green — **26 tests** |
| `./gradlew --no-daemon :app:testDebugUnitTest --tests 'dev.swarm.phone.theme.*'` | green — **15 tests** |
| `golangci-lint run` | **not run.** Not installed on this host. `gofmt -l` is clean on all S22 Go files. |
| `go test ./android/gate/` (whole package) | **RED as of 2026-08-01 at `af246df` — 4 failures, none of them S22's.** |
| `./gradlew --no-daemon test` (whole module) | **RED as of 2026-08-01 at `af246df` — 10 failures, none of them S22's.** |

**Read that last pair carefully rather than as a caveat, and read it as a claim about a moment.**
S23 is building `ui/kit/` in the same tree and was mid-flight when this was written. At `af246df`
the failures were `TestPBDS6_EveryInboxComponentIsAKitFactory`,
`TestPBDS6_EveryKitSpacingIsTheLedgersStep`, `TestPBDS7_EveryDerivationCitationResolvesToARow`,
`TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber`, plus `KitFoundationTest` (8) and `MotionTest` (2).
Every one is an S23 requirement asserting against components that did not exist yet. **No S22 test
was among them**, which the scoped commands above establish by running the S22 set alone.

Those two rows are **expected to go green** as S23 lands, and they are dated rather than left
standing because a whole-suite figure is the kind of claim that rots: ADR-007 B97 records this
project doing exactly that once — a `--strict-section11` row asserted about a commit and left
reading as a claim about HEAD, which stopped being true and went on being read for days. The rows
that are *not* dated, because they are properties of this slice rather than of the tree, are the
four scoped commands above them. A reader who finds an S22 test among the failures has found this
paragraph to be wrong, which is the point of naming them individually.

## What this proves

That the token supply chain, which was verified and under-fed, now reaches the app. Sixteen of
sixteen colours have a resource; every token declares a kind and a kind with no converter fails the
gate instead of being skipped; the four `color-mix` derivations are computed by one blend function
rather than transcribed; the four session Groups — including `ReadyForReview`, which Substrate never
coloured — are bound to tokens by a checked-in table joined bidirectionally against the real
constants in `internal/status`. And the two scales the design never declared exist: a 2dp spacing
grid with its rounding ledger checked against the artifact, and eighteen named text styles whose
every size, weight, tracking, family and line height is **computed from the design source at test
time** rather than transcribed.

That last property is the one worth auditing hardest, because it is the one the previous slice got
wrong. `docs/research/remote-control-design-directions.html` is staged as a unit-test resource and
parsed by both a Go gate and the Robolectric suite. A test that recorded `27sp` because `type.xml`
says `27sp` would certify that the app renders whatever `type.xml` says — which is exactly what it
would do if `type.xml` were wrong, and is precisely how `colors.xml` drifted to a third divergent
palette with its own test green throughout.

## What it does NOT prove

**Nothing here proves the app looks right.** Every assertion in this slice is over a *resolved
resource value* — an ARGB int, a dimension in dp, a typeface's advance width, a letter-spacing
float. Not one is over a rendered pixel. There is no screenshot corpus, no golden-image comparison
and no layout assertion. The class of defect this evidence cannot catch is therefore large and
specific: a component that reads every correct value and positions it wrongly; two views that
overlap; text that clips at its container; a scale entry applied to the wrong edge; a colour
correct in isolation and illegible against the surface it lands on. **A screen can satisfy every
test in this file and be unusable.**

**And there is no emulator run behind it either.** PB-E2E-2 was re-scoped to physical hardware on
2026-07-30 (ADR-007 B91) because the emulator keymaster reports `SECURITY_LEVEL_SOFTWARE` and
PB-KEY-8's hardware-downgrade refusal fails closed **before any screen renders** — so the app cannot
start on an emulator at all, by construction and correctly. The first time any of this is seen by a
human eye will be on a handset, under PB-E2E-5. Contrast checking (PB-DS-12) and touch targets are
not in this slice either.

**The Robolectric half runs against AOSP's font configuration**, which is what a stock handset
ships, not a survey of OEM customisation. §7 depends on that and states it again where it matters.

## Failing-first evidence (GG-5)

| Requirement | RED evidence |
|---|---|
| PB-TOK-5, PB-TOK-6 (gate half) | `docs/verification/remote-phaseB-s22a-red/pbtok5-colour-join-red.txt` |
| PB-TOK-6 (origin half) | `docs/verification/remote-phaseB-s22a-red/pbtok6-kinds-red.txt` |
| PB-TOK-7 | `docs/verification/remote-phaseB-s22a-red/pbtok7-derivations-red.txt` |
| PB-TOK-8 | `docs/verification/remote-phaseB-s22a-red/pbtok8-group-bindings-red.txt` |
| PB-DS-1..4 (Go) | `docs/verification/remote-phaseB-s22b-red/gate-red.txt` |
| PB-DS-1..4 (Kotlin) | `docs/verification/remote-phaseB-s22b-red/kotlin-red.txt` |
| PB-DS-1..4 and the PB-TOK-5 seam | `docs/verification/remote-phaseB-s22b-red/negative-controls.txt` — **16 mutations** (12 against the Go gates, K1–K2 against the Robolectric chain, T1–T2 against `ThemeTokenOriginTest`) |
| provenance | `docs/verification/remote-phaseB-s22b-red/README.md` |

Every file contains real failure output with values, not a claim that a run happened. Each is dated
and carries the HEAD it ran against. Where a test **passes** in a RED file, the file says which one
and why — they are the negative controls and the design-source guards, green before and after, in
the style `TestPBTOK1_TheComparisonCanActuallyFail` established.

Two provenance notes, stated because an auditor should not have to infer them:

- The S22b RED files are a **re-run of the final assertions** against a tree with `dimens.xml` and
  `type.xml` removed. The tests were written and run before the implementation existed; the re-run
  records the wording the assertions ship with rather than an earlier draft's. `PhoneSurface.kt` was
  reverted to its committed form for those runs because it references `R.dimen.swarm_space_24` and
  the module would not otherwise compile — **a build failure is not a failing test.**
- The PB-TOK-7 RED is in two stages for the same reason, and its own header says so: stage 1 is a
  compile error, which proves only that a symbol is absent, so stage 2 records the assertions
  failing *with values* against a first implementation.

**Three defects in the tests themselves were found by the first RED run**, and are recorded here
because they are the loop working rather than embarrassments to bury:

1. **The spacing scale is a recorded assignment, not an arithmetic.** The first
   `TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale` rounded each design literal to the nearest
   step. That cannot reproduce the decision: `5px` is equidistant between 4 and 6 and is absorbed
   **down**; `7px` is equidistant between 6 and 8 and is absorbed **up**. No tie-break rule yields
   both. The test reported `nearest(7) = 6` and the assignment became an explicit table.
2. **`swarm_space_24` absorbs nothing in Substrate.** The gate's reverse direction — a step may not
   claim a literal the design does not declare — reported that the artifact contains no 24px
   spacing value at all. `swarm_space_6` is in the same position. Both are kept, both are justified
   in `dimens.xml`, and both are asserted empty so "unused" cannot decay into "unjustified".
3. **The mono-rule count was 9, not the 11 that had been guessed.** `.panelframe .cap` looks like a
   tenth and is not: it names `var(--mono)`, which no skin declares.

---

## 1. PB-TOK-5 — every colour token reaches the app

> **Acceptance criterion.** "All 16 colour tokens have a row in `android/design-tokens.tsv` and a
> `<color>` in `colors.xml`; the existing bidirectional gate (unmapped `<color>` fails, divergent
> value fails) is unchanged and now covers 16 rows."

**Built.** `android/design-tokens.tsv` carries 16 data rows; `colors.xml` declares 16 `<color>`
resources. `--p-cta-bg` and `--p-cta-ink` are byte-identical aliases of `--p-hero`/`--p-hero-ink`
and are mapped **separately**, as the requirement's parenthetical demands, so a future skin that
breaks the alias cannot pass a join that deduplicated them.

**Tests.** `TestPBTOK5_EveryColourTokenReachesTheApp` (`android/gate/s16_tokens_test.go`), plus the
pre-existing bidirectional gate now running over 16 rows:
`TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens` and
`TestPBTOK1_TheThemesRecordedColoursComeFromTheOrigin`.

**Negative controls.** `TestPBTOK1_TheComparisonCanActuallyFail` — mutates one hex digit and
requires the comparator to report it. It is labelled in its own source as green-now-and-after, and
no evidence line counts it among the assertions this slice earned.

**One assertion had to change to admit the other thirteen colours, and it was strengthened rather
than relaxed.** `ThemeTokenOriginTest` compared `SwarmTheme.EXPECTED_DARK_COLORS.size` against the
size of the *whole* token join — true only while the join happened to hold three rows, and a
coincidence rather than an invariant: that constant is one entry per themed **attribute**
(`colorBackground`, `textColorPrimary`, `textColorSecondary`), while the join is every colour the app
owns. Widening to sixteen made it read 16 vs 3 and fail for the wrong reason.

It is now **positional correspondence**: entry *i* must equal the origin's value for the resource
attribute *i* binds to. That is strictly stronger than what it replaced — the containment loop
beside it only says each recorded colour is *some* mapped token's value, so transposing background
and text-primary passes it while the theme paints text in the background colour. Mutations **T1**
(transposition) and **T2** (the comparison neutered) prove both directions. T2 is the one that
matters structurally: the control now feeds its transposed palette to the **same function** the
requirement calls, rather than to a reimplementation of the comparison, so a check that had been
normalised, short-circuited or reduced to a self-comparison can no longer stay certified.

**MET.**

## 2. PB-TOK-6 — the non-colour tokens get a typed conversion path

> **Acceptance criterion.** "`tokens.json` gains a `kind` per token
> (`color`/`dimen`/`font`/`weight`/`tracking`/`effect`); the TSV join grows a kind column; radii
> land in `values/dimens.xml`, tracking as `em` floats, weight as `textFontWeight`. A token whose
> kind has no converter fails the gate rather than being skipped."

**Built.** `internal/design/tokens.json` gains a `kinds` object covering all 31 tokens.
`design-tokens.tsv`'s columns are now `token / android_resource / kind / note`, and the kind
declared there must equal the origin's — the origin is upstream and wins. The gate dispatches per
kind and **fails on a kind it has no converter for**, which is what replaced the old behaviour of
sniffing the value and refusing anything without a `#`. That sniffing was wrong about `--p-tabbg`
(`rgba(8,9,10,0.88)` *is* a colour) and could not distinguish `--p-grain` `0.05` from
`--p-display-wt` `650`.

The three destinations the criterion names are delivered by this slice's other half and are
cross-referenced rather than restated: radii in `values/dimens.xml` (§8), tracking as `em` floats
and weight as `android:textFontWeight` in `values/type.xml` (§6).

**Tests.** `TestPBTOK6_EveryTokenDeclaresAKindAndTheKindMatchesTheValue`
(`internal/design/tokens_test.go`), `TestPBTOK6_AKindWithNoConverterFailsTheGate`
(`android/gate/s16_tokens_test.go`).

**Negative controls.** `TestPBTOK6_TheKindCheckCanActuallyFail` (the classifier) and
`TestPBTOK6_TheDimenConverterCanActuallyFail` (the px→dp conversion).

**MET.**

## 3. PB-TOK-7 — derived values are computed, never transcribed

> **Acceptance criterion.** "Every derived colour is produced by a single documented blend function
> over token inputs; a gate asserts no Kotlin or XML literal equals a derivation's output, and that
> changing a base token moves the derived value."

**Built.** `internal/design/derive.go` — one `Mix(x, fraction, y)` over token inputs, and four named
derivations: `attention-row-border`, `deny-fill`, `needs-input-dot-glow`, `working-dot-glow`. The
blend is the **premultiplied** form, which is the one CSS `color-mix(in srgb, …)` specifies;
interpolating un-premultiplied gets the alpha right and the hue wrong, and still reads as "a dimmer
version of the token" in a diff, which is what makes it worth a test rather than a comment.

**Tests.** Six in `internal/design/derive_test.go` —
`TheFourArtifactDerivationsAreComputedFromTheTokens`,
`MixingWithAColourBlendsRGBAndMixingWithTransparentScalesAlpha`, `ChangingABaseTokenMovesTheDerivedValue`,
`EveryDerivationNamesRealTokens`, `TheColourCodecRoundTrips`, `TheBlendCanActuallyFail`. Three in
`android/gate/s22_derived_test.go` — `NoShippedLiteralIsADerivationsOutput`,
`TheDerivationsAreReachableFromTheOrigin`, `TheLiteralScanCanActuallyFail`.

**Negative controls.** `TestPBTOK7_TheBlendCanActuallyFail` and
`TestPBTOK7_TheLiteralScanCanActuallyFail`. The second matters most: "no literal equals a
derivation's output" is trivially true of a scanner that matches nothing, which is the failure this
requirement's own text warns about — the third-copy defect "one indirection further out where the
existing gate cannot see it".

**MET.**

## 4. PB-TOK-8 — the four Groups are bound to tokens, machine-readably

> **Acceptance criterion.** "The Group→token mapping is a checked-in table joined bidirectionally
> to both `status.Group` and the theme, in the style of `design-tokens.tsv`. A Group with no token,
> or a token bound to two Groups, fails. Recorded in the ADR."

**Built.** `android/group-tokens.tsv`, columns `group_const / group_value / token / note`. Both
Group columns are checked against the **real constants parsed out of `internal/status`**, not
transcribed — so a fifth Group cannot enter the status model without this table being updated with
it. The binding is B134 decision 1: `NeedsInput → --p-att`, `Working → --p-work`,
**`ReadyForReview → --p-ok`**, `Completed → --p-ink3`.

The rebinding is the substantive decision, not the table. Substrate's demo labelled the green dot
"Done"; this moves green to `ReadyForReview` and gives `Completed` the recessive tertiary ink,
which is what swarm's own TUI identity already does and what a triage surface needs — finished work
should recede rather than hold the most saturated colour on screen. Zero new tokens, four distinct
hues.

**Tests.** Four in `android/gate/s22_grouptokens_test.go` —
`EveryStatusGroupIsBoundToExactlyOneToken`, `EveryBoundTokenReachesTheTheme`,
`TheRebindingRecordedInTheADRIsTheOneInTheTable`, `TheGroupParserCanActuallyFail`.

**Negative control.** `TestPBTOK8_TheGroupParserCanActuallyFail` — proves the parser reads the four
real Group constants out of `internal/status` and does **not** sweep up the Process/Turn/Interaction
constants beside them. Without it, every other assertion in the file is built on a parser nobody
checked.

**MET.**

## 5. PB-DS-1 — a declared spacing scale

> **Acceptance criterion.** "`values/dimens.xml` carries the scale; a gate asserts no layout
> dimension in Kotlin is a raw literal — every padding, margin and gap resolves to a scale entry or
> to a frame constant. The current `PADDING = 24` raw-pixel constant is deleted (it is px, not dp,
> and renders at ~8dp on a 3× handset)."

**Built.** Ten steps (2, 4, 6, 8, 10, 12, 14, 16, 18, 24 dp), three frame constants
(`swarm_screen_top` 54dp, `swarm_screen_bottom` 76dp, `swarm_tabbar_height` 74dp), and the deletion.
`PhoneSurface.kt` now reads `resources.getDimensionPixelSize(R.dimen.swarm_space_24)`.

The frame constants are **read from the design source**, not from the requirement's prose —
`.pscreen { padding: 54px 0 76px }` and `.ptabs { height: 74px }` — because prose is where a number
gets retyped. The absorption ledger is computed on every run and asserted exactly: six of fourteen
Substrate spacing values move, each by 1dp (`5→4, 7→8, 9→8, 11→10, 13→12, 15→14`). The
requirement's ledger says "seven ... one by 2dp (26→24)"; 26px is a **mock-only** literal outside
this artifact, and the gate's message says so rather than leaving the counts unreconciled.

**Tests.** Five in `android/gate/s22b_spacing_test.go` — `TheSpacingScaleIsDeclared`,
`TheFrameConstantsAreTheDesignsOwn`, `EveryDesignSpacingIsAbsorbedByTheScale`,
`TheAbsorptionLedgerCanActuallyFail`, `NoRawPixelPaddingSurvives`. Two in
`DesignScaleResolutionTest.kt` — `the frame constants resolve to the design's own frame`, `the
spacing scale resolves as an ascending 2dp grid`.

**Negative controls.** Mutations 10, 11 and 12 in `negative-controls.txt` (a frame constant moved, a
step off the 2dp grid, the deleted constant restored), plus K2 on the Robolectric side.
`TheAbsorptionLedgerCanActuallyFail` pins the two equidistant movers going opposite ways, so a
ledger that had silently become "nearest step" fails.

**PARTIALLY MET, and the shortfall is in the middle clause.** The gate asserts the *named* defect —
`PADDING = 24`, and any `const val …PADDING/MARGIN/INSET/GAP… = <int>` or `setPadding`/`setMargins`
call taking a numeric literal. It does **not** yet assert the criterion's full sentence, that *every*
layout dimension in Kotlin resolves to a scale entry. That fence is **PB-DS-11**, which was
reassigned S23 → S24 on 2026-08-01 because it forbids allowlisting existing violations and every
existing violation lives in the three surface files S24 rewrites. The narrow gate is deliberate and
its source says so; it is not a substitute for PB-DS-11 and does not claim to be.

## 6. PB-DS-2 — a typography scale of named styles

> **Acceptance criterion.** "`values/type.xml` defines `TextAppearance.Swarm.*` for all 18; a gate
> joins the style set to the recorded scale bidirectionally (an unlisted style fails, an
> unimplemented row fails). No `setTextSize`/`setTypeface`/`setLetterSpacing` call survives in
> surface code outside the theme."

**Built.** Eighteen styles, each carrying `android:textSize` (sp), `android:textFontWeight`,
`android:letterSpacing` (em, the one unit-identical row in the whole conversion),
`android:fontFamily`, and `android:lineHeight` on the seven whose CSS rule declares a line height.

Each style declares the CSS rule it descends from in a machine-read `<!-- origin: … -->` comment.
**That comment is the only decision in the file**; every number is read out of the design source and
compared. Line height is the design's multiplier times its own size, recomputed by the gate rather
than trusted, because Android has no unitless multiplier on a `TextAppearance`.

**Colour is deliberately absent from these styles**, and that is a scoping decision rather than an
omission — the criterion lists "colour token" among the six attributes. One CSS rule, `.acts2
button`, renders in three colours (hero ink / error red / primary ink) at its three call sites, so a
`TextAppearance` carrying one would be wrong at two of them. Colour is applied by the component kit
(PB-DS-6). `TestPBDS2_NoTextStyleCarriesAColour` makes that a checked property rather than a habit.

**Tests.** Three in `android/gate/s22b_type_test.go` — `TheTypeScaleJoinsTheDesignBidirectionally`,
`NoTextStyleCarriesAColour`, `TheDocChromeExclusionIsStillTrue`. One in
`DesignScaleResolutionTest.kt` — `every text style resolves to its design rule`.

**Negative controls.** Mutations 1, 3, 4, 5, 6, 7 in `negative-controls.txt` — a size, a tracking
value, a computed line height, a dropped style, a style re-pointed at a rule the design does not
have, and a colour smuggled in. K1 on the Robolectric side.

`TheDocChromeExclusionIsStillTrue` deserves its own line, because a gate with an exclusion list is a
gate with a hole in it. The one exclusion, `.panelframe .cap`, is justified by evidence — it names
`var(--mono)`, which no skin declares — and that evidence is **checked** rather than assumed, so the
day someone fixes the typo the exclusion must be re-argued instead of silently swallowing a
nineteenth style.

**PARTIALLY MET. The third clause is NOT met.** Five calls survive in surface code:

    PhoneSurface.kt:103     typeface = Typeface.MONOSPACE
    PhoneSurface.kt:712     setTypeface(typeface, Typeface.BOLD)
    PairingSurface.kt:91    typeface = Typeface.MONOSPACE
    PairingSurface.kt:591   setTypeface(typeface, Typeface.BOLD)
    SettingsSurface.kt:222  setTypeface(typeface, Typeface.BOLD)

These are the three surface files S24 rewrites, and they are the same files PB-DS-11's reassignment
note names. **No gate in this slice asserts their absence**, so nothing today would catch a sixth
being added. Owed to S24 with PB-DS-11.

## 7. PB-DS-3 — the font substitution is a recorded decision

> **Acceptance criterion.** "The decision and its residual are in the ADR. A gate asserts the mono
> style's family is the one recorded, and a test renders a box-drawing string through it so the
> residual is observed rather than assumed."

**Built.** `sans-serif` and `monospace`, zero bundled assets. Weight 650 is reachable because
`android:textFontWeight` resolves against the platform's variable Roboto at API 33, which `minSdk`
guarantees (B134 decision 4).

**Tests.** Three in `android/gate/s22b_type_test.go` — `TheSubstitutionIsTheOneTheADRRecords` (joins
the gate's substitution table back to the ADR's own words, so changing it here without changing the
record fails), `NoFontIsBundled`, `EveryMonoRuleBecomesAMonoStyle`. Two in `MonoBoxDrawingTest.kt`.

### The residual was recorded wrong, and the test falsified it

This is the most important line in this file, because it is the one place where running the
requirement changed what the project believes.

ADR-007 B134 originally predicted that Android's `monospace` would render U+2500–257F as **tofu**.
`MonoBoxDrawingTest` renders `┌─┬─┐ │ └─┴─┘` through `TextAppearance.Swarm.Mono.Code` and measures:

    PB-DS-3 observed residual at 100.0px: mono advance 60.0, box-drawing advance 71.0
    (18.3% wider), tofu: none. The frame renders and does not align.

Every box-drawing character **resolves** — through font *fallback*, to a glyph in another family at
`0.71em` against the monospace family's own `0.60em`. So the terminal peek draws its frame, and the
frame is 18% wider per character than the text inside it. A missing glyph would have been obvious to
anyone who looked at the screen once; **a frame that is silently 18% off is the failure that
ships.** ADR-007 B134 is corrected, dated, and says which test corrected it.

**`Paint.hasGlyph` is not the measurement, and that is recorded in the test.** It returns `true` for
every one of these characters, because it consults the whole fallback chain rather than the named
family. A test built on it would be green, would look like coverage, and would certify the opposite
of the truth.

**The evidence's own limit.** What is measured is the font configuration in Robolectric's Android
runtime — AOSP's, the same Droid Sans Mono and Noto fallback set a stock handset ships. **It is not
a survey of every OEM's font customisation.** It is enough to settle the question the ADR left open,
which was whether the peek renders the frame at all, and it moves the recorded upgrade path
(bundling JetBrains Mono) from speculative to evidence-backed. The upgrade is deliberately **not
taken in this slice**: the peek's screen is S24's and the asset-weight cost belongs with whoever
owns that screen.

**Negative controls.** Mutation 2 in `negative-controls.txt` (a mono style switched to `sans-serif`)
and a planted `.ttf` under `res/font/`, which fires `NoFontIsBundled`. Inside
`MonoBoxDrawingTest` the controls are structural: a `TEXT_MEASUREMENT_IS_REAL` guard that fails
loudly if `@GraphicsMode(NATIVE)` is ever removed (Robolectric's default LEGACY graphics returns one
pixel per character and would make every assertion true for reasons unconnected to any font), a
sans-vs-mono contrast proving `android:fontFamily` reaches the typeface at all, and an assertion
that U+00B7 — the middle dot in the peek's own copy — *is* in the mono family, so the finding is "it
lacks the box-drawing block" rather than "non-ASCII falls back".

**MET**, with the residual corrected rather than confirmed.

## 8. PB-DS-4 — a shape scale, with the degeneracy recorded

> **Acceptance criterion.** "Four radii in `dimens.xml`; the dot is an `oval` shape and the ADR
> records why its token value is not its rendered value."

**Built.** `swarm_radius_card` 9dp, `swarm_radius_sheet` 14dp, `swarm_radius_button` 9dp,
`swarm_radius_chip` 8dp, each joined to its token in both directions. **There is no fifth radius,
and its absence is the requirement.** `--p-dot-r` is 4px on a 7×7px box: `2 × 4 = 8 ≥ 7`, so CSS
clamps the corner, the dot is a full circle, and the literal `4` is unreachable.

The gate **computes** that degeneracy from the artifact rather than asserting it from prose, so a
design that later grew the dot to 12px fails here and forces the decision to be retaken instead of
inherited. It also forbids `swarm_radius_dot` by name — which is the trap: transcribe the token into
a dimen, hand it to a rectangle shape, and ship a rounded square nobody designed.

**Tests.** `TestPBDS4_TheRadiiAreTheRadiusTokens` and
`TestPBDS4_TheDotRadiusTokenIsNotTranscribedAsARadius` (`android/gate/s22b_spacing_test.go`); `the
radii resolve to the radius tokens` (`DesignScaleResolutionTest.kt`).

**Negative controls.** Mutations 8 and 9 in `negative-controls.txt`. Mutation 9 plants
`swarm_radius_dot` and both PB-DS-4 tests fire — it is the exact trap the requirement exists for.

**PARTIALLY MET. Two clauses are open:**

- **"the dot is an `oval` shape"** — no oval drawable exists anywhere in `res/`. `dimens.xml`
  records in prose that the dot *will be* an oval, and the gate asserts that prose is present, but
  the shape itself is `StatusDot.kt` in S23's kit, which is in flight and currently red. **This
  slice records the decision; it does not ship the shape.**
- **"the ADR records why its token value is not its rendered value"** — **it does not.** B134 has no
  dot-degeneracy paragraph; I checked after B134's font correction landed. The record exists only in
  `dimens.xml`, which is where an implementer reads it and is the more useful of the two places, but
  it is not the place the criterion names. The text it needs is in §10.

## 9. PB-DS-5 — not evidenced here, and this is the blocker

PB-DS-5 (the five effects: the inset key-light, the two dot glows, the CTA bloom, the workbar
gradient, the grain raster) **is assigned to S22** — `docs/specifications/remote-phaseB-manifest.tsv`
reads `PB-DS-5 → S22`, and the slice table at `remote-phaseB-requirements.md:876` lists it among
S22's nine.

**It is not implemented in this slice and not evidenced by this file.** Its subject lives in S23's
kit: `KitFoundationTest.kt` carries `the key light is an inset one-dp band clipped to the card
radius` and `the two live Groups glow at the design's share and the other two do not glow`, and
`android/gate/s23_kit_test.go` carries PB-DS-5's `elevation` fence. Those tests exist and are
**currently failing**, because the kit is mid-flight.

**No amendment records that move.** PB-DS-11's reassignment carries a dated parenthetical in the
requirements table; PB-DS-5's does not, in either the manifest or the requirements table. So one of
two things must happen before S22 can close under GG-4, and neither is mine to do:

1. amend the manifest and the slice table to reassign PB-DS-5 to S23, with a dated reason, as
   PB-DS-11's reassignment was recorded; **or**
2. keep it in S22, in which case **S22 is not closeable** until the kit's effect work lands and is
   evidenced.

Stating this is worth more than a clean claim. An evidence file asserting that S22 met eight of
eight, against a manifest that says the slice has nine rows, is a discrepancy an auditor finds in
one `grep` — and it is the kind of drift ADR-007 B97 already records this project shipping once,
when a claim about a commit was left standing as a claim about HEAD.

---

## 10. What is owed, and to whom

| # | Owed | To | Why it is not here |
|---|---|---|---|
| 1 | PB-DS-5's five effects, or the amendment reassigning it | **spec owner / S23** | §9. Blocks S22's closure either way. |
| 2 | The dot's `oval` drawable | **S23** | `StatusDot.kt`; this slice ships the decision and the fence, not the shape. |
| 3 | A B134 paragraph on the dot degeneracy | **ADR owner** | §8. `ADR-007` was under concurrent edit; the text it needs is below. |
| 4 | The full raw-dimension fence | **S24 (PB-DS-11)** | §5. The narrow gate is not a substitute. |
| 5 | Five surviving `setTypeface` calls | **S24 (PB-DS-11)** | §6. No gate asserts their absence today. |
| 6 | Bundling a mono with U+2500–257F coverage | **S24** | §7. Now evidence-backed; the asset cost belongs with the peek's screen. |
| 7 | A `golangci-lint` run | **CI** | Not installed on this host. `gofmt -l` clean on all S22 Go files. |
| 8 | Anything about pixels | **PB-E2E-5** | "What it does NOT prove". No screenshot corpus exists. |

**Draft text for row 3**, so its owner does not have to reconstruct it:

> **`--p-dot-r` is a token whose declared value never renders.** It is 4px, applied to `.pdot`, a
> 7×7px box. `2 × 4 = 8 ≥ 7`, so the corner is clamped and the dot is a full circle; the literal 4
> is unreachable. The status dot is therefore `<shape android:shape="oval">` and there is
> deliberately **no** `swarm_radius_dot` resource — a radius resource for it could only be used to
> draw a rounded rectangle the design does not contain, which is exactly what an implementer who
> found four radii and a fifth token would build. `values/dimens.xml` carries the reason where an
> implementer reads it, and `android/gate/s22b_spacing_test.go` computes the degeneracy from the
> artifact on every run, so a design that later grows the dot forces the decision to be retaken
> rather than inherited.

## 11. One mechanism worth recording

**`type.xml`'s staging silently produced nothing while the file did not exist.** Gradle's `Copy`/`Sync`
skips a missing source without complaint — no warning, no failure, an empty output directory. Had
the Robolectric scaffolding treated an absent resource as "no styles to check", the whole PB-DS-2
suite would have iterated zero times and passed.

It does not. `TypeScale.readResource` fails with a message naming the build file that must stage it,
and `kotlin-red.txt` records that exact failure:

    java.lang.IllegalStateException: type.xml is not on the unit-test classpath.
    app/build.gradle.kts must stage it so the style-to-selector join is read from the same
    artifact aapt compiles, rather than from a second copy of it

This is the "vacuously green" class this repository keeps rediscovering — the same shape as a
3-row join sitting behind a fence built for sixteen, and as a scan pointed at a directory that does
not exist. Every reader in this slice therefore refuses an empty input loudly rather than reporting
a clean result over it: `DesignScale.sharedCss`, `DesignScale.tokens`, `TypeScale.styles`,
`s22bSharedCSS`, `s22bDimens`, `s22bStyles`, `s22bDesignSpacings` and `s22bDesignTypeScale` all
carry that guard, and `TestPBDS3_NoFontIsBundled` was rewritten during self-review for exactly this
reason — it walked `res/font/`, a directory that does not exist, and was green over nothing.
