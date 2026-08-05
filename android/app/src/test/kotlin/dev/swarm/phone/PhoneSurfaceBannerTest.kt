package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.screens.ScaffoldTag
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-e6mi over the WINDOW the user actually opens.
 *
 * `ScaffoldBannerTest` asserts the composition in isolation: a scaffold handed a banner draws it
 * above the content and outside the scroll. This is the other half -- that the app wires one, and
 * that the wiring is to the scaffold rather than to the inbox's own column.
 *
 * WHY THAT NEEDS ITS OWN FILE. The defect was never in a view: `PhoneSurface.status` rendered
 * correctly, in a container the surface detached on the way to every other destination
 * (`hostContent` -> `detachHostedViews`). A suite that only exercised `triageInboxView` and
 * `phoneScaffoldView` would have stayed green through the whole of it, which is what happened.
 *
 * WHAT IT CAN AND CANNOT SEE. The phone core is a gomobile AAR carrying .so files cross-compiled
 * for Android ABIs, so `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run
 * and everything past that branch is out of reach here -- the argument is
 * android/gate/pbapp6_pbinput2_surface_test.go's, in full, and `PhoneSurfaceNavigationTest` is
 * bounded the same way. So what this file asserts is the SLOT: that it exists, that it is the
 * scaffold's, and that navigating does not take it away. What each line SAYS is asserted where
 * this JVM can build the models -- `StatusBannerTest` and `ScaffoldBannerTest`.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceBannerTest {

    private val destinations = listOf("Inbox", "Machines", "Activity", "Settings")

    @Test
    fun `the window carries the banner slot`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertNotNull(
                    "there is no banner slot on the phone screen at all. PB-APP-8's " +
                        "offline/reconnecting/stale states and PB-APP-11's freshness verdict are " +
                        "written to a line inside the inbox's own column, which is one of four " +
                        "destinations -- so the link dropping changes nothing on screen for a " +
                        "user standing anywhere else",
                    activity.bannerSlot(),
                )
            }
        }
    }

    @Test
    fun `the banner slot survives every destination the bar navigates to`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (destination in destinations) {
                    activity.tapTab(destination)
                    assertNotNull(
                        "the banner slot is gone after navigating to $destination, so a machine " +
                            "that goes silent while the user is on that tab has nowhere to say " +
                            "so. This is exactly what `detachHostedViews` did to the status line",
                        activity.bannerSlot(),
                    )
                }
            }
        }
    }

    @Test
    fun `the banner is not a child of the scaffold's scrolling content`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val content = activity.onScreen().first { it.tag == ScaffoldTag.CONTENT }

                assertTrue(
                    "the banner is inside the scaffold's ScrollView, so on a long destination it " +
                        "scrolls off the top -- the same disappearance in a slower form",
                    (content as ViewGroup).flatten().none { it.tag == ScaffoldTag.BANNER },
                )
            }
        }
    }

    // -----------------------------------------------------------------------
    // Reading the window. The helpers are `PhoneSurfaceNavigationTest`'s, and the tap is found by
    // the tag the KIT puts on a tab's label rather than by child index, for the reason that file
    // records: an assertion that walked indices would silently start checking a different view.
    // -----------------------------------------------------------------------

    private fun android.app.Activity.onScreen(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun android.app.Activity.bannerSlot(): View? =
        onScreen().firstOrNull { it.tag == ScaffoldTag.BANNER }

    private fun android.app.Activity.tapTab(label: String) {
        val text = onScreen()
            .filterIsInstance<android.widget.TextView>()
            .firstOrNull { it.tag == KitTag.TAB_LABEL && it.text.toString() == label }
        assertTrue("there is no tab labelled \"$label\" on screen", text != null)
        (text!!.parent as View).performClick()
    }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
