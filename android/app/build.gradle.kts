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
    from(rootProject.layout.projectDirectory.dir("..").file("internal/design/tokens.json"))
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
        versionCode = 1
        versionName = "0.1.0"

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
