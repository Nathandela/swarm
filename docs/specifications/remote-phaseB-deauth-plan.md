# Phase B — removing phone-side user authentication

**Status: PLAN. Nothing here is executed yet.**

## The decision

The trust boundary is **the wire between phone and computer**. Both endpoints are trusted; the
relay, the network, and **FCM/Google** remain the declared adversary.

All local user authentication on the phone is removed: no biometric, no PIN, no device credential,
no per-use gate, no timed gate, no content lock, no unlock button. The app opens freely. **This is a
production decision, not a demo shortcut.**

### Explicitly KEPT — a strict reading would wrongly delete these

| Kept | Why it is a *wire* concern, not a phone concern |
|---|---|
| Keystore sealing at rest, with `setUserAuthenticationRequired(false)` | Non-exportability defends offline extraction of the app data dir, not the holder. Costs one flag. |
| Android backup exclusion (PB-STATE-6) | A backup is **a copy of keys leaving the device over the network**. |
| The two-tier wake/content key split (PB-KEY-2) | The wake key is content-free because **FCM reads push payloads**. Google is on the wire. |
| Noise XXpsk0, channel binding, SAS, epoch grants, relay-distrust | This is now the *whole* security budget. |

> **The SAS emoji comparison becomes the ONLY human-in-the-loop security step in the product.** It is
> what defeats a relay MITM. It must get harder to skip, never easier: no auto-confirm, no
> timeout-to-accept.

### Verified contained

- **The content key does not feed the Noise handshake, channel binding, or SAS.** It seals content at
  rest and nothing else.
- **`GateToken` is NOT a cryptographic biometric attestation.** It is a random one-shot token
  (`internal/phonesim/phonesim.go:409`) whose function is anti-swap — the daemon recomputes
  `content_hash = SHA256(GateToken)` so a relay that swaps it breaks the device signature
  (`internal/protocol/server.go:1519,1539-1541`) — plus single-use replay prevention. **It survives
  entirely and matters more under the new model.** Only the word "biometric-attested" in its comments
  is now wrong.
- **No wire format, on-disk format, protocol, or Go interface signature changes.**
- `internal/remote/crypto` stays **FROZEN**. Dropping an authenticator from the capability matrix is a
  **NARROWING**, which ADR-007 B8 permits.

## Ordering rule — non-negotiable

**ADR → requirements → tests → code.** A test encoding the old decision is *correct as written* for a
superseded decision. It is deleted or rewritten **deliberately, after the ADR lands** — never bent to
make code pass (CLAUDE.md, implementation-goals GG-5).

---

## Phase 0 — decision of record (blocks everything)

1. **ADR-007: new entry recording the threat-model change.** This is the root decision; everything
   below is derivable from it. Must state the accepted residual risk: *a stolen unlocked phone gives
   the holder full control of agents that edit code on the Mac; the only surviving mitigation is
   `swarm remote off` / `revoke` from the computer.*
2. **ADR-007 B59 SUPERSEDED.** It refused `AUTH_DEVICE_CREDENTIAL` on the premise that "the threat
   model is a device someone else is holding." That premise is retired, so the conclusion goes.
3. **ADR-007 A15 AMENDED.** Two-tier split kept; rationale rewritten to transport-only.
4. **Requirements** (`docs/specifications/remote-phaseB-requirements.md`):
   - `PB-SEC-2` → **VOID**. Its entire subject is "the biometric gate is cryptographically enforced,
     not cosmetic."
   - `PB-KEY-2` → **NARROWS**. Kill the clause "the content key is user-authentication-gated". Keep
     "not readable by the push path or derivable from it."
   - `PB-KEY-7` → **NARROWS**. "Lock purges live memory" has no lock event. Becomes: **revoke/unpair**
     purges live memory. `MailboxRouter` still holds `ContentKey` by value.
   - `PB-KEY-8` → **NARROWS**. Matrix no longer expresses auth-gated key generation.
   - `PB-APP-7` → **NARROWS**. Two push toggles stay; the biometric-gate toggle goes.
   - `PB-E2E-5` → **NARROWS legitimately**. "Real biometrics" leaves scope because the *feature* leaves
     the product. Real camera, real FCM, real Doze, hardware Keystore attestation **stay deferred.**
     This is removal-by-feature-deletion, not reclassification by fiat.
   - `PB-PUSH-4` → **NARROWS.** Verified text: "renders a **content-free** notification *unless the
     user has authenticated*; it never decrypts session content with a locked device." The italicised
     clause loses its producer. **Content-free rendering itself is KEPT** — FCM reads payloads and the
     lock screen still exists.
   - `PB-KEY-5` → **UNAFFECTED.** *(Correcting a misattribution: PB-KEY-5 is "custody tier per role,
     not one undifferentiated core key" — role separation across `NoiseStatic`, `OpenSealedBox`,
     `SignCommand`, `SignRelayAuth`. It has nothing to do with authentication and survives whole.)*

   > **Where the "after-first-unlock attacker reaches no content key" claim actually lives.** It is
   > **PB-KEY-2's dying clause**, not PB-KEY-5. And it is **FALSIFIED, not narrowed**: with
   > `setUserAuthenticationRequired(false)` the content key becomes reachable after first unlock, so
   > the claim is **false by design** rather than reduced. `PerRoleCustodyTest.kt`'s two attacker
   > tests fence *that*, which is why they cannot be repaired — only removed or re-premised. A
   > narrowed requirement keeps a true residue; this one does not. Do not reword it into something
   > true.
   - `PB-SEC-1`, `PB-STATE-6`, `PB-KEY-6`, `PB-KEY-9` → **UNAFFECTED.**
