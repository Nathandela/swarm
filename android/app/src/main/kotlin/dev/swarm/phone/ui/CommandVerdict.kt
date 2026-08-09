package dev.swarm.phone.ui

/**
 * What the MACHINE answered one command with, claimed by operation id (agents-tracker-qlf9).
 *
 * **THE DEFECT IT WAS WRITTEN FOR.** Three verbs were fire-and-forget. `kill` dropped its
 * operation id, so a refusal left the outcome line cleared and the session sitting in the inbox --
 * which is exactly what a kill that SUCCEEDED looks like one redraw before the roster catches up.
 * `take_control` kept the id and then reduced the reply to `code == "lease"`, collapsing every
 * refusal to `false` and discarding the machine's own words. `device_revoke` never read its
 * outcome at all, while purging both key tiers regardless.
 *
 * **IT IS ONE TABLE AND NOT THREE.** [LaunchScreen.resolve] already read these codes correctly and
 * is the reasoning this generalises, so it delegates here rather than keeping a second copy: a
 * policy rejection is its own answer because retrying changes nothing; a rate limit is its own
 * because waiting is the one thing that helps; and everything else the machine can seal --
 * kill_switch, not_authorized, stale_approval, invalid_field -- keeps the machine's own message,
 * because the codes are the MACHINE'S and this side must not claim to know the whole set. Folding
 * a kill-switch refusal into "policy" sends a user to change a spec that was fine.
 *
 * **WHAT IS NEW BESIDE THAT TABLE IS [CommandResult.ENDED].** `internal/remotegw/lease_sever.go`
 * seals a lease death as `protocol.OpDetach` under the take_control's OWN operation id, so an
 * ordinary session that ends leaves a `detach` on the very outcome the peek reads. A reading of
 * "anything that is not the accepting code is a refusal" would tell the user their machine refused
 * control of a session they had just been typing into.
 *
 * **THE CODES ARE WIRE OPS HELD AS LITERALS**, for [SwarmErrorTokens]'s reason: the unit-test JVM
 * does not load the gomobile AAR, so this side cannot read the Go constants. Each names the
 * constant it pins.
 */
enum class CommandResult {
    /**
     * No answer THIS operation may claim -- none yet, or one addressed to somebody else. Both are
     * the same fact about this one: nothing is known. PB-SYNC-2 keys outcomes by id precisely so a
     * screen never resolves one by proximity.
     */
    PENDING,

    /** The machine did it. Which code says so is the caller's, because it differs per verb. */
    ACCEPTED,

    /**
     * `protocol.OpDetach`: an operation the machine ACCEPTED and later ended. It is not a refusal
     * and must never be rendered as one -- see the class comment.
     */
    ENDED,

    /** `schema.CodePolicy`. The machine's considered answer; retrying changes nothing. */
    REJECTED_BY_POLICY,

    /** `schema.CodeRateLimit`. Retryable, but only after waiting. */
    REFUSED_TRANSIENTLY,

    /** Every other refusal the machine can seal: kill switch, authorization, a bad field. */
    REFUSED,
}

/**
 * One resolved (or still unresolved) command, with the machine's own reason attached.
 *
 * IT IS A VALUE AND NOT A READ ON A SURFACE, for [PressFeedback]'s reason: the phone core is a
 * gomobile AAR that does not load on the unit-test JVM, so nothing that only runs after a verb
 * returns can be reached by a test at all. Here the decision is a data class and
 * `CommandVerdictTest` is the whole of it.
 */
