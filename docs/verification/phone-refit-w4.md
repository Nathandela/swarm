# Phone refit W4 — Slate and breathing: RED evidence and gate record

Bead `agents-tracker-d45a.4` · playbook `docs/specifications/phone-refit-playbook.md` section 5 ·
ADR: [ADR-021](../adr/ADR-021-slate-palette-and-breathing-scale.md) · worktree `refit-w4`,
branch `refit/w4` · 2026-08-27.

**Renumbered 2026-08-28 at review:** `main` already carried ADR-020 (the unattended daemon
restart), so the Slate decision is ADR-021. Every citation on this branch, including the test
identifiers (`TestADR021_*`) and the RED/GREEN output quoted below, was renumbered in one pass
(`git grep` for the Slate `ADR-020` is empty); output quoted here therefore reads `ADR-021` where
the gate printed `ADR-020` at the time it ran.

Every RED below was captured before the GREEN edit that follows it, by running the affected
gate against the tree as it stood. Where a gate could not see the change, the new assertion was
written first and its failure is what is quoted. The negative controls named in the playbook
(`TestTheDriftCheckCanActuallyFail`, `TestPBTOK1_TheComparisonCanActuallyFail`,
`TestPBDS2_TheRungReadersRefusePerturbedInput`, `TestPBTOK7_TheBlendCanActuallyFail`,
`TestADR009D8_TheContrastCheckerCanActuallyFail` and their siblings) were not edited to pass;
where an in-memory control names a skin literal it was retargeted to Slate's, quoted in place.

## W4.1 The token origin points at a Slate maquette

**RED** — `internal/design/tokens_test.go` retargeted at `docs/research/slate-maquette.html`,
`skin: slate`, negative controls on `#0b0e14` / `--p-sheet-lo: #10141d`, before the file or the
JSON existed (`go test ./internal/design/ -count=1`):

```
--- FAIL: TestTokenSourceExistsAndMatchesSchema (0.00s)
    tokens_test.go:174: PB-TOK-1: source must reference "docs/research/slate-maquette.html", got "docs/research/obsidian-maquette.html"
--- FAIL: TestTokenSourceMatchesChosenSkinInDesignHTML (0.00s)
    tokens_test.go:192: PB-TOK-2/ADR-021 D1: recorded skin "obsidian" has no block in docs/research/slate-maquette.html. The skin is a DECISION, so adding one here is an ADR's job, not a JSON edit's.
--- FAIL: TestTheDriftCheckCanActuallyFail (0.00s)
    tokens_test.go:226: design source docs/research/slate-maquette.html not readable: open ../../docs/research/slate-maquette.html: no such file or directory
--- FAIL: TestChosenSkinIsSlateAndPinnedDark (0.00s)
    tokens_test.go:301: PB-TOK-2/ADR-021 D1: skin must be "slate", got "obsidian"
FAIL
FAIL	github.com/Nathandela/swarm/internal/design	0.661s
FAIL
```

**GREEN** — `docs/research/slate-maquette.html` created (a copy of the Obsidian maquette: `:root`
moved to the 35 Slate values, the accent's own literals in `.sdot.att`, `.field.focus .fbox` and
`.veil` moved with it, the mark SVGs re-materialled, prose and page chrome recoloured; every
selector and both block markers unchanged) and `tokens.json` `source`/`skin`/values moved.
`htmlTokenCount` 35 and `colourTokenCount` 19 untouched, `kinds` untouched. All six token tests
pass including `TestTheDriftCheckCanActuallyFail` and `TestPBTOK6_TheKindCheckCanActuallyFail`.

## W4.2 The join follows without a row change

**RED** — with `tokens.json` moved and `colors.xml` untouched
(`go test ./android/gate/ -run 'PBTOK1|PBTOK5' -count=1`):

```
--- FAIL: TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens (0.01s)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_background"> = #FF0E0B08  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_surface_card"> = #FF171310  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_surface_elevated"> = #FF1F1A13  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_surface_well"> = #FF090705  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_hairline"> = #FF2E271D  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_text_primary"> = #FFF6F3EC  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_text_secondary"> = #FFA69D8E  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_text_tertiary"> = #FF746B5D  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_hero"> = #FFC9A876  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_hero_ink"> = #FF1A1206  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_state_attention"> = #FFC9A876  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_state_working"> = #FF6FA7A4  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_state_ok"> = #FF8CAF8E  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
        	<color name="swarm_state_error"> = #FFD96A62  (colors.xml)
    s16_tokens_test.go:401: PB-TOK-1: the Android theme and the token origin DISAGREE.
```

**GREEN** — the nineteen `<color>` values in `colors.xml` moved; `android/design-tokens.tsv` diff is
comment lines only (`git diff` shows `#` lines only). `TestPBTOK1_TheComparisonCanActuallyFail`
untouched and green.

## W4.7 The launcher mark is repainted, not redrawn

**RED** — with `tokens.json` moved and `solid-wedge.svg` untouched
(`go test ./android/gate/ -run Icon -count=1`):

```
--- FAIL: TestLauncherAdaptiveIconDeclaresAllThreeLayers (0.01s)
    appicon_test.go:665: PB-TOK-1: ic_launcher.xml's background layer references @color/swarm_background = #FF0E0B08, but token --p-bg carries #FF0B0E14
    appicon_test.go:665: PB-TOK-1: ic_launcher_round.xml's background layer references @color/swarm_background = #FF0E0B08, but token --p-bg carries #FF0B0E14
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	0.811s
FAIL
```

**GREEN** — the SVG's three colour literals and its token comment moved, geometry byte-identical;
zero edits under `res/`. `TestLauncherIconComparisonCanActuallyFail` untouched and green.

The two 512px PNGs were still the Substrate phosphor-green render (corner `#08090a`, mark
`#53ce7c`) and were re-rendered from the Slate SVG with `rsvg-convert -w 512 -h 512` at the framing
`appicon-evidence.md` recorded for the approved asset (`viewBox 15.54 18.37 72 72`, the mark's own
bounding box), saved RGB without alpha as Play requires (512x512, colour type 2, 6261 bytes).
The XOR check the evidence describes, re-run (marks binarised on luminance, 3x3 erosion):

```
new store icon vs previous store icon (same framing, new colours): XOR 575 px of 262144 = 0.219 %; surviving 3x3 erosion: 0
new store icon vs canvas-centred render, bbox-aligned (shift 17,-2 px): XOR 743 px of 262144 = 0.283 %; surviving 3x3 erosion: 0
new mark bbox in canvas units: x 24.82..76.99 y 32.29..76.31
```

Same drawing, same framing, new material. `docs/ops/play-assets/play-store-icon-512.png` is a byte
copy of `docs/design/store-assets/icon-512.png`, as before. (`scripts/render-play-assets.py`
still draws the pre-Solid-Wedge chevron and Substrate colours; it has been stale since the mark
changed and was not used.)

## W4.3 Derivations recompute themselves

**RED** — with `tokens.json` moved (`go test ./internal/design/ -run PBTOK7 -count=1`):

