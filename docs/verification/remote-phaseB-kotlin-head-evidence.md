# Kotlin lane certificate at `4edcf5d`

This file exists to answer one question: **do the Android module's tests pass at the
commit this repository is currently on?** Before it, the most recent answer was
`remote-phaseB-s23-head-evidence.md`, which certifies `5493de1` — and **22 commits have
touched `android/app/src/main/kotlin/` since**.

That is the second time this hole has opened. The file it supersedes was itself written to
close it, after nine commits had landed past the certificate before it, one of which moved
rendering by 2.6 px. It was repaired by hand, once, and nothing was put in place to stop it
recurring. **Nothing gates this file either**, so it will go stale the same way — see
`agents-tracker-wxa`, which is the gate this document is a stopgap for.

## The run

Announced on the shared lane before starting, and the lane was confirmed carrying no active
build (`pgrep -f gradle-wrapper.jar`). Started 00:45:16, 2026-08-02.

```
cd android && ./gradlew --no-daemon test --rerun-tasks --no-build-cache
BUILD SUCCESSFUL in 4m 55s
61 actionable tasks: 61 executed
```

| lane | classes | tests | failures | errors | results written |
|---|---|---|---|---|---|
| `testDebugUnitTest` | 77 | 605 | 0 | 0 | 00:49:13 |
| `testReleaseUnitTest` | 77 | 605 | 0 | 0 | 00:50:11 |

`go test ./android/gate/ -count=1` — ok.

**Why each of those columns is here.** `61 actionable tasks: 61 executed` is the line that
distinguishes a real run from the up-to-date false green: a build reporting
`54 up-to-date` has not run the tests it claims. Non-zero class and test counts rule out the
zero-result worker death described in `agents-tracker-6qi`, where a second build in this
project directory clears the first's live results and the loser dies having measured
nothing. The written-at timestamps postdate the last source edit, so the results belong to
this run and not to an earlier one left on disk.

**No results directory was deleted to force this run.** `--rerun-tasks --no-build-cache`
gives the same guarantee without destroying shared state; `rm -rf app/build/test-results` is
what *causes* the collision, because it removes the in-progress results file a concurrent
build is writing.

## What this certifies, and what it does not

**Certifies:** every checked-in Kotlin test compiles and passes in both build variants at
`4edcf5d`, and the Go gates that scan the Kotlin and resource sources as text pass with it.

**Does not certify** — and none of these are hypothetical:

- **That `FacadeBridge` maps anything correctly.** `swarmmobile.App` is a gomobile class over
  `.so` files built for Android ABIs and cannot be constructed on the unit-test JVM, so every
  mapping in that file is checked by *compilation alone*. Two fields have already been lost
  this way — the journal's `SessionID` and the roster's `Agent` — and both were found by a
  person reading the file. `agents-tracker-9tn`.
- **That any screen behaves on a device.** `PhoneRuntime.phone()` answers `Unavailable` on
  every JVM run, so the `Ready` branch — which is every screen with data on it — is
  unreachable under Robolectric. `agents-tracker-9ds` was an app crash on a normal state that
  605 passing tests could not see; it was found by reading `mobile/app.go`.
- **That the app is free of the defects already filed against it.** The Machines tab renders
  no data (`agents-tracker-xtj`), the peek panel reports a grid size the machine never sent
  (`agents-tracker-2ub`), the disabled CTA draws at full strength (`agents-tracker-325`), and
  48 dp touch targets and the focus ring are specified and enforced nowhere
  (`agents-tracker-jrr`, `agents-tracker-a3k`). All are open, and this certificate is
  perfectly compatible with every one of them.

A passing lane means the assertions that exist hold. It is not a statement about the
assertions that do not.
