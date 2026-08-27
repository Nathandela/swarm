package dev.swarm.phone.ui.screens

import android.content.Context
import android.graphics.Rect
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.noticeDetail
import dev.swarm.phone.ui.kit.screenAir

/**
 * PB-APP-3 -- inventory C2: the session detail, composed from the component kit.
 *
 * IT IS THE FIRST REAL DRILL-DOWN DESTINATION THIS PRODUCT HAS, and that closes a defect rather
 * than merely adding a screen. `navHeaderDrill` has shipped since S24 drawing a chevron with no
 * listener behind it, because the peek was composed UNDER the inbox rather than pushed over it and
 * there was nowhere for a back control to go (agents-tracker-2yb: "the chevron therefore looks like
 * a control and does not act"). The drill-down is that destination arriving; the header itself
 * left this column for the conversation scaffold's fixed region (see [DetailTag]).
 *
 * WHAT IT COMPOSES, AS OF THE CONVERSATION SLICE: the sentences ABOUT the conversation -- the
 * stale notice and its repair, PB-INPUT-1's ledger and its clear, PB-APP-9's routed line for what
 * the machine last answered, the per-send state -- the conversation itself, and the decision card
 * under the block that points at it. It decides nothing about how any of them looks:
 * `android/gate/s24_screens_test.go` fences this package, so an `R.color`, an `R.dimen`, an
 * `R.style`, a `setTextAppearance`, a `setPadding` or a `background =` here fails the build.
 *
 * WHAT IT NO LONGER COMPOSES is everything that must not move when a reader scrolls: the drill
 * header, the composer and the two destructive controls. [DetailTag] carries that amendment in
 * full, and `conversationScaffoldView` is where they went.
 *
 * **THE TERMINAL WELL IS GONE, AT THE DATE THE DECISION SET.**
 * `docs/adr/ADR-009-structured-chat-interaction.md` (3) deletes "the plain-text terminal well ...
 * `PhoneSurface.kt`'s `peekHost` / `PeekPanel` path and the screens under it" at slice I1's exit,
 * and (1) says why this screen is what is left: "the phone's only session surface is a structured
 * chat transcript ... no terminal emulation and no raw grid anywhere in the app". This file drew
 * that grid twice over -- `monoWell(terminal = true)` here, the same well one screen over -- and it
 * is the transcript below, not the well, that a person now reads a session on.
 *
 * **PB-INPUT-2's SENTENCE AND THE TAKE CONTROL BUTTON ARRIVED WITH THAT DELETION.** They were the
 * peek's, because the peek was where the keyboard lived; the requirement is untouched by the ADR
 * -- (5) keeps raw input "exactly as decided, as the substrate" -- so the capability did not move,
 * only its home did. A slice that deleted the grid and the way to take control with it would have
 * left the phone unable to type for as long as the composer takes to land, which no decision asked
 * for.
 *
 * THE CONTROLS ARE SLOTS, and the rule outlived the two that left. Every control on the
 * conversation reaches a facade verb, carries PB-SEC-12 clause 1's touch filter and must survive a
 * redraw, so `PhoneSurface` builds them out of the kit and hands them in -- the arrangement
 * `launchPanelView` already uses for its submit. A screen that constructed them would be a screen
 * owning a listener and a native call. What changed is WHICH composition they are handed to: the
 * repair and the ledger's clear are still this column's, because they qualify sentences inside it;
 * Stop went to the pinned composer region and Kill to the header's menu, because a control the
 * thumb must reach at any moment cannot live in a scroll.
 *
 *
 * **THE COLUMN IS BARE AND ITS FLUSH CHILDREN CARRY THE SCREEN'S AIR** (owner ruling
 * 2026-08-09, agents-tracker-nx44.10). Every leaf on this screen renders at least
 * `swarm_space_12` from both edges, spent exactly once: the components that already hold
 * themselves off the glass keep their own step, and the ones §4 leaves bare -- the notice
 * line, a loose CTA, row 9's field -- get `screenAir` here. A padding on the column would
 * add 12 to the first group and re-run agents-tracker-2pnu F2's doubling; the argument is
 * `ui/kit/ScreenColumn.kt`'s and `ScreenAirSweepTest` is what holds every screen to it.
 *
 * ## What C2 draws that this does not
 *
 * **The composer (derivation row 9) IS BUILT, AND IS NO LONGER PLACED HERE** (agents-tracker-hxv,
 * agents-tracker-nx44.6; moved by chat-surface-plan §5). `ui/kit/Composer.kt` draws the bar and
 * `PhoneSurface` owns the field, the Send control, the verb and PB-SEC-12 clause 1's touch filter
 * -- and the bar is now pinned by `conversationScaffoldView` below this column rather than being
 * its last child, which is the whole of what "pinned" means and the reason the IME inset had to
 * be read at all. What row 9 still does not get is its backdrop blur -- `RenderEffect` blurs a view's OWN content, so applying it to the
 * bar would blur the field and leave the transcript behind it sharp, which is a visible defect
 * rather than an approximation, and the tab bar is the first site of the same omission. Its voice
 * and stop glyphs are absent for the chips' reason below: no facade verb takes dictation, and the
 * stop is this screen's own Stop. **PB-INPUT-6's IME-COMMIT path is also unbuilt**
 * (agents-tracker-76j): an IME commit therefore travels the ordinary keystroke path, coalesced at
 * 125 ms, rather than arriving as one event the way a clipboard paste does.
 *
 * **The quick-reply chips (row 10).** No facade verb sends a canned string, so a chip would be a
 * control whose behaviour the wire does not define. Same call the machines screen made about a
 * kill-switch toggle.
 *
 * **Tool cards.** `swarmmobile.JournalEntry` is `(Cursor, SessionID, Type, Group)` -- no tool, no
 * arguments, no result. There is nothing on the wire to build a card out of. Same reason as the
 * chips, recorded beside them so both absences are explained in one place.
 *
 * ## How this screen is reached, and how it is left
 *
 * WRITTEN HERE RATHER THAN ONLY IN A MESSAGE, because the reasoning below was ruled on by review
 * and is the expensive part. It was a HANDOFF while the navigation was outstanding; the navigation
 * is now in [dev.swarm.phone.PhoneSurface] and [dev.swarm.phone.PhoneActivity], and the paragraphs
 * stand unchanged as the record of WHY it is shaped that way:
 *
 * **The detail is a SUB-STATE of the Inbox destination, not a fifth one.** `PhoneSurface` gains
 * `detail: String?`; `Destination.INBOX` renders the inbox list when it is null and this view when
 * it is set; the other three destinations are untouched. The reason is structural rather than
 * aesthetic: [Destination] draws exactly four tabs from the labels `TriageInboxScreen` records, and
 * `Destination.forLabel` THROWS on a label it cannot place -- so a fifth value would be a
 * destination the bar cannot express and the lookup cannot produce. It also keeps the Inbox tab
 * reading as selected while you are inside it, which is where you are, and which a fifth value
 * would need a special case to fake.
 *
 * **THE TAB BAR SHOWED ON THIS SCREEN AND THE DESIGN DECIDED THAT; THE OWNER HAS SINCE DECIDED
 * OTHERWISE** (chat-surface-plan §5, and it is recorded rather than quietly reversed). The
 * argument was arithmetic: derivation row 9 puts the composer's bottom at `tabbar_height` and row
 * 10 puts the quick chips at `tabbar_height + composer_height`, both measured UP FROM A TAB BAR,
 * so a detail screen that hid the bar would place the composer 74 dp above nothing. What the
 * arithmetic could not say is that a conversation is a place you go INTO -- a bar here is an
 * invitation to leave a screen you have just arrived at, and back returns to the inbox, which
 * keeps its bar. The measurements are re-derived for a bar-less container (plan B.6) rather than
 * ignored; the connection strip stays, which is one decision each and not one decision.
 *
 * **Back clears `detail`, and it is `PhoneSurface.closeSessionDetail`.** Switching tabs mid-session
 * PRESERVES the drill-down -- it is state inside the Inbox tab, and a user who checks the activity
 * feed should return to where they were.
 *
 * **Tapping the ALREADY-SELECTED Inbox tab pops to the list.** THE DESIGN IS SILENT ON THIS and it
 * is the platform convention, adopted deliberately: a tab that does nothing when tapped reads as
 * dead. It is navigation behaviour rather than a fact about the machine, which is the line the
 * "never render what the wire does not carry" rule actually draws.
 *
 * **The system back button must pop the detail too**, or the gesture leaves the app from a screen
 * the user reached by tapping a row. That is an `OnBackPressedCallback` in `PhoneActivity`, and it
 * carries a HARD BOUNDARY set by review: it may only clear local screen state and must touch no
 * facade verb, no key custody, nothing reaching the Go core. `PhoneActivity` is the exported
 * component (PB-SEC-11), so a back callback that reached a verb would put session-acting code on
 * the one surface any app on the device can start.
 *
 * **The loose typed/send controls ARE GONE FROM THE INBOX COLUMN** (agents-tracker-nx44.6). They
 * stood at the bottom of the triage inbox, hosted through `triageInboxView`'s anonymous `below:`
 * parameter, and `PhoneSurface.detachHostedViews` ripped them off the window on the way into this
 * screen -- so the sentence below promising that what you type is sent live was drawn over a screen
 * with no field on it. The same two views are the composer's slots now.
 *
 * **The composer and the ledger SHIPPED TOGETHER** (agents-tracker-hxv's do-not-split ruling).
 * PB-INPUT-1's undelivered ledger is what stops an input path losing keystrokes with nothing on
 * screen saying so, so a composer delivered without it reintroduces exactly the defect the ledger
 * exists to prevent.
 */
