# PB-E2E-5 — the physical-handset gate

# ⚠ EVERY STEP IN THIS DOCUMENT IS **UNRUN** ⚠

**Nothing below has been performed. Not once, not partially, not on an emulator.** There is no
physical handset attached to this project, which is the entire reason PB-E2E-5 is deferred
(ADR-007 **B31**). This document exists so the gate is *checkable later* rather than a promise, and
writing it is the one runbook in S20 where "do not write a step you have not run" cannot be honoured
by declining to write it — so the honesty requirement inverts: the steps are written, and their
un-run status is marked on every one of them.

Every step is tagged **`[UNRUN]`**. **That tag is the operator's to remove, one step at a time, and
only after performing that step on hardware.** A step whose tag is still present has not been done,
whatever else the document says around it.

> **An emulator is not a handset.** ADR-007 B31 makes it a standing prohibition that no artifact
> produced in an emulator may be cited as evidence for anything here. If you are tempted to run a
> step against an AVD to "check the runbook works", the result may not be recorded in §9 and may not
> be used to remove an `[UNRUN]` tag.

> **Until this gate runs, Phase B is "provisionally implemented", not done** (§13 of the
> requirements, ratified by B31). The audit may find every other requirement met and must still
> record Phase B as provisional. This gate is not assignable to an agent.

> **AMENDED 2026-07-31 (ADR-007 B133) — the gate has NARROWED, and this document is not yet
> rewritten against the post-removal code.** All phone-side user authentication was removed from
> the product: no biometric, PIN, per-use gate, timed gate or content lock exists any more.
> "Real biometrics" leaves this gate's scope because the feature left the product; **real camera
> (§5), real FCM (§6), real Doze (§6d) and hardware Keystore attestation (§2b) stay deferred and
> stay in the gate.** Until the revision lands, read the steps with these corrections:
>
> - **§4 (Real biometrics) is VOID in full.** There is no prompt, no per-use tier, no timed
>   window, no `REAUTH_REQUIRED`, and no enrollment invalidation of the content KEK.
> - **The alias table in §2 and the result sheets in §9 shrink.** The `gate.timed` and
>   `gate.peruse.*` aliases are deleted with the gates. The content KEK is provisioned with
>   `setUserAuthenticationRequired(false)` — not user-auth-gated, not invalidated by enrollment
>   change — and hardware backing is retained, which stays the thing to measure.
> - **§3c's expected refusal has no producer.** A locked-device unwrap of the content KEK is no
>   longer expected to refuse; record what the device reports, but a success there is the new
>   design, not the gate failing to exist.
> - **§1a/§1b preconditions shrink.** No biometric enrollment is required and §1b's second slot
>   existed only for §4d, which is void. A screen lock is still wanted: §6e's lock-screen
>   notification checks survive, because FCM still reads payloads and the lock screen still
>   exists.
> - **§2c/§9's `USER_AUTH_PER_USE` row is narrowed away by PB-KEY-8**; whether the capability
>   remains probed at all is settled by the code pass, so re-check §2c against the shipped
>   matrix before running it.
> - **§7a's timed-window clause is void.** What, if anything, severs a lease on backgrounding is
>   an open behaviour decision recorded in B133 — re-check before running §7.
>
> The `[UNRUN]` discipline is unchanged: every tag stays until its step is performed on
> hardware, against the revised form of this document.

---

## 0. Why "it worked" is the wrong thing to record

Read this before step 1. It is the difference between running the gate and appearing to.

**Almost every step below has a way to pass while the property it exists for is absent.** A key that
is software-only unwraps perfectly. A device with no biometric enrolled skips the prompt and
succeeds. A push that never arrives looks like a quiet machine. In each case the *step* completes
and the *guarantee* does not exist.

So the recording rule is:

> **Record what the device reported, per role and per alias — never that the step succeeded.**
> Where a step has a hardware form and a fallback form, record **which one you observed**. A result
> line reading "OK" or "works" is not a result and the gate is not discharged by one.