```
--- FAIL: TestPBTOK7_TheThreeArtifactDerivationsAreComputedFromTheTokens (0.01s)
    --- FAIL: TestPBTOK7_TheThreeArtifactDerivationsAreComputedFromTheTokens/attention-row-border (0.00s)
        derive_test.go:162: PB-TOK-7: attention-row-border resolves to #4B5E7B, the design artifact renders #66553D.
    --- FAIL: TestPBTOK7_TheThreeArtifactDerivationsAreComputedFromTheTokens/deny-fill (0.00s)
        derive_test.go:162: PB-TOK-7: deny-fill resolves to #21E5736B, the design artifact renders #21D96A62.
    --- FAIL: TestPBTOK7_TheThreeArtifactDerivationsAreComputedFromTheTokens/needs-input-dot-glow (0.00s)
        derive_test.go:162: PB-TOK-7: needs-input-dot-glow resolves to #808EB4E6, the design artifact renders #80C9A876.
FAIL
FAIL	github.com/Nathandela/swarm/internal/design	0.538s
FAIL
```

**GREEN** — `derive.go` untouched; `derive_test.go`'s recorded artifact values moved under an
AUTHORIZED VALUE MIGRATION note quoting the Obsidian three. The longhand cross-check recomputes
each from the tokens. No Slate token literal collides with `#4B5E7B`, `#21E5736B` or `#808EB4E6`.
The three `Site:` selectors are `.prow.attention`, `.a2-no` (both in the directions artifact, as
before ADR-009) and `.sdot.att` (in the Slate maquette); the playbook's "exist in the new
maquette" is true of the third only, exactly as it was of the Obsidian maquette.

## W4.8 (first half) Kotlin literals the Go gates read

**RED** — with `tokens.json` moved (`go test ./android/gate/ -run 'PBTOK1|PBDS7' -count=1`):

```
--- FAIL: TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber (0.03s)
    s23_kit_test.go:2551: PB-DS-7: Kit.kt:400: KEY_LIGHT_ALPHA = 0.1, but --p-card-fx declares alpha = 0.08 in the token origin
    s23_kit_test.go:2551: PB-DS-7: Kit.kt:420: LIT_KEY_LIGHT_ALPHA = 0.22, but --p-lit-fx declares alpha = 0.18 in the token origin
--- FAIL: TestPBDS7_TheMetricJoinCanActuallyFail (0.00s)
    s23_kit_test.go:4148: the token metric reader returns 0.08 for `--p-card-fx alpha`, and the origin declares 0.1
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	1.022s
FAIL
    s16_tokens_test.go:709: PB-TOK-1: SwarmTheme records colour 0xFF0E0B08, which is not any mapped token's value. [...]
    s16_tokens_test.go:709: PB-TOK-1: SwarmTheme records colour 0xFFF6F3EC, which is not any mapped token's value. [...]
    s16_tokens_test.go:709: PB-TOK-1: SwarmTheme records colour 0xFFA69D8E, which is not any mapped token's value. [...]
```

**GREEN** — `SwarmTheme.EXPECTED_DARK_COLORS` to `0xFF0B0E14, 0xFFEEF2F8, 0xFF9AA6BA`; `Kit.kt`
`KEY_LIGHT_ALPHA` 0.10f to 0.08f and `LIT_KEY_LIGHT_ALPHA` 0.22f to 0.18f with their prose;
`KitFoundationTest`'s known-answer alpha and `s23_kit_test.go`'s known-answer alpha (the
`TestPBDS7_TheMetricJoinCanActuallyFail` control, which pins `--p-card-fx alpha` independently of
the reader) moved under AUTHORIZED VALUE MIGRATION notes. These had to land with the origin, not
after it: both are read by Go gates, so a commit that moved the tokens alone would have left
`go test ./android/gate/` red.

## Deviation forced by the join: `dimens.xml` radii

**RED** — with `tokens.json` moved (`go test ./android/gate/ -run PBDS4 -count=1`):

```
--- FAIL: TestPBDS4_TheRadiiAreTheRadiusTokens (0.00s)
    s22b_spacing_test.go:709: PB-DS-4: <dimen name="swarm_radius_card"> is 14dp and --p-card-r is 16px. A radius that disagrees with its token is the same defect class as a colour that does.
    s22b_spacing_test.go:709: PB-DS-4: <dimen name="swarm_radius_sheet"> is 18dp and --p-sheet-r is 20px. [...]
    s22b_spacing_test.go:709: PB-DS-4: <dimen name="swarm_radius_button"> is 10dp and --p-btn-r is 12px. [...]
    s22b_spacing_test.go:709: PB-DS-4: <dimen name="swarm_radius_chip"> is 8dp and --p-chip-r is 10px. [...]
```

The playbook says `dimens.xml` changes in comment lines only and, in the same section, moves the
four radius tokens. PB-DS-4 joins the four `swarm_radius_*` dimens to those tokens in both
directions, so the two instructions cannot both hold. **GREEN** — the four values flowed
(14/18/10/8 to 16/20/12/10) with a comment; the ten spacing steps and the frame constants are
byte-identical. `DesignScaleResolutionTest`'s two radius known answers moved with them.

## Contract amendment (lead, 2026-08-27): the tab bar and composer bar are opaque

The playbook's `--p-tabbg rgba(11,14,20,0.88), unspent after W1` line was superseded mid-wave: W1
cannot stop spending `swarm_tabbar_background` (`o3_material_test.go:148` refuses a colour resource
nothing draws), so the opacity moved into the token, `rgba(11,14,20,1)`, in the maquette and the
origin. **RED** with the token moved and nothing downstream touched
(`go test ./android/gate/ ./internal/design/ -run 'PBTOK1|PBDS7|PBTOK6|TestTokenSource' -count=1`):

```
--- FAIL: TestPBTOK1_TheAndroidThemeColoursAreTheDesignTokens (0.07s)
        	--p-tabbg = rgba(11,14,20,1)  (internal/design/tokens.json)
        	<color name="swarm_tabbar_background"> = #E00B0E14  (colors.xml)
--- FAIL: TestPBDS7_TheMetricJoinCanActuallyFail (0.00s)
    s23_kit_test.go:4156: the token metric reader returns 1 for `--p-tabbg alpha`, and the origin declares 0.88
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	2.821s
FAIL
```

**GREEN** — `swarm_tabbar_background` `#E00B0E14` to `#FF0B0E14` (the join's own `argbFromToken`),
`s23_kit_test.go`'s known-answer `{"--p-tabbg", "alpha", 0.88}` to `1` under an AUTHORIZED REWRITE
note; `Composer.kt:292` and `TabBar.kt:115` untouched. `InboxChromeTest`'s `assertNotEquals(--p-bg,
fill)` is false by design now (the fill IS the ground) and is inverted to `assertEquals` with the
rewritten message citing ADR-021 D1; its RED and GREEN are Kotlin-lane runs recorded below. The
`design-tokens.tsv` row for `--p-tabbg` keeps its note (`rgba(14,11,8,0.88) -> #E00E0B08`) as a
row cell is not a comment line; it is history and reads as such.

## Rulings received mid-wave (lead, 2026-08-27)

1. **W4.5 widened.** The Kotlin call sites move, and the file list gains
   `android/gate/s23_kit_test.go` (the `.prows` rows of `s23Spacing` and the row-14/row-26 rows of
   `s23DerivedSpacing`) and `docs/design/substrate-components.md` rows 14 and 26, re-pointed to the
   Slate maquette's `.slab`, plus the Kotlin claims in `InboxRowTest`, `ActivityRowTest` and
   `KitDensityTest:299`. Section W4.5 below records it.
