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
    /**
     * The MACHINE'S own words about the refusal, for derivation row 1's mono suffix cell
     * (agents-tracker-ksvb.10).
     *
     * **THE TOAST IS THE HARD SEAM OF THAT DEMOTION AND NOT A DEAD END.** Two of the five refusal
     * sites -- `PhoneSurface.renderKillVerdict` and `SettingsSurface`'s push_prefs refusal -- reach
     * the user through this model and nothing else, and a toast is one line. Row 1 gives that line
     * a SEPARATE mono cell beside its message, which is exactly the register the demotion asks for:
     * `Mono.CodeSmall` in `--p-ink2`, not part of the sentence but appended to it. So the reason
     * travels here and lands there, rather than being spliced back into the screen's own prose at
     * the one seam with no second view to put it in.
     *
     * IT IS DEFAULTED EMPTY. `ofSuccess`, `ofUnsent` and the routed `ofRefusal` have no machine
     * reply behind them at all -- a confirmation the design wrote, a press that never reached the
     * wire, and `ErrorRouter`'s single classified sentence -- and a suffix invented for any of them
     * would be an identifier appended to a sentence nobody sealed.
     */
    val detail: String = "",
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

        /**
         * @param routed the message [ErrorRouter] produced, or the SCREEN's own sentence for a
         *  refusal the machine sealed. It is what BOTH places show.
         * @param detail the machine's own words, for the toast's mono suffix -- see [PressFeedback.detail].
         *  Defaulted empty because a routed failure has none: `ErrorRouter` writes one sentence and
         *  it is the whole of what the press knows.
         */
        fun ofRefusal(routed: String, detail: String = ""): PressFeedback =
            PressFeedback(line = routed, toast = routed, detail = detail)

        /**
         * The same refusal, WITH the remedy the router decided for it (agents-tracker-agre).
         *
         * IT IS AN OVERLOAD AND NOT A REPLACEMENT, because the three other callers of [ofRefusal]
         * hand this model a SCREEN's own sentence rather than a routed failure -- a kill the
         * machine declined, a lease it severed, a push preference it refused. Those have no
         * [Remedy] behind them at all, and requiring one would have meant inventing a row per call
         * site: the exact "a routed state whose remedy nobody chose" the taxonomy's own set-equality
         * gate exists to prevent.
         *
         * @param routed the failure the wire produced, classified. The words shown are unchanged --
         *  [RoutedError.message] is what the line and the toast have always carried.
         */
        fun ofRefusal(routed: RoutedError): PressFeedback = PressFeedback(
            line = routed.message,
            toast = routed.message,
        )

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
