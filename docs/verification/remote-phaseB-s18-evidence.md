# S18 evidence — app security hardening (PB-SEC-3, 4, 5, 6, 7, 8, 11, 12, 13, 14)

> **SUPERSEDED IN PART — READ THIS FIRST (added 2026-07-30).** Everything below about
> `FLAG_SECURE` and `setRecentsScreenshotEnabled` describes the app **as it was when this slice
> closed**, and is no longer true of the shipped app. ADR-007 **B65** withdrew PB-SEC-4 by owner
> decision: the app now ALLOWS screenshots, screen recording and the recents thumbnail. Both APIs
> are gone, `SecureWindow.protect()` was removed with them, and the gate that once required the
> flag now **requires its absence** — reinstating it fails `android/gate/s18_sec4_windowsecurity_test.go`.
>
> This file is left otherwise unedited because it is signed-off evidence for a closed slice, and
> rewriting history to match a later decision is how an evidence trail stops being one. Read the
> `FLAG_SECURE` passages as a record of what was true then, not as a claim about now.
>
> **`filterTouchesWhenObscured` and PB-SEC-12 are UNAFFECTED** and everything this file says about
> them still holds: the touch filter on the gated actions is untouched by B65.

**Written by the GREEN implementer, 2026-07-26, from an uncommitted working tree.** Nothing here
is staged or committed; every command below was run in
`.claude/worktrees/remote-control-research`.

## Starting state

`go test ./android/gate/ -run TestPBSEC` and `go test ./internal/skeleton/ -run TestPBSEC`
against the RED author's ten files, at HEAD `a2b6397`:

- **22 failing test functions**, across PB-SEC-3, 4, 5, 8, 11, 12, 13 and 14.
- **PB-SEC-6 and PB-SEC-7 passed already**, and their files say so in their own headers rather
  than claiming a RED they do not have. They are re-assertion requirements; they were left
  untouched. `internal/skeleton` is green before and after.
- **Zero skips** across the 3131 lines, and that property is preserved: every gate assertion
  still reads a checked-in file, so the verdict on a bare CI runner equals the verdict here.

## Requirement by requirement

### PB-SEC-4 — the Activity, and why S18 grew one

The requirement was amended (`cb657db`) because it had **no subject**: the module declared no
`<activity>`, so `FLAG_SECURE` had no Window and `filterTouchesWhenObscured` (PB-SEC-12
clause 1) had no View. The ruling assigns S18 a **minimal Activity**. Delivered:

| file | what it is |
| --- | --- |
| `android/app/src/main/kotlin/dev/swarm/phone/PhoneActivity.kt` | the Activity. Owns the window and nothing else. |
| `android/app/src/main/kotlin/dev/swarm/phone/SecureWindow.kt` | **the one sink**: `FLAG_SECURE` + `setRecentsScreenshotEnabled(false)`, plus `gate()` for the touch filter. |
| `android/app/src/main/kotlin/dev/swarm/phone/PhoneSurface.kt` | the views: pairing surface, terminal peek, three gated controls. |
| `android/window-security.tsv` | the policy table: seven screens, what is at stake on each. |

**It wires S16's models rather than rewriting them.** Every string comes from `PairingFlow`,
`ConnectionBanner`, `TerminalPeek` or `RoutedError` through `FacadeBridge`.

**The Activity and the surface are separate files for a requirement, not for style.**
PhoneActivity is exported with a LAUNCHER filter — forced, because a launcher is another
process and cannot start a component the app keeps to itself. PB-SEC-11's last clause is that
no exported component can act on the session, and the gate enforces it by scanning the Kotlin
file *named after* each exported component. So every facade verb lives one call away in
`PhoneSurface`.

**Runtime assertions, not just source ones.** `PhoneActivityWindowTest` (Robolectric, 3 tests)
drives a real Activity: the window carries `FLAG_SECURE` after `onCreate`, every gated action
filters obscured touches, and a hostile launch intent (a `swarm://` data URI plus
`session`/`action`/`relay` extras) renders byte-identically to a plain launch.

Both were **validated by breaking the link**, and both edits were reverted immediately:

