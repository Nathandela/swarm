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

## Derivation

**MACHINE-READABLE** (ADR-007 B129). `scripts/phaseb-traceability.py` reads this section for the
traceability table's DERIVATION column and `internal/verify/phaseb_derivation_test.go` fences that
it does. `DERIVED` means somebody made this row's fence FAIL ON PURPOSE and restored it — not that
a test exists, not that the slice shipped. A `DERIVED` row naming no mutation is malformed and
counted NOT DERIVED. It is ORTHOGONAL to Status: PB-PUSH-3 below is NOT MET and derived.

Every mutation is to the CONNECTION in production code (B113), never to a constant a test
transcribes. All reverted; the packages are green at HEAD.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-PUSH-0 | DERIVED | eight, all caught. Trigger selection: `isWakeWorthy` widened to include `GroupWorking` -> `TestPBPUSH0_TransitionIntoWorkingNeverPushes`; `isTransition` always true -> three tests; `Snapshot` stops seeding `lastGroup` -> `_ReconnectRosterSeedsWithoutPushing`; `claimWindow`'s window disabled -> `_CoalescesRepeatTransitionsWithinTheWindow`. Key custody: a `crypto.ContentKey` field added to `PushConfig` -> `_PushConfigCarriesNoContentKey` (whose positive control requiring a `WakeKey` was verified present, so an empty struct cannot pass); loud key ids restored to the wake header -> `PBPUSH3_WakeHeaderCarriesNoStableEndpointIdentifiers`. Production wiring: `NewService`'s `PushTriggerer` type-assert disabled, and separately the notifier unhooked from the journal path -> `_ServiceWiresTheNotifierIntoTheLiveJournalPath` names each cause distinctly |
| PB-PUSH-1 | DERIVED | `type APNsSink = PushSink` appended to `internal/remote/relay/push.go` — the alias the requirement calls a documented landmine — -> `TestPBPUSH1_NoExportedAPNsNameSurvivesTheRename` fails naming the offender. This is the mutation the fence was written for: the alias keeps every call site compiling and every Phase A test green, so only a declaration scan distinguishes a rename from a second name |
| PB-PUSH-2 | DERIVED | three, all caught. `classify` rekeyed to prune on any 404 instead of the structured `UNREGISTERED` errorCode — the misconfiguration-prunes-the-fleet mistake its own comment warns about — -> `TestPBPUSH2_OtherNotFoundIsNotAPruningSignal`; 5xx made non-retryable -> `_ServerErrorsAreRetriedAndThenSucceed` and `_RetriesAreBounded`; `accessTokenFor`'s cache-hit disabled so every push exchanges -> `_AccessTokenIsAcquiredOnceAndReused` (3 exchanges for 3 pushes) |
| PB-PUSH-3 | DERIVED | **derived, and still NOT MET — the two facts are orthogonal (B129).** Two mutations broke its shipped fences: loud `RecipientKeyID`/`SenderKeyID` restored to the wake header -> `TestPBPUSH3_WakeHeaderCarriesNoStableEndpointIdentifiers`; `AcceptWake`'s refusal bypassed so a replayed or forged wake renders -> four `mobile/conformance` non-vacuity assertions. What was NOT re-derived, by instruction, is the row's OPEN defect — the presence sweep is separable by SHAPE, not by size — whose remedy is known false: the producer enumeration matches syntax, so a `var p PushPayload` with field assignment plus an indirect call through a func value is invisible to it |
| PB-PUSH-5 | DERIVED | three caught, and **one SURVIVED — FINDING G, fixed here.** Caught: the relay's push verdict made to fail the trigger -> three `TestPushDelivery_*`; `PushNotifier.Event` returning the wake error -> `_PushFailureNeverFailsTheJournalRecord` plus two fail-closed siblings; the `cfg.Pusher == nil` arm removed -> `_NoPusherConfiguredLeavesTheCorePathsUntouched`. Survived: **the credential validation deleted from BOTH layers** (`LoadServiceAccount`'s required-field loop AND `NewFCM`'s completeness check) left `internal/remote/push` and `internal/remote/relay` entirely green. See FINDING G |
| PB-PUSH-6 | DERIVED | three, all caught. `putToken` removed so the token is cached but never persisted -> four tests including `_TokenIsNotStoredInTheClearAlongsideTheCiphertext`; `deleteToken` removed so a restart resurrects a revoked token -> `_TokenDeleteAlsoSurvivesARestart` (2 pushes, want 1); `loadTokens` replaced by an empty map at boot -> three restart tests. The fixture DISCRIMINATES on the third: the relay is genuinely restarted against the same store rather than re-read in process |
| PB-PUSH-7 | DERIVED | `handleTokenRegister` changed from last-wins to first-wins, so a re-registered handset keeps the stale token -> `TestPBPUSH7_SecondTokenForOneRoutingIDReplacesTheFirst` fails naming the old token. The row asks for a decision plus a test PINNING the behaviour, and the pin is what the mutation proves is real |
| PB-PUSH-8 | DERIVED | three, all caught, and the first is the requirement's own distinction. `categoryEnabled` removed from `maybeWake`, i.e. suppression moved off the sender — the "local filtering is not sufficient" defect the row names — -> `_DisabledCategorySendsNoPushAtAll` (3 pushes, want 0) and three siblings; `applyPushPrefs`'s `reply.Op == OpError` arm disabled so the gateway applies without the daemon's authorization -> `_DaemonRefusalLeavesThePreferenceUnchanged`; the ack moved ahead of the persist -> `PBPUSH10_AcknowledgementIsSealedOnlyAfterThePreferenceIsDurable` |
| PB-PUSH-10 | DERIVED | five caught, one survived and is fenced here. Caught: the persist replaced by an in-memory cache -> `_PreferenceSurvivesAProcessRestart`, `_PreferenceIsStoredAsInspectableState` and the requirement's literal criterion `_DisabledPreferenceStillSuppressesAtTheSenderAfterARestart` (3 pushes at the SENDER, want 0); the version guard disabled -> `_StaleVersionNeverOverwritesANewerPreference`; a corrupt file read as the ENABLED default -> `_CorruptPreferenceIsNeverSilentlyTheEnabledDefault`; the bootstrap flipped to off -> `_NeverConfiguredDefaultsToBothCategoriesEnabled`. Survived: `SavePrefs` falling back to the bootstrap default when the STORED record cannot be read — defeating the fail-closed rule its own doc states — left every PB-PUSH-8/-10 test green. Fenced now by `TestPBPUSH10_AnUnreadableRecordBLOCKSTheWriteRatherThanResettingTheVersion`, verified RED under exactly that mutation |

