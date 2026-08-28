package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.text.style.TextAppearanceSpan
import android.util.TypedValue
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.theme.TypeScale
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation table row 14 -- the activity row.
 *
 * WHAT RESOLVES FROM THE ORIGIN. Row 14 states four surface values that are `.prow`'s to the
 * digit, so the claims below are computed from `.prow`'s own CSS rather than from a number copied
 * out of the table -- which makes them the assertion that the reuse actually happened as well as
 * the assertion that the values are right. The three type roles resolve the same way: [TypeScale]
 * follows `type.xml`'s `origin:` comments to `.sheet2 .ctx` (`Mono.Meta`), `.m2` (`Body.Message`)
 * and `.prow .ln b` (`Mono.InlineStrong`) and reads the size, the tracking and the family out of
 * the design source. The two padding steps are joined to row 14 by `s23DerivedSpacing` in the Go
 * lane, which reads the table; what is asserted here is that they reach the VIEW off the resource
 * table Android actually merges.
 *
 * **THE TWO ASSERTIONS THAT MATTER ARE ABOUT A COLUMN THAT IS NOT THERE AND A FACE THAT IS.**
 *
 * The first is the timestamp gutter. Row 14 makes the column wrap-content rather than the mock's
 * fixed 52 dp, because a fixed column clips at the 1.3x font scale PB-DS-12 requires. The
 * plausible wrong answer is a component that reserves the column anyway and renders it blank --
 * which looks identical in a screenshot that has a timestamp, and wrong in the only case this
 * product actually has, where there is no time on the wire at all. `the row with no timestamp has
 * no timestamp cell` is what holds it.
 *
 * The second is the emphasis, and row 14 is the reason it needs holding. **Both inks are
 * `--p-ink`** -- the body's and the emphasis's -- so nothing about the COLOUR distinguishes the
 * marked span, and what does is the face: `Mono.InlineStrong` at 600 against `Body.Message`'s sans
 * at 400. A span that carried the ink and not the appearance would therefore be invisible, and
 * every colour assertion in this file would still pass. `the emphasis is set in a face the body is
 * not` is the half that catches it, and it is the one that would have been easy not to write,
 * because the ink claim looks like it is doing the work.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ActivityRowTest {

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

    /** The one caller this component has passes exactly this: a body, a Group, and no time. */
    private val body = "group_transition · needs_input"
    private val emphasis = "needs_input"

    private fun row(
        body: CharSequence = this.body,
        emphasis: CharSequence? = this.emphasis,
        timestamp: CharSequence? = null,
    ) = activityRow(context, body = body, emphasis = emphasis, timestamp = timestamp)

    private fun bodyView(row: LinearLayout) = row.kitRequire(KitTag.ACTIVITY_BODY) as TextView

    /**
     * A row with no emphasis holds a plain `String`, not a [Spanned] -- so this returns null
     * rather than casting. An `as Spanned` here would turn "the component applied no span", which
     * is a state this component has and one test below asserts, into a ClassCastException.
     */
    private fun spanned(row: LinearLayout): Spanned? = bodyView(row).text as? Spanned

    private inline fun <reified T> spansOf(row: LinearLayout): List<T> {
        val text = spanned(row) ?: return emptyList()
        return text.getSpans(0, text.length, T::class.java).toList()
    }

    // ---- the card is `.prow`'s, which is row 14's first four cells --------------

    /**
     * AUTHORIZED REWRITE, ADR-020 D2 (2026-08-27, wave W4). Row 14's padding is the Slate slab's
     * `space_12` x `space_16` now, and no longer the session row's. What the test was called and
     * what its four padding claims said before:
     *
     *     fun `the row is the session row's card and the session row's padding`() {
     *     Claim("row 14 padding-x start", dimenPx("swarm_space_12"), subject.paddingStart),
     *     Claim("row 14 padding-x end", dimenPx("swarm_space_12"), subject.paddingEnd),
     *     Claim("row 14 padding-y top", dimenPx("swarm_space_10"), subject.paddingTop),
     *     Claim("row 14 padding-y bottom", dimenPx("swarm_space_10"), subject.paddingBottom),
     */
    @Test
    fun `the row is the session row's card and the Slate slab's padding`() {
        val subject = row()
        val surface = subject.background as SubstrateSurface

        val claims = listOf(
            Claim("`.prow` fill", KitOrigin.cssColour(".prow", "background"), surface.spec.fill),
            Claim("`.prow` hairline", KitOrigin.cssColour(".prow", "border"), surface.spec.stroke),
            Claim("row 14 padding-x start", dimenPx("swarm_space_16"), subject.paddingStart),
            Claim("row 14 padding-x end", dimenPx("swarm_space_16"), subject.paddingEnd),
            Claim("row 14 padding-y top", dimenPx("swarm_space_12"), subject.paddingTop),
            Claim("row 14 padding-y bottom", dimenPx("swarm_space_12"), subject.paddingBottom),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 14's surface, border, radius and key-light cells, asserted as the identity they are:
     * four values that read the same as `.prow`'s.
     *
     * THE EXPECTED VALUE IS THE SESSION ROW'S OWN SURFACE, and that is what makes this the reuse
     * claim rather than a fourth transcription of four numbers. `.prow`'s fill and hairline are
     * checked against the design source above and again in `InboxRowTest`; what is checked HERE is
     * that the activity row did not build a second recipe that happens to agree today -- radius,
     * key light, stroke width and all -- which is the drift §2's reuse rule exists to prevent.
     *
     * The key light is the value inside it that a plausible wrong implementation loses: a row
     * built from a plain `GradientDrawable` gets the fill, the hairline and the radius right and
     * silently drops the one depth cue Substrate allows, and no screenshot says which it is.
     */
    @Test
    fun `the row's card IS the session row's card, not a second recipe for it`() {
        val subject = (row().background as SubstrateSurface).spec
        val prow = (
            sessionRow(context, "quanthome/api", "claude", "Wants to run something", "completed", lit = false, promoted = false)
                .background as SubstrateSurface
            ).spec

        assertEquals(prow, subject)
        assertNotNull(
            "the card has no key light. Substrate bans drop shadows, so this inset highlight is " +
                "the row's only depth cue and a null here is a flat card",
            subject.keyLight,
        )
        assertNull(
            "the activity row grew `.prow.attention`'s rail. That rail means \"this row needs " +
                "you\", and a line of history needs nobody",
            subject.rail,
        )
    }

    // ---- the three type roles --------------------------------------------------

    /**
     * Row 14's body cell: `Body.Message` / `--p-ink`.
     *
     * THE PLAUSIBLE WRONG ANSWER IS `--p-ink2`, and it is plausible because it is what the nearest
     * neighbour does: `.prow .ln` -- the session row's need line, which is the rule this app's
     * `Body.Secondary` descends from -- is `--p-ink2`, and a body line under a heading is
     * de-emphasised almost everywhere else in this kit. Row 14 says primary, and the reason is
     * structural: on the session row that line is subordinate to a project name above it, and on
     * this row the sentence IS the row.
     */
    @Test
    fun `the body is row 14's role in row 14's ink`() {
        val claims = KitOrigin.textClaims(
            // `.m2` is what type.xml records as Body.Message's origin.
            view = bodyView(row()),
            selector = ".m2",
            ink = KitOrigin.token("--p-ink"),
            spScale = spScale,
        )
        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the body is `--p-ink2`, which is what `.prow .ln` takes one screen over and what a " +
                "body line takes almost everywhere else in this kit. Row 14 says `--p-ink`: this " +
                "sentence is the row, not a second line under something else",
            KitOrigin.token("--p-ink2"),
            bodyView(row()).currentTextColor,
        )
    }

    @Test
    fun `the timestamp is row 14's role in row 14's ink`() {
        val cell = row(timestamp = "09:38").kitRequire(KitTag.ACTIVITY_TIME) as TextView
        val claims = KitOrigin.textClaims(
            // `.sheet2 .ctx` is what type.xml records as Mono.Meta's origin.
            view = cell,
            selector = ".sheet2 .ctx",
            ink = KitOrigin.token("--p-ink3"),
            spScale = spScale,
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * `Mono.InlineStrong` / `--p-ink`, over the exact range of the sentence the caller named.
     *
     * The span is asserted rather than a second TextView because that is what row 14 specifies --
     * an INLINE emphasis inside the body, which is `.prow .ln b` one screen over -- and the range
     * matters as much as the styling: a span applied over the whole body would style the right
     * row and read as one with no emphasis at all.
     */
    @Test
    // The name carries no `.` on purpose: a backticked Kotlin identifier is a JVM name, and a dot
    // in one is a compile error rather than a style problem. The selector it refers to is in the
    // KDoc above, where it costs nothing.
    fun `the emphasis is the ln b span, over the words the caller named`() {
        val subject = row()
        val text = requireNotNull(spanned(subject)) {
            "the body holds a plain String, so the emphasis was never applied and every claim " +
                "below would be about nothing"
        }
        // The rendered spec, not the design's, for the reason ADR-012 phase 2 gives: a size claim
        // is about the rung a style stands on. `.prow .ln b` happens not to have moved -- 11.5px
        // in the design, 11.5 sp on the code rung -- and reading the design px here would be
        // right by coincidence and wrong on the next ruling.
        val spec = TypeScale.renderedSpec(".prow .ln b")
        val appearance = spansOf<TextAppearanceSpan>(subject).single()
        val ink = spansOf<ForegroundColorSpan>(subject).single()

        val start = body.indexOf(emphasis)
        val claims = listOf(
            Claim("`.ln b` appearance start", start, text.getSpanStart(appearance)),
            Claim("`.ln b` appearance end", start + emphasis.length, text.getSpanEnd(appearance)),
            Claim("`.ln b` ink start", start, text.getSpanStart(ink)),
            Claim("`.ln b` ink end", start + emphasis.length, text.getSpanEnd(ink)),
            Claim(
                "`.prow .ln b` size",
                KitOrigin.quantisedTextSize(spec.sizePx * spScale).roundToInt(),
                appearance.textSize,
            ),
            // ADR-009 D7: the span's face is asked as PITCH rather than as a family string.
            // TextAppearanceSpan.getFamily() returns null once the style names a bundled family,
            // because the platform resolved the resource instead of carrying a name; see
            // KitOrigin.isFixedPitch(TextAppearanceSpan).
            Claim("`.prow .ln b` family", spec.isMono, KitOrigin.isFixedPitch(appearance)),
            Claim("`.prow .ln b` ink", KitOrigin.token("--p-ink"), ink.foregroundColor),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * WHAT ACTUALLY MAKES THE EMPHASIS AN EMPHASIS, given that row 14 inks it the same as the body.
     *
     * Every colour claim above passes for a component that applied the [ForegroundColorSpan] and
     * dropped the [TextAppearanceSpan] -- `--p-ink` over `--p-ink` is a span nobody can see. The
     * face is the whole of the distinction, so it is asserted as a DIFFERENCE between the two
     * rather than as a property of one: the body is `Body.Message` sans, the marked span is
     * `Mono.InlineStrong` mono, and the design says so in both directions.
     */
    @Test
    fun `the emphasis is set in a face the body is not`() {
        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )

        val claims = listOf(
            Claim(
                "`.prow .ln b` is monospace",
                TypeScale.MONO_FAMILY,
                TypeScale.designSpec(".prow .ln b").androidFamily,
            ),
            Claim("`.m2` is not monospace", false, TypeScale.designSpec(".m2").isMono),
            Claim(
                "the emphasis span carries the mono family",
                true,
                KitOrigin.isFixedPitch(spansOf<TextAppearanceSpan>(row()).single()),
            ),
            Claim("the body renders proportional", false, KitOrigin.isFixedPitch(bodyView(row()).paint)),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `a row with no emphasis carries no span at all`() {
        // The plausible wrong answer is a zero-length span, or one over the whole sentence. Both
        // render identically to no span today and stop doing so the moment the ink changes.
        val subject = row(body = "presence", emphasis = null)
        assertEquals(emptyList<TextAppearanceSpan>(), spansOf<TextAppearanceSpan>(subject))
        assertEquals(emptyList<ForegroundColorSpan>(), spansOf<ForegroundColorSpan>(subject))
        assertEquals("presence", bodyView(subject).text.toString())
    }

    /**
     * A named emphasis that is not in the sentence is a copy bug, and it fails loudly.
     *
     * Dropping the emphasis instead would render a row that looks finished and has quietly lost
     * the one word the design puts the eye on -- which is the class of failure nobody reports,
     * because there is nothing on screen to report.
     */
    @Test(expected = IllegalStateException::class)
    fun `emphasising a span the sentence does not contain fails loudly`() {
        row(body = "launched", emphasis = "needs_input")
    }

    // ---- the dropped gutter ----------------------------------------------------

    /**
     * Row 14's substantive decision, and the one this product depends on.
     *
     * `swarmmobile.JournalEntry` carries no timestamp -- `internal/journal.Record` has a `TS` and
     * the wire form drops it -- so every row this app renders today passes none. A component that
     * reserved the mock's 52 dp column would show a blank gutter on every row of the only activity
     * screen that exists.
     */
    @Test
    fun `the row with no timestamp has no timestamp cell`() {
        assertNull(
            "the row built a timestamp view for a caller that supplied no timestamp. Row 14 makes " +
                "that column wrap-content rather than the mock's fixed gutter, and the journal " +
                "carries no time at all -- so this is an empty column on every row in the app",
            row(timestamp = null).kitFind(KitTag.ACTIVITY_TIME),
        )
        assertEquals(
            "a row with no timestamp is not a row with one child",
            1,
            row(timestamp = null).childCount,
        )
    }

    @Test
    fun `the timestamp column is wrap-content and the body takes the slack`() {
        val subject = row(timestamp = "09:38")
        val time = subject.kitRequire(KitTag.ACTIVITY_TIME).layoutParams as LinearLayout.LayoutParams
        val text = bodyView(subject).layoutParams as LinearLayout.LayoutParams

        assertEquals(
            "the timestamp column has a fixed width. Row 14: a fixed column clips at the 1.3x " +
                "font scale PB-DS-12 requires",
            LinearLayout.LayoutParams.WRAP_CONTENT,
            time.width,
        )
        assertEquals(
            "the body does not take the row's slack, so a long sentence pushes the row wider " +
                "than the screen instead of wrapping inside it",
            0,
            text.width,
        )
        assertEquals(1f, text.weight, 0.001f)
    }

    @Test
    fun `the timestamp and the body are separated by row 14's gap and nothing else`() {
        val subject = row(timestamp = "09:38")
        val time = subject.kitRequire(KitTag.ACTIVITY_TIME).layoutParams as LinearLayout.LayoutParams
        val text = bodyView(subject).layoutParams as LinearLayout.LayoutParams
        val claims = listOf(
            // CSS `gap` puts space BETWEEN children and nowhere else, which is what KitStack is
            // for: a per-child margin would indent the timestamp from the card's own padding.
            Claim("row 14 gap, before the timestamp", 0, time.marginStart),
            Claim("row 14 gap, between the two", dimenPx("swarm_space_10"), text.marginStart),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * PB-DS-10's control, fed to the SAME function every assertion above calls.
     */
    @Test
    fun `the comparison fails when a value diverges`() {
        val subject = row()
        val ink = spansOf<ForegroundColorSpan>(subject).single()

        assertTrue(
            "a perturbed emphasis ink produced no mismatch, so the span claim above would hold " +
                "against an emphasis painted in any colour at all",
            mismatches(
                listOf(Claim("`.ln b` ink", ink.foregroundColor + 1, ink.foregroundColor)),
            ).isNotEmpty(),
        )
        assertTrue(
            "a perturbed padding produced no mismatch, so the geometry claims are about nothing",
            mismatches(
                listOf(Claim("row 14 padding", subject.paddingTop + 1, subject.paddingTop)),
            ).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("row 14 padding", subject.paddingTop, subject.paddingTop)))
                .isEmpty(),
        )
    }
}
