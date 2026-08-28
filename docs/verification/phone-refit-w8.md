# Phone refit W8 — Leadings render, the file-change row breathes: RED evidence and gate record

Bead `agents-tracker-a84l` · playbook `docs/specifications/phone-refit-playbook.md` section 8b ·
worktree `refit-w8`, branch `refit/w8`, branched from `main` at `1d693bde` · 2026-08-28.

Every RED below was captured before the GREEN edit that follows it, by running the affected suite
or gate against the tree as it stood. No test was edited to pass. The wave's negative controls
(`NoticeTest`, `KitDensityTest` beyond its one row-31 claim, `TranscriptScreenGoldenTest`,
`DesignScaleResolutionTest`, `i1_screengolden_test.go`) are byte-identical to `main`
(`git diff 1d693bde --stat` over those paths is empty) and green in the runs quoted.

Gradle lane discipline for every Kotlin run: the wait script matched only the running wrapper JVM
(`pgrep -f '^/[^ ]*java .*gradle-wrapper[.]jar'`, from a script file); a start stamp was recorded
before each run; afterwards every `app/build/test-results/testDebugUnitTest/*.xml` was newer than
that stamp (zero older files, counted with `find ! -newer`), and `app/libs/swarm.aar` kept its
mtime `2026-08-28 11:54:05` throughout. No exported facade field changed, so no AAR rebuild.

## W8.1 A style's leading reaches the view

### Kotlin: `LeadingTest.kt`, RED

`LeadingTest.kt` written first (two tests: `a styled view reports the leading its style declares`,
a table over the five `android:lineHeight` styles asserting `view.lineHeight == round(sp x
density)` with bare `setTextAppearance` as the in-test control; `a style without a leading leaves
the platform line height`, over `Title.Row`). Run A, start `2026-08-28T12:04:25+0200`,
`./gradlew --no-daemon testDebugUnitTest --rerun-tasks --no-build-cache --tests dev.swarm.phone.ui.kit.LeadingTest`
against the tree without the helper:

```
> Task :app:compileDebugUnitTestKotlin FAILED
e: file:///Users/Nathan/Code/swarm/.claude/worktrees/refit-w8/android/app/src/test/kotlin/dev/swarm/phone/ui/kit/LeadingTest.kt:55:81 Unresolved reference 'appearance'.
BUILD FAILED in 1m 55s
```

The single compile error is the missing implementation (`Kit.appearance`), not a test defect.

### Kotlin: GREEN

