# Obsidian phase O3 — the material kit

ADR: [ADR-009](../adr/ADR-009-obsidian-visual-direction.md) · plan:
[obsidian-migration-plan.md](../specifications/obsidian-migration-plan.md) phase O3 · 2026-08-07

**Status: the five items are implemented and every gate that can run here is green. One exit
criterion is NOT met and is not claimed — the screenshot diff against O2 needs a device or an
emulator this session had neither of. Section 6 says exactly what that leaves unverified.**

O2 flowed the values. O3 spends them: the material that only exists as code. Two of the five items
close counts that `remote-phaseB-requirements.md` has recorded as unmet since 2026-08-01 and that
no gate had ever objected to.

---

## 1. The five items, and what each actually changed

| # | Item | What changed | Where the expectation comes from |
|---|---|---|---|
| 1 | Key-light 0.10 | **nothing — already done in O2** (`e0c7475`), verified below | `--p-card-fx` alpha, via `KitMetrics.KEY_LIGHT_ALPHA` |
| 2 | The lit slab | `cardSurface(attention = true)` moves to `--p-elev` and `--p-lit-fx`; the promotion becomes `InboxRow.lit`, named once | the maquette's `.slab.lit` |
| 3 | The approval sheet | new `sheetSurface`: the app's one vertical gradient, at `--p-sheet-r`, under the strong edge | the maquette's `.sheet` |
| 4 | Grain | new 140×140 warm-neutral raster + `grainOverlay`, on the scaffold's foreground | `--p-grain`, derivation row 21 |
| 5 | Focus ring | `--p-ink` → `--p-hero`; the `#e2a33b` orphan leaves production code and the gate | derivation row 23, amended by ADR-009 D3 |

### Item 1 was already true, and that is worth recording rather than restating

The plan gives O3 "key-light strengthens to 0.10". It already had: O2's `e0c7475` moved
`KitMetrics.KEY_LIGHT_ALPHA` from 0.045 to 0.10 **and** moved the highlight's RGB from
`Color.WHITE` to `--p-ink`'s linen, because the Robolectric appearance suite caught the second one
(`key light colour: design says 0x1AF6F3EC, component resolved 0x1AFFFFFF`). Nothing was changed
here. `KitFoundationTest`'s `the key light is an inset one-dp band clipped to the card radius` and
`TestPBDS7_EveryKitMetricIsTheDesignsOwnNumber` both pass against the token.

### Item 2 changed a decision, not only two values

The two values are the maquette's: `.slab.lit { background: var(--p-elev); box-shadow:
var(--p-lit-fx) }`. Either alone is the wrong drawing — the stronger edge on the card fill is a
card with a bright line on it, and the elevated fill under the resting edge is a **toast**, which
this app already has, painted exactly that way.

The decision that changed is where "which Group is blocked on the human" is made. It was made in
three places: `TriageInboxScreen` counts the tab badge from it, `TriageInbox` orders it first, and
`sessionRow` tested `group == "needs_input"` for its own surface. Three copies disagree the day one
moves — promote a different Group in a future skin and the slab moves while the badge goes on
counting the old one, with every test green because each component was asked for exactly what it
drew. `InboxRow.lit` is the one name now; the kit renders what it is told. ADR-009 D5's sweep fires
from the same fact, so O4 does not need a fourth derivation.

### Item 4 closes a two-month-old silence

`remote-phaseB-requirements.md` §6.20 has said "**PB-DS-5 remains owned by S22 and unmet on both
counts**: no grain raster exists, and …" since 2026-08-01. Every gate in this repository was green
over that, because nothing anywhere asked. Both halves now have fences.

---

## 2. Gate results

| Gate | Result |
|---|---|
| `go build ./...` | green |
| `go vet ./...` | green |
| `go test ./...` | green |
| `golangci-lint run` | green, exit 0, **no new findings** |
| `./gradlew --no-daemon test --rerun` | green — see the counts in section 3, taken out of the JUnit XML |

## 3. The Android suite, counted rather than inferred

Run through `scripts/o2-gradle-run.sh`, which encodes the three Gradle facts O2 paid for (the two
env vars a subagent shell lacks, `--stop` before `--no-daemon` as the terminating form of the
serialization rule, and `${PIPESTATUS[0]}` instead of the pipe's status).

```
$ bash scripts/o2-gradle-run.sh test --rerun
BUILD SUCCESSFUL in 16m 13s
gradle exit status: 0
testDebugUnitTest:   125 result files, 125 written in the last hour
testReleaseUnitTest: 125 result files, 125 written in the last hour

counted out of the JUnit XML, both variants:
  testDebugUnitTest:   125 files, 978 tests, 0 skipped, 0 failures, 0 errors
  testReleaseUnitTest: 125 files, 978 tests, 0 skipped, 0 failures, 0 errors
