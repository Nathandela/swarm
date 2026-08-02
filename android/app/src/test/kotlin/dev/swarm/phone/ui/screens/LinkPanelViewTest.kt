package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.StreamView
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the link section AS DRAWN.
 *
 * WHAT THIS ASKS THAT `LinkPanelTest` CANNOT. That suite asks what the section SAYS; this asks
 * whether it is on screen -- which component renders it, in what order, and which of the two
 * mutually exclusive row slots each channel actually got. "The model is beautiful and nothing
 * renders it" is the exact defect this whole screen was raised to fix: `ClockBanner` and
 * `StreamView` were modelled, tested and reached by the adapter, and drawn by nothing.
 *
 * **THE ASSERTION THIS SUITE EXISTS FOR IS AN ABSENCE: `KitTag.SETTINGS_STATUS` must not appear on
 * a stale channel's row.** `statusLabel` is `--p-hero`, which derivation row 15 spells out as the
 * LIVENESS claim rather than a status colour, so a stale row carrying one is a known hole painted
 * with the colour that means alive. Nothing about the rendered text distinguishes a screen that
 * gets this right from one that puts the label on every row -- the words in the second line differ
 * and the label reads `Live` either way -- which is why the tag is what gets asserted.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED: appearance. The settings row's padding, the section label's
 * metrics and `statusLabel`'s ink are PB-DS-10's and are asserted in `ui/kit`; repeating them here
 * would be a second opinion that can disagree with the first.
 */
