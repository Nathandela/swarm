package dev.swarm.phone

import android.content.Intent
import android.net.Uri
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Phase B slice S18 -- PB-SEC-4 and PB-SEC-12 clause 1, asserted against a REAL Activity.
 *
 * These two requirements had no subject until this slice: the module declared no `<activity>`,
 * so the secure-window flag had no Window and the touch filter had no View, and
 * android/gate/s18_sec4_windowsecurity_test.go said so as a loud failure rather than a skip.
 * The Go gate asserts the SOURCE (one sink, a policy table naming every protected screen); this
 * file asserts the RUNTIME, which is the half a source scan cannot reach: that the flag is
 * actually on the window a user sees after onCreate, not merely written down somewhere.
 *
 * WHAT IS NOT CLAIMED, and PB-E2E-5 stays deferred. FLAG_SECURE is honoured by the system
 * compositor, and Robolectric has no compositor: this asserts the app ASKED for it. Whether a
 * screenshot is actually refused, and whether the recents thumbnail is actually dropped, are
 * physical-handset facts. `setRecentsScreenshotEnabled` is likewise asserted at the source
 * level by the Go gate and not here, because a Robolectric shadow returning what the test told
 * it to is not evidence about a thumbnail on disk.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneActivityWindowTest {

    // --- PB-SEC-4 -----------------------------------------------------------

    @Test
    fun the_window_carries_flag_secure_after_oncreate() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val flags = activity.window.attributes.flags
                assertNotEquals(
                    "PB-SEC-4: the window does not carry FLAG_SECURE. The pairing screen shows " +
                        "the SAS and the destination origin and the peek shows the session's " +
                        "terminal grid; without the flag both are screenshottable, screen " +
                        "recordable, and written to disk as the recents thumbnail when the app " +
                        "backgrounds",
                    0,
                    flags and WindowManager.LayoutParams.FLAG_SECURE,
                )
            }
        }
    }

    // --- PB-SEC-12 clause 1 -------------------------------------------------

    @Test
    fun every_gated_action_filters_obscured_touches() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val gated = activity.gatedActionViews()
                assertTrue(
                    "PB-SEC-12: the Activity exposes no gated action view, so this assertion " +
                        "has no subject. Tapjacking is the attack where an overlay covers a " +
                        "confirm button so the user's tap lands on something they cannot see, " +
                        "and the buttons at stake revoke a device and take control of a shell",
                    gated.isNotEmpty(),
                )
                for (view in gated) {
                    assertTrue(
                        "PB-SEC-12: a gated action view does not filter obscured touches",
                        view.filterTouchesWhenObscured,
                    )
                }
            }
        }
    }

    // --- PB-SEC-11 ----------------------------------------------------------

    @Test
    fun a_crafted_launch_intent_selects_nothing() {
        // The Activity is exported with a LAUNCHER filter, so any app on the device can start
        // it with any extras and any data URI. The defence is that it reads NONE of them: what
        // the screen shows is chosen from persisted local state alone. This drives the hostile
        // case and compares against the plain launch.
        val hostile = Intent(ApplicationProvider.getApplicationContext(), PhoneActivity::class.java)
            .setData(Uri.parse("swarm://take-control/some-session"))
            .putExtra("session", "../../etc/passwd")
            .putExtra("action", "revoke")
            .putExtra("relay", "https://attacker.example")

        val plain = ActivityScenario.launch(PhoneActivity::class.java).use { it.renderedText() }
        val crafted = ActivityScenario.launch<PhoneActivity>(hostile).use { it.renderedText() }

        assertEquals(
            "PB-SEC-11: a crafted intent changed what the exported Activity rendered, so a " +
                "third-party app can steer this screen by sending one",
            plain,
            crafted,
        )
    }

    private fun ActivityScenario<PhoneActivity>.renderedText(): String {
        var text = ""
        onActivity { activity ->
            text = activity.findViewById<ViewGroup>(android.R.id.content).allText()
        }
        return text
    }

    private fun View.allText(): String = when (this) {
        is ViewGroup -> (0 until childCount).joinToString("|") { getChildAt(it).allText() }
        is android.widget.TextView -> text.toString()
        else -> ""
    }
}
