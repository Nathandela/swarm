package dev.swarm.phone.ui.screens

import android.content.Context
import android.graphics.Rect
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import dev.swarm.phone.ui.kit.ComposerAvailability
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.ctaStack
import dev.swarm.phone.ui.kit.navHeaderDrill
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
 * a control and does not act"). [onBack] is that destination arriving.
 *
 * WHAT IT COMPOSES: the drill header (§4), a `sectionLabel` over the session's own journal, an
 * `activityRow` per record -- or row 8's `emptyState` when there are none -- PB-APP-9's routed line
 * for what the machine last answered, PB-INPUT-2's lease sentence, and the three controls the
 * surface owns. It decides nothing about how any of them
 * looks: `android/gate/s24_screens_test.go` fences this package, so an `R.color`, an `R.dimen`, an
 * `R.style`, a `setTextAppearance`, a `setPadding` or a `background =` here fails the build.
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
 * THE CONTROLS ARE SLOTS. Stop and Kill reach facade verbs, carry PB-SEC-12 clause 1's touch filter
 * and must survive a redraw, so `PhoneSurface` builds them out of the kit and hands them in -- the
 * arrangement `peekPanelView` already uses for `[Take control]` and `launchPanelView` for its
 * submit. A screen that constructed them would be a screen owning a listener and a native call.
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
 * **The composer (derivation row 9) IS BUILT NOW** (agents-tracker-hxv, agents-tracker-nx44.6),
 * and it is placed here as a slot: `ui/kit/Composer.kt` draws the bar and `PhoneSurface` owns the
 * field, the Send control, the verb and PB-SEC-12 clause 1's touch filter. What row 9 still does
 * not get is its backdrop blur -- `RenderEffect` blurs a view's OWN content, so applying it to the
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
 * **The tab bar SHOWS on this screen, and the design decides that.** Derivation row 9 puts the
 * composer's bottom at `tabbar_height` and row 10 puts the quick chips at `tabbar_height +
 * composer_height`; both are measured UP FROM A TAB BAR, so a detail screen that hid it would place
 * the composer 74 dp above nothing and contradict the two rows that specify it.
 *
 * **Back is [onBack], already wired here, and it clears `detail`.** Switching tabs mid-session
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
    /** C2.1 -- §4's drill-down header: the chevron, and the session it names. */
    const val NAV = "detail.nav"

    /** PB-APP-8: what the screen says when the session's journal has a hole in it. */
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

    /** PB-APP-3's persistent Stop, supplied by the surface that owns the verb. */
    const val STOP = "detail.stop"

    /** The escalation, behind its own confirmation. */
    const val KILL = "detail.kill"

    /** Derivation row 9's bar: the field and the control that sends what is in it. */
    const val COMPOSER = "detail.composer"

    /**
     * What the screen says INSTEAD of the composer, for a session whose structured record tore
     * (ADR-017 T2 rule 2, Mirror M2.4).
     *
     * IT IS ITS OWN TAG AND NOT A STATE ON [COMPOSER], which is exactly the point: the bar is
     * absent, not disabled, so there is no composer to carry a state. A test that could only ask
     * "is the composer disabled" would pass over the case where it is still there.
     */
    const val COMPOSER_ABSENT = "detail.composer.absent"

    /**
     * ADR-009 (6)'s visible per-send report: pending, sent, or refused with the gentle
     * `stale_turn` wording. Above the bar, which is where every notice on this screen sits
     * relative to what it qualifies.
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
     */
    val COMPOSITION: Set<String> = setOf(
        NAV, STALE, RESYNC, UNDELIVERED, UNDELIVERED_DETAIL, ACKNOWLEDGE, NOT_SENT, TRANSCRIPT,
        APPROVAL, OUTCOME, STOP, COMPOSER,
    )
}

/**
 * The session detail as a view.
 *
 * @param stop PB-APP-3's persistent Stop. It is ON SCREEN IN EVERY STATE -- `SessionDetail`'s own
 *  `stopVisible` is a `val` and not a condition -- and what CHANGES with the lease is its wording,
 *  which [SessionDetailPanel.stopLabel] decides. A screen that hid it for an observer would remove
 *  the one control that tells them what to do next.
 * @param kill the escalation. The confirmation is the surface's, because it is a dialog rather than
 *  a part of this composition.
 * @param outcome PB-APP-9's routed sentence for whatever [stop] or [kill] last asked the machine,
 *  empty when they have asked nothing or the answer was yes. It is a STRING and not a slot for the
 *  reason the notices are strings: the surface holds the one routed line the whole app reports on,
 *  and handing the VIEW in would take it out of the column it belongs to and never give it back.
 * @param resync PB-SYNC-1's repair, drawn only beside the stale notice it mends. A slot because the
 *  verb is rate-bounded and its ErrClassRateLimited refusal routes through PB-APP-9's table, which
 *  is `PhoneSurface`'s line and not this composition's.
 * @param acknowledge PB-INPUT-1's clear, drawn only while there is a backlog to clear.
 * @param composer derivation row 9's bar. A slot rather than a construction for the reason every
 *  other control here is one, plus a second: the field holds what the user typed, so it is built
 *  once and re-parented, and a composition that built its own would empty it on every redraw.
 * @param approval ADR-009 (4)'s card, IN PLACE (agents-tracker-dwwv.2.4). `PhoneSurface`'s
 *  `approvalHost` -- the same view the inbox list places under its own column -- re-parented
 *  here whenever this session is the one the phone holds a pending approval for. It is composed
 *  unconditionally, the way [composer] is: empty while there is nothing pending, which draws no
 *  height at all, so there is never a moment this screen has to add or remove the slot itself.
 * @param onBack where §4's chevron goes: back to the list this session was opened from.
 * @param onApproval where an approval block in the conversation goes when it is tapped, called with
 *  the block's `item_id` -- which IS the `interaction_id` a signed `ActionApprove` names (IS-APR-1).
 *  Passed straight through: this screen places the transcript and decides nothing about it. Null
 *  draws the block and no control, which is `navHeaderDrill(back = null)`'s ruling -- never hide what
 *  the machine is waiting on, and never draw a tap with nothing behind it.
 */
fun sessionDetailView(
    context: Context,
    panel: SessionDetailPanel,
    stop: View,
    kill: View,
    resync: View,
    acknowledge: View,
    composer: View,
    approval: View,
    outcome: String,
    onBack: () -> Unit,
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
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        // A glowing dot and an inflated halo are drawn past their own bounds, and every container
        // between them and the window has to allow it.
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(
        navHeaderDrill(context, back = panel.back, title = panel.title).apply {
            tag = DetailTag.NAV
            // THE CHEVRON, NOT THE WHOLE HEADER. The header carries the session's name, and a
            // listener on the row would make reading the title a navigation. The kit tags the back
            // control precisely so "the screen that owns a destination can reach it" -- its own
            // words -- and `findViewWithTag` is the platform's way of doing exactly that, rather
            // than a child index that would start meaning something else the day the header gains
            // a part.
            findViewWithTag<View>(KitTag.DRILL_BACK)?.setOnClickListener { onBack() }
        },
    )

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
    // goes where the person reading about the gap already is. It is a slot for [stop]'s reason:
    // `App.Resync` is rate-bounded and refuses with ErrClassRateLimited, and the surface is what
    // routes a refusal through PB-APP-9's table.
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
        column.addView(loadEarlier.tagged(DetailTag.LOAD_EARLIER).screenAir())
    }

    column.addView(
        transcriptView(context, panel.transcript, onApproval, onToolTap, onDetail)
            .apply { tag = DetailTag.TRANSCRIPT },
    )

    // THE ANSWER, DIRECTLY UNDER THE QUESTION (agents-tracker-dwwv.2.4). The transcript's own
    // approval block is a POINTER and not the decision -- TranscriptView's own words, "the
    // transcript's job is to say that a decision is waiting and to get the reader to it; the
    // sheet is where it is taken" -- and this is that pointer's destination, placed where a
    // reader who has just read the block lands next rather than somewhere they have to leave
    // this screen to find.
    column.addView(approval.tagged(DetailTag.APPROVAL))

    // IT SITS WITH THE CONTROLS RATHER THAN WITH THE OTHER NOTICES, and the placement is the same
    // rule they follow: a notice goes above what it qualifies. The stale line qualifies the
    // transcript and the not-sent line qualifies what was typed; this one qualifies the two
    // controls, and a refusal drawn at the top of a scrolling transcript is a report the person who
    // pressed the button is no longer looking at.
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
    // by the composer's own shut state at the bottom of the column.
    // TWO CONTROLS, NOT FOUR. Take control and Release were deleted with the lease they named
    // (owner ruling R1); what is left is Stop and Kill, and Kill is on its way to the header.
    //
    // agents-tracker-nx44.1: `ctaStack` AND NOT `column`, so the controls carry `.acts2`'s own
    // `space_8` gap between them rather than the zero addView gave them.
    val controls = ctaStack(context).also { it.screenAir() }
    controls.addView(stop.tagged(DetailTag.STOP))
    controls.addView(kill.tagged(DetailTag.KILL))
    column.addView(controls)

    // DERIVATION ROW 9'S BAR, AND THE PROMISE ABOVE IT ARRIVING AT AN AFFORDANCE. The lease
    // sentence has said "what you type is sent live" on this screen since the peek's deletion while
    // the app's only field and Send were parented at the bottom of the triage inbox -- which
    // `PhoneSurface.detachHostedViews` takes off screen on the way in here. It is LAST because row
    // 9 puts the composer at the bottom of the screen, and because everything above it is either
    // the conversation or a report about it: a control the user is reaching for must not be moved
    // by a notice arriving above it.
    // ADR-009 (6)'s VISIBLE SEND, above the bar it reports on -- the same placement rule every
    // other notice on this screen follows. It is the ERROR variant only for a refusal; a pending
    // or delivered send is this screen reporting its own state, and painting that `--p-err` would
    // report a refusal nobody made (see [outcome]'s own paragraph).
    if (panel.composerStateLabel.isNotEmpty()) {
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
    val shut = panel.composerShut
    if (shut != null && panel.composerAvailability != ComposerAvailability.OFFLINE) {
        column.addView(
            notice(context, shut.placeholder + " " + shut.detail)
                .apply { tag = DetailTag.COMPOSER_ABSENT }
                .screenAir(),
        )
    } else {
        column.addView(composer.tagged(DetailTag.COMPOSER))
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
 * retired with the well. The take-control and Stop controls are slots the surface OWNS and
 * re-parents (see [tagged]), so a rebuild at output rate re-attached the button under the finger
 * about to press it -- and the notices above them are what the user was reading to decide.
 *
 * IT PATCHES THE CONVERSATION AND NOTHING ELSE, which is what keeps it inside PB-DS-9: a screen
 * states what is on it by composing it, and an update that patched several parts would be a second,
 * contradictable statement of the same screen. The one difference this accepts is the transcript;
 * anything else differing is a rebuild. The rows themselves are recomposed -- a conversation is a
 * list, not a string, so there is no `.text` to set -- but the header, the notices and both control
 * slots are left exactly where the finger last saw them.
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
): Boolean {
    if (drawn == null) return false
    // THE FIELDS A NEW CONVERSATION MOVES ON ITS OWN, and no others. Wave R6 gave the panel four
    // values derived from the transcript, and three of them are not COMPOSED into this column at
    // all -- the placeholder is applied to a field the surface owns, the expected turn is read at
    // press time, and the page's `before_item` is read at press time too -- so a difference in
    // them can ride along with the rows. The fourth pair, `composerAvailability` and
    // `offersLoadEarlier`, ADD OR REMOVE CHILDREN of this column, so they are deliberately left
    // out: a patch that let them through would leave a composer over a torn session, or a "load
    // earlier" control over a floor the machine has just declared.
    val patchable = drawn.copy(
        transcript = next.transcript,
        composerPlaceholder = next.composerPlaceholder,
        expectedTurn = next.expectedTurn,
        loadEarlierBeforeItem = next.loadEarlierBeforeItem,
    )
    if (next != patchable) return false
    val slot = host.findViewWithTag<View>(DetailTag.TRANSCRIPT) as? ViewGroup ?: return false
    val list = slot.findViewWithTag<View>(TranscriptTag.LIST) as? ViewGroup
    // Row 8's empty state is not a row container, so a conversation arriving at an empty screen
    // (or leaving one) has no rows to mutate: that transition recomposes the block, which is the
    // behaviour this patch had for every case before it learned to mutate.
    if (list == null || drawn.transcript.blocks.isEmpty() || next.transcript.blocks.isEmpty()) {
        slot.removeAllViews()
        val rebuilt = transcriptView(slot.context, next.transcript, onApproval, onToolTap, onDetail)
        (rebuilt.parent as? ViewGroup)?.removeView(rebuilt)
        slot.addView(rebuilt)
        return true
    }
    patchConversation(
        list, drawn.transcript.blocks, next.transcript.blocks, onApproval, onToolTap, onDetail,
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
                removeRun(list, childOffsetOf(current, index, onDetail),
                    transcriptBlockViewCount(current[index], onDetail))
                current.removeAt(index)
            }
            is BlockMutation.Rebind -> {
                if (mutation.index >= current.size) continue
                val at = childOffsetOf(current, mutation.index, onDetail)
                removeRun(list, at, transcriptBlockViewCount(current[mutation.index], onDetail))
                insertRun(list, at, mutation.block, onApproval, onToolTap, onDetail)
                current[mutation.index] = mutation.block
            }
            is BlockMutation.Insert -> {
                val at = childOffsetOf(current, minOf(mutation.index, current.size), onDetail)
                insertRun(list, at, mutation.block, onApproval, onToolTap, onDetail)
                current.add(minOf(mutation.index, current.size), mutation.block)
            }
        }
    }

    // M2.3's stick-to-bottom, and its whole point is the negative half: a reader who had scrolled
    // UP is never yanked down by a burst, and a page of history arriving at the FRONT never
    // scrolls at all -- which is why the predicate reads the insertion's own position rather than
    // the fact that something arrived.
    if (TranscriptIncremental.stickToBottom(atBottom, mutations)) {
        val last = list.getChildAt(list.childCount - 1) ?: return
        last.requestRectangleOnScreen(Rect(0, 0, last.width, last.height))
    }
}

/** Where [index]'s views start in a flat container holding [blocks]. */
private fun childOffsetOf(
    blocks: List<TranscriptBlock>,
    index: Int,
    onDetail: ((View, String) -> Unit)?,
): Int = blocks.take(index).sumOf { transcriptBlockViewCount(it, onDetail) }

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
) {
    transcriptBlockViews(list.context, block, onApproval, onToolTap, onDetail)
        .forEachIndexed { offset, view -> list.addView(view, at + offset) }
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

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
