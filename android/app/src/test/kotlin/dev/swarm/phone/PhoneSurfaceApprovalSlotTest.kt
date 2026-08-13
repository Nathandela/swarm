package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.screens.SheetTag
import org.junit.Assert.assertNotNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-dwwv.2.4: **one approval host, and two places
 * it can be parented -- the inbox destination stays one of them.**
 *
 * WHY THIS FILE AND NOT `PhoneSurfaceSyncSlotTest`. That suite's own KDoc states the bound this
 * one inherits: `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run, so
 * `PhoneSurface.renderReady` -- and with it `drawDetail`, `drawApproval` with real data, and
 * `openApproval` -- is out of reach here. What IS reachable on this branch is `drawInbox`, which
 * now re-parents [PhoneSurface]'s `approvalHost` on every draw instead of holding it as a
 * permanent child of `unrecomposedControls` -- and that re-parenting has to survive the same
 * tab navigation every other slot on this window survives, or `approvalSlot()`'s detach-then-
 * `addView` throws "the specified child already has a parent" the first time a user leaves and
 * returns to the Inbox tab.
 *
 * WHAT THIS DOES NOT CLAIM. It says nothing about what the sheet SHOWS -- that needs a real
 * `App`, which this JVM cannot build -- and nothing about the session-detail host, which
 * `renderReady` never reaches here either. `SessionDetailViewTest` covers the second half, over
 * the view composition directly.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceApprovalSlotTest {

    @Test
    fun `the approval host is on the inbox destination`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertNotNull(
                    "there is no approval host on the Inbox destination at all, so a pending " +
                        "approval has nowhere to draw -- ApprovalSheetView's host is the same " +
                        "view `SessionDetailView` re-parents in, and this is the first of its " +
                        "two homes",
                    activity.approvalHost(),
                )
            }
        }
    }

    /**
     * THE REGRESSION THIS SUITE EXISTS TO CATCH. `approvalHost` used to be a permanent child of
     * `unrecomposedControls`, added once at construction and never touched again -- so it simply
     * could not be reparented anywhere. Making it a slot (`PhoneSurface.approvalSlot`, on
     * `statusSlot`'s own pattern) means every return to the Inbox tab now does a detach-and-
     * `addView` that a bug could get backwards -- attaching before detaching, or detaching the
     * wrong view -- and Android refuses that outright rather than warning.
     */
    @Test
    fun `the approval host survives navigating away from and back to the inbox`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                for (round in 1..3) {
                    activity.tapTab("Activity")
                    activity.tapTab("Settings")
                    activity.tapTab("Inbox")
                    assertNotNull(
                        "the approval host did not come back to the Inbox destination on round " +
                            "$round, so the same reparenting either dropped it or crashed getting " +
                            "here",
                        activity.approvalHost(),
                    )
                }
            }
        }
    }

    // -----------------------------------------------------------------------
    // Reading the window -- `PhoneSurfaceNavigationTest`'s own helpers, copied rather than
    // shared for that file's own reason: its helpers are private to it.
    // -----------------------------------------------------------------------

    private fun android.app.Activity.onScreen(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun android.app.Activity.approvalHost(): View? =
        onScreen().firstOrNull { it.tag == SheetTag.HOST }

    private fun android.app.Activity.tapTab(label: String) {
        val text = onScreen()
            .filterIsInstance<TextView>()
            .firstOrNull { it.tag == KitTag.TAB_LABEL && it.text.toString() == label }
        assertNotNull("there is no tab labelled \"$label\" on screen", text)
        (text!!.parent as View).performClick()
    }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
