# S14 evidence — Android key custody, and PB-KEY-9's closing half

> ## AMENDED 2026-07-31 (ADR-007 B133) — the newest of this file's two banners, read it first
>
> The trust boundary is now the **wire** between phone and computer. Both endpoints are trusted,
> and all phone-side user authentication has been removed with its code (`e0e644d`, `11f0517`,
> `52b8abf`, `7863f8c`). This file is one of the most affected in `docs/verification/`, because
> its subject is exactly the custody layer that was auth-gated.
>
> **What changes here, enumerated rather than summarised.** Each is marked in place below; none
> of the superseded text is deleted.
>
> - **PB-SEC-2 is VOID**, not failed. Its subject — "the biometric gate is cryptographically
>   enforced, not cosmetic" — has left the product. Its Derivation row cited five mutations
>   against a `KeyGenParameterSpec` and gate code that no longer exist, and is now **NOT DERIVED**
>   with the original text quoted inside it.
> - **PB-KEY-2, PB-KEY-7 and PB-KEY-8 are NARROWED.** Each keeps a true residue, each is marked
>   where it appears, and each Derivation row now says which of its mutations still has a
>   subject and which does not.
> - **PB-KEY-1, PB-KEY-5, PB-KEY-6, PB-KEY-9 and PB-SEC-1 are unaffected as requirements.**
>   PB-KEY-5's derivation row is annotated anyway, because one of the mutations recorded for it
>   was caught by a test that has since been deleted as falsified.
> - **PB-E2E-5 is NARROWED**: real biometrics left its scope because the feature left the product.
>   Real camera, real FCM, real Doze and hardware Keystore attestation stay deferred.
>
> **What does NOT change, and a strict reading would wrongly delete it.** Keystore sealing at
> rest survives whole — only the auth *parameters* went, and non-exportability defends offline
> extraction of the app data directory by an attacker who is not the holder. The two-tier
> wake/content split survives whole, because FCM reads push payloads and the split is enforced at
> the SENDER. Everything this file says about the custody *seam* — B8's single inbound crossing,
> the reverse-bound `KeyCustody` interface, fail-closed construction, the golden diff, the
> cleartext-sealer inventory, the two verdict tokens — is untouched by B133.
>
> The accepted residual risk is recorded once, in ADR-007 B133: a stolen unlocked phone gives its
> holder full control of the agents on the machine, and the only surviving mitigation is
> `swarm remote off` or a revoke issued from the machine.

> ## SCOPE CORRECTION (independent review, 2026-07-25) — read before citing this file
>
> This file's framing is broader than what is proven. **What S14 establishes is that the Go core
> seals under whatever `KeyCustody` it is handed, and that no constructor can omit one.** That part
> is real, fenced, and was verified by the reviewer against the built AAR rather than the Go source:
> every `byte[]` in the emitted surface is inbound, no bound method returns bytes, and the Kotlin
> gate is 123 tests with 0 failures.
>
> **The ANDROID half has no production wiring yet**, verified independently and by me:
> - `SealedStore` (`KeyCustodySession.kt`) is an in-memory map. There is **no file I/O anywhere** in
>   `android/app/src/main/` — no `openFileOutput`, no `filesDir`, no `SharedPreferences`, no `File`.
> - No `KekProvider` implementation exists outside tests; `KeystoreProvisioner.generate` is
>   unimplemented.
> - **Nothing in `main/` constructs `swarmmobile.NewApp`** — the symbol appears only in comments.
>
> So statements in this file about "the shipped app" describe the Go seam, not a wired handset app.
> PB-KEY-9 is delivered in the sense that the cleartext sealer is gone and custody is mandatory; it
> is NOT yet delivered in the sense of a phone that stores a real Keystore-backed KEK.
>
> **Four tests are green today that this blocker makes MISLEADING**, and an auditor must not read
> them as covering it: the two PB-SEC-1 byte-level gates, the state-at-rest tier gate, and the
> facade-seals-both-tiers test. All four drive the Go core with sealers or a KEK **the test
> supplies**. They prove the Go side seals correctly, which is real. **None of them can see that the
> handset has no code to supply a KEK that survives a restart** — the gap is upstream of every one
> of them. This is the same shape as the epoch-key defect: a real-components test supplying the
> missing input by hand.
>
> One more detail, and it is the part that hid it: `SealedStore.rawBytes`'s own doc says "The
> persisted bytes, exactly as they sit on disk" — over an in-memory map. **The comment is the
> plausible-but-wrong value**, and a comment is what a reader checks instead of the code.
>
> **This is an S16 BLOCKER, and it is standing defect class (ii) — a plausible-but-wrong value hiding
> a brick.** An in-memory `SealedStore` means the KEK vanishes on process death, and on the next
> start `device.key` and `phone-state.json` are sealed under a key that no longer exists —
> **permanently unopenable**. On Android a process death is routine. S16 must wire real persistence
> and a real Keystore-backed provider, and the recovery path for a KEK that is genuinely gone is
> `ErrKeyInvalidated` (re-pair), not a silent failure to start.

**Requirements**: PB-KEY-1, PB-KEY-2, PB-KEY-5, PB-KEY-6, PB-KEY-7, PB-KEY-8, PB-SEC-1, PB-SEC-2,
and the closing half of **PB-KEY-9**.
**Decisions**: ADR-007 B8 (single inbound crossing), B9 (tiers per role), B16/B17 (minSdk 33 as a
product choice), B18 (the custody seam).
**RED**: `67a9116` — 77 `@Test` across nine Kotlin files, plus 563 lines of scaffolding.

> **PB-E2E-5, the physical-handset gate, is DEFERRED and nothing here changes that.** No test in
> this slice covers real biometrics, a real camera, real FCM delivery, real Doze behaviour or
> hardware Keystore attestation. Robolectric and JVM tests model POLICY — which tier a role is in,
> which `KeyGenParameterSpec` is REQUESTED, which failures are distinguished, what the code does
> with each. Every file that could be misread as claiming more says so in its own header.
>
> **AMENDED 2026-07-31 (ADR-007 B133).** "Real biometrics" has left PB-E2E-5's scope, because the
> feature left the product — this is removal by feature deletion, not reclassification. The rest
> of the list stands and stays deferred. The sentence about which `KeyGenParameterSpec` is
> REQUESTED is still exactly right about method; what is requested is now
> `setUserAuthenticationRequired(false)` on both KEKs.

