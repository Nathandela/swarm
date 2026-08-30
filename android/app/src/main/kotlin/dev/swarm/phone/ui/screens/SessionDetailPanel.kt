package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandResult
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.ErrorState
import dev.swarm.phone.ui.MachineRefusalCodes
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.StopAction
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.UndeliveredLedger
import dev.swarm.phone.ui.kit.ComposerAvailability
import dev.swarm.phone.ui.kit.ComposerModel
import dev.swarm.phone.ui.kit.ComposerShut
import dev.swarm.phone.ui.kit.MenuChoice
import dev.swarm.phone.ui.kit.SendState

/**
 * PB-APP-3 -- inventory C2's screen model: what the session detail SAYS about a [SessionDetail].
 *
 * WHY THERE IS A MODEL BESIDE [SessionDetail]. That one answers what the session is and what may be
 * done to it, and its semantics are RECORDED DECISIONS from 2026-07-25 which this file implements
 * rather than revisits:
 *
 *  - Stop is a KEYSTROKE and not a new signed action -- 0x03 through a PTY in ISIG mode. No action
 *    was minted for it, because one would need its own authz tuple and replay story and the
 *    daemon's CLOSED capability switch would refuse it one hop short while sealing no reply, so
 *    Stop would hang forever.
 *  - It REQUIRES NOTHING FIRST (owner ruling R1, 2026-08-26). This read "it REQUIRES THE LEASE",
 *    and `SessionDetail.stop()` answered ACQUIRE_LEASE_FIRST for an observer -- a precondition
 *    that was fake: `turn_interrupt` takes no lease at any layer, so the step being offered
 *    changed nothing on the wire and the Stop it withheld would have worked.
 *  - It rides the LIVE-ONLY path (ADR-007 D7). An offline Stop is NOT_SENT and is never queued.
 *
 * What this answers is what a person READS: the title, the heading over the transcript, what each
 * control is called, what the confirmation asks, and what the screen says when something did not
 * reach the machine. All of it is copy or arrangement, and PB-DS-9 assigns both to the screen.
 *
 * THE TRANSCRIPT IS [TranscriptPanel]'s AND NO LONGER THIS MODEL'S. This object built a section of
 * `ActivityEntry` out of `JournalRow`s -- a record type and a display group per line -- because that
 * is what the wire carried. Interaction items carry the conversation itself, so the section is a
 * screen model of its own and is handed in; what is left here is the screen's own copy, which is
 * what PB-DS-9 assigns it.
 *
 * ## What inventory C2 draws that this does not
 *
 * **The quick-reply chips (derivation row 10).** Fully specified visually and NOT BUILT: there is
 * no facade verb behind a quick reply. Nothing in `mobile/screen_coverage.tsv` sends a canned
 * string, so a chip would be a control whose behaviour the wire does not define -- the same call
 * the machines screen made about a kill-switch toggle, for the same reason. Recorded here rather
 * than left for the next reader to wonder about.
 *
 * **Tool cards, which are now HALF here.** The mock draws structured cards per tool call, and
 * §3.3's `tool_run` is what one is made of -- [TranscriptPanel] renders it as a row plus a mono
 * well. Since agents-tracker-dwwv.1.2 that well also says when the tool is still `in_progress`:
 * `InteractionItem.status` (populated `FacadeBridge.kt:120`) was read by nothing until this bead,
 * and a running tool and a finished one rendered the identical block. The row now carries its own
 * tag (`TranscriptTag.RUNNING`) and its mono line leads with the word "running", both STATIC --
 * no pulse, no colour. What is still absent is the CARD: a bordered block with a tool glyph, an
 * amber running mark and an expandable body, none of which the kit has a factory for yet.
 * `docs/specifications/mirror-program.md` M2.2 owns it (`ui/kit/ToolCard.kt`), including the
 * pulse this bead deliberately leaves out.
 */