data class CommandVerdict(
    val result: CommandResult,
    /**
     * The machine's message, verbatim. Empty while [CommandResult.PENDING], and possibly after.
     *
     * IT IS THE DETAIL AND NO LONGER PART OF [sentence] (agents-tracker-ksvb.10). A screen shows it
     * beside the sentence rather than inside one -- `noticeDetail` on the three panel sites, the
     * toast's own mono suffix on the two that have no panel cell.
     */
    val reason: String,
    val retryable: Boolean,
) {
    /** Whether the machine has said anything at all about this operation. */
    val answered: Boolean get() = result != CommandResult.PENDING

    val accepted: Boolean get() = result == CommandResult.ACCEPTED

    /**
     * Whether the machine REFUSED it, which deliberately excludes [CommandResult.ENDED]: a lease
     * that was granted and later severed is not a take_control the machine declined.
     */
    val refused: Boolean get() = when (result) {
        CommandResult.REJECTED_BY_POLICY,
        CommandResult.REFUSED_TRANSIENTLY,
        CommandResult.REFUSED,
        -> true

        CommandResult.PENDING, CommandResult.ACCEPTED, CommandResult.ENDED -> false
    }

    /**
     * The screen's own sentence about this answer -- the WHOLE primary copy.
     *
     * **IT USED TO SPLICE [reason] INTO THE MIDDLE OF IT** (`head: reason.`), and that is what
     * agents-tracker-ksvb.10 undoes. The machine's string is a daemon Go error -- lower case,
     * parenthesised, with a wire code in front of it -- and putting it inside a sentence this
     * product authored, in this product's own body type, leaves a reader unable to tell where the
     * copy stops and the diagnostic starts. The words are NOT dropped: [reason] carries them
     * verbatim and every screen that shows one shows them in the register they belong to, which is
     * `ui/kit/Notice.kt`'s `noticeDetail` -- mono, tertiary, visibly the machine talking.
     *
     * IT IS STILL HERE AND NOT AT THREE CALL SITES, for the half that is genuinely shared: a
     * refusal that waiting fixes must not read like one that nothing fixes, and [RETRY_HINT] is a
     * property of the ANSWER rather than of any one screen.
     *
     * @param head the screen's own words for what did not happen. It is a PARAMETER because it is
     *  copy, and copy belongs to the screen (PB-DS-9): "your machine did not end this session" and
     *  "your machine refused to remove this device" are different sentences about the same shape.
     */
    fun sentence(head: String): String {
        val stated = "$head."
        return if (retryable) stated + RETRY_HINT else stated
    }

    companion object {
        /** The verdict a screen starts from: nothing issued, or nothing answered. */
        val UNANSWERED = CommandVerdict(CommandResult.PENDING, reason = "", retryable = false)

        /** `protocol.OpOK` -- what an accepted command replies. */
        const val ACCEPTED_OK = "ok"

        /**
         * What a refusal waiting would fix says about itself, spelled ONCE.
         *
         * `LaunchPanelScreen` wrote this sentence first and now reads it from here, because two
         * copies of one piece of copy is the drift PB-DS-9 assigns copy to the screen to prevent
         * -- and there is no screen that owns it: it is a property of the ANSWER, and every screen
         * that renders one appends it.
         */
        const val RETRY_HINT = " This one is worth trying again shortly."

        /** `protocol.OpDetach` -- see [CommandResult.ENDED]. */
        private const val CODE_DETACH = "detach"

        /** `schema.CodePolicy`, `schema.CodeRateLimit`. */
        private const val CODE_POLICY = "policy"
        private const val CODE_RATE_LIMIT = "rate_limit"

        /**
         * PB-SYNC-2's answer for [operationId], read as a CODE.
         *
         * @param accepted the code that means the machine DID IT, which differs per verb and is
         *  therefore asked for rather than assumed: a command replies [ACCEPTED_OK] and a
         *  take_control replies `protocol.OpLease`, so a table that knew only the first would
         *  report every granted lease as a refusal.
         */
        fun of(
            outcome: OperationOutcome,
            operationId: String,
            accepted: String,
        ): CommandVerdict {
            if (operationId.isEmpty() ||
                outcome.operationId != operationId ||
                outcome.code.isBlank()
            ) {
                return UNANSWERED
            }
            return when (outcome.code) {
                accepted -> CommandVerdict(CommandResult.ACCEPTED, outcome.message, retryable = false)
                CODE_DETACH -> CommandVerdict(CommandResult.ENDED, outcome.message, retryable = false)
                CODE_POLICY ->
                    CommandVerdict(CommandResult.REJECTED_BY_POLICY, outcome.message, retryable = false)

                CODE_RATE_LIMIT ->
                    CommandVerdict(CommandResult.REFUSED_TRANSIENTLY, outcome.message, retryable = true)

                else -> CommandVerdict(CommandResult.REFUSED, outcome.message, retryable = false)
            }
        }
    }
}
