# S12 evidence — the push transport (PB-PUSH-0, 1, 2, 3, 5, 6, 7, 8, 10)

**Commit**: `2cb9b13` — one commit, 33 files, +4461/-54.
**Follow-up in the same area**: `4c24b34` (closes S12's own declared residual — see below).
**Requirements**: 9. **Decisions**: ADR-007 **B19** (the wake key may reach the gateway),
**B20** (the wake payload carries zero key ids and a constant size), recorded in `59864c2`.

> **RECONSTRUCTED**, 2026-07-25, from the commit, the diff and the tests. All 66 S12-owned tests
> were re-run at HEAD.

## Why this slice exists

Push is the **sole background wake path**. ADR-007 B16 decided that backgrounding disconnects and
that no foreground service ships in v1, so a phone that is not in the user's hand is reachable only
by push — which makes the FCM priority decision (S13's `fcm-priority.tsv`) load-bearing rather than
a preference.

And it shipped with **no producer at all**: nothing machine-side called `PushTrigger` outside relay
tests. The only push that fired in the whole system was the relay's presence-timeout sweep.

## What shipped

- **`internal/remotegw/push.go`** — the gateway-side trigger: journal transitions -> a wake sealed
  under the **wake key**, with per-session coalescing.
- **`internal/remote/push/`** — an FCM v1 sender (`fcm.go`, `oauth.go`, `serviceaccount.go`)
  implementing the renamed, transport-neutral `PushSink`/`PushPayload` seam.
- **`internal/remotegw/pushprefs.go` + `internal/protocol/schema`** — a durable,
  machine-authoritative, versioned push preference and the signed `push_prefs` verb that carries it.
- **Relay-side persistence** — push tokens survive a restart, with deletion and revocation
  persisting too.
- **`cmd/swarm-relay/main.go`** — the wiring that makes the sender real.

## Per requirement: what proves it

| Requirement | What proves it |
|---|---|
| PB-PUSH-0 (gateway-side trigger; which transitions, with coalescing; sealed under the wake key) | `TestPBPUSH0_TransitionIntoNeedsInputFiresExactlyOnePush`, `_TransitionIntoFinishedFiresAPush`, `_TransitionIntoWorkingNeverPushes`, `_RepeatedSameGroupIsNotATransition`, `_ReconnectRosterSeedsWithoutPushing`, `_RecordWithNoGroupIsNotATransition`, `_CoalescesRepeatTransitionsWithinTheWindow`, `_CoalescingWindowIsPerSessionNotGlobal`, `_WakeIsSealedUnderTheWakeKeyAndOpaqueToTheContentKey`, `_PushConfigCarriesNoContentKey`, plus the wiring pair `_ServiceWiresTheNotifierIntoTheLiveJournalPath` and `_NotifierPassesThroughTheWrappedSinkContracts` |
| PB-PUSH-1 (rename the seam transport-neutral) | `TestPBPUSH1_PushSeamIsNamedForTheTransportItActuallyCarries`, `_NoExportedAPNsNameSurvivesTheRename` |
| PB-PUSH-2 (an FCM v1 sender) | 10 tests in `internal/remote/push/fcm_test.go`: high-priority data-only send, OAuth acquired once and reused, expired token refreshed, refreshed **before** expiry not after, messaging scope only, 5xx retried then succeeding, retries bounded, 4xx not retried, `UNREGISTERED` -> pruning sentinel, other 404 **not** a pruning signal; plus `_SenderSatisfiesTheRelaySeam` and `_UnregisteredSinkErrorPrunesTheStoredToken` at the relay |
| PB-PUSH-3 (specified payload schema; nothing provider-visible) | `TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize`, `_WakePlaintextIsEmptyAndNamesNothing`, `_WakeHeaderCarriesNoStableEndpointIdentifiers`, `_WakeCarriesAMonotonicReplayCoordinate`, `_WakeSeqDoesNotRestartAfterAGatewayRestart`; sender side `_SenderCarriesTheOpaqueCiphertextAndNothingElse` |
| PB-PUSH-5 (missing/invalid credentials degrade gracefully and loudly) | `TestPBPUSH5_InvalidCredentialsFailLoudlyAtConstruction`, `_ConstructionErrorNamesTheProblem`, `_UnreachableProviderReturnsAnErrorAndNeverPanics`, `_ContextCancellationIsHonoured`, `_SinkFailureNeverFailsTheTrigger`, `_PushFailureNeverFailsTheJournalRecord`, `_NoPusherConfiguredLeavesTheCorePathsUntouched`, `_JournalDeliveryFailureSuppressesTheWake` |
| PB-PUSH-6 (tokens survive a relay restart) | `TestPBPUSH6_PushTokenSurvivesARelayRestart`, `_TokenDeleteAlsoSurvivesARestart`, `_RevokedDeviceTokenIsNotResurrectedByARestart`, `_TokenIsNotStoredInTheClearAlongsideTheCiphertext` |
| PB-PUSH-7 (single token per routing id: decided + pinned) | `TestPBPUSH7_SecondTokenForOneRoutingIDReplacesTheFirst` — the behaviour is *pinned*, not merely documented |
| PB-PUSH-8 (a transport verb for the toggles) | Authorization: `TestPBPUSH8_PushPrefsIsAKnownActionInTheReadClass`, `_PushPrefsIsPermittedAtEveryPairedTier`, `_ForgedOrExpiredPushPrefsIsRefused`, `_DaemonServesThePushPrefsOp`. Gateway: `_PushPrefsIsAuthorizedByTheDaemonBeforeItIsApplied`, `_DaemonRefusalLeavesThePreferenceUnchanged`, `_AbsentPreferenceCustodyRefusesTheVerb`, `_PushPrefsCommandWithNoBodyIsRefused`. Effect: `_DisabledCategorySendsNoPushAtAll`, `_CategoriesAreIndependent`, `_UnreadablePreferenceFailsClosed`, `_AbsentPreferenceSourceFailsClosed` |
| PB-PUSH-10 (durable where delivery is decided; acknowledged, versioned) | `TestPBPUSH10_AcknowledgementIsSealedOnlyAfterThePreferenceIsDurable`, `_AcknowledgementIsAttributableToTheCommand`, `_StaleVersionNeverOverwritesANewerPreference`, `_PreferenceSurvivesAProcessRestart`, `_NeverConfiguredDefaultsToBothCategoriesEnabled`, `_CorruptPreferenceIsNeverSilentlyTheEnabledDefault`, `_PreferenceIsStoredAsInspectableState`, and the requirement's literal criterion `_DisabledPreferenceStillSuppressesAtTheSenderAfterARestart` |

## The two ADR decisions this slice forced

**B19 — the wake key may reach `internal/remotegw`.** PB-PUSH-0 could not be implemented without it:
`gatewayParams` carried only `ContentKey`, and `WakeKey` appeared nowhere in `internal/remotegw/`,
`cmd/swarm-remote/` or `internal/remote/relay/` outside tests. The decision rests on a fact about
the process rather than about the package: **the sidecar already materialises the wake key via
`machineid` and merely dropped it**, so the crossing widens the *package inventory*, not the
*process exposure*. `TestPBPUSH0_PushConfigCarriesNoContentKey` fences the other half — the trigger
gets the wake key and **not** the content key.

**B20 — the wake carries zero key ids and a constant size.** This is the one that changes a
security claim. `crypto.Envelope.Marshal` emits a **62-byte cleartext header** carrying
`RecipientKeyID` and `SenderKeyID` (8 bytes each) plus a monotonic seq, and the obvious
implementation puts the whole marshalled envelope in the push payload. Reused verbatim, that shows
the push provider **a stable machine/device pair for the life of the epoch** — so PB-PUSH-3's
"token, timing, size" claim was **false for the obvious implementation**, and D11 was contradicted.

What shipped instead is a fixed-size, identifier-free wake: `PushWakeEnvelopeSize = 78`
(`internal/remotegw/push.go:29`), asserted **invariant** rather than merely small.
`TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize` deliberately uses a loud session name —
`build-box-17.local/refactor-the-auth-middleware` — so a leak has something recognisable to leak,
and then asserts that a 70-character session and a 3-character session produce **the same number of
bytes**: size itself is a disclosure channel, and it is closed by construction.

## The trigger's rules, and why each exists

- Fires on transitions **into** `needs_input` and into `ready_for_review`/`completed`.
- **Never on `working`**, never on a repeat of the same group, and **never from a reconnect
  roster** — otherwise a gateway restart with *n* idle sessions fires *n* pushes for events from
  hours ago, **repeatedly under a restart loop**. `_ReconnectRosterSeedsWithoutPushing` is that
  fence.
- Coalescing is **30 s per SESSION, not global**. A single shared timestamp passes every other test
  while **silently dropping the second of two agents that stop at the same moment** —
  `_CoalescingWindowIsPerSessionNotGlobal` is the standing "what if there is more than one?"
  question, asked and answered.

## The class-(v) hole this slice closed, and it is the headline

**`internal/remote/push` had ZERO production callers.** A fully-tested FCM sender that no binary
installs would have shipped as *a push system that does not exist in production*, with every test
green — the project's standing class (v) in its purest form, and no test inside the package could
have caught it.

`cmd/swarm-relay/main.go` now installs it (verified at HEAD: `push.LoadServiceAccount` ->
`push.NewFCM` -> `relay.WithPushSink`), **failing closed on a configured-but-broken credential**,
and that wiring is itself covered rather than assumed. The seam's own comment names the reason:
"the seam that makes `internal/remote/push` more than a library nobody calls".

## Failing-first evidence (GG-5)

There is **no preserved RED transcript** for S12 — tests and implementation landed in one commit.
What is durable:

- **Two requirement texts were falsified before the code was written**, and both falsifications are
  in the spec and the ADR rather than in anyone's memory: PB-PUSH-3's "token, timing, size" claim
  (B20, above) and PB-PUSH-0's unnamed key crossing (B19). Both are checkable against
  `crypto.Envelope.Marshal` and against `gatewayParams` at `2cb9b13^`.
- **The zero-production-caller finding is mechanically reproducible** at the parent commit:
  `grep -rn 'remote/push' --include='*.go' . | grep -v _test.go` returns nothing outside the
  package at `2cb9b13^`, and four hits at HEAD.
- **Negative controls are in the tests**: `_TransitionIntoWorkingNeverPushes`,
  `_RepeatedSameGroupIsNotATransition`, `_RecordWithNoGroupIsNotATransition`,
  `_ReconnectRosterSeedsWithoutPushing`, `_OtherNotFoundIsNotAPruningSignal`,
  `_ClientErrorsAreNotRetried`, `_UnreadablePreferenceFailsClosed`,
  `_AbsentPreferenceSourceFailsClosed`, `_CorruptPreferenceIsNeverSilentlyTheEnabledDefault`. Each
  is a case where the *permissive* behaviour is the defect.
- The commit states **"No test was weakened."** Checked: the six one-line edits to existing relay
  and transport test files in the diff are harness signature changes (`harness_test.go`,
  `bounds_test.go`, `harden_test.go`, `metadata_test.go`, `presence_test.go`, `store_test.go`), not
  assertion changes.

## S12's declared residual — and it was CLOSED by `4c24b34`

S12 shipped with this stated: *"push delivery is synchronous on the connection request loop,
bounded to 2 s — deliberately tighter than the gateway's own 5 s push timeout so the inner bound
fires first. A push_trigger can therefore delay the next frame on that connection by up to 2 s."*

`4c24b34` closed it, and the reasoning is worth preserving because the **first proposed fix was
wrong and was retracted on evidence**:

- The synchronous delivery was harmless only while every configured sink was a test double
  answering instantly. A real FCM sender retries a 5xx over its own timeouts, and that holds the
  **machine's** request loop — the same one the gateway uses for `mailbox_read`/`mailbox_wait` to
  collect phone input. The realistic shape is multi-session: one agent goes idle and fires a wake
  **while the user is typing into a different session**.
- The proposal was to keep the first attempt synchronous and background only retries, assuming an
  `UNREGISTERED` verdict always arrives on attempt one. **That assumption was wrong**: a 503
  followed by a dead token, or an auth blip then `UNREGISTERED`, both surface it later — so pruning
  would have silently stopped for exactly those cases, with the symptom (quota burned against dead
  handsets) invisible from the relay.
- What shipped: delivery on its own goroutine with the caller waiting up to **1 s** for a verdict,
  so every fast verdict — including every `UNREGISTERED`, which is non-retryable — still prunes
  before the caller resumes. Worst-case stall **2 s -> 1 s**; total budget rises to 10 s so retries
  complete instead of being guillotined. Delivery no longer rides the caller's context: a machine
  disconnecting must not abandon a push aimed at a **different** device, least of all its prune.
- **Recorded honestly in that commit**: the fire-and-forget mutation was initially *not* caught,
  because the existing prune test has an instant sink and passes on timing luck. The new test
  (`TestPushDelivery_CallerWaitsForTheVerdictSoThePruneIsNotARace`) uses a sink deliberately slower
  than the reply path. And removing the `Close` join produces **no deterministic failure**, because
  cancellation fires first and the cancelled sink never touches the store — the real cost is a lost
  prune, not a crash, so the guard is kept "as correct and cheap, with no test rather than a flaky
  one".

Re-run at HEAD: `TestPushDelivery_SlowProviderDoesNotHoldTheRequestLoop`,
`_UnregisteredVerdictAfterTheWaitStillPrunes`, `_CloseJoinsInFlightDeliveries`,
`_CallerWaitsForTheVerdictSoThePruneIsNotARace` — **all PASS**.

## Gates, re-run at HEAD 2026-07-25

`go test -count=1 -run 'PBPUSH|WiresThePushPath' ./internal/remotegw/ ./internal/remote/push/
./internal/remote/relay/ ./internal/skeleton/ ./cmd/swarm-remote/` — **66 tests, all PASS, five
packages ok, zero failures.**

| Package | Tests | Result |
|---|---|---|
| `internal/remotegw` | 36 (25 trigger, 11 prefs) | ok 1.30 s |
| `internal/remote/push` | 16 (FCM + OAuth + service account) | ok 2.96 s |
| `internal/remote/relay` | 9 (seam, token persistence, pruning) | ok 2.24 s |
| `internal/skeleton` | 4 (`push_prefs` authorization) | ok 4.25 s |
| `cmd/swarm-remote` | 1 (`_WiresThePushPath`) | ok 2.30 s |

## Accepted residuals

- **PB-PUSH-3's replay window is a field, not a mechanism — and this is an UNMET requirement in
  shipped work.** §6.0 requires a 10-minute push TTL / replay window "with the replay coordinate
  persisted per PB-STATE-1", against PB-PUSH-3, which S12 shipped. The coordinate **is** persisted
  (`State.WakeReplay`) and `TestPBPUSH3_WakeCarriesAMonotonicReplayCoordinate` /
  `_WakeSeqDoesNotRestartAfterAGatewayRestart` cover the **sender** half — but outside `state.go`'s
  own persist/merge/load plumbing, **nothing in `internal/` or `mobile/` ever writes it**, so a wake
  envelope replayed by the relay (the declared adversary, and the party that necessarily handles
  every wake) **is not detected**. Bounded, not severe: the payload is content-free and a constant
  78 bytes, so a replay costs a spurious reconnect and battery, not disclosure. **Owner: S17**,
  which owns the phone's push client — the receiver is where the check belongs. Found by the S15
  RED author, verified independently.
- **PB-PUSH-4 and PB-PUSH-9 are not in this slice** and are not claimed: the app-side notification
  rendering (S17) and the client-side FCM token lifecycle. A facade method can exist while no
  Android code calls it, which is why PB-PUSH-9 explicitly demands an end-to-end test.
- **The relay now holds a provider-visible device identifier at rest, in an untrusted store.** It
  cannot be encrypted — the relay must hand the token to FCM. The mitigation is **auditability**: a
  named bucket, so an operator finds every device identifier in one place rather than discovering
  one smuggled into the item log. `TestPBPUSH6_TokenIsNotStoredInTheClearAlongsideTheCiphertext` is
  the fence on where it may live.
- **Nothing here has ever run against Google.** Every FCM request in the suite goes to an
  `httptest.Server` on loopback, and the relay wiring carries an explicit note saying so. PB-E2E-5
  (real FCM, real Doze, real handset) is the deferred gate under §13 and is untouched by this slice.
- **The revoke path drops the token inside `revokeAndPurge`'s existing transaction**, deliberately,
  so a crash between two writes cannot resurrect a token the owner killed.
  `_RevokedDeviceTokenIsNotResurrectedByARestart` is the fence.
- **`App.SetPushPreference` still records a LOCAL refusal on the phone side.** S8 owns the facade
  *surface* and S12 shipped the *verb*; the wiring between them is S16's, and S16's RED tests for
  exactly this are in the working tree now ("has been stale since S12 shipped `ActionPushPrefs`").
  Until that lands, the user's toggle is a boolean the machine has never heard of.