```
remove SecureWindow.protect(this) from onCreate
  -> the_window_carries_flag_secure_after_oncreate FAILED   (3 tests completed, 1 failed)
remove the SecureWindow.gate(...) wrapper from gatedButton
  -> every_gated_action_filters_obscured_touches FAILED     (3 tests completed, 1 failed)
```

`setRecentsScreenshotEnabled` is asserted at the **source** level by the Go gate and not by a
Robolectric shadow: a shadow returning what the test told it to is not evidence about a
thumbnail on disk. PB-E2E-5 stays deferred.

### PB-SEC-5 — cleartext, with the scope limitation beside it

`android:usesCleartextTraffic="false"` on `<application>`, with a manifest comment naming
**PB-NET-2** as the control that actually protects the relay socket. The attribute governs the
Java/WebView stack; the relay connection is Go's `crypto/tls` inside the gomobile `.so` and is
unaffected by it. v1 of the spec drew the opposite inference and shipped it, which is why the
statement sits beside the control rather than in an evidence file.

### PB-SEC-11 — the exported allowlist

`android/exported-components.tsv`: three rows, joined bidirectionally against the manifest.
`.PhoneActivity` (true, forced by LAUNCHER), `.runtime.BootReceiver` (true, BOOT_COMPLETED is
implicit), `.push.SwarmMessagingService` (false).

The BootReceiver row records that its body is **empty beyond the action check** — acting on
`LifecycleConvergence`'s plan is owed by a later slice — so the row is where that widened reach
has to be argued when it lands.

### PB-SEC-12 — three clauses, three treatments

- **Tapjacking**: `SecureWindow.gate` applies `filterTouchesWhenObscured` by construction to
  take-control, kill and revoke. Mutation-checked above.
- **Clipboard**: no production Kotlin references `ClipboardManager`, `setPrimaryClip`,
  `getPrimaryClip`, `ClipData` or `CLIPBOARD_SERVICE`. The clause is satisfied as written.
- **Documented limits**: `android/input-path-limits.md`.

The document records the **paste residual** the RED author flagged: `App.Paste` is not a
clipboard use, but the text crosses as a Java `String` (PB-BIND-4 keeps `[]byte` crossings to
an enumerated few), Strings are immutable, and neither the String nor the derived byte slice is
wiped. So a reader who finds "no clipboard use: verified" cannot conclude the path leaves
nothing behind. Fixing it is a PB-BIND-4 / ADR-007 B8 decision, not an S18 one.

It also records the two limits the app genuinely cannot close: a third-party IME sees every
keystroke before the app does (**so the lease and the biometric gate protect the channel, not
the keyboard**), and an accessibility service can read the rendered screen and synthesise
gestures (**so neither `FLAG_SECURE` nor `filterTouchesWhenObscured` excludes it**).

### PB-SEC-13 — the release build

`isDebuggable = false` and `isProfileable = false`, stated rather than inherited, with the
heap-dump and crash-report decision recorded in the build file where it is read: the app ships
**no crash reporter at all**, so an uncaught exception produces a system tombstone and uploads
nothing. That is a deliberate posture a later reader would otherwise mistake for an omission.

### PB-SEC-14 — locking *and* verification

Both halves, because neither implies the other:

- `dependencyLocking { lockAllConfigurations() }` in `android/app/build.gradle.kts`, with
  **`android/app/gradle.lockfile`** checked in (266 locked modules).
- **`android/gradle/verification-metadata.xml`**: **438 components, 785 sha256 entries**,
  generated with real checksums by `./gradlew --write-verification-metadata sha256 lint test`.

Verification is **live and enforced**, and that was demonstrated accidentally and usefully: the
first metadata pass was generated against `:app:dependencies`, which does not resolve
`com.android.tools.build:aapt2`. The next ordinary build **failed**:

```
Dependency verification failed for configuration ':app:detachedConfiguration2'
2 artifacts failed verification:
  - aapt2-8.13.2-14304508-osx.jar (com.android.tools.build:aapt2:8.13.2-14304508) from repository Google
```

Regenerating against the real gate command (`lint test`) fixed it. A subsequent
`./gradlew clean && ./gradlew lint test` is green with verification active.

### PB-SEC-8 — the inventory, derived from the resolved closure

