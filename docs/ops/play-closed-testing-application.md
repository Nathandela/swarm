# Google Play — Closed Testing Application Pack

Everything needed to get `dev.swarm.phone` into a Play Console **closed testing** track,
with copy-pasteable answers for every form.

Facts below are read from the repository, not assumed. Where a Play *policy* detail may have
moved since this was written, it is marked **[VERIFY]** — the Console is authoritative.

---

## 0. Read this first: five blockers

None of these stop you filling in the forms. All of them stop you shipping a build testers can
use. Ordered by how long they take.

| # | Blocker | Where | Effort |
|---|---|---|---|
| 1 | **No launcher icon.** `AndroidManifest.xml` declares no `android:icon`, and `app/src/main/res` has no `mipmap` directories. The app installs with the default Android robot. | `android/app/src/main/res/` | 30 min |
| 2 | **No Firebase project.** `google-services.json` is absent and the `google-services` plugin is deliberately not applied. `PushTokens.requestInitialToken` catches the resulting `IllegalStateException` and logs it. The app runs fine — but **background wake never fires**, which is the feature the phone exists for. | `android/app/build.gradle.kts` | 1–2 h |
| 3 | **Release signing material is operator-supplied and absent.** `requireReleaseSigning` fails the build by design if unset — good, but you must create the keystore. | env / `~/.gradle/gradle.properties` | 20 min |
| 4 | **No hosted privacy policy.** Play requires a public URL before the listing can be submitted. Draft in §9 below. | external hosting | 30 min |
| 5 | **Three Phase B requirements are NOT MET**, two of them security: `PB-SEC-2` (the per-use biometric gate — a stale callback can resurrect authorization), `PB-PAIR-4` (a half-paired state is reachable), `PB-E2E-2`. Separately `PB-E2E-5` (real-hardware validation of biometrics, camera, FCM delivery, Doze, Keystore attestation) is **deferred and unvalidated**. | see `docs/verification/remote-phaseB-residuals.md` | in progress |

**On #5 and closed testing.** Closed testing is genuinely the right venue to burn down
`PB-E2E-5` — it is the only way to get real handsets, real Doze, real FCM. That argues *for*
shipping to a closed track soon. It does not argue for shipping `PB-SEC-2` broken: the per-use
biometric gate is the control standing between a picked-up unlocked phone and the ability to
type into a shell on your laptop. Land that fix before you invite testers, even internal ones.

**Also check before upload:** Play's minimum `targetSdk` for *new* apps rises every August.
The app targets **35**. **[VERIFY]** in the Console whether 35 is still accepted for a new app
at your upload date — if the floor has moved to 36, bump `SWARM_ANDROID_TARGET_SDK` in
`android/toolchain.env` (the build reads it from there; nothing else needs editing).

---

## 1. App identity — the facts the Console will ask for

| Field | Value | Source |
|---|---|---|
| Application ID (package) | `dev.swarm.phone` | `app/build.gradle.kts:150` |
| App name (device label) | `swarm` | `res/values/strings.xml` |
| versionCode / versionName | `1` / `0.1.0` | `app/build.gradle.kts:153-154` |
| minSdk | **33** (Android 13) | `toolchain.env` → `SWARM_ANDROID_MIN_SDK` |
| targetSdk / compileSdk | **35** (Android 15) | `toolchain.env` |
| ABIs in the native AAR | `arm64-v8a`, `x86_64` | `toolchain.env` → `SWARM_AAR_ABIS` |
| Foreground services | **none declared** | manifest — by design (ADR-007 B16) |
| Analytics / crash reporting | **none** | `android/dependency-inventory.tsv`, gate-enforced |

**Permissions requested** (all four, nothing else):

- `android.permission.INTERNET`
- `android.permission.CAMERA` — QR pairing. `uses-feature required="false"`, so camera-less
  devices still install; there is a manual-entry fallback.
- `android.permission.POST_NOTIFICATIONS`
- `android.permission.RECEIVE_BOOT_COMPLETED`