PB-KEY-8 requires a **defined refusal or fallback** when a handset lacks a required algorithm or
auth capability, so observing the fallback is a legitimate outcome — a *recorded* fallback is a pass
for PB-KEY-8 and simultaneously a **fail** for the hardware-backing claims in PB-SEC-1 and B31. Both
must be written down. Collapsing them into one verdict is the failure this section exists to
prevent.

### Two things found while writing this runbook that change what you must record

**(a) Nothing in the code fails when the KEK is not in secure hardware.**
`CustodyProvisioning.provision` (`android/app/src/main/kotlin/dev/swarm/phone/keys/Provisioning.kt`)
does the right thing in general — it generates and then **reads back** `KeyInfo`, and throws
`KeystoreDowngrade` when the achieved parameters differ from the requested ones. But its
`downgrades` list compares only `userAuthenticationRequired`, the validity window,
`invalidatedByBiometricEnrollment`, and a spuriously-reported StrongBox. **`insideSecureHardware` is
read into `KeyInfoRecord` and never compared against anything.** A handset whose Keystore hands back
a purely software KEK provisions cleanly, `ProvisionedKey.strongBoxBacked` is `false`, and no code
path objects.

That is not a bug in the read-back so much as a limit of the API — `KeyGenParameterSpec` has no
"require secure hardware" setter, so there is no requested value to compare against. **Which is
exactly why it has to be a human observation.** Step 2 requires you to record
`insideSecureHardware` per alias, because nothing in the product will tell you when it is false.

**(b) The Curve25519 question is not where you think it is, and it can brick provisioning.**
B31 and PB-KEY-8 both flag Curve25519 (KeyMint, Android 13+, device-dependent) as the least-exercised
path. Per the shipped matrix (`Custody.kt`) that framing needs one correction and one warning:

- **No role is `KEYSTORE_NATIVE`.** All four are `KEYSTORE_WRAPPED` — the X25519 and Ed25519 private
  halves live in the Go core's `device.key`, sealed under an **AES-GCM** Keystore KEK. That is
  ADR-007 **B17(a)** (a native key would need a Java→Go reverse seam that B8 forbids), not a device
  limitation. **So `KeyInfo` can only ever be read for the AES KEK aliases**, and any report claiming
  "X25519 is hardware-backed on this device" would be false regardless of what the device supports.
  The `KeyAlgorithm` column in the matrix records the **wire** algorithm, not what Keystore is asked
  for. The `requiresApi = 30` on the three content rows is about
  `setUserAuthenticationParameters`, not about Curve25519.
- **And yet `CustodyPlan.forDevice` still refuses to provision unless `KEYSTORE_X25519` and
  `KEYSTORE_ED25519` both probe `PRESENT`** — `UNKNOWN` fails closed exactly as `ABSENT` does. So
  the app requires two Keystore capabilities that no row in the matrix consumes. That is defensible
  as a canary, and its own comment argues it: at API 33 both are meant to be guaranteed, so a
  non-`PRESENT` answer is a Keystore not behaving as its API level promises, and failing closed on it
  is better than downgrading silently. **The consequence is still that on a handset whose Curve25519
  probe answers `ABSENT` or `UNKNOWN`, the app will refuse to provision at all — over a capability it
  does not use.** Whether that is correct caution or a real defect depends on what a real device
  answers, which is the entire point of this gate. It is the single most likely way the gate fails on
  first contact with hardware, so step 2c is deliberately first.

Neither of these is a step you can skip past. Both are recorded as findings in §10.

---

## 1. Preconditions and what to have ready

**`[UNRUN]` 1a.** A physical Android handset, **API 33 or higher** (`SWARM_ANDROID_MIN_SDK`), with a
working camera, a screen lock, and at least one biometric enrolled. Record make, model, Android
version, build number, and security patch level.

**`[UNRUN]` 1b.** A **second** biometric enrollment slot free. Step 4d requires enrolling a new
fingerprint mid-gate to prove invalidation, and a device at its enrollment limit cannot perform it.

**`[UNRUN]` 1c.** A **real Firebase project** with FCM v1 enabled, a service-account JSON for the
relay, and a `google-services.json` for the app. **This project ships neither by design** (B31), so
both must be created for this gate. Until they exist, every step in §6 is unrunnable — not
"expected to pass".