`Kit.appearance(view, style)` added to `Kit.kt` (commit `57da6b43`): `setTextAppearance(style)`,
then `obtainStyledAttributes(style, intArrayOf(android.R.attr.lineHeight))` and
`TextViewCompat.setLineHeight` when the style states one. Run B, start `2026-08-28T12:09:34+0200`,
targeted `LeadingTest`, `NoticeTest`, `KitDensityTest`, `FileChangeRowTest` (the latter two carry
W8.2's RED, below):

```
dev.swarm.phone.ui.kit.LeadingTest   tests="2" skipped="0" failures="0" errors="0"
  a styled view reports the leading its style declares
  a style without a leading leaves the platform line height
dev.swarm.phone.ui.kit.NoticeTest    tests="7" skipped="0" failures="0" errors="0"
```

The in-test control passed in the same run: through bare `setTextAppearance`, none of the five
styles reports its declared leading (the `dropped` assertion), so the claims are ones the kit as
it stood fails. `Kit.textView`'s bare view is the platform path the helper replaces.

### Go gate: `w8_leading_test.go`, RED

Written before the helper existed, so the RED shows both halves. The `0` literal the helper uses
as the TypedArray index is on `s23LiteralExemptions`; that row's prose now names the use, per the
file's own convention. `go test -race -count=1 -run 'W8|PBDS7' ./android/gate/`:

```
--- FAIL: TestW8_EveryKitStyleIsAppliedThroughKitAppearance (0.08s)
    w8_leading_test.go:61: W8.1: Kit.kt declares no `fun appearance(view: TextView, @StyleRes style: Int)`: the kit has no way of putting a style on a view that carries the style's leading, so type.xml's five android:lineHeight items reach nothing
    w8_leading_test.go:66: W8.1: Kit.kt calls `view.setTextAppearance(style)` 0 times; the platform path belongs in Kit.appearance exactly once
    w8_leading_test.go:70: W8.1: 46 bare setTextAppearance( call(s) in ui/kit outside Kit.appearance. Each applies the style's size, weight, family and tracking and drops its leading; route it through Kit.appearance(this, style):
        	ActivityRow.kt:106: setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
        	ActivityRow.kt:117: setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
        	ActivityRow.kt:129: setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
        	ApprovalSheet.kt:60: setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
        	...
        	TabBar.kt:203: setTextAppearance(R.style.TextAppearance_Swarm_Label_Tab)
        	TextField.kt:61: setTextAppearance(
        	Toast.kt:59: setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
FAIL
FAIL	github.com/Nathandela/swarm/android/gate	5.499s
```

The gate's own negative control, `TestW8_TheLeadingFenceCanActuallyFail`, passed in this same run:
a scratch kit under `t.TempDir()` with the helper as shipped, one component with a bare call and
one whose comment names the call is reported as exactly `Badge.kt:2`; a second bare call in
`Kit.kt` and one on an explicit receiver are then both reported (the exemption covers one call,
not one file).

### Go gate: GREEN

All 46 sites in 29 files routed to `Kit.appearance(this, style)` mechanically (commit `3654c52d`;
the two multi-line sites, `TextField.kt:61` and `MessageBubble.kt:64`, keep their `if (mono)`
argument). `go test -race -count=1 -run W8 -v ./android/gate/`:

```
--- PASS: TestW8_EveryKitStyleIsAppliedThroughKitAppearance (0.08s)
--- PASS: TestW8_TheLeadingFenceCanActuallyFail (0.00s)
ok  	github.com/Nathandela/swarm/android/gate	2.275s
```

`grep -rn 'setTextAppearance(' android/app/src/main/kotlin/dev/swarm/phone/ui/kit/` now finds
the helper's own call and its KDoc, nothing else.

## W8.2 The file-change row follows the activity row

### Tests first, RED

The joins were amended before the code: `s23_kit_test.go` rows for `#31 File change row`
(`padding-y` `swarm_space_12`, `padding-x` `swarm_space_16`) under an `AUTHORIZED REWRITE, phone
refit W8.2 (2026-08-28)` note quoting the old rows, the row's derivation `Why` (which said the row
"keeps the tighter box pending its own ruling"), `docs/design/substrate-components.md` row #31's
spacing cell (the new pair is the first ``padding `space_N` x `space_M` `` the reader meets; the
single ``gap `space_10` `` is unchanged), `FileChangeRowTest.kt`'s two padding assertions, and the
`KitDensityTest` row-31 claim (moved from the x step to the y step, since `space_16` is whole at
420dpi and that file asserts only steps whose rounding can fail there; the same move ADR-021 D2
made for row 26).

Go, same run as the W8 RED above:

```
--- FAIL: TestPBDS7_EveryDerivedSpacingIsTheRowsStep (0.01s)
    s23_kit_test.go:1952: PB-DS-7: FileChangeRow.kt never references R.dimen.swarm_space_16, which is the step `#31 File change row` states for its padding-x. A component whose only specification is prose in a table is the one whose spacing has to be read out of that table rather than out of itself.
```

Kotlin, Run B (start `2026-08-28T12:09:34+0200`, the tree with `FileChangeRow.kt` still at
`space_10` x `space_12`):

```
FileChangeRowTest > it is drawn as the row it sits beside FAILED
    java.lang.AssertionError: row 14's own horizontal step expected:<16> but was:<12>
KitDensityTest > the conversation surface spends the whole pixels the platform would FAILED
    java.lang.AssertionError: expected:<[]> but was:<[row 31 row padding-y: design says 0x00000020, component resolved 0x0000001A]>