## What PB-KEY-9 was missing, and what closed it

`phonecore.Resume` has taken a `Sealer` per tier since S14a and fails closed without one. But the
seam is a Go struct field and **gomobile cannot set one**, so the only reachable answer was
`phonecore.InsecureCleartextSealer` — identity `Seal`/`Open`. The shipped app wrote the epoch
content key to `phone-state.json` in the clear while PB-SEC-1's acceptance gate stayed green by
injecting real sealers from Go. ADR-007 B18 forecast exactly this and assigned it to S14.

**The verb.** `swarmmobile.NewApp` now takes a second argument:

```go
type KeyCustody interface {
	WakeKEK() ([]byte, error)
	ContentKEK() ([]byte, error)
}

func NewApp(cfg *Config, custody KeyCustody) (*App, error)
```

`KeyCustody` is **reverse-bound**: Go calls it, Kotlin implements it. `NewApp` derives one
`custodySealer` per tier (AES-256-GCM, nonce-prefixed) that holds the **fetcher, never a key** —
every seal and every open goes back to Keystore, so an auth-gated content KEK re-checks
authorisation on every use and a lock actually stops content operations (PB-KEY-7).

> **CORRECTION 2026-07-26 — the last clause of the sentence above is FALSE.** See ADR-007 B35/B36.
> It is true of the **at-rest seal** and the `Resume` path, and false of the **live send path**:
> `mobile/commands.go` `resolveSend` reads `core.State().Keys.ContentKey` straight from Go memory
> with no Keystore round-trip, so after a single `Resume` neither a screen lock nor PB-SEC-2's
> freshness window stops anything. PB-KEY-7 and PB-SEC-2 are recorded **NOT MET**.
>
> Struck in place rather than deleted, because this is precisely the defect PB-E2E-3 was built to
> catch and did not: the fence checks that an evidence file **names** its requirement, and this file
> names it. **Naming is checkable; truth is not.** The correction is left visible so the limit of
> that fence is legible next to an instance of it.
>
> > **AMENDED 2026-07-31 (ADR-007 B133) — this correction is itself now overtaken, in both
> > halves.** The claim it was correcting is dead at the root rather than half-true: there is no
> > auth-gated content KEK to re-check authorisation on any path, at rest or live, because
> > `Provisioning.kt:421` now requests `setUserAuthenticationRequired(false)` for both tiers. And
> > there is no screen-lock or freshness event to stop anything, because `runtime/ContentLock.kt`
> > and `keys/TimedTierGate.kt` were deleted. **The re-fetch-per-operation behaviour of
> > `custodySealer` is unchanged and still real** — it just no longer buys an authorisation check,
> > only the property that no Go-side field holds the KEK.
> >
> > The two verdicts recorded above also move apart: **PB-SEC-2 is VOID**, so nothing is owed on
> > it and no later slice may reopen it; **PB-KEY-7 is NARROWED**, the purge mechanism survives
> > whole and its trigger moves from screen lock to revoke/unpair.

**There is no constructor that omits it.** A nil `KeyCustody` is a refusal that also leaves the
state directory empty. An optional parameter is a parameter somebody passes nil to, and B18(c)
decided cleartext must not be reachable by forgetting something.

### Why the seam is not a `Seal`/`Open` pair, which is the shape a reader will reach for first