`android/dependency-inventory.tsv`: **319 rows**, one per distinct `group:name` in the
verification metadata, each with a justification and a requirement. Three classes, and the
difference matters to a reviewer: ships in the APK (on `releaseRuntimeClasspath`), test
classpath only, and build classpath only — the last still checksum-verified because it executes
with full privileges on the machine that signs the release.

The firebase-messaging row names what it drags in (play-services-basement, -base, -tasks,
-stats, -cloud-messaging plus the datatransport modules), which is the reasoning PB-PAIR-3
applied when it **rejected ML Kit on the same grounds**.

No analytics, telemetry, attribution or crash-reporting module resolves into any of the three.

### PB-SEC-3 — the log-sink inventory

`docs/verification/s18-log-sinks.tsv`, generated by
`go test ./android/gate/ -run TestPBSEC3 -update-logscan`. **One phone-side logging call site**
(`push/PushTokens.kt:70`), content-free: a static message saying push is unavailable in this
build. The new Activity, surface and sink add none.

## The phone's panic button, which did not work

`opForAction` (`internal/remotegw/command_loop.go`) had **no arm for `ActionDeviceRevoke`**. The
action is in the signed set, `skeleton/deviceauth.go` classes it and `handleDeviceRevoke` serves
it — but a phone-sealed revoke hit the default and was refused *"unsupported command action"*
one hop short of the daemon, **with no reply sealed**, so the op could never resolve either.

**Two production edits, and the second was not in the brief:**

1. `opForAction` maps `ActionDeviceRevoke -> protocol.OpDeviceRevoke`.
2. `Gateway.ForwardCommand` copies the signed subject into `Control.TargetDeviceID`.
   `handleDeviceRevoke` reads that field both as its authorization subject and as the device to
   remove; the phone signs the target in the **session** position of the command tuple, because
   that tuple has no separate device field. **The arm alone is not enough** — without the copy
   the daemon receives a revoke naming no device and refuses it. The gateway cannot escalate by
   doing this: the subject is bound under the phone's signature, so any other value simply
   fails authorization.

`mobile.RevokeThisDevice` now seals the signed command instead of recording a durable local
refusal. `(*App).refuse` had no callers left and was deleted; its absence is the record that no
facade surface is verb-less any more.

**New test, `internal/skeleton/s18_revokeverb_test.go`**, on the S18 rig (real relay, real
gateway Service, real daemon, real `phonecore.Core`). Two tests, both mutation-checked:

```
remove the opForAction arm     -> "the device is STILL REGISTERED 15s after ...", test 1 fails
remove the TargetDeviceID copy -> the daemon refuses ("device command not authorized"),
                                  the device survives, tests 1 and 2 fail
```

The first test does **not** wait for a reply, and that is deliberate: a successful self-revoke
destroys the path its own reply comes back on (epoch rotation, lease severed, gateway exits,
relay registration dropped). It asserts the machine-side effect and the gateway exit with
`remotegw.ErrDeviceRevoked` instead. The **reply path** is asserted by the second test, where
nothing is severed.

`mobile/screen_coverage.tsv`'s `revoke` row, which recorded the gap, now records the fix.

## Tests corrected because the fix made their premise false

Reported rather than settled quietly.

**`mobile/conformance/verbs_test.go`** — `TestS8_SurfacesWithNoWireVerbFailVisiblyAndLeakNoPendingOps`
pinned the workaround: it asserted that *no* `device_revoke` reached the machine. Kept as it
was, it would have failed the correct implementation. Renamed to
`TestS8_TheRevokePanicActionSealsItsSignedCommandAndResolves`, asserting the property the old
one protected on the path that now exists: the revoke seals its command, names **this** device,
is counted in flight, and `PendingOpCount` returns to baseline once a reply resolves it. The
correction and its reason are written into the test's header.

**`mobile/conformance/s17_tokenlifecycle_test.go`** — `TestS17_RevokingThisDeviceDeletesItsPushToken`
regressed, and the regression is real rather than scaffolding. `RevokeThisDevice` now runs
PB-SYNC-7's fail-closed gate (`requireReconciled`), and the S17 push rig's phone has never seen
a reconcile record, so the **wire half is correctly refused** with `swarm/unreconciled`.