data class SessionDetailPanel(
    /**
     * The session this conversation IS, by the id the wire gave it (W3 review re-check,
     * 2026-08-28). The surface's presses name it at press time; a press's settle that lands
     * after the drill-down closed, or while another conversation is drawn, reads this off the
     * panel on screen to decide whether it has anything to say there (`PhoneSurface.drawStopped`).
     */
    val sessionId: String,
    /** The drill-down header's title: the session the user opened, by the id the wire gave it. */
    val title: String,
    /** §4's back control. The label a screen reader reads; the chevron is the kit's. */
    val back: String,
    /**
     * The header's overflow control, by what it opens.
     *
     * IT IS [back]'S ARRANGEMENT ON THE OTHER END OF THE SAME ROW: a glyph-only control needs a
     * label for a screen reader, the words are copy, and copy belongs to the screen (PB-DS-9) --
     * which is why `overflowControl` deliberately sets none and says so.
     *
     * **THE DRAWING'S COPY TABLE RECORDS NO STRING FOR IT, AND THIS ONE IS THEREFORE AUTHORED
     * HERE RATHER THAN QUOTED.** That is a gap in the sheet rather than a licence: what the table
     * covers is what is drawn, and U+22EE is drawn without words. The alternative was an
     * unlabelled control on the one screen a person reaches every session through, which
     * `notice`'s own ruling is written against. It says WHAT OPENS and not what the control looks
     * like, so it stays true if the mark changes.
     */
    val menu: String,
    /**
     * The conversation header's second line: what this session is DOING, and on which machine.
     *
     * "`<state> · <machine>`", which is the drawing's own cell (`header.state`), and it is one
     * string rather than two fields because it is one line of copy -- PB-DS-9 puts copy on the
     * screen model, and a header handed two halves would be a header deciding how they join.
     *
     * THE STATE IS READ FROM THE OPEN TURN AND THE LINK, NEVER FROM "A TOOL IS RUNNING"
     * ([headerStateFor] carries the argument). The machine half is [SessionDetail.machineLabel],
     * and it drops out entirely -- separator and all -- for a session id that names no machine,
     * because a trailing separator over nothing is punctuation claiming a fact.
     *
     * **EITHER HALF MAY BE ABSENT NOW, AND SO MAY BOTH.** The state half goes where this phone
     * holds no record of the session and has no evidence for any of the five words
     * ([headerStateFor]'s `holdsRecord`); the machine half goes where the id names no machine.
     * [headerSubtitleFor] spends the separator only between two halves that exist, so this field
     * is `""` for a cold-opened session on an unnamespaced id -- one line drawn, not two, and no
     * punctuation standing in for a fact.
     */
    val headerSubtitle: String,
    /** The computer's name the subtitle was built from, or "" (phone refit W5.2). */
    val machineLabel: String = "",
    /**
     * The Group the header's dot draws: ALWAYS one `Kit.groupColour` can place, OR EMPTY.
     *
     * IT IS THE ROSTER'S OWN WORD AND THIS FIELD IS WHERE THE RACE IS ABSORBED. `statusDot` fails
     * loudly on a Group outside the four (PB-TOK-8, and it is right to: a Group with no colour is a
     * whole inbox section with no state) -- so a drill-down whose session left the roster between
     * two draws would take the header's dot down with it, on the one screen a person is reading.
     * [SessionDetailScreen.of] is what guarantees the value.
     *
     * **EMPTY IS A THIRD ANSWER AND NOT A MISSING ONE** (plan H.8). It says the roster could not
     * name this session's Group, and `conversationHeader` draws no mark at all for it rather than
     * a stand-in -- because every one of the four asserts something, and the one that used to
     * stand here asserted that the agent had FINISHED, beside a word that may read `working`.
     * [GROUP_UNKNOWN] carries the argument. Any consumer beyond the conversation header owes the
     * same guard the kit spends: a Group to draw, or nothing drawn.
     */
    val headerGroup: String,
    /**
     * The conversation, which is ADR-009-structured-chat-interaction (1) landing on this screen.
     *
     * IT IS THE ITEM TRANSCRIPT AND NOT THE JOURNAL LOG ANY MORE. What stood here was a
     * `TranscriptSection` over `JournalRow`s -- a record type and a display group per line, which is
     * what the wire carried before interaction items existed. It is now [TranscriptPanel], folded
     * from the items themselves, and the two are not two views of one thing: IS-SS-1 splits them
     * deliberately ("a client renders the roster from the latter and the transcript from the
     * former"), and the journal log stays where it belongs, on the activity feed that spans every
     * session.
     */
    val transcript: TranscriptPanel,
    /** What pressing Stop does NOW, from the model that decides it. */
    val stopAction: StopAction,
    /** What pressing Stop THROUGH the confirmation would do, including refusing while offline. */
    val confirmedStopAction: StopAction,
    val killLabel: String,
    /** Kill ends the session outright and is never one tap away. */
    val killConfirmation: String,
    /** PB-INPUT-1: what did not reach the machine, in words that promise no retry. */
    val notSentNotice: String,
    /** PB-APP-8: what the screen says when the journal has a hole in it. */
    val staleNotice: String,
    /**
     * PB-SYNC-1's repair, offered exactly where [staleNotice] reports the hole
     * (agents-tracker-upbo).
     *
     * IT IS SITED HERE AND NOT ON THE LINK SECTION, which is where that bead put it. The four
     * channels are drawn on the Machines destination and the gap is FELT on this one -- a
     * conversation with records missing from it -- so the control that repairs the journal sits
     * under the sentence that says the journal is incomplete. The link section carries the second
     * entry point; this is the one the user reaches for.
     */
    val offersResync: Boolean,
    /** What the repair control reads as. */
    val resyncLabel: String,
    /**
     * PB-INPUT-1's ledger, in words that refuse the retry inference (agents-tracker-hxv).
     *
     * IT IS EMPTY WHERE NOTHING WAS LOST, which is the same call the two notices above it make: a
     * report of a loss over a session that has had none is a warning nobody wrote.
     */
    val undeliveredNotice: String,
    /** The MACHINE'S own reason for the loss, in `noticeDetail`'s cell, or empty where it sent none. */
    val undeliveredDetail: String,
    /**
     * Whether there is a backlog to acknowledge.
     *
     * IT IS A SEPARATE CONTROL AND NOT A DRAINING READ, which is `App.ClearUndeliveredInputs`' own
     * argument: a screen that OPENS must see the backlog, and a user who DISMISSES it says so once.
     */
    val offersAcknowledge: Boolean,
    /** What the acknowledgement reads as. */
    val acknowledgeLabel: String,
    /**
     * Whether the permanent composer shell can send right now, and the reason when it cannot
     * (Mirror M2.4, `ComposerModel.availabilityFor`). The shell itself is retained for every open
     * session; capability and connectivity change its enabled state and inline copy.
     */
    val composerAvailability: dev.swarm.phone.ui.kit.ComposerAvailability,
    /** M2.5's status-driven placeholder: "Message" idle, "Add feedback..." while working. */
    val composerPlaceholder: String,
    /** ADR-009 (6)'s visible per-send state, or "" before anything has been sent. */
    val composerStateLabel: String,
    /** The refusal's copy -- `stale_turn` gets its own gentle one -- or "" when there is none. */
    val composerNotice: String,
    /** The machine's own words under [composerNotice], verbatim, or "" when it sent none (W2.3). */
    val composerNoticeDetail: String,
    /** Whether the refused operation retains its submitted text for retry. It always does. */
    val composerRetainsDraft: Boolean,
    /**
     * What a disabled composer shell says, or null when it is sendable: the sentence in the field
     * and the line under it that says what is still possible.
     *
     * IT REPLACES A SINGLE SENTENCE THAT COVERED FOUR STATES. The old copy accused this
     * session's record of BREAKING, while the condition it was drawn under also covered "no
     * record was ever authored", "the record is inconsistent" and "this machine predates R8"
     * -- and it was drawn alongside "Read-only, take control to type", which contradicted it
     * on the same screen. See [ComposerModel.shutCopyFor].
     */
    val composerShut: ComposerShut?,
    /**
     * The open turn both `App.ComposerSend` and `App.Interrupt` are drawn against. It is advisory
     * signed context for queued composer delivery and remains the strict destructive target for
     * Stop (review finding B7).
     */
    val expectedTurn: String,
    /** ADR-014: whether "load earlier" is offered, and the item id it pages before. */
    val offersLoadEarlier: Boolean,
    val loadEarlierBeforeItem: String,
    /** What that control reads as. */
    val loadEarlierLabel: String,
    /**
     * The drawing's one persistent affordance: what the pill says while a decision is unanswered.
     *
     * IT IS A LABEL AND NOT A CONDITION. Whether the pill is DRAWN is [pendingDecisionId]'s
     * question; what it says is copy, and copy is the screen's (PB-DS-9). Splitting them is what
     * lets the pill's condition ride the patch path -- it is derived from the transcript, which
     * moves on its own -- while the words stay a constant nothing recomputes.
     */
    val decisionPillLabel: String,
) {
    /**
     * The oldest unanswered decision's item id, or "" when the machine is waiting on nothing.
     *
     * A PASSTHROUGH AND NOT A FIELD, deliberately. It is derived from the transcript, which is
     * already the one value `sessionDetailRedraw` accepts a difference in -- so as a computed
     * property it rides the patch for free, and as a constructor field it would have had to be
     * argued into the whitelist beside the header's two. What it decides is the pill above the
     * composer and the stick-to-bottom suppression inside the list, and both of those are facts
     * about the conversation rather than about this screen's chrome.
     */
    val pendingDecisionId: String get() = transcript.pendingDecisionId

    /** Every session keeps the same pinned composer-shaped shell; capability changes its state. */
    val composerIsBar: Boolean
        get() = true

    /** Whether the retained shell may issue composer_send right now. */
    val composerCanSend: Boolean
        get() = composerAvailability == ComposerAvailability.AVAILABLE ||
            composerAvailability == ComposerAvailability.TORN

    /**
     * Whether the agent is inside a turn: the ONE source of "working" for this screen (phone refit
     * W3.2, tbpm.4's hazard closed at the model). [SessionDetailScreen.headerStateFor]'s word,
     * [composerPlaceholder], the menu's Stop row and the composer's square all read the open turn
     * -- the value `composer_send` and `turn_interrupt` are drawn against -- so none of them can
     * disagree about whether this session is busy. It is derived from [transcript], which
     * `sessionDetailRedraw`'s patch admits, so it rides the patch path with the placeholder
     * rather than forcing a rebuild of the conversation at every turn boundary.
     */
    val composerWorking: Boolean = transcript.latestTurnId.isNotEmpty()
}

/**
 * The MACHINE's answer to one composer_send, as everything the composer does about it
 * (Mirror M2.4, ADR-009 (6)). Built by [SessionDetailScreen.composerVerdictFor], which is where
 * the reasoning lives.
 *
 * IT IS A VALUE AND NOT A READ ON A SURFACE, for [CommandVerdict]'s reason exactly: the phone
 * core is a gomobile AAR the unit-test JVM does not load, so a decision taken inside a settle
 * cannot be reached by any test at all -- which is how the old composer both declared SENT and
 * cleared unconditionally on local sealing with an exhaustive suite standing over it. The current
 * surface seals the operation into its ledger and clears only an unchanged live field so another
 * send can be typed; the later machine verdict changes only that operation's bubble.
 */
