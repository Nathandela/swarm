package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.LinearLayout
import dev.swarm.phone.ui.SyncState
import dev.swarm.phone.ui.SyncStatus
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.SyncTone
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow
import dev.swarm.phone.ui.kit.syncPill
import dev.swarm.phone.ui.kit.syncStrip

/**
 * agents-tracker-nx44.2 -- where the composed sync status lands.
 *
 * The parts, and the three different places they belong:
 *
 *  - [syncPillView] is the NAV ROW's, beside the screen's own title. It is on screen for three of
 *    the four states and for none of the fourth.
 *  - [syncStatusView] is the SCAFFOLD's, above the destination and outside its scroll. It holds the
 *    strip -- which only a broken link draws -- and the detail the pill and the strip both open.
 *  - the detail's own repair leads somewhere, which is the SURFACE's: navigation is not something
 *    a composition can know ([phoneScaffoldView]'s `content` records the general form).
 */
object SyncTag {

    /** The nav-row mark. It is the kit's [dev.swarm.phone.ui.kit.KitTag.SYNC_PILL] under the name this package finds it by. */
    const val PILL = "sync.pill"

    /** The opaque one-line escalation, above the nav row. */
    const val STRIP = "sync.strip"

    /** The detail the mark opens: the three retired sentences, the gaps, and the repair. */
    const val SHEET = "sync.sheet"

    /** One labelled fact in the detail. */
    const val ROW = "sync.sheet.row"

    /** One repair channel's own sentence about its own hole. */
    const val GAP = "sync.sheet.gap"

    /**
     * The detail's one CONTROL, which is a different thing from a line.
     *
     * IT HAS ITS OWN TAG BECAUSE IT IS NOT A FACT -- the retired banner's action tag carried the
     * same argument, and it is the argument that survives: a test that found the repair under
     * [GAP] could assert the remedy was "on screen" while it was drawn as one more sentence.
     */
    const val REPAIR = "sync.sheet.repair"
}

/**
 * The nav row's mark, or null when the phone is live.
 *
 * **NULL IS THE WHOLE POINT AND NOT AN EDGE CASE.** `ConnectionBanner` has said since S16 that
 * "online is the only quiet state" and nothing consulted it, so every healthy phone carried a
 * permanent "Connected to your machine." where its warnings go. A mark that is always up is a mark
 * nobody reads. [SyncStatus.silent] is the model's own answer and this is the only place it is
 * spent for the pill.
 *
 * @param onOpen what the press does. The pill is a control -- it opens [syncStatusView]'s detail --
 *  and where that detail lives is the surface's, which is why the callback is a parameter.
 */
fun syncPillView(context: Context, status: SyncStatus, onOpen: () -> Unit): View? {
    if (status.silent) return null
    return syncPill(
        context = context,
        label = status.pill,
        tone = toneOf(status.state),
        description = status.description,
    ).apply {
        tag = SyncTag.PILL
        setOnClickListener { onOpen() }
        announceAsButton(this)
    }
}

/**
 * The scaffold's status chrome: the strip when the link is broken, and the detail when it is open.
 *
 * **IT IS EMPTY ON A HEALTHY PHONE AND STILL EXISTS**, which is the retired banner host's own
 * ruling: a container holding nothing costs nothing on screen -- no fill, no border, no padding of
 * its own -- and keeps the slot findable, so a test can tell "the phone has nothing to report" from
 * "the slot went away again".
 *
 * **THE STRIP IS IN LAYOUT, WHICH IS THE ENTIRE REASON IT IS SHAPED THIS WAY.** Field test 3 shows
 * the retired banner's sentences over the nav title. An overlay cannot be made not to overlap; it
 * can only be positioned so that it usually does not. A sibling above the nav row in the same
 * column cannot overlap anything by construction, and the cost -- the destination moving down by
 * the strip's own height -- is paid for one state out of four rather than for every state that had
 * anything to say.
 *
 * @param open whether the detail is showing. It is the SURFACE's fact and not this composition's:
 *  the chrome is rebuilt on every draw and a flag owned here would close itself under the user
 *  every time an agent produced an event.
 * @param onOpen what a press on the strip does. The strip opens the same detail the pill does --
 *  the sentence on it is truncated to one line, and the thing that finishes it is the detail.
 * @param onRepair what the detail's one control does. Where it leads is the surface's: the resync
 *  verb is a facade call and the pairing offer is a destination, and a composition knows neither.
 */
