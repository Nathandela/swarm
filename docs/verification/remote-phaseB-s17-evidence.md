# S17 evidence — the phone's push client (PB-PUSH-4, PB-PUSH-9)

> **PARTLY SUPERSEDED — READ THIS FIRST (added 2026-07-30).** This file predates both the defect
> that made PB-PUSH-9 NOT MET and the fix that restored it, so it does not contain the evidence
> that is load-bearing for the requirement's CURRENT status.
>
> ADR-007 **B61** found that a party could consent to ITSELF at the relay. One self-edge makes
> `grantsAnyone()` permanently true, which disabled the push-token purge in `revokeAndPurge` for
> **any** revoke by **any** party — so "deletion on revoke/disable" was dead while this file read
> as proof that it worked. The fix refuses self-consent, and the test that demonstrates it is
> `TestB61_TheOwnersRevokeForgetsThePushTokenDespiteASelfConsent` in
> `internal/remote/relay/b61_selfconsent_test.go`, not anything below.
>
> The body is left unedited: it is signed-off evidence for a closed slice and accurate as of the
> commit that produced it. Read it as a record of what was true then. See ADR-007 B67(1) for why
> this class of staleness was invisible — the traceability index checks that an evidence file
> EXISTS, never that it is current.

**RECONSTRUCTED, not written by the implementer.** S17 shipped without an evidence file; its
only durable record was a commit message. This file was written on 2026-07-26 at HEAD
`21307d5`, from the commits and from running the tests. Every claim below was executed in this
worktree, and every claim that is about *a guard's ability to fail* was mutation-checked — the
mutation applied, the result recorded, the mutation reverted. `git status` is clean.

