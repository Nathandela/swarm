# Phone refit W4 — Slate and breathing: RED evidence and gate record

Bead `agents-tracker-d45a.4` · playbook `docs/specifications/phone-refit-playbook.md` section 5 ·
ADR: [ADR-020](../adr/ADR-020-slate-palette-and-breathing-scale.md) · worktree `refit-w4`,
branch `refit/w4` · 2026-08-27.

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
    tokens_test.go:192: PB-TOK-2/ADR-020 D1: recorded skin "obsidian" has no block in docs/research/slate-maquette.html. The skin is a DECISION, so adding one here is an ADR's job, not a JSON edit's.
--- FAIL: TestTheDriftCheckCanActuallyFail (0.00s)
    tokens_test.go:226: design source docs/research/slate-maquette.html not readable: open ../../docs/research/slate-maquette.html: no such file or directory
--- FAIL: TestChosenSkinIsSlateAndPinnedDark (0.00s)
    tokens_test.go:301: PB-TOK-2/ADR-020 D1: skin must be "slate", got "obsidian"
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
rewritten message citing ADR-020 D1; its RED and GREEN are Kotlin-lane runs recorded below. The
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
   and `MotionTest`, neither in this wave. Recorded in ADR-020 D1.
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
**GREEN edit:** the control is retargeted to `--p-card`, a pair ADR-020 keeps distinct, under an
AUTHORIZED REWRITE note quoting the old form. No exemption was added. Run 2, on the committed
tree, is recorded in the gate table at the end.

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
identifiers `obsidian*` became `slate*` and the tests `TestADR009D8_*` became `TestADR020_*`; every
ADR-009 D8.1 citation stays as the floors' history and each floor's comment gained a Slate column.
The in-memory controls that named Obsidian's ladder were retargeted to Slate's (`#131824`/`#1b2334`,
`#131824`/`#0b0e14`, `rgba(11,14,20,1)`), quoted in place.

**RED** — the champagne proof's own claim (ceiling below the retired large floor of 60), written
first against the live `--p-hero` read through the join (`go test ./android/gate/ -run
'ADR020_AFloorNoInkCanReach' -count=1`):

```
--- FAIL: TestADR020_AFloorNoInkCanReachIsAFloorAndNotAPalette (0.05s)
    slate_contrast_test.go:974: apcaCeiling(--p-hero #8eb4e6) = 62.04, at or above the RETIRED large floor of 60 (the champagne proof's claim, applied to the live fill). ADR-020 re-derived this proof on a fill that clears it (62.04 on #8eb4e6); a fill that does not is the champagne case again and the proof must be re-derived, not inherited.
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
--- PASS: TestADR020_EveryInkOnSurfacePairClearsItsAPCAFloor (0.05s)
--- PASS: TestADR020_EveryRoleFloorIsDeclaredAgainstItsRung (0.00s)
--- PASS: TestADR020_TheStateIndicatorsClearWCAGNonTextContrast (0.00s)
--- PASS: TestADR020_TheContrastCheckerCanActuallyFail (0.00s)
--- PASS: TestADR020_AFloorNoInkCanReachIsAFloorAndNotAPalette (0.00s)
ok  	github.com/Nathandela/swarm/android/gate	3.990s
```

`go vet ./android/gate/` and `golangci-lint run ./android/gate/` clean after the rename.
