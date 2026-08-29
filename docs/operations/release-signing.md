# Release signing — the upload keystore and the signed AAB

**Tracks `agents-tracker-cwz`.** This document covers generating the keystore, wiring its
credentials into the build the way `android/app/build.gradle.kts` actually reads them, producing
a signed and provenanced `app-release.aab`, and handing that artifact to the guarded publisher.
Play Console application and track setup remains `agents-tracker-2qm`.

**This runbook does not generate the keystore for you and does not run the build.** Both need a
password only the operator has, and a keystore an agent generated with a password it chose is
worse than no keystore at all — anyone who read the transcript would hold the password too.

---

## Read this first: what you are creating, and what happens if you lose it

`keytool` below generates an **upload key**, not the app's real signing key.

**Play App Signing is mandatory for this app, full stop — there is no opt-out to navigate.** Google
requires it for every app created from August 2021 onward
(<https://developer.android.com/studio/publish/app-signing>: "Configuring Play App Signing is
required to sign your app for distribution through Google Play... except for apps created before
August 2021, which may continue distributing self-signed APKs"). `dev.swarm.phone` is a new app
being created in 2026 — nowhere near that grandfather clause — so Play Console will not offer a
choice to decline, and there is nothing to confirm during app creation.

Under Play App Signing, **Google generates and holds the real app signing key.** The keystore you
create here only signs the AAB you *upload to Google*, which Google re-signs with the key it holds
before it ever reaches a device. That means **losing this upload keystore, or forgetting its
password, is recoverable**: request an upload-key reset in Play Console (identity-verified, on the
order of days, not instant), generate a fresh keystore the same way as below, and continue shipping
updates — existing installs are unaffected because the key that actually signs what a device sees
never changes.

**Back this file up anyway.** A recoverable-but-days-long reset is still a real outage for anyone
relying on an update landing on schedule.

---

## 1. Prerequisites

- A JDK providing `keytool` and `jarsigner` — this project already pins JDK 21
  (`android/toolchain.env`'s `SWARM_JDK_MAJOR`), and both tools ship with any JDK, so sourcing that
  file is enough; no separate install.
- **One** password of your own choosing. `build.gradle.kts` reads the keystore password and the key
  password as two independent settings (§4), but you must set **both to the same value** — see the
  warning under §2, which is a property of the keystore format and not a convention.
- The operator-provisioned production Firebase client config at
  `android/app/google-services.json`. It is deliberately gitignored. For the Play artifact it must
  name project `swarm-8404f`, package `dev.swarm.phone`, and Firebase app
  `1:733314021126:android:ff6e016cffe98782535087`; a development or fork config is not equivalent.

## 2. Generate the upload keystore

```bash
mkdir -p ~/.keystores
keytool -genkeypair -v \
  -keystore ~/.keystores/swarm-upload.jks \
  -alias swarm-upload \
  -keyalg RSA -keysize 4096 \
  -validity 10000
```

`keytool` prompts interactively for the keystore password and then for the certificate's
distinguished-name fields (name, org, locale) — none of those need to be accurate identities, Play
does not check them, they only need to be *something* so the certificate is well-formed.

**It does not ask you for a key password, and that is not an omission — there is only one password
here.** Despite the `.jks` filename, JDK 9 and later produce a **PKCS12** keystore, and PKCS12 has
no per-entry password: the key is protected by the store password and nothing else. Executed
against this project's pinned JDK 17, the prompts run keystore password → DN questions → done, and
`keytool -list` on the result reports `Keystore type: PKCS12`. **So `SWARM_RELEASE_KEY_PASSWORD`
and `SWARM_RELEASE_KEYSTORE_PASSWORD` (§4) must be set to the same value.**

If you try to force a second password with `-keypass`, `keytool` does not fail — it discards the
value and says so, in a warning easily lost in the surrounding output:

```
Warning:  Different store and key passwords not supported for PKCS12 KeyStores. Ignoring user-specified -keypass value.
```

Take that at face value: the keystore now holds a key openable only by the *store* password. Set
`SWARM_RELEASE_KEY_PASSWORD` to the value you passed to `-keypass` and Gradle asks the keystore for
the key with a password it does not have, so `bundleRelease` fails — after the keystore exists and
has been backed up, which is the worst moment to discover it.

(Two distinct passwords are possible only in the older, proprietary JKS format, via `-storetype
JKS`. That format does prompt for a key password — `Enter key password for <swarm-upload>` with a
"(RETURN if same as keystore password)" option — and `keytool` recommends migrating away from it
on every use. Nothing in this project needs it.)

- **`-keysize 4096`**: stronger than the 2048-bit figure Android's own sample commands use, and
  well within what every JDK/Android verifier supports. There is no reason to prefer a smaller key
  for an upload credential you intend to keep for the life of the app.
- **`-validity 10000`** (days, ≈27 years): long enough that this certificate outlives any
  realistic lifetime of this application, so you are never forced into an unplanned key rotation
  purely because a certificate expired. This also sidesteps the historical Android v1
  (JAR-signature) pitfall where an expired signing certificate could stop an already-installed APK
  from verifying at all — moot for an app whose `minSdk` is 33 and which therefore signs with
  scheme v2/v3 only (`android/toolchain.env`), but there is no cost to the long validity either
  way.

**Do not put the keystore inside this repository, even though `.gitignore` would catch it.**
`~/.keystores/` (outside the tree entirely) is what the command above uses. `.gitignore` already
carries `*.jks`, `*.keystore`, `*.p12`, `*.pfx`, and `*.bks` rules (added for exactly this file) as
a second line of defense — `android/gate/signing_test.go`'s
`TestPBTOOL3_GitignoreExcludesSigningMaterial` asserts those rules exist and stays green today —
but a rule that only stops an accidental `git add` is not a reason to place the one irreplaceable
file where an accidental `git add -A` gets a chance to test it.

## 3. Back up the keystore now, before you build anything

Copy `~/.keystores/swarm-upload.jks` to at least one location independent of this machine — a
password manager's file attachments, encrypted cloud storage, a second physical device. This step
has no build dependency and no reason to wait.

## 4. Supply the credentials to Gradle

`android/app/build.gradle.kts` reads exactly four settings through `operatorSetting`, each resolved by
`findProperty(name) ?: System.getenv(name)` — a Gradle project property (`-P<name>=...` on the
command line, or a `gradle.properties` entry) wins if both are set, otherwise the environment
variable is used:

| Setting | What it is |
|---|---|
| `SWARM_RELEASE_KEYSTORE` | Absolute path to the `.jks` file |
| `SWARM_RELEASE_KEYSTORE_PASSWORD` | The keystore's own password |
| `SWARM_RELEASE_KEY_ALIAS` | The `-alias` value from §2 (`swarm-upload` above) |
| `SWARM_RELEASE_KEY_PASSWORD` | The key's password inside the keystore — **the same value as `SWARM_RELEASE_KEYSTORE_PASSWORD`**, for the PKCS12 reason in §2 |

Any release build missing `SWARM_RELEASE_KEYSTORE` fails outright — `requireReleaseSigning` in
`build.gradle.kts` is wired as a dependency of `assembleRelease`/`bundleRelease`
specifically so a forgotten credential produces a loud build failure instead of a silently
unsigned artifact.

**Two ways to supply these, and one file you must never use for it:**

- **Shell environment variables**, set in your own shell session (or a shell rc file that is
  itself outside this repository):

  ```bash
  export SWARM_RELEASE_KEYSTORE="$HOME/.keystores/swarm-upload.jks"
  export SWARM_RELEASE_KEYSTORE_PASSWORD='<your password>'
  export SWARM_RELEASE_KEY_ALIAS='swarm-upload'
  export SWARM_RELEASE_KEY_PASSWORD='<the same password again>'
  ```

- **`~/.gradle/gradle.properties`** — Gradle's *global*, per-machine properties file in your home
  directory. It is read for every project on the machine and is not part of any git repository, so
  a value placed there can never be committed by this project's own history:

  ```properties
  SWARM_RELEASE_KEYSTORE=/Users/you/.keystores/swarm-upload.jks
  SWARM_RELEASE_KEYSTORE_PASSWORD=...
  SWARM_RELEASE_KEY_ALIAS=swarm-upload
  SWARM_RELEASE_KEY_PASSWORD=...
  ```

- **Never `android/gradle.properties`.** That file is tracked by this repository (`git ls-files`
  confirms it) — it is where the *project's* JVM/build settings live
  (`org.gradle.jvmargs`, `android.useAndroidX`), not a place for secrets. A password placed there
  would be committed on the next `git add` of that file, and `android/gate/signing_test.go`'s
  `TestPBTOOL3_NoSigningPasswordIsTracked` scans exactly that file (among others) for a literal
  `storePassword`/`keyPassword`-shaped assignment for this reason — it is a safety net for an
  accident, not a place to intentionally test its limits.

## 5. Build the signed AAB

```bash
cd android
. ./toolchain.env    # nothing Android is on PATH without this
test -s app/google-services.json
./build-aar.sh       # rebuilds the gomobile AAR the app module links; the release build's
                      # preBuild task (requireSwarmAar) refuses to proceed without it
./gradlew :app:bundleRelease
```

`bundleRelease` depends on `requireProductionFirebaseConfig`. It fails before producing the Play
artifact if the file is absent or if its project, package or Firebase app id is not the production
one above. This is deliberately narrower than the general Android build: CI lint/tests, debug APKs
and the bootstrap `assembleRelease` path remain usable without a production Firebase file.

Do not upload an `app-release.aab` left over from before the config was provisioned. The artifact
below must be the output of the successful `bundleRelease` invocation in this run; an older bundle
can be correctly signed and still contain no Firebase resources.

The signed bundle lands at:

```
android/app/build/outputs/bundle/release/app-release.aab
```

The same successful task writes a hash-bound publisher provenance sidecar:

```
android/app/build/outputs/bundle/release/app-release.aab.swarm-firebase-provenance.json
```

It contains only public Firebase identifiers plus the AAB's SHA-256; it does not contain the API
key or signing material. `swarm-publish` requires this exact adjacent file and recomputes the AAB
hash before it reads the Play credential. A missing sidecar, a development-project sidecar, or a
sidecar copied from a different/stale AAB is a local hard failure and makes no Google request.
The publisher keeps that verified AAB descriptor open through credential validation and upload, so
replacing the pathname after the check cannot change which bytes reach Play.

If this fails on the signing step with a message about recovering the key, or the keystore password
being incorrect, the cause is almost always the PKCS12 password rule in §2: set
`SWARM_RELEASE_KEY_PASSWORD` to the same value as `SWARM_RELEASE_KEYSTORE_PASSWORD` and rerun. The
keystore is fine and does not need regenerating.

## 6. Verify the AAB is actually signed

An `.aab` is a zip/JAR-structured archive, so `jarsigner` — not `apksigner`, which targets
installable APKs — is what reads its signature:

```bash
jarsigner -verify -verbose -certs android/app/build/outputs/bundle/release/app-release.aab
```

Expect `jar verified.` and a certificate block naming the alias you signed with
(`swarm-upload`) and a validity range starting today and ending roughly 27 years from now,
matching §2. If this instead reports the jar is unsigned, `requireReleaseSigning` should already
have failed the build before you got here — recheck that all four settings in §4 were visible to
the exact shell/invocation that ran `bundleRelease`.

## 7. Publish only through the guarded CLI

> AMENDED BY ADR-015 (2026-08-15): the Play closed-testing track named below is now the release
> path (Play-Store Android is the first client, not a deferred distribution option); the task
> split itself is unchanged.

Creating the app and closed-testing track in Play Console (which also enrolls it in mandatory Play
App Signing) is covered by `docs/ops/play-console-walkthrough.md`. The Console's manual AAB upload
control is not an allowed release path: it does not consume the provenance sidecar. After the app
and `alpha` track exist, rehearse with:

```bash
cd .. # run the publisher from the repository root
go run ./cmd/swarm-publish \
  --aab android/app/build/outputs/bundle/release/app-release.aab \
  --key /absolute/path/to/play-service-account.json \
  --package dev.swarm.phone \
  --track alpha \
  --dry-run
```

With explicit approval to publish, repeat the identical command without `--dry-run`. Before either
command, re-check Play's current target API level requirement —
`docs/ops/play-closed-testing-application.md` flags that the floor moves every August and this
project sits on the pinned value in `android/toolchain.env`.
