package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.text.style.TextAppearanceSpan
import android.util.TypedValue
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 12 -- the kill-switch panel.
 *
 * THE TWO CLAIMS THIS SUITE EXISTS FOR are the ones a reviewer cannot check by looking:
 *
 * **The border is a BLEND and not a token.** Row 12 asks for `color-mix(--p-err 36%, --p-hair)`,
 * which is `.prow.attention`'s recipe with one token substituted -- so the expected value is
 * computed here from the two tokens and the share the ARTIFACT declares for the attention row,
 * never from `Kit`'s own arithmetic. `--p-err` straight would be the obvious wrong answer and it
 * would look almost right.
 *
 * **There is no trailing control, and there is no way to add one.** Row 12's amendment deletes the
 * toggle because `App.KillSwitchEngaged` is read-only by design; the component has no parameter for
 * one, so what is asserted is the child count -- a panel that grew a third child grew something the
 * signature cannot express, which means someone added it here.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class KillSwitchPanelTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private val density: Float get() = context.resources.displayMetrics.density

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    private fun dimenPx(name: String): Int = dimen(name).roundToInt()

    private val title = "Remote access"
    private val command = "swarm remote off"
    private val body = "Remote control is switched on at your machine. The switch lives on the " +
        "machine: $command."

    private fun panel(command: CharSequence? = this.command): ViewGroup =
        killSwitchPanel(context, title = title, body = body, command = command) as ViewGroup

    private fun bodyView(panel: ViewGroup): TextView =
        panel.kitRequire(KitTag.KILL_BODY) as TextView

    private inline fun <reified T> spansOf(panel: ViewGroup): List<T> {
        val text = bodyView(panel).text as? Spanned ?: return emptyList()
        return text.getSpans(0, text.length, T::class.java).toList()
    }

    // ---- the charged container -----------------------------------------------

    /**
     * Row 12's first three cells: no fill, the substituted border mix, `--p-card-r`.
     *
     * THE SHARE IS READ OUT OF THE ARTIFACT rather than out of the kit. `.prow.attention`'s own
     * `border-color` declares the 36%, row 12's words are that its border is that recipe with
     * `--p-err` substituted, and [KitOrigin.overOpaque] is an independent implementation of the
     * mix -- so nothing in this claim comes from the code it is checking.
     */
    @Test
    fun `the panel is a border on the ground, in the attention recipe with err substituted`() {
        val subject = panel()
        val surface = subject.background as? SubstrateSurface
        assertTrue("the panel's background is not a kit surface", surface != null)

        val share = KitOrigin.cssPercent(".prow.attention", "border-color")
        val claims = listOf(
            Claim(
                "row 12 border",
                KitOrigin.overOpaque(KitOrigin.token("--p-err"), share, KitOrigin.token("--p-hair")),
                surface!!.spec.stroke,
            ),
            // `.prow { border: 1px solid var(--p-hair) }` -- the hairline every surface in this
            // kit spends, read out of the design rather than off the resource it becomes.
            Claim(
                "row 12 border width",
                (KitOrigin.cssFirstPx(".prow", "border") * density).roundToInt(),
                surface.spec.strokeWidthPx,
            ),
            Claim("row 12 radius", dimen("swarm_radius_card"), surface.spec.radiusPx),
            Claim("row 12 key light", null, surface.spec.keyLight),
            Claim("row 12 rail", null, surface.spec.rail),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "the border is `--p-err` straight. Row 12 mixes it 36% into `--p-hair` -- the same " +
                "recipe the attention row uses one token over -- and the undiluted token is a " +
                "louder line than any border in this skin",
            KitOrigin.token("--p-err"),
            surface.spec.stroke,
        )
    }

    /**
     * Row 12's surface cell is `none (the ground shows)`, which is the cell most likely to be
     * quietly upgraded to a card: every other container in this kit has a fill.
     */
    @Test
    fun `the panel paints no fill of its own`() {
        val surface = panel().background as SubstrateSurface
        assertNotEquals(
            "the panel is filled with `--p-card`, so it reads as a card with a warm border " +
                "rather than as a bordered container on the ground",
            KitOrigin.token("--p-card"),
            surface.spec.fill,
        )
        assertEquals("row 12's fill cell says none", ColorMix.TRANSPARENT, surface.spec.fill)
    }

    // ---- the geometry row 12 states ------------------------------------------

    @Test
    fun `the panel spends row 12's padding and its own margins`() {
        val subject = panel()
        val params = subject.layoutParams as LinearLayout.LayoutParams

        val claims = listOf(
            Claim("row 12 padding-y (top)", dimenPx("swarm_space_12"), subject.paddingTop),
            Claim("row 12 padding-y (bottom)", dimenPx("swarm_space_12"), subject.paddingBottom),
            Claim("row 12 padding-x (start)", dimenPx("swarm_space_14"), subject.paddingStart),
            Claim("row 12 padding-x (end)", dimenPx("swarm_space_14"), subject.paddingEnd),
            Claim("row 12 margin top", dimenPx("swarm_space_8"), params.topMargin),
            Claim("row 12 margin start", dimenPx("swarm_space_14"), params.marginStart),
            Claim("row 12 margin end", dimenPx("swarm_space_14"), params.marginEnd),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    // ---- the two type cells ---------------------------------------------------

    @Test
    fun `the title is the error ink and the body is the secondary one`() {
        val subject = panel()
        val claims = KitOrigin.textClaims(
            subject.kitRequire(KitTag.KILL_TITLE) as TextView,
            ".prow .pj",
            KitOrigin.token("--p-err"),
            spScale,
        ) + KitOrigin.textClaims(
            bodyView(subject),
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

    /** Row 12's third cell: the daemon-side verb, marked inline inside the subtitle. */
    @Test
    fun `the command is the inline strong span, over the verb the caller named`() {
        val subject = panel()
        val text = bodyView(subject).text
        assertTrue(
            "the body holds a plain String, so the command was never marked and the panel says " +
                "the verb in the same face as the sentence around it",
            text is Spanned,
        )

        val appearance = spansOf<TextAppearanceSpan>(subject).single()
        val ink = spansOf<ForegroundColorSpan>(subject).single()
        val start = body.indexOf(command)
        val claims = listOf(
            Claim("command span start", start, (text as Spanned).getSpanStart(appearance)),
            Claim("command span end", start + command.length, text.getSpanEnd(appearance)),
            Claim("command ink", KitOrigin.token("--p-ink"), ink.foregroundColor),
            // ADR-009 D7: pitch, not a family string -- a bundled family reaches the span as a
            // resolved Typeface and leaves getFamily() null.
            Claim("command family is fixed-pitch", true, KitOrigin.isFixedPitch(appearance)),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `a panel with no command carries no span at all`() {
        val subject = panel(command = null)
        assertEquals(emptyList<TextAppearanceSpan>(), spansOf<TextAppearanceSpan>(subject))
        assertEquals(emptyList<ForegroundColorSpan>(), spansOf<ForegroundColorSpan>(subject))
    }

    // ---- the control that must not exist --------------------------------------

    /**
     * The amendment, asserted as a shape rather than as a comment.
     *
     * `App.KillSwitchEngaged` is read-only by design and the panel has no parameter for a trailing
     * view, so the only way a control gets in here is by someone editing this component. Two
     * children is the whole panel: the title, and the body.
     */
    @Test
    fun `the panel has no room for a control it could never wire`() {
        val subject = panel()
        assertEquals(
            "the panel has ${subject.childCount} children. Row 12's amendment deletes its " +
                "trailing control -- `handleRemoteSetControl` refuses the remote tier before the " +
                "backend is consulted, so a switch here is one that cannot act",
            2,
            subject.childCount,
        )
        assertNotNull(subject.kitFind(KitTag.KILL_TITLE))
        assertNotNull(subject.kitFind(KitTag.KILL_BODY))
        assertNull(
            "the toggle is on the panel, which is the one control this screen may never carry",
            subject.kitFind(KitTag.TOGGLE_TRACK),
        )
    }

    // ---- PB-DS-10's control ----------------------------------------------------

    @Test
    fun `the comparison fails when a value diverges`() {
        val surface = panel().background as SubstrateSurface

        assertTrue(
            "a perturbed border produced no mismatch, so the blend claim holds against any colour",
            mismatches(listOf(Claim("row 12 border", surface.spec.stroke + 1, surface.spec.stroke)))
                .isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("row 12 border", surface.spec.stroke, surface.spec.stroke)))
                .isEmpty(),
        )
    }
}