data class ComposerVerdict(
    /** Whether the machine has said anything about THIS send. */
    val answered: Boolean,
    /** The send's visible lifecycle state, or null while unanswered. */
    val state: SendState?,
    /**
     * PB-APP-9's routed ERROR STATE token, as `ComposerModel.noticeFor` speaks it -- or, for a
     * daemon code with its own sentence and no routing row, that code (W2.2). "" if accepted.
     */
    val refusal: String,
    /**
     * Legacy verdict marker for acceptance. It no longer instructs the surface to clear the live
     * field; exact-match clearing happens when the local command is sealed.
     */
    val clearsDraft: Boolean,
    /** The refusal's copy, or "" where there is nothing to report. */
    val notice: String,
    /** The machine's own words, verbatim, for the mono detail cell beside [notice]. */
    val detail: String,
) {
    companion object {
        /** Nothing issued, or nothing answered: the screen the press already drew. */
        val UNANSWERED = ComposerVerdict(
            answered = false, state = null, refusal = "",
            clearsDraft = false, notice = "", detail = "",
        )
    }
}

object SessionDetailScreen {

    /**
     * Stop, in the two wordings the lease decides.
     *
     * THE OBSERVER'S WORDING IS A DIFFERENT SENTENCE AND NOT A DISABLED BUTTON. PB-INPUT-3 refuses
     * input without a confirmed lease, and the recorded failure is precise: the surface showed the
     * same control whether the machine had granted a lease or not, so a user could not tell until
     * a keystroke vanished. The step that would make Stop work is what the control offers instead.
     */
    /**
     * ADR-014's page, in the reader's terms rather than the wire's.
     *
     * "Earlier" AND NOT "older" OR "history": what the control fetches is the part of THIS
     * conversation that happened before what is on screen, and the reader's question is where the
     * conversation started rather than how old a record is.
     */
    private const val LOAD_EARLIER = "Show earlier"

    private const val KILL = "Kill session"

    /**
     * Kill's confirmation, and it states the consequence rather than the action.
     *
     * Stop is recoverable -- the agent is interrupted and the session survives. Kill is not, and a
     * confirmation that read the same for both would train the user to dismiss the one that
     * matters.
     */
    private const val KILL_CONFIRMATION = "End this session? This can't be undone."

    /**
     * What the screen says when the machine REFUSED the kill (agents-tracker-qlf9).
     *
     * WHY THIS ONE MATTERS MOST OF THE THREE. The control is behind [KILL_CONFIRMATION], which
     * states the consequence, so a user who answers it has decided something irreversible is about
     * to happen. What a refusal showed them was a cleared outcome line and the session still in
     * the inbox -- indistinguishable from a kill that succeeded one redraw before the roster
     * caught up. The next thing that user does is press it again, or walk away believing the agent
     * is dead.
     *
     * IT SAYS WHAT IS TRUE OF THE SESSION rather than naming the verb. "Kill failed" is a report
     * about a button; "your machine did not end this session" is the fact the user acted on, and
     * the machine's own reason follows it because a kill switch, a revoked device and a policy
     * refusal end in three different places.
     */
    private const val KILL_REFUSED = "Couldn't end the session"

    /**
     * What the screen says when the machine REFUSED the Stop (Wave R6 review round 2).
     *
     * [KILL_REFUSED]'s argument, on the verb beside it, and it was the same silence: a Stop the
     * machine declined and a Stop that worked drew the identical screen -- the outcome line is
     * cleared for both and the button comes back enabled either way -- because the press
     * discarded the operation and nothing ever claimed its answer. The two honest refusals this
     * now carries are `interrupt_unsupported` (the provider proves no cancel key, so NOTHING was
     * typed) and `stale_turn` (the turn it was drawn against is over), and both are facts the
     * user cannot infer from anything else on the screen.
     *
     * IT NAMES WHAT IS TRUE OF THE TURN rather than the verb, for [KILL_REFUSED]'s reason, and
     * the machine's own words follow it in the detail cell because those three refusals are
     * fixed in three different places.
     */
    private const val INTERRUPT_REFUSED = "Couldn't stop"

    /**
     * What the screen says when THIS PHONE can hold no more of the conversation (review round 2,
     * ADR-014 A8).
     *
     * IT IS NOT THE FLOOR'S SILENCE, and that distinction is the whole reason it exists. When the
     * MACHINE says nothing older is retained, the control simply goes: the reader has reached the
     * beginning and there is nothing to explain. When the PHONE runs out of room there IS more,
     * and a control that vanished without a word would be telling the reader they had reached a
     * beginning that is not there.
     *
     * IT NAMES WHERE THE REST IS, because that is the only act available: nothing on the handset
     * recovers it, and the machine still has it.
     */
    /**
     * What the screen says when the MACHINE refused a "load earlier" (round 3, finding F4).
     *
     * IT NAMES WHAT IS TRUE OF THE CONVERSATION, on [KILL_REFUSED]'s rule, and it is deliberately
     * NOT [HISTORY_AT_CAPACITY]: that sentence is the PHONE's own limit and this is the machine
     * declining, which are different facts with different remedies. The machine's own words follow
     * in the detail cell.
     */
    private const val HISTORY_REFUSED = "Couldn't load more"

    /** [HISTORY_REFUSED]'s argument on the clipped-card fetch: something refused it, not nothing. */
    private const val DETAIL_REFUSED = "Couldn't load the full message"

    /**
     * IS-CAP-3's `unavailable`, said as the fact it is: the whole body is GONE, and no number of
     * taps brings it back. What is on screen is what the machine kept.
     */
    private const val DETAIL_UNAVAILABLE = "This is all that's left of this message"

    /**
     * PB-APP-8's sentence for one session's chronology, in the register the other screens set.
     *
     * A transcript reads as complete unless it says otherwise, and for a chronology the honest
     * failure is INCOMPLETE rather than merely old -- which is the reading a user would not assume.
     */
    private const val STALE_NOTICE = "Some messages are missing."

    /** §4's back control, by where it goes rather than by a glyph a screen reader cannot read. */
    private const val BACK = "Back to inbox"

    /**
     * [BACK]'s rule at the other end of the header: what the overflow OPENS, not what it looks
     * like.
     *
     * IT IS THE SHEET'S OWN STRING NOW. This read "More for this session", authored here because
     * the drawing's copy table had no row for a glyph-only control; the owner added `Session menu`
     * to the table rather than leaving the app speaking a sentence nobody signed. Quoted, not
     * invented.
     */
    private const val MENU = "Session menu"

    /**
     * The drawing's `decision.pill` row, verbatim: the one persistent affordance in the flow.
     *
     * IT LIVES HERE BECAUSE IT IS COPY (PB-DS-9). `decisionPill` is a kit factory and the kit
     * authors no words -- the string existed only inside `DecisionPillTest`, which is a string
     * with no owner and no production caller: the same "computed and read by nothing" shape this
     * wave is closing everywhere else.
     */
    private const val DECISION_PILL = "Needs your answer"

    /**
     * What a conversation-menu row ANSWERS TO, which is never the words on it.
     *
     * `MenuChoice` splits the id from the label precisely so a menu keyed on its own visible copy
     * does not re-route itself the day `Kill session` becomes `End session`. These are that key,
     * and they are spelled once, here, beside the function that mints them.
     */
    const val MENU_LOAD_EARLIER = "conversation.menu.earlier"
    const val MENU_STOP = "conversation.menu.stop"
    const val MENU_KILL = "conversation.menu.kill"

    /**
     * What the composer's one control SAYS (phone refit W3.2): a glyph speaks through its content
     * description, and the words are copy, so they are the screen's (PB-DS-9). "Send" while the
     * field has a draft or the agent is idle; "Stop" over a working agent and an empty field. The
     * menu's Stop row reads the same word.
     */
    const val COMPOSER_SEND = "Send"
    const val COMPOSER_STOP = "Stop"

