# Obsidian phase O4 — the motion register and the specular sweep

ADR: [ADR-009](../adr/ADR-009-obsidian-visual-direction.md) D5 and D8.2 · plan:
[obsidian-migration-plan.md](../specifications/obsidian-migration-plan.md) phase O4 · 2026-08-07

**Status: the three items are implemented and every gate that can run here is green. One exit
criterion is NOT met and is not claimed — the plan asks for a "reduced-motion device pass
recorded", and this session had neither a device nor an emulator. Section 6 says exactly what that
leaves unverified, and it is more than usual: the sweep is the one effect in this skin that no
still frame can show.**

O3 spent the material. O4 spends the time: the register everything moves at, and the one new thing
that moves.

---

## 1. The three items, and what each actually changed

| # | Item | What changed | Where the expectation comes from |
|---|---|---|---|
| 1 | The register | `NAV_DURATION_MS` 350 → 300; `NAV_EASE` (0.32,0.72,0,1) → `EASE` (0.22,1,0.36,1); `ENTRANCE_DURATION_MS`, `ENTRANCE_MAX_TRAVEL_DP`, `PRESS_RESPONSE_CEILING_MS` added; toggle and caret unchanged and now joined | ADR-009 D5's table, parsed |
| 2 | The sweep | `Motion.specularSweep` + its private streak drawable + the `inFlightSweep` slot; `sessionRow(promoted =)`; `TriageInboxScreen.promotions`; `triageInboxView(promoted =)`; one line in `PhoneSurface.drawInbox` | `--p-sweep-fx` and the maquette's `.slab.lit.sweep::after` / `@keyframes sweep` |
| 3 | The press audit | nothing in production — a measurement, and a recorded residual | ADR-009 D5's press row |

### Item 1 needed a third design document on the classpath

`remote-control-mock.html` is a **drawing**, and four of D5's six numbers cannot exist in one: an
entrance duration for components the maquette draws at rest, a `max travel` **ceiling** (a bound,
not a distance anything travels), a press bound on a document that cannot be pressed, and a sweep
the maquette itself annotates *"looped at 6s here for display only"*. The other two are the ones D5
**changes** — reading `.sheet { transition }` for the navigation duration after the amendment is
reading the superseded decision with a green test. So ADR-009 itself is staged as a unit-test
resource beside `tokens.json`, the two artifacts and `substrate-components.md`, and `AdrRegister`
parses D5's table.

The one parse decision worth arguing: **the value cell and the rule cell are addressed
separately.** The Navigation row's rule cell reads `replaces 350ms / (0.32, 0.72, 0, 1)`, so a
millis scan over the whole row finds 350 first and pins the app to the number D5 abolished — the
failure mode where the gate and the defect agree.

`NAV_EASE` became `EASE`, which is a decision and not tidying: D5 gives the entrance and both
navigation surfaces one curve (the Navigation row writes *"same curve"* rather than four control
points of its own), and two names for one curve is where the two come to differ.
`the_register_gives_entrance_and_navigation_ONE_curve` reads that cell, so the day it states its
own points this fails instead of silently leaving navigation on the entrance's.

**Two of the new constants are numbers no animator takes**, and their KDoc says so rather than
leaving a reader to wonder. There is no entrance animator to spend `ENTRANCE_DURATION_MS`, and
`PRESS_RESPONSE_CEILING_MS` is by D5's own clause "audited, not animated". They are here because
the register is a decision about the skin rather than about the three animators that exist today: a
constant with a machine-read origin is what stops the next entrance being a fresh `200` typed at a
call site.

### Item 2 is a transition, not a state, and that is the whole design

`lit` (O3) is true for as long as a session waits. `promoted` is true for the one draw in which it
**started** waiting. Deriving the second from the first sweeps every waiting row on every journal
event — the ambient field-register motion D5 bans in the same paragraph that permits this one,
arrived at by forgetting a comparison.

Who transitioned is `TriageInboxScreen.promotions(previous, next)`, and it compares **screens, not
rosters**. What the phone core reports and what the user was looking at are different things: a
session filtered out by the scope chips is not on this viewport, and a screen being drawn for the
first time has nothing to have transitioned from. Both fall out of one comparison, and both are the
correct answer — a sweep is an announcement, and in those cases there is nobody to announce to.
`PhoneSurface.drawInbox` supplies the one input only it has: `inboxDrawn`, *what the inbox last
drew*, read one line before it is overwritten.

Five layers, each owning one thing: Motion owns the effect and its constraints, the screen model
owns who transitioned, the row plays what it is told, the view passes ids through, the surface
supplies the previous screen.

### Item 3 found the wiring correct and nothing painting it

See section 5's press row, and section 6 item 3.

---

## 2. Gate results

