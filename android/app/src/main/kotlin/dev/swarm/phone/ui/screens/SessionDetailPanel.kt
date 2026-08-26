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
import dev.swarm.phone.ui.UndeliveredLedger
import dev.swarm.phone.ui.kit.ComposerModel
import dev.swarm.phone.ui.kit.ComposerShut
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
 *  - It REQUIRES THE LEASE. `SessionDetail.stop()` answers ACQUIRE_LEASE_FIRST for an observer, so
 *    this screen offers the step that would make Stop work instead of a button the machine will
 *    silently refuse.
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
    /** The drill-down header's title: the session the user opened, by the id the wire gave it. */
    val title: String,
    /** §4's back control. The label a screen reader reads; the chevron is the kit's. */
    val back: String,
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
    /**
     * PB-INPUT-2's "visibly", in whichever of the two states the machine has put the user in, or
     * EMPTY where the machine has confirmed the lease and there is nothing left to say.
     *
     * IT ARRIVED HERE WITH THE PEEK'S DELETION. The terminal peek carried the lease copy because
     * the peek was where the keyboard was; ADR-009 (3) deletes that screen, and this is the screen
     * a session is read on -- and typed into -- now. The requirement is untouched by the ADR --
     * (5) keeps the input substrate "exactly as decided" -- so what moved is where the sentence is
     * drawn, and what changed since is its density (agents-tracker-ksvb.6): see [LEASE_CONFIRMED].
     */
    val leaseNotice: String,
    /**
     * The MACHINE'S own words under [leaseNotice], or empty where it sent none
     * (agents-tracker-ksvb.10).
     *
     * IT IS A SECOND FIELD AND NOT A LONGER SENTENCE. It used to be spliced into the middle of
     * [leaseNotice] -- `Your machine refused this phone control of the session: <a Go error>.` --
     * so a wire string was drawn in the same type, the same ink and the same voice as the copy this
     * screen wrote. `sessionDetailView` draws it through the kit's `noticeDetail`, which is the
     * `.sheet2 .ctx` cell: mono, tertiary, and visibly not this product talking.
     */
    val leaseDetail: String,
    /**
     * Whether the Take control step is on offer, per `SessionLease.showsTakeControl`.
     *
     * IT IS OFFERED EXACTLY WHILE IT IS THE STEP TO TAKE, and [offersRelease] is now its mirror.
     */
    val offersTakeControl: Boolean,
    /**
     * PB-INPUT-3's take_control_end, per `SessionLease.showsRelease` (agents-tracker-nx44.6).
     *
     * IT IS THE HALF OF THE LEASE THIS APP NEVER HAD. `App.ReleaseControl` sat in
     * `android/unbound-verbs.tsv` reading "the surface can TAKE a lease and cannot give one back,
     * so a lease is held until the machine expires it" -- and the model that would drive the
     * control, `SessionLease.showsRelease`, was decided, unit-tested and read by nothing.
     */
    val offersRelease: Boolean,
    /** What the Release control reads as. */
    val releaseLabel: String,
    /** What pressing Stop does NOW, from the model that decides it. */
    val stopAction: StopAction,
    /** What the Stop control reads as, which differs for an observer -- see [SessionDetailScreen]. */
    val stopLabel: String,
    /** What the confirmation asks before a Stop is sent. */
    val stopConfirmation: String,
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
     * Whether the structured composer exists at all, and whether it can send right now
     * (Mirror M2.4, `ComposerModel.availabilityFor`).
     *
     * ABSENT IS STRUCTURAL AND NOT A DISABLED CONTROL (ADR-017 T2 rule 2): `structured_chat=false`
     * means there is no message sink, so the composer is GONE rather than greyed -- a greyed
     * control promises a verb that will come back, and this degrade is one-way.
     */
    val composerAvailability: dev.swarm.phone.ui.kit.ComposerAvailability,
    /** M2.5's status-driven placeholder: "Message" idle, "Add feedback..." while working. */
    val composerPlaceholder: String,
    /** ADR-009 (6)'s visible per-send state, or "" before anything has been sent. */
    val composerStateLabel: String,
    /** The refusal's copy -- `stale_turn` gets its own gentle one -- or "" when there is none. */
    val composerNotice: String,
    /** Whether what the user typed survives that refusal. It always does; see the notice model. */
    val composerRetainsDraft: Boolean,
    /** What the screen says where the composer WOULD be, for a session that has none. */
    /**
     * What a SHUT composer says, or null when it is not shut: the sentence in the field and
     * the line under it that says what is still possible.
     *
     * IT REPLACES A SINGLE SENTENCE THAT COVERED FOUR STATES. The old copy accused this
     * session's record of BREAKING, while the condition it was drawn under also covered "no
     * record was ever authored", "the record is inconsistent" and "this machine predates R8"
     * -- and it was drawn alongside "Read-only, take control to type", which contradicted it
     * on the same screen. See [ComposerModel.shutCopyFor].
     */
    val composerShut: ComposerShut?,
    /**
     * The turn both `App.ComposerSend` and `App.Interrupt` are drawn against (review finding B7):
     * the transcript's latest, read off the panel the screen is showing, so the precondition the
     * daemon checks is the one the reader could see.
     */
    val expectedTurn: String,
    /** ADR-014: whether "load earlier" is offered, and the item id it pages before. */
    val offersLoadEarlier: Boolean,
    val loadEarlierBeforeItem: String,
    /** What that control reads as. */
    val loadEarlierLabel: String,
)

