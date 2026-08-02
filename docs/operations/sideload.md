# Sideload — from a checked-out repo to the app running on your handset

The fastest path to a phone, and the one every other path passes through first. Play's mandatory
screenshots need a real device (`docs/ops/play-closed-testing-application.md` §3), and
`docs/operations/physical-handset-gate.md` assumes an installed app from its first step. This is
the install step.

`docs/operations/release-signing.md` is **not** this document: it produces an `app-release.aab`,
and `adb` cannot install an AAB.

Everything below is read from the tree. Nothing below has been run on a handset — the app has
never started on one (`docs/operations/operator-runbook.md` §3, ADR-007 B31).

---

## 0. Prerequisites, all of them

Discovered by reading `android/toolchain.env`, `android/build-aar.sh`,
`android/app/build.gradle.kts` and `.github/workflows/ci.yml`. The last one is the trap: **nothing
in the repo installs `gomobile`**, and you find that out ten minutes into a build.

| Needs | Pin | Where it comes from |
|---|---|---|
| Go toolchain | `1.25` or newer (`SWARM_GO_VERSION`) | ADR-008 |
| JDK | 17 (`SWARM_JDK_MAJOR`) | AGP requires it |
| Android SDK platform | `android-35` (`SWARM_ANDROID_COMPILE_SDK`) | `sdkmanager` |
| Build tools | `35.0.0` (`SWARM_ANDROID_BUILD_TOOLS`) | `sdkmanager` |
| NDK | `27.2.12479018` (`SWARM_ANDROID_NDK`) | `sdkmanager`; `gomobile bind` needs it explicitly |
| `gomobile` **and** `gobind` | `SWARM_GOMOBILE_VERSION` | **you install these; see below** |
| A handset | Android 13+ (`SWARM_ANDROID_MIN_SDK` = 33), `arm64-v8a` | |

`toolchain.env` **discovers** locations and **pins** versions, so an already-exported `JAVA_HOME`
or `ANDROID_HOME` wins. If nothing is installed it does not fail loudly — it hands you a path that
does not exist.

SDK components, if you do not have them:

```sh
. android/toolchain.env
sdkmanager --install \
    "platforms;android-$SWARM_ANDROID_COMPILE_SDK" \
    "build-tools;$SWARM_ANDROID_BUILD_TOOLS" \
    "ndk;$SWARM_ANDROID_NDK"
```

`gomobile` and `gobind`, both, at the pinned version (`.github/workflows/ci.yml:290-294`):

```sh
. android/toolchain.env
go install "golang.org/x/mobile/cmd/gomobile@$SWARM_GOMOBILE_VERSION"
go install "golang.org/x/mobile/cmd/gobind@$SWARM_GOMOBILE_VERSION"
```

Install **both**. `gomobile bind` spawns `gobind` as a child process and, when it is missing,
reports `gobind was not found. Please run gomobile init` — which names the wrong cause. Running
`gomobile init` does not help. `toolchain.env` puts `${GOPATH:-$HOME/go}/bin` on `PATH` for exactly
this reason.

---

## 1. Source the pin

`android/toolchain.env` is meant to be **sourced, not executed**. A shell that has not sourced it
concludes there is no Android toolchain on this host at all, which is wrong and has already cost
three readers.

```sh
cd /path/to/swarm
. android/toolchain.env
command -v go gomobile gobind javac adb
```

All five must print a path. **`adb` comes from here** — it is
`$ANDROID_HOME/platform-tools/adb`, put on `PATH` by the last block of `toolchain.env`. There is no
need to hunt for it and no need to install Android Studio.

## 2. Build the AAR

The app module links a native library cross-compiled from the Go phone core. Without it the module
does not compile, and `preBuild` fails with `android/app/libs/swarm.aar is missing. Run
./android/build-aar.sh first` (`android/app/build.gradle.kts:72-82`). This applies to the debug
build too.

```sh
./android/build-aar.sh
```

Produces `android/app/libs/swarm.aar` for `arm64-v8a` and `x86_64` (`SWARM_AAR_ABIS`). `arm64-v8a`
is every current handset. The script sources `toolchain.env` itself, so it works in a shell that has
not — but then `adb` will not be on your `PATH` in step 4.

