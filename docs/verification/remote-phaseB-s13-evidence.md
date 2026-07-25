# S13 evidence — the Android skeleton and the toolchain gate (PB-TOOL-1..7, PB-RUN-1..5, PB-TOK-4)

**Commit**: `3fbaa50` — one commit, 50 files, +5637/-10. **Requirements**: 13.
**Decisions**: ADR-007 B16 (background disconnects, minSdk 33), ADR-008 (Go floor 1.25).

> **RECONSTRUCTED**, 2026-07-25, from the commit, the diff and the tests — not from memory, and
> not from the commit message alone. Everything below that is stated as a result was re-run at
> HEAD; everything that could not be re-verified is marked as such.

## Why this slice exists

It is the first Android code in the repository. Before it, §2 of the requirements recorded a
toolchain that existed only in one laptop's shell history: Gradle "wrapper **generation** verified
in a scratch build", no wrapper checked in, no module, no lane. Every downstream Android slice
(S14-S17) needs a build that a fresh shell can reproduce, and PB-TOOL-5's "no Go regression" needs
the Android work to be inside the existing `go test ./...` gate rather than beside it.

## Shape of what shipped, and why it is shaped that way

- `android/` is the Gradle root, `android/app/` the application module. Not the repository root:
  `gradlew`, `settings`, the wrapper directory and Gradle's build outputs would otherwise land in
  the top level of a Go repository (`helpers_test.go:39-45` records the reasoning).
- **`android/gate/` is a Go test package holding no production code.** Every file/content
  assertion therefore runs under the repository's existing `go test ./...` job. An Android gate
  that only ran in a Gradle lane would be invisible to the Go gate the project's GG-4 names.
- **Two lanes** (`android/gate/doc.go`):
  - *untagged* — files and config, no SDK, no JDK, no Gradle. Runs on every runner.
  - *`androidgate` build tag* — sources the pin in a **scrubbed** shell and actually builds the
    AAR, builds the debug APK, and runs `./gradlew lint test`.
  - `artifact_androidgate_test.go:24-25`: "Nothing here calls `t.Skip`." Confirmed at HEAD —
    `grep -c 't.Skip' android/gate/artifact_androidgate_test.go` is 0.
