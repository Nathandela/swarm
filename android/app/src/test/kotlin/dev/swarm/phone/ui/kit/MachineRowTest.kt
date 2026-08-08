package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 11 -- the machine row.
 *
 * WHY THIS COMPONENT EXISTS BESIDE `sessionRow`, since the two look alike and one of them was very
 * nearly used for both. The pixels really are close: a mark, a bold name, a mono identifier in
 * `--p-ink3`, a secondary line under them, on the same `cardSurface`. What differs is the seam.
 * `sessionRow` builds its leading mark by calling `statusDot(context, group)` with a
 * `status.Group`, and a machine's reachability is not one -- so reusing it means passing a Group
 * the server never sent. **A reuse justified by identical pixels is not a reuse justified by a
 * compatible seam.** This row passed the first test and failed the second, and the padding turned
 * out to differ too: row 11 is `space_12` x `space_14` where `.prow` is `space_10` x `space_12`.
 *
 * WHAT IS SHARED IS SHARED PROPERLY. The card is `cardSurface` -- one recipe for `--p-card`, the
 * hairline, the radius and the key light, called by every row in the app -- and the three type
 * roles are the three `.prow` spends, because `.mrow .eid` and `.prow .ag` are the same cell.
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE, for the reason InboxRowTest gives: LEGACY graphics stubs the text stack, which makes
// every font measure fixed-pitch and turns each `is monospace` claim below into its opposite.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MachineRowTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    private fun row(
        endpoint: CharSequence? = "endpoint a3f2",
        mark: PresenceMark = PresenceMark.ONLINE,
    ): ViewGroup = machineRow(
        context = context,
        machine = "nathans-mbp",
        endpoint = endpoint,
        presence = "Your machine is online.",
        mark = mark,
    ) as ViewGroup

    // ---- the card, and the three cells row 11 states -------------------------

    @Test
    fun `the machine row resolves row 11's card, padding and three type roles`() {
        val subject = row()
        val surface = subject.background as? SubstrateSurface
        assertTrue("the row's background is not a kit surface", surface != null)

        val claims = mutableListOf(
            // The card is `.prow`'s, which is what row 11's first three cells say and what
            // `cardSurface` paints for every row in this app.
            Claim("row 11 fill", KitOrigin.cssColour(".prow", "background"), surface!!.spec.fill),
            Claim("row 11 border", KitOrigin.token("--p-hair"), surface.spec.stroke),
            Claim("row 11 padding-y (top)", dimenPx("swarm_space_12"), subject.paddingTop),
            Claim("row 11 padding-y (bottom)", dimenPx("swarm_space_12"), subject.paddingBottom),
            Claim("row 11 padding-x (start)", dimenPx("swarm_space_14"), subject.paddingStart),
            Claim("row 11 padding-x (end)", dimenPx("swarm_space_14"), subject.paddingEnd),
        )
        claims += KitOrigin.textClaims(
            subject.kitRequire(KitTag.MACHINE_NAME) as TextView,
            ".prow .pj",
            // Row 11: `Title.Row` / `--p-ink`. `.prow .pj` declares no colour of its own and
            // inherits `.pscreen { color: var(--p-ink) }`, which is the same token.
            KitOrigin.token("--p-ink"),
            spScale,
        )
        claims += KitOrigin.textClaims(
            subject.kitRequire(KitTag.MACHINE_ENDPOINT) as TextView,
            ".prow .ag",
            KitOrigin.token("--p-ink3"),
            spScale,
        )
        claims += KitOrigin.textClaims(
            subject.kitRequire(KitTag.MACHINE_META) as TextView,
            ".prow .ln",
            KitOrigin.token("--p-ink2"),
            spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 11's two internal gaps, which are what make this a shape rather than a card with text in
     * it: `space_8` between the name and the identifier trailing it, `space_4` between that line
     * and the meta line beneath.
     */
    @Test
    fun `the row spends row 11's two internal gaps and no others`() {
        val subject = row()
        val endpoint = subject.kitRequire(KitTag.MACHINE_ENDPOINT)
        val meta = subject.kitRequire(KitTag.MACHINE_META)

        val claims = listOf(
            Claim(
                "row 11 top-line gap",
                dimenPx("swarm_space_8"),
                (endpoint.layoutParams as LinearLayout.LayoutParams).marginStart,
            ),
            Claim(
                "row 11 meta below",
                dimenPx("swarm_space_4"),
                (meta.layoutParams as LinearLayout.LayoutParams).topMargin,
            ),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * The name takes the slack, so the identifier sits hard right and a long machine name pushes
     * rather than overlaps -- `flex: 1` on `.mrow .name`, which is `.prow .pj`'s arrangement.
     */
    @Test
    fun `the name takes the slack so the identifier is pushed to the trailing edge`() {
        val name = row().kitRequire(KitTag.MACHINE_NAME).layoutParams as LinearLayout.LayoutParams
        assertEquals(0, name.width)
        assertEquals(1f, name.weight, 0.001f)
    }

    // ---- the mark ------------------------------------------------------------

    /**
     * THE ROW'S LEADING MARK IS A PRESENCE DOT AND NOT A STATUS DOT, which is the whole reason this
     * component exists rather than a call to `sessionRow`. The two are the same drawable at the
     * same 7 dp, so nothing about the rendered result tells them apart -- what is asserted is which
     * factory drew it, because the wrong one requires a `status.Group` the server never sent.
     */
    @Test
    fun `the leading mark is the presence dot, in the state the row was given`() {
        // The loop used to be `listOf(true, false)` over a `Boolean`, and it asserted
        //
        //	KitOrigin.token(if (online) "--p-ok" else "--p-ink3")
        //
        // -- two states, because the factory had two. The maquette draws three (`.pdot.unknown`,
        // a hollow ring) and ADR-009 D2 makes it normative, so the third is exercised here rather
        // than folded onto the second: what a row must never do is render "we have no record"
        // with the same mark as "asleep", and a two-value loop cannot see that.
        PresenceMark.entries.forEach { mark ->
            val subject = row(mark = mark)
            val dot = subject.kitRequire(KitTag.PRESENCE_DOT)
            assertNull(
                "a `.pdot` is on the machine row. The status dot takes a `status.Group`, and " +
                    "machine presence is not one -- the Group whose colour happens to match " +
                    "renders this correctly while inventing a state nothing on the wire sent",
                subject.kitFind(KitTag.DOT),
            )
            val drawn = dot.background as StatusDotDrawable
            assertEquals(
                "the mark is not the colour the maquette gives a machine that is $mark",
                KitOrigin.maquetteColour(
                    ".pdot.${mark.name.lowercase()}",
                    if (mark == PresenceMark.UNKNOWN) "border" else "background",
                ),
                drawn.fill,
            )
            assertEquals(
                "the $mark mark is drawn as a ${if (drawn.strokePx > 0f) "ring" else "disc"}, " +
                    "which is not what `.pdot.${mark.name.lowercase()}` draws",
                mark == PresenceMark.UNKNOWN,
                drawn.strokePx > 0f,
            )
        }
    }

    // ---- the cell with no source --------------------------------------------

    /**
     * The identifier is OPTIONAL, and that is this file's decision rather than row 11's.
     *
     * The row states the cell (`endpoint id`, `Mono.Agent` / `--p-ink3`) and the product has one
     * identifier for a machine rather than two, so the one caller this component has passes null --
     * see `MachinesPanel`, which argues it. An empty string would leave a zero-width TextView in
     * the line and a gap in front of it; null renders no view at all, so the name simply runs to
     * the row's trailing edge.
     */
    @Test
    fun `a row with no identifier renders no identifier cell at all`() {
        val subject = row(endpoint = null)
        assertNull(
            "the row rendered an identifier cell for a machine that has no second identifier, so " +
                "the line carries a blank view and the gap in front of it",
            subject.kitFind(KitTag.MACHINE_ENDPOINT),
        )
        assertTrue(
            "dropping the identifier dropped the name with it",
            subject.kitFind(KitTag.MACHINE_NAME) != null,
        )
    }

    // ---- PB-DS-10's control ---------------------------------------------------

    @Test
    fun `the comparison fails when a value diverges`() {
        val subject = row()
        val surface = subject.background as SubstrateSurface

        assertTrue(
            "a perturbed fill produced no mismatch, so the card claims hold against any colour",
            mismatches(listOf(Claim("row 11 fill", surface.spec.fill + 1, surface.spec.fill)))
                .isNotEmpty(),
        )
        assertTrue(
            "a perturbed padding produced no mismatch, so the row could spend any step",
            mismatches(
                listOf(Claim("row 11 padding", subject.paddingTop + 1, subject.paddingTop)),
            ).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("row 11 fill", surface.spec.fill, surface.spec.fill))).isEmpty(),
        )
    }

    /** A component that painted the attention variant would be claiming the row needs the user. */
    @Test
    fun `the machine row is never the attention variant`() {
        val surface = row().background as SubstrateSurface
        assertNull(
            "the machine row carries `.prow.attention`'s rail. That mark means this row is " +
                "blocked on the human, and a machine is not blocked on anyone",
            surface.spec.rail,
        )
        assertNotEquals(
            "the machine row's border is the warmed attention hairline rather than `--p-hair`",
            Kit.attentionBorder(context),
            surface.spec.stroke,
        )
    }
}