| Gate | Result |
|---|---|
| `go build ./...` | green |
| `go vet ./...` | green |
| `go test ./...` | green |
| `golangci-lint run` | green, exit 0, **no new findings** |
| `bash scripts/o2-gradle-run.sh test` | green — counts in section 3, taken out of the JUnit XML |

## 3. The Android suite, counted rather than inferred

```
$ bash scripts/o2-gradle-run.sh test
BUILD SUCCESSFUL in 5m 45s
gradle exit status: 0
testDebugUnitTest:   126 result files, 126 written in the last hour
testReleaseUnitTest: 126 result files, 126 written in the last hour

counted out of the JUnit XML, both variants:
  testDebugUnitTest:   126 files, 1013 tests, 0 skipped, 0 failures, 0 errors
  testReleaseUnitTest: 126 files, 1013 tests, 0 skipped, 0 failures, 0 errors
```

125 → 126 files (`PressFeedbackAuditTest` is the new one) and 978 → 1013 tests: 11 register
assertions, 12 sweep assertions in `MotionTest`, 7 promotion assertions in `TriageInboxScreenTest`,
2 composition assertions in `TriageInboxViewTest`, 2 in `InboxRowTest`, 5 in the press audit — less
the two `nav_ease_*` names that were rewritten rather than added.

Every intermediate RED and GREEN was verified the same way: result XML counted, never an exit code
alone, and no `test-results` directory ever deleted. The intermediate runs were filtered with
`--tests`, which makes Gradle write only the filtered classes' XML; the counts above come from the
**unfiltered** final run, which regenerated all 126 in both variants.

## 4. The RED→GREEN ledger

| Pair | RED | GREEN | The RED, in one line |
|---|---|---|---|
| The register | `c08a957` | `127c979` | 17 `Unresolved reference` — `EASE`, `ENTRANCE_DURATION_MS`, `ENTRANCE_MAX_TRAVEL_DP`, `PRESS_RESPONSE_CEILING_MS`, the four control points |
| The sweep | `ee62906` | `030ad33` | Go: `Motion.kt declares no fun specularSweep(`, and **10 of the sweep's numbers are not declared at all** — the gate reading each one out of the maquette and the token. Kotlin: 71 compile errors naming `specularSweep`, `inFlightSweep`, `sweepOffsetAt`, `promoted`, `promotions` |
| The press audit | — | `c7a05bd` | none: see below |

**The register's RED is a compile failure**, which is the honest shape of the test in a statically
typed language and the same shape three of O3's five REDs took. The two assertions that *would*
have run — 300 against Motion's 350, and the new curve sampled against the old — could not, because
the file stopped compiling until the register existed. They both pass in the GREEN.

**The sweep's RED ran its negative controls green in the same red run.** All six construction-site
perturbations and both number-join perturbations passed while the two real assertions failed, which
is what says the fence can fail at all rather than being unsatisfiable.

**The press audit has no RED and the commit says so out loud.** D5's press row is the only row of
the register with no animator behind it — "audited, not animated" — so what it asks for is a
measurement, not a behaviour to build red-first. What keeps a green-on-arrival test from being a
tautology is the two controls beside it: an unwired view must **not** report a pressed state, and
the same control inside a scroller must **not** press immediately.

### The authorized test rewrite, quoting what it replaced

`MotionTest`'s five navigation assertions were written for ADR-007 B134 decision 3 and are correct
for it. ADR-009 D5 amends that decision, so they are rewritten RED-first with the old assertions
quoted in `c08a957`'s body — `assertEquals(banner, Motion.NAV_DURATION_MS)` at 350ms,
`assertEquals(fromBanner, listOf(Motion.NAV_EASE_P1X, …))` at (0.32,0.72,0,1), and the three
sampling tests around them.

**Nothing was weakened, and the superseded values are still read.**
`the_navigation_row_records_which_duration_and_curve_it_replaces` requires D5's own row to name the
mock's two values, joined by parsing both documents; `ease_is_not_the_curve_the_register_replaced`
samples the old curve against the new one, so a Motion left on (0.32,0.72,0,1) fails loudly rather
than passing a test nobody rewrote.

## 5. What is now fenced that was not

| New gate | What it catches |
|---|---|
| `TestD82_TheSweepIsConstructedOnlyInsideTheKit` | a sweep built anywhere but `Motion.kt`. PB-DS-8's fence judges spelling — `animat|transition` — and `specularSweep` contains neither, so a screen could have hand-rolled a streak past a green lane. The permitted-receiver test is `animatorPermitted` **called, not copied**. |
| `TestD82_TheSweepBodyHoldsItsThreeConstraints` | a sweep that loops, that cancels its predecessor instead of ending it, that never records itself in the one-per-viewport slot, or that constructs before it asks about reduced motion |
| `TestD82_TheSweepsNumbersAreTheDesignsOwn` | all eight sweep constants, recomputed from `--p-sweep-fx` and the maquette. `s23_kit_test.go` deliberately does not read `Motion.kt` (`s23MotionFile` records why), so this is the join that was previously impossible |
| `AdrRegister` (Kotlin) | the register's six numbers, read from D5's table instead of transcribed. Nothing in the repository could read an ADR before this |
| `PressFeedbackAuditTest` | a control that stops responding to ACTION_DOWN, and a platform tap timeout that outgrows D5's 120ms ceiling |

