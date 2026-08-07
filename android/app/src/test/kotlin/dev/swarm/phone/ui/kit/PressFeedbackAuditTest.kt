package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.drawable.StateListDrawable
import android.os.SystemClock
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.widget.ScrollView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * ADR-009 D5's LAST ROW: "Press feedback -- <= 120ms to first visible response. Slower reads as
 * latency; audited, not animated."
 *
 * IT IS THE ONE ROW OF THE REGISTER WITH NO ANIMATOR, and that is the decision rather than an
 * omission. Every other row names something that MOVES; this one names a bound on how long a
 * control may take to answer the finger, and the only answer fast enough is the platform's own
 * pressed state, applied on the ACTION_DOWN frame. A transition INTO a pressed state would spend
 * part of the budget rendering the beginning of the response, which is why the row says "audited,
 * not animated" -- so what this file does is the audit, mechanically.
 *
 * THE AUDIT ASKS THREE QUESTIONS, and only the third has a hole in it:
 *
 *  1. Does the control respond at all? A view that is not clickable never has a pressed state, so
 *     ACTION_DOWN reaches nothing. Asserted per control, over a real [MotionEvent].
 *  2. Does the PLATFORM's own deferral fit in the budget? Android does not press a clickable view
 *     immediately when it sits inside a scrolling container -- it waits `ViewConfiguration
 *     .getTapTimeout()` to see whether the gesture is a scroll. Every control in this app is
 *     inside the scaffold's scroller, so that delay IS the app's press latency, and it must fit
 *     under the register's ceiling. It does, at 100ms against 120, and this is the assertion that
 *     goes red if a platform release ever moves it.
 *  3. Does anything PAINT the pressed state? **No, and this file records that rather than
 *     implying otherwise** -- see [no_kit_control_paints_a_pressed_state_yet]. The wiring is
 *     correct and nothing is drawn through it, because the owner-signed maquette (ADR-009 D2)
 *     draws no pressed treatment for any control: it has no `:active` rule anywhere. Choosing one
 *     here -- a ladder step, a tint, an opacity -- would be inventing a design value in code,
 *     which is the exact ordering the migration plan forbids ("no token value exists until it
 *     exists in the maquette"). It is a maquette question, and it is recorded as one.
 *
 * WHY THE CONTROLS ARE WIRED WITH A LISTENER HERE. The kit does not make its own components
 * clickable: what a press DOES is the screen's (PB-DS-9), so `setOnClickListener` is called at the
 * composition site, and it is what makes a view clickable at all. These tests do exactly what
 * `triageInboxView` and the settings screens do, and then measure the platform's response to it.
 */
@RunWith(RobolectricTestRunner::class)
class PressFeedbackAuditTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /**
     * Every interactive control the kit builds, wired the way a screen wires it.
     *
     * IT IS A CLAIM AND NOT A SURVEY, in `s23TouchTargets`' sense: each entry is a control a user
     * presses, and a kit component missing from it is a control this audit does not cover. The
     * three that are NOT here are here in prose instead, so the omission is reviewed rather than
     * silent: `toggle` is never pressed on its own (row 15 makes the whole settings row the
     * target, and the row is in this table), `statusDot` and `workingBar` are marks rather than
     * controls, and `textField` answers a focus rather than a press -- its response is the
     * champagne focus ring, which is PB-DS-7's row and FocusRingTest's subject.
     */
    private fun controls(): Map<String, View> = mapOf(
        "ctaButton(APPROVE)" to ctaButton(context, "Approve", CtaKind.APPROVE),
        "ctaButton(DENY)" to ctaButton(context, "Deny", CtaKind.DENY),
        "ctaButton(MORE)" to ctaButton(context, "Take control", CtaKind.MORE),
        "denyChip" to denyChip(context, "Revoke"),
        "filterChip" to filterChip(context, "All machines", selected = false, present = null),
        "sessionRow" to sessionRow(
            context, "quanthome/api", "claude", "Wants to run something", "working",
            lit = false, promoted = false,
        ),
        "settingsRow" to settingsRow(context, "Notifications"),
        "machineRow" to machineRow(context, "nathans-mbp", "online", mark = PresenceMark.ONLINE),
    ).mapValues { (_, view) -> view.apply { setOnClickListener { } } }

    /** One ACTION_DOWN at the view's own origin, as the platform delivers it. */
    private fun pressDown(view: View) {
        val now = SystemClock.uptimeMillis()
        val event = MotionEvent.obtain(now, now, MotionEvent.ACTION_DOWN, 0f, 0f, 0)
        view.dispatchTouchEvent(event)
        event.recycle()
    }

    @Test
    fun every_interactive_kit_control_answers_the_down_event_on_the_same_frame() {
        val late = controls().filterNot { (_, view) ->
            pressDown(view)
            view.isPressed
        }.keys
        assertEquals(
            "these controls did not enter their pressed state on ACTION_DOWN. D5 gives a press " +
                "120ms to its first visible response, and a control with no pressed state has no " +
                "response to give at any latency -- the finger lands on something that does not " +
                "acknowledge it until the machine answers, which is a network round trip",
            emptySet<String>(),
            late,
        )
    }

    @Test
    fun a_view_that_no_screen_wired_does_not_pretend_to_respond() {
        // NEGATIVE CONTROL for the test above, through the same dispatch. `isPressed` is a
        // property of a CLICKABLE view; if it were set on anything the event reached, the
        // assertion above would pass over a kit that had no controls in it at all.
        val unwired = ctaButton(context, "Approve", CtaKind.APPROVE)
        pressDown(unwired)
        assertFalse(
            "an unwired view must not report a pressed state, or the audit above measures nothing",
            unwired.isPressed,
        )
    }

    @Test
    fun the_platforms_own_deferral_inside_a_scroller_fits_the_registers_ceiling() {
        // THE DELAY THAT ACTUALLY SHIPS. Every screen in this app is hosted in the scaffold's
        // scroller, and Android defers the pressed state of a clickable view inside a scrolling
        // container by `getTapTimeout()` so a scroll does not light up whatever it started on.
        // That deferral IS this app's press latency, and D5's row is the bound on it.
        val tapTimeout = ViewConfiguration.getTapTimeout().toLong()
        assertTrue(
            "the platform defers a press inside a scroller by ${tapTimeout}ms, and ADR-009 D5 " +
                "allows ${Motion.PRESS_RESPONSE_CEILING_MS}ms to the first visible response. " +
                "This is not a number this app can tune -- if a platform release moves it past " +
                "the ceiling, the register is what must be re-decided, and the ONE alternative " +
                "the framework offers is a control that opts out of the deferral, which lights " +
                "up under every scroll that starts on it",
            tapTimeout <= Motion.PRESS_RESPONSE_CEILING_MS,
        )
    }

    @Test
    fun a_control_inside_a_scroller_is_the_case_the_ceiling_is_about() {
        // The other half of the test above, measured rather than assumed: the same control that
        // presses instantly on its own does NOT press instantly inside a scroller. Without this,
        // the tap-timeout assertion would be a fact about the platform with no bearing on this
        // app -- and the fact that it holds here is what makes it one.
        val control = ctaButton(context, "Approve", CtaKind.APPROVE).apply { setOnClickListener { } }
        ScrollView(context).addView(control)
        pressDown(control)
        assertFalse(
            "a clickable view inside a scrolling container must defer its pressed state; if the " +
                "platform stopped doing that, the ceiling assertion beside this one would be " +
                "guarding a delay the app no longer pays",
            control.isPressed,
        )
    }

    /**
     * THE RESIDUAL, ASSERTED RATHER THAN COMMENTED: nothing paints the pressed state.
     *
     * This test PASSES over the app as it is, and what it asserts is a hole. Every control above
     * enters `isPressed` on the down frame, and every one of them is painted by a
     * [SubstrateSurface] -- a `LayerDrawable` with no state list in it -- so the state changes and
     * the pixels do not.
     *
     * WHY IT IS NOT FIXED HERE. A pressed treatment is a design value: which way the surface
     * moves, and by how much. ADR-009 D2 makes `docs/research/obsidian-maquette.html` the
     * normative design source, and it draws no pressed state for any control -- there is no
     * `:active` rule in the file. The superseded Substrate mock had one (`.srow:active
     * { background: #2c2c2e }`) and that value is a pre-skin iOS grey that is not on Obsidian's
     * warm ladder at all. Choosing a replacement in Kotlin is exactly the "invent rather than
     * transcribe" the whole token regime exists to prevent, and the migration plan's ordering rule
     * says so in one line: no value exists until it exists in the maquette.
     *
     * WHY IT IS AN ASSERTION AND NOT A COMMENT. A comment saying "we know" decays into a comment
     * nobody reads. This fails in BOTH directions and both are useful: the day a pressed treatment
     * is drawn in the maquette and implemented here, this test goes red, and whoever implemented
     * it must come and delete the row they closed -- so the record of what the press audit found
     * cannot drift away from what the app does.
     */
    @Test
    fun no_kit_control_paints_a_pressed_state_yet() {
        val painted = controls().filter { (_, view) -> view.background is StateListDrawable }.keys
        assertEquals(
            "a control now paints a pressed state, which is an improvement and makes this record " +
                "wrong. Delete its row from this test, assert the treatment against the maquette " +
                "rule that introduced it, and say in ADR-009's D5 press row what the treatment is.",
            emptySet<String>(),
            painted,
        )
    }
}