The verb's internal order is what makes this safe and is now asserted: **drop the push token
durably first, attempt the wire half second.** An owner pressing revoke on a handset they no
longer trust is in exactly the state where the machine cannot be told, and a token left
registered there is a provider-visible identifier for a device its owner disowned. The test now
asserts **both**: that the call fails closed with the unreconciled class, and that the token is
deleted anyway. That is a stronger test than the one it replaces.

**No exemption from PB-SYNC-7 was granted to device_revoke.** It is a mutating op like any
other; exempting it would be a spec decision and is flagged below rather than taken.

## Gate results

```
go build ./...        clean
go vet ./...          clean
./gradlew lint test   BUILD SUCCESSFUL, from clean, with dependency verification active
```

`go test ./...` — **every package S18 touches is green, deterministically**:

```
./android/gate          ok   (all 22 previously-RED assertions pass)
./internal/skeleton     ok   (PB-SEC-6, PB-SEC-7, and the new revoke-verb tests)
./mobile                ok
./mobile/conformance    ok   when run alone
./internal/remotegw     ok   when run alone
```

**Three tests are unstable or RED across the whole-suite run, and none of them is S18's.**
Each was reproduced in a pristine `git archive HEAD` tree with none of this slice's changes:

| test | package | at HEAD | what it is |
| --- | --- | --- | --- |
| `TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset` | `cmd/swarm` | fails deterministically | **S18b's declared RED file** (`s18b_unbrick_test.go`, tracked at HEAD). The relay's `revoked` bucket is never cleared. Owned by the S18b implementer. |
| `TestS6B_GatewayInputLatencyIsNotPollGated` | `internal/remotegw` | flakes 2/2 full-suite runs | Timing-sensitive latency assertion; passes 6/6 in isolation in both trees, fails under whole-suite parallel load. |
| `TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT` | `mobile/conformance` | flakes 3/4 runs at `-count=5` (2/4 here) | `TempDir RemoveAll cleanup: directory not empty` — a goroutine still writing after the test returns. Not an assertion failure. |

The first full-suite run of this slice happened to draw a clean sample, which is exactly why
the comparison above was run rather than reported as green.

Kotlin, counted from source (`@Test` occurrences) and cross-checked against a forced
`--rerun-tasks` run so no cumulative result files are aggregated:

```
classes=32 tests=208 failures=0 errors=0     (baseline 205 across 31; +3 in 1 new class)
```

## Residuals, flagged rather than fixed

1. **`golangci-lint run ./...` is not clean at HEAD** and was not clean before this slice: 26
   findings, all in files S18 does not touch (`internal/remote/relay/store.go`,
   `internal/remotegw/outbox.go`, `internal/remote/device/capability.go`, and a spread of
   `errcheck`/`ineffassign` in test files). S18 introduced one — `(*App).refuse` going unused —
   and cleared it by deleting the dead helper. implementation-goals.md GG-4 requires all four
   gates green before an epic closes, so this is owed by someone.
2. **`PhoneSurface` acts on the first row of the triage inbox** because the surface has no
   navigation. That is the bounded-scope consequence of "a minimal Activity, not a finished
   app"; a session picker belongs to the PB-APP-1..8 work, not here.
3. **CORRECTED 2026-07-26 — this residual has been answered, and the answer went the other way.**
   It originally read: *"`device_revoke` is now gated on PB-SYNC-7 reconciliation. Whether the
   panic action should be exempt is a spec question with a real argument on each side, and it is
   not S18's to answer."* It was answered by **PB-STATE-4's amendment of 2026-07-26**
   (`e16b0c7`): `RevokeThisDevice` is **EXEMPT** from the reconcile fail-closed gate. The
   boundary drawn is not "revoke is special" — the gate protects ops whose **target is selected
   from synchronized state** (`kill`, `launch`, `take_control`), because stale state makes them
   act on the wrong object, and a self-revoke selects no target and only removes capability.
   Implemented at `mobile/commands.go:557`; both directions required by the amendment are tested
   and green (`TestPBSTATE4_AnUnreconciledPhoneCompletesItsRevokeEndToEnd`, and
   `TestPBSTATE4_TheRevokeExemptionDoesNotWidenToTheStateSelectedVerbs` over Kill/Launch/
   TakeControl). S18's own reading — that exempting it would be a spec decision rather than an
   implementation one — was right; the decision was then taken.
