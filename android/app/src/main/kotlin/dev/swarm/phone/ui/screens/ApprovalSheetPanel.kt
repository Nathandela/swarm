package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.CommandVerdict

/**
 * The obsidian migration plan's phase O6.1: what the pull-quote approval sheet SAYS.
 *
 * PB-DS-9 assigns copy and arrangement to the screen, so the lines are decided here and
 * `ui/kit/ApprovalSheet.kt` only paints them.
 *
 * ## What this sheet reads, and what filled it
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
 * **Three of those five things did not exist on this product's wire when this file was written,
 * and all three now do.** They were recorded here as refusals rather than invented -- the
 * discipline `ActivityPanel` states at length for the same class of gap -- and each is replaced
 * below by what it turned into, because a refusal left standing over a wire that has moved reads
 * to the next agent as a rule (ADR-009-structured-chat-interaction, Notes).
 *
 *  - **The sentence** was `swarmmobile.Session.Need`, the verbatim journal record TYPE that last
 *    touched the session -- a value like `needs_input` -- because no prose existed to render. It is
 *    now [ApprovalItem.summary], `interaction-schema.md` §3.5's "one line for the card headline",
 *    written machine-side by the adapter that captured the permission. Still verbatim, and still
 *    never a phrase this side invents: what changed is that the machine now sends a sentence.
 *  - **The command** was the daemon-rendered terminal snapshot, because nothing carried the literal
 *    a session was blocked on. It is now [ApprovalItem.command] -- §7's structured `action`, or the
 *    sanitized `prompt_lines` on IS-LIFE-6's fallback -- and the snapshot is gone from this app
 *    entirely (ADR-009 (1)/(3)). An empty one still draws no well at all.
 *  - **The actions** were recorded as impossible: the facade exported no approve, no deny and no
 *    answer, so the model carried no labels and the composition passed no views. It exports one now
 *    -- `App.Approve(session, itemID,
 *    decisionID)` (IS-LIFE-4's signed `ActionApprove`), and §3.5's `decisions[]` say what the
 *    buttons are called. [actions] is that list, in wire order.
 *
 * ## The one thing the sheet still refuses to draw: a polarity
 *
 * The maquette colours Allow and Deny. **This sheet cannot**, and the reason is normative rather
 * than unfinished. IS-APR-4 keeps a decision's verdict -- `allow` | `deny` | `other` -- MACHINE-SIDE
 * and off the item: the adapter classifies it at capture, the daemon resolves §3.6's
 * `allowed`/`denied` from it, and "no phone surface switches on polarity". The wire carries
 * `{id, label}`; `internal/skeleton/interaction_chain_e2e_test.go` fails the build if a verdict ever
 * rides along. So every decision is one equal-weight action, which is also what
 * `ui/kit/ApprovalSheet.kt` already rules about width: "A sheet whose Allow is wider than its Deny
 * has decided for the user, and this is the one surface in the app where it must not."
 */
data class ApprovalSheetPanel(
    /** Who is asking: the project and the agent, joined. Never a machine name this side invents. */
    val contextLine: String,
    /** The blocking question: §3.5's `summary`, verbatim. */
    val question: String,
    /** The literal the decision is about. Empty when the item names none, and then there is no well. */
    val command: String,
    /**
     * The buttons, labelled by §3.5's `decisions[].label` and answered by their `id`.
     *
     * IT IS THE WIRE'S ORDER AND NOT A SORTED ONE. The ids are the CLI's own vocabulary (§3.5) and
     * so is their sequence; reordering them would be this side ranking choices it cannot classify,
     * which is the same refusal the paint follows.
     */
    val actions: List<ApprovalDecision>,
    /** What an answer names, beside the decision's own id: `App.Approve(session, itemID, decisionID)`. */
    val sessionId: String,
    /** IS-APR-1: the item's `item_id` **is** D7's `interaction_id`. There is exactly one such id. */
    val itemId: String,
) {
    /**
     * Whether there is a well to draw.
     *
     * ABSENT IS NOT EMPTY -- the call every other card in this app makes: an empty well is a
     * recessed box that says "we have nothing" in the shape of "the machine is asking about
     * nothing".
     */
    val hasCommand: Boolean get() = command.isNotEmpty()
}

/** The model, over one pending approval. */
object ApprovalSheetScreen {

