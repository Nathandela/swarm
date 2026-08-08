package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.activityRow
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList

/**
 * PB-APP-3 -- inventory C2: the session detail, composed from the component kit.
 *
 * IT IS THE FIRST REAL DRILL-DOWN DESTINATION THIS PRODUCT HAS, and that closes a defect rather
 * than merely adding a screen. `navHeaderDrill` has shipped since S24 drawing a chevron with no
 * listener behind it, because the peek was composed UNDER the inbox rather than pushed over it and
 * there was nowhere for a back control to go (agents-tracker-2yb: "the chevron therefore looks like
 * a control and does not act"). [onBack] is that destination arriving.
 *
 * WHAT IT COMPOSES: the drill header (§4), the snapshot in the terminal mono well (C3's component,
 * reused here because a session's grid and the peek's grid are the same object), a `sectionLabel`
 * over the session's own journal, an `activityRow` per record -- or row 8's `emptyState` when there
 * are none -- PB-APP-9's routed line for what the machine last answered, and the two controls the
 * surface owns. It decides nothing about how any of them
 * looks: `android/gate/s24_screens_test.go` fences this package, so an `R.color`, an `R.dimen`, an
 * `R.style`, a `setTextAppearance`, a `setPadding` or a `background =` here fails the build.
 *
 * THE CONTROLS ARE SLOTS. Stop and Kill reach facade verbs, carry PB-SEC-12 clause 1's touch filter
 * and must survive a redraw, so `PhoneSurface` builds them out of the kit and hands them in -- the
 * arrangement `peekPanelView` already uses for `[Take control]` and `launchPanelView` for its
 * submit. A screen that constructed them would be a screen owning a listener and a native call.
 *
 * ## What C2 draws that this does not
 *
 * **The composer (derivation row 9).** Not built here: it is a kit component that does not exist
 * yet, and until it does the field and Send stay where they are, in `PhoneSurface`'s unrecomposed
 * column. Row 9's own backdrop blur will land in the same recorded-omission state the tab bar's did
 * -- `RenderEffect` blurs a view's OWN content, so applying it to the composer bar would blur the
 * field and leave the transcript behind it sharp, which is a visible defect rather than an
 * approximation. **PB-INPUT-6's IME-COMMIT path is also unbuilt** (agents-tracker-76j): an IME
 * commit therefore travels the ordinary keystroke path, coalesced at 125 ms, rather than arriving
 * as one event the way a clipboard paste does.
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
 * **The loose typed/send controls STAY WHERE THEY ARE** until the composer lands, and no
 * composer-shaped affordance is drawn here in the meantime: an empty bar at the bottom would
 * promise an input path that does not exist, which is the machines-screen lesson in the other
 * direction. If a slot is needed, it is left absent rather than blank.
 *
 * **The composer and the ledger ship TOGETHER or not at all** (agents-tracker-hxv). PB-INPUT-1's
 * undelivered ledger is what stops an input path losing keystrokes with nothing on screen saying
 * so, so a composer delivered without it reintroduces exactly the defect the ledger exists to
 * prevent. They are one issue for that reason and must not be split.
 */
object DetailTag {
    /** C2.1 -- §4's drill-down header: the chevron, and the session it names. */
    const val NAV = "detail.nav"

    /** PB-APP-8: what the screen says when the session's journal has a hole in it. */
    const val STALE = "detail.stale"

    /** PB-INPUT-1: what did not reach the machine. */
    const val NOT_SENT = "detail.notsent"

    /**
     * PB-APP-8 for the grid: what the screen says when the machine has stopped sending frames.
     *
     * IT IS BESIDE THE CARD AND NOT INSIDE IT (agents-tracker-0qe7). The well prints
     * `swarmmobile.Snapshot.Text` byte for byte, so a sentence written into the text would be
     * English in the machine's own register -- which is the defect that issue reports on the peek.
     */
    const val SNAPSHOT_STALE = "detail.snapshot.stale"

    /** C2.2 -- the daemon-rendered grid. Absent entirely when no frame has arrived. */
    const val SNAPSHOT = "detail.snapshot"

    /** C2.3 `.plabel` over the session's own journal. */
    const val SECTION_LABEL = "detail.section.label"

    /** C2.3 -- one record. */
    const val ROW = "detail.row"

    /** Row 8's block, under a heading whose session has no records yet. */
    const val EMPTY = "detail.empty"

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

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> =
        setOf(NAV, STALE, NOT_SENT, SNAPSHOT_STALE, SNAPSHOT, SECTION_LABEL, ROW, OUTCOME, STOP)
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
 * @param onBack where §4's chevron goes: back to the list this session was opened from.
 */
fun sessionDetailView(
    context: Context,
    panel: SessionDetailPanel,
    stop: View,
    kill: View,
    outcome: String,
    onBack: () -> Unit,
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
    if (panel.notSentNotice.isNotEmpty()) {
        column.addView(notice(context, panel.notSentNotice).apply { tag = DetailTag.NOT_SENT })
    }

    // ABSENT IS NOT EMPTY. A session the machine has sent no frame for gets no card at all rather
    // than a well containing nothing, which would present "we have not heard from this session" as
    // "this session's screen is blank".
    if (panel.hasSnapshot) {
        // AND THE MARK GOES WITH THE CARD, drawn only where there is a card to mark: a warning
        // that the grid is out of date, over a session that has sent no grid at all, describes
        // something that is not on screen.
        if (panel.snapshotStaleNotice.isNotEmpty()) {
            column.addView(
                notice(context, panel.snapshotStaleNotice).apply { tag = DetailTag.SNAPSHOT_STALE },
            )
        }
        column.addView(
            monoWell(context, panel.snapshot, terminal = true, lines = panel.snapshotRows)
                .apply { tag = DetailTag.SNAPSHOT },
        )
    }

    column.addView(
        sectionLabel(context, panel.transcript.heading).apply { tag = DetailTag.SECTION_LABEL },
    )
    if (panel.transcript.rows.isEmpty()) {
        column.addView(
            emptyState(context, panel.transcript.emptyCopy).apply { tag = DetailTag.EMPTY },
        )
    } else {
        column.addView(
            sessionList(context).apply {
                panel.transcript.rows.forEach { entry ->
                    addView(
                        activityRow(
                            context = context,
                            body = entry.body,
                            emphasis = entry.emphasis,
                        ).apply { tag = DetailTag.ROW },
                    )
                }
            },
        )
    }

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

    column.addView(stop.tagged(DetailTag.STOP))
    column.addView(kill.tagged(DetailTag.KILL))
    return column
}

/**
 * Tag a slot with the part it renders and detach it from whatever last held it.
 *
 * The detach is not tidiness: the panel is rebuilt whenever the transcript changes, and a slot
 * arriving at its next `addView` still claiming a discarded parent is refused by Android with "the
 * specified child already has a parent". `PeekPanelView` carries the same four lines for the same
 * reason.
 */
private fun View.tagged(tag: String): View = apply {
    this.tag = tag
    (parent as? ViewGroup)?.removeView(this)
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
