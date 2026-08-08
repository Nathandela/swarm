package dev.swarm.phone.ui.screens

import android.content.Context
import android.text.TextUtils
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
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-e6mi -- PB-APP-8 and PB-APP-11 given a place on
 * screen that a tab change cannot take away.
 *
 * WHAT WAS WRONG, structurally rather than cosmetically. The connection banner, the machine's
 * freshness verdict and the roster's stale notice were written to `PhoneSurface.status`, a child of
 * `unrecomposedControls` -- and that column is handed to `triageInboxView(below = ...)`, which is
 * to say it is hosted UNDER the four Group sections of ONE of four destinations. `hostContent`
 * detaches it on the way to Machines, Activity, Settings and into a session drill-down. So the link
 * dropping, or the machine going silent, changed nothing on screen for a user standing anywhere but
 * the bottom of the Inbox tab -- which is precisely the moment PB-APP-8 exists for.
 *
 * THE FIX IS A SLOT IN THE SCAFFOLD, ABOVE THE CONTENT AND OUTSIDE ITS SCROLL. The scaffold is what
 * survives a destination change (`PhoneScaffoldViewTest` records why it exists at all), so a banner
 * parented there is on screen on all four destinations and inside every drill-down. Two structural
 * facts below are therefore not decoration:
 *
 *  - it is NOT inside `ScaffoldTag.CONTENT`. That view is the ScrollView; a banner in it scrolls
 *    away under a long journal, which is the same disappearance in a slower form.
 *  - it is ABOVE the content. A warning under the fold is a warning nobody has read yet.
 *
 * THE SLOT TAKES A VIEW AND DOES NOT BUILD ONE, which is `content`'s own arrangement and for the
 * same reason: what it says changes on the surface's clock, and a scaffold rebuilt for a banner
 * would re-parent the destination under whoever is using it.
 */
@RunWith(RobolectricTestRunner::class)
class ScaffoldBannerTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val reconnecting = ConnectionBanner.of(ConnectionState.RECONNECTING).text

    private val silent = MachineFreshness(silent = true, lastHeardUnixMs = 0L).notice { "09:14" }!!

    private val holed = TriageInbox.from(emptyList(), journalStale = true).staleNotice

    private fun everything() = StatusBanner(
        connection = reconnecting,
        freshness = silent,
        stale = holed,
    )

    private fun scaffold(banner: View?, destination: Destination = Destination.INBOX): View =
        phoneScaffoldView(
            context = context,
            banner = banner,
            content = TextView(context).apply { text = destination.label },
            tabs = TriageInboxScreen.of(TriageInbox.from(emptyList(), journalStale = false)).tabs,
            destination = destination,
            onSelectDestination = {},
        )

    /** Every descendant carrying [tag], in depth-first (that is, on-screen) order. */
    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun View.bannerLines(): List<String> =
        allTagged(ScaffoldTag.BANNER_LINE).map { (it as TextView).text.toString() }

    /** The tags of the scaffold's own parts, in the order they are drawn. */
    private fun View.scaffoldOrder(): List<String> {
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in ScaffoldTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return order
    }

    /** Detach [view] the way the surface does before handing it to a rebuilt scaffold. */
    private fun detach(view: View) {
        (view.parent as? ViewGroup)?.removeView(view)
    }

    // ---- the lines ---------------------------------------------------------

    @Test
    fun `each fact is its own line and they are drawn in the model's order`() {
        val view = statusBannerView(context, everything())

        assertEquals(
            "the banner does not render one line per fact, so the three sentences are still one " +
                "run-on paragraph",
            listOf(reconnecting, silent, holed),
            view.bannerLines(),
        )
    }

    /**
     * agents-tracker-ksvb.3: the one `readOnlyNote` in the app that is drawn OUTSIDE a scroll.
     *
     * A LINE THAT WRAPS HERE DOES NOT COST ITS OWN HEIGHT. It pushes the destination down, on all
     * four destinations, under a user who is reading something else -- and the banner carries up
     * to three facts, so three wrapped sentences is a third of a handset. The cap is the slot's
     * decision and not the component's: the same note under a terminal well still wraps, because
     * there it is prose inside a column that scrolls.
     */
    @Test
    fun `each line is capped so a long fact cannot push the app down`() {
        val lines = statusBannerView(context, everything()).allTagged(ScaffoldTag.BANNER_LINE)

        assertEquals("the banner drew no lines to assert over", 3, lines.size)
        for (line in lines) {
            assertEquals(
                "a banner line wraps without bound, so a long sentence above the scroll moves " +
                    "the whole destination down",
                2,
                (line as TextView).maxLines,
            )
            assertEquals(
                "a banner line is clipped rather than ellipsised, so a truncated warning reads " +
                    "as a complete one",
                TextUtils.TruncateAt.END,
                line.ellipsize,
            )
        }
    }

    @Test
    fun `a fact with nothing to say draws no line`() {
        val view = statusBannerView(
            context,
            StatusBanner(connection = "", freshness = "", stale = holed),
        )

        assertEquals(
            "an empty fact was drawn as an empty line, so the banner carries blank rows of air " +
                "wherever the phone happens to have nothing to report",
            listOf(holed),
            view.bannerLines(),
        )
    }

    @Test
    fun `a silent banner draws nothing at all`() {
        val view = statusBannerView(context, StatusBanner.NONE)

        assertEquals(emptyList<String>(), view.bannerLines())
    }

    // ---- the slot ----------------------------------------------------------

    @Test
    fun `the banner is above the content and outside its scroll`() {
        val banner = statusBannerView(context, everything())
        val root = scaffold(banner)

        assertEquals(
            "the scaffold does not draw the banner first, so a warning about the link sits under " +
                "the destination -- and under the fold on any screen worth scrolling",
            listOf(ScaffoldTag.BANNER, ScaffoldTag.CONTENT, ScaffoldTag.TABS),
            root.scaffoldOrder(),
        )
        assertNull(
            "the banner is INSIDE the scaffold's scroll, so it scrolls away under a long journal " +
                "-- the same disappearance the slot exists to end, in a slower form",
            root.kitRequire(ScaffoldTag.CONTENT).kitFind(ScaffoldTag.BANNER),
        )
    }

    @Test
    fun `the same banner is on screen on every destination`() {
        val banner = statusBannerView(context, everything())

        for (destination in Destination.entries) {
            // What `PhoneSurface.drawScaffold` does with the content host: the surface owns the
            // view, the scaffold is rebuilt around it.
            detach(banner)
            val root = scaffold(banner, destination)

            assertSame(
                "the ${destination.label} destination rebuilt the banner instead of hosting the " +
                    "one the surface owns",
                banner,
                root.kitRequire(ScaffoldTag.BANNER),
            )
            assertEquals(
                "the banner is gone on the ${destination.label} destination, so PB-APP-8's " +
                    "offline/reconnecting/stale states are visible on one screen out of four -- " +
                    "which is the defect, moved rather than fixed",
                listOf(reconnecting, silent, holed),
                root.bannerLines(),
            )
        }
    }

    @Test
    fun `a scaffold handed no banner still draws its destination and its bar`() {
        // The slot is optional at the composition level: the surface owns one host and hands it
        // over always, but a caller with nothing to say must not lose the app.
        val root = scaffold(banner = null)

        assertNull(root.kitFind(ScaffoldTag.BANNER))
        assertNotNull(root.kitFind(ScaffoldTag.CONTENT))
        assertNotNull(root.kitFind(ScaffoldTag.TABS))
        assertTrue(root.bannerLines().isEmpty())
    }
}
