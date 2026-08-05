package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.ui.kit.KitTag
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-j171 -- **the Activity and Settings tabs
 * render a black void when the phone core is unavailable, with no explanation on either.**
 *
 * THE ROUTED MESSAGE HAS SOMEWHERE TO GO ON EVERY OTHER DESTINATION AND NOT ON THESE TWO.
 * [PhoneSurface.renderUnavailable] writes PB-APP-9's routed startup failure onto `status`, a
 * child of `unrecomposedControls` -- so the Inbox tab shows it, and `PhoneSurfaceNavigationTest`
 * already pins that the Machines tab says something of its own
 * ([dev.swarm.phone.ui.screens.MachinesPanelScreen.UNAVAILABLE_COPY]) rather than nothing.
 * `drawActivity(null)` calls `hostContent(null)` with no fallback, and
 * `SettingsSurface.render()`'s `Unavailable` branch clears its host and hides its own outcome
 * line -- so a user who taps either tab while the phone core refused construction is shown an
 * empty area under the bar, indistinguishable from a crash.
 *
 * THE EXPECTED TEXT IS DERIVED, NEVER TRANSCRIBED. `startup.error.message` depends on whatever
 * this JVM's Keystore/native-library probe actually throws, which this file does not predict --
 * it reads it back off the SAME [PhoneRuntime] instance [PhoneSurface] renders from
 * ([SwarmApplication.phoneRuntime]), by calling [PhoneRuntime.phone] again. A refusal is never
 * latched (see that class's own KDoc), so the second call reproduces the identical
 * [PhoneStartup.Unavailable] the screen already rendered.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceUnavailableTabsTest {

    private fun routedMessage(): String {
        val runtime = ApplicationProvider.getApplicationContext<SwarmApplication>().phoneRuntime
        val startup = runtime.phone()
        assertTrue(
            "this suite bounds itself to the branch every JVM run takes -- PhoneRuntime.phone() " +
                "answered Ready, so there is no routed startup failure for this test to compare " +
                "against",
            startup is PhoneStartup.Unavailable,
        )
        return (startup as PhoneStartup.Unavailable).error.message
    }

    @Test
    fun `the activity destination shows the routed startup failure instead of a blank screen`() {
        val message = routedMessage()
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.tapTab("Activity")
                assertEquals(
                    "the Activity destination puts NOTHING on screen when the phone core refused " +
                        "construction: drawActivity(null) hosts a null view with no fallback, " +
                        "which is a blank area under the bar indistinguishable from a crash. It " +
                        "should carry the same routed reason the Inbox tab already shows",
                    listOf(message),
                    activity.readableContent(),
                )
            }
        }
    }

    @Test
    fun `the settings destination shows the routed startup failure instead of a blank screen`() {
        val message = routedMessage()
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                activity.tapTab("Settings")
                assertEquals(
                    "the Settings destination puts NOTHING on screen when the phone core refused " +
                        "construction: SettingsSurface.render()'s Unavailable branch clears its " +
                        "host and hides its own outcome line rather than showing it, which is a " +
                        "blank area under the bar indistinguishable from a crash",
                    listOf(message),
                    activity.readableContent(),
                )
            }
        }
    }

    // -----------------------------------------------------------------------
    // Reading the window -- copied from PhoneSurfaceNavigationTest's own helpers rather than
    // shared, because that file's are private to it and this suite must not modify an existing
    // test file to grow a seam for a new one.
    // -----------------------------------------------------------------------

    private fun android.app.Activity.onScreen(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun android.app.Activity.tapTab(label: String) {
        val text = onScreen()
            .filterIsInstance<TextView>()
            .firstOrNull { it.tag == KitTag.TAB_LABEL && it.text.toString() == label }
        assertTrue("there is no tab labelled \"$label\" on screen", text != null)
        (text!!.parent as View).performClick()
    }

    private fun android.app.Activity.readableContent(): List<String> = onScreen()
        .filterIsInstance<TextView>()
        .filter { it.tag != KitTag.TAB_LABEL }
        .map { it.text.toString() }
        .filter { it.isNotBlank() }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
