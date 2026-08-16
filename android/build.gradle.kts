// AGP is deliberately on the 8.x line. From AGP 9.0 the `org.jetbrains.kotlin.android` plugin
// is rejected outright in favour of AGP's built-in Kotlin support, and PB-TOOL-6's gate
// asserts that plugin is applied -- the mechanism by which src/test/kotlin gets compiled is
// part of what that requirement pins. Moving to AGP 9 is a real decision with a test to
// change, not a version bump, and it is not this slice's.
plugins {
    id("com.android.application") version "8.13.2" apply false
    id("org.jetbrains.kotlin.android") version "2.3.21" apply false
    // The google-services plugin (R3, ADR-015): the VERSION is pinned here, on the build
    // classpath, like every other plugin above -- `apply false`, because whether it is
    // APPLIED is the app module's decision and is conditional on the gitignored
    // android/app/google-services.json existing locally. CI has no such file and must keep
    // resolving this classpath all the same, which is why the pin cannot live behind the
    // conditional. See android/app/build.gradle.kts for the conditional and its reasons.
    id("com.google.gms.google-services") version "4.4.4" apply false
}
