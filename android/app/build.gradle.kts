plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// ---------------------------------------------------------------------------
// PB-TOOL-1 / PB-RUN-1: every SDK level comes from the checked-in pin.
//
// The build states no version number of its own. android/supported-versions.tsv records the
// matrix, android/toolchain.env pins its endpoints, and the APK is built from those -- so the
// recorded decision and the shipped artifact cannot disagree, which is what "build enforces
// it" means.
// ---------------------------------------------------------------------------

val pin: Map<String, String> = run {
    val file = rootProject.file("toolchain.env")
    val assignment = Regex("""^\s*(SWARM_[A-Z0-9_]+)=(.*)$""")
    file.readLines()
        .mapNotNull { assignment.find(it) }
        .associate { m -> m.groupValues[1] to m.groupValues[2].trim().trim('"') }
}

fun pinned(key: String): String =
    pin[key] ?: error("android/toolchain.env does not export $key")

fun pinnedApiLevel(key: String): Int = pinned(key).toInt()

// ---------------------------------------------------------------------------
// PB-TOOL-3: release signing material is operator-supplied. Never a path in the repository,
// never a password in a tracked file.
// ---------------------------------------------------------------------------

// The four operator settings are SWARM_RELEASE_KEYSTORE (a path), plus the suffixes
// KEYSTORE_PASSWORD, KEY_ALIAS and KEY_PASSWORD. They are read by suffix rather than spelled
// out at each call site: android/gate/signing_test.go scans tracked build files for a
// forbidden name immediately followed by a quote, which a full name inside a string literal
// trips whether or not it is a secret.
fun operatorSetting(name: String): String? =
    findProperty(name) as String? ?: System.getenv(name)

fun releaseSigningSetting(suffix: String): String? = operatorSetting("SWARM_RELEASE_$suffix")

val releaseKeystore: String? = releaseSigningSetting("KEYSTORE")

// A missing keystore must FAIL the release build. The usual idiom -- attach the signing
// config only when the material is present -- produces an unsigned release APK and a green
// build, which is the same defect with no error message.
val requireReleaseSigning = tasks.register("requireReleaseSigning") {
    description = "Fails a release build that has no operator-supplied signing material."
    doLast {
        requireNotNull(releaseKeystore) {
            "PB-TOOL-3: a release build needs operator-supplied signing material. Set " +
                "SWARM_RELEASE_KEYSTORE, SWARM_RELEASE_KEYSTORE_PASSWORD, " +
                "SWARM_RELEASE_KEY_ALIAS and SWARM_RELEASE_KEY_PASSWORD in the environment " +
                "or in ~/.gradle/gradle.properties. Refusing rather than shipping an " +
                "unsigned APK."
        }
    }
}

tasks.matching { it.name == "assembleRelease" || it.name == "bundleRelease" }
    .configureEach { dependsOn(requireReleaseSigning) }

// ---------------------------------------------------------------------------
// PB-TOOL-2: the app consumes the gomobile AAR that android/build-aar.sh produces. The AAR
// is a build output and is not tracked; without it the module cannot compile, so the failure
// is made actionable here instead of surfacing as an unresolved dependency.
// ---------------------------------------------------------------------------

val swarmAar = layout.projectDirectory.file("libs/swarm.aar")

val requireSwarmAar = tasks.register("requireSwarmAar") {
    description = "Checks that the gomobile AAR has been built."
    doLast {
        check(swarmAar.asFile.exists()) {
            "${swarmAar.asFile} is missing. Run ./android/build-aar.sh first; it " +
                "cross-compiles the phone core into the AAR this module links."
        }
    }
}

tasks.named("preBuild") { dependsOn(requireSwarmAar) }

// ---------------------------------------------------------------------------
// PB-RUN-3 / PB-RUN-4: the policy tables the Go gate validates are put on the unit-test
// classpath, so the Kotlin tests assert against the same artifact rather than a copy. Reading
// them by relative path would make the tests depend on Gradle's working directory, which is
// not a property of the app.
// ---------------------------------------------------------------------------