    /**
     * The rows this session's header menu has -- which is a fact about the session, and therefore
     * a decision this model takes rather than one the kit does.
     *
     * **WHAT IS NEVER HERE, AND BOTH ABSENCES ARE REFUSALS.** There is no TERMINAL VIEW:
     * ADR-017:60-65 forbids a raw-terminal route on a session with a structured record, so a row
     * offering one would be a door onto a room that is not there. And there is no REPAIR: the tear
     * has a POSITION and the repair is drawn at it, inside the conversation, because two
     * affordances for one live-only operation are two pending states for one act, competing over
     * which of them is in flight.
     *
     * **AND `Session details` IS NOT HERE EITHER, WHICH IS A DEPARTURE FROM THE SIGNED DRAWING AND
     * IS RECORDED AS ONE.** The drawing tables three rows; this build has no session-details
     * screen and no route to one, so the row would be a control that goes nowhere -- the
     * dead-chevron defect (agents-tracker-2yb), which is precisely what the drawing's own "never a
     * dead end" rule forbids one section further down. The two facts such a screen would carry --
     * which machine, and what the session is doing -- are already in the header the menu hangs
     * off, which is why the omission costs a reader nothing today. It comes back the moment there
     * is a screen behind it.
     *
     * LOAD EARLIER IS OFFERED ON THE SAME CONDITION THE CHIP AT THE HEAD OF THE LIST IS, and both
     * press the same control: one operation, one pending state. It is DROPPED rather than greyed
     * once the machine has declared the floor -- a tap that can only come back empty is the same
     * dead affordance one row up.
     *
     * STOP IS OFFERED WHILE THE AGENT WORKS (phone refit W3.1), on the one fact the composer's
     * square reads ([SessionDetailPanel.composerWorking]), and it presses the same interrupt:
     * the square stops only over an EMPTY field, and a reader with a draft in the field still
     * needs a way to stop. It wears a route's ink; the mark for an act that cannot be undone
     * stays Kill's. First, because it is the most urgent thing a working session can want.
     *
     * KILL IS ALWAYS OFFERED, because the reader who most needs it is the one whose session is
     * not answering. What guards it is the question it has always carried, not its absence.
     */
    fun menuChoicesFor(panel: SessionDetailPanel): List<MenuChoice> = buildList {
        if (panel.composerWorking) add(MenuChoice(MENU_STOP, COMPOSER_STOP))
        if (panel.offersLoadEarlier) add(MenuChoice(MENU_LOAD_EARLIER, panel.loadEarlierLabel))
        add(MenuChoice(MENU_KILL, panel.killLabel, destructive = true))
    }

    /**
     * The five words the conversation header may say about a session, and no sixth.
     *
     * THEY ARE THE DRAWING'S `header.state` CELL VERBATIM and are lower case because that is how
     * it draws them -- a subtitle in the machine's own register, beside the machine's own name,
     * rather than a label this screen capitalised into a status badge.
     *
     * "needs you" IS NOT "needs_input". The Group is the wire's word and belongs to the DOT, which
     * draws it as a colour; this is the same fact said to a person, and the two are kept apart so
     * nobody is tempted to render a Group verbatim in a sentence.
     */
    private const val STATE_IDLE = "Idle"
    private const val STATE_WORKING = "Working"
    private const val STATE_NEEDS_YOU = "Needs you"
    private const val STATE_OFFLINE = "Not connected"
    private const val STATE_ENDED = "Ended"

    /**
     * What the header says about a session this phone holds no record of: NOTHING, and this is
     * not a sixth word.
     *
     * IT IS THE ABSENCE OF ONE OF THE FIVE, which is a different thing from a new one and the
     * distinction is the whole reason it is named rather than written `""` inline. The sheet's
     * `header.state` cell still holds exactly five strings and this surface still may not enter a
     * state the drawing does not draw; what this says is that where the phone has no evidence for
     * any of the five, it says none of them -- the same answer [GROUP_UNKNOWN] gives the dot, for
     * the same reason, and it needed no trip to the sheet for precisely that reason.
     */
    private const val STATE_UNKNOWN = ""

    /** What joins the state to the machine on the header's second line. */
    private const val SUBTITLE_SEPARATOR = " · "

    /**
     * What a header draws when the ROSTER cannot name this session's Group: NOTHING, which is
     * the whole of what this constant now says.
     *
     * WHEN IT IS SPENT, WHICH IS TWO PATHS AND NOT THE ONE THIS WAS WRITTEN FOR. One is the race
     * `FacadeBridge.sessionTitle`'s own KDoc names -- "a drill-down on a session that has just
     * left the roster is an ordinary race rather than a failure" -- which
     * `PhoneSurface.detailPanel` catches into an EMPTY Group. The other is a Group
     * `internal/status` grew and this build cannot place, and nothing filters it out on the way
     * here: `FacadeBridge.sessionRow` reads `App.Session`
     * directly rather than through `TriageInbox.from`, so that model's loud check
     * never sees this value, and the one screen where it does fire swallows it --
     * `PhoneSurface.inboxScreen` catches every Exception and falls back to an empty roster. A
     * fifth Group therefore empties the inbox quietly AND arrives here, which is why the second
     * path is named rather than folded into the first: they are one line of code and two failures.
     *
     * **WHY `completed` WAS THE WRONG SUBSTITUTE, WHICH IS PLAN H.8'S FINDING.** The argument this
     * replaces ran: every other Group ASSERTS something -- `needs_input` says the agent is blocked
     * on this reader, `working` says it is doing something right now, `ready_for_review` says
     * there is an answer waiting -- so the recessive grey, claiming no live activity, is the
     * weakest of the four. The premise is true and the conclusion does not follow. Grey is not the
     * absence of a claim: it is `completed`, the one section of the triage inbox that means the
     * agent is FINISHED, and `Kit.groupColourRes`'s own rebinding paragraph is what makes it
     * recede -- finished work should not hold the most saturated colour on a triage surface. The
     * WORD beside the dot is computed independently of the Group, from the link and the open turn
     * ([headerStateFor]), so a session with a turn open and a Group this build cannot place draws
     * a finished mark next to the word `working`: the mark contradicts the sentence beside it, and
     * it contradicts it in the one direction that tells a reader to stop watching.
     *
     * **AND THE RULING THAT REPLACES IT WAS ALREADY IN THIS APP.** `presenceDot`
     * refused exactly this move for the machine mark: rendering presence as
     * `statusDot(context, if (online) "ready_for_review" else "completed")` produces every correct
     * pixel and is "the phone inventing a Group", which that file records as THE defect rather
     * than as the cheap implementation. The four Groups are derived once, on the server, and
     * rendered verbatim (PB-TOK-8, `android/group-tokens.tsv`); a phone holding no Group holds no
     * fact to render, and ADR-009 D2 draws that state as `.pdot.unknown` -- the absence of a
     * record drawn as the absence of a mark. Dropping the dot costs a reader nothing they were
     * reading: the other half of the line still says what this session is doing, in one of the
     * five words the drawing tables, and it needs no sixth.
     *
     * **WHY IT IS A CONSTANT AT ALL, RATHER THAN THE LITERAL `""`.** The empty string is what the
     * roster hands over on the race path already, so a bare `?: ""` at the call site would read as
     * a defaulted value nobody chose. It is named here so the substitution question has one place
     * that answers it, and so the answer -- that there is no substitute -- is stated where the
     * next reader will look for the one that used to be here.
     *
     * **AND THE KIT IS WHAT MAKES THE EMPTY VALUE SAFE, WHICH IS NOT THIS FILE'S TO ASSUME.**
     * `conversationHeader` spends `statusDot` only where it is given a Group; before that guard
     * existed, emitting this would not have been a weaker claim, it would have been a crash in
     * `Kit.groupColour` on the conversation surface -- the exact failure the substitution was
     * invented to prevent. Any second consumer of [SessionDetailPanel.headerGroup] owes the same
     * guard, and that is the whole cost of the honest value.
     */
    private const val GROUP_UNKNOWN = ""