2. **`--p-sweep-fx` keeps `rgba(255,252,244,0.30)`**: ADR-009 D5 survives with its sweep tint, and
   `Motion.kt`'s tint constants and D5's register row are joined to the token by `o4_sweep_test.go`
   and `MotionTest`, neither in this wave. Recorded in ADR-021 D1.
3. **Both forced deviations accepted**: the four `swarm_radius_*` dimens follow the radius tokens
   under PB-DS-4; `android/app/build.gradle.kts`'s staging line and `DesignScale.kt` point at
   `slate-maquette.html`.

## The Go gate command, exactly

This shell is swarm-supervised and inherits `SWARM_HOOK_CAPTURE`, `SWARM_SESSION_ID`,
`SWARM_HOOK_SEQ_FILE`, `SWARM_HOOK_TOKEN`, `SWARM_DAEMON_SOCK` and `SWARM_SHIM_HOOK_SOCK`, which
makes `cmd/swarm TestRunHook_*`, `internal/hookclient` and `internal/daemon` fail for reasons
unrelated to any change (found by fleet W2). Every Go gate in this wave ran as:

```
env -u SWARM_HOOK_CAPTURE -u SWARM_SESSION_ID -u SWARM_HOOK_SEQ_FILE -u SWARM_HOOK_TOKEN -u SWARM_DAEMON_SOCK -u SWARM_SHIM_HOOK_SOCK go test -race -count=1 -timeout 40m ./...
```

(same prefix on the targeted `-run` invocations quoted above). The machine was shared by three
fleets and a Gradle lane throughout; a load-timing failure in a package this wave does not touch
was re-run once in isolation before being called anything, and the outcome is in the gate table.

## The Kotlin lane, commit 1

Run from the serialised script (`pgrep -f gradle-wrapper.jar` empty, START recorded, then
`. android/toolchain.env && cd android && ./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache`,
then every result XML newer than START and `app/libs/swarm.aar` unmoved).

**Run 1, the tree before the tab-bar amendment:** `RESULT XMLS: 202 files, 0 older than START`,
`COUNTS: tests=1589 failures=1 errors=0 skipped=0`, AAR unmoved. The one failure is a token
consequence and is the RED for the claim it names:

```
InboxChromeTest > the badge is the attention pill the derivation table specifies FAILED
    expected:<[]> but was:<[badge height: design says 20.0, component resolved 16.0]>
```