object DetailTag {
    /**
     * **C2.1'S DRILL HEADER LEFT THIS COLUMN AND IS NOW `ScaffoldTag.HEADER`** (chat-surface-plan
     * §5, owner ruling on the conversation surface). MOVED, not deleted: the subject -- what you
     * are reading and the way back -- survives, as `conversationHeader` in the scaffold's fixed
     * region, and what changed is that it no longer scrolls away with the conversation. A title
     * and a way out that slide off the top the moment a reader moves are a title and a way out
     * the reader has to scroll back for, which is what a drill-down inside `phoneScaffoldView`
     * did.
     *
     * The same removal, one line each, for the three tags that went with it:
     *
     *  - `STOP` and `COMPOSER` are `ScaffoldTag.COMPOSER`'s region now -- the pinned control the
     *    thumb reaches without leaving the conversation, rather than the last children of a
     *    document.
     *  - `KILL` is the header's overflow menu, behind the question it already ships (ruling R2).
     *    A 48 dp mark in the header replaces 160 dp of stacked CTAs above the transcript.
     *
     * PB-APP-8: what the screen says when the session's journal has a hole in it.
     */
    const val STALE = "detail.stale"

    /** PB-SYNC-1's repair, directly under the notice that reports the hole it mends. */
    const val RESYNC = "detail.stale.resync"

    /** PB-INPUT-1: what did not reach the machine. */
    const val NOT_SENT = "detail.notsent"

    /** PB-INPUT-1's ledger: the input this phone took and could not deliver. */
    const val UNDELIVERED = "detail.undelivered"

    /** The machine's own reason for that loss, in `.sheet2 .ctx`. */
    const val UNDELIVERED_DETAIL = "detail.undelivered.detail"

    /** The acknowledgement, which is a separate control because a clear is a separate verb. */
    const val ACKNOWLEDGE = "detail.undelivered.clear"

    /** C2.3 -- the conversation, composed by `transcriptView` and placed here. */
    const val TRANSCRIPT = "detail.transcript"

    /**
     * ADR-009 (4)'s approval card, composed IN PLACE (agents-tracker-dwwv.2.4).
     *
     * IT USED TO BE REACHABLE ONLY BY LEAVING. The only host that ever composed the sheet was
     * under the inbox list, so `PhoneSurface.openApproval` -- the tap on [TranscriptTag.APPROVAL]
     * below -- called `closeSessionDetail()`: answering a question this very screen had just
     * shown meant navigating away from the conversation to find the card that answers it. This
     * tag is `PhoneSurface.approvalHost`, the SAME view the inbox list places, re-parented here
     * on the pattern [ScaffoldTag.STATUS] already uses for `statusHost` -- one component,
     * reparented to whichever screen the pending session is open on, never a second composition.
     */
    const val APPROVAL = "detail.approval"