**`[UNRUN]` 1d.** A relay the handset can reach over `wss://`, per
`docs/operations/relay-runbook.md`, with `push_credentials` pointing at the service-account JSON.
Record the SPKI pin you computed and whether the certificate is publicly trusted or self-signed.

**`[UNRUN]` 1e.** A machine running `swarm` with `swarm remote init` completed and `swarm-remote` on
`PATH`, per `docs/operations/operator-runbook.md` §1.

**`[UNRUN]` 1f.** A cellular plan on the handset, or step 8c cannot run. Wi-Fi-only devices cannot
discharge the radio-handoff clause.

**`[UNRUN]` 1g.** `adb` over USB, and a shell you can read logcat from.

> **Blocking issue you will hit before step 1a matters.** The transport-security policy
> (`relay.Security` — the certificate pin, the cleartext refusal, the redirect re-check) has **no
> production caller**: `mobile/relay.go` dials with `relay.Dial` and `mobile/pairing.go` with
> `relay.DialRaw`, so the pin is not applied on the handset's connection. On Android the trust-root
> source is pinning-only for a reason Go's root loader makes real — `/system/etc/security/cacerts`
> is empty on Android 14+. **A `wss://` relay with a self-signed certificate may therefore be
> unreachable from the handset entirely, with no pin available to make it reachable.** Recorded in
> `docs/verification/remote-phaseB-residuals.md` §1.1. Until it is closed, use a **publicly trusted**
> certificate for this gate, and record in §9 which you used — because a pass obtained over a
> publicly trusted certificate says nothing about the pinned path the product ships.

---

## 2. Hardware-backed Keystore — the measurement, per alias

This is the section B31 exists for, and the one most easily faked by recording a success.

The aliases (from `KeystoreAliases`, `Custody.kt`):

| Alias | Tier | Requested properties |
|---|---|---|
| `dev.swarm.phone.kek.wake` | WAKE | AES-GCM, **not** user-auth-gated, **not** invalidated by enrollment change |
| `dev.swarm.phone.kek.content` | CONTENT | AES-GCM, user-auth-gated, invalidated by enrollment change |
| `dev.swarm.phone.gate.timed` | — | the shared timed-authorization entry |
| `dev.swarm.phone.gate.peruse.input` | — | per-use, `CryptoObject`-bound |
| `dev.swarm.phone.gate.peruse.take_control` | — | per-use |
| `dev.swarm.phone.gate.peruse.revoke` | — | per-use |
| `dev.swarm.phone.gate.peruse.kill_switch` | — | per-use |
| `dev.swarm.phone.gate.peruse.launch` | — | per-use |
| `dev.swarm.phone.gate.peruse.kill` | — | per-use |

**`[UNRUN]` 2a. Read back `KeyInfo` for every alias above and record all five fields verbatim.**
The five in `KeyInfoRecord` are `insideSecureHardware`, `strongBoxBacked`,
`userAuthenticationRequired`, `userAuthenticationValidityDurationSeconds`,
`invalidatedByBiometricEnrollment`. On API 31+ prefer `KeyInfo.getSecurityLevel()`
(`SECURITY_LEVEL_STRONGBOX` / `SECURITY_LEVEL_TRUSTED_ENVIRONMENT` / `SECURITY_LEVEL_SOFTWARE`) and
record the constant, since `isInsideSecureHardware()` is deprecated and collapses TEE and StrongBox
into one boolean.

> **What would falsely pass:** recording "provisioning succeeded, no `KeystoreDowngrade` thrown".
> Per §0(a), no downgrade is thrown for a software-only key. **A row with
> `SECURITY_LEVEL_SOFTWARE` is a recorded FAILURE of the hardware-backing claim even though the app
> is working perfectly.**

**`[UNRUN]` 2b. Obtain a hardware attestation certificate chain for `dev.swarm.phone.kek.content`
and verify it to a Google root.** `KeyInfo` reports what the *platform says*; attestation is what
makes it evidence. PB-E2E-5's criterion is "assert hardware backing through `KeyInfo`/attestation
rather than asserting 'hardware-backed' by assumption", and `KeyInfo` alone is the assumption with
one extra step. Record the root the chain terminates at and the attestation security level.

