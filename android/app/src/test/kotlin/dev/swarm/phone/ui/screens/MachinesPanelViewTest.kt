package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.CompoundButton
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.MachinePane
import dev.swarm.phone.ui.kit.CtaSurface
import dev.swarm.phone.ui.kit.Kit
import dev.swarm.phone.ui.kit.KitTag
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the machines screen AS DRAWN --
 * derivation rows 11, 12 and 13.
 *
 * WHAT THIS ASKS THAT `MachinesPanelTest` CANNOT. That suite asks what the screen SAYS; this asks
 * whether it is on screen -- which component renders it, in what order, and which of the two 7 dp
 * marks the machine row got. "The model is beautiful and nothing renders it" is the defect PB-DS-6
 * was recorded NOT MET over.
 *
 * **THE ASSERTION THIS SUITE EXISTS FOR IS AN ABSENCE: `KitTag.DOT` must appear nowhere.** The
 * status dot and the presence dot are the same drawable at the same 7 dp, and `--p-ok` / `--p-ink3`
 * are the two tokens `ready_for_review` and `completed` already carry -- so
 * `statusDot(context, if (online) "ready_for_review" else "completed")` renders this screen
 * pixel-for-pixel and is the phone inventing a `status.Group` for a machine. Nothing about the
 * rendered result distinguishes the two implementations, which is exactly why the tag is what gets
 * asserted.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED: appearance. The settings row's padding, the nav header's
 * metrics and the presence dot's own colours are PB-DS-10's and are asserted in `ui/kit`;
 * repeating them here would be a second opinion that can disagree with the first. The one colour
 * this file names is the deny fill, and it is asked as a COMPOSITION question -- which variant of
 * `ctaButton` the revoke control is -- rather than as an appearance one.
 */
