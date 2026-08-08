# Obsidian phase O5 — JetBrains Mono, and the death of an 18 percent frame

ADR: [ADR-009](../adr/ADR-009-obsidian-visual-direction.md) D7 (with its 2026-08-07 amendment) ·
plan: [obsidian-migration-plan.md](../specifications/obsidian-migration-plan.md) phase O5 ·
2026-08-07

**Status: the phase's five items are done and every gate that can run here is green — go
build/test/vet, golangci-lint, and 1015 Robolectric tests on each variant with 0 failures. Two
things are NOT
claimed and are stated in §6: the visual comparison of the 19 styles against the maquette (plan
item 3) needs a device, and the OFL notice is checked into the repository but is not yet surfaced
in the app's legal screen. One plan line could not be executed as written and was ruled on rather
than fudged; §2 is that ruling.**

The measured headline, from the test's own stdout:

```
ADR-007 B134 residual, still reproducing on Typeface.MONOSPACE at 100.0px:
    ASCII 60.0, box-drawing 71.0 (18.3% wider).
ADR-009 D7 / O5 measured at 100.0px:
    ASCII advance 60.0, box-drawing advance 60.0, features `tnum, zero, calt`. The frame aligns.
```

---

## 1. What changed

| # | Item | What changed |
|---|---|---|
| 1 | The faces | `res/font/jetbrains_mono_regular.ttf` (400), `res/font/jetbrains_mono_medium.ttf` (500), `res/font/jetbrains_mono.xml`; licence and authors at `docs/design/fonts/` |
| 2 | The substitution | `s22bFontSubstitution["--p-mono"]` and `TypeScale.ANDROID_FAMILY["--p-mono"]`: `monospace` → `@font/jetbrains_mono`. **`tokens.json` is unchanged — see §2.** |
| 3 | The features | `android:fontFeatureSettings` = `tnum, zero, calt` on all nine mono styles, on no sans style; asserted in both directions by the Go gate AND by the resolved-attribute test |
| 4 | The defect | `MonoBoxDrawingTest` flips from pinning the residual to asserting its absence, and keeps the old inequality as a control |
| 5 | The record | ADR-009 D7 amendment; ADR-007 B134 decision 2 supersession note; PB-DS-3 requirement cell; `type.xml`'s own header |

Provenance: `JetBrains/JetBrainsMono` release v2.304, `JetBrainsMono-2.304.zip`
(sha256 `6f6376c6ed2960ea8a963cd7387ec9d76e3f629125bc33d1fdcd7eb7012f7bbf`), OFL-1.1.
Regular `a0bf60ef0f83c5ed4d7a75d45838548b1f6873372dfac88f71804491898d138f`,
Medium `31c92d01a8a08528b718a43addf0ad3df0af2ca4b7b3290a452f70f358e14d3d`. `file(1)` reports both
as TrueType. Neither was modified: the only change is the resource-legal file name.

## 2. The one plan line that could not be executed, and what was done instead

Plan O5 item 1 says "`--p-mono` token updates (one value change through the pipeline)", and
ADR-009 D3's row for the token predicts "O5 prepends bundled JetBrains Mono". **Both are
unexecutable, and the reason is a rule this migration made deliberately.**

ADR-009 D2 makes `docs/research/obsidian-maquette.html` the normative design source, and
`internal/design/tokens_test.go`'s `TestTokenSourceMatchesChosenSkinInDesignHTML` joins
`tokens.json` to its `:root` in **both** directions with no exception mechanism. The maquette
declares `--p-mono: ui-monospace, "SF Mono", Menlo, monospace`. So prepending a family name to the
JSON is a drift failure by construction, and the two ways out are both wrong: editing the
owner-signed maquette to satisfy a gate is the tail wagging the dog, and adding a per-token
exemption to the drift check is exactly the "bend the test" move this repository forbids.

**The bundling belongs one layer down and always did.** PB-DS-3 exists *because* the token states
a stack the platform cannot supply (`SF Mono` is not licensable off Apple); the substitution table
is what says what Android renders for it. That is the layer that moved. The value still flows one
way — `tokens.json` (design intent, unchanged) → substitution (`@font/jetbrains_mono`) →
`type.xml` → the resolved typeface — and every hop is gated. ADR-009 D7's amendment records this
as a correction to D3's row rather than leaving the plan line looking merely unfinished.

## 3. The measurement

`MonoBoxDrawingTest`, `@GraphicsMode(NATIVE)`, `Paint.measureText` at 100 px through
`TextView.setTextAppearance(R.style.TextAppearance_Swarm_Mono_Code)` — the same path the terminal
peek renders through, not a hand-built Paint.

| | ASCII (`M`) | box drawing (`U+250C`) | verdict |
|---|---|---|---|
| `Typeface.MONOSPACE` (the platform, control) | 60.0 | 71.0 | **18.3% wider** — ADR-007 B134's residual, still real |
| `@font/jetbrains_mono` (the app, after O5) | 60.0 | 60.0 | equal — the frame aligns |

