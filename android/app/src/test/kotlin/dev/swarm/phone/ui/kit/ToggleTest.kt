package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Color
import android.provider.Settings
import android.util.TypedValue
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 4.
 *
 * THE TOGGLE IS THE COMPONENT IN TWO DOCUMENTS' BLIND SPOTS, and both gaps show up here. Substrate
 * draws no `.toggle` rule at all, so row 4 is the whole specification -- there is no CSS for
 * [KitOrigin] to resolve, which is why every other appearance suite in this package can name a
 * selector and this one cannot. And the artifact's own `prefers-reduced-motion` list omits the
 * toggle (`.g-work, .banner, .sheet, .stream-caret`), which ADR-007 B134 decision 3 reads as an
 * omission rather than an exclusion -- so the 150 ms below is routed through [Motion] like every
 * other movement in the app, and the reduced-motion assertion is what makes that mean something.
 *
 * WHAT THIS LANE CAN RESOLVE FROM THE ORIGIN AND WHAT IT CANNOT, stated up front because the split
 * is the whole reason two lanes exist:
 *
 *   - THE COLOURS ARE FULLY RESOLVED. `--p-hero`, `--p-ok` and `--p-ink` come out of the token
 *     origin through [KitOrigin.token], and the off track's blend is checked against
 *     [KitOrigin.overTransparent] -- an independent implementation of the premultiplied form -- so
 *     an un-premultiplied blend (which darkens the RGB on its way to transparent and still reads as
 *     "a dimmer --p-ink3" in a diff) fails here.
 *   - THE OFF TRACK'S 40% SHARE IS NOT RESOLVED HERE, and there is no way to make it so from this
 *     classpath: `internal/design/derive.go` holds it and `docs/design/substrate-components.md`
 *     states it, and neither is staged as a unit-test resource. That join is
 *     `android/gate/s23_kit_test.go`'s, where `origin: derivation toggle-track-off` above the share
 *     is recomputed against `design.Derivations()`. What is asserted here instead is the SHAPE of
 *     the blend at whatever share the kit resolved, which is the half this lane can see.
 *   - ROW 4's 24 AND 18 ARE NOT RESOLVED HERE EITHER, for the same reason row 8's padding is read
 *     in the Go lane by [EmptyStateTest]'s own account: the derivation table is not on this
 *     classpath. `derived: ... #4 Toggle { thumb }` and `{ travel }` are recomputed from the row by
 *     the Go gate. What is asserted here is row 4's ARITHMETIC over them -- track height is
 *     `thumb + inset + inset` and width is `thumb + travel + inset + inset`, with the inset spent
 *     from the scale -- which holds whatever the two leaves turn out to be.
 *
 * THE >=48 dp TOUCH TARGET IS NOT SET, and the reason is [textField]'s exactly: row 4 writes
 * ">=48", rows 10, 13, 15 and 22 write the same, and this package's annotation grammar cannot read
 * a value behind a `>=`. It is a WCAG floor rather than a design value. Row 15 says where it lands
 * instead -- "the whole row is one >=48 dp target when it carries a toggle" -- so the target is the
 * settings row's and the toggle is the 46x28 visual inside it. Recorded so the absence reads as a
 * known boundary rather than as an oversight.
 */