    /**
     * PB-APP-9: what the machine answered the two controls below.
     *
     * IT IS ON THIS SCREEN BECAUSE THIS SCREEN REPLACES THE ONE THAT CARRIED IT. `PhoneSurface`
     * reports every verb's refusal on a single routed line, and that line is a child of the
     * unrecomposed column under the INBOX -- so a drill-down that pushed over the list would leave
     * Stop and Kill reaching a machine with nowhere to say what came back, which is the surface's
     * own recorded failure: "the user presses a control, something refuses, and the screen looks
     * identical either way".
     */
    const val OUTCOME = "detail.outcome"

    /**
     * What the screen says INSTEAD of the composer, for a session whose structured record tore
     * (ADR-017 T2 rule 2, Mirror M2.4).
     *
     * IT IS ITS OWN TAG AND NOT A STATE ON THE BAR, which is exactly the point: the bar is
     * absent, not disabled, so there is no composer to carry a state. A test that could only ask
     * "is the composer disabled" would pass over the case where it is still there.
     *
     * AND IT IS WHY THE SENTENCE STAYED IN THIS COLUMN WHILE THE BAR LEFT IT. The bar is pinned
     * outside the scroll because it is a control the thumb must always reach; the sentence is not
     * a control at all -- it is the conversation's last line, saying why there is nothing to type
     * with -- and pinning it would reserve permanent height at the bottom of the screen to repeat
     * a fact the reader has already read. [SessionDetailPanel.composerIsBar] is the ONE predicate
     * that decides which of the two a session gets, read by this column and by the surface that
     * pins the other.
     */
    const val COMPOSER_ABSENT = "detail.composer.absent"

    /**
     * ADR-009 (6)'s visible per-send report: pending, sent, or refused with the gentle
     * `stale_turn` wording.
     *
     * **IT IS NOT "ABOVE THE BAR" AND HAS NOT BEEN SINCE THE BAR LEFT** (plan H.7). This line
     * read "Above the bar, which is where every notice on this screen sits relative to what it
     * qualifies", and the bar it named is `conversationScaffoldView`'s pinned region now -- not
     * on this screen at all -- so the sentence explained a position that had stopped existing.
     * Where these two lines actually belong is the drawing's own answer and it is neither here
     * nor the pinned region; the composition below carries it, at the site that adds them.
     */
    const val COMPOSER_NOTICE = "detail.composer.notice"

    /**
     * The state of the send itself -- Sending / Sent / Not sent -- which is a SEPARATE line from
     * [COMPOSER_NOTICE]'s remedy: one is what happened and the other is what to do about it,
     * and only the second is a refusal.
     */
    const val COMPOSER_STATE = "detail.composer.state"

    /** ADR-014's "load earlier", at the TOP of the conversation it extends backwards. */
    const val LOAD_EARLIER = "detail.transcript.earlier"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition.
     *
     * FOUR ENTRIES LEFT WITH THE LEASE (owner ruling R1): LEASE, LEASE_DETAIL, TAKE_CONTROL and
     * -- already absent from this list, for a reason that is now moot -- RELEASE. They were the
     * machine's answer about a take_control this phone issued, and it issues none.
     *
     * **AND FOUR MORE LEFT WITH THE CHROME, WHICH IS A DIFFERENT KIND OF DEPARTURE.** NAV, STOP,
     * KILL and COMPOSER are not gone from the product: three of them are the scaffold's fixed
     * regions and the fourth is the header's menu. What this set is FOR is the order of the
     * column, so a part that is no longer in the column cannot be in it -- an order that named
     * views drawn by somebody else would be this screen making a second, contradictable statement
     * about a composition it does not own, which is `sessionDetailRedraw`'s standing argument one
     * sense over. The conversation's whole-screen order is `ConversationScaffoldViewTest`'s, over
     * `ScaffoldTag`, which is where those four now live.
     */
    val COMPOSITION: Set<String> = setOf(
        STALE, RESYNC, UNDELIVERED, UNDELIVERED_DETAIL, ACKNOWLEDGE, NOT_SENT, TRANSCRIPT,
        APPROVAL, OUTCOME,
    )
}

/**
 * The conversation as a scrolling column -- and ONLY the part of it that scrolls.
 *
 * **WHAT LEFT THIS FUNCTION, AND IT IS THE POINT OF THE SLICE** (chat-surface-plan §5). It used to
 * compose the drill header at the top, the composer at the bottom and a stack of full-width CTAs
 * between the transcript and the composer -- and `phoneScaffoldView` then wrapped the whole lot in
 * one `ScrollView` with the tab bar underneath. So a session's notices, its conversation and its
 * controls scrolled as a single document: roughly 414 dp of fixed furniture before the first
 * message on a healthy session, and the composer reachable only by scrolling past the entire
 * transcript and then past three buttons. That is why one session needed two screenshots.
 *
 * The three regions are `conversationScaffoldView`'s now -- a header that does not move, this
 * column, and a pinned composer -- and what is left here is the conversation and the sentences
 * ABOUT the conversation. Every one of those is a report the reader reads once and scrolls past;
 * none of them is a control the thumb has to be able to reach at any moment, which is the whole
 * test of whether something belongs in a scroll.
 *
 * @param outcome PB-APP-9's routed sentence for whatever the surface's controls last asked the
 *  machine, empty when they have asked nothing or the answer was yes. It is a STRING and not a slot
 *  for the reason the notices are strings: the surface holds the one routed line the whole app
 *  reports on, and handing the VIEW in would take it out of the column it belongs to and never give
 *  it back.
 * @param resync PB-SYNC-1's repair, drawn only beside the stale notice it mends. A slot because the
 *  verb is rate-bounded and its ErrClassRateLimited refusal routes through PB-APP-9's table, which
 *  is `PhoneSurface`'s line and not this composition's.
 * @param acknowledge PB-INPUT-1's clear, drawn only while there is a backlog to clear.
 * @param approval ADR-009 (4)'s card, IN PLACE (agents-tracker-dwwv.2.4). `PhoneSurface`'s
 *  `approvalHost` -- the same view the inbox list places under its own column -- re-parented
 *  here whenever this session is the one the phone holds a pending approval for. It is composed
 *  unconditionally: empty while there is nothing pending, which draws no height at all, so there is
 *  never a moment this screen has to add or remove the slot itself.
 * @param onApproval where an approval block in the conversation goes when it is tapped, called with
 *  the block's `item_id` -- which IS the `interaction_id` a signed `ActionApprove` names (IS-APR-1).
 *  Passed straight through: this screen places the transcript and decides nothing about it. Null
 *  draws the block and no control, which is `navHeaderDrill(back = null)`'s ruling -- never hide what
 *  the machine is waiting on, and never draw a tap with nothing behind it.
 */
