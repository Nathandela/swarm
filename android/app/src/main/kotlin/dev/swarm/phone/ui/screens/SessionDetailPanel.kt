package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandResult
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.StopAction
import dev.swarm.phone.ui.UndeliveredLedger

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
)

object SessionDetailScreen {

    /**
     * Stop, in the two wordings the lease decides.
     *
     * THE OBSERVER'S WORDING IS A DIFFERENT SENTENCE AND NOT A DISABLED BUTTON. PB-INPUT-3 refuses
     * input without a confirmed lease, and the recorded failure is precise: the surface showed the
     * same control whether the machine had granted a lease or not, so a user could not tell until
     * a keystroke vanished. The step that would make Stop work is what the control offers instead.
     */
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
     * @param verdict the rest of the machine's answer to this screen's own take_control.
     */
    fun of(
        detail: SessionDetail,
        transcript: TranscriptPanel,
        lease: SessionLease,
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
    )

}
