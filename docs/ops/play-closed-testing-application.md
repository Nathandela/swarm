# Google Play — Closed Testing Application Pack

Everything needed to get `dev.swarm.phone` into a Play Console **closed testing** track,
with copy-pasteable answers for every form.

Facts below are read from the repository, not assumed. Where a Play *policy* detail may have
moved since this was written, it is marked **[VERIFY]** — the Console is authoritative.

---

## 0. Read this first: the blockers

None of these stop you filling in the forms. All of them stop you shipping a build testers can
use.

### Blocker 0 — the daemon pairing hang — **FIXED (ADR-007 B64)**

Recorded here because it is the defect that most shaped this sprint, and because its fence turned
out to be vacuous, which is worth knowing if you are judging how much the green suite means.

**The fix is in.** The pairing window is now enforced as well as announced. What follows describes
what was wrong, not what ships.

The owner scans the QR, the desktop shows the verification code, and the owner presses `y` on
the desktop **first** — that prompt is the one in front of them. From that moment, anything
that cancels the phone's side (rejecting the code, cancelling, or the 60-second pairing timer
expiring) parks the daemon **permanently**. `Pair` never returns, so the pairing is never
cleared, so **every later pairing attempt on that connection is refused "pairing already in
progress"**. There is no cancel operation; only dropping the owner connection escapes.

Two causes compose, and neither change was wrong alone: the production pairing context has no
deadline (`internal/protocol/server.go:2098` — the window value is computed two lines away and
never applied), and the abort frame is sent on the context that was just cancelled
(`internal/remote/pairing/pairing.go:665`), so **the abort is never delivered on any production
path that produces it**.

Both existing tests for this are vacuous. One injects its own 2-second deadline into `Pair` and
takes its green entirely from a safety property production never supplies — changing the literal
to ten minutes fails it by name. The other drives a rejection shape the shipped phone cannot
produce.

**Caveat that survives the fix (ADR-007 B69(3)):** the deadline half of it is UNFENCED. Deleting
the deadline entirely leaves both test suites green, because a courtesy abort resolves the
scenario first and absorbs the test. A replacement fence is in progress. Treat the pairing path as
fixed-but-lightly-tested, not as proven.

### The rest

Ordered by how long they take.