5. **`scripts/phaseb-traceability.py`** — move voided IDs to `NOT_MET`, adjust the denominator, drop
   stale `## Derivation` rows. **This script has been broken twice by careless range edits that
   silently deleted rows. Change it line-by-line and diff the generated counts before and after.**

## Phase 1 — DELETE (whole files)

### Android main — ~1,545 lines

| File | Lines |
|---|---|
| `keys/PerUseGate.kt` | 384 |
| `keys/BiometricPolicy.kt` | 359 |
| `keys/BiometricPrompts.kt` | 287 |
| `runtime/ContentLock.kt` | 296 |
| `keys/TimedTierGate.kt` | 219 |

### Android tests — ~2,257 lines

`keys/PerUseGateTest.kt` (454), `keys/TimedTierGateTest.kt` (474), `keys/BiometricGateTest.kt` (347),
`keys/StalePromptCallbackTest.kt` (331), `keys/PromptSupersessionTest.kt` (274),
`runtime/ContentLockTest.kt` (200), `runtime/ContentUnlockTest.kt` (177).

### `android/gate/` Go fences — PB-SEC-2's fences, ~1,452 lines

`s20_pbsec2_peruse_test.go` (527), `s20_pbsec2_wiring_test.go` (387),
`s20_pbsec2_timedprompt_test.go` (274), `s20_pbsec2_freshness_test.go` (264).

**Deletion total ≈ 5,250 lines.**

## Phase 2 — MODIFY

### Android main

| File | Change |
|---|---|
| `PhoneSurface.kt` (62 hits) | `gatedButton`/`perUseButton` → plain buttons; drop `unlockContent` and `gatedActions`. **Largest single diff.** |
| `keys/Provisioning.kt` (28) | `setUserAuthenticationParameters(…, AUTH_BIOMETRIC_STRONG)` at ~406 and ~432 → `setUserAuthenticationRequired(false)`. Keep hardware backing. |
| `ui/MachineAndLaunch.kt` (18) | Gated launch controls → plain. |
| `PhoneRuntime.kt` (11) | Drop gate-derived startup verdicts; keep `DEVICE_UNSUPPORTED` for genuine capability loss. |
| `ui/SettingsScreen.kt` (11) + `SettingsSurface.kt` (5) | Remove the biometric-gate toggle; keep both push toggles. |
| `PairingSurface.kt` (10) | Its buttons are gated. → plain. **Fixes the B132 first-run strand.** |
| `keys/Custody.kt` (9) | Drop auth-gated provisioning branches. |
| `SwarmApplication.kt` (5) | Drop `contentLock` wiring. |
| `keys/PlatformCapabilities.kt` (4) | Narrow the matrix. |
| `keys/KeystoreKek.kt` (3), `keys/KeystoreCustody.kt` (3), `keys/KeyCustodySession.kt` (2) | Drop auth params; keep sealing. |
| `ui/FacadeBridge.kt` (3), `PhoneActivity.kt` (3), `ui/ConnectionUi.kt` (1) | Drop gate plumbing. |

### Go — comments and one behavioural question

- `internal/protocol/server.go:1400,1513,1608` — reword "biometric gate"/"biometric-attested". **The
  mechanism stays; only the naming is wrong.**
- `internal/phonecore/state.go:368,781,1121,1247`, `lease.go:52,198`, `core.go:42`,
  `coalesce.go:152` — comments, **plus a real question**: lease severing currently triggers partly on
  *biometric-freshness expiry*. With no such expiry, decide what (if anything) severs a lease on
  backgrounding. **Do not silently drop it — that is a behaviour change, not a comment change.**

### Android UI defect worth fixing in the same pass