    /**
     * What the conversation header says this session is doing.
     *
     * **THE OPEN TURN IS THE SOURCE OF "working", AND `blocks.any { it.running }` IS NOT** (plan
     * D.1). A tool run is not the unit of work: an agent that is only THINKING has no open tool
     * and would read idle while it types, and a tool whose completion never arrived would read
     * working forever -- a header stuck on a word no event can clear. `TranscriptPanel.latestTurnId`
     * already mirrors IS-ENV-1's rule (a turn opens on a `user_message` and closes on any terminal
     * `agent_message`) and is the same value both `composer_send` and `turn_interrupt` are drawn
     * against, so the header, the composer and Stop cannot disagree about whether this session is
     * busy.
     *
     * **THE ORDER IS THE PRECEDENCE AND EVERY STEP OF IT IS A RULING.** `ended` outranks
     * everything because there is nothing to type into whatever the link or the record says
     * ([SessionDetail.ended]'s own words). The LINK comes next: a session may well have a turn
     * open on the machine, but a phone that cannot reach it must say so rather than reporting
     * activity it is not receiving. `needs you` outranks `working` because a session blocked on
     * this reader is not working -- it is stopped, waiting, and that is the fact worth the
     * subtitle. `idle` is what is left, and it is a real state rather than a fallback: no turn is
     * open, which is exactly the value the daemon matches an idle session by.
     *
     * **AND `idle` USED TO BE CLAIMED FROM A RECORD THIS PHONE DOES NOT HAVE, WHICH IS
     * [holdsRecord]'S WHOLE SUBJECT.** `TranscriptScreen.openTurnOf` answers `""` for an empty
     * item list and for a closed turn alike, so ONE VALUE CARRIED TWO FACTS -- "nothing is
     * running" and "I hold nothing to read a turn from"
     * -- and this `when` read the second as the first. It was not an
     * edge case: `PhoneSurface.backfillOnOpen` exists precisely because a cold-opened session has
     * zero items, and the panel is built before the backfill lands, so every cold open of a
     * session this phone holds nothing for read `idle` on a session that might be mid-turn. That
     * is H.8's defect one seam over -- a positive claim derived from a value this build does not
     * recognise -- and it gets H.8's answer: [STATE_UNKNOWN], no word at all.
     *
     * **THE FIX IS A SECOND PARAMETER AND NOT A SHARPER READ OF [openTurn], deliberately.**
     * `latestTurnId` is CORRECT for its own purpose: it mirrors IS-ENV-1 and is the value the
     * daemon matches, and narrowing it would break the two senders drawn against it. What was
     * wrong is that this function inferred a second fact from it. Taking that fact as its own
     * argument is what stops the two collapsing again the day somebody reads `openTurn` for a
     * different purpose -- after this, `openTurn` no longer carries the weight alone.
     *
     * **THE NEW ARM GOES LAST, AND ITS POSITION IS ALSO A RULING.** The three facts above it do
     * not come from the transcript at all -- `ended` is the session's, the link is the lease's,
     * and `needs you` is the ROSTER's -- so a phone with no items still knows every one of them
     * and still owes the reader the word. Only `idle`, the arm that was inferring absence of
     * activity from absence of evidence, gives way. The `working` arm above it cannot be reached
     * without a record, so its order relative to this one is arithmetic rather than a decision.
     *
     * @param holdsRecord whether this phone holds ANY item for the session -- `blocks.isNotEmpty()`
     *  and nothing else. **It is not `oldestItemId.isNotEmpty()`, which is the near-miss to
     *  refuse**: `pageableAnchorOf` skips `structured_gap` elements, so a transcript holding
     *  nothing but a proven tear answers "" there while genuinely holding a record -- and feeding
     *  this parameter that value would be the same conflation moved up one level, which is the
     *  thing the parameter exists to end.
     */
    fun headerStateFor(
        ended: Boolean,
        online: Boolean,
        group: String,
        openTurn: String,
        holdsRecord: Boolean,
    ): String = when {
        ended -> STATE_ENDED
        !online -> STATE_OFFLINE
        group == NEEDS_INPUT -> STATE_NEEDS_YOU
        openTurn.isNotEmpty() -> STATE_WORKING
        !holdsRecord -> STATE_UNKNOWN
        else -> STATE_IDLE
    }

    /** The one Group the header turns into a word of its own. */
    private const val NEEDS_INPUT = "needs_input"

    /**
     * The header's second line, assembled: the state, and the machine where there is one to name.
     *
     * THE SEPARATOR RIDES WITH THE MACHINE AND NOT WITH THE STATE, which is the whole of what an
     * empty [machineLabel] costs. A session id carrying no namespace has no machine half at all --
     * `TriageInboxScreen.machineOf` answers null for exactly that shape -- and "idle · " is a
     * line promising a word the wire never sent.
     *
     * **AND IT NOW RIDES WITH NEITHER HALF ALONE, BECAUSE THE STATE CAN BE ABSENT TOO.** Until
     * [STATE_UNKNOWN] the state was always one of five, so "empty state" was unreachable and the
     * single `machineLabel.isEmpty()` test was sufficient. It is not any more: a session this
     * phone holds no record of has no state half, and the expression above would have hung the
     * separator off the FRONT of the line -- " · nathans-mbp", a subtitle opening on punctuation,
     * promising a word that was deliberately not said. That is the same defect the original
     * paragraph is written against, arriving from the other side, so it gets the same answer
     * rather than a special case: the separator is spent only where there are two halves to join.
     *
     * BOTH HALVES EMPTY ANSWERS "", which is a real case and not a degenerate one -- a session
     * with no record whose id names no machine -- and it draws no second line at all rather than
     * a bare mark.
     */
    fun headerSubtitleFor(state: String, machineLabel: String): String = when {
        state.isEmpty() -> machineLabel
        machineLabel.isEmpty() -> state
        else -> state + SUBTITLE_SEPARATOR + machineLabel
    }


    /**
     * PB-SYNC-1's repair, named for what it mends rather than for the verb behind it
     * (agents-tracker-upbo). "Resync" is the wire's word for a reseed; what the user is looking at
     * is a log with records missing from it, and [STALE_NOTICE] is the sentence directly above.
     */
    private const val RESYNC = "Reload"

    /**
     * PB-INPUT-1's acknowledgement, and it says what it clears rather than "dismiss".
     *
     * The verb's own doc is why: a clear "does not disable the ledger", so a label reading as
     * "stop telling me" would describe something this control deliberately does not do.
     */
    private const val ACKNOWLEDGE = "Dismiss"

    /**
     * PB-INPUT-1's ledger, in the words that refuse the inference a queue would invite.
     *
     * THE SECOND SENTENCE IS THE WHOLE POINT AND IS NOT PADDING. A report that input did not arrive
     * reads, to anyone who has used a messaging app, as a thing that will go out when the link
     * returns -- and this transport never does that: `App.SendInput` rides the live-only path
     * (ADR-007 D7), so what was lost is lost. Saying only "3 things did not reach your machine"
     * would be true and would leave the user waiting for a delivery.
     *
     * IT COUNTS RATHER THAN ECHOING. `UndeliveredInput` carries a byte count and not the bytes for
     * the reason its own type records, so there is nothing here that could quote what was typed
     * even if this file wanted to.
     */
    private fun undeliveredHead(count: Int): String = when (count) {
        1 -> "1 message didn't get through."
        else -> "$count messages didn't get through."
    }

