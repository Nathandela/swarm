package dev.swarm.phone

import android.app.Activity
import android.os.SystemClock
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import org.junit.Assert.fail

/**
 * PB-E2E-2's driver: it presses the app's OWN CONTROLS and reads the app's own screen.
 *
 * WHY IT WORKS THIS WAY RATHER THAN CALLING THE FACADE. An instrumented test that reached
 * `swarmmobile.App` directly would prove the Go core runs on an Android runtime -- worth
 * something, and NOT this requirement, whose words are "APK installs, pairs against a local
 * relay + daemon, SAS matches, observes, takes control, types". Every step below therefore goes
 * through a View: a button is found by the label a user reads and clicked, and what is asserted
 * is what the screen says afterwards.
 *
 * IT NEVER SKIPS. Every precondition it needs is an instrumentation argument, and a missing one
 * is a FAILURE naming what was not supplied. A skipped exit demonstration is the demonstration
 * not happening.
 *
 * PB-E2E-5 STAYS DEFERRED, and this file is one of the places that would quietly breach it. The
 * QR reaches the app through PB-PAIR-2's manual-entry path -- the same payload, the same
 * `BeginPairing`, the same display-then-confirm step -- and NOT through the camera. Nothing
 * produced by this run is evidence about a physical camera, a real biometric, real FCM delivery,
 * Doze, or hardware Keystore attestation. An emulator is not a handset.
 */
object PhoneScreenDriver {

    /** How long any one transition may take before the run is called failed. */
    const val PATIENCE_MILLIS = 30_000L

    /**
     * An instrumentation argument, or a failure saying which one is missing and what supplies it.
     *
     * @param supplies the runbook step that produces the value, so a failure is actionable
     *  rather than an accusation.
     */
    fun require(arguments: android.os.Bundle, name: String, supplies: String): String {
        val value = arguments.getString(name)
        if (value.isNullOrBlank()) {
            fail(
                "PB-E2E-2: no instrumentation argument -e $name. $supplies\n" +
                    "Reported as a failure rather than a skip: this test IS the exit " +
                    "demonstration, and a skip reads as green in a run summary.",
            )
        }
        return checkNotNull(value)
    }

    /** Every TextView's text on screen, joined, which is what an assertion reads. */
    fun screenText(activity: Activity): String = activity
        .findViewById<ViewGroup>(android.R.id.content)
        .flatten()
        .filterIsInstance<TextView>()
        .filter { it.visibility == View.VISIBLE }
        .joinToString("\n") { it.text.toString() }

    fun ActivityScenario<PhoneActivity>.awaitScreen(needle: String, why: String) {
        val deadline = SystemClock.uptimeMillis() + PATIENCE_MILLIS
        var last = ""
        while (SystemClock.uptimeMillis() < deadline) {
            onActivity { last = screenText(it) }
            if (last.contains(needle)) return
            Thread.sleep(200)
        }
        fail("PB-E2E-2: $why\nwaited for: $needle\nthe screen said:\n$last")
    }

    /**
     * Wait until a control becomes pressable.
     *
     * It is how "observes" is asserted without inventing a fact the test would have to be told.
     * `PhoneSurface.setActionsEnabled` raises the session controls only once the triage inbox
     * yielded a row, so an enabled Take control IS the phone having drawn the machine's roster.
     *
     * SEND LINE IS RAISED BY A DIFFERENT FACT and it is worth waiting on separately:
     * `PhoneSurface.renderLease` enables it from `TerminalPeek.keyboardEnabled`, which is the
     * lease the MACHINE confirmed and the link being up (PB-INPUT-2). So an enabled Send is the
     * take_control having been answered, not the roster having a row.
     */
    fun ActivityScenario<PhoneActivity>.awaitPressable(label: String, why: String) {
        val deadline = SystemClock.uptimeMillis() + PATIENCE_MILLIS
        var ready = false
        while (SystemClock.uptimeMillis() < deadline) {
            onActivity { activity ->
                ready = activity.controls().filterIsInstance<Button>().any {
                    it.text.toString() == label && it.visibility == View.VISIBLE && it.isEnabled
                }
            }
            if (ready) return
            Thread.sleep(200)
        }
        fail("PB-E2E-2: \"$label\" never became pressable. $why\nthe screen said:\n${textOnScreen()}")
    }

    /** Press the control a user would press, found by the label they would read. */
    fun ActivityScenario<PhoneActivity>.press(label: String) {
        var pressed = false
        onActivity { activity ->
            val button = activity.controls().filterIsInstance<Button>()
                .firstOrNull { it.text.toString() == label && it.visibility == View.VISIBLE }
            if (button != null && button.isEnabled) {
                button.performClick()
                pressed = true
            }
        }
        if (!pressed) {
            var seen = ""
            onActivity { activity ->
                seen = activity.controls().filterIsInstance<Button>()
                    .joinToString("\n") {
                        "${it.text} visible=${it.visibility == View.VISIBLE} enabled=${it.isEnabled}"
                    }
            }
            fail(
                "PB-E2E-2: no enabled, visible control labelled \"$label\" on screen, so the " +
                    "action this clause of the requirement names cannot be performed.\n" +
                    "the controls present were:\n$seen",
            )
        }
    }

    /** Type into the field a user would type into, found by its hint. */
    fun ActivityScenario<PhoneActivity>.type(hint: String, text: String) {
        var typed = false
        onActivity { activity ->
            val field = activity.controls().filterIsInstance<EditText>()
                .firstOrNull { it.hint?.toString() == hint && it.visibility == View.VISIBLE }
            if (field != null) {
                field.setText(text)
                typed = true
            }
        }
        if (!typed) {
            fail("PB-E2E-2: no visible field hinted \"$hint\", so there is nothing to type into")
        }
    }

    fun ActivityScenario<PhoneActivity>.textOnScreen(): String {
        var text = ""
        onActivity { text = screenText(it) }
        return text
    }

    internal fun Activity.controls(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