`PhoneActivity`/`PhoneSurface` apply no window insets; the status bar overlaps the top text on SDK 36
(observed on the A26). Trivial.

## Phase 3 — tests to REWRITE (not delete)

The property still matters; its expected outcome inverts.

- `keys/KeystoreSpecTest.kt` (26 hits) — asserts the `KeyGenParameterSpec`. New expectation:
  `setUserAuthenticationRequired(false)`, hardware backing retained.
- `ui/SettingsScreenTest.kt` (17) — toggle set shrinks to two.
- `ui/MachineAndLaunchTest.kt` (12) — controls reachable without a gate.
- `PhoneLaunchSurfaceTest.kt` (10) — **contains the `perUseButton` call-site FLOOR** (fails if fewer
  than two gated call sites exist). That floor breaks loudly when the gate goes. Expect it.
- `PhoneSurfaceControlsTest.kt` (3), `keys/LockPurgeTest.kt` — purge trigger moves lock → revoke.

### Residual set — CLASSIFIED

| File | Verdict | Note |
|---|---|---|
| `keys/GoCustodyFailureTest.kt` | REWRITE | Compile break: `Recovery`/`recoveryFor`, `needsBiometricPrompt`. Keep two-token classification + B8 bound-interface test. |
| `keys/KeyCustodyMatrixTest.kt` | REWRITE | `matrixConsumes` calls deleted `BiometricPolicy.specFor`. Consumed set shrinks to `{KEYSTORE_AES_GCM}`. |
| `keys/KeystoreHardwareFloorTest.kt` | REWRITE (one test) | Floor is a KEPT at-rest concern. `the_floor_does_not_replace_the_requested_versus_achieved_comparison` goes RED because the spec will now *request* `false` — swap the downgrade axis. |
| `keys/FailableCustodyTest.kt` | REWRITE | Compile break on `GateInvalidation`. Keep typed-invalidation, failed-unwrap-installs-nothing, wake-tier independence. |
| `keys/CustodyPersistenceTest.kt` | REWRITE (light) | Persistence and destroyed-KEK-is-permanent KEPT (PB-KEY-9). |
| `keys/PerRoleCustodyTest.kt` | REWRITE | Its headline criterion is **FALSIFIED, not narrowed** (see PB-KEY-5). Role-set/tier-assignment KEPT. |
| `keys/DeviceCapabilitiesTest.kt` | REWRITE | Breaks compile if `USER_AUTH_PER_USE` leaves the enum; **vacuous if it stays**. |
| `keys/CustodyFixtures.kt` | REWRITE (light) | **Highest-leverage line in the set** — see below. |
| `ui/ConnectionAndErrorTest.kt` | REWRITE | `REAUTH_REQUIRED` assertions lose their producer; revoked/staleness/clock survive load-bearing. |
| `push/WakeNotificationTest.kt` | REWRITE (narrow) | `VISIBILITY_SECRET`, no-leak, no-interpolation KEPT — the lock screen still exists and FCM still reads payloads. |
| `PhoneActivityWindowTest.kt` | REWRITE (rename-level) | PB-SEC-12 tapjacking filtering **SURVIVES and matters more** on un-gated revoke/take-control. Re-anchor, do not delete. |
| `android/gate/pbapp11_freshness_test.go` | **UNAFFECTED** | Confirmed zero biometric content; it is pure staleness-by-silence, and self-guarding. |
| `android/gate/s16_ui_test.go` | UNAFFECTED (reword) | Its set-equality check is the **anti-vacuity enforcer** — see below. |
| `android/gate/s16_wiring_test.go` | UNAFFECTED (reword) | Only a `t.Errorf` string mentions the gate. |
| `internal/phonecore/contentlock_test.go` | REWRITE | Biggest behavioural decision in the set. Both `TestPBSEC2_*` DELETE. |

> **`CustodyFixtures.kt:52` defaults `lockedTiers = setOf(KeyTier.CONTENT)` — a state production can
> no longer enter, and it is what keeps four of the vacuous tests below green. Flip it to
> `emptySet` early; several false greens collapse on their own.**

### The dangerous class: VACUOUS-GREEN

Still compiles, still passes, **fences nothing**. A green test reads as coverage.

1. **`internal/phonecore/contentlock_test.go` — the whole file. Worst case in the repo.** Pure Go over
   fake sealers, and `crypto.ErrKeyAuthRequired` still exists because **crypto is FROZEN** — so it
   stays green while fencing a screen-lock event that exists nowhere and asserting a **VOIDed**
   requirement.
2. `PerRoleCustodyTest.kt` — the two after-first-unlock attacker tests. **These read as the phase's
   central security claim** and would pass against the fake's lock.
