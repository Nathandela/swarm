package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.Claim
import dev.swarm.phone.ui.kit.KitOrigin
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.StatusDotDrawable
import dev.swarm.phone.ui.kit.SubstrateSurface
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import dev.swarm.phone.ui.kit.mismatches
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the triage inbox AS DRAWN.
 *
 * WHAT THIS SUITE IS FOR AND WHAT `android/gate/s24_screens_test.go` CANNOT DO. The Go gate reads
 * SOURCES: it can say the screen imports the kit, that it names no colour of its own, and that its
 * copy tables cover every Group. It cannot say the screen actually PUTS those components on
 * screen, in the recorded order, carrying the recorded strings -- and "the model is beautiful and
 * nothing renders it" is precisely the defect this slice exists to close. PB-DS-6 was recorded NOT
 * MET over a kit with twelve tested components and zero call sites; a suite that asserted the
 * model harder would have reproduced that exactly.
 *
 * EVERY APPEARANCE EXPECTATION COMES FROM THE ORIGIN. [KitOrigin] resolves the design artifact and
 * the checked-in `group-tokens.tsv` join; nothing below is compared against a constant this file
 * records or against the kit's own opinion. That is not general caution -- it is the specific way
 * `colors.xml` once drifted into a third palette with its own test green.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED HERE: the metrics of the components themselves. The row's
 * padding, the dot's halo geometry, the chip's fill and the label's tracking are PB-DS-10's and
 * are asserted in `ui/kit/`. Repeating them here would be a second opinion that can disagree with
 * the first. What is asserted is what only a SCREEN can get wrong: which component is used, in
 * what order, with which Group and which words.
 */