On a reverse-bound interface **the directions are mirrored**: Go is the caller, so a RESULT travels
Java → Go (inbound — B8's single permitted crossing) and a PARAMETER travels Go → Java (outbound).
`Seal(plaintext []byte)` would therefore hand the Java layer the three content-tier **private
scalars** in the clear on every first launch. `KeyCustody` returns bytes and accepts none.

`TestS14_TheCustodySeamIsInboundOnly` fences it. **PB-BIND-4's own guard cannot**: its
`entryPoints()` covers funcs and methods only, so an `ifacemethod` is invisible to it — that gap is
why the test exists. `GoCustodyFailureTest.the_bound_custody_interface_takes_no_key_material_outbound`
pins the same property on what gomobile actually **emitted into the AAR**, which is what the app
compiles against.

## The golden diff, and why each line is B8-compliant

Regenerated deliberately with `-update-surface`:

```
+const KeyCustodyAuthRequired
+const KeyCustodyKeyInvalidated
-func NewApp(*Config) (*App, error)
+func NewApp(*Config, KeyCustody) (*App, error)
+ifacemethod KeyCustody.ContentKEK() ([]byte, error)
+ifacemethod KeyCustody.WakeKEK() ([]byte, error)
+type KeyCustody interface
```

- **The two consts** are strings, carry no key material, and exist so Kotlin binds the same literal
  the Go side stamps rather than keeping a copy that could drift.
- **`NewApp`** gains an interface parameter, not a `[]byte` one. No new byte channel.
- **The two `ifacemethod` lines** are the only new `[]byte` in the golden, taking it from three
  occurrences to five. All five remain **inbound**: three are parameters on Go-implemented methods,
  two are results on a Kotlin-implemented interface. No bound **method** returns `[]byte`, which is
  the invariant `TestPBBIND4_TheOnlySecretCrossingIsNamedAndInbound` enforces and which still holds.
- **The crossing did not widen.** It is the same artifact B8 names — "a transient per-tier data key,
  unwrapped by an authenticated-Keystore AES KEK on the Java side and passed Java → Go" — now
  supplied for the at-rest seal as well as for the epoch keys. `KeyCustody` is exactly two verbs,
  one per PB-KEY-2 tier, and the fence fails on a third.

`screen_coverage.tsv` was updated rather than left stale: `lifecycle.restore` records that `NewApp`
requires custody, `key_custody.install` records that the at-rest half rides the reverse-bound
interface (which is not an entry point, so it has no row of its own), and `connection_state` records
the two new states.

## Both `InsecureCleartextSealer` call sites are gone, and the fence went red on purpose

`mobile/app.go` now passes the two `custodySealer`s. `mobile/conformance/harness_test.go` uses a
`testCustody` (real AES-GCM under two software keys) for both its direct `phonecore` calls and the
facade. `grep -rn InsecureCleartextSealer --include=*.go` finds no call expression anywhere.

`TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites` failed at that moment, exactly as
designed, and its message named what to do:

```
ADR-007 B18(c): the InsecureCleartextSealer call sites are [], want exactly
    [mobile/app.go mobile/conformance/harness_test.go].
FEWER means S14 landed the facade verb, PB-KEY-9 is delivered, and both this fence and the
'THE SHIPPED APP STILL WRITES THE CONTENT KEY IN THE CLEAR' section of
docs/verification/remote-phaseB-progress.md are now false and must be retired.
```

Both were retired **in this change**, and neither was relaxed:

- The fence became `TestS14A_TheCleartextSealerHasNoCallSitesLeft` with a floor of **zero**. It was
  not deleted and its list was not widened; the rename is the record, and its doc says so.
  Mutation-proven in the MORE direction: putting `mobile/app.go` back on the cleartext sealer
  produces `call sites are [mobile/app.go], want exactly []`.
- The progress doc section is now "PB-KEY-9 IS DELIVERED", stating what closed it, what still does
  not hold, and the byte-search limitation below.

**One thing the empty inventory does not prove**: that anything was sealed. It proves only that
nobody *asked* for cleartext. The positive half is separate and is named from the fence's own
comment.

## The positive half: the acceptance gate now drives the path production takes

`android/gate/keycustody_test.go`'s two PB-SEC-1 tests were untouched — no assertion moved — but
they drive `phonecore.Resume` directly with sealers injected from Go, which is a path the Android
app **cannot take**. That is the fifth standing defect class, one layer up.

`TestS14_TheShippedFacadeSealsBothTiersUnderTheInjectedKEK` (new) goes through
`swarmmobile.NewApp` and `App.InstallWakeKey`/`InstallContentKey` — the constructor the app uses and
B8's single inbound crossing — then reads the bytes of `device.key` and `phone-state.json`.

**Mutation, run and recorded.** With `mobile/app.go` put back on the cleartext sealers:

```
PB-SEC-1: device.key holds a 96-byte content blob and a 32-byte wake blob over 96 and 32 bytes
of plaintext. A blob no longer than its plaintext carries neither nonce nor tag and is not
authenticated encryption -- the facade sealed NOTHING and the four device private scalars are
on disk in the clear
```

That assertion is deliberately **first**, before anything needing a key: without it the failure was
an opaque `cipher: message authentication failed` that says nothing about the material being in the
clear.

Non-vacuity is asserted in the same test: reopening the directory under the same KEKs must recover
**exactly** the two installed keys, and they must be distinct. A phone that wrote nothing would
satisfy every "not verbatim" check.

`TestS14_TheFacadeRefusesToConstructAPhoneWithNoCustody` covers fail-closed, and additionally that
the refusal leaves the state directory empty — a refusal that leaves key material behind is not one.

### DO NOT read a green byte search as "no cleartext key material"

S14a's round-3 correction stands, and both new byte-level assertions carry it in their own comments.
Base64 encodes three bytes at a time, so a 32-byte needle's encoding appears inside a longer field's
encoding **only** when it happens to be 3-byte aligned and terminal. Under a leak of all four device
privates as one base64 field the search catches exactly one of them, by an accident of layout. What
carries the property is the POSITIVE assertion — that the material went through the injected sealer
and comes back only under it.

## PB-KEY-6: the two sentinels are now distinguishable in Kotlin

Recorded by S14a as a residual: `crypto.ErrKeyAuthRequired` and `crypto.ErrKeyInvalidated` survive
every Go hop distinctly and then hit gomobile, **which flattens a Go error into a Java exception
carrying only a message**. No facade verb classified them, so Kotlin saw prose.

Two stable tokens now cross, in **both** directions:

- **Outbound**: `barrier` — the panic barrier every entry point already installs as its first
  statement — stamps `KeyCustodyAuthRequired` / `KeyCustodyKeyInvalidated` onto any error that
  `errors.Is` one of the two sentinels. Doing it centrally is the point: it is total by
  construction, and a verb added later inherits it. Per-verb classification would have been an
  enumeration somebody has to keep correct.
- **Inbound**: `classifyCustodyVerdict` reads the token off what Kotlin threw and maps it back onto
  the crypto sentinel. This direction is the more consequential one:
  `phonecore.openSealedDeviceKeys` refuses a Resume outright for any content-tier error that is NOT
  one of the two, so a refusal that failed to classify would turn a **locked handset into an app
  that cannot start**.

`KeyCustodyException.UserAuthenticationRequired` and `.KeyPermanentlyInvalidated` carry the token in
their message for exactly that reason, `GoCustodyFailure.classify` maps a message back onto the
type, and `GoCustodyFailure.recoveryFor` gives each a different answer: re-prompt versus re-pair.

`TestS14_TheTwoCustodyVerdictTokensAgreeAcrossTheLanguageBoundary` pins the Kotlin literals to the Go
constants — **Go is authoritative**, and Kotlin holds literals only because the unit-test JVM does
not load the AAR. A drifted copy would fail silently and in the worst direction: an unrecognised
token degrades a permanent invalidation into a prompt the user can satisfy and that changes nothing,
forever.

## PB-KEY-6 at the transport edge: the dial refusal, closed

`mobile/relay.go` discarded its dial error with a bare `continue`, and the phone is the only
production caller of `relay.ClientAuth.Sign` that can refuse. Unreachable on the software keystore;
**live the moment this slice's KEK landed**.

| verdict | before | now |
|---|---|---|
| `ErrKeyAuthRequired` | endless `reconnecting`, no prompt | `reauth_required`, PERSISTS across retries, keeps dialing, cleared by the first success |
| `ErrKeyInvalidated` | the same silent loop against a destroyed key | `repair_required`, and the loop **returns** |

Returning rather than breaking is deliberate: `break` falls through to `setConn("offline")` and
erases the one state that tells the user to pair again. The recoverable state is preserved at the
top of the loop rather than overwritten by `reconnecting`, because a spinner tells the user to wait
for something only a biometric will end.

> **AMENDED 2026-07-31 (ADR-007 B133).** The `ErrKeyAuthRequired` row of the table above no longer
> describes the product. `reauth_required` has been removed atomically across `error_taxonomy.tsv`,
> `mobile/relay.go` (`mobile/relay.go:176` now records its absence by name), the Kotlin
> `ConnectionState`/`ErrorState` enums and `Remedy.AUTHENTICATE`: with no phone-side authenticator
> there is no prompt to offer and nothing for the loop to wait for, so "PERSISTS across retries,
> keeps dialing, cleared by the first success" describes a product that does not exist. **Both
> verdicts now land on `repair_required`**, and `TestS14_ARecoverableCustodyRefusalAsksForTheBiometricRatherThanSpinning`
> is renamed `TestS14_AnAuthGatedCustodyRefusalTellsTheUserToRePairRatherThanSpinning`
> (`mobile/conformance/s14_dialrefusal_test.go:84`).
>
> **The reason this section exists is untouched**: the dial error must not be discarded, leaving
> the user on a spinner for a condition nothing can clear. Only the remedy moved, from
> "authenticate" to "pair this device again".
>
> **`ErrKeyAuthRequired` is still raisable, and by exactly one population** — an install
> provisioned *before* B133, whose content KEK still carries `AUTH_BIOMETRIC_STRONG` because
> `KeystoreCustodyBootstrap.ensure` returns early when the alias exists and does not re-request
> the spec on upgrade. A re-pair discards the alias, so the remedy the arm now gives is one that
> population can actually carry out.

**Both tests fail against the old `continue`**, verbatim:

```
PB-KEY-6: a RECOVERABLE custody refusal (crypto.ErrKeyAuthRequired) left the phone reporting
"reconnecting". ...
PB-KEY-6: a PERMANENT custody refusal (crypto.ErrKeyInvalidated) left the phone reporting
"reconnecting". ...
```

The refusal is injected **at the KEK**, not at the signature, so the assertion covers the whole
chain — `KeyCustody` → `custodySealer` → `sealedKeyStore.SignRelayAuth` → `relay.ClientAuth.Sign` →
`relay.Dial` → the transport loop — over a real relay and a real handshake. A fake `ClientAuth.Sign`
would have tested the last two links and skipped the ones this slice built.

Terminality is observed by **counting wake-tier unwraps across two windows** (one dial each), not by
reading a flag. Non-vacuity in the recoverable case: the KEK is restored and the phone must reach
`online`, or `reauth_required` is a dead end rather than a prompt.

**Recorded consequence**: after `repair_required` the run goroutine has returned but `a.sess` is
still set, so `Start` is a no-op until `Stop`. That is correct for a device whose key is gone —
recovery is a re-pair, which builds a new App — but it is a behaviour, not an accident.

## The F5 residual is now fenced

S14a recorded: with the content tier **locked** the three content publics cannot be re-derived,
because the material to check against is exactly what the lock withholds. The claim that this is not
exploitable rested on `mobile/pairing.go` hoisting `NoiseStatic()` above the literal that reads
`RecipientPublic`/`CommandSigningPublic` — **an ordering dependency, not a structural guarantee, and
unfenced**.

`TestS14_EveryContentTierPublicReadIsOrderedBehindAnUnsealOrInventoried` requires every function
reading a content-tier public to either call a content-tier operation **strictly before** the read
(so `checkPublic` has run and a locked tier stops it), or be named in an inventory with a reason.
"Somewhere in the same function" was rejected: it is the weaker claim that makes the fence look like
it holds while a reorder walks straight through.

**Mutation-proven.** Moving pairing's `NoiseStatic()` hoist below the payload literal produces:

```
mobile/pairing.go:BeginPairing reads RecipientPublic()
```

Two exemption lists, kept separate because they mean different things:

- `s14PublicMechanism` — `sealDeviceKeys`, `contentStore`, `wakeStore` read the publics off a store
  built from material they just generated or unsealed. They are the **authoritative side** of
  `checkPublic`'s comparison; requiring an unseal of the unseal is circular. Named, not matched by
  file, so a genuine consumer added to `keycustody.go` still trips.
- `s14UnverifiedPublicReaders` — one entry, `mobile/app.go:deviceID`, which S14a's own re-review
  found and which is real: it reads `CommandSigningPublic` with no unseal. Its only consumer is
  `RevokeThisDevice`, which **seals nothing** (device_revoke has no gateway mapping), so the value
  lands in a durable local refusal record and never on the wire. Forcing an unseal there would put a
  biometric prompt in front of the panic button. **The entry says a second consumer invalidates it.**

  > **AMENDED 2026-07-31 (ADR-007 B133).** The last sentence of the paragraph above has lost its
  > producer: there is no prompt an unseal could put in front of anything. What still holds is the
  > exemption's *first* and load-bearing half — the value reaches no other party — and the
  > re-decide clause, which is unchanged. **The narrower reason that survives is a cost, not a
  > safety argument**: an unseal on the revoke path is a Keystore round-trip on the one path whose
  > purpose is to work when something has gone wrong. That is a weaker justification than the one
  > originally written, and it is recorded as weaker rather than swapped in silently.
  >
  > The exemption's own text in `internal/phonecore/s14_publicordering_test.go:88-90` still reads
  > "it would put a biometric prompt in front of the panic button". That is a stale comment in
  > code, outside this file's ownership, and is reported rather than edited here.

A **stale** entry fails too: an exemption for a function that no longer needs it is a standing
licence nobody re-examined. `TestS14_TheOrderingFenceCanActuallyFail` is the negative control — an
AST fence that stops matching passes forever, so it asserts the tree really contains both a reader
and a reader ordered behind an unseal.

## The Kotlin side

All **77** RED tests pass, plus **9** added for the cross-language seam (86 total in
`dev.swarm.phone.keys`). RED baseline before implementation: **72 of 77 failing**, every one with
`kotlin.NotImplementedError` — the right reason, a missing decision rather than a broken test. No
RED test was weakened, rewritten or deleted.

Decisions the tests demanded, and the ones worth stating:

- **Tiers** (ADR-007 B9): `RELAY_AUTH` is WAKE — background reconnect must work on a locked handset.
  `RECIPIENT`, `NOISE_STATIC`, `COMMAND_SIGN` are CONTENT — `OpenSealedBox` recovers **both** epoch
  keys from a grant, so an after-first-unlock recipient key *is* a content key.
- **Enforcement**: `KEYSTORE_AUTH_GATING`, never `OS_PROCESS_ISOLATION`. `FirebaseMessagingService`
  runs in the app process, so iOS's Notification-Service-Extension argument does not transfer.
- **Backing**: every row `KEYSTORE_WRAPPED`, no row `KEYSTORE_NATIVE`, at any API level — ADR-007
  B17(a), because native operation must RUN inside Keystore and that needs a reverse seam B8
  forbids. `requiresApi` is 30 for the content roles and 23 for RELAY_AUTH, both **below** the
  pinned floor, deliberately: B17 records that 33 is a **product** choice, and a row claiming to
  need exactly the floor would re-assert the falsified rationale.
- **Grant retention**: DISCARDED_AFTER_OPEN. A retained blob opens both epoch keys, so it would be a
  second independent path to the content key; it buys nothing, because PB-KEY-7's recovery is a
  fresh unwrap of the sealed tier key, not a re-open of the grant.
- **Enrollment change → `REPAIR_DEVICE`**, not `REPROVISION_KEK`. Reprovisioning re-seals plaintext
  you still hold; here the destroyed KEK protected the only copy of the content-tier scalars,
  including the COMMAND_SIGN seed the daemon registry pins the device id to.
- **Wake KEK**: `setUserAuthenticationRequired(false)` **and** `setUnlockedDeviceRequired` left at
  its default, and `setInvalidatedByBiometricEnrollment(false)` set **explicitly** — the Builder
  defaults it to true, so a re-enrolled fingerprint would silently kill the sole background wake
  path with no error anywhere.
- **Timed operations share one Keystore entry; per-use operations get one each.** That makes
  `sharesAuthorizationWith` structurally true rather than a claim beside separate keys.
- **The session holds no key material in any field**, which is what lets `LockPurgeTest` sweep it
  reflectively. The zeroize is in a `finally` around the install, not after it.
- **`KeystoreKeyCustody`** implements the bound `swarmmobile.KeyCustody` over the sealed store, so
  the seam has something on the far side. The AAR was rebuilt with `android/build-aar.sh`; the
  emitted interface is `byte[] wakeKEK() throws Exception` / `byte[] contentKEK() throws Exception`,
  and the two verdict tokens are bound constants on `swarmmobile.Swarmmobile`.

Two artifacts were added to `AtRestArtifact` — `STATE_KEK_WAKE`, `STATE_KEK_CONTENT` — because the
design stores each tier's state-sealing data key **wrapped** under its Keystore KEK. An artifact
with no record is key material nobody decided where to keep, which is how `device.key` came to sit
in the clear.

> **AMENDED 2026-07-31 (ADR-007 B133) — four of the decisions listed above no longer hold, and the
> rest do.** Taken in order, so a reader can see which is which:
>
> - **Tiers** — UNCHANGED. `RELAY_AUTH` is WAKE, the three content roles are CONTENT, and the
>   `OpenSealedBox` argument for why an after-first-unlock recipient key *is* a content key is
>   unchanged as a statement about what the grant opens. What it no longer implies is that anything
>   stops the attacker: see the residual below.
> - **Enforcement** — CHANGED. `KEYSTORE_AUTH_GATING` is gone from the enum; the value is now
>   `EnforcementMechanism.CODE_DISCIPLINE` (`keys/Custody.kt:184,192`). B133 states this narrowing
>   rather than glossing it: **removing auth-gating leaves code discipline as the only phone-side
>   enforcement of the tier boundary.** The `FirebaseMessagingService`-runs-in-process reason for
>   rejecting `OS_PROCESS_ISOLATION` is unchanged and is why the alternative was never available.
>   The property the split exists to buy — that the carrier of the push cannot read content — is
>   enforced at the SENDER, in the gateway, and never rested on this half.
> - **Backing** — UNCHANGED. Every row `KEYSTORE_WRAPPED`, the `requiresApi` values and the
>   minSdk-33-is-a-product-choice reasoning are untouched by B133.
> - **Grant retention** — UNCHANGED. `DISCARDED_AFTER_OPEN`, and PB-KEY-7's recovery is still a
>   fresh unwrap of the sealed tier key rather than a re-open of the grant.
> - **Enrollment change → `REPAIR_DEVICE`** — the *routing* stands, but its trigger is now
>   vestigial: `Provisioning.kt:422` sets `setInvalidatedByBiometricEnrollment(false)` on **both**
>   KEKs, so a re-enrolment destroys neither.
> - **Wake KEK** — the flags are unchanged and are now what **both** KEKs carry:
>   `setUserAuthenticationRequired(false)` and an explicit
>   `setInvalidatedByBiometricEnrollment(false)`. The reason given for setting the second
>   explicitly — the Builder defaults it to true, and a silent kill of the background wake path
>   has no error anywhere — is unchanged and now protects the content path too.
> - **Timed vs per-use Keystore entries** — GONE. Both tier gates were deleted, and
>   `sharesAuthorizationWith` no longer exists in `android/app/src/main/`. **This bullet cannot be
>   re-demonstrated.**
> - **The session holds no key material in any field** — UNCHANGED, and still what lets
>   `LockPurgeTest` sweep it reflectively. That file keeps its name; its trigger moved from lock to
>   revoke (`a_revoke_purges_the_core_and_leaves_neither_tier_armed`).
> - **`KeystoreKeyCustody`, the AAR and the two verdict tokens** — UNCHANGED.
> - **The two `AtRestArtifact` rows** — UNCHANGED. Sealing at rest is explicitly KEPT by B133;
>   only the auth parameters went.

## Gates

- `go build ./...`, `go vet ./...` — clean.
- `go test -count=1 ./...` — **whole repository green**.
- `golangci-lint run` — 26 findings, **all pre-existing**, none in any file this slice touched
  (`mobile/`, `android/`, `internal/phonecore/s14*`).
- `android/gate` including both PB-SEC-1 tests — green, plus the two new facade-driven ones.
- `./gradlew lint test` — green (86 tests in `dev.swarm.phone.keys`, 0 failures).

## Residuals

- **PB-E2E-5 stays deferred.** Nothing here is evidence about hardware.
- **The byte search is not a proof of no cleartext** — see above. Both fences say so in comments.
- **The call-site fence's function-value evasion gap** is inherited unchanged:
  `var mint = phonecore.InsecureCleartextSealer` in a third package stays green. It has no
  var-initializer gap; it is not evasion-proof.
- **`InsecureCleartextSealer` still exists** with zero call sites. Deleting it was considered and
  rejected: it would push a future test wanting unsealed custody into hand-rolling an identity
  sealer, which no grep finds — B18(c)'s named-choice property is worth more than the symbol's
  absence.
- **The conformance harness reproduces the facade's AEAD construction** (AES-256-GCM, 12-byte nonce
  prefix) because it seals the seed state the facade then opens. A second copy of a construction is
  a second thing to get wrong; it is stated at the definition rather than assumed.
