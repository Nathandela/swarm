package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.ScrollView
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.abs

/**
 * FAILING-FIRST (TDD RED, GG-5) for the screen an unpaired phone opens on, AS DRAWN.
 *
 * WHAT THIS SCREEN IS. One offer to pair, on the ground, and nothing else -- no tab bar, no triage
 * inbox, no empty sections announcing that nothing is waiting on a user who has no machine to be
 * waited on by. Taking up the offer puts the EXISTING pairing flow on this same screen, full width,
 * where a person looking for it is already looking. It ceases to exist the moment a machine is
 * pinned; see [PairOnlyScreenTest] for the decision and why an unreadable state answers PAIR_ONLY.
 *
 * ## The two assertions that are the whole point
 *
 * **EXACTLY ONE CONTROL.** The screen that shipped had the pairing block below four section
 * headings, a scope bar, a nav header and a launch form, and the owner could not find it on a real
 * handset. A hero CTA drawn ABOVE that same column would be an improvement and would not be this
 * requirement: what makes an offer unmissable is that there is nothing else to look at. The count
 * below is over the whole hierarchy, so a control added anywhere on this screen fails it.
 *
 * **THE FLOW REPLACES THE OFFER RATHER THAN STACKING ABOVE IT.** `PairingSurface` composes its own
 * heading and its own controls per step -- the SCAN step already offers "Scan the code on your
 * machine" -- so a hero CTA left on screen beside it is a second way in to the same step, one of
 * which the surface does not own. Two entry points to a security flow is one more than the flow was
 * argued with.
 *
 * WHAT IS NOT ASSERTED HERE: appearance, for `PairingPanelViewTest`'s reason, and specifically NOT
 * the ground. `--p-bg` is the theme's `android:colorBackground` (`SwarmTheme.EXPECTED_DARK_COLORS`)
 * and reaches this screen as the window's own background; `android/gate/s24_screens_test.go` fences
 * this package against `background =`, so a screen painting its own ground would fail the build.
 * The ground is therefore something this screen must NOT do, and the fence is where that is checked.
 */
