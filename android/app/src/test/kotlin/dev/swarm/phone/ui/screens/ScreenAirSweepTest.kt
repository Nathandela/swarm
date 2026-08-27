package dev.swarm.phone.ui.screens

import android.app.Activity
import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.PhoneActivity
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.ScannerState
import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.PresenceMark
import dev.swarm.phone.ui.kit.composerBar
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow
import dev.swarm.phone.ui.kit.textField
import dev.swarm.phone.ui.kit.toggle
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.10: the owner's field report of
 * 2026-08-09 -- "side padding is right on the Inbox and pairing, absent everywhere else".
 *
 * THE RULING THIS GUARDS, in the bead's own words: *every leaf text/button renders with an
 * EFFECTIVE side inset of at least `swarm_space_12`, applied EXACTLY ONCE.* Both halves are
 * defects. Under the floor is the report itself -- a sentence or a control touching the glass.
 * Twice is agents-tracker-2pnu F2, where `pairOnlyView`'s column and `pairingPanelView`'s own
 * spent row 18's cell one inside the other and the flow rendered at 48 dp sides.
 *
 * WHY A SWEEP AND NOT ONE ASSERTION PER SCREEN. Six screens shipped flush at once, each with its
 * own suite, because no suite in this app ever asked what a person MEASURES: the composition
 * tests read tags and copy, and the kit tests read one component standing alone, where every
 * padding is present and correct. The inset a reader sees is a property of the whole stack from
 * the window's edge down, so it can only be read off a screen that has been laid out -- and a
 * check that walks every destination is the only shape that a seventh screen cannot ship past.
 *
 * ## What counts as a leaf, and where its edge is
 *
 * A leaf is what a person reads or presses: a visible `TextView` with words in it, or anything
 * clickable. Its EDGE is what they see -- the painted box where the view has a background of its
 * own (a card, a well, a CTA), and the TEXT where it does not (a notice, a heading, an empty
 * state), because the ground shows through the second kind and the padding is the only thing
 * holding the glyphs off the glass.
 *
 * ## And what counts as a SURFACE
 *
 * **THE RULING IS ABOUT ELEMENTS AND NOT ONLY ABOUT WORDS.** A leaf sweep alone certifies a card
 * that runs edge to edge as long as its own padding holds the label 14 dp in, which is exactly
 * what shipped: `settingsRow` and `machineRow` pay `space_14` INSIDE their box and carry no margin
 * at all, so on Settings the `--p-card` fill and its `--p-hair` border touched both edges of the
 * screen while every assertion in this file stayed green. What a person sees is the painted box,
 * so every view that paints one is measured against the same floor -- and the fix is the seam the
 * Inbox already proves: `sessionList` insets its cards by `space_12` and the card keeps its own
 * padding inside -- `space_12` on a session row, rows 11 and 15's `space_14` on a settings or
 * machine row. The ARRANGEMENT is what a settings row lacked, not the number: an outer step from
 * the container, the card's own padding kept within it.
 *
 * The exemptions are the app's FURNITURE, and they are named one by one in [fullBleedChrome]
 * rather than inferred from a shape -- an exemption a reader cannot see is how the next full-bleed
 * card gets certified.
 *
 * NEGATIVE MARGINS ARE ROOM AND NOT POSITION. `ctaButton`'s bloom variant inflates itself by the
 * halo's radius and hands every pixel back with a negative margin ([dev.swarm.phone.ui.kit
 * .CtaSpec]'s `insetPx`), so its VIEW is 18 dp wider on each side than the button anybody aims
 * at. The visible box is the view's box shrunk by whatever a negative margin gave back.
 *
 * A HORIZONTAL SCROLLER IS ITS CONTENT'S EDGE. `monoWell(...).scrolledHorizontally()` measures
 * its child with no width ceiling, so a diff wider than the phone has a right edge off-screen and
 * a naive walk would report it as a negative inset. What a reader sees is the viewport, so the
 * scroller's box is the box for everything inside it.
 *
 * ## What is deliberately out of the sweep
 *
 * THE SCAFFOLD'S OWN CHROME. `tabBar` draws its items at `.ptabs`'s `padding: 14px 8px 24px` and
 * the sync strip is ruled "radius none and no side inset: it is full-bleed chrome across the top
 * of the app" (§4's Sync status pill and strip row). Both are the app's furniture rather than a
 * destination's content, both are ruled at their own numbers, and neither is what the field
 * report is about. The destinations are swept as the scaffold hosts them, which is at the
 * window's own width: `phoneScaffoldView` puts the content in a `ScrollView` that spends no side
 * padding, so a destination's own left edge IS the screen's.
 */
