package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.kit.KitTag
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9's navigation: **the tab bar is the window's, and
 * tapping a tab goes somewhere.**
 *
 * THE STATE OF THE WORLD BEFORE THIS FILE, verified rather than assumed:
 *
 *	TabItem                 carries a label, an icon, a selection and a badge, and NO tap handler
 *	tabBar(...)             called from exactly one place -- inside `triageInboxView`
 *	machinesPanelView       zero production call sites
 *	activityPanelView       zero production call sites
 *
 * so the bar renders four tabs that do nothing, and two screens that are built, composed from the
 * kit and covered by their own suites cannot be reached from the app at all. That is the same
 * defect PB-DS-6 was recorded NOT MET over one level up -- a component library nothing renders --
 * and a screen nothing navigates to is worth exactly as much.
 *
 * WHY THE BAR CANNOT STAY INSIDE THE INBOX, which is the structural fact this file pins. `tabBar`
 * is composed by the inbox view and `machinesPanelView` composes none, so a tab that merely swapped
 * the content would land the user on Machines with no bar to come back with. The bar therefore
 * belongs to a SCAFFOLD that hosts the selected destination above one shared bar, and
 * `PhoneScaffoldViewTest`'s subject is that composition in isolation. This file is the other half:
 * that the app the user actually opens is built that way.
 *
 * WHAT IT CAN AND CANNOT SEE. The phone core is a gomobile AAR carrying .so files cross-compiled
 * for Android ABIs, so `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run
 * and everything past that branch is out of reach here (the argument is
 * android/gate/pbapp6_pbinput2_surface_test.go's, in full). That bounds this file to what is true
 * on BOTH branches: the bar is the window's chrome, it survives on every destination, and tapping
 * a tab changes what is under it. A destination's own composition is asserted where it can be --
 * `ActivityPanelViewTest`, `SettingsPanelConnectionViewTest` -- over models this JVM can build.
 *
 * THE FOURTH TAB IS GONE (agents-tracker-nx44.3) and the paragraphs above are left as written,
 * because they are the record of why the bar moved out of the inbox and that reasoning is
 * unchanged by there being one destination fewer. `machinesPanelView` no longer exists.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceNavigationTest {

    /**
     * The destinations this app has, in the order the artifact draws the tabs it kept.
     *
     * AMENDED BY agents-tracker-nx44.3. It read `listOf("Inbox", "Machines", "Activity",
     * "Settings")` -- inventory C1.4's four -- and the Machines destination is deleted: everything
     * on it was either failure prose or four gap cards, and the two facts a person wanted from it
     * are the Settings screen's CONNECTION section now. The artifact still draws four tabs and its
     * fourth glyph still joins to `swarm_tab_machines` (android/gate/tabbar_test.go reads the
     * owner-signed artifact and is not edited for this); what changed is which of them this app
     * puts a destination behind.
     */
    private val destinations = listOf("Inbox", "Activity", "Settings")

    /**
     * A control from the INBOX destination, by the words on it.
     *
     * It is the launch form's submit: `PhoneSurface` hosts the launch form in the column below the
     * inbox, and that column is what a tab change has to take off screen. Chosen because it is
     * attached on the Unavailable branch too -- `renderUnavailable` draws the form deliberately,
     * so this assertion is about navigation rather than about which branch the phone is on.
     */
    private val inboxControl = "launch"

    @Test
    fun `the window carries the tab bar, whatever is under it`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertEquals(
                    "the tab bar is not on the phone screen at all, so the four destinations " +
                        "inventory C1.4 names have no way in. It is composed inside " +
                        "`triageInboxView`, which is one destination among the four -- a bar that " +
                        "belongs to the inbox is a bar the other three screens do not have",
                    destinations,
                    activity.tabLabels(),
                )
            }
        }
    }

    @Test
    fun `the bar survives on every destination it navigates to`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (destination in destinations) {
                    activity.tapTab(destination)
                    assertEquals(
                        "the bar is gone after navigating to $destination, so the user is on a " +
                            "screen with no way back. This is what a tab that swapped the " +
                            "content without lifting the bar out of the inbox produces",
                        destinations,
                        activity.tabLabels(),
                    )
                }
            }
        }
    }

    @Test
    fun `tapping a tab swaps what is under the bar, and coming back restores it`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertTrue(
                    "the inbox destination does not carry the launch form, so this test cannot " +
                        "tell a tab that navigates from one that does nothing",
                    activity.showsInboxControl(),
                )

                activity.tapTab("Activity")
                assertTrue(
                    "the inbox's own controls are STILL on screen after the Activity tab was " +
                        "tapped, so the tab is decoration: `TabItem` carries no tap handler and " +
                        "nothing swaps the content host's child",
                    !activity.showsInboxControl(),
                )

                activity.tapTab("Inbox")
                assertTrue(
                    "the Inbox tab does not bring the inbox back, so navigation is one-way",
                    activity.showsInboxControl(),
                )
            }
        }
    }

    /**
     * agents-tracker-nx44.3: **the fourth tab is gone, and pressing where it was does nothing.**
     *
     * THIS TEST REPLACES ONE THAT PINNED THE OPPOSITE, and the deleted assertion is quoted rather
     * than dropped. It read:
     *
     *	activity.tapTab("Machines")
     *	assertEquals(listOf(MachinesPanelScreen.UNAVAILABLE_COPY), activity.readableContent())
     *
     * -- PB-DS-9's empty-section rule applied to a whole destination, which was the right call
     * while the destination existed and could not be drawn (agents-tracker-xtj). The fold deletes
     * the destination itself, so the sentence has nothing left to qualify: `MachinesPanelScreen`
     * and its `UNAVAILABLE_COPY` are deleted with it, and this asserts what replaced them --
     * nothing on the bar leads there, so a user cannot arrive at a screen with no content.
     */
    @Test
    fun `there is no machines destination to arrive at`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertTrue(
                    "the bar still draws a Machines tab. Its whole content was four gap cards " +
                        "and a sentence saying this phone could not read its machine's details, " +
                        "which is the page field test 3 could not find a purpose for",
                    activity.tabLabels().none { it == "Machines" },
                )
            }
        }
    }

    // -----------------------------------------------------------------------
    // Reading the window.
    //
    // The bar is found by the tag the KIT puts on a tab's label, never by child index: an
    // assertion that walked indices would start checking a different view the day a component
    // gained a child, silently. It is deliberately NOT found by the screen's own tag either --
    // the tag moves as part of the change under test, and a test that asked for it would fail as
    // a missing constant rather than as a missing tab bar.
    // -----------------------------------------------------------------------

    private fun android.app.Activity.onScreen(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun android.app.Activity.tabLabels(): List<String> = onScreen()
        .filterIsInstance<TextView>()
        .filter { it.tag == KitTag.TAB_LABEL }
        .map { it.text.toString() }

    /**
     * Press the tab reading [label].
     *
     * THE TAP GOES ON THE TAB AND NOT ON ITS TEXT. The kit builds a tab as a column holding the
     * glyph frame and the label, and the whole column is the target -- a listener on the words
     * alone would leave the icon half of a 74 dp bar dead.
     */
    private fun android.app.Activity.tapTab(label: String) {
        val text = onScreen()
            .filterIsInstance<TextView>()
            .firstOrNull { it.tag == KitTag.TAB_LABEL && it.text.toString() == label }
        assertTrue("there is no tab labelled \"$label\" on screen", text != null)
        val tab = text!!.parent as View
        assertTrue(
            "the \"$label\" tab has no click listener, so `TabItem` still carries no tap handler " +
                "and the bar is four labels a user can press with nothing behind them",
            tab.hasOnClickListeners(),
        )
        tab.performClick()
    }

    private fun android.app.Activity.showsInboxControl(): Boolean = onScreen()
        .filterIsInstance<TextView>()
        .filter { it is Button || it.hasOnClickListeners() }
        .any { it.text.toString().lowercase().contains(inboxControl) }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