## 3. Build the APK — `assembleDebug`

**Use `assembleDebug` for a first run.** Three reasons, all in the build file:

- It needs **no keystore**. `assembleRelease` and `bundleRelease` both depend on
  `requireReleaseSigning`, which fails the build outright when `SWARM_RELEASE_KEYSTORE` and its
  three companions are unset (`android/app/build.gradle.kts:48-62`). That refusal is deliberate —
  the alternative is an unsigned release APK and a green build.
- A debug APK is signed with the local debug key automatically, so `adb install` accepts it.
- It is the path CI proves works: `android/gate/artifact_androidgate_test.go:289-315` builds
  `:app:assembleDebug`, then asserts the APK carries `AndroidManifest.xml`, `classes.dex` and
  `lib/*/libgojni.so`, and that `apksigner verify` passes.

```sh
cd android && ./gradlew :app:assembleDebug
```

Output lands in `android/app/build/outputs/apk/debug/` (the gate walks that directory).

Two things a debug build is not. It is **debuggable** — `buildTypes` configures only `release`
(`build.gradle.kts:244-251`), so debug takes AGP's defaults. Do not hand it to a tester, and do not
upload it to Play; that route is `docs/operations/release-signing.md`.

**Do not run two Gradle builds in this checkout at once.** `android/app/build` is shared, and
concurrent runs destroy each other's outputs — `gradlew test` can even exit 0 having run zero tests
(beads `agents-tracker-6qi`, `agents-tracker-4ok`).

## 4. Put it on the phone

**Enable developer options**: Settings > About phone > tap **Build number** seven times. On Samsung
it is nested one level deeper, under About phone > Software information. Then Settings > Developer
options > **USB debugging** on.

> **Samsung handsets: Auto Blocker blocks `adb` entirely** (observed on a Galaxy A26, Android 16 —
> ADR-007 B132, recorded at `docs/operations/operator-runbook.md:151-157`). With Auto Blocker on,
> both USB debugging and wireless debugging are blocked: `adb` cannot see the device at all, and the
> USB-debugging toggle in developer options is unavailable. Remedy: Settings > Security and privacy
> > Auto Blocker, then turn off either the master toggle or just the USB-cable sub-setting.

Plug the phone in, then:

```sh
adb devices
```

Expect one line ending in `device`. `unauthorized` means the RSA fingerprint prompt is waiting on
the handset's screen — accept it. Nothing at all, on a Samsung, is Auto Blocker.

```sh
adb install -r android/app/build/outputs/apk/debug/app-debug.apk
```

The package is `dev.swarm.phone` (`applicationId`, `build.gradle.kts:194`). `adb install -r`
**preserves app data** (`docs/verification/remote-phaseB-residuals.md:924`), which is what you want
when reinstalling mid-debug and not what you want when you need a genuinely clean first launch — for
that, `adb uninstall dev.swarm.phone` first.

## 5. What you should see on first launch, in order

1. **The app opens.** There is no splash screen and no launch-time permission prompt.
2. **A bottom bar with four destinations** — Inbox, Machines, Activity, Settings — and you land on
   Inbox (`PhoneSurface.kt:398`, `:910-916`).
3. **An unpaired message**, verbatim: *"This phone is not paired with a machine yet. Nothing is
   broken -- pair it to begin."* (`ui/ErrorRouting.kt:183-186`). This is the healthy first-launch
   state, not an error.
4. **The camera permission is requested when you open the scanner**, not at launch
   (`PairingSurface.kt:337`). Notifications are requested from Settings
   (`SettingsSurface.kt:143-152`).
5. **No push notification will ever arrive**, and that is not a fault of your install. The
   `google-services` plugin is deliberately not applied and no `google-services.json` exists, so
   `FirebaseApp` never initialises and `FirebaseMessagingService` is never invoked
   (`build.gradle.kts:285-303`). The app logs `push unavailable: no Firebase project is configured
   for this build` under the tag `SwarmPush` (`push/PushTokens.kt:35`, `:85`).