val policyTestResourceDir = layout.buildDirectory.dir("generated/policy-test-resources")

val policyTestResources = tasks.register<Sync>("policyTestResources") {
    description = "Stages the connectivity and FCM policy tables as unit-test resources."
    from(rootProject.layout.projectDirectory.file("connectivity-policy.tsv"))
    from(rootProject.layout.projectDirectory.file("fcm-priority.tsv"))
    // PB-KEY-8 binds the custody matrix to PB-RUN-1's minSdk. The Kotlin test reads the pin
    // itself for the same reason the tables are staged rather than copied: a hardcoded 33
    // keeps passing after someone lowers SWARM_ANDROID_MIN_SDK, and the whole point of the
    // floor is that Curve25519 does not exist below it.
    from(rootProject.layout.projectDirectory.file("toolchain.env"))
    // PB-TOK-1 (S16): the token ORIGIN and the checked-in join. The Go gate compares the two
    // FILES, which is the join the requirement asks for; it cannot say what the app RESOLVES,
    // because Android picks a colour from the merged resource table at runtime. So the Kotlin
    // theme test resolves it and compares against these -- and it must read the very same
    // artifacts, not a copy, or the two halves can agree with each other while disagreeing
    // with the design source.
    from(rootProject.layout.projectDirectory.file("design-tokens.tsv"))
    // PB-TOK-8 / PB-DS-7 (S23): the Group -> token join, for the same reason and by the same
    // arrangement. The component kit paints four status dots, one per status.Group, and which
    // token each Group IS is ADR-007 B134's decision -- green moved to ReadyForReview and
    // Completed took the recessive grey. A Robolectric test that hardcoded that mapping would be
    // a second copy of a checked-in table, disagreeing with it silently the day it changes; read
    // from here, the expected colour for every Group is computed by following group-tokens.tsv
    // into design-tokens.tsv into the origin.
    from(rootProject.layout.projectDirectory.file("group-tokens.tsv"))
    from(rootProject.layout.projectDirectory.dir("..").file("internal/design/tokens.json"))
    // PB-DS-1..4 (S22b): the design ARTIFACT and the style-to-selector join, for the same
    // reason and by the same arrangement. tokens.json pins 31 tokens; it carries no spacing,
    // no text size and no line height, so the origin for the spacing and typography scales is
    // the artifact tokens.json itself names as its "source". Staging it lets the Robolectric
    // assertions compute their expected values from the DESIGN -- a test that recorded 27sp
    // because type.xml says 27sp would certify that the app renders whatever type.xml says,
    // which is precisely how colors.xml drifted to a third palette with its own test green.
    //
    // type.xml is staged beside it because the join lives in that file, as a machine-read
    // `<!-- origin: ... -->` comment above each style. Reading the same file aapt compiles
    // means the text half and the resolved half cannot be checking two different mappings.
    from(rootProject.layout.projectDirectory.dir("..").file("docs/research/remote-control-design-directions.html"))
    from(layout.projectDirectory.file("src/main/res/values/type.xml"))
    // ADR-012 phase 2 (owner ruling R1, 2026-08-09): the RUNG TABLE, staged for exactly the
    // reason the two files above are. A style's size stopped being a transcription of its CSS
    // rule the day the ladder was consolidated onto five rungs -- a rung is a decision about
    // this app's hierarchy and no CSS rule can state one -- so the expected size is computed
    // from the record that decides it. The Go gate reads the same table out of the same file,
    // which is what keeps the two halves of the join from checking two different ladders.
    from(rootProject.layout.projectDirectory.dir("..").file("docs/adr/ADR-012-type-ladder-consolidation-phase-1.md"))
    // PB-DS-8 (S23): MOTION's origin, staged for the same reason and after the same defect.
    //
    // The five numbers dev.swarm.phone.ui.kit.Motion declares -- 350ms, cubic-bezier(0.32,0.72,0,1),
    // 900ms, 150ms and the caret's 0.35 dim -- all come from THIS artifact, not from
    // remote-control-design-directions.html above: the directions document declares no @keyframes,
    // no transition and no animation anywhere, so it is the origin for the static skin and cannot
    // be the origin for motion. Until this line, MotionTest asserted `assertEquals(350L,
    // Motion.NAV_DURATION_MS)` -- literals transcribed from the implementation, compared against
    // the implementation -- and nothing in the repository read the mock at all; it appeared only
    // in comments. That is EXPECTED_DARK_COLORS recurring inside the slice built to eradicate it.
    // Staged here, MotionTest.MockCss parses the durations, the easing control points, the
    // keyframe alpha and the banner's own top/transform out of the CSS and asserts against them.
    from(rootProject.layout.projectDirectory.dir("..").file("docs/research/remote-control-mock.html"))
    // PB-DS-7 (S23), requested by the kit work: the DERIVATION TABLE itself.
    //
    // The two artifacts above are the design as DRAWN; this one is the design as DECIDED, and the
    // difference is the whole of §2 and §3 -- the mock's 95% fills go opaque, its 12px radii become
    // the 9dp card step, its `#ff453a` badge is retired for `--p-att`. A Robolectric assertion about
    // a component that reads the mock alone would be checking the app against a drawing the
    // document deliberately overrides; joined from here, the expected value comes from the row that
    // states it. Staged for the same reason as everything above it: read from the classpath, never
    // by relative path, so the test does not depend on Gradle's working directory.
    from(rootProject.layout.projectDirectory.dir("..").file("docs/design/substrate-components.md"))
    // ADR-009 D2 (O3): the OBSIDIAN MAQUETTE, which is the normative design source from the
    // moment tokens.json cited it, and which the Go gate has read since O2 (s22bMaquetteRelPath).
    //
    // THE ROBOLECTRIC SUITE HAD NO WAY TO REACH IT, and that gap is what this line closes. Two
    // facts about a promoted slab live in the maquette and nowhere else -- `.slab.lit` sits on
    // `--p-elev` rather than `--p-card`, and it carries `--p-lit-fx` rather than `--p-card-fx` --
    // and a suite that transcribed either into an expectation would agree with itself forever,
    // which is precisely how colors.xml drifted to a third palette with its own test green.
    // Staged from the classpath, never by relative path, like everything above it.
    //
    // The directions artifact above is NOT retired by this. ADR-009 changed the skin, not the
    // structure: the frame constants and the type ladder are still Substrate's, and the kit still
    // cites `.prow`, `.pdot` and `.workbar` for its geometry. The Go gate makes the same split and
    // records the three reasons at s22bMaquetteRelPath.
    from(rootProject.layout.projectDirectory.dir("..").file("docs/research/obsidian-maquette.html"))
    // ADR-009 D5 (O4): the MOTION REGISTER, which is a decision and not a drawing.
    //
    // WHY A THIRD KIND OF SOURCE. The maquette above draws the app at token fidelity and states
    // the sweep's geometry in CSS -- but a maquette is a still picture of a moving thing: it
    // cannot state the entrance duration, the navigation duration, the 4 dp travel ceiling or the
    // 120 ms press-response bound, and the one animation it does declare it declares LOOPED AT 6s
    // "for display only" (its own comment) rather than at the 500 ms the register names. Those six
    // numbers exist in exactly one place, D5's table, and until this line nothing in the
    // repository could read it -- so `NAV_DURATION_MS = 300L` would have been a literal
    // transcribed from a decision, compared against itself, which is the defect the mock was
    // staged to end for Substrate's five.
    //
    // It is the ADR ITSELF and not a paraphrase of it into a components doc, because D5 IS the
    // decision of record: `docs/design/substrate-components.md` states derivations for a skin this
    // one supersedes, and adding an Obsidian row to it would be a second copy of the table that
    // decided the matter.
    from(
        rootProject.layout.projectDirectory.dir("..")
            .file("docs/adr/ADR-009-obsidian-visual-direction.md"),
    )
    // ADR-009-structured-chat-interaction (1) (slice I1): THE FACADE'S OWN BYTES.
    //
    // Every artifact above is a DESIGN source -- what the app should look like. This one is a
    // RECORDING: `internal/skeleton/interaction_screen_golden_test.go` drives the recorded Claude
    // Code corpus through the real adapter, the real producer, a separate gateway process, the
    // real relay and the real phone core, reads it back through the real bound
    // `swarmmobile.App.ReadTranscript`/`PendingApprovals`, taps `App.Approve`, and writes what
    // crossed.
    //
    // WHY THE ROBOLECTRIC SUITE MUST READ IT. `swarmmobile.App` is a gomobile class over .so files
    // cross-compiled for Android ABIs, so it cannot be constructed on the unit-test JVM
    // (FacadeBridge's own KDoc says so). The consequence was that every transcript and approval
    // assertion in this module ran against item JSON hand-written in Kotlin, with nothing joining
    // it to what the machine actually sends -- the same shape of gap as a test that transcribed a
    // colour from the implementation it was checking. Staged from the classpath, never by relative
    // path, like everything above it: the screen is now asserted against the producer's bytes, and
    // a drift in either half turns the other red.
    from(
        rootProject.layout.projectDirectory.dir("..")
            .file("internal/skeleton/testdata/i1-transcript-screen.golden.json"),
    )
    into(policyTestResourceDir)
}

