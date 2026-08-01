package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the SCAFFOLD -- the composition that
 * makes inventory C1.4 a navigation control rather than a picture of one.
 *
 * WHY THERE IS A SCAFFOLD AT ALL, which is the one thing to read before extending this file. The
 * tab bar was composed INSIDE `triageInboxView`, and `machinesPanelView` and `activityPanelView`
 * compose none -- so a tab that merely swapped the screen would land the user on Machines with no
 * bar to come back with. The bar is therefore the WINDOW'S and not the inbox's: one bar, under
 * whichever of the four destinations is on screen. Derivation row 20 is the same shape read from
 * the design -- the screen scaffold's padding is "bottom `screen_bottom` (or inset +
 * `tabbar_height`)", which puts the platform inset UNDER a bar that is `tabbar_height` tall -- and
 * row 19 is why the inset is the platform's rather than the mock's 76.
 *
 * THREE ASSERTIONS BELOW ARE `TriageInboxViewTest`'S, MOVED RATHER THAN REWRITTEN. That suite
 * asserted the tab bar was on screen, that it was the last thing on it, and that the badge appears
 * only when a session needs the user. All three are facts about the bar, and the bar is this
 * composition's now; the checks and their wording travel with it. Nothing was weakened on the way:
 * the order assertion still walks the INBOX's own composition tags alongside the scaffold's, so
 * "the nav header first, the tab bar last" is still one statement about one screen.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED HERE: the bar's own metrics -- its height, its hairline, its
 * fill, the badge's geometry and the glyph paths. Those are PB-DS-10's and are asserted in
 * `ui/kit/` and in android/gate/tabbar_test.go against the artifact. Repeating them here would be a
 * second opinion that can disagree with the first. What is asserted is what only the SCAFFOLD can
 * get wrong: which tabs exist, which one reads as selected, where the bar sits, and whether
 * pressing one goes anywhere.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneScaffoldViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun row(id: String, group: String) =
        SessionRow(id = id, title = id.substringAfter('/'), group = group, need = "doing something", present = true, agent = "claude")

    private fun inbox(rows: List<SessionRow>): InboxScreen =
        TriageInboxScreen.of(inbox = TriageInbox.from(rows, journalStale = false))

    /** The scaffold over the inbox, which is the composition the app opens on. */
    private fun scaffold(
        rows: List<SessionRow> = listOf(row("mbp/one", "working")),
        destination: Destination = Destination.INBOX,
        content: View? = null,
        onSelectDestination: (Destination) -> Unit = {},
    ): View {
        val screen = inbox(rows)
        return phoneScaffoldView(
            context = context,
            content = content ?: triageInboxView(
                context = context,
                screen = screen,
                onSelectSession = {},
                onSelectScope = {},
            ),
            tabs = screen.tabs,
            destination = destination,
            onSelectDestination = onSelectDestination,
        )
    }

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

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    /** The four tabs, as the views a finger lands on. */
    private fun View.tabs(): List<View> = allTagged(KitTag.TAB_LABEL).map { it.parent as View }

    // ---- the composition --------------------------------------------------

    @Test
    fun `the scaffold hosts the destination above the bar`() {
        val root = scaffold()

        // MOVED FROM TriageInboxViewTest.`the inbox is composed of the components its recorded
        // composition names`, where it read InboxTag.TABS. The subject noun is the only word that
        // changed: the sentence names who failed to render the bar, and that is no longer the
        // inbox.
        assertNotNull(
            "the scaffold renders nothing for C1.4 `.ptabs` -- the tab bar",
            root.kitFind(ScaffoldTag.TABS),
        )
        assertNotNull(
            "the scaffold hosts no content, so the destination has nowhere to be drawn",
            root.kitFind(ScaffoldTag.CONTENT),
        )
    }

    @Test
    fun `the composition is in the recorded order and the tab bar is last`() {
        val root = scaffold()
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let {
                if (it in InboxTag.COMPOSITION || it in ScaffoldTag.COMPOSITION) order += it
            }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        // MOVED VERBATIM FROM TriageInboxViewTest, which asserted the same order over the inbox's
        // own root while the bar was still the inbox's.
        assertEquals(
            "the tab bar is not the last thing on screen, so it is scrolling with the content " +
                "rather than being the fixed bar the design draws",
            ScaffoldTag.TABS,
            order.last(),
        )
        assertEquals(
            "the inbox's first two elements are not the nav header and the scope bar",
            listOf(InboxTag.NAV, InboxTag.SCOPES),
            order.filter { it in InboxTag.COMPOSITION }.take(2),
        )
    }

    @Test
    fun `the tab badge appears only when a session needs the user`() {
        // MOVED VERBATIM FROM TriageInboxViewTest, where both roots were inbox views. The badge is
        // the inbox's COUNT and the bar's PLACE: the count still comes from InboxScreen.tabs, and
        // the only thing that changed is which composition draws it.
        val quiet = scaffold(listOf(row("mbp/one", "working")))
        val blocked = scaffold(listOf(row("mbp/one", "needs_input"), row("mbp/two", "needs_input")))

        assertNull(
            "a badge was drawn over an inbox where nothing needs anybody",
            quiet.kitRequire(ScaffoldTag.TABS).kitFind(KitTag.BADGE),
        )
        val badge = blocked.kitRequire(ScaffoldTag.TABS).kitRequire(KitTag.BADGE)
        assertEquals("2", textOf(badge))
        assertEquals("2 sessions need you", badge.contentDescription)
    }

    @Test
    fun `the bar draws every destination, in the order the inventory names them`() {
        val root = scaffold()

        assertEquals(
            "the bar does not draw inventory C1.4's four tabs, so a destination has no way in",
            Destination.entries.map { it.label },
            root.allTagged(KitTag.TAB_LABEL).map { textOf(it) },
        )
    }

    // ---- the bar is not a picture -----------------------------------------

    @Test
    fun `tapping a tab reports that destination and not the one beside it`() {
        var chosen: Destination? = null
        val root = scaffold(onSelectDestination = { chosen = it })

        root.tabs()[1].performClick()

        assertEquals(
            "the tab's handler was built from a captured variable rather than from its own tab, " +
                "so every tab navigates to the same destination",
            Destination.MACHINES,
            chosen,
        )
    }

    @Test
    fun `every tab is pressable, so no destination is drawn without a way in`() {
        val root = scaffold()

        for ((index, tab) in root.tabs().withIndex()) {
            assertTrue(
                "the ${Destination.entries[index].label} tab has no click listener: `TabItem` " +
                    "carries no tap handler, so the bar is four labels with nothing behind them",
                tab.hasOnClickListeners(),
            )
        }
    }

    @Test
    fun `the destination on screen is the one tab that reads as selected`() {
        // A bar whose selection came from the inbox model rather than from navigation renders the
        // Inbox tab selected on all four screens -- which tells a user standing on Machines that
        // they are somewhere else. The colours are the kit's (`--p-hero` against `--p-ink3`) and
        // are asserted there; what this reads is the PARTITION, so it needs no constant of its own.
        for (destination in Destination.entries) {
            val inks = scaffold(destination = destination)
                .allTagged(KitTag.TAB_LABEL)
                .map { (it as TextView).currentTextColor }
            val selected = inks[destination.ordinal]
            val rest = inks.filterIndexed { index, _ -> index != destination.ordinal }

            assertTrue(
                "on the ${destination.label} destination the selected tab's ink is the same as " +
                    "the unselected ones, so nothing on the bar says where the user is",
                rest.none { it == selected },
            )
            assertEquals(
                "more than one tab reads as selected on the ${destination.label} destination",
                1,
                rest.distinct().size,
            )
        }
    }

    @Test
    fun `the bar is the same four tabs on every destination`() {
        // The structural fact the scaffold exists for: `machinesPanelView` and `activityPanelView`
        // compose no bar of their own, so a bar that stayed inside the inbox would leave three of
        // the four destinations with no way back.
        for (destination in Destination.entries) {
            val root = scaffold(destination = destination, content = TextView(context))

            assertEquals(
                "the ${destination.label} destination has no tab bar, so a user who navigates " +
                    "to it cannot navigate anywhere else",
                Destination.entries.map { it.label },
                root.allTagged(KitTag.TAB_LABEL).map { textOf(it) },
            )
        }
    }
}
