package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.screens.ScaffoldTag
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2pnu F4, over the WINDOW the user actually
 * opens. It is `PhoneSurfaceBannerTest` restored -- deleted in d5262d5 with the banner it was
 * named for -- with [ScaffoldTag.BANNER] renamed to the slot that replaced it.
 *
 * THE THREE CLAIMS ARE THE SAME THREE AND THEY ARE STILL WINDOW-LEVEL, which is the whole reason
 * this file exists rather than more cases in `SyncStatusViewTest`. That suite asserts the
 * composition in isolation: a scaffold handed a status view draws it above the content and outside
 * the scroll. This is the other half -- that the app WIRES one, and that the wiring is to the
 * scaffold rather than to a destination's own column.
 *
 * WHY THAT NEEDS ITS OWN FILE, in the deleted file's own words, because it is the record of how
 * the original defect shipped: "The defect was never in a view: `PhoneSurface.status` rendered
 * correctly, in a container the surface detached on the way to every other destination
 * (`hostContent` -> `detachHostedViews`). A suite that only exercised `triageInboxView` and
 * `phoneScaffoldView` would have stayed green through the whole of it, which is what happened."
 * Asserting one level lower is exactly the seam that missed it.
 *
 * WHAT IT CAN AND CANNOT SEE. The phone core is a gomobile AAR carrying .so files cross-compiled
 * for Android ABIs, so `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run
 * and everything past that branch is out of reach here -- the argument is
 * android/gate/pbapp6_pbinput2_surface_test.go's, in full, and `PhoneSurfaceNavigationTest` is
 * bounded the same way. So what this file asserts is the SLOT: that it exists, that it is the
 * scaffold's, and that navigating does not take it away. What each line SAYS is asserted where
 * this JVM can build the models -- `SyncStatusTest` and `SyncStatusViewTest`.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceSyncSlotTest {

    /**
     * AMENDED BY agents-tracker-nx44.3, which is the one change from the deleted file besides the
     * tag. It read `listOf("Inbox", "Machines", "Activity", "Settings")` and the Machines
     * destination no longer exists; `PhoneSurfaceNavigationTest` carries the same list for the
     * same reason.
     */
    private val destinations = listOf("Inbox", "Activity", "Settings")

    @Test
    fun `the window carries the sync status slot`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertNotNull(
                    "there is no sync status slot on the phone screen at all. PB-APP-8's " +
                        "offline/reconnecting/stale states and PB-APP-11's freshness verdict are " +
                        "written to a line inside one destination's own column -- so the link " +
                        "dropping changes nothing on screen for a user standing anywhere else",
                    activity.statusSlot(),
                )
            }
        }
    }

    @Test
    fun `the sync status slot survives every destination the bar navigates to`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (destination in destinations) {
                    activity.tapTab(destination)
                    assertNotNull(
                        "the sync status slot is gone after navigating to $destination, so a " +
                            "machine that goes silent while the user is on that tab has nowhere " +
                            "to say so. This is exactly what `detachHostedViews` did to the " +
                            "status line",
                        activity.statusSlot(),
                    )
                }
            }
        }
    }

    @Test
    fun `the sync status is not a child of the scaffold's scrolling content`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val content = activity.onScreen().first { it.tag == ScaffoldTag.CONTENT }

                assertTrue(
                    "the sync status is inside the scaffold's ScrollView, so on a long " +
                        "destination it scrolls off the top -- the same disappearance in a " +
                        "slower form",
                    (content as ViewGroup).flatten().none { it.tag == ScaffoldTag.STATUS },
                )
            }
        }
    }

    /**
     * The negative control, and it carries more weight here than usual: the three claims above
     * pass against HEAD as written, because what d5262d5 deleted was the coverage and not the
     * behaviour. A reader that answered "found it" for anything would restore a file that can
     * never fail, which is the one outcome worse than the gap it fills.
     */
    @Test
    fun `the window readers can actually answer no`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertNull(
                    "the slot reader answers for a tag no view carries, so all three claims " +
                        "above could be reading whatever they happened to reach first",
                    activity.onScreen().firstOrNull { it.tag == "no view carries this" },
                )
                assertTrue(
                    "the walk over the window found nothing tagged at all, so 'the slot is " +
                        "there' would be indistinguishable from 'the walk sees nothing'",
                    activity.onScreen().any { it.tag == ScaffoldTag.CONTENT },
                )
                assertTrue(
                    "every destination in the list is reachable, so the navigation claim above " +
                        "is over three real tab presses rather than over an empty loop",
                    destinations.all { label ->
                        activity.onScreen().filterIsInstance<android.widget.TextView>().any {
                            it.tag == KitTag.TAB_LABEL && it.text.toString() == label
                        }
                    },
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

    private fun android.app.Activity.statusSlot(): View? =
        onScreen().firstOrNull { it.tag == ScaffoldTag.STATUS }

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
