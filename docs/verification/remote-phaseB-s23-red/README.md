# S23 RED evidence — PB-DS-6, PB-DS-7, PB-DS-8, PB-DS-10

GG-5 requires the failing-first run to be evidenced. This directory holds the actual output, not a
claim that it happened.

## Files

This table described three of the ten evidence files, which made the other seven discoverable only
by listing the directory. All of them are here now, in the order they were written.

**The original failing-first runs**, from before the implementation existed:

| File | What it is |
|---|---|
| `gate-red.txt` | `go test ./android/gate/ -run "TestPBDS[567]_" -v` with the eleven kit files absent. 8 failing, 5 passing. |
| `kotlin-red.txt` | The three Robolectric suites against a kit of signatures with `TODO()` bodies. 26 failing of 29. |
| `motion-red.txt` | PB-DS-8's, not this slice's: `MotionTest.kt` run against a `dev.swarm.phone.ui.kit.Motion` that did not yet exist, failing on the missing implementation rather than on a syntax error. |
| `negative-controls.txt` | 18 mutations, each with the failure it produces — the two defects they found in this slice's own fences, one recorded non-mutation (the elevation fence is not trippable by a comment), and one over the pitch probe itself. Carries an integrity note: a re-audit found four records that were not evidence, two corrected in place and three marked VOID. |

**The audit rounds**, each recording defects found in work that was already green:

| File | What it is |
|---|---|
| `kitfix-red.txt` | Round one, the kit: six defects an adversarial committee found, and the run showing each fence failing before its fix. |
| `motionfix-red.txt` | Round one, motion: four defects in the S23 motion work, each with the real output and the values it reported. |
| `motionjoin-red.txt` | Round two, motion: two defects in `MotionTest.kt` itself — a false claim in its header and an anchor test that could not fail. |
| `fences-red.txt` | Round three: the finding that both fences were allowlists of SPELLINGS rather than of behaviour. The round-one defects were closed and the CLASS was not. |
| `crosscheck-red.txt` | Round three, the metric cross-check: the runs showing the fence green where it should have been red, and red after the fix. |
| `contested-hold-red.txt` | Not a round: making ADR-007 B134 decision 1's CONTESTED hold mechanical, so a gate cannot report agreement the designer has not given. |
| `closeout-red.txt` | Round four: the five findings that closed the slice — the kit meaning 11 of 12 files, a number spelled inside a string, a completeness claim covering two of three states, a self-confirming control, and an invariant never run on the real sources. States what is still fail-open. |

## How these were produced, exactly

The tests were written before the implementation existed and were run then. Both files here are a
**re-run of the final assertions**, so what is recorded is the wording the tests ship with rather
than the wording of an earlier draft.

The two lanes needed two different trees, and the difference is the point rather than an
inconvenience:

- **The Go gate's subject is the kit's STRUCTURE** — which files exist, what they declare, which
  design rule each cites. Its honest failing-first state is therefore *no kit at all*, so
  `gate-red.txt` is a run with the eleven files this slice owns deleted. `ui/kit/Motion.kt` stays:
  it is PB-DS-8's, written concurrently, and not this slice's to remove.
- **The Robolectric suite's subject is what a component RESOLVES**, so it must be able to
  construct one. A tree with no kit does not compile, and *a build failure is not a failing test*
  — it cannot be read as RED evidence for anything, which is the correction S22b's own evidence
  had to make. So `kotlin-red.txt` is a run against a kit of real signatures with `TODO("S23")`
  bodies: every test reaches its subject, calls it, and fails on `NotImplementedError` rather than
  on the compiler.

## Why five gate tests pass in `gate-red.txt`

Correct, and not padding. Three of them are FENCES — "no colour literal", "no typeface", "no
elevation" — and a kit that does not exist violates none of them; they go red the moment a
violation is written, which is what the mutation ledger demonstrates. The other two,
`TheMetricJoinCanActuallyFail` and `TheAnnotationParserCanActuallyFail`, are negative controls in
the sense `TestPBTOK1_TheComparisonCanActuallyFail` established: they are green now, they must stay
green, and they would go red the day a comparator is "simplified" into one that cannot fail.

The same holds for the three passing Kotlin tests. They are the per-suite negative controls, which
exercise the comparison and the design readers rather than the kit.

## What the RED run caught in the tests themselves

Recorded because it is the loop working.

**1. The scale step for the tab item's gap was wrong in the gate's own table.** The first run of
`TestPBDS6_EveryKitSpacingIsTheLedgersStep` reported that `.ptabs div { gap }` is 4 px, which
PB-DS-1's ledger absorbs into `swarm_space_4`, against a table row claiming `swarm_space_8`. The
table is a reviewable claim about which declaration a component renders; the ledger is the
authority for what step that declaration becomes, and the gate computing the second from the design
is what caught the first.

## Two facts about Android the first GREEN attempt turned up

Both are properties of the platform rather than defects in the kit, and both are recorded because
the type scale was specified without them.

**1. `TextView` quantises text size to whole pixels, and five of the eighteen styles are
fractional.** `android:textSize` is read with `TypedArray.getDimensionPixelSize`, which is
`(int)(f + 0.5f)`. At density 1.0 the 9.5 sp tab label renders at 10 px and the 10.5 sp section
label at 11 px — up to 0.5 px above their stated size. The error is absolute in pixels, so it
shrinks as density rises: on a 2.75x handset 9.5 sp is 26.125 px, quantised to 26, which is
9.45 sp. `KitOrigin.quantisedTextSize` recomputes it with the platform's own rule rather than
hiding it behind a tolerance, so what the assertions say is "the size is the design's, as the
platform is able to express it".

**2. Robolectric's default graphics mode makes every font measure fixed-pitch.** The suites assert
family as PITCH, because a TextView's resolved typeface has no readable name. Under LEGACY
graphics — Robolectric's default — `measureText` returns one pixel per character, so `i` and `W`
advance identically and every sans style reports as monospace. All three suites therefore carry
`@GraphicsMode(NATIVE)`, the same annotation `MonoBoxDrawingTest` needs for the same reason, and
all three controls assert `KitOrigin.typefaceProbeFaults()` is empty — which validates the probe
against `Typeface.MONOSPACE` and `Typeface.SANS_SERIF` constructed directly, with no view, no style
and no resource table involved.

**Why the probe is validated separately from the components.** Two different faults produce the
identical symptom "a sans selector reports monospace": the probe cannot tell any two faces apart, or
`setTextAppearance` is not delivering `android:fontFamily`. They have opposite fixes, so blaming a
component before ruling out the probe is guesswork. The last entry in `negative-controls.txt` widens
the probe's tolerance until it answers "fixed pitch" for everything and records what happens: the
three control tests name the probe, and four component tests fail on sans selectors across the nav
header, the chip, the tab items and the session row. That is the fault signature, isolated.

## What the negative controls found in the fences

Two of the seventeen mutations passed on the first run, and both were defects in this slice's own
gate rather than in the kit: the PB-DS-5 fence could not see `elevation = 2f` inside an `apply {}`
block (the idiomatic spelling, with no receiver and no dot), and the raw-dimension scan bounded its
argument list with `[^)]*`, which stops at the first close paren — inside the nested `Kit.dimen(…)`
of the next argument. Both are fixed, both now fire, and both carry a probe inside the test so the
recognisers cannot quietly stop matching. `negative-controls.txt` has the detail.