| # | Blocker | Where | Effort |
|---|---|---|---|
| 1 | ~~No launcher icon.~~ **DONE** — the adaptive launcher now uses the same generated Atmospheric Swarm trajectories as the Play listing, over the app's Slate background token. The transparent source, mask-safe inset, and adaptive/monochrome wiring are gated; the separate 512x512 store icon and 1024x500 feature graphic are in `docs/ops/play-assets/`. | — | done |
| 2 | ~~No Firebase project/config.~~ **BUILD PROVISIONING DONE** — project `swarm-8404f` and Android app `dev.swarm.phone` exist; the local gitignored `google-services.json` applies the Google Services plugin, and `bundleRelease` refuses an absent, development or wrong-app config. **Physical delivery remains unvalidated:** a successful token registration and wake on the Play-signed handset are still required. | `android/app/build.gradle.kts`, physical-handset gate | device run |
| 3 | **Release signing material is operator-supplied and absent.** `requireReleaseSigning` fails the build by design if unset — good, but you must create the keystore. | env / `~/.gradle/gradle.properties` | 20 min |
| 4 | **No hosted privacy policy.** Play requires a public URL before the listing can be submitted. Draft in §9 below, published page at `docs/ops/privacy-policy/index.html`. **Gated on a merge to `main`:** GitHub Pages is configured to deploy from `main`/`docs`, and the whole ops track exists only on `design-system-substrate` — `git log main -- docs/ops/privacy-policy` returns nothing — so the URL 404s until that branch merges. | external hosting + a merge | 30 min |
| 5 | **Two Phase B requirements are genuinely NOT MET** (`PB-PAIR-4`, `PB-NET-4`), one is unsatisfiable without physical hardware (`PB-E2E-2`), and one requirement's entire subject has left the product: `PB-SEC-2` (the per-use biometric gate) is **VOID**, not merely unmet — ADR-007 B133 (2026-07-31) deleted all phone-side user authentication, so there is nothing left for the requirement to grade against. `PB-INPUT-4`, previously listed here as NOT MET, no longer is: ADR-007 B92 (2026-07-30) adjudicated it — the retry clause presupposed a queueing mechanism ADR-007 D7 forbids for live input, so the clause was withdrawn, and production's actual behaviour (never resend) satisfies what remains (`docs/specifications/remote-phaseB-requirements.md:502`). `PB-PAIR-4` (a half-paired state is reachable) remains open. `PB-NET-4` (the reconnect/resilience row) also remains open, but not for the reason this bullet used to give: its queue self-contradiction was resolved by the same kind of amendment in ADR-007 B90 (2026-07-30) — the current reason it is NOT MET is unfenced evidence in the resilience half (nothing ties the reconnect delay to the stated backoff, nothing names re-auth after reconnect, and the connection-state-surfaced clause is covered only by an unrelated test's side effect — ADR-007 B113/B114). A related NEW requirement, `PB-NET-8` (added 2026-07-31, ADR-007 B120), covers the gateway's OWN reconnect — nothing required it before, and it was found completely absent. **`PB-PAIR-4` is worse than first recorded: a half-pair is reachable in BOTH
directions from an ordinary clock, with no attacker — pairing near the 60-second expiry is enough.
Give your testers the recovery in writing, because it will happen.** Separately `PB-E2E-5` (real-hardware validation of camera, FCM delivery, Doze, Keystore attestation — "real biometrics" left this list when ADR-007 B133 removed the feature it would have validated) is **deferred and unvalidated**. | `docs/verification/remote-phaseB-residuals.md`, `docs/adr/ADR-007-remote-access.md` (B90, B92, B120, B133) | in progress |

**On #5 and closed testing.** Closed testing is genuinely the right venue to burn down
`PB-E2E-5` — it is the only way to get real handsets, real Doze, real FCM. That argues *for*
shipping to a closed track soon.

For a **trusted, closed loop of people you know**, `PB-PAIR-4` and `PB-NET-4` are defensible to
defer — but know what you are deferring. `PB-SEC-2` is not on this list to defer, because there
is nothing left of it to land:

- **`PB-SEC-2` is VOID, not deferred.** ADR-007 B133 (2026-07-31) removed the per-use biometric
  gate — and every other form of phone-side user authentication — as a deliberate owner decision,
  not a bug awaiting a fix: *"the trust boundary is the wire ... the phone endpoint is trusted the
  way the computer endpoint has always been trusted."* **The accepted residual risk: a stolen,
  unlocked phone gives the holder full control of agents that edit code on your laptop — no
  prompt stands between them and take-control, type, kill or launch.** The only surviving
  mitigation is `swarm remote off` or a device revoke, issued **from the computer**. Put that in
  writing for your testers before they install; "lock your phone" is no longer the whole answer
  it used to be.
- **`PB-PAIR-4`** — a half-pair is reachable from an ordinary clock near the 60-second pairing
  expiry, with no attacker required (see the blocker row above). Give your testers the recovery
  in writing.
- **`PB-NET-4`** — the resilience row still has unfenced evidence gaps (reconnect-delay-to-backoff,
  re-auth-after-reconnect, connection-state-surfaced — ADR-007 B113/B114). The failure mode is a
  stuck "reconnecting" UI, not a security hole, so it is defensible to defer for twelve known
  testers who can restart the app.

(`PB-INPUT-4` and `PB-PUSH-9` were on this list and are now MET — see the blocker row above for
`PB-INPUT-4` (ADR-007 B92); refusing self-consent restored the push-token deletion that a
self-edge had disabled for `PB-PUSH-9`.)

### The relay: the boot-bricking defect is FIXED, but keep it private anyway

**FIXED as of ADR-007 B70** (commit `8861488`), mutation-verified in both halves. What follows is
what was wrong, and why the recommendation below still stands.

`token_register` accepts an attacker-chosen token with **no length check** — the only bound is the
1 MiB frame limit. The per-identity rate limit does not bind, because minting a fresh identity is
free and earns a fresh window. No pairing, no consent signature and no victim are required.

- **~1.79 MiB of unreclaimable disk per call. ~1 GiB/min. ~1.5 TB/day from one IP.**
- **Worse: the relay loads the entire token bucket into memory at startup**, deliberately
  fail-closed. A filled store means the relay **OOMs on every boot** — a crash loop whose only
  recovery is deleting `relay.db`, **which destroys every pairing edge, consent and token you
  have.**

Two more unauthenticated defects sit beside it (B70-C2, B70-C3): one connection can occupy the
entire rendezvous table **and keeps the slots after disconnecting**, so two sources mean no phone
can pair at all; and a connection parked mid-pairing has no deadline, no quota and no accounting.

All five are now fixed and fenced: the token is length-bounded on **both** the write path and the
boot path (a disk bound alone would have left the OOM), rendezvous slots are released on disconnect
and reclaimed without needing a further create, a parked connection is bounded and metered,
presence answers `unknown` to a stranger, and the presence sweep charges its pushes against the
push window.

**Run it on a private network anyway for the closed test** — localhost, Tailscale, WireGuard or an
SSH tunnel. Not because of these five, but because of what the committee found underneath them:
**every per-identity bound in the relay is a bound on nothing, since minting an identity is free.**
One connection was measured leaving 6,000 permanent rows with every existing limit respected
throughout. Global per-bucket caps and connection-rate admission control are recorded as production
blockers and are **not** done.

Also worth doing while testing: **alarm on `relay.db` size.** Unexpected growth is the first
symptom of every defect in this class, and there have been five.

### The other relay items — **FIXED (ADR-007 B61), with one caveat**

Self-consent is refused, the ceremony ID is length-bounded, and the retired-consents bucket is
bounded. That closed a 72 GB/day disk drain **and** a defect where a device could make its own
pairing *permanently unrevokable* by choosing an oversized ceremony ID.

**Caveat (ADR-007 B69(4)):** that bound is **per pair, not global**. Minting fresh identities is
free, so total durable growth is still unbounded — which is the same root as the `token_register`
defect above, and why the warning there stands regardless.

**Verified 2026-08-02** (`agents-tracker-cwz`), against Google's own current page, last updated
2026-07-15: <https://developer.android.com/google/play/requirements/target-sdk>. Today, **35 is
still the accepted floor for a new app** — the stricter requirement is not yet in force. **It
changes on August 31, 2026**: from that date, new apps and app updates must target Android 16
(API level 36) or higher to be submitted at all (an extension to November 1, 2026 is requestable
in Console if needed). **Closed/internal testing tracks are not exempt from this** — the only
exemption is for apps that are permanently private to an organization, which this is not — so if
the upload in `agents-tracker-2qm` lands on or after August 31, it needs 36 regardless of track.

**The bump was made on 2026-08-02 (`agents-tracker-xfw`) rather than raced against the date**,
because discovering the requirement at upload time would mean re-running the whole Kotlin
verification under a deadline. `SWARM_ANDROID_TARGET_SDK` and `SWARM_ANDROID_COMPILE_SDK` in
`android/toolchain.env` are both **36**, `SWARM_ANDROID_BUILD_TOOLS` is **36.0.0**, and
`android/supported-versions.tsv` carries `36 / Android 16 / target` with the reasoning for each
Android 16 behaviour change written out row by row. The Android Gradle Plugin pinned in
`android/build.gradle.kts` (8.13.2) took `compileSdk` 36 without a plugin upgrade, as its 8.13.0
release notes said it would. No app code changed: the app never set
`windowOptOutEdgeToEdgeEnforcement`, never overrode `onBackPressed()`, declares no
`screenOrientation` and holds no foreground service, so the four behaviour changes that gate on
targeting 36 have no subject here.

---

## 1. App identity — the facts the Console will ask for

| Field | Value | Source |
|---|---|---|
| Application ID (package) | `dev.swarm.phone` | `app/build.gradle.kts:150` |
| App name (device label) | `swarm` | `res/values/strings.xml` |
| versionCode / versionName | `1` / `0.1.0` | `app/build.gradle.kts:153-154` |
| minSdk | **33** (Android 13) | `toolchain.env` → `SWARM_ANDROID_MIN_SDK` |
| targetSdk / compileSdk | **36** (Android 16) | `toolchain.env` |
| ABIs in the native AAR | `arm64-v8a`, `x86_64` | `toolchain.env` → `SWARM_AAR_ABIS` |
| Foreground services | **none declared** | manifest — by design (ADR-007 B16) |
| Analytics / crash reporting | **none** | `android/dependency-inventory.tsv`, gate-enforced |

**Permissions requested — the MERGED set, which is eight.** Play receives the merged manifest,
not the four lines in `app/src/main/AndroidManifest.xml`. This section listed those four until
2026-08-02, which understated it by half. Each row below names the manifest that actually
contributes the line, read out of the merger's own attribution file
(`app/build/intermediates/manifest_merge_blame_file/release/processReleaseMainManifest/manifest-merger-blame-release-report.txt`)
rather than inferred from the dependency list:

| Permission | Contributed by | Why |
|---|---|---|
| `android.permission.INTERNET` | app manifest | the daemon connection |
| `android.permission.CAMERA` | app manifest | QR pairing. `uses-feature required="false"`, so camera-less devices still install; there is a manual-entry fallback |
| `android.permission.POST_NOTIFICATIONS` | app manifest | push notifications |
| `android.permission.RECEIVE_BOOT_COMPLETED` | app manifest | re-arm after reboot |
| `android.permission.ACCESS_NETWORK_STATE` | `com.google.firebase:firebase-messaging:24.1.2` | FCM |
| `android.permission.WAKE_LOCK` | `com.google.firebase:firebase-messaging:24.1.2` | FCM. The library's own comment: "required by older versions of Google Play services to create IID tokens" |
| `com.google.android.c2dm.permission.RECEIVE` | `com.google.firebase:firebase-messaging:24.1.2` | FCM message delivery |
| `dev.swarm.phone.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION` | `androidx.core:core:1.13.0` | app-private, `protectionLevel="signature"`; guards receivers registered through `ContextCompat.registerReceiver`. Never shown to a user |

**`play-services` contributes none of them.** All three FCM lines come from `firebase-messaging`
itself; Play Services appears only as the *reason* in the `WAKE_LOCK` comment, not as the
contributor.

**It was ten until 2026-08-02.** `androidx.biometric:biometric:1.1.0` also contributed
`USE_BIOMETRIC` and `USE_FINGERPRINT` — for the per-use biometric gate ADR-007 B133 had already
deleted, while the privacy policy was being corrected to stop claiming biometric protection. The
dependency was removed (bead `agents-tracker-i3u6`) and the merged manifest re-read to confirm
both permissions are gone and nothing else moved.

**None of these triggers a Play sensitive-permission declaration form** — and that now follows
from the set Play actually receives rather than from a subset of it. No `QUERY_ALL_PACKAGES`, no
SMS/Call Log, no `MANAGE_EXTERNAL_STORAGE`, no location, no
`REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`, no `REQUEST_INSTALL_PACKAGES`, no `USE_EXACT_ALARM`, no
AccessibilityService, no `READ_MEDIA_*`, and no foreground service type. `CAMERA` and
`POST_NOTIFICATIONS` raise runtime prompts, which is not the same thing as a declaration form;
the other six are `normal` or `signature` and are granted without any user interaction. That is
a materially easier review than most apps get — do not add any of them casually.

**Re-derive this table after any dependency change rather than trusting it.** A library
contributes permissions whether or not a line of Kotlin calls it, which is exactly how the two
biometric entries outlived the feature that justified them:

```bash
cd android && ./gradlew :app:processReleaseManifestForPackage
grep -o 'uses-permission[^/]*android:name="[^"]*"' \
  app/build/intermediates/packaged_manifests/release/processReleaseManifestForPackage/AndroidManifest.xml \
  | sed 's/.*android:name="//;s/"//' | sort
```

---

## 2. Build and upload

Play requires an **AAB** (Android App Bundle) for new apps, not an APK.

```bash
cd android
. ./toolchain.env          # nothing Android is on PATH without this

# One-time: create the upload keystore. Keep the .jks OUT of the repo.
keytool -genkeypair -v \
  -keystore ~/.keystores/swarm-upload.jks \
  -alias swarm-upload \
  -keyalg RSA -keysize 4096 -validity 10000

# Signing material is read from env or ~/.gradle/gradle.properties, never a tracked file.
export SWARM_RELEASE_KEYSTORE="$HOME/.keystores/swarm-upload.jks"
export SWARM_RELEASE_KEYSTORE_PASSWORD='...'
export SWARM_RELEASE_KEY_ALIAS='swarm-upload'
export SWARM_RELEASE_KEY_PASSWORD='...'
# The exact provider-issued, bare HTTPS URL shown by `gcloud run services describe`.
export SWARM_PUSH_GATEWAY_URL='https://your-service-identifier.run.app'

./build-aar.sh             # rebuild the gomobile AAR first
./gradlew :app:bundleRelease
# → app/build/outputs/bundle/release/app-release.aab
# → app/build/outputs/bundle/release/app-release.aab.swarm-firebase-provenance.json
```

Publish only with `cmd/swarm-publish`, first with `--dry-run` and then, after explicit approval,
without it. The Play Console's manual AAB upload control does not validate the adjacent provenance
sidecar and is not an allowed release path; see `docs/operations/release-signing.md` §7.

**Enrol in Play App Signing** (mandatory for AAB). Google holds the app signing key; your
`swarm-upload.jks` is only the *upload* key. If you lose the upload key you can request a
reset; if you opt out and lose the app signing key, the app is unrecoverable.

---

## 3. Store listing — copy-paste

**App name** (30 char max)

```
swarm
```

**Short description** (80 char max)

```
Watch and steer your coding agents from your phone. End-to-end encrypted.
```

**Full description** (4000 char max)

```
swarm lets you keep an eye on the coding agents running on your own computer, from your
phone.

Agents work for a long time and then stop to ask you something. If you are not at your desk,
they wait. swarm shows you that question on your phone, shows you what the agent is doing, and
lets you answer -- so the work carries on.

WHAT IT DOES

- See every agent session running on your machine, live.
- Read the terminal output as it happens.
- Answer an agent that is blocked waiting on a decision.
- Stop a session that has gone the wrong way.

HOW IT IS BUILT

swarm pairs your phone directly with your own computer. Pairing is confirmed on both ends
with a short verification code that you check yourself, so you know the device you paired
with is the one in front of you.

Everything between your phone and your machine is end-to-end encrypted. The relay that
carries the traffic cannot read it: it sees encrypted bytes and routing information, and
nothing else. The design treats that relay as hostile and is built so that it does not
matter.

Trust in this design is anchored at the wire between your phone and your computer, not at a lock
screen: the two devices are paired once, verified by a short code you check yourself, and both
ends are then trusted the way a second monitor on your own desk would be. If your unlocked phone
is ever lost or stolen, turn off remote access or revoke the device from your computer -- that is
the control that stops it.

The app contains no analytics, no telemetry, no crash reporting and no advertising SDKs. It
collects nothing about you.

WHAT YOU NEED

- A computer running the swarm daemon and the swarm remote gateway.
- Android 13 or later.

swarm is a tool for developers who run coding agents on their own hardware. It is not a chat
client and it is not a remote desktop -- it is a window onto processes you already own.
```

*(Trim the last paragraph if you would rather not set that expectation in a closed test.)*

> **This listing promised push until 2026-08-02, before the build was provisioned for it.** The
> bullet *"Get a notification the moment a session needs you."* was removed, and the opening
> paragraph's "swarm sends that question to your phone" became "shows you that question on your
> phone". Production Firebase build provisioning now exists and Play bundles fail closed without
> it, but that proves only configuration: real token registration, FCM delivery and Doze behaviour
> still need a Play-signed handset run. A store listing is a claim made to Google and every tester,
> so configuration alone is not evidence for restoring the promise.
>
> **Restore both when, and only when, a wake actually arrives on the Play-signed handset**
> (§10 item 2). Background wake is "the feature the phone exists for" per §0, so this is a listing
> that should get its bullet back once the physical claim is true.

**Graphic assets — all mandatory assets are present in the repo**

| Asset | Spec |
|---|---|
| App icon | 512 × 512 PNG, 32-bit, ≤ 1 MB, no alpha |
| Feature graphic | 1024 × 500 PNG/JPEG |
| Phone screenshots | **2–8 required. THESE NEED A PHYSICAL HANDSET — see below.** 16:9 or 9:16, each side 320–3840 px |
| 7" / 10" tablet screenshots | optional unless you declare tablet support |

The launcher icon (blocker #1) is **done** — an adaptive icon using the approved transparent
Atmospheric Swarm mark over the app's own Slate background token. Its 24dp foreground inset keeps
the complete generated trail inside Android's guaranteed mask-safe circle, and the same layer is
declared for themed icons. The Console's 512×512 field remains a separate PNG upload; its matching
asset is `docs/ops/play-assets/play-store-icon-512.png`.

### Screenshots need a physical Android 13+ handset — you cannot use an emulator

This is a hard dependency and the most likely thing to slip the sprint, so plan for it now.

The app **cannot start on a standard emulator, and that is correct behaviour, not a bug**. The
emulator's keymaster reports `SECURITY_LEVEL_SOFTWARE`; `PB-KEY-8`'s hardware-downgrade refusal
fails closed before any screen renders. That is recorded as `PB-E2E-2` — a requirement in direct
conflict with `PB-KEY-8`, resolved in `PB-KEY-8`'s favour because the alternative is an app that
silently accepts software-backed keys.

Consequences:

- Play's mandatory 2–8 phone screenshots must come from a **real device running Android 13+**.
- So must any hands-on verification of the pairing flow.
- The same handset is what finally lets you close `PB-E2E-5` — real camera, real FCM delivery,
  real Doze, real Keystore attestation ("real biometrics" left this list when ADR-007 B133 removed
  the feature it would have validated) — which is the largest unvalidated area in the project.

If you do not already have an Android 13+ device to hand, **acquiring one is on the critical
path**, ahead of most of the Console work.

**Categorisation**

- App category: **Tools**
- Tags: developer tools, remote access, productivity
- Contact email: *(your public support address — it is displayed on the listing)*

---

## 4. Data safety form — answers

This is the form most likely to get you rejected if answered carelessly. Answers below reflect
what the code actually does.

**Does your app collect or share any of the required user data types?** → **Yes**
*(Because of the FCM registration token. Answering "No" while shipping
`com.google.firebase:firebase-messaging` is the kind of mismatch Play's automated scan
catches.)*

**Is all of the user data collected by your app encrypted in transit?** → **Yes**
*(The relay socket is Go `crypto/tls` inside the gomobile `.so`, plus a Noise XXpsk0 channel
inside that. Note that the manifest's `usesCleartextTraffic="false"` does NOT cover this
socket — it governs the Java/WebView stack only. PB-NET-2 is the control that does.)*

**Do you provide a way for users to request that their data be deleted?** → **Yes**
*(Revoking the device from the desktop, or "revoke this device" in-app, deletes the push token
and purges relay-side state. Uninstalling removes all local state — there is no cloud copy.)*

### Data types to declare

| Type | Collected | Shared | Purpose | Optional? |
|---|---|---|---|---|
| **Device or other IDs** | Yes | Yes → Google (FCM) | App functionality | Required |

That is the only entry that is unambiguous. Two more need a judgement call:

**Terminal / session content.** It leaves the device — but end-to-end encrypted, to a relay
that cannot decrypt it, en route to the user's own machine. Play documents an exemption for
end-to-end encrypted data, but the exact wording of that carve-out has moved more than once.
**[VERIFY]** — I am not confident enough in the current phrasing to tell you to rely on it.
The conservative answer, which cannot be wrong: declare it as collected.

- Type: **App activity → Other user-generated content**, or **App info and performance →
  Other app data**
- Collected: Yes · Shared: No · Purpose: App functionality · Required

**Cryptographic keys and pairing state.** Never leaves the device. `allowBackup="false"` plus
`dataExtractionRules` excluding `domain="root"` from both cloud backup and device-to-device
transfer. **Do not declare** — on-device-only data is explicitly exempt.

### What to answer "No" to

Personal info (name, email, address, phone, race, political/religious belief, sexual
orientation, other IDs) · Financial info · Health and fitness · Messages · Photos and videos ·
Audio files · Files and docs · Calendar · Contacts · **Location** · Web browsing history ·
Purchase history · **App activity: app interactions, in-app search history, installed apps,
other actions** · Crash logs · Diagnostics · Performance data.

The "no analytics, no crash reporting" claim is not a promise — `android/dependency-inventory.tsv`
enumerates the full resolved dependency closure and `android/gate/s18_sec8_depinventory_test.go`
matches a denylist against it. You can defend that answer with an artifact.

---

## 5. Content rating (IARC) questionnaire

Category: **Utility, Productivity, Communication, or Other**

| Question | Answer |
|---|---|
| Violence, sexuality, language, controlled substances, gambling, horror | **No** to all |
| Does the app allow users to interact or exchange content with other users? | **No** — the phone communicates with the user's own machine. There is no user-to-user surface. |
| Does the app share the user's current location with other users? | **No** |
| Does the app allow users to purchase digital goods? | **No** |
| Does the app contain or allow unrestricted access to the internet? | **[VERIFY] — read this one carefully.** Strictly the app renders terminal output from a machine the user owns and controls; it embeds no browser and no arbitrary URL fetch. But a terminal *is* a general-purpose surface. If in doubt answer **Yes** — a stricter rating costs you nothing here and a rating obtained by an answer a reviewer disagrees with can be invalidated later. |
| Does the app natively allow users to communicate via text/voice/video? | **No** |

Expect **Everyone / PEGI 3 / ESRB Everyone**, possibly **Teen** if you answer the internet
question Yes.

---

## 6. App content declarations

| Declaration | Answer |
|---|---|
| **Privacy policy URL** | Required — see §9. **PENDING PUBLICATION**: page drafted at `docs/ops/privacy-policy/index.html`, not yet hosted. Will be `https://nathandela.github.io/swarm/ops/privacy-policy/` once GitHub Pages is enabled (steps in `docs/ops/privacy-policy/README.md`). |
| **Ads** — does your app contain ads? | **No** |
| **App access** — is any functionality restricted? | **Yes → All functionality restricted.** See the reviewer instructions below. This is the single most important declaration for this app. |
| **Content ratings** | §5 |
| **Target audience** | **18+** only. Do **not** tick any bracket under 13, or you enter the Families programme and its whole extra policy surface. |
| **Appeal to children?** | **No** |
| **News app?** | **No** |
| **COVID-19 contact tracing/status?** | **No** |
| **Data safety** | §4 |
| **Government app?** | **No** |
| **Financial features** | **None of these** |
| **Health apps** | **No** |

### App access — reviewer instructions (paste verbatim)

This matters more than it looks. A Play reviewer opening this app sees a pairing screen and
can go no further, because pairing requires a desktop machine running the swarm daemon. Apps
that reviewers cannot exercise get rejected for it.

```
This app is a remote control for coding-agent sessions running on the user's own computer.
It cannot function standalone: all functionality requires pairing with a machine running
the swarm daemon and the swarm remote gateway, over a relay.

There are no user accounts, no username, and no password. Access is established by a
one-time pairing ceremony in which the phone and the machine each display a short
verification code that the operator compares and confirms on both devices.

Because pairing requires physical possession of both devices at the same moment, we cannot
supply demo credentials that would let a reviewer complete it unattended.

To review the full app, please contact <YOUR EMAIL> and we will arrange a live pairing
session against a machine we will stand up for that purpose, at a time of your choosing.

Without pairing, the reviewable surface is: the pairing screen, the camera permission
prompt, the notification permission prompt, the manual pairing-code entry fallback, and
the settings screen.
```

Replace `<YOUR EMAIL>` before submitting.

---

## 7. Export compliance / encryption

Play does not present the same encryption questionnaire Apple does, but US export law still
applies and the Console asks about it in some territories. The facts:

- The app uses encryption for **authentication and confidentiality of the user's own data in
  transit**. It is not a cryptographic product sold as such.
- Primitives: TLS (Go `crypto/tls`), Noise XXpsk0 handshake, X25519 via Android Keystore
  (KeyMint) on API 33+, AEAD for content sealing.
- All standard, published algorithms. No proprietary cryptography.
- This normally qualifies for the **ECCN 5D992.c mass-market** classification and the
  self-classification report route rather than a licence.

**[VERIFY]** — I am not qualified to give you an export-control determination, and this is a
legal call, not a technical one. If you distribute from the US or to sanctioned destinations,
get it confirmed. The technical facts above are accurate and are what a lawyer will ask for.

---

## 8. Setting up the closed test

1. **Play Console → Testing → Closed testing → Create track.** Name it `alpha`.
2. **Testers**: create an email list, or use a Google Group. Testers must opt in via the
   share link before they can install.
3. **Countries**: pick explicitly; a closed track with no country selected reaches nobody.
4. **Publish the AAB with the guarded `swarm-publish` CLI**, add release notes, and confirm the
   committed version in Console. Never use the Console's manual AAB upload control.
5. Testers install via the opt-in URL. Propagation takes up to a few hours on the first
   release.

### The graduation rule — plan for it now

**[VERIFY]** For **personal / individual** developer accounts created after **13 Nov 2023**,
Google requires a closed test with a minimum number of testers **continuously opted in for
14 days** before you can apply for production access. The figure has been **12 testers**, and
Google has adjusted both the count and the mechanics more than once.

Two consequences worth acting on before you invite anyone:

- **Recruit above the minimum.** The count is of testers *continuously opted in*. Someone who
  opts out on day 9 can reset your clock. Aim for 20 if the requirement is 12.
- **Organisation accounts are exempt.** If this is going to be a company product, registering
  the developer account as an organisation rather than an individual removes the requirement
  entirely. Worth deciding before you create the account, because changing account type later
  is painful.

Check the exact current numbers on the Console's own dashboard — it shows your progress
against whatever rule is live for your account.

---

## 9. Privacy policy — draft

**Status: published as a corrected static page, not yet hosted.** The final copy lives at
`docs/ops/privacy-policy/index.html`; publishing steps are in
`docs/ops/privacy-policy/README.md`. **PENDING PUBLICATION** — the URL will be:

```
https://nathandela.github.io/swarm/ops/privacy-policy/
```

until someone enables GitHub Pages for this repo (steps in the README above; nobody has run
them yet). Record that URL in §6's "Privacy policy URL" row once the page is confirmed live.

The final page corrects three claims this draft below still makes, verified against source
during that pass:

- **Biometric protection — removed, not published.** ADR-007 B133 (`docs/adr/ADR-007-remote-access.md:7748`,
  2026-07-31) deleted all phone-side user authentication; `PB-SEC-2` is now VOID
  (`android/app/src/main/kotlin/dev/swarm/phone/keys/Provisioning.kt:19`,
  `KeystoreSpecs.aesGcm` in the same package). There is no `BiometricPrompt` anywhere in
  `android/app/src/main/kotlin/`. The published page does not claim biometric protection of
  keys or sensitive actions; only this draft below still does.
- **Push token — timing corrected.** `PushTokens.requestInitialToken` is called
  unconditionally from `SwarmApplication.onCreate` (not gated on a notifications toggle).
  Config-free development builds still handle Firebase's unavailable state, while a Play bundle
  now requires the production Firebase config. The published page phrases token collection as
  conditional on push being available because build provisioning is not proof that Google issued
  a token on the installed Play-signed app.
- **Relay retention — quantified.** The relay purges undelivered mailbox items after a fixed
  7-day cap (`internal/remote/relay/config.go:106`, `server.go:1803` `SweepRetention`), not
  merely "as long as needed" — the published page states the figure.

The relay-operator blank below (`[WHO OPERATES IT]`) is resolved on the published page as
"whoever operates the computer you pair with" — the codebase has no central relay service; a
machine's owner runs `swarm-relay` themselves and provisions the URL via `swarm remote init`
(`internal/remote/relaycfg/relaycfg.go`, `docs/operations/relay-runbook.md`). **If the closed
test in fact routes testers through a relay the developer operates centrally**, that is a
one-sentence addition the published page still needs — confirm before relying on this policy
for real testers.

Host at a stable public URL (GitHub Pages is fine). Replace the bracketed fields.

```markdown
# swarm — Privacy Policy

Last updated: [DATE]

swarm ("the app") is an Android client that lets you monitor and control coding-agent
sessions running on a computer you own.

## The short version

The app collects no personal information about you. It has no accounts, no analytics, no
advertising and no crash reporting. The only data it transmits off your phone is (a) a push
notification token, and (b) end-to-end encrypted traffic to your own computer.

## What is collected

**Push notification token.** When you enable notifications, Google Firebase Cloud Messaging
issues this installation a token. It is sent to the relay server so that your computer can
wake your phone. It identifies this app installation, not you. It is deleted when you revoke
the device or turn notifications off.

**Session content, end-to-end encrypted.** Terminal output and the input you send travel
between your phone and your computer through a relay server. This traffic is end-to-end
encrypted. The relay carries encrypted bytes and the routing information needed to deliver
them. It cannot read the content, and the app is designed on the assumption that the relay
is hostile.

## What is not collected

No name, email address, phone number or postal address. No location. No contacts, calendar,
photos, audio or files. No browsing history. No advertising identifier. No analytics or
usage telemetry of any kind. No crash reports.

## What stays on your device

Cryptographic keys, pairing records and session state never leave your phone. Cloud backup
and device-to-device transfer are both disabled for this app, so this data is not copied to
Google's servers or to a new handset during setup.

~~Sensitive keys are held in the Android Keystore and are protected by your device biometric.~~
**SUPERSEDED (see the status note above §9): ADR-007 B133 removed all phone-side biometric
protection. The published page at `docs/ops/privacy-policy/index.html` does not carry this
sentence — do not copy it from here.**

## Permissions

- **Camera** — to scan the pairing QR code. There is a manual-entry alternative; you can
  decline this permission and still use the app. No image is stored or transmitted.
- **Notifications** — to alert you when a session needs your attention.
- **Internet** — to reach the relay.
- **Run at startup** — declared so the app can reconnect to your computer on its own after a
  reboot. This is not implemented yet: the app currently does nothing when your phone restarts,
  and you open it yourself.

## Third parties

**Google Firebase Cloud Messaging** delivers notifications and receives the token described
above. Google's handling is governed by its own privacy policy at
https://firebase.google.com/support/privacy.

The relay server is operated by [WHO OPERATES IT]. It stores encrypted message payloads only
for as long as needed to deliver them, along with the routing metadata required to do so.

No data is sold, and none is shared with anyone else.

## Deleting your data

Revoke the device — from the app, or from the swarm interface on your computer — to delete
its push token and purge its relay-side state. Uninstalling the app removes everything held
locally. There is no server-side copy of your content to request.

## Children

swarm is a developer tool and is not directed at anyone under 18.

## Changes

Material changes will be posted here with an updated date.

## Contact

[YOUR EMAIL]
```

`[WHO OPERATES IT]` is a real decision, not a blank to fill casually. If you run the relay,
say so — it is a point in your favour, and it is also a commitment about retention you are
then making publicly.

---

## 10. Order of work

**Before you open the Console** (~half a day)

1. Draw the launcher icon; wire `android:icon`; generate the 512×512 store icon.
2. Confirm the gitignored production `google-services.json` is present, run
   `:app:requireProductionFirebaseConfig`, and then prove token registration plus one real wake on
   the Play-signed handset. Project/app creation and Gradle wiring are done; provider delivery is
   the remaining claim.
3. Create the upload keystore; verify `./gradlew :app:bundleRelease` produces a signed AAB.
4. **Merge this branch to `main`**, then host the privacy policy. The merge is not bookkeeping:
   Pages serves from `main`/`docs` and `docs/ops/privacy-policy/` does not exist there, so the
   policy URL 404s and the listing cannot be submitted until it does
   (`docs/ops/privacy-policy/README.md`).
5. Write the tester-facing risk notice and hand it out before anyone installs: `PB-SEC-2` is VOID
   (ADR-007 B133 — no per-use gate exists; `swarm remote off` / revoke from the computer is the
   only mitigation for a lost unlocked phone), plus `PB-PAIR-4`'s half-pair recovery steps.

**In the Console** (~2 hours)

6. Create the app. Complete §4, §5, §6 from the answers above.
7. Set up Play App Signing; publish the AAB to the closed track with the guarded
   `swarm-publish` CLI, never the Console's manual upload control.
8. Add testers — above the minimum, per §8.

**Then**

9. Capture screenshots from a real device once testers are on it.
10. Use the closed test to burn down `PB-E2E-5`: real camera, real FCM delivery, real Doze, real
    Keystore attestation ("real biometrics" left this list when ADR-007 B133 removed the feature).
    It is the only environment where those can be validated, and it is currently the largest
    unvalidated area in the project.

---

*Generated from the repository at commit `9ff52d5`. Every technical fact is sourced from the
tree; every **[VERIFY]** marks a Play policy detail that changes and should be confirmed in
the Console.*
