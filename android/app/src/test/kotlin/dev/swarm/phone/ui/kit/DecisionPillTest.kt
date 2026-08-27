package dev.swarm.phone.ui.kit

import android.content.Context
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The one persistent affordance in the reading flow (derivation row 32).
 *
 * WHAT MAKES IT LEGITIMATE IS THE CONDITION AND NOT THE PAINT, and the condition is the screen's:
 * it is drawn only while an unanswered decision is off screen. What this suite can hold the
 * component to is that it refuses to be anything else -- no count, no dismissal, no second state,
 * and no glow.
 */
@RunWith(RobolectricTestRunner::class)
class DecisionPillTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    @Test
    fun `it says only the words the copy sheet records`() {
        assertEquals(
            "the drawing puts an arrow after the words and the copy table does not; a string not " +
                "on that sheet is not on the screen",
            "Decision needed",
            decisionPill(context, "Decision needed").text.toString(),
        )
    }

    @Test
    fun `it is the champagne fill at the ground ink`() {
        val pill = decisionPill(context, "Decision needed")
        val spec = (pill.background as SubstrateSurface).spec
        assertEquals(
            "the champagne is what \"you\" looks like in this skin, and a question the agent is " +
                "blocked on is the strongest reading of that there is",
            Kit.colour(context, R.color.swarm_hero),
            spec.fill,
        )
        assertEquals(
            "a saturated fill carries the state on its own -- `.chip.on`'s rule",
            0,
            spec.strokeWidthPx,
        )
        assertEquals(
            "a saturated fill takes the ground ink and not the linen, which is row 3's pairing",
            Kit.colour(context, R.color.swarm_hero_ink),
            pill.currentTextColor,
        )
    }

    @Test
    fun `it does not glow`() {
        val spec = (decisionPill(context, "Decision needed").background as SubstrateSurface).spec
        assertNull(
            "nothing glows unless it is alive, and ruling R8 narrowed the one glow this app has to " +
                "the single session that needs you -- a second champagne halo dilutes the first",
            spec.keyLight,
        )
        assertNull("and no rail either: this is a mark, not a row that needs you", spec.rail)
    }

    @Test
    fun `it clears the touch floor and places itself nowhere`() {
        val pill = decisionPill(context, "Decision needed")
        assertEquals(
            "it is the one control a person reaches for while the agent is working, which is when " +
                "a thumb is least careful",
            Kit.dpPx(context, KitMetrics.MIN_TARGET_DP),
            pill.minimumHeight,
        )
        val params = pill.layoutParams as LinearLayout.LayoutParams
        assertEquals("the drawing centres it, and centring is the screen's half of the fence", -1, params.gravity)
        assertEquals(
            "it hugs its words: a full-width bar over a conversation is a banner, and a banner is " +
                "what this surface just deleted",
            LinearLayout.LayoutParams.WRAP_CONTENT,
            params.width,
        )
        assertTrue("row 23", pill.isFocusable)
    }
}
