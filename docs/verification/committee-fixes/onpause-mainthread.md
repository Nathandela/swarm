# onPause — the release path off the Android main thread (committee round 3)

Committee finding (codex round-2 finding 5, reproduced by the orchestrator; untracked,
ANR-class): `PhoneActivity.onPause()` synchronously calls `surface.release()`
(PhoneActivity.kt:193 pre-fix), which called `live.enterBackground()`,
`live.unsubscribeJournal()` and `live.stop()` INLINE on the main looper. `App.Stop` cancels
the relay session and then waits on `<-s.done` (mobile/app.go:480) for the drain goroutine,
whose teardown performs a documented five-second graceful close
(internal/remote/relay/client.go:411) — so every pause could park the main thread ~5 s, a
silent ANR (gomobile sockets never trip `NetworkOnMainThreadException`, Robolectric cannot
see a blocked looper). `converge()` called `app.start()` inline on the same thread. The s25
main-thread gate derives its waiting set from the two send-context policies and `Stop` never
touches `sendContext`, which is exactly why every s25 assertion stayed green over it.

## The fix

Matched the round-2 `TerminalWatchLane` idiom (`VerbDispatch`'s serial command lane):

- **`android/app/src/main/kotlin/dev/swarm/phone/LifecycleLane.kt`** (new):
  `LifecycleHandle` (enterBackground / unsubscribeJournal / stop / start — the facade's own
  names, deliberately, so the by-name gate below sees every call site) and
  `LifecycleLane<H>`:
  - `background(disconnect)` enqueues the T8-b severance UNKEYED on `SendPlane.COMMAND`,
    each verb swallowing its own refusal (the inline try/catch order preserved: sever, then
    withdraw journal delivery while there is still a socket, then stop — the last two only
    where the connectivity policy closes the socket). `VerbDispatch.enqueue` gates only the
    SETTLE on attachment, never the work, so the severance survives `release()`'s
    `dispatch.detach()` — the owner-locked rule: a posted severance that runs promptly in
    onPause's background window is acceptable, a dropped one is not.
  - `foreground(handle, refused)` enqueues the start on the SAME lane, which is the
    no-restart-before-stopped property: `App.Start` no-ops while `a.sess != nil`, so a start
    issued inline while a queued stop was still draining would be swallowed against the
    dying session and the stop would then land on the fresh state — a foregrounded phone,
    disconnected. On the serial lane it runs behind whatever the pause enqueued. The hold is
    eager (one start per connect cycle, not one per redraw); a refused start claws it back
    on the settle, keyed on a PER-ATTEMPT TOKEN rather than TerminalWatchLane's handle
    identity, because the handle is cached per App and identity cannot tell a superseded
    attempt from its successor (a stale refusal clearing the successor's hold would make the
    next release() skip the severance).
- **`android/app/src/main/kotlin/dev/swarm/phone/AppLifecycle.kt`** (new): the one
  production `LifecycleHandle`, every member a thin wrapper named exactly for its facade
  verb (the gate's declaration rule — renaming is jx1x's laundering and is rejected where
  declared). Exposes `app` for the per-App handle cache.
- **`PhoneSurface.kt`**: `connected: App?` replaced by
  `lifecycle = LifecycleLane<AppLifecycle>(dispatch)` plus a per-App cached handle
  (`lifecycleFor`); `release()` now calls `lifecycle.background(disconnect = policy CLOSED)`
  after `dispatch.detach()`; `converge()` calls `lifecycle.foreground(...)` with the routed
  refusal landing on `outcome.text` where the inline catch used to put it, and still calls
  `observe(app)` on the looper (subscription state, order-independent of the start
  crossing). `PhoneActivity` is untouched: `onPause -> surface.release()` still runs on the
  main thread, and now returns without joining any teardown.

## Gate extension (the fence that missed this)

**`android/gate/s25r3_releasepath_test.go`** (new, same package as s25):

- `s25r3TeardownVerbs` DERIVES the teardown-waiting verbs from the facade instead of
  listing them: an exported `(*App)` method that — itself or through the existing s25 call
  graph — performs a BARE channel receive (outside any `select`, outside any FuncLit).
  Today that derives exactly `stop` and `close`. Floor test pins both, and pins `start`
  NOT derived (FuncLit exclusion) and `isRunning` NOT derived (select exclusion). Stated
  limit: a teardown join rewritten as a select would evade and needs a row the day it is
  written.
- `TestS25R3_NoTeardownVerbBlocksTheMainLooper` scans a fixed judged set — the lifecycle
  owners and every file that can hold a live App on the lifecycle path (PhoneActivity,
  PhoneRuntime, PhoneSurface, SettingsSurface, TerminalWatchLane, LifecycleLane,
  AppLifecycle) — for teardown-verb calls outside dispatched bodies. The body reader knows
  the three s25 press shapes plus `machineVerb(` and extends each span through Kotlin's
  trailing lambda (addComputer's correctly-laned stop/add/start stays clean). NOT
  module-wide, deliberately and stated: `stop`/`close` are promiscuous bare names
  (`QrScanner.stop()` releases a camera on the main thread correctly).
- The declaration rule: in `AppLifecycle.kt` only, a stray teardown call is legal iff its
  enclosing function is named exactly the verb it wraps.
- Exemption ledger with s25's shrink-only rule; one row: `PhoneRuntime.rebuildAfterPairing`
  (see Residuals).
- Negative controls feed perturbed sources to the SAME functions: inline release stop
  reported; lane-shaped accepted; trailing-lambda accepted; stray after the lambda still
  reported; `halt()` laundering rejected; wrapper shape outside binding files rejected.
- `s25_mainthread_test.go`: `s25Surfaces` gains `LifecycleLane.kt` (subject, like
  TerminalWatchLane — a raw facade call growing inside the dispatcher is the defect hiding
  in the fix's pocket).
- `wiring_test.go` `TestWiring_TheScreenLeavingWithdrawsWhatItAskedFor` — **sanctioned
  change to a passing gate, mandated by the fix**: the inline `UnsubscribeJournal` this
  walk saw directly now crosses the seam, so the property is asserted link by link:
  release reaches `.background(`; `LifecycleLane.background` calls `.unsubscribeJournal(`
  BEFORE `.stop(`; `AppLifecycle.unsubscribeJournal` contains the DOTTED
  `.unsubscribeJournal(` — dotted because the undotted s17 match was satisfiable by the
  member's own declaration (measured, mutation 5 below).
- `mobile/r8_background_test.go` `TestR8Background_TheAndroidLifecycleReachesIt` —
  **sanctioned change to a passing gate, mandated by the fix** (a Kotlin-source fence that
  lives in the mobile package; no mobile PRODUCTION Go was touched, and the relay teardown
  constant another agent owns was not approached). It read `enterBackground()` straight out
  of `release()` and failed the moment the chunk moved onto the lane. Its two T8-b
  properties are restated link by link: release reaches `.background(`;
  `LifecycleLane.background` calls `.enterBackground(` BEFORE the braced
  `if (disconnect) {` guard (the policy's only reach into the lane — the unbraced
  `if (disconnect) started = null` eager state-clear can contain no call and is excluded by
  the brace on purpose); `AppLifecycle.enterBackground` contains the DOTTED
  `.enterBackground(`. The behavioural half — a sever-only background still severs — is
  LifecycleLaneTest's `a_sever_only_background_neither_unsubscribes_nor_stops_and_keeps_the_hold`.

## TDD evidence (failing first, GG-5)

**Gate red** (2026-08-21, before any Kotlin change):

```
--- FAIL: TestS25R3_NoTeardownVerbBlocksTheMainLooper (0.02s)
    s25r3_releasepath_test.go: committee-r3-onpause:
      PhoneSurface.kt: "release" calls [stop] outside any dispatched press
```

(Derivation floor and negative controls PASS on the same run; the PhoneRuntime ledger row
matched its live stray, so the shrink rule stayed quiet.)

**JVM red** (LifecycleLaneTest.kt written before the lane existed; gradle log
`/tmp/onpause-red-gradle.log`, `:app:testDebugUnitTest --rerun-tasks --no-build-cache`,
PIPESTATUS[0]=1, 0 new JUnit XML):

```
e: .../LifecycleLaneTest.kt:70:9 Unresolved reference 'LifecycleHandle'.
e: .../LifecycleLaneTest.kt:91:79 Unresolved reference 'LifecycleLane'.
(40 unresolved-reference errors in total)
```

**JVM green**: `:app:testDebugUnitTest --rerun-tasks --no-build-cache
--tests dev.swarm.phone.LifecycleLaneTest` (`/tmp/onpause-green-lane.log`) — BUILD
SUCCESSFUL, PIPESTATUS[0]=0, 1 JUnit XML newer than the start mark:
`TEST-dev.swarm.phone.LifecycleLaneTest.xml: tests="13" skipped="0" failures="0" errors="0"`.
Tests: real-executor thread assertion (stop never on the releasing thread); the drain
interleave (lane parked INSIDE a latched stop, re-foreground asserted not to have run, then
ordered stop-returned -> start — deterministic, no sleeps); order
(sever -> unsubscribe -> stop); sever-only background keeps the hold and a later foreground
does not restart; refused severance still unsubscribes and stops; severance survives
detach(); stop-before-start after background+foreground; eager hold (one start per cycle);
nothing-started sends nothing; refused start claws back the hold and reports; stale refusal
spares the replacement (the per-attempt token); landed start keeps the hold.

## Mutation proofs (cp-backup, cmp-restore byte-verified)

1. **Re-inline the stop** into `PhoneSurface.release()` (`lifecycleHandle?.stop()` in place
   of the lane call): `TestS25R3_NoTeardownVerbBlocksTheMainLooper` FAILED
   (`"release" calls [stop] outside any dispatched press`). Restored; `cmp` byte-identical.
2. **Remove `handle.stop()`** from `LifecycleLane.background`: 7 JVM tests FAILED
   (`/tmp/onpause-mutant-dropstop.log`), including the thread test, the drain interleave and
   the detach-survival test. Restored; `cmp` byte-identical.
3. **Run `handle.start()` synchronously** in `LifecycleLane.foreground` (no enqueue): 3 JVM
   tests FAILED (`/tmp/onpause-mutant-inlinestart.log`) —
   `a_start_asked_while_the_stop_is_still_draining_runs_after_it`,
   `foregrounding_after_backgrounding_runs_the_stop_before_the_start`,
   `a_stale_refusal_does_not_clear_the_replacement_start` — the exact
   restart-before-stopped interleaving the fix must prevent. Restored; `cmp` byte-identical.
4. **Launder the verb** (`override fun stop() { halt() }; fun halt() { app.stop() }` in
   AppLifecycle.kt): gate FAILED (`"halt" calls [stop] outside any dispatched press`) — the
   declaration rule holds only for the verb's own name. Restored; `cmp` byte-identical.
5. **Hollow the wrapper** (`AppLifecycle.unsubscribeJournal` body emptied): first exposed
   that the wiring chain's last link used `s17NamesVerb`, which the member's own
   declaration satisfies — the gate stayed green. Strengthened to the DOTTED
   `.unsubscribeJournal(`, after which the same mutant FAILED
   `TestWiring_TheScreenLeavingWithdrawsWhatItAskedFor` while the fake-driven JVM suite
   would have stayed green. Restored; `cmp` byte-identical; gate green.
6. **Sever under the disconnect guard** (`handle.enterBackground()` moved inside
   `if (disconnect) {` in LifecycleLane.background):
   `TestR8Background_TheAndroidLifecycleReachesIt` FAILED (severs only under the guard —
   the by-consequence answer restated). Restored; `cmp` byte-identical.
7. **Hollow the severance wrapper** (`AppLifecycle.enterBackground` body emptied): the same
   r8 fence FAILED on the dotted last link. Restored; `cmp` byte-identical.
8. **Sever the reach** (`lifecycle.background(...)` deleted from `release()`): the same r8
   fence FAILED (release does not reach the lane). Restored; `cmp` byte-identical;
   `go test -run TestR8Background ./mobile/` green after every restore.

## Gradle lane discipline

Every gradle invocation ran from the script file `/tmp/onpause-gradle-run.sh`: pgrep
`gradle-wrapper.jar` serialization check (refuses to start when busy; never was),
`. ./toolchain.env`, `./gradlew --no-daemon ... --rerun-tasks --no-build-cache`, tee to a
log, `PIPESTATUS[0]` reported, JUnit XML counted against a start-time mark, AAR mtime
printed before AND after every run — `app/libs/swarm.aar` stayed `Aug 21 06:59:02 2026`
throughout (mobile/ untouched by this task; no AAR rebuild). `build/`/`test-results/` never
deleted.

## Final gates

- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `PATH=$HOME/go/bin:$PATH golangci-lint run` — 0 issues
- `go test -race -count=1 ./android/gate/ ./mobile/` — ok (100 s / 51 s; the two Go
  packages this task touched, both test-only in mobile's case), run after every edit and
  every mutation restore

FINAL FULL GRADLE RUN (2026-08-21 15:03-15:12, `/tmp/onpause-final-full2.log`):
`./gradlew --no-daemon test --rerun-tasks --no-build-cache` — BUILD SUCCESSFUL in 8m 31s,
PIPESTATUS[0]=0, 354 JUnit XML files newer than the run's start mark, aggregate across
test-results: tests=2844 skipped=0 failures=0 errors=0 (includes the 13 LifecycleLaneTest
cases in both debug and release variants). AAR mtime before AND after:
`Aug 21 06:59:02 2026` (unchanged; mobile/ production Go untouched, no AAR rebuild). An
earlier full run (`/tmp/onpause-final-full.log`, BUILD SUCCESSFUL in 8m 28s, same XML
count) overlapped mutations 6-8 being applied and restored on disk, so it was rerun over
the quiescent final tree; the quiescent run is the record. No orphaned gradle/robolectric
processes (ppid==1 sweep clean after the run).

## Residuals

- **`PhoneRuntime.rebuildAfterPairing` calls `App.Close` on the main thread** (the pairing
  completion path). Close joins the drain (`<-s.done`, mobile/app.go:699) plus the pairing
  WaitGroup; on a fresh install the pre-pairing App was never started so the join is
  immediate, but a re-pair over a live session parks the looper for the close. Ledgered in
  `s25r3Exemptions` under the gate's shrink-only rule; reported for its own fix rather than
  folded into this change's footprint.
- **The Go-side 5 s bound itself** (internal/remote/relay/client.go:411) is another agent's
  scope; nothing in this task needed it changed — off the main thread the graceful close is
  the correct citizenship, and the lane's serial order already keeps a resume behind it.
- The join between `PhoneSurface`'s release/converge and the lane executing real facade
  verbs is a source-text fact (s25r3) plus JVM behaviour on a fake handle; the live-App
  join remains owed to the hardware run, exactly as VerbDispatchTest's header records.
- Stated gate limits, each written in the gate itself: a teardown join rewritten as a
  select-with-timeout evades the derivation; the judged file set is fixed (a teardown call
  moved to an unlisted file is jx1x's one-hop bound); the dispatched-body spans include
  `settle = { ... }` arguments, which run on the looper (inherited from s25's own spans).
- Behavioural note: `observe(app)` now runs beside the enqueued start rather than after a
  synchronous one; both its calls are local flags plus a listener install, guarded and
  idempotent, and a refusal routes to `outcome.text` exactly as before.
