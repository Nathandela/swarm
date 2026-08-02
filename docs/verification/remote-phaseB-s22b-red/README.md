# S22b RED evidence — PB-DS-1, PB-DS-2, PB-DS-3, PB-DS-4

GG-5 requires the failing-first run to be evidenced. This directory holds the actual output, not
a claim that it happened.

## Files

| File | What it is |
|---|---|
| `gate-red.txt` | `go test ./android/gate/ -run TestPBDS -v` with `dimens.xml` and `type.xml` absent. 8 failing, 5 passing. |
| `kotlin-red.txt` | The two Robolectric suites under the same condition. 6 failing of 7, with the per-assertion messages from the JUnit XML. |
| `negative-controls.txt` | 12 mutations of the finished implementation, each with the failure it produces, and the restored tree green at the end. |

## How these were produced, exactly

The tests were written before the implementation existed and were run then; that first run is what
the two corrections below came out of. The files here are a **re-run of the final assertions**
against a tree with `dimens.xml` and `type.xml` removed, so what is recorded is the wording the
assertions ship with rather than the wording of an earlier draft. `PhoneSurface.kt` was reverted to
its committed form for those runs, because it references `R.dimen.swarm_space_24` and the module
would not otherwise compile — and a build failure is not a failing test.

Five gate tests pass in `gate-red.txt` and one Kotlin test passes in `kotlin-red.txt`. That is
correct and is not padding: they are the guards, not the requirements.

- `TestPBDS2_TheDocChromeExclusionIsStillTrue`, `TestPBDS3_TheSubstitutionIsTheOneTheADRRecords`
  and `TestPBDS3_NoFontIsBundled` assert facts about the design source and the ADR, which were
  already true before any resource existed.
- `TestPBDS1_TheAbsorptionLedgerCanActuallyFail` and `the design parse can distinguish two values`
  are negative controls in the sense `TestPBTOK1_TheComparisonCanActuallyFail` established: they
  are green now and must stay green, and they would go red the day a comparator is "simplified"
  into one that cannot fail.

## What the first RED run caught in the tests themselves

Both are recorded because they are the loop working, and because the second one is a fact about
the design that no document in the repository stated.

1. **The spacing scale is a recorded assignment, not an arithmetic.** The first version of
   `TestPBDS1_EveryDesignSpacingIsAbsorbedByTheScale` rounded each design literal to the nearest
   step. That cannot reproduce the decision: `5` is equidistant between `4` and `6` and goes
   **down**, `7` is equidistant between `6` and `8` and goes **up**. No tie-break gives both. The
   test reported `nearest(7) = 6`, and the assignment is now a table in the gate.

2. **`swarm_space_24` absorbs nothing in Substrate.** The gate's reverse check — a step may not
   claim a literal the design does not declare — reported that the artifact contains no 24px
   spacing value at all. 24dp comes from the mock (pairing-scaffold padding), and it is
   additionally the step PB-DS-1's own ledger routes the mock's `26px` through. `swarm_space_6` is
   in the same position. Both are kept, with the reason recorded in `dimens.xml` and in the gate.

3. **The mono-rule count was 9, not 11.** `TestPBDS3_EveryMonoRuleBecomesAMonoStyle` asserts the
   number of `--p-mono` rules so that a family resolver which stopped working cannot leave the
   test iterating zero times. The constant was guessed at 11; the design has 9, and
   `.panelframe .cap` — which looks like a tenth — names the undeclared `var(--mono)` and is
   documentation chrome.

## The PB-DS-3 residual, as measured rather than assumed

`MonoBoxDrawingTest` renders `┌─┬─┐ │ └─┴─┘` through `TextAppearance.Swarm.Mono.Code` and reports:

```
PB-DS-3 observed residual at 100.0px: mono advance 60.0, box-drawing advance 71.0
(18.3% wider), tofu: none. The frame renders and does not align.
```

ADR-007 B134 decision 2 records the residual as *tofu*. That is not what happens. Every
box-drawing character resolves — through font **fallback**, at `0.71em` against the monospace
family's own `0.60em`. So the terminal peek draws its frame and the frame is 18.3% wider per
character than the text inside it. `Paint.hasGlyph` returns `true` for all of them, because it
consults the whole fallback chain rather than the named family: a test built on `hasGlyph` would
have been green and would have certified the opposite of the truth.

The measurement is of the AOSP font configuration in Robolectric's Android runtime — the same
Droid Sans Mono and Noto fallback set a stock handset ships. It is not a survey of OEM font
customisation. It is enough to settle the question the ADR left open, which was whether the peek
renders the frame at all, and it makes the failure worse than recorded rather than better: tofu is
visible to anyone who looks at the screen once, a frame that is silently 18% off is not.