    /**
     * What the ledger's own bound threw away, said whenever it is non-zero (PB-INPUT-1).
     *
     * "A bound that discarded silently would be a second defect wearing the first one's clothes:
     * the user is told about the last N keystrokes they lost and never told there were thousands,
     * which understates the failure exactly when it is worst."
     */
    private fun undeliveredOverflow(dropped: Int): String =
        if (dropped <= 0) {
            ""
        } else {
            " $dropped earlier losses are not listed: this record is bounded and discarded them."
        }

    fun undeliveredNoticeFor(ledger: UndeliveredLedger): String =
        if (ledger.entries.isEmpty()) {
            ""
        } else {
            undeliveredHead(ledger.entries.size) + undeliveredOverflow(ledger.dropped)
        }

    /**
     * The MACHINE'S own reasons, in the machine's own register (agents-tracker-ksvb.10's ruling,
     * applied to this notice rather than restated for it).
     *
     * DISTINCT, IN THE ORDER THEY FIRST ARRIVED. A link that drops mid-session produces one reason
     * per lost keystroke, so printing every entry's reason would be the same sentence forty times;
     * printing only the last would hide a second, different failure underneath it.
     */
    fun undeliveredDetailFor(ledger: UndeliveredLedger): String =
        ledger.entries.map { it.reason }.filter { it.isNotEmpty() }.distinct().joinToString("\n")

    /**
     * The machine's answer to the kill this screen issued, or NOTHING where it has none to give.
     *
     * SILENCE ON SUCCESS IS DELIBERATE and is [PressFeedback]'s rule: the session leaving the
     * roster IS the confirmation, and `remote-control-mock.html` wrote no toast for a kill. A
     * "done" nobody specified is a sentence invented to fill a gap, at the one seam PB-DS-9 keeps
     * copy out of.
     *
     * SILENCE WHILE PENDING IS THE SAME RULE ONE STEP EARLIER. An unresolved operation is neither
     * a success nor a failure, and saying either is worse than saying nothing.
     */
    fun killNoticeFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.sentence(KILL_REFUSED) else ""

    /**
     * The machine's OWN words about the refusal, for the detail cell beside the sentence
     * (agents-tracker-ksvb.10).
     *
     * IT IS A SECOND FUNCTION AND NOT A SECOND SENTENCE. `CommandVerdict.sentence` used to splice
     * this string into the middle of [KILL_REFUSED], so a daemon Go error read as copy this screen
     * had written. The words still matter for the reason qlf9 recorded -- a kill switch, a revoked
     * device and a policy end in three different places -- so they are demoted rather than dropped:
     * `ui/kit/Notice.kt`'s `noticeDetail` draws them mono and tertiary, and this surface's toast
     * puts them in derivation row 1's own mono suffix cell.
     *
     * EMPTY WHERE THERE IS NOTHING TO EXPLAIN, which is what makes it a detail. A mono line under
     * a sentence that reported no refusal is a diagnostic about nothing -- and the refusal the
     * machine sent NO words with reaches here as `""` too, because the head already said
     * everything known.
     */
    fun killDetailFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.reason else ""

    /**
     * [killNoticeFor]'s program on the Stop (Wave R6 review round 2). See [INTERRUPT_REFUSED].
     *
     * SILENT ON ACCEPTED, which is this screen's standing rule and is sharper here than for the
     * kill: the confirmation the agent stopped is the transcript itself -- the turn's terminal
     * item -- and a toast saying so beside it would be the app claiming credit for a fact the
     * conversation already shows.
     */
    fun interruptNoticeFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.sentence(INTERRUPT_REFUSED) else ""

    /** The machine's own words about that refusal, for the detail cell. See [killDetailFor]. */
    fun interruptDetailFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.reason else ""

    /** See [HISTORY_AT_CAPACITY]: said once, where the reader's finger was, when the page cannot be held. */
    fun historyCapacityNotice(machine: String = ""): String =
        "That's all this phone can show. Older messages are on ${machine.ifEmpty { "your computer" }}."

    /**
     * What the screen says when the machine REFUSED a "load earlier" (Wave R6 review round 3,
     * finding F4). [killNoticeFor]'s program, on the read.
     *
     * WHY IT IS HERE RATHER THAN IN THE ROUTER. Both M3 reads used to hand their wire code to
     * `ErrorRouter.routeMachineCode` and say whatever came back, through the ONE-ARG
     * `PressFeedback.ofRefusal` overload -- which carries no detail cell, so the machine's own
     * words were dropped on the floor. `unavailable` and `invalid_field` are in no routing table
     * (deliberately: see [dev.swarm.phone.ui.MachineRefusalCodes]), so what a user actually read
     * was `ErrorState.UNKNOWN`'s "Something failed in a way the app does not recognise" -- the
     * app's own shrug, in place of the sentence the daemon sent. These are the only two
     * machine-answering verbs on this surface that did that; the composer, the Stop, the kill and
     * the approval all say a verb-specific sentence and put the machine's words in the detail
     * cell, and IS-CAP-3's legibility rule is that the words are shown, not classified away.
     */
    fun historyReadNoticeFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.sentence(HISTORY_REFUSED) else ""

    /** The machine's own words about that refusal, for the detail cell. See [killDetailFor]. */
    fun historyReadDetailFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.reason else ""

    /**
     * What the screen says when the machine refused the whole of a clipped card ([historyReadNoticeFor]'s
     * argument, on the detail read).
     *
     * IT NAMES THE ONE REFUSAL THE USER CAN ACT ON. `unavailable` is IS-CAP-3's answer for a body
     * the machine no longer retains -- capture-time retention is bounded and this one has aged out
     * -- and the honest sentence says the whole of it is GONE rather than that a fetch failed,
     * because a fetch that failed invites the tap again and this one can never succeed. The offer
     * under the card is withdrawn on the same fact ([detailReadIsTerminal]).
     */
    fun detailReadNoticeFor(code: String, verdict: CommandVerdict): String = when {
        !verdict.refused -> ""
        code == MachineRefusalCodes.UNAVAILABLE -> verdict.sentence(DETAIL_UNAVAILABLE)
        else -> verdict.sentence(DETAIL_REFUSED)
    }

    /** The machine's own words about that refusal, for the detail cell. See [killDetailFor]. */
    fun detailReadDetailFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.reason else ""

    /**
     * Whether a refused detail read can NEVER succeed for that card, so the offer must go.
     *
     * THE DEFECT IT CLOSES (round 3, finding F4). `TranscriptBlock.offersDetail` is derived from
     * the item's own `truncated`+`detail` fields, which were journalled when the item was CAPTURED
     * -- so a body the daemon has since evicted still advertises the fetch, and the refusal left
     * it advertising it forever. A user who tapped it read a sentence and tapped again.
     *
     * TWO CODES AND NOT EVERY REFUSAL: `unavailable` (the body is gone -- the store is oldest-first
     * and never refills) and `invalid_field` (this id is not one the machine can look up at all).
     * A transport failure or a rate limit is NOT terminal and the offer stays, because for those
     * tapping again is exactly the remedy.
     */
    fun detailReadIsTerminal(code: String): Boolean =
        code == MachineRefusalCodes.UNAVAILABLE || code == MachineRefusalCodes.INVALID_FIELD

