package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import androidx.lifecycle.Lifecycle
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.ToastHost
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the seam derivation row 1 needs and this app did not have: a
 * toast is hosted by the WINDOW, not by a screen.
 *
 * WHY THE HIERARCHY IS THE ASSERTION. `PhoneSurface` rebuilds its own root on every tab change and
 * on every unpaired/paired transition -- `host.removeAllViews()` in both draw paths -- so a toast
 * parented there would be destroyed by the next journal event to arrive, which is the clock this
 * app redraws on. It also has to sit ABOVE the tab bar rather than inside a destination: the four
 * destinations are swapped under one bar, and a toast belonging to one of them would be a message
 * that disappears when the user looks at another screen to see what happened.
 *
 * WHAT THIS CANNOT ASSERT. The phone core is a gomobile AAR and does not load on the unit-test JVM,
 * so no press here reaches a verb and no answer settles: what a refusal PUTS in the toast is
 * `PressFeedbackTest`'s, on the model both surfaces route through. This is the other half -- that
 * there is somewhere for it to land, in a window that will still be standing when it does.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceToastTest {

    private fun descendants(view: View): List<View> = when (view) {
        is ViewGroup -> listOf(view) + (0 until view.childCount).flatMap {
            descendants(view.getChildAt(it))
        }
        else -> listOf(view)
    }

    private fun hostIn(activity: PhoneActivity): ToastHost? =
        descendants(activity.window.decorView).filterIsInstance<ToastHost>().singleOrNull()

    @Test
    fun the_window_hosts_exactly_one_toast_overlay() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertNotNull(
                    "the window hosts no ToastHost, so derivation row 1's component has nowhere " +
                        "to be shown and every press answer stays in the three text lines it is " +
                        "in today",
                    hostIn(activity),
                )
            }
        }
    }

    /**
     * IT IS THE LAST CHILD OF THE ROOT, which is what "above the tab bar" means in a FrameLayout:
     * z-order is child order. A toast under the scaffold is a toast behind an opaque bar.
     */
    @Test
    fun the_toast_overlay_is_drawn_above_everything_the_surface_composes() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val toasts = requireNotNull(hostIn(activity))
                val root = requireNotNull(toasts.parent as? ViewGroup)

                assertEquals(
                    "the toast overlay is not the topmost child of the surface's root, so the " +
                        "tab bar draws over it",
                    root.childCount - 1,
                    root.indexOfChild(toasts),
                )
                assertTrue(
                    "the surface's root holds nothing but the toast overlay, so the app it is " +
                        "supposed to float over is somewhere else entirely",
                    root.childCount > 1,
                )
                assertTrue(
                    "the toast overlay is not attached to the window, so nothing it is asked to " +
                        "say can be drawn",
                    toasts.isAttachedToWindow,
                )
            }
        }
    }

    /**
     * The handler discipline this codebase already keeps: `PairingSurface.release` clears its
     * poller, because a callback queued against a screen nobody is holding is the one thing that
     * outlives it.
     *
     * WHAT IT COSTS TO SKIP, which is small and is still wrong in the way that matters: a toast
     * shown as the user leaves is a message they did not read, and it would be waiting for them
     * -- with whatever is left of 3.2 seconds -- on a screen they come back to minutes later, over
     * whatever it says by then.
     */
    @Test
    fun leaving_the_screen_takes_down_a_toast_that_was_up() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                requireNotNull(hostIn(activity)).show("Interrupt sent")
            }
            scenario.moveToState(Lifecycle.State.CREATED)
            scenario.onActivity { activity ->
                val toast = requireNotNull(hostIn(activity)).kitRequire(KitTag.TOAST)
                assertEquals(
                    "the surface was released with a toast still up, and its expiry still queued " +
                        "against the view",
                    View.GONE,
                    toast.visibility,
                )
            }
        }
    }
}
