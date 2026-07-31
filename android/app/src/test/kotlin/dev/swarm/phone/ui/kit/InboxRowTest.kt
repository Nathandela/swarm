package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.View
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

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the three components a triage row is made of:
 * the status dot, the session row and the working bar.
 *
 * These are the components where getting it wrong is invisible. A dot that is 8dp instead of 7dp,
 * a row that spends the wrong scale step, a glow drawn with `View.elevation` -- each renders
 * something that looks like a design and is not one, and the existing UI tests in this module
 * assert strings, enums and booleans, so not one of them would notice.
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE and not the default. Robolectric's LEGACY graphics stubs the text stack -- measureText
// returns one pixel per character -- which makes every font measure fixed-pitch and every
// typeface assertion in this file certify the opposite of the truth. The same annotation, for the
// same reason, as MonoBoxDrawingTest.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class InboxRowTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val density: Float get() = context.resources.displayMetrics.density

    private fun px(dp: Float) = dp * density

    /** sp -> px at the test display's density and font scale, the supported question. */
    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    // ---- the status dot ---------------------------------------------------

    /**
     * `.pdot`: 7dp, round, and the Group's colour.
     *
     * The radius is NOT asserted as a number, and that is PB-DS-4's finding rather than an
     * omission: `--p-dot-r` is 4px on a 7px box, `2 x 4 >= 7`, so the corner is clamped and the
     * dot is a full circle. The literal 4 never renders. What is asserted is that the drawable is
     * a circle of the design's diameter.
     */
    @Test
    fun `the status dot is the design's 7dp mark in its Group's colour`() {
        KitOrigin.groupTokens().forEach { (group, token) ->
            val dot = statusDot(context, group)
            val drawable = dot.background as? StatusDotDrawable
            assertTrue(
                "the $group dot's background is ${dot.background}, not a StatusDotDrawable -- so " +
                    "whatever it paints, it is not the dot this kit specifies",
                drawable != null,
            )
            assertEquals(
                emptyList<String>(),
                mismatches(
                    listOf(
                        Claim("$group fill", KitOrigin.token(token), drawable!!.fill),
                        Claim("`.pdot` diameter", px(KitOrigin.cssDp(".pdot", "width")), drawable.diameterPx),
                        Claim("`.pdot` height", px(KitOrigin.cssDp(".pdot", "height")), drawable.diameterPx),
                        Claim(
                            "$group dot laid out at its design size",
                            px(KitOrigin.cssDp(".pdot", "width")).toInt(),
                            dot.layoutParams.width,
                        ),
                    ),
                ),
            )
        }
    }

    /**
     * The glow is `Paint.setShadowLayer(9dp, 0, 0, blend)` ON A SOFTWARE LAYER, and both halves
     * are asserted because either one alone renders nothing.
     *
     * A shadow layer is ignored under hardware acceleration for anything but text, so a dot that
     * set the shadow and left the view hardware-accelerated draws a flat circle -- correct in
     * every value a test could read off the Paint, and wrong on screen. The layer type is the
     * only observable that distinguishes them.
     *
     * The two Groups that do not glow must be LAYER_TYPE_NONE rather than software: a software
     * layer allocates a bitmap per view, and paying for it on rows that draw a flat 7dp circle is
     * the kind of cost that is never found again.
     */
    @Test
    fun `only the live Groups glow, and only they are on a software layer`() {
        val claims = KitOrigin.groupTokens().flatMap { (group, _) ->
            val dot = statusDot(context, group)
            val drawable = dot.background as StatusDotDrawable
            val glows = Kit.groupGlow(context, group) != null
            listOf(
                Claim("$group glow colour", Kit.groupGlow(context, group), drawable.glow),
                Claim(
                    "$group glow radius",
                    if (glows) px(KitOrigin.cssFirstPx(".pdot.att", "box-shadow")) else 0f,
                    if (glows) drawable.glowRadiusPx else 0f,
                ),
                Claim(
                    "$group layer type",
                    if (glows) View.LAYER_TYPE_SOFTWARE else View.LAYER_TYPE_NONE,
                    dot.layerType,
                ),
            )
        }
        assertEquals(emptyList<String>(), mismatches(claims))
    }

    // ---- the session row --------------------------------------------------

    private fun row(group: String): ViewGroup =
        sessionRow(context, "quanthome/api", "claude", "Wants to run something", group) as ViewGroup

    /** `.prow`: the card, its padding, and the three text roles it carries. */
    @Test
    fun `the session row resolves the design's card and type`() {
        val row = row("completed")
        val surface = row.background as? SubstrateSurface
        assertTrue("the row's background is not a kit surface", surface != null)

        val claims = mutableListOf(
            Claim("`.prow` fill", KitOrigin.cssColour(".prow", "background"), surface!!.spec.fill),
            Claim("`.prow` padding-y (top)", dimen("swarm_space_10").toInt(), row.paddingTop),
            Claim("`.prow` padding-y (bottom)", dimen("swarm_space_10").toInt(), row.paddingBottom),
            Claim("`.prow` padding-x (start)", dimen("swarm_space_12").toInt(), row.paddingStart),
            Claim("`.prow` padding-x (end)", dimen("swarm_space_12").toInt(), row.paddingEnd),
        )
        claims += KitOrigin.textClaims(
            row.kitRequire(KitTag.PROJECT) as TextView,
            ".prow .pj",
            // `.prow .pj` declares no colour and inherits `.pscreen { color: var(--p-ink) }`.
            KitOrigin.cssColour(".pscreen", "color"),
            spScale,
        )
        claims += KitOrigin.textClaims(
            row.kitRequire(KitTag.AGENT) as TextView,
            ".prow .ag",
            KitOrigin.cssColour(".prow .ag", "color"),
            spScale,
        )
        claims += KitOrigin.textClaims(
            row.kitRequire(KitTag.NEED) as TextView,
            ".prow .ln",
            KitOrigin.cssColour(".prow .ln", "color"),
            spScale,
        )
        assertEquals(emptyList<String>(), mismatches(claims))

        // The design's own inheritance, asserted rather than assumed: if `.prow .pj` ever gains a
        // colour, the claim above is reading the wrong rule and should be updated with it.
        assertTrue(
            "`.prow .pj` now declares a colour of its own, so the claim above -- which reads " +
                "`.pscreen`'s inherited ink -- is checking the wrong rule",
            KitOrigin.inheritsColour(".prow .pj"),
        )
    }

    /** The gaps and the offsets: `.t`'s 7px gap and `.ln`'s 4px top margin, through the ledger. */
    @Test
    fun `the row's internal spacing is the scale's`() {
        val row = row("completed")
        val line = row.kitRequire(KitTag.LINE) as ViewGroup
        val dot = row.kitRequire(KitTag.DOT)
        val agent = row.kitRequire(KitTag.AGENT)
        val need = row.kitRequire(KitTag.NEED)

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "`.prow .t` gap after the dot",
                        dimen("swarm_space_8").toInt(),
                        (dot.layoutParams as ViewGroup.MarginLayoutParams).marginEnd,
                    ),
                    Claim(
                        "`.prow .t` gap before the agent",
                        dimen("swarm_space_8").toInt(),
                        (agent.layoutParams as ViewGroup.MarginLayoutParams).marginStart,
                    ),
                    Claim(
                        "`.prow .ln` margin-top",
                        dimen("swarm_space_4").toInt(),
                        (need.layoutParams as ViewGroup.MarginLayoutParams).topMargin,
                    ),
                    Claim(
                        "the project name takes the remaining width (`.pj { flex: 1 }`)",
                        1f,
                        ((row.kitRequire(KitTag.PROJECT).layoutParams) as LinearLayout.LayoutParams).weight,
                    ),
                ),
            ),
        )

        // The glow is drawn OUTSIDE the 7dp dot -- 9dp of blur in every direction -- and a
        // ViewGroup clips its children to their own bounds by default. Without this the dot's
        // Paint is correct, its layer type is correct, and the halo is invisible.
        assertTrue(
            "the line holding the status dot clips its children, so the 9dp glow is clipped to " +
                "the 7dp dot and nothing of it renders",
            !line.clipChildren,
        )
    }

    /** `.prow.attention` is the NeedsInput row, and it is the only one. */
    @Test
    fun `only the NeedsInput row is the attention variant`() {
        KitOrigin.groupTokens().keys.forEach { group ->
            val surface = row(group).background as SubstrateSurface
            val attention = group == "needs_input"
            assertEquals(
                "the $group row's rail",
                attention,
                surface.spec.rail != null,
            )
            if (attention) {
                assertNotEquals(
                    "the NeedsInput row's border is the plain hairline; the design warms it with " +
                        "the attention colour, which is the second of the four sites of that state",
                    KitOrigin.token("--p-hair"),
                    surface.spec.stroke,
                )
            } else {
                assertEquals(
                    "a $group row carries the warmed attention border, so every row on the inbox " +
                        "reads as needing the user",
                    KitOrigin.cssColour(".prow", "border"),
                    surface.spec.stroke,
                )
            }
        }
    }

    /** `.prows`: the rows' container, which owns the side padding and the 7px gap. */
    @Test
    fun `the session list carries the side padding and the gap between rows`() {
        val list = sessionList(context)
        list.addView(row("needs_input"))
        list.addView(row("working"))

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.prows` padding-x (start)", dimen("swarm_space_12").toInt(), list.paddingStart),
                    Claim("`.prows` padding-x (end)", dimen("swarm_space_12").toInt(), list.paddingEnd),
                    Claim(
                        "no gap above the first row",
                        0,
                        (list.getChildAt(0).layoutParams as ViewGroup.MarginLayoutParams).topMargin,
                    ),
                    Claim(
                        "`.prows` gap above the second row",
                        dimen("swarm_space_8").toInt(),
                        (list.getChildAt(1).layoutParams as ViewGroup.MarginLayoutParams).topMargin,
                    ),
                ),
            ),
        )
    }

    // ---- the working bar --------------------------------------------------

    /**
     * `.workbar` appears on Working rows and nowhere else -- it is half of Substrate's static
     * Working affordance, the other half being the dot glow.
     */
    @Test
    fun `the working bar is on the Working row and on no other`() {
        KitOrigin.groupTokens().keys.forEach { group ->
            val bar = row(group).kitFind(KitTag.WORKBAR)
            if (group == "working") {
                assertTrue("the Working row has no workbar", bar != null)
            } else {
                assertNull("a $group row carries a workbar, which means it is running", bar)
            }
        }
    }

    /**
     * The gradient's transparent stop KEEPS ITS RGB, and that is the whole test.
     *
     * `linear-gradient(90deg, #00c2d7, transparent 85%)` in Android is an end colour of
     * `#0000C2D7`. The obvious spelling, `#00000000`, is also "transparent" and fades the bar
     * through BLACK -- so the visible half greys out toward its middle. Both are invisible in a
     * diff; only the RGB channels of a fully transparent colour tell them apart.
     */
    @Test
    fun `the working bar fades to a transparent WORKING colour, not to a transparent black`() {
        val bar = row("working").kitRequire(KitTag.WORKBAR)
        val shape = bar.background as? WorkingBarShape
        assertTrue("the workbar's background is not a WorkingBarShape", shape != null)

        val work = KitOrigin.token("--p-work")
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("gradient start", work, shape!!.startColour),
                    Claim("gradient end", work and 0x00FFFFFF, shape.endColour),
                    Claim("fade stop", KitOrigin.percentInToken("--p-workbar"), shape.fadeStop),
                    Claim("`.workbar` radius", px(KitOrigin.cssDp(".workbar", "border-radius")), shape.radiusPx),
                    Claim(
                        "`.workbar` height",
                        px(KitOrigin.cssDp(".workbar", "height")).toInt(),
                        bar.layoutParams.height,
                    ),
                    Claim(
                        "`.workbar` margin-top",
                        dimen("swarm_space_2").toInt(),
                        (bar.layoutParams as ViewGroup.MarginLayoutParams).topMargin,
                    ),
                ),
            ),
        )
        assertNotEquals(
            "the workbar's end colour is a transparent BLACK, so the bar fades through grey " +
                "instead of dissolving in place",
            0,
            shape.endColour and 0x00FFFFFF,
        )
    }

    /**
     * The negative control PB-DS-10 requires for this suite.
     *
     * It moves each kind of value this file asserts by the smallest amount that matters -- one
     * unit of colour, one dp, one boolean -- and requires the shared comparison to object. A
     * control that passed here would mean every `assertEquals(emptyList(), mismatches(...))`
     * above is green over a component nobody looked at.
     */
    @Test
    fun `the row assertions can actually fail`() {
        val dotDp = KitOrigin.cssDp(".pdot", "width")
        val att = KitOrigin.token("--p-att")
        assertTrue(
            "a dot one dp wider than the design's passes the comparison",
            mismatches(listOf(Claim("diameter", px(dotDp), px(dotDp + 1)))).isNotEmpty(),
        )
        assertTrue(
            "a glow that lost its alpha passes the comparison",
            mismatches(
                listOf(Claim("glow", KitOrigin.overTransparent(att, 0.70f), att)),
            ).isNotEmpty(),
        )
        assertTrue(
            "a hardware-accelerated dot passes the layer-type comparison, so the glow could be " +
                "silently absent on every row",
            mismatches(
                listOf(Claim("layer", View.LAYER_TYPE_SOFTWARE, View.LAYER_TYPE_NONE)),
            ).isNotEmpty(),
        )
        assertTrue(
            "a workbar ending in transparent black passes the comparison against one ending in " +
                "transparent --p-work, which is the one mistake this component has",
            mismatches(
                listOf(Claim("end", KitOrigin.token("--p-work") and 0x00FFFFFF, 0)),
            ).isNotEmpty(),
        )

        // THE PROBE IS VALIDATED BEFORE ANY COMPONENT IS BLAMED. Two failure modes produce the
        // identical symptom "a sans selector reports monospace": the probe cannot tell two faces
        // apart, or setTextAppearance is not delivering android:fontFamily. The first check uses
        // the platform's own two faces -- no view, no style, no resource table -- so it isolates
        // the probe; the second then says the family really is reaching a styled TextView.
        assertEquals(emptyList<String>(), KitOrigin.typefaceProbeFaults())
        assertNotEquals(
            "a sans style and a mono style measure the same pitch on a real TextView, so " +
                "android:fontFamily is not surviving setTextAppearance and the pitch claims " +
                "above cannot fail",
            KitOrigin.isFixedPitch(
                TextView(context).apply { setTextAppearance(dev.swarm.phone.R.style.TextAppearance_Swarm_Title_Row) }.paint,
            ),
            KitOrigin.isFixedPitch(
                TextView(context).apply { setTextAppearance(dev.swarm.phone.R.style.TextAppearance_Swarm_Mono_Agent) }.paint,
            ),
        )

        // And the readers must distinguish the values they are asked about, or the equalities in
        // this file hold over one number repeated.
        assertNotEquals(
            "the CSS reader returns the same size for the 7px dot and the 5px presence dot",
            KitOrigin.cssDp(".pdot", "width"),
            KitOrigin.cssDp(".chip .pd", "width"),
        )
        assertNotEquals(
            "the type reader returns the same size for the row title and the need line",
            KitOrigin.textClaims(TextView(context), ".prow .pj", 0, spScale).first().want,
            KitOrigin.textClaims(TextView(context), ".prow .ln", 0, spScale).first().want,
        )
    }
}