**`[UNRUN]` 2c. Record the platform-capability probe's answer for each of the four capabilities
`CustodyPlan.forDevice` requires**, individually: `KEYSTORE_AES_GCM`, `USER_AUTH_PER_USE`,
`KEYSTORE_X25519`, `KEYSTORE_ED25519`. Each is `PRESENT`, `ABSENT` or `UNKNOWN`.

> **Do this before anything else in §2.** Per §0(b), any answer other than `PRESENT` for any of the
> four makes `CustodyPlan.forDevice` return `Refused` and **the app will not provision at all** —
> including for `KEYSTORE_X25519`, which no row in the matrix actually consumes. If you see a
> refusal here, stop and record it as the finding in §10 rather than working around it: a refusal
> over an unused capability is a real product defect on a real device, and it is more valuable than
> the rest of this gate.

**`[UNRUN]` 2d. Record the StrongBox outcome as one of three things**, never as a boolean: StrongBox
requested and granted; StrongBox requested and `StrongBoxUnavailableException` caught, falling back
to the non-StrongBox spec (which `CustodyProvisioning` does deliberately); or StrongBox not
requested. The fallback is **correct behaviour** and also **not StrongBox**.

**`[UNRUN]` 2e. Record per role what backing was actually achieved**, using §9's table. Expected for
every role is `KEYSTORE_WRAPPED` with the private half in the Go core — **not** `KEYSTORE_NATIVE`,
and any report claiming native backing is wrong per §0(b). What varies by device, and what you are
actually measuring, is the **security level of the KEK that wraps it**.

---

## 3. At-rest custody, on the real device

**`[UNRUN]` 3a.** With the app paired and the device **unlocked**, pull the app's private data
directory (`adb shell run-as dev.swarm.phone`) and confirm `device.key` and `phone-state.json`
exist. Record their sizes.

**`[UNRUN]` 3b.** Search both files for the raw key material verbatim — the four device private
scalars and both epoch keys — and record that none is found. **This is the weaker half of the check
and its own source says so:** base64 alignment means a needle buried mid-field is invisible to a byte
search. Record it as "no verbatim match", never as "no key material present".

**`[UNRUN]` 3c.** With the device **locked**, attempt to unwrap `dev.swarm.phone.kek.content` and
record the exact exception. Expected: a refusal that classifies as
`KeyCustodyException.UserAuthenticationRequired`, i.e. carrying the token
`swarm-custody/auth-required`.

> **What would falsely pass:** the unwrap *succeeding* while locked. That is the gate not existing,
> and on a device where the content KEK came back with `userAuthenticationRequired = false` it is
> exactly what will happen. Cross-check against your 2a row.

**`[UNRUN]` 3d.** With the device **locked**, unwrap `dev.swarm.phone.kek.wake` and record that it
**succeeds**. This one must work with no user present or the background wake path is dead in the
state it exists for. A wake KEK that refuses while locked is a failure, not extra security.

---

## 4. Real biometrics

**`[UNRUN]` 4a. Success path.** Perform a content-tier operation and record that a real
`BiometricPrompt` appeared, which modality it offered, and that the operation completed after
success.

**`[UNRUN]` 4b. User cancel.** Dismiss the prompt. Record the app's resulting state. Expected:
`reauth_required` on the wire (`ConnectionState.REAUTH_REQUIRED`), which
`needsBiometricPrompt` marks as the one state that must not sit behind a spinner. Record whether the
UI actually offered a re-prompt or showed a spinner.

**`[UNRUN]` 4c. Per-use versus timed, observed rather than configured.** Perform two consecutive
`INPUT` operations inside the timed window and record whether the second re-prompted; then perform a
per-use operation (`TAKE_CONTROL`, `LAUNCH`, `KILL`, `REVOKE`, `KILL_SWITCH`) and record that it
**did** prompt again even inside that window. PB-SEC-2's rule is that one authorization may not
authorize a different action, and only a device can show it.

> **What would falsely pass:** a device with **no** biometric enrolled, or one where the content KEK
> came back `userAuthenticationRequired = false`. Both make every operation succeed with no prompt at
> all, and a step recorded as "the operation completed" passes. **Record the presence or absence of
> the prompt itself, not the operation's outcome.**

