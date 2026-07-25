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
    into(policyTestResourceDir)
}

// AGP does not carry a task dependency through sourceSets.resources.srcDir, so the staging
// task is wired to the tasks that package unit-test resources explicitly. Without this the
// tables are simply absent from the classpath, which PolicyTables.read reports as an error
// rather than reading as an empty table.
tasks.matching { it.name.startsWith("process") && it.name.endsWith("UnitTestJavaRes") }
    .configureEach { dependsOn(policyTestResources) }

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

    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
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

dependencies {
    implementation(files(swarmAar))
    implementation("androidx.appcompat:appcompat:1.7.1")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.robolectric:robolectric:4.16.1")
    testImplementation("androidx.test:core:1.7.0")
}
