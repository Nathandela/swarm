package dev.swarm.phone.ui.screens

/**
 * The obsidian migration plan's phase O6.1: what the pull-quote approval sheet SAYS.
 *
 * PB-DS-9 assigns copy and arrangement to the screen, so the three lines are decided here and
 * `ui/kit/ApprovalSheet.kt` only paints them. Every one of them is read off a row the inbox
 * already carries: the plan's clause is "no new information, reordered hierarchy", and a sheet
 * that computed a fourth fact would be a new screen wearing a reordering's clothes.
 *
 * ## The gap between this and the maquette, stated rather than filled in
 *
 * The maquette's frame 2 reads:
 *
 * ```
 *   swarm · claude · mbp-m1
 *   Claude wants to push the release commit to main.
 *   $ git push origin main
 *   [ Allow ]  [ Deny ]
 * ```
 *
 * Three of those five things do not exist on this product's wire, and each is recorded here
 * rather than invented -- the discipline `ActivityPanel` states at length for the same class of
 * gap, and the defect class ADR-007 B135 exists for.
 *
 *  - **The sentence.** `swarmmobile.Session.Need` is "the verbatim journal record TYPE that last
 *    touched the session" (`mobile/types.go`), which is a value like `needs_input`, and
 *    [InboxRow.need] restates the rule that binds every consumer of it: "the journal record type
 *    verbatim, never an invented phrase". So [question] is that value, VERBATIM. A table turning
 *    it into English would have to fail loudly on a type it did not know, and a server that added
 *    one would take this sheet down at the moment it is needed. **What O6.1 changes is where the
 *    line sits and how big it is** -- which is the whole of what the plan asks for -- not what it
 *    says.
 *  - **The command.** Nothing on this wire carries the literal a session is blocked on. What the
 *    phone has is the daemon-rendered terminal snapshot, which is where the command is actually
 *    printed, so [command] is that snapshot verbatim and the well keeps its meaning: the literal
 *    the machine is showing. An empty one draws no well at all.
 *  - **The actions.** THERE IS NO APPROVE VERB. `mobile/app.go` exports no approve, no deny and
 *    no answer; the way a blocked session is resolved from this phone is take-control plus
 *    send-line, and `android/unbound-verbs.tsv` has no row for an approval because there is no
 *    facade surface to ledger. So this model carries no action labels, the composition passes no
 *    action views, and the two CTAs the maquette draws are the part of this frame that waits for
 *    a protocol decision rather than a skin one.
 */
data class ApprovalSheetPanel(
    /** Who is asking: the project and the agent, joined. Never a machine name this side invents. */
    val contextLine: String,
    /** The blocking question, verbatim. See the class KDoc: this is `need` and nothing else. */
    val question: String,
    /** The literal the machine is showing. Empty when this phone has not watched the session. */
    val command: String,
) {
    /**
     * Whether there is a well to draw.
     *
     * `SessionDetailPanel.hasSnapshot` decides the same question the same way one screen over: an
     * empty well is a recessed box that says "we have nothing" in the shape of "this session has
     * a blank screen".
     */
    val hasCommand: Boolean get() = command.isNotEmpty()
}

/** The model, over the row the user tapped. */
object ApprovalSheetScreen {

    /**
     * The Group a sheet exists for. A session that is working, ready for review or done is not
     * asking anything, and a sheet over one would be a question whose answer changes nothing.
     *
     * IT IS THE MODEL'S OWN WORD AND NOT A FOURTH COPY OF THE STRING. `TriageInboxScreen` records
     * that `needs_input` was written out at three sites before it was named once; this reads the
     * row's own [InboxRow.lit], which that file's KDoc establishes as "the promotion, named by the
     * model" for exactly this reason.
     */
    fun of(row: InboxRow, snapshot: String): ApprovalSheetPanel? {
        if (!row.lit) return null
        return ApprovalSheetPanel(
            // AN EMPTY AGENT MEANS THE MACHINE REPORTED NONE, which InboxRow.agent states in as
            // many words -- so the line reads `swarm`, never `swarm · ` with a hanging separator
            // and never `swarm · unknown`. Filtering is what makes the separator a join rather
            // than a decoration.
            contextLine = listOf(row.project, row.agent).filter { it.isNotEmpty() }.joinToString(
                CONTEXT_SEPARATOR,
            ),
            question = row.need,
            command = snapshot,
        )
    }

    /**
     * The maquette's own separator, and the one `sessionRow` already draws between project and
     * agent. A middle dot rather than a slash or a pipe: it separates without ranking.
     */
    private const val CONTEXT_SEPARATOR = " · "
}