```

`--rerun` on both variants, so these are an execution rather than an UP-TO-DATE verdict. 970 → 978
tests and 124 → 125 files per variant: `GrainOverlayTest` is the new file, and the eight added
tests are the lit slab's two model claims, the promoted-slab material claim, the rewritten
promoted/resting row claim, the sheet, the grain's three.

Every intermediate RED and GREEN in section 4 was verified the same way: result XML counted, never
an exit code alone, and never a `test-results` directory deleted.

## 4. The RED→GREEN ledger

Ten commits, five pairs. Each RED quotes its own failing output in the commit body.

| Pair | RED | GREEN | The RED, in one line |
|---|---|---|---|
| Focus ring | `287a2ce` | `d4ab50b` | `the ring is the ink row 23 states`: expected 0xFFC9A876, resolved 0xFFF6F3EC |
| Lit slab (material) | `14c37cf` | `38d2769` | `.slab.lit` fill 0xFF1F1A13 vs 0xFF171310; key light 0x38F6F3EC vs 0x1AF6F3EC |
| Lit slab (the name) | `a6045b2` | `c36a7b6` | six `Unresolved reference 'lit'` — the compiler naming the member the tests specify |
| Approval sheet | `961919d` | `9c451b8` | two colours declared, joined, and **drawn by nothing** |
| Grain | `db750f6` | `87dde55` | the raster does not exist; the kit names it nowhere; the scaffold composes nothing |

**Three of the five REDs are compile failures, and that is the honest shape of the test in a
statically typed language.** `Unresolved reference 'lit'`, `'sheetSurface'`, `'grainOverlay'` are
the compiler naming the missing implementation the suite specifies; there is no spelling of those
assertions that both compiles today and fails for the right reason, because reaching the fact
through `group == "needs_input"` is the hand-fed state the recorded qx9m lesson forbids. The other
two REDs are value failures with the numbers quoted, and the two that landed new Go gates ran
**with their negative controls passing in the same red run** — the perturbation controls are what
say the fences can fail at all.

### The three authorized test rewrites, each quoting what it replaced

1. **`FocusRingTest`'s §1.1 rejections** (`287a2ce`). `assertNotEquals(token("--p-hero"), ink)` is
   *reversed*, not weakened: hero was rejected because it meant SELECTED, and in Obsidian the
   accent means "you". `--p-att` is deliberately absent from the new rejection list — it
   value-aliases `--p-hero` by D6, so a distinctness claim there would assert against the decision.
2. **`KitFoundationTest`'s `the fill is unchanged` claim** (`14c37cf`). It stated Substrate's
   decision as a property. Re-pointed at `.slab.lit { background }` in the maquette rather than
   deleted, so it still fails if the promoted fill drifts.
3. **`InboxRowTest`'s `only the NeedsInput row is the attention variant`** (`c36a7b6`). It asserted
   that the KIT knows which Group is blocked. The binding half moved to `TriageInboxViewTest`,
   driven by the real resolver; the half that was always this component's now covers **both**
   answers and all four values instead of four Group strings producing two.

## 5. What is now fenced that was not

| New gate | What it catches |
|---|---|
| `TestPBDS5_EveryColourResourceIsSpentBySomethingThatDraws` | a colour that reaches `colors.xml`, passes the whole token join, and is drawn by nothing. `--p-sheet-hi`/`--p-sheet-lo` were in exactly that state for the length of O2. |
| `TestPBDS5_TheGrainRasterIsTheCheckedInWarmNeutralTile` | the tile missing, off-centre in luma, flat, cool, or chromatic — measured out of the PNG, size read from row 21 |
| `TestPBDS5_TheGrainOverlayReachesTheScaffold` | the raster existing with nothing opening it, or a screen composing an overlay that never touches it |
| `DesignScale.maquetteKitCss` + three `KitOrigin` readers | the Robolectric suite could not read ADR-009 D2's normative source at all until now, so any fact stated only in the maquette had to be transcribed into a test — which is how a suite comes to agree with itself forever |
| `origin: --p-grain opacity` | a fourth part kind in the metric join, for the one token whose whole value is a number |

Each carries its own negative control, perturbing **in memory** — never a file on disk.

## 6. What this evidence does NOT establish

Stated plainly, because a partial recorded as complete is worse than a partial.

1. **No screenshot diff against O2.** The plan's O3 exit asks for one showing only the intended
   deltas. This session had no device and no emulator. Four visible changes are therefore verified
   in resolved values and pixels-in-a-JVM-bitmap but **not on a panel**: the promoted slab's new
   fill, the grain at 4% over the whole app, the champagne focus ring, and — indirectly — whether
   the grain's soft-light composite reads as texture rather than as haze on a real OLED. The O7
   device QA protocol is where those land; the dimmed-state pass in particular is the one that
   matters for a promoted slab now sitting on `--p-elev`.
2. **`sheetSurface` has no screen.** The app composes no approval sheet at all; the plan gives that
   to O6 (the pull-quote headline over the mono well). The recipe is implemented and asserted
   against the maquette, and the colour-spend gate says in as many words that it cannot see
   composition. This is a recipe waiting for its screen, not a rendered surface.
3. **The maquette's `.slab.lit` draws no rail and no warmed border, and this build keeps both.**
   ADR-009's amendment to PB-DS-5 says "Two effects are added, not substituted", so Substrate's two
   existing ways of saying "this row needs you" survive. That is a real divergence between the
   drawing and the requirement; O3's scope is the material, so the requirement wins here and the
   drawing is left to a decision that names it rather than to a silent deletion inside a material
   commit. **It needs an owner's eye at O7's glance pass.**
4. **The maquette gives `.banner` the same sheet gradient and the same strong edge**, which D4.4's
   "the only vertical gradient" does not anticipate. The app's banner is untouched here. Same
   disposition as item 3: a divergence recorded rather than resolved in passing.
5. **`colourTokenCount` is still 17.** ADR-009 D8.3 and PB-TOK-5 both say the count-pinned gates
   move to 19 at O2. They did not — the pin is a floor (`len(colours) < colourTokenCount`), so 19
   satisfied it silently and O2 went green without touching it. That is an **O2 gap, not an O3
   one**, left for the phase that owns it rather than tightened in passing from another; it is a
   one-line change with a RED-first rewrite quoting the old assertion.