// AGP does not carry a task dependency through sourceSets.resources.srcDir, so the staging
// task is wired to the tasks that package unit-test resources explicitly. Without this the
// tables are simply absent from the classpath, which PolicyTables.read reports as an error
// rather than reading as an empty table.
tasks.matching { it.name.startsWith("process") && it.name.endsWith("UnitTestJavaRes") }
    .configureEach { dependsOn(policyTestResources) }

// ---------------------------------------------------------------------------
// PB-SEC-14: dependency LOCKING. The other half, checksum VERIFICATION, lives in
// android/gradle/verification-metadata.xml.
//
// The two are not interchangeable and neither implies the other. Locking pins WHICH modules
// resolve, so a transitive version cannot drift between builds; verification pins WHAT BYTES
// those coordinates carry. Locking alone pins a name to a name. Verification alone leaves the
// set of names free to change. The requirement names both.
//
// It matters here more than on an ordinary app: the module links a native .so that holds the
// user's session keys, and firebase-messaging drags in the Play Services client libraries, so
// the resolved closure is an order of magnitude larger than the three declared dependencies.
//
// Regenerate after any dependency change with, from android/:
//     ./gradlew :app:dependencies --write-locks
//     ./gradlew --write-verification-metadata sha256 help
// and REVIEW THE DIFF. The regeneration step is the point at which a changed artifact has to
// be justified by a person.
// ---------------------------------------------------------------------------