The claim derived the badge height as `2 x swarm_radius_chip`, which was 16 only while
`--p-chip-r` happened to be half of row 3's 16; at Slate's 10 the box is still a pill (the corner
clamps at half the height, PB-DS-4's dot degeneracy one token over) and row 3's 16 is the value
`android/gate/s23_kit_test.go` joins to `KitMetrics.BADGE_HEIGHT_DP`. **GREEN edit:** the claim
asserts the pill property and the row-3 height, under an AUTHORIZED REWRITE note; `Kit.kt`'s
prose for `BADGE_HEIGHT_DP` corrected.

**Targeted RED run for the tab-bar amendment** (`--tests dev.swarm.phone.ui.kit.InboxChromeTest`,
the token at alpha 1 and the pre-amendment assertion restored for the run):

```
InboxChromeTest > the tab bar is the design's translucent bar with a hairline rule FAILED
InboxChromeTest > the chrome assertions can actually fail FAILED
BUILD FAILED in 7m 45s
```

The first is the `assertNotEquals(--p-bg, fill)` the amendment predicted, now false by design and
inverted. The second is the same file's negative control, whose perturbation was `fill
rgbaToken(--p-tabbg)` against `token(--p-bg)`: a discrimination satisfiable by the skin rather than
by the reader, the same defect the file already records for `--p-hero`/`--p-att` under ADR-009.
**GREEN edit:** the control is retargeted to `--p-card`, a pair ADR-021 keeps distinct, under an
AUTHORIZED REWRITE note quoting the old form. No exemption was added. The full runs on the committed tree are
recorded under "The gates on the committed tree" at the end.

## W4.4 Contrast is measured, not asserted

The Slate ledger, as the gate prints it over the joined origin before the file was renamed
(`go test ./android/gate/ -run ADR009D8 -v`; floors byte-identical):

```
	--p-err       on --p-bg       Lc   -45.2  (error-text floor 38)  terracotta as TEXT: the deny label, the destructive row action
	--p-hero      on --p-bg       Lc   -60.0  (accent-text floor 50)  champagne as TEXT: the LIVE counter, the active tab, the peek foreground
	--p-hero-ink  on --p-cta-bg   Lc    60.9  (cta-label floor 55)  the CTA's label on its fill (--p-cta-bg value-aliases --p-hero and keeps its own row)
	--p-hero-ink  on --p-hero     Lc    60.9  (cta-label floor 55)  ink on a champagne fill: selected chip, badge, toggle knob
	--p-ink       on --p-bg       Lc   -98.8  (body-primary floor 90)  primary text
	--p-ink       on --p-card     Lc   -97.9  (body-primary floor 90)  primary text
	--p-ink       on --p-elev     Lc   -96.5  (body-primary floor 90)  primary text
	--p-ink       on --p-well     Lc   -98.9  (body-primary floor 90)  primary text
	--p-ink2      on --p-bg       Lc   -53.3  (supplementary floor 45)  secondary text -- spot-read status, never the sole carrier of a decision
	--p-ink2      on --p-card     Lc   -52.5  (supplementary floor 45)  secondary text -- spot-read status, never the sole carrier of a decision
	--p-ink2      on --p-elev     Lc   -51.0  (supplementary floor 45)  secondary text -- spot-read status, never the sole carrier of a decision
	--p-ink2      on --p-well     Lc   -53.5  (supplementary floor 45)  secondary text -- spot-read status, never the sole carrier of a decision
	--p-ink3      on --p-bg       Lc   -27.5  (incidental floor 24)  tertiary: section labels and the receded Completed group -- incidental only
	--p-ink3      on --p-card     Lc   -26.7  (incidental floor 24)  tertiary: section labels and the receded Completed group -- incidental only
	--p-ink3      on --p-elev     Lc   -25.2  (incidental floor 24)  tertiary: section labels and the receded Completed group -- incidental only
	--p-ink3      on --p-well     Lc   -27.6  (incidental floor 24)  tertiary: section labels and the receded Completed group -- incidental only
--- PASS: TestADR009D8_EveryInkOnSurfacePairClearsItsAPCAFloor (0.02s)
--- PASS: TestADR009D8_EveryRoleFloorIsDeclaredAgainstItsRung (0.00s)
	--p-att   on --p-bg     9.03:1  (Group NeedsInput status dot)
	--p-att   on --p-card   8.30:1  (Group NeedsInput status dot)
	--p-err   on --p-bg     6.44:1  (the sync status pill's BROKEN mark)
	--p-err   on --p-card   5.91:1  (the sync status pill's BROKEN mark)
	--p-ink3  on --p-bg     3.95:1  (Group Completed status dot, and the OFFLINE presence dot)
	--p-ink3  on --p-card   3.63:1  (Group Completed status dot, and the OFFLINE presence dot)
	--p-ok    on --p-bg     9.64:1  (Group ReadyForReview status dot, and the ONLINE presence dot)
	--p-ok    on --p-card   8.85:1  (Group ReadyForReview status dot, and the ONLINE presence dot)
	--p-work  on --p-bg     9.39:1  (Group Working status dot and the workbar's opaque stop)
	--p-work  on --p-card   8.62:1  (Group Working status dot and the workbar's opaque stop)
--- PASS: TestADR009D8_TheStateIndicatorsClearWCAGNonTextContrast (0.00s)
--- PASS: TestADR009D8_TheContrastCheckerCanActuallyFail (0.00s)
--- PASS: TestADR009D8_AFloorNoInkCanReachIsAFloorAndNotAPalette (0.00s)

```

All 16 ink pairs and all 10 indicator pairs clear their floors. The tight pair is `--p-ink3` on
`--p-elev` at |Lc| 25.2 against 24.0 (headroom 1.2); every other pair's headroom is at least 5.9.

**Rename and re-derivation.** `obsidian_contrast_test.go` became `slate_contrast_test.go`; the
identifiers `obsidian*` became `slate*` and the tests `TestADR009D8_*` became `TestADR021_*`; every
ADR-009 D8.1 citation stays as the floors' history and each floor's comment gained a Slate column.
The in-memory controls that named Obsidian's ladder were retargeted to Slate's (`#131824`/`#1b2334`,
`#131824`/`#0b0e14`, `rgba(11,14,20,1)`), quoted in place.

**RED** — the champagne proof's own claim (ceiling below the retired large floor of 60), written
first against the live `--p-hero` read through the join (`go test ./android/gate/ -run
'ADR021_AFloorNoInkCanReach' -count=1`):

```
--- FAIL: TestADR021_AFloorNoInkCanReachIsAFloorAndNotAPalette (0.05s)
    slate_contrast_test.go:974: apcaCeiling(--p-hero #8eb4e6) = 62.04, at or above the RETIRED large floor of 60 (the champagne proof's claim, applied to the live fill). ADR-021 re-derived this proof on a fill that clears it (62.04 on #8eb4e6); a fill that does not is the champagne case again and the proof must be re-derived, not inherited.
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	4.028s
FAIL
```

**GREEN** — the proof re-derived: the champagne sweep stays as the amendment's evidence (59.73 on
`#c9a876`, a constant), and the live fill is held to what is true of it: its ceiling (62.04) is
below the retired body floor of 75, at or above the retired large floor of 60, and above the CTA
floor of 55, which the live pair clears at 60.9. The four near-black grounds are Slate's (ceilings
105.3 to 107.7 against the primary floor of 90).

```
--- PASS: TestADR021_EveryInkOnSurfacePairClearsItsAPCAFloor (0.05s)
--- PASS: TestADR021_EveryRoleFloorIsDeclaredAgainstItsRung (0.00s)
--- PASS: TestADR021_TheStateIndicatorsClearWCAGNonTextContrast (0.00s)
--- PASS: TestADR021_TheContrastCheckerCanActuallyFail (0.00s)
--- PASS: TestADR021_AFloorNoInkCanReachIsAFloorAndNotAPalette (0.00s)
ok  	github.com/Nathandela/swarm/android/gate	3.990s
```

`go vet ./android/gate/` and `golangci-lint run ./android/gate/` clean after the rename.

## W4.5 Spacing: the maquette moves, the components follow (widened by the lead's ruling)

The Slate maquette's slab goes from `margin: 0 14px 10px; padding: 13px 15px` to
`margin: 0 16px 14px; padding: 12px 16px`. The ten steps and the frame constants are untouched;
`dimens.xml`'s spacing lines are byte-identical.

**RED, step 1** -- the slab moved, the ledger unchanged (`go test ./android/gate/ -run PBDS1`):

```
--- FAIL: TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale (0.00s)
    s22b_spacing_test.go:386: PB-DS-1: "swarm_space_14" claims to absorb 15px, which the Obsidian maquette does not declare as a spacing value
    s22b_spacing_test.go:406: PB-DS-1: 6 of 19 maquette spacing values move onto the scale, want 7.
```

**GREEN** -- `s22b_spacing_test.go`: `swarm_space_14` absorbs `{14}` (15px was the slab's alone;
13px stays, four other rules declare it), `wantMovers` 7 to 6, both under AUTHORIZED REWRITE notes
quoting the old lines; the reverse assertion ("claims to absorb a value the maquette does not
declare") is what produced the RED and stays. `TestPBDS1_TheAbsorptionLedgerCanActuallyFail` and
`TestPBDS1_TheRatifiedExceptionsAreExactlyTheseThree` untouched and green.

**RED, step 2** -- the joins re-pointed before any call site moved
(`go test ./android/gate/ -run 'PBDS6|PBDS7'`). `s23Spacing` gained a `Maquette` source flag (a
separate form, not a fallback, for `s23MetricMaquetteOrigin`'s reason) and its two `.prows` rows
became `.slab { margin }` fields 1 and 2 read from the Slate maquette (16 sides, 14 bottom); the
`s23DerivedSpacing` rows for `#14 Activity row` and `#26 Message bubble` became `space_12` x
`space_16`, and `docs/design/substrate-components.md` rows 14 and 26 state that pair with an
AMENDED note naming `.slab { padding: 12px 16px }` as the value's source (row 26's bubble gap
became `space_14`, the slab's `margin-bottom`). No design value was invented: every step is one
the maquette declares. Row 31's "all row 14's" cross-reference and the `fileChangeRow` factory
prose were annotated as stale rather than moved (the file-change row keeps `space_10` x `space_12`
pending its own ruling).

```
--- FAIL: TestPBDS6_EveryKitSpacingIsTheLedgersStep (0.01s)
    s23_kit_test.go:1668: PB-DS-6: SessionRow.kt never references R.dimen.swarm_space_16, which is the step PB-DS-1's ledger assigns to `.slab { margin }` = 16px. A dimension that is not read from the scale is one typed at the call site, and the constant this requirement replaced was PhoneSurface's `PADDING = 24` in raw pixels.
    s23_kit_test.go:1668: PB-DS-6: SessionRow.kt never references R.dimen.swarm_space_14, which is the step PB-DS-1's ledger assigns to `.slab { margin }` = 14px. A dimension that is not read from the scale is one typed at the call site, and the constant this requirement replaced was PhoneSurface's `PADDING = 24` in raw pixels.
--- FAIL: TestPBDS7_EveryDerivedSpacingIsTheRowsStep (0.00s)
    s23_kit_test.go:1944: PB-DS-7: ActivityRow.kt never references R.dimen.swarm_space_16, which is the step `#14 Activity row` states for its padding-x. A component whose only specification is prose in a table is the one whose spacing has to be read out of that table rather than out of itself.
    s23_kit_test.go:1944: PB-DS-7: MessageBubble.kt never references R.dimen.swarm_space_16, which is the step `#26 Message bubble` states for its padding-x. A component whose only specification is prose in a table is the one whose spacing has to be read out of that table rather than out of itself.
```

**GREEN, step 3** -- `sessionList` gap `space_8` to `space_14` and sides `space_12` to `space_16`
(KDoc keeps `origin: .prows` for the factory join and names the slab's margin as the steps'
source); `activityRow` 12/10 to 16/12; `messageBubble` 12/8 to 16/12 with `topMargin` `space_14`.
`sessionRow`'s own `.prow` padding is not in the ruling and did not move.

```
    s22b_spacing_test.go:440: PB-DS-1 drift ledger over 19 distinct maquette spacing values (16 of them absorbed, 3 ratified exceptions), worst 1dp:
        	3->2 (-1)
        	5->4 (-1)
        	7->8 (+1)
        	9->8 (-1)
        	11->10 (-1)
        	13->12 (-1)
--- PASS: TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale (0.01s)
--- PASS: TestPBDS6_EveryKitFactoryIsAnInboxComponent (0.00s)
--- PASS: TestPBDS6_EveryKitFileIsClaimedByAFence (0.00s)
--- PASS: TestPBDS6_EveryKitSpacingIsTheLedgersStep (0.00s)
--- PASS: TestPBDS7_EveryDerivedSpacingIsTheRowsStep (0.00s)
--- PASS: TestPBDS6_EveryKitMetricIsRenderedOneWay (0.01s)
ok  	github.com/Nathandela/swarm/android/gate	1.695s
```

**RED, step 4 (Kotlin)** -- the call sites moved, the Robolectric claims not yet (targeted run,
`--tests` InboxChromeTest, InboxRowTest, ActivityRowTest, KitDensityTest; InboxChromeTest is
commit C's guard fix and passed):

```
ActivityRowTest > the row is the session row's card and the session row's padding FAILED
InboxRowTest > the session list carries the side padding and the gap between rows FAILED
KitDensityTest > the conversation surface spends the whole pixels the platform would FAILED
> Task :app:testDebugUnitTest FAILED
BUILD FAILED in 4m 33s
COUNTS: tests=48 failures=3 errors=0 skipped=0
```

**GREEN** -- `InboxRowTest`'s `.prows` claims become the slab's margin (16 sides, 14 gap),
`ActivityRowTest`'s row-14 claims become 16/12 and the test is renamed for what it now asserts,
`KitDensityTest:299`'s row-26 padding-x becomes `space_16`; each under an AUTHORIZED REWRITE note
quoting the old claim. `MessageBubbleTest` carries no padding claim. The full Kotlin runs on the
committed tree are recorded under "The gates on the committed tree" at the end.

## W4.6 Type: five rungs, all shifted

**RED** -- ADR-012 amended first (P10, ruling R9, the new machine-read table; R1's table struck
through so neither reader parses a style on two rungs), `type.xml` untouched
(`go test ./android/gate/ -run PBDS2`):

```
--- FAIL: TestPBDS2_TheTypeScaleJoinsTheDesignBidirectionally (0.02s)
    s22b_type_test.go:755: PB-DS-2: android/app/src/main/res/values/type.xml:197 TextAppearance.Swarm.Body.Message (origin `.m2`) is 12.5sp; ADR-012 phase 2 puts it on the 14sp rung (the design draws 12.5px). A size is not this file's to choose: it is the design's rule and the ruled ladder, and this style agrees with neither.
    s22b_type_test.go:755: PB-DS-2: android/app/src/main/res/values/type.xml:197 TextAppearance.Swarm.Body.Message (origin `.m2`) has line height 18.125sp; the design's multiplier on the size this style renders at is 1.45 x 14sp = 20.3sp
    s22b_type_test.go:755: PB-DS-2: android/app/src/main/res/values/type.xml:206 TextAppearance.Swarm.Body.Secondary (origin `.prow .ln`) is 12.5sp; ADR-012 phase 2 puts it on the 14sp rung (the design draws 12px). A size is not this file's to choose: it is the design's rule and the ruled ladder, and this style agrees with neither.
    s22b_type_test.go:755: PB-DS-2: android/app/src/main/res/values/type.xml:206 TextAppearance.Swarm.Body.Secondary (origin `.prow .ln`) has line height 17.5sp; the design's multiplier on the size this style renders at is 1.4 x 14sp = 19.599999999999998sp
    [... 21 PB-DS-2 faults in all: the sixteen sizes and the five leadings ...]
```

**Deviation, ruled by the lead (2026-08-28).** The playbook amends ADR-012 "not by editing R1's
text", but `s22bRungTable` (`android/gate/s22b_type_test.go:307`) and the Robolectric `TypeScale`
reader both scan the whole ADR and refuse a style that stands on two rungs, so R1's sixteen
style-name cells were wrapped in `~~` under a SUPERSEDED callout (every other cell and number
byte-identical, the readers untouched); the lead accepted this as the amendment mechanism and
amends the playbook line on `main` at merge.

**GREEN** -- `type.xml`: display 24 / title 15 / body 14 / code 12.5 / micro 11, every
`android:lineHeight` recomputed on the rung (`Body.Message` 20.3sp, `Body.Secondary` 19.6sp,
`Mono.Code` 18.75sp, `Mono.CodeSmall` 19.375sp, `Mono.Fine` 17.6sp); `Display.SAS` 34sp untouched;
a header paragraph names the ruling. `DesignScaleResolutionTest`'s rung known answers (22/10)
move to 24/11 under an AUTHORIZED VALUE MIGRATION note. Every PB-DS-2 and PB-DS-3 test passes,
`TestPBDS2_TheRungReadersRefusePerturbedInput` and `TestPBDS2_TheRenderEqualityRefusesAPerturbedPair`
untouched:

```
--- PASS: TestPBDS2_TheSansHeaderOverrideIsRuled (0.01s)
--- PASS: TestPBDS2_TheTypeScaleJoinsTheDesignBidirectionally (0.03s)
--- PASS: TestPBDS2_TheLadderIsTheFiveRuledRungs (0.00s)
--- PASS: TestPBDS2_EveryStyleIsClaimedByExactlyOneClass (0.00s)
--- PASS: TestPBDS2_TheAddedStylesAreTheOnesTheDocumentAdds (0.01s)
--- PASS: TestPBDS2_TheDerivedReadersRefusePerturbedInput (0.00s)
--- PASS: TestPBDS2_TheRungReadersRefusePerturbedInput (0.00s)
--- PASS: TestPBDS2_NoTextStyleCarriesAColour (0.00s)
--- PASS: TestPBDS2_TheDocChromeExclusionIsStillTrue (0.00s)
--- PASS: TestPBDS2_TheUnimplementedRulesAreTheOnesTheRecordDecides (0.00s)
--- PASS: TestPBDS2_TheRenderEqualityRefusesAPerturbedPair (0.00s)
--- PASS: TestPBDS3_TheSubstitutionIsTheOneTheADRRecords (0.00s)
--- PASS: TestPBDS3_ExactlyTheDecidedFontsAreBundled (0.00s)
--- PASS: TestPBDS3_EveryMonoRuleBecomesAMonoStyle (0.00s)
ok  	github.com/Nathandela/swarm/android/gate	0.949s
```

## W4.8 ADR-021 and the Kotlin literals

The Go-gated literals (`SwarmTheme.EXPECTED_DARK_COLORS`, `Kit.kt`'s two key-light alphas and the
`rgba(238,242,248,...)` prose) landed with the origin in commit A, because the Go gates read them;
ADR-021 (Status Accepted, D1-D4, supersedes/survives, consequences, alternatives) and the README
row with "next FREE" 021 landed there too, so every AUTHORIZED REWRITE note cites a record that
exists in the same commit. What is left here is prose: `Toggle.kt` and `ToggleTest.kt` carry the
ceiling re-measured on `#8eb4e6` (62.04, white 46.58) beside the champagne 59.73 they quote,
`MessageBubble.kt`'s radius ladder reads 16 / 20 / 12 / 10, and the `s23_kit_test.go` message that
named `obsidian_contrast_test.go` names `slate_contrast_test.go`.

The done-when grep, `grep -rn "c9a876\|C9A876\|0e0b08\|0E0B08" android/ internal/ docs/design/`
(build directories excluded), returns quoted history inside the wave's files -- the AUTHORIZED
REWRITE quotes in `derive_test.go`, the champagne evidence constant in `slate_contrast_test.go`,
the Toggle prose, the TSV's `--p-tabbg` note (a row cell, not a comment line, so it stays) -- and
three live literals in files the wave has no right to edit, reported rather than touched:
`docs/design/conversation-drawing.html:10,12` (an Obsidian CSS block nothing reads),
`docs/design/substrate-components.md:80` and row 23 (contrast figures measured on Obsidian). ADR-021
lists them under Consequences.

## The commits

| SHA | Subject | Section |
|---|---|---|
| `6be8db30` | Move the phone skin to Slate: origin, join, mark, derivations (ADR-021) | W4.1, W4.2, W4.3, W4.7, W4.8 (Go-gated literals, ADR-021, README), the radius deviation, the tab-bar amendment |
| `fc07ad98` | Rename the contrast gate for Slate and re-derive the ceiling proof | W4.4 |
| `d0ff31a1` | Fix the InboxChromeTest control guard for the opaque tab bar | the tab-bar amendment's Kotlin control |
| `98e19397` | Breathing: the slab spends wider steps and the components follow (ADR-021 D2) | W4.5 (widened by ruling 1) |
| `afa25ed7` | Shift the type ladder one rung up: 24 / 15 / 14 / 12.5 / 11 (ADR-021 D3, ruling R9) | W4.6 |
| `faf4e589` | Re-measure the Toggle ceiling on Slate and settle the remaining prose | W4.8 (prose) |
| `fb439c52` | Re-measure the focus ring's contrast on Slate in substrate-components row 23 and its 1.1 note | W4.8 done-when grep (ruling 2) |
| `913c4b5a` | Count sessionList's space_16 as the screen's side air in the sweep (ADR-021 D2) | Kotlin gate, run 1-2 RED (ruling 3) |
| `b7eccbf1` | Delete the notice's size guard: on the body rung nothing can tell a dropped appearance (ADR-021 D3) | Kotlin gate, run 1-2 RED (ruling 4) |

Every file in every commit is in section 5's list as widened by the rulings above; the
untracked `.lintcache/` (the lint cache the gate runs with) was never staged. The evidence
commit that closes this file follows the nine and is named in the branch's log.

## One negative control per behavioural change

Each change in this wave is a value move that a gate reads; the control for each is the test
that proves the reading gate can fail, and none was edited to pass. All of them, in one run on
the committed tree (`go test ./internal/design/ ./android/gate/ -run 'CanActuallyFail|RefusePerturbed|RefusesAPerturbed' -count=1 -v`):

| Change | Control |
|---|---|
| W4.1 origin and skin | `TestTheDriftCheckCanActuallyFail`, `TestPBTOK6_TheKindCheckCanActuallyFail` |
| W4.2 the nineteen colours | `TestPBTOK1_TheComparisonCanActuallyFail` |
| W4.3 derivations | `TestPBTOK7_TheBlendCanActuallyFail` |
| W4.4 contrast on Slate | `TestADR021_TheContrastCheckerCanActuallyFail` |
| W4.5 spacing steps | `TestPBDS1_TheAbsorptionLedgerCanActuallyFail`, `TestPBDS6_TheDpScanCanActuallyFail`, `TestPBDS7_TheCrossChecksCanActuallyFail` |
| W4.6 the ladder | `TestPBDS2_TheRungReadersRefusePerturbedInput`, `TestPBDS2_TheRenderEqualityRefusesAPerturbedPair` |
| W4.7 the mark | `TestLauncherIconComparisonCanActuallyFail` |
| W4.8 key-light alphas and the theme colours | `TestPBDS7_TheMetricJoinCanActuallyFail`, `TestPBTOK1_TheComparisonCanActuallyFail` |
| radii (deviation) | `TestPBTOK6_TheDimenConverterCanActuallyFail` |
| the opaque tab bar (amendment) | `InboxChromeTest > the chrome assertions can actually fail` (Kotlin lane) |
| Toggle ceiling prose | `ToggleTest > the toggle assertions can actually fail` (Kotlin lane) |
| the sweep's air steps (`913c4b5a`) | `ScreenAirSweepTest > the sweep can see a flush leaf and a doubled one` -- the rewrite IS the file's own control, red in runs 1-2 and green in the targeted run and run 3 |
| the notice guard's deletion (`b7eccbf1`) | none: a deletion has no control; the notice's rung and ink stay under `NoticeTest > the notice is Body Secondary in the secondary ink` |

```
--- PASS: TestPBTOK7_TheBlendCanActuallyFail (0.00s)
--- PASS: TestTheDriftCheckCanActuallyFail (0.01s)
--- PASS: TestPBTOK6_TheKindCheckCanActuallyFail (0.00s)
ok  	github.com/Nathandela/swarm/internal/design	0.912s
--- PASS: TestLauncherIconComparisonCanActuallyFail (0.02s)
--- PASS: TestPBDS5_TheColourSpendScanCanActuallyFail (0.10s)
--- PASS: TestPBDS5_TheGrainReaderCanActuallyFail (0.00s)
--- PASS: TestD82_EachConstraintCanActuallyFail (0.00s)
--- PASS: TestD82_TheNumberJoinCanActuallyFail (0.01s)
--- PASS: TestADR009D5_TheMemoGateCanActuallyFail (0.00s)
--- PASS: TestPBTOK1_TheComparisonCanActuallyFail (0.00s)
--- PASS: TestPBTOK6_TheDimenConverterCanActuallyFail (0.00s)
--- PASS: TestPBTOK7_TheLiteralScanCanActuallyFail (0.00s)
--- PASS: TestPBTOK8_TheGroupParserCanActuallyFail (0.00s)
--- PASS: TestPBDS1_TheAbsorptionLedgerCanActuallyFail (0.00s)
--- PASS: TestPBDS2_TheDerivedReadersRefusePerturbedInput (0.00s)
--- PASS: TestPBDS2_TheRungReadersRefusePerturbedInput (0.00s)
--- PASS: TestPBDS2_TheRenderEqualityRefusesAPerturbedPair (0.00s)
--- PASS: TestPBDS12_TheTouchTargetReaderCanActuallyFail (0.00s)
--- PASS: TestPBDS7_TheMetricJoinCanActuallyFail (0.00s)
--- PASS: TestPBDS7_TheMetricScanCanActuallyFail (0.11s)
--- PASS: TestPBDS7_TheCrossChecksCanActuallyFail (0.05s)
--- PASS: TestPBDS6_TheDpScanCanActuallyFail (0.00s)
--- PASS: TestPBDS6_TheAnnotationParserCanActuallyFail (0.00s)
--- PASS: TestADR021_TheContrastCheckerCanActuallyFail (0.00s)
ok  	github.com/Nathandela/swarm/android/gate	3.248s
```

## W4.7 and W4.8 done-when, on the committed tree

`go test ./android/gate/ -run Icon -count=1 -v`, with `git status --short android/app/src/main/res/`
empty and `git diff --stat main...HEAD -- android/app/src/main/res/drawable android/app/src/main/res/mipmap-anydpi-v26`
empty (zero edits under `res/` beyond the three `values/` files the wave owns):

```
--- PASS: TestLauncherForegroundIsTheChosenIconCandidate (0.03s)
--- PASS: TestLauncherAdaptiveIconDeclaresAllThreeLayers (0.01s)
--- PASS: TestLauncherIconComparisonCanActuallyFail (0.00s)
PASS
ok  	github.com/Nathandela/swarm/android/gate	1.283s
```

`grep -rn "c9a876\|C9A876\|0e0b08\|0E0B08" android/ internal/ docs/design/` (build directories
excluded) on the committed tree returns sixteen lines, every one quoted history: `design-tokens.tsv:70`
(the `--p-tabbg` row's note), `ToggleTest.kt:214`, `Toggle.kt:29`,
`slate_contrast_test.go:895,924,931,939,944,989`, `derive_test.go:96,104,112`,
`substrate-components.md:82,270` (the amended note and row 23, each quoting the Obsidian figure it
replaced), and `docs/design/conversation-drawing.html:10,12` -- an Obsidian CSS block nothing
reads, in W5's file, which the lead ruled stays as it is and goes into W5's brief.

**Ruling 2 (lead, 2026-08-28): the two live Obsidian literals in `substrate-components.md` are
amended, not left.** Line 80's section 1.1 note ("computed over Obsidian's ladder: `--p-hero`
`#c9a876` is 8.74:1 on `--p-bg`, 8.22:1 on `--p-card` and 7.69:1 on `--p-elev`") and row 23's
fill cell carried the same three figures. Both now state the Slate measurement under an AMENDED
note in the style of rows 14 and 26, with the Obsidian figures quoted as history. The numbers are
the gate's, not hand arithmetic: `slate_contrast_test.go`'s `wcagRatio` on `#8eb4e6` over the
three grounds, printed through a throwaway `_test.go` in the package (run once, deleted, never
staged) and cross-checked against the two lines the gate itself prints for `--p-att`, which
value-aliases `--p-hero`:

```
        	--p-att   on --p-bg     9.03:1  (Group NeedsInput status dot)
        	--p-att   on --p-card   8.30:1  (Group NeedsInput status dot)
--- PASS: TestADR021_TheStateIndicatorsClearWCAGNonTextContrast (0.01s)
    zz_tmp_hero_ratio_test.go:14: --p-hero #8eb4e6 on --p-bg   9.03:1
    zz_tmp_hero_ratio_test.go:14: --p-hero #8eb4e6 on --p-card 8.30:1
    zz_tmp_hero_ratio_test.go:14: --p-hero #8eb4e6 on --p-elev 7.35:1
```

`go test ./android/gate/ ./internal/design/ -count=1` green after the amendment (`s23_kit_test.go`
reads row 23 by number and still finds it). Commit `fb439c52` (`Re-measure the focus ring's contrast
on Slate in substrate-components row 23 and its 1.1 note`).

## The gates on the committed tree

**Go**, from a script (`go build ./... ; go vet ./... ; golangci-lint run` with
`GOLANGCI_LINT_CACHE` in the worktree, then the test command above), on the tree at `faf4e589`
plus the working copy; 63 packages `ok`, no `FAIL` line, no load-timing rerun needed:

```
START 2026-08-28 07:23:54
GATE build exit 0
GATE vet exit 0
GATE lint exit 0
GATE test exit 0
GO GATES DONE 2026-08-28 07:33:18
```

`golangci-lint` 2.12.2 (the version `.github/workflows/ci.yml` pins), `go1.26.5`. The
`android/gate` and `internal/design` packages were run again after the row-23 amendment
(`fb439c52`), both `ok` (recorded under ruling 2 above); no other package reads
`substrate-components.md`.

**Kotlin**, from the serialised script (`pgrep -f gradle-wrapper.jar` empty, START recorded,
`. android/toolchain.env && cd android && ./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache`,
then every result XML newer than START and `app/libs/swarm.aar` unmoved).

Run 1, on `faf4e589` with the row-23 amendment written after Gradle had staged
`substrate-components.md` (07:25 staged, 07:27 amended):

```
START 2026-08-28 07:24:34 aar_mtime_before=1787859790
GATE kotlin exit 1
RESULT XMLS: 202 files, 0 older than START
SUMMARY tests=1589 failures=2 errors=0 skipped=0 fresh=202/202 aar_moved=no
KOTLIN GATE DONE 2026-08-28 07:32:51
```

Run 2, on the committed `fb439c52` (queued behind run 1 on the lane):

```
LANE FREE 07:33:00
START 2026-08-28 07:33:00 aar_mtime_before=1787859790
GATE kotlin exit 1
RESULT XMLS: 202 files, 0 older than START
SUMMARY tests=1589 failures=2 errors=0 skipped=0 fresh=202/202 aar_moved=no
KOTLIN GATE DONE 2026-08-28 07:38:12
```

Both runs fail the same two tests, and both are guards whose premise ADR-021 moved rather than
defects in a component; they are the RED for the two rewrites that follow, each ruled on by the
lead before the test was touched:

```
NoticeTest > the notice is smaller than the platform default it used to render at FAILED
    java.lang.AssertionError: the notice renders at 14.0 sp against the platform's 14.0 sp. This component exists because a bare TextView is not unstyled -- it is styled bigger than anything in the ladder
ScreenAirSweepTest > the sweep can see a flush leaf and a doubled one FAILED
    java.lang.AssertionError: the sweep counted one air spend where two containers spent it
```

- `ScreenAirSweepTest.airSteps()` (`:114`) is `setOf(space_12, space_24)`, "the destinations'
  `swarm_space_12`" -- the step `sessionList` spent until W4.5 moved it to `space_16` (ADR-021 D2,
  ruling 1). The file's own negative control nests one `sessionList` in another and expects two
  air spends; at 16 neither registers. The ruled floor `air()` = `space_12` is untouched and every
  real-screen sweep in the file passes.
- `NoticeTest:90` asserts `line().textSize < TextView(context).textSize`. The notice is
  `Body.Secondary`, which R9 (ADR-021 D3) put on the 14sp body rung; a bare `TextView` is 14sp.
  The guard exists so a dropped `TextAppearance` would be caught by rendering BIGGER; size alone
  can no longer carry that.

### The sweep: ruled, rewritten, green (`913c4b5a`)

Lead's ruling (2026-08-28): `airSteps()` becomes `setOf(space_12, space_16, space_24)`, the old set
quoted, the KDoc naming `space_16` as `sessionList`'s step under ADR-021 D2; the whole file rerun.
Targeted lane run (`:app:testDebugUnitTest --tests ScreenAirSweepTest --tests NoticeTest --rerun-tasks --no-build-cache`,
lane waited for 08:48-08:53, run 08:53:22-08:57:45):

```
<testsuite name="dev.swarm.phone.ui.screens.ScreenAirSweepTest" tests="7" skipped="0" failures="0" errors="0"
```

All seven pass, the doubling control and `no leaf is given the screen's air twice` on every
destination among them. The air-spend histogram per destination -- leaves grouped by how many
times the side air was spent above them, printed by a temporary line in the test that was removed
before the commit -- with nothing at more than one:

```
AIRSPENDS Inbox: {0=11, 1=1}
AIRSPENDS Activity: {0=4, 1=2}
AIRSPENDS Settings: {0=6, 1=14}
AIRSPENDS Session detail: {0=1, 1=11}
AIRSPENDS Launch form: {0=2, 1=4}
AIRSPENDS Approval sheet: {1=4}
AIRSPENDS Pair-only offer: {1=5}
AIRSPENDS Pairing (started): {1=9}
```

This is the pre-W4.5 count by construction: the only unpainted box whose padding moved is
`sessionList` (12 to 16) and each counts once; the activity row, the bubble and the toast pay
their 16 inside a painted box, which the walk does not count. The pre-W4.5 tree was not re-run
to measure it.

### The notice: the ruled rewrite (a) cannot hold, and why

Ruling (2)(a) -- `textSize <= the platform default` and `lineHeight != a bare TextView's` -- was
applied and run in the same targeted run, with a temporary print of the view's own values:

```
NoticeTest > the notice is no bigger than the platform default and carries the ladder's leading FAILED
    java.lang.AssertionError at NoticeTest.kt:109
NOTICE textSize notice=14.0 bare=14.0 lineHeight notice=16 bare=16
```

`notice()` is `Kit.textView` (a framework `TextView`, not AppCompat) plus
`setTextAppearance(Body_Secondary)` plus `setTextColor(noticeInk(kind))`; nothing under
`app/src/main` calls `setLineHeight`, and a style's `android:lineHeight` does not travel through
`setTextAppearance` on a framework `TextView` (Robolectric 4.16.1). So the leading cannot
discriminate -- and on the body rung every other attribute of `Body.Secondary` (sans-serif, 400,
tracking 0, 14sp) is the platform default with the ink set outside the appearance, so a dropped
`setTextAppearance` on the notice is invisible to every rendered attribute; the file's first
test would pass too. The guard's "is it styled at all" half has no attribute left, a consequence
of D3 rather than a defect the wave introduced. Reported to the lead for a second ruling.

**Side finding, outside this wave's files:** every `android:lineHeight` in `type.xml` (the leadings
ADR-012 P6 and ADR-021 D3 recompute, which `DesignScaleResolutionTest` asserts from the XML text)
never renders on a `Kit.textView` view. Pre-existing since the leadings were introduced; a bead,
not a W4 change.

### The notice: ruled (b), deleted (`b7eccbf1`)

Lead's ruling (2026-08-28): delete the test under a DELETED note quoting the old assertion and
stating both facts -- on the body rung every rendered attribute of `Body.Secondary` equals the
platform default, and `android:lineHeight` does not travel through `setTextAppearance` on a
framework `TextView` -- so no attribute is left to discriminate a dropped appearance, and a test
asserting `14 <= 14` would be a claim nobody could fail. The notice's rung and ink stay pinned by
the file's first test (`the notice is Body Secondary in the secondary ink`, `textClaims` against
`.prow .ln` in `--p-ink2`). `NoticeTest` keeps seven tests. The temporary print of the (a) attempt
went with the deleted block; `grep TEMPORARY` on the file is empty and the commit refused to
proceed otherwise.

## Found, not fixed

- **`android:lineHeight` never renders on a `Kit.textView` view.** Every `TextAppearance.Swarm.*`
  leading in `type.xml` (`Body.Message` 20.3sp, `Body.Secondary` 19.6sp, `Mono.Code` 18.75sp,
  `Mono.CodeSmall` 19.375sp, `Mono.Fine` 17.6sp; the values ADR-012 P6 introduced and ADR-021 D3
  recomputed) is applied through `setTextAppearance` on a framework `TextView`, and `lineHeight`
  is a `TextView` attribute rather than a `TextAppearance` one, so it is dropped on a device as
  under Robolectric. Measured on the notice (`Body.Secondary`) against a bare `TextView` in the
  targeted run: `NOTICE textSize notice=14.0 bare=14.0 lineHeight notice=16 bare=16`.
  `DesignScaleResolutionTest` asserts the leadings from the XML text, which is why nothing caught
  it. Pre-existing since the leadings were introduced; outside W4's files; the lead is filing its
  own bead.
- `docs/design/conversation-drawing.html:10,12` keeps an Obsidian `:root` block nothing reads
  (W5's file; the lead adds it to W5's brief).
- `scripts/render-play-assets.py` still draws the pre-Solid-Wedge chevron in Substrate colours;
  stale since the mark changed, not used by this wave (the PNGs were rendered from the SVG).

## Run 3 and the closing gate line

**Kotlin run 3**, the same serialised script, on the committed tree `b7eccbf1` (the lane was
free; `tests` is 1588 because one test was deleted under ruling (b)):

```
WAITING for the Gradle lane 09:01:22
LANE FREE 09:01:22
START 2026-08-28 09:01:22 aar_mtime_before=1787859790
GATE kotlin exit 0
RESULT XMLS: 202 files, 0 older than START
SUMMARY tests=1588 failures=0 errors=0 skipped=0 fresh=202/202 aar_moved=no
KOTLIN GATE DONE 2026-08-28 09:06:47
```

**Go after the last commits.** The full `go test -race ./...` above ran on `faf4e589` plus the
working copy. The three commits since touch `docs/design/substrate-components.md` and two Kotlin
test files; the only Go packages that read either are `android/gate` (ten of its files scan
`src/test`) and `internal/design`, and both were re-run on `b7eccbf1`:

```
ok  	github.com/Nathandela/swarm/android/gate	9.986s
ok  	github.com/Nathandela/swarm/internal/design	0.598s
```

| Gate | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `golangci-lint run` (2.12.2) | exit 0 |
| `go test -race -count=1 -timeout 40m ./...` | exit 0, 63 packages ok, no rerun needed |
| `./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache` | run 3: exit 0, tests=1588 failures=0 errors=0 skipped=0 fresh=202/202 aar_moved=no |
| `go test ./android/gate/ -run Icon` | 3 pass, zero edits under `res/` beyond the wave's three `values/` files |
| done-when grep | quoted history only, plus `conversation-drawing.html:10,12` (W5's, by ruling) |

Rulings applied in this file, in order: (1) ADR-012 strikethrough accepted; (2) row 23 and the
1.1 note amended (`fb439c52`); (3) `airSteps()` gains `space_16` (`913c4b5a`); (4) the notice
size guard deleted (`b7eccbf1`). The bead stays open for the lead's review round and merge.
