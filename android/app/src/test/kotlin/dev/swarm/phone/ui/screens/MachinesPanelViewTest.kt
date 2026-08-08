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
import dev.swarm.phone.ui.kit.SubstrateSurface
import dev.swarm.phone.ui.kit.Kit
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
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
            "the machine is not the kit's `machineRow`, so the screen hand-built it",
            root.kitFind(MachinesTag.MACHINE)?.kitFind(KitTag.MACHINE_NAME),
        )
        assertNotNull("the machine carries no presence mark", root.kitFind(KitTag.PRESENCE_DOT))
        assertNotNull(
            "the remote-access state is not the kit's `killSwitchPanel`",
            root.kitFind(MachinesTag.REMOTE_ACCESS)?.kitFind(KitTag.KILL_TITLE),
        )
        assertNotNull("the device section has no label", root.kitFind(MachinesTag.SECTION_LABEL))
        assertNotNull(
            "the paired device is not the kit's `settingsRow`",
            root.kitFind(MachinesTag.DEVICE)?.kitFind(KitTag.SETTINGS_LABEL),
        )
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
        assertEquals(page.machine.name, textOf(machine.kitRequire(KitTag.MACHINE_NAME)))
        assertEquals(page.machine.presenceLine, textOf(machine.kitRequire(KitTag.MACHINE_META)))
        assertNull(
            "the machine row rendered row 11's `endpoint id` cell. The product has one identifier " +
                "for a machine and it is already the name, so whatever is in that cell is a copy " +
                "of the name or an invention",
            machine.kitFind(KitTag.MACHINE_ENDPOINT),
        )

        val remote = root.kitRequire(MachinesTag.REMOTE_ACCESS)
        assertEquals(page.remoteAccess.title, textOf(remote.kitRequire(KitTag.KILL_TITLE)))
        assertEquals(page.remoteAccess.body, textOf(remote.kitRequire(KitTag.KILL_BODY)))

        // The uppercase is `sectionLabel`'s `isAllCaps`, which leaves the text it was given
        // alone -- so a screen that shouted its own copy would fail here, and a screen reader
        // still hears a phrase rather than thirteen letters.
        assertEquals(
            page.pairedDevicesHeading,
            textOf(root.kitRequire(MachinesTag.SECTION_LABEL)),
        )

        val device = root.kitRequire(MachinesTag.DEVICE)
        assertEquals(page.pairedDevice.name, textOf(device.kitRequire(KitTag.SETTINGS_LABEL)))
        assertNull(
            "the device row rendered a second line. Row 13's second cell is a fingerprint in " +
                "`Mono.Agent` and nothing here can compute one, so a `Body.Secondary` sublabel is " +
                "the right words in the wrong role at best and an invention at worst",
            device.kitFind(KitTag.SETTINGS_SUBLABEL),
        )
        assertEquals(page.pairedDevice.revokeLabel, textOf(root.kitRequire(MachinesTag.REVOKE)))
        assertEquals(
            page.pairedDevice.revokeDescription,
            root.kitRequire(MachinesTag.REVOKE).contentDescription,
        )
    }

    // ---- the mark is a presence mark and not a Group -------------------------

    /**
     * See the class KDoc: the two dots are indistinguishable once drawn, so what is asserted is
     * which factory drew them.
     */
    @Test
    fun `the machine's mark is the presence dot and no status dot is on this screen`() {
        // Was `listOf(true, false)` over the model's old `online: Boolean`. All three of the
        // relay's words are walked now, because the maquette draws three `.pdot` states and the
        // third one reaches this screen through `MachinesPanelScreen.of`.
        listOf("online", "offline", "unknown").forEach { presence ->
            val root = view(panel(presence = presence))
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
     * The dot says nothing a screen reader needs WHILE the line under it still says presence in
     * words.
     *
     * A described dot here would have the row read its presence twice, and `contentDescription = ""`
     * -- the shape a caller writing `description ?: ""` ships -- would ask the reader to skip a
     * view that has something to say.
     */
    @Test
    fun `the presence mark is decorative while the row still states presence in words`() {
        val root = view(panel(silent = true))
        val dot = root.kitRequire(KitTag.PRESENCE_DOT)

        assertEquals(
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            dot.importantForAccessibility,
        )
        assertNull(dot.contentDescription)
        assertTrue(
            "the row does not say presence in words either, so the state is carried by a colour " +
                "alone -- which for a screen reader user is not carried at all",
            textOf(root.kitRequire(MachinesTag.MACHINE).kitRequire(KitTag.MACHINE_META))
                .contains("online"),
        )
    }

    /**
     * agents-tracker-ksvb.6: a healthy machine now prints NOTHING where "Your machine is
     * online." used to sit on every visit -- the presence dot's colour was already carrying the
     * fact. The words did not disappear with the sentence; they moved to the one thing left on
     * screen that can still speak for a screen reader.
     *
     * THIS REPLACES `the presence mark is decorative, because the row states presence in words`,
     * which asserted the opposite of both halves below over the same default panel: that the dot
     * carried `View.IMPORTANT_FOR_ACCESSIBILITY_NO` with a null description, and that the meta
     * line's text contained "online". Neither survives a healthy machine printing an empty line.
     */
    @Test
    fun `the presence mark announces the state once the row has nothing printed to say it`() {
        val root = view(panel())
        val dot = root.kitRequire(KitTag.PRESENCE_DOT)

        assertTrue(
            "the meta line still carries a sentence, so a description on the dot would have the " +
                "row say its own state twice",
            textOf(root.kitRequire(MachinesTag.MACHINE).kitRequire(KitTag.MACHINE_META)).isEmpty(),
        )
        assertNotEquals(
            "a healthy machine's dot is still marked decorative, so a screen reader learns " +
                "nothing about presence at all now that the sentence beside it is gone",
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            dot.importantForAccessibility,
        )
        assertTrue(
            "the dot's description does not say the machine is online: ${dot.contentDescription}",
            dot.contentDescription?.contains("online") == true,
        )
    }

    // ---- remote access ships as a statement, not a control -------------------

    /**
     * Row 12 as amended: no trailing control, and the mock's toggle does not ship.
     *
     * BOTH SPELLINGS ARE ASKED FOR, because the kit's own toggle is a `View` with a drawable and
     * not a `CompoundButton`: a check for the platform type alone would pass over the very control
     * this screen must not have, and a check for the kit's tag alone would pass over a platform
     * `Switch` reached for instead. That the PANEL has no room for one at all is asserted where
     * the component is -- `KillSwitchPanelTest` counts its children -- because the signature is
     * what makes it true.
     */
    @Test
    fun `no control for the kill switch is anywhere on the screen`() {
        val root = view(panel(killSwitchEngaged = true))

        assertNull(
            "the kit's toggle is on the machines screen. `App.KillSwitchEngaged` is read-only by " +
                "design -- handleRemoteSetControl refuses the remote tier before consulting its " +
                "backend -- so a switch here is one that cannot act",
            root.kitFind(KitTag.TOGGLE_TRACK),
        )
        assertTrue(
            "a CompoundButton is on the machines screen, which is the platform's switch reached " +
                "for instead of the kit's",
            root.descendants().none { it is CompoundButton },
        )
    }

    // ---- revoke -------------------------------------------------------------

    /**
     * WHICH VARIANT, ASKED AS A COMPOSITION QUESTION. `ctaButton` has three, they differ by fill
     * and ink, and row 13 says this one is `.a2-no` at chip metrics -- the same tinted fill the
     * approval sheet's Deny carries, at the scope chip's size. A neutral bordered control would
     * read as an ordinary action, which for the one thing on this screen that destroys something
     * is the wrong sentence. What that treatment IS remains `DenyChipTest`'s to assert against the
     * design source; what is asked here is which control the screen composed.
     */
    @Test
    fun `revoke is the kit's denial control and the surface can reach it`() {
        val root = view(panel())
        val revoke = root.kitRequire(MachinesTag.REVOKE)
        val surface = revoke.background

        assertTrue(
            "revoke's background is $surface, so it is not a kit component at all",
            surface is SubstrateSurface,
        )
        assertEquals(
            "revoke is not painted the deny fill. Row 13: the mock's bespoke outline button is " +
                "discarded for the `.a2-no` treatment, which is the one destructive idiom this " +
                "skin has -- an outline control beside a tinted one is two idioms for one meaning",
            Kit.denyFill(context),
            (surface as SubstrateSurface).spec.fill,
        )
        assertTrue(
            "revoke is inside the paired-device row it acts on",
            root.kitRequire(MachinesTag.DEVICE).descendants().any { it === revoke },
        )
    }

    /**
     * The button must not starve the row it sits in.
     *
     * `ctaButton` -- the control this row used before row 13 was read -- lays itself out
     * MATCH_PARENT wide, which is right for the full-width sheet CTA it was written for and wrong
     * inside a horizontal row: LinearLayout measures the unweighted MATCH_PARENT child against the
     * whole row and leaves the weighted text column zero, so the device name vanishes and the row
     * renders as one wide button. `denyChip` hugs, and this is where that is held.
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
            activity = listOf(JournalRow(cursor = 9, sessionId = "atlas", type = "launch", group = "working")),
        )
        assertEquals(
            "the screen grew when the journal did, so an audit log is being raised here as well " +
                "as by the activity screen -- two components rendering one section",
            view(panel()).descendants().size,
            view(busy).descendants().size,
        )
    }
}