@RunWith(RobolectricTestRunner::class)
class ScreenAirSweepTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** The ruled floor: `swarm_space_12`, the step the Inbox's own row container already spends. */
    private fun air(): Int = context.resources.getDimensionPixelSize(R.dimen.swarm_space_12)

    /**
     * The two steps a COLUMN is allowed to spend as the screen's own side air: the destinations'
     * `swarm_space_12` and the pairing scaffold's ruled `swarm_space_24` (derivation row 18).
     * Either one, once -- a path that crosses two of them is the F2 doubling.
     */
    private fun airSteps(): Set<Int> = setOf(
        context.resources.getDimensionPixelSize(R.dimen.swarm_space_12),
        context.resources.getDimensionPixelSize(R.dimen.swarm_space_24),
    )

    /** A handset's width, so the sweep reads the layout a person holds rather than an unbounded one. */
    private fun screenWidthPx(): Int = (360 * context.resources.displayMetrics.density).toInt()

    // ---- the sweep --------------------------------------------------------

    /** One leaf, as measured: what it says, how far in each edge sits, and how often the air was spent. */
    private data class Leaf(
        val what: String,
        val start: Int,
        val end: Int,
        val airSpends: Int,
    )

    /**
     * Every visible leaf on [root], laid out at a handset's width.
     *
     * The walk carries three things down: the absolute left of the current view, the box a
     * horizontal scroller has clamped it to (null outside one), and how many times a container
     * above it has spent the screen's own side air.
     */
    private fun sweep(root: View): List<Leaf> = leavesOf(layOut(root), screenWidthPx())

    private fun leavesOf(root: View, width: Int): List<Leaf> {
        val leaves = mutableListOf<Leaf>()
        fun walk(view: View, left: Int, clamp: IntRange?, spentAbove: Int) {
            if (view.visibility != View.VISIBLE) return
            if (chromeLeaf(view)) return
            val margins = view.layoutParams as? ViewGroup.MarginLayoutParams
            val spent = spentAbove + if (margins?.marginStart in airSteps()) 1 else 0
            val scroller = clamp ?: if (view is HorizontalScrollView) left..(left + view.width) else null

            if (view is ViewGroup && !isLeaf(view)) {
                val inner = spent + if (view.background == null && view.paddingStart in airSteps()) 1 else 0
                for (i in 0 until view.childCount) {
                    walk(view.getChildAt(i), left + view.getChildAt(i).left, scroller, inner)
                }
                return
            }
            if (!isLeaf(view)) return

            // The view's own box, then the room it took inside its own bounds and does not paint,
            // then the padding on a view that paints nothing -- the glyphs are its edge when the
            // ground shows through.
            val room = bloomRoom(view) to bloomRoom(view)
            val pad = if (view.background == null) view.paddingStart to view.paddingEnd else 0 to 0
            val box = clamp ?: left..(left + view.width)
            leaves += Leaf(
                what = describe(view),
                start = box.first + room.first + pad.first,
                end = width - box.last + room.second + pad.second,
                airSpends = spent,
            )
        }
        walk(root, 0, null, 0)
        return leaves
    }

    /**
     * [root] laid out at a handset's width, inside the bare host that hosts it, which is what both
     * walks read from.
     *
     * **THE HOST IS NOT SCAFFOLDING FOR THE TEST: IT IS THE ONE THING A MARGIN NEEDS TO EXIST.**
     * Android applies a child's margin in its PARENT's layout pass, so a destination measured as the
     * root of the window reports every margin it carries as zero -- and the approval sheet's air is
     * a margin on the composition's own root. `phoneScaffoldView` puts a destination in a
     * `ScrollView` that spends no side padding (the claim below is what proves it), so a bare
     * `MATCH_PARENT` column IS that host, and reading from it is the more faithful measurement
     * rather than a softer one.
     */
    private fun layOut(root: View): View {
        val width = screenWidthPx()
        (root.parent as? ViewGroup)?.removeView(root)
        val host = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            // A glowing dot and an inflated halo are drawn past their own bounds, and every
            // container between them and the window has to allow it.
            clipChildren = false
            clipToPadding = false
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(root)
        }
        host.measure(
            View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
        )
        host.layout(0, 0, host.measuredWidth, host.measuredHeight)
        return host
    }

    /** One painted surface, as measured: what it is, and how far in each painted edge sits. */
    private data class Surface(val what: String, val start: Int, val end: Int)

    /**
     * Every visible SURFACE on [root]: a view that paints a background of its own.
     *
     * IT IS A SECOND WALK AND NOT A CASE IN THE FIRST, because the two stop in different places.
     * [sweep] stops at a leaf -- what it measures is the words a person reads, and a clickable card
     * IS that leaf. A card is the box those words sit in, and the box inside it is a box too, so
     * this walk descends all the way down and measures every one of them.
     */
    private fun surfaces(root: View): List<Surface> = surfacesOf(layOut(root), screenWidthPx())

    private fun surfacesOf(root: View, width: Int): List<Surface> {
        val painted = mutableListOf<Surface>()
        fun walk(view: View, left: Int, clamp: IntRange?) {
            if (view.visibility != View.VISIBLE) return
            val scroller = clamp ?: if (view is HorizontalScrollView) left..(left + view.width) else null
            if (view.background != null && !fullBleedChrome(view)) {
                val room = bloomRoom(view)
                val box = clamp ?: left..(left + view.width)
                painted += Surface(describe(view), box.first + room, width - box.last + room)
            }
            if (view is ViewGroup) {
                for (i in 0 until view.childCount) {
                    walk(view.getChildAt(i), left + view.getChildAt(i).left, scroller)
                }
            }
        }
        walk(root, 0, null)
        return painted
    }

    /**
     * The full-bleed chrome, BY NAME. Nothing else is exempt.
     *
     * THE TAB BAR AND THE COMPOSER BAR, which are one construction and the only two views in this
     * app that paint [TopRule]: a fill plus a 1 dp `--p-hair` line across the WHOLE width, with the
     * line as the only thing separating the bar from the content scrolling under it (Substrate bans
     * drop shadows, and `TabBar.kt` records exactly that). A side margin here would stop the rule
     * spanning the screen and leave the ground showing beside a bar that has no radius -- the two
     * are the app's furniture, at their own ruled numbers (rows 19 and 9), rather than a
     * destination's content.
     *
     * THE BROKEN-STATE STRIP, which §4's Sync status pill and strip row rules "radius none and no
     * side inset: it is full-bleed chrome across the top of the app". It is tagged, so this names it
     * exactly rather than by a shape something else could take.
     *
     * THE SCROLLED CONTENT'S OWN SCROLLER, for the reason the leaf walk already gives: a
     * [HorizontalScrollView] measures its child with no width ceiling, so a diff wider than the
     * phone has a right edge off-screen. The walk clamps everything inside one to the viewport; the
     * scroller itself is the viewport and has nothing to be measured against.
     */
    private fun fullBleedChrome(view: View): Boolean = when {
        view.background is dev.swarm.phone.ui.kit.TopRule -> true
        view.tag == KitTag.SYNC_STRIP -> true
        view is HorizontalScrollView -> true
        else -> false
    }

    /**
     * The same furniture where it is a LEAF rather than a surface, which is where a person presses
     * it: ONE ITEM in the tab bar, and the strip.
     *
     * A tab item is `.ptabs div` -- a `flex: 1` column of a glyph and a word, at the bar's own
     * `padding: 14px 8px 24px` -- so the first and last of the three necessarily reach the edges of
     * a bar that is itself full-bleed. It is the only clickable thing in this app that is allowed
     * to, and it is named by the label the kit tags rather than by its shape. The strip is exempt
     * for the reason it is exempt as a surface, one sentence up; it appears here as well because it
     * is a `TextView` with words in it, which is the definition of a leaf.
     *
     * NOTHING ELSE INSIDE THE TWO BARS IS EXEMPT, and that is the difference between this and
     * [fullBleedChrome]. The composer's field and its Send control are content in a bar, held off
     * the glass by row 9's own `space_14`, and the walk keeps descending to measure them.
     */
    private fun chromeLeaf(view: View): Boolean =
        view.tag == KitTag.SYNC_STRIP ||
            (
                view is ViewGroup &&
                    (0 until view.childCount).any { view.getChildAt(it).tag == KitTag.TAB_LABEL }
                )

    /**
     * The room a view took inside its own bounds and does not paint: `ctaButton`'s halo.
     *
     * IT IS READ OFF THE DRAWABLE AND NOT OFF THE NEGATIVE MARGIN, which is the more accurate of
     * two readings of the same fact rather than a softer one. `CtaSurface` insets every layer it
     * paints by [dev.swarm.phone.ui.kit.CtaSpec.insetPx], so that IS where the visible edge is; the
     * negative margin is the room being handed back to the layout, and `screenAir` legitimately
     * changes it -- the air minus the room -- while the drawable's inset does not move.
     */
    private fun bloomRoom(view: View): Int =
        (view.background as? dev.swarm.phone.ui.kit.CtaSurface)?.spec?.insetPx ?: 0

    /** What a person reads or presses: words on screen, or anything that takes a tap. */
    private fun isLeaf(view: View): Boolean =
        (view is TextView && view.text.isNotBlank()) || view.isClickable

    /**
     * What a fault names, and it names something a person could point at.
     *
     * A container's own words are the words INSIDE it: a tab item, a card and a bar are all
     * untagged `LinearLayout`s, and three faults reading "LinearLayout" are three faults nobody can
     * act on. So an untagged group borrows the first line of text under it.
     */
    private fun describe(view: View): String {
        val words = ((view as? TextView)?.text?.toString() ?: view.words()).take(40)
        val tag = view.tag?.toString()?.takeIf { it.isNotEmpty() }
        return "${view.javaClass.simpleName}${tag?.let { "[$it]" } ?: ""}" +
            if (words.isBlank()) "" else " \"$words\""
    }

    /** The first words under [this], or "" where there are none. */
    private fun View.words(): String {
        if (this is TextView && text.isNotBlank()) return text.toString()
        if (this is ViewGroup) {
            for (i in 0 until childCount) {
                getChildAt(i).words().takeIf { it.isNotEmpty() }?.let { return it }
            }
        }
        return contentDescription?.toString().orEmpty()
    }

    /** Every screen this app can put in front of a person, built the way production builds it. */
    private fun destinations(): Map<String, View> = mapOf(
        "Inbox" to inbox(),
        "Activity" to activity(),
        "Settings" to settings(),
        "Session detail" to sessionDetail(),
        "Launch form" to launchForm(),
        "Approval sheet" to approvalSheet(),
        "Pair-only offer" to pairOnlyOffer(),
        "Pairing (started)" to pairingStarted(),
    )

    // ---- the claims -------------------------------------------------------

    @Test
    fun `every leaf on every destination clears the ruled side inset`() {
        val floor = air()
        val faults = destinations().flatMap { (screen, root) ->
            sweep(root).filter { minOf(it.start, it.end) < floor }.map { leaf ->
                "$screen: ${leaf.what} sits ${leaf.start}px from the left edge and ${leaf.end}px " +
                    "from the right, against the ruled ${floor}px floor"
            }
        }

        assertEquals(
            "agents-tracker-nx44.10: ${faults.size} leaves render inside the ruled " +
                "`swarm_space_12` side inset -- text and buttons touching the glass:\n" +
                faults.joinToString("\n"),
            emptyList<String>(),
            faults,
        )
    }

    /**
     * The same floor, against the box a person actually sees drawn.
     *
     * THE OWNER'S RULING IS ABOUT ELEMENTS. A card whose own padding holds its label 14 dp in still
     * paints its fill and its border on the glass, and the leaf claim above cannot see it -- which
     * is how Settings shipped a column of `--p-card` rows running edge to edge with every leaf
     * assertion green.
     */
    @Test
    fun `every visible surface on every destination clears the ruled side inset`() {
        val floor = air()
        val faults = destinations().flatMap { (screen, root) ->
            surfaces(root).filter { minOf(it.start, it.end) < floor }.map { surface ->
                "$screen: ${surface.what} paints ${surface.start}px from the left edge and " +
                    "${surface.end}px from the right, against the ruled ${floor}px floor"
            }
        }

        assertEquals(
            "${faults.size} SURFACES touch the glass -- cards, wells and panels painted inside " +
                "the ruled `swarm_space_12` side inset. The owner's 2026-08-09 ruling is about " +
                "elements and not only about the words in them:\n" + faults.joinToString("\n"),
            emptyList<String>(),
            faults,
        )
    }

    @Test
    fun `no leaf is given the screen's air twice`() {
        val faults = destinations().flatMap { (screen, root) ->
            sweep(root).filter { it.airSpends > 1 }.map { leaf ->
                "$screen: ${leaf.what} has the screen's own side air spent ${leaf.airSpends} " +
                    "times above it, so it renders ${leaf.start}px in"
            }
        }

        assertEquals(
            "agents-tracker-nx44.10: ${faults.size} leaves are double-padded. This is " +
                "agents-tracker-2pnu F2's defect: a column inset stacked on a child that " +
                "already carried its own.\n" + faults.joinToString("\n"),
            emptyList<String>(),
            faults,
        )
    }

    /**
     * The premise both claims above rest on: a destination's own left edge IS the screen's.
     *
     * The sweep lays each destination out at the window's width and reads from its own box, which
     * is only the same measurement a person takes if nothing between it and the glass adds a side
     * inset. `phoneScaffoldView` puts the destination in a `ScrollView` inside a column, and a side
     * padding on either would shift every leaf on every screen by the same amount with the sweep
     * still reporting green -- so it is checked rather than assumed.
     */
    @Test
    fun `the scaffold hands the destination the whole width of the screen`() {
        val destination = TextView(context).apply { text = "the destination" }
        val scaffold = phoneScaffoldView(
            context = context,
            content = destination,
            tabs = Destination.entries.map { d ->
                InboxTab(label = d.label, selected = d == Destination.INBOX, badgeCount = 0, badgeDescription = null)
            },
            destination = Destination.INBOX,
            onSelectDestination = {},
        )
        val width = screenWidthPx()
        scaffold.measure(
            View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(width * 2, View.MeasureSpec.EXACTLY),
        )
        scaffold.layout(0, 0, width, width * 2)

        var left = 0
        var view: View = destination
        while (view !== scaffold) {
            left += view.left
            view = view.parent as View
        }
        assertEquals(
            "the scaffold insets the destination, so every inset this sweep measures is short by " +
                "that much and the ruled floor is being read against the wrong edge",
            0,
            left,
        )
        assertEquals("the destination is not given the screen's whole width", width, destination.width)
    }

    /**
     * The negative controls, in memory. A reader that always answered "far enough" would certify
     * a screen built entirely out of flush text, and one that never counted the air twice would
     * certify F2 itself.
     */
    @Test
    fun `the sweep can see a flush leaf and a doubled one`() {
        val flush = LinearLayout(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(TextView(context).apply { text = "flush against the glass" })
        }
        assertTrue(
            "the sweep certified a bare TextView with no inset at all",
            sweep(flush).any { it.start < air() },
        )

        val doubled = sessionList(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(sessionList(context).apply { addView(notice(context, "twice over")) })
        }
        assertTrue(
            "the sweep counted one air spend where two containers spent it",
            sweep(doubled).any { it.airSpends > 1 },
        )
    }

    /**
     * The surface reader's own negative controls, in memory.
     *
     * A reader that always answered "far enough" would certify the very column this claim was
     * added for, and one that exempted anything with a background would exempt every card in the
     * app -- so both directions are held: a bare `settingsRow` in a bare column is seen, and the
     * same row inside `sessionList`'s ruled `space_12` is not.
     */
    @Test
    fun `the surface sweep can see a card on the glass and passes the one that is held off it`() {
        val flush = LinearLayout(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(settingsRow(context, label = "Alerts", sublabel = "When a session needs you"))
        }
        assertTrue(
            "the surface sweep certified a card painted edge to edge, which is the defect it " +
                "exists for: its label sits `space_14` in and the box it is drawn on does not",
            surfaces(flush).any { minOf(it.start, it.end) < air() },
        )

        val held = LinearLayout(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            addView(
                sessionList(context).apply {
                    addView(settingsRow(context, label = "Alerts", sublabel = "the same row"))
                },
            )
        }
        assertTrue(
            "the surface sweep found a fault in the seam the Inbox already ships -- a card inside " +
                "`sessionList`'s ruled `space_12` -- so it would report every screen in the app",
            surfaces(held).none { minOf(it.start, it.end) < air() },
        )
    }

    // ---- the window, as the app actually assembles it -----------------------

    /**
     * The same two floors, read off the WINDOW rather than off a factory's return value.
     *
     * **WHAT NO DESTINATION BUILDER CAN REACH IS THE HALF THE SURFACES OWN** (agents-tracker-
     * nx44.11). Five of the screen views take a `below:` slot -- views the slice has not recomposed,
     * hosted under the panel -- and every builder above passes null, because what production puts
     * in that slot is built by `PhoneSurface` and `SettingsSurface`: the startup line, the
     * capability notice and PB-APP-9's routed outcome, which need an Activity, a facade and paired
     * state to exist at all. They are kit `notice`s added to bare `MATCH_PARENT` columns that
     * "carry no padding of their own any more", so three sentences on the Inbox and one that IS the
     * whole of Settings rendered against both edges while every claim above stayed green. A guard
     * that stops at the factories is a guard the next surface-owned view walks straight past.
     *
     * IT IS THE APP AND NOT A HARNESS. `PhoneRuntime.phone()` answers [dev.swarm.phone
     * .PhoneStartup.Unavailable] on every JVM run -- the phone core is a gomobile AAR of `.so`
     * files -- which is the one state that puts all of this on screen at once: the routed sentence
     * is written to `PhoneSurface`'s `status` on the Inbox, to the Activity destination's own
     * notice, and on Settings it replaces the panel entirely (`SettingsSurface.render`'s
     * `Unavailable` branch clears the host and adds `outcome` alone). `PhoneSurfaceUnavailableTabs
     * Test` reads the same three tabs in the same state for the same reason.
     *
     * THE CHROME IS ON SCREEN HERE AND NOWHERE ELSE, which is what makes [fullBleedChrome]'s
     * exemptions load-bearing rather than documentary: the tab bar and the sync strip belong to the
     * scaffold, so a destination built alone has neither.
     */
    @Test
    fun `every leaf and every surface in the real window clears the ruled side inset`() {
        val floor = air()
        val faults = mutableListOf<String>()
        val read = mutableMapOf<String, Int>()

        for (tab in listOf("Inbox", "Activity", "Settings")) {
            ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
                scenario.onActivity { activity ->
                    activity.tapTab(tab)
                    val window = activity.laidOutWindow()
                    val width = screenWidthPx()

                    val leaves = leavesOf(window, width)
                    read[tab] = leaves.size
                    leaves.filter { minOf(it.start, it.end) < floor }.forEach { leaf ->
                        faults += "$tab: ${leaf.what} sits ${leaf.start}px from the left edge " +
                            "and ${leaf.end}px from the right, against the ruled ${floor}px floor"
                    }
                    surfacesOf(window, width).filter { minOf(it.start, it.end) < floor }
                        .forEach { surface ->
                            faults += "$tab: ${surface.what} paints ${surface.start}px from the " +
                                "left edge and ${surface.end}px from the right, against the " +
                                "ruled ${floor}px floor"
                        }
                }
            }
        }

        // NON-VACUOUS FIRST. A sweep of a window that never laid itself out reports no faults and
        // no leaves, which is the one way this claim could certify the defect it was written for.
        read.forEach { (tab, leaves) ->
            assertTrue(
                "the $tab tab put NOTHING readable on screen, so this sweep measured an empty " +
                    "window and would have passed whatever the app renders",
                leaves > 0,
            )
        }
        assertEquals(
            "agents-tracker-nx44.11: ${faults.size} things the SURFACES own render inside the " +
                "ruled `swarm_space_12` side inset -- the lines no screen factory builds:\n" +
                faults.joinToString("\n"),
            emptyList<String>(),
            faults,
        )
    }

    /** The window, measured and laid out at a handset's size -- Robolectric lays out nothing itself. */
    private fun Activity.laidOutWindow(): View {
        val width = screenWidthPx()
        val height = width * 2
        val content = findViewById<ViewGroup>(android.R.id.content)
        content.measure(
            View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(height, View.MeasureSpec.EXACTLY),
        )
        content.layout(0, 0, width, height)
        return content
    }

    /** Press a tab by the word on it, which is what a person does and what the smoke does. */
    private fun Activity.tapTab(label: String) {
        val bar = mutableListOf<TextView>()
        fun walk(view: View) {
            if (view is TextView && view.tag == KitTag.TAB_LABEL) bar += view
            if (view is ViewGroup) for (i in 0 until view.childCount) walk(view.getChildAt(i))
        }
        walk(findViewById<ViewGroup>(android.R.id.content))
        val tab = bar.firstOrNull { it.text.toString() == label }
        assertTrue("there is no tab labelled \"$label\" on screen", tab != null)
        (tab!!.parent as View).performClick()
    }

    // ---- the screens, as production builds them ---------------------------

    private fun inbox(): View = triageInboxView(
        context = context,
        screen = TriageInboxScreen.of(
            inbox = TriageInbox.from(
                listOf(
                    SessionRow(
                        id = "mbp/api",
                        title = "api",
                        group = "needs_input",
                        need = "waiting on you",
                        present = true,
                        agent = "claude",
                    ),
                ),
                journalStale = true,
            ),
        ),
        onSelectSession = {},
        onSelectScope = {},
    )

    private fun activity(): View = activityPanelView(
        context = context,
        panel = ActivityPanel(
            title = "Activity",
            sections = listOf(
                ActivitySection(
                    heading = "TODAY",
                    rows = listOf(ActivityEntry(cursor = 1, body = "api launched", emphasis = "api")),
                    emptyCopy = "Nothing has happened yet.",
                ),
                ActivitySection(heading = "EARLIER", rows = emptyList(), emptyCopy = "Nothing here."),
            ),
            staleNotice = "Some records did not reach this phone.",
        ),
    )

    private fun settings(): View = settingsPanelView(
        context = context,
        panel = SettingsPanel(
            title = "Settings",
            sections = listOf(
                SettingsSection(
                    heading = "NOTIFICATIONS",
                    rows = listOf(
                        SettingsRow(
                            toggle = dev.swarm.phone.ui.PushToggle.FIRST,
                            label = "Alerts",
                            sublabel = "When a session needs you",
                            checked = true,
                            enabled = true,
                            description = "Alerts, on",
                        ),
                    ),
                ),
            ),
            notices = listOf("Notifications are blocked for this app."),
            disclosure = "Battery saver can delay a push.",
            machineSection = MachineSection(
                heading = "PAIRING",
                row = PairedMachineRow(
                    label = "mbp",
                    sublabel = "Replacing revokes this device",
                    replaceLabel = "Replace",
                    replaceConfirmation = "Replace mbp?",
                ),
            ),
            connection = ConnectionSection(
                heading = "CONNECTION",
                machine = MachineRow(
                    name = "mbp",
                    endpoint = "relay",
                    presenceLine = "Online",
                    presenceDescription = null,
                    mark = PresenceMark.ONLINE,
                ),
                health = "The journal has a gap in it.",
                clockNotice = "This phone's clock is ahead.",
                remoteAccess = RemoteAccessRow(
                    title = "Remote access",
                    body = "The machine refuses commands. Turn it back on with swarm remote on.",
                    command = "swarm remote on",
                ),
            ),
            permissionRedirectLabel = "Open notification settings",
            deliveryRedirectLabel = "Open the wake channel",
        ),
        rowFor = { row -> toggle(context, checked = row.checked, description = row.description) },
    )

    private fun sessionDetail(): View = sessionDetailView(
        context = context,
        panel = detailPanel(),
        resync = ctaButton(context, "Fetch what is missing", CtaKind.MORE),
        acknowledge = ctaButton(context, "Clear", CtaKind.MORE),
        // EMPTY, DELIBERATELY: the sheet's own air is the "Approval sheet" destination's claim
        // below (`approvalSheet()`), the same fixture `PhoneSurface.drawApproval` composes into
        // this host when a session is blocked on one. A session detail with nothing pending is
        // the common case, and an empty host sweeps to nothing rather than vacuously.
        approval = FrameLayout(context),
        outcome = "The machine refused: remote control is disabled.",
    )

    /** Every conditional block on the detail screen drawn at once, so nothing is swept vacuously. */
    private fun detailPanel(): SessionDetailPanel = SessionDetailScreen.of(
        dev.swarm.phone.ui.SessionDetail(
            sessionId = "mbp/api",
            online = true,
            journalStale = true,
            stopNotSent = true,
        ),
        TranscriptScreen.of(emptyList()),
        dev.swarm.phone.ui.SessionLease(sessionId = "mbp/api", online = true),
    ).copy(
        transcript = TranscriptPanel(
            heading = "CONVERSATION",
            blocks = listOf(
                TranscriptBlock(itemId = "i1", kind = "message", line = "Running the suite"),
                TranscriptBlock(
                    itemId = "i2",
                    kind = "tool_result",
                    line = "go test ./...",
                    well = "ok  dev.swarm/internal/design  0.4s\nok  dev.swarm/internal/wire  1.1s",
                ),
            ),
            emptyCopy = "Nothing has been said yet.",
        ),
        undeliveredNotice = "4 bytes never reached the machine.",
        undeliveredDetail = "the link dropped",
        offersAcknowledge = true,
        offersResync = true,
    )

    private fun launchForm(): View = launchPanelView(
        context = context,
        panel = LaunchPanel(
            heading = "START A SESSION",
            fields = listOf(
                LaunchField(id = LaunchFieldId.AGENT, hint = "agent", required = true),
                LaunchField(id = LaunchFieldId.CWD, hint = "working directory", required = true),
            ),
            submit = "Launch",
            notice = "The machine refused the launch.",
            noticeDetail = "no such directory",
        ),
        fieldFor = { field -> textField(context, field.name) },
        submit = ctaButton(context, "Launch", CtaKind.APPROVE),
    )

    private fun approvalSheet(): View = approvalSheetView(
        context = context,
        panel = ApprovalSheetPanel(
            contextLine = "MBP / API",
            question = "Run the test suite?",
            command = "go test ./...",
            actions = listOf(ApprovalDecision(id = "accept", label = "Allow")),
            sessionId = "mbp/api",
            itemId = "i-approve-1",
        ),
    )

    private fun pairOnlyOffer(): View = pairOnlyView(
        context = context,
        pairing = View(context),
        started = false,
        onStartPairing = {},
        revokedNotice = "Your machine kept this device registered.",
        revokedDetail = "revoke refused: unknown device",
    )

    private fun pairingStarted(): View = pairOnlyView(
        context = context,
        pairing = pairingPanelView(
            context,
            PairingPanelScreen.of(
                attempt = PairingAttempt(
                    step = PairingStep.SCAN,
                    originShown = "",
                    originIsLocalNetwork = false,
                    explainsInterruptedAttempt = false,
                ),
                scanner = ScannerState.SCANNING,
                sas = null,
                holding = false,
                machine = "",
                relayKnown = true,
            ),
            PairingSlots(
                body = notice(context, "Point the camera at the code on your machine."),
                notice = notice(context, ""),
                destination = notice(context, ""),
                sas = notice(context, ""),
                sasInstruction = notice(context, ""),
                scanner = View(context),
                scanProgress = notice(context, ""),
                controls = PairingControl.entries.associateWith { control ->
                    ctaButton(context, control.name, CtaKind.MORE)
                },
            ),
        ),
        started = true,
        onStartPairing = {},
    )
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