fun sessionDetailView(
    context: Context,
    panel: SessionDetailPanel,
    resync: View,
    acknowledge: View,
    approval: View,
    outcome: String,
    onApproval: ((String) -> Unit)? = null,
    /**
     * ADR-014's "load earlier" (Mirror M3.1). A SLOT for [resync]'s reason: it reaches a facade
     * verb whose refusals route through PB-APP-9, and both of those belong to the surface. Null
     * composes nothing at all -- the panel may offer the page while the caller has no control to
     * put there, and a promise with no affordance is worse than neither.
     */
    loadEarlier: View? = null,
    onToolTap: ((String) -> Unit)? = null,
    onDetail: ((View, String) -> Unit)? = null,
    /**
     * Owner rulings R8 and R9's destinations, called with the `item_id` whose route the affordance
     * opens: a tool body past the in-place bound, and a file's diff.
     *
     * THEY ARE SLOTS FOR [onApproval]'S REASON AND FOR ONE MORE. Navigation is the surface's --
     * `outputScreen` and `diffScreen` are whole screens and this column cannot host one -- and
     * `transcriptView` draws NO offer for a handler it was not given, which is deliberate on both
     * sides: an offer onto a page nobody hosts is the dead-chevron defect wearing a route, and a
     * route with no offer is a screen nothing reaches. Passing them is what closes the pair.
     */
    onOutput: ((String) -> Unit)? = null,
    onDiff: ((String) -> Unit)? = null,
    /**
     * Owner ruling R4's answer, called with the block's `item_id` and the choice the reader
     * pressed: the question is a message in the stream **carrying its own buttons**.
     *
     * IT IS A SLOT FOR [onApproval]'S REASON AND THE VERB IS THE SURFACE'S. Answering reaches
     * `App.Approve` on the COMMAND plane, claims an operation id the surface reads back, and its
     * refusals route through PB-APP-9 -- none of which this column may own.
     *
     * **NULL DRAWS THE QUESTION AND NO BUTTONS, AND THAT WAS THE SHIPPED STATE.** `decisionCard`
     * resolves `answer = if (block.approval) onDecision else null`, so until this parameter
     * existed the card fell back to being wholly tappable: a pointer to a sheet, on a screen whose
     * whole purpose is that the question is answered where it was asked. The stream got the
     * question and the buttons stayed behind (`agents-tracker-ryuk`).
     *
     * THE CHOICES ARE THE CLI'S, NEVER THIS SIDE'S. One to eight labels in the order the wire sent
     * them; IS-APR-4 keeps the verdict machine-side and `interaction_chain_e2e_test.go` fails the
     * build if one rides along. "Allow/Deny" is precisely the copy this surface may not author.
     */
    onDecision: ((View, String, ApprovalDecision) -> Unit)? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        // A glowing dot and an inflated halo are drawn past their own bounds, and every container
        // between them and the window has to allow it.
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // THE DRILL HEADER USED TO BE THE FIRST CHILD HERE, and it is `conversationHeader` in the
    // scaffold's fixed region now (DetailTag's own amendment has the argument). Nothing replaces
    // it in this column: a second title inside the scroll, under a header that already names the
    // session, would be the screen saying the same thing twice and disagreeing the first time one
    // of the two is redrawn without the other.

    // BOTH NOTICES SIT ABOVE THE CONTENT THEY QUALIFY, and each is drawn only when it has something
    // to say -- a blank warning line over a healthy session is a warning nobody wrote.
    if (panel.staleNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.staleNotice).apply { tag = DetailTag.STALE }.screenAir(),
        )
    }
    // PB-SYNC-1's REPAIR, DIRECTLY UNDER THE SENTENCE IT ANSWERS (agents-tracker-upbo). The link
    // section on Machines draws all four channels' verdicts and can act on none of them; this is
    // the one place a hole is FELT -- a conversation with records missing from it -- so the control
    // goes where the person reading about the gap already is. It is a slot for [acknowledge]'s
    // reason: `App.Resync` is rate-bounded and refuses with ErrClassRateLimited, and the surface
    // is what routes a refusal through PB-APP-9's table.
    if (panel.offersResync) column.addView(resync.tagged(DetailTag.RESYNC).screenAir())
    // PB-INPUT-1'S LEDGER, ABOVE THE TRANSCRIPT AND NEVER OVER THE COMPOSER (agents-tracker-hxv's
    // own placement): it concerns input already gone, and a report of a loss must not cover the
    // control the user is reaching for to try again by hand.
    if (panel.undeliveredNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.undeliveredNotice)
                .apply { tag = DetailTag.UNDELIVERED }
                .screenAir(),
        )
    }
    if (panel.undeliveredDetail.isNotEmpty()) {
        column.addView(
            noticeDetail(context, panel.undeliveredDetail)
                .apply { tag = DetailTag.UNDELIVERED_DETAIL }
                .screenAir(),
        )
    }
    // THE ACKNOWLEDGEMENT IS A CONTROL AND NOT A DISMISS GESTURE, which is
    // `App.ClearUndeliveredInputs`' own split: a screen that OPENS must see the backlog, and a
    // user who dismisses it says so once, for every screen.
    if (panel.offersAcknowledge) column.addView(acknowledge.tagged(DetailTag.ACKNOWLEDGE).screenAir())
    if (panel.notSentNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.notSentNotice).apply { tag = DetailTag.NOT_SENT }.screenAir(),
        )
    }

    // THE CONVERSATION, WHICH IS THIS SCREEN'S SUBJECT NOW. It is `transcriptView`'s composition and
    // not a second one written here: the heading, the rows, the wells and row 8's empty state are
    // all its, and a screen that rebuilt them would be the copy §2's reuse rule exists to prevent.
    // ADR-014's PAGE, ABOVE THE CONVERSATION IT EXTENDS. It goes here and not under the
    // transcript for the same rule the notices follow: a control goes where what it acts on
    // begins, and what this one acts on is the TOP of the conversation. It is dropped once the
    // machine has declared the floor -- a tap that can only come back empty is the dead-chevron
    // defect wearing a page (agents-tracker-2yb).
    if (panel.offersLoadEarlier && loadEarlier != null) {
        // **A PILL AT THE HEAD OF THE LIST, CENTRED -- NOT A FULL-WIDTH BUTTON IN THE READING
        // PATH.** The control is the surface's (`earlierChip` wrapped in the press plumbing) and
        // where it SITS is this column's, which is the half of the fence a screen keeps: a chip
        // hugs its own words, so without a gravity it would hang off the leading edge and read as
        // the first row of the conversation rather than as its head. The condition is unchanged --
        // `offersLoadEarlier` already carries "when the machine says nothing older is retained,
        // the chip is not drawn at all rather than drawn dead".
        column.addView(
            loadEarlier.tagged(DetailTag.LOAD_EARLIER).screenAir().apply {
                (layoutParams as? LinearLayout.LayoutParams)?.gravity = Gravity.CENTER_HORIZONTAL
            },
        )
    }

    column.addView(
        transcriptView(
            context, panel.transcript, onApproval, onToolTap, onDetail,
            onOutput = onOutput, onDiff = onDiff, onDecision = onDecision,
        ).apply { tag = DetailTag.TRANSCRIPT },
    )

    // THE ANSWER, DIRECTLY UNDER THE QUESTION (agents-tracker-dwwv.2.4). The transcript's own
    // approval block is a POINTER and not the decision -- TranscriptView's own words, "the
    // transcript's job is to say that a decision is waiting and to get the reader to it; the
    // sheet is where it is taken" -- and this is that pointer's destination, placed where a
    // reader who has just read the block lands next rather than somewhere they have to leave
    // this screen to find.
    column.addView(approval.tagged(DetailTag.APPROVAL))

    // IT SITS AT THE FOOT OF THE COLUMN RATHER THAN WITH THE OTHER NOTICES, and the placement is
    // the same rule they follow: a notice goes above what it qualifies. The stale line qualifies
    // the transcript and the not-sent line qualifies what was typed; this one qualifies the
    // controls the surface pins below this column, and a refusal drawn at the top of a scrolling
    // transcript is a report the person who pressed the button is no longer looking at.
    //
    // IT STAYED IN THE SCROLL WHILE THE CONTROLS LEFT IT, which is the split this whole slice
    // turns on: a REPORT is read once and scrolled past, and a CONTROL has to be under the thumb
    // whenever it is wanted. Pinning the routed line would spend permanent height on a sentence
    // that is empty in every state but one.
    //
    // IT IS THE ERROR VARIANT AND THE OTHER THREE ARE NOT, which is `§4 Notice line`'s own split
    // between a state the screen is reporting and a verdict the machine returned. What reaches
    // this parameter is `PhoneSurface`'s routed line, and that line is only ever non-empty on a
    // REFUSAL: `PressFeedback.ofSuccess` and `ofUnsent` both leave it "" and say what they have to
    // say in the toast. The stale, not-sent and snapshot-stale lines above are the screen's own
    // sentences about a link and a lease, and painting those `--p-err` would report a refusal
    // nobody made -- which is `ofUnsent`'s own recorded argument, one layer up.
    if (outcome.isNotEmpty()) {
        column.addView(
            notice(context, outcome, NoticeKind.ERROR)
                .apply { tag = DetailTag.OUTCOME }
                .screenAir(),
        )
    }

    // THE LEASE SENTENCE AND ITS DETAIL WERE DELETED HERE (owner ruling R1, 2026-08-26).
    // Both were the machine's answer about a take_control this phone issued, and it issues
    // none. What a reader needs from this region -- can I type, and if not why -- is answered
    // by the composer's own shut state.
    //
    // **AND THE CTA STACK WENT WITH THE CHROME** (owner ruling R2, chat-surface-plan §5). Three
    // stacked full-width CTAs stood here -- 160 dp between the conversation and the composer, on
    // a viewport with roughly 150 dp left for the conversation itself. Take control and Release
    // left the product with the lease; Stop is pinned with the composer, where a thumb reaches it
    // without leaving the reading position; Kill is the header's overflow menu, behind exactly
    // the question it already ships. `ctaStack` has no caller on this screen any more, and the
    // reading flow costs nothing for either of the two controls that survived.
    // ADR-009 (6)'s VISIBLE SEND, AT THE FOOT OF THE COLUMN AND DIRECTLY UNDER THE CONVERSATION
    // IT REPORTS ON. What stood here was "above the bar it reports on -- the same placement rule
    // every other notice on this screen follows", and it was false from the moment the bar left:
    // the bar is `PhoneSurface.composerRegion`, pinned by `conversationScaffoldView` OUTSIDE this
    // scroll, so there is nothing on this screen for these two lines to be above. A placement
    // rule quoted about a component that is no longer here is how a file comes to explain a
    // position nobody chose.
    //
    // **AND THEY ARE NOT FOLLOWING THE BAR OUT, BECAUSE THE DRAWING DOES NOT PUT THEM THERE**
    // (plan H.7, and `docs/design/conversation-drawing.html` is authoritative for where a tabled
    // string is drawn). All three of these sentences are sited on the MESSAGE and not on the
    // chrome: `bubble.pending`, `bubble.refused` and `bubble.stale` are the line drawn directly
    // beneath the sender's own bubble, inside the list, beside the bubble they are about. Pinning
    // them under the composer would put tabled copy somewhere the sheet draws nothing, and the
    // slice that settles a bubble against its own echo (plan H.4) would then have to take it back
    // -- or, worse, would leave one send reported in two places at once, which is the plan's
    // defect 2 exactly. They stay at the foot of the column until that slice moves them onto the
    // bubble, which is a move this file does not own the transcript to make.
    //
    // It is the ERROR variant only for a refusal; a pending or delivered send is this screen
    // reporting its own state, and painting that `--p-err` would report a refusal nobody made
    // (see [outcome]'s own paragraph).
    // **A LABEL NAMES A STATE, A NOTICE EXPLAINS ONE, AND NO STATE GETS BOTH** (owner ruling, this
    // wave). `stateLabel` keeps "Sending" because that names a state and explains nothing;
    // `SENT` has no words at all -- a settled bubble is DRAWN, not narrated -- and the two
    // refusals speak through `noticeFor`, which says what happened AND what to do about it. Drawn
    // together they were two near-duplicate wordings under one bubble ("Not sent" over "Not sent
    // - the conversation moved on. Read the latest turn and send again."), which reads as two
    // failures rather than one. The notice wins wherever there is one, because it is the half
    // that carries the remedy.
    if (panel.composerStateLabel.isNotEmpty() && panel.composerNotice.isEmpty()) {
        column.addView(
            notice(context, panel.composerStateLabel).apply { tag = DetailTag.COMPOSER_STATE }
                .screenAir(),
        )
    }
    if (panel.composerNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.composerNotice, NoticeKind.ERROR)
                .apply { tag = DetailTag.COMPOSER_NOTICE }
                .screenAir(),
        )
    }
    // A COMPOSER THAT CANNOT SEND SAYS WHY, IN THE WORDS OF ITS OWN REASON: one sentence per
    // state rather than one accusation over four (ComposerModel.shutCopyFor).
    //
    // ABSENT AND DISABLED ARE DIFFERENT ANSWERS, and which one a state gets turns on whether
    // the session HAS A MESSAGE SINK AT ALL. A torn record, a machine that reports no chat
    // surface and an ended session have none, and never will for this instance -- so a
    // composer over one is a message that goes in and can never be shown, and the honest
    // shape is the sentence where the composer would have been.
    //
    // OFFLINE IS NOT ONE OF THOSE. The sink exists and the link is coming back, so the draft
    // is still worth typing and a control that vanished would teach the reader the feature
    // was gone. It keeps the bar and carries its reason inside it -- which is more than it
    // did before, where an offline session drew a composer visually identical to a live one
    // and the availability state that would have said otherwise was read by nothing.
    //
    // ONE PREDICATE, TWO FILES: [SessionDetailPanel.composerIsBar] decides which of the two a
    // session gets, and the surface reads the SAME property to decide whether to pin a bar. The
    // condition used to be spelled out here, in this file only, because there was only one place
    // it could be spent -- and now that the bar is pinned outside this column and the sentence is
    // inside it, a second copy would be how a session comes to draw both.
    val shut = panel.composerShut
    if (!panel.composerIsBar && shut != null) {
        column.addView(
            // THE SEPARATOR RIDES WITH THE SECOND HALF, which stopped being cosmetic the day
            // `ENDED` lost its detail: "This session has ended" plus a space plus nothing is a
            // sentence with a trailing space, invisible on screen and read aloud as a pause by a
            // screen reader. Three of the four shut states name a remedy that is really nearby;
            // the fourth has nothing on the other side to type at, and says so in one sentence.
            notice(context, shutLine(shut.placeholder, shut.detail))
                .apply { tag = DetailTag.COMPOSER_ABSENT }
                .screenAir(),
        )
    }
    return column
}