- **Unsealed plaintext buffers in the Go core are still not zeroized after use**, unchanged from
  S14a. The KEK the facade fetches *is* zeroized as soon as the cipher is built.
- **`mobile/commands.go` `resolveSend` copies the content key into a `sendCtx`**, so an operation in
  flight continues past a concurrent purge. Unchanged, still owned by whoever next touches the
  facade send path.
- **`Start` is a no-op after `repair_required`** until `Stop` — recorded above.
- **The wake KEK is not user-authentication-gated**, by design (B9/B16). `KEYSTORE_WRAPPED` is
  accurate for RELAY_AUTH — the key *is* wrapped by a Keystore AES key — but the tier's gate is the
  split, not the KEK. Stated in the row's rationale, since PB-KEY-8's matrix forbids a residual on a
  non-`SOFTWARE_ONLY` row.

> **AMENDED 2026-07-31 (ADR-007 B133) — two of the residuals above, and one addition.**
>
> - **"The wake KEK is not user-authentication-gated" is now true of BOTH KEKs**, and the sentence
>   that follows it is the one that survives intact: *the tier's gate is the split, not the KEK*.
>   That was written as a caveat and is now the whole statement. The split is enforced at the
>   SENDER — the push path holds the wake key only — so it is untouched by anything removed from
>   the phone.
> - **PB-E2E-5 stays deferred**, narrowed: real biometrics left its scope with the feature.
> - **ADDED, because it belongs in this list and is stated once in ADR-007 B133 rather than
>   re-argued per slice**: a stolen unlocked phone gives its holder full control of agents that
>   edit code on the Mac. Nothing in this custody layer stands between them and take-control,
>   type, kill or launch. The only surviving mitigation is `swarm remote off` or a device revoke
>   issued FROM THE COMPUTER, which makes `SeverAllRemoteControl` load-bearing in a way it was
>   not: it was the outer of two layers and is now the only layer.