Four assertions carry it, and the shape of each is deliberate:

1. **Per character, not summed.** Every non-space character of `┌─┬─┐ │ └─┴─┘` is measured
   individually. A sum can be right while two glyphs are wrong in opposite directions, and a frame
   with one wide corner is still a frame that does not line up.
2. **Then the whole string**, so a per-character equality could not hide a shaping rule that
   closes the gaps up when the characters are laid out together.
3. **`hasGlyph` is asserted but is not the test**, and the old file's reasoning is kept verbatim:
   it consults the whole fallback chain rather than the named family and returns true for every
   one of these characters, so a test built on it would have been green through the entire life of
   the defect.
4. **The platform control is the reason the equality means anything.** An equality is precisely
   what a collapsed text stack produces — stubbed measurement, a fallback chain that stopped
   distinguishing anything — so `the platform mono family still lays box drawing at a different
   advance` must stay RED-shaped. If it ever goes green the platform gained the block, the bundle
   proves nothing, and B134's residual needs rewriting again. `TEXT_MEASUREMENT_IS_REAL` guards
   the same failure from the other side.

**Both faces advance 600/1000 em on every glyph in ASCII, U+2500–257F and U+00B7**, verified off
the `hmtx` table before either was checked in. That is why a mono style asking for weight 600 —
five of the nine do — cannot disturb the grid: it resolves to the 500 face (nearest available; the
100-unit gap is well under the 300 that triggers synthetic bolding) and the advance is identical.

## 4. Font features

`tnum, zero, calt` on all nine mono styles, nothing on the ten sans ones. Three checks, at three
different distances from the file:

- `s22bStyleFaults` (Go): both directions over `type.xml` as text, with a mono spec added to the
  perturbation control — every pre-existing perturbation ran against a *sans* spec, so the new
  branch would otherwise have been unexercised.
- `DesignScaleResolutionTest` (Robolectric): the value the **merged resource table** resolves,
  which is a different question from what the file declares.
- `MonoBoxDrawingTest`: the value that reaches the **Paint**, which is a third question again —
  this repository has already paid once for assuming a declaration implies delivery.

`calt` is stated even though JetBrains Mono ships it on, because `android:fontFeatureSettings` is
a full override rather than an addition: naming two features without it would silently switch the
family's contextual alternates off.

## 5. Gates and cost

```
go build ./...        clean
go vet ./...          clean
go test ./...         all packages ok
golangci-lint run     clean (no new findings)
./gradlew --no-daemon test assembleDebug     BUILD SUCCESSFUL in 11m 33s
   testDebugUnitTest    126 suites, 1015 tests, 0 failures, 0 errors, 0 skipped
   testReleaseUnitTest  126 suites, 1015 tests, 0 failures, 0 errors, 0 skipped
```

Counted off the testsuite XML files and their mtimes rather than off the exit code — Gradle is
serialized on this machine and an up-to-date task reports success without running anything.

APK, `assembleDebug`, same machine and same AAR immediately before and after:

| | bytes |
|---|---|
| before | 35,165,498 |
| after | 35,574,834 |
| **delta** | **+409,336 (+1.16%)** |

The three `res/font/` entries account for 257,994 bytes of that compressed (regular 273,900 →
128,157; medium 273,860 → 129,597; the family XML 540 → 240). The remaining ~151 KB is packaging
overhead this measurement does not decompose, and the whole-file delta is the honest number to
quote. **No subsetting**: the peek renders whatever an agent TUI emits, so a subset chosen against
today's screens is a tofu bug waiting for tomorrow's — which is the failure mode this decision
exists to remove.

## 6. What this does NOT verify

1. **Plan item 3 — the visual comparison of the 19 styles against the maquette — did not happen.**
   It needs a rendered screen and this session had no device and no emulator. What is asserted
   mechanically is that every style resolves to the design's own size, weight, tracking, family
   and leading, in the merged resource table; what is not asserted is that the result *looks*
   right at 11.5 sp on glass. O7's device pass is where that lands.
2. **The measurement is Robolectric's runtime, not a handset.** It is AOSP's font configuration
   plus this module's own resource table — which is what a stock handset ships and is exactly the
   limit the previous version of this test stated about itself. It is not a survey of OEM font
   customisation, and it is not a photograph of the peek.
3. **The OFL notice is not surfaced in the app.** `docs/design/fonts/JetBrainsMono-OFL.txt` and
   `-AUTHORS.txt` are checked in and the gate requires the licence file to exist, which satisfies
   OFL-1.1's "the licence travels with the font" for the repository. The APK ships the font
   without an in-app attribution screen. Plan O6 owns the settings/legal section; this is the
   thing to put in it, and it is recorded here rather than assumed done.
4. **Release-variant behaviour is unexercised beyond its unit tests.** No `bundleRelease` was
   produced (it needs operator signing material), so the AAB's font contribution — which is what
   the Play Store actually serves, and which will differ from the debug APK — is unmeasured. O7
   builds the release; the number to record there is the AAB delta, not this one.
