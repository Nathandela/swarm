# S18 evidence — app security hardening (PB-SEC-3, 4, 5, 6, 7, 8, 11, 12, 13, 14)

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
