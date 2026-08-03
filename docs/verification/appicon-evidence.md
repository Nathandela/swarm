# Launcher icon — evidence that Solid Wedge is what the app now launches with

The failing-first runs are in [`appicon-red/`](appicon-red/). This is the green side, and the half
of it that matters is not the test output: an adaptive icon that compiles is not an adaptive icon
that looks right, so the drawable was rasterised and looked at.

## What changed

| File | Change |
|---|---|
| `android/app/src/main/res/drawable/ic_launcher_foreground.xml` | The placeholder chevron replaced by the chosen candidate, at the candidate's own coordinates. |
| `android/app/src/main/res/mipmap-anydpi-v26/ic_launcher_round.xml` | Comment only: it claimed the mark sits inside the 66dp safe circle, which is no longer true. |
| `docs/design/icon-candidates/solid-wedge.svg` | Comment only: the safe-zone measurement was stale. See below. |
| `android/gate/appicon_test.go` | New. The join between the candidate and the drawable. |

`versionCode` and `versionName` are untouched. The two `mipmap-anydpi-v26` files already declared
`<monochrome>`; that layer was not added by this slice, it was fenced by it.

## The transcription is exact, not close

The drawable and the design source rasterise to the **same image, byte for byte**:

```
rsvg-convert -w 1296 -h 1296 (source SVG) vs (shipped drawable restated as SVG)
  max channel diff 0    pixels differing at all: 0 of 1679616
```

The right-hand side is not a copy of the SVG. `appicon-red/render.py` parses
`ic_launcher_foreground.xml`, resolves `@color/swarm_hero` through `values/colors.xml`, and restates
what those files say; no coordinate is typed in the script. So the zero above is a statement about
the shipped resource.

## It survives the real toolchain

`aapt2 dump xmltree` on the assembled debug APK, i.e. the compiled binary XML rather than the source:

```
E: vector
  A: android:height=108.000000dp   android:width=108.000000dp
  A: android:viewportWidth=108     android:viewportHeight=108
    E: path
      A: android:pathData="M57,36 L34,48 L57,60 L34,72"
      A: android:strokeColor=@0x7f050060      -> color/swarm_hero  #ff53ce7c
      A: android:strokeWidth=8.5
      A: android:strokeLineCap=0              -> butt
      A: android:strokeLineJoin=0             -> miter
    E: path
      A: android:fillColor=@0x7f050060        -> color/swarm_hero  #ff53ce7c
      A: android:pathData="M62,67.75 L80,67.75 L80,76.25 L62,76.25 Z"

E: adaptive-icon
  E: background   A: android:drawable=@0x7f05005c   -> color/swarm_background  #ff08090a
  E: foreground   A: android:drawable=@0x7f070072   -> drawable/ic_launcher_foreground
  E: monochrome   A: android:drawable=@0x7f070072   -> drawable/ic_launcher_foreground
```

The two enum attributes are the ones worth checking here: `butt` and `miter` are also the platform
defaults, so a file that omitted them would compile to the same bytes and would still be a file that
did not say what the candidate says.

## Rendered and looked at

`appicon-red/mask.py` masks the 108dp canvas the way a launcher does — the mask covers the central
72dp, the outer 18dp on each side is reserve — and reports what each mask shape removes:

```
shipped drawable, unmasked: bbox x 24.96..79.96 y 32.29..76.21  max r 34.16 at (79.96, 76.21)
mask circle   : clips 0 of 117285 mark pixels (0.000%)
mask squircle : clips 0 of 117285 mark pixels (0.000%)
mask rounded  : clips 0 of 117285 mark pixels (0.000%)
mask square   : clips 0 of 117285 mark pixels (0.000%)
```

At 48dp under both masks the three legs keep an open channel between them and the cursor bar stays
a separate shape rather than fusing to the bottom leg, which is the thing the weight came down from
10 to 8.5 to buy. **The monochrome layer reads as a silhouette**: rendered as Android 13 renders a
themed icon — the same foreground flat-tinted, one colour on one colour, no hue left to separate
anything — the mark keeps its shape, because nothing in it relied on colour to be legible. Both are
in `contact_sheet.png`, which `mask.py` writes.

## Against the approved appearance

`docs/design/store-assets/icon-512.png` is the approved look. Binarising both marks and XORing:

```
binarised mark XOR: 611 px of 262144 = 0.233 %
XOR pixels surviving a 3x3 erosion (thicker than a 1px antialiasing fringe): 0
```

Same drawing. The 0.233% is a one-pixel edge fringe, and it is there because the store asset is
framed on the mark's own bounding box rather than on the canvas — its crop solves to
`viewBox 15.54 18.37 72 72` where a canvas-centred crop is `18 18 72 72`. Spans agree to 0.02dp
horizontally and 0.19dp vertically, so it is the same artwork at the same scale, cropped differently
for a square store tile. Nothing to reconcile in the app.

## The safe zone, re-measured

The candidate's comment said `max r = 32.5 against the 33 the launcher mask guarantees`. That is
wrong, and it is what this slice's brief carried forward. Measured from rendered pixels:

```
max radius from centre = 34.2, at the CURSOR BAR's bottom-right corner (80, 76.25)
```

32.5 is the radius of the **previous** cursor — the stepped shape `comparison.html` still draws,
far corner (78, 76) = 32.56. The straight bar (owner direction, 2026-08-03) moved the corner out and
nothing re-measured. The artwork ships at the candidate's own coordinates anyway: it is the approved
drawing, it clips under none of the four masks above, and re-centring an approved mark is not a
transcription decision. The overrun is filed as `agents-tracker-aww2` for the owner. The candidate's
comment now carries the measured figure.

## Runs

```
go build ./... && go vet ./...                     clean
golangci-lint run ./android/gate/...               clean
go test ./android/gate/ -count=1                   ok   4.641s
./gradlew :app:testDebugUnitTest --rerun-tasks     BUILD SUCCESSFUL
  counted from the result XMLs: 91 files, 739 tests, 0 failures, 0 errors, 0 skipped
./gradlew :app:assembleDebug                       BUILD SUCCESSFUL
```

739/0 is the baseline unchanged, which is the expected result: this slice moves a resource that no
Kotlin test reads.