4. **Two pre-existing flakes in the shared suite** (`TestS6B_GatewayInputLatencyIsNotPollGated`,
   `TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT`), reproduced at HEAD and characterised in
   the table above. Neither is S18's, and neither has an owner recorded anywhere I could find.
   A latency assertion that fails under suite load, and a temp-dir cleanup race, are both the
   kind of noise that trains readers to re-run a red gate rather than read it.
5. **Nothing here claims a physical-handset property.** PB-E2E-5 stays deferred: no real
   biometrics, camera, FCM delivery, Doze or hardware attestation. `FLAG_SECURE`,
   `setRecentsScreenshotEnabled` and `filterTouchesWhenObscured` are asserted as *what the app
   asks the platform for*, never as what the platform then does.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** to emit the
traceability table's DERIVATION column (ADR-007 B129). One row per requirement, the verdict
token `DERIVED` or `NOT DERIVED`, and — for `DERIVED` — **the mutation that was made to fail, in
the same row**. A `DERIVED` verdict with an empty mutation cell is refused and counted NOT
DERIVED. Any requirement with no row here is NOT DERIVED.

**`DERIVED` means somebody made this row's fence fail on purpose and restored it.** Reading a
fence is not deriving it. Every mutation below was applied to a PRODUCTION file in a detached
worktree, the named test run, and the mutation reverted; the `AndroidManifest.xml`,
`build.gradle.kts`, `verification-metadata.xml`, `toolchain.env` and Kotlin edits are edits to
the artifacts the build and the platform actually consume, not to a table a test transcribes
(B113).

**THE KOTLIN MODULE COMPILES AGAIN.** The stale `android/app/libs/swarm.aar` that blocked every
Kotlin fence this morning was rebuilt (`android/build-aar.sh`, exit 0) and
`:app:testDebugUnitTest` is green, so PB-SEC-12's clause-1 mutation below was driven through a
real Robolectric Activity rather than deferred. That is what separated this row's two fences.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-SEC-3 | DERIVED | Kotlin: `Log.d("swarm", "grid " + gridText)` added to `PhoneSurface.kt` -> caught. Kotlin: a log call taking `contentKey` added to `keys/KeystoreCustody.kt` -> caught by the ARGUMENT scan, not only the inventory. **Go root proven live too**: `log.Printf("ck=%x", contentKey)` added to `internal/phonecore/state.go` (compiling) -> caught, naming the file, line and identifier |
| PB-SEC-4 | DERIVED | the requirement is INVERTED, so the mutation reinstates what it forbids: a `FLAG_SECURE` `setFlags` call added to `SecureWindow.kt` -> caught; `setRecentsScreenshotEnabled(false)` added to `PhoneActivity.onCreate` -> caught. Both fail `TestPBSEC4_NoProductionSourceReinstatesTheScreenshotBlock`, which is the property B65 kept |
| PB-SEC-5 | DERIVED | `android:usesCleartextTraffic="false"` -> `"true"` in the shipped manifest -> caught |
| PB-SEC-6 | DERIVED | `controlGateOpen` clause 3 (the server-clock expiry re-check) deleted -> caught: *"a phone kept typing past the horizon its own take_control signed"*. Kill switch needed BOTH layers removed to fail — clause 1 alone SURVIVES, and `SeverAllRemoteControl` alone SURVIVES; removing both fails `the_kill_switch_halts_a_lease_that_is_already_live`. **FINDING E below: clause 2, the gate's fail-closed default, survives the WHOLE of `internal/skeleton` + `internal/protocol`** |
| PB-SEC-7 | DERIVED | `a.rotateEpoch()` neutered at the revoke call site -> caught (*"the epoch did not rotate on revoke (1 -> 1)"*); the gateway's `return ErrDeviceRevoked` removed -> caught by two independent tests. The chain's two arrows are fenced separately |
| PB-SEC-8 | DERIVED | `com.google.firebase:firebase-analytics` injected into the RESOLVED closure (`verification-metadata.xml`) -> caught by both the analytics scan and the bidirectional inventory join; a non-analytics `org.example:widget-lib` injected -> caught as an unjustified dependency, so the join is not analytics-specific |
| PB-SEC-11 | DERIVED | `SwarmMessagingService` `exported="false"` -> `"true"` -> caught by the manifest/allowlist join; `android:exported` deleted from `BootReceiver` -> caught; a `sendInput(` call added to the exported `PhoneActivity.kt` -> caught by the last clause |
| PB-SEC-12 | DERIVED | `SecureWindow.gate` changed to set `filterTouchesWhenObscured = false` — the identifier stays, so the Go text scan SURVIVES — caught by the Kotlin `PhoneSurfaceControlsTest.every_button_and_switch_on_screen_filters_obscured_touches` walking the real View hierarchy. **FINDING F below**: removing every `SecureWindow.gate(...)` CALL SITE while leaving the object intact survives the entire Go gate |
| PB-SEC-13 | DERIVED | release `isDebuggable = false` -> `true` -> caught; `isProfileable = true` added to the release block -> caught; `android:debuggable="true"` hardcoded on `<application>` -> caught; `if (BuildConfig.DEBUG) { b.takeControl(1L) }` added to production Kotlin -> caught |
| PB-SEC-14 | DERIVED | the `dependencyLocking { lockAllConfigurations() }` block deleted -> caught; `<verify-metadata>true</verify-metadata>` -> `false` -> caught; EVERY `<sha256>` stripped from one component (`androidx.activity:activity`) -> caught as *"1 of 459 pinned components carry no sha256/sha512"*; `SWARM_ANDROID_NDK` floated to `27` -> caught; `SWARM_GOMOBILE_VERSION` -> `latest` -> caught |