23 tests completed, 2 failed
```

(32 px is `space_12` at density 2.625; 26 px is the old `space_10`.)

### GREEN

`FileChangeRow.kt`: `setPaddingRelative` at `space_16` / `space_12` / `space_16` / `space_12`, the
`space_10` gap untouched, KDoc amended under the same dated note (commit `837e2b62`).

Go, `go test -race -count=1 -run 'PBDS7|PBDS1' -v ./android/gate/`: every `TestPBDS1_*` and
`TestPBDS7_EveryDerivedSpacingIsTheRowsStep` PASS, `ok ... 10.868s`; the whole gate package
afterwards `ok github.com/Nathandela/swarm/android/gate 119.038s`. No exemption added
(`git diff 1d693bde -- android/gate/s23_kit_test.go` adds no exemption row).

Kotlin, Run C, start `2026-08-28T12:16:03+0200`, targeted `FileChangeRowTest`, `KitDensityTest`:

```
BUILD SUCCESSFUL in 5m 1s
dev.swarm.phone.ui.kit.FileChangeRowTest  tests="8" skipped="0" failures="0" errors="0"
dev.swarm.phone.ui.kit.KitDensityTest     tests="6" skipped="0" failures="0" errors="0"
```

## Gates on the final tree

- `go build ./...` exit 0; `go vet ./...` exit 0; `golangci-lint run` (2.12.2, the CI pin)
  `0 issues.`
- `go test -race -count=1 -timeout 40m ./android/gate/`: `ok` (119.038s).
- `go test -race -count=1 -timeout 40m ./...` (with the `env -u SWARM_*` prefix): 63 packages `ok`, one failure,
  `internal/e2e` `TestE2E_ReplayProductionPath_AgyOpencode` (`replay_e2e_test.go:403: agy: idle
  observed only 2.878819s after the first active sample (want >= 3s ...)`), a wall-clock assertion
  on a replayed PTY session that ran while refit-w3's own `go test ./...` and a Gradle build shared
  the host (load 8.98 at the time). Rerun once in isolation per fleet protocol 6:
  `go test -race -count=1 -timeout 40m ./internal/e2e/` -> `ok github.com/Nathandela/swarm/internal/e2e 41.375s`.
  No package touched by this wave failed.
- Kotlin full suite, Run D, start `2026-08-28T12:29:07+0200`, `./gradlew --no-daemon testDebugUnitTest
  --rerun-tasks --no-build-cache`: `BUILD SUCCESSFUL in 4m 48s`;
  from the 204 result files, tests=1619 failures=0 errors=0 skipped=0. `LeadingTest` 2/0,
  `NoticeTest` 7/0, `KitDensityTest` 6/0, `FileChangeRowTest` 8/0, `TranscriptScreenGoldenTest`
  7/0. Zero result files older than the run's stamp; `swarm.aar` mtime unchanged.

## Changed assertions (W8.2, tests-first; accepted by team-lead before the edit)

Both were amended BEFORE `FileChangeRow.kt` moved, and their failures against the old padding are
the Kotlin RED quoted above. Nothing else in either file changed.

`FileChangeRowTest.kt`, `it is drawn as the row it sits beside`:

```kotlin
// before
assertEquals("row 14's own horizontal step", Kit.dimenPx(context, R.dimen.swarm_space_12), row.paddingStart)
assertEquals("and its vertical one",          Kit.dimenPx(context, R.dimen.swarm_space_10), row.paddingTop)
// after (under the dated W8.2 note)
assertEquals("row 14's own horizontal step", Kit.dimenPx(context, R.dimen.swarm_space_16), row.paddingStart)
assertEquals("and its vertical one",          Kit.dimenPx(context, R.dimen.swarm_space_12), row.paddingTop)
```

`KitDensityTest.kt`, `the conversation surface spends the whole pixels the platform would`:

```kotlin
// before
Claim("row 31 row padding-x", dimenPx("swarm_space_12"), change.paddingStart),
// after (under the dated W8.2 note): the x step is now space_16, which is whole at 420dpi and
// cannot tell truncation from rounding there, so the claim moves to the y step, which can.
Claim("row 31 row padding-y", dimenPx("swarm_space_12"), change.paddingTop),
```

The Go-side joins (`s23_kit_test.go` row-31 rows, quoted before/after in the `AUTHORIZED REWRITE`
note in that file; `substrate-components.md` row #31) moved in the same commit.

## Commits on `refit/w8`

- `57da6b43` Kit.appearance: a style's leading reaches the view (W8.1)
- `3654c52d` Route every kit style through Kit.appearance, and fence it (W8.1)
- `837e2b62` File change row: row 14's padding again, space_12 x space_16 (W8.2)
- the commit adding this file: Record W8 evidence

## Outside the contract's file list, reported to team-lead before acting

1. `FileChangeRowTest.kt:106-115` pinned the old padding; amended tests-first (it is W8.2's RED,
   quoted above), not a test edited to pass.
2. `s23_kit_test.go` row-31 derivation `Why` said the row keeps the tighter box pending its own
   ruling; the parenthetical now records that W8.2 is that ruling. The string is printed only in
   failure messages.
3. The `0` exemption row in `s23LiteralExemptions` names `Kit.appearance`'s index use.