## FINDING G — PB-PUSH-5's credential fixtures could not tell a validated loader from an unvalidated one

**Measured.** `LoadServiceAccount`'s required-field loop and `NewFCM`'s completeness check were
BOTH deleted, so a service account missing `project_id`, `client_email` and `token_uri` is
accepted and an `FCM` sender constructs happily. `go test ./internal/remote/push/
./internal/remote/relay/` was **green**.

The cause is the fixture set, not the code. All five cases in
`TestPBPUSH5_InvalidCredentialsFailLoudlyAtConstruction` carry a private key that is absent or
unparseable, so every one is refused by `json.Unmarshal` or by `parseRSAPrivateKey` — measured
directly: **zero of the five reach `NewFCM` at all**, because the loop's
`if err != nil { return // refused at load }` fires first every time. The assertion at that
test's `NewFCM` arm is unreachable, and the requirement's property was resting on two checks
each of which was fenced only by the OTHER one existing.

The reachable misconfiguration this leaves unfenced is ordinary: a credential with a valid
private key and an empty or missing `project_id` — a hand-edited or templated service account.
The sender constructs, then posts to `/v1/projects//messages:send` and fails on every wake,
which is precisely the "constructs happily and fails on every send" failure the test's own
comment says it exists to prevent: *"a relay that looks healthy while push is dead; the operator
finds out from a user who missed a hand-off."*

**Fixed here.** Three discriminating cases were added — a well-formed generated key with exactly
one of `project_id` / `client_email` / `token_uri` removed. Controls measured: at HEAD all three
pass; with both validation layers deleted all three fail naming the field. Removing either layer
alone still passes, which is correct and is recorded as such — the property survives on either
check, so the redundancy is defence in depth, the same shape as PB-GW-8's two `resumed` guards.
