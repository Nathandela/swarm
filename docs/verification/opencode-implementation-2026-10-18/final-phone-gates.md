# Final phone release gates — 2026-10-18

**PASS:** full native Go phonecore/mobile and Android contract race suites, all Android debug/release unit tests, and both lint variants. Both Android variants ran **1,764 tests, zero failures, zero errors, zero skips** (3,528 executions total). Gradle executed all 62 tasks with `--rerun-tasks`.

## Source and artifact custody

The clean AAR/Go build source was `cd88cb37f1a3977d94c4959b83bc2e676852c63a` (tree `54298d7fbb2f24a854f113eed8ba4154250b6349`), exactly equal to integration `f25fe625`. Final Android test source was `1d471d52dd3c7a7b528326bebe9f544a6abf5eb7` (tree `69efa4e1bebf4ad050f98bd3d9c7c0cd2b83b811`); only the relay trust **test fixture** changed. Comparison against root `785e79aa7bd1917c7a0cb0a19b34748f2c97b21f` found only later host skeleton/evidence changes and reviewed test fixtures/contracts, so the AAR production code remains equal to the integrated root. The JSON lists every changed path.

Fresh real debug AAR and independently regenerated release AAR have identical bytes:

- SHA-256: `2478a67e537074f97bb57bbdc777c894b15da710fa0ba11df64c3ad33ed961ad`
- Size: 13,286,295 bytes
- Native libraries: `jni/arm64-v8a/libgojni.so`, `jni/x86_64/libgojni.so`
- `javap` confirms `replyNativeRequest(String, String, String, String, String)` in both artifacts.
- Debug AAR did not exist before the build. Its post-build, pre-test, and post-test mtime remained `1792351052263887862` ns.
- Release mtime recorded before the fixture reruns: `1792351100551955728` ns; after the final Android unit-test regeneration: `1792351431977046596` ns. The baseline was captured before the fixture-rerun sequence.

The later lint task also regenerated the release AAR with the same SHA-256; its final mtime is `1792351794614475004` ns. Debug mtime remained unchanged through every gate.

Only private worktree outputs were built; no installed app, binary, or AAR was replaced.

## Executed gates

Toolchain: native Go 1.26.9, JDK 21.0.12.1+1, Gradle 9.5.1, NDK 27.2.12479018. Inherited `SWARM_*` variables were removed before each command.

`go test -race ./internal/phonecore ./mobile -count=1` passed: phonecore **69.172 s**, mobile **57.775 s**. The full mobile suite included real native gobind legality/negative-control checks and real gomobile AAR generation with SDK configured; no `-short` was used.

`./gradlew --offline --no-daemon --continue --rerun-tasks :app:testDebugUnitTest :app:testReleaseUnitTest` passed in **121.525 s**.

| Variant | Tests | Failures/errors/skips | XML suites | Fresh XML timestamp range (UTC) |
| --- | ---: | --- | ---: | --- |
| Debug | 1764 | 0 / 0 / 0 | 219 | 2026-10-18T19:24:27.764Z – 2026-10-18T19:25:00.405Z |
| Release | 1764 | 0 / 0 / 0 | 219 | 2026-10-18T19:25:03.069Z – 2026-10-18T19:25:35.054Z |

The companion JSON retains XML filesystem mtime ranges and links the raw per-suite metadata.

## Android contract and lint checks

`go test -json -race ./android/gate -count=1` passed **570 test results, zero failures, zero skips** (487 top-level tests), package time **156.513 s**. Source `a1fa1a291dbba7688d152f2c6310103903c5ef07` adds only the native request card's actual kit-composition claims (`a1fa1a29`). Its preserved RED was 569 pass / 1 fail / 0 skip: the new screen had been omitted from the existing exhaustive composition table. This strengthens the gate without changing production code. Additional-gate source status contained only the untracked evidence files being curated.

Both `:app:lintDebug` and `:app:lintRelease` passed in **186.379 s**, **zero errors and 34 warnings each**. No warnings were suppressed and no production code was changed. The first offline invocation stopped before analysis because pinned cache artifacts were missing. The authorized follow-up resolved the exact pinned artifacts through the declared official repositories and existing verification metadata, then completed real analysis. Dependency verification SHA-256 stayed `dbae92ea6ab2e9b9dbe76e2967a0fff73e273e2ec080bbb259fc761f7b3b1f77` before and after; no dependency versions or verification checksums changed. The JSON records warning categories and full XML report paths.

## Preserved failing-first fixture evidence

The initial full run had six failures in each variant, all before production verification: the host JDK's SunJSSE trust manager lacks Android's required three-argument method. Test-only commit `417dd905` supplied that public signature while delegating actual CA/path validation. Five failures cleared; real untrusted-chain rejection then surfaced SunJSSE's private cert-path cause, conservatively classified as unavailable by production.

Test-only `1d471d52` wraps only the delegate's checked `CertificateException`, retaining the original cause. Other verifier failures are unchanged. Trusted, untrusted, wrong-host, IP, wildcard, and inaccessible-verifier tests now all pass. Production TLS code and hostname enforcement were never changed; no expectations were weakened or tests skipped. Root and independent Astra both reviewed these corrections.

Raw evidence is retained at `/private/tmp/swarm-opencode-implementation-20261018/review-phone`:

- `final-phone-aar.{json,log}`, `final-phone-go.{json,log}`
- `final-phone-jvm-red.{json,log}` — six-failure baseline
- `final-phone-jvm-provider-red.{json,log}` — one-failure provider-shape baseline
- `final-phone-jvm.{json,log}` — complete green rerun and every XML suite
- `final-phone-artifacts.json`, `final-libs-javap.txt`, `final-release-swarm-aar-javap.txt`
- `final-phone-gate{,-red}.{json,log}` — complete Android contract RED/GREEN results
- `final-phone-lint-{offline,online}.{json,log}`, `final-phone-lint-{debug,release}.xml`
- `final-phone-junit-xml.zip` — all 438 fresh JUnit XML files
- `final_phone_gate.py` — retained private runner, including the later gate/lint modes

[Machine-readable record](final-phone-gates.json) includes absolute paths, source/tree hashes, artifact before/after observations, commands, times, and raw evidence hashes.

This gate covers JVM/Robolectric and native Go bindings. It does not claim physical-handset or hosted-relay end-to-end execution. Connected sealed phone/native packet tests exercise the real application path with a bounded SDK fixture; actual native runtime/owner qualification is recorded separately.
