package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.screens.ScaffoldTag
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the half of chat-surface-plan B.5 that is owed rather than
 * given up: **the two universal scaffold assertions are narrowed to the three tab destinations,
 * and this is what replaces the coverage they lose.**
 *
 * WHAT WAS NARROWED, QUOTED SO A DIFF CANNOT HIDE IT. `PhoneSurfaceNavigationTest` asserts that the
 * tab bar is on screen "whatever is under it" and that it "survives on every destination it
 * navigates to"; `PhoneScaffoldViewTest` sweeps `Destination.entries` for the same bar. All three
 * were written to catch exactly the change this wave makes -- a screen that drops the bar -- and
 * all three are now claims about the THREE TAB DESTINATIONS rather than about whatever is on
 * screen. A universal claim quietly weakened and a universal claim replaced by two specific ones
 * look identical in a diff, so the replacement is written down here, in the positive direction:
 * on a tab destination the bar is there and the conversation's own regions are NOT.
 *
 * **WHAT THIS FILE CANNOT REACH, AND IT IS RECORDED RATHER THAN LEFT AS A GAP.** The conversation
 * itself. `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run -- the phone
 * core is a gomobile AAR of `.so` files cross-compiled for Android ABIs -- so the roster is empty,
 * no session row exists, and nothing can open a drill-down here. That is the same limit
 * `android/gate/s24_screens_test.go` answers with a source scan for the session detail
 * (`TestPBAPP3_TheSessionDetailIsReachedFromTheApp`), and the conversation scaffold and the
 * conversation header need the same fence: they are composed, covered by their own suites, and a
 * JVM cannot prove the app renders them. That request is filed with the gate's owner. What IS
 * proved here is everything true on BOTH branches -- which is precisely what this suite's sibling
 * bounds itself to as well.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceConversationHostTest {

    private val destinations = listOf("Inbox", "Activity", "Settings")

    /**
     * The conversation's own fixed regions, by the scaffold's own names.
     *
     * THEY ARE ASKED FOR BY TAG AND NOT BY COMPONENT because the claim is about ARRANGEMENT: a
     * header inside the tab scaffold's one `ScrollView` would still be a header, and would still
     * slide away under a reader. What must not exist on a tab destination is a fixed region.
     */
    private val conversationRegions = listOf(ScaffoldTag.HEADER, ScaffoldTag.COMPOSER)

    @Test
    fun `a tab destination keeps its bar and has none of the conversation's fixed regions`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (destination in destinations) {
                    activity.tapTab(destination)

                    assertEquals(
                        "the bar is gone after navigating to $destination, so the user is on a " +
                            "screen with no way back. The conversation drops the bar; a TAB " +
                            "destination may not, and narrowing the universal assertions must " +
                            "not have quietly narrowed this one too",
                        destinations,
                        activity.tabLabels(),
                    )
                    for (region in conversationRegions) {
                        assertNull(
                            "$destination carries \"$region\", the conversation's own fixed " +
                                "region. The two compositions are separate precisely so a " +
                                "destination cannot acquire a pinned composer or a fixed header " +
                                "by accident -- and a tab destination that had one would be a " +
                                "third arrangement nobody drew",
                            activity.taggedOrNull(region),
                        )
                    }
                }
            }
        }
    }

    /**
     * The connection strip is the one part BOTH compositions have, which is what makes keeping it
     * on the conversation a decision rather than an inheritance (plan B.2).
     */
    @Test
    fun `the connection strip's slot is on every tab destination`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (destination in destinations) {
                    activity.tapTab(destination)
                    assertTrue(
                        "$destination has no slot for the app's sync chrome, so a link that " +
                            "dropped while the user was standing here would change nothing on " +
                            "screen -- the moment PB-APP-8 exists for",
                        activity.taggedOrNull(ScaffoldTag.STATUS) != null,
                    )
                }
            }
        }
    }

    /**
     * The window holds ONE root at a time, which is the whole of what "a second top-level
     * composition inside the existing Activity" means.
     *
     * IT IS ASSERTED BECAUSE THE ALTERNATIVE IS SILENT. Two roots stacked in the same host draw on
     * top of each other: the one underneath keeps its listeners, keeps taking accessibility focus,
     * and keeps whatever the user had typed in it, while the one on top looks correct. A swap that
     * forgot its `removeAllViews` would pass every assertion about what is on screen.
     */
    @Test
    fun `swapping compositions replaces the root rather than stacking a second one`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (destination in destinations + destinations) {
                    activity.tapTab(destination)
                    assertEquals(
                        "the window holds more than one composition at once after navigating to " +
                            "$destination, so a root the user has left is still on screen under " +
                            "the one they are looking at",
                        1,
                        activity.appHostChildCount(),
                    )
                }
            }
        }
    }

    // -----------------------------------------------------------------------
    // Reading the window. Found by the tags the KIT and the SCAFFOLD put on their parts, never by
    // child index: an assertion that walked indices would start checking a different view the day
    // a composition gained a child, silently.
    // -----------------------------------------------------------------------

    private fun android.app.Activity.onScreen(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun android.app.Activity.tabLabels(): List<String> = onScreen()
        .filterIsInstance<android.widget.TextView>()
        .filter { it.tag == KitTag.TAB_LABEL }
        .map { it.text.toString() }

    private fun android.app.Activity.taggedOrNull(tag: String): View? =
        onScreen().firstOrNull { it.tag == tag }

    /**
     * Press the tab reading [label].
     *
     * THE TAP GOES ON THE TAB AND NOT ON ITS TEXT: the kit builds a tab as a column holding the
     * glyph frame and the label, and the whole column is the target. `PhoneSurfaceNavigationTest`
     * carries the same helper for the same reason; it is private there, and a test that reached
     * across into another suite's internals would break the day that suite changed shape.
     */
    private fun android.app.Activity.tapTab(label: String) {
        val text = onScreen()
            .filterIsInstance<android.widget.TextView>()
            .firstOrNull { it.tag == KitTag.TAB_LABEL && it.text.toString() == label }
        assertTrue("there is no tab labelled \"$label\" on screen", text != null)
        (text!!.parent as View).performClick()
    }

    /**
     * How many roots the app host holds.
     *
     * THE HOST IS FOUND BY ITS OWN TAG and not by walking up from the composition, because the
     * walk would have to know how deep each composition is -- and the two are different depths,
     * which is the whole subject of this file.
     */
    private fun android.app.Activity.appHostChildCount(): Int {
        val host = taggedOrNull(PHONE_APP_HOST)
        assertTrue("the app host is not on screen at all", host != null)
        return (host as ViewGroup).childCount
    }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
