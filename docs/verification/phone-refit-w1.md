# Phone refit W1 -- Pin the frame: verification evidence

Bead `agents-tracker-d45a.1`. Contract: `docs/specifications/phone-refit-playbook.md` section 2
(W1.1-W1.4). Worktree `refit-w1`, branched from `main` at `1cee3d9`. Every file:line the contract
cites was re-verified against the tree before a test was written; all matched.

## Scope as executed

W1.1 (the top inset is the platform's) and W1.2 (both scroll viewports clip) landed. W1.3 and W1.4
(opaque composer bar and tab bar) were **re-homed in W4** by the orchestrator's ruling, recorded
below, before any file under `ui/kit/` was changed.

## RED -- before any implementation

Run from the serialised lane script (`pgrep -f gradle-wrapper.jar` empty; start recorded; AAR
mtime unchanged; every result file newer than the start), filtered to the touched classes:

```
./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache \
  --tests dev.swarm.phone.PhoneActivityInsetTest \
  --tests dev.swarm.phone.ui.screens.PhoneScaffoldViewTest \
  --tests dev.swarm.phone.ui.screens.ConversationScaffoldViewTest \
  --tests dev.swarm.phone.ui.kit.ComposerTest \
  --tests dev.swarm.phone.ui.kit.InboxChromeTest
GRADLE_EXIT=1
AAR unchanged (mtime 1787859790)
RESULT_FILES fresh=5 stale=0 START=1787860337
SUMMARY tests=40 failures=7 errors=0 skipped=0
```

### W1.1

```
FAILURE dev.swarm.phone.PhoneActivityInsetTest > the top padding is the platform's own inset and nothing else
    java.lang.AssertionError: W1.1: the root's top padding is not the status bar the platform reported (0 px).
    A floor under the measured inset pushes the header into dead space on every handset whose bar is
    thinner than the design's 54dp preview value expected:<0> but was:<54>
```

The test launches the real `PhoneActivity` under Robolectric and dispatches a `WindowInsets` with
a status bar of 0, 30 and 120 px to the content root; the first dispatch fails at 54 (the
`maxOf(measured, swarm_screen_top)` floor). Deleted from `PhoneActivityInsetTest.kt`, as the
contract lists, because each one pinned the floor: `the design's floor wins when the platform
reports no inset yet`, `the real inset wins once the platform reports one taller than the design's
floor`, `the two agree at the design's own preview value`.

### W1.2

```
FAILURE dev.swarm.phone.ui.screens.ConversationScaffoldViewTest > the transcript viewport clips, so nothing draws over the pinned composer
    java.lang.AssertionError: the transcript's ScrollView does not clip its children, so a row scrolled out of
    the viewport is still painted -- over the pinned composer below it
FAILURE dev.swarm.phone.ui.screens.PhoneScaffoldViewTest > the destination viewport clips what scrolls out of it
    java.lang.AssertionError: the destination's ScrollView does not clip its children, so a row scrolled past
    the top of the viewport is still painted -- over whatever chrome sits there
```

`the badge still escapes because the bar holds it` (root `clipChildren == false`) passed in the same
run: it is the guard the contract names, not the change.

### W1.3 / W1.4 -- written, run, then discarded

The four RED tests the contract names for the two bars were written and failed for the right
reason in the same run (`ComposerTest > the composer bar is opaque` and `InboxChromeTest > the tab
bar is opaque, so no row reads through it`: `expected:<255> but was:<224>`; the two recipe tests:
`0xE00E0B08` where `--p-bg` was expected). They were then reverted with `git checkout --` under the
ruling below; no Kotlin under `ui/kit/` changed in this wave.

## W1.3 / W1.4 re-homed in W4

Found before implementation: `android/gate/o3_material_test.go:148`
(`TestPBDS5_EveryColourResourceIsSpentBySomethingThatDraws`) errors for every `<color>` in
`colors.xml` that no Kotlin source and no resource XML spends:

> ADR-009 D4: <color name=%q> is declared, joined to a design token, and drawn by nothing. ...
> Either a component must spend it, or it does not belong in the palette.

`swarm_tabbar_background`'s only consumers are `Composer.kt:292` and `TabBar.kt:115`. W1.3 and W1.4
remove both and require `colors.xml:61` and `design-tokens.tsv:65` to stay, so the wave as
contracted leaves that Go gate red; the contract's gate list (`tabbar_test.go`,
`obsidian_contrast_test.go`, `TOK|S23`) omits it.

Orchestrator ruling (2026-08-27): option (c) -- W1.3/W1.4 move to W4, not held. With W1.2's
clipping nothing scrolls under the composer or the tab bar (both are siblings of the `ScrollView`
under a vertical `LinearLayout`, laid out below its bottom edge), so the visible bleed is closed by
W1.1 + W1.2 alone; the bars' opacity is a token VALUE, and W4 mints the Slate maquette where
`--p-tabbg` becomes opaque with both bars still spending `swarm_tabbar_background`. No o3
exemption, no dead palette row. The sibling geometry is asserted as a guard in both scaffold tests
(`... laid out below the viewport's bottom edge, never over it`).