- **The runtime policy is a table, not prose**: `android/connectivity-policy.tsv` (7 columns,
  5 rows) and `android/fcm-priority.tsv` encode ADR-007 B16 with closed vocabularies, and
  `android/supported-versions.tsv` records the version matrix. The argument, in the slice's own
  words: *a table nothing reads is a document; an implementation with no table is a decision made
  in code review.* Both halves are enforced — the Go gate checks the table is total and internally
  consistent, `ConnectivityPolicyTest.kt` asserts the shipping Kotlin **is** that table, read from
  the unit-test **classpath** rather than by relative path (a relative path would make the test
  depend on Gradle's working directory, which is not a property of the app).

## Per requirement: what proves it

| Requirement | What proves it | Lane |
|---|---|---|
| PB-TOOL-1 (pin; a fresh shell can build) | `android/toolchain.env` + `TestPBTOOL1_PinFileIsCheckedIn`, `_FreshShellSourcingExportsEveryPin`, `_AndroidAPIFloorIsTheNDKFloor`, `_AndroidAPIIsNotConflatedWithMinSdk`, `_GomobileVersionMatchesGoMod`; `gotoolchain_test.go`'s five Go-pin tests + `_ADR008Exists`. Artifact half: `_FreshShellCanResolveTheWholeToolchain`, `_GomobileFindsGobindFromThePinnedShell`, `_PinnedSDKComponentsExistOnDisk` | both |
| PB-TOOL-2 (one command, explicit ABI set incl. `arm64-v8a`) | `android/build-aar.sh` + `TestPBTOOL2_OneCommandBuildsTheAAR`, `_BuildCommandDeclaresAnExplicitABISet`, `_BuildCommandPassesTrimpathAndTheAndroidAPIFloor`; artifact half `_OneCommandProducesAnAARWithEveryDeclaredABI` -> `assertAARContents` | both |
| PB-TOOL-3 (debug APK; release signing from operator config/env, never the repo) | `signing_test.go` (5 tests: no keystore tracked, no password tracked, `.gitignore` covers the material, release signing reads config/env, release refuses without it); artifact half `_DebugAPKBuildsAndIsSigned` + `_ReleaseBuildRefusesWithNoOperatorKeystore` | both |
| PB-TOOL-4 (wrapper checked in, distribution pinned) | `wrapper_test.go` (checked in, distribution pinned **and SHA-256 verified**, wrapper-jar checksum matches the pin); artifact half `_GradlewRunsWithoutSystemGradle` | both |
| PB-TOOL-5 (no Go regression) | `TestPBTOOL5_ExistingGoLanesAreUnchanged`, `_EveryCIGoPinSatisfiesTheModuleFloor`, `_EveryLaneThatRunsGoPinsIt`, `_GomobileToolDoesNotEnterTheDaemonBinaries` | untagged |
| PB-TOOL-6 (`./gradlew lint test` green) | `gradlegate_test.go` (lint fails the build, no baseline suppresses findings, the Kotlin unit-test source set is wired, every checked-in Kotlin test is discoverable); artifact half `_GradleGateIsGreen` + `_EveryCheckedInKotlinTestActuallyRan` | both |
| PB-TOOL-7 (CI lane) | `ci_test.go` (6 tests) against a purpose-built YAML reader (`ciyaml_test.go`, itself covered by 5 tests including one that parses the real workflow) | untagged |
| PB-RUN-1 (minSdk/targetSdk + supported matrix) | `android/supported-versions.tsv` + `sdkmatrix_test.go` (matrix recorded and contiguous, endpoints ARE the pinned min/target, the build carries no competing SDK literals); artifact half `_BuiltAPKReportsThePinnedSdkLevels` | both |
| PB-RUN-2 (runtime permissions with denial paths) | `PermissionGateTest.kt` — 8 tests covering granted / denied-never-asked / denied-with-rationale / permanently-denied for `CAMERA`, the API-33 boundary for `POST_NOTIFICATIONS`, plus totality and "every non-granted state names a degraded capability". `RuntimeManifestTest.kt` joins it to the manifest | Gradle |
| PB-RUN-3 (foreground/background connectivity policy) | `connectivity-policy.tsv` + 8 Go tests + `ConnectivityPolicyTest.kt` + `RuntimeManifestTest.foreground_service_declaration_agrees_with_the_connectivity_policy` | both |
| PB-RUN-4 (FCM priority chosen deliberately) | `fcm-priority.tsv` + `TestPBRUN4_PriorityIsRecordedPerMessageClass`, `_DozeWakeRequiresHighPriority`, `FcmPriorityPolicyTest.kt` | both |
| PB-RUN-5 (lifecycle events converge) | `LifecycleConvergenceTest.kt` — 8 tests: force-stop cold start, convergence not depending on a boot broadcast, `BOOT_COMPLETED` reaching a declared receiver, the receiver holding `RECEIVE_BOOT_COMPLETED`, `PACKAGE_REPLACED`, network loss cancelling the outstanding wait, regain re-establishing **exactly once**, and planner totality | Gradle |
| PB-TOK-4 (the app does not follow the system `uiMode`, by any of three routes) | `theme_test.go` — `_NoThemeInheritsADayNightParent`, `_NoNightQualifiedResources`, `_ManifestDoesNotOptIntoSystemDarkening`; behavioural half `ThemeNightModeTest.kt` (4 tests: identical resolution under system light and system dark, the default night mode pinned rather than following, and the applied configuration reporting night under a light system) | both |

**PB-RUN-3's two joins are the non-obvious part.** The gate does not merely check the table is
filled in; it checks the table is *jointly consistent* with the rest of the system:
`_WaitCeilingNeverExceedsTheRelayCeiling` ties the 25 s foreground wait to §6.0's server-side
bound, `_ForegroundServiceTypeIsDeclaredExactlyWhenUsed` ties the `fgs_type` column to the
manifest in **both** directions, `_AnUnsustainedSocketMustNameAWakePath` forbids a row that closes
the socket and names no wake path, and `TestPBRUN4_DozeWakeRequiresHighPriority` joins the two
tables — a `push` wake path in Doze requires `high` priority, and `high` with no push wake path is
a quota paid for nothing.

## Failing-first evidence (GG-5)

**State this plainly: S13 has no preserved RED transcript.** Tests and implementation landed in one
commit, and there is no `docs/verification/remote-phaseB-*-red` directory for any Phase B slice.
What *is* durable and checkable, in descending order of strength:

1. **PB-TOK-4 was widened by the S13 RED author, and the widening is in this commit's own diff.**
   `git show 3fbaa50 -- docs/specifications/remote-phaseB-requirements.md`. The requirement had
   named only the `DayNight` parent; the RED author showed that was **satisfiable while the defect
   ships** — a `values-night/` qualifier reproduces it with a compliant parent, and
   `AppCompatDelegate`'s default is `MODE_NIGHT_FOLLOW_SYSTEM`, which leaves no trace in any
   resource file at all. All three routes are now closed and each has its own test. This is the
   project's standing class (iv) caught before it shipped, and it is machine-checkable evidence
   rather than a recollection.

2. **A test that could not pass as written, with the diagnosis pinned in source.**
   `artifact_androidgate_test.go:350-358` records, with the commands and their measured output,
   that the original assertion grepped aapt2's output for AAPT1's `sdkVersion:'` label. aapt2 emits
   `minSdkVersion:'33'` and **zero** bare `sdkVersion:'`, so the assertion was unsatisfiable by any
   APK, forever — while the `targetSdkVersion` row of the same table passed, because that label is
   identical in both tools, and masked it. The APK was correct; the test was not. The `[^a-zA-Z]`
   anchor now present keeps the fix from failing in the other direction.

3. **`-trimpath` alone did not close the builder-path leak**, and the analysis is in
   `build-aar.sh`'s comment rather than in a commit message: it removed 46 of the 48 absolute paths
   the S8 reviewer found. The last two come from the throwaway module `gomobile bind` synthesises,
   whose `replace <module> => <absolute dir>` the linker records **verbatim** in the build-info
   blob; `-trimpath` does not rewrite them (verified against a two-module probe).
   `-ldflags "-X=runtime.modinfo="` removes them, at the stated cost of the embedded module graph.

4. **Negative controls are built into the tests**, which is what a slice with no RED transcript has
   instead. Each of these fails in the direction that matters:
   - `TestPBTOOL7_AndroidLaneCannotBeSilentlyGreen` and `_AndroidLaneRunsTheTaggedArtifactAssertions` —
     the expensive half cannot become an orphan behind its build tag.
   - `TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore` — the release build must **fail** with
     no operator material, because succeeding means it produced an unsigned or debug-signed
     release artifact, "the quiet form of the defect".
   - `TestPBTOOL6_EveryCheckedInKotlinTestActuallyRan` reads the JUnit XML: "`./gradlew test` is
     green when it runs nothing", and a `*Test.kt` outside the compiled source set leaves the gate
     green with its report simply absent.
   - `findAAR` (`artifact_androidgate_test.go:189-193`) — "an exit-status check is vacuous here:
     gobind exits 0 while silently dropping bind-illegal exports".
   - `PolicyTables.read` fails loudly on an empty table, because "a table that silently reads as
     empty makes every assertion over it pass".
   - `TestPBTOOL4_GradlewRunsWithoutSystemGradle` runs with `/usr/local/bin` **off** PATH; this
     host has Gradle 9.6.1 there, so an ambient-PATH run would pass with no wrapper checked in.

## Gates, re-run at HEAD 2026-07-25

**Untagged lane** — `go test ./android/gate/ -count=1 -v`: **every S13-owned test PASSES**
(PB-TOOL-1..7, PB-RUN-1, PB-RUN-3, PB-RUN-4, PB-TOK-4; 43 tests including the CI-YAML parser's own
five).

> The package as a whole is currently **RED**, and not because of S13: three failures come from
> `android/gate/s15_backup_test.go`, an **uncommitted** S15 RED file present in the working tree
> (`TestPBSEC10_TheManifestDeclaresDataExtractionRules`,
> `TestPBSEC10_TheRulesExcludeBothCloudBackupAndDeviceTransfer`,
> `TestPBSTATE6_StateAtRestIsSealedPerTierAndExcludedFromBackup`). Those are S15's declared
> failing-first state, not an S13 regression. Anyone re-running this gate before S15 lands should
> expect exactly those three.

**Tagged lane** — `go test -tags androidgate ./android/gate/ -count=1`:

| Test | Result at HEAD |
|---|---|
| `TestPBTOOL1_FreshShellCanResolveTheWholeToolchain` | PASS (0.28 s) |
| `TestPBTOOL1_GomobileFindsGobindFromThePinnedShell` | PASS (0.75 s) |
| `TestPBTOOL1_PinnedSDKComponentsExistOnDisk` | PASS (0.04 s) |
| `TestPBTOOL4_GradlewRunsWithoutSystemGradle` | PASS (1.22 s) |
| `TestPBTOOL2_OneCommandProducesAnAARWithEveryDeclaredABI` | PASS (18.31 s) — rebuilt `android/app/libs/swarm.aar` from `build-aar.sh` and inspected it |

`TestPBTOOL2_...` is the load-bearing re-verification: it rebuilds the AAR through the checked-in
command and asserts the artifact carries **exactly** `arm64-v8a` and `x86_64` — no missing ABI and
no extra one — and that **each** shipped `libgojni.so` scans to **zero** absolute builder paths.
That is the commit message's central artifact claim, re-established at HEAD rather than taken on
trust. It also re-establishes that `gomobile bind ./mobile` still succeeds after S8/S11/S14 changed
the facade, which nothing else in the default gate checks (see the residual on PB-BIND-1 below).

| Test | Result at HEAD |
|---|---|
| `TestPBTOOL6_GradleGateIsGreen` (`./gradlew --no-daemon lint test`) | PASS (115.4 s) |
| `TestPBTOOL6_EveryCheckedInKotlinTestActuallyRan` | PASS — every checked-in `*Test.kt` produced a JUnit report with a non-zero test count |
| `TestPBTOOL3_DebugAPKBuildsAndIsSigned` | PASS (64.3 s) — APK carries `AndroidManifest.xml`, `classes.dex` and `lib/*/libgojni.so`, and `apksigner verify` accepts it |
| `TestPBTOOL3_ReleaseBuildRefusesWithNoOperatorKeystore` | PASS (51.7 s) — the release build **fails** with the operator variables unset |
| `TestPBRUN1_BuiltAPKReportsThePinnedSdkLevels` | PASS — `aapt2 dump badging` reports `minSdkVersion=33`, `targetSdkVersion=35`, matching the pin |

Whole tagged package: `ok github.com/Nathandela/swarm/android/gate 232.8s`, **zero failures**. So
PB-TOOL-2, PB-TOOL-3, PB-TOOL-4, PB-TOOL-6 and PB-RUN-1's artifact halves are re-established
against real artifacts at HEAD, not carried over from the commit message. The Kotlin half
(PB-RUN-2, PB-RUN-5, PB-TOK-4 behavioural) ran inside `TestPBTOOL6_GradleGateIsGreen`, and
`_EveryCheckedInKotlinTestActuallyRan` is what makes that a real result rather than a green
no-op.

## Decisions recorded here because a later reader will otherwise re-litigate them

- **Gradle 9.5.1, not the host's 9.6.1.** 9.6.0 removed an internal API AGP 8.x needs, and AGP 9
  rejects the Kotlin Android plugin PB-TOOL-6's unit tests require. 9.5.1 satisfies every test on
  current stable releases, and as a side effect makes "runs without system gradle" a real
  assertion rather than a coincidence — the host's Gradle is a *different* version, so a wrapper
  that silently was not used would be visible. The spec table was corrected in the same commit.
- **`SWARM_ANDROID_API=21` is gomobile's `-androidapi`, the NDK's floor — not the app's `minSdk`
  (33).** Two tests exist solely to keep them from being conflated
  (`_AndroidAPIFloorIsTheNDKFloor`, `_AndroidAPIIsNotConflatedWithMinSdk`), and
  `supported-versions.tsv`'s header says it again. gomobile defaults to API 16 and NDK 27 refuses
  it, so the flag is part of the build contract.
- **minSdk 33** is ADR-007 B16's: Curve25519 entered KeyMint only in Android 13, and below it the
  `NoiseStatic`/`Recipient` roles cannot be Keystore-native — which would silently degrade
  PB-KEY-8 on old devices. 33 makes PB-KEY-8 clean by construction instead of forcing a fallback
  matrix nobody exercises.
- **`GOTOOLCHAIN: local` at workflow level** (`.github/workflows/ci.yml:13`), with the seven Go
  pins raised to 1.25 per ADR-008. `GOTOOLCHAIN` defaults to `auto`, so a pin that silently
  downloads a different toolchain "reads as verified while naming a toolchain nothing was built
  with".
- **No foreground service in v1** (ADR-007 B16). The table's reasoning is recorded in
  `connectivity-policy.tsv`'s header: holding B7's 25 s parked wait through Doze/App Standby/battery
  saver would force a `foregroundServiceType` of `dataSync` (capped from API 34 at ~6 h/day, after
  which the system force-stops the service) or `specialUse` (a Play-review dependency on a personal
  tool). Dropping it makes the high-priority FCM wake load-bearing, which ADR-007 D6 already assumed.
- **High FCM priority with `coalesce` on quota exhaustion.** Google publishes no quota number, so
  the exhaustion behaviour is half the decision and is recorded rather than discovered in
  production.

## Accepted residuals

- **The Gradle/Kotlin half of the gate cannot run on a runner with no SDK**, by construction. The
  untagged lane is what always runs; PB-RUN-2, PB-RUN-5 and PB-TOK-4's behavioural half live
  entirely in Kotlin and are therefore exercised **only** in the Android lane. `ci_test.go` is what
  keeps that lane from silently disappearing, and it is the single point of failure for those
  three requirements' coverage.
- **No instrumented or on-device test.** Everything here is unit-level (Robolectric) or artifact
  inspection. The progress doc records that an AVD named `swarmtest` exists and that emulator tests
  are genuinely runnable on this host, so this is a scope boundary rather than a tooling block;
  PB-E2E-5 remains the deferred hardware gate.
- **`TestPBTOOL2_...` insists on exactly one `.aar` under `android/`.** A stale artifact from an
  earlier run would otherwise let the inspection assert against bytes that predate the change under
  test. Anyone who builds an AAR by hand into `android/` will see this test fail for a reason that
  is not a defect.
- **Blanking `runtime.modinfo` drops the embedded module graph**, so an SBOM tool can no longer
  read the dependency list out of the shipped `.so`. Accepted on the stated grounds that `go.mod`
  and `go.sum` are tracked and authoritative.
- **PB-RUN-5's convergence tests are planner-level, not process-level.** They assert a total
  lifecycle-event planner and a declared, permission-holding `BootReceiver`; they do not kill a
  real process. Composed with PB-STATE-2, whose durable half S7 evidences through a real SIGKILL.
- **Pre-existing `gofmt` drift** (10 files, none in `android/`) is recorded in the progress doc and
  fails no gate — `gofmt` is not in the golangci config.

## One cross-slice consequence worth flagging

`mobile/bind_test.go`'s `TestPBBIND1_GomobileBindProducesAnAAR` — PB-BIND-1's literal criterion —
**skips itself** when `ANDROID_HOME`/`ANDROID_SDK_ROOT` are unset, which is the default for
`go test ./...` because only the `androidgate` lane sources the pin. So the "`gomobile bind`
succeeds on the facade" property is carried in practice by **S13's** `TestPBTOOL2_...`, not by S8's
own test. That is fine — it is covered — but an auditor reading S8 alone would over-read its green
gate. Recorded here and in the S8 evidence file.