@RunWith(RobolectricTestRunner::class)
class LinkPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val channelNames = listOf("journal", "terminal", "reply", "grant")

    private fun panel(
        verdict: String = "",
        stale: Set<String> = emptySet(),
        repairing: Set<String> = emptySet(),
    ): LinkPanel = LinkPanelScreen.of(
        ClockBanner.of(verdict),
        channelNames.map { name ->
            StreamView(
                stream = name,
                stale = name in stale || name in repairing,
                resyncPending = name in repairing,
            )
        },
    )

    private fun view(panel: LinkPanel, below: View? = null): View =
        linkPanelView(context = context, panel = panel, below = below)

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

    /** The channel row for one stream, found by the label the kit put its name in. */
    private fun View.channelRow(stream: String): View = allTagged(LinkTag.CHANNEL)
        .single { textOf(it.kitFind(KitTag.SETTINGS_LABEL)) == stream }

    // ---- the composition --------------------------------------------------------

    @Test
    fun `the link section is composed of the kit components row 15 names`() {
        val root = view(panel(verdict = "this device's clock is 2m0s ahead of the machine"))

        assertNotNull("the section has no heading", root.kitFind(LinkTag.SECTION_LABEL))
        assertNotNull("a skewed clock is not drawn at all", root.kitFind(LinkTag.CLOCK))
        assertEquals(
            "the four repair channels did not each get a row",
            channelNames.size,
            root.allTagged(LinkTag.CHANNEL).size,
        )
        assertNotNull(
            "a channel is not the kit's `settingsRow`, so the screen hand-built it",
            root.kitFind(LinkTag.CHANNEL)?.kitFind(KitTag.SETTINGS_LABEL),
        )
    }

    @Test
    fun `every part carries the model's own copy`() {
        val page = panel(verdict = "this device's clock is 2m0s ahead of the machine")
        val root = view(page)

        assertEquals(page.heading, textOf(root.kitRequire(LinkTag.SECTION_LABEL)))
        assertEquals(page.clockNotice, textOf(root.kitRequire(LinkTag.CLOCK)))
        assertEquals(
            channelNames,
            root.allTagged(LinkTag.CHANNEL).map { textOf(it.kitRequire(KitTag.SETTINGS_LABEL)) },
        )
    }

    @Test
    fun `the section reads heading, then the clock, then the channels`() {
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in LinkTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(view(panel(verdict = "this device's clock is 2m0s ahead of the machine")))

        assertEquals(
            listOf(LinkTag.SECTION_LABEL, LinkTag.CLOCK) + channelNames.map { LinkTag.CHANNEL },
            order,
        )
    }

    // ---- PB-TIME-1: nothing is drawn over a healthy clock -----------------------

    /**
     * The plausible wrong implementation is a notice view that is always attached and sometimes
     * empty. A blank `TextView` still occupies its line height and its gap, so the section would
     * carry a permanent hole where a warning goes -- and the model's own decision that an empty
     * verdict is HEALTH would have been rendered as a gap in the layout instead of as nothing.
     */
    @Test
    fun `a healthy clock draws no view at all, not an empty one`() {
        val root = view(panel(verdict = ""))

        assertNull(
            "a clock notice view is on screen over a phone whose clock is fine. An empty verdict " +
                "is a healthy clock, and an always-attached notice is either a permanent warning " +
                "or a permanent blank line",
            root.kitFind(LinkTag.CLOCK),
        )
        assertNotNull("the channels went away with the clock notice", root.kitFind(LinkTag.CHANNEL))
    }

    // ---- the liveness label is on the live rows and nowhere else ----------------

    /** See the class KDoc: this is the assertion the suite exists for. */
    @Test
    fun `only a channel with no hole in it carries the kit's liveness label`() {
        val root = view(panel(stale = setOf("terminal"), repairing = setOf("reply")))

        listOf("journal", "grant").forEach { whole ->
            assertNotNull(
                "the `$whole` channel has no hole and carries no liveness label, so a section " +
                    "with four healthy channels renders the same as one that failed to read them",
                root.channelRow(whole).kitFind(KitTag.SETTINGS_STATUS),
            )
        }
        listOf("terminal", "reply").forEach { holed ->
            assertNull(
                "the `$holed` channel has a known hole in it AND the kit's `statusLabel`, which " +
                    "is `--p-hero` -- row 15's liveness claim. Everything from `StreamState` " +
                    "answering \"stale\" through a repair to `ResyncPending` being orthogonal " +
                    "exists to stop a known hole reading as live; this is the screen undoing it",
                root.channelRow(holed).kitFind(KitTag.SETTINGS_STATUS),
            )
        }
    }

    @Test
    fun `a holed channel says what is missing on its second line, and a whole one has none`() {
        val page = panel(stale = setOf("terminal"))
        val root = view(page)

        assertEquals(
            page.channels.single { it.stream == "terminal" }.notice,
            textOf(root.channelRow("terminal").kitRequire(KitTag.SETTINGS_SUBLABEL)),
        )
        assertNull(
            "a healthy channel drew a second line. `settingsRow` renders no sublabel at all for " +
                "null rather than an empty one, because a blank line still takes its own height " +
                "and would leave the healthy rows taller than the row that has something to say",
            root.channelRow("journal").kitFind(KitTag.SETTINGS_SUBLABEL),
        )
    }

    /**
     * The two slots stay mutually exclusive once drawn.
     *
     * `LinkPanelTest` holds it on the model; a view that rendered the notice AND a hardcoded label
     * would satisfy that suite and put both on screen.
     */
    @Test
    fun `no row draws a liveness label and a hole in the same row`() {
        val root = view(panel(stale = setOf("grant"), repairing = setOf("journal")))

        root.allTagged(LinkTag.CHANNEL).forEach { row ->
            val label = row.kitFind(KitTag.SETTINGS_STATUS)
            val sub = row.kitFind(KitTag.SETTINGS_SUBLABEL)
            assertTrue(
                "the `${textOf(row.kitRequire(KitTag.SETTINGS_LABEL))}` row draws " +
                    (if (label != null && sub != null) "a liveness label AND a hole" else "neither") +
                    ". One is a row that says one true thing; both is a channel claiming to be " +
                    "live and holed at once, and neither is a channel rendered as a bare name",
                (label != null) != (sub != null),
            )
        }
    }

    // ---- what this section does not own -----------------------------------------

    /**
     * The Machines destination's remaining sentence -- what this phone still cannot read about its
     * machine -- is hosted UNDER the section rather than replaced by it. Presence and the paired
     * device name are agents-tracker-xtj's and neither may be invented.
     */
    @Test
    fun `what this section does not own is hosted under it, not instead of it`() {
        val trailing = View(context)
        val root = view(panel(), below = trailing) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
        assertNotNull("hosting the remainder dropped the section", root.kitFind(LinkTag.SECTION_LABEL))
    }

    /**
     * PB-SYNC-1's repair action is not here, and its absence is asserted rather than assumed.
     *
     * `App.Resync` is rate-bounded per section 6.0 and its refusal needs rendering, so this screen
     * reports and cannot repair. A control that looked like a repair and did nothing would be the
     * defect `navHeaderDrill`'s chevron shipped in -- an affordance that looks like a control and
     * does not act.
     */
    @Test
    fun `no control is on this section, because nothing here can repair anything`() {
        val root = view(panel(stale = setOf("journal", "terminal", "reply", "grant")))

        val clickable = mutableListOf<View>()
        fun walk(v: View) {
            if (v.isClickable) clickable += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        assertTrue(
            "$clickable on the link section respond to a tap. Nothing here can repair a stream -- " +
                "`App.Resync` is unbound -- so a control is one that cannot act",
            clickable.isEmpty(),
        )
    }
}