    /**
     * What the MACHINE's answer to one composer_send does to the composer (Mirror M2.4,
     * ADR-009 (6)), claimed BY OPERATION ID (PB-SYNC-2).
     *
     * ## Why this exists at all
     *
     * Wave R6 review round 2: the send's `settle` ran on the FACADE CALL returning, and
     * `App.ComposerSend` returns its `*Op` the instant the envelope is appended to the mailbox.
     * So the composer set `SendState.SENT` and cleared the field on LOCAL SEALING -- before the
     * machine had seen the message -- and a refused send was shown as sent with the user's words
     * erased. `Press.refused` could then only fire for facade-LOCAL errors, so the daemon's
     * `stale_turn` never routed and `SendState.STALE_TURN` had no producer at all.
     *
     * ## The three decisions, in one place
     *
     * **Unanswered is not a state.** Until the machine replies there is nothing to say, and the
     * `PENDING` label the press already set is the honest screen. Saying anything else here is
     * the defect above, one layer over.
     *
     * **Machine acceptance never empties the live field.** Local sealing records the operation and
     * clears only when the field still equals that operation's captured text, freeing it for the
     * next queued send without erasing edits made while the command crossed the lane.
     * [ComposerVerdict.clearsDraft] remains a compatibility verdict marker; rendering does not use
     * it to mutate the field.
     *
     * **The refusal is routed, never string-matched.** `stale_turn` earns [SendState.STALE_TURN]
     * and `ComposerModel`'s gentle copy because `ErrorRouter.routeMachineCode` says it is that
     * class; every other refusal is [SendState.REFUSED] with the generic copy, and the machine's
     * own words ride beside it in [ComposerVerdict.detail] rather than inside the sentence
     * (agents-tracker-ksvb.10's split).
     */
    fun composerVerdictFor(outcome: OperationOutcome, operationId: String): ComposerVerdict {
        val verdict = CommandVerdict.of(outcome, operationId, CommandVerdict.ACCEPTED_OK)
        if (!verdict.answered) {
            return ComposerVerdict.UNANSWERED
        }
        if (verdict.accepted) {
            return ComposerVerdict(
                answered = true, state = SendState.SENT, refusal = "",
                clearsDraft = true, notice = "", detail = "",
            )
        }
        val routed = ErrorRouter.routeMachineCode(outcome.code)
        val state = if (routed.state == ErrorState.STALE_TURN) {
            SendState.STALE_TURN
        } else {
            SendState.REFUSED
        }
        val notice = ComposerModel.noticeFor(routed.state.name)
        // W2.2's caller (phone-refit-playbook §3): an unmapped code with its own sentence says it
        // (`routed.message` carries it; state and remedy stay UNKNOWN), and the refusal token
        // carries the CODE so the panel can say the same sentence from
        // `SessionDetail.composerRefusal`. A code this build has never seen keeps the generic copy.
        val sentenced = routed.state == ErrorState.UNKNOWN && outcome.code in MachineRefusalCodes.sentence
        return ComposerVerdict(
            answered = true,
            state = state,
            refusal = if (sentenced) outcome.code else routed.state.name,
            // NEVER on a refusal, and never conditional on WHICH refusal: a composer that ate
            // the user's words punishes them for the machine's answer, and `stale_turn` -- the
            // ordinary one -- is the case that makes it obvious.
            clearsDraft = false,
            notice = if (sentenced) routed.message else notice.copy,
            detail = verdict.reason,
        )
    }

    // THE LEASE'S ENTIRE VOCABULARY WAS DELETED HERE (owner ruling R1, 2026-08-26).
    //
    // Four sentences and two functions stood in this space: LEASE_CONFIRMED (empty),
    // LEASE_NOT_CONFIRMED ("Read-only -- take control to type."), LEASE_REFUSED and
    // LEASE_ENDED with their shared " The keyboard stays shut." suffix, plus leaseNoticeFor
    // and leaseDetailFor which chose between them.
    //
    // EVERY ONE OF THEM ANSWERED A QUESTION THE PRODUCT NO LONGER ASKS. composer_send and
    // turn_interrupt take no lease at any layer, so there was never a keystroke for a lease to
    // gate here; the notice drawn in the ordinary case told the reader to press a button that
    // changes nothing on the wire, and it was drawn on !leaseHeld with no capability input at
    // all -- which is why it appeared beside a sentence saying typing was impossible, on the
    // same screen, in the state the owner photographed.
    //
    // A REFUSAL AND A SEVERANCE HAVE NO SUBJECT EITHER: both were the machine's answer to a
    // take_control this phone issued, and it issues none. What replaced all of it is the
    // composer's own shut state, which names which of four reasons it cannot send
    // (ComposerModel.shutCopyFor) -- a stronger visible confirmation than a lease flag, because
    // a held lease never implied a session could receive a message and those four do.

