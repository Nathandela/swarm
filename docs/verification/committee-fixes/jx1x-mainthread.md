# jx1x — Terminal watch verbs off the Android main thread

Committee finding (Opus H3, unanimous, P1 beta blocker): `PhoneSurface.reconcileTerminalWatch`
(PhoneSurface.kt:2611-2643 pre-fix) called `TerminalFallbackBinding.watch()` / `unwatch()`
inline on the Android main thread, and `renewHeldWatch()` ran `renew()` from a 20-second
main-looper `Handler` tick. Each verb crosses JNI into `App.TerminalViewWatch/Unwatch/Renew`
(mobile/commands.go:444-465), which resolve through `unsignedCommand` -> `sendContext` ->
`awaitConn` (5 s busy-wait, mobile/relay.go:178) plus a MailboxAppend bounded at 10 s — worst
case ~15 s of main-thread blocking, a silent ANR (gomobile sockets never trip
`NetworkOnMainThreadException`).

## The fix

Matched the existing idiom (`VerbDispatch`, the seam agents-tracker-7j4b installed for every
other facade verb):

- **`android/app/src/main/kotlin/dev/swarm/phone/TerminalWatchLane.kt`** (new):
  `TerminalWatchHandle` (the watch/unwatch seam; `TerminalFallbackBinding` is the one
  production implementation) and `TerminalWatchLane<H>` — holds the watch state and enqueues
  both verbs UNKEYED on `SendPlane.COMMAND` (the single serial `swarm-command` thread), so an
  unwatch-then-watch sequence cannot interleave. The hold is eager (redraws renew instead of
  re-watching); a refused watch clears the hold on the main-looper settle, with an identity
  check so a stale refusal cannot clear a replacement watch.
- **`PhoneSurface.kt`**: `watchedFallback`/`watchedBinding` replaced by
  `watchLane = TerminalWatchLane<TerminalFallbackBinding>(dispatch)`;
  `reconcileTerminalWatch` now drops/holds through the lane; `renewHeldWatch` enqueues
  `held.renew()` on `SendPlane.COMMAND` with a single-flight key (`watchRenewalCrossing`) so a
  renewal parked in awaitConn is not joined by a queue of successors. The renewal tick stays a
  main-looper `Handler` (cheap), only the verb crosses on the lane.
- **Release path preserved**: `release()` -> `reconcileTerminalWatch(null, null)` ->
  `watchLane.drop()` runs BEFORE `dispatch.detach()`, and `VerbDispatch.enqueue` gates only
  the settle on attachment, never the work — the unwatch always reaches the machine
  (asserted by `dropping_the_watch_still_unwatches_after_the_dispatch_detaches`).
- **`TerminalFallbackScreen.kt`**: `TerminalFallbackBinding : TerminalWatchHandle`
  (`override` on `watch`/`unwatch`; constructor stays private, `forRoutedSession` stays the
  only factory — the r8r4 structural fence is untouched).

## Gate extension (the fence that missed this)

`android/gate/s25_mainthread_test.go` scanned only PhoneSurface.kt/SettingsSurface.kt for
FACADE verb names; the binding's bare `watch`/`unwatch`/`renew` names matched nothing, and
its exemption ledger's comment still claimed terminal watching was absent. Extended:

- **Assertion 4** (`TestS25_AWrappedWaitingVerbIsStillDispatchedOrMisplaned`):
  `s25IndirectWaitingVerbs` derives WRAPPER verbs from every production Kotlin file that is
  not a subject — any function calling a waiting facade verb outside a dispatched press is a
  wrapper, and its bare name is judged like the verb itself, across ALL production Kotlin
  (stray = outside `Press(`/`press(`/`enqueue(` bodies; presses containing one must declare
  `SendPlane.COMMAND`). Derivation floor pinned on `watch`/`unwatch`/`renew`.
- **Negative control** (`TestS25_TheWrapperDerivationDiscriminates`): perturbed sources
  through the same predicates — thin wrapper derived; dispatched facade call derives nothing;
  re-inlined wrapper call reported; lane-shaped dispatch accepted; wrapper on the LIVE lane
  rejected.
- `s25Surfaces` gains `TerminalWatchLane.kt`; header bullet and the stale
  `s25RenderPathExemptions` comment now record jx1x as paid.
- `r8_fallback_ui_test.go` lease gate reads PhoneSurface + TerminalWatchLane as the pair, in
  the DOTTED call shape (bare names were satisfiable by the interface's own declarations —
  measured, see mutation 2).
- `r8r3_fallback_ui_test.go` `r8r3BindingMethods` accepts `override fun` so the modifier does
  not drop the two watch verbs from the bound-verb ledger's sight.
- `r8r4_fallback_binding_test.go` structured-screen ban additionally names
  `TerminalWatchLane`/`TerminalWatchHandle`.

## TDD evidence (failing first, GG-5)

**Gate red** (2026-08-21, before any Kotlin change):