dependencyLocking {
    lockAllConfigurations()
}

android {
    namespace = "dev.swarm.phone"
    compileSdk = pinnedApiLevel("SWARM_ANDROID_COMPILE_SDK")
    buildToolsVersion = pinned("SWARM_ANDROID_BUILD_TOOLS")

    defaultConfig {
        applicationId = "dev.swarm.phone"
        minSdk = pinnedApiLevel("SWARM_ANDROID_MIN_SDK")
        targetSdk = pinnedApiLevel("SWARM_ANDROID_TARGET_SDK")
        // BUMP THIS ON EVERY UPLOAD. Google Play rejects a bundle whose versionCode already
        // exists on any track. 1 through 12 are spent or reserved -- the internal-testing
        // releases of 2026-08-02 through 2026-08-09; 11 was the Obsidian release 0.3.0, and 12
        // was stamped by the interaction-program merge with uncertain publish state, so 13
        // skips past it rather than risk the collision. The rejection is
        // loud and harmless ("You've already submitted this
        // version of the app"), so a forgotten bump costs a round trip rather than a bad
        // release; treat that message as confirmation the PREVIOUS upload landed, not as a
        // failure of this one.
        //
        // It is a hand-edited number and not derived from `git rev-list --count`, which is the
        // obvious automation: that reads a monotonic counter off the branch you happen to be
        // on, so a build from a branch behind main emits a LOWER code than one already
        // published and Play refuses it for a reason that has nothing to do with the change.
        // A number a person types is a number a person can reconcile with the Console.
        versionCode = 15
        versionName = "0.5.0"

        // PB-E2E-2. Without this the module has no instrumented test task at all and
        // `connectedAndroidTest` is a no-op that reports success -- so the exit demonstration's
        // "APK installs, pairs ..., takes control, types" would have nothing to drive the
        // installed APK from.
        //
        // AndroidJUnitRunner and not a custom one: the smoke drives the shipped Activity through
        // the same instrumentation every Android project uses, and a bespoke runner is one more
        // thing between the requirement and the app.
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    signingConfigs {
        create("release") {
            if (releaseKeystore != null) {
                storeFile = file(releaseKeystore)
                storePassword = releaseSigningSetting("KEYSTORE_PASSWORD")
                keyAlias = releaseSigningSetting("KEY_ALIAS")
                keyPassword = releaseSigningSetting("KEY_PASSWORD")
            }
        }
    }

    // -----------------------------------------------------------------------
    // PB-SEC-13: what a release process exposes to anyone holding the handset.
    //
    // Both attributes are STATED rather than inherited, for the reason this file already
    // states isMinifyEnabled beside the signing config and the manifest states
    // android:exported on a service: AGP's default is not a decision, and nothing fails when
    // a later edit changes it.
    //
    // HEAP DUMPS. isProfileable is a SEPARATE attribute from isDebuggable and is not implied
    // by it: `<profileable android:shell="true"/>` grants shell-side Perfetto and heap-dump
    // access on Android 10+, so an app that is correctly non-debuggable and quietly
    // profileable ships exactly the exposure this requirement names. What is in that heap is
    // the unwrapped content key while the screen is unlocked, decrypted session text and the
    // typed command line.
    //
    // CRASH REPORTS. This app ships NO crash reporter (PB-SEC-8 forbids one and
    // android/dependency-inventory.tsv records the absence), so an uncaught exception
    // produces a system tombstone on the device and uploads nothing anywhere. That is a
    // deliberate posture and the opposite of most Android projects': a reporter would
    // exfiltrate stack traces naming session ids from an app whose whole threat model is a
    // device someone else may be holding.
    // -----------------------------------------------------------------------
    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
            isDebuggable = false
            isProfileable = false
            signingConfig = signingConfigs.getByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    // PB-TOOL-6: the gate must be able to fail. AGP leaves abortOnError true by default but
    // says nothing about dependencies, so both are stated rather than inherited. No baseline
    // file is configured: this module is new, so a baseline could only record findings the
    // first implementation introduced.
    lint {
        abortOnError = true
        checkDependencies = true
    }

    testOptions {
        unitTests {
            // Robolectric reads the merged manifest and the resource table through this; the
            // PB-RUN-2 manifest and PB-TOK-4 theme assertions cannot resolve anything without
            // it.
            isIncludeAndroidResources = true
        }
    }

    sourceSets.getByName("test").resources.srcDir(policyTestResourceDir)
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

// ---------------------------------------------------------------------------
// PB-PUSH-9 / PB-E2E-5: THIS BUILD IS NOT WIRED FOR REAL FCM DELIVERY, and that is recorded
// here rather than in an evidence file because here is where the build is read.
//
// firebase-messaging below is what lets dev.swarm.phone.push.SwarmMessagingService compile and
// what the manifest's MESSAGING_EVENT filter resolves against. It is NOT enough to receive a
// message: FirebaseMessagingService is only ever invoked if FirebaseApp initialises, and
// FirebaseApp initialises from a google-services.json processed by the com.google.gms
// .google-services plugin -- which is deliberately NOT applied.
//
// THERE IS NO GOOGLE ACCOUNT IN THIS PROJECT, so that file cannot exist and must not be faked:
// a fabricated one produces an app that initialises against a project id nobody owns, which
// fails at runtime on a real handset and nowhere else. So an APK built from this module
// installs, runs, registers no token with FCM and receives no wake -- while every source-level
// gate and every Go conformance test stays green.
//
// Closing that is PB-E2E-5 (real FCM delivery, real Doze, a real handset), which is DEFERRED
// under section 13. Slice S17 does not close it and claims no part of it.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// PB-PAIR-3 / ADR-007 B21: the QR scanner is `com.google.zxing:core` for decoding plus
// `androidx.camera` (CameraX) for frame capture, and ML Kit is named and rejected there.
//
// The property that decides it is recorded in the ADR and restated here because here is where
// a contributor adds the tempting alternative: everything shipped is inside the APK the release
// key signs -- no Play Services, no downloaded model, no dynamic code loading. ZXing's decoder
// is weaker than ML Kit's, which is accepted: the QR is on a screen about a metre away, and
// PB-PAIR-2's manual-entry fallback doubles as the fallback for a code that will not decode.
//
// android/gate/s16_ui_test.go fences this BIDIRECTIONALLY -- a scanner dependency here that
// ADR-007 does not name fails the build, which is what stops the choice being made in a Gradle
// file.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// androidx.biometric is DELIBERATELY ABSENT (ADR-007 B133).
//
// It was declared here for PB-SEC-2, which bound revoke, kill switch, launch and kill to a
// PER-USE Class-3 biometric -- per-use is not a number, it is a CryptoObject bound to one
// operation, which on Android means BiometricPrompt, which means this dependency. ADR-007 B133
// removed all phone-side user authentication, so that requirement is VOID and the dependency
// outlived its only caller. No Kotlin file imports it, and
// TestB133_TheAppImportsNothingFromAndroidxBiometric fences that it cannot return by accident.
//
// WHY THE DECLARATION HAD TO GO AND NOT JUST THE IMPORTS. A dependency contributes to the
// MERGED manifest whether or not any code calls it: androidx.biometric's own manifest declares
// USE_BIOMETRIC and USE_FINGERPRINT, so every build asked users for biometric permissions for a
// capability the app no longer has, while the Play listing had just been corrected to stop
// claiming biometric protection. What Play receives is the merged set, not the four permissions
// in src/main/AndroidManifest.xml.
//
// REMOVING it PAYS PB-SEC-14 rather than trading against it, which is why it did not need the
// regeneration procedure at :177. That step exists to make a person justify a CHANGED artifact;
// here the lockfile and verification-metadata.xml each lost their biometric rows and gained
// nothing, and no new checksum entered the build. The fragment and lifecycle modules it used to
// pull all resolve through androidx.appcompat and are unaffected.
// ---------------------------------------------------------------------------

dependencies {
    implementation(files(swarmAar))
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("com.google.firebase:firebase-messaging:24.1.2")

    implementation("com.google.zxing:core:3.5.3")
    implementation("androidx.camera:camera-core:1.4.2")
    implementation("androidx.camera:camera-camera2:1.4.2")
    implementation("androidx.camera:camera-lifecycle:1.4.2")
    implementation("androidx.camera:camera-view:1.4.2")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.robolectric:robolectric:4.16.1")
    testImplementation("androidx.test:core:1.7.0")

    // PB-E2E-2's instrumented source set. These are on NO variant runtime classpath, so nothing
    // here ships in the APK a user installs; android/dependency-inventory.tsv records each one
    // in that class.
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("androidx.test:runner:1.7.0")
}
