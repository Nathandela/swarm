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
import dev.swarm.phone.ui.StreamView
import dev.swarm.phone.ui.SyncStatus
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.2's composition half.
 *
 * `SyncStatusTest` asserts what the MODEL decides. This asserts where it lands: a pill in the nav
 * row beside the title, an opaque strip above the nav when the link is broken, and the detail
 * behind a tap.
 *
 * ## Why the strip is IN LAYOUT and not an overlay
 *
 * Field test 3's screenshots show the banner stack overlapping the nav title. An overlay cannot be
 * made not to overlap -- it can only be positioned so that it usually does not -- whereas a strip
 * that is a sibling ABOVE the nav in the same column cannot overlap anything by construction. That
 * is what it costs: while it is up, the destination moves down by exactly its height. It is drawn
 * for one state, which is the state where the user has to act.
 *
 * ## Why the pill is the nav row's and not the scaffold's
 *
 * The nav row is where a person's eye already is: it carries the screen's own title, and the sync
 * status is a property of what that title names. A pill in a band of its own is a fifth bar on a
 * screen that already has a status bar, a nav row, a scroll and a tab bar.
 */
@RunWith(RobolectricTestRunner::class)
class SyncStatusViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val nowMs = 1_754_000_000_000L

    private val channelNames = listOf("journal", "terminal", "reply", "grant")

    private fun streams(holed: String? = null) = channelNames.map {
        StreamView(stream = it, stale = it == holed, resyncPending = false)
    }

    private fun status(
        state: ConnectionState = ConnectionState.ONLINE,
        freshness: MachineFreshness = MachineFreshness(silent = false, lastHeardUnixMs = nowMs),
        holed: String? = null,
        reconciled: Boolean = true,
    ) = SyncStatus.of(
        connection = ConnectionBanner.of(state),
        freshness = freshness,
        nowUnixMs = nowMs,
        streams = streams(holed),
        reconciled = reconciled,
    )

    private val quiet = status(
        freshness = MachineFreshness(silent = true, lastHeardUnixMs = nowMs - 18 * 3_600_000L),
    )

    private val broken = status(state = ConnectionState.RELAY_UNTRUSTED)

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun View.textOf(tag: String): String = (kitRequire(tag) as TextView).text.toString()

    private fun scaffold(status: View?, destination: Destination = Destination.INBOX): View =
        phoneScaffoldView(
            context = context,
            status = status,
            content = TextView(context).apply { text = destination.label },
            tabs = TriageInboxScreen.of(TriageInbox.from(emptyList(), journalStale = false)).tabs,
            destination = destination,
            onSelectDestination = {},
        )

    // ---- the pill ----------------------------------------------------------

    @Test
    fun `a live phone draws no pill at all`() {
        assertNull(
            "a phone with nothing to report draws a mark in its nav row. A mark that is always " +
                "up is a mark nobody reads, which is the whole defect the banner stack shipped",
            syncPillView(context, status()) {},
        )
    }

    @Test
    fun `the pill carries the model's words and its announcement`() {
        val pill = syncPillView(context, quiet) {}!!

        assertEquals("QUIET 18h", (pill as TextView).text.toString())
        assertEquals(
            "the pill announces its own three upper-case characters, which read aloud are not a " +
                "sentence and do not say that pressing opens anything",
            quiet.description,
            pill.contentDescription,
        )
    }

    @Test
    fun `the pill opens the detail`() {
        var opened = 0
        val pill = syncPillView(context, quiet) { opened++ }!!

        pill.performClick()

        assertEquals("the pill is drawn with no listener behind it", 1, opened)
    }

    @Test
    fun `the pill sits in the nav row beside the title`() {
        val pill = syncPillView(context, quiet) {}!!
        val screen = triageInboxView(
            context = context,
            screen = TriageInboxScreen.of(TriageInbox.from(emptyList(), journalStale = false)),
            onSelectSession = {},
            onSelectScope = {},
            status = pill,
        )

        val nav = screen.kitRequire(InboxTag.NAV)
        assertSame(
            "the nav row does not host the status pill, so the sync state is drawn in a band of " +
                "its own -- a fifth bar on a screen that already has four",
            pill,
            nav.kitFind(SyncTag.PILL),
        )
        assertNotNull("the pill displaced the screen's own title", nav.kitFind(KitTag.TITLE))
    }

    // ---- the strip ---------------------------------------------------------

    @Test
    fun `only a broken link draws the strip, and it carries the transport's sentence`() {
        assertEquals(
            ConnectionBanner.of(ConnectionState.RELAY_UNTRUSTED).text,
            syncStatusView(context, broken).textOf(SyncTag.STRIP),
        )
        assertNull("a quiet machine escalated to the strip", syncStatusView(context, quiet).kitFind(SyncTag.STRIP))
        assertNull(
            "a phone with a journal gap escalated to the strip",
            syncStatusView(context, status(holed = "journal")).kitFind(SyncTag.STRIP),
        )
        assertNull(syncStatusView(context, status()).kitFind(SyncTag.STRIP))
    }

    @Test
    fun `the strip is one line, so a long refusal cannot push the app down without bound`() {
        val strip = syncStatusView(context, broken).kitRequire(SyncTag.STRIP) as TextView

        assertEquals(
            "the strip wraps, so the destination on all four tabs moves down by however long the " +
                "transport's sentence happens to be",
            1,
            strip.maxLines,
        )
    }

    @Test
    fun `the strip is above the content and outside its scroll`() {
        val root = scaffold(syncStatusView(context, broken))

        assertEquals(
            "the scaffold does not draw the status chrome first, so a strip about a dead link " +
                "sits under the destination and under the fold",
            listOf(ScaffoldTag.STATUS, ScaffoldTag.CONTENT, ScaffoldTag.TABS),
            root.scaffoldOrder(),
        )
        assertNull(
            "the strip is INSIDE the scaffold's scroll, so it leaves the screen as soon as the " +
                "user reads past it",
            root.kitRequire(ScaffoldTag.CONTENT).kitFind(ScaffoldTag.STATUS),
        )
    }

    @Test
    fun `the strip opens the detail too`() {
        var opened = 0
        val view = syncStatusView(context, broken, onOpen = { opened++ })

        view.kitRequire(SyncTag.STRIP).performClick()

        assertEquals(1, opened)
    }

    @Test
    fun `a live phone draws no status chrome at all`() {
        val view = syncStatusView(context, status())

        assertEquals(
            "a healthy phone still costs a row of chrome above every destination",
            0,
            (view as ViewGroup).childCount,
        )
    }

    // ---- the detail --------------------------------------------------------

    @Test
    fun `the detail is closed until it is opened`() {
        assertNull(syncStatusView(context, quiet).kitFind(SyncTag.SHEET))
        assertNotNull(syncStatusView(context, quiet, open = true).kitFind(SyncTag.SHEET))
    }

    @Test
    fun `the detail labels the three facts the banner used to stack`() {
        val sheet = syncStatusView(context, quiet, open = true).kitRequire(SyncTag.SHEET)

        assertEquals(
            listOf(SyncStatus.HEARD, SyncStatus.READING, SyncStatus.VIEWS),
            sheet.allTagged(SyncTag.ROW).map { (it.kitRequire(KitTag.SETTINGS_LABEL) as TextView).text.toString() },
        )
        assertEquals(
            quiet.detail.rows.map { it.value },
            sheet.allTagged(SyncTag.ROW).map { (it.kitRequire(KitTag.SETTINGS_SUBLABEL) as TextView).text.toString() },
        )
    }

    @Test
    fun `the detail names every gap and a whole roster names none`() {
        val holed = syncStatusView(context, status(holed = "journal"), open = true)
        val whole = syncStatusView(context, quiet, open = true)

        assertEquals(
            status(holed = "journal").detail.gaps,
            holed.allTagged(SyncTag.GAP).map { (it as TextView).text.toString() },
        )
        assertTrue(whole.allTagged(SyncTag.GAP).isEmpty())
    }

    @Test
    fun `the repair is a control and it acts`() {
        var repaired = 0
        val view = syncStatusView(
            context,
            status(holed = "journal"),
            open = true,
            onRepair = { repaired++ },
        )

        val repair = view.kitRequire(SyncTag.REPAIR) as TextView
        assertEquals(SyncStatus.REPAIR, repair.text.toString())
        repair.performClick()

        assertEquals("the repair is drawn with no listener behind it", 1, repaired)
    }

    @Test
    fun `a phone with nothing to repair draws no control`() {
        assertNull(
            "a quiet machine offers a repair. Nothing this phone can press makes a silent " +
                "machine speak",
            syncStatusView(context, quiet, open = true).kitFind(SyncTag.REPAIR),
        )
    }

    @Test
    fun `a broken link draws the strip and the detail together`() {
        val view = syncStatusView(context, broken, open = true)

        assertNotNull(view.kitFind(SyncTag.STRIP))
        assertNotNull(view.kitFind(SyncTag.SHEET))
        assertEquals(SyncStatus.PAIR_AGAIN, (view.kitRequire(SyncTag.REPAIR) as TextView).text.toString())
    }

    @Test
    fun `the same status chrome is on screen on every destination`() {
        val chrome = syncStatusView(context, broken)

        for (destination in Destination.entries) {
            (chrome.parent as? ViewGroup)?.removeView(chrome)
            val root = scaffold(chrome, destination)

            assertSame(
                "the ${destination.label} destination rebuilt the status chrome instead of " +
                    "hosting the one the surface owns",
                chrome,
                root.kitRequire(ScaffoldTag.STATUS),
            )
            assertFalse(root.allTagged(SyncTag.STRIP).isEmpty())
        }
    }

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
}
