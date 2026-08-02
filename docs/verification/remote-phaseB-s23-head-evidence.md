# S22 + S23 verification at HEAD `5493de1`

Written to close audit round 3's finding S-6: every prior Robolectric certificate named
`446f1cb`, and nine commits had landed since -- including `05c813c`, which changed
**production Kotlin** (`TabBar.kt`'s rule stroke from `Kit.dp` to `Kit.dpPx`, a real
rendering change of 2.625px -> 3px at 420dpi) and whose own message said the Kotlin lane was
not run. The tree was fine; the evidence set did not establish it. This file does.

## The run

    git rev-parse --short HEAD          5493de1
    ./gradlew --no-daemon test --rerun-tasks

`--rerun-tasks` is not optional and is the point of this file. A plain `./gradlew test`
against this tree returned **BUILD SUCCESSFUL in 38 seconds with `:app:testDebugUnitTest`
absent from the task list** -- the false green that motionfix documented after a build
collision, where a FROM-CACHE compile with empty output yields `NO-SOURCE` (or here,
`UP-TO-DATE`) and Gradle exits 0 having executed nothing.

Both variants are confirmed to have EXECUTED, by the task lines and by result-XML content:

    > Task :app:testDebugUnitTest
    > Task :app:testReleaseUnitTest
    BUILD SUCCESSFUL in 5m 44s

    debug:   45 classes, 306 tests, 0 failures
    release: 45 classes, 306 tests, 0 failures

Go lanes at the same commit: `go build ./...`, `go vet ./...`,
`go test ./internal/design/... ./android/gate/... ./internal/status/... -count=1` all green;
`python3 scripts/check-phaseb-manifest.py --strict-section11` OK at 162 requirements.

## A correction this file exists to carry, because a commit message cannot be amended once pushed

**Commit `55d4e2a`'s message claims "Kotlin lane green, and testDebugUnitTest verified to have
actually executed rather than trusting the exit code". That claim is FALSE of the commit it is
attached to.** Audit round 3 established it: `55d4e2a` shipped
`pushBannerHiddenTranslation(...) = -130f`, and reproducing that exact tree yields 37 tests
with 3 failures.

The mechanism was a race, not a fabrication -- the lane was genuinely green for the tree that
was tested, and `git add -A` then swept a concurrently-placed debugging mutation into the
commit, so the claim was true of what ran and false of what shipped. **That distinction
explains it and does not excuse it**: a verification claim is about the artefact, and the
window between testing and committing is exactly where a claim like that has to hold. The
reviewer was right that this is the more serious half of that incident, worse than the
`git add -A` itself, because a slip is visible and a false certificate is not.

`0ca1935` reverted the mutation and filed the process defect. It did not correct this claim.
This file is that correction.

## What this evidence does NOT establish

Unchanged from the S22 evidence file, and still true: every assertion here is over resolved
resource values, never rendered pixels. There is no screenshot corpus. No emulator or device
run exists -- `PB-KEY-8` refuses a software-backed keystore and fails closed before any
screen renders. **A screen can satisfy every test counted above and be unusable.**

Nor does it establish that the kit is used: `PB-DS-6` remains NOT MET, the kit has zero
production call sites, and the three surface files still render the old unstyled UI.
