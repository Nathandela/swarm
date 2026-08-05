package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ConnectionBanner
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.StatusBanner
import dev.swarm.phone.ui.TriageInbox
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-agre's composition half.
 *
 * `RemedyControlsTest` asserts that the MODEL decides a control is owed. This asserts that the
 * banner draws one -- and that it is a control rather than a fourth sentence, which is the whole
 * distinction the issue is about. A remedy rendered as another line of body copy is the defect with
 * a different tag on it.
 *
 * WHY THE PRESS GOES TO THE SETTINGS DESTINATION AND NOT TO `BeginPairing`. This banner is drawn
 * over a phone that is STILL PAIRED -- `RELAY_UNTRUSTED` and `RELAY_INSECURE` do not end a pairing
 * (`PairOnlyReason` records why) -- and `swarm remote pair` is refused while a device is registered
 * (PB-STATE-10, single-device v1). A button that opened the pairing flow from here would walk into
 * that refusal, which is the same failure loop one step further along. The Settings tab is where
 * this product's pairing section lives (`SettingsPanel.machineSection`, heading "Pairing") and it
 * carries the one control that clears the registration first, so the banner leads there. Navigating
 * is a mechanism this app already has; a re-pair from inside the app is not, and this issue does not
 * invent one.
 */
@RunWith(RobolectricTestRunner::class)
class ScaffoldPairAgainTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun bannerFor(state: ConnectionState) = StatusBanner.of(
        connection = ConnectionBanner.of(state),
        freshness = MachineFreshness(silent = true, lastHeardUnixMs = 0L).notice { "09:14" },
        staleNotice = TriageInbox.from(emptyList(), journalStale = true).staleNotice,
    )

    private fun View.firstTagged(tag: String): View? {
        if (this.tag == tag) return this
        if (this is ViewGroup) {
            for (i in 0 until childCount) getChildAt(i).firstTagged(tag)?.let { return it }
        }
        return null
    }

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    @Test
    fun `a banner whose remedy is pairing draws a control with the model's words`() {
        val banner = bannerFor(ConnectionState.RELAY_UNTRUSTED)
        val view = statusBannerView(context, banner) {}

        val control = view.firstTagged(ScaffoldTag.BANNER_ACTION)
        assertNotNull(
            "the banner tells the user to pair this phone again and draws nothing to press. " +
                "PB-APP-10's stronger form asks for an explicit prompt, which means a control",
            control,
        )
        assertEquals(banner.pairAgain, (control as TextView).text.toString())
    }

    @Test
    fun `the control acts`() {
        var pressed = 0
        val view = statusBannerView(context, bannerFor(ConnectionState.RELAY_INSECURE)) {
            pressed++
        }

        view.firstTagged(ScaffoldTag.BANNER_ACTION)!!.performClick()

        assertEquals(
            "the control is drawn with no listener behind it, which is a button that looks like " +
                "a button and does not act -- `PeekPanelScreen` records why that is worse than a " +
                "gap",
            1,
            pressed,
        )
    }

    @Test
    fun `a banner with no pairing remedy draws no control`() {
        val view = statusBannerView(context, bannerFor(ConnectionState.RECONNECTING)) {}

        assertNull(
            "a dropped link draws a pair-again control. The remedy is to wait; the link comes " +
                "back on its own",
            view.firstTagged(ScaffoldTag.BANNER_ACTION),
        )
    }

    @Test
    fun `a silent banner draws no control`() {
        val view = statusBannerView(context, StatusBanner.NONE) {}

        assertNull(view.firstTagged(ScaffoldTag.BANNER_ACTION))
        assertEquals(0, (view as ViewGroup).childCount)
    }

    @Test
    fun `the control is not drawn as one of the banner's lines`() {
        val banner = bannerFor(ConnectionState.RELAY_UNTRUSTED)
        val view = statusBannerView(context, banner) {}

        val lines = view.allTagged(ScaffoldTag.BANNER_LINE).map { (it as TextView).text.toString() }
        assertEquals(banner.lines, lines)
        assertFalse(
            "the control's words are drawn as a banner line, so the remedy is prose again",
            lines.contains(banner.pairAgain),
        )
        assertTrue(
            "the control is not a BANNER_LINE and it is also not anywhere else on the banner",
            view.firstTagged(ScaffoldTag.BANNER_ACTION) != null,
        )
    }
}