**`[UNRUN]` 4d. Enrollment invalidation — the destructive step, do it last in this section.**
Enroll an **additional** fingerprint, then attempt a content-tier operation. Record the exact
failure. Expected: a refusal carrying `swarm-custody/key-invalidated`, classifying as
`KeyPermanentlyInvalidated`, surfacing as `repair_required`, with recovery `REPAIR_DEVICE`.

Then record the two things that matter more than the exception:

- **The app must NOT offer a biometric re-prompt**, because a permanent invalidation the UI treats
  as recoverable produces a prompt the user can satisfy that changes nothing, forever.
- **`dev.swarm.phone.kek.wake` must be UNAFFECTED** — it is deliberately not invalidated by an
  enrollment change. Confirm background reconnect still works after 4d. If the wake KEK was also
  invalidated, the tier split is not doing what `Custody.kt` says it does.

**`[UNRUN]` 4e.** Recover by re-pairing, and record how many steps it took and whether the machine
side needed `swarm remote revoke` first. (It will: `BeginPairing` fail-fasts while a device is
registered.)

---

## 5. Real camera pairing

**`[UNRUN]` 5a.** Display the pairing QR with `swarm remote pair` on the machine's terminal and scan
it with the handset's camera. Record the ambient conditions, the distance, and how long the decode
took. ZXing + CameraX (ADR-007 B21) has never decoded a physical screen under real optics.

**`[UNRUN]` 5b.** Record whether the QR was decodable at the terminal's default font size, and at
what point it stopped being so. `MaxRelayURLLen` is 39 characters because the symbol must stay at QR
version 6 to draw in 80x24 — this step is the only check that the resulting symbol is decodable by a
real sensor and not merely renderable.

**`[UNRUN]` 5c.** Compare the SAS on both screens and confirm they match. Record both strings.

**`[UNRUN]` 5d.** Confirm the origin-display-and-confirm step appeared before joining (PB-PAIR-6),
and record what it displayed.

**`[UNRUN]` 5e. Negative control, and this is the one worth doing carefully:** point the camera at a
QR encoding a **different** relay destination and confirm the app displays that origin and requires
explicit confirmation rather than joining silently.

---

## 6. Real FCM — registration, delivery, and the states that only exist on hardware

**All of §6 is unrunnable until §1c exists.** Nothing in this repository has ever exchanged a byte
with Google.

**`[UNRUN]` 6a. Registration.** Launch the app and record that `onNewToken` fired with a real FCM
token, and that the relay stored it (check the relay's `tokens` bucket for the device's routing id).

**`[UNRUN]` 6b. Foreground delivery.** With the app in the foreground, trigger a state transition on
the machine and record that the wake arrived. Record the observed payload size — it must be
**exactly 78 bytes** (ADR-007 B20) — and confirm both key ids are zero.

**`[UNRUN]` 6c. Backgrounded delivery.** Background the app (which per ADR-007 B16 **disconnects**
the relay socket, so this is the normal state, not an edge case) and repeat. Record the latency from
machine-side transition to notification.

**`[UNRUN]` 6d. Doze.** Force the device into Doze (`adb shell dumpsys deviceidle force-idle`),
trigger a transition, and record whether the high-priority wake arrived and how long it took.
Record the exact Doze state you achieved — `IDLE` versus `IDLE_MAINTENANCE` — because a wake
delivered during a maintenance window is not a Doze wake.

> **What would falsely pass:** a quiet machine. If no transition fires, no push is expected and
> "nothing arrived" looks identical to a working system in an idle state. **Trigger a transition you
> can independently confirm happened on the machine side**, and record that confirmation alongside
> the handset observation.

**`[UNRUN]` 6e. Locked-device push.** With the handset **locked**, trigger a transition. Record: that
the notification appeared; that its content is `VISIBILITY_SECRET` and reveals no session name,
hostname, agent name or Group label on the lock screen; and that the app did **not** attempt to
decrypt session content — the wake tier must be sufficient, and the content KEK must still refuse.