/**
 * Put a new conversation into a drill-down that is ALREADY on screen, or refuse and let the caller
 * rebuild.
 *
 * IT IS agents-tracker-ksvb.3'S ARGUMENT, TRANSLATED TO THE SURFACE THAT REPLACED ITS SUBJECT.
 * The original stood over the daemon-rendered grid: `PhoneSurface.drawDetail` guards on whole-panel
 * equality, [SessionDetailPanel] CONTAINED the snapshot, and an agent writing to its terminal made
 * that guard false on every journal event -- so the header, the notices and both controls were
 * destroyed and rebuilt at output rate. ADR-009 (1)/(3) deleted the grid, and the defect moved with
 * the traffic rather than dying with it: the transcript is now what a working agent changes on
 * every item, and it is CONTAINED by the panel in exactly the same way.
 *
 * WHAT THAT REBUILD MOVES IS THIS SCREEN'S OWN HARM, and it is why the rule is kept rather than
 * retired with the well. The take-control and Stop controls were slots the surface OWNS and
 * re-parents (see [tagged]), so a rebuild at output rate re-attached the button under the finger
 * about to press it -- and the notices above them are what the user was reading to decide.
 *
 * **WHAT A REBUILD COSTS CHANGED WHEN THE CHROME LEFT THIS COLUMN, AND THE ANSWER IS RECORDED
 * HERE RATHER THAN LEFT FOR REVIEW** (chat-surface-plan D.2, the RED-phase decision it demands).
 * The header, the composer and Stop are `conversationScaffoldView`'s fixed regions now, so a
 * rebuild of this column cannot reach any of them: the button under the finger is safe by
 * CONSTRUCTION and no longer by this guard. What a rebuild still destroys is the thing the
 * conversation surface exists for -- the reader's PLACE. The scroll is the scaffold's, its child
 * is this column, and replacing that child resets the offset; every row re-measures and
 * re-antialiases; and this is the one screen in the app whose purpose is continuous reading. So
 * the guard is not weakened by the move, it is re-aimed: it used to protect the CONTROLS and it
 * now protects the POSITION.
 *
 * **AND THAT IS WHY THE HEADER'S TWO FIELDS JOIN THE WHITELIST BELOW RATHER THAN THE HEADER GOING
 * WITHOUT THEM.** `headerSubtitle` carries the state word, and the state word is read from the
 * OPEN TURN -- so it flips at every turn boundary, which on a working session is exactly as often
 * as the transcript moves. Left out of the whitelist it would force a full rebuild on every one of
 * those flips (chat-surface-plan standing risk 4), which is the defect this function exists
 * against, arriving through the field added to fix the header. Left out of the HEADER instead, the
 * conversation would be the one screen that cannot say whether the agent is working -- the fact a
 * reader opened it to check. Both fields are safe to admit for the whitelist's OWN stated test:
 * they are not composed into this column at all, so a difference in them changes nothing this
 * function is patching. What draws them is redrawn on its own clock by the surface, above this
 * scroll and outside it, exactly as the sync chrome already is.
 *
 * IT PATCHES THE CONVERSATION AND NOTHING ELSE, which is what keeps it inside PB-DS-9: a screen
 * states what is on it by composing it, and an update that patched several parts would be a second,
 * contradictable statement of the same screen. The one difference this accepts is the transcript;
 * anything else differing is a rebuild. The rows themselves are recomposed -- a conversation is a
 * list, not a string, so there is no `.text` to set -- but the notices around them are left exactly
 * where the reader last saw them.
 *
 * @param onApproval passed through unchanged, because the recomposed blocks carry the tap that
 *  answers an approval and a patch that dropped it would leave a card that draws and does nothing.
 * @return true when [host] now shows [next]. False means nothing was touched.
 */
