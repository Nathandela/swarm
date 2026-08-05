package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for what a press's answer puts on screen, now that there is a
 * component that can carry it.
 *
 * THE DEFECT THIS MODEL EXISTS FOR, in `PhoneSurface.dispatchPress`'s own words: the outcome line
 * is cleared to `""` before the verb is dispatched, because it holds the LAST command's answer and
 * a stale answer under a press in flight reads as this press's. Success then leaves it cleared --
 * so "still crossing" and "done" are the same empty line, and a refusal lands at the BOTTOM OF ONE
 * TAB while the press that produced it happened somewhere else entirely.
 *
 * **THE REFUSAL GOES TO BOTH PLACES AND THAT IS THE DECISION.** A toast is gone in 3200 ms; a
 * routed error frequently names a remedy the user has to act on ("try again once the connection is
 * back"), and a remedy that scrolls past in three seconds is worse than one that sits still. So the
 * line KEEPS the message -- nothing existing is removed -- and the toast is what puts it in front
 * of the eye that was looking at the control.
 *
 * **SUCCESS IS SILENT UNLESS THE DESIGN WROTE WORDS FOR IT.** `remote-control-mock.html` fires a
 * toast for seven actions and not for the rest; where it wrote none, this model says nothing rather
 * than inventing a confirmation, because a confirmation nobody specified is copy invented at the
 * one seam PB-DS-9 exists to keep copy out of.
 */
class PressFeedbackTest {

    private val routed = "Your machine is not reachable. Try again once the connection is back."

    private val confirmation = "Interrupt sent"

    @Test
    fun `a refusal takes the persistent line AND the toast`() {
        val feedback = PressFeedback.ofRefusal(routed)

        assertEquals(
            "the routed message stopped reaching the outcome line. Nothing is removed by the " +
                "toast: a remedy the user has to act on cannot be the only copy that vanishes",
            routed,
            feedback.line,
        )
        assertEquals(
            "a refusal says nothing where the press happened, which is the whole defect -- the " +
                "outcome line is at the bottom of one tab and the control is anywhere",
            routed,
            feedback.toast,
        )
    }

    @Test
    fun `a success with copy of its own toasts it and leaves the line clear`() {
        val feedback = PressFeedback.ofSuccess(confirmation)

        assertEquals(confirmation, feedback.toast)
        assertEquals(
            "a success wrote to the outcome line. That line holds what went WRONG with the last " +
                "command, and a confirmation parked in it is read as an error by the next glance",
            "",
            feedback.line,
        )
    }

    @Test
    fun `a success the design wrote no copy for says nothing at all`() {
        val feedback = PressFeedback.ofSuccess(null)

        assertEquals(
            "a success with no design copy produced a toast. Whatever is in it was invented here",
            "",
            feedback.toast,
        )
        assertEquals("", feedback.line)
        assertTrue("silence is not silent", feedback.saysNothing)
    }

    @Test
    fun `a blank confirmation is silence rather than an empty toast`() {
        assertTrue(
            "a confirmation of \"\" produced a toast with nothing in it, which is a surface " +
                "flashing an empty box over the tab bar for 3200 ms",
            PressFeedback.ofSuccess("").saysNothing,
        )
        assertTrue(!PressFeedback.ofRefusal(routed).saysNothing)
    }
}