## Derivation

**MACHINE-READABLE. `scripts/phaseb-traceability.py` reads this section** (ADR-007 B129). One row
per requirement, the verdict token `DERIVED` or `NOT DERIVED`, and — for `DERIVED` — **the
mutation that was made to fail, in the same row**. A `DERIVED` verdict with an empty mutation
cell is refused and counted NOT DERIVED. Any requirement with no row here is NOT DERIVED.

`DERIVED` means somebody made this row's fence fail on purpose and restored it. Every mutation
below was applied to a PRODUCTION file in a detached worktree, the named test run, and the
mutation reverted. **`internal/remote/crypto` was not touched**: it is frozen, and one clause
below is NOT DERIVED for exactly that reason rather than being waved through.

**THE KOTLIN FENCES RAN.** `android/app/libs/swarm.aar` was stale and the app module did not
compile; `android/build-aar.sh` rebuilt it (exit 0) and `:app:testDebugUnitTest` is green at
HEAD, so every Kotlin mutation below is a real Robolectric/JVM run rather than a deferral.

> **AMENDED 2026-07-31 (ADR-007 B133) — five of these eight rows are annotated in place, and one
> changes verdict.** The rule applied, stated so it can be checked: where **every** mutation
> recorded for a row targeted code that B133 deleted, the verdict becomes **NOT DERIVED** and the
> original text is quoted inside the cell rather than removed. Where **some** mutations survive
> with live subjects and those cover the requirement's surviving clause, the verdict is carried
> forward and the dead mutation is named.
>
> **Only PB-SEC-2 changes verdict**, DERIVED -> NOT DERIVED, because its requirement is VOID and
> all five of its mutations were against a `KeyGenParameterSpec` and gate code that no longer
> exist. PB-KEY-2, PB-KEY-5, PB-KEY-6, PB-KEY-7 and PB-KEY-8 keep their verdicts with the
> annotation.
>
> **Nothing below was re-run for this amendment.** What was checked is narrower and is stated as
> such: whether each named mutation still has a subject in the tree, and whether the test named as
> catching it still exists. A verdict carried forward is carried forward from its original date,
> not re-earned on 2026-07-31.