fun sessionDetailRedraw(
    host: View,
    drawn: SessionDetailPanel?,
    next: SessionDetailPanel,
    onApproval: ((String) -> Unit)? = null,
    onToolTap: ((String) -> Unit)? = null,
    onDetail: ((View, String) -> Unit)? = null,
    /**
     * **THE SAME HANDLERS THE COMPOSITION WAS GIVEN, AND `transcriptBlockViewCount` SAYS WHY.** Two
     * of the transcript's offers are drawn only when there is somewhere to send them, so a patch
     * that COUNTED a block's views with `onOutput = null` while the column was COMPOSED with one
     * wired would splice the next block's row into the middle of this one. They are threaded rather
     * than defaulted at the call site for exactly that reason.
     */
    onOutput: ((String) -> Unit)? = null,
    onDiff: ((String) -> Unit)? = null,
    /**
     * **THE SAME HANDLER THE COMPOSITION WAS GIVEN**, for the reason recorded above [onOutput]:
     * a block rebuilt without a handler the column was composed with is a different view for the
     * same block. Here the cost is sharper than a miscount -- a decision that loses its buttons on
     * the first incremental update is worse than one that never had them, because the reader
     * watched them disappear while the agent was still waiting.
     */
    onDecision: ((View, String, ApprovalDecision) -> Unit)? = null,
): Boolean {
    if (drawn == null) return false
    // THE FIELDS A NEW CONVERSATION MOVES ON ITS OWN, and no others. Wave R6 gave the panel four
    // values derived from the transcript, and three of them are not COMPOSED into this column at
    // all -- the placeholder is applied to a field the surface owns, the expected turn is read at
    // press time, and the page's `before_item` is read at press time too -- so a difference in
    // them can ride along with the rows. The fourth pair, `composerAvailability` and
    // `offersLoadEarlier`, ADD OR REMOVE CHILDREN of this column, so they are deliberately left
    // out: a patch that let them through would leave the "record torn" sentence over a healthy
    // session, or a "load earlier" control over a floor the machine has just declared.
    //
    // AND THE HEADER'S TWO JOIN THEM ON THE SAME TEST (chat-surface-plan D.2; the KDoc above
    // carries the argument). `headerSubtitle` and `headerGroup` are drawn by the scaffold's fixed
    // header, which the surface redraws on its own clock -- so like the three above they are not
    // composed here, and unlike `composerAvailability` they add and remove nothing. Admitting
    // them is what keeps the reader's place across a turn opening and closing; refusing them
    // would rebuild the conversation at exactly the rate the state word changes.
    val patchable = drawn.copy(
        transcript = next.transcript,
        composerPlaceholder = next.composerPlaceholder,
        expectedTurn = next.expectedTurn,
        loadEarlierBeforeItem = next.loadEarlierBeforeItem,
        headerSubtitle = next.headerSubtitle,
        headerGroup = next.headerGroup,
    )
    if (next != patchable) return false
    val slot = host.findViewWithTag<View>(DetailTag.TRANSCRIPT) as? ViewGroup ?: return false
    val list = slot.findViewWithTag<View>(TranscriptTag.LIST) as? ViewGroup
    // Row 8's empty state is not a row container, so a conversation arriving at an empty screen
    // (or leaving one) has no rows to mutate: that transition recomposes the block, which is the
    // behaviour this patch had for every case before it learned to mutate.
    if (list == null || drawn.transcript.blocks.isEmpty() || next.transcript.blocks.isEmpty()) {
        slot.removeAllViews()
        val rebuilt = transcriptView(
            slot.context, next.transcript, onApproval, onToolTap, onDetail,
            onOutput = onOutput, onDiff = onDiff, onDecision = onDecision,
        )
        (rebuilt.parent as? ViewGroup)?.removeView(rebuilt)
        slot.addView(rebuilt)
        return true
    }
    patchConversation(
        list, drawn.transcript.blocks, next.transcript.blocks, onApproval, onToolTap, onDetail,
        onOutput = onOutput,
        onDiff = onDiff,
        onDecision = onDecision,
        // THE SUPPRESSION IS PASSED IN, NOT DERIVED HERE. "Auto-scroll to the newest message is
        // suppressed while a decision is unanswered" is the drawing's own rule, and it is inert
        // unless somebody spends it: a transcript that kept following the agent would carry the
        // reader past the very question the session is blocked on, which is the one row they have
        // to act on. It is read off the NEXT panel because the question that matters is whether a
        // decision is open AFTER this burst.
        decisionPending = next.pendingDecisionId.isNotEmpty(),
    )
    return true
}

