package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.SettingsScreen
import dev.swarm.phone.ui.StreamView
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.3's CONNECTION section AS DRAWN.
 *
 * WHAT THIS ASKS THAT [SettingsPanelConnectionTest] CANNOT: whether the section is on screen, out
 * of which components, and whether the two silent states are silent in the view as well as in the
 * model. A model field that reads `""` and a view that draws an empty line for it are the same
 * screen to a reader -- a blank line of body copy under a machine row reads as a warning nobody
 * wrote -- so the absence has to be asserted where the views are.
 *
 * THE ROW IS THE KIT'S `machineRow`, WHICH IS THE COMPONENT DERIVATION ROW 11 SPECIFIES, and this
 * is the only screen left that reaches it: `machinesPanelView` was its one production call site and
 * the fold deletes that file. Composing the same drawing here by hand -- a dot, a bold name and a
 * mono id -- is the copy-paste PB-DS-6 exists to prevent, so the assertion is that the factory is
 * reached rather than that some views with the right text exist.
 */
@RunWith(RobolectricTestRunner::class)
class SettingsPanelConnectionViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** This phone's clock, fixed so an elapsed duration is arithmetic rather than a wait. */
    private val NOW = 1_754_000_000_000L

    private fun streams(vararg gapped: String): List<StreamView> =
        listOf("journal", "terminal", "reply", "grant").map { name ->
            StreamView(stream = name, stale = name in gapped, resyncPending = false)
        }

    private fun panel(
        presence: String = "online",
        silent: Boolean = false,
        streams: List<StreamView> = streams(),
        clock: ClockBanner = ClockBanner.of(""),
        killSwitchEngaged: Boolean = false,
    ): SettingsPanel = SettingsPanelScreen.of(
        SettingsScreen(alerts = true, mentions = true),
        machine = "nathans-mbp",
        connection = SettingsPanelScreen.connectionOf(
            machineId = "ep-1a2b3c4d",
            machineName = "nathans-mbp",
            presence = presence,
            freshness = MachineFreshness(silent = silent, lastHeardUnixMs = 1_000L),
            streams = streams,
            clock = clock,
            killSwitchEngaged = killSwitchEngaged,
            nowUnixMs = NOW,
        ),
    )

    private fun view(panel: SettingsPanel): View = settingsPanelView(
        context = context,
        panel = panel,
        rowFor = { View(context) },
    )

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

    // ---- the section is on screen ------------------------------------------

    @Test
    fun `the connection row is the kit's machine row, carrying the name and the endpoint id`() {
        val row = view(panel()).allTagged(SettingsTag.CONNECTION_ROW).single()

        assertEquals("nathans-mbp", textOf(row.kitRequire(KitTag.MACHINE_NAME)))
        assertEquals(
            "row 11's second cell is the endpoint id, and a row that dropped it would print one " +
                "fact where the design draws two",
            "ep-1a2b3c4d",
            textOf(row.kitRequire(KitTag.MACHINE_ENDPOINT)),
        )
    }

    @Test
    fun `the computer card leads, under its own heading, and the pairing row trails`() {
        // phone-refit-playbook W7.5: this used to pin Pairing -> Connection -> preferences. The
        // computer card (this section's row) now leads and the pairing row -- the one carrying
        // the destructive Replace control -- is the last thing on the screen.
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in SettingsTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(view(panel()))

        assertEquals(
            listOf(
                SettingsTag.NAV,
                SettingsTag.SECTION_LABEL,
                SettingsTag.CONNECTION_ROW,
                SettingsTag.SECTION_LABEL,
                SettingsTag.ROW,
                SettingsTag.ROW,
                SettingsTag.SECTION_LABEL,
                SettingsTag.MACHINE_ROW,
            ),
            order,
        )
    }

    @Test
    fun `the computer card carries exactly one status line`() {
        // W7.5's card is name, presence dot, ONE status line. A guard rather than a change: the
        // kit's machine row already draws one `MACHINE_META` line, and this pins that the fold
        // did not add a second (the old Computers row's sublabel, say) under it.
        val row = view(panel()).allTagged(SettingsTag.CONNECTION_ROW).single()

        assertEquals(1, row.allTagged(KitTag.MACHINE_META).size)
    }

    // ---- silence is the healthy state --------------------------------------

    @Test
    fun `a healthy link draws no health line and no clock line at all`() {
        val root = view(panel())

        assertNull(
            "an empty health line still occupies its line height and its gap, so a phone with " +
                "nothing wrong gets a blank strip under its machine that reads as a warning " +
                "somebody forgot to write",
            root.kitFind(SettingsTag.CONNECTION_HEALTH),
        )
        assertNull(root.kitFind(SettingsTag.CONNECTION_CLOCK))
    }

    @Test
    fun `channels with holes get ONE line naming them`() {
        val root = view(panel(streams = streams("journal", "terminal")))
        val lines = root.allTagged(SettingsTag.CONNECTION_HEALTH)

        assertEquals(
            "the fold deletes four gap cards; drawing one line per channel here would be the " +
                "same four cards with the borders taken off",
            1,
            lines.size,
        )
        assertEquals("Some updates are missing.", textOf(lines.single()))
    }

    @Test
    fun `a skewed clock is reported where the link it breaks is described`() {
        val root = view(panel(clock = ClockBanner.of("Your clock is 42s ahead.")))

        assertNotNull(
            "PB-TIME-1's verdict was drawn by `linkPanelView` and by nothing else; the fold " +
                "deletes that file, so a section that does not carry the verdict loses it",
            root.kitFind(SettingsTag.CONNECTION_CLOCK),
        )
        assertEquals(
            "Your clock is 42s ahead.",
            textOf(root.kitFind(SettingsTag.CONNECTION_CLOCK)),
        )
    }

    // ---- the kill switch, re-homed (agents-tracker-2pnu F5) -----------------

    /**
     * FAILING-FIRST for agents-tracker-zecs. `killSwitchPanel` -- derivation row 12, the one
     * component in this kit with a border and no fill -- lost its only production call site when
     * agents-tracker-nx44.3 deleted `MachinesPanelView`. So a machine whose owner has turned
     * remote control off refuses everything this phone asks and says nothing about why, on any
     * screen in the app.
     *
     * IT IS THE KIT'S FACTORY AND NOT A SENTENCE. The panel is the whole teaching -- an `--p-err`
     * title, the state, and the recovery verb in row 12's inline mono cell -- and composing that
     * by hand here is the copy-paste PB-DS-6 exists to prevent, which is why the assertion is on
     * the tags the factory puts on its own two cells.
     */
    @Test
    fun `a machine with remote access off draws row 12's panel`() {
        val root = view(panel(killSwitchEngaged = true))

        assertNotNull(
            "the app says nothing anywhere about a machine that refuses every command it sends",
            root.kitFind(SettingsTag.REMOTE_ACCESS),
        )
        assertEquals("Remote access", textOf(root.kitFind(KitTag.KILL_TITLE)))
        assertEquals(
            "the panel's body is not row 12's teaching",
            true,
            textOf(root.kitFind(KitTag.KILL_BODY)).contains("swarm remote on"),
        )
    }

    @Test
    fun `a machine with remote access on draws no panel at all`() {
        val root = view(panel(killSwitchEngaged = false))

        assertNull(
            "a working switch is reported on every visit, which is the always-on chrome the tab " +
                "fold deleted four cards of",
            root.kitFind(SettingsTag.REMOTE_ACCESS),
        )
        assertNull(root.kitFind(KitTag.KILL_TITLE))
    }

    // ---- an unpaired phone -------------------------------------------------

    @Test
    fun `no connection means no section, not an empty one`() {
        val root = view(SettingsPanelScreen.of(SettingsScreen(alerts = true, mentions = true)))

        assertNull(root.kitFind(SettingsTag.CONNECTION_ROW))
        assertNull(root.kitFind(SettingsTag.CONNECTION_HEALTH))
    }
}