## GREEN -- after W1.1 and W1.2

Full suite from the lane script, no filter, after `9e5efd9` (W1.1) and `249901f` (W1.2):

```
./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache
GRADLE_EXIT=0
AAR unchanged (mtime 1787859790)
RESULT_FILES fresh=202 stale=0 START=1787860790
SUMMARY tests=1592 failures=0 errors=0 skipped=0
```

New tests, all passing: `PhoneActivityInsetTest > the top padding is the platform's own inset and
nothing else`; `PhoneScaffoldViewTest > the destination viewport clips what scrolls out of it`,
`> the badge still escapes because the bar holds it`, `> the tab bar is laid out below the
viewport's bottom edge, never over it`; `ConversationScaffoldViewTest > the transcript viewport
clips, so nothing draws over the pinned composer`, `> the composer is laid out below the viewport's
bottom edge, never over it`.

Untouched negative controls of the wave, green and unchanged in the same run:
`PhoneScaffoldViewTest > the tab badge appears only when a session needs the user` (the badge
overhang survives the clip), `> the grain travels with the content instead of hanging off the
window`, `ConversationScaffoldViewTest > the window's bottom inset is the keyboard when one is up`
(`bottomInsetPx`, the IME max-not-sum, untouched), `DesignScaleResolutionTest > the frame
constants resolve to the design's own frame` (`swarm_screen_top` still resolves from the design).

## Negative controls -- one per behavioural change

With `9e5efd9` and `249901f` committed and the tree clean, the three fix sites were perturbed back
in the working tree (`PhoneActivity.kt`: `bars.top` -> `maxOf(bars.top, 54)`;
`PhoneScaffoldView.kt`: both viewports back to `clipChildren = false; clipToPadding = false`),
the three classes were run, and the two files were restored with `git checkout --`:

```
./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache \
  --tests dev.swarm.phone.PhoneActivityInsetTest \
  --tests dev.swarm.phone.ui.screens.PhoneScaffoldViewTest \
  --tests dev.swarm.phone.ui.screens.ConversationScaffoldViewTest
GRADLE_EXIT=1
AAR unchanged (mtime 1787859790)
RESULT_FILES fresh=3 stale=0 START=1787869260
SUMMARY tests=21 failures=3 errors=0 skipped=0
FAILURE PhoneActivityInsetTest > the top padding is the platform's own inset and nothing else
    ... expected:<0> but was:<54>                                            (W1.1, the floor)
FAILURE ConversationScaffoldViewTest > the transcript viewport clips, so nothing draws over the pinned composer
    ... the transcript's ScrollView does not clip its children ...          (W1.2, conversation)
FAILURE PhoneScaffoldViewTest > the destination viewport clips what scrolls out of it
    ... the destination's ScrollView does not clip its children ...         (W1.2, inbox)
```

Exactly the three tests that name the change failed; the guards (`the badge still escapes
because the bar holds it`, both `laid out below the viewport's bottom edge` tests, badge, grain,
IME) stayed green under the perturbation, as they should: the root never clipped and the bars
were always siblings. The first attempt at this run gave up after 45 minutes because another
fleet held the Gradle lane; it was rerun once on the free lane, which is the run above.

## Go gates

Run with the supervised session's `SWARM_*` variables unset (they make unrelated `cmd/swarm`,
`internal/hookclient` and `internal/daemon` tests fail); `golangci-lint` 2.12.2 matches the pin in
`.github/workflows/ci.yml`:

```
P="env -u SWARM_HOOK_CAPTURE -u SWARM_SESSION_ID -u SWARM_HOOK_SEQ_FILE -u SWARM_HOOK_TOKEN -u SWARM_DAEMON_SOCK -u SWARM_SHIM_HOOK_SOCK"
$P go build ./...                                              # exit 0
$P go vet ./...                                                # exit 0
$P go test -race -count=1 -timeout 40m ./android/gate/...      # ok  325.353s, exit 0
$P golangci-lint run ./...                                     # 0 issues, exit 0
```

`tabbar_test.go` (PB-DS-6) still matches `setPadding(... bars.bottom ...)` in `PhoneActivity.kt`;
`s22b_spacing_test.go` still reads `swarm_screen_top` from `dimens.xml`; no gate under
`android/gate/` pattern-matches `clipChildren` (checked before landing).

## Done-when, checked

- W1.1: `PhoneActivity.kt` names `swarm_screen_top` nowhere (`grep -c` = 0); `dimens.xml:52`
  unchanged; the handset screenshot is the owner's to take.
- W1.2: both `ScrollView`s clip (`PhoneScaffoldView.kt:194-195`, `:374-375`), both root
  `LinearLayout`s do not (`:209-210`, `:390-391`); badge and grain tests unchanged.