Pairing from here is `swarm remote init` then `swarm remote pair` on the machine —
`docs/operations/operator-runbook.md` §1-§3. The handset needs to reach a relay: LAN setup is
`docs/operations/relay-runbook.md`, public is `docs/operations/relay-vps-deploy.md`.

### Build the machine side from THIS branch too, not from a release

An installed `swarm` — a Homebrew cask, or anything built before this branch — is not enough, and
the way it fails is silent. The fix for `agents-tracker-r3p` (typing a line and pressing Enter, and
having the agent never see it) is in two halves, and only one of them ships in the APK:

- the phone half, splitting the submit off the text it follows, is `internal/phonecore`, which IS
  in the AAR's dependency tree;
- the machine half, holding that submit 150 ms off the text so the two do not land in one PTY read
  tick, is `internal/remotegw` — reached by `cmd/swarm-remote`, and **not in the AAR's tree at
  all**.

A new handset against an old gateway therefore sends two frames the relay's batched delivery
recombines into one read tick, which is the case the CLI reads as a paste. You get the original bug
back, from a phone that contains the fix. Build all three binaries from this branch first
(`docs/operations/operator-runbook.md` §1) and put them on `PATH` before `swarm remote init`.

---

## 6. Troubleshooting the first launch

Nothing else in the repository covers this, and the app has never started on a physical device.

### 6a. Every control is dead and there is a message about keys

This is **provisioning refusing**, and it is fail-closed by design. What is actually required is
narrower than older documents in this repo describe:

- **Exactly one capability can refuse a handset: `KEYSTORE_AES_GCM`.** `CustodyPlanner.required`
  is a one-entry map (`keys/Provisioning.kt:102-104`). Non-`PRESENT` — and `UNKNOWN` fails closed
  exactly as `ABSENT` does — yields `CustodyPlan.Refused`, which `PhoneRuntime.capabilityPlan`
  throws as `PlatformCapabilityMissing` (`PhoneRuntime.kt:156-168`).
- **`KEYSTORE_X25519` and `KEYSTORE_ED25519` are canaries and cannot stop the app.** They are
  recorded on the plan as anomalies and never refuse (`keys/Provisioning.kt:112-115`, `:134-137`;
  `keys/PlatformCapabilities.kt:59-63`). ADR-007 B133 and residuals §2.8 demoted them precisely
  because refusing a handset over a capability no matrix row consumes is an app that will not
  start. **`docs/operations/physical-handset-gate.md` §0(b) and §2c still describe the old fatal
  behaviour and are stale on this point** — the code is authoritative.
- **The likelier refusal is the hardware-backing floor.** If the platform reports
  `SECURITY_LEVEL_SOFTWARE` for the KEK, the read-back throws `KeystoreDowngrade`
  (`keys/Provisioning.kt:347-352`). This is what makes the app refuse on a standard emulator. A
  real handset should report `TRUSTED_ENVIRONMENT` or `STRONGBOX`. A platform that declines to name
  a level (`UNKNOWN`) provisions and is recorded — it does not refuse (`Provisioning.kt:203-207`).

Both refusals route to `DEVICE_UNSUPPORTED` (`PhoneRuntime.kt:381-384`), whose on-screen text is:

> This handset cannot protect keys the way this app requires. Nothing you do fixes it and pairing
> again would land here; please report it.

(`ui/ErrorRouting.kt:194-198`, remedy `REPORT_BUG`.)

**The app does not die silently, and a document that says it does is wrong.** `PhoneRuntime.attach`
catches `Throwable` — an unloadable native library is an `Error`, not an `Exception` — and returns
`PhoneStartup.Unavailable` (`PhoneRuntime.kt:117-125`). `PhoneSurface.renderUnavailable` then draws
the nav bar and puts that sentence in the status line with every action disabled
(`PhoneSurface.kt:735-767`). So: **a screen with that sentence and dead controls is a refusal; an
app that vanishes off the screen is a crash**, and only the second one is a logcat problem.

### 6b. logcat