**`[UNRUN]` 6f. After reboot.** Reboot, do **not** open the app, and trigger a transition. Record
whether the notification arrived. This is the one that most often fails on real devices and cannot
be observed anywhere else.

**`[UNRUN]` 6g. Token rotation.** Force a token rotation (clear app storage on the FCM side, or use
`FirebaseInstallations.delete()`), and record that the app re-registered on its next authenticated
reconnect and that delivery resumed.

**`[UNRUN]` 6h. Re-registration against an EMPTY relay store.** Restart the relay with a **fresh**
`db_path`, then confirm delivery resumes. Doing it against a *kept* store proves nothing — the relay
persists tokens, so a restart restores delivery with the phone doing nothing at all. This is the
amendment PB-PUSH-9 already carries; on hardware it must be honoured the same way.

**`[UNRUN]` 6i. Push disabled.** Turn a push category off from the app's settings, confirm the
machine stops sending that category, and confirm the token is deleted when push is disabled
entirely. **Note the recorded residual before you interpret a failure here:** `PushTokens.disable`
currently has no caller, so deletion-on-disable may not happen. Record what you observe either way.

---

## 7. Lifecycle on real hardware

**`[UNRUN]` 7a. Lock and unlock** mid-session. Record whether typing resumes without a re-prompt
inside the timed window, and with one outside it.

**`[UNRUN]` 7b. Process death.** `adb shell am kill dev.swarm.phone` mid-session, reopen, and record
that durable state survived: the same device id, no re-pair, sequence coordinates intact, and that
the fail-closed refusal of mutating ops clears on gateway reconnect. (Note the recorded residual:
reconcile adoption is not persisted, so every process death re-arms that refusal.)

**`[UNRUN]` 7c. Reboot.** Reboot the handset, reopen the app, and record the same. Then repeat
**without** unlocking first, and record whether the wake path works before first unlock.

**`[UNRUN]` 7d. `am force-stop`.** Explicitly **not** part of this gate — B31 moved it into PB-E2E-2
because an emulator can issue it. If you run it anyway, record it separately; it does not discharge
anything here.

---

## 8. The session itself, and radio handoff

**`[UNRUN]` 8a.** Observe the roster, launch a session, take control, and type. Record end-to-end
keystroke latency as observed by a human — the automated numbers are `phonecore → PTY`, not
`phone → PTY`, and were taken through Rosetta on an x86_64 Go binary.

**`[UNRUN]` 8b.** Revoke from the handset (`RevokeThisDevice`) and record that the machine reflects
it and that the app reaches `revoked`, not `repair_required` — the two share a remedy and not a
cause, and must read differently on screen.

**`[UNRUN]` 8c. Wi-Fi ↔ cellular handoff.** Mid-session, disable Wi-Fi and record: how long
reconnection took, whether the lease was severed (it must be — a disconnect severs rather than
pauses), whether buffered input resolved as *undelivered* rather than riding the reconnect, and
whether any keystroke was duplicated. Then re-enable Wi-Fi and repeat in the other direction.

**`[UNRUN]` 8d. Clock skew, because it is cheap here and product-fatal.** Set the handset's clock
**11 minutes fast** and record what happens. Expected from the recorded residual
(`docs/verification/remote-phaseB-residuals.md` §4.1): the phone goes **silently deaf to the entire
machine→phone plane** while `ConnectionState()` still reads `online`. Confirming that on hardware
turns a reasoned-about defect into an observed one. Restore the clock afterwards.

---

## 9. The result sheet — fill this in; do not summarise it

**A verdict without this table filled in is not a discharge of PB-E2E-5.**

### Device

| | |
|---|---|
| Make / model | |
| Android version / build / security patch | |
| Biometric modality enrolled | |
| Relay certificate: publicly trusted or self-signed | |
| Date, operator name | |

### Per-alias `KeyInfo` (step 2a) — one row each, no blanks