@RunWith(RobolectricTestRunner::class)
class TriageInboxViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun row(id: String, group: String, need: String = "doing something", present: Boolean = true) =
        SessionRow(id = id, title = id.substringAfter('/'), group = group, need = need, present = present, agent = "claude")

    private fun screen(
        rows: List<SessionRow>,
        stale: Boolean = false,
        scope: String? = null,
        selected: String? = null,
    ) = TriageInboxScreen.of(
        inbox = TriageInbox.from(rows, journalStale = stale),
        scope = scope,
        selectedSession = selected,
    )

    private fun view(
        rows: List<SessionRow>,
        stale: Boolean = false,
        scope: String? = null,
        selected: String? = null,
        onSelectSession: (String) -> Unit = {},
        onSelectScope: (String?) -> Unit = {},
    ): View = triageInboxView(
        context = context,
        screen = screen(rows, stale, scope, selected),
        onSelectSession = onSelectSession,
        onSelectScope = onSelectScope,
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

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the composition --------------------------------------------------

    @Test
    fun `the inbox is composed of the components its recorded composition names`() {
        val root = view(listOf(row("mbp/one", "needs_input"), row("mbp/two", "working")))

        // Inventory C1's four parts, in its order. Each is found by the tag the screen puts on the
        // component it got back from the kit, never by child index -- an assertion that read
        // `getChildAt(0).getChildAt(1)` starts checking a different view the day a component
        // gains a child, silently.
        listOf(
            InboxTag.NAV to "C1.1 `.pnav` -- the title and the live counter",
            InboxTag.SCOPES to "C1.2 `.chips` -- the scope bar",
            InboxTag.SECTION_LABEL to "C1.3 `.plabel` -- one heading per Group",
            InboxTag.SECTION_ROWS to "C1.3 `.prows` -- the rows' container",
        ).forEach { (tag, what) ->
            assertNotNull("the inbox renders nothing for $what", root.kitFind(tag))
        }
    }

    /**
     * THE TAB BAR HALF OF THIS ASSERTION MOVED TO `PhoneScaffoldViewTest`, unchanged, along with
     * the bar itself. The bar is the window's now -- one bar under whichever of the four
     * destinations is on screen -- so "the tab bar is the last thing on screen" is a statement
     * about the scaffold's composition, and asserting it here would ask the inbox to render
     * something it no longer owns. The scaffold's suite walks this screen's own tags beside its
     * own, so the two halves are still one statement about one screen.
     */
    @Test
    fun `the composition is in the recorded order`() {
        val root = view(listOf(row("mbp/one", "working")))
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in InboxTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        assertEquals(
            "the inbox's first two elements are not the nav header and the scope bar",
            listOf(InboxTag.NAV, InboxTag.SCOPES),
            order.take(2),
        )
    }

    @Test
    fun `every group renders a heading, in the model's order, empty or not`() {
        val root = view(listOf(row("mbp/one", "working")))

        assertEquals(
            "the headings on screen are not TriageInbox.TRIAGE_ORDER's four -- an empty section " +
                "was dropped, which is the failure PB-DS-9 names by name",
            TriageInbox.TRIAGE_ORDER.map { TriageInboxScreen.headingFor(it) },
            root.allTagged(InboxTag.SECTION_LABEL).map { textOf(it) },
        )
    }

    @Test
    fun `an empty section renders its own copy under its own heading`() {
        val root = view(listOf(row("mbp/one", "working")))
        val empties = root.allTagged(InboxTag.SECTION_EMPTY).map { textOf(it) }

        assertEquals(
            "an empty section put nothing on screen where its copy belongs, so it reads as a " +
                "heading over nothing",
            TriageInbox.TRIAGE_ORDER.filter { it != "working" }
                .map { TriageInboxScreen.emptyCopyFor(it) },
            empties,
        )
    }

    @Test
    fun `a section with rows renders one row per session and no empty block`() {
        val root = view(
            listOf(
                row("mbp/one", "working", need = "writing pairing tests"),
                row("mbp/two", "working", need = "refactoring auth middleware"),
            ),
        )
        val rows = root.allTagged(InboxTag.ROW)

        assertEquals(2, rows.size)
        assertEquals(
            listOf("writing pairing tests", "refactoring auth middleware"),
            rows.map { textOf(it.kitRequire(KitTag.NEED)) },
        )
        assertEquals(
            listOf("one", "two"),
            rows.map { textOf(it.kitRequire(KitTag.PROJECT)) },
        )
        assertEquals(
            "the section holding both rows also drew its empty copy, so a populated section " +
                "tells the user it has nothing in it",
            TriageInbox.TRIAGE_ORDER.size - 1,
            root.allTagged(InboxTag.SECTION_EMPTY).size,
        )
    }

    // ---- the four-Group identity ------------------------------------------

    @Test
    fun `each row's dot is the colour the origin binds to its own section's group`() {
        // The bug this catches is the plausible one: a screen that passes the FIRST group, or a
        // constant group, to every row it builds. Every section then renders correctly-shaped
        // rows in one colour, and every kit test stays green because the kit was asked for
        // exactly what it drew.
        val root = view(
            TriageInbox.TRIAGE_ORDER.map { group -> row("mbp/$group", group) },
        )
        val claims = root.allTagged(InboxTag.ROW).mapIndexed { index, view ->
            val group = TriageInbox.TRIAGE_ORDER[index]
            Claim(
                "the $group row's `.pdot` fill",
                KitOrigin.token(KitOrigin.groupToken(group)),
                (view.kitRequire(KitTag.DOT).background as StatusDotDrawable).fill,
            )
        }

        assertEquals(4, claims.size)
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `the group assertion can actually fail`() {
        // The negative control PB-DS-10 requires, through the SAME comparison the assertion above
        // uses: one unit of colour, which is the smallest divergence that matters.
        val att = KitOrigin.token(KitOrigin.groupToken("needs_input"))

        assertTrue(
            "a dot one unit away from the origin's colour passes the comparison, so the claim " +
                "above is green over a screen nobody checked",
            mismatches(listOf(Claim("`.pdot` fill", att, att + 1))).isNotEmpty(),
        )
    }

    @Test
    fun `the needs_input row is the attention variant and no other row is`() {
        val root = view(TriageInbox.TRIAGE_ORDER.map { group -> row("mbp/$group", group) })
        val rails = root.allTagged(InboxTag.ROW).mapIndexed { index, view ->
            TriageInbox.TRIAGE_ORDER[index] to ((view.background as SubstrateSurface).spec.rail != null)
        }.toMap()

        // `.prow.attention` says the row is blocked on the human three times over -- the rail, the
        // warmed border and the dot's glow -- and it is the point of the component. A screen that
        // renders every row identically loses the whole distinction the four Groups exist for.
        assertEquals(
            mapOf(
                "needs_input" to true,
                "ready_for_review" to false,
                "completed" to false,
                "working" to false,
            ),
            rails,
        )
    }

    /**
     * ADR-009 D4's promoted slab reaches the screen, and it reaches it FROM THE MODEL.
     *
     * THE STATE IS THE REAL RESOLVER'S, WHICH IS THE WHOLE POINT OF ASSERTING IT HERE. `KitFoundationTest`
     * already asks whether `cardSurface(attention = true)` paints the maquette's `.slab.lit`; what
     * only a screen can get wrong is WHICH row gets it, and a test that hand-fed `lit = true` to
     * one row and asserted it came out promoted would be certifying that the renderer reads its
     * argument. So the rows go in as the wire describes them, through `TriageInbox.from` and
     * `TriageInboxScreen.of`, and the expectation for each drawn row is read off the model's own
     * answer rather than off the Group string this file typed.
     *
     * BOTH SURFACES ARE READ FROM THE MAQUETTE. `.slab` and `.slab.lit` are two rules in ADR-009
     * D2's normative source; naming `--p-card` and `--p-elev` here would be this suite agreeing
     * with the kit about a design neither of them read.
     */
    @Test
    fun `the row the model marks promoted is the one drawn on the lit slab`() {
        val sessions = TriageInbox.TRIAGE_ORDER.map { group -> row("mbp/$group", group) }
        val model = screen(sessions).sections.flatMap { it.rows }
        val drawn = view(sessions).allTagged(InboxTag.ROW)

        assertEquals(
            "the screen drew a different number of rows from the number the model holds, so the " +
                "pairing below is comparing a row against some other row's answer",
            model.size,
            drawn.size,
        )
        val claims = model.zip(drawn).map { (row, view) ->
            Claim(
                "the ${row.group} row's slab",
                if (row.lit) {
                    KitOrigin.maquetteColour(".slab.lit", "background")
                } else {
                    KitOrigin.maquetteColour(".slab", "background")
                },
                (view.background as SubstrateSurface).spec.fill,
            )
        }
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))

        // AND THE MODEL MUST ANSWER BOTH WAYS OVER THIS ROSTER, or every claim above holds over
        // one expectation repeated -- a model that promoted nothing, or everything, would satisfy
        // each equality while drawing an inbox with no promotion in it at all.
        assertTrue("the model promoted no row at all", model.any { it.lit })
        assertTrue("the model promoted every row", model.any { !it.lit })
    }

    @Test
    fun `only a working row carries the workbar`() {
        val root = view(TriageInbox.TRIAGE_ORDER.map { group -> row("mbp/$group", group) })
        val bars = root.allTagged(InboxTag.ROW).mapIndexed { index, view ->
            TriageInbox.TRIAGE_ORDER[index] to (view.kitFind(KitTag.WORKBAR) != null)
        }.toMap()

        assertEquals(
            mapOf(
                "needs_input" to false,
                "ready_for_review" to false,
                "completed" to false,
                "working" to true,
            ),
            bars,
        )
    }

    @Test
    fun `the dot announces the state it is the only carrier of`() {
        val root = view(listOf(row("mbp/one", "needs_input")))
        val dot = root.allTagged(InboxTag.ROW).single().kitRequire(KitTag.DOT)

        assertEquals("Needs you", dot.contentDescription)
    }

    // ---- the chrome -------------------------------------------------------

    @Test
    fun `the nav header carries the title and the live counter`() {
        val root = view(listOf(row("mbp/one", "working"), row("mbp/two", "needs_input")))
        val nav = root.kitRequire(InboxTag.NAV)

        assertEquals("Inbox", textOf(nav.kitRequire(KitTag.TITLE)))
        assertEquals("2 LIVE", textOf(nav.kitRequire(KitTag.LIVE)))
    }

    @Test
    fun `nothing in flight leaves the counter off the header entirely`() {
        val root = view(listOf(row("mbp/one", "completed")))

        assertNull(
            "a `0 LIVE` counter was drawn, which is an in-flight readout about nothing",
            root.kitRequire(InboxTag.NAV).kitFind(KitTag.LIVE),
        )
    }

    // `the tab badge appears only when a session needs the user` MOVED TO PhoneScaffoldViewTest,
    // verbatim. The COUNT is still this screen's -- `InboxScreen.tabs` carries it and
    // `TriageInboxScreen` decides the wording -- and the bar that draws it is the scaffold's, so
    // the assertion belongs where the badge is rendered.

    @Test
    fun `the scope bar draws every machine in the roster`() {
        val root = view(
            listOf(row("nathans-mbp/one", "working"), row("mac-studio/two", "working")),
        )
        val chips = (root.kitRequire(InboxTag.SCOPES) as ViewGroup)

        assertEquals(
            listOf("All machines", "mac-studio", "nathans-mbp"),
            (0 until chips.childCount).map { textOf(chips.getChildAt(it)) },
        )
    }

    // ---- the screen is not a picture --------------------------------------

    @Test
    fun `tapping a row selects that session and not the one above it`() {
        var chosen: String? = null
        val root = view(
            listOf(row("mbp/one", "working"), row("mbp/two", "working")),
            onSelectSession = { chosen = it },
        )

        root.allTagged(InboxTag.ROW)[1].performClick()

        assertEquals(
            "the row's tap handler was built from a captured variable rather than from its own " +
                "row, so every row selects the same session",
            "mbp/two",
            chosen,
        )
    }

    @Test
    fun `tapping a scope chip narrows to that machine, and All machines clears it`() {
        var chosen: String? = "unset"
        val root = view(
            listOf(row("nathans-mbp/one", "working"), row("mac-studio/two", "working")),
            onSelectScope = { chosen = it },
        )
        val chips = root.kitRequire(InboxTag.SCOPES) as ViewGroup

        chips.getChildAt(1).performClick()
        assertEquals("mac-studio", chosen)

        chips.getChildAt(0).performClick()
        assertNull("the All machines chip must clear the scope, not select a machine named it", chosen)
    }
}