```
--- FAIL: TestS25_AWrappedWaitingVerbIsStillDispatchedOrMisplaned (0.15s)
    s25_mainthread_test.go:929: agents-tracker-jx1x:
      dev/swarm/phone/PhoneSurface.kt: "reconcileTerminalWatch" calls [unwatch watch] outside any dispatched press
      dev/swarm/phone/PhoneSurface.kt: "renewHeldWatch" calls [renew] outside any dispatched press
```

**JVM red** (TerminalWatchLaneTest.kt written before the lane existed; gradle log
`/tmp/jx1x-red-gradle.log`, `test --rerun-tasks --no-build-cache`, PIPESTATUS[0]=1):

```
e: .../TerminalWatchLaneTest.kt:56:9 Unresolved reference 'TerminalWatchHandle'.
e: .../TerminalWatchLaneTest.kt:66:79 Unresolved reference 'TerminalWatchLane'.
```

**JVM green**: `:app:testDebugUnitTest --rerun-tasks --no-build-cache
--tests dev.swarm.phone.TerminalWatchLaneTest` — BUILD SUCCESSFUL, PIPESTATUS[0]=0, 1 JUnit
XML newer than the run's start mark:
`TEST-dev.swarm.phone.TerminalWatchLaneTest.xml: tests="8" skipped="0" failures="0" errors="0"`.
Tests: real-executor thread assertion (verb never on the reconciling thread), order
(`watch a, unwatch a, watch b` on one lane), release-path unwatch after `detach()`,
no-op drop, refused watch clears hold, stale refusal spares the replacement, landed watch
kept. (No Robolectric: the suite's own recorded pattern — threads via real executor + latch,
order/lifecycle via hand-driven executors, VerbDispatchTest's split.)

## Mutation proofs (cp-backup, cmp-restore byte-verified)

1. **Re-inline** `binding.watch()` into `reconcileTerminalWatch` (scratch copy of
   PhoneSurface.kt): `TestS25_AWrappedWaitingVerbIsStillDispatchedOrMisplaned` FAILED
   (`"reconcileTerminalWatch" calls [watch] outside any dispatched press`). Restored;
   `cmp` byte-identical; gate green.
2. **Remove `handle.unwatch()`** from the lane: first exposed that the union scan was
   satisfiable by the interface declaration (gate stayed green) — lease gate strengthened to
   dotted `.unwatch()`/`.renew()`/`.watch()`, after which the same mutant FAILED
   `TestR8Gate_TheWatchIsOpenedOnceClosedAndRenewed`. Restored; `cmp` byte-identical; green.
3. **Inline `handle.watch()`** in `TerminalWatchLane.hold` (synchronous on the caller):
   4 JVM tests FAILED, including
   `the_watch_verb_does_not_run_on_the_thread_that_reconciled_the_screen` and
   `replacing_the_held_watch_unwatches_the_old_one_first_on_one_lane`
   (log `/tmp/jx1x-mutant-lane.log`). Restored; `cmp` byte-identical.

## Gradle lane discipline

Every gradle invocation ran from the script file `/tmp/jx1x-gradle-run.sh`: pgrep
`gradle-wrapper.jar` serialization check (never busy), `. ./toolchain.env`,
`./gradlew --no-daemon ... --rerun-tasks --no-build-cache`, tee to a log,
`PIPESTATUS[0]` reported, JUnit XML counted against a start-time mark, AAR mtime printed
before AND after every run — `app/libs/swarm.aar` stayed `Aug 21 06:59:02 2026` throughout
(mobile/ untouched by this task; no AAR rebuild). `build/`/`test-results/` never deleted.

## Final gates

- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `PATH=$HOME/go/bin:$PATH golangci-lint run` — 0 issues
- `go test -race -count=1 ./android/gate/` — ok (93 s)
- `go test ./android/gate/` — ok (full package, all fences)
- Full `./gradlew --no-daemon test --rerun-tasks --no-build-cache` — see the closing record
  below.

FINAL FULL GRADLE RUN (2026-08-21 13:02-13:10, `/tmp/jx1x-final-full.log`):
`./gradlew --no-daemon test --rerun-tasks --no-build-cache` — BUILD SUCCESSFUL in 7m 52s,
PIPESTATUS[0]=0, 352 JUnit XML files newer than the run's start mark, aggregate across
test-results: tests=2818 skipped=0 failures=0 errors=0. AAR mtime before AND after:
`Aug 21 06:59:02 2026` (unchanged). No orphaned gradle/robolectric processes (ppid==1 sweep
clean after the run).

## Residuals

- The join between `PhoneSurface`'s reconcile and the lane executing real facade verbs is a
  source-text fact (s25 assertion 4) plus JVM behaviour on a fake handle; the live-App join
  remains owed to the hardware run, exactly as VerbDispatchTest's header records for every
  other verb.
- `binding.grid()` (`App.Peek` + `App.Session`) stays on the main thread in
  `heldWatchLapsed`: it is a local snapshot-cache read, not a send-path verb (verified: no
  send-path reach from `Peek`/`Session` in mobile/), and r8r5 requires the grid-shaped
  question at that seam.
- The wrapper derivation is one hop (a wrapper of a wrapper in a third file would need the
  first wrapper's name to appear undispatched somewhere scanned — the permissive-by-name
  direction the fence records); today's derived set is exactly the binding's verbs.
