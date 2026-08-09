package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.ctaStack
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.noticeDetail

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

    /** Row 22's standalone tertiary button, supplied by the surface that owns the verb. */
    const val TAKE_CONTROL = "detail.control.take"

    /** PB-INPUT-3's take_control_end, the mirror of [TAKE_CONTROL] and never beside it. */
    const val RELEASE = "detail.control.release"

    /** Derivation row 9's bar: the field and the control that sends what is in it. */
    const val COMPOSER = "detail.composer"

    /** PB-INPUT-2's "visibly", in row 22's component. */
    const val LEASE = "detail.lease"

    /**
     * The machine's own words under [LEASE], in `.sheet2 .ctx` (agents-tracker-ksvb.10).
     *
     * IT IS ITS OWN PART AND NOT PART OF [LEASE], because it is drawn only when the machine sent
     * words -- a lease nobody has asked for has a sentence and no diagnostic -- and because a test
     * that could not tell them apart could not say which of the two carried the wire string.
     */
    const val LEASE_DETAIL = "detail.lease.detail"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition.
     *
     * [RELEASE] IS DELIBERATELY NOT IN IT, and the omission is what makes this an ordered claim at
     * all: release and [TAKE_CONTROL] occupy the SAME position in the control stack and can never
     * both be drawn -- `SessionLease` decides them as the two sides of one fact -- so a list
     * carrying both could not be compared against any single screen. Its placement is asserted
     * against its own state instead, in `SessionDetailViewTest`.
     */
    val COMPOSITION: Set<String> = setOf(
        NAV, STALE, RESYNC, UNDELIVERED, UNDELIVERED_DETAIL, ACKNOWLEDGE, NOT_SENT, TRANSCRIPT,
        OUTCOME, LEASE, LEASE_DETAIL, TAKE_CONTROL, STOP, COMPOSER,
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
 * @param takeControl PB-INPUT-2's step, drawn only while [SessionDetailPanel.offersTakeControl] says
 *  it is the step to take. IT IS THE PEEK'S BUTTON, ARRIVING WITH THAT SCREEN'S DELETION: the same
 *  `ctaButton(kind = MORE)` the surface already built there, on the screen a session is read on now.
 *  A slot rather than a construction for [stop]'s reason -- `PhoneSurface` owns the verb, the
 *  operation id the lease is claimed by, and PB-SEC-12 clause 1's touch filter.
 * @param release PB-INPUT-3's take_control_end, drawn only while [SessionDetailPanel.offersRelease]
 *  says a lease is held. A slot for [takeControl]'s reason, and never on screen beside it.
 * @param resync PB-SYNC-1's repair, drawn only beside the stale notice it mends. A slot because the
 *  verb is rate-bounded and its ErrClassRateLimited refusal routes through PB-APP-9's table, which
 *  is `PhoneSurface`'s line and not this composition's.
 * @param acknowledge PB-INPUT-1's clear, drawn only while there is a backlog to clear.
 * @param composer derivation row 9's bar. A slot rather than a construction for the reason every
 *  other control here is one, plus a second: the field holds what the user typed, so it is built
 *  once and re-parented, and a composition that built its own would empty it on every redraw.
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
    takeControl: View,
    release: View,
    stop: View,
    kill: View,
    resync: View,
    acknowledge: View,
    composer: View,
    outcome: String,
    onBack: () -> Unit,
    onApproval: ((String) -> Unit)? = null,
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
        column.addView(notice(context, panel.staleNotice).apply { tag = DetailTag.STALE })
    }
    // PB-SYNC-1's REPAIR, DIRECTLY UNDER THE SENTENCE IT ANSWERS (agents-tracker-upbo). The link
    // section on Machines draws all four channels' verdicts and can act on none of them; this is
    // the one place a hole is FELT -- a conversation with records missing from it -- so the control
    // goes where the person reading about the gap already is. It is a slot for [stop]'s reason:
    // `App.Resync` is rate-bounded and refuses with ErrClassRateLimited, and the surface is what
    // routes a refusal through PB-APP-9's table.
    if (panel.offersResync) column.addView(resync.tagged(DetailTag.RESYNC))
    // PB-INPUT-1'S LEDGER, ABOVE THE TRANSCRIPT AND NEVER OVER THE COMPOSER (agents-tracker-hxv's
    // own placement): it concerns input already gone, and a report of a loss must not cover the
    // control the user is reaching for to try again by hand.
    if (panel.undeliveredNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.undeliveredNotice).apply { tag = DetailTag.UNDELIVERED },
        )
    }
    if (panel.undeliveredDetail.isNotEmpty()) {
        column.addView(
            noticeDetail(context, panel.undeliveredDetail).apply {
                tag = DetailTag.UNDELIVERED_DETAIL
            },
        )
    }
    // THE ACKNOWLEDGEMENT IS A CONTROL AND NOT A DISMISS GESTURE, which is
    // `App.ClearUndeliveredInputs`' own split: a screen that OPENS must see the backlog, and a
    // user who dismisses it says so once, for every screen.
    if (panel.offersAcknowledge) column.addView(acknowledge.tagged(DetailTag.ACKNOWLEDGE))
    if (panel.notSentNotice.isNotEmpty()) {
        column.addView(notice(context, panel.notSentNotice).apply { tag = DetailTag.NOT_SENT })
    }

    // THE CONVERSATION, WHICH IS THIS SCREEN'S SUBJECT NOW. It is `transcriptView`'s composition and
    // not a second one written here: the heading, the rows, the wells and row 8's empty state are
    // all its, and a screen that rebuilt them would be the copy §2's reuse rule exists to prevent.
    column.addView(
        transcriptView(context, panel.transcript, onApproval).apply { tag = DetailTag.TRANSCRIPT },
    )

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
            notice(context, outcome, NoticeKind.ERROR).apply { tag = DetailTag.OUTCOME },
        )
    }

    // PB-INPUT-2's "visibly", above the controls it qualifies -- the same rule the notices above
    // follow, INCLUDING their gate (agents-tracker-ksvb.6, re-applied by agents-tracker-nx44.6).
    // A CONFIRMED LEASE PRINTS NOTHING: this was the app's one ungated notice, drawn over the
    // state where nothing needs saying, and with the composer now at the bottom of this column the
    // sentence it printed told the user they could type into the field already under their thumb.
    // The refusal and severance sentences this field also carries are transitions rather than a
    // healthy resting state, so they still draw.
    if (panel.leaseNotice.isNotEmpty()) {
        column.addView(notice(context, panel.leaseNotice).apply { tag = DetailTag.LEASE })
    }

    // THE MACHINE'S OWN WORDS, IN THE MACHINE'S OWN REGISTER (agents-tracker-ksvb.10). They used to
    // be spliced into the sentence above, so a daemon Go error was drawn in the same type and ink
    // as the copy this screen wrote. Drawn only when there are words: a lease nobody has asked for
    // has a sentence and nothing to diagnose, and an empty mono line under it would be a cell
    // reserved for a reply that does not exist.
    if (panel.leaseDetail.isNotEmpty()) {
        column.addView(
            noticeDetail(context, panel.leaseDetail).apply { tag = DetailTag.LEASE_DETAIL },
        )
    }

    // THE BUTTON SITS DIRECTLY UNDER THE SENTENCE, which is row 22's own arrangement: it is that
    // sentence's `[Take control]` promoted out of the prose, not a control that happens to be
    // nearby. It is added only while the model offers it -- once the machine has confirmed the
    // lease there is nothing left to take, and a screen that composed it anyway and then hid it
    // would be the second, contradictable statement PB-DS-9 fences against.
    //
    // agents-tracker-nx44.1: `ctaStack` AND NOT `column`, so the three controls carry `.acts2`'s
    // own `space_8` gap between them rather than the zero addView gave them.
    val controls = ctaStack(context)
    if (panel.offersTakeControl) controls.addView(takeControl.tagged(DetailTag.TAKE_CONTROL))
    // AND THE WAY BACK OUT (agents-tracker-nx44.6). The two are never on screen together --
    // `SessionLease` decides them as the two sides of one fact -- so this is the same site in the
    // stack rather than a second one, and a lease can now be given back from the screen that took
    // it instead of being held until the machine expires it.
    if (panel.offersRelease) controls.addView(release.tagged(DetailTag.RELEASE))
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
    column.addView(composer.tagged(DetailTag.COMPOSER))
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
): Boolean {
    if (drawn == null || next != drawn.copy(transcript = next.transcript)) return false
    val slot = host.findViewWithTag<View>(DetailTag.TRANSCRIPT) as? ViewGroup ?: return false
    slot.removeAllViews()
    val rebuilt = transcriptView(slot.context, next.transcript, onApproval)
    (rebuilt.parent as? ViewGroup)?.removeView(rebuilt)
    slot.addView(rebuilt)
    return true
}

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
