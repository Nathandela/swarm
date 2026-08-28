package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.util.TypedValue
import android.view.View
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
import kotlin.math.hypot
import kotlin.math.roundToInt

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

    /**
     * A scale step in whole pixels, ROUNDED -- which is what `getDimensionPixelSize` does and
     * therefore what a component that spends the step correctly resolves to.
     *
     * `.toInt()` truncates, and at Robolectric's mdpi default the two agree on every value in
     * this file: 12dp is 12.0px, and there is nothing to round. That is why every expectation
     * here could be written with a cast and still pass while the kit lost a pixel on every real
     * handset. [KitDensityTest] is the suite that runs where they differ.
     */
    private fun dimenPx(name: String): Int = dimen(name).roundToInt()

    // ---- the status dot ---------------------------------------------------

    /** The dot's core, in whole pixels: the design's 7dp as the platform spends a dimension. */
    private val corePx: Int get() = px(KitOrigin.cssDp(".pdot", "width")).roundToInt()

    /**
     * The layout the dot consumes: its box, plus whatever it gives back for a halo that -- like
     * every CSS `box-shadow` -- must not participate in layout.
     */
    private fun footprint(dot: View): Int {
        val params = dot.layoutParams as ViewGroup.MarginLayoutParams
        return params.width + params.marginStart + params.marginEnd
    }

    /**
     * `.pdot`: 7dp, round, and the Group's colour.
     *
     * The radius is NOT asserted as a number, and that is PB-DS-4's finding rather than an
     * omission: `--p-dot-r` is 4px on a 7px box, `2 x 4 >= 7`, so the corner is clamped and the
     * dot is a full circle. The literal 4 never renders. What is asserted is that the drawable is
     * a circle of the design's diameter.
     *
     * THE SIZE ASSERTED IS THE FOOTPRINT, NOT THE VIEW. A glowing dot's view is deliberately
     * larger than 7dp -- see [statusDot] -- and gives the difference back as a negative margin,
     * because a software layer's bitmap is the view's own bounds and a 7dp view has nowhere to
     * put a 9dp halo. What the design fixes at 7px is the space the mark occupies, and that is
     * what this reads. The earlier `dot.layoutParams.width == 7dp` claim pinned the defect: it
     * asserted the one number that had to change.
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
                            "$group dot occupies the design's 7px of layout and no more",
                            corePx,
                            footprint(dot),
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
     * The three Groups that do not glow must be LAYER_TYPE_NONE rather than software: a software
     * layer allocates a bitmap per view, and paying for it on rows that draw a flat 7dp circle is
     * the kind of cost that is never found again.
     *
     * EVERY EXPECTATION HERE IS THE DESIGN'S, WHICH IT WAS NOT. The colour was compared against
     * `Kit.groupGlow(context, group)` -- the call the drawable was built from, so the claim held
     * for any colour -- and WHICH Groups glow was decided by that same call, so a kit that glowed
     * on all four would have been checked against its own opinion and passed. Both now come from
     * the `.sdot.*` variant whose fill is the Group's token (ruling R8, 2026-08-09: moved from
     * the shared block's `.pdot.*` to the maquette's own selector for this dot).
     */
    @Test
    fun `only the live Group that glows does, and only it is on a software layer`() {
        val claims = KitOrigin.groupTokens().flatMap { (group, token) ->
            val dot = statusDot(context, group)
            val drawable = dot.background as StatusDotDrawable
            val glow = KitOrigin.dotGlow(token)
            listOf(
                Claim("$group glow colour", glow?.colour, drawable.glow),
                Claim(
                    "$group glow radius",
                    px(glow?.radiusDp ?: 0f),
                    drawable.glowRadiusPx,
                ),
                Claim(
                    "$group layer type",
                    if (glow != null) View.LAYER_TYPE_SOFTWARE else View.LAYER_TYPE_NONE,
                    dot.layerType,
                ),
            )
        }
        assertEquals(emptyList<String>(), mismatches(claims))
        assertEquals(
            "the design gives a halo to a number of Groups other than one, so \"nothing glows " +
                "unless it is alive\" is being read off something that is not the design -- ruling " +
                "R8 (2026-08-09) made this a one-way implication and dropped the count from two to " +
                "one",
            1,
            KitOrigin.groupTokens().values.count { KitOrigin.dotGlow(it) != null },
        )
    }

    // ---- the glow, in pixels ----------------------------------------------

    /** What a dot leaves in the bitmap its own layer is backed by. */
    private data class DotRender(val layerPx: Int, val core: Int, val halo: Int)

    /**
     * Draws the dot into a bitmap THE SIZE OF ITS OWN VIEW, which is what a software layer is.
     *
     * `setLayerType(LAYER_TYPE_SOFTWARE)` allocates a bitmap of the view's bounds and draws the
     * view into it; anything painted outside those bounds -- a `setShadowLayer` halo, for
     * instance -- is clipped by the layer before any parent's `clipChildren` has a say. Rendering
     * into a bitmap of exactly the view's size reproduces that faithfully, which is why this
     * probe can tell a halo that renders from one that does not.
     *
     * THE LAYER IS MODELLED RATHER THAN EXERCISED, DELIBERATELY. Drawing the dot through a parent
     * would not reproduce it: a layer bitmap is what the platform allocates when it composites a
     * view onto a HARDWARE canvas, and a JVM test has no hardware canvas to obtain -- the halo
     * would render unclipped here and be clipped on a device, which is the failure this test
     * exists to prevent, inverted. A bitmap of exactly the view's bounds is what that layer is.
     *
     * WHAT IT CANNOT SEE is the layer TYPE. A canvas over a `Bitmap` is a software canvas, so the
     * shadow renders here whether or not the view asked for a layer -- and on a device it would
     * not, because `setShadowLayer` is ignored under hardware acceleration. That half stays where
     * it was, in `only the live Groups glow, and only they are on a software layer`. The two
     * assertions are the two halves of one effect and neither is sufficient.
     */
    private fun renderDot(dot: View, layerPx: Int = dot.layoutParams.width): DotRender {
        val w = layerPx
        val h = layerPx
        dot.measure(
            View.MeasureSpec.makeMeasureSpec(w, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(h, View.MeasureSpec.EXACTLY),
        )
        dot.layout(0, 0, w, h)
        val bitmap = Bitmap.createBitmap(w, h, Bitmap.Config.ARGB_8888)
        dot.draw(Canvas(bitmap))

        // The circle is drawn centred, so the core is a disc rather than the layer's middle
        // square. One pixel of slack around it belongs to the circle's own antialiased edge:
        // counting that as halo would report a glow on the two Groups the design leaves flat.
        val radius = (dot.background as StatusDotDrawable).diameterPx / 2f
        var core = 0
        var halo = 0
        for (y in 0 until h) {
            for (x in 0 until w) {
                if (Color.alpha(bitmap.getPixel(x, y)) == 0) continue
                val distance = hypot(x + 0.5f - w / 2f, y + 0.5f - h / 2f)
                when {
                    distance <= radius -> core++
                    distance > radius + 1f -> halo++
                }
            }
        }
        return DotRender(layerPx = w, core = core, halo = halo)
    }

    /**
     * @return one line per way the halo fails to reach the screen. Empty means the layer holds it.
     *
     * Both the real assertion and its control feed this the same rendering, so the control
     * exercises the check rather than a copy of it.
     */
    private fun haloFaults(render: DotRender, glowDp: Float): List<String> {
        val faults = mutableListOf<String>()
        val glowPx = px(glowDp).roundToInt()
        val need = corePx + 2 * glowPx
        if (render.core == 0) {
            faults += "the dot painted nothing at all into its layer, so this probe is measuring " +
                "an empty bitmap and every count below is zero for a reason that has nothing to " +
                "do with the glow"
        }
        if (render.layerPx < need) {
            faults += "the layer is ${render.layerPx}px and a ${corePx}px core with a ${glowPx}px " +
                "halo needs ${need}px. A software layer's bitmap IS the view's bounds, so the " +
                "halo is clipped inside the layer -- before clipChildren on any parent is " +
                "consulted, and with the Paint, the shadow radius and the layer type all correct."
        }
        if (render.halo == 0) {
            faults += "not one pixel outside the ${corePx}px core is painted, so the dot renders " +
                "as a flat circle and the halo is the design's on paper only"
        }
        return faults
    }

    /**
     * VALIDATES THE PIXEL PROBE ITSELF, before any component is blamed for a missing halo.
     *
     * Two failure modes produce the identical symptom "no glow pixels": the component clips its
     * halo, or the graphics stack cannot render a blur at all and no bitmap in this suite would
     * ever hold one. They have opposite fixes, so a shadow is drawn here on a bare canvas, with
     * no view and no layer, and compared against the same circle drawn without one.
     *
     * @return one line per fault. Empty means the probe can answer the question it is asked.
     */
    private fun shadowProbeFaults(): List<String> {
        val faults = mutableListOf<String>()
        val radius = 8f
        val blur = 12f
        val size = ((radius + blur) * 2).toInt()
        fun paint(shadow: Boolean) = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            color = Color.WHITE
            if (shadow) setShadowLayer(blur, 0f, 0f, Color.WHITE)
        }
        fun beyond(shadow: Boolean): Int {
            val bitmap = Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888)
            Canvas(bitmap).drawCircle(size / 2f, size / 2f, radius, paint(shadow))
            var count = 0
            for (y in 0 until size) {
                for (x in 0 until size) {
                    if (Color.alpha(bitmap.getPixel(x, y)) == 0) continue
                    if (hypot(x + 0.5f - size / 2f, y + 0.5f - size / 2f) > radius + 1f) count++
                }
            }
            return count
        }
        if (beyond(shadow = true) == 0) {
            faults += "a ${blur}px shadow layer on a bare canvas paints nothing outside its own " +
                "circle, so this graphics stack renders no blur and no halo assertion in this " +
                "file is about the kit"
        }
        if (beyond(shadow = false) != 0) {
            faults += "a circle drawn with NO shadow layer paints pixels well outside itself, so " +
                "the probe counts something other than a halo and would report one on the two " +
                "Groups the design leaves flat"
        }
        return faults
    }

    /**
     * THE GLOW REACHES THE SCREEN, asserted in pixels rather than in Paint fields.
     *
     * Every earlier assertion about the halo read a value off the drawable or the view -- the
     * blend, the blur radius, the layer type -- and all three were right while the halo rendered
     * nowhere: the layer was allocated at the view's 7dp bounds, so the 9dp shadow was clipped
     * inside it. Nothing short of looking at the pixels distinguishes that from a working glow,
     * which is the whole reason this test draws.
     */
    @Test
    fun `the one live Group's glow survives into the layer, and the flat Groups paint no halo`() {
        assertEquals(emptyList<String>(), shadowProbeFaults())

        // `.pdot.att` rather than `.sdot.att`: this is only a generic "how much room does a 9dp
        // halo need" measurement, not a per-Group claim, and both design sources still state 9px
        // for it -- ruling R8 moved the SHARE and the SITE COUNT, not this radius.
        val roomForAHalo = corePx + 2 * px(KitOrigin.cssFirstPx(".pdot.att", "box-shadow")).roundToInt()
        KitOrigin.groupTokens().forEach { (group, token) ->
            val glow = KitOrigin.dotGlow(token)
            if (glow != null) {
                assertEquals(
                    "the $group dot's halo does not render",
                    emptyList<String>(),
                    // Its OWN variant's blur radius, read from the maquette's `.sdot.att`.
                    haloFaults(renderDot(statusDot(context, group)), glow.radiusDp),
                )
            } else {
                // MEASURED IN A LAYER THE SIZE A GLOWING DOT'S IS, which is the only way this
                // says anything: a flat dot's own layer is 7dp square and has no room for a halo
                // pixel whether or not it paints one, so counting zero there would be geometry
                // rather than evidence. Given the same room as a live dot, it must still be flat.
                val render = renderDot(statusDot(context, group), layerPx = roomForAHalo)
                assertEquals(
                    "the $group dot paints ${render.halo} pixels outside its core given the room " +
                        "a live dot's halo needs, and the design gives it none -- either it glows " +
                        "when it should not, or this probe counts the circle's own antialiased " +
                        "edge and cannot tell a halo from one",
                    0,
                    render.halo,
                )
            }
        }
    }

    /**
     * The negative control for the assertion above: the geometry this kit shipped, through the
     * same probe.
     *
     * The dot was a view of exactly `size x size` = 7dp with `LAYER_TYPE_SOFTWARE` set on it. Both
     * of those are individually correct and the combination renders no halo, because a software
     * layer's backing bitmap is the view's own bounds. `clipChildren = false` on the row and the
     * line -- which the kit sets, and which an earlier test asserted -- governs the PARENT's clip
     * and cannot widen a layer. A control that rebuilt the check inline would prove the copy
     * works; this one builds the defect and hands it to [haloFaults].
     */
    @Test
    fun `the pixel probe rejects the geometry that clipped the glow`() {
        val glow = requireNotNull(KitOrigin.dotGlow(KitOrigin.groupToken("needs_input")))
        val clipped = View(context).apply {
            // The kit's own drawable, unchanged: only the geometry is put back the way it was.
            background = statusDot(context, "needs_input").background
            layoutParams = LinearLayout.LayoutParams(corePx, corePx)
            setLayerType(View.LAYER_TYPE_SOFTWARE, null)
        }
        val faults = haloFaults(renderDot(clipped), glow.radiusDp)
        assertTrue(
            "a dot whose software layer is exactly its 7dp core passes the halo check, so the " +
                "check would not have caught the defect it exists for",
            faults.isNotEmpty(),
        )
        assertTrue(
            "the halo check objects to the clipped dot for some reason other than the layer " +
                "having no room for a halo: $faults",
            faults.any { it.contains("A software layer's bitmap IS the view's bounds") },
        )
    }

    /**
     * The dot is silent unless a screen gives it words, and it says so rather than leaving it to
     * a heuristic.
     *
     * The dot's colour is the ONLY carrier of a session's state -- four Groups, four hues, no
     * text -- so a screen reader user gets nothing from it at all. The kit cannot fix that by
     * itself: the words are copy, copy is the screen's (PB-DS-9), and `res/values/strings.xml` is
     * not this slice's file. What it can do is take the description as data and, when it has
     * none, mark the view decorative EXPLICITLY instead of shipping a view whose announcement
     * depends on which platform heuristic runs.
     */
    @Test
    fun `the status dot announces the state when the screen names it`() {
        val silent = statusDot(context, "needs_input")
        assertEquals(
            "a dot with no words is left at IMPORTANT_FOR_ACCESSIBILITY_AUTO, so whether the one " +
                "carrier of a session's state reaches a screen reader is an accident rather than " +
                "a decision",
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            silent.importantForAccessibility,
        )
        assertNull(
            "the silent dot carries an empty content description, which is the platform's idiom " +
                "for a decorative view and is strictly worse than none at all",
            silent.contentDescription,
        )

        val spoken = statusDot(context, "needs_input", "needs you")
        assertEquals("needs you", spoken.contentDescription)
        assertNotEquals(
            "a dot the screen gave words to is marked not-important-for-accessibility, so the " +
                "words never reach anyone",
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            spoken.importantForAccessibility,
        )
        assertNull(announcementFault(spoken.contentDescription, null))

        // The row is where a screen supplies them, and it must carry them through to the dot.
        val row = sessionRow(
            context, "quanthome/api", "claude", "Wants to run something", "needs_input",
            lit = true,
            promoted = false,
            stateDescription = "needs you",
        )
        assertEquals("needs you", row.kitRequire(KitTag.DOT).contentDescription)

        // The control, on the same function the assertion above calls: the shipped spelling.
        assertNotNull(
            "an empty content description passes the announcement check, so the defect this " +
                "test exists for would pass it too",
            announcementFault("", null),
        )
    }

    // ---- the session row --------------------------------------------------

    /**
     * WHY THIS SUITE MAY STATE `lit` AND THE SCREEN SUITE MAY NOT. ADR-009 D4's promotion is a
     * fact about a SESSION, and `TriageInboxScreen` is where it is decided -- `TriageInboxViewTest`
     * asserts the mapping by driving the real resolver, which is the only place that assertion
     * says anything. What this component owes is the other half: given the answer, it must draw
     * the right slab. Hand-feeding it here is asking exactly that question.
     */
    private fun row(group: String, lit: Boolean = false): ViewGroup = sessionRow(
        context, "quanthome/api", "claude", "Wants to run something", group, lit,
        promoted = false,
    ) as ViewGroup

    // ---- ADR-009 D5's sweep, from the row that earns it ---------------------

    @Test
    fun `a promoted row sweeps once, and a resting one never does`() {
        // THE ROW IS TOLD, IT DOES NOT DECIDE -- `lit`'s own argument, applied to the moment
        // rather than the state. Which Group is blocked on the human is `TriageInboxScreen`'s
        // decision, and WHEN it changed is the view state's; what this component owes is that a
        // row told it was just promoted plays the one effect, and a row told nothing plays none.
        Motion.inFlightSweep?.end()
        sessionRow(
            context, "quanthome/api", "claude", "Wants to run something", "needs_input",
            lit = true, promoted = false,
        )
        assertNull(
            "a row that is merely lit has been waiting; the slab says so and nothing moves",
            Motion.inFlightSweep,
        )

        sessionRow(
            context, "quanthome/api", "claude", "Wants to run something", "needs_input",
            lit = true, promoted = true,
        )
        assertNotNull(
            "a row that has just been promoted plays D5's one new exception",
            Motion.inFlightSweep,
        )
        Motion.inFlightSweep?.end()
    }

    @Test
    fun `two promoted rows leave one sweep, not two`() {
        // D5's one-per-viewport rule, asserted where a viewport actually gets two of them: one
        // journal event promoting two sessions builds two rows in one pass. The rule is Motion's
        // (newest wins, the superseded one completes instantly); this is the composition that
        // exercises it, and the assertion is that the kit routes through that rule rather than
        // around it.
        Motion.inFlightSweep?.end()
        val first = sessionRow(
            context, "quanthome/api", "claude", "Wants to run something", "needs_input",
            lit = true, promoted = true,
        )
        val firstSweep = Motion.inFlightSweep
        val second = sessionRow(
            context, "quanthome/web", "codex", "Wants to run something", "needs_input",
            lit = true, promoted = true,
        )
        assertNotNull(firstSweep)
        assertNotNull(Motion.inFlightSweep)
        assertNotEquals(
            "the second row's sweep must have superseded the first's, not joined it",
            firstSweep,
            Motion.inFlightSweep,
        )
        assertNotEquals(first, second)
        Motion.inFlightSweep?.end()
    }

    /** `.prow`: the card, its padding, and the three text roles it carries. */
    @Test
    fun `the session row resolves the design's card and type`() {
        val row = row("completed")
        val surface = row.background as? SubstrateSurface
        assertTrue("the row's background is not a kit surface", surface != null)

        val claims = mutableListOf(
            Claim("`.prow` fill", KitOrigin.cssColour(".prow", "background"), surface!!.spec.fill),
            Claim("`.prow` padding-y (top)", dimenPx("swarm_space_10"), row.paddingTop),
            Claim("`.prow` padding-y (bottom)", dimenPx("swarm_space_10"), row.paddingBottom),
            Claim("`.prow` padding-x (start)", dimenPx("swarm_space_12"), row.paddingStart),
            Claim("`.prow` padding-x (end)", dimenPx("swarm_space_12"), row.paddingEnd),
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

    /**
     * `.prow .ag` carries what the MACHINE reported, and draws nothing at all when it reported
     * nothing.
     *
     * BOTH DIRECTIONS, because only one of them is the defect. `swarmmobile.Session.Agent` is
     * "verbatim from the wire" and mobile/types.go states that an empty Agent means "the session's
     * records carried none" -- it is never derived on-device. So a row for a session the machine
     * said nothing about must show NOTHING: no placeholder, no "unknown", and above all no falling
     * back to the title or the id, which would render a fabricated identity in the exact cell a
     * reader trusts to name the agent. That is ADR-007 B135's class.
     *
     * AN EMPTY CELL IS NOT NOTHING, which is what this row used to draw: the agent `TextView` was
     * added unconditionally, so a session with no agent still got a view carrying the 8 dp gap
     * before it. The component already makes this distinction where the design does -- the workbar
     * exists only on a Working row -- and the tab badge makes it for a zero count ("Zero means no
     * badge at all, not a badge reading 0"). This is the same rule for the same reason.
     */
    @Test
    fun `the agent cell is the wire's word, and is absent when the wire carried none`() {
        val named = sessionRow(context, "quanthome/api", "claude", "Wants to run something", "working", lit = false, promoted = false)
        assertEquals(
            "the agent cell does not carry the agent the machine reported",
            "claude",
            (named.kitRequire(KitTag.AGENT) as TextView).text.toString(),
        )

        val anonymous = sessionRow(context, "quanthome/api", "", "Wants to run something", "working", lit = false, promoted = false)
        assertNull(
            "a session whose records carried NO agent still gets an agent cell on its row -- an " +
                "empty TextView holding the 8 dp gap before it. `swarmmobile.Session.Agent` is " +
                "verbatim from the wire and an empty one means the machine reported none, so the " +
                "honest rendering is no view at all rather than a blank one",
            anonymous.kitFind(KitTag.AGENT),
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
                    // The GAP, not the margin: a glowing dot's margins also carry the negative
                    // compensation for its halo, and reading marginEnd alone would report the
                    // design's 7px on a Completed row and 7px minus the glow radius on a NeedsInput
                    // one -- two different answers to a question the design gives one answer to.
                    Claim(
                        "`.prow .t` gap after the dot",
                        dimenPx("swarm_space_8"),
                        footprint(dot) - corePx,
                    ),
                    Claim(
                        "`.prow .t` gap before the agent",
                        dimenPx("swarm_space_8"),
                        (agent.layoutParams as ViewGroup.MarginLayoutParams).marginStart,
                    ),
                    Claim(
                        "`.prow .ln` margin-top",
                        dimenPx("swarm_space_4"),
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

        // NECESSARY AND NOT SUFFICIENT, which is worth stating because this assertion once stood
        // alone. The dot's view is inflated by its glow radius and given the difference back as a
        // negative margin, so it extends past the line's own bounds on every side; a line that
        // clipped its children would cut the halo off at them. But clipChildren governs the
        // PARENT's clip and can do nothing about a layer bitmap sized to the child -- which is
        // exactly how the halo came to be missing while this assertion passed. The pixels are
        // what settles it, and `the live Groups' glow survives into the layer` reads them.
        assertTrue(
            "the line holding the status dot clips its children, so the 9dp glow is cut off at " +
                "the line's bounds and nothing of it renders",
            !line.clipChildren,
        )
    }

    /**
     * A promoted row is the attention variant and a resting one is not -- the rail, the warmed
     * border, and ADR-009 D4's elevated slab under the stronger key-light, all four at once.
     *
     * THIS TEST'S SUBJECT MOVED WITH THE DECISION, AND HALF OF IT NOW LIVES ONE LAYER UP. What it
     * said before:
     *
     *     KitOrigin.groupTokens().keys.forEach { group ->
     *         val surface = row(group).background as SubstrateSurface
     *         val attention = group == "needs_input"
     *         assertEquals("the $group row's rail", attention, surface.spec.rail != null)
     *         ...
     *     }
     *
     * -- an assertion that the KIT knows which `status.Group` is blocked on the human. It did know,
     * as `group == "needs_input"` inside `sessionRow`, and that was a third copy of a product
     * decision `TriageInboxScreen` already made twice. The binding half is now asserted where the
     * decision is made, from the real resolver, by `TriageInboxViewTest`'s
     * `the needs_input row is the attention variant and no other row is`. Nothing is lost: what
     * remains here is the half that was always this component's, and it is now asserted over BOTH
     * answers rather than over four Group strings that produced two.
     */
    @Test
    fun `a promoted row is the attention variant and a resting row is not`() {
        val promoted = row("needs_input", lit = true).background as SubstrateSurface
        val resting = row("needs_input", lit = false).background as SubstrateSurface

        assertNotNull("a promoted row carries no attention rail", promoted.spec.rail)
        assertNull(
            "a row the screen did NOT promote still carries the attention rail, so every row on " +
                "the inbox reads as needing the user",
            resting.spec.rail,
        )
        assertNotEquals(
            "the promoted row's border is the plain hairline; the design warms it with the " +
                "attention colour, which is the second of the four sites of that state",
            KitOrigin.token("--p-hair"),
            promoted.spec.stroke,
        )
        assertEquals(
            "a resting row carries the warmed attention border",
            KitOrigin.cssColour(".prow", "border"),
            resting.spec.stroke,
        )
        // ADR-009 D4's two-value promotion, read off the maquette rather than named here.
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "the promoted slab's fill",
                        KitOrigin.maquetteColour(".slab.lit", "background"),
                        promoted.spec.fill,
                    ),
                    Claim(
                        "the resting slab's fill",
                        KitOrigin.maquetteColour(".slab", "background"),
                        resting.spec.fill,
                    ),
                    Claim(
                        "the promoted key light",
                        KitOrigin.maquetteRgba(".slab.lit", "box-shadow"),
                        promoted.spec.keyLight,
                    ),
                    Claim(
                        "the resting key light",
                        KitOrigin.maquetteRgba(".slab", "box-shadow"),
                        resting.spec.keyLight,
                    ),
                ),
            ),
        )
        // THE GROUP MUST NOT BE ABLE TO DECIDE IT ANY MORE, which is the property this whole
        // change exists to create: the same Group string, two answers, because the answer comes
        // from the screen.
        assertNotEquals(
            "a row's slab is the same whether or not the screen promoted it, so `lit` is being " +
                "ignored and the component is deciding promotion by some other means",
            resting.spec.fill,
            promoted.spec.fill,
        )
    }

    /**
     * `.prows`: the rows' container, which owns the side padding and the gap between rows.
     *
     * AUTHORIZED REWRITE, ADR-020 D2 (2026-08-27, wave W4). The steps are the Slate maquette's
     * `.slab { margin: 0 16px 14px }` now -- 16 dp at the sides, 14 dp between rows -- where the
     * claims read `space_12` and `space_8` for `.prows`'s own 12px and 7px gap. What the four
     * claims said before:
     *
     *     Claim("`.prows` padding-x (start)", dimenPx("swarm_space_12"), list.paddingStart),
     *     Claim("`.prows` padding-x (end)", dimenPx("swarm_space_12"), list.paddingEnd),
     *     Claim("`.prows` gap above the second row", dimenPx("swarm_space_8"), ...topMargin),
     */
    @Test
    fun `the session list carries the side padding and the gap between rows`() {
        val list = sessionList(context)
        list.addView(row("needs_input"))
        list.addView(row("working"))

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.slab` margin sides (start)", dimenPx("swarm_space_16"), list.paddingStart),
                    Claim("`.slab` margin sides (end)", dimenPx("swarm_space_16"), list.paddingEnd),
                    Claim(
                        "no gap above the first row",
                        0,
                        (list.getChildAt(0).layoutParams as ViewGroup.MarginLayoutParams).topMargin,
                    ),
                    Claim(
                        "`.slab` margin-bottom as the gap above the second row",
                        dimenPx("swarm_space_14"),
                        (list.getChildAt(1).layoutParams as ViewGroup.MarginLayoutParams).topMargin,
                    ),
                ),
            ),
        )
    }

    // ---- the working bar --------------------------------------------------

    /**
     * `.workbar` appears on Working rows and nowhere else -- Working's own liveness affordance.
     *
     * IT USED TO BE HALF OF THE STORY, THE DOT GLOW BEING THE OTHER HALF. Ruling R8 (2026-08-09)
     * retired the Working dot's glow -- "a working session already has its bar; glowing it too
     * dilutes the only 'look here' signal the inbox has" -- so the workbar is now Working's WHOLE
     * static affordance, and NeedsInput's dot glow is not paired with anything of its own.
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
                        px(KitOrigin.cssDp(".workbar", "height")).roundToInt(),
                        bar.layoutParams.height,
                    ),
                    Claim(
                        "`.workbar` margin-top",
                        dimenPx("swarm_space_2"),
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
                listOf(Claim("glow", KitOrigin.overTransparent(att, 0.50f), att)),
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
        assertTrue(
            "a dot that grew by its glow radius and gave nothing back passes the footprint " +
                "comparison, so the halo could be paid for out of the row's spacing",
            mismatches(
                listOf(
                    Claim(
                        "footprint",
                        corePx,
                        corePx + 2 * px(KitOrigin.cssFirstPx(".pdot.att", "box-shadow")).roundToInt(),
                    ),
                ),
            ).isNotEmpty(),
        )

        // The glow reader must answer the four Groups DIFFERENTLY, or the claims it feeds hold
        // over one value repeated -- which is what reading them off Kit.groupGlow amounted to.
        // Ruling R8 (2026-08-09) dropped the glowing count from two to one, so "differently" now
        // means "finds the one glow and nothing else", which the null checks below exercise.
        val glows = KitOrigin.groupTokens().values.map { KitOrigin.dotGlow(it) }
        assertEquals(
            "the design reader finds a halo on a number of Groups other than one",
            1,
            glows.count { it != null },
        )
        assertNotEquals(
            "the design reader finds no halo on NeedsInput at all, so the one glow the maquette " +
                "draws would go unread",
            null,
            KitOrigin.dotGlow(KitOrigin.groupToken("needs_input"))?.colour,
        )
        assertNull(
            "the design reader finds a halo on Working, which ruling R8 retired -- the maquette's " +
                "`.sdot.work` declares no `box-shadow` at all",
            KitOrigin.dotGlow(KitOrigin.groupToken("working")),
        )
        assertNull(
            "the design reader finds a halo on Completed, which `--p-ink3` has no `.sdot` rule " +
                "for at all",
            KitOrigin.dotGlow(KitOrigin.groupToken("completed")),
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
