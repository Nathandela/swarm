// AGP is deliberately on the 8.x line. From AGP 9.0 the `org.jetbrains.kotlin.android` plugin
// is rejected outright in favour of AGP's built-in Kotlin support, and PB-TOOL-6's gate
// asserts that plugin is applied -- the mechanism by which src/test/kotlin gets compiled is
// part of what that requirement pins. Moving to AGP 9 is a real decision with a test to
// change, not a version bump, and it is not this slice's.
plugins {
    id("com.android.application") version "8.13.2" apply false
    id("org.jetbrains.kotlin.android") version "2.3.21" apply false
}