/**
 * The MACHINE's answer to one composer_send, as everything the composer does about it
 * (Mirror M2.4, ADR-009 (6)). Built by [SessionDetailScreen.composerVerdictFor], which is where
 * the reasoning lives.
 *
 * IT IS A VALUE AND NOT A READ ON A SURFACE, for [CommandVerdict]'s reason exactly: the phone
 * core is a gomobile AAR the unit-test JVM does not load, so a decision taken inside a settle
 * cannot be reached by any test at all -- which is how a composer came to clear the user's
 * draft on local sealing with an exhaustive suite standing over it.
 */
data class ComposerVerdict(
    /** Whether the machine has said anything about THIS send. */
    val answered: Boolean,
    /** The send's visible lifecycle state, or null while unanswered. */
    val state: SendState?,
    /** PB-APP-9's routed ERROR STATE token, as `ComposerModel.noticeFor` speaks it. "" if accepted. */
    val refusal: String,
    /** Whether what the user typed is now spent. True on acceptance ONLY -- see the builder. */
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
     * What the screen says where the composer WOULD be, for a session whose structured record
     * tore (ADR-017 T2 rule 2).
     *
     * IT SAYS WHAT IS STILL POSSIBLE. A control that simply vanishes reads as a bug, and the
     * remedy here is real and nearby: the machine still has the session, and the owner can type
     * at it. What the sentence must never do is imply the phone will get the composer back --
     * the degrade is one-way for the life of the session instance.
     */
    private const val COMPOSER_ABSENT =
        "This session's structured record broke, so it can no longer be typed into from the " +
            "phone. It is still running on your machine, where you can still type at it."

    /**
     * ADR-014's page, in the reader's terms rather than the wire's.
     *
     * "Earlier" AND NOT "older" OR "history": what the control fetches is the part of THIS
     * conversation that happened before what is on screen, and the reader's question is where the
     * conversation started rather than how old a record is.
     */
    private const val LOAD_EARLIER = "Load earlier messages"

    private const val STOP = "Stop"
    private const val STOP_NEEDS_LEASE = "Take control to stop this"

    /**
     * The confirmation before an interrupt goes out.
     *
     * It names what will actually happen -- an interrupt, the same key a person would press at the
     * terminal -- rather than "are you sure", which asks the user to confirm something the sentence
     * never told them.
     */
    private const val STOP_CONFIRMATION =
        "Interrupt what this session is doing? This sends Ctrl-C, the same key you would press at " +
            "the terminal."

    private const val KILL = "Kill session"

    /**
     * Kill's confirmation, and it states the consequence rather than the action.
     *
     * Stop is recoverable -- the agent is interrupted and the session survives. Kill is not, and a
     * confirmation that read the same for both would train the user to dismiss the one that
     * matters.
     */
    private const val KILL_CONFIRMATION =
        "End this session? The agent stops and the session is gone; this cannot be undone."

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
    private const val KILL_REFUSED = "Your machine did not end this session"

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
    private const val INTERRUPT_REFUSED = "Your machine did not stop this turn"

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
    private const val HISTORY_AT_CAPACITY =
        "This phone is holding as much of this conversation as it can. Anything earlier is still " +
            "on your machine."

    /**
     * What the screen says when the MACHINE refused a "load earlier" (round 3, finding F4).
     *
     * IT NAMES WHAT IS TRUE OF THE CONVERSATION, on [KILL_REFUSED]'s rule, and it is deliberately
     * NOT [HISTORY_AT_CAPACITY]: that sentence is the PHONE's own limit and this is the machine
     * declining, which are different facts with different remedies. The machine's own words follow
     * in the detail cell.
     */
    private const val HISTORY_REFUSED = "Your machine did not send more of this conversation"

    /** [HISTORY_REFUSED]'s argument on the clipped-card fetch: something refused it, not nothing. */
    private const val DETAIL_REFUSED = "Your machine did not send the whole of this message"

    /**
     * IS-CAP-3's `unavailable`, said as the fact it is: the whole body is GONE, and no number of
     * taps brings it back. What is on screen is what the machine kept.
     */
    private const val DETAIL_UNAVAILABLE =
        "Your machine no longer keeps the whole of this message, so what is shown is all of it"

    /**
     * PB-APP-8's sentence for one session's chronology, in the register the other screens set.
     *
     * A transcript reads as complete unless it says otherwise, and for a chronology the honest
     * failure is INCOMPLETE rather than merely old -- which is the reading a user would not assume.
     */
    private const val STALE_NOTICE =
        "Some records are missing: the event stream from your machine had a gap that has not been " +
            "repaired, so this is not a complete log of the session."

    /** §4's back control, by where it goes rather than by a glyph a screen reader cannot read. */
    private const val BACK = "Back to inbox"

    /**
     * PB-INPUT-3's take_control_end, by what it does to the SESSION rather than by naming the verb.
     *
     * It is the mirror of "Take control" on purpose: the two are one fact in two states, and a
     * release worded any other way would read as a different subject from the button it replaces.
     */
    private const val RELEASE = "Release control"

    /**
     * PB-SYNC-1's repair, named for what it mends rather than for the verb behind it
     * (agents-tracker-upbo). "Resync" is the wire's word for a reseed; what the user is looking at
     * is a log with records missing from it, and [STALE_NOTICE] is the sentence directly above.
     */
    private const val RESYNC = "Repair this record"

    /**
     * PB-INPUT-1's acknowledgement, and it says what it clears rather than "dismiss".
     *
     * The verb's own doc is why: a clear "does not disable the ledger", so a label reading as
     * "stop telling me" would describe something this control deliberately does not do.
     */
    private const val ACKNOWLEDGE = "Clear this record"

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
    private const val UNDELIVERED_LIVE_ONLY =
        " Input is live-only: none of it was held, and nothing is sent when the connection comes " +
            "back."

    private fun undeliveredHead(count: Int): String = when (count) {
        1 -> "Something you typed did not reach your machine."
        else -> "$count things you typed did not reach your machine."
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
            undeliveredHead(ledger.entries.size) + UNDELIVERED_LIVE_ONLY +
                undeliveredOverflow(ledger.dropped)
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
    fun historyCapacityNotice(): String = HISTORY_AT_CAPACITY

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
     * **Acceptance is the ONLY thing that empties the field.** That is what
     * [ComposerVerdict.clearsDraft] is, and it is a property of the ANSWER rather than a line at
     * a call site, because the call site is exactly where it was got wrong.
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
        return ComposerVerdict(
            answered = true,
            state = state,
            refusal = routed.state.name,
            // NEVER on a refusal, and never conditional on WHICH refusal: a composer that ate
            // the user's words punishes them for the machine's answer, and `stale_turn` -- the
            // ordinary one -- is the case that makes it obvious.
            clearsDraft = false,
            notice = notice.copy,
            detail = verdict.reason,
        )
    }

    /**
     * PB-INPUT-2's "visibly", for the two states this app renders as themselves rather than as a
     * transition.
     *
     * **CONFIRMED IS SILENT** (agents-tracker-ksvb.6, re-applied by agents-tracker-nx44.6). It
     * used to spend a full sentence saying control is granted, drawn unconditionally -- the app's
     * one UNGATED notice, printed over the state where nothing needs saying. "Healthy is silent"
     * is the pattern every other conditional notice here already follows, and the lease sentence
     * is not exempt from it for being the one PB-INPUT-2 names by name. The composer is now on
     * this screen, which makes the old copy worse than redundant: it told the user that what they
     * type is sent live, directly above the field they were already looking at.
     *
     * **NOT CONFIRMED IS ONE LINE.** It still says what to do -- a shut keyboard with no reason
     * beside it is the invisible suppression the requirement is against -- in the fewest words
     * that say it, and "Read-only" is the state the rest of the screen is in.
     *
     * THE RULING DIED IN A MERGE ONCE. 4493a3f made this change on `PeekPanelScreen`; the peek was
     * deleted a slice later (ADR-009 (3)) and the copy moved here from the file's pre-ksvb.6
     * state, so a cross-session resolution of a deleted file silently reverted an owner density
     * decision. It is recorded here so the next reader knows the old two-sentence form is a
     * regression rather than an earlier draft.
     */
    private const val LEASE_CONFIRMED = ""

    private const val LEASE_NOT_CONFIRMED = "Read-only -- take control to type."

    /**
     * The two sentences a REFUSAL and a SEVERANCE get instead (agents-tracker-qlf9).
     *
     * [LEASE_NOT_CONFIRMED] was shown for both, and it is wrong for both in the same way: it reads
     * as "you have not pressed the button yet", and the step it offers is the one that was just
     * declined. The machine's own words follow, because a kill switch, a revoked device and a
     * policy refusal have three different remedies and only the reply says which one this is.
     *
     * THEY ARE TWO SENTENCES AND NOT ONE. A lease the machine GRANTED and later ended is not a
     * lease it refused; `internal/remotegw/lease_sever.go` seals the detach under the
     * take_control's own operation id, so the difference arrives on this very outcome and a single
     * wording would accuse the machine of declining a lease it had given.
     */
    private const val LEASE_REFUSED = "Your machine refused this phone control of the session"

    private const val LEASE_ENDED = "Your machine ended this phone's control of the session"

    /** What every not-granted state shares, said once rather than in each sentence. */
    private const val KEYBOARD_SHUT = " The keyboard stays shut."

    /**
     * @param verdict the machine's answer to the take_control THIS screen issued. It is defaulted
     *  to [CommandVerdict.UNANSWERED] rather than required, because a phone that has asked for no
     *  lease has not been refused one -- and the two must not read the same.
     */
    fun leaseNoticeFor(
        confirmed: Boolean,
        verdict: CommandVerdict = CommandVerdict.UNANSWERED,
    ): String = when {
        // THE LEASE ITSELF IS THE AUTHORITY and this clause is first for that reason: `leaseHeld`
        // is what shuts the keyboard, and a notice announcing control over a shut keyboard is the
        // contradiction PB-INPUT-2's "visibly" exists to prevent.
        confirmed -> LEASE_CONFIRMED
        verdict.result == CommandResult.ENDED -> verdict.sentence(LEASE_ENDED) + KEYBOARD_SHUT
        verdict.refused -> verdict.sentence(LEASE_REFUSED) + KEYBOARD_SHUT
        else -> LEASE_NOT_CONFIRMED
    }

    /**
     * [killDetailFor]'s program on this verb: the machine's own words beside the lease sentence,
     * rather than inside it (agents-tracker-ksvb.10).
     *
     * IT ANSWERS FOR THE SEVERANCE AS WELL AS THE REFUSAL. `lease_sever.go` seals the detach with
     * a message of its own -- what ended the lease is as diagnostic as what refused one -- and the
     * two sentences above already keep them apart, so this cell does not have to.
     *
     * A CONFIRMED LEASE HAS NO DETAIL, and the clause is first for [leaseNoticeFor]'s reason: the
     * lease itself is the authority, so a screen the model says holds control must not draw a
     * refusal's diagnostic under a sentence announcing it.
     */
    fun leaseDetailFor(
        confirmed: Boolean,
        verdict: CommandVerdict = CommandVerdict.UNANSWERED,
    ): String = when {
        confirmed -> ""
        verdict.result == CommandResult.ENDED || verdict.refused -> verdict.reason
        else -> ""
    }

    /**
     * @param transcript the conversation, decided by [TranscriptScreen] off the items themselves.
     *  It is a PARAMETER and not something read here, for [lease]'s reason: this object owns copy
     *  and arrangement, and the transcript is a screen model of its own with its own heading and its
     *  own empty copy.
     * @param lease PB-INPUT-2's three lease facts, which used to reach the user through the peek.
     * @param capabilities the MACHINE's own capability record for this session (ADR-017 T2 rule 3).
     *  It is a parameter for [lease]'s reason and for one more: the phone renders from the record
     *  and INFERS NOTHING, so the only correct default is [SessionCapabilityFacts.ABSENT] -- which
     *  offers no composer, because a session whose capability the machine did not state is not a
     *  session this screen may guess about.
     * @param verdict the rest of the machine's answer to this screen's own take_control.
     */
    fun of(
        detail: SessionDetail,
        transcript: TranscriptPanel,
        lease: SessionLease,
        capabilities: SessionCapabilityFacts = SessionCapabilityFacts.ABSENT,
        verdict: CommandVerdict = CommandVerdict.UNANSWERED,
        undelivered: UndeliveredLedger = UndeliveredLedger.EMPTY,
    ): SessionDetailPanel = SessionDetailPanel(
        // THE SESSION'S OWN NAME, and the id only where there is none
        // (agents-tracker-ksvb.1). The id keeps every other job it had on this screen --
        // it is what Stop, kill and take_control act on -- and loses only the one it was
        // never good at: being read.
        title = detail.title.ifEmpty { detail.sessionId },
        back = BACK,
        transcript = transcript,
        // THE VERDICT IS THE MODEL'S, not the press's: `showsRelease` is what the MACHINE answered
        // this screen's own take_control with, claimed by operation id, and [verdict] is the rest of
        // that same answer -- which the peek used to discard on the way in.
        leaseNotice = leaseNoticeFor(lease.showsRelease, verdict),
        leaseDetail = leaseDetailFor(lease.showsRelease, verdict),
        offersTakeControl = lease.showsTakeControl,
        // THE LEASE MODEL DECIDES BOTH, and they are read from the two properties rather than from
        // one and its negation: `showsTakeControl` and `showsRelease` are the two sides of one
        // fact, and a screen that computed the second here would be a second copy of a decision
        // `SessionLease` already states.
        offersRelease = lease.showsRelease,
        releaseLabel = RELEASE,
        stopAction = detail.stop(),
        stopLabel = if (detail.leaseHeld) STOP else STOP_NEEDS_LEASE,
        stopConfirmation = STOP_CONFIRMATION,
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
        // A WORKING AGENT IS ONE WITH AN OPEN TOOL RUN, read off the blocks this screen has
        // already decided rather than off a second source. `TranscriptBlock.running` is §4's
        // `in_progress` and exists precisely so a caller can ask without re-parsing a sentence.
        composerPlaceholder = ComposerModel.placeholderFor(
            working = transcript.blocks.any { it.running },
        ),
        composerStateLabel = detail.composerState?.let { ComposerModel.stateLabel(it) }.orEmpty(),
        composerNotice = if (detail.composerRefusal.isEmpty()) {
            ""
        } else {
            ComposerModel.noticeFor(detail.composerRefusal).copy
        },
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
         * NO RECORD, which is the honest status card and not "assume the best" (amendment T2-a).
         *
         * It is the state of every session launched before this ruling shipped, and of every
         * session whose machine is older than the field. Offering a composer there is offering a
         * send that can only be refused; offering a terminal there is opening a peek onto every
         * one of them.
         */
        val ABSENT = SessionCapabilityFacts(structuredChat = false)
    }
}