**The commit ids, corrected.** The reconstruction brief named `59fcbd5` as the shipping commit.
It is not: `59fcbd5` is *"docs: correct my Kotlin test count -- 205, not 328"*. S17 and S18b
shipped together in **`3b6694f`**, and were remediated in **`a1110dd`** (*"Make B22's safety
argument true (B24) and de-race the PB-STATE-10 fences"*). This file describes the code **as it
is now**, i.e. after `a1110dd`.

**Nothing here claims a physical-handset property.** PB-E2E-5 stays deferred: no real
biometrics, camera, FCM delivery, Doze or hardware attestation. Every Android-side assertion is
about *what the app asks the platform for*, never about what the platform then does.

## What is asserted, and what could not be run

| | |
| --- | --- |
| Go tests owned by S17 | **50 functions**, all green (counts per file below) |
| Kotlin tests owned by S17 | `WakeNotificationTest`, **7 `@Test` methods** — **NOT RUN**, see below |
| Mutations applied and reverted | 8, each named at the claim it supports |

**THE KOTLIN HALF WAS NOT EXECUTED AND IS NOT CLAIMED AS GREEN.** There is no JDK in this
environment (`/usr/libexec/java_home` reports *"Unable to locate a Java Runtime"*, and
`./gradlew test` fails before Gradle starts). PB-PUSH-4's stated acceptance is *"Robolectric
test: locked -> generic alert only; authenticated -> content rendered"*, and that test exists —
`android/app/src/test/kotlin/dev/swarm/phone/push/WakeNotificationTest.kt`, 7 `@Test` methods,
read line by line below — but its **passing is asserted by the commit, not measured here**. It
is listed in the unsubstantiated table at the end rather than folded into the green count.

Go test functions by file:

| file | functions |
| --- | --- |
| `android/gate/s17_pushclient_test.go` | 13 (one with 3 subtests) |
| `internal/phonecore/s17_wakereplay_test.go` | 14 (one with 7 subtests) |
| `mobile/conformance/s17_pushwake_test.go` | 11 (one with 4 subtests) |
| `mobile/conformance/s17_tokenlifecycle_test.go` | 9 |
| `mobile/s17_screencoverage_test.go` | 3 |

```
go test ./android/gate/        -run TestS17   ok  0.704s   (13/13 PASS)
go test ./internal/phonecore/  -run TestS17   ok  1.501s   (14/14 PASS)
go test ./mobile/conformance/  -run TestS17   ok  10.371s  (20/20 PASS)
go test ./mobile/             -run TestS17    ok  1.767s   (3/3 PASS)
```

## PB-PUSH-9 — the client-side token lifecycle

The criterion is **amended**, and the amendment is the most load-bearing thing in this slice.
Current text, verbatim from `docs/specifications/remote-phaseB-requirements.md`:

> End-to-end test with a fake FCM and a real relay: rotate the token and assert delivery still
> works; restart the relay and assert re-registration restores it. **AMENDED 2026-07-25 — the
> second half was VACUOUS as written.** S12 made relay push tokens persistent, so a restart
> restores delivery with the phone doing nothing at all: the criterion could not fail, and would
> have been reported as evidence for a re-registration path that was never exercised. The
> restart must be against an **EMPTY token store**, which is the only configuration in which
> re-registration is the thing being measured. Standing class (i), found by the S17 RED author
> against this row rather than against the code.

**The amendment is demonstrated, not accepted on its word.** Two mutations, same production
edit — `mobile/relay.go` `onConnected`'s `TokenRegister` call disabled, i.e. re-registration
deleted outright:

```
re-registration DELETED + relay restarted with its store KEPT   (criterion AS WRITTEN)
  -> TestS17_ReRegistrationRestoresDeliveryAfterARelayRestart    ok 1.874s   PASSES

re-registration DELETED + relay restarted with an EMPTY store   (criterion AS AMENDED)
  -> TestS17_ReRegistrationRestoresDeliveryAfterARelayRestart    FAIL 5.63s
```

So the row as originally written **could not fail against a phone with no re-registration path
at all**. The shipped test uses `r.RestartRelay(false)`, which repoints `relayCfg.DBPath` at a
fresh file (`mobile/conformance/s17_pushrig_test.go:305`) — the relay has genuinely never heard
of the device.

**What the rig is.** Real `relay.Server` with its real persistent token store, the real
`internal/remote/push.FCM` sender against an `httptest.Server` speaking the FCM v1 protocol,
the real gateway-side `remotegw.PushNotifier` over a durable seq file, and the real
`swarmmobile.App` over a real state directory. Nothing on the Go side is simulated except the
provider endpoint.

### The five lifecycle elements the requirement enumerates

| element | where it is | test |
| --- | --- | --- |
| initial `getToken` | `PushTokens.requestInitialToken`, called from `SwarmApplication.onCreate` | `TestS17_ProductionKotlinAsksFirebaseForATokenAtLeastOnce` (source fact) |
| `onNewToken` rotation | `SwarmMessagingService.onNewToken` -> `PushTokens.register` -> `App.registerPushToken` | `TestS17_OnNewTokenReachesTheFacadeRegistration`, `TestS17_ARotatedTokenStillReceivesDelivery` |
| re-registration on authenticated reconnect | `mobile/relay.go:285` `onConnected` | `TestS17_ReRegistrationRestoresDeliveryAfterARelayRestart` (empty store) |
| deletion on revoke/disable | `App.dropPushToken`, shared by `DeletePushToken` and `RevokeThisDevice` | `TestS17_DeletingTheTokenStopsDeliveryAtTheProvider`, `TestS17_RevokingThisDeviceDeletesItsPushToken` |
| process death / app upgrade | `State.PushToken`, durable and wake-tier (PB-STATE-9) | `TestS17_ARegisteredTokenSurvivesAProcessDeath`, `TestS17_ATokenIsReadableAndReRegistrableWithTheContentTierLocked` |

**"Deletion on disable" has no production Android caller, and this is a real gap.** The code
says so itself (`PushTokens.kt:130`): `PushTokens.disable` exists and nothing calls it —
`grep -rn "PushTokens\." android/app/src/main/kotlin/` returns exactly two call sites,
`requestInitialToken` and `register`. The *revoke* half is genuinely wired (`PhoneSurface.kt:61`
-> `app.revokeThisDevice()` -> `dropPushToken`), so the token does get deleted on the path that
matters most. But the gate that is supposed to catch this,
`TestS17_ProductionKotlinDeletesTheTokenOnRevokeOrDisable`, only requires the string
`deletePushToken(` to appear anywhere in production Kotlin — **it passes on a function nothing
calls**, which is PB-PUSH-9's own warning ("a façade method can exist while no Android code ever
calls it") reproduced one level up, in the fence written to catch it. Recorded as residual 1.

### Three shipped defects in the token path, each fixed and each fenced

These were pre-existing bugs in S7/S12 code, not new work, and each has a test that fails
without the fix (verified by the fix's own shape: `RegisterPushToken` used to take `a.conn()` as
its first act):

1. **A token rotated while disconnected was lost.** `RegisterPushToken` now persists to
   `State.PushToken` *before* dialling and returns `nil` on a missing connection
   (`mobile/app.go:1017-1036`). Fenced by `TestS17_ATokenRotatedWithNoConnectionIsNotLost`,
   which drives an `App` that was never `Start`ed.
2. **A deletion cleared local state whether or not the relay was told.** `dropPushToken` makes
   durable state authoritative and lets `onConnected` reconcile
   (`mobile/app.go:1064-1075`). Fenced by `TestS17_ADeletionIssuedWithNoConnectionStillReachesTheRelay`
   and `TestS17_ReconnectingAfterADeletionDoesNotResurrectTheToken`.
3. **Revoking did not delete the token.** `RevokeThisDevice` now calls `dropPushToken` first
   (`mobile/commands.go:132`). Fenced by `TestS17_RevokingThisDeviceDeletesItsPushToken`.

Three tests in `s17_tokenlifecycle_test.go` carry the header **"PASSES TODAY, AND THAT IS NOT
COVERAGE"** in the source. They are counted here as green and explicitly **not** counted as
earned: `ARotatedTokenStillReceivesDelivery`, `ReRegistrationRestoresDeliveryAfterARelayRestart`
(the empty-store variant is what it adds), `ARegisteredTokenSurvivesAProcessDeath`.

## PB-PUSH-4 — the content-free notification

> The app receives a push and renders a **content-free** notification unless the user has
> authenticated; it never decrypts session content with a locked device (PB-KEY-2). Lock-screen
> redaction and notification-channel privacy are set.
> *Criterion:* Robolectric test: locked -> generic alert only; authenticated -> content rendered.

### The defect is the FETCH, not the string, and that is where the fences are

The payload cannot leak: it is a constant 78 bytes over an empty plaintext with zeroed key ids
(ADR-007 B20). So the only route from a session to a lock screen is an app that goes and gets
content. Three independent fences, at three layers:

- **Custody seam.** `TestS17_HandlingAWakeCostsZeroContentUnwraps` counts content-tier KEK
  unwraps across one `HandlePushWake` on a locked phone and requires **0**. It is the assertion
  that fails for an app that reads the roster, is *refused*, and renders the generic string
  anyway — which the requirement's own stated acceptance cannot see. It carries its own
  non-vacuity arm (a wake that did nothing would also cost zero unwraps, so the replay refusal
  is checked to prove the wake was processed).
- **Source reachability.** `TestS17_TheWakeCallbackReachesNoContentVerb` walks the Kotlin call
  graph three hops from `onMessageReceived` and forbids nine content verbs.
- **Exported surface.** `mobile/s17_screencoverage_test.go` pins `WakeAlert`'s field set closed
  (`Text`, `ContentReady`) so a third field cannot be added and then filled by a fetch.

`HandlePushWake` answers `ContentReady` from state the process already holds
(`core.State().Keys.ContentKey != zero`, `mobile/pushwake.go:127`) rather than asking custody —
which is what makes the zero-unwrap count structural rather than incidental.

### "Authenticated -> content rendered" is met in a WEAKENED form, and it is weakened deliberately

**This is a criterion mismatch and it is reported rather than smoothed over.** No session
content is rendered on the authenticated branch, ever. `WakeNotifications.bodyFor` returns the
supplied constant plus a *second* constant string resource
(`WakeNotifications.kt:98-99`), and the Robolectric test asserts only that the two notifications
are **distinguishable**:

```kotlin
lockedText != unlockedText || locked.actions?.size != unlocked.actions?.size
```

The test file states the narrowing in its own doc comment ("What 'content rendered' means here
is deliberately narrow"). The argument is sound — the wake path has no content to render, and
fetching some is the defect the requirement exists to stop — but the requirement's criterion
says "content rendered" and the code renders a constant. **This wording should be amended to
match, or the mismatch will read as verified.** Flagged for the spec owner; I did not edit
`docs/specifications/`.

### Lock-screen redaction and channel privacy

Asserted as source configuration (`TestS17_TheNotificationAndItsChannelAreLockScreenSecret`) and
as Robolectric policy (`WakeNotificationTest`, not run here). Both the **channel**
(`setLockscreenVisibility(VISIBILITY_SECRET)`, `WakeNotifications.kt:64`) and the
**notification** (`setVisibility(VISIBILITY_SECRET)`, line 89) are set — the requirement names
them separately because they fail separately. `VISIBILITY_PUBLIC` is forbidden anywhere in
production Kotlin. Channel importance is `IMPORTANCE_HIGH`, which is not a preference: a
high-priority FCM message delivered into a low-importance channel is a wake that arrives and is
not shown.

`TestS17_TheNotificationTextIsNotBuiltByInterpolation` opens with a **non-vacuity fatal** —
zero notification setters found is a failure, not a pass — because "no interpolated text" is
satisfied perfectly by an app that builds no notification at all.

## PB-PUSH-3's receiver half: the ordering in `AcceptWake`

Not S17's requirement, but S17 is what implemented it (`internal/phonecore/wake.go`), and the
ordering is the security property the commit message leads with. The order is: parse and require
type `0x02` -> **keyless** -> **epoch** -> **open under the wake key** -> seq -> expiry ->
persist.

**Both orderings are fenced, and both fences were shown failing.**

```
MUTATION: advance the replay coordinate BEFORE crypto.OpenWake
  -> TestS17_AForgedWakeNeverAdvancesTheCoordinate FAILS:
     "an UNAUTHENTICATED envelope moved the replay coordinate to 4611686018427387904.
      The relay can send this; one packet then makes every genuine wake look like a
      replay for the life of the epoch"
     (9 further tests in the file fail with it, several on their own non-vacuity arms)

MUTATION: epoch check placed above the keyless check
  -> TestS17_AKeylessPhoneReportsNoWakeKeyRatherThanAnEpochMismatch FAILS:
     "a wake at a phone with NO epoch wake key was refused with `push wake names epoch 11,
      not 0`, want ErrNoWakeKey"
```

The second is a **later remediation** (`a1110dd`): the keyless-before-epoch order shipped
correct in `3b6694f` but unfenced, and a reviewer swapped it without turning the S17 suite red.
`AcceptWake`'s comment now also states the order's *actual reach* rather than overstating it —
the two orders are indistinguishable for a paired phone, and differ only at a phone holding
epoch 0, which is reachable because `requestInitialToken` runs from `Application.onCreate`
before any pairing.

## The three gate defects, measured

The commit message claims three defects in S17's own RED gate file, from one root cause: gomobile
lowercases the first letter for the Java binding, so no correct Kotlin call site can contain the
Go-cased name. **All three claims hold, and the counts are right.** Each was reproduced by
reverting the fix and running the suite against today's *correct* production Kotlin.

**Defect 1 — five assertions unsatisfiable.** `s17NamesVerb` reverted to matching the Go casing
only:

```
go test ./android/gate/ -run TestS17   ->  6 test functions FAIL
  TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers   <- added later, by a1110dd
  TestS17_OnNewTokenReachesTheFacadeRegistration
  TestS17_OnMessageReceivedReachesTheFacadeWakeHandler
  TestS17_ProductionKotlinAsksFirebaseForATokenAtLeastOnce
  TestS17_ProductionKotlinDeletesTheTokenOnRevokeOrDisable
  TestS17_TheAppTheServiceUsesIsTheProductionOne
```

Excluding the fence that did not exist when the defect was found: **five**, exactly as claimed.

**Defect 2 — the body scanner off by one.** `s17BodyAt`'s `depth` reverted from `1` to `0`
(the parameter list is already open at the offset the regexp hands it, so the outer `)` sent
depth to -1 and the break could never hold):

```
go test ./android/gate/ -run TestS17   ->  4 FAIL; 3 excluding the later-added walk fence
```

The count matches "silenced three more". **The word "silenced" does not.** In today's file those
three fail *loudly* — `s17Reachable` returns `ok=false` and the tests `t.Fatalf` with "there is
no wake callback to walk" — because each carries an explicit "FAILS rather than skips" arm. So
the defect is real and the arithmetic is right; the failure mode described is not what the
current file does. Recorded rather than repeated.

**Defect 3 — the forbidden-verb guard could not fire at all.** This is the one carrying
PB-PUSH-4's security property, and it is the sharpest measurement in this file. The *same*
production defect was injected twice — a real content fetch, `startup.app.roster()`, added to
`SwarmMessagingService.renderWake`:

```
with the CURRENT guard   -> TestS17_TheWakeCallbackReachesNoContentVerb FAIL
                            "the wake callback reaches 1 content verb(s)"
with the SHIPPED guard   -> ok  1.635s        <- the fetch is invisible
   (s17NamesVerb reverted to Go casing only, everything else identical)
```

A guard that cannot fail is worth less than no guard, because it is reported as coverage. The
commit's claim that this "was proven fixed by making it fail" is substantiated.

## The later remediation (`a1110dd`)

**The expression-bodied-Kotlin bypass.** `s17BodyAt` looked only for an opening brace, so
`private fun sessionsFor(app: App) = app.roster()` yielded no body, never entered the body map,
and the reachability walk stopped at the call site — silently satisfying every exclusion
assertion in the file. Closed, and fenced by
`TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers`, which drives the reader against
**synthetic** Kotlin with a braced control, because against the production tree a walk that
understands nothing and a codebase that does nothing wrong are indistinguishable.

```
MUTATION: remove the expression-body branch from s17BodyAt
  -> TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers/expression_body FAIL
```

**Precision, because the subtest results are not uniform**: only the one-liner subtest fails
under that mutation. The two-line form (`= \n when { ... }`) still resolves, because the
brace-matcher happens to find the `when` block's `{`. The fence catches the removal; it catches
it through one of its three cases, not three.

## Gate results

```
go build ./...        clean
go vet ./...          clean
go test ./...         two known flakes, neither S17's, both on the do-not-chase list:
                        TestS6B_GatewayInputLatencyIsNotPollGated   (internal/remotegw)
                        TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT (mobile/conformance)
                      every other package green
golangci-lint run ./...   25 findings, NONE in a file S17 owns
./gradlew test        COULD NOT RUN — no JDK in this environment
```

`golangci-lint` findings are `errcheck`/`ineffassign`/`gosimple` in test files plus four in
`internal/remotegw/outbox.go`, `internal/remote/relay/store.go` (two functions orphaned at
`26a47a4`, Phase A, long before this slice) and `internal/remote/device/capability.go`.
implementation-goals.md GG-4 requires all four gates green before an epic closes, so these are
owed by someone — they are not S17's.

## Claims I could NOT substantiate

| claim | source | status |
| --- | --- | --- |
| "Kotlin: 328 tests across 47 classes, zero failures" | `3b6694f` commit message | **FALSE, and already retracted** by `59fcbd5` (205 across 31; inflated by summing stale Gradle result XML). Today's tree: **208 `@Test` methods across 31 files**, counted from source. |
| "zero failures" for the Kotlin suite | `3b6694f`, `59fcbd5` | **UNVERIFIED HERE.** No JDK; `./gradlew test` cannot start. Carried forward, not re-asserted. |
| PB-PUSH-4's Robolectric acceptance (`WakeNotificationTest`, 7 tests) | requirement criterion | **NOT RUN.** The file exists and was read; its assertions are described above from source. |
| "authenticated -> content rendered" | PB-PUSH-4 criterion | **MET IN A WEAKENED FORM.** No content is rendered on either branch; the test asserts distinguishability of two constants. Deliberate and argued, but the criterion's wording does not match the code. |
| "deletion on revoke/**disable**" fully wired in Android | PB-PUSH-9 | **HALF TRUE.** Revoke is wired; `PushTokens.disable` has no production caller, and the gate for it passes on an uncalled function. |
| a failing-first (RED) run for S17 | GG-5 | **NOT IN THE HISTORY.** `git log --follow` shows the gate file, `wake.go` and the conformance files first appearing in `3b6694f` — the RED file and the GREEN implementation landed in the same commit. The vacuous-pass probes documented in the test headers are the only failing-first record, and they are the author's own report. |

## Residuals

1. **`PushTokens.disable` has no caller, and its gate cannot see that.**
   `TestS17_ProductionKotlinDeletesTheTokenOnRevokeOrDisable` requires only that
   `deletePushToken(` appear in production Kotlin. PB-APP-7's settings switches are where the
   call belongs. Until then, "deletion on disable" is a method with no caller — the exact class
   PB-PUSH-9's text warns about, in the fence written to catch it.
2. **The interpolation fence sees only direct setter arguments.** Text built in a helper and
   passed by name (`setContentText(wakeLine(session))`) is invisible to it. Closing that needs
   dataflow rather than brace matching, and a half-done version would report as coverage. It is
   bounded from above by the reachability guard, which forbids the content read that would
   supply the text.
3. **`TestS17_TheAppTheServiceUsesIsTheProductionOne` carries a stale doc comment.** It says
   "THIS IS EXPECTED TO FAIL … there is NO production wiring on the Android side at all". That
   is no longer true — `PhoneRuntime.kt:110` calls `Swarmmobile.newApp(config,
   KeystoreKeyCustody(store))` and the test passes on that real call, verified with comments
   stripped. The comment, not the test, is out of date.
4. **`WakeNotifications.build`'s doc comment is stale** in the same way: "this app declares no
   Activity yet". S18 shipped `PhoneActivity`. The reasoning it supports (no notification action,
   because a tap would drive a decrypt on a locked handset) still stands on its own.
5. **Nothing here claims a physical-handset property.** `VISIBILITY_SECRET`, channel importance,
   `POST_NOTIFICATIONS` and the Firebase initialisation gap are all asserted as *what the app
   asks for*. `TestS17_TheFirebaseInitialisationGapIsRecordedRatherThanClaimed` is where the
   boundary is written down: there is no `google-services.json` and no Google account in this
   project, so FCM never actually calls `onNewToken` here. PB-E2E-5 stays deferred.

## Derivation

**MACHINE-READABLE** (ADR-007 B129). `DERIVED` means the fence was made to FAIL ON PURPOSE and
restored; a `DERIVED` row naming no mutation is malformed and counted NOT DERIVED.

**Scope stated before the verdicts.** Both rows have a Kotlin/Robolectric half that **could not
be run in this tranche**: there is no Java runtime on this host (`./gradlew --version` reports
*"Unable to locate a Java Runtime"*), so `WakeNotificationTest` and every other JVM assertion is
unexercised — the same wall S13 hit on PB-RUN-2/-5. That is NOT the PB-E2E-5 hardware deferral
and is not being reclassified as one: PB-E2E-5 (real FCM delivery, real Doze, real handset
attestation) stays DEFERRED and nothing below touches it. What WAS broken on purpose is
everything reachable from Go: the `android/gate` source fences over production Kotlin, and the
`mobile/conformance` rig with a real relay, the real FCM v1 sender and a fake FCM endpoint.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-PUSH-4 | DERIVED | three, all caught, covering both halves of the amended criterion. **The unreachability half** (the one the amendment calls "what makes it safe"): a `PhoneRuntime.facade()?.roster()` call added to `SwarmMessagingService.onMessageReceived` -> `TestS17_TheWakeCallbackReachesNoContentVerb` fails naming the verb — the FETCH is the defect, not the string. **The distinguishability half**: `HandlePushWake`'s `ContentReady` forced true -> `TestS17_ALockedDeviceIsToldContentIsUnavailable` fails ("that is the flag the app renders session content on"). **Non-vacuity**: `AcceptWake`'s refusal bypassed so any input renders -> four conformance tests fail on their own NON-VACUITY assertions rather than on the alert's contents. Unexercised: the 7 Robolectric assertions in `WakeNotificationTest` (no JVM, see scope above) |
| PB-PUSH-9 | DERIVED | three, all caught, including the criterion's own end-to-end half. `onNewToken` emptied so rotation never reaches the facade -> `TestS17_OnNewTokenReachesTheFacadeRegistration`; `PushTokens.disable` no longer calling `App.DeletePushToken` -> `_ProductionKotlinDeletesTheTokenOnRevokeOrDisable`; and the one that matters most, **`App.onConnected`'s token arm removed** so nothing re-registers on an authenticated reconnect -> `TestS17_ReRegistrationRestoresDeliveryAfterARelayRestart` against a relay with an EMPTY token store (the AMENDED criterion, the only configuration in which re-registration is the thing measured) plus `_ATokenRotatedWithNoConnectionIsNotLost`, `_ARegisteredTokenSurvivesAProcessDeath` and `_ATokenIsReadableAndReRegistrableWithTheContentTierLocked`. Unexercised: nothing on the JVM was needed for these; the recorded residual that `PushTokens.disable` has no production caller is unchanged by this tranche |