    /**
     * The sheet for [item], titled by the roster row it interrupts.
     *
     * THE ITEM'S EXISTENCE IS THE ASK, which is what replaced the old `row.lit` gate. That gate was
     * the roster's word for "this session is on the Needs-you list", which is a display group and
     * not a question: it could be true with nothing pending and false while a card waited. An
     * unresolved `approval_request` is the machine blocked on an answer, and IS-LIFE-2 guarantees it
     * reaches exactly one `approval_resolved` -- so the list of sheets to show is
     * `App.PendingApprovals`, and dismissal is that guarantee arriving rather than a rule here.
     *
     * @param row the session's roster row, or null when the roster does not hold it. A pending
     *  approval outlives a reconnect (IS-LIFE-3) and spans every session, so the two can legitimately
     *  disagree -- and a sheet is still answerable when the list it is not on has not caught up.
     */
    fun of(item: ApprovalItem, row: InboxRow?): ApprovalSheetPanel = ApprovalSheetPanel(
        // AN EMPTY AGENT MEANS THE MACHINE REPORTED NONE, which InboxRow.agent states in as many
        // words -- so the line reads `swarm`, never `swarm · ` with a hanging separator and never
        // `swarm · unknown`. Filtering is what makes the separator a join rather than a decoration.
        // With no row at all the session's own id is what is left, and it is what the transcript's
        // header names the session by.
        contextLine = row
            ?.let { listOf(it.project, it.agent).filter(String::isNotEmpty) }
            ?.takeIf { it.isNotEmpty() }
            ?.joinToString(CONTEXT_SEPARATOR)
            ?: item.sessionId,
        question = item.summary,
        command = item.command,
        actions = item.decisions,
        sessionId = item.sessionId,
        itemId = item.itemId,
    )

    /**
     * The maquette's own separator, and the one `sessionRow` already draws between project and
     * agent. A middle dot rather than a slash or a pipe: it separates without ranking.
     */
    private const val CONTEXT_SEPARATOR = " · "

    /**
     * What the screen says when the daemon refused to APPLY a tap (agents-tracker-dwwv.2.4,
     * mirror-program.md M1.2).
     *
     * `App.Approve`'s `ok` changed meaning under M1.2: it now means the daemon TYPED the
     * dialog's keys, not that the card resolved. Resolution arrives later, by observation, as
     * an `approval_resolved` item [TranscriptScreen] already renders -- so a REFUSAL is the one
     * answer this sheet has to say anything about at all.
     *
     * IT CLAIMS WHAT IS TRUE OF ALL FIVE REFUSAL REASONS AND NOT ONE CAUSE, which is the
     * 2026-08-13 review's correction (mirror-m1.md M1.8). This string used to read "This
     * approval was already answered", and one head string is shown for every refusal the verb
     * can carry: `stale_approval` (four causes plus `already_applied`), `no_dialog`,
     * `invalid_field`'s two, and the code-less `not_applicable`. "Already answered" is true of
     * the first group and FALSE of the rest. The expensive one is `no_dialog`: the recognizer
     * anchors on claude 2.1.231's recorded title and label strings and nothing checks the
     * installed version at runtime, so the day claude auto-updates off that version EVERY tap
     * refuses -- the CLI sits blocked at the terminal while the phone tells its owner the
     * request was answered. The owner's next move depends on which of those two worlds they are
     * in. So the sentence says only that the answer was not applied.
     *
     * IT IS STILL CALM RATHER THAN AN ERROR, on purpose. The dominant real case is the ordinary
     * one IS-LIFE-2 exists for -- the owner answered at the terminal, or a second tap raced the
     * first one still crossing -- and a red "your machine refused" over a card that just got
     * pre-empted by its own conversation reads as a fault nobody made. It names the machine and
     * not the verb, for [SessionDetailScreen.killNoticeFor]'s reason: "your machine did not end
     * this session" is a fact about the session, where "kill failed" is a report about a button.
     * [CommandVerdict.sentence] carries it; the machine's own words follow underneath, in
     * `noticeDetail`'s register, saying WHICH of the codes this one was -- the same split
     * [SessionDetailScreen.killNoticeFor] and `killDetailFor` already draw.
     */
    private const val NOT_APPLIED = "Couldn't send your answer. Try again"

    /**
     * The verdict's own sentence, or nothing while the daemon has APPLIED the tap or has not
     * answered it yet.
     *
     * SILENT ON ACCEPTED, FOR [SessionDetailScreen.killNoticeFor]'s REASON RESTATED HERE: an
     * `ok` is APPLIED and not resolved, so there is nothing true to confirm yet -- the
     * `approval_resolved` item arriving IS the confirmation, and a sentence invented to fill the
     * gap before it lands would be a claim this phone is not yet in a position to make.
     */
    fun refusalNoticeFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.sentence(NOT_APPLIED) else ""

    /** The machine's own words beside [refusalNoticeFor], mono and tertiary, or empty. */
    fun refusalDetailFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.reason else ""
}