**None of these triggers a Play sensitive-permission declaration form.** No
`QUERY_ALL_PACKAGES`, no SMS/Call Log, no `MANAGE_EXTERNAL_STORAGE`, no location, no
`REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`, no foreground service type. That is a materially
easier review than most apps get — do not add any of them casually.

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

./build-aar.sh             # rebuild the gomobile AAR first
./gradlew :app:bundleRelease
# → app/build/outputs/bundle/release/app-release.aab
```

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
they wait. swarm sends that question to your phone, shows you what the agent is doing, and
lets you answer -- so the work carries on.

WHAT IT DOES

- See every agent session running on your machine, live.
- Read the terminal output as it happens.
- Answer an agent that is blocked waiting on a decision.
- Get a notification the moment a session needs you.
- Stop a session that has gone the wrong way.

HOW IT IS BUILT

swarm pairs your phone directly with your own computer. Pairing is confirmed on both ends
with a short verification code that you check yourself, so you know the device you paired
with is the one in front of you.

Everything between your phone and your machine is end-to-end encrypted. The relay that
carries the traffic cannot read it: it sees encrypted bytes and routing information, and
nothing else. The design treats that relay as hostile and is built so that it does not
matter.

Sensitive actions -- sending input to a session, revealing session content -- are held behind
your device biometric each time you use them, not once at unlock.

The app contains no analytics, no telemetry, no crash reporting and no advertising SDKs. It
collects nothing about you.

WHAT YOU NEED

- A computer running the swarm daemon and the swarm remote gateway.
- Android 13 or later.

swarm is a tool for developers who run coding agents on their own hardware. It is not a chat
client and it is not a remote desktop -- it is a window onto processes you already own.
```

*(Trim the last paragraph if you would rather not set that expectation in a closed test.)*

**Graphic assets — all mandatory, none currently exist in the repo**

| Asset | Spec |
|---|---|
| App icon | 512 × 512 PNG, 32-bit, ≤ 1 MB, no alpha |
| Feature graphic | 1024 × 500 PNG/JPEG |
| Phone screenshots | **2–8 required.** 16:9 or 9:16, each side 320–3840 px |
| 7" / 10" tablet screenshots | optional unless you declare tablet support |

Also fix blocker #1 while you are making the icon — generate `mipmap-*/ic_launcher` and add
`android:icon="@mipmap/ic_launcher"` to `<application>`.

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
| **Privacy policy URL** | Required — see §9 |
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
4. **Upload the AAB**, add release notes, roll out.
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

Sensitive keys are held in the Android Keystore and are protected by your device biometric.

## Permissions

- **Camera** — to scan the pairing QR code. There is a manual-entry alternative; you can
  decline this permission and still use the app. No image is stored or transmitted.
- **Notifications** — to alert you when a session needs your attention.
- **Internet** — to reach the relay.
- **Run at startup** — to restore your sessions after a reboot.

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
2. Create the Firebase project, drop in `google-services.json`, apply the plugin. Without
   this, closed testing cannot exercise push — which is most of what closed testing is for.
3. Create the upload keystore; verify `./gradlew :app:bundleRelease` produces a signed AAB.
4. Host the privacy policy.
5. Land the `PB-SEC-2` fix.

**In the Console** (~2 hours)

6. Create the app. Complete §4, §5, §6 from the answers above.
7. Set up Play App Signing; upload the AAB to the closed track.
8. Add testers — above the minimum, per §8.

**Then**

9. Capture screenshots from a real device once testers are on it.
10. Use the closed test to burn down `PB-E2E-5`: real biometrics, real camera, real FCM
    delivery, real Doze, real Keystore attestation. It is the only environment where those
    can be validated, and it is currently the largest unvalidated area in the project.

---

*Generated from the repository at commit `9ff52d5`. Every technical fact is sourced from the
tree; every **[VERIFY]** marks a Play policy detail that changes and should be confirmed in
the Console.*