3. `CustodyPersistenceTest.kt` — locked-tier-recoverable, and "still asks the KEK on every open".
4. `FailableCustodyTest.kt` — every `lockedTiers` case, if fixed only enough to compile.
5. `GoCustodyFailureTest.kt` — `AUTH_REQUIRED`-token classification, if minimally patched.
6. `ConnectionAndErrorTest.kt` — `REAUTH_REQUIRED` assertions if the taxonomy row is kept "for safety".
7. `WakeNotificationTest.kt` — the `contentReady=false` trio if production hardwires `true`.
8. `DeviceCapabilitiesTest.kt` — the `USER_AUTH_PER_USE` test if the enum entry survives.

Every survivor must be mutation-checked: break what it claims to fence, confirm it fails (B129) —
**then revert the mutation** (B132).

### `REAUTH_REQUIRED` must be removed atomically

It appears in `error_taxonomy.tsv`, `mobile/relay.go:182` (`connReauthRequired`), the Kotlin
`ConnectionState`/`ErrorState` enums, and `Remedy.AUTHENTICATE` (`ErrorRouting.kt:99`). **All in one
change**, or `s16_ui_test.go`'s set-equality forces keeping a producer-less state. `mobile/conformance/
s16_errorstates_test.go` ("every row reachable") is coupled and was outside the classification pass.

## Follow-up: `androidx.biometric` still ships, justified by a VOID requirement

`android/app/build.gradle.kts:303` still declares `androidx.biometric:biometric:1.1.0`, justified at
`:277-300` entirely by **PB-SEC-2 (now VOID)**. `android/dependency-inventory.tsv:37` carries the same
stale justification. **No Kotlin file imports it** — `TestB133_TheAppImportsNothingFromAndroidxBiometric`
in `android/gate/s16_ui_test.go` fences that, so it cannot return by accident.

**Attempted and deliberately backed out.** Removing the line breaks the build twice over: the
dependency lock state (`Did not resolve 'androidx.biometric:biometric:1.1.0' which is part of the
dependency lock state`), and then, under `--write-locks`, **dependency verification fails for 21
artifacts**. `build.gradle.kts:133-137` prescribes the real procedure and says why it is not automatic:

```
./gradlew :app:dependencies --write-locks
./gradlew --write-verification-metadata sha256 help
```
> "and REVIEW THE DIFF. The regeneration step is the point at which a changed artifact has to be
> justified by a person."

Regenerating checksum verification is a **PB-SEC-14 supply-chain action requiring human review**, not
cleanup collateral. It is its own slice. Removing the dependency *pays* PB-SEC-14 by shrinking the
closure — worth doing, deliberately, with the diff reviewed.

## Design decisions OWED — do not let these be resolved silently

Each is a behaviour change wearing a comment change's clothes.

1. **What severs a lease on backgrounding**, now that biometric-freshness expiry is gone
   (`internal/phonecore/lease.go:52,198`).
2. **What sets `contentReady = false`**, now that "unless the user has authenticated" has no producer.
3. **On revoke/unpair**: is the wake tier purged too? Is the content tier recoverable without a
   re-grant? Are watermarks and the op-queue carried across? `PB-KEY-7`'s `PurgeKeys` mechanism
   survives; only its trigger moves — but these three expectations need re-deciding, not renaming.

> **B129 made mutation mandatory and never said to revert it.** A `&& false` from an earlier
> derivation escaped into a shipped binary (ADR-007 B132). **Revert every mutation; verify `git
> status` is clean before building anything.**

## Phase 4 — docs

`docs/operations/operator-runbook.md` (also fix: it implies the gateway is running before pairing —
it is quiescent until a device exists), `docs/specifications/system-spec.md`, every
`docs/verification/` evidence file citing a voided or narrowed requirement.

## Phase 5 — verify, then demo

`go build ./...`, `go test ./...` (`-race` where goroutines spawn), `go vet ./...`,
`golangci-lint run`, plus the Android suite. All green before close (GG-4).

Then: rebuild APK, **uninstall** (fresh install — the spec is baked into the Keystore key at
generation), install, pair with the A26 over `adb reverse tcp:8443 tcp:8443`, compare SAS, start the
gateway.

## Guard rails

- `internal/remote/crypto` — **FROZEN.**
- ADR-007 B8 — key material crosses the binding once, inbound; no bound method returns `[]byte`;
  the matrix may only **NARROW**.
- ADR-007 D5 — gateway under an external supervisor, never spawned by the daemon.
- ADR-007 D7 — input is live-only, never queued or replayed.
- Never meter auth against `pendingRID`.