    /**
     * @param transcript the conversation, decided by [TranscriptScreen] off the items themselves.
     *  It is a PARAMETER and not something read here, for [lease]'s reason: this object owns copy
     *  and arrangement, and the transcript is a screen model of its own with its own heading and its
     *  own empty copy.
     * @param lease PB-INPUT-2's three lease facts, which used to reach the user through the peek.
     * @param capabilities the MACHINE's own capability record for this session (ADR-017 T2 rule 3).
     *  It is a parameter for [lease]'s reason and for one more: the phone renders from the record
     *  and INFERS NOTHING, so the only correct default is [SessionCapabilityFacts.ABSENT] -- which
     *  retains the normal shell but disables sending, because a capability the machine did not
     *  state is not one this screen may guess about.
     */
    fun of(
        detail: SessionDetail,
        transcript: TranscriptPanel,
        lease: SessionLease,
        capabilities: SessionCapabilityFacts = SessionCapabilityFacts.ABSENT,
        undelivered: UndeliveredLedger = UndeliveredLedger.EMPTY,
    ): SessionDetailPanel = SessionDetailPanel(
        sessionId = detail.sessionId,
        // THE SESSION'S OWN NAME, and the id only where there is none
        // (agents-tracker-ksvb.1). The id keeps every other job it had on this screen --
        // it is what Stop, kill and take_control act on -- and loses only the one it was
        // never good at: being read.
        title = detail.title.ifEmpty { detail.sessionId },
        back = BACK,
        menu = MENU,
        // THE HEADER'S SECOND LINE, AND IT IS COMPOSED HERE RATHER THAN IN THE HEADER because it
        // is copy: PB-DS-9 puts every string on the screen model, and the join between the state
        // and the machine is as much a decision as either half.
        headerSubtitle = headerSubtitleFor(
            headerStateFor(
                ended = detail.ended,
                online = lease.online,
                group = detail.group,
                // D.1: the OPEN turn, which is the same value `composer_send` and `turn_interrupt`
                // are drawn against. See [headerStateFor] for why it is not a running tool.
                openTurn = transcript.latestTurnId,
                // WHETHER THIS PHONE HOLDS THE SESSION AT ALL, which is the fact the line above
                // used to be asked for on top of its own. `blocks` is one entry per item, so this
                // is `items.isEmpty()` said in the transcript's own vocabulary -- the same
                // condition `transcript.emptyCopy` is drawn for and the same one
                // `PhoneSurface.backfillOnOpen` fires on, so the three cannot disagree about
                // whether a session is cold. [headerStateFor]'s own parameter doc names the
                // near-miss this must never become.
                holdsRecord = transcript.blocks.isNotEmpty(),
            ),
            detail.machineLabel,
        ),
        // GUARDED HERE AND NOWHERE ELSE ([GROUP_UNKNOWN] carries the argument): the dot fails
        // loudly on a Group it cannot place, and every caller that could hand one in is a caller
        // that could hand in the empty string a lost roster row leaves behind.
        //
        // THE `takeIf` REFUSES AND THE `?:` NO LONGER SUBSTITUTES (plan H.8). It used to answer
        // `completed` -- a Group the roster never sent, saying the agent had finished -- on both
        // the race this line was written for and on a Group the wire grew, which it was not
        // written for at all. [GROUP_UNKNOWN] is the empty string now and the kit draws no mark
        // for it, so the header claims exactly what this phone knows and no more.
        headerGroup = detail.group.takeIf { it in TriageInbox.TRIAGE_ORDER } ?: GROUP_UNKNOWN,
        machineLabel = detail.machineLabel,
        transcript = transcript,
        // THE LEASE MODEL DECIDES BOTH, and they are read from the two properties rather than from
        stopAction = detail.stop(),
        // ONE WORDING NOW (owner ruling R1). The other was STOP_NEEDS_LEASE -- "Take control to
        // stop this" -- shown to a reader holding no lease, which was every reader: a fake
        // precondition in front of a real Stop, since turn_interrupt takes no lease either.
        confirmedStopAction = detail.confirmStop(),
        killLabel = KILL,
        killConfirmation = KILL_CONFIRMATION,
        // THE MODEL'S OWN SENTENCE, not a second one written here. `SessionDetail.notSentNotice`
        // already says the Stop did not arrive and was not held for later, and PB-INPUT-1's whole
        // subject is that the user is TOLD -- two files deciding that separately is how one of them
        // ends up promising a delivery this transport never makes.
        notSentNotice = detail.notSentNotice,
        staleNotice = if (detail.stale) STALE_NOTICE else "",
        // THE SAME CONDITION AS THE SENTENCE, deliberately: the control is what the sentence's
        // reader reaches for, and a repair offered over a chronology this screen has just called
        // complete would spend a rate-bounded verb on nothing.
        offersResync = detail.stale,
        resyncLabel = RESYNC,
        undeliveredNotice = undeliveredNoticeFor(undelivered),
        undeliveredDetail = undeliveredDetailFor(undelivered),
        offersAcknowledge = undelivered.entries.isNotEmpty(),
        acknowledgeLabel = ACKNOWLEDGE,
        // ---- Mirror M2.4/M2.5, the composer -------------------------------------
        //
        // THE GATE IS THE KIT MODEL'S AND IS NOT RE-DECIDED HERE. `ComposerModel` holds the
        // availability rule, the notice copy and the placeholder; this file supplies the two
        // facts it asks about, and both come from things this screen already reads.
        //
        // `structuredChat` COMES OFF THE CAPABILITY RECORD (ADR-017 T2 rule 3), and Wave R8 is
        // what made that possible: the daemon now authors a record on every session-creation
        // path and publishes it on the roster, so the disclosure this line used to carry --
        // "there is no capability read on this facade ... the daemon authors session capability
        // records that nothing publishes" -- is discharged rather than restated.
        //
        // THE INFERENCE IT REPLACES WAS THE ONE THE RULE NAMES BY EXAMPLE. Deriving support from
        // `transcript.structureTorn` is deriving it from THE SHAPE OF THE TRANSCRIPT, and a torn
        // transcript and a provider that never had structured chat are DIFFERENT STATES with
        // different explanations: only the record knows which one the user is looking at, and
        // only the record can say so before the first refusal arrives. A session whose gap the
        // retention bound has since evicted is now answered by the record too, instead of by the
        // machine's late `structured_unsupported`.
        composerAvailability = ComposerModel.availabilityFor(
            online = lease.online,
            structuredChat = capabilities.structuredChat,
            // THE FACT THAT SEPARATES A BROKEN RECORD FROM ONE THAT NEVER EXISTED, and it was
            // computed here and read by nothing until now. `structureTorn` is this phone
            // holding the daemon's OWN `structured_gap` element -- the machine's proof, not
            // an inference from the shape of the transcript, which is the move T2 rule 3
            // forbids. It does not decide WHETHER the composer is shut (the record does that,
            // above); it decides WHICH SENTENCE the reader is owed.
            recordTorn = transcript.structureTorn,
            ended = detail.ended,
        ),
        // **A WORKING AGENT IS ONE WITH AN OPEN TURN** (plan D.1, and [headerStateFor] carries the
        // argument in full). This read `transcript.blocks.any { it.running }` under a comment
        // asserting that a working agent is one with an open TOOL RUN, and that argument is
        // deleted rather than left standing beside the one that replaced it: a tool run is not the
        // unit of work. An agent that is only thinking has no open tool, so the placeholder said
        // "Message" while the agent was typing; a tool whose completion never arrived leaves the
        // predicate true forever, so it said "Add feedback..." for the rest of the session.
        //
        // ONE SOURCE, FOUR READERS. The header word, the working line, this placeholder and Stop
        // all read `latestTurnId` -- which is the same value `App.ComposerSend` and `App.Interrupt`
        // are drawn against -- so the screen cannot disagree with itself about whether this
        // session is busy, and cannot disagree with the daemon either.
        //
        // **AND THE COLD OPEN RESOLVES TO THE IDLE INVITATION BY DECISION, NOT BY FALL-THROUGH**
        // (owner ruling, this wave). A session no item has reached yet has an empty
        // `latestTurnId` -- the same value a CLOSED turn produces -- so this lands on "Message",
        // and the header REFUSED that same inference one field up ([headerStateFor]'s
        // `holdsRecord`). The divergence is deliberate and is recorded here so the next reader
        // does not resolve it into a defect: the header word REPORTS what the agent is doing,
        // and this INVITES an action that is genuinely available in both states. "Message" on a
        // cold-opened working session is a weaker claim than `idle` on one, because typing really
        // is available. The costs are not symmetric either -- dropping the state word costs
        // nothing, since the machine name still carries the line, while dropping this hint costs
        // a label outright, `PhoneSurface.field`'s own rule being that the hint IS the label on
        // this surface. No fourth composer state was tabled for the case, because a string saying
        // the phone does not know whether the agent is working says less than the two that exist,
        // on a control whose only job is to invite typing. `SessionDetailComposerTest`'s
        // no-record test is where the decision is asserted rather than inherited.
        composerPlaceholder = ComposerModel.placeholderFor(
            working = transcript.latestTurnId.isNotEmpty(),
        ),
        composerStateLabel = detail.composerState?.let { ComposerModel.stateLabel(it) }.orEmpty(),
        composerNotice = if (detail.composerRefusal.isEmpty()) {
            ""
        } else {
            // W2.2's caller: a daemon code with its own sentence says it; a routed state keeps
            // the composer's own copy.
            MachineRefusalCodes.sentence[detail.composerRefusal]
                ?: ComposerModel.noticeFor(detail.composerRefusal, detail.machineLabel).copy
        },
        composerNoticeDetail = if (detail.composerRefusal.isEmpty()) "" else detail.composerRefusalDetail,
        composerRetainsDraft = ComposerModel.noticeFor(detail.composerRefusal).retainsDraft,
        composerShut = ComposerModel.shutCopyFor(
            ComposerModel.availabilityFor(
                online = lease.online,
                structuredChat = capabilities.structuredChat,
                recordTorn = transcript.structureTorn,
                ended = detail.ended,
            ),
        ),
        expectedTurn = transcript.latestTurnId,
        offersLoadEarlier = transcript.offersLoadEarlier,
        loadEarlierBeforeItem = transcript.oldestItemId,
        loadEarlierLabel = LOAD_EARLIER,
        decisionPillLabel = DECISION_PILL,
    )

}

/**
 * The MACHINE's own capability record for one session, as this screen reads it (ADR-017 T2).
 *
 * IT IS A READ AND NEVER A DERIVATION. T2 rule 3: "the phone renders from that record and infers
 * nothing -- it never infers support from whether a transcript happens to be empty". This type
 * exists so the inference has nowhere to hide: the panel takes the facts, and a caller that has no
 * record hands over [ABSENT] rather than an approximation built out of whatever else it can see.
 */
data class SessionCapabilityFacts(
    /** Whether the machine authored `structured_chat` for this session's incarnation. */
    val structuredChat: Boolean,
) {
    companion object {
        /**
         * NO RECORD, which disables the normal composer shell rather than assuming support.
         *
         * It is the state of every session launched before this ruling shipped, and of every
         * session whose machine is older than the field. The transcript remains readable, while
         * the field and action stay disabled until the machine supplies authoritative capability.
         */
        val ABSENT = SessionCapabilityFacts(structuredChat = false)
    }
}