@RunWith(RobolectricTestRunner::class)
class MachinesPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val formatTime: (Long) -> String = { millis -> "at $millis" }

    private fun panel(
        presence: String = "online",
        killSwitchEngaged: Boolean = false,
        silent: Boolean = false,
        activity: List<JournalRow> = emptyList(),
    ): MachinesPanel = MachinesPanelScreen.of(
        MachinePane(
            machineId = "machine-endpoint-0001",
            presence = presence,
            freshness = MachineFreshness(silent = silent, lastHeardUnixMs = 1_753_900_000_000),
            pairedDeviceName = "swarm phone",
            killSwitchEngaged = killSwitchEngaged,
            activity = activity,
        ),
        formatTime,
    )

    /**
     * NO DEFAULT FOR [panel], deliberately: `panel: MachinesPanel = panel()` would put a parameter
     * and a function of the same name in one scope, which is a resolution question a test should
     * not be asking.
     */
    private fun view(panel: MachinesPanel, below: View? = null): View =
        machinesPanelView(context = context, panel = panel, below = below)

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun View.descendants(): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the composition ----------------------------------------------------

    @Test
    fun `the machines screen is composed of the kit components rows 11 to 13 name`() {
        val root = view(panel())

        assertNotNull("the screen has no title", root.kitFind(MachinesTag.NAV))
        assertNotNull(
            "the machine is not the kit's `settingsRow`, so the screen hand-built it",
            root.kitFind(MachinesTag.MACHINE)?.kitFind(KitTag.SETTINGS_LABEL),
        )
        assertNotNull("the machine carries no presence mark", root.kitFind(KitTag.PRESENCE_DOT))
        assertNotNull("the remote-access state is missing", root.kitFind(MachinesTag.REMOTE_ACCESS))
        assertNotNull("the device section has no label", root.kitFind(MachinesTag.SECTION_LABEL))
        assertNotNull("the paired device is missing", root.kitFind(MachinesTag.DEVICE))
        assertNotNull("there is no way to revoke this device", root.kitFind(MachinesTag.REVOKE))
    }

    @Test
    fun `the title is drawn by the root nav header and carries no live counter`() {
        val nav = view(panel()).kitRequire(MachinesTag.NAV)

        assertEquals("Machines", textOf(nav.kitRequire(KitTag.TITLE)))
        assertNull(
            "a live counter was drawn on the machines screen. It is the inbox's in-context " +
                "liveness readout (derivation §1.4), counting sessions in flight, and this screen " +
                "has no session list for it to be about",
            nav.kitFind(KitTag.LIVE),
        )
    }

    @Test
    fun `the screen reads in the order the mock's own machines screen sets out`() {
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in MachinesTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(view(panel()))

        assertEquals(
            listOf(
                MachinesTag.NAV,
                MachinesTag.MACHINE,
                MachinesTag.REMOTE_ACCESS,
                MachinesTag.SECTION_LABEL,
                MachinesTag.DEVICE,
            ),
            order,
        )
    }

    @Test
    fun `every row carries the model's own copy`() {
        val page = panel()
        val root = view(page)

        val machine = root.kitRequire(MachinesTag.MACHINE)
        assertEquals(page.machine.name, textOf(machine.kitRequire(KitTag.SETTINGS_LABEL)))
        assertEquals(page.machine.presenceLine, textOf(machine.kitRequire(KitTag.SETTINGS_SUBLABEL)))

        val remote = root.kitRequire(MachinesTag.REMOTE_ACCESS)
        assertEquals(page.remoteAccess.label, textOf(remote.kitRequire(KitTag.SETTINGS_LABEL)))
        assertEquals(page.remoteAccess.sublabel, textOf(remote.kitRequire(KitTag.SETTINGS_SUBLABEL)))

        // The uppercase is `sectionLabel`'s `isAllCaps`, which leaves the text it was given
        // alone -- so a screen that shouted its own copy would fail here, and a screen reader
        // still hears a phrase rather than thirteen letters.
        assertEquals(
            page.pairedDevicesHeading,
            textOf(root.kitRequire(MachinesTag.SECTION_LABEL)),
        )

        val device = root.kitRequire(MachinesTag.DEVICE)
        assertEquals(page.pairedDevice.name, textOf(device.kitRequire(KitTag.SETTINGS_LABEL)))
        assertEquals(page.pairedDevice.sublabel, textOf(device.kitRequire(KitTag.SETTINGS_SUBLABEL)))
        assertEquals(page.pairedDevice.revokeLabel, textOf(root.kitRequire(MachinesTag.REVOKE)))
    }

    // ---- the mark is a presence mark and not a Group -------------------------

    /**
     * See the class KDoc: the two dots are indistinguishable once drawn, so what is asserted is
     * which factory drew them.
     */
    @Test
    fun `the machine's mark is the presence dot and no status dot is on this screen`() {
        listOf(true, false).forEach { online ->
            val root = view(panel(presence = if (online) "online" else "offline"))
            assertEquals(1, root.allTagged(KitTag.PRESENCE_DOT).size)
            assertNull(
                "a `.pdot` is on the machines screen. The status dot takes a `status.Group`, a " +
                    "machine's reachability is not one, and the Group whose colour happens to " +
                    "match renders this correctly while inventing a state the server never sent",
                root.kitFind(KitTag.DOT),
            )
        }
    }

    /**
     * The dot says nothing a screen reader needs, because the line under it says it in words.
     *
     * A described dot here would have the row read its presence twice, and `contentDescription = ""`
     * -- the shape a caller writing `description ?: ""` ships -- would ask the reader to skip a
     * view that has something to say.
     */
    @Test
    fun `the presence mark is decorative, because the row states presence in words`() {
        val root = view(panel())
        val dot = root.kitRequire(KitTag.PRESENCE_DOT)

        assertEquals(
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            dot.importantForAccessibility,
        )
        assertNull(dot.contentDescription)
        assertTrue(
            "the row does not say presence in words either, so the state is carried by a colour " +
                "alone -- which for a screen reader user is not carried at all",
            textOf(root.kitRequire(MachinesTag.MACHINE).kitRequire(KitTag.SETTINGS_SUBLABEL))
                .contains("online"),
        )
    }

    // ---- remote access ships as a statement, not a control -------------------

    /**
     * Row 12 as amended: no trailing control, and the mock's toggle does not ship.
     *
     * THE ROW IS ASSERTED TO HAVE NOTHING BESIDE ITS TEXT rather than merely to have no `Switch`,
     * because the kit's own toggle is a `View` with a drawable and not a `CompoundButton` -- a
     * check for the platform type would pass over the very control this row must not have. Both
     * are asked: the structural one for the kit's toggle, the type one for a platform switch
     * somebody reaches for instead.
     */
    @Test
    fun `remote access carries no control at all`() {
        val root = view(panel(killSwitchEngaged = true))
        val row = root.kitRequire(MachinesTag.REMOTE_ACCESS) as ViewGroup

        assertEquals(
            "the remote-access row has ${row.childCount} children: its text column, and " +
                "something beside it. `App.KillSwitchEngaged` is read-only by design -- " +
                "handleRemoteSetControl refuses the remote tier before consulting its backend -- " +
                "so a control here is one that cannot act",
            1,
            row.childCount,
        )
        assertTrue(
            "a CompoundButton is on the machines screen",
            root.descendants().none { it is CompoundButton },
        )
    }

    // ---- revoke -------------------------------------------------------------

    /**
     * WHICH VARIANT, ASKED AS A COMPOSITION QUESTION. `ctaButton` has three, they differ by fill
     * and ink, and row 13 says this one is `.a2-no` -- the same tinted-fill denial control the
     * approval sheet uses. `MORE` would render a neutral bordered button that reads as an ordinary
     * action, which for the one control on this screen that destroys something is the wrong
     * sentence. What `.a2-no` IS remains `CtaButtonTest`'s to assert against the design source.
     */
    @Test
    fun `revoke is the kit's denial control and the surface can reach it`() {
        val root = view(panel())
        val revoke = root.kitRequire(MachinesTag.REVOKE)
        val surface = revoke.background

        assertTrue(
            "revoke's background is $surface, so it is not a `ctaButton` at all",
            surface is CtaSurface,
        )
        assertEquals(
            "revoke is not the DENY variant. Row 13: the mock's bespoke outline button is " +
                "discarded for the `.a2-no` treatment, which is the one destructive idiom this " +
                "skin has -- an outline control beside a tinted one is two idioms for one meaning",
            Kit.denyFill(context),
            (surface as CtaSurface).spec.fill,
        )
        assertTrue(
            "revoke is inside the paired-device row it acts on",
            root.kitRequire(MachinesTag.DEVICE).descendants().any { it === revoke },
        )
    }

    /**
     * The button must not starve the row it sits in.
     *
     * `ctaButton` lays itself out MATCH_PARENT wide, which is right for the full-width sheet CTA it
     * was written for and wrong inside a horizontal row: LinearLayout measures the unweighted
     * MATCH_PARENT child against the whole row and leaves the weighted text column zero. The
     * machine's name and the device's name would both vanish, and the screen would look like two
     * buttons.
     */
    @Test
    fun `revoke hugs its own width so the row's text keeps its space`() {
        val revoke = view(panel()).kitRequire(MachinesTag.REVOKE)
        assertEquals(
            "revoke is laid out MATCH_PARENT wide inside a row whose text column is weighted, so " +
                "the text is measured at zero",
            ViewGroup.LayoutParams.WRAP_CONTENT,
            revoke.layoutParams.width,
        )
    }

    // ---- what this slice has not built ---------------------------------------

    /**
     * The audit log. The mock draws one on this screen; its component is derivation row 14 and it
     * is being built beside this slice. Until it lands the section is absent rather than
     * approximated, and `below` is where it will be hosted.
     */
    @Test
    fun `what this slice has not recomposed is hosted under the panel, not instead of it`() {
        val trailing = View(context)
        val root = view(panel(), below = trailing) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
        assertNotNull("hosting the remainder dropped the screen", root.kitFind(MachinesTag.NAV))
    }

    @Test
    fun `a machine full of journal activity draws the same screen`() {
        val busy = panel(
            activity = listOf(JournalRow(cursor = 9, type = "launch", group = "working")),
        )
        assertEquals(
            "the screen grew when the journal did, so an audit log is being raised here as well " +
                "as by the activity screen -- two components rendering one section",
            view(panel()).descendants().size,
            view(busy).descendants().size,
        )
    }
}