### FINDING E — PB-SEC-6's fail-closed default has no fence anywhere. OPEN.

`controlGateOpen` clause 2 is the daemon's fail-closed default: no control session on this
connection means drop. Changing it to **`if ctl == nil { return true }`** — the gate opens for a
connection that never took control, and clauses 3-5 are skipped with it — passes
`go test ./internal/skeleton/ ./internal/protocol/` in full: 128 s of skeleton and the whole
protocol package, both `ok`.

The end-to-end refusal PB-SEC-6 observes comes from one layer up: `LeaseManager.Input` finds no
`LeaseConn` for an unleased session and returns, so the keystroke never reaches the daemon and
the daemon's own default is never consulted. That is defence in depth working — and it is also
why nothing measures the second layer. `no_lease_the_server_refuses_the_keystroke` cannot
distinguish a daemon that refuses from a daemon that is never asked.

Contrast clause 4, which looked identical from the PB-SEC-6 suite (removing it survives both
PB-SEC-6 tests) and turned out to be fenced elsewhere:
`internal/protocol/TestProtocol_RevokeSeversLeaseViaSeparateServer` catches it. Clause 2 has no
such test. The distinction only appears if the mutation is run against the whole suite rather
than against the requirement's own file, which is the method note worth keeping.

### FINDING F — PB-SEC-12 clause 1: the Go gate counts the identifier, not the call sites. RECORDED, covered.

`TestPBSEC12_GatedActionsFilterObscuredTouches` scans `src/main` for the string
`filterTouchesWhenObscured` and passes if any file contains it. Deleting **all five**
`SecureWindow.gate(...)` call sites — `PhoneSurface.kt` x3, `SettingsSurface.kt`,
`PairingSurface.kt` — leaves the identifier inside a now-dead `SecureWindow.gate` and the whole
Go PB-SEC-12 suite stays green. The requirement is quantified over *gated actions*; that fence
is quantified over *the module containing a string*.

**It is covered, and by the fence that was unrunnable this morning.**
`PhoneSurfaceControlsTest.every_button_and_switch_on_screen_filters_obscured_touches` walks
`android.R.id.content` and asserts the property of every `Button`/`CompoundButton` actually on
screen, so it catches both the disconnection and a new control added ungated. Recorded rather
than fixed: the two fences are correctly layered, but the Go one reads as covering the clause on
its own and does not.