/**
 * Mirror M2.3: apply the difference between two conversations to the rows ALREADY ON SCREEN.
 *
 * WHAT IT BUYS IS EVERY ROW THAT DID NOT CHANGE. A recomposition per journal event re-measures and
 * re-antialiases every `TextView` in the column, so the conversation shimmers exactly while it is
 * being read, and each row loses its selection, its accessibility focus and any touch in flight.
 * `TranscriptIncremental` decides WHICH rows changed; this spends that decision.
 *
 * THE WALK IS BY BLOCK AND THE CONTAINER IS FLAT, so the two have to be kept in step: one block
 * composes one row plus an optional well plus an optional detail offer, and
 * `transcriptBlockViewCount` is the count that says how many. `current` is the list of blocks the
 * container is showing AT THIS POINT IN THE WALK -- removals are applied first (which is why
 * `reconcileBlocks` emits them first), so every later index names the same block in the list and
 * the same run of views in the container.
 */
private fun patchConversation(
    list: ViewGroup,
    drawn: List<TranscriptBlock>,
    next: List<TranscriptBlock>,
    onApproval: ((String) -> Unit)?,
    onToolTap: ((String) -> Unit)?,
    onDetail: ((View, String) -> Unit)?,
    onOutput: ((String) -> Unit)? = null,
    onDiff: ((String) -> Unit)? = null,
    // Carried for the same reason as the two above: a block rebuilt here must be the block the
    // column composed, and a decision rebuilt without its handler loses its buttons mid-question.
    onDecision: ((View, String, ApprovalDecision) -> Unit)? = null,
    decisionPending: Boolean = false,
) {
    val mutations = TranscriptIncremental.reconcileBlocks(drawn, next)
    if (mutations.isEmpty()) return
    val current = drawn.toMutableList()
    val atBottom = listIsScrolledToBottom(list)

    for (mutation in mutations) {
        when (mutation) {
            is BlockMutation.Remove -> {
                val index = current.indexOfFirst { it.itemId == mutation.itemId }
                if (index < 0) continue
                removeRun(list, childOffsetOf(current, index, onDetail, onOutput),
                    transcriptBlockViewCount(current[index], onDetail, onOutput))
                current.removeAt(index)
            }
            is BlockMutation.Rebind -> {
                if (mutation.index >= current.size) continue
                val at = childOffsetOf(current, mutation.index, onDetail, onOutput)
                removeRun(list, at, transcriptBlockViewCount(current[mutation.index], onDetail, onOutput))
                insertRun(list, at, mutation.block, onApproval, onToolTap, onDetail, onOutput, onDiff, onDecision)
                current[mutation.index] = mutation.block
            }
            is BlockMutation.Insert -> {
                val at = childOffsetOf(current, minOf(mutation.index, current.size), onDetail, onOutput)
                insertRun(list, at, mutation.block, onApproval, onToolTap, onDetail, onOutput, onDiff, onDecision)
                current.add(minOf(mutation.index, current.size), mutation.block)
            }
        }
    }

    // M2.3's stick-to-bottom, and its whole point is the negative half: a reader who had scrolled
    // UP is never yanked down by a burst, and a page of history arriving at the FRONT never
    // scrolls at all -- which is why the predicate reads the insertion's own position rather than
    // the fact that something arrived.
    if (TranscriptIncremental.stickToBottom(atBottom, mutations, decisionPending)) {
        val last = list.getChildAt(list.childCount - 1) ?: return
        last.requestRectangleOnScreen(Rect(0, 0, last.width, last.height))
    }
}

