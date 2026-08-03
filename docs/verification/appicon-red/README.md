# Launcher icon RED evidence — Solid Wedge becomes the app's launcher icon

GG-5 requires the failing-first run to be evidenced. This directory holds the actual output, not a
claim that it happened.

The owner chose **Solid Wedge** out of the six candidates in `docs/design/icon-candidates/`. Before
this slice the app shipped a placeholder — a white chevron and a rounded cursor rule — so the gate
that says "the shipped mark IS the chosen candidate" had something real to fail against.

## Files

| File | What it is |
|---|---|
| `gate-red.txt` | `go test ./android/gate/ -run TestLauncher -v -count=1` with `android/gate/appicon_test.go` written and no artwork moved. 2 of 4 rows failing; the two that pass say why they pass. |
| `render.py`, `mask.py` | The renderer used to verify the result by looking at it rather than by reading it. They parse the SHIPPED resource files, resolve `@color/` through `colors.xml`, restate what those files say as SVG, rasterise with `rsvg-convert` and apply the launcher masks. No coordinate is typed in either script, so what they draw is the drawable. |

## What the gate asserts

It is a **join**, in the same shape as PB-TOK-1's: the drawable is a transcription of
`docs/design/icon-candidates/solid-wedge.svg` and the gate parses both files and compares the
geometry it evaluates from each. It deliberately holds no copy of the coordinates — a gate carrying
its own answer is a third place for the design to rot, and it goes stale exactly the way the two
files it watches would.

The colour half runs SVG literal → token → resource: the drawable may name only `@color/`
resources, each must have a row in `android/design-tokens.tsv`, and the token that row names must
carry the value the candidate paints. The icon itself holds none of the three.

`TestLauncherIconComparisonCanActuallyFail` is the negative control, driven with perturbed
geometries **in memory** — the working tree is shared with other agents.

## What it does not assert, so the absence is not read as a pass

**The safe zone.** That figure can only be had by rasterising, and this package must run with no
renderer, no JDK and no Android SDK. The measurement lives with the artwork, in the candidate's own
comment, and the join above is what stops the drawable from moving on its own.

That comment was wrong when this slice read it, which is recorded in `gate-red.txt` and filed as
`agents-tracker-aww2`: it claimed max r = 32.5 against the 33 Android guarantees. Re-measured from
rendered pixels, the mark reaches **34.2**, at the cursor bar's bottom-right corner. The 32.5 was
the radius of the previous stepped cursor. The artwork ships at the candidate's own coordinates
regardless — it is the approved drawing — and the overrun is the owner's to accept or close.