| Requirement | Verdict | The mutation, and its result |
|---|---|---|
| PB-KEY-1 | DERIVED | BIDIRECTIONAL, both on `mobile/app.go`. Added an OUTBOUND secret verb `App.ExportNoiseStaticPrivate() []byte` -> caught by `TestPBBIND4_TheOnlySecretCrossingIsNamedAndInbound`. Renamed `InstallContentKey` -> `LoadContentKey` -> caught three ways: the crossing is no longer one of the documented artifacts, the named crossing has vanished, and `screen_coverage.tsv` no longer resolves |
| PB-KEY-2 | DERIVED | Kotlin, in the real `KeyGenParameterSpec`: the CONTENT KEK's `setUserAuthenticationRequired(true)` -> `false` -> caught (`KeystoreSpecTest.the_content_kek_requires_a_strong_biometric`); the WAKE KEK's `false` -> `true` -> caught (`the_wake_kek_works_on_a_locked_handset`), so both directions of the split are fenced. Go at-rest half: `sealDeviceKeys` sealing the CONTENT tier under the WAKE KEK -> caught. **AMENDED 2026-07-31 (ADR-007 B133) — NARROWED, and the FIRST of the three mutations above has lost its subject.** The clause "the content key is user-authentication-gated" is deleted from the requirement, and `the_content_kek_requires_a_strong_biometric` is deleted with it: `KeystoreSpecTest` now asserts the inverse, `the_content_kek_asks_for_no_user_authentication`, because `Provisioning.kt:421` requests `setUserAuthenticationRequired(false)` on both KEKs. **The clause that SURVIVES is "not readable by the push path or derivable from it" — the two-tier split — and the other two mutations still have live subjects**: `the_wake_kek_works_on_a_locked_handset` (`KeystoreSpecTest.kt:130`) and `sealDeviceKeys` (`internal/phonecore/keycustody.go:136`). The split's real enforcement was always at the SENDER, in the gateway, so it is untouched by anything removed from the phone. Verdict carried forward from 2026-07-25, not re-earned |
| PB-KEY-5 | DERIVED | `KeyCustodyMatrix.rows[RECIPIENT].tier` CONTENT -> WAKE (the matrix production reads through `KeyTierPolicy.tierOf` and `CustodyPlan.Provisioned`) -> caught by `PerRoleCustodyTest.an_after_first_unlock_attacker_reaches_no_content_key`, which is the requirement's own criterion, plus the tier-assignment and inventory joins. `COMMAND_SIGN` CONTENT -> WAKE -> caught. **AMENDED 2026-07-31 (ADR-007 B133) — the REQUIREMENT is unaffected, but the words "which is the requirement's own criterion" were a MISATTRIBUTION, and the test they name has since been DELETED as falsified rather than narrowed.** PB-KEY-5 is "custody tier per role, not one undifferentiated core key" — role separation across `NoiseStatic`, `OpenSealedBox`, `SignCommand`, `SignRelayAuth` — and has nothing to do with authentication. The after-first-unlock claim was **PB-KEY-2's dying clause**: with `setUserAuthenticationRequired(false)` the content key IS reachable after first unlock, by design, so `an_after_first_unlock_attacker_reaches_no_content_key` was removed rather than reworded. A narrowed requirement keeps a true residue and that claim keeps none; rewording it would have produced a test reading as the phase's central security claim while fencing a fixture's own lock. **What still catches both tier mutations is the "plus" clause of the original row**, live in `PerRoleCustodyTest.kt`: `each_role_has_the_tier_ADR_007_B9_assigns_it` and `the_custody_matrix_agrees_with_the_tier_policy`. Verdict carried forward, not re-earned |
| PB-KEY-6 | DERIVED | `sealedKeyStore.SignCommand` memoizing the unsealed content tier -> caught (*"a memoized tier keeps signing after the screen locks"*). `OpenSealedBox` swallowing the custody refusal -> caught three ways including `TestS14A_LockedContentTierRefusesEverySigningPath`. `SignRelayAuth` swallowing its refusal and returning an empty signature -> **survives `internal/phonecore`, `mobile` and `android/gate`, and is caught only in `mobile/conformance`** — see the note below on where "every signing path" actually lives. **AMENDED 2026-07-31 (ADR-007 B133) — the requirement is UNAFFECTED and all three mutation subjects are live; one QUOTED FAILURE MESSAGE has lost its producer.** "A memoized tier keeps signing after the screen locks" describes an event that no longer exists — there is no screen-lock verdict on this handset. **The property the mutation actually fences is unchanged and still load-bearing**: no Go-side field holds the unsealed tier, so every operation re-fetches, and a memoized tier would keep signing after a REVOKE, which is what PB-KEY-7's purge now exists to prevent. `TestS14A_LockedContentTierRefusesEverySigningPath` and `TestS14A_TheContentTierIsUnsealedPerOperationNotCached` both still live in `internal/phonecore/s14a_sealing_test.go`; the conformance test named in the note below has been renamed, and the note carries the new name. Verdict carried forward, not re-earned |
| PB-KEY-7 | DERIVED | Kotlin: `KeyCustodySession.invalidate` no longer calling `core.purgeKeys()` -> caught (`LockPurgeTest.every_invalidation_event_purges_the_core`). Go: memoizing the content tier in `sealedKeyStore` -> caught by `TestS14A_TheContentTierIsUnsealedPerOperationNotCached`, which is the in-process screen-lock case a restart-based test cannot see. **AMENDED 2026-07-31 (ADR-007 B133) — NARROWED, and the KOTLIN mutation above is no longer applicable as written.** `KeyCustodySession.invalidate(event)` and its five `InvalidationEvent`s are deleted, replaced by an unconditional `purge()` (`KeyCustodySession.kt:160`), and `every_invalidation_event_purges_the_core` is deleted with them — `LockPurgeTest` keeps its filename and now reads `a_revoke_purges_the_core_and_leaves_neither_tier_armed`. **The purge MECHANISM survives whole; only its TRIGGER moved, from lock/background/auth-expiry to revoke or unpair**, because none of the three old triggers has a producer any more. **One change is of substance rather than of name and must not be read past: the purge SCOPE inverted.** The lock purge deliberately SPARED the wake tier (ADR-007 B35) since a screen lock is a state the phone comes back from; a revoke is not, so both tiers now go and the state is unrecoverable without pairing again. **The Go mutation is untouched and still has a live subject** — `TestS14A_TheContentTierIsUnsealedPerOperationNotCached` in `internal/phonecore/s14a_sealing_test.go` — and it is what carries this verdict forward: a memoized tier would keep signing straight through a purge. Its "in-process screen-lock case" framing is dead; the property is not. The rewritten Kotlin fence has NOT been mutation-checked by anyone, and this row does not claim it has |
| PB-KEY-8 | DERIVED | `USER_AUTH_PER_USE` removed from `CustodyPlan.required` — the silent downgrade the row forbids -> caught by three `KeyCustodyMatrixTest` assertions. An UNKNOWN StrongBox probe claimed as PRESENT -> caught by `absent_strongbox_falls_back_without_claiming_hardware_it_lacks`. **The wire-KAT clause is NOT derived**: its subject is `internal/remote/crypto`, which is frozen, so no mutation was attempted there and none is claimed. **PB-E2E-5's `KeyInfo` clause is HARDWARE-BLOCKED**. **AMENDED 2026-07-31 (ADR-007 B133) — NARROWED, and the FIRST mutation above is now the SHIPPED STATE rather than a mutation.** The matrix no longer expresses auth-gated key generation: `USER_AUTH_PER_USE` has left the `PlatformCapability` enum entirely, because it probed an API-level fact — `setUserAuthenticationParameters(timeout, type)` landed in API 30 — that the design no longer calls anywhere, so the row could only ever have refused a handset over a capability nothing uses (`PlatformCapabilities.kt:27-30`). The three `KeyCustodyMatrixTest` assertions that caught it can no longer be made to fail that way; the consumed set is now the single row `KEYSTORE_AES_GCM`. **The SECOND mutation is untouched and is what carries this verdict**: `absent_strongbox_falls_back_without_claiming_hardware_it_lacks` is live at `KeyCustodyMatrixTest.kt:347`, and "do not claim hardware you lack" is squarely inside the narrowed requirement. Dropping an authenticator from the capability matrix is a NARROWING, which ADR-007 B8 permits. Verdict carried forward, not re-earned |
| PB-SEC-1 | DERIVED | `sealDeviceKeys` writing the content tier in the clear instead of through `content.Seal` -> caught; the same for the wake tier -> caught; the epoch-key half, `sl.Seal(key)` replaced by the identity in `internal/phonecore/state.go` -> caught naming BOTH epoch keys verbatim in `phone-state.json`. **FINDING I below**: a container that seals correctly AND writes a second cleartext copy survives every fence |
| PB-SEC-2 | NOT DERIVED | **VOID, NOT FAILED (ADR-007 B133, 2026-07-31), and the DERIVED verdict this row carried until today is WITHDRAWN.** The requirement's entire subject — "the biometric gate is cryptographically enforced, not cosmetic" — has left the product: all phone-side user authentication was removed with its code, and `keys/PerUseGate.kt`, `keys/BiometricPolicy.kt`, `keys/BiometricPrompts.kt`, `keys/TimedTierGate.kt`, `runtime/ContentLock.kt` and the four `android/gate/s20_pbsec2_*` fences are deleted. **All five mutations recorded here were against a `KeyGenParameterSpec` and gate code that no longer exist, and NOT ONE of the five tests that caught them still exists.** The withdrawn text is quoted verbatim so the record is not destroyed: *"five mutations, all on the real KeyGenParameterSpec and gate code, all caught: content KEK setInvalidatedByBiometricEnrollment(true) -> false (the_content_kek_is_invalidated_by_a_biometric_enrollment_change); every gated op given the TIMED window instead of its own spec (each_operation_requests_the_timeout_its_freshness_tier_implies); per-use ops sharing one Keystore alias (per_use_operations_do_not_share_a_keystore_entry); TimedTierGate.withFreshAuthorization running the action instead of pausing on a stale window (the_action_runs_without_a_prompt_inside_the_window_and_not_outside_it); the timed gate accepting a per-use operation (the_timed_gate_refuses_a_per_use_operation). This does not make the row MET -- its open first clause has no fence to break, which is why it is NOT MET, and derivation is orthogonal to status (B129)."* **NOT DERIVED here does not mean "nobody has looked yet", which is what it means on every other row.** It means there is nothing left to look at: a requirement with no subject cannot be derived, ever, and no later slice may reopen this as unfinished work. The fuller record of what was demonstrated on 2026-07-26, and what it cost, is in `docs/verification/remote-phaseB-pbsec2-peruse-evidence.md`, which now carries a WITHDRAWN banner |