The app carries **one** log tag, `SwarmPush` (`push/PushTokens.kt:35`), and nothing else logs. There
is no diagnostic stream to filter on, so filter by process and read the crash buffer.

```sh
adb logcat -c                                     # clear, then launch the app
adb logcat --pid=$(adb shell pidof -s dev.swarm.phone)
```

If the process is already gone, `pidof` prints nothing and that pipeline is empty. The crash buffer
survives the process:

```sh
adb logcat -b crash -d
adb logcat -d AndroidRuntime:E SwarmPush:W '*:S'
```

Do not hunt logcat for a provisioning refusal. It produces a screen, not a log line.

### 6c. Half-paired: the phone says paired, the machine has nothing

`PB-PAIR-4` is open, this state **is** reachable, and
`docs/ops/play-closed-testing-application.md` §10 requires the recovery be handed to testers.

**Only one direction is reachable through the ceremony.** The phone commits its pin durably
*before* it acknowledges (`internal/remote/pairing/pairing.go:1011-1031`), and the machine enrolls
the device *only after* that acknowledgement arrives inside a 2-second window
(`pairing.go:587-592`). A lost ack therefore leaves the phone pinned and the machine holding
nothing. The reverse — machine enrolled, phone blank — is not a race; it comes only from later
phone-side state loss such as clearing app data.

The CLI names it when it happens (`cmd/swarm/remote.go:1014-1016`):

> the phone never confirmed it received the acceptance, so this machine paired NOTHING -- even if
> the phone now shows it is paired. Nothing needs revoking and the slot is free. Run `swarm remote
> pair` again.

**Recovery, phone pinned / machine blank:**

```sh
swarm remote status     # expect "paired devices (0):"
swarm remote pair       # succeeds; the slot is free
```

Then scan again. No phone-side action is needed: re-pairing to the **same** machine overwrites the
pin cleanly (`mobile/pairing.go:768+`), and the phone's commit guard refuses only when the machine's
Noise static differs (`mobile/pairing.go:736-745`). Do **not** press *Revoke this device* here — it
seals a command to a machine that has no record of it, and it purges the phone's keys in a `finally`
block whether or not the machine was reachable (`PhoneSurface.kt:260-266`).

**Recovery, machine registered / phone blank** (after an app-data clear, or a phone that was reset):

```sh
swarm remote devices                 # read the DEVICE ID
swarm remote revoke <device-id>
swarm remote devices                 # VERIFY: empty table
swarm remote pair
```

`swarm remote revoke` prints `revoked device <id>` and exits 0 **even for an id that was never
paired** (`docs/operations/operator-runbook.md:203-220`). The success line is not evidence; an empty
`swarm remote devices` table is.

Skipping the revoke gets you the fail-fast (`internal/skeleton/pairing.go:133-137`):

```
remote pair: pair_start: a device is already paired (single-device v1); run `swarm remote devices`
to see its id, then `swarm remote revoke <device-id>` to unregister it, and pair again
```

If the phone shows `different_machine` (`mobile/pairing.go:71`), it is pinned to a **different**
machine's static key and no machine-side action fixes it. Use *Revoke this device* on the phone, or
clear app data, then pair again.

The pairing window is 60 seconds (`internal/remote/relay/config.go:104`), and the CLI prints
`the pairing window closed before the device connected; the code above is dead` when it lapses
(`cmd/swarm/remote.go:923-926`). Have the phone unlocked and the scanner open before you run
`swarm remote pair`.

### 6d. Build failures you will actually hit

| Message | Cause | Fix |
|---|---|---|
| `android/app/libs/swarm.aar is missing` | step 2 not run | `./android/build-aar.sh` |
| `gobind was not found. Please run gomobile init` | `gobind` is not on `PATH` | install it at the pinned version (§0); `gomobile init` does nothing for this |
| `PB-TOOL-3: a release build needs operator-supplied signing material` | you ran `assembleRelease` or `bundleRelease` | use `assembleDebug`, or `docs/operations/release-signing.md` |
| `adb devices` shows `unauthorized` | fingerprint prompt unanswered | accept it on the handset |
| `adb devices` shows nothing, Samsung | Auto Blocker | §4 |