Each carries its own negative control, perturbing **in memory** — never a file on disk.

Two details in the gate are findings about gates rather than about the app, and are recorded where
they happened:

- The travel end is a **derivation, not a stop read by name**. `@keyframes sweep` states three
  stops and two distinct positions, the far one held from the travel stop to 100% — which is how
  the maquette makes a 500ms sweep out of a 6s display loop (its own comment says so). Reading
  "the 8.3% stop" would bind the gate to the maquette's **display** timing; reading "the position
  that is not the start" binds it to the geometry, which is the part that ships.
- The "records nothing" perturbation had to be `ReplaceAll`. The body assigns the holder twice —
  once to claim the slot, once to release it when the sweep ends — with the release first in source
  order, so perturbing only the first occurrence left the claim intact and the gate was **right**
  to pass it.

## 6. What this evidence does NOT establish

Stated plainly, because a partial recorded as complete is worse than a partial.

1. **No reduced-motion device pass.** The plan's O4 exit asks for one and this session had no
   device and no emulator. What IS established is that `Motion.specularSweep` returns null and
   leaves the one-per-viewport slot empty when `ANIMATOR_DURATION_SCALE == 0`, and that the builder
   consults that setting before it constructs anything (asserted at the source level, with the
   ordering perturbation as its control). What is **not** established is the thing the pass is for:
   that a user with "Remove animations" on sees no flash, no artefact, and no half-drawn highlight
   on a real panel.
2. **Nobody has seen the sweep.** This is the sharpest gap in the phase, and it is structural: the
   effect exists for 500ms, leaves no trace, and no screenshot can contain it. Every constraint on
   it is asserted — one-shot, one per viewport, ended not cancelled, nothing under reduced motion,
   built only in the kit, all eight numbers the design's — and **none of that says it looks right**.
   The three ways it can be wrong on a panel and right in the JVM: the streak may be invisible at
   0.30 over `--p-elev` on an OLED at low brightness; it may read as a rendering artefact rather
   than as light, which ADR-009's own "a premium signifier detected as decoration inverts into
   cheapness" is exactly about; and its first frame lands before the row is measured (bounds are
   read per-frame from the slab, so frame 1 draws nothing) which is correct but has never been
   watched. The plan's own risk row already names the remedy — *if it reads as decoration, the
   sweep is deleted and the lit key-light carries promotion alone*, a one-line kit change. **O7's
   glance pass is where that decision gets its evidence.**
3. **Nothing paints the pressed state.** Every interactive control enters `isPressed` on the
   ACTION_DOWN frame and every one of them is painted by a `SubstrateSurface` — a `LayerDrawable`
   with no state list in it — so the state changes and the pixels do not. This is **not fixed
   here** because a pressed treatment is a design value and ADR-009 D2's normative maquette draws
   none: there is no `:active` rule in the file, and the superseded Substrate mock's
   `.srow:active { background: #2c2c2e }` is a pre-skin iOS grey that is not on Obsidian's warm
   ladder. It is recorded as a **passing assertion** (`no_kit_control_paints_a_pressed_state_yet`)
   rather than a comment, so the day a treatment is drawn and implemented, that test goes red and
   whoever implemented it must delete the row they closed. **It needs a maquette rule and an
   owner's decision, which is an O1-shaped question arriving late.**
4. **The surface wiring has no test of its own.** `TriageInboxScreen.promotions` is asserted seven
   ways as a pure function, and `triageInboxView` is asserted to sweep the id it is handed. What is
   asserted nowhere is the one line in `PhoneSurface.drawInbox` that joins them — that the
   *previous* argument is `inboxDrawn` and that it is read before the assignment overwrites it.
   Driving that needs a `FacadeBridge` over the gomobile AAR, which does not load on the unit-test
   JVM (`PressFeedback`'s own KDoc records the same limitation for the same reason). A reordering
   of those two lines would make every promotion invisible and every test here would stay green.
5. **The entrance row is a decision with no animator.** `ENTRANCE_DURATION_MS` and
   `ENTRANCE_MAX_TRAVEL_DP` are joined to D5 and spent by nothing; the 4dp ceiling in particular is
   a bound that no code can currently violate, so nothing enforces it on the entrance that
   eventually arrives. Whoever builds one must route it through these constants — the KDoc says so,
   and that is all that says so.
6. **`colourTokenCount` is still 17.** Unchanged from O3's section 6 item 5, and still an O2 gap
   rather than this phase's: the pin is a floor, so 19 satisfies it silently.