fun syncStatusView(
    context: Context,
    status: SyncStatus,
    open: Boolean = false,
    onOpen: () -> Unit = {},
    onRepair: (View) -> Unit = {},
): View = LinearLayout(context).apply {
    orientation = LinearLayout.VERTICAL
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    tag = ScaffoldTag.STATUS

    if (status.strip.isNotEmpty()) {
        addView(
            syncStrip(context, status.strip).apply {
                tag = SyncTag.STRIP
                setOnClickListener { onOpen() }
            },
        )
    }

    if (!open || status.silent) return@apply

    addView(
        sessionList(context).apply {
            tag = SyncTag.SHEET
            status.detail.rows.forEach { row ->
                addView(
                    settingsRow(context, label = row.label, sublabel = row.value)
                        .apply { tag = SyncTag.ROW },
                )
            }
            // THE GAPS ARE `notice` AND NOT MORE ROWS. A labelled row states a fact about the
            // phone; these are `StreamView`'s own sentences about ONE channel, and drawing them as
            // rows would make "the journal view has a gap" look like a fourth thing this sheet
            // measures rather than the detail behind the third.
            status.detail.gaps.forEach { gap ->
                addView(notice(context, gap).apply { tag = SyncTag.GAP })
            }
            // `CtaKind.MORE` IS THE NEUTRAL RULE AND IT IS THE RIGHT ONE FOR BOTH REMEDIES. The
            // press approves nothing and destroys nothing -- it asks for a rewind, or it opens the
            // screen the pairing offer names -- and `.a2-ok` on a fault report would read as the
            // app recommending the act.
            if (status.detail.repair.isNotEmpty()) {
                addView(
                    ctaButton(context, status.detail.repair, CtaKind.MORE).apply {
                        tag = SyncTag.REPAIR
                        setOnClickListener { control -> onRepair(control) }
                        announceAsButton(this)
                    },
                )
            }
        },
    )
}

/**
 * The tone the pill paints, from the state the model ranked.
 *
 * [SyncState.LIVE] CANNOT REACH HERE and the `error` says so rather than picking a colour: the pill
 * is not built at all in that state ([syncPillView] returns null), so a fourth tone would be an ink
 * for a component that is not on screen. An exhaustive `when` over a four-value enum is what makes
 * a fifth state fail the build instead of falling through to whichever colour happened to be last.
 */
private fun toneOf(state: SyncState): SyncTone = when (state) {
    SyncState.SYNCING -> SyncTone.SYNCING
    SyncState.QUIET -> SyncTone.QUIET
    SyncState.BROKEN -> SyncTone.BROKEN
    SyncState.LIVE -> error(
        "agents-tracker-nx44.2: a live phone has no pill to paint. syncPillView returns null " +
            "before this is reached, and a tone chosen here would be a colour for a mark that is " +
            "not drawn.",
    )
}

/**
 * A `TextView` ANNOUNCES ITSELF AS TEXT, which on both of these is the distinction being drawn: the
 * detail is made of sentences a reader reads, and a screen reader that heard one more of them would
 * meet the same defect the sighted user has just stopped meeting. `pairOnlyView` sets the role at
 * the click for the same reason -- the kit has no click to hang it on.
 */
private fun announceAsButton(view: View) {
    view.setAccessibilityDelegate(
        object : View.AccessibilityDelegate() {
            override fun onInitializeAccessibilityNodeInfo(host: View, info: AccessibilityNodeInfo) {
                super.onInitializeAccessibilityNodeInfo(host, info)
                info.className = Button::class.java.name
            }
        },
    )
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT

private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
