package dev.swarm.phone.ui

/**
 * What one press's answer puts on screen: the persistent line, and the toast.
 *
 * **THE DEFECT IT WAS WRITTEN FOR** is recorded in `PhoneSurface.dispatchPress`'s own KDoc. The
 * outcome line holds the LAST command's answer, so a press in flight clears it -- and a success
 * leaves it cleared, which makes "still crossing" and "done" the same empty line. Meanwhile a
 * refusal is written to a view that lives at the bottom of the Inbox tab's unrecomposed column,
 * whatever screen the control that produced it was on. Derivation row 1's toast is the component
 * that was missing; this is the decision about what goes in it.
 *
 * **A REFUSAL GOES TO BOTH PLACES.** A toast is gone in 3200 ms and a routed error routinely names
 * a remedy the user has to act on ("try again once the connection is back") -- so the line KEEPS
 * the message and the toast is what puts it in front of the eye that was on the control. Nothing
 * that was on screen before is removed.
 *
 * **A SUCCESS IS SILENT UNLESS THE DESIGN WROTE WORDS FOR IT.** `remote-control-mock.html` fires a
 * toast for seven actions and nothing for the rest, so where it wrote none this says none. A
 * confirmation invented at this seam would be copy authored in the one place PB-DS-9 keeps copy
 * out of, and "the machine did what you asked" is exactly the sentence a product invents when it
 * has nothing to say.
 *
 * IT IS A MODEL AND NOT TWO LINES INSIDE THE SURFACE because the phone core is a gomobile AAR that
 * does not load on the unit-test JVM: nothing on a surface that only runs after a verb returns can
 * be reached by a test at all. Here the decision is a value, and `PressFeedbackTest` is the whole
 * of it.
 */
data class PressFeedback(
    /** What the persistent outcome line reads afterwards. Empty leaves it cleared. */
    val line: String,
    /** What the toast says, or empty when the press has nothing to announce. */
    val toast: String,
) {

    /**
     * True when this press says nothing at all -- which is most of them.
     *
     * IT IS A NAMED QUESTION RATHER THAN `toast.isEmpty()` AT THE CALL SITE, because the call site
     * is the one that must not show an empty toast: a blank message would flash a 92 dp-high empty
     * box over the tab bar for 3.2 seconds, and `""` reaches here from any caller that has a
     * nullable confirmation.
     */
    val saysNothing: Boolean get() = toast.isEmpty()

    companion object {

        /**
         * @param confirmation the words the DESIGN gives this press, or null where it gives none.
         *  Blank is treated as none: a caller holding an empty string has nothing to say, and the
         *  alternative is an empty toast.
         */
        fun ofSuccess(confirmation: String?): PressFeedback =
            PressFeedback(line = "", toast = confirmation?.takeIf { it.isNotBlank() }.orEmpty())

        /** @param routed the message [ErrorRouter] produced. It is what BOTH places show. */
        fun ofRefusal(routed: String): PressFeedback = PressFeedback(line = routed, toast = routed)

        /**
         * A press that never reached the wire, and whose screen already carries the sentence
         * (agents-tracker-4lta).
         *
         * IT IS NOT A REFUSAL. Nothing refused an offline Stop -- the link is down, input is
         * live-only (ADR-007 D7) and the keystroke is discarded rather than sent, so no machine
         * ever saw it. The outcome line is what the MACHINE answered the last command, and putting
         * these words there would report a refusal nobody made.
         *
         * THE TOAST IS WHY IT EXISTS AT ALL. PB-INPUT-1's not-sent notice is drawn ABOVE the
         * transcript and Stop is drawn below it, so a user pressing the button at the bottom of a
         * long session log would see the screen change somewhere they are not looking. Derivation
         * row 1's toast is what puts the answer in front of the eye that was on the control.
         *
         * @param notice the screen model's own sentence for the state -- [dev.swarm.phone.ui.SessionDetail.NOT_SENT_NOTICE].
         */
        fun ofUnsent(notice: String): PressFeedback = PressFeedback(line = "", toast = notice)
    }
}