@RunWith(RobolectricTestRunner::class)
class ToggleTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    @Before
    fun startFromUnreducedMotion() {
        // A prior test's zero scale must not leak into this one -- see MotionTest, same reason.
        setAnimatorScale(1f)
    }

    private fun setAnimatorScale(scale: Float) {
        Settings.Global.putFloat(
            context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, scale,
        )
    }

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    /** The design's px is Android dp at 1:1 -- the artifact is a 386x812 frame at device scale. */
    private fun dpPx(value: Float): Int = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    ).roundToInt()

    private val description = "End-to-end encryption"

    private fun off() = toggle(context, checked = false, description = description)

    private fun on() = toggle(context, checked = true, description = description)

    private fun trackOf(subject: ToggleSwitch) = subject.background as TogglePill

    private fun thumbOf(subject: ToggleSwitch) = subject.kitRequire(KitTag.TOGGLE_THUMB)

    private fun thumbFillOf(subject: ToggleSwitch) = thumbOf(subject).background as TogglePill

    /**
     * Row 4: "On = `--p-hero`, not `--p-ok`."
     *
     * The negative half is the whole reason the row spells it out. After B134 `--p-ok` carries
     * ReadyForReview, and a control's on-state is not a status -- a green toggle and a green dot on
     * the inbox would be the same colour saying two unrelated things. `.chip.on` is the precedent
     * the row cites: hero fill is what "engaged" looks like in this skin.
     */
    @Test
    fun `the on track is the hero fill and not the ok green`() {
        val hero = KitOrigin.token("--p-hero")

        assertEquals(mismatches(listOf(Claim("row 4 on track", hero, trackOf(on()).colour))).joinToString("\n"),
            emptyList<String>(),
            mismatches(listOf(Claim("row 4 on track", hero, trackOf(on()).colour))))
        assertNotEquals(
            "the on track is --p-ok, which after B134 is ReadyForReview's colour -- a status " +
                "colour on a control that reports no status",
            KitOrigin.token("--p-ok"),
            trackOf(on()).colour,
        )
    }

    /**
     * Row 4: track off is `--p-ink3` at 40%, over transparent.
     *
     * TWO THINGS ARE ASSERTED AND THE SECOND IS THE ONE THAT CATCHES A REAL MISTAKE. That the track
     * is translucent at all rules out the obvious implementation, a flat `--p-ink3` fill. That its
     * RGB is the token's UNTOUCHED at the alpha it resolved rules out the un-premultiplied blend --
     * which produces a plausible, darker colour that survives review because it still reads as a
     * dimmer version of the token.
     */
    @Test
    fun `the off track is the tertiary ink blended the premultiplied way`() {
        val ink3 = KitOrigin.token("--p-ink3")
        val resolved = trackOf(off()).colour
        val share = Color.alpha(resolved) / 255f

        val claims = listOf(
            Claim("row 4 off track over transparent", KitOrigin.overTransparent(ink3, share), resolved),
        )

        assertTrue(
            "the off track is fully opaque, so it is a flat --p-ink3 rather than a share of it: " +
                "row 4 states 40% and the 1.48:1 it measures is the blended value's, not the token's",
            Color.alpha(resolved) < 255,
        )
        assertNotEquals(
            "the off track IS --p-ink3, so no blend happened at all",
            ink3,
            resolved,
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** Row 4: "thumb `--p-ink` both states" -- the thumb's position carries the state, not its ink. */
    @Test
    fun `the thumb is the primary ink in both states`() {
        val ink = KitOrigin.token("--p-ink")
        val claims = listOf(
            Claim("row 4 thumb, off", ink, thumbFillOf(off()).colour),
            Claim("row 4 thumb, on", ink, thumbFillOf(on()).colour),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 4's geometry, as the arithmetic the row states rather than as its two resolved numbers.
     *
     * The row writes "track 46x28, thumb 24, inset `space_2`, travel 18", and 28 and 46 are the sums
     * of the other three: `24 + 2 + 2` and `24 + 18 + 2 + 2`. Asserting the sums rather than the
     * literals is what makes this independent of the component -- the inset comes from the resource
     * table, and the two leaves are joined to the row by the Go gate, so a transcription error in
     * either shows up there rather than being echoed here.
     */
    @Test
    fun `the track is the thumb plus its inset on both sides, and the travel`() {
        val subject = off()
        val inset = dimenPx("swarm_space_2")
        val thumb = thumbOf(subject).layoutParams
        val track = subject.layoutParams

        val claims = listOf(
            Claim("row 4 track height", thumb.height + inset + inset, track.height),
            Claim("row 4 track width", thumb.width + travelPx() + inset + inset, track.width),
            Claim("row 4 thumb is square", thumb.height, thumb.width),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 4's pill exception: "radius = half the track (14) and half the thumb (12)".
     *
     * Substrate's shape ladder has no pill step and a squared track reads as a checkbox, which is
     * the argument the row makes for the exception. Both halves are asserted because they are two
     * different radii on two different drawables, and getting one right says nothing about the other.
     */
    @Test
    fun `the pill radius is half the track and half the thumb`() {
        val subject = off()
        val claims = listOf(
            Claim("row 4 track radius", subject.layoutParams.height / 2f, trackOf(subject).radiusPx),
            Claim(
                "row 4 thumb radius",
                thumbOf(subject).layoutParams.height / 2f,
                thumbFillOf(subject).radiusPx,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** Row 4: the thumb starts at the track's inset and slides by the travel, nowhere else. */
    @Test
    fun `the thumb rests at each end of its travel`() {
        val claims = listOf(
            Claim("row 4 thumb at rest, off", 0f, thumbOf(off()).translationX),
            Claim("row 4 thumb at rest, on", travelPx().toFloat(), thumbOf(on()).translationX),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 4: "150 ms", and B134 decision 3's routing.
     *
     * BOTH PARTS MOVE AND BOTH ARE CHECKED. A toggle whose thumb slid while its track jumped would
     * pass an assertion that only looked at the thumb, and it is the crossfade that carries the
     * state change on a control whose thumb travels 18 dp.
     */
    @Test
    fun `flipping the toggle slides the thumb and crosses the track together`() {
        val subject = off()
        val motion = subject.moveTo(checked = true)

        assertEquals("row 4 moves two things: the thumb and the track", 2, motion.size)
        motion.forEach {
            assertEquals(
                "row 4 states 150 ms; Motion.TOGGLE_DURATION_MS is where that number lives",
                Motion.TOGGLE_DURATION_MS,
                it.duration,
            )
            it.end()
        }

        val claims = listOf(
            Claim("thumb after the move", travelPx().toFloat(), thumbOf(subject).translationX),
            Claim("track after the move", KitOrigin.token("--p-hero"), trackOf(subject).colour),
            Claim("state after the move", true, subject.checked),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * ADR-007 B134 decision 3: the toggle inherits reduced-motion coverage from [Motion].
     *
     * THE END STATE STILL HAS TO LAND. "Reduced motion" is not "the control stops working" -- a
     * toggle that collapsed its duration and left the thumb where it was would be a switch that
     * does nothing for the users who asked for less movement. The sibling above, which leaves the
     * platform setting alone and expects the full 150 ms, is this assertion's negative control:
     * without it an implementation that always built a zero-duration animator would pass here.
     */
    @Test
    fun `reduced motion collapses the duration and still lands the end state`() {
        setAnimatorScale(0f)
        val subject = off()
        val motion = subject.moveTo(checked = true)

        motion.forEach {
            assertEquals(
                "an animator built under ANIMATOR_DURATION_SCALE 0 still carries its full " +
                    "duration, so the toggle bypassed Motion's reduced-motion check",
                0L,
                it.duration,
            )
            it.end()
        }

        val claims = listOf(
            Claim("thumb under reduced motion", travelPx().toFloat(), thumbOf(subject).translationX),
            Claim("track under reduced motion", KitOrigin.token("--p-hero"), trackOf(subject).colour),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * PB-DS-12: every non-text control has a content description.
     *
     * The toggle is the extreme case of it -- four dp of moving thumb and no text anywhere -- so a
     * screen reader gets nothing at all from the visual. The words are the screen's (PB-DS-9); what
     * the kit owes is that they arrive and that the empty string is never what arrives instead,
     * which is the platform's idiom for "decorative, skip me".
     */
    @Test
    fun `the toggle announces the description it was given`() {
        assertEquals(description, off().contentDescription)
        assertEquals(
            listOf<String?>(null),
            listOf(announcementFault(off().contentDescription, null)),
        )
    }

    /**
     * The negative control PB-DS-10 requires, through the SAME comparison every assertion above
     * uses: one unit of colour, one pixel of geometry, one state.
     *
     * A control that rebuilt the comparison inline would prove the copy works and say nothing about
     * the assertion. Every claim above goes through [mismatches], and so does every perturbation
     * here.
     */
    @Test
    fun `the toggle assertions can actually fail`() {
        val hero = KitOrigin.token("--p-hero")
        val ink3 = KitOrigin.token("--p-ink3")
        val inset = dimenPx("swarm_space_2")
        val thumb = thumbOf(off()).layoutParams.height

        assertTrue(
            "a track colour one unit from the origin's passes the comparison",
            mismatches(listOf(Claim("on track", hero, hero + 1))).isNotEmpty(),
        )
        assertTrue(
            "a track one pixel taller than thumb + inset + inset passes the comparison",
            mismatches(listOf(Claim("track height", thumb + inset + inset, thumb + inset + inset + 1)))
                .isNotEmpty(),
        )
        assertTrue(
            "spending ONE inset where row 4's sum states two passes the comparison, which is the " +
                "mistake a reader who skimmed `track 46x28` would make",
            mismatches(listOf(Claim("track height", thumb + inset + inset, thumb + inset))).isNotEmpty(),
        )
        assertTrue(
            "a thumb that never moved passes the travel comparison",
            mismatches(listOf(Claim("thumb", travelPx().toFloat(), 0f))).isNotEmpty(),
        )
        assertTrue(
            "a toggle left in its old state passes the state comparison",
            mismatches(listOf(Claim("checked", true, false))).isNotEmpty(),
        )
        assertNotEquals(
            "--p-ok compares equal to --p-hero, so row 4's argument for the hero fill is about " +
                "nothing and the on-state assertion would accept either",
            KitOrigin.token("--p-ok"),
            hero,
        )
        assertNotEquals(
            "the premultiplied blend of --p-ink3 compares equal to the flat token, so the off " +
                "track assertion would accept an unblended fill",
            ink3,
            KitOrigin.overTransparent(ink3, Color.alpha(trackOf(off()).colour) / 255f),
        )
    }

    /**
     * The travel, in pixels, as the kit spends it.
     *
     * It is read from the component's own metric rather than typed, and it is the one leaf in this
     * file that is: row 4's 18 lives in `docs/design/substrate-components.md`, which is not on this
     * classpath, and `android/gate/s23_kit_test.go` recomputes the constant from that row. Named
     * here so the dependency is visible at every call site rather than buried in one.
     */
    private fun travelPx(): Int = dpPx(KitMetrics.TOGGLE_TRAVEL_DP)
}