/** Where [index]'s views start in a flat container holding [blocks]. */
private fun childOffsetOf(
    blocks: List<TranscriptBlock>,
    index: Int,
    onDetail: ((View, String) -> Unit)?,
    onOutput: ((String) -> Unit)?,
): Int = blocks.take(index).sumOf { transcriptBlockViewCount(it, onDetail, onOutput) }

private fun removeRun(list: ViewGroup, at: Int, count: Int) {
    repeat(count) { if (at < list.childCount) list.removeViewAt(at) }
}

private fun insertRun(
    list: ViewGroup,
    at: Int,
    block: TranscriptBlock,
    onApproval: ((String) -> Unit)?,
    onToolTap: ((String) -> Unit)?,
    onDetail: ((View, String) -> Unit)?,
    onOutput: ((String) -> Unit)?,
    onDiff: ((String) -> Unit)?,
    onDecision: ((View, String, ApprovalDecision) -> Unit)?,
) {
    transcriptBlockViews(
        list.context, block, onApproval, onToolTap, onDetail,
        onOutput = onOutput, onDiff = onDiff, onDecision = onDecision,
    ).forEachIndexed { offset, view -> list.addView(view, at + offset) }
}

/**
 * Whether the reader is at the bottom of whatever scrolls above these rows.
 *
 * IT IS ASKED BEFORE THE MUTATION and not after, which is the whole of the fact being read: "was
 * the reader following the conversation" is a question about the screen they were looking at, and
 * inserting rows changes the answer. No scrolling ancestor means nothing scrolls, which is
 * honestly "at the bottom": there is nowhere else to be.
 */
private fun listIsScrolledToBottom(list: View): Boolean {
    var parent = list.parent
    while (parent is View) {
        if (parent is ScrollView) {
            val content = parent.getChildAt(0) ?: return true
            return parent.scrollY + parent.height >= content.height - SCROLL_BOTTOM_SLACK_PX
        }
        parent = parent.getParent()
    }
    return true
}

/**
 * How far from the exact bottom still counts as "at the bottom", in raw pixels.
 *
 * IT IS NOT A DESIGN VALUE and is not in the ledger for that reason: nothing is drawn at this
 * size and nothing is spaced by it. It is the tolerance on a comparison between a scroll offset
 * and a content height, both of which move by a fraction of a pixel when a row re-measures, and
 * an exact comparison would make "following the conversation" depend on rounding.
 */
private const val SCROLL_BOTTOM_SLACK_PX = 4

/**
 * Tag a slot with the part it renders and detach it from whatever last held it.
 *
 * The detach is not tidiness: the panel is rebuilt whenever the transcript changes, and a slot
 * arriving at its next `addView` still claiming a discarded parent is refused by Android with "the
 * specified child already has a parent". `PairingPanelView` and `ApprovalSheetView` carry the same
 * four lines for the same reason.
 */
private fun View.tagged(tag: String): View = apply {
    this.tag = tag
    (parent as? ViewGroup)?.removeView(this)
}

/**
 * Two halves of one sentence, joined only where there are two.
 *
 * IT IS A FUNCTION AND NOT AN `ifEmpty` AT THE CALL SITE for `MachineLabel.of`'s reason: there is
 * one place a shut composer's copy is assembled, and the day a fifth state arrives with no remedy
 * the join is already right.
 */
private fun shutLine(placeholder: String, detail: String): String =
    if (detail.isEmpty()) placeholder else "$placeholder $detail"

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