@RunWith(RobolectricTestRunner::class)
class PairOnlyViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /**
     * The pairing flow, as a stand-in that CARRIES A CONTROL.
     *
     * It is clickable on purpose: the real flow is `PairingSurface.root`, which offers up to eight
     * controls of its own, so a stub with none would let a composition that hosts the flow before
     * anyone asked for it still pass the one-control count below.
     */
    private fun flow(): View = View(context).apply { setOnClickListener {} }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    /** Everything on this screen a finger can press. */
    private fun View.controls(): List<View> = flatten().filter { it.hasOnClickListeners() }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the offer ---------------------------------------------------------

    @Test
    fun `an unpaired phone is offered exactly one control`() {
        val root = pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {})

        assertEquals(
            "the unpaired screen offers ${root.controls().size} controls. It is the only screen " +
                "this phone has and there is exactly one thing a person can do on it",
            1,
            root.controls().size,
        )
        assertEquals(
            "the one control on the screen is not the pairing offer",
            listOf(root.kitRequire(PairOnlyTag.CTA)),
            root.controls(),
        )
    }

    @Test
    fun `the offer draws no tab bar and no inbox`() {
        val root = pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {})

        assertNull(
            "the unpaired screen carries the tab bar, so it is the four-tab shell again -- and " +
                "three of those tabs lead to screens that read a roster this phone has no " +
                "connection to fill",
            root.kitFind(ScaffoldTag.TABS),
        )
        assertNull(root.kitFind(KitTag.TAB_LABEL))
        for (part in listOf(
            InboxTag.NAV,
            InboxTag.SCOPES,
            InboxTag.SECTION_LABEL,
            InboxTag.SECTION_EMPTY,
            InboxTag.ROW,
        )) {
            assertNull(
                "the unpaired screen draws `$part`. The triage inbox states what is waiting on " +
                    "the user, and a phone with no machine pinned to it is in no position to " +
                    "make that claim -- its four always-drawn section headings are what buried " +
                    "the pairing block in the first place",
                root.kitFind(part),
            )
        }
    }

    @Test
    fun `the pairing flow is not on screen until someone asks for it`() {
        val pairing = flow()
        val root = pairOnlyView(context, pairing = pairing, started = false, onStartPairing = {})

        assertTrue(
            "the pairing flow is in the hierarchy before the offer was taken up, so the screen " +
                "opens on a camera permission prompt and a paste field rather than on one offer",
            pairing !in root.flatten(),
        )
    }

    @Test
    fun `pressing the offer is what starts the pairing flow`() {
        var started = false
        val root = pairOnlyView(
            context,
            pairing = flow(),
            started = false,
            onStartPairing = { started = true },
        )

        root.kitRequire(PairOnlyTag.CTA).performClick()

        assertTrue(
            "the offer has no handler behind it, so the one control on the only screen an " +
                "unpaired phone has does nothing when pressed",
            started,
        )
    }

    @Test
    fun `Signal Field belongs to first run and leads the offer`() {
        val root = pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {})
        val order = root.flatten().mapNotNull { it.tag as? String }

        assertNotNull("the approved atmospheric swarm mark is absent", root.kitFind(PairOnlyTag.SIGNAL_FIELD))
        assertTrue(
            "the atmospheric field does not introduce the welcome story",
            order.indexOf(PairOnlyTag.SIGNAL_FIELD) < order.indexOf(PairOnlyTag.TITLE),
        )
        assertEquals("the mark accidentally became a second control", 1, root.controls().size)

        for (reason in listOf(PairOnlyReason.REVOKED, PairOnlyReason.REPAIR_REQUIRED)) {
            val recovery = pairOnlyView(
                context,
                pairing = flow(),
                started = false,
                onStartPairing = {},
                copy = PairOnlyScreen.copyFor(reason),
            )
            assertNull("$reason inherited first-run artwork", recovery.kitFind(PairOnlyTag.SIGNAL_FIELD))
        }

        val started = pairOnlyView(context, pairing = flow(), started = true, onStartPairing = {})
        assertNull("the welcome artwork survives over the security flow", started.kitFind(PairOnlyTag.SIGNAL_FIELD))
    }

    @Test
    fun `Signal Field centres the welcome while a short handset still scrolls to its action`() {
        fun laidOut(widthDp: Int, heightDp: Int): ScrollView {
            val density = context.resources.displayMetrics.density
            return (pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {}) as ScrollView)
                .apply {
                    val width = (widthDp * density).toInt()
                    val height = (heightDp * density).toInt()
                    measure(
                        View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
                        View.MeasureSpec.makeMeasureSpec(height, View.MeasureSpec.EXACTLY),
                    )
                    layout(0, 0, width, height)
                }
        }

        fun View.topInside(ancestor: View): Int {
            var result = top
            var current = parent
            while (current is View && current !== ancestor) {
                result += current.top
                current = current.parent
            }
            return result
        }

        for (width in listOf(360, 400)) {
            val root = laidOut(width, 800)
            val mark = root.kitRequire(PairOnlyTag.SIGNAL_FIELD)
            val cta = root.kitRequire(PairOnlyTag.CTA)
            val groupMiddle = (mark.topInside(root) + cta.topInside(root) + cta.height) / 2
            val tolerance = (24 * context.resources.displayMetrics.density).toInt()

            assertTrue(
                "the $width dp Signal Field welcome is top-heavy instead of centred: " +
                    "group midpoint=$groupMiddle viewport midpoint=${root.height / 2}",
                abs(groupMiddle - root.height / 2) <= tolerance,
            )
        }

        val short = laidOut(360, 360)
        assertTrue("the compact welcome no longer fills the viewport", short.isFillViewport)
        assertTrue(
            "the short welcome clips instead of becoming scrollable",
            short.getChildAt(0).measuredHeight > short.measuredHeight,
        )
        short.fullScroll(View.FOCUS_DOWN)
        assertTrue("the pairing action cannot be reached by scrolling", short.scrollY > 0)
    }

    // ---- the flow ----------------------------------------------------------

    @Test
    fun `the flow is hosted on this same screen once it has begun`() {
        val pairing = flow()
        val root = pairOnlyView(context, pairing = pairing, started = true, onStartPairing = {})

        assertTrue(
            "the pairing flow is not on screen after the offer was taken up, so the CTA leads " +
                "nowhere",
            pairing in root.flatten(),
        )
        assertNotNull(
            "the composition does not name where the flow is hosted, so nothing can find it but " +
                "a walk over child indices",
            root.kitFind(PairOnlyTag.PAIRING),
        )
    }

    @Test
    fun `the flow replaces the offer rather than stacking under it`() {
        val root = pairOnlyView(context, pairing = flow(), started = true, onStartPairing = {})

        assertNull(
            "the hero CTA is still on screen beside the running flow, so there are two ways to " +
                "start scanning -- and only one of them is the surface's own SCAN control",
            root.kitFind(PairOnlyTag.CTA),
        )
        for (part in listOf(PairOnlyTag.TITLE, PairOnlyTag.BODY)) {
            assertNull(
                "the offer's `$part` survives above the running flow, which draws its own " +
                    "heading and its own sentence for every step it is in",
                root.kitFind(part),
            )
        }
    }

    // ---- the words ---------------------------------------------------------

    @Test
    fun `the words on the offer are the screen model's and not the view's`() {
        val root = pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {})

        // ASSERTED AS AN IDENTITY, which is `PairingPanelScreenTest`'s discipline: copy belongs to
        // the screen model (PB-DS-9), and a suite that transcribed the sentences here would agree
        // with a re-worded copy of them.
        assertEquals(PairOnlyScreen.TITLE, textOf(root.kitFind(PairOnlyTag.TITLE)))
        assertEquals(PairOnlyScreen.BODY, textOf(root.kitFind(PairOnlyTag.BODY)))
        assertEquals(PairOnlyScreen.CTA, textOf(root.kitFind(PairOnlyTag.CTA)))

        for (copy in listOf(PairOnlyScreen.TITLE, PairOnlyScreen.BODY, PairOnlyScreen.CTA)) {
            assertTrue(
                "the only screen an unpaired phone has carries an empty string where a sentence " +
                    "should be",
                copy.isNotBlank(),
            )
        }
    }

    /**
     * agents-tracker-w6o3: the reason reaches the screen, or the model decided it for nobody.
     *
     * [PairOnlyTerminalReasonTest] argues WHAT each terminal state says. This is the other half and
     * the one the defect actually was: a phone whose owner revoked it opened on the fresh-install
     * screen because nothing between the transport and this composition carried the reason. A model
     * that answers correctly into a view drawing three constants is the same screen it was.
     */
    @Test
    fun `the reason the pairing ended is what the screen says`() {
        for (reason in PairOnlyReason.entries) {
            val copy = PairOnlyScreen.copyFor(reason)
            val root = pairOnlyView(
                context,
                pairing = flow(),
                started = false,
                onStartPairing = {},
                copy = copy,
            )

            assertEquals(
                "the screen draws its own heading over a phone in $reason, so the model's " +
                    "decision reaches nobody",
                copy.title,
                textOf(root.kitFind(PairOnlyTag.TITLE)),
            )
            assertEquals(
                "the screen draws the first-run sentence over a phone in $reason -- the defect " +
                    "this issue is, one level further out",
                copy.body,
                textOf(root.kitFind(PairOnlyTag.BODY)),
            )
            assertEquals(copy.cta, textOf(root.kitFind(PairOnlyTag.CTA)))
        }
    }
    /** Phone refit W5.1: a command the screen points at is a mono well under the sentence. */
    @Test
    fun `a command the screen points at is drawn in a well under the sentence`() {
        val copy = PairOnlyScreen.copyFor(PairOnlyReason.REPAIR_REQUIRED)
        val repair = pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {}, copy = copy)
        assertEquals(copy.command, textOf(repair.kitFind(PairOnlyTag.COMMAND)))

        val revoked = pairOnlyView(
            context,
            pairing = flow(),
            started = false,
            onStartPairing = {},
            revokedNotice = PairOnlyScreen.revokeNoticeFor(CommandVerdict.UNANSWERED),
            revokedCommand = PairOnlyScreen.REVOKE_COMMAND,
        )
        assertEquals(PairOnlyScreen.REVOKE_COMMAND, textOf(revoked.kitFind(PairOnlyTag.COMMAND)))

        val plain = pairOnlyView(context, pairing = flow(), started = false, onStartPairing = {})
        assertNull("a screen with no command to run drew a well", plain.kitFind(PairOnlyTag.COMMAND))
    }
}
