package dev.swarm.phone

import android.content.Intent
import android.net.Uri
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Phase B slice S18 -- PB-SEC-4 (INVERTED, see below), PB-SEC-11 and PB-SEC-12 clause 1,
 * asserted against a REAL Activity.
 *
 * These requirements had no subject until this slice: the module declared no `<activity>`, so
 * the window flag had no Window and the touch filter had no View, and
 * android/gate/s18_sec4_windowsecurity_test.go said so as a loud failure rather than a skip.
 * The Go gate asserts the SOURCE; this file asserts the RUNTIME, which is the half a source
 * scan cannot reach: what is actually on the window a user sees after onCreate, rather than
 * what is written down somewhere.
 *
 * PB-SEC-4 IS WITHDRAWN AND THIS FILE'S ASSERTION IS INVERTED WITH IT. ADR-007 B65, ruled by
 * the owner on 2026-07-26: the shipped app allows screenshots and screen recording. So the
 * assertion below runs in the negative -- the window must NOT carry FLAG_SECURE -- and it is
 * the half the Go gate cannot cover: a source scan sees the constant's NAME, and this sees the
 * bit, so a flag set as a raw 0x2000 or through a theme attribute fails here.
 *
 * WHAT IS NOT CLAIMED, and PB-E2E-5 stays deferred. Robolectric has no compositor: this asserts
 * what the app asked the window for. Whether a screenshot is actually taken, and whether the
 * recents thumbnail actually appears, are physical-handset facts.
 * `setRecentsScreenshotEnabled` is asserted at the source level by the Go gate and not here,
 * because a Robolectric shadow returning what the test told it to is not evidence about a
 * thumbnail on disk.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneActivityWindowTest {

    // --- PB-SEC-4 -----------------------------------------------------------

    @Test
    fun the_window_does_not_carry_flag_secure_after_oncreate() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val flags = activity.window.attributes.flags
                assertEquals(
                    "PB-SEC-4/B65: the window carries FLAG_SECURE. THE SHIPPED APP ALLOWS " +
                        "SCREENSHOTS -- a product decision the owner made on 2026-07-26, " +
                        "recorded in docs/adr/ADR-007-remote-access.md under B65. Read it before " +
                        "changing this assertion. The flag is a compositor hint: not attested, " +
                        "no defence against a camera pointed at the screen, and none against an " +
                        "accessibility service, which reads the rendered screen regardless -- " +
                        "while it blocks users of a developer tool from sharing terminal output. " +
                        "This runs in the negative on purpose, and it catches what the source " +
                        "gate cannot: a flag set as a raw constant or through a theme",
                    0,
                    flags and WindowManager.LayoutParams.FLAG_SECURE,
                )
            }
        }
    }

    // --- PB-SEC-12 clause 1 -------------------------------------------------

    /**
     * PB-SEC-12 clause 1 SURVIVES ADR-007 B133 AND MATTERS MORE, which is why this is re-anchored
     * rather than deleted along with everything else the word "gated" used to attach to.
     *
     * The subject is `PhoneActivity.touchFilteredViews()`, which was `gatedActionViews()`. Only
     * the NAME changed: the list always carried the touch filter and never a gate, and after
     * B133 a name saying "gated" would claim a protection this app no longer has. The attack is
     * unchanged -- an overlay covers a control so the user's tap lands on something they cannot
     * see -- and the controls behind it are the ones that revoke a device and take control of a
     * shell. WHAT CHANGED IS THAT THERE IS NO SECOND CHECKPOINT BEHIND THEM ANY MORE. The touch
     * filter used to be the outer of two defences on those buttons; it is now the only one.
     */
    @Test
    fun every_touch_filtered_action_filters_obscured_touches() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val filtered = activity.touchFilteredViews()
                assertTrue(
                    "PB-SEC-12: the Activity exposes no touch-filtered action view, so this " +
                        "assertion has no subject. Tapjacking is the attack where an overlay " +
                        "covers a confirm button so the user's tap lands on something they " +
                        "cannot see, and the buttons at stake revoke a device and take control " +
                        "of a shell -- with nothing else behind them since ADR-007 B133",
                    filtered.isNotEmpty(),
                )
                for (view in filtered) {
                    assertTrue(
                        "PB-SEC-12: a declared action view does not filter obscured touches",
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

    /**
     * M1.5 (agents-tracker-dwwv.2.5): the tap intent [dev.swarm.phone.push.WakeNotifications]
     * builds is deliberately as inert as the hostile one above -- no extra, no data URI, one
     * component target and two task flags -- so what a tapped wake renders is exactly what a
     * plain launch renders: [dev.swarm.phone.PhoneSurface]'s own default screen, `Destination
     * .INBOX` with no drill-down, where the `needs_input` section (an approval's session's group)
     * is already first. `WakeNotificationTest` asserts the intent's shape in isolation; this is
     * the same join `a_crafted_launch_intent_selects_nothing` makes, run against the real intent
     * the notification carries rather than a hostile stand-in.
     */
    @Test
    fun the_wake_notification_tap_intent_renders_the_same_as_a_plain_launch() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val wake = dev.swarm.phone.push.WakeNotifications.build(
            context,
            text = "Swarm has an update for you.",
            contentReady = false,
        )
        val tapIntent = org.robolectric.Shadows.shadowOf(wake.contentIntent!!).savedIntent

        val plain = ActivityScenario.launch(PhoneActivity::class.java).use { it.renderedText() }
        val fromTap = ActivityScenario.launch<PhoneActivity>(tapIntent).use { it.renderedText() }

        assertEquals(
            "M1.5: the wake notification's tap intent rendered something other than a plain " +
                "launch, so tapping it does not land on the inbox's default, approvals-first view",
            plain,
            fromTap,
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
