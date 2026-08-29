# Signal Field: first-three-frame verification

Branch: `codex/mobile-ui-direction`
Tracker: `agents-tracker-6cn8`

## Scope

The approved Signal Field direction is implemented only for:

1. true first-run welcome;
2. live QR scanning;
3. SAS trust confirmation.

Revoked and repair-required pair-only states keep their established recovery composition. All
post-pairing destinations remain outside the production diff.

## Failing-first evidence

The tests were added before the production primitives. The focused command was:

```text
cd android
. ./toolchain.env
./gradlew --no-daemon :app:testDebugUnitTest \
  --tests 'dev.swarm.phone.ui.kit.SignalFieldTest' \
  --tests 'dev.swarm.phone.ui.kit.ScanReticleTest' \
  --tests 'dev.swarm.phone.ui.screens.PairOnlyViewTest' \
  --tests 'dev.swarm.phone.ui.screens.PairingGuidanceViewTest' \
  --rerun-tasks --no-build-cache
```

After the worktree-local AAR was built, the run reached test compilation and failed as intended on
the absent production contract:

```text
ScanReticleTest.kt: unresolved reference beamInk / beamStrokePx
SignalFieldTest.kt: unresolved reference signalFieldMark / sasSequence / SignalPathDrawable
Task :app:compileDebugUnitTestKotlin FAILED
BUILD FAILED
```

That is the RED state: the test suite named the approved atmospheric mark, static scan path, and
ordered trust sequence before any of them existed.

The adversarial pass then added seven narrower contracts before their fixes. The focused run
failed all seven as intended: recovery evidence still admitted the welcome art, the welcome was
top-aligned, a live camera retained its redundant scan action, both colored paths used the wrong
transparent endpoint, the trust path missed the beacon centers, and 2x-font-scale emoji did not
opt into bounded sizing. The same seven-test command passed after those changes.

The final scope audit then tightened the recovery contract from “no artwork” to “retain the
previous factual title/body/CTA.” Its first compile failed on the intentionally absent
`copyForRevokeEvidence`; the focused recovery suite and the complete JVM suite passed after that
presentation was added.

## Invariants exercised

- The welcome raster is the checked-in atmospheric mark, decorative, non-clickable, and limited to
  `FIRST_RUN`.
- Recovery states, revoke evidence, and the started pairing flow contain no welcome artwork.
- A live scanner and its progress caption precede the quiet manual fallback and guidance; the
  already-completed Scan action is absent, while the pre-scan state keeps its existing
  instruction-first structure.
- The scan trajectory remains a static foreground `Drawable`; no new animation or touch surface is
  introduced, and its colored fade retains the work-teal RGB at alpha zero.
- Trust renders the protocol's real six symbols in source order, as six cards over one static path.
- The six emoji remain unclipped at 320 dp / 2x font scale, and the path crosses their centers.
- TalkBack receives one ordered sequence; the connector and tiles do not become seven extra stops.
- Match, mismatch, and stop retain their existing order and handlers.

## Final green evidence

All commands below passed on 2026-08-29:

```text
./gradlew --no-daemon :app:testDebugUnitTest --rerun-tasks --no-build-cache
BUILD SUCCESSFUL in 6m 43s (31 actionable tasks)

./gradlew --no-daemon :app:testDebugUnitTest --no-build-cache
BUILD SUCCESSFUL in 4m 2s (post-audit rerun)

./gradlew --no-daemon :app:compileDebugAndroidTestKotlin --no-build-cache
BUILD SUCCESSFUL in 45s

./gradlew --no-daemon :app:lintDebug :app:assembleDebug --no-build-cache
BUILD SUCCESSFUL in 3m 4s (49 actionable tasks)

go test ./android/gate -skip '^TestPBTOOL5_GomobileToolDoesNotEnterTheDaemonBinaries$' -count=1
ok github.com/Nathandela/swarm/android/gate

go test ./android/gate -run '^TestPBTOOL5_GomobileToolDoesNotEnterTheDaemonBinaries$' -count=1
ok github.com/Nathandela/swarm/android/gate

go test ./cmd/... ./deploy/... ./internal/... ./mobile/... ./scripts/... -timeout=15m
all packages passed

go vet ./...
go build ./...
golangci-lint run
0 issues
```

The Android gate's recursive `go list` test is intentionally recorded separately. Running it
inside a fully parallel `go test ./...` held the shared Go build-cache lock until the package's
ten-minute timeout; alone it passed in 1.986 seconds. This is a runner-contention split, not a
waiver: the isolated test and every remaining package both passed.