| Alias | securityLevel | strongBoxBacked | userAuthRequired | validityDuration (s) | invalidatedByEnrollment |
|---|---|---|---|---|---|
| `dev.swarm.phone.kek.wake` | | | | | |
| `dev.swarm.phone.kek.content` | | | | | |
| `dev.swarm.phone.gate.timed` | | | | | |
| `dev.swarm.phone.gate.peruse.input` | | | | | |
| `dev.swarm.phone.gate.peruse.take_control` | | | | | |
| `dev.swarm.phone.gate.peruse.revoke` | | | | | |
| `dev.swarm.phone.gate.peruse.kill_switch` | | | | | |
| `dev.swarm.phone.gate.peruse.launch` | | | | | |
| `dev.swarm.phone.gate.peruse.kill` | | | | | |

### Platform capability probe (step 2c)

| Capability | PRESENT / ABSENT / UNKNOWN | Consumed by any matrix row? |
|---|---|---|
| `KEYSTORE_AES_GCM` | | yes — both KEKs |
| `USER_AUTH_PER_USE` | | yes — the content KEK and the per-use gates |
| `KEYSTORE_X25519` | | **no** (see §0(b)) — but provisioning refuses without it |
| `KEYSTORE_ED25519` | | **no** (see §0(b)) — but provisioning refuses without it |

### Per-role achieved backing (step 2e)

| Role | Tier | Backing in the matrix | **Observed** | KEK security level | Fallback observed? |
|---|---|---|---|---|---|
| `NOISE_STATIC` | CONTENT | `KEYSTORE_WRAPPED` | | | |
| `RECIPIENT` | CONTENT | `KEYSTORE_WRAPPED` | | | |
| `COMMAND_SIGN` | CONTENT | `KEYSTORE_WRAPPED` | | | |
| `RELAY_AUTH` | WAKE | `KEYSTORE_WRAPPED` | | | |

### Attestation (step 2b)

| | |
|---|---|
| Chain verified to a Google root? | |
| Attestation security level | |
| If not obtained, why | |

### Step ledger

One row per `[UNRUN]` step above. **`not run` is a legitimate entry; a blank is not.**

| Step | Observed (what the device reported) | Verdict: hardware / fallback / refused / not run |
|---|---|---|
| 1a … 8d | | |

---

## 10. Findings this runbook already carries into the gate

Recorded now so the operator meets them as expectations rather than surprises.

1. **`insideSecureHardware` is never compared by the read-back** (§0(a)). A software-only KEK
   provisions cleanly. Nothing in the product will tell you; step 2a is the only place it surfaces.
2. **Provisioning refuses without `KEYSTORE_X25519` / `KEYSTORE_ED25519`, which no matrix row
   consumes** (§0(b)). Defensible as a fail-closed canary; still a plausible hard failure on a real
   device, over a capability the design does not use. Step 2c is deliberately first.
3. **The certificate pin is not applied on the handset's dial path.** `relay.Security` has no
   production caller. Use a publicly trusted certificate for this gate and record that you did —
   a pass over a trusted certificate says nothing about the pinned path (§1, and residuals §1.1).
4. **A phone >10 minutes fast goes silently deaf while reporting `online`** (§8d). Known from
   source; unobserved on hardware.
5. **`PushTokens.disable` has no caller**, so deletion-on-disable may not occur (§6i).
6. **PB-E2E-2 is RED** — no scanner, no SAS display, no confirm control, no keyboard reported by
   `android/gate/s19_pbe2e2_test.go`, and no instrumented source set. **§5 and §8 of this runbook
   may not be executable until that lands**, since they require in-app pairing and typing surfaces.
   Confirm PB-E2E-2 is green before scheduling a device session, or the gate will stall at step 5a.

---

## 11. Discharging the gate

PB-E2E-5 is discharged when **every** `[UNRUN]` tag above has been removed by the operator who
performed that step, §9's tables are complete with no blanks, and the result is committed under
`docs/verification/` with the operator named.

**A partial run discharges nothing.** B31's consequence clause is unconditional: until this gate
runs, Phase B is provisionally implemented. There is no partial credit, and an exclusion that let
the phase be called complete would be a waiver of the conclusion rather than of the test.

If steps fail, that is a **result**, not a blocked gate: record what failed, file it, and the gate is
discharged as *run with findings*. The one outcome that is not permitted is a gate reported as
passed on steps that still carry their `[UNRUN]` tag.