### FINDING I — PB-SEC-1's recorded blind spot has a stated mitigation that does not hold. OPEN.

`android/gate/keycustody_test.go` records that its byte search cannot see material buried at an
unaligned offset inside a longer base64 field, and states that *"the positive half — that the
material went through an injected sealer — is what covers that, and it lives in phonecore's
in-package mirror (`TestS14A_ResumeSealsBothTheDeviceKeysAndTheEpochKeys`)"*.

Measured: adding a `Backup []byte` field to `sealedDeviceKeys` and writing
`NoiseStaticPriv || RecipientPriv || CommandSignSeed` into it **in the clear**, while sealing the
content tier correctly as well, gives `ok internal/phonecore` and `ok android/gate`. The mirror
does not catch it because `s14aSealedMaterial` asks only whether the material was EVER handed to
a sealer — which stays true when a duplicate is also written down beside it — and
`s14aFindMaterial` searches base64 with the same 3-byte alignment blind spot as the gate it is
supposed to cover. Neither test enumerates the container's fields.

The gap is that no fence asserts what `device.key` MAY contain, only what it may not contain
verbatim. B125's ninth axis puts it plainly: the core mints these bytes and the disk cannot
un-write them.

### Where "every signing path" actually lives, and it is not the test named for it

PB-KEY-6's criterion quantifies over **every signing path**.
`TestS14A_LockedContentTierRefusesEverySigningPath` covers the three CONTENT-tier paths
(`SignCommand`, `NoiseStatic`, `OpenSealedBox`) and asserts `SignRelayAuth` **succeeds** — the
wake tier is not gated on the user, which is correct. So no assertion in that file drives a
custody FAILURE through the wake path, and `SignRelayAuth` returning `(nil, nil)` on a refusal
passes `internal/phonecore`, `mobile` and `android/gate`.

It is caught, two packages away, by `mobile/conformance`:
`TestS14_ARecoverableCustodyRefusalAsksForTheBiometricRatherThanSpinning` and
`TestS14_APermanentCustodyRefusalIsTerminalAndStopsRetrying`.

> **AMENDED 2026-07-31 (ADR-007 B133).** The first of those two is now
> `TestS14_AnAuthGatedCustodyRefusalTellsTheUserToRePairRatherThanSpinning`
> (`mobile/conformance/s14_dialrefusal_test.go:84`) — the remedy moved from "authenticate" to
> "pair this device again" when the phone-side authenticator went. The finding recorded in this
> section is about WHERE a quantified criterion is actually fenced, and is unaffected by the
> rename.

The requirement is covered. What is
recorded here is that the fence a reader would go to for it does not carry it, and that a
package-scoped mutation run would have reported a hole that is not there — the reason every
survival above was re-run against a wider package set before being written down.
